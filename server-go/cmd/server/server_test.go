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

func signBody(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return fmt.Sprintf("sha256=%x", mac.Sum(nil))
}
