PROFILE=nova-sre
PYTHON ?= python3

.PHONY: cluster-create cluster-delete cluster-info addons addons-ingress dashboard \
        tf-init tf-plan tf-apply tf-destroy tf-import-observability docker-env docker-build deploy-apps \
        port-forward-server port-forward-agent port-forward-frontend port-forward-prometheus port-forward-grafana \
        validate-metrics validate-api-cors run-server run-frontend all-local test-go lint-go test-agent lint-agent

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

# ── Local control panel demo ──────────────────────────────────────────────────

run-server: ## Run the Go API locally on localhost:8080
	cd server-go && go run ./cmd/server

run-frontend: ## Run the React control panel locally on localhost:5173
	cd frontend && npm run dev

# ── Go server ─────────────────────────────────────────────────────────────────

test-go: ## Run Go unit tests
	cd server-go && go test ./...

lint-go: ## Lint Go source
	cd server-go && go vet ./...

# ── Python agent ──────────────────────────────────────────────────────────────

test-agent: ## Run Python agent tests
	cd agent-python && $(PYTHON) -m pytest

lint-agent: ## Lint Python agent source
	cd agent-python && $(PYTHON) -m ruff check .

# ── Composite ─────────────────────────────────────────────────────────────────

all-local: cluster-create addons tf-init tf-apply docker-build deploy-apps ## Full local setup from scratch
