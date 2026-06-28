package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildGitHubEventJobUsesConfigAndWebhookMetadata(t *testing.T) {
	ttl := int32(120)
	backoff := int32(1)
	receivedAt := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	job, err := BuildGitHubEventJob(JobConfig{
		Namespace:          "runner-jobs",
		Image:              "ghcr.io/example/nova-runner:test",
		Command:            []string{"/bin/runner", "--once"},
		TTLSecondsFinished: &ttl,
		BackoffLimit:       &backoff,
		ServiceAccountName: "nova-runner",
	}, Event{
		DeliveryID: "delivery-123",
		Type:       "push",
		Body:       []byte(`{"repository":{"full_name":"acme/widgets"},"ref":"refs/heads/main","before":"0000000000000000","after":"abcdef1234567890"}`),
		ReceivedAt: receivedAt,
	})
	if err != nil {
		t.Fatalf("BuildGitHubEventJob returned error: %v", err)
	}

	if job.APIVersion != "batch/v1" || job.Kind != "Job" {
		t.Fatalf("unexpected type meta: %s %s", job.APIVersion, job.Kind)
	}
	if job.Namespace != "runner-jobs" {
		t.Fatalf("expected namespace runner-jobs, got %q", job.Namespace)
	}
	if job.GenerateName != "nova-sre-push-" {
		t.Fatalf("expected generateName nova-sre-push-, got %q", job.GenerateName)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != ttl {
		t.Fatalf("expected ttl %d, got %#v", ttl, job.Spec.TTLSecondsAfterFinished)
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != backoff {
		t.Fatalf("expected backoff %d, got %#v", backoff, job.Spec.BackoffLimit)
	}

	podSpec := job.Spec.Template.Spec
	if podSpec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("expected RestartPolicy Never, got %q", podSpec.RestartPolicy)
	}
	if podSpec.ServiceAccountName != "nova-runner" {
		t.Fatalf("expected service account nova-runner, got %q", podSpec.ServiceAccountName)
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(podSpec.Containers))
	}

	container := podSpec.Containers[0]
	if container.Image != "ghcr.io/example/nova-runner:test" {
		t.Fatalf("expected runner image, got %q", container.Image)
	}
	if got := container.Command; len(got) != 2 || got[0] != "/bin/runner" || got[1] != "--once" {
		t.Fatalf("unexpected command: %#v", got)
	}
	if got := container.Resources.Requests.Cpu().String(); got != defaultRunnerCPURequest {
		t.Fatalf("expected default cpu request %q, got %q", defaultRunnerCPURequest, got)
	}
	if got := container.Resources.Requests.Memory().String(); got != defaultRunnerMemoryRequest {
		t.Fatalf("expected default memory request %q, got %q", defaultRunnerMemoryRequest, got)
	}
	if got := container.Resources.Limits.Cpu().String(); got != defaultRunnerCPULimit {
		t.Fatalf("expected default cpu limit %q, got %q", defaultRunnerCPULimit, got)
	}
	if got := container.Resources.Limits.Memory().String(); got != defaultRunnerMemoryLimit {
		t.Fatalf("expected default memory limit %q, got %q", defaultRunnerMemoryLimit, got)
	}

	assertEnv(t, container.Env, "GITHUB_EVENT_NAME", "push")
	assertEnv(t, container.Env, "GITHUB_DELIVERY_ID", "delivery-123")
	assertEnv(t, container.Env, "GITHUB_REPOSITORY", "acme/widgets")
	assertEnv(t, container.Env, "GITHUB_SHA", "abcdef1234567890")
	assertEnv(t, container.Env, "GITHUB_REF", "refs/heads/main")
	assertEnv(t, container.Env, "GITHUB_BEFORE", "0000000000000000")
	assertEnv(t, container.Env, "GITHUB_EVENT_PAYLOAD", `{"repository":{"full_name":"acme/widgets"},"ref":"refs/heads/main","before":"0000000000000000","after":"abcdef1234567890"}`)

	if job.Labels["nova-sre.io/event"] != "push" {
		t.Fatalf("expected event label push, got %q", job.Labels["nova-sre.io/event"])
	}
	if job.Annotations["nova-sre.io/delivery-id"] != "delivery-123" {
		t.Fatalf("expected delivery annotation, got %q", job.Annotations["nova-sre.io/delivery-id"])
	}
	if job.Annotations["nova-sre.io/repository"] != "acme/widgets" {
		t.Fatalf("expected repository annotation, got %q", job.Annotations["nova-sre.io/repository"])
	}
	if job.Annotations["nova-sre.io/received-at"] != receivedAt.Format(time.RFC3339Nano) {
		t.Fatalf("expected received-at annotation, got %q", job.Annotations["nova-sre.io/received-at"])
	}
}

