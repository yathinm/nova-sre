package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yathinm/nova-sre/server-go/internal/runner"
)

const defaultActivityLimit = 100

type activityStore struct {
	mu      sync.Mutex
	limit   int
	records map[string]activityRecord
	order   []string
	now     func() time.Time
}

type activityRecord struct {
	DeliveryID string    `json:"delivery_id"`
	Event      string    `json:"event"`
	Repository string    `json:"repository,omitempty"`
	SHA        string    `json:"sha,omitempty"`
	Status     string    `json:"status"`
	Namespace  string    `json:"namespace,omitempty"`
	JobName    string    `json:"job_name,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type activitySummary struct {
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"by_status"`
	ByEvent   map[string]int `json:"by_event"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func newActivityStore(limit int) *activityStore {
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	return &activityStore{
		limit:   limit,
		records: make(map[string]activityRecord),
		now:     time.Now,
	}
}

func (s *activityStore) recordReceived(event githubEvent) {
	if s == nil {
		return
	}
	metadata := webhookMetadata(event.Body)
	now := s.timestamp()

	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[event.DeliveryID]
	if !exists {
		record = activityRecord{
			DeliveryID: event.DeliveryID,
			Event:      event.Event,
			ReceivedAt: now,
		}
		s.order = append(s.order, event.DeliveryID)
	}
	record.Event = firstNonEmpty(record.Event, event.Event)
	record.Repository = firstNonEmpty(record.Repository, metadata.Repository)
	record.SHA = firstNonEmpty(record.SHA, metadata.SHA)
	record.Status = "received"
	record.UpdatedAt = now
	s.records[event.DeliveryID] = record
	s.pruneLocked()
}

func (s *activityStore) recordAccepted(event githubEvent) {
	s.update(event.DeliveryID, func(record activityRecord) activityRecord {
		record.Event = firstNonEmpty(record.Event, event.Event)
		if record.Status == "" || record.Status == "received" {
			record.Status = "accepted"
		}
		return record
	})
}

func (s *activityStore) recordDuplicate(event githubEvent) {
	s.update(event.DeliveryID, func(record activityRecord) activityRecord {
		record.Event = firstNonEmpty(record.Event, event.Event)
		record.Status = "duplicate"
		return record
	})
}

func (s *activityStore) recordError(event githubEvent, message string) {
	s.update(event.DeliveryID, func(record activityRecord) activityRecord {
		record.Event = firstNonEmpty(record.Event, event.Event)
		record.Status = "error"
		record.Message = message
		return record
	})
}

func (s *activityStore) ObserveJob(update runner.JobStatusUpdate) {
	if s == nil {
		return
	}
	s.update(update.DeliveryID, func(record activityRecord) activityRecord {
		record.Event = firstNonEmpty(record.Event, update.Event)
		record.Repository = firstNonEmpty(update.Repository, record.Repository)
		record.SHA = firstNonEmpty(update.SHA, record.SHA)
		record.Status = normalizeActivityStatus(firstNonEmpty(update.Status, record.Status))
		record.Namespace = firstNonEmpty(update.Namespace, record.Namespace)
		record.JobName = firstNonEmpty(update.JobName, record.JobName)
		record.Reason = firstNonEmpty(update.Reason, record.Reason)
		record.Message = firstNonEmpty(update.Message, record.Message)
		return record
	})
}

func (s *activityStore) summary() activitySummary {
	if s == nil {
		return activitySummary{ByStatus: map[string]int{}, ByEvent: map[string]int{}}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	summary := activitySummary{
		ByStatus: map[string]int{},
		ByEvent:  map[string]int{},
	}
	for _, record := range s.records {
		summary.Total++
		summary.ByStatus[normalizeActivityStatus(firstNonEmpty(record.Status, "unknown"))]++
		summary.ByEvent[firstNonEmpty(record.Event, "unknown")]++
		if record.UpdatedAt.After(summary.UpdatedAt) {
			summary.UpdatedAt = record.UpdatedAt
		}
	}
	return summary
}

func summarizeActivityRecords(records []activityRecord) activitySummary {
	summary := activitySummary{
		ByStatus: map[string]int{},
		ByEvent:  map[string]int{},
	}
	for _, record := range records {
		summary.Total++
		summary.ByStatus[normalizeActivityStatus(firstNonEmpty(record.Status, "unknown"))]++
		summary.ByEvent[firstNonEmpty(record.Event, "unknown")]++
		if record.UpdatedAt.After(summary.UpdatedAt) {
			summary.UpdatedAt = record.UpdatedAt
		}
	}
	return summary
}

func (s *activityStore) recent(limit int) []activityRecord {
	if s == nil {
		return nil
	}
	if limit <= 0 || limit > s.limit {
		limit = s.limit
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	records := make([]activityRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records
}

func normalizeActivityStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "success", "complete", "completed":
		return "succeeded"
	case "":
		return "unknown"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func (s *activityStore) recentJobs(limit int) []activityRecord {
	records := s.recent(limit)
	jobs := make([]activityRecord, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.JobName) != "" {
			jobs = append(jobs, record)
		}
	}
	return jobs
}

func (s *activityStore) update(deliveryID string, mutate func(activityRecord) activityRecord) {
	if s == nil || strings.TrimSpace(deliveryID) == "" {
		return
	}
	now := s.timestamp()

	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[deliveryID]
	if !exists {
		record = activityRecord{
			DeliveryID: deliveryID,
			ReceivedAt: now,
		}
		s.order = append(s.order, deliveryID)
	}
	record = mutate(record)
	record.UpdatedAt = now
	s.records[deliveryID] = record
	s.pruneLocked()
}

func (s *activityStore) timestamp() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *activityStore) pruneLocked() {
	for len(s.order) > s.limit {
		deliveryID := s.order[0]
		s.order = s.order[1:]
		delete(s.records, deliveryID)
	}
}

type webhookPayloadMetadata struct {
	Repository string
	SHA        string
}

func webhookMetadata(body []byte) webhookPayloadMetadata {
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
		return webhookPayloadMetadata{}
	}
	return webhookPayloadMetadata{
		Repository: firstNonEmpty(payload.Repository.FullName, payload.PullRequest.Head.Repo.FullName, payload.WorkflowRun.Repository.FullName),
		SHA:        firstNonEmpty(payload.HeadCommit.ID, payload.After, payload.PullRequest.Head.SHA, payload.WorkflowRun.HeadSHA),
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
