package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchtypedv1 "k8s.io/client-go/kubernetes/typed/batch/v1"
)

const (
	defaultNamespace = "nova-sre"
	defaultTTL       = int32(3600)
	defaultBackoff   = int32(0)
	defaultImage     = "alpine:3.20"
)

var dnsLabelPattern = regexp.MustCompile(`[^a-z0-9-]+`)

type Event struct {
	DeliveryID string
	Type       string
	Body       []byte
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
}

type JobCreator interface {
	Create(ctx context.Context, job *batchv1.Job) (*batchv1.Job, error)
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

type JobRunner struct {
	Config  JobConfig
	Creator JobCreator
	Logger  *log.Logger
}

func NewJobRunner(config JobConfig, creator JobCreator, logger *log.Logger) JobRunner {
	return JobRunner{
		Config:  config,
		Creator: creator,
		Logger:  logger,
	}
}

func (r JobRunner) EnqueueGitHubEvent(ctx context.Context, event Event) error {
	job, err := BuildGitHubEventJob(r.Config, event)
	if err != nil {
		return err
	}

	if r.Creator == nil {
		r.logf("prepared Kubernetes Job namespace=%s generate_name=%s delivery=%s event=%s",
			job.Namespace, job.GenerateName, event.DeliveryID, event.Type)
		return nil
	}

	created, err := r.Creator.Create(ctx, job)
	if err != nil {
		return fmt.Errorf("create Kubernetes Job: %w", err)
	}

	r.logf("created Kubernetes Job namespace=%s name=%s delivery=%s event=%s",
		created.Namespace, created.Name, event.DeliveryID, event.Type)
	return nil
}

func (r JobRunner) logf(format string, args ...any) {
	if r.Logger != nil {
		r.Logger.Printf(format, args...)
	}
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

	container := corev1.Container{
		Name:    "runner",
		Image:   config.Image,
		Command: append([]string(nil), config.Command...),
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

	return c
}

type payloadMetadata struct {
	Repo string
	SHA  string
}

func githubPayloadMetadata(body []byte) payloadMetadata {
	var payload struct {
		After      string `json:"after"`
		HeadCommit struct {
			ID string `json:"id"`
		} `json:"head_commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Head struct {
				SHA  string `json:"sha"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
		WorkflowRun struct {
			HeadSHA    string `json:"head_sha"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"workflow_run"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return payloadMetadata{}
	}

	return payloadMetadata{
		Repo: firstNonEmpty(payload.Repository.FullName, payload.PullRequest.Head.Repo.FullName, payload.WorkflowRun.Repository.FullName),
		SHA:  firstNonEmpty(payload.HeadCommit.ID, payload.After, payload.PullRequest.Head.SHA, payload.WorkflowRun.HeadSHA),
	}
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
