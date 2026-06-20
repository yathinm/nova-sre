package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	server := NewServer(os.Getenv("GITHUB_WEBHOOK_SECRET"))

	log.Println("nova-sre server starting on :8080")
	if err := http.ListenAndServe(":8080", server); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
