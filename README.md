# Nova-SRE

Nova-SRE is a Kubernetes-native CI/CD orchestration engine with AI-powered SRE diagnostics.

The only supported local runtime is **Minikube**. Local development, Terraform provider
configuration, Docker image builds, and Kubernetes deployment steps all target the
`nova-sre` Minikube profile. No other local Kubernetes runtime is supported.

The project is currently in the local Minikube integration phase. The Go server,
Python agent, Kubernetes manifests, Terraform provider wiring, and Terraform-managed
Prometheus/Grafana releases are present. The local work now is validating the full
webhook-to-job-to-agent path and making observability useful as real pipeline data
flows through it.

## Core Flow

```text
GitHub webhook -> Go orchestrator -> Kubernetes Job -> logs and metrics -> LangGraph agent -> GitHub PR comment
```

## Main Components

- **Minikube** local Kubernetes runtime
- **Terraform** Kubernetes and Helm provider wiring for the Minikube context
- **Go** webhook runner
- **Kubernetes Job** executor
- **React** frontend control panel
- **Prometheus and Grafana** telemetry
- **Python LangGraph** diagnostic agent
- **Kubernetes Secrets / External Secrets**

The checked-in React app is the frontend control panel for local webhook and
runner activity. Grafana remains the stats frontend for Prometheus dashboards;
run `make port-forward-grafana` after Terraform installs the Helm release and
open `http://localhost:3000`. See
[docs/control-panel-demo.md](docs/control-panel-demo.md) for the end-to-end local
demo workflow.

## Local Prerequisites

- Docker
- Minikube
- kubectl
- Terraform >= 1.6
- Go, for server tests and local development
- Python with the agent tooling installed, for agent tests and local development
- Node.js and npm, for the React control panel
- A public tunnel tool, such as ngrok or cloudflared, when testing GitHub webhooks

## Local Setup: GitHub Webhook to Minikube

The Makefile is the source of truth for local commands. It uses the Minikube profile
`nova-sre`, which also becomes the Kubernetes context consumed by Terraform.

1. Start the local cluster:

   ```sh
   make cluster-create
   ```

2. Confirm kubectl is pointed at Minikube:

   ```sh
   make cluster-info
   ```

3. Enable the standard local addons:

   ```sh
   make addons
   ```

   Enable ingress separately only when you are testing manifests that require it:

   ```sh
   make addons-ingress
   ```

4. Initialize and apply Terraform against the Minikube context:

   ```sh
   make tf-init
   make tf-plan
   make tf-apply
   ```

   The Terraform provider configuration targets the `nova-sre` kube context. The
   current Terraform tree installs the local observability namespace plus Prometheus
   and Grafana Helm releases. If those resources already exist in Minikube but this
   checkout does not have Terraform state, run `make tf-import-observability` before
   `make tf-plan`.

5. Build application images inside Minikube's Docker daemon:

   ```sh
   make docker-build
   ```

   This tags the server, agent, and frontend images as `nova-sre-server:local`,
   `nova-sre-agent:local`, and `nova-sre-frontend:local` inside the Minikube
   profile, where Kubernetes can pull them without a registry push.

6. Deploy the Kubernetes app surface:

   ```sh
   make deploy-apps
   ```

   This applies `k8s/rbac/` and `k8s/base/`, including the `nova-sre` namespace,
   the Go server, the Python agent, the frontend control panel, and the service
   account/RBAC needed for the Go server to create Jobs and read pod logs. The
   target restarts the app deployments after apply so pods pick up freshly built
   local `:local` images. Local runner Jobs are retained for 15 minutes after
   completion so you have time to inspect logs without letting completed Jobs
   accumulate indefinitely. The app Deployments set conservative resource requests
   and limits for Minikube, and generated runner Jobs default to `100m` CPU /
   `128Mi` memory requests with `500m` CPU / `256Mi` memory limits. The server
   keeps the latest 200 activity records in memory and deduplicates GitHub
   deliveries for 15 minutes by default.

7. Expose the Go server locally after its Kubernetes Service exists:

   ```sh
   make port-forward-server
   ```

   The expected local endpoint is `http://localhost:8080`. The current server exposes
   `GET /healthz`, `GET /metrics`, `GET /api/config`, `GET /api/events`,
   `GET /api/jobs`, and `POST /webhook`.

   For a local process outside Kubernetes, run:

   ```sh
   make run-server
   ```

   When the server can load Kubernetes configuration, webhook deliveries create
   cluster Jobs. Without Kubernetes configuration, the API still accepts requests
   and records recent activity in memory for the control panel.

8. Expose the frontend control panel locally after its Kubernetes Service exists:

   ```sh
   make port-forward-frontend
   ```

   Open `http://localhost:8081`. The frontend deployment sets
   `NOVA_SRE_API_BASE=http://localhost:8080`, so keep `make port-forward-server`
   running in another terminal while using the control panel.

