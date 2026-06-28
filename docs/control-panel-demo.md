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
- `GET /api/events`
- `GET /api/jobs`
- `POST /webhook`

If the process can load Kubernetes configuration, the runner creates Kubernetes
Jobs in the configured namespace. If it cannot, the server still accepts supported
GitHub events and the control panel shows recent in-memory activity for the
current server process.

For the Minikube deployment path, create `nova-sre-secrets`, deploy the app, and
port-forward the service instead:

```sh
make docker-build
make deploy-apps
make port-forward-server
```

## 2. Run the React Control Panel

For local frontend development, run Vite in another terminal:

```sh
make run-frontend
```

Open `http://localhost:5173`. The panel defaults to `http://localhost:8080`.
Change `frontend/public/config.js` or use the API base input if your API is on a
different URL.

For the Minikube deployment path, use the frontend service instead:

```sh
make port-forward-frontend
```

Open `http://localhost:8081`. Keep `make port-forward-server` running because the
frontend deployment points the browser at `http://localhost:8080`.

The panel polls health, metrics, recent webhook events, and recent runner jobs.
Rows may disappear when the Go process restarts because the current activity
lists are process-local memory.

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

## 5. Create a Harmless Test PR

Create a short-lived branch in the same repository, make a tiny non-sensitive
change such as editing a scratch note, and open a pull request. Avoid changes that
trigger production deploys or publish credentials.

After GitHub sends the `pull_request` webhook:

```sh
curl -fsS http://localhost:8080/api/events
curl -fsS http://localhost:8080/api/jobs
```

The control panel should show the delivery. With Kubernetes runner configuration,
`/api/jobs` reflects the queued runner activity and the cluster should also show a
Job. Without cluster-backed runner configuration, the endpoint shows the in-memory
job record captured when the webhook was accepted.

When finished, close the tunnel and disable or delete the temporary GitHub webhook.
