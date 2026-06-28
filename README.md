# Nova-SRE

Nova-SRE is a Kubernetes-native SRE automation platform that monitors GitHub webhooks, runs CI checks as Kubernetes Jobs, diagnoses failures with an AI-powered agent, and posts actionable PR comments — all observable through a real-time control panel.

```
GitHub Webhook → Go Orchestrator → Kubernetes Job → LangGraph Agent → GitHub PR Comment
```

## Architecture

Nova-SRE has four main components that work together in a pipeline:

| Component | Stack | Purpose |
|-----------|-------|---------|
| **Server** | Go 1.25, Prometheus client, Kubernetes client-go | Receives webhooks, validates signatures, creates K8s Jobs, watches failures, calls the agent |
| **Agent** | Python 3.11+, FastAPI, LangGraph, optional OpenAI | Parses logs, classifies failures, generates diagnoses, posts PR comments |
| **Frontend** | React 19, TypeScript 6, Vite 8 | Real-time control panel for monitoring the full webhook → comment pipeline |
| **Infrastructure** | Minikube, Terraform, Prometheus, Grafana | Local Kubernetes cluster with observability stack |

### How It Works

1. **Webhook received** — GitHub sends a `push`, `pull_request`, or `workflow_run` event to the Go server at `/webhook`. The server validates the HMAC signature, deduplicates by delivery ID, and records the event.

2. **Runner Job created** — The server creates a Kubernetes Batch Job with the repository context (repo, SHA, PR metadata) injected as environment variables. The job image and command are configurable per event type and per repository.

3. **Failure detected** — The server watches the Job until completion. On failure, it collects pod logs (up to 64 KiB per container) and calls the Python agent at `/diagnose`.

4. **AI diagnosis** — The LangGraph pipeline parses and filters logs, classifies the failure type (test, lint, dependency, timeout, auth, deploy, runner, or runtime), summarizes the root cause, proposes a fix, and generates a markdown PR comment. An optional LLM path (OpenAI) enhances the diagnosis when enabled.

5. **PR comment posted** — The agent upserts (or creates) a diagnosis comment on the pull request via the GitHub API, including a fenced log excerpt and actionable next steps.

6. **Control panel** — The React dashboard at `localhost:8081` shows the full pipeline state: webhook acceptance, runner execution, agent diagnosis, and comment delivery — with live auto-refresh, status breakdowns, and operator guidance.

## Project Structure

```
nova-sre/
├── server-go/              # Go webhook server and Kubernetes runner
│   ├── cmd/server/          # Entry point, HTTP routes, activity store
│   └── internal/runner/     # Job creation, agent client, metrics
├── agent-python/            # Python diagnosis agent
│   ├── app/
│   │   ├── main.py          # FastAPI app with /diagnose endpoint
│   │   ├── graph/           # LangGraph diagnosis pipeline
│   │   ├── models/          # Pydantic schemas and log normalization
│   │   └── clients/         # GitHub PR comment client
│   └── tests/
├── frontend/                # React control panel
│   ├── src/main.tsx         # Full dashboard application
│   └── src/styles.css       # Dashboard styles
├── k8s/
│   ├── base/                # Local Minikube manifests
│   ├── rbac/                # ServiceAccount and Role for server
│   ├── overlays/production/ # Production overlay (ingress, TLS, PVC)
│   └── examples/            # Example secret manifest
├── terraform/               # Prometheus and Grafana via Helm
├── dashboards/              # Grafana pipeline dashboard JSON
├── scripts/                 # Ruby helpers for local dev and validation
└── docs/                    # Operational documentation
```

## Prerequisites

