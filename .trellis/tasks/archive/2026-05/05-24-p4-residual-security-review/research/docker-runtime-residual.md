# Research: Docker/runtime residual risk surface

- **Query**: Research residual P4 risk surface: Docker/runtime output and diagnostic evidence surfaces in `/Users/weibo/Code/xirang`. Focus on Docker build/publish/runtime output, Docker volume/config response surfaces, task/runtime diagnostic output, and any server responses/logs/audit paths not covered by task-runtime, node-log, and diagnostic evidence sanitization. Constraints: local-only hardening; no external providers; preserve API/deployment/UI behavior. Include files inspected, existing sanitizers, remaining risks, smallest compatible fix if any, and recommendation.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found / Inspected

| File Path | Description |
|---|---|
| `Makefile` | Docker build/push/buildx targets, deployment kit generation, official image naming. |
| `docker-compose.yml` | Official runtime Compose contract: image, env file, bind mounts, port, healthcheck. |
| `.dockerignore` | Docker build-context exclusions for env files, Trellis, credentials, data, backups, logs, deploy-kit, temp paths. |
| `deploy/allinone/Dockerfile` | All-in-one image build/runtime stages, runtime dependencies, environment defaults, volumes, healthcheck. |
| `deploy/allinone/entrypoint.sh` | Container startup output, ownership setup for `/data`, `/backup`, `/logs`, backend readiness polling, nginx startup. |
| `deploy/allinone/xirang-backup.cron` | Container cron schedule for DB backup and retention cleanup. |
| `deploy/nginx/templates/default.conf.template` | Nginx proxy and runtime access/error log configuration. |
| `scripts/backup-db.sh` | Local/container database backup command output. |
| `scripts/restore-db.sh` | Local/container database restore command output and usage text. |
| `.github/workflows/publish-images.yml` | External GitHub Actions Docker build/publish workflow and image summary. |
| `.github/workflows/deploy.yml` | External GitHub Actions deploy workflow; failure path prints container logs. |
| `.github/workflows/dockerhub-description.yml` | External Docker Hub description sync workflow. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume API, SSH Docker command execution, warning/error mapping, volume audit metadata. |
| `backend/internal/api/handlers/docker_handler_test.go` | Test proving Docker volume audit excludes raw Docker output and volume names. |
| `web/src/lib/api/docker-api.ts` | Frontend Docker volume API client contract. |
| `web/src/components/docker-volumes-panel.tsx` | Frontend rendering of Docker volume name, driver, mountpoint, and warning. |
| `backend/internal/api/handlers/response.go` | Unified response envelope and generic internal-error response behavior. |
| `backend/internal/api/handlers/helpers.go` | Shared client/audit sanitizers, safe path hashing, path character validation. |
| `backend/internal/credentialaudit/audit.go` | Credential audit field, metadata, and error sanitization. |
| `backend/internal/credentialaudit/audit_test.go` | Tests for credential audit metadata bounds and raw-output redaction. |
| `backend/internal/sshutil/credential_provider.go` | SSH credential resolution metadata and invalid private-key error behavior. |
| `backend/internal/sshutil/credential_provider_test.go` | Tests for encrypted SSH credentials and safe credential metadata/errors. |
| `backend/internal/middleware/audit.go` | HTTP audit log middleware; stores method/path/status/client IP/user agent, not query/body. |
| `backend/internal/middleware/structured_logger.go` | HTTP structured access logging; logs URL path, not query/body. |
| `backend/internal/util/sanitize.go` | Global message sanitizer for private keys, URLs, tokens, host lookup/dial messages, sensitive key-value pairs, and length bounding. |
| `backend/internal/util/telegram.go` | Telegram token masking and delivery error sanitizer delegation. |
| `backend/internal/task/runtime_sanitize.go` | Task runtime log/last-error/output/evidence sanitizer. |
| `backend/internal/task/runtime_sanitize_test.go` | Tests for task runtime endpoint/token/path/host/output redaction. |
| `backend/internal/task/verifier/runtime_sanitize.go` | Verifier runtime evidence sanitizer for URLs, output markers, paths, hosts, host-sensitive fragments. |
| `backend/internal/task/log_writer.go` | Task log persistence/WebSocket publication path; sanitizes before DB and realtime delivery. |
| `backend/internal/task/runner.go` | Task run lifecycle, hook/executor/restore error handling, TaskRun last_error persistence, credential audit context. |
| `backend/internal/task/hook.go` | SSH hook execution; discards success output and sanitizes failure error text. |
| `backend/internal/task/drill.go` | Restore drill evidence persistence and phase credential audit sanitization. |
| `backend/internal/task/integrity_checker.go` | Restic/rclone integrity-check output/error sanitization before logs/alerts. |
| `backend/internal/task/executor/executor.go` | Rsync executor streaming sanitizer, shell escaping, raw `RunSSHCommandOutput` primitive. |
| `backend/internal/task/executor/command_executor.go` | Command executor stdout/stderr streaming sanitizer and lifecycle logging. |
| `backend/internal/task/executor/restic_executor.go` | Restic executor streaming/error output sanitizer and repository-password command env construction path. |
| `backend/internal/task/executor/rclone_executor.go` | Rclone executor output streaming sanitizer and progress parsing. |
| `backend/internal/task/executor/ssh_connect.go` | Executor SSH dial credential audit with safe metadata/error messages. |
| `backend/internal/task/executor/restic_repository_access.go` | Restic repository access safe metadata and env-prefix construction. |
| `backend/internal/task/executor/runtime_sanitize.go` | Executor runtime evidence/output sanitizer. |
| `backend/internal/task/executor/runtime_sanitize_test.go` | Tests for executor path/host/token/output redaction. |
| `backend/internal/model/models.go` | Model JSON redaction/encryption hooks and API-visible fields (`TaskLog.Message`, `TaskRun.LastError`, `RestoreDrillEvidence`). |
| `backend/internal/api/handlers/task_handler.go` | Task list/detail/create/update/trigger/batch response surfaces and node sanitization. |
| `backend/internal/api/handlers/task_handler_test.go` | Tests proving task responses redact executor config and restic password. |
| `backend/internal/api/handlers/task_run_handler.go` | Task run list/detail/log response surfaces, including `LastError`, `DrillEvidence`, and `TaskLog`. |
| `backend/internal/api/handlers/audit_handler.go` | Audit list/export response surfaces; no request body/query included. |
| `backend/internal/api/handlers/config_handler.go` | Config import/export boundaries, default secret filtering, admin-only `include_secrets=true`. |
| `backend/internal/api/handlers/config_handler_test.go` | Tests proving default config export excludes private keys, passwords, executor tokens, SMTP password, bearer token, and sensitive key names. |
| `backend/internal/api/handlers/integration_handler.go` | Integration response URL masking, secret omission, sender error sanitization. |
| `backend/internal/api/handlers/integration_handler_test.go` | Tests proving endpoint/proxy masking, secret omission, and sanitized test-send failure response. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command/task response surface; node sanitization and executor_config redaction by model tag. |
| `backend/internal/api/handlers/batch_handler_test.go` | Test proving batch detail omits executor_config and restic password. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment runtime contract for Compose/image/ports/bind mounts/log paths and validation expectations. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging contract: do not log secrets, decrypted values, Docker command output/volume names, diagnostic evidence, command output, executor config. |
| `.trellis/spec/backend/error-handling.md` | Error handling contract: do not expose raw Docker output, diagnostic evidence, command output, file content, exported config payloads, raw SQL, or stack-like details to clients. |
| `.trellis/tasks/05-24-p4-residual-security-review/prd.md` | Active task PRD and P4 residual review constraints. |

