PROFILE=nova-sre
PYTHON ?= python3.11
KUBECTL_VALIDATE ?= true
GOVULNCHECK_VERSION ?= v1.5.0

.PHONY: cluster-create cluster-delete cluster-info addons addons-ingress dashboard \
        tf-init tf-validate tf-plan tf-apply tf-destroy tf-import-observability docker-env docker-build docker-build-ci deploy-apps \
        port-forward-server port-forward-agent port-forward-frontend port-forward-prometheus port-forward-grafana \
        validate-metrics validate-api-cors validate-k8s validate-scripts validate-secrets validate-cluster-runtime validate-observability-config validate-local-runtime validate-local-webhook validate-webhook-tunnel run-server run-frontend all-local \
        test-go lint-go audit-go test-agent lint-agent audit-frontend audit-deps

# ── Cluster lifecycle ──────────────────────────────────────────────────────────

cluster-create: ## Start Minikube cluster
	minikube start --driver=docker --profile $(PROFILE)

cluster-delete: ## Delete Minikube cluster
	minikube delete --profile $(PROFILE)

cluster-info: ## Switch context and show cluster info
	kubectl config use-context $(PROFILE)
	kubectl cluster-info
	kubectl get nodes

# ── Addons ────────────────────────────────────────────────────────────────────

addons: ## Enable dashboard and metrics-server addons
	minikube addons enable dashboard --profile $(PROFILE)
	minikube addons enable metrics-server --profile $(PROFILE)

addons-ingress: ## Enable ingress addon
	minikube addons enable ingress --profile $(PROFILE)

dashboard: ## Open Minikube dashboard
	minikube dashboard --profile $(PROFILE)

# ── Terraform ─────────────────────────────────────────────────────────────────

tf-init: ## Initialise Terraform
	cd terraform && terraform init

tf-validate: ## Validate Terraform formatting and configuration
	cd terraform && terraform fmt -check
	cd terraform && terraform init -backend=false
	cd terraform && terraform validate

tf-plan: ## Preview Terraform changes
	cd terraform && terraform plan

tf-apply: ## Terraform apply
	cd terraform && terraform apply

tf-destroy: ## Terraform destroy
	cd terraform && terraform destroy

tf-import-observability: ## Import existing local observability resources into Terraform state
	cd terraform && terraform import kubernetes_namespace.observability observability
	cd terraform && terraform import helm_release.prometheus observability/prometheus
	cd terraform && terraform import helm_release.grafana observability/grafana
	cd terraform && terraform import kubernetes_config_map.grafana_pipeline_dashboard observability/grafana-dashboard-nova-sre-pipeline

# ── Docker (inside Minikube) ──────────────────────────────────────────────────

docker-env: ## Print eval command to point Docker CLI at Minikube's daemon
	eval $$(minikube docker-env --profile $(PROFILE))

docker-build: ## Build images directly inside Minikube's Docker daemon
	eval $$(minikube docker-env --profile $(PROFILE)) && docker build -t nova-sre-server:local ./server-go
	eval $$(minikube docker-env --profile $(PROFILE)) && docker build -t nova-sre-agent:local ./agent-python
	eval $$(minikube docker-env --profile $(PROFILE)) && docker build -t nova-sre-frontend:local ./frontend

docker-build-ci: ## Build images with the active Docker daemon for CI validation
	docker build -t nova-sre-server:ci ./server-go
	docker build -t nova-sre-agent:ci ./agent-python
	docker build -t nova-sre-frontend:ci ./frontend

# ── Kubernetes ────────────────────────────────────────────────────────────────

deploy-apps: ## Apply RBAC and base manifests
	kubectl apply -f k8s/rbac/
	kubectl apply -f k8s/base/
	kubectl rollout restart deployment/nova-sre-server deployment/nova-sre-agent deployment/nova-sre-frontend -n nova-sre
	kubectl rollout status deployment/nova-sre-server -n nova-sre --timeout=120s
	kubectl rollout status deployment/nova-sre-agent -n nova-sre --timeout=120s
	kubectl rollout status deployment/nova-sre-frontend -n nova-sre --timeout=120s

