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

---

## Scenario: Backup Asset Content Gateway And Cache Runtime

### 1. Scope / Trigger

- Trigger: changing the asset-content Nginx routes, Range/streaming proxy
  behavior, content-specific logs, effective origin forwarding, or the
  authenticated cache directory in the All-in-One image.
- Applies to `deploy/nginx/templates/default.conf.template`,
  `deploy/nginx/README.md`, `deploy/allinone/Dockerfile`, the Nginx checker, and
  its mutation self-test.

### 2. Signatures

- Exact gateway route:
  `location ~ "^/api/v1/asset-content/[0-9a-f]{32}$"`.
- Redacted shaped fallback:
  `location ~ "^/api/v1/asset-content(?:/|$)"`.
- Dedicated access format/file:
  `xirang_asset_content` and `/logs/nginx-asset-content.log`.
- Cache root: `/var/cache/xirang/asset-content`, owned by the runtime user and
  not declared as a volume.
- Cache root identity: pre-open `os.Lstat`, `os.OpenRoot`, `Root.Stat(".")`,
  final `os.Lstat`, and `os.SameFile` comparisons before lock or cleanup.
- Verification:
  `scripts/check-asset-content-nginx.sh` and
  `scripts/check-asset-content-nginx.test.sh`.

### 3. Contracts

- Only the exact 32-lowercase-hex route receives Range/If-Range forwarding,
  disabled proxy/request buffering, cache/temp-file/gzip suppression, and
  finite 75-second proxy/send ceilings. Application grant/lease/write
  deadlines remain shorter and authoritative.
- The shaped fallback exists only to keep malformed, trailing-slash, and
  unsupported-method requests on redacted logs and safe rejection handlers. It
  must not inherit the exact route's streaming, Range, buffering, or timeout
  directives.
- Both content locations precede generic `/api/v1/`, use the dedicated access
  format, and override unformattable Nginx error logging with
  `error_log /dev/null crit`.
- The content access format contains only request ID, status, response bytes,
  and upstream/request timing. URI, args, cookies, referrer, user agent,
  client identity, and request line are forbidden.
- Both content routes overwrite `X-Forwarded-Proto` with their actual `$scheme`;
  they never preserve a client-supplied forwarded proto. The exact route also
  preserves an explicit Host port through `$http_host`, appends the peer to
  `X-Forwarded-For` with `$proxy_add_x_forwarded_for`, and explicitly removes
  `X-Real-IP` with `proxy_set_header X-Real-IP ""`. The shaped fallback
  explicitly removes both `X-Forwarded-For` and `X-Real-IP`; merely omitting a
  `proxy_set_header` directive is insufficient because Nginx otherwise passes
  inbound request headers through. These headers are closed transport/origin
  evidence, not authorization.
- Generic API/WebSocket behavior, HTTP port `10761`, external TLS ownership,
  health route, SPA/image policy, and official image name remain unchanged.
- The cache root is dedicated and non-persistent, outside `/data`, `/backup`,
  and `/logs`. Do not bind it to a backup source or substitute ordinary disk
  temp storage when runtime containment checks disable it.
- Path validation and `EvalSymlinks` do not make a later name-based open safe.
  Capture the validated directory identity before mount/source checks, open it
  with `os.OpenRoot`, then require the opened root and final non-symlink path to
  be the same file before creating the process lock or deleting orphans. Any
  rename/symlink replacement disables disk cache without touching the
  replacement target.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Exact route contains a raw URI/cookie log variable | Checker fails; do not deploy. |
| Exact route enables buffering, gzip, cache, temp files, or unbounded timeout | Checker fails. |
| Shaped fallback gains Range/If-Range or exact-route streaming directives | Checker fails; malformed requests must stay rejection-only. |
| Content route inherits normal Nginx error logging | Checker fails because opaque IDs may enter error logs. |
| Host uses `$host`, a content route trusts inbound XFP, exact content omits appended XFF or explicit X-Real-IP removal, or shaped fallback omits explicit XFF/X-Real-IP removal | Checker fails; transport/origin evidence became ambiguous or over-broad. |
| Generic API route, port 10761, or external TLS contract changes | Checker fails or deployment review rejects the change. |
| Cache path is persistent or overlaps data/backup/log/source storage | Runtime cache disables; no plaintext/disk fallback is permitted. |
| Cache root is replaced after validation but before `os.OpenRoot` | Identity comparison fails, disk cache stays disabled, and replacement files remain untouched. |

### 5. Good/Base/Bad Cases

- Good: a valid asset-content Range request uses the exact unbuffered route,
  preserves Host port and closed proto evidence, and logs only status/bytes/time.
- Base: a malformed asset-content-shaped request reaches the backend safe
  rejection path under the same redacted log without streaming policy.
- Base: a stable cache directory passes pre-open/opened/final identity checks;
  later pathname replacement cannot redirect operations already confined to
  the stable `os.Root` descriptor.
- Bad: route all content-shaped paths through one unbuffered regex, log
  `$request_uri`, persist the cache under `/data`, or trust `EvalSymlinks`
  followed by an unchecked name-based `os.OpenRoot`.

### 6. Tests Required

- Render with the official `nginx:1.29-alpine` entrypoint, run `nginx -T`, and
  assert exact/fallback/generic route order and every required/forbidden
  directive.
- Mutation self-tests must independently break log variables, error logging,
  exact route, fallback isolation, buffering/cache/temp/gzip, finite timeouts,
  Host, `$scheme` ownership, exact-route XFF append/X-Real-IP removal,
  fallback XFF/X-Real-IP removal, port, and generic API behavior and observe
  checker failure.