### Code Patterns

#### Docker build/publish/runtime surfaces

- Docker build context is already constrained by `.dockerignore`: local `.env`, `.env.*`, `.trellis`, `.claude`, `.github`, database files, `data`, `backups`, `logs`, `deploy-kit`, and temp paths are excluded from image build context. This reduces local secret/data ingress into Docker build output and image layers.
- `Makefile` build/push targets use the fixed official image `docker.io/linnea7171/xirang` and print Docker CLI progress/tag output, but do not intentionally echo env secrets or runtime config values.
- `docker-compose.yml` keeps the official runtime shape stable: `linnea7171/xirang:${IMAGE_TAG:-latest}`, `.env`, `TZ`, bind mounts `./data:/data`, `./backups:/backup`, `./logs:/logs`, port `10761:10761`, and a local healthcheck. This matches `.trellis/spec/backend/deployment-runtime.md`.
- `deploy/allinone/Dockerfile` sets local runtime defaults such as `SQLITE_PATH=/data/xirang.db`, `SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts`, and `LOG_FILE=/logs/xirang.log`, declares `/data`, `/backup`, and `/logs` volumes, and does not bake application secrets into image defaults.
- `deploy/allinone/entrypoint.sh` prints only generic lifecycle/readiness messages and does not dump environment, config, backend logs, or command output. It does expose fixed local paths `/data`, `/backup`, and `/logs` through container behavior, which is part of the deployment contract.
- `scripts/backup-db.sh` and `scripts/restore-db.sh` print local backup/restore file paths and generic usage text. The PostgreSQL branch uses `DB_DSN` but does not echo the live DSN. This is local operator output, not an API/UI surface.
- `.github/workflows/publish-images.yml`, `.github/workflows/deploy.yml`, and `.github/workflows/dockerhub-description.yml` are external-provider surfaces. They were inspected for context, but the user constraint is “local-only hardening; no external providers”. The deploy workflow has a failure path that prints `docker logs xirang --tail=50` into GitHub Actions logs; this is relevant to runtime output but out of scope for local-only hardening unless explicitly re-scoped.

