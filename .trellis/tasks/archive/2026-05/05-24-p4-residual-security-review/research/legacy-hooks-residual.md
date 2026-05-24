# Research: legacy evidence at-rest and AppCredential rendered hooks residual

- **Query**: Research residual P4 risk surface: legacy evidence at-rest behavior and AppCredential rendered hook persistence in `/Users/weibo/Code/xirang`. Focus on whether existing response-time masking makes DB backfill unnecessary, and whether persisted profile-generated hooks still contain credential-derived command text or secret/path/endpoint material in API responses/storage after the AppCredential resolver seam.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Defines `Policy`, `AppCredential`, `TaskRun`, and `RestoreDrillEvidence`; AppCredential config is encrypted/masked, policy hook fields and drill evidence fields are persisted model state. |
| `backend/internal/profile/app_profile_access.go` | AppCredential resolver seam (`ResolveAppProfileAccess`, `AppProfileAccess.Config`, `SafeMetadata`) that hides raw config from handler metadata/JSON. |
| `backend/internal/profile/profile.go` | App-aware profile definitions and `RenderHooks`; profile templates render command text from credential config. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update uses resolver seam but persists rendered hooks; policy responses return `pre_hook` and `post_hook`. |
| `backend/internal/api/handlers/app_credential_handler.go` | AppCredential responses use sanitized config; credential update cascades newly rendered hooks into linked policies. |
| `backend/internal/api/handlers/hook_templates_handler.go` | Deprecated generic hook templates endpoint; exposes placeholder templates, not AppCredential-rendered hook state. |
| `backend/internal/api/handlers/config_handler.go` | Config export policy section omits policy hook fields in inspected export path. |
| `backend/internal/api/router.go` | Route/RBAC boundaries for AppCredentials, policies, task runs, and task logs. |
| `backend/internal/middleware/rbac.go` | Role permissions show AppCredential read/write is admin-only, while policy/task read is broader. |
| `backend/internal/task/runner.go` | Hook execution uses persisted policy hook strings at runtime; current runtime log messages avoid echoing full hook command text. |
| `backend/internal/task/hook.go` | Executes hook command over SSH; errors are wrapped and sanitized through task runtime sanitizer. |
| `backend/internal/task/log_writer.go` | New task logs are sanitized before queued storage/WebSocket publication. |
| `backend/internal/task/runtime_sanitize.go` | Shared task runtime sanitizer for command/output/path/host-sensitive evidence used on new runtime writes/errors. |
| `backend/internal/util/sanitize.go` | Base sanitizer for private-key blocks, URLs, tokens, key/value secret markers, selected host errors, and length. |
| `backend/internal/api/handlers/task_run_handler.go` | Task-run detail and log endpoints return stored `TaskRun`, `RestoreDrillEvidence`, and `TaskLog` rows directly. |
| `backend/internal/task/drill.go` | Restore drill evidence writer stores sandbox identity fields by design and sanitizes current error writes. |
| `backend/internal/api/handlers/backup_confidence_handler.go` | Backup confidence evidence summaries minimize task/drill detail and do not expose stored raw error text. |
| `backend/internal/api/handlers/integration_app_aware_test.go` | Tests document that rendered hooks include credential material for execution behavior. |
| `backend/internal/api/handlers/app_credential_handler_test.go` | Tests AppCredential config encryption/API masking and policy hook cascade persistence. |
| `backend/internal/profile/app_profile_access_test.go` | Tests resolver seam safety, safe metadata, and JSON non-exposure of raw config. |
| `backend/internal/task/drill_test.go` | Tests current drill error sanitization/audit safety. |
| `backend/internal/api/handlers/task_run_handler_test.go` | Tests task-run detail returns stored drill evidence fields. |
| `web/src/lib/api/policies-api.ts` | Frontend maps backend `pre_hook`/`post_hook` directly into policy objects and sends hook fields on create/update. |
| `web/src/components/policy-editor-dialog.tsx` | Policy editor renders and edits hook text in advanced settings. |
| `web/src/components/credential-editor-dialog.tsx` | Credential editor receives sanitized credential config and keeps password empty on edit to preserve existing password. |
| `web/src/lib/api/task-runs-api.ts` | Frontend maps `drill_evidence` and task-run fields directly. |
| `web/src/components/task-run-detail.tsx` | Task-run detail UI renders drill evidence path/name/error fields received from backend. |
| `backend/internal/database/migrations/sqlite/000022_policy_hooks.up.sql` | Adds plaintext policy hook columns for SQLite. |
| `backend/internal/database/migrations/postgres/000022_policy_hooks.up.sql` | Adds plaintext policy hook columns for PostgreSQL. |
| `backend/internal/database/migrations/sqlite/000051_app_credentials_and_policy_profile.up.sql` | Adds app credentials and policy profile linkage for SQLite. |
| `backend/internal/database/migrations/postgres/000051_app_credentials_and_policy_profile.up.sql` | Adds app credentials and policy profile linkage for PostgreSQL. |
| `backend/internal/database/migrations/sqlite/000058_restore_drill_evidence.up.sql` | Adds restore drill evidence table with sandbox identity and error fields for SQLite. |
| `backend/internal/database/migrations/postgres/000058_restore_drill_evidence.up.sql` | Adds restore drill evidence table with sandbox identity and error fields for PostgreSQL. |
| `.trellis/spec/backend/error-handling.md` | Backend contract for sanitized API errors and restore drill evidence response semantics. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security contract for evidence/output sanitization and credential audit limits. |
| `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/research/profile-hook-app-credential-flow.md` | Prior profile/AppCredential research; current code has since added resolver seam, but hook persistence risk remains. |
| `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/research/task-runtime-log-sanitization.md` | Prior runtime log hardening research; historical task log rewriting was explicitly deferred. |
| `.trellis/tasks/archive/2026-05/05-24-p4-restic-repo-password-resolver/research/diagnostic-evidence-surfaces.md` | Parallel diagnostic-evidence analysis treating API responses/frontend state as evidence surfaces. |

