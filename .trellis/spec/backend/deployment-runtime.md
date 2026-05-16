# Deployment Runtime Guidelines

> Executable contracts for the official Docker Compose and All-in-One image runtime.

---

## Scenario: Official All-in-One Docker Deployment

### 1. Scope / Trigger

- Trigger: changing Docker Compose, All-in-One Dockerfile, Nginx templates, entrypoint behavior, deployment env examples, deployment workflows, or docs that describe production deployment.
- Applies to `docker-compose.yml`, `deploy/allinone/*`, `deploy/nginx/*`, `.env.deploy`, `backend/.env.production.example`, `README.md`, `docs/deployment.md`, `docs/env-vars.md`, `docs/maintainers/release.md`, Makefile deployment targets, and release/deploy workflows.

### 2. Signatures

- Compose command signature: `docker compose pull`, `docker compose up -d`, `docker compose logs ...`, and `docker compose -f docker-compose.yml config`.
- Official image signature: `linnea7171/xirang:${IMAGE_TAG:-latest}` in Compose and `docker.io/linnea7171/xirang:<tag>` in user-facing docs.
- Container port signature: host `10761` maps to container `10761`.
- Backend internal signature: `SERVER_ADDR=:3000` in the All-in-One image; Nginx proxies to `http://127.0.0.1:3000`.
- Healthcheck signature: `curl -fsS http://127.0.0.1:10761/healthz`.

### 3. Contracts

- `IMAGE_TAG` is the only Compose image-selection variable. Do not add registry, namespace, or image-name variables for the official deployment path.
- `docker-compose.yml` is the official production Compose file at repository root. Do not keep a second production Compose file or restore a development Compose deployment path.
- The All-in-One public entry is HTTP on `10761`. HTTPS/TLS termination is external to the project and belongs to Caddy, Nginx Proxy Manager, Nginx, a load balancer, or equivalent user-managed infrastructure.
- Required bind mounts:
  - `./data:/data` for SQLite and persistent application data.
  - `./backups:/backup` for database backups.
  - `./logs:/logs` for application and Nginx logs.
- `LOG_FILE` defaults to `/logs/xirang.log` in the All-in-One image. Nginx logs must also write under `/logs`.
- Compose must not configure Docker logging options for the official deployment path.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| `.env` is missing or required production secrets are blank | Container startup fails through existing production config/bootstrap validation; docs must instruct copying `.env.deploy` to `.env` and filling required secrets. |
| `docker-compose.yml` references `HTTP_PORT`, `HTTPS_PORT`, registry, namespace, or image-name variables | Reject the change; deployment path became over-variable. |
| Nginx template listens on `8080`, `8443`, or configures certificates/redirects | Reject the change; TLS and alternate entrypoints are outside the image contract. |
| `/logs` is not created, owned by the runtime user, or mounted in Compose | Reject the change; persistent log files are part of the deployment contract. |
| Deployment docs mention built-in HTTPS, certificate mounts, or `docker-compose.prod.yml` | Reject the change; docs no longer match runtime behavior. |

### 5. Good/Base/Bad Cases

- Good: `docker-compose.yml` uses `linnea7171/xirang:${IMAGE_TAG:-latest}`, maps `10761:10761`, mounts `./logs:/logs`, and docs tell users to terminate HTTPS in an external reverse proxy.
- Base: a release workflow or Makefile target updates tags for `docker.io/linnea7171/xirang` without changing the user deployment contract.
- Bad: adding `IMAGE_NAMESPACE`, `HTTP_PORT`, `HTTPS_PORT`, TLS certificate mounts, or a second `docker-compose.prod.yml` for compatibility.

### 6. Tests Required

- Run `docker compose -f docker-compose.yml config` with `.env.deploy` copied to `.env` in a temporary directory.
- Run `git diff --check`.
- Run `bash scripts/check-doc-freshness.sh`.
- Validate changed YAML files.
- Validate Nginx template syntax with a `/logs` directory mounted or otherwise present.
- Search active files for stale deployment references: `docker-compose.prod.yml`, `HTTP_PORT`, `HTTPS_PORT`, `IMAGE_REGISTRY`, `IMAGE_NAMESPACE`, certificate-template switching, and `8443` entrypoints. Exclude historical archives and changelog hashes when interpreting results.
- Run backend tests for changes that touch backend env examples, Go code, or packaging behavior; run frontend checks when build packaging or public UI docs are part of the release surface.

### 7. Wrong vs Correct

Wrong:

```yaml
services:
  xirang:
    image: ${IMAGE_REGISTRY:-docker.io}/${IMAGE_NAMESPACE:-linnea7171}/${IMAGE_NAME:-xirang}:${IMAGE_TAG:-latest}
    ports:
      - "${HTTP_PORT:-8080}:8080"
      - "${HTTPS_PORT:-8443}:8443"
    volumes:
      - ./certs:/etc/nginx/certs:ro
    logging:
      options:
        max-size: "10m"
```

Correct:

```yaml
services:
  xirang:
    image: linnea7171/xirang:${IMAGE_TAG:-latest}
    ports:
      - "10761:10761"
    volumes:
      - ./data:/data
      - ./backups:/backup
      - ./logs:/logs
```
