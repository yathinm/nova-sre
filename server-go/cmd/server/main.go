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
	activity := newActivityStore(defaultActivityLimit)
	jobRunner := runner.NewJobRunner(jobConfig, nil, log.Default())
	jobRunner.Observer = activity

	if clientset, configSource, err := runner.KubernetesClientsetFromEnv(os.Getenv); err != nil {
		log.Printf("Kubernetes Job creation disabled: %v", err)
	} else {
		jobs := clientset.BatchV1().Jobs(jobConfig.Namespace)
		jobRunner.Creator = runner.KubernetesJobCreator{Jobs: jobs}
		log.Printf("Kubernetes Job creation enabled using %s config", configSource)
		if agent, err := runner.NewHTTPAgentClient(os.Getenv("NOVA_SRE_AGENT_URL"), durationFromEnv("NOVA_SRE_AGENT_TIMEOUT", 10*time.Second)); err != nil {
			log.Printf("agent client disabled: %v", err)
		} else if agent != nil {
			jobRunner.Watcher = runner.KubernetesJobWatcher{
				Jobs:         jobs,
				PollInterval: durationFromEnv("RUNNER_JOB_POLL_INTERVAL", 2*time.Second),
			}
			jobRunner.LogCollector = runner.KubernetesJobLogCollector{
				Pods: clientset.CoreV1().Pods(jobConfig.Namespace),
			}
			jobRunner.Agent = agent
			jobRunner.CallbackTimeout = durationFromEnv("RUNNER_JOB_CALLBACK_TIMEOUT", 5*time.Minute)
		}
	}
	server := NewServerWithRunnerAndActivity(os.Getenv("GITHUB_WEBHOOK_SECRET"), jobRunner, activity)
	if creator, ok := jobRunner.Creator.(runner.KubernetesJobCreator); ok {
		server.SetKubernetesJobLister(creator.Jobs)
	}

	log.Println("nova-sre server starting on :8080")
	if err := http.ListenAndServe(":8080", server); err != nil {
		log.Fatalf("server error: %v", err)
	}
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
