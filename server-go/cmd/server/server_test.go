package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestHealthz(t *testing.T) {
	server := NewServer("")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("expected ok body, got %q", got)
	}
}

func TestMetrics(t *testing.T) {
	server := NewServer("")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go_gc_duration_seconds") {
		t.Fatalf("expected Prometheus metrics body, got %q", rec.Body.String())
	}
}

func TestWebhookAcceptsSignedSupportedEvent(t *testing.T) {
	const secret = "top-secret"
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	server := NewServer(secret)
	req := webhookRequest(body, "delivery-1", "ping")
	req.Header.Set(githubSignatureHeader, signBody(body, secret))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
}

func TestWebhookRecordsRecentEventAndJob(t *testing.T) {
	body := []byte(`{"repository":{"full_name":"acme/widgets"},"after":"abc123"}`)
	server := NewServerWithEnqueuer("", func(_ context.Context, _ githubEvent) error {
		return nil
	})
	req := webhookRequest(body, "delivery-1", "push")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	events := getJSON[struct {
		Events []activityEvent `json:"events"`
	}](t, server, "/api/events")
	if len(events.Events) != 1 {
		t.Fatalf("expected one event, got %#v", events.Events)
	}
	if events.Events[0].DeliveryID != "delivery-1" || events.Events[0].Event != "push" || events.Events[0].Repository != "acme/widgets" {
		t.Fatalf("unexpected event: %#v", events.Events[0])
	}
	if events.Events[0].Status != "accepted" {
		t.Fatalf("expected accepted event, got %q", events.Events[0].Status)
	}

	jobs := getJSON[struct {
		Jobs []activityJob `json:"jobs"`
	}](t, server, "/api/jobs")
	if len(jobs.Jobs) != 1 {
		t.Fatalf("expected one job, got %#v", jobs.Jobs)
	}
	if jobs.Jobs[0].Event != "push" || jobs.Jobs[0].Repository != "acme/widgets" || jobs.Jobs[0].DeliveryID != "delivery-1" {
		t.Fatalf("unexpected job: %#v", jobs.Jobs[0])
	}
	if jobs.Jobs[0].Status != "queued" {
		t.Fatalf("expected queued job, got %q", jobs.Jobs[0].Status)
	}
}

func TestAPIJobsReflectKubernetesJobStatus(t *testing.T) {
	started := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	completed := started.Add(3 * time.Minute)
	clientset := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nova-sre-push-abc123",
			Namespace:         "runner-jobs",
			CreationTimestamp: metav1.NewTime(started.Add(-time.Minute)),
			Labels: map[string]string{
				"app.kubernetes.io/name": "nova-sre-runner",
			},
			Annotations: map[string]string{
				"nova-sre.io/delivery-id": "delivery-1",
				"nova-sre.io/event":       "push",
				"nova-sre.io/repository":  "acme/widgets",
				"nova-sre.io/commit-sha":  "abcdef",
				"nova-sre.io/received-at": started.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			},
		},
		Status: batchv1.JobStatus{
			StartTime:      &metav1.Time{Time: started},
			CompletionTime: &metav1.Time{Time: completed},
			Succeeded:      1,
			Conditions: []batchv1.JobCondition{{
				Type:    batchv1.JobComplete,
				Status:  corev1.ConditionTrue,
				Reason:  "Completed",
				Message: "runner completed",
			}},
		},
	}, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated",
			Namespace: "runner-jobs",
		},
	})
	server := NewServer("")
	server.SetKubernetesJobLister(clientset.BatchV1().Jobs("runner-jobs"))

	jobs := getJSON[struct {
		Jobs []activityJob `json:"jobs"`
	}](t, server, "/api/jobs")

	if len(jobs.Jobs) != 1 {
		t.Fatalf("expected one runner job, got %#v", jobs.Jobs)
	}
	job := jobs.Jobs[0]
	if job.JobName != "nova-sre-push-abc123" || job.Namespace != "runner-jobs" {
		t.Fatalf("unexpected job identity: %#v", job)
	}
	if job.Status != "succeeded" || job.Reason != "Completed" || job.Message != "runner completed" {
		t.Fatalf("unexpected status details: %#v", job)
	}
	if job.Event != "push" || job.Repository != "acme/widgets" || job.SHA != "abcdef" || job.DeliveryID != "delivery-1" {
		t.Fatalf("unexpected job metadata: %#v", job)
	}
	if job.Succeeded != 1 || job.Active != 0 || job.Failed != 0 {
		t.Fatalf("unexpected job counters: %#v", job)
	}
	if job.StartedAt == nil || !job.StartedAt.Equal(started) {
		t.Fatalf("expected started_at %s, got %#v", started, job.StartedAt)
	}
	if job.CompletedAt == nil || !job.CompletedAt.Equal(completed) {
		t.Fatalf("expected completed_at %s, got %#v", completed, job.CompletedAt)
	}
	if !job.ObservedTime.Equal(completed) {
		t.Fatalf("expected observed time %s, got %s", completed, job.ObservedTime)
	}
}

