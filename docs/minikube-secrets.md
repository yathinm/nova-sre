# Minikube Secret Setup

Nova-SRE Kubernetes deployments expect a `nova-sre-secrets` secret in the
`nova-sre` namespace. Create it locally before running `make deploy-apps`.

```sh
kubectl apply -f k8s/base/namespace.yaml
make sync-k8s-secret
```

`make sync-k8s-secret` reads exported environment variables, builds the
Kubernetes Secret manifest in memory, and pipes it to `kubectl apply -f -`.
This avoids putting secret values in shell history or `kubectl` command
arguments. For a safe preview that prints only key names and value lengths, run:

```sh
NOVA_SRE_SECRET_DRY_RUN=true make sync-k8s-secret
```

For local testing, export these required values in your shell first:

```sh
export GITHUB_WEBHOOK_SECRET="$(openssl rand -hex 32)"
export GITHUB_TOKEN="replace-with-local-github-token"
export OPENAI_API_KEY="replace-with-local-openai-api-key"
```

Use a webhook secret that matches the value configured in GitHub. Use personal
or project-scoped tokens with the least privileges needed for the workflow being
tested. GitHub PR comment posting requires a token that can read and write
issue comments for the target repository; permission failures are returned as
sanitized diagnosis metadata and surfaced in recent job details.

`NOVA_SRE_API_TOKEN` is optional. When it is present, `/api/*` control-panel
endpoints require either an `Authorization: Bearer <token>` header or an
`X-Nova-SRE-API-Token` header. Leave it unset only for isolated local demos.
Export it before `make sync-k8s-secret` to include it in `nova-sre-secrets`.

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
NOVA_SRE_GITHUB_COMMENT_MODE=upsert
RUNNER_JOB_TTL_SECONDS=900
RUNNER_JOB_COMMAND_PUSH=<optional push command>
RUNNER_JOB_COMMAND_PULL_REQUEST=<optional pull request command>
RUNNER_JOB_COMMAND_WORKFLOW_RUN=<optional workflow run command>
RUNNER_JOB_COMMAND_REPOSITORY_OVERRIDES=<optional owner/repo=command entries>
NOVA_SRE_ACTIVITY_LIMIT=200
NOVA_SRE_ACTIVITY_STORE_PATH=/var/lib/nova-sre/activity.json
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

Runner command selection uses the event-specific command first, then a
repository-specific command, then the global `RUNNER_JOB_COMMAND`, then the
default diagnostic echo command. The supported event-specific variables are
`RUNNER_JOB_COMMAND_PUSH`, `RUNNER_JOB_COMMAND_PULL_REQUEST`, and
`RUNNER_JOB_COMMAND_WORKFLOW_RUN`. Repository overrides use
`RUNNER_JOB_COMMAND_REPOSITORY_OVERRIDES` with newline- or semicolon-separated
entries such as:

```sh
RUNNER_JOB_COMMAND_REPOSITORY_OVERRIDES='acme/widgets=/bin/runner --widgets;acme/api=/bin/runner --api'
```

The agent truncates normalized submitted logs to `NOVA_SRE_MAX_LOG_CHARS` before
classification and optional LLM prompting. The default keeps diagnosis payloads
bounded while preserving the beginning and end of oversized log streams.
`NOVA_SRE_GITHUB_COMMENT_MODE` accepts `upsert` or `create`. The default
`upsert` mode asks the agent to update the existing hidden-marker Nova-SRE PR
comment when present and create one otherwise. Use `create` only for workflows
that intentionally want a fresh PR comment for every failed diagnosis.
The Go server also asks Kubernetes for at most
`NOVA_SRE_RUNNER_LOG_LIMIT_BYTES` bytes per runner pod container before sending
logs to the agent, keeping raw log collection bounded at the source.

The Go server writes recent activity to `NOVA_SRE_ACTIVITY_STORE_PATH` when it
is set. The local Kubernetes deployment mounts `/var/lib/nova-sre` as an
`emptyDir`, so activity survives server container restarts within the same pod.
Use a persistent volume for non-local environments that need activity to
survive pod replacement.

An example manifest is available at `k8s/examples/nova-sre-secret.example.yaml`
for local experimentation. Keep real secret values out of git.
