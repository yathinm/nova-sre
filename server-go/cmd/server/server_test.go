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

	"github.com/yathinm/nova-sre/server-go/internal/runner"
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

func TestWebhookRejectsOversizedBody(t *testing.T) {
	server := NewServer("")
	body := strings.Repeat("x", maxWebhookBodyBytes+1)
	req := webhookRequest([]byte(body), "delivery-1", "ping")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d", http.StatusRequestEntityTooLarge, rec.Code)
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

func TestAPIEventsTracksWebhookAndRunnerStatus(t *testing.T) {
	activity := newActivityStore(10)
	server := NewServerWithEnqueuerAndActivity("", func(_ context.Context, event githubEvent) error {
		activity.ObserveJob(runner.JobStatusUpdate{
			DeliveryID: event.DeliveryID,
			Event:      event.Event,
			Repository: "acme/widgets",
			SHA:        "abcdef",
			Status:     "success",
			Namespace:  "nova-sre",
			JobName:    "nova-sre-push-abc12",
		})
		return nil
	}, activity)

	req := webhookRequest([]byte(`{"repository":{"full_name":"acme/widgets"},"after":"abcdef"}`), "delivery-1", "push")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected accepted webhook, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected events status %d, got %d", http.StatusOK, rec.Code)
	}

	var response struct {
		Events []activityRecord `json:"events"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(response.Events) != 1 {
		t.Fatalf("expected one event, got %#v", response.Events)
	}
	event := response.Events[0]
	if event.Status != "success" || event.JobName != "nova-sre-push-abc12" || event.Repository != "acme/widgets" {
		t.Fatalf("unexpected event record: %#v", event)
	}

	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected jobs status %d, got %d", http.StatusOK, rec.Code)
	}
	var jobsResponse struct {
		Jobs []activityRecord `json:"jobs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&jobsResponse); err != nil {
		t.Fatalf("decode jobs response: %v", err)
	}
	if len(jobsResponse.Jobs) != 1 || jobsResponse.Jobs[0].JobName != "nova-sre-push-abc12" {
		t.Fatalf("unexpected jobs response: %#v", jobsResponse.Jobs)
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
		Jobs []activityRecord `json:"jobs"`
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
	if !job.UpdatedAt.Equal(completed) {
		t.Fatalf("expected updated_at %s, got %s", completed, job.UpdatedAt)
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
	server.activity.recordReceived(githubEvent{
		DeliveryID: "memory-delivery",
		Event:      "push",
		Body:       []byte(`{"repository":{"full_name":"acme/api"},"after":"123456"}`),
	})
	server.activity.recordAccepted(githubEvent{
		DeliveryID: "memory-delivery",
		Event:      "push",
	})

	events := getJSON[struct {
		Events []activityRecord `json:"events"`
	}](t, server, "/api/events")

	if len(events.Events) != 2 {
		t.Fatalf("expected memory and Kubernetes events, got %#v", events.Events)
	}
	if events.Events[1].DeliveryID != "durable-delivery" {
		t.Fatalf("expected durable event from Kubernetes jobs, got %#v", events.Events)
	}
	durable := events.Events[1]
	if durable.Event != "pull_request" || durable.Repository != "acme/widgets" {
		t.Fatalf("unexpected durable event: %#v", durable)
	}
	if durable.SHA != "abcdef" || durable.Status != "queued" || !durable.ReceivedAt.Equal(receivedAt) {
		t.Fatalf("unexpected durable event details: %#v", durable)
	}
}

func TestAPISummaryAndCORS(t *testing.T) {
	activity := newActivityStore(10)
	server := NewServerWithEnqueuerAndActivity("", func(context.Context, githubEvent) error { return nil }, activity)
	req := webhookRequest([]byte(`{"repository":{"full_name":"acme/widgets"},"after":"abcdef"}`), "delivery-1", "push")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected accepted webhook, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	req.Header.Set("Origin", "http://localhost:3001")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected summary status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected CORS header, got %q", got)
	}

	var summary activitySummary
	if err := json.NewDecoder(rec.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if summary.Total != 1 || summary.ByStatus["accepted"] != 1 || summary.ByEvent["push"] != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
}

func TestAPIAllowsConfiguredCORSOrigin(t *testing.T) {
	server := NewServer("")
	server.SetAPIAllowedOrigins("https://panel.example.com, http://localhost:5173")
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
		t.Fatalf("expected configured CORS origin, got %q", got)
	}
	if got := rec.Header().Values("Vary"); !containsValue(got, "Origin") {
		t.Fatalf("expected Vary Origin, got %#v", got)
	}
}

func TestAPIDoesNotAllowUnconfiguredCORSOrigin(t *testing.T) {
	server := NewServer("")
	server.SetAPIAllowedOrigins("https://panel.example.com")
	req := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected disallowed origin to be omitted, got %q", got)
	}
}

func TestHealthzAndMetricsUseConfiguredCORSOrigin(t *testing.T) {
	server := NewServer("")
	server.SetAPIAllowedOrigins("https://panel.example.com")

	for _, path := range []string{"/healthz", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Origin", "https://panel.example.com")
		rec := httptest.NewRecorder()

		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected status %d, got %d", path, http.StatusOK, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://panel.example.com" {
			t.Fatalf("%s: expected configured CORS origin, got %q", path, got)
		}
	}
}

func TestParseAllowedOriginsTrimsAndDeduplicates(t *testing.T) {
	origins := parseAllowedOrigins(" https://a.example, ,https://b.example,https://a.example ")

	if len(origins) != 2 || origins[0] != "https://a.example" || origins[1] != "https://b.example" {
		t.Fatalf("unexpected origins: %#v", origins)
	}
}

