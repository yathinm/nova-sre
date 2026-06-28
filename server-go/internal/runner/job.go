package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	batchtypedv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
	coretypedv1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	defaultNamespace = "nova-sre"
	defaultTTL       = int32(3600)
	defaultBackoff   = int32(0)
	defaultImage     = "alpine:3.20"
	defaultPoll      = 2 * time.Second
	defaultCallback  = 5 * time.Minute
	defaultAgentWait = 10 * time.Second

	defaultRunnerCPURequest    = "100m"
	defaultRunnerMemoryRequest = "128Mi"
	defaultRunnerCPULimit      = "500m"
	defaultRunnerMemoryLimit   = "256Mi"
)

var dnsLabelPattern = regexp.MustCompile(`[^a-z0-9-]+`)

type Event struct {
	DeliveryID string
	Type       string
	Body       []byte
	ReceivedAt time.Time
}

type JobConfig struct {
	Namespace          string
	Image              string
	Repo               string
	SHA                string
	Command            []string
	TTLSecondsFinished *int32
	BackoffLimit       *int32
	ServiceAccountName string
	Resources          corev1.ResourceRequirements
}

type JobCreator interface {
	Create(ctx context.Context, job *batchv1.Job) (*batchv1.Job, error)
}

type JobWatcher interface {
	WaitForCompletion(ctx context.Context, namespace string, name string) (JobResult, error)
}

type JobLogCollector interface {
	CollectJobLogs(ctx context.Context, namespace string, jobName string) ([]LogEntry, error)
}

type AgentClient interface {
	Diagnose(ctx context.Context, request DiagnoseRequest) error
}

type JobObserver interface {
	ObserveJob(update JobStatusUpdate)
}

type JobStatusUpdate struct {
	DeliveryID string
	Event      string
	Repository string
	SHA        string
	Status     string
	Namespace  string
	JobName    string
	Reason     string
	Message    string
	ObservedAt time.Time
}

type JobResult struct {
	Failed    bool
	Succeeded bool
	Reason    string
	Message   string
}

type LogEntry struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Logs      string `json:"logs,omitempty"`
	Error     string `json:"error,omitempty"`
}

type PullRequestMetadata struct {
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	Head   string `json:"head,omitempty"`
	Base   string `json:"base,omitempty"`
}

type DiagnoseRequest struct {
	DeliveryID   string              `json:"delivery_id"`
	Event        string              `json:"event"`
	Repository   string              `json:"repository,omitempty"`
	SHA          string              `json:"sha,omitempty"`
	JobName      string              `json:"job_name"`
	Namespace    string              `json:"namespace"`
	Reason       string              `json:"reason,omitempty"`
	Message      string              `json:"message,omitempty"`
	PullRequest  PullRequestMetadata `json:"pull_request,omitempty"`
	Logs         []LogEntry          `json:"logs"`
	WebhookBody  json.RawMessage     `json:"webhook_body,omitempty"`
	ObservedTime time.Time           `json:"observed_time"`
}

type KubernetesJobCreator struct {
	Jobs batchtypedv1.JobInterface
}

func (c KubernetesJobCreator) Create(ctx context.Context, job *batchv1.Job) (*batchv1.Job, error) {
	if c.Jobs == nil {
		return nil, errors.New("kubernetes job interface is nil")
	}

	return c.Jobs.Create(ctx, job, metav1.CreateOptions{})
}

type KubernetesJobWatcher struct {
	Jobs         batchtypedv1.JobInterface
	PollInterval time.Duration
}

func (w KubernetesJobWatcher) WaitForCompletion(ctx context.Context, namespace string, name string) (JobResult, error) {
	if w.Jobs == nil {
		return JobResult{}, errors.New("kubernetes job interface is nil")
	}
	if strings.TrimSpace(name) == "" {
		return JobResult{}, errors.New("job name is required")
	}

	interval := w.PollInterval
	if interval <= 0 {
		interval = defaultPoll
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		job, err := w.Jobs.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return JobResult{}, fmt.Errorf("get Kubernetes Job: %w", err)
		}
		if result, done := jobResult(job); done {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return JobResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type KubernetesJobLogCollector struct {
	Pods coretypedv1.PodInterface
}

func (c KubernetesJobLogCollector) CollectJobLogs(ctx context.Context, namespace string, jobName string) ([]LogEntry, error) {
	if c.Pods == nil {
		return nil, errors.New("kubernetes pod interface is nil")
	}
	if strings.TrimSpace(jobName) == "" {
		return nil, errors.New("job name is required")
	}

	selector := labels.Set{"job-name": jobName}.String()
	pods, err := c.Pods.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list Kubernetes Pods for Job: %w", err)
	}

	var entries []LogEntry
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.InitContainers {
			entries = append(entries, c.collectContainerLog(ctx, pod.Name, container.Name))
		}
		for _, container := range pod.Spec.Containers {
			entries = append(entries, c.collectContainerLog(ctx, pod.Name, container.Name))
		}
	}
	return entries, nil
}

