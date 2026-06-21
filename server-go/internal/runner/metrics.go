package runner

import (
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type PipelineMetrics interface {
	JobStarted(repo string)
	JobFinished(metrics PipelineJobMetrics)
}

type PipelineJobMetrics struct {
	Status            string
	Repo              string
	SchedulingLatency time.Duration
	EventTime         time.Time
	FinishedAt        time.Time
	Active            bool
}

type noopPipelineMetrics struct{}

func (noopPipelineMetrics) JobStarted(string) {}

func (noopPipelineMetrics) JobFinished(PipelineJobMetrics) {}

type PrometheusPipelineMetrics struct {
	jobsTotal         *prometheus.CounterVec
	schedulingLatency prometheus.Histogram
	agentMTTD         prometheus.Histogram
	activeJobs        *prometheus.GaugeVec
}

func NewPrometheusPipelineMetrics(registry prometheus.Registerer) *PrometheusPipelineMetrics {
	if registry == nil {
		registry = prometheus.DefaultRegisterer
	}

	metrics := &PrometheusPipelineMetrics{
		jobsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pipeline_jobs_total",
			Help: "Total Nova-SRE runner jobs by status and repository.",
		}, []string{"status", "repo"}),
		schedulingLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pipeline_scheduling_latency_seconds",
			Help:    "Seconds from webhook receipt to Kubernetes Job creation.",
			Buckets: prometheus.DefBuckets,
		}),
		agentMTTD: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "pipeline_agent_mttd_seconds",
			Help:    "Seconds from source event time to agent detection of a completed job.",
			Buckets: prometheus.DefBuckets,
		}),
		activeJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pipeline_active_jobs",
			Help: "Currently active Nova-SRE runner jobs by repository.",
		}, []string{"repo"}),
	}

	registerCollector(registry, metrics.jobsTotal)
	registerCollector(registry, metrics.schedulingLatency)
	registerCollector(registry, metrics.agentMTTD)
	registerCollector(registry, metrics.activeJobs)
	return metrics
}

func (m *PrometheusPipelineMetrics) JobStarted(repo string) {
	if m == nil || m.activeJobs == nil {
		return
	}
	m.activeJobs.WithLabelValues(repoLabel(repo)).Inc()
}

func (m *PrometheusPipelineMetrics) JobFinished(metrics PipelineJobMetrics) {
	if m == nil {
		return
	}
	repo := repoLabel(metrics.Repo)
	status := firstNonEmpty(metrics.Status, "unknown")
	if m.jobsTotal != nil {
		m.jobsTotal.WithLabelValues(status, repo).Inc()
	}
	if m.schedulingLatency != nil && metrics.SchedulingLatency > 0 {
		m.schedulingLatency.Observe(metrics.SchedulingLatency.Seconds())
	}
	if m.agentMTTD != nil && !metrics.EventTime.IsZero() {
		finishedAt := metrics.FinishedAt
		if finishedAt.IsZero() {
			finishedAt = time.Now()
		}
		if duration := finishedAt.Sub(metrics.EventTime); duration >= 0 {
			m.agentMTTD.Observe(duration.Seconds())
		}
	}
	if m.activeJobs != nil && !metrics.Active {
		m.activeJobs.WithLabelValues(repo).Dec()
	}
}

var (
	defaultMetricsOnce sync.Once
	defaultMetrics     PipelineMetrics
)

func DefaultPipelineMetrics() PipelineMetrics {
	defaultMetricsOnce.Do(func() {
		defaultMetrics = NewPrometheusPipelineMetrics(nil)
	})
	return defaultMetrics
}

func registerCollector(registerer prometheus.Registerer, collector prometheus.Collector) {
	if err := registerer.Register(collector); err != nil {
		if alreadyRegistered, ok := err.(prometheus.AlreadyRegisteredError); ok {
			_ = alreadyRegistered.ExistingCollector
		}
	}
}

func durationSince(start time.Time, end time.Time) time.Duration {
	if start.IsZero() || end.IsZero() {
		return 0
	}
	duration := end.Sub(start)
	if duration < 0 {
		return 0
	}
	return duration
}

func firstTime(values ...string) time.Time {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func repoLabel(repo string) string {
	if repo = strings.TrimSpace(repo); repo != "" {
		return repo
	}
	return "unknown"
}
