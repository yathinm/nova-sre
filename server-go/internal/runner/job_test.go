package runner

import (
	"context"
	"io"
	"log"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildGitHubEventJobUsesConfigAndWebhookMetadata(t *testing.T) {
	ttl := int32(120)
	backoff := int32(1)
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
		Body:       []byte(`{"repository":{"full_name":"acme/widgets"},"after":"abcdef1234567890"}`),
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

	assertEnv(t, container.Env, "GITHUB_EVENT_NAME", "push")
	assertEnv(t, container.Env, "GITHUB_DELIVERY_ID", "delivery-123")
	assertEnv(t, container.Env, "GITHUB_REPOSITORY", "acme/widgets")
	assertEnv(t, container.Env, "GITHUB_SHA", "abcdef1234567890")
	assertEnv(t, container.Env, "GITHUB_EVENT_PAYLOAD", `{"repository":{"full_name":"acme/widgets"},"after":"abcdef1234567890"}`)

	if job.Labels["nova-sre.io/event"] != "push" {
		t.Fatalf("expected event label push, got %q", job.Labels["nova-sre.io/event"])
	}
	if job.Annotations["nova-sre.io/delivery-id"] != "delivery-123" {
		t.Fatalf("expected delivery annotation, got %q", job.Annotations["nova-sre.io/delivery-id"])
	}
	if job.Annotations["nova-sre.io/repository"] != "acme/widgets" {
		t.Fatalf("expected repository annotation, got %q", job.Annotations["nova-sre.io/repository"])
	}
}

func TestBuildGitHubEventJobExtractsPullRequestMetadata(t *testing.T) {
	job, err := BuildGitHubEventJob(JobConfig{
		Image: "ghcr.io/example/nova-runner:test",
	}, Event{
		DeliveryID: "delivery-123",
		Type:       "pull_request",
		Body: []byte(`{
			"pull_request": {
				"head": {
					"sha": "0123456789abcdef",
					"repo": {"full_name": "acme/pr-source"}
				}
			}
		}`),
	})
	if err != nil {
		t.Fatalf("BuildGitHubEventJob returned error: %v", err)
	}

	assertContainerEnv(t, job, "GITHUB_REPOSITORY", "acme/pr-source")
	assertContainerEnv(t, job, "GITHUB_SHA", "0123456789abcdef")
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

func TestJobRunnerCanUseStubCreatorWithoutCluster(t *testing.T) {
	creator := &recordingCreator{}
	runner := NewJobRunner(JobConfig{
		Image: "ghcr.io/example/nova-runner:test",
	}, creator, log.New(io.Discard, "", 0))

	if err := runner.EnqueueGitHubEvent(context.Background(), Event{
		DeliveryID: "delivery-123",
		Type:       "workflow_run",
		Body:       []byte(`{"workflow_run":{"head_sha":"abc","repository":{"full_name":"acme/widgets"}}}`),
	}); err != nil {
		t.Fatalf("EnqueueGitHubEvent returned error: %v", err)
	}

	if creator.created == nil {
		t.Fatal("expected stub creator to receive a job")
	}
	assertContainerEnv(t, creator.created, "GITHUB_REPOSITORY", "acme/widgets")
	assertContainerEnv(t, creator.created, "GITHUB_SHA", "abc")
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
