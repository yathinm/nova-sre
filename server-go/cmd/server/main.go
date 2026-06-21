package main

import (
	"log"
	"net/http"
	"os"

	"github.com/yathinm/nova-sre/server-go/internal/runner"
)

func main() {
	jobConfig := runner.JobConfigFromEnv(os.Getenv)
	jobCreator, configSource, err := runner.KubernetesJobCreatorFromEnv(jobConfig, os.Getenv)
	var creator runner.JobCreator
	if err != nil {
		log.Printf("Kubernetes Job creation disabled: %v", err)
	} else {
		creator = jobCreator
		log.Printf("Kubernetes Job creation enabled using %s config", configSource)
	}

	jobRunner := runner.NewJobRunner(jobConfig, creator, log.Default())
	server := NewServerWithRunner(os.Getenv("GITHUB_WEBHOOK_SECRET"), jobRunner)

	log.Println("nova-sre server starting on :8080")
	if err := http.ListenAndServe(":8080", server); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
