# Minikube Observability

Nova-SRE's local observability stack is installed into the `nova-sre` Minikube
profile by Terraform. Prometheus and Grafana run in the `observability`
namespace, while the Go server exposes Prometheus-format application metrics at
`/metrics` on port `8080`.

Grafana is the local observability frontend, available at
`http://localhost:3000` after port-forwarding the Grafana service. The separate
Nova-SRE control panel is a product UI for pipeline triage and is documented in
[control-panel.md](control-panel.md).

## Current Phase

The project is in the local Minikube integration phase. The repository already
contains:

- Kubernetes manifests for the Go server, Python agent, namespace, service
  account, RBAC, and ClusterIP services.
- Terraform provider wiring for the `nova-sre` Minikube context.
- Terraform-managed Helm releases for Prometheus and Grafana.
- A Go server with `GET /healthz`, `GET /metrics`, and `POST /webhook`.
- Prometheus counters, gauges, and histograms for pipeline jobs.

The next local milestone is validating the deployed server, runner jobs, agent
traffic, Prometheus scrape target, and Grafana dashboard together with real
GitHub webhook traffic.

## Install Prometheus and Grafana

Start Minikube and point kubectl at the expected profile:

```sh
make cluster-create
make cluster-info
make addons
```

Install the Terraform-managed observability stack:

```sh
make tf-init
make tf-apply
```

Terraform creates the `observability` namespace and installs:

- Prometheus from the `prometheus-community/prometheus` Helm chart.
- Grafana from the `grafana/grafana` Helm chart.
- A Grafana dashboard ConfigMap for `dashboards/pipeline-stats.json`.

Confirm the pods and services exist:

```sh
kubectl get pods -n observability
kubectl get svc -n observability
```

The expected service names are:

- `prometheus-server`
- `grafana`

## Port-Forward the Stack

Use separate terminals for long-running port-forwards.

Prometheus:

```sh
make port-forward-prometheus
```

Open Prometheus at `http://localhost:9090`.

Grafana:

```sh
make port-forward-grafana
```

Open Grafana at `http://localhost:3000`.

The local Grafana Helm values set the admin password to `admin` in
`terraform/grafana-values.yaml`. The Nova-SRE Pipeline Stats dashboard is
provisioned automatically from `dashboards/pipeline-stats.json`.

## Validate the Server Metrics Endpoint

Deploy the app surface and start the server port-forward:

```sh
make docker-build
make deploy-apps
make port-forward-server
```

In another terminal, validate the metrics endpoint:

```sh
make validate-metrics
```

The target curls `http://localhost:8080/metrics` and checks for either default Go
runtime metrics or Nova-SRE pipeline metrics. A manual equivalent is:

```sh
curl -fsS http://localhost:8080/metrics | grep -E 'go_gc_duration_seconds|pipeline_jobs_total'
```

The first response may only include default Go/process metrics until runner code
has recorded pipeline activity. After pipeline jobs execute, useful Nova-SRE
series include:

- `pipeline_jobs_total`
- `pipeline_scheduling_latency_seconds`
- `pipeline_agent_mttd_seconds`
- `pipeline_active_jobs`

## Prometheus Scrape Check

After Prometheus is port-forwarded, open `http://localhost:9090/targets` and
confirm the `nova-sre-server` target is up. The local Prometheus Helm values
scrape `nova-sre-server.nova-sre.svc.cluster.local:8080` directly.
