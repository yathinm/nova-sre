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
}

func NewServer(webhookSecret string) *Server {
	s := &Server{
		mux:           http.NewServeMux(),
		webhookSecret: webhookSecret,
		deliveries:    newDeliveryCache(15 * time.Minute),
		enqueueEvent:  enqueueGitHubEvent,
	}

	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.Handle("/metrics", promhttp.Handler())
	s.mux.HandleFunc("/webhook", s.handleWebhook)
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

	if alreadySeen := s.deliveries.Add(deliveryID, time.Now()); alreadySeen {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if err := s.enqueueEvent(r.Context(), githubEvent{
		DeliveryID: deliveryID,
		Event:      event,
		Body:       body,
	}); err != nil {
		log.Printf("failed to enqueue GitHub event delivery=%s event=%s: %v", deliveryID, event, err)
		http.Error(w, "failed to enqueue GitHub event", http.StatusInternalServerError)
		return
	}

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

func enqueueGitHubEvent(_ context.Context, event githubEvent) error {
	// TODO: Create a Kubernetes Job for supported GitHub webhook events.
	log.Printf("accepted GitHub event delivery=%s event=%s body_bytes=%d", event.DeliveryID, event.Event, len(event.Body))
	return nil
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
