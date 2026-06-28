# Project Status

Last updated: 2026-06-28

Nova-SRE is well into the local Minikube integration phase. The local developer
loop is usable end to end: GitHub webhooks can reach the Go orchestrator through
a temporary tunnel, the orchestrator creates Kubernetes runner Jobs, the Python
agent can diagnose failed runner activity, the React control panel shows live
activity, and Prometheus/Grafana wiring is present for pipeline metrics.

## Current Completion Estimate

The project is roughly 82-85% through the local MVP.

Completed work is concentrated in the local runtime, security hardening,
validation, observability foundation, local durability, and release
preparation. The remaining work is mostly product depth and productionization:
richer runner behavior, stronger end-to-end automation, production secret
management, and a more complete operator control surface.

## Working Now

- Go webhook server exposes `GET /healthz`, `GET /metrics`, `GET /api/config`,
  `GET /api/summary`, `GET /api/events`, `GET /api/jobs`, and `POST /webhook`.
- GitHub webhook signature validation, delivery deduplication, supported-event
  filtering, and bounded webhook body reads are implemented.
- Kubernetes runner Jobs are created from webhook deliveries and retained briefly
  for inspection with TTL cleanup.
- Runner Jobs receive normalized GitHub context environment variables for push,
  pull request, and workflow run events, so runner images do not need to reparse
  the full webhook payload for common fields.
- Runner Jobs can use event-specific command overrides for push, pull request,
  and workflow run events, falling back to the global runner command or the
  default diagnostic command.
- Runner status is normalized into `succeeded`, duplicate, error, and other
  dashboard-friendly buckets, including diagnosis delivery success or failure
  after failed runner Jobs.
- Python agent accepts runner diagnosis payloads, truncates oversized normalized
  logs, produces deterministic fallback diagnoses, and can upsert GitHub PR
  comments when metadata and token permissions are present.
- GitHub PR comments include a hidden Nova-SRE marker, update the existing
  Nova-SRE diagnosis comment by default, support explicit create mode, and
  return sanitized create/update/skip/failure status for control-panel display.
- Go runner diagnosis requests can set GitHub PR comment mode through
  `NOVA_SRE_GITHUB_COMMENT_MODE`, and `/api/config` reports that non-secret
  mode for the control panel.
- Go runner log collection asks Kubernetes for a bounded number of pod log bytes
  before forwarding failed-job logs to the agent.
- Server-to-agent `/diagnose` calls can be protected with
  `NOVA_SRE_AGENT_TOKEN`; the live Minikube deployment has this token configured.
- React control panel shows API health, metrics, runtime config, pipeline
  totals, status breakdowns, recent events, recent jobs, activity persistence
  mode, and job-level failure details such as GitHub PR comment permission
  errors.
- Go server can persist bounded recent activity to an atomic JSON snapshot and
  reload it on startup; the local Kubernetes deployment mounts this path for
  server container restarts.
- A production-oriented Kubernetes overlay now provides starter TLS ingress and
  PVC-backed activity storage manifests, with validation for TLS, API auth/CORS
  secret wiring, production image replacements, and the activity PVC.
- A release checklist now covers preflight validation, image publishing,
  production overlay customization, rollout, smoke tests, monitoring, and
  rollback.
- Runtime config reports whether server-to-agent auth is enabled without exposing
  the shared token.
- Frontend nginx serves CSP, frame, referrer, permissions, and MIME hardening
  headers.
- Control-panel API tokens are kept in browser session storage, and legacy local
  storage tokens are cleared.
- Terraform installs local Prometheus and Grafana, including the Nova-SRE
  pipeline dashboard ConfigMap.
- Grafana admin password is generated instead of hardcoded.
- Go, Python, frontend, Kubernetes, Terraform, Docker image, script syntax,
  dependency audit, secret-scan, local runtime, cluster runtime, tunnel, and
  observability validation commands exist.

## Live Local Runtime

The current local runtime has been validated with:

```sh
make validate-local-runtime
make validate-cluster-runtime
PROMETHEUS_BASE_URL=http://localhost:9090 make validate-observability-config
```

Current expected local endpoints:

- Frontend control panel: `http://localhost:8081`
- Go API: `http://localhost:8080`
- Prometheus, when port-forwarded: `http://localhost:9090`
- Grafana, when port-forwarded: `http://localhost:3000`

## CI Coverage

CI currently gates:

- Go tests, `go vet`, and `govulncheck`.
- Python agent tests and `ruff`.
- Frontend smoke/security tests, dependency audit, and production build.
- Kubernetes manifest validation.
- Terraform validation.
- Docker image builds for server, agent, and frontend.
- Ruby helper syntax checks.
- Tracked-file secret scanning.

## Remaining MVP Work

- Promote the local file-backed activity store to production-grade persistence
  with persistent volumes or an external database.
- Expand runner execution from event-specific command overrides into
  repository-specific workflow profiles.
- Add stronger end-to-end CI that can exercise a fake webhook through a local
  Kubernetes test environment without relying on a developer laptop.
- Add richer GitHub PR comment controls in the control panel instead of only
  showing the server-level create/upsert mode.
- Customize and harden the production ingress/TLS overlay for the target domain,
  ingress controller, certificate issuer, image registry, and secret manager.
- Add environment-specific production secret-management automation for
  `NOVA_SRE_API_TOKEN`, `NOVA_SRE_ALLOWED_ORIGINS`, GitHub credentials, and
  agent auth.
- Connect image publishing and production image stamping into a single release
  command or workflow.

## Useful Verification Commands

```sh
make test-go
make lint-go
make audit-go
make test-agent
make lint-agent
make audit-frontend
make validate-scripts
make validate-secrets
make KUBECTL_VALIDATE=false validate-k8s
make validate-local-runtime
make validate-cluster-runtime
```

For webhook tunnel validation:

```sh
WEBHOOK_BASE_URL=https://example-tunnel.trycloudflare.com make validate-webhook-tunnel
```

For signed webhook tunnel validation:

```sh
WEBHOOK_BASE_URL=https://example-tunnel.trycloudflare.com \
  GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
  make validate-webhook-tunnel
```