#### Docker volume/config API surfaces

- `backend/internal/api/handlers/docker_handler.go` exposes Docker volume `name`, `driver`, and `mountpoint` through the authenticated API. The frontend client and UI (`web/src/lib/api/docker-api.ts`, `web/src/components/docker-volumes-panel.tsx`) expect and render those fields. Masking or deleting them would change API/UI behavior.
- The Docker handler maps Docker command failures to generic warnings such as “Docker 未安装或不在 PATH 中”, “无权访问 Docker（当前用户可能不在 docker 组中）”, or “执行 docker volume ls 失败”, instead of returning raw Docker output.
- Docker volume audit is already intentionally narrow. `writeDockerVolumeAudit` metadata is limited to `stage`, `count`, and `has_warning`; error text is produced through `credentialAuditSafeError(stage, err)` and therefore becomes a generic `<stage> failed` message. `backend/internal/api/handlers/docker_handler_test.go` verifies raw Docker output and volume names are not persisted in audit metadata/errors.
- `safeDockerName` validates inspected volume names before using them in `docker volume inspect`, reducing shell/injection risk for volume inspection.

#### Task/runtime diagnostic output surfaces

- `backend/internal/task/log_writer.go` sanitizes every task log message through `sanitizeTaskLogMessage` before persistence and before WebSocket delivery. `TaskLog.Message` is API-visible, so this is the primary control point for task log response and realtime surfaces.
- `backend/internal/task/runtime_sanitize.go` applies layered task runtime redaction:
  - global `util.SanitizeMessage` first;
  - output markers (`输出`, `output`, `stdout`, `stderr`) collapsed to `[输出已隐藏]`;
  - command lifecycle lines such as `执行命令`, `在远程节点执行`, and `执行 restic/rclone check` collapsed to `[命令已隐藏]`;
  - URLs collapsed to `<scheme>://***`;
  - remote paths, named paths, absolute paths, Windows paths, IPs, hostnames, and host-sensitive fragments replaced with placeholders;
  - non-empty raw runtime output is fully replaced by `[输出已隐藏]`.