9. Validate the Prometheus metrics endpoint:

   ```sh
   make validate-metrics
   ```

   The target expects the server port-forward above to be running. It curls
   `http://localhost:8080/metrics` and checks for default Go runtime metrics or
   Nova-SRE pipeline metrics.

10. Open the local observability services after Terraform has installed them.
   Use separate terminals for these long-running port-forwards:

   ```sh
   make port-forward-prometheus
   make port-forward-grafana
   ```

   Prometheus is available at `http://localhost:9090`. Grafana, the local stats
   frontend, is available at `http://localhost:3000`. See
   [docs/observability.md](docs/observability.md) for install, port-forward, and
   scrape validation details.

11. For local frontend development outside Kubernetes, run the React control panel
   in another terminal:

   ```sh
   make run-frontend
   ```

   Open `http://localhost:5173`. The panel reads the API from
   `http://localhost:8080` unless `frontend/public/config.js`, `localStorage`, or
   the API base input points it somewhere else.

12. Create a public tunnel to the local server port and configure the GitHub webhook:

   ```sh
   ngrok http 8080
   ```

   Or, with cloudflared:

   ```sh
   cloudflared tunnel --url http://localhost:8080
   ```

   In GitHub, set the webhook payload URL to the tunnel URL plus `/webhook`. Use
   the same webhook secret in GitHub and `GITHUB_WEBHOOK_SECRET`.

13. Follow the event through the local pipeline:

   - GitHub sends the webhook to the public tunnel.
   - The tunnel forwards to the port-forwarded Go server in Minikube.
   - The Go orchestrator creates a Kubernetes Job in Minikube.
   - The job emits logs and metrics for collection.
   - The LangGraph agent diagnoses failures.
   - The GitHub integration posts the result back to the pull request.

## Makefile Workflow

| Target | What it does |
|--------|--------------|
| `make cluster-create` | Starts Minikube with Docker driver and profile `nova-sre`. |
| `make cluster-delete` | Deletes the `nova-sre` Minikube profile. |
| `make cluster-info` | Switches kubectl to the `nova-sre` context and prints cluster details. |
| `make addons` | Enables Minikube dashboard and metrics-server addons. |
| `make addons-ingress` | Enables the Minikube ingress addon. |
| `make dashboard` | Opens the Minikube dashboard. |
| `make tf-init` | Runs `terraform init` in `terraform/`. |
| `make tf-plan` | Runs `terraform plan` in `terraform/`. |
| `make tf-apply` | Runs `terraform apply` in `terraform/`. |
| `make tf-destroy` | Runs `terraform destroy` in `terraform/`. |
| `make tf-import-observability` | Imports existing local observability resources into Terraform state. |
| `make docker-env` | Prints the command that points Docker at Minikube's daemon. |
| `make docker-build` | Builds server, agent, and frontend images into Minikube's Docker daemon. |
| `make deploy-apps` | Applies `k8s/rbac/` and `k8s/base/`, then restarts and waits for local app deployments. |
| `make port-forward-server` | Forwards `svc/nova-sre-server` in namespace `nova-sre` to `localhost:8080`. |
| `make port-forward-agent` | Forwards `svc/nova-sre-agent` in namespace `nova-sre` to `localhost:8000`. |
| `make port-forward-frontend` | Forwards `svc/nova-sre-frontend` in namespace `nova-sre` to `localhost:8081`. |
| `make port-forward-prometheus` | Forwards `svc/prometheus-server` in namespace `observability` to `localhost:9090`. |
| `make port-forward-grafana` | Forwards `svc/grafana` in namespace `observability` to `localhost:3000`. |
| `make validate-metrics` | Curls `http://localhost:8080/metrics` and checks for Prometheus metrics. |
| `make run-server` | Runs the Go API locally on `localhost:8080`. |
| `make run-frontend` | Runs the React control panel locally on `localhost:5173`. |
| `make test-go` | Runs Go tests under `server-go/`. |
| `make lint-go` | Runs `go vet ./...` under `server-go/`. |
| `make test-agent` | Runs `python -m pytest` under `agent-python/`. |
| `make lint-agent` | Runs `ruff check .` under `agent-python/`. |
| `make all-local` | Runs `cluster-create`, `addons`, `tf-init`, `tf-apply`, `docker-build`, and `deploy-apps`. |

`make all-local` does not enable ingress, open dashboards, start port-forwards,
validate `/metrics`, run the React control panel, create a public webhook tunnel,
or configure GitHub. Run those steps explicitly when needed.

## Branch Structure

| Branch | Purpose |
|--------|---------|
| `main` | Stable working releases |
| `dev` | Active development |
| `feature/*` | Feature branches (for example, `feature/go-webhook`, `feature/k8s-runner`, `feature/langgraph-agent`) |
