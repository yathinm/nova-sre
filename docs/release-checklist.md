# Release Checklist

Use this checklist when promoting Nova-SRE beyond the local Minikube workflow.
It assumes `dev` is the integration branch and `main` is the stable release
branch.

## 1. Preflight

Run the local validation suite from a clean `dev` checkout:

```sh
git status --short --branch
make test-go
make lint-go
make test-agent
make lint-agent
cd frontend && npm ci && npm test && npm run build
cd ..
make validate-k8s
make validate-production-k8s
make validate-scripts
make validate-secrets
make audit-deps
```

Confirm the latest GitHub Actions run on `dev` is green before cutting a release
candidate.

## 2. Image Publishing

The local manifests use `nova-sre-server:local`, `nova-sre-agent:local`, and
`nova-sre-frontend:local` for Minikube. A non-local release needs immutable
registry tags.

Build and push the three images with the release tag used by the target
environment:

```sh
docker build -t registry.example.com/nova-sre/server:RELEASE_TAG ./server-go
docker build -t registry.example.com/nova-sre/agent:RELEASE_TAG ./agent-python
docker build -t registry.example.com/nova-sre/frontend:RELEASE_TAG ./frontend
docker push registry.example.com/nova-sre/server:RELEASE_TAG
docker push registry.example.com/nova-sre/agent:RELEASE_TAG
docker push registry.example.com/nova-sre/frontend:RELEASE_TAG
```

Stamp the production overlay with the target registry and release tag:

```sh
PRODUCTION_IMAGE_REGISTRY=registry.example.com/nova-sre \
  RELEASE_TAG=RELEASE_TAG \
  make set-production-images
make validate-release-tools
```

## 3. Environment Configuration

Before applying the production overlay, confirm these values are set through
Kubernetes Secrets or an external secret manager:

- `GITHUB_WEBHOOK_SECRET`
- `GITHUB_TOKEN`
- `OPENAI_API_KEY`
- `NOVA_SRE_API_TOKEN`
- `NOVA_SRE_AGENT_TOKEN`
- `NOVA_SRE_ALLOWED_ORIGINS`

For manual Kubernetes Secret setup from exported environment variables, use:

```sh
NOVA_SRE_SECRET_DRY_RUN=true make sync-k8s-secret
make sync-k8s-secret
```

Also confirm the production overlay has the correct:

- Hostname in `k8s/overlays/production/ingress.yaml`
- TLS issuer or pre-created `nova-sre-tls` secret
- Ingress class
- PersistentVolumeClaim storage class and size
- Image registry and immutable image tags

## 4. Deploy

Render and validate the final manifests:

```sh
kubectl kustomize k8s/overlays/production
make validate-production-k8s
```

Apply the overlay:

```sh
kubectl apply -k k8s/overlays/production
kubectl rollout status deployment/nova-sre-server -n nova-sre --timeout=180s
kubectl rollout status deployment/nova-sre-agent -n nova-sre --timeout=180s
kubectl rollout status deployment/nova-sre-frontend -n nova-sre --timeout=180s
```

## 5. Smoke Test

After rollout:

```sh
curl -fsS https://nova-sre.example.com/healthz
curl -fsS https://nova-sre.example.com/api/config
curl -fsS https://nova-sre.example.com/
```

Check that `/api/config` reports:

- `api_auth_enabled: true`
- `agent_auth_enabled: true`
- `api_cors_restricted: true`
- `activity_store_enabled: true`
- `github_comment_mode: upsert` unless the environment intentionally uses
  create-only diagnosis comments

Send a signed GitHub `ping` or redeliver a safe test event, then confirm it
appears in the control panel and in `/api/events`.

## 6. Monitor

After release, watch:

- Server, agent, and frontend pod readiness
- Runner Job creation and TTL cleanup
- `/metrics` scrape health
- GitHub webhook delivery success rate
- GitHub PR comment create/update failures
- Activity PVC capacity

## 7. Rollback

Rollback by applying the previous production image tags or reverting the release
commit and reapplying the overlay:

```sh
kubectl rollout undo deployment/nova-sre-server -n nova-sre
kubectl rollout undo deployment/nova-sre-agent -n nova-sre
kubectl rollout undo deployment/nova-sre-frontend -n nova-sre
```

After rollback, rerun the smoke checks and inspect recent activity for failed
diagnosis or comment-delivery statuses.