### Code Patterns

#### AppCredential resolver seam is present and narrows direct credential access

- `backend/internal/profile/app_profile_access.go` defines `ResolveAppProfileAccess(db, appCredentialID)` and returns `AppProfileAccess` with unexported `config` plus exported safe labels (`Provider`, `Kind`, `Source`). `Config()` returns a copy, and `SafeMetadata()` returns only provider/kind/source.
- `backend/internal/profile/app_profile_access_test.go` verifies encrypted-at-rest credential config is resolved only through the seam, safe metadata excludes password/host/config/credential/command/output style values, JSON marshaling does not expose raw config, and invalid config errors are safe.
- `backend/internal/api/handlers/policy_handler.go` now uses `profile.ResolveAppProfileAccess` in policy create/update instead of directly flowing `model.AppCredential.Config` through handler logic.

Assessment: this seam is effective for handler metadata/serialization risk, but it is not itself a storage/output sanitizer for rendered hook strings produced from the resolved config.

#### AppCredential API responses are masked, but credential-derived hook state is separate

- `backend/internal/model/models.go` stores `AppCredential.Config` with `json:"-"` and encrypts/decrypts it through `BeforeSave` / `AfterFind` hooks.
- `AppCredential.SanitizedConfig()` deletes the `password` key before response use.
- `backend/internal/api/handlers/app_credential_handler.go` uses `sanitizeAppCredential`, which returns sanitized config, `has_password`, metadata, timestamps, and reference count.
- `backend/internal/api/handlers/app_credential_handler_test.go` verifies DB config is encrypted and API response omits password.

Assessment: AppCredential CRUD is not the residual issue. The residual issue is the policy hook text derived from AppCredential config after resolution.

#### Profile-generated hooks still render credential-derived command text

- `backend/internal/profile/profile.go` keeps profile templates as non-JSON fields (`PreHookTemplate`, `PostHookTemplate` use `json:"-"`), so profiles API does not expose the templates directly.
- `RenderHooks(profileID, config)` renders both templates with the resolved credential config. The inspected profile templates use credential/config values such as database user, password, host, port, container name, and temporary file paths in command text.
- `backend/internal/api/handlers/integration_app_aware_test.go` explicitly documents that rendered hooks include credential material for command execution behavior.

Assessment: rendering secrets into the command string at execution time is currently intentional product behavior. Persisting and returning those generated command strings is a separable leakage surface.

#### Policy hook fields are persisted plaintext and returned by policy APIs

- `backend/internal/model/models.go` defines plaintext `Policy.PreHook` / `Policy.PostHook` fields with JSON names `pre_hook` / `post_hook`. No policy-level encryption hook for these fields was found.
- `backend/internal/database/migrations/sqlite/000022_policy_hooks.up.sql` and `backend/internal/database/migrations/postgres/000022_policy_hooks.up.sql` add hook columns as `TEXT NOT NULL DEFAULT ''`.
- `backend/internal/api/handlers/policy_handler.go` persists rendered hooks on create/update when app profile and credential are selected. `buildPolicyResponse` returns persisted `pre_hook` and `post_hook` values directly.
- `backend/internal/api/handlers/app_credential_handler.go` cascades app credential updates by comparing old/new rendered hooks and saving new hook strings into linked policies when the policy still matches the old generated hook.
- `backend/internal/api/handlers/app_credential_handler_test.go` verifies that updating an app credential updates linked policy hook text to the newly rendered credential-derived command.

