# Research: P4 residual alternatives

- **Query**: Research the remaining P4 residual candidate surfaces for the active Trellis task: file browser/process logs, snapshot indexer/restic find raw output, Docker/nginx query logging. Compare these against AppCredential rendered hook hardening and identify which should be excluded from this slice.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/profile/profile.go` | Defines built-in AppCredential application profiles and renders decrypted config into pre/post hook command strings. |
| `backend/internal/profile/app_profile_access.go` | Resolves encrypted `AppCredential.Config` and returns copied decrypted config plus safe metadata. |
| `backend/internal/api/handlers/app_credential_handler.go` | Sanitizes AppCredential API responses and cascades policy hook re-rendering when credentials change. |
| `backend/internal/api/handlers/policy_handler.go` | Creates/updates policies, validates user-supplied hooks, and auto-renders AppCredential profile hooks. |
| `backend/internal/task/runner.go` | Emits hook lifecycle task logs and executes stored `Policy.PreHook` / `Policy.PostHook`. |
| `backend/internal/task/hook.go` | Runs hook commands over purpose-scoped SSH (`task_hook`) and sanitizes hook execution errors. |
| `backend/internal/task/runtime_sanitize.go` | Sanitizes task runtime evidence, command lifecycle text, paths, hosts, URLs, and output markers. |
| `backend/internal/task/log_writer.go` | Sanitizes task logs before persistence and websocket publication. |
| `backend/internal/api/handlers/task_run_handler.go` | Sanitizes task run errors and task logs again on API read. |
| `backend/internal/api/handlers/task_handler.go` | Serves task logs through sanitized response helpers. |
| `backend/internal/api/handlers/file_handler.go` | Implements SFTP file browser; uses purpose-scoped SSH, path validation, and safe credential audit path hashes. |
| `backend/internal/api/handlers/file_handler_validate_test.go` | Contains regression coverage that file browser audit metadata excludes raw path/content/output. |
| `backend/internal/nodelogs/fetcher.go` | Builds and runs remote journal/file log fetch scripts. |
| `backend/internal/nodelogs/parser.go` | Parses node logs and sanitizes path/message before row creation. |
| `backend/internal/nodelogs/sanitize.go` | Hashes node log paths and replaces message bodies with placeholders. |
| `backend/internal/nodelogs/ssh_runner.go` | Runs node log collection with purpose-scoped SSH and bounded credential audit metadata. |
| `backend/internal/api/handlers/node_logs_handler.go` | Sanitizes node-log rows again before responses and filters by sanitized path key. |
| `backend/internal/snapshot/indexer.go` | Indexes restic snapshot files; `restic find` failure currently includes trimmed raw command output in returned error. |
| `backend/internal/task/executor/restic_executor.go` | Restic executor list/snapshot paths already hide command output on errors. |
| `backend/internal/task/executor/runtime_sanitize.go` | Executor output sanitizer returns `[输出已隐藏]` for non-empty output. |
| `backend/internal/api/handlers/snapshot_search_handler.go` | Triggers snapshot indexing and returns stored snapshot file paths for authorized task readers. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot browser returns file entries/paths for authorized task readers. |
| `web/src/lib/api/files-api.ts` | Sends file-browser `path` values in query strings. |
| `web/src/lib/api/snapshots-api.ts` | Sends snapshot browser `path` and snapshot search `q` values in query strings. |
| `web/src/lib/ws/logs-socket.ts` | WebSocket logs URL includes `task_id`/`since_id` query parameters only, not auth tokens. |
| `deploy/nginx/templates/default.conf.template` | All-in-one Nginx template enables `/logs/nginx-access.log` with default access-log format. |
| `docker-compose.yml` | Maps container `/logs` to host `./logs`. |
| `deploy/allinone/Dockerfile` | All-in-one image is based on Nginx and exposes `/logs`. |
| `backend/internal/middleware/structured_logger.go` | Backend HTTP access logger records `URL.Path`, not raw query string. |
| `backend/internal/middleware/audit.go` | Hash-chained audit middleware uses route/full path and skips GET/HEAD/OPTIONS. |
| `backend/internal/api/router.go` | Shows RBAC/ownership protection for file browser, node logs, task logs, snapshots, and AppCredential routes. |
| `backend/internal/sshutil/scope.go` | Defines stable SSH purpose strings and validates managed SSH key scope. |
| `.trellis/spec/backend/quality-guidelines.md` | Related security requirements for least-privilege SSH scope and credential audit event hygiene. |
| `.trellis/spec/backend/logging-guidelines.md` | Related logging requirements forbidding secrets, decrypted hook values, raw command output, file contents, and unsafe audit metadata in logs. |

### Code Patterns

#### AppCredential rendered hook hardening baseline

- `model.Policy` persists hook command text as `pre_hook` / `post_hook`, while `model.AppCredential.Config` is encrypted and omitted from normal JSON responses (`backend/internal/model/models.go:94`, `backend/internal/model/models.go:155`, `backend/internal/model/models.go:188`).
- Built-in application profile templates interpolate config fields, including password-bearing fragments, directly into hook commands (`backend/internal/profile/profile.go:55`, `backend/internal/profile/profile.go:65`, `backend/internal/profile/profile.go:75`, `backend/internal/profile/profile.go:85`, `backend/internal/profile/profile.go:95`, `backend/internal/profile/profile.go:115`, `backend/internal/profile/profile.go:125`). Examples include MySQL `-p'{{.password}}'`, Postgres `PGPASSWORD='{{.password}}'`, Mongo `--password '{{.password}}'`, and Redis `-a '{{.password}}'`.
- `RenderHooks` renders Go `text/template` strings using decrypted config (`backend/internal/profile/profile.go:145`, `backend/internal/profile/profile.go:147`, `backend/internal/profile/profile.go:152`, `backend/internal/profile/profile.go:156`, `backend/internal/profile/profile.go:164`).
- Policy create/update auto-renders hooks when `app_profile` is set and hooks were not user-provided (`backend/internal/api/handlers/policy_handler.go:242`, `backend/internal/api/handlers/policy_handler.go:271`, `backend/internal/api/handlers/policy_handler.go:567`, `backend/internal/api/handlers/policy_handler.go:594`). User-provided hooks are validated separately (`backend/internal/api/handlers/policy_handler.go:206`, `backend/internal/api/handlers/policy_handler.go:542`, `backend/internal/api/handlers/policy_handler.go:968`, `backend/internal/api/handlers/policy_handler.go:996`).
- Credential update cascade re-renders hooks for referencing policies and saves new rendered hook strings when stored hooks still match old rendered values (`backend/internal/api/handlers/app_credential_handler.go:288`, `backend/internal/api/handlers/app_credential_handler.go:299`, `backend/internal/api/handlers/app_credential_handler.go:302`, `backend/internal/api/handlers/app_credential_handler.go:309`, `backend/internal/api/handlers/app_credential_handler.go:313`).
- Hook execution reads the persisted command string and runs it over SSH purpose `task_hook`; lifecycle logs do not include the raw command, and errors go through task runtime sanitization (`backend/internal/task/runner.go:321`, `backend/internal/task/runner.go:325`, `backend/internal/task/runner.go:408`, `backend/internal/task/hook.go:12`, `backend/internal/task/hook.go:17`, `backend/internal/task/hook.go:23`, `backend/internal/task/hook.go:25`).

Conclusion for baseline: AppCredential rendered hook hardening is distinct because decrypted credential values can be materialized into durable policy hook command text. That is narrower and more direct than the alternate residual surfaces below.

#### File browser / process logs candidate

- File browser APIs are RBAC/ownership guarded (`backend/internal/api/router.go:164`, `backend/internal/api/router.go:165`) and use purpose-scoped SFTP credentials (`backend/internal/api/handlers/file_handler.go:356`, `backend/internal/api/handlers/file_handler.go:357`; `backend/internal/sshutil/scope.go:22`, `backend/internal/sshutil/scope.go:129`).
- File browser credential-audit metadata stores hashed paths rather than raw paths (`backend/internal/api/handlers/file_handler.go:96`, `backend/internal/api/handlers/file_handler.go:98`, `backend/internal/api/handlers/file_handler.go:129`, `backend/internal/api/handlers/file_handler.go:132`, `backend/internal/api/handlers/file_handler.go:181`, `backend/internal/api/handlers/file_handler.go:183`, `backend/internal/api/handlers/file_handler.go:250`, `backend/internal/api/handlers/file_handler.go:253`).
- File browser audit writer records purpose `file_browser` and bounded metadata (`backend/internal/api/handlers/file_handler.go:333`, `backend/internal/api/handlers/file_handler.go:341`). Regression coverage asserts audit rows do not persist raw path/content/output (`backend/internal/api/handlers/file_handler_validate_test.go:328`, `backend/internal/api/handlers/file_handler_validate_test.go:343`, `backend/internal/api/handlers/file_handler_validate_test.go:346`, `backend/internal/api/handlers/file_handler_validate_test.go:356`).
- Node/process log collection uses purpose `node_logs` (`backend/internal/nodelogs/ssh_runner.go:31`, `backend/internal/nodelogs/ssh_runner.go:92`; `backend/internal/sshutil/scope.go:24`) and stores sanitized parsed rows. Journal unit/path values become hashed log-path labels, while message bodies become `[日志内容已隐藏]` (`backend/internal/nodelogs/parser.go:72`, `backend/internal/nodelogs/parser.go:75`, `backend/internal/nodelogs/parser.go:103`, `backend/internal/nodelogs/parser.go:106`, `backend/internal/nodelogs/sanitize.go:37`, `backend/internal/nodelogs/sanitize.go:53`).
- Node-log APIs are RBAC guarded (`backend/internal/api/router.go:177`) and sanitize again on read (`backend/internal/api/handlers/node_logs_handler.go:82`, `backend/internal/api/handlers/node_logs_handler.go:194`, `backend/internal/api/handlers/node_logs_handler.go:201`, `backend/internal/api/handlers/node_logs_handler.go:202`).
- Task/process logs are sanitized on write and again on read (`backend/internal/task/log_writer.go:65`, `backend/internal/task/runtime_sanitize.go:24`, `backend/internal/task/runtime_sanitize.go:28`, `backend/internal/api/handlers/task_handler.go`, `backend/internal/api/handlers/task_run_handler.go`).

Comparison: file browser intentionally returns file paths/content to authorized users as product behavior, while its credential-audit trail is already hashed/bounded. Node/process logs are separate SSH/runtime evidence surfaces with sanitizers and purpose-scoped credentials. They do not persist decrypted AppCredential values as policy commands. Exclude from this AppCredential rendered-hook slice.

#### Snapshot indexer / `restic find` raw output candidate

- Snapshot indexing uses purpose-scoped SSH (`snapshot` purpose is defined in `backend/internal/sshutil/scope.go:28`) and builds a `restic find --json --long --path=/ ... 2>&1` command (`backend/internal/snapshot/indexer.go:189`).
- On command failure, `indexSnapshot` returns an error containing trimmed raw `restic find` output: `restic find 执行失败: %w, 输出: %s` (`backend/internal/snapshot/indexer.go:196`). Parsed successful output is used to create `SnapshotFileIndex` records (`backend/internal/snapshot/indexer.go:199`, `backend/internal/snapshot/indexer.go:205`, `backend/internal/snapshot/indexer.go:207`, `backend/internal/snapshot/indexer.go:226`, `backend/internal/snapshot/indexer.go:228`).
- Snapshot search triggers indexing and then returns distinct stored paths, sizes, and mtimes to authorized task readers (`backend/internal/api/handlers/snapshot_search_handler.go:65`, `backend/internal/api/handlers/snapshot_search_handler.go:78`; routes at `backend/internal/api/router.go:317`, `backend/internal/api/router.go:318`, `backend/internal/api/router.go:321`).
- Other restic executor list/snapshot operations already sanitize command output on errors (`backend/internal/task/executor/restic_executor.go:301`, `backend/internal/task/executor/restic_executor.go:333`, `backend/internal/task/executor/runtime_sanitize.go`).

Comparison: this is a real adjacent residual because raw command output can cross an error boundary, and snapshot paths are intentionally stored/searchable. However, it is restic/snapshot-specific and not tied to decrypted AppCredential rendering into durable hook text. Behavior-compatible error-output hiding can be tracked separately; broad path sanitization would change snapshot search/browse behavior. Exclude from this AppCredential rendered-hook slice unless the slice is explicitly broadened beyond AppCredential hooks.

#### Docker / Nginx query logging candidate

- The all-in-one Nginx template enables access logs at `/logs/nginx-access.log` without a custom `log_format` (`deploy/nginx/templates/default.conf.template:19`). Default Nginx access log format includes the request line, which normally includes the query string.
- Docker compose maps `/logs` to host `./logs` (`docker-compose.yml`), and the all-in-one image is Nginx-based with `/logs` as a volume (`deploy/allinone/Dockerfile`).
- Frontend file-browser APIs send `path` in query strings (`web/src/lib/api/files-api.ts:33`, `web/src/lib/api/files-api.ts:46`, `web/src/lib/api/files-api.ts:48`, `web/src/lib/api/files-api.ts:59`). Snapshot APIs send `path` and search query `q` in query strings (`web/src/lib/api/snapshots-api.ts:38`, `web/src/lib/api/snapshots-api.ts:52`, `web/src/lib/api/snapshots-api.ts:60`). Logs websocket query includes only `task_id`/`since_id`, not auth tokens (`web/src/lib/ws/logs-socket.ts:210`).
- Backend structured HTTP access logging records `c.Request.URL.Path`, not `RawQuery` (`backend/internal/middleware/structured_logger.go:15`). Hash-chained audit middleware skips GET/HEAD/OPTIONS and falls back to route/full path rather than raw query (`backend/internal/middleware/audit.go:37`).

Comparison: query logging is deployment/logging configuration debt, not an AppCredential rendered-hook persistence issue. It can expose path/search terms in Nginx access logs, but the inspected client surfaces do not place auth tokens in those query strings. Exclude from this AppCredential rendered-hook slice.

### External References

- Not used. The request was local-only, and all findings are based on repository code/spec inspection.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Sensitive surfaces should fail closed; SSH managed-key use sites must call purpose-aware helpers; credential audit events must never persist raw secrets, file contents, command output, Docker output/volume names, diagnostic evidence, exported payloads, or full command text (`quality-guidelines.md:172`, `quality-guidelines.md:224`, `quality-guidelines.md:229`, `quality-guidelines.md:236`, `quality-guidelines.md:239`, `quality-guidelines.md:398`, `quality-guidelines.md:400`).
- `.trellis/spec/backend/logging-guidelines.md` — HTTP access logs should include method/path/status/latency/client IP; logs must not include passwords, decrypted hook values, full command output that may contain credentials, SFTP file contents, Docker output/volume names, executor config, or unsafe credential audit metadata (`logging-guidelines.md:41`, `logging-guidelines.md:70`, `logging-guidelines.md:73`, `logging-guidelines.md:75`, `logging-guidelines.md:78`).

## Recommendation

Keep the P4 slice limited to AppCredential rendered hook storage/exposure hardening. Exclude file browser/process logs, snapshot indexer/restic find raw output, and Docker/nginx query logging from this slice as separate/non-AppCredential residual surfaces; snapshot indexer raw output is the closest adjacent follow-up but should be tracked separately to preserve this slice's behavior-compatible scope.

## Caveats / Not Found

- The snapshot indexer raw-output finding is adjacent and security-relevant, but not AppCredential-specific.
- Nginx query-string exposure depends on runtime use of the all-in-one/deploy Nginx config and default access-log format; backend structured logging itself uses path-only logging.
- No external documentation was consulted because the request constrained research to local-only behavior-compatible analysis.
