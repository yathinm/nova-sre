PROFILE=nova-sre

.PHONY: cluster-create cluster-delete cluster-info addons addons-ingress dashboard \
        tf-init tf-apply tf-destroy docker-env docker-build deploy-apps \
        port-forward-server port-forward-agent port-forward-prometheus port-forward-grafana \
        all-local test-go lint-go test-agent lint-agent

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

tf-apply: ## Terraform apply
	cd terraform && terraform apply

tf-destroy: ## Terraform destroy
	cd terraform && terraform destroy

# ── Docker (inside Minikube) ──────────────────────────────────────────────────

docker-env: ## Print eval command to point Docker CLI at Minikube's daemon
	eval $$(minikube docker-env --profile $(PROFILE))

docker-build: ## Build images directly inside Minikube's Docker daemon
	eval $$(minikube docker-env --profile $(PROFILE)) && docker build -t nova-sre-server:local ./server-go
	eval $$(minikube docker-env --profile $(PROFILE)) && docker build -t nova-sre-agent:local ./agent-python

# ── Kubernetes ────────────────────────────────────────────────────────────────

deploy-apps: ## Apply RBAC and base manifests
	kubectl apply -f k8s/rbac/
	kubectl apply -f k8s/base/

# ── Port-forwards ─────────────────────────────────────────────────────────────

port-forward-server: ## Forward Go server → localhost:8080
	kubectl port-forward -n nova-sre svc/nova-sre-server 8080:8080

port-forward-agent: ## Forward Python agent → localhost:8000
	kubectl port-forward -n nova-sre svc/nova-sre-agent 8000:8000

port-forward-prometheus: ## Forward Prometheus → localhost:9090
	kubectl port-forward -n observability svc/prometheus-server 9090:80

port-forward-grafana: ## Forward Grafana → localhost:3000
	kubectl port-forward -n observability svc/grafana 3000:80

# ── Go server ─────────────────────────────────────────────────────────────────

test-go: ## Run Go unit tests
	cd server-go && go test ./...

lint-go: ## Lint Go source
	cd server-go && go vet ./...

# ── Python agent ──────────────────────────────────────────────────────────────

test-agent: ## Run Python agent tests
	cd agent-python && python -m pytest

lint-agent: ## Lint Python agent source
	cd agent-python && ruff check .

# ── Composite ─────────────────────────────────────────────────────────────────

all-local: cluster-create addons tf-init tf-apply docker-build deploy-apps ## Full local setup from scratch
