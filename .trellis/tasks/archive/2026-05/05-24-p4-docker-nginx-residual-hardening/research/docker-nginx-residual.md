# Research: Docker/Nginx residual hardening slice

- **Query**: Research the current Trellis task `.trellis/tasks/05-24-p4-docker-nginx-residual-hardening`: identify the smallest local-only, behavior-compatible P4 hardening slice across Docker/nginx residual surfaces. Focus on deployment Nginx access/error logs, Docker volume discovery/audit/logging, Docker app-profile rendered hooks, hook templates, and Docker build/deploy docs/workflows. Context: prior P4 work already sanitized task/task-run runtime evidence and task list/detail policy hook fields; external Vault/KMS/SSH CA/session recording/command approval/WebAuthn/passkeys/device trust are out of scope; preserve API/deployment/UI behavior unless inspection proves broader need.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/05-24-p4-docker-nginx-residual-hardening/task.json` | Active task metadata; planning status and target branch. |
| `deploy/nginx/templates/default.conf.template` | Production All-in-One Nginx template; defines access/error log paths and proxy/static routes. |
| `deploy/nginx/README.md` | Nginx deployment notes; documents `/logs/nginx-access.log` and `/logs/nginx-error.log`. |
| `deploy/allinone/Dockerfile` | All-in-one image build/runtime stages; creates `/logs`, copies Nginx template, sets `LOG_FILE=/logs/xirang.log`, exposes `10761`. |
| `deploy/allinone/entrypoint.sh` | Container runtime startup; fixes `/data`, `/backup`, `/logs` ownership and starts backend/supercronic/Nginx. |
| `docker-compose.yml` | Official production Compose contract; maps `./logs:/logs` and exposes `10761:10761`. |
| `.dockerignore` | Build context exclusions for `.env*`, `.trellis`, `.claude`, `.github`, `data`, `backups`, `logs`, temp paths, and artifacts. |
| `Makefile` | Docker build/push/buildx targets and `deploy-init` kit generation. |
| `.env.deploy` | Production env template; documents app log behavior under `/logs/xirang.log`. |
| `.github/workflows/deploy.yml` | Maintainer deploy workflow; SSH deploy path prints `docker logs xirang --tail=50` on healthcheck failure. |
| `.github/workflows/publish-images.yml` | Docker Hub publish workflow; fixed `docker.io/linnea7171/xirang` image. |
| `docs/deployment.md` | User deployment guide; documents Compose, `/logs`, local image build, and `docker compose logs`. |
| `docs/env-vars.md` | Env var reference; documents `IMAGE_TAG` and fixed container port/TLS contract. |
| `docs/maintainers/release.md` | Maintainer release guide; documents Docker Hub publish flow. |
| `scripts/check-doc-freshness.sh` | Warns when deploy/release files change without paired public/maintainer docs or repo guidance updates. |
| `backend/internal/api/router.go` | Registers Docker volume, AppCredential profile, hook-template, policy, and task routes with RBAC/ownership. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume discovery via SSH; warning mapping, safe volume-name inspection, and credential audit event writing. |
| `backend/internal/api/handlers/docker_handler_test.go` | Regression test proving Docker volume audit excludes raw Docker output and volume names. |
| `web/src/lib/api/docker-api.ts` | Frontend Docker volume API client contract: returns `name`, `driver`, and `mountpoint`. |
| `web/src/components/docker-volumes-panel.tsx` | UI renders Docker volume names/mountpoints and uses mountpoint selection. |
| `backend/internal/profile/profile.go` | Built-in app-aware profiles, including Docker profiles; hook templates render credential/container fields. |
| `backend/internal/profile/app_profile_access.go` | AppCredential resolver seam with copied config access and safe metadata labels. |
| `backend/internal/profile/profile_test.go` | Tests profile rendering; Docker profiles require `docker inspect`; tests intentionally assert rendered hooks can include passwords. |
| `backend/internal/profile/app_profile_access_test.go` | Tests AppCredential resolver safety, encrypted-at-rest loading, safe metadata, and JSON non-exposure. |
| `backend/internal/model/models.go` | Defines encrypted `AppCredential.Config`, plaintext JSON-visible `Policy.PreHook`/`PostHook`, and sanitized node/credential model behavior. |
| `backend/internal/api/handlers/app_credential_handler.go` | Sanitized AppCredential CRUD responses, profile list response, and credential-update hook cascade. |
| `backend/internal/api/handlers/app_credential_handler_test.go` | Tests AppCredential encryption/API masking, profile template non-exposure, and hook cascade behavior. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update renders app-profile hooks; policy responses return `pre_hook`/`post_hook`; manual hook validation. |
| `backend/internal/api/handlers/hook_templates_handler.go` | Deprecated `/hook-templates` endpoint; returns static hook examples including Docker stop/start. |
| `backend/internal/api/handlers/task_handler.go` | Current task list/detail read boundary sanitizes legacy `LastError` and clears nested policy hooks. |
| `backend/internal/api/handlers/task_handler_test.go` | Tests task list/detail policy-hook clearing and legacy evidence sanitization without DB mutation. |
| `backend/internal/api/handlers/response.go` | Standard API response envelope and generic internal-error response. |
| `backend/internal/middleware/structured_logger.go` | Backend structured access logger records URL path, not query string. |
| `backend/internal/middleware/audit.go` | HTTP audit middleware records method/route/status/client metadata, not query/body. |
| `backend/internal/credentialaudit/audit.go` | Credential audit writer sanitizes fields, metadata, and error strings. |
| `backend/internal/api/handlers/helpers.go` | Shared credential-audit safe error/metadata helpers. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment contract: fixed image/port, `/logs` bind mount, Nginx logs under `/logs`, no Compose logging options. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging contract: do not log secrets, decrypted values, Docker output/volume names, diagnostic evidence, executor config, or command output. |
| `.trellis/spec/backend/error-handling.md` | API error contract: no raw Docker output, command output, diagnostic evidence, exported config payloads, SQL, or stack-like details to clients. |
| `.trellis/spec/backend/database-guidelines.md` | Credential audit/storage contract: no raw credentials, decrypted executor config, command output, terminal streams, or file contents. |
| `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/research/docker-runtime-residual.md` | Prior Docker/runtime residual review; selected Nginx access-log query minimization as the clearest remaining local runtime log slice. |
| `.trellis/tasks/archive/2026-05/05-24-p4-next-residual-security-hardening/research/appcredential-hook-residual.md` | Prior AppCredential-hook residual comparison; task response hook duplication has since been hardened in current code. |

### Code Patterns

#### 1. Deployment Nginx access/error logs

`deploy/nginx/templates/default.conf.template` configures the production Nginx entrypoint and persistent log files:

```nginx
access_log /logs/nginx-access.log;
error_log /logs/nginx-error.log warn;
```

These paths are part of the deployment contract:

- `docker-compose.yml:9-12` maps `./logs:/logs` and labels it “应用与 Nginx 日志”.
- `deploy/allinone/Dockerfile:66-80` creates/chowns `/logs`, sets `LOG_FILE=/logs/xirang.log`, and declares `/logs` as a volume.
- `deploy/allinone/entrypoint.sh:41-48` fixes bind-mount ownership and ensures `/logs` exists before Nginx starts.
- `deploy/nginx/README.md:24-26` documents `/logs/nginx-access.log` and `/logs/nginx-error.log`.
- `.trellis/spec/backend/deployment-runtime.md:27-32` requires `./logs:/logs`, app logs under `/logs`, and Nginx logs under `/logs`.

The backend itself avoids query-string logging on the main server-side paths:

- `backend/internal/middleware/structured_logger.go:15-27` stores `c.Request.URL.Path` as `path`, not `RawQuery`.
- `backend/internal/middleware/audit.go:35-51` records `c.FullPath()`/`URL.Path` plus status/client metadata, not request body/query.

Residual surface: the Nginx template uses default `access_log` formatting. Default Nginx access logs normally include the request line, which includes query strings. Several authenticated API routes use query parameters for operational data (`/nodes/:id/files`, `/nodes/:id/files/content`, `/tasks/:id/logs?limit=...`, node logs filters, etc.). Even when backend audit/structured logs avoid queries, Nginx can persist full request URIs under the local `/logs` bind mount. This is a local deployment log surface; it does not require API/UI changes to narrow.

`error_log /logs/nginx-error.log warn` is less directly fixable: Nginx error-log format is not customizable like `access_log`, and warning/error entries may include request context. Lowering the level from `warn` to `error` or `crit` would reduce some request-context entries but changes operational observability. The smallest compatible slice should therefore focus on access-log query minimization and document error-log behavior as a caveat rather than attempt a broad error-log rewrite.

#### 2. Docker volume discovery, audit, and logging

Route and behavior:

- `backend/internal/api/router.go:166` registers `GET /nodes/:id/docker-volumes` with `nodes:read` and `OwnershipNodeCheck`.
- `backend/internal/api/handlers/docker_handler.go:23-28` defines the response DTO as `name`, `driver`, and `mountpoint`.
- `backend/internal/api/handlers/docker_handler.go:144-199` runs `docker volume ls --format '{{json .}}'`, parses JSON lines, and optionally calls `docker volume inspect` for missing mountpoints.
- `backend/internal/api/handlers/docker_handler.go:201-219` validates inspect targets with `safeDockerName` before interpolating the volume name into the inspect command.
- `web/src/lib/api/docker-api.ts:3-27` and `web/src/components/docker-volumes-panel.tsx:96-118` consume and render the same fields; `mountpoint` drives the “use path” action.

Failure and audit behavior:

- `backend/internal/api/handlers/docker_handler.go:154-164` maps common Docker command failures to generic warnings such as Docker missing, permission denied, or `docker volume ls` failure; raw command output is not returned.
- `backend/internal/api/handlers/docker_handler.go:92-116` writes credential audit action `docker_volumes.discover` with purpose `docker_volumes` and metadata limited to `stage`, `count`, and `has_warning`.
- `backend/internal/api/handlers/helpers.go:103-112` turns audit errors into generic `<stage> failed` strings.
- `backend/internal/credentialaudit/audit.go:208-281` sanitizes audit metadata and denies keys/values containing secret/output/command/config-like markers.
- `backend/internal/api/handlers/docker_handler_test.go:31-66` verifies Docker audit does not persist raw Docker output or volume names.

Residual surface: Docker volume names and mountpoints are still returned to authenticated/authorized API callers and displayed in UI. That is current product behavior needed for the Docker volume selector. Masking or removing those fields would break the API/UI contract; audit persistence is already narrow and covered by a regression test. No smaller compatible Docker-volume hardening slice was found.

#### 3. Docker app-profile rendered hooks

Profile and rendering behavior:

- `backend/internal/profile/profile.go:47-128` defines 8 built-in profiles; Docker profiles are `docker-mysql`, `docker-postgres`, `docker-mongodb`, and `docker-redis`.
- Docker pre-hook templates run `docker inspect {{.container_name}}` before `docker exec ...` and can include user/password/container fields in command text (`profile.go:89-126`).
- `backend/internal/profile/profile.go:145-174` renders the templates through Go `text/template` and returns concrete pre/post hook strings.
- `backend/internal/profile/profile_test.go:317-331` asserts every Docker profile is marked Docker and starts with `docker inspect`.
- `backend/internal/profile/profile_test.go:347-377` explicitly asserts passwords can appear in rendered hooks and that shell-looking values are output as values. That is current execution behavior, not merely display behavior.

Credential and response behavior:

- `backend/internal/model/models.go:152-196` stores `AppCredential.Config` with `json:"-"`, encrypts/decrypts it with GORM hooks, and removes `password` in `SanitizedConfig()`.
- `backend/internal/profile/app_profile_access.go:20-89` wraps AppCredential config access in an `AppProfileAccess` object with safe metadata.
- `backend/internal/api/handlers/app_credential_handler.go:58-78` builds AppCredential responses from sanitized config.
- `backend/internal/api/handlers/app_credential_handler.go:335-346` returns profile schemas from `profile.ListProfiles()`; template fields have `json:"-"` and tests verify they are not exposed (`app_credential_handler_test.go:400-403`).
- `backend/internal/api/handlers/policy_handler.go:242-282` and `policy_handler.go:567-605` render profile hooks on policy create/update when an app profile is selected and one or both hook fields are omitted.
- `backend/internal/api/handlers/app_credential_handler.go:299-333` cascades credential config changes into policies whose current hooks still match old rendered values.
- `backend/internal/api/handlers/policy_handler.go:1082-1133` returns policy `pre_hook` and `post_hook` directly through policy list/detail responses.

Prior P4 state now present in current code:

- `backend/internal/api/handlers/task_handler.go:69-78` sanitizes task responses by clearing nested `Policy.PreHook` and `Policy.PostHook` and sanitizing legacy `LastError`.
- `backend/internal/api/handlers/task_handler_test.go:175-386` verifies task list/detail responses clear nested policy hooks and do not mutate stored policy rows.

Residual surface: policy rows and policy list/detail responses can still contain rendered hook strings, including Docker app-profile values. Fully removing that exposure would be a larger product/API/storage redesign because the policy editor currently displays hook text and tests/docs treat rendered hook visibility as expected behavior. Docker profile `container_name` is required for container profiles, but inspected validation only requires it to be non-empty (`app_credential_handler.go:101-114`); broad escaping/validation would touch profile input semantics and the broader rendered-hook design. No smaller Docker-profile-only response/logging slice was found that is as behavior-compatible as the Nginx access-log slice.

#### 4. Deprecated hook templates

`backend/internal/api/handlers/hook_templates_handler.go:16-52` returns static examples:

- MySQL/PostgreSQL/MongoDB/Redis templates with placeholder/local paths.
- Docker stop/start template using `docker compose -f /path/to/compose.yml stop` and `start`.

The route is deprecated (`hook_templates_handler.go:61-74`) in favor of app-aware profiles. These templates are static examples, not rendered from stored credentials or runtime evidence. The MySQL example contains `$MYSQL_ROOT_PASSWORD` as an environment-variable placeholder, not a stored secret. The frontend inserts the returned strings into policy editor hook textareas (`web/src/components/policy-editor-dialog.tsx:679-703`). No local-only leakage slice was found here without changing the existing hook-template feature behavior.

#### 5. Docker build/deploy docs and workflows

Local deployment/build surfaces:

- `.dockerignore:1-31` excludes local env files, Trellis/agent directories, databases, `data`, `backups`, `logs`, deploy kits, and temp paths from Docker build context.
- `deploy/allinone/Dockerfile:71-80` sets runtime defaults (`SERVER_ADDR`, SQLite path, known_hosts path, `LOG_FILE`, `TZ`) but does not bake application secrets.
- `Makefile:87-112` builds/pushes fixed `docker.io/linnea7171/xirang` tags; no registry/namespace env expansion is used.
- `Makefile:113-121` copies `docker-compose.yml` and `.env.deploy` into `deploy-kit/` and prints generic next steps, not secret values.
- `docs/deployment.md:176-184` documents `./logs -> /logs` for app and Nginx logs; `docs/deployment.md:215-229` tells operators how to view stdout and persisted logs.
- `docs/deployment.md:317-338` documents local image builds and says not to overwrite `latest` manually.
- `.trellis/spec/backend/deployment-runtime.md:24-32` requires fixed image/port/bind-mount behavior and no Compose logging options.

Workflow surfaces:

- `.github/workflows/deploy.yml:80-83` prints `docker logs xirang --tail=50` to GitHub Actions logs on deployment healthcheck failure. This can export local runtime logs to an external provider.
- `.github/workflows/publish-images.yml:43-96` uses fixed Docker Hub image and DockerHub credentials via Actions secrets.

The user scoped this task to local-only hardening and excluded external-provider work. Therefore the deploy workflow’s `docker logs` failure path is a caveat for a future external-provider workflow review, not the smallest local-only slice here.

### Smallest Compatible Slice

**Recommended slice: minimize Nginx access-log query persistence while keeping the same log file, routes, ports, bind mounts, and UI/API behavior.**

Candidate implementation shape for the main agent:

```nginx
log_format xirang_access '$remote_addr - $remote_user [$time_local] '
                         '"$request_method $uri $server_protocol" '
                         '$status $body_bytes_sent '
                         '"$http_referer" "$http_user_agent" '
                         '"$http_x_forwarded_for"';