Assessment: after the resolver seam, profile-generated hooks can still contain credential-derived command text and host/path/endpoint-like material in both storage (`policies.pre_hook`, `policies.post_hook`) and API responses (`pre_hook`, `post_hook`).

#### RBAC makes policy hook responses broader than AppCredential responses

- `backend/internal/api/router.go` protects AppCredential routes with `app_credentials:read/write`, while policy list/get routes use `policies:read`.
- `backend/internal/middleware/rbac.go` grants `app_credentials:read/write` to admin, but grants `policies:read` and `tasks:read` to admin/operator/viewer.
- `web/src/lib/api/policies-api.ts` maps backend `pre_hook` / `post_hook` directly to frontend policy state.
- `web/src/components/policy-editor-dialog.tsx` renders hook textareas from policy state.

Assessment: app credential password masking is admin-only and safe, but generated hook responses can reach any principal with policy read access (subject to existing route/ownership behavior). This widens the blast radius for credential-derived hook text beyond AppCredential-admin surfaces.

#### Hook execution semantics currently depend on persisted policy hook strings

- `backend/internal/task/runner.go` executes `taskEntity.Policy.PreHook` and `taskEntity.Policy.PostHook` at runtime while logging only generic pre/post hook lifecycle text.
- `backend/internal/task/hook.go` runs the passed hook command over SSH and sanitizes execution errors.

Assessment: changing storage representation for generated hooks would require careful runtime re-rendering or compatibility logic. The smallest compatible slice must avoid breaking manual hooks, app-profile hook execution, and frontend update roundtrips.

#### Current runtime hardening sanitizes new task logs/errors but not all legacy reads

- `backend/internal/task/log_writer.go` sanitizes new task log messages before enqueue/storage/publication through `sanitizeTaskLogMessage`.
- `backend/internal/task/runtime_sanitize.go` hides output markers, command lifecycle text, URLs/endpoints, remote/named/absolute/Windows paths, IPv4/hostnames, and host-sensitive fragments after applying `util.SanitizeMessage`.
- `backend/internal/task/runner.go`, `backend/internal/task/hook.go`, and `backend/internal/task/drill.go` use sanitizer helpers for current runtime errors/logs/drill error writes.
- Prior archived research (`05-23-p4-task-runtime-log-sanitization`) explicitly deferred historical task log rewriting.

Assessment: new runtime writes are substantially safer, but this does not prove legacy stored rows are safe at response time.

#### Existing response-time masking is partial, not comprehensive enough to make DB backfill unnecessary today

- `backend/internal/api/handlers/backup_confidence_handler.go` uses minimized evidence messages for confidence summaries: it reports status/verification state and generic “error recorded” style summaries instead of raw `TaskRun.LastError` or drill error text.
- `backend/internal/api/handlers/task_run_handler.go` returns task-run detail as `taskRunDetailResponse{TaskRun: run, DrillEvidence: evidencePtr}` after loading stored rows. No response-time sanitizer was found on `TaskRun.LastError`, `TaskRun.Output`, or loaded `RestoreDrillEvidence` error/path fields in this handler.
- The same handler’s logs endpoint loads `[]model.TaskLog` and returns it directly via `respondOK(c, logs)`. No response-time sanitizer was found for `TaskLog.Message` on this endpoint.
- `web/src/lib/api/task-runs-api.ts` and `web/src/components/task-run-detail.tsx` trust and render task-run/drill-evidence fields from the backend.

Assessment: response-time masking currently exists on some aggregate/summary surfaces, but not on task-run detail and task-log read boundaries. Therefore existing code does not currently make DB backfill unnecessary for all legacy evidence at-rest rows. A read-boundary sanitizer could make backfill unnecessary while preserving schema and avoiding historical rewrites, but that sanitizer is not present on the key read endpoints today.

#### Restore drill evidence has both intended identity fields and sanitizable error evidence

- `.trellis/spec/backend/error-handling.md` defines restore drill evidence as a structured task-run detail payload and includes identity fields such as sandbox node/path. That makes some returned identity fields current API/product behavior.
- `backend/internal/task/drill.go` stores sandbox node/name/path and snapshot/source identity fields by design. It sanitizes current restore/verify/post-verify/cleanup error writes.
- `backend/internal/api/handlers/task_run_handler.go` returns loaded drill evidence directly; legacy rows with raw error text would be returned unless sanitized at read time.