func TestAPIEventsMergeMemoryAndKubernetesJobs(t *testing.T) {
	receivedAt := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	clientset := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nova-sre-pull-request-abc123",
			Namespace:         "runner-jobs",
			CreationTimestamp: metav1.NewTime(receivedAt.Add(time.Minute)),
			Labels: map[string]string{
				"app.kubernetes.io/name": "nova-sre-runner",
			},
			Annotations: map[string]string{
				"nova-sre.io/delivery-id": "durable-delivery",
				"nova-sre.io/event":       "pull_request",
				"nova-sre.io/repository":  "acme/widgets",
				"nova-sre.io/commit-sha":  "abcdef",
				"nova-sre.io/received-at": receivedAt.Format(time.RFC3339Nano),
			},
		},
	})
	server := NewServer("")
	server.SetKubernetesJobLister(clientset.BatchV1().Jobs("runner-jobs"))
	server.activity.RecordEvent(activityEvent{
		DeliveryID: "memory-delivery",
		Event:      "push",
		Repository: "acme/api",
		ReceivedAt: receivedAt.Add(2 * time.Minute),
		Status:     "accepted",
	})

	events := getJSON[struct {
		Events []activityEvent `json:"events"`
	}](t, server, "/api/events")

	if len(events.Events) != 2 {
		t.Fatalf("expected memory and Kubernetes events, got %#v", events.Events)
	}
	if events.Events[0].DeliveryID != "memory-delivery" {
		t.Fatalf("expected newest in-memory event first, got %#v", events.Events)
	}
	durable := events.Events[1]
	if durable.DeliveryID != "durable-delivery" || durable.Event != "pull_request" || durable.Repository != "acme/widgets" {
		t.Fatalf("unexpected durable event: %#v", durable)
	}
	if durable.SHA != "abcdef" || durable.Status != "accepted" || !durable.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("unexpected durable event details: %#v", durable)
	}
}

func TestAPIResponsesIncludeCORSHeaders(t *testing.T) {
	server := NewServer("")
	req := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected CORS origin *, got %q", got)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	server := NewServer("top-secret")
	req := webhookRequest([]byte(`{"ok":true}`), "delivery-1", "ping")
	req.Header.Set(githubSignatureHeader, signBody([]byte(`{"ok":false}`), "top-secret"))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestWebhookAcceptsDuplicateDeliveryWithoutReprocessing(t *testing.T) {
	server := NewServer("")
	enqueued := 0
	server.enqueueEvent = func(_ context.Context, _ githubEvent) error {
		enqueued++
		return nil
	}

	for i := 0; i < 2; i++ {
		req := webhookRequest([]byte(`{"ref":"main"}`), "delivery-1", "push")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: expected status %d, got %d", i+1, http.StatusAccepted, rec.Code)
		}
	}

	if enqueued != 1 {
		t.Fatalf("expected duplicate delivery to enqueue once, got %d", enqueued)
	}
}

func TestWebhookRejectsUnsupportedEvent(t *testing.T) {
	server := NewServer("")
	req := webhookRequest([]byte(`{}`), "delivery-1", "repository")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected status %d, got %d", http.StatusNotImplemented, rec.Code)
	}
}

func TestDeliveryCacheExpiresOldDeliveries(t *testing.T) {
	cache := newDeliveryCache(time.Minute)
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if cache.Add("delivery-1", now) {
		t.Fatal("first delivery should not be a duplicate")
	}
	if !cache.Add("delivery-1", now.Add(30*time.Second)) {
		t.Fatal("delivery inside TTL should be a duplicate")
	}
	if cache.Add("delivery-1", now.Add(2*time.Minute)) {
		t.Fatal("delivery after TTL should be accepted again")
	}
}

func webhookRequest(body []byte, deliveryID string, event string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set(githubDeliveryHeader, deliveryID)
	req.Header.Set(githubEventHeader, event)
	return req
}

func getJSON[T any](t *testing.T, server *Server, path string) T {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: expected status %d, got %d: %s", path, http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload T
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: decode response: %v", path, err)
	}
	return payload
}

func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}
