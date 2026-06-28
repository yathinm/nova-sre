# Local Control Panel Demo

This workflow runs the Go API, the React control panel, and a temporary public
tunnel so GitHub can deliver webhooks to your machine.

## Prerequisites

- Go, Node.js, npm, and either `ngrok` or `cloudflared`.
- A GitHub repository where you can create a webhook and open a harmless test PR.
- A random webhook secret exported as `GITHUB_WEBHOOK_SECRET`.
- Optional: Minikube profile `nova-sre` plus the Kubernetes manifests if you want
  webhook deliveries to create cluster Jobs.

Temporary tunnel URLs are public and usually change every time the tunnel restarts.
Do not paste long-lived tokens, OpenAI keys, or real secret values into screenshots,
logs, PRs, or issues. Remove or disable the GitHub webhook when the demo is done.

## 1. Run the Go API

For a local process:

```sh
export GITHUB_WEBHOOK_SECRET="$(openssl rand -hex 32)"
make run-server
```

The API listens on `http://localhost:8080` and exposes:

- `GET /healthz`
- `GET /metrics`
- `GET /api/config`
- `GET /api/events`
- `GET /api/jobs`
- `POST /webhook`

If the process can load Kubernetes configuration, the runner creates Kubernetes
Jobs in the configured namespace. If it cannot, the server still accepts supported
GitHub events and the control panel shows recent in-memory activity for the
current server process.

For the Minikube deployment path, create `nova-sre-secrets`, deploy the app, and
port-forward the services instead:

```sh
make local-up
```

This starts managed API and frontend port-forwards in the background, validates
the local stack, and leaves the control panel at `http://localhost:8081`. Stop
those managed forwards later with `make local-down`.

## 2. Run the React Control Panel

For local frontend development, run Vite in another terminal:

```sh
make run-frontend
```

Open `http://localhost:5173`. The panel defaults to `http://localhost:8080`.
Change `frontend/public/config.js` or use the API base input if your API is on a
different URL.

For the Minikube deployment path, `make local-up` already forwards the frontend
service. To run the frontend forward manually instead:

```sh
make port-forward-frontend
```

Open `http://localhost:8081`. Keep `make port-forward-server` running because the
frontend deployment points the browser at `http://localhost:8080`.

The panel polls health, metrics, recent webhook events, and recent runner jobs.
Rows may disappear when the Go process restarts because the current activity
lists are process-local memory.

If `NOVA_SRE_API_TOKEN` is configured on the Go server, enter the same value in
the control panel's API token field before refreshing. The token is stored in
browser session storage for the current demo tab only, and any legacy local
storage token is cleared. Do not include token values in screenshots, logs, PRs,
or issues.

## 3. Start a Temporary Webhook Tunnel

Use one of these in a separate terminal:

```sh
ngrok http 8080
```

```sh
cloudflared tunnel --url http://localhost:8080
```

Copy the HTTPS forwarding URL and append `/webhook`, for example:

```text
https://example-tunnel.ngrok-free.app/webhook
```

## 4. Update the GitHub Webhook URL

In GitHub, open the repository settings, then **Webhooks**.

- Payload URL: the tunnel HTTPS URL plus `/webhook`.
- Content type: `application/json`.
- Secret: the exact value of `GITHUB_WEBHOOK_SECRET`.
- Events: select **Pull requests** for this demo. **Ping** is also supported.
- Active: enabled only while the local tunnel is running.

GitHub signs each delivery with `X-Hub-Signature-256`. If the secret differs from
the server environment, Nova-SRE returns `401`.

Before redelivering from GitHub, validate the public tunnel:

```sh
WEBHOOK_BASE_URL=https://example-tunnel.trycloudflare.com make validate-webhook-tunnel
```

That checks `/healthz` and confirms `/webhook` is reachable. To also verify the
signature path, include the same secret used by the running server:

```sh
WEBHOOK_BASE_URL=https://example-tunnel.trycloudflare.com \
  GITHUB_WEBHOOK_SECRET="$GITHUB_WEBHOOK_SECRET" \
  make validate-webhook-tunnel
```

The signed check sends a supported `ping` delivery, so use it only when you want
Nova-SRE to exercise the webhook enqueue path.

To verify the signed webhook path locally before involving GitHub, keep
`make port-forward-server` running and run:

```sh
make validate-local-webhook
```

The command uses `GITHUB_WEBHOOK_SECRET` when set, otherwise it reads the
`GITHUB_WEBHOOK_SECRET` key from the local `nova-sre-secrets` Kubernetes secret
without printing it.
It verifies that invalid signatures are rejected, signed `ping` deliveries are
accepted, duplicate delivery IDs are surfaced as duplicates, and `/api/summary`
reflects the new webhook activity.

## Webhook Delivery Troubleshooting

In GitHub delivery history, `failed to connect to host` means GitHub could not
reach the tunnel host at all. For quick tunnels, this usually means the tunnel
process stopped or the webhook still points at an older tunnel URL. Start a fresh
tunnel, update the Payload URL, and verify the public route before redelivering:

```sh
cloudflared tunnel --url http://localhost:8080
WEBHOOK_BASE_URL=https://example-tunnel.trycloudflare.com make validate-webhook-tunnel
```

The `/webhook` GET check should return `405 Method Not Allowed` with
`Allow: POST`; that proves the public URL reaches the Nova-SRE server. If GitHub
then reports `401`, the tunnel is reachable but the webhook Secret does not match
`GITHUB_WEBHOOK_SECRET` in the running server or Kubernetes secret.

## 5. Create a Harmless Test PR

Create a short-lived branch in the same repository, make a tiny non-sensitive
change such as editing a scratch note, and open a pull request. Avoid changes that
trigger production deploys or publish credentials.

After GitHub sends the `pull_request` webhook:

```sh
curl -fsS http://localhost:8080/api/events
curl -fsS http://localhost:8080/api/jobs
curl -fsS http://localhost:8080/api/config
```

The control panel should show the delivery. With Kubernetes runner configuration,
`/api/jobs` reflects the queued runner activity and the cluster should also show a
Job. Without cluster-backed runner configuration, the endpoint shows the in-memory
job record captured when the webhook was accepted.
Failed, cancelled, rejected, and diagnosis-error runner jobs are also promoted into
the Latest Issue panel so operators do not have to scan the full recent jobs table
first.
When a failed PR diagnosis posts or updates a GitHub comment, the Recent Jobs
detail column links directly to that comment and shows the create/update action.

When finished, close the tunnel and disable or delete the temporary GitHub webhook.

## Verified Real Webhook Path

The real webhook path was validated with a temporary `cloudflared` tunnel and a
temporary GitHub webhook pointed at `/webhook`. The GitHub `ping` delivery
returned `202`, Nova-SRE created a Kubernetes runner Job, and `/api/events`
showed the completed `ping` activity. Use this sequence for future validation:

```sh
make port-forward-server
cloudflared tunnel --url http://localhost:8080
```

Create a temporary GitHub webhook with the tunnel URL plus `/webhook`, content
type `application/json`, the same `GITHUB_WEBHOOK_SECRET` used by the server, and
only the events needed for the test. Confirm the delivery in GitHub and in
Nova-SRE:

```sh
curl -fsS http://localhost:8080/api/events
kubectl get jobs -n nova-sre
```

Delete the temporary GitHub webhook and stop the tunnel immediately after the
validation run.