func (c KubernetesJobLogCollector) collectContainerLog(ctx context.Context, podName string, containerName string) LogEntry {
	entry := LogEntry{Pod: podName, Container: containerName}
	body, err := c.Pods.GetLogs(podName, &corev1.PodLogOptions{Container: containerName}).DoRaw(ctx)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.Logs = string(body)
	return entry
}

type HTTPAgentClient struct {
	URL        string
	HTTPClient *http.Client
	Timeout    time.Duration
	Token      string
}

func NewHTTPAgentClient(agentURL string, timeout time.Duration, tokens ...string) (*HTTPAgentClient, error) {
	agentURL = strings.TrimSpace(agentURL)
	if agentURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(agentURL)
	if err != nil {
		return nil, fmt.Errorf("parse NOVA_SRE_AGENT_URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("NOVA_SRE_AGENT_URL must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/diagnose"
	if timeout <= 0 {
		timeout = defaultAgentWait
	}
	return &HTTPAgentClient{
		URL:        parsed.String(),
		HTTPClient: &http.Client{Timeout: timeout},
		Timeout:    timeout,
		Token:      firstString(tokens...),
	}, nil
}

func (c *HTTPAgentClient) Diagnose(ctx context.Context, request DiagnoseRequest) error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("agent URL is required")
	}

	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal diagnose request: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultAgentWait
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create diagnose request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(c.Token); token != "" {
		req.Header.Set("X-Nova-SRE-Agent-Token", token)
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send diagnose request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("diagnose request returned status %d", resp.StatusCode)
	}
	return nil
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type JobRunner struct {
	Config          JobConfig
	Creator         JobCreator
	Watcher         JobWatcher
	LogCollector    JobLogCollector
	Agent           AgentClient
	Metrics         PipelineMetrics
	Observer        JobObserver
	Logger          *log.Logger
	CallbackTimeout time.Duration
	Now             func() time.Time
}

func NewJobRunner(config JobConfig, creator JobCreator, logger *log.Logger) JobRunner {
	return JobRunner{
		Config:  config,
		Creator: creator,
		Metrics: DefaultPipelineMetrics(),
		Logger:  logger,
		Now:     time.Now,
	}
}

func (r JobRunner) EnqueueGitHubEvent(ctx context.Context, event Event) error {
	startedAt := r.now()
	job, err := BuildGitHubEventJob(r.Config, event)
	if err != nil {
		return err
	}
	metadata := githubPayloadMetadata(event.Body)
	repo := firstNonEmpty(job.Annotations["nova-sre.io/repository"], metadata.Repo)
	r.metrics().JobStarted(repo)

	if r.Creator == nil {
		r.logf("prepared Kubernetes Job namespace=%s generate_name=%s delivery=%s event=%s",
			job.Namespace, job.GenerateName, event.DeliveryID, event.Type)
		r.observeJob(event, metadata, JobStatusUpdate{
			Status:    "prepared",
			Namespace: job.Namespace,
		})
		r.metrics().JobFinished(PipelineJobMetrics{
			Status:            "prepared",
			Repo:              repo,
			SchedulingLatency: durationSince(startedAt, r.now()),
			EventTime:         metadata.EventTime,
			FinishedAt:        r.now(),
		})
		return nil
	}

	created, err := r.Creator.Create(ctx, job)
	if err != nil {
		r.observeJob(event, metadata, JobStatusUpdate{
			Status:  "error",
			Reason:  "CreateFailed",
			Message: err.Error(),
		})
		r.metrics().JobFinished(PipelineJobMetrics{
			Status:            "failed",
			Repo:              repo,
			SchedulingLatency: durationSince(startedAt, r.now()),
			EventTime:         metadata.EventTime,
			FinishedAt:        r.now(),
		})
		return fmt.Errorf("create Kubernetes Job: %w", err)
	}

	r.logf("created Kubernetes Job namespace=%s name=%s delivery=%s event=%s",
		created.Namespace, created.Name, event.DeliveryID, event.Type)
	r.observeJob(event, metadata, JobStatusUpdate{
		Status:    "created",
		Namespace: created.Namespace,
		JobName:   created.Name,
	})
	r.metrics().JobFinished(PipelineJobMetrics{
		Status:            "created",
		Repo:              repo,
		SchedulingLatency: durationSince(startedAt, r.now()),
		EventTime:         metadata.EventTime,
		FinishedAt:        r.now(),
		Active:            true,
	})
	r.startFailureCallback(ctx, created, event)
	return nil
}

func (r JobRunner) startFailureCallback(ctx context.Context, job *batchv1.Job, event Event) {
	if r.Watcher == nil || r.LogCollector == nil || r.Agent == nil || job == nil {
		return
	}

	go r.runFailureCallback(context.WithoutCancel(ctx), job.DeepCopy(), event)
}

func (r JobRunner) runFailureCallback(ctx context.Context, job *batchv1.Job, event Event) {
	timeout := r.CallbackTimeout
	if timeout <= 0 {
		timeout = defaultCallback
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := r.Watcher.WaitForCompletion(ctx, job.Namespace, job.Name)
	if err != nil {
		metadata := githubPayloadMetadata(event.Body)
		r.observeJob(event, metadata, JobStatusUpdate{
			Status:    "error",
			Namespace: job.Namespace,
			JobName:   job.Name,
			Reason:    "WatchFailed",
			Message:   err.Error(),
		})
		r.logf("failed to observe Kubernetes Job namespace=%s name=%s delivery=%s event=%s: %v",
			job.Namespace, job.Name, event.DeliveryID, event.Type, err)
		return
	}

	metadata := githubPayloadMetadata(event.Body)
	repo := firstNonEmpty(job.Annotations["nova-sre.io/repository"], metadata.Repo)
	status := "succeeded"
	if result.Failed {
		status = "failed"
	}
	r.observeJob(event, metadata, JobStatusUpdate{
		Status:    status,
		Namespace: job.Namespace,
		JobName:   job.Name,
		Reason:    result.Reason,
		Message:   result.Message,
	})
	r.metrics().JobFinished(PipelineJobMetrics{
		Status:     status,
		Repo:       repo,
		EventTime:  metadata.EventTime,
		FinishedAt: r.now(),
	})

	if !result.Failed {
		r.logf("Kubernetes Job completed without diagnosis namespace=%s name=%s delivery=%s event=%s",
			job.Namespace, job.Name, event.DeliveryID, event.Type)
		return
	}

	logs, err := r.LogCollector.CollectJobLogs(ctx, job.Namespace, job.Name)
	if err != nil {
		r.logf("failed to collect Kubernetes logs namespace=%s name=%s delivery=%s event=%s: %v",
			job.Namespace, job.Name, event.DeliveryID, event.Type, err)
		logs = nil
	}

	request := DiagnoseRequest{
		DeliveryID:   event.DeliveryID,
		Event:        event.Type,
		Repository:   metadata.Repo,
		SHA:          metadata.SHA,
		JobName:      job.Name,
		Namespace:    job.Namespace,
		Reason:       result.Reason,
		Message:      result.Message,
		PullRequest:  metadata.PullRequest,
		Logs:         logs,
		WebhookBody:  append(json.RawMessage(nil), event.Body...),
		ObservedTime: r.now(),
	}
	if err := r.Agent.Diagnose(ctx, request); err != nil {
		r.logf("failed to send diagnosis request namespace=%s name=%s delivery=%s event=%s: %v",
			job.Namespace, job.Name, event.DeliveryID, event.Type, err)
		return
	}

	r.logf("sent diagnosis request namespace=%s name=%s delivery=%s event=%s logs=%d",
		job.Namespace, job.Name, event.DeliveryID, event.Type, len(logs))
}

func (r JobRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

func (r JobRunner) logf(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
}

func (r JobRunner) metrics() PipelineMetrics {
	if r.Metrics != nil {
		return r.Metrics
	}
	return noopPipelineMetrics{}
}

func (r JobRunner) observeJob(event Event, metadata payloadMetadata, update JobStatusUpdate) {
	if r.Observer == nil {
		return
	}
	update.DeliveryID = firstNonEmpty(update.DeliveryID, event.DeliveryID)
	update.Event = firstNonEmpty(update.Event, event.Type)
	update.Repository = firstNonEmpty(update.Repository, metadata.Repo)
	update.SHA = firstNonEmpty(update.SHA, metadata.SHA)
	if update.ObservedAt.IsZero() {
		update.ObservedAt = r.now()
	}
	r.Observer.ObserveJob(update)
}

func JobConfigFromEnv(getenv func(string) string) JobConfig {
	ttl := defaultTTL
	backoff := defaultBackoff

	config := JobConfig{
		Namespace:          firstNonEmpty(getenv("RUNNER_JOB_NAMESPACE"), defaultNamespace),
		Image:              firstNonEmpty(getenv("RUNNER_JOB_IMAGE"), defaultImage),
		Repo:               strings.TrimSpace(getenv("RUNNER_REPO")),
		SHA:                strings.TrimSpace(getenv("RUNNER_SHA")),
		Command:            strings.Fields(getenv("RUNNER_JOB_COMMAND")),
		TTLSecondsFinished: &ttl,
		BackoffLimit:       &backoff,
		ServiceAccountName: strings.TrimSpace(getenv("RUNNER_JOB_SERVICE_ACCOUNT")),
		Resources:          runnerResourceRequirements(getenv),
	}

	if raw := strings.TrimSpace(getenv("RUNNER_JOB_TTL_SECONDS")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 32); err == nil {
			value := int32(parsed)
			config.TTLSecondsFinished = &value
		}
	}
	if raw := strings.TrimSpace(getenv("RUNNER_JOB_BACKOFF_LIMIT")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 32); err == nil {
			value := int32(parsed)
			config.BackoffLimit = &value
		}
	}
	if len(config.Command) == 0 {
		config.Command = []string{
			"/bin/sh",
			"-c",
			"echo Nova-SRE runner received ${GITHUB_EVENT_NAME} for ${GITHUB_REPOSITORY}@${GITHUB_SHA}",
		}
	}

	return config
}

func BuildGitHubEventJob(config JobConfig, event Event) (*batchv1.Job, error) {
	metadata := githubPayloadMetadata(event.Body)
	config = config.withDefaults(metadata)

	if strings.TrimSpace(event.DeliveryID) == "" {
		return nil, errors.New("delivery id is required")
	}
	if strings.TrimSpace(event.Type) == "" {
		return nil, errors.New("event type is required")
	}
	if strings.TrimSpace(config.Namespace) == "" {
		return nil, errors.New("job namespace is required")
	}
	if strings.TrimSpace(config.Image) == "" {
		return nil, errors.New("runner image is required")
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "nova-sre-runner",
		"app.kubernetes.io/component":  "github-event-runner",
		"nova-sre.io/event":            labelValue(event.Type),
		"nova-sre.io/delivery-hash":    shortHash(event.DeliveryID),
		"nova-sre.io/repository-hash":  shortHash(config.Repo),
		"nova-sre.io/commit-sha-short": labelValue(firstN(config.SHA, 12)),
	}
	annotations := map[string]string{
		"nova-sre.io/delivery-id": event.DeliveryID,
		"nova-sre.io/event":       event.Type,
		"nova-sre.io/repository":  config.Repo,
		"nova-sre.io/commit-sha":  config.SHA,
	}
	if !event.ReceivedAt.IsZero() {
		annotations["nova-sre.io/received-at"] = event.ReceivedAt.UTC().Format(time.RFC3339Nano)
	}

	container := corev1.Container{
		Name:      "runner",
		Image:     config.Image,
		Command:   append([]string(nil), config.Command...),
		Resources: *config.Resources.DeepCopy(),
		Env: []corev1.EnvVar{
			{Name: "GITHUB_EVENT_NAME", Value: event.Type},
			{Name: "GITHUB_DELIVERY_ID", Value: event.DeliveryID},
			{Name: "GITHUB_REPOSITORY", Value: config.Repo},
			{Name: "GITHUB_SHA", Value: config.SHA},
			{Name: "GITHUB_EVENT_PAYLOAD", Value: string(event.Body)},
		},
	}

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    config.Namespace,
			GenerateName: fmt.Sprintf("nova-sre-%s-", labelValue(event.Type)),
			Labels:       labels,
			Annotations:  annotations,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            config.BackoffLimit,
			TTLSecondsAfterFinished: config.TTLSecondsFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      cloneStringMap(labels),
					Annotations: cloneStringMap(annotations),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: config.ServiceAccountName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers:         []corev1.Container{container},
				},
			},
		},
	}, nil
}

