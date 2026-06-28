# Minikube Secret Setup

Nova-SRE Kubernetes deployments expect a `nova-sre-secrets` secret in the
`nova-sre` namespace. Create it locally before running `make deploy-apps`.

```sh
kubectl apply -f k8s/base/namespace.yaml
kubectl create secret generic nova-sre-secrets \
  --namespace nova-sre \
  --from-literal=GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
  --from-literal=GITHUB_TOKEN="$GITHUB_TOKEN" \
  --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY"
```

For local testing, export the values in your shell first. Use a webhook secret
that matches the value configured in GitHub. Use personal or project-scoped
tokens with the least privileges needed for the workflow being tested.

`NOVA_SRE_API_TOKEN` is optional. When it is present, `/api/*` control-panel
endpoints require either an `Authorization: Bearer <token>` header or an
`X-Nova-SRE-API-Token` header. Leave it unset only for isolated local demos.
Add it with `kubectl edit secret nova-sre-secrets -n nova-sre` or recreate the
secret with `--from-literal=NOVA_SRE_API_TOKEN="$NOVA_SRE_API_TOKEN"`.

The server deployment also sets:

```sh
NOVA_SRE_AGENT_URL=http://nova-sre-agent.nova-sre.svc.cluster.local:8000
RUNNER_JOB_TTL_SECONDS=900
NOVA_SRE_ACTIVITY_LIMIT=200
NOVA_SRE_DELIVERY_CACHE_TTL=15m
NOVA_SRE_READ_HEADER_TIMEOUT=5s
```

`NOVA_SRE_ALLOWED_ORIGINS` is optional. Leave it unset for isolated local demos,
where browser-readable endpoints return wildcard CORS headers. Set it to a
comma-separated list such as `http://localhost:8081,https://panel.example.com`
when you want the Go API to echo only approved frontend origins.

Generated runner Jobs use conservative default resources for local Minikube:
`RUNNER_JOB_CPU_REQUEST=100m`, `RUNNER_JOB_MEMORY_REQUEST=128Mi`,
`RUNNER_JOB_CPU_LIMIT=500m`, and `RUNNER_JOB_MEMORY_LIMIT=256Mi`. Add those
environment variables to `k8s/base/server-deployment.yaml` only when a local
test needs different runner sizing.

An example manifest is available at `k8s/examples/nova-sre-secret.example.yaml`
for local experimentation. Keep real secret values out of git.