# ── Port-forwards ─────────────────────────────────────────────────────────────

port-forward-server: ## Forward Go server → localhost:8080
	kubectl port-forward -n nova-sre svc/nova-sre-server 8080:8080

port-forward-agent: ## Forward Python agent → localhost:8000
	kubectl port-forward -n nova-sre svc/nova-sre-agent 8000:8000

port-forward-frontend: ## Forward frontend control panel → localhost:8081
	kubectl port-forward -n nova-sre svc/nova-sre-frontend 8081:80

port-forward-prometheus: ## Forward Prometheus → localhost:9090
	kubectl port-forward -n observability svc/prometheus-server 9090:80

port-forward-grafana: ## Forward Grafana → localhost:3000
	kubectl port-forward -n observability svc/grafana 3000:80

validate-metrics: ## Verify the port-forwarded Go server exposes Prometheus metrics
	curl -fsS http://localhost:8080/metrics | grep -E 'go_gc_duration_seconds|pipeline_jobs_total'

validate-api-cors: ## Verify browser-readable Go API endpoints include CORS headers
	@for path in /healthz /metrics /api/config; do \
		echo "Checking $$path"; \
		curl -fsS -D - -o /dev/null -H 'Origin: http://localhost:8081' "http://localhost:8080$$path" | grep -i '^Access-Control-Allow-Origin:'; \
	done

validate-k8s: ## Validate Kubernetes app manifests client-side
ifeq ($(KUBECTL_VALIDATE),false)
	ruby scripts/validate-k8s-yaml.rb
else
	kubectl apply --dry-run=client --validate=true -f k8s/base -f k8s/rbac
endif

validate-scripts: ## Validate repository Ruby helper scripts
	ruby -c scripts/validate-k8s-yaml.rb
	ruby -c scripts/validate-cluster-runtime.rb
	ruby -c scripts/validate-local-runtime.rb
	ruby -c scripts/validate-local-webhook.rb
	ruby -c scripts/validate-observability-config.rb
	ruby -c scripts/validate-secrets.rb
	ruby -c scripts/validate-webhook-tunnel.rb

validate-cluster-runtime: ## Validate live Kubernetes deployments, services, and agent auth
	ruby scripts/validate-cluster-runtime.rb

validate-secrets: ## Check tracked files for real-looking committed secrets
	ruby scripts/validate-secrets.rb

validate-observability-config: ## Validate Prometheus, Grafana, and dashboard wiring
	ruby scripts/validate-observability-config.rb

validate-local-runtime: ## Validate local API and frontend port-forwards
	ruby scripts/validate-local-runtime.rb

validate-local-webhook: ## Send a signed local webhook ping and confirm activity
	ruby scripts/validate-local-webhook.rb

validate-webhook-tunnel: ## Validate a public webhook tunnel; set WEBHOOK_BASE_URL and optionally GITHUB_WEBHOOK_SECRET
	ruby scripts/validate-webhook-tunnel.rb

# ── Local control panel demo ──────────────────────────────────────────────────

run-server: ## Run the Go API locally on localhost:8080
	cd server-go && go run ./cmd/server

run-frontend: ## Run the React control panel locally on localhost:5173
	cd frontend && npm run dev

audit-frontend: ## Audit frontend dependencies for high severity vulnerabilities
	cd frontend && npm audit --audit-level=high

# ── Go server ─────────────────────────────────────────────────────────────────

test-go: ## Run Go unit tests
	cd server-go && go test ./...

lint-go: ## Lint Go source
	cd server-go && go vet ./...

audit-go: ## Audit Go dependencies and reachable symbols for known vulnerabilities
	cd server-go && go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# ── Python agent ──────────────────────────────────────────────────────────────

test-agent: ## Run Python agent tests
	cd agent-python && $(PYTHON) -m pytest

lint-agent: ## Lint Python agent source
	cd agent-python && $(PYTHON) -m ruff check .

# ── Composite ─────────────────────────────────────────────────────────────────

audit-deps: audit-go audit-frontend ## Audit Go and frontend dependencies

all-local: cluster-create addons tf-init tf-apply docker-build deploy-apps ## Full local setup from scratch
