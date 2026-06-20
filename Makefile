.PHONY: help build-go build-agent test-go test-agent lint-go lint-agent tf-init tf-plan tf-apply

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ── Go server ─────────────────────────────────────────────────────────────────

build-go: ## Build the Go webhook server
	cd server-go && go build -o bin/server ./cmd/server

test-go: ## Run Go unit tests
	cd server-go && go test ./...

lint-go: ## Lint Go source
	cd server-go && go vet ./...

# ── Python agent ──────────────────────────────────────────────────────────────

build-agent: ## Build the Python agent Docker image
	docker build -t nova-sre-agent:latest ./agent-python

test-agent: ## Run Python agent tests
	cd agent-python && python -m pytest

lint-agent: ## Lint Python agent source
	cd agent-python && ruff check .

# ── Terraform ─────────────────────────────────────────────────────────────────

tf-init: ## Initialise Terraform
	cd terraform && terraform init

tf-plan: ## Terraform plan
	cd terraform && terraform plan

tf-apply: ## Terraform apply
	cd terraform && terraform apply
