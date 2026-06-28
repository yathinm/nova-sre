# Project Status

Last updated: 2026-06-28

Nova-SRE is well into the local Minikube integration phase. The local developer
loop is usable end to end: GitHub webhooks can reach the Go orchestrator through
a temporary tunnel, the orchestrator creates Kubernetes runner Jobs, the Python
agent can diagnose failed runner activity, the React control panel shows live
activity, and Prometheus/Grafana wiring is present for pipeline metrics.

## Current Completion Estimate

The project is roughly 70% through the local MVP.

Completed work is concentrated in the local runtime, security hardening,
validation, and observability foundation. The remaining work is mostly product
depth and productionization: richer runner behavior, durable storage, production
ingress/TLS, broader GitHub event handling, and stronger end-to-end automation.

## Working Now

- Go webhook server exposes `GET /healthz`, `GET /metrics`, `GET /api/config`,
  `GET /api/summary`, `GET /api/events`, `GET /api/jobs`, and `POST /webhook`.
- GitHub webhook signature validation, delivery deduplication, supported-event
  filtering, and bounded webhook body reads are implemented.
- Kubernetes runner Jobs are created from webhook deliveries and retained briefly
  for inspection with TTL cleanup.
- Runner status is normalized into `succeeded`, duplicate, error, and other
  dashboard-friendly buckets.
- Python agent accepts runner diagnosis payloads, truncates oversized normalized
  logs, produces deterministic fallback diagnoses, and can post GitHub PR
  comments when metadata and token permissions are present.
- Go runner log collection asks Kubernetes for a bounded number of pod log bytes
  before forwarding failed-job logs to the agent.
- Server-to-agent `/diagnose` calls can be protected with
  `NOVA_SRE_AGENT_TOKEN`; the live Minikube deployment has this token configured.
- React control panel shows API health, metrics, runtime config, pipeline
  totals, status breakdowns, recent events, and recent jobs.
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

- Add a durable activity store so events/jobs survive Go server restarts.
- Expand runner behavior beyond the current local job execution path.
- Add stronger end-to-end CI that can exercise a fake webhook through a local
  Kubernetes test environment without relying on a developer laptop.
- Turn GitHub PR comment behavior into a fuller workflow with clearer posting
  controls, update-vs-create behavior, and permission failure reporting in the
  control panel.
- Add production-ready ingress, TLS, domain configuration, and environment
  overlays instead of relying on port-forwards and temporary tunnels.
- Decide how API auth and CORS should be configured outside isolated local demos.
- Add release/deployment documentation for non-local environments.

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
