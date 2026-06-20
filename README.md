# Nova-SRE

Nova-SRE is a Kubernetes-native CI/CD orchestration engine with AI-powered SRE diagnostics.

## Core Flow

```
GitHub Webhook → Go Orchestrator → Kubernetes Job → Logs + Metrics → LangGraph Agent → GitHub PR Comment
```

## Main Components

- **Terraform** infrastructure
- **Go** webhook runner
- **Kubernetes Job** executor
- **Prometheus and Grafana** telemetry
- **Python LangGraph** diagnostic agent
- **Kubernetes Secrets / External Secrets**

## Branch Structure

| Branch | Purpose |
|--------|---------|
| `main` | Stable working releases |
| `dev` | Active development |
| `feature/*` | Feature branches (e.g. `feature/go-webhook`, `feature/k8s-runner`, `feature/langgraph-agent`) |
