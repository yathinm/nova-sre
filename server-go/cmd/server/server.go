package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
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
	apiTokenHeader        = "X-Nova-SRE-API-Token"
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
	apiToken      string
	apiOrigins    []string
	runtimeConfig runtimeConfig
}

type runtimeConfig struct {
	ActivityLimit        int    `json:"activity_limit"`
	ActivityStoreEnabled bool   `json:"activity_store_enabled"`
	DeliveryCacheTTL     string `json:"delivery_cache_ttl"`
	APIAuthEnabled       bool   `json:"api_auth_enabled"`
	AgentAuthEnabled     bool   `json:"agent_auth_enabled"`
	APICORSRestricted    bool   `json:"api_cors_restricted"`
	RunnerNamespace      string `json:"runner_namespace,omitempty"`
	RunnerImage          string `json:"runner_image,omitempty"`
	RunnerJobTTLSeconds  int32  `json:"runner_job_ttl_seconds,omitempty"`
	GitHubCommentMode    string `json:"github_comment_mode"`
}

func NewServer(webhookSecret string) *Server {
	return NewServerWithEnqueuer(webhookSecret, enqueueGitHubEvent)
}

func NewServerWithRunner(webhookSecret string, eventRunner githubEventRunner) *Server {
	return NewServerWithRunnerAndActivity(webhookSecret, eventRunner, newActivityStore(defaultActivityLimit))
}

func NewServerWithRunnerAndActivity(webhookSecret string, eventRunner githubEventRunner, activity *activityStore) *Server {
	return NewServerWithEnqueuerAndActivity(webhookSecret, enqueueGitHubEventWithRunner(eventRunner), activity)
}

func NewServerWithEnqueuer(webhookSecret string, enqueueEvent githubEventEnqueuer) *Server {
	return NewServerWithEnqueuerAndActivity(webhookSecret, enqueueEvent, newActivityStore(defaultActivityLimit))
}

func NewServerWithEnqueuerAndActivity(webhookSecret string, enqueueEvent githubEventEnqueuer, activity *activityStore) *Server {
	if activity == nil {
		activity = newActivityStore(defaultActivityLimit)
	}
	s := &Server{
		mux:           http.NewServeMux(),
		webhookSecret: webhookSecret,
		deliveries:    newDeliveryCache(15 * time.Minute),
		enqueueEvent:  enqueueEvent,
		activity:      activity,
	}

	s.routes()
	return s
}

func (s *Server) SetKubernetesJobLister(jobLister kubernetesJobLister) {
	s.jobLister = jobLister
}

func (s *Server) SetAPIToken(token string) {
	s.apiToken = strings.TrimSpace(token)
	s.runtimeConfig.APIAuthEnabled = s.apiToken != ""
}

func (s *Server) SetAPIAllowedOrigins(raw string) {
	s.apiOrigins = parseAllowedOrigins(raw)
	s.runtimeConfig.APICORSRestricted = len(s.apiOrigins) > 0 && !containsOrigin(s.apiOrigins, "*")
}

func (s *Server) SetDeliveryCacheTTL(ttl time.Duration) {
	if ttl <= 0 || s.deliveries == nil {
		return
	}
	s.deliveries.SetTTL(ttl)
	s.runtimeConfig.DeliveryCacheTTL = ttl.String()
}

func (s *Server) SetRuntimeConfig(config runtimeConfig) {
	config.APIAuthEnabled = s.runtimeConfig.APIAuthEnabled
	if strings.TrimSpace(config.DeliveryCacheTTL) == "" && s.deliveries != nil {
		config.DeliveryCacheTTL = s.deliveries.TTL().String()
	}
	s.runtimeConfig = config
	s.runtimeConfig.APIAuthEnabled = s.apiToken != ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.isBrowserReadablePath(r.URL.Path) {
		s.setAPIHeaders(w, r)
		if r.Method == http.MethodOptions && strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.validAPIToken(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) isBrowserReadablePath(path string) bool {
	return strings.HasPrefix(path, "/api/") || path == "/healthz" || path == "/metrics"
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.Handle("/metrics", promhttp.Handler())
	s.mux.HandleFunc("/api/config", s.handleAPIConfig)
	s.mux.HandleFunc("/api/summary", s.handleAPISummary)
	s.mux.HandleFunc("/api/events", s.handleAPIEvents)
	s.mux.HandleFunc("/api/jobs", s.handleAPIJobs)
	s.mux.HandleFunc("/webhook", s.handleWebhook)
}

func (s *Server) handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.runtimeConfig)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
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

	eventPayload := githubEvent{
		DeliveryID: deliveryID,
		Event:      event,
		Body:       body,
		ReceivedAt: time.Now().UTC(),
	}
	s.activity.recordReceived(eventPayload)

	if alreadySeen := s.deliveries.Add(deliveryID, time.Now()); alreadySeen {
		s.activity.recordDuplicate(eventPayload)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if err := s.enqueueEvent(r.Context(), eventPayload); err != nil {
		log.Printf("failed to enqueue GitHub event delivery=%s event=%s: %v", deliveryID, event, err)
		s.activity.recordError(eventPayload, err.Error())
		http.Error(w, "failed to enqueue GitHub event", http.StatusInternalServerError)
		return
	}

	s.activity.recordAccepted(eventPayload)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAPISummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, summarizeActivityRecords(s.activityEvents(r.Context(), defaultActivityLimit)))
}

