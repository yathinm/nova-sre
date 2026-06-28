package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yathinm/nova-sre/server-go/internal/runner"
)

func main() {
	jobConfig := runner.JobConfigFromEnv(os.Getenv)
	activityLimit := intFromEnv("NOVA_SRE_ACTIVITY_LIMIT", defaultActivityLimit)
	activityStorePath := strings.TrimSpace(os.Getenv("NOVA_SRE_ACTIVITY_STORE_PATH"))
	deliveryCacheTTL := durationFromEnv("NOVA_SRE_DELIVERY_CACHE_TTL", 15*time.Minute)
	activity, err := newPersistentActivityStore(activityLimit, activityStorePath)
	if err != nil {
		log.Printf("activity store started empty after load failure path=%s: %v", activityStorePath, err)
	}
	jobRunner := runner.NewJobRunner(jobConfig, nil, log.Default())
	jobRunner.Observer = activity

	if clientset, configSource, err := runner.KubernetesClientsetFromEnv(os.Getenv); err != nil {
		log.Printf("Kubernetes Job creation disabled: %v", err)
	} else {
		jobs := clientset.BatchV1().Jobs(jobConfig.Namespace)
		jobRunner.Creator = runner.KubernetesJobCreator{Jobs: jobs}
		log.Printf("Kubernetes Job creation enabled using %s config", configSource)
		if agent, err := runner.NewHTTPAgentClient(os.Getenv("NOVA_SRE_AGENT_URL"), durationFromEnv("NOVA_SRE_AGENT_TIMEOUT", 10*time.Second), os.Getenv("NOVA_SRE_AGENT_TOKEN")); err != nil {
			log.Printf("agent client disabled: %v", err)
		} else if agent != nil {
			jobRunner.Watcher = runner.KubernetesJobWatcher{
				Jobs:         jobs,
				PollInterval: durationFromEnv("RUNNER_JOB_POLL_INTERVAL", 2*time.Second),
			}
			jobRunner.LogCollector = runner.KubernetesJobLogCollector{
				Pods:          clientset.CoreV1().Pods(jobConfig.Namespace),
				LogLimitBytes: int64FromEnv("NOVA_SRE_RUNNER_LOG_LIMIT_BYTES", runner.DefaultRunnerLogLimitBytes),
			}
			jobRunner.Agent = agent
			jobRunner.CallbackTimeout = durationFromEnv("RUNNER_JOB_CALLBACK_TIMEOUT", 5*time.Minute)
		}
	}
	server := NewServerWithRunnerAndActivity(os.Getenv("GITHUB_WEBHOOK_SECRET"), jobRunner, activity)
	server.SetRuntimeConfig(runtimeConfig{
		ActivityLimit:        activityLimit,
		ActivityStoreEnabled: activityStorePath != "",
		DeliveryCacheTTL:     deliveryCacheTTL.String(),
		AgentAuthEnabled:     strings.TrimSpace(os.Getenv("NOVA_SRE_AGENT_TOKEN")) != "",
		RunnerNamespace:      jobConfig.Namespace,
		RunnerImage:          jobConfig.Image,
		RunnerJobTTLSeconds:  int32Value(jobConfig.TTLSecondsFinished),
		GitHubCommentMode:    jobConfig.GitHubCommentMode,
	})
	server.SetDeliveryCacheTTL(deliveryCacheTTL)
	server.SetAPIToken(os.Getenv("NOVA_SRE_API_TOKEN"))
	server.SetAPIAllowedOrigins(os.Getenv("NOVA_SRE_ALLOWED_ORIGINS"))
	if creator, ok := jobRunner.Creator.(runner.KubernetesJobCreator); ok {
		server.SetKubernetesJobLister(creator.Jobs)
	}

	httpServer := httpServerFromEnv(server)

	log.Printf("nova-sre server starting on %s", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func httpServerFromEnv(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: durationFromEnv("NOVA_SRE_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       durationFromEnv("NOVA_SRE_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      durationFromEnv("NOVA_SRE_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       durationFromEnv("NOVA_SRE_IDLE_TIMEOUT", 60*time.Second),
	}
}

func int32Value(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func intFromEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func int64FromEnv(name string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(raw); err == nil {
		return parsed
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}
