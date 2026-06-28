# Control Panel Phase

Nova-SRE now has Kubernetes wiring for a separate local control panel alongside
the Go server, Python agent, and Grafana observability stack.

The control panel is intended to be the operator-facing web UI for pipeline
activity, diagnosis status, and links back to GitHub pull requests. Grafana
remains the metrics and dashboard frontend for Prometheus data; the control panel
is the product UI for day-to-day pipeline triage.

## Local Image

Build the control panel image into Minikube's Docker daemon with the rest of the
application images:

```sh
make docker-build
```

The Makefile expects a frontend build context at `./frontend` and tags the image
as `nova-sre-frontend:local`. The Kubernetes deployment uses `imagePullPolicy:
Never`, matching the server and agent, so the image must exist inside the
`nova-sre` Minikube profile before the pod can start.

## Kubernetes Surface

The base manifests include:

- `k8s/base/frontend-deployment.yaml`
- `k8s/base/frontend-service.yaml`

The deployment runs one `nova-sre-frontend` pod in the `nova-sre` namespace and
serves nginx on container port `80`. The service is a ClusterIP named
`nova-sre-frontend` on port `3000`.

The deployment provides the local browser API URL through `NOVA_SRE_API_BASE`:

```text
http://localhost:8080
```

Because the control panel runs in your browser through a port-forward, the API
base points at the server port-forward instead of Kubernetes service DNS.

## Local Access

After building images and applying the base manifests:

```sh
make deploy-apps
make port-forward-frontend
```

Open the control panel at `http://localhost:3001`.

Run the frontend port-forward in a separate terminal, the same way as the server,
agent, Prometheus, and Grafana forwards.
