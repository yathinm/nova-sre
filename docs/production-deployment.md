# Production Deployment

Nova-SRE's local `k8s/base` manifests are intentionally Minikube-focused. Use
`k8s/overlays/production` as the starting point for a non-local deployment with
Ingress, TLS, and persistent recent activity.

Use [release-checklist.md](release-checklist.md) when promoting a tested build
through a real release.

## Overlay

Render or apply the production overlay with:

```sh
kubectl kustomize k8s/overlays/production
make validate-production-k8s
kubectl apply -k k8s/overlays/production
```

Before applying it to a real cluster:

- Replace `nova-sre.example.com` in `k8s/overlays/production/ingress.yaml` with
  the production control-panel and webhook host.
- Set `cert-manager.io/cluster-issuer` to an issuer that exists in the target
  cluster, or remove the annotation and pre-create the `nova-sre-tls` secret.
- Confirm the ingress controller supports `ingressClassName: nginx`, or change
  it to the target cluster's ingress class.
- Replace the `images` entries in `k8s/overlays/production/kustomization.yaml`
  with the target registry and immutable release tag.
- Set `NOVA_SRE_ALLOWED_ORIGINS` in `nova-sre-secrets` to the final HTTPS
  frontend origin, for example `https://nova-sre.example.com`.
- Set `NOVA_SRE_API_TOKEN` for the control-panel API before exposing `/api/*`
  beyond an isolated local demo.
- Run `make sync-k8s-secret` from a trusted shell with the required environment
  variables exported, or use an external secret manager that creates the same
  `nova-sre-secrets` keys.

`make validate-production-k8s` renders the overlay and checks that TLS ingress,
production image replacements, the activity PVC, `NOVA_SRE_API_TOKEN`, and
`NOVA_SRE_ALLOWED_ORIGINS` wiring remain present.

The overlay routes:

- `/` to the frontend service.
- `/api`, `/healthz`, `/metrics`, and `/webhook` to the Go server service.

Configure the GitHub webhook payload URL as:

```text
https://nova-sre.example.com/webhook
```

Use the same `GITHUB_WEBHOOK_SECRET` in GitHub and in the Kubernetes
`nova-sre-secrets` secret.

## Activity Storage

The base deployment uses an `emptyDir` for `/var/lib/nova-sre`, which is enough
for local server container restarts. The production overlay replaces that volume
with the `nova-sre-activity-store` persistent volume claim so recent activity can
survive pod replacement.

The default PVC requests `1Gi` with `ReadWriteOnce`. Adjust the storage class,
access mode, or size for the target cluster before applying the overlay.

## Security Notes

- Keep `GITHUB_TOKEN`, `GITHUB_WEBHOOK_SECRET`, `OPENAI_API_KEY`,
  `NOVA_SRE_API_TOKEN`, and `NOVA_SRE_AGENT_TOKEN` in Kubernetes Secrets or an
  external secret manager.
- Prefer `make sync-k8s-secret` over `kubectl create secret --from-literal` for
  manual setup so secret values do not appear in shell history or process
  arguments.
- Use the least-privileged GitHub token that can read and write issue comments
  for repositories where Nova-SRE should post diagnoses.
- Keep TLS enabled for all public traffic.
- Restrict CORS to the production frontend origin.
