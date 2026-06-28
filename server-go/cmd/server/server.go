package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yathinm/nova-sre/server-go/internal/runner"
)

const (
	githubDeliveryHeader  = "X-GitHub-Delivery"
	githubEventHeader     = "X-GitHub-Event"
	githubSignatureHeader = "X-Hub-Signature-256"
	maxWebhookBodyBytes   = 1 << 20
)

var supportedGitHubEvents = map[string]struct{}{
	"ping":         {},
	"pull_request": {},
	"push":         {},
	"workflow_run": {},
}

type Server struct {
	mux           *http.ServeMux
	webhookSecret string
	deliveries    *deliveryCache
	enqueueEvent  githubEventEnqueuer
	activity      *activityStore
}

func NewServer(webhookSecret string) *Server {
	return NewServerWithEnqueuer(webhookSecret, enqueueGitHubEvent)
}

func NewServerWithRunner(webhookSecret string, eventRunner githubEventRunner) *Server {
	return NewServerWithEnqueuer(webhookSecret, enqueueGitHubEventWithRunner(eventRunner))
}

func NewServerWithEnqueuer(webhookSecret string, enqueueEvent githubEventEnqueuer) *Server {
	s := &Server{
		mux:           http.NewServeMux(),
		webhookSecret: webhookSecret,
		deliveries:    newDeliveryCache(15 * time.Minute),
		enqueueEvent:  enqueueEvent,
		activity:      newActivityStore(50),
	}

	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.Handle("/metrics", promhttp.Handler())
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/jobs", s.handleJobs)
	s.mux.HandleFunc("/webhook", s.handleWebhook)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"events": s.activity.Events()})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"jobs": s.activity.Jobs()})
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	deliveryID := strings.TrimSpace(r.Header.Get(githubDeliveryHeader))
	if deliveryID == "" {
		http.Error(w, "missing GitHub delivery header", http.StatusBadRequest)
		return
	}

	event := strings.TrimSpace(r.Header.Get(githubEventHeader))
	if event == "" {
		http.Error(w, "missing GitHub event header", http.StatusBadRequest)
		return
	}
	if _, ok := supportedGitHubEvents[event]; !ok {
		http.Error(w, "unsupported GitHub event", http.StatusNotImplemented)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes))
	if err != nil {
		http.Error(w, "webhook body too large", http.StatusRequestEntityTooLarge)
		return
	}

	if s.webhookSecret != "" && !validGitHubSignature(r.Header.Get(githubSignatureHeader), body, s.webhookSecret) {
		http.Error(w, "invalid GitHub signature", http.StatusUnauthorized)
		return
	}

	if alreadySeen := s.deliveries.Add(deliveryID, time.Now()); alreadySeen {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	receivedAt := time.Now().UTC()
	repository := repositoryFromGitHubPayload(body)
	s.activity.RecordEvent(activityEvent{
		DeliveryID: deliveryID,
		Event:      event,
		Repository: repository,
		ReceivedAt: receivedAt,
		Status:     "accepted",
	})

	if err := s.enqueueEvent(r.Context(), githubEvent{
		DeliveryID: deliveryID,
		Event:      event,
		Body:       body,
	}); err != nil {
		s.activity.UpdateEventStatus(deliveryID, "failed")
		log.Printf("failed to enqueue GitHub event delivery=%s event=%s: %v", deliveryID, event, err)
		http.Error(w, "failed to enqueue GitHub event", http.StatusInternalServerError)
		return
	}

	s.activity.RecordJob(activityJob{
		JobName:      "delivery-" + shortDeliveryID(deliveryID),
		Namespace:    "nova-sre",
		Event:        event,
		Repository:   repository,
		DeliveryID:   deliveryID,
		ObservedTime: receivedAt,
		Status:       "queued",
	})
	w.WriteHeader(http.StatusAccepted)
}

