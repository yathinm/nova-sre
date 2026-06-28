# Minikube Secret Setup

Nova-SRE Kubernetes deployments expect a `nova-sre-secrets` secret in the
`nova-sre` namespace. Create it locally before running `make deploy-apps`.

```sh
kubectl apply -f k8s/base/namespace.yaml
kubectl create secret generic nova-sre-secrets \
  --namespace nova-sre \
  --from-literal=GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
  --from-literal=GITHUB_TOKEN="$GITHUB_TOKEN" \
  --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" \
  --from-literal=NOVA_SRE_AGENT_TOKEN="$NOVA_SRE_AGENT_TOKEN" \
  --from-literal=NOVA_SRE_ALLOWED_ORIGINS=http://localhost:8081
```

For local testing, export the values in your shell first. Use a webhook secret
that matches the value configured in GitHub. Use personal or project-scoped
tokens with the least privileges needed for the workflow being tested. GitHub
PR comment posting requires a token that can read and write issue comments for
the target repository; permission failures are returned as sanitized diagnosis
metadata and surfaced in recent job details.

`NOVA_SRE_API_TOKEN` is optional. When it is present, `/api/*` control-panel
endpoints require either an `Authorization: Bearer <token>` header or an
`X-Nova-SRE-API-Token` header. Leave it unset only for isolated local demos.
Add it with `kubectl edit secret nova-sre-secrets -n nova-sre` or recreate the
secret with `--from-literal=NOVA_SRE_API_TOKEN="$NOVA_SRE_API_TOKEN"`.

`NOVA_SRE_AGENT_TOKEN` is optional but recommended. When present, the Go server
sends it to the Python agent on `/diagnose` requests and the agent rejects
requests without the matching `X-Nova-SRE-Agent-Token` header. Generate a local
value with a cryptographically secure random source such as:

```sh
openssl rand -hex 32
```

The server deployment also sets:

```sh
NOVA_SRE_AGENT_URL=http://nova-sre-agent.nova-sre.svc.cluster.local:8000
NOVA_SRE_AGENT_TOKEN=<from nova-sre-secrets when configured>
RUNNER_JOB_TTL_SECONDS=900
NOVA_SRE_ACTIVITY_LIMIT=200
NOVA_SRE_DELIVERY_CACHE_TTL=15m
NOVA_SRE_RUNNER_LOG_LIMIT_BYTES=65536
NOVA_SRE_READ_HEADER_TIMEOUT=5s
NOVA_SRE_READ_TIMEOUT=15s
NOVA_SRE_WRITE_TIMEOUT=30s
NOVA_SRE_IDLE_TIMEOUT=60s
NOVA_SRE_MAX_LOG_CHARS=20000
```

`NOVA_SRE_ALLOWED_ORIGINS` is optional. Leave it unset for isolated local demos,
where browser-readable endpoints return wildcard CORS headers. Set it to a
comma-separated list such as `http://localhost:8081,https://panel.example.com`
when you want the Go API to echo only approved frontend origins.
The Kubernetes server deployment reads this value from the optional
`NOVA_SRE_ALLOWED_ORIGINS` key in `nova-sre-secrets`.

Generated runner Jobs use conservative default resources for local Minikube:
`RUNNER_JOB_CPU_REQUEST=100m`, `RUNNER_JOB_MEMORY_REQUEST=128Mi`,
`RUNNER_JOB_CPU_LIMIT=500m`, and `RUNNER_JOB_MEMORY_LIMIT=256Mi`. Add those
environment variables to `k8s/base/server-deployment.yaml` only when a local
test needs different runner sizing.

The agent truncates normalized submitted logs to `NOVA_SRE_MAX_LOG_CHARS` before
classification and optional LLM prompting. The default keeps diagnosis payloads
bounded while preserving the beginning and end of oversized log streams.
When `github_comment_mode` is omitted, diagnosis requests use `upsert` mode:
the agent updates the existing hidden-marker Nova-SRE PR comment when present
and creates one otherwise. Set `github_comment_mode=create` on a diagnosis
request only when a fresh comment is explicitly desired.
The Go server also asks Kubernetes for at most
`NOVA_SRE_RUNNER_LOG_LIMIT_BYTES` bytes per runner pod container before sending
logs to the agent, keeping raw log collection bounded at the source.

An example manifest is available at `k8s/examples/nova-sre-secret.example.yaml`
for local experimentation. Keep real secret values out of git.