func TestBuildGitHubEventJobExtractsPullRequestMetadata(t *testing.T) {
	job, err := BuildGitHubEventJob(JobConfig{
		Image: "ghcr.io/example/nova-runner:test",
	}, Event{
		DeliveryID: "delivery-123",
		Type:       "pull_request",
		Body: []byte(`{
			"action": "opened",
			"pull_request": {
				"number": 42,
				"html_url": "https://github.com/acme/pr-source/pull/42",
				"head": {
					"sha": "0123456789abcdef",
					"ref": "feature/context",
					"repo": {"full_name": "acme/pr-source"}
				},
				"base": {
					"ref": "main"
				}
			}
		}`),
	})
	if err != nil {
		t.Fatalf("BuildGitHubEventJob returned error: %v", err)
	}

	assertContainerEnv(t, job, "GITHUB_REPOSITORY", "acme/pr-source")
	assertContainerEnv(t, job, "GITHUB_SHA", "0123456789abcdef")
	assertContainerEnv(t, job, "GITHUB_ACTION", "opened")
	assertContainerEnv(t, job, "GITHUB_PR_NUMBER", "42")
	assertContainerEnv(t, job, "GITHUB_PR_URL", "https://github.com/acme/pr-source/pull/42")
	assertContainerEnv(t, job, "GITHUB_HEAD_REF", "feature/context")
	assertContainerEnv(t, job, "GITHUB_BASE_REF", "main")
	if job.Namespace != defaultNamespace {
		t.Fatalf("expected default namespace %q, got %q", defaultNamespace, job.Namespace)
	}
}