- [Docker](https://www.docker.com/)
- [Minikube](https://minikube.sigs.k8s.io/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Terraform](https://www.terraform.io/) >= 1.6
- [Go](https://go.dev/) 1.25
- [Python](https://www.python.org/) >= 3.11
- [Node.js](https://nodejs.org/) >= 20 and npm
- A tunnel tool ([ngrok](https://ngrok.com/) or [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)) for GitHub webhook testing

## Quick Start

Check that all tools are available:

```sh
make local-doctor
```

### Full Setup (from scratch)

```sh
make cluster-create          # Start Minikube with profile "nova-sre"
make addons                  # Enable dashboard and metrics-server
make tf-init && make tf-apply  # Install Prometheus and Grafana
make local-up                # Build images, deploy apps, start port-forwards, validate
```

Open the control panel at **http://localhost:8081**.

### One-Command Rebuild

After the cluster exists, rebuild and redeploy everything:

```sh
make local-up
```

Skip build/deploy when only restarting port-forwards:

```sh
NOVA_SRE_LOCAL_SKIP_BUILD=true NOVA_SRE_LOCAL_SKIP_DEPLOY=true make local-up
```

Stop managed port-forwards:

```sh
make local-down
```

## Local Services

| Service | URL | Make target |
|---------|-----|-------------|
| Go API | http://localhost:8080 | `make port-forward-server` |
| Control Panel | http://localhost:8081 | `make port-forward-frontend` |
| Python Agent | http://localhost:8000 | `make port-forward-agent` |
| Prometheus | http://localhost:9090 | `make port-forward-prometheus` |
| Grafana | http://localhost:3000 | `make port-forward-grafana` |

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/webhook` | GitHub webhook receiver (signature-validated) |
| `GET` | `/healthz` | Health check |
| `GET` | `/metrics` | Prometheus metrics |
| `GET` | `/api/config` | Runtime configuration |
| `GET` | `/api/summary` | Activity summary (counts by status/event) |
| `GET` | `/api/events` | Recent webhook deliveries |
| `GET` | `/api/jobs` | Recent runner job observations |

## Configuration

### Kubernetes Secrets

Create a secret named `nova-sre-secrets` in the `nova-sre` namespace:

| Key | Required | Description |
|-----|----------|-------------|
| `GITHUB_WEBHOOK_SECRET` | Yes | HMAC secret for webhook signature validation |
| `GITHUB_TOKEN` | Yes | GitHub API token for PR comments |
| `NOVA_SRE_API_TOKEN` | No | Bearer token for control panel API auth |
| `NOVA_SRE_ALLOWED_ORIGINS` | No | Comma-separated CORS allowed origins |
| `NOVA_SRE_AGENT_TOKEN` | No | Token for server → agent authentication |
| `OPENAI_API_KEY` | No | Required only when LLM diagnosis is enabled |

See `k8s/examples/nova-sre-secret.example.yaml` for a template, or sync from environment variables:

```sh
make sync-k8s-secret
```

### Server Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NOVA_SRE_AGENT_URL` | — | Agent endpoint (e.g. `http://nova-sre-agent:8000`) |
| `NOVA_SRE_AGENT_TIMEOUT` | `10s` | Agent HTTP client timeout |
| `NOVA_SRE_ACTIVITY_LIMIT` | `200` | Max activity records kept |
| `NOVA_SRE_ACTIVITY_STORE_PATH` | — | Path for persistent activity JSON |
| `NOVA_SRE_DELIVERY_CACHE_TTL` | `15m` | Webhook deduplication window |
| `NOVA_SRE_GITHUB_COMMENT_MODE` | `upsert` | `upsert` keeps one comment current; `create` posts a new one each time |
| `NOVA_SRE_RUNNER_LOG_LIMIT_BYTES` | `65536` | Max log bytes collected per pod |

### Runner Job Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `RUNNER_JOB_IMAGE` | — | Container image for runner jobs |
| `RUNNER_JOB_COMMAND` | — | Default command for all events |
| `RUNNER_JOB_COMMAND_PUSH` | — | Override command for push events |
| `RUNNER_JOB_COMMAND_PULL_REQUEST` | — | Override command for PR events |
| `RUNNER_JOB_COMMAND_WORKFLOW_RUN` | — | Override command for workflow_run events |
| `RUNNER_JOB_COMMAND_REPOSITORY_OVERRIDES` | — | Per-repo commands (`owner/repo=command`, newline-separated) |
| `RUNNER_JOB_TTL_SECONDS` | `900` | Finished job cleanup TTL |
| `RUNNER_JOB_NAMESPACE` | `nova-sre` | Namespace for runner jobs |
| `RUNNER_JOB_MEMORY_REQUEST` | `128Mi` | Memory request |
| `RUNNER_JOB_MEMORY_LIMIT` | `256Mi` | Memory limit |
| `RUNNER_JOB_CPU_REQUEST` | `100m` | CPU request |
| `RUNNER_JOB_CPU_LIMIT` | `500m` | CPU limit |

### Agent Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NOVA_SRE_AGENT_TOKEN` | — | Required bearer token for `/diagnose` |
| `GITHUB_TOKEN` | — | GitHub API token for posting PR comments |
| `NOVA_SRE_ENABLE_LLM` | `false` | Enable LLM-enhanced diagnosis (`1`, `true`, or `yes`) |
| `NOVA_SRE_LLM_MODEL` | `gpt-4o-mini` | OpenAI model for LLM diagnosis |
| `NOVA_SRE_MAX_LOG_CHARS` | — | Max log characters for diagnosis input |

## Testing a Webhook

1. Start the local stack:

```sh
make local-up
```

2. Create a public tunnel to the Go server:

```sh
ngrok http 8080
```

3. Configure a GitHub webhook pointing to `https://<tunnel-url>/webhook` with your webhook secret, sending `pull_request` events.

4. Open or update a PR. Watch the pipeline in the control panel at http://localhost:8081.

5. Validate the tunnel before redelivering:

```sh
WEBHOOK_BASE_URL=https://<tunnel-url> \
  GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
  make validate-webhook-tunnel
```

## Testing

```sh
make test-go       # Go unit tests
make lint-go       # go vet
make test-agent    # Python pytest
make lint-agent    # ruff check
make audit-deps    # govulncheck + npm audit
```

## CI

GitHub Actions runs on PRs and pushes to `main`/`dev` with path-filtered jobs:

| Job | Trigger paths | Checks |
|-----|---------------|--------|
| Go tests | `server-go/**` | `go test`, `go vet`, `govulncheck` |
| Python agent tests | `agent-python/**` | `pytest`, `ruff check` |
| Frontend build | `frontend/**` | `npm audit`, smoke tests, `vite build` |
| K8s manifests | `k8s/**` | Manifest validation, production overlay checks |
| Terraform | `terraform/**`, `dashboards/**` | `terraform validate`, observability config checks |
| Docker builds | Server/agent/frontend changes | Build all three images |
| Script validation | `scripts/**` | Syntax checks, release tool tests |
| Secret scan | All changes | Scan tracked files for committed secrets |

## Observability

Terraform provisions Prometheus and Grafana into an `observability` namespace via Helm.

```sh
make port-forward-prometheus   # http://localhost:9090
make port-forward-grafana      # http://localhost:3000
```

The Go server exposes `pipeline_*` Prometheus metrics (webhook counts, job durations, diagnosis outcomes). A pre-built Grafana dashboard is provisioned from `dashboards/pipeline-stats.json`.

See [docs/observability.md](docs/observability.md) for details.

## Documentation

| Document | Description |
|----------|-------------|
| [docs/control-panel-demo.md](docs/control-panel-demo.md) | End-to-end local demo walkthrough |
| [docs/minikube-secrets.md](docs/minikube-secrets.md) | Secret setup for Minikube |
| [docs/observability.md](docs/observability.md) | Prometheus and Grafana setup |
| [docs/production-deployment.md](docs/production-deployment.md) | Production deployment guide (ingress, TLS, persistence) |
| [docs/project-status.md](docs/project-status.md) | Current completion status and roadmap |
| [docs/release-checklist.md](docs/release-checklist.md) | Release promotion steps |

## Makefile Reference

<details>
<summary>All available targets</summary>

### Cluster Lifecycle
| Target | Description |
|--------|-------------|
| `cluster-create` | Start Minikube with Docker driver |
| `cluster-delete` | Delete the Minikube cluster |
| `cluster-info` | Switch context and print cluster info |
| `addons` | Enable dashboard and metrics-server |
| `addons-ingress` | Enable ingress addon |
| `dashboard` | Open the Minikube dashboard |

### Terraform
| Target | Description |
|--------|-------------|
| `tf-init` | Initialize Terraform |
| `tf-validate` | Check formatting and validate config |
| `tf-plan` | Preview changes |
| `tf-apply` | Apply changes |
| `tf-destroy` | Destroy resources |
| `tf-import-observability` | Import existing observability resources |

### Build and Deploy
| Target | Description |
|--------|-------------|
| `docker-env` | Print Docker-to-Minikube eval command |
| `docker-build` | Build images inside Minikube's Docker |
| `docker-build-ci` | Build images with active Docker (CI) |
| `deploy-apps` | Apply RBAC and base manifests, restart deployments |

### Local Stack
| Target | Description |
|--------|-------------|
| `local-doctor` | Check local tool dependencies |
| `local-up` | Build, deploy, port-forward, validate |
| `local-down` | Stop managed port-forwards |
| `local-status` | Show port-forward status |
| `run-server` | Run Go API locally (localhost:8080) |
| `run-frontend` | Run React dev server (localhost:5173) |

### Port Forwards
| Target | Description |
|--------|-------------|
| `port-forward-server` | Go server → localhost:8080 |
| `port-forward-agent` | Python agent → localhost:8000 |
| `port-forward-frontend` | Control panel → localhost:8081 |
| `port-forward-prometheus` | Prometheus → localhost:9090 |
| `port-forward-grafana` | Grafana → localhost:3000 |

### Validation
| Target | Description |
|--------|-------------|
| `validate-metrics` | Check Prometheus metrics endpoint |
| `validate-api-cors` | Check CORS headers |
| `validate-k8s` | Validate K8s manifests |
| `validate-production-k8s` | Validate production overlay |
| `validate-cluster-runtime` | Check live deployments and services |
| `validate-local-runtime` | Check local API and frontend |
| `validate-local-webhook` | Send signed local webhook ping |
| `validate-webhook-tunnel` | Validate public tunnel |
| `validate-secrets` | Scan for committed secrets |
| `validate-scripts` | Check Ruby script syntax |
| `validate-observability-config` | Check Prometheus/Grafana config |
| `validate-secret-sync` | Test secret sync helper |
| `validate-release-tools` | Test release helper scripts |
| `validate-release-command` | Test release preflight flow |

### Tests and Audits
| Target | Description |
|--------|-------------|
| `test-go` | Go unit tests |
| `lint-go` | Go vet |
| `test-agent` | Python agent tests |
| `lint-agent` | Ruff check |
| `audit-go` | govulncheck |
| `audit-frontend` | npm audit (high severity) |
| `audit-deps` | All dependency audits |

### Release
| Target | Description |
|--------|-------------|
| `set-production-images` | Stamp production overlay images |
| `prepare-production-release` | Full release preflight |
| `sync-k8s-secret` | Apply secrets from env vars |

### Full Setup
| Target | Description |
|--------|-------------|
| `all-local` | Everything from cluster creation through deploy |

</details>

## Branch Structure

| Branch | Purpose |
|--------|---------|
| `main` | Stable releases |
| `dev` | Active development |