- `backend/internal/task/executor/runtime_sanitize.go` mirrors the same protections for executor-level streaming and error output. The command, rsync, restic, and rclone executors send stdout/stderr lines through executor sanitization before forwarding to the manager log callback.
- `backend/internal/task/runner.go`, `backend/internal/task/hook.go`, `backend/internal/task/drill.go`, and `backend/internal/task/integrity_checker.go` sanitize hook, executor, restore, drill, integrity-check, and verification failures before persisting `TaskRun.LastError`, `RestoreDrillEvidence` error fields, task logs, alerts, or credential audit errors.
- `backend/internal/task/executor/RunSSHCommandOutput` remains a raw-output primitive: it returns combined stdout/stderr directly to the caller. Current inspected callers sanitize, ignore, or summarize output before persistence/response. The residual risk is future misuse, not a currently observed raw persisted/returned output path in this scope.

#### Server responses/logs/audit paths outside task-runtime/node-log/diagnostic sanitizers

- `backend/internal/api/handlers/response.go` returns generic client text for unexpected internal errors through `respondInternalError`, while logging the raw `err` server-side with route path context. This preserves client behavior but means server logs must rely on upstream call sites not passing secret-bearing errors to `respondInternalError`.
- `backend/internal/api/handlers/helpers.go` provides `sanitizedClientError`, `safePathHash`, `credentialAuditSafeError`, and `validatePathChars`. These are used in sensitive areas to avoid raw path/output/error persistence and to hash path metadata where needed.
- `backend/internal/credentialaudit/audit.go` sanitizes audit fields, metadata, and errors. Metadata keys/values containing markers such as private/password/token/secret/credential/config/output/stream/command/content/payload are dropped. Error messages go through `util.SanitizeMessage` and output markers are redacted. Tests cover raw command output redaction and metadata bounding.
- `backend/internal/middleware/audit.go` stores HTTP audit records with method, route/path, status, client IP, and user agent; it skips request query/body. CSV export in `audit_handler.go` exports the same fields.
- `backend/internal/middleware/structured_logger.go` logs HTTP path, not query/body. This avoids backend structured access log leakage of query parameters or request bodies.
- `deploy/nginx/templates/default.conf.template` uses default `access_log /logs/nginx-access.log;`. In nginx, the default access log format includes the request line, which normally includes query strings. Backend logging/audit avoid query strings, but nginx access logs may still persist full request URIs with query parameters. This is the clearest remaining local runtime log surface found in this Docker/runtime-focused review.
- `backend/internal/model/models.go` uses JSON tags and hooks to keep major secret fields out of API responses: `SSHKey.PrivateKey` is `json:"-"`, `Task.ExecutorConfig` is `json:"-"`, `Integration.Secret` is `json:"-"`, and `Node.Sanitized()` clears password/private key and nested SSH key private key. `TaskRun.LastError`, `TaskLog.Message`, and `RestoreDrillEvidence` are API-visible and therefore depend on producer-side sanitization.
- `backend/internal/api/handlers/task_handler.go`, `task_run_handler.go`, and `batch_handler.go` preserve product-visible task configuration fields such as command/source/target and task run/log details. Existing tests verify `executor_config` and repository password are not exposed.
- `backend/internal/api/handlers/config_handler.go` default export excludes node passwords/private keys, SSH private keys, task executor_config, and sensitive settings by key/value heuristics. `include_secrets=true` is admin-only and intentionally exports secrets; this is an explicit product escape hatch and is audited with safe count metadata.
- `backend/internal/api/handlers/integration_handler.go` masks endpoint/proxy URLs in responses and omits `Secret`, while test-send errors are sanitized through `util.SanitizeError`/related helpers. Tests cover endpoint/proxy masking and secret omission.

### Existing Sanitizers