func TestAPISummaryIncludesKubernetesJobs(t *testing.T) {
	receivedAt := time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)
	completed := receivedAt.Add(3 * time.Minute)
	clientset := fake.NewSimpleClientset(&batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "nova-sre-push-abc123",
			Namespace:         "runner-jobs",
			CreationTimestamp: metav1.NewTime(receivedAt.Add(time.Minute)),
			Labels: map[string]string{
				"app.kubernetes.io/name": "nova-sre-runner",
			},
			Annotations: map[string]string{
				"nova-sre.io/delivery-id": "durable-delivery",
				"nova-sre.io/event":       "push",
				"nova-sre.io/repository":  "acme/widgets",
				"nova-sre.io/commit-sha":  "abcdef",
				"nova-sre.io/received-at": receivedAt.Format(time.RFC3339Nano),
			},
		},
		Status: batchv1.JobStatus{
			CompletionTime: &metav1.Time{Time: completed},
			Succeeded:      1,
		},
	})
	server := NewServer("")
	server.SetKubernetesJobLister(clientset.BatchV1().Jobs("runner-jobs"))

	summary := getJSON[activitySummary](t, server, "/api/summary")

	if summary.Total != 1 {
		t.Fatalf("expected one durable event, got %#v", summary)
	}
	if summary.ByStatus["succeeded"] != 1 || summary.ByEvent["push"] != 1 {
		t.Fatalf("unexpected durable summary buckets: %#v", summary)
	}
	if !summary.UpdatedAt.Equal(completed) {
		t.Fatalf("expected updated_at %s, got %s", completed, summary.UpdatedAt)
	}
}

func TestAPIOptionsIncludeWebhookHeaders(t *testing.T) {
	server := NewServer("")
	req := httptest.NewRequest(http.MethodOptions, "/api/events", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Hub-Signature-256") {
		t.Fatalf("expected webhook signature header to be allowed, got %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, apiTokenHeader) || !strings.Contains(got, "Authorization") {
		t.Fatalf("expected API auth headers to be allowed, got %q", got)
	}
}

func TestAPIRequiresTokenWhenConfigured(t *testing.T) {
	server := NewServer("")
	server.SetAPIToken("control-panel-token")
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAPIAcceptsBearerToken(t *testing.T) {
	server := NewServer("")
	server.SetAPIToken("control-panel-token")
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Authorization", "Bearer control-panel-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestAPIRejectsWrongLengthBearerToken(t *testing.T) {
	server := NewServer("")
	server.SetAPIToken("control-panel-token")
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestAPIAcceptsTokenHeader(t *testing.T) {
	server := NewServer("")
	server.SetAPIToken("control-panel-token")
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set(apiTokenHeader, "control-panel-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
}

func TestAPIConfigReportsRuntimeSettings(t *testing.T) {
	server := NewServer("webhook-secret")
	server.SetRuntimeConfig(runtimeConfig{
		ActivityLimit:       25,
		DeliveryCacheTTL:    "5m0s",
		RunnerNamespace:     "runner-jobs",
		RunnerImage:         "nova-sre-runner:local",
		RunnerJobTTLSeconds: 900,
	})
	server.SetAPIToken("control-panel-token")
	server.SetAPIAllowedOrigins("https://panel.example.com")
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.Header.Set(apiTokenHeader, "control-panel-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var config runtimeConfig
	if err := json.NewDecoder(rec.Body).Decode(&config); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if config.ActivityLimit != 25 || config.DeliveryCacheTTL != "5m0s" {
		t.Fatalf("unexpected activity config: %#v", config)
	}
	if !config.APIAuthEnabled {
		t.Fatalf("expected api auth to be enabled: %#v", config)
	}
	if !config.APICORSRestricted {
		t.Fatalf("expected api cors to be restricted: %#v", config)
	}
	if config.RunnerNamespace != "runner-jobs" || config.RunnerImage != "nova-sre-runner:local" {
		t.Fatalf("unexpected runner config: %#v", config)
	}
	if strings.Contains(rec.Body.String(), "webhook-secret") ||
		strings.Contains(rec.Body.String(), "control-panel-token") ||
		strings.Contains(rec.Body.String(), "panel.example.com") {
		t.Fatalf("config response leaked a secret: %s", rec.Body.String())
	}
}

func TestAPIConfigReportsWildcardCORSAsUnrestricted(t *testing.T) {
	server := NewServer("")
	server.SetAPIAllowedOrigins("*")

	config := getJSON[runtimeConfig](t, server, "/api/config")

	if config.APICORSRestricted {
		t.Fatalf("expected wildcard cors to be unrestricted: %#v", config)
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

func TestDeliveryCacheTTLCanBeUpdated(t *testing.T) {
	cache := newDeliveryCache(time.Hour)
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if cache.Add("delivery-1", now) {
		t.Fatal("first delivery should not be a duplicate")
	}
	cache.SetTTL(time.Second)
	if cache.Add("delivery-1", now.Add(2*time.Second)) {
		t.Fatal("delivery after updated TTL should be accepted again")
	}
}

func TestIntFromEnv(t *testing.T) {
	t.Setenv("NOVA_SRE_ACTIVITY_LIMIT", "25")
	if got := intFromEnv("NOVA_SRE_ACTIVITY_LIMIT", 100); got != 25 {
		t.Fatalf("expected parsed value 25, got %d", got)
	}

	t.Setenv("NOVA_SRE_ACTIVITY_LIMIT", "-1")
	if got := intFromEnv("NOVA_SRE_ACTIVITY_LIMIT", 100); got != 100 {
		t.Fatalf("expected fallback for invalid value, got %d", got)
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

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}