func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := s.apiListLimit(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"events": s.activityEvents(r.Context(), limit),
	})
}

func (s *Server) handleAPIJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := s.apiListLimit(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": s.activityJobs(r.Context(), limit),
	})
}

func (s *Server) apiListLimit(r *http.Request) int {
	limit := defaultActivityLimit
	if s.activity != nil && s.activity.limit > 0 {
		limit = s.activity.limit
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < limit {
			limit = parsed
		}
	}
	return limit
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
	Body       []byte
	ReceivedAt time.Time
}

type githubEventEnqueuer func(context.Context, githubEvent) error

type githubEventRunner interface {
	EnqueueGitHubEvent(context.Context, runner.Event) error
}

func enqueueGitHubEvent(_ context.Context, event githubEvent) error {
	// Fallback for tests and local runs without an injected Kubernetes runner.
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

func (s *Server) setAPIHeaders(w http.ResponseWriter, r *http.Request) {
	if origin := s.allowedAPIOrigin(r.Header.Get("Origin")); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if origin != "*" {
			w.Header().Add("Vary", "Origin")
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-GitHub-Delivery, X-GitHub-Event, X-Hub-Signature-256, "+apiTokenHeader)
	w.Header().Set("Cache-Control", "no-store")
}

func (s *Server) allowedAPIOrigin(requestOrigin string) string {
	origins := s.apiOrigins
	if len(origins) == 0 {
		return "*"
	}
	for _, origin := range origins {
		if origin == "*" {
			return "*"
		}
		if requestOrigin != "" && origin == requestOrigin {
			return requestOrigin
		}
	}
	return ""
}

func parseAllowedOrigins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	values := strings.Split(raw, ",")
	origins := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		origin := strings.TrimSpace(value)
		if origin == "" {
			continue
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins
}

func containsOrigin(origins []string, want string) bool {
	for _, origin := range origins {
		if origin == want {
			return true
		}
	}
	return false
}

func (s *Server) validAPIToken(r *http.Request) bool {
	if strings.TrimSpace(s.apiToken) == "" {
		return true
	}
	candidate := strings.TrimSpace(r.Header.Get(apiTokenHeader))
	if candidate == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if scheme, value, ok := strings.Cut(auth, " "); ok && strings.EqualFold(scheme, "Bearer") {
			candidate = strings.TrimSpace(value)
		}
	}
	if candidate == "" {
		return false
	}
	return secureCompareToken(candidate, s.apiToken)
}

func secureCompareToken(candidate string, expected string) bool {
	if candidate == "" || expected == "" {
		return false
	}
	candidateHash := sha256.Sum256([]byte(candidate))
	expectedHash := sha256.Sum256([]byte(expected))
	return hmac.Equal(candidateHash[:], expectedHash[:])
}

type kubernetesJobLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*batchv1.JobList, error)
}

func (s *Server) activityEvents(ctx context.Context, limit int) []activityRecord {
	events := s.activity.recent(limit)
	if s.jobLister == nil {
		return events
	}

	jobs, err := s.listRunnerJobs(ctx)
	if err != nil {
		log.Printf("failed to list Kubernetes runner jobs for activity events: %v", err)
		return events
	}

	byDelivery := make(map[string]activityRecord, len(events)+len(jobs.Items))
	for _, event := range events {
		byDelivery[event.DeliveryID] = event
	}
	for _, job := range jobs.Items {
		record, ok := activityRecordFromJob(&job)
		if !ok {
			continue
		}
		if existing, exists := byDelivery[record.DeliveryID]; !exists || record.UpdatedAt.After(existing.UpdatedAt) {
			byDelivery[record.DeliveryID] = mergeActivityRecord(existing, record)
		}
	}

	merged := make([]activityRecord, 0, len(byDelivery))
	for _, event := range byDelivery {
		merged = append(merged, event)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].UpdatedAt.After(merged[j].UpdatedAt)
	})
	return limitActivity(merged, limit)
}