Assessment: sandbox identity fields are product/API behavior under the current spec, although they are host/path-sensitive under the residual-review PRD’s stricter target. Drill error fields are fixable leakage if raw legacy values remain and are returned without read-boundary sanitization.

### Remaining Risks

1. **Profile-generated hook persistence leakage**
   - Remaining risk: `policies.pre_hook` / `policies.post_hook` can store command strings rendered from AppCredential config, including credential-derived command text plus host/path/container/endpoint-like material.
   - Exposure: policy list/get responses return these fields directly; frontend policy editor stores/renders them.
   - Why resolver seam did not close it: the seam narrows raw credential object handling, but `RenderHooks(access.Config())` still materializes the credential into a command string, and handlers save/return that string.

2. **Legacy task logs and task-run detail evidence on read**
   - Remaining risk: historical `TaskLog.Message`, `TaskRun.LastError`/output-like fields, and `RestoreDrillEvidence` error fields can be returned raw by detail/log endpoints because those read paths return stored rows directly.
   - Current write-path status: new runtime log/error/drill writes are sanitized.
   - Response-time status: aggregate confidence responses minimize evidence, but task-run detail/log responses do not apply equivalent read-boundary sanitization.

3. **Restore drill identity fields**
   - Remaining risk: `sandbox_path`, `sandbox_node_name`, and related identity fields are returned as part of structured drill evidence.
   - Product/API behavior: current backend spec describes these fields as part of the task-run detail evidence object, so suppressing them would be broader than a simple legacy read sanitizer unless the spec is intentionally tightened.

4. **Frontend state/rendering is not an independent sanitizer**
   - Policy and task-run clients/components map backend fields directly. If backend returns raw hook/evidence text, the UI will store/render it.

### Product Behavior vs Fixable Leakage

| Surface | Classification | Rationale |
|---|---|---|
| Rendering credential material into a hook command for execution | Intentionally product behavior today | Existing tests state the hook must include the password/material because the target command executes that way; user constraint says avoid changing hook execution semantics. |
| Persisting generated AppCredential hook command text in `Policy.PreHook` / `Policy.PostHook` | Fixable leakage | Execution needs a command at runtime, but the API/storage boundary can be changed to avoid storing or returning generated credential-derived text raw if runtime re-rendering or masked response/roundtrip guards are added. |
| Returning generated hook text from policy APIs | Fixable leakage | API field names can remain stable while values are masked/minimized for generated hooks; update handling must prevent masked values from overwriting real manual/generated hooks. |
| Manual user-authored hooks stored in policy fields | Product behavior with inherent sensitivity | Manual hooks are an existing policy feature; masking all manual hook responses may affect editor behavior and roundtrips. This is broader than the generated AppCredential leakage. |
| New task runtime logs/errors/drill error writes | Mostly already hardened | Current write paths use sanitizer helpers. |
| Legacy task logs/task-run detail/drill error fields returned as stored | Fixable leakage | A response-time sanitizer on read endpoints can preserve DB schema and response shapes while avoiding backfill. |
| Restore drill sandbox path/name identity fields | Current product/API behavior, but security-sensitive | The spec describes structured evidence including sandbox identity. Removing/masking these may be justified by the residual PRD, but it is broader than error/evidence sanitization and may affect UI semantics. |
| AppCredential CRUD config response | Already hardened | Password is removed from config responses and config is encrypted at rest. |
| Config export of policy hooks | Not found in inspected export path | Policy export section omits hook fields; no AppCredential-rendered hook export was found in the inspected config export path. |

### Smallest Compatible Slice Options

#### Option A: Read-boundary sanitizer for legacy task evidence

- Add response DTO/sanitization in `backend/internal/api/handlers/task_run_handler.go` for task-run detail and logs:
  - Sanitize `TaskRun.LastError` and other user-visible runtime evidence fields before returning.
  - Sanitize `TaskLog.Message` before returning log rows.
  - Sanitize `RestoreDrillEvidence` error fields before returning.
  - Preserve field names and response shape.
- Do not mutate DB rows and do not add migrations/backfill.
- Tests should insert intentionally raw legacy rows directly, call detail/log endpoints, and assert responses are sanitized while stored rows remain untouched.

Compatibility: high. It avoids execution semantics and schema changes, and directly answers whether backfill is necessary: after this slice, read-time masking could make backfill unnecessary for these endpoints.