func validGitHubSignature(signatureHeader string, body []byte, secret string) bool {
	const prefix = "sha256="

	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}

	got, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type githubEvent struct {
	DeliveryID string
	Event      string
	Body       json.RawMessage
}

type githubEventEnqueuer func(context.Context, githubEvent) error

type githubEventRunner interface {
	EnqueueGitHubEvent(context.Context, runner.Event) error
}

func enqueueGitHubEvent(_ context.Context, event githubEvent) error {
	// TODO: Create a Kubernetes Job for supported GitHub webhook events.
	log.Printf("accepted GitHub event delivery=%s event=%s body_bytes=%d", event.DeliveryID, event.Event, len(event.Body))
	return nil
}

func enqueueGitHubEventWithRunner(eventRunner githubEventRunner) githubEventEnqueuer {
	return func(ctx context.Context, event githubEvent) error {
		return eventRunner.EnqueueGitHubEvent(ctx, runner.Event{
			DeliveryID: event.DeliveryID,
			Type:       event.Event,
			Body:       event.Body,
		})
	}
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, X-GitHub-Delivery, X-GitHub-Event, X-Hub-Signature-256")
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func repositoryFromGitHubPayload(body []byte) string {
	var payload struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest struct {
			Head struct {
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
		WorkflowRun struct {
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"workflow_run"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return firstNonEmpty(payload.Repository.FullName, payload.PullRequest.Head.Repo.FullName, payload.WorkflowRun.Repository.FullName)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func shortDeliveryID(deliveryID string) string {
	deliveryID = strings.TrimSpace(deliveryID)
	if len(deliveryID) <= 8 {
		return deliveryID
	}
	return deliveryID[:8]
}

type activityEvent struct {
	DeliveryID string    `json:"delivery_id"`
	Event      string    `json:"event"`
	Repository string    `json:"repository,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	Status     string    `json:"status"`
}

type activityJob struct {
	JobName      string    `json:"job_name"`
	Namespace    string    `json:"namespace"`
	Event        string    `json:"event"`
	Repository   string    `json:"repository,omitempty"`
	DeliveryID   string    `json:"delivery_id"`
	ObservedTime time.Time `json:"observed_time"`
	Status       string    `json:"status"`
}

type activityStore struct {
	limit  int
	mu     sync.Mutex
	events []activityEvent
	jobs   []activityJob
}

func newActivityStore(limit int) *activityStore {
	return &activityStore{limit: limit}
}

func (s *activityStore) RecordEvent(event activityEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = prependLimited(event, s.events, s.limit)
}

func (s *activityStore) UpdateEventStatus(deliveryID string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.events {
		if s.events[i].DeliveryID == deliveryID {
			s.events[i].Status = status
			return
		}
	}
}

func (s *activityStore) RecordJob(job activityJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = prependLimited(job, s.jobs, s.limit)
}

func (s *activityStore) Events() []activityEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]activityEvent(nil), s.events...)
}

func (s *activityStore) Jobs() []activityJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]activityJob(nil), s.jobs...)
}

func prependLimited[T any](item T, items []T, limit int) []T {
	next := append([]T{item}, items...)
	if limit > 0 && len(next) > limit {
		return next[:limit]
	}
	return next
}

type deliveryCache struct {
	ttl  time.Duration
	mu   sync.Mutex
	seen map[string]time.Time
}

func newDeliveryCache(ttl time.Duration) *deliveryCache {
	return &deliveryCache{
		ttl:  ttl,
		seen: make(map[string]time.Time),
	}
}

func (c *deliveryCache) Add(deliveryID string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.prune(now)
	if _, ok := c.seen[deliveryID]; ok {
		return true
	}

	c.seen[deliveryID] = now
	return false
}

func (c *deliveryCache) prune(now time.Time) {
	cutoff := now.Add(-c.ttl)
	for deliveryID, seenAt := range c.seen {
		if seenAt.Before(cutoff) {
			delete(c.seen, deliveryID)
		}
	}
}