func TestBuildGitHubEventJobRequiresRunnerImage(t *testing.T) {
	_, err := BuildGitHubEventJob(JobConfig{}, Event{
		DeliveryID: "delivery-123",
		Type:       "push",
		Body:       []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected missing runner image to fail")
	}
}

func TestJobConfigFromEnvParsesRunnerSettings(t *testing.T) {
	values := map[string]string{
		"RUNNER_JOB_NAMESPACE":       "runner-jobs",
		"RUNNER_JOB_IMAGE":           "ghcr.io/example/nova-runner:test",
		"RUNNER_REPO":                "acme/widgets",
		"RUNNER_SHA":                 "abcdef",
		"RUNNER_JOB_COMMAND":         "/bin/runner --once",
		"RUNNER_JOB_TTL_SECONDS":     "900",
		"RUNNER_JOB_BACKOFF_LIMIT":   "2",
		"RUNNER_JOB_SERVICE_ACCOUNT": "nova-runner",
		"RUNNER_JOB_CPU_REQUEST":     "150m",
		"RUNNER_JOB_MEMORY_REQUEST":  "160Mi",
		"RUNNER_JOB_CPU_LIMIT":       "750m",
		"RUNNER_JOB_MEMORY_LIMIT":    "384Mi",
	}

	config := JobConfigFromEnv(func(name string) string {
		return values[name]
	})

	if config.Namespace != "runner-jobs" || config.Image != "ghcr.io/example/nova-runner:test" {
		t.Fatalf("unexpected namespace/image: %#v", config)
	}
	if config.Repo != "acme/widgets" || config.SHA != "abcdef" {
		t.Fatalf("unexpected repo metadata: %#v", config)
	}
	if got := config.Command; len(got) != 2 || got[0] != "/bin/runner" || got[1] != "--once" {
		t.Fatalf("unexpected command: %#v", got)
	}
	if config.TTLSecondsFinished == nil || *config.TTLSecondsFinished != 900 {
		t.Fatalf("expected ttl 900, got %#v", config.TTLSecondsFinished)
	}
	if config.BackoffLimit == nil || *config.BackoffLimit != 2 {
		t.Fatalf("expected backoff 2, got %#v", config.BackoffLimit)
	}
	if config.ServiceAccountName != "nova-runner" {
		t.Fatalf("expected service account nova-runner, got %q", config.ServiceAccountName)
	}
	if got := config.Resources.Requests.Cpu().String(); got != "150m" {
		t.Fatalf("expected cpu request 150m, got %q", got)
	}
	if got := config.Resources.Requests.Memory().String(); got != "160Mi" {
		t.Fatalf("expected memory request 160Mi, got %q", got)
	}
	if got := config.Resources.Limits.Cpu().String(); got != "750m" {
		t.Fatalf("expected cpu limit 750m, got %q", got)
	}
	if got := config.Resources.Limits.Memory().String(); got != "384Mi" {
		t.Fatalf("expected memory limit 384Mi, got %q", got)
	}
}

func TestJobConfigFromEnvFallsBackForInvalidRunnerResources(t *testing.T) {
	config := JobConfigFromEnv(func(name string) string {
		if name == "RUNNER_JOB_CPU_REQUEST" {
			return "not-a-quantity"
		}
		return ""
	})

	if got := config.Resources.Requests.Cpu().String(); got != defaultRunnerCPURequest {
		t.Fatalf("expected default cpu request %q, got %q", defaultRunnerCPURequest, got)
	}
}

func TestJobRunnerCanUseStubCreatorWithoutCluster(t *testing.T) {
	creator := &recordingCreator{}
	runner := NewJobRunner(JobConfig{
		Image: "ghcr.io/example/nova-runner:test",
	}, creator, log.New(io.Discard, "", 0))

	if err := runner.EnqueueGitHubEvent(context.Background(), Event{
		DeliveryID: "delivery-123",
		Type:       "workflow_run",
		Body: []byte(`{
			"action": "completed",
			"workflow_run": {
				"id": 123456,
				"name": "CI",
				"html_url": "https://github.com/acme/widgets/actions/runs/123456",
				"status": "completed",
				"conclusion": "failure",
				"run_attempt": 2,
				"head_sha": "abc",
				"repository": {"full_name": "acme/widgets"}
			}
		}`),
	}); err != nil {
		t.Fatalf("EnqueueGitHubEvent returned error: %v", err)
	}

	if creator.created == nil {
		t.Fatal("expected stub creator to receive a job")
	}
	assertContainerEnv(t, creator.created, "GITHUB_REPOSITORY", "acme/widgets")
	assertContainerEnv(t, creator.created, "GITHUB_SHA", "abc")
	assertContainerEnv(t, creator.created, "GITHUB_ACTION", "completed")
	assertContainerEnv(t, creator.created, "GITHUB_WORKFLOW_RUN_ID", "123456")
	assertContainerEnv(t, creator.created, "GITHUB_WORKFLOW_NAME", "CI")
	assertContainerEnv(t, creator.created, "GITHUB_WORKFLOW_RUN_URL", "https://github.com/acme/widgets/actions/runs/123456")
	assertContainerEnv(t, creator.created, "GITHUB_WORKFLOW_STATUS", "completed")
	assertContainerEnv(t, creator.created, "GITHUB_WORKFLOW_CONCLUSION", "failure")
	assertContainerEnv(t, creator.created, "GITHUB_WORKFLOW_RUN_ATTEMPT", "2")
}

func TestJobRunnerSendsFailedJobLogsToAgent(t *testing.T) {
	agent := &recordingAgent{}
	observer := &recordingObserver{}
	runner := JobRunner{
		Watcher: &recordingWatcher{
			result: JobResult{
				Failed:  true,
				Reason:  "BackoffLimitExceeded",
				Message: "runner exited 1",
			},
		},
		LogCollector: &recordingLogCollector{
			logs: []LogEntry{{
				Pod:       "nova-sre-pod",
				Container: "runner",
				Logs:      "panic: missing config",
			}},
		},
		Agent:    agent,
		Observer: observer,
		Now: func() time.Time {
			return time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
		},
		Logger: log.New(io.Discard, "", 0),
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "runner-jobs",
			Name:      "nova-sre-push-abc123",
		},
	}

	runner.runFailureCallback(context.Background(), job, Event{
		DeliveryID: "delivery-123",
		Type:       "pull_request",
		Body: []byte(`{
			"repository": {"full_name": "acme/widgets"},
			"pull_request": {
				"number": 42,
				"html_url": "https://github.com/acme/widgets/pull/42",
				"head": {"sha": "abcdef", "ref": "feature/logs", "repo": {"full_name": "acme/widgets"}},
				"base": {"ref": "main"}
			}
		}`),
	})

	if !agent.called {
		t.Fatal("expected agent to be called")
	}
	if agent.request.Repository != "acme/widgets" {
		t.Fatalf("expected repository metadata, got %q", agent.request.Repository)
	}
	if agent.request.SHA != "abcdef" {
		t.Fatalf("expected SHA metadata, got %q", agent.request.SHA)
	}
	if agent.request.PullRequest.Number != 42 {
		t.Fatalf("expected PR number 42, got %d", agent.request.PullRequest.Number)
	}
	if agent.request.PullRequest.Head != "feature/logs" || agent.request.PullRequest.Base != "main" {
		t.Fatalf("unexpected PR refs: %#v", agent.request.PullRequest)
	}
	if len(agent.request.Logs) != 1 || agent.request.Logs[0].Logs != "panic: missing config" {
		t.Fatalf("expected collected logs, got %#v", agent.request.Logs)
	}
	if agent.request.Reason != "BackoffLimitExceeded" || agent.request.Message != "runner exited 1" {
		t.Fatalf("expected failure details, got reason=%q message=%q", agent.request.Reason, agent.request.Message)
	}
	if got := observer.last().Status; got != "diagnosed" {
		t.Fatalf("expected final observed status diagnosed, got %q in %#v", got, observer.updates)
	}
}