Limit: does not address generated hook storage/return leakage.

#### Option B: Mask generated AppCredential hook responses with roundtrip guard

- For policies linked to `app_profile` + `app_credential_id`, return masked/minimized `pre_hook` and `post_hook` values in policy responses while preserving field names.
- Add update handling so masked placeholder values from the frontend do not overwrite existing generated hooks.
- Keep runtime execution unchanged by leaving stored hook commands intact.
- Tests should prove policy list/get responses do not return raw generated credential-derived command text and that updating unrelated policy fields does not replace stored hooks with masks.

Compatibility: medium. API field names remain stable, but existing UI would show placeholders instead of full generated commands. This addresses return leakage but not storage leakage.

Limit: storage still contains generated credential-derived hooks; the user asked about storage as well as responses.

#### Option C: Stop persisting generated AppCredential hook text; re-render at execution/runtime boundary

- Store only `app_profile` + `app_credential_id` linkage for generated hooks and compute hook commands at execution time through `ResolveAppProfileAccess` + `RenderHooks`.
- Preserve manual hook semantics for policies without generated app profile hooks.
- Avoid returning generated raw hook text by response masking/minimization.
- Add compatibility logic for existing policies whose stored hooks equal generated hooks, and avoid changing custom user-authored hooks.

Compatibility: lower for a smallest slice. It likely touches policy create/update, credential cascade, task loading/execution, tests, and possibly frontend update behavior.

Security value: highest for the AppCredential hook surface because it addresses both storage and response leakage.

Limit: larger impact area and higher regression risk; may exceed “smallest compatible slice” unless this surface is chosen over legacy evidence sanitization.

### Recommendation

The strongest residual findings are:

1. **Legacy evidence read-boundary gap**: existing response-time masking does not currently make DB backfill unnecessary because task-run detail and task-log endpoints return stored rows directly. A local read-boundary sanitizer is the smallest behavior-compatible hardening slice. It preserves field names, avoids migrations/backfill, avoids execution semantics, and directly protects historical raw rows on user-visible endpoints.

2. **AppCredential rendered hook persistence**: generated hooks still contain credential-derived command text after the resolver seam, and the current implementation stores and returns them. Command-time secret material is intentional product behavior; storage/API return of generated command text is fixable leakage. However, fully fixing storage without changing execution semantics requires runtime re-rendering/compatibility logic and is likely a larger slice than legacy read-boundary sanitization. Masking only API responses is smaller but leaves storage leakage unresolved.

For this residual P4 task’s single executable slice, prefer **Option A: task-run/detail/log read-boundary sanitizer** if the goal is the smallest local-only, behavior-compatible fix that avoids DB backfill and protects legacy evidence immediately. Capture AppCredential rendered hook persistence as a confirmed residual issue and plan it as the next slice, with the likely durable direction being runtime re-rendering for generated hooks plus stable masked policy responses and update roundtrip guards.

### External References

No external references were used; this is local-only code/spec research.

### Related Specs

- `.trellis/spec/backend/error-handling.md` — API errors and restore drill evidence must avoid raw secrets/evidence; restore drill evidence is a structured task-run detail payload and currently includes sandbox identity fields.
- `.trellis/spec/backend/quality-guidelines.md` — user-visible evidence/output should use shared sanitizers; credential audit and evidence surfaces must not contain raw command output, terminal streams, file contents, diagnostic evidence, executor config, or full command text.
- `.trellis/spec/backend/database-guidelines.md` — credential/audit storage should keep only safe identifiers and sanitized metadata/errors, not raw credentials or command/output/file content.
- `.trellis/spec/backend/logging-guidelines.md` — logs must not contain decrypted values, executor config, full command output, Docker output, node diagnostic evidence, or credential-derived sensitive strings.

## Caveats / Not Found

- No response-time sanitizer was found on `TaskRunHandler.Get` or `TaskRunHandler.Logs` in the inspected code. If another middleware/serializer layer sanitizes these specific fields, it was not found during this review.
- No policy hook encryption hook was found for `Policy.PreHook` / `Policy.PostHook`; AppCredential config encryption does not cover rendered hook strings saved into policies.
- Config export policy payload in the inspected handler omits hook fields; no config export leak of rendered policy hooks was found in that path.
- The research did not modify code and did not run verification tests; findings are from static code/spec inspection.
- Restore drill sandbox identity fields are both security-sensitive under the residual PRD and documented product/API behavior in the current backend spec, so changing them should be treated as a deliberate API behavior hardening rather than an incidental sanitizer-only fix.