- A hermetic runtime probe must render the official template and verify that an
  inbound XFP `https` reaches the upstream as actual `http`, exact content
  appends XFF while removing a spoofed X-Real-IP, shaped content removes
  spoofed XFF and X-Real-IP, and GET/HEAD preserve Host, cookie, Range/If-Range,
  bytes, and the dedicated redacted log contract.
- Build the All-in-One Dockerfile and assert the cache root exists with runtime
  ownership while `/data`, `/backup`, `/logs`, image, TLS, and `10761` remain
  unchanged.
- Deterministically replace the root from the source-validator test seam after
  path validation and before open. Assert disk cache is disabled and a sentinel
  under the symlink target is not deleted. Keep post-open rename tests for
  startup reconciliation and shutdown as separate coverage.

### 7. Wrong vs Correct

Wrong:

```nginx
location ~ ^/api/v1/asset-content {
  access_log /logs/nginx-access.log xirang_access;
  proxy_buffering off;
  proxy_set_header Range $http_range;
}
```

Correct:

```nginx
location ~ "^/api/v1/asset-content/[0-9a-f]{32}$" {
  access_log /logs/nginx-asset-content.log xirang_asset_content;
  error_log /dev/null crit;
  proxy_buffering off;
  proxy_set_header Range $http_range;
}
location ~ "^/api/v1/asset-content(?:/|$)" {
  access_log /logs/nginx-asset-content.log xirang_asset_content;
  error_log /dev/null crit;
  # Redaction/rejection only: no Range or streaming directives.
}
```

Wrong cache-root open:

```go
resolved, _ := filepath.EvalSymlinks(rootPath)
root, _ := os.OpenRoot(resolved) // the name may now identify another directory
```

Correct cache-root open:

```go
validated, _ := os.Lstat(resolved)
root, _ := os.OpenRoot(resolved)
opened, _ := root.Stat(".")
current, _ := os.Lstat(resolved)
if !os.SameFile(validated, opened) || !os.SameFile(opened, current) {
    _ = root.Close()
    return CacheReasonRootUnverified
}
```

---

## Scenario: Backup Asset Export Named Volume

### 1. Scope / Trigger

- Trigger: changing Compose volumes, Worker init, export-root docs, or
  image publication for backup-asset runtime.
- Applies to `docker-compose.yml`, `deploy/worker/entrypoint.sh`
  `initialize_volumes`, `.env.deploy`, `docs/env-vars.md`,
  `docs/deployment.md`, and `.github/workflows/publish-images.yml`.

### 2. Signatures

- Named volume: `asset-worker-export-store`.
- Mount target: `/var/lib/xirang-asset-runtime/export` on Core (`xirang`)
  and `asset-worker-init` only.
- Init: `EXPORT_ROOT=/var/lib/xirang-asset-runtime/export`, mode `0700`,
  owner `10000:10000`, same pattern as derived.
- Setting/env: `backup_assets.export.root` /
  `BACKUP_ASSETS_EXPORT_ROOT`. CodeDefault of `backup_assets.enabled`
  remains `"false"`.
- Official image: `linnea7171/xirang` on host port `10761`. Worker image
  stays local/optional and unpublished.

### 3. Contracts

- Parser (`asset-worker`) and updater must not list
  `asset-worker-export-store` as a source or target.
- Do not bind the export root under `/data`, `/backup`, `/logs`, content
  cache, or derived store.
- Do not edit `deploy/worker/Dockerfile`, seccomp, or
  `publish-images.yml` to publish a Worker image.
- New-install env examples may comment that `BACKUP_ASSETS_ENABLED=true`
  still has to pass the GA gate; copied existing env files are not
  rewritten, and the committed value stays `false`.
- Readiness probes validate the live foundation export config, not merely
  a non-empty path string.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Parser or updater mounts `asset-worker-export-store` | `check-compose-config` fails. |
| Init skips export-root `0700:10000:10000` | Worker/core volume contract fails. |
| `publish-images.yml` adds `xirang-asset-worker` | Reject; no-publish contract broken. |
| Docs claim Worker is GA / Docker Hub image | Reject; docs truth broken. |
| CodeDefault of `backup_assets.enabled` is `"true"` | Reject unless a later PRD amends it. |

### 5. Good/Base/Bad Cases

- Good: Core + init mount the named volume; replacing the Core container
  preserves export ciphertext.
- Base: Worker profile off; Core still has the export volume and boots
  with the feature disabled.
- Bad: mounting export on parser/updater, or publishing a Worker image
  to make GA “complete”.

### 6. Tests Required

- `./scripts/check-compose-config.sh` and its unit test for export
  isolation.
- `ASSET_WORKER_STATIC_ONLY=1 ./scripts/test-asset-worker.sh`.
- `scripts/test-core-compose.sh` when Compose changes.
- Doc freshness after env/deployment README edits.

### 7. Wrong vs Correct

Wrong:

```yaml
asset-worker:
  volumes:
    - type: volume
      source: asset-worker-export-store
      target: /var/lib/xirang-asset-runtime/export
```

Correct:

```yaml
xirang:
  volumes:
    - type: volume
      source: asset-worker-export-store
      target: /var/lib/xirang-asset-runtime/export
      volume:
        nocopy: true
asset-worker-init:
  volumes:
    - type: volume
      source: asset-worker-export-store
      target: /var/lib/xirang-asset-runtime/export
      volume:
        nocopy: true
```
