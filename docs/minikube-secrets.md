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
```

An example manifest is available at `k8s/examples/nova-sre-secret.example.yaml`
for local experimentation. Keep real secret values out of git.