func (c JobConfig) withDefaults(metadata payloadMetadata) JobConfig {
	c.Namespace = firstNonEmpty(c.Namespace, defaultNamespace)
	c.Repo = firstNonEmpty(c.Repo, metadata.Repo)
	c.SHA = firstNonEmpty(c.SHA, metadata.SHA)

	if c.TTLSecondsFinished == nil {
		ttl := defaultTTL
		c.TTLSecondsFinished = &ttl
	}
	if c.BackoffLimit == nil {
		backoff := defaultBackoff
		c.BackoffLimit = &backoff
	}
	if len(c.Resources.Requests) == 0 && len(c.Resources.Limits) == 0 {
		c.Resources = runnerResourceRequirements(nil)
	}

	return c
}

func runnerResourceRequirements(getenv func(string) string) corev1.ResourceRequirements {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resourceQuantity(getenv("RUNNER_JOB_CPU_REQUEST"), defaultRunnerCPURequest),
			corev1.ResourceMemory: resourceQuantity(getenv("RUNNER_JOB_MEMORY_REQUEST"), defaultRunnerMemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resourceQuantity(getenv("RUNNER_JOB_CPU_LIMIT"), defaultRunnerCPULimit),
			corev1.ResourceMemory: resourceQuantity(getenv("RUNNER_JOB_MEMORY_LIMIT"), defaultRunnerMemoryLimit),
		},
	}
}