func (s *Server) activityJobs(ctx context.Context, limit int) []activityRecord {
	records := s.activity.recentJobs(limit)
	if s.jobLister == nil {
		return records
	}

	jobs, err := s.listRunnerJobs(ctx)
	if err != nil {
		log.Printf("failed to list Kubernetes runner jobs for activity jobs: %v", err)
		return records
	}

	byDelivery := make(map[string]activityRecord, len(records)+len(jobs.Items))
	for _, record := range records {
		byDelivery[record.DeliveryID] = record
	}
	for _, job := range jobs.Items {
		record, ok := activityRecordFromJob(&job)
		if !ok {
			continue
		}
		if existing, exists := byDelivery[record.DeliveryID]; !exists || record.UpdatedAt.After(existing.UpdatedAt) {
			byDelivery[record.DeliveryID] = mergeActivityRecord(existing, record)
		}
	}

	merged := make([]activityRecord, 0, len(byDelivery))
	for _, record := range byDelivery {
		if strings.TrimSpace(record.JobName) != "" {
			merged = append(merged, record)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].UpdatedAt.After(merged[j].UpdatedAt)
	})
	return limitActivity(merged, limit)
}

func (s *Server) listRunnerJobs(ctx context.Context) (*batchv1.JobList, error) {
	selector := labels.Set{"app.kubernetes.io/name": "nova-sre-runner"}.String()
	return s.jobLister.List(ctx, metav1.ListOptions{LabelSelector: selector})
}

func activityRecordFromJob(job *batchv1.Job) (activityRecord, bool) {
	if job == nil || strings.TrimSpace(job.Annotations["nova-sre.io/delivery-id"]) == "" {
		return activityRecord{}, false
	}

	status, reason, message := jobStatus(job)
	receivedAt := parseAnnotationTime(job.Annotations["nova-sre.io/received-at"])
	if receivedAt.IsZero() {
		receivedAt = job.CreationTimestamp.Time
	}
	updatedAt := jobObservedTime(job)
	if updatedAt.IsZero() {
		updatedAt = receivedAt
	}
	return activityRecord{
		DeliveryID: job.Annotations["nova-sre.io/delivery-id"],
		Event:      job.Annotations["nova-sre.io/event"],
		Repository: job.Annotations["nova-sre.io/repository"],
		SHA:        job.Annotations["nova-sre.io/commit-sha"],
		Status:     status,
		Namespace:  job.Namespace,
		JobName:    job.Name,
		Reason:     reason,
		Message:    message,
		ReceivedAt: receivedAt.UTC(),
		UpdatedAt:  updatedAt.UTC(),
	}, true
}

func mergeActivityRecord(memory activityRecord, durable activityRecord) activityRecord {
	if strings.TrimSpace(memory.DeliveryID) == "" {
		return durable
	}
	memory.Event = firstNonEmpty(durable.Event, memory.Event)
	memory.Repository = firstNonEmpty(durable.Repository, memory.Repository)
	memory.SHA = firstNonEmpty(durable.SHA, memory.SHA)
	memory.Status = firstNonEmpty(durable.Status, memory.Status)
	memory.Namespace = firstNonEmpty(durable.Namespace, memory.Namespace)
	memory.JobName = firstNonEmpty(durable.JobName, memory.JobName)
	memory.Reason = firstNonEmpty(durable.Reason, memory.Reason)
	memory.Message = firstNonEmpty(durable.Message, memory.Message)
	if memory.ReceivedAt.IsZero() || (!durable.ReceivedAt.IsZero() && durable.ReceivedAt.Before(memory.ReceivedAt)) {
		memory.ReceivedAt = durable.ReceivedAt
	}
	if durable.UpdatedAt.After(memory.UpdatedAt) {
		memory.UpdatedAt = durable.UpdatedAt
	}
	return memory
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

func limitActivity[T any](items []T, limit int) []T {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
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

func (c *deliveryCache) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ttl > 0 {
		c.ttl = ttl
	}
}

func (c *deliveryCache) TTL() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ttl
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
