# Grafana Dashboards

## Nova-SRE Pipeline Stats

`pipeline-stats.json` tracks the runner metrics exported by the Go server:

- `pipeline_jobs_total` by `status` and `repo`
- `pipeline_active_jobs` overall and by `repo`
- p50/p95 `pipeline_scheduling_latency_seconds`
- p50/p95 `pipeline_agent_mttd_seconds`

Terraform provisions this dashboard into Grafana through a labeled ConfigMap in
the `observability` namespace. You can also import the JSON manually and select
the Prometheus data source when prompted. The local Grafana service can be
reached with:

```sh
make port-forward-grafana
```

Then open `http://localhost:3000` and import the dashboard JSON.
