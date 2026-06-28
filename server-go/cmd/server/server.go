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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/yathinm/nova-sre/server-go/internal/runner"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
	jobLister     kubernetesJobLister
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

func (s *Server) SetKubernetesJobLister(jobLister kubernetesJobLister) {
	s.jobLister = jobLister
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
	events, err := s.activityEvents(r.Context())
	if err != nil {
		http.Error(w, "failed to list activity events", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobs, err := s.activityJobs(r.Context())
	if err != nil {
		http.Error(w, "failed to list activity jobs", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"jobs": jobs})
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
		ReceivedAt: receivedAt,
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
	ReceivedAt time.Time
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
			ReceivedAt: event.ReceivedAt,
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
	SHA        string    `json:"sha,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	Status     string    `json:"status"`
}

type activityJob struct {
	JobName      string     `json:"job_name"`
	Namespace    string     `json:"namespace"`
	Event        string     `json:"event"`
	Repository   string     `json:"repository,omitempty"`
	SHA          string     `json:"sha,omitempty"`
	DeliveryID   string     `json:"delivery_id"`
	ObservedTime time.Time  `json:"observed_time"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"`
	Message      string     `json:"message,omitempty"`
	Active       int32      `json:"active"`
	Succeeded    int32      `json:"succeeded"`
	Failed       int32      `json:"failed"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

type kubernetesJobLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*batchv1.JobList, error)
}

func (s *Server) activityEvents(ctx context.Context) ([]activityEvent, error) {
	events := s.activity.Events()
	if s.jobLister == nil {
		return events, nil
	}

	jobs, err := s.listRunnerJobs(ctx)
	if err != nil {
		return nil, err
	}

	byDelivery := make(map[string]activityEvent, len(events)+len(jobs.Items))
	for _, event := range events {
		byDelivery[event.DeliveryID] = event
	}
	for _, job := range jobs.Items {
		event, ok := activityEventFromJob(&job)
		if !ok {
			continue
		}
		if _, exists := byDelivery[event.DeliveryID]; !exists {
			byDelivery[event.DeliveryID] = event
		}
	}

	merged := make([]activityEvent, 0, len(byDelivery))
	for _, event := range byDelivery {
		merged = append(merged, event)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].ReceivedAt.After(merged[j].ReceivedAt)
	})
	return limitActivity(merged, s.activity.limit), nil
}

func (s *Server) activityJobs(ctx context.Context) ([]activityJob, error) {
	if s.jobLister == nil {
		return s.activity.Jobs(), nil
	}

	jobs, err := s.listRunnerJobs(ctx)
	if err != nil {
		return nil, err
	}

	activity := make([]activityJob, 0, len(jobs.Items))
	for _, job := range jobs.Items {
		activity = append(activity, activityJobFromJob(&job))
	}
	sort.SliceStable(activity, func(i, j int) bool {
		return activity[i].ObservedTime.After(activity[j].ObservedTime)
	})
	return limitActivity(activity, s.activity.limit), nil
}

func (s *Server) listRunnerJobs(ctx context.Context) (*batchv1.JobList, error) {
	selector := labels.Set{"app.kubernetes.io/name": "nova-sre-runner"}.String()
	return s.jobLister.List(ctx, metav1.ListOptions{LabelSelector: selector})
}

func activityEventFromJob(job *batchv1.Job) (activityEvent, bool) {
	if job == nil || strings.TrimSpace(job.Annotations["nova-sre.io/delivery-id"]) == "" {
		return activityEvent{}, false
	}

	receivedAt := parseAnnotationTime(job.Annotations["nova-sre.io/received-at"])
	if receivedAt.IsZero() {
		receivedAt = job.CreationTimestamp.Time
	}
	return activityEvent{
		DeliveryID: job.Annotations["nova-sre.io/delivery-id"],
		Event:      job.Annotations["nova-sre.io/event"],
		Repository: job.Annotations["nova-sre.io/repository"],
		SHA:        job.Annotations["nova-sre.io/commit-sha"],
		ReceivedAt: receivedAt.UTC(),
		Status:     "accepted",
	}, true
}

func activityJobFromJob(job *batchv1.Job) activityJob {
	status, reason, message := jobStatus(job)
	observedAt := jobObservedTime(job)
	return activityJob{
		JobName:      job.Name,
		Namespace:    job.Namespace,
		Event:        job.Annotations["nova-sre.io/event"],
		Repository:   job.Annotations["nova-sre.io/repository"],
		SHA:          job.Annotations["nova-sre.io/commit-sha"],
		DeliveryID:   job.Annotations["nova-sre.io/delivery-id"],
		ObservedTime: observedAt.UTC(),
		Status:       status,
		Reason:       reason,
		Message:      message,
		Active:       job.Status.Active,
		Succeeded:    job.Status.Succeeded,
		Failed:       job.Status.Failed,
		StartedAt:    metav1TimePtr(job.Status.StartTime),
		CompletedAt:  metav1TimePtr(job.Status.CompletionTime),
	}
}

func jobStatus(job *batchv1.Job) (string, string, string) {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return "succeeded", condition.Reason, condition.Message
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return "failed", condition.Reason, condition.Message
		}
	}
	if job.Status.Active > 0 {
		return "running", "", ""
	}
	if job.Status.Succeeded > 0 {
		return "succeeded", "", ""
	}
	if job.Status.Failed > 0 {
		return "failed", "", ""
	}
	return "queued", "", ""
}

func jobObservedTime(job *batchv1.Job) time.Time {
	for _, value := range []*metav1.Time{job.Status.CompletionTime, job.Status.StartTime} {
		if value != nil && !value.IsZero() {
			return value.Time
		}
	}
	if !job.CreationTimestamp.IsZero() {
		return job.CreationTimestamp.Time
	}
	return time.Time{}
}

func parseAnnotationTime(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func metav1TimePtr(value *metav1.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	utc := value.Time.UTC()
	return &utc
}

func limitActivity[T any](items []T, limit int) []T {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
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