| Sanitizer / Boundary | Location | What it protects |
|---|---|---|
| Global message sanitizer | `backend/internal/util/sanitize.go` | Private key blocks, sensitive URLs/query/path, Telegram tokens, host lookup/dial hostnames, sensitive key-value values, long messages. |
| Task runtime sanitizer | `backend/internal/task/runtime_sanitize.go` | Task logs, task last_error, runtime errors, raw task output, command lifecycle text, URLs, paths, hosts, host-sensitive labels. |
| Verifier sanitizer | `backend/internal/task/verifier/runtime_sanitize.go` | Verification diagnostic evidence: output markers, URLs, paths, hosts, host-sensitive labels. |
| Executor runtime sanitizer | `backend/internal/task/executor/runtime_sanitize.go` | Executor stdout/stderr/error evidence and raw runtime output. |
| Task log sink | `backend/internal/task/log_writer.go` | Sanitizes before TaskLog DB persistence and WebSocket log event publication. |
| Credential audit sanitizer | `backend/internal/credentialaudit/audit.go` | Bounded/sanitized audit fields, metadata denylist, output-marker redaction in error messages. |
| Docker audit safe error/metadata | `backend/internal/api/handlers/docker_handler.go`, `backend/internal/api/handlers/helpers.go` | Avoids raw Docker output and volume names in credential audit metadata/errors. |
| Response helper generic internal errors | `backend/internal/api/handlers/response.go` | Prevents unexpected server errors from being reflected to API clients. |
| HTTP audit middleware | `backend/internal/middleware/audit.go` | Avoids request query/body in audit logs; stores method/path/status/client/user-agent only. |
| Structured HTTP logger | `backend/internal/middleware/structured_logger.go` | Logs path rather than query/body. |
| Model JSON redaction | `backend/internal/model/models.go` | Prevents private keys, executor_config, integration secret from normal JSON responses. |
| Node response sanitizer | `backend/internal/model/models.go` and handlers | Clears node password/private key/nested SSH private key before node/task/batch responses. |
| Integration response sanitizer | `backend/internal/api/handlers/integration_handler.go` | Masks endpoint/proxy URL and omits secret. |
| Config export default filtering | `backend/internal/api/handlers/config_handler.go` | Excludes private keys, passwords, executor config, sensitive settings unless admin explicitly requests secrets. |
| Safe path hashing | `backend/internal/api/handlers/helpers.go`, `backend/internal/task/drill.go` | Stores hashes/summary metadata instead of raw sensitive paths in audit-like records. |

### Remaining Risks

1. **Nginx access logs may persist query strings locally.**
   - Location: `deploy/nginx/templates/default.conf.template`.
   - Current line uses default access logging: `access_log /logs/nginx-access.log;`.
   - Why it matters: backend structured logs and audit middleware intentionally avoid query strings, but nginx default access logs usually log the full request line (`$request`), which includes the URI and query string. If any token, one-time value, callback parameter, or operator-supplied sensitive value appears in a query string, it may be persisted under the local `/logs` bind mount.
   - Scope fit: local-only; deployment/runtime log surface; can be fixed without changing API response shapes, UI behavior, routes, bind mounts, image name, port, or healthcheck.

2. **Docker volume API intentionally returns volume names and mountpoints.**
   - Location: `backend/internal/api/handlers/docker_handler.go`, `web/src/lib/api/docker-api.ts`, `web/src/components/docker-volumes-panel.tsx`.
   - Why it matters: names/mountpoints can reveal host/container filesystem layout or tenant/application naming.
   - Why not a compatible fix in this scope: these fields are the existing authenticated API/UI contract and are required for the Docker volumes panel and “use path” flow. Removing/masking them would violate “preserve API/deployment/UI behavior”. Audit persistence already avoids Docker volume names/output.

3. **GitHub Actions deploy workflow can copy container logs to an external provider on failure.**
   - Location: `.github/workflows/deploy.yml` failure path prints `docker logs xirang --tail=50 >&2`.
   - Why it matters: local runtime log output could become external CI logs.
   - Why not selected here: user explicitly constrained the review/fix to “local-only hardening; no external providers”. This should be treated as an out-of-scope caveat unless the task is later re-scoped to external provider workflows.

4. **Local backup/restore scripts print filesystem paths.**
   - Location: `scripts/backup-db.sh`, `scripts/restore-db.sh`.
   - Why it matters: paths may reveal local deployment layout in operator terminals or local cron logs.
   - Why not selected here: the output is local operator/runtime status, does not echo live DSNs/secrets, and `/backup`/`/data` are deployment-contract paths. Reducing this output could affect operability more than the residual value found.