func TestJobRunnerReportsGitHubCommentErrors(t *testing.T) {
	agent := &recordingAgent{
		response: DiagnoseResponse{
			GitHubCommentError:  "GitHub PR comment lookup failed with HTTP 403; check GITHUB_TOKEN permissions for issue comments.",
			GitHubCommentAction: "failed",
		},
	}
	observer := &recordingObserver{}
	runner := JobRunner{
		Watcher:      &recordingWatcher{result: JobResult{Failed: true, Reason: "BackoffLimitExceeded", Message: "runner exited 1"}},
		LogCollector: &recordingLogCollector{logs: []LogEntry{{Logs: "failed"}}},
		Agent:        agent,
		Observer:     observer,
		Logger:       log.New(io.Discard, "", 0),
	}

	runner.runFailureCallback(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "runner-jobs", Name: "failed-job"},
	}, Event{DeliveryID: "delivery-123", Type: "push", Body: []byte(`{}`)})

	update := observer.last()
	if update.Status != "diagnosis_comment_error" || update.Reason != "GitHubCommentFailed" {
		t.Fatalf("expected GitHub comment error observation, got %#v", update)
	}
	if update.Message != agent.response.GitHubCommentError {
		t.Fatalf("expected sanitized comment error, got %q", update.Message)
	}
}

func TestJobRunnerSkipsAgentForSuccessfulJob(t *testing.T) {
	agent := &recordingAgent{}
	runner := JobRunner{
		Watcher:      &recordingWatcher{result: JobResult{Succeeded: true}},
		LogCollector: &recordingLogCollector{logs: []LogEntry{{Logs: "unused"}}},
		Agent:        agent,
		Logger:       log.New(io.Discard, "", 0),
	}

	runner.runFailureCallback(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "runner-jobs", Name: "successful-job"},
	}, Event{DeliveryID: "delivery-123", Type: "push", Body: []byte(`{}`)})

	if agent.called {
		t.Fatal("did not expect agent call for successful job")
	}
}

func TestJobRunnerHandlesAgentErrorsGracefully(t *testing.T) {
	agent := &recordingAgent{err: errors.New("agent unavailable")}
	observer := &recordingObserver{}
	runner := JobRunner{
		Watcher:      &recordingWatcher{result: JobResult{Failed: true}},
		LogCollector: &recordingLogCollector{logs: []LogEntry{{Logs: "failed"}}},
		Agent:        agent,
		Observer:     observer,
		Logger:       log.New(io.Discard, "", 0),
	}

	runner.runFailureCallback(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "runner-jobs", Name: "failed-job"},
	}, Event{DeliveryID: "delivery-123", Type: "push", Body: []byte(`{}`)})

	if !agent.called {
		t.Fatal("expected best-effort agent call")
	}
	update := observer.last()
	if update.Status != "diagnosis_error" || update.Reason != "DiagnosisRequestFailed" {
		t.Fatalf("expected diagnosis error observation, got %#v", update)
	}
}

