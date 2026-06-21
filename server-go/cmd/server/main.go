package main

import (
	"log"
	"net/http"
	"os"

	"github.com/yathinm/nova-sre/server-go/internal/runner"
)

func main() {
	jobRunner := runner.NewJobRunner(runner.JobConfigFromEnv(os.Getenv), nil, log.Default())
	server := NewServerWithRunner(os.Getenv("GITHUB_WEBHOOK_SECRET"), jobRunner)

	log.Println("nova-sre server starting on :8080")
	if err := http.ListenAndServe(":8080", server); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