access_log /logs/nginx-access.log xirang_access;
```

Key point: use `$uri` (path without query args) rather than `$request`/`$request_uri` in the access-log request line. The file path `/logs/nginx-access.log` remains stable, and access logging remains enabled. This narrows only local runtime log contents.

Why this is the smallest behavior-compatible slice found:

1. It is local-only and confined to the official Docker/Nginx runtime surface.
2. It preserves deployment shape: image name, port `10761`, `/logs` bind mount, `/healthz`, `/api/v1/*`, SPA fallback, and backend behavior remain unchanged.
3. It preserves API/UI schemas and route behavior.
4. It aligns with existing backend behavior, where structured logging and HTTP audit already avoid request query strings.
5. It avoids broader AppCredential/policy hook redesign, Docker volume API/UI changes, and external GitHub Actions workflow changes.

Likely paired implementation notes:

- If `deploy/nginx/templates/default.conf.template` changes, `scripts/check-doc-freshness.sh:77-81` may warn unless a public/maintainer doc or repo guidance file is also updated. A minimal `docs/deployment.md` note that Nginx access logs omit query strings would satisfy the deployment-doc sync intent.
- Validate Nginx syntax with the template included in an Nginx config and a writable `/logs` directory.
- Run `docker compose -f docker-compose.yml config` with an `.env` present/copy of `.env.deploy`, `git diff --check`, and `bash scripts/check-doc-freshness.sh`.

### External References

No external references were used. The requested scope was internal/local code, deployment, docs, and workflow inspection.

### Related Specs

- `.trellis/spec/backend/deployment-runtime.md` — Official Docker Compose/All-in-One image contract; any fix must preserve fixed image, port `10761`, `/logs`, healthcheck, and no Compose logging options.
- `.trellis/spec/backend/logging-guidelines.md` — Logging contract forbids secrets, decrypted values, Docker command output/volume names, diagnostic evidence, executor config, and command output in logs.
- `.trellis/spec/backend/error-handling.md` — API error contract forbids raw Docker output, command output, diagnostic evidence, file content, exported config payloads, SQL, and stack-like details to clients.
- `.trellis/spec/backend/database-guidelines.md` — Credential audit/storage contract forbids raw credentials, decrypted executor config, terminal streams, command output, and file contents.
- `.trellis/spec/guides/documentation-truth-guide.md` — Deployment and Docker claims must match current source/config/docs.

## Caveats / Not Found

- Not found: Docker volume audit persisting raw Docker output or volume names; an existing test covers this.
- Not found: Docker volume API/UI fields that can be removed without changing behavior; `name` and `mountpoint` are part of the selection flow.
- Not found: AppCredential profile API exposing hook templates or raw passwords; profile templates have `json:"-"`, and AppCredential responses use sanitized config.
- Still present by current product contract: policy list/detail responses return `pre_hook` and `post_hook`, and policy rows can store rendered AppCredential-derived hook text. Fixing that is larger than this Docker/Nginx residual slice.
- Already present from prior P4 work: task list/detail responses clear nested policy hooks and sanitize legacy runtime evidence.
- Nginx `error_log` query/context minimization is not as directly format-customizable as `access_log`; lowering its level would be an operational logging behavior change and was not selected as the smallest compatible slice.
- GitHub Actions deploy failure logs (`docker logs xirang --tail=50`) are external-provider output and out of this local-only scope.