5. **`RunSSHCommandOutput` returns raw combined output to callers.**
   - Location: `backend/internal/task/executor/executor.go`.
   - Why it matters: future callers could persist or return raw SSH command output if they bypass existing sanitizers.
   - Current status: inspected current Docker/task/hook/drill/integrity paths sanitize, discard, or summarize output before persistence/response. This is a guardrail concern rather than a demonstrated current leak in the scoped surfaces.

6. **Product-visible task/drill/config fields remain intentionally visible.**
   - Locations: `backend/internal/api/handlers/task_handler.go`, `task_run_handler.go`, `batch_handler.go`, `config_handler.go`, `model/models.go`.
   - Examples: task command/source/target fields, task run last_error/logs, restore drill sandbox node/path, Docker volume name/mountpoint, admin `include_secrets=true` config export.
   - Current status: diagnostic/error/log fields have producer-side sanitization; configuration/evidence identity fields are part of existing API/UI behavior. Masking them broadly would violate the task constraint unless a concrete raw secret/evidence leak is proven.

### Smallest Compatible Fix If Any

**Best local-only behavior-compatible hardening slice found:** change the nginx access log format to omit query strings while preserving the same log file path and core operational fields.

- Candidate implementation shape:
  - Define a custom nginx `log_format` in `deploy/nginx/templates/default.conf.template` that logs method, `$uri` (path without args), status, body bytes, referer/user-agent, forwarded-for, and timing fields as needed.
  - Change `access_log /logs/nginx-access.log;` to use that format.
- Why this is compatible:
  - Keeps `/logs/nginx-access.log` path stable.
  - Keeps nginx access logging enabled.
  - Does not change routes, API responses, UI flows, Docker image name, Compose bind mounts, ports, healthcheck, or backend behavior.
  - Narrows only local runtime log contents by removing query strings from the request-line portion.
- Test/validation likely needed if implemented:
  - Validate nginx template syntax with a `/logs` directory present.
  - Run `docker compose -f docker-compose.yml config` using deployment env setup if deployment files change.
  - Run `git diff --check`.
  - Backend tests are likely not needed for an nginx-only template change unless surrounding Go code changes.

### Recommendation

- Select the **nginx access-log query minimization** slice as the single smallest compatible local-only hardening candidate from this Docker/runtime residual review.
- Do **not** mask Docker volume `name` or `mountpoint` in API/UI as part of this task; that would break current Docker volume selection behavior and conflict with the “preserve API/deployment/UI behavior” constraint. Keep the existing audit-only redaction controls.
- Treat `.github/workflows/deploy.yml` printing `docker logs` as a documented caveat for a future external-provider hardening review, not as part of this local-only slice.
- Keep `RunSSHCommandOutput` unchanged unless a current caller is found to persist/return raw output; current inspected callers already sanitize or discard output.

### Related Specs

- `.trellis/spec/backend/deployment-runtime.md` — official Docker Compose/all-in-one image runtime contract; any nginx/deploy fix must preserve image name, port `10761`, bind mounts, `/logs`, and healthcheck behavior.
- `.trellis/spec/backend/logging-guidelines.md` — logging contract explicitly forbids secrets, decrypted values, Docker command output/volume names, diagnostic evidence, executor config, command output, and remote evidence in logs.
- `.trellis/spec/backend/error-handling.md` — response/error contract forbids exposing raw Docker output, diagnostic evidence, command output, SFTP/file contents, exported config payloads, raw SQL, and stack-like details to clients.
- `.trellis/tasks/05-24-p4-residual-security-review/prd.md` — active task constraints: local-only, no external providers, preserve API/deployment/UI behavior, select at most one executable hardening slice.

## Caveats / Not Found

- No current scoped path was found where Docker command output is persisted to credential audit or returned to API clients; tests already cover Docker audit redaction.
- No current scoped task/executor/verifier path was found that persists non-empty raw runtime output without sanitization; task logs and TaskRun last_error are sanitized at producer/sink boundaries.
- The review did not implement changes; this file records research only.
- Some inspected workflow files are external-provider surfaces and therefore not selected for local-only hardening.