func TestKubernetesJobLogCollectorUsesDefaultLogLimit(t *testing.T) {
	collector := KubernetesJobLogCollector{}
	options := collector.podLogOptions("runner")

	if options.Container != "runner" {
		t.Fatalf("expected container runner, got %q", options.Container)
	}
	if options.LimitBytes == nil || *options.LimitBytes != DefaultRunnerLogLimitBytes {
		t.Fatalf("expected default log limit %d, got %#v", DefaultRunnerLogLimitBytes, options.LimitBytes)
	}
}

func TestKubernetesJobLogCollectorUsesConfiguredLogLimit(t *testing.T) {
	collector := KubernetesJobLogCollector{LogLimitBytes: 4096}
	options := collector.podLogOptions("runner")

	if options.LimitBytes == nil || *options.LimitBytes != 4096 {
		t.Fatalf("expected configured log limit 4096, got %#v", options.LimitBytes)
	}
}

func TestHTTPAgentClientPostsDiagnoseRequest(t *testing.T) {
	var got DiagnoseRequest
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/diagnose" {
			t.Fatalf("expected /diagnose path, got %s", r.URL.Path)
		}
		gotToken = r.Header.Get("X-Nova-SRE-Agent-Token")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{
			"github_comment_posted": true,
			"github_comment_url": "https://github.com/acme/widgets/pull/42#issuecomment-1",
			"github_comment_action": "updated"
		}`))
	}))
	defer server.Close()

	client, err := NewHTTPAgentClient(server.URL, time.Second, "agent-token")
	if err != nil {
		t.Fatalf("NewHTTPAgentClient returned error: %v", err)
	}

	response, err := client.Diagnose(context.Background(), DiagnoseRequest{
		DeliveryID: "delivery-123",
		Event:      "push",
		Repository: "acme/widgets",
		SHA:        "abcdef",
		Logs:       []LogEntry{{Pod: "pod-1", Container: "runner", Logs: "boom"}},
	})
	if err != nil {
		t.Fatalf("Diagnose returned error: %v", err)
	}

	if got.Repository != "acme/widgets" || got.SHA != "abcdef" {
		t.Fatalf("expected request metadata, got %#v", got)
	}
	if len(got.Logs) != 1 || got.Logs[0].Logs != "boom" {
		t.Fatalf("expected request logs, got %#v", got.Logs)
	}
	if gotToken != "agent-token" {
		t.Fatalf("expected agent token header, got %q", gotToken)
	}
	if !response.GitHubCommentPosted || response.GitHubCommentAction != "updated" {
		t.Fatalf("expected parsed GitHub comment response, got %#v", response)
	}
}

func TestJobRunnerRecordsCreatedJobMetrics(t *testing.T) {
	metrics := NewPrometheusPipelineMetrics(prometheus.NewRegistry())
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	creator := &recordingCreator{}
	runner := NewJobRunner(JobConfig{
		Image: "ghcr.io/example/nova-runner:test",
	}, creator, log.New(io.Discard, "", 0))
	runner.Metrics = metrics
	runner.Now = sequenceClock(now, now.Add(2*time.Second))

	err := runner.EnqueueGitHubEvent(context.Background(), Event{
		DeliveryID: "delivery-123",
		Type:       "push",
		Body: []byte(`{
			"repository": {"full_name": "acme/widgets"},
			"head_commit": {
				"id": "abc",
				"timestamp": "2026-06-21T11:59:55Z"
			}
		}`),
	})
	if err != nil {
		t.Fatalf("EnqueueGitHubEvent returned error: %v", err)
	}

	assertCounterValue(t, metrics.jobsTotal.WithLabelValues("created", "acme/widgets"), 1)
	assertGaugeValue(t, metrics.activeJobs.WithLabelValues("acme/widgets"), 1)
	assertHistogram(t, metrics.schedulingLatency, 1, 2)
	assertHistogram(t, metrics.agentMTTD, 1, 7)
}

func TestJobRunnerRecordsFailedJobMetricsAndClearsActiveGauge(t *testing.T) {
	metrics := NewPrometheusPipelineMetrics(prometheus.NewRegistry())
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	runner := NewJobRunner(JobConfig{
		Image: "ghcr.io/example/nova-runner:test",
	}, failingCreator{}, log.New(io.Discard, "", 0))
	runner.Metrics = metrics
	runner.Now = sequenceClock(now, now.Add(1500*time.Millisecond))

	err := runner.EnqueueGitHubEvent(context.Background(), Event{
		DeliveryID: "delivery-123",
		Type:       "workflow_run",
		Body:       []byte(`{"workflow_run":{"head_sha":"abc","created_at":"2026-06-21T11:59:50Z","repository":{"full_name":"acme/widgets"}}}`),
	})
	if err == nil {
		t.Fatal("expected create failure")
	}

	assertCounterValue(t, metrics.jobsTotal.WithLabelValues("failed", "acme/widgets"), 1)
	assertGaugeValue(t, metrics.activeJobs.WithLabelValues("acme/widgets"), 0)
	assertHistogram(t, metrics.schedulingLatency, 1, 1.5)
	assertHistogram(t, metrics.agentMTTD, 1, 11.5)
}

type recordingCreator struct {
	created *batchv1.Job
}

func (c *recordingCreator) Create(_ context.Context, job *batchv1.Job) (*batchv1.Job, error) {
	c.created = job
	created := job.DeepCopy()
	created.Name = created.GenerateName + "abc123"
	return created, nil
}

type failingCreator struct{}

func (failingCreator) Create(context.Context, *batchv1.Job) (*batchv1.Job, error) {
	return nil, errors.New("boom")
}

type recordingWatcher struct {
	result JobResult
	err    error
}

func (w *recordingWatcher) WaitForCompletion(_ context.Context, _ string, _ string) (JobResult, error) {
	return w.result, w.err
}

type recordingLogCollector struct {
	logs []LogEntry
	err  error
}

func (c *recordingLogCollector) CollectJobLogs(_ context.Context, _ string, _ string) ([]LogEntry, error) {
	return c.logs, c.err
}

type recordingAgent struct {
	called   bool
	request  DiagnoseRequest
	response DiagnoseResponse
	err      error
}

func (a *recordingAgent) Diagnose(_ context.Context, request DiagnoseRequest) (DiagnoseResponse, error) {
	a.called = true
	a.request = request
	return a.response, a.err
}

type recordingObserver struct {
	updates []JobStatusUpdate
}

func (o *recordingObserver) ObserveJob(update JobStatusUpdate) {
	o.updates = append(o.updates, update)
}

func (o *recordingObserver) last() JobStatusUpdate {
	if len(o.updates) == 0 {
		return JobStatusUpdate{}
	}
	return o.updates[len(o.updates)-1]
}

func sequenceClock(times ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		if i >= len(times) {
			return times[len(times)-1]
		}
		now := times[i]
		i++
		return now
	}
}

func assertCounterValue(t *testing.T, counter prometheus.Counter, want float64) {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("counter Write returned error: %v", err)
	}
	if got := metric.GetCounter().GetValue(); got != want {
		t.Fatalf("expected counter %v, got %v", want, got)
	}
}

func assertGaugeValue(t *testing.T, gauge prometheus.Gauge, want float64) {
	t.Helper()
	metric := &dto.Metric{}
	if err := gauge.Write(metric); err != nil {
		t.Fatalf("gauge Write returned error: %v", err)
	}
	if got := metric.GetGauge().GetValue(); got != want {
		t.Fatalf("expected gauge %v, got %v", want, got)
	}
}

func assertHistogram(t *testing.T, histogram prometheus.Histogram, wantCount uint64, wantSum float64) {
	t.Helper()
	metric := &dto.Metric{}
	if err := histogram.Write(metric); err != nil {
		t.Fatalf("histogram Write returned error: %v", err)
	}
	if got := metric.GetHistogram().GetSampleCount(); got != wantCount {
		t.Fatalf("expected histogram count %d, got %d", wantCount, got)
	}
	if got := metric.GetHistogram().GetSampleSum(); got != wantSum {
		t.Fatalf("expected histogram sum %v, got %v", wantSum, got)
	}
}

func assertContainerEnv(t *testing.T, job *batchv1.Job, name string, want string) {
	t.Helper()
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected one container, got %d", len(job.Spec.Template.Spec.Containers))
	}
	assertEnv(t, job.Spec.Template.Spec.Containers[0].Env, name, want)
}

func assertEnv(t *testing.T, env []corev1.EnvVar, name string, want string) {
	t.Helper()
	for _, item := range env {
		if item.Name == name {
			if item.Value != want {
				t.Fatalf("expected env %s=%q, got %q", name, want, item.Value)
			}
			return
		}
	}
	t.Fatalf("missing env %s in %#v", name, env)
}