func resourceQuantity(raw string, fallback string) resource.Quantity {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = fallback
	}
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return resource.MustParse(fallback)
	}
	return quantity
}

type payloadMetadata struct {
	Repo        string
	SHA         string
	PullRequest PullRequestMetadata
	EventTime   time.Time
}

func githubPayloadMetadata(body []byte) payloadMetadata {
	var payload struct {
		After      string `json:"after"`
		HeadCommit struct {
			ID        string `json:"id"`
			Timestamp string `json:"timestamp"`
		} `json:"head_commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Number    int    `json:"number"`
			HTMLURL   string `json:"html_url"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			Head      struct {
				SHA  string `json:"sha"`
				Ref  string `json:"ref"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		WorkflowRun struct {
			HeadSHA      string `json:"head_sha"`
			CreatedAt    string `json:"created_at"`
			UpdatedAt    string `json:"updated_at"`
			RunStartedAt string `json:"run_started_at"`
			Repository   struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"workflow_run"`
		CheckRun struct {
			StartedAt   string `json:"started_at"`
			CompletedAt string `json:"completed_at"`
		} `json:"check_run"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payloadMetadata{}
	}

	return payloadMetadata{
		Repo: firstNonEmpty(payload.Repository.FullName, payload.PullRequest.Head.Repo.FullName, payload.WorkflowRun.Repository.FullName),
		SHA:  firstNonEmpty(payload.HeadCommit.ID, payload.After, payload.PullRequest.Head.SHA, payload.WorkflowRun.HeadSHA),
		PullRequest: PullRequestMetadata{
			Number: payload.PullRequest.Number,
			URL:    payload.PullRequest.HTMLURL,
			Head:   payload.PullRequest.Head.Ref,
			Base:   payload.PullRequest.Base.Ref,
		},
		EventTime: firstTime(
			payload.WorkflowRun.RunStartedAt,
			payload.WorkflowRun.CreatedAt,
			payload.PullRequest.CreatedAt,
			payload.CheckRun.StartedAt,
			payload.HeadCommit.Timestamp,
			payload.CreatedAt,
			payload.UpdatedAt,
			payload.WorkflowRun.UpdatedAt,
			payload.PullRequest.UpdatedAt,
			payload.CheckRun.CompletedAt,
		),
	}
}

func jobResult(job *batchv1.Job) (JobResult, bool) {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return JobResult{Succeeded: true, Reason: condition.Reason, Message: condition.Message}, true
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return JobResult{Failed: true, Reason: condition.Reason, Message: condition.Message}, true
		}
	}
	return JobResult{}, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func labelValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = dnsLabelPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "unknown"
	}
	if len(value) <= 63 {
		return value
	}
	return strings.Trim(value[:50], "-") + "-" + shortHash(value)
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func firstN(value string, count int) string {
	if len(value) <= count {
		return value
	}
	return value[:count]
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
