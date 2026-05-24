# Research: AppCredential hook residual

- **Query**: Research remaining P4 residual security hardening candidate: AppCredential rendered hooks and related policy hook persistence/response surfaces in Xirang after previous P4 work. Compare against prior archived research if present; identify a local-only, behavior-compatible, minimal executable slice without redesigning hook behavior unless necessary.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/profile/profile.go` | Defines built-in application-aware profiles and renders pre/post hook command strings from credential config. |
| `backend/internal/profile/app_profile_access.go` | Current AppCredential resolver seam with copied config access and safe metadata labels. |
| `backend/internal/model/models.go` | Defines encrypted `AppCredential.Config`, plaintext/JSON-visible `Policy.PreHook` and `Policy.PostHook`, and `Task.Policy` response embedding. |
| `backend/internal/api/handlers/app_credential_handler.go` | Sanitizes AppCredential API responses; preserves password on omitted update; cascades credential updates into linked policy hook rows. |
| `backend/internal/api/handlers/policy_handler.go` | Renders profile hooks during policy create/update and returns `pre_hook`/`post_hook` in policy list/detail responses. |
| `backend/internal/api/handlers/task_handler.go` | Task list/detail preload `Policy` and return serialized `model.Task`; task logs are sanitized on read. |
| `backend/internal/api/handlers/task_run_handler.go` | Task-run list/detail/log read boundaries sanitize legacy runtime evidence and drill error fields. |
| `backend/internal/task/runner.go` | Executes persisted policy hooks at runtime; current lifecycle logs no longer include full hook command text. |
| `backend/internal/task/hook.go` | Runs hook commands over SSH and sanitizes hook execution error text. |
| `backend/internal/task/log_writer.go` | Sanitizes task log messages before DB persistence and live publish. |
| `backend/internal/task/runtime_sanitize.go` | Exposes `SanitizeRuntimeEvidenceForRead` for API read-boundary sanitization. |
| `backend/internal/ws/hub.go` | WebSocket task-log backfill sanitizes stored legacy messages before replay. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import omits AppCredential records and policy hook/app-profile fields in inspected paths. |
| `backend/internal/middleware/rbac.go` | Grants AppCredential routes only to admin, while policy/task read is granted to admin/operator/viewer. |
| `backend/internal/api/router.go` | Registers AppCredential, policy, task, task-run, and hook-template routes with RBAC. |
| `web/src/lib/api/policies-api.ts` | Frontend policy mapper carries `pre_hook`/`post_hook` into policy editor state and sends them on create/update. |
| `web/src/lib/api/tasks-api.ts` | Frontend task mapper reads only nested policy id/name, not nested policy hook fields. |
| `docs/admin/backup-recovery.md` | Documents current behavior: credentials are encrypted and API credentials omit passwords, but rendered hooks are visible to authorized users. |
| `.trellis/spec/backend/logging-guidelines.md` | Says decrypted model-hook values and full command output that may contain credentials must not be logged. |
| `.trellis/spec/backend/database-guidelines.md` | Requires response sanitizers for secret-bearing model values and no raw credential/output evidence in audit storage. |
| `.trellis/spec/backend/quality-guidelines.md` | RBAC and credential-audit contracts for fail-closed sensitive surfaces and safe metadata. |
| `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/research/profile-hook-app-credential-flow.md` | Earlier research before the current AppProfileAccess seam; identified rendered hook persistence/log exposure. |
| `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/research/legacy-hooks-residual.md` | Earlier residual review; identified legacy evidence read-boundary gaps and AppCredential hook persistence. |
| `.trellis/tasks/archive/2026-05/05-24-p4-appcredential-hook-hardening/research/appcredential-rendered-hooks.md` | Earlier AppCredential-hook research; recommended WebSocket backfill sanitization as the smallest compatible slice. |

### Code Patterns

#### 1. AppCredential resolver seam now exists, but rendered hook strings still materialize the config

`backend/internal/profile/app_profile_access.go:20-89` defines `AppProfileAccess` with unexported config, copied `Config()` access, `HasPassword()`, and `SafeMetadata()` returning only provider/kind/source labels. `ResolveAppProfileAccess(db, appCredentialID)` loads `model.AppCredential` and parses the decrypted config through this seam.

This addresses the earlier direct raw-config handler flow noted in archived research, but rendering still calls `profile.RenderHooks(appProfile, access.Config())` in policy create/update paths.

#### 2. Profile templates intentionally render credential-derived command text

`backend/internal/profile/profile.go:47-128` defines the built-in profiles. The templates interpolate user/password/host/port/container values into command strings, for example:

```go
PreHookTemplate: `mysqldump{{if .user}} -u {{.user}}{{end}}{{if .password}} -p'{{.password}}'{{end}}{{if .host}} -h {{.host}}{{end}}{{if .port}} -P {{.port}}{{end}} --all-databases --single-transaction --routines --triggers > /tmp/xirang-mysql-backup.sql`,
```

```go
PreHookTemplate: `{{if .password}}PGPASSWORD='{{.password}}' {{end}}su - postgres -c 'pg_dumpall{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .user}} -U {{.user}}{{end}} > /tmp/xirang-pg-backup.sql'`,
```

```go
PreHookTemplate: `redis-cli{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .password}} -a '{{.password}}'{{end}} BGSAVE && sleep 2 && cp /var/lib/redis/dump.rdb /tmp/xirang-redis-backup.rdb`,
```

`RenderHooks` at `backend/internal/profile/profile.go:145-174` uses Go `text/template` and returns concrete pre/post hook strings. Current tests and docs treat this plaintext command materialization as execution behavior, not merely display behavior.

#### 3. AppCredential CRUD responses remain sanitized

`backend/internal/model/models.go:152-196` defines `AppCredential.Config` as `json:"-"`, encrypts it in `BeforeSave`, decrypts it in `AfterFind`, and removes `password` in `SanitizedConfig()`.

`backend/internal/api/handlers/app_credential_handler.go:58-78` builds response DTOs from `SanitizedConfig()`. Create/update/list/get responses therefore do not return raw credential passwords through AppCredential routes.

#### 4. Policy hook fields are plaintext, JSON-visible storage

`backend/internal/model/models.go:94-123` defines policy hook/profile fields as normal model fields:

```go
PreHook            string `gorm:"type:text;not null;default:''" json:"pre_hook"`
PostHook           string `gorm:"type:text;not null;default:''" json:"post_hook"`
HookTimeoutSeconds int    `gorm:"not null;default:300" json:"hook_timeout_seconds"`
AppProfile         string `gorm:"size:32;not null;default:''" json:"app_profile"`
AppCredentialID    *uint  `gorm:"index" json:"app_credential_id"`
```

No policy model encryption hook was found for `PreHook` or `PostHook`. Migrations `000022_policy_hooks.up.sql` add these as text columns; AppCredential config encryption does not cover the rendered hook string copied into policy rows.

#### 5. Policy create/update persists rendered hooks and policy APIs return them

`backend/internal/api/handlers/policy_handler.go:242-282` renders missing hooks on create when `app_profile` is set. The rendered strings are assigned to `model.Policy` fields at `policy_handler.go:391-394`.

`backend/internal/api/handlers/policy_handler.go:567-605` repeats the same render-on-missing-hook pattern for update and assigns the result at `policy_handler.go:744-747`.

`buildPolicyResponse` at `backend/internal/api/handlers/policy_handler.go:1082-1133` returns both hook fields directly:

```go
"pre_hook":              p.PreHook,
"post_hook":             p.PostHook,
```

This means policy list/detail responses can contain rendered AppCredential-derived command text.

#### 6. AppCredential update cascades can rewrite stored policy hook rows with newly rendered values

`backend/internal/api/handlers/app_credential_handler.go:256-288` resolves old/new config maps, preserves the old password when update omits password, saves the credential, then calls `cascadePolicyHooks`.

`cascadePolicyHooks` at `backend/internal/api/handlers/app_credential_handler.go:299-333` re-renders old/new hooks and saves `p.PreHook`/`p.PostHook` when the current hook still equals the previous generated value:

```go
if p.PreHook == oldPre && newPre != oldPre {
	p.PreHook = newPre
	needsSave = true
}
if p.PostHook == oldPost && newPost != oldPost {
	p.PostHook = newPost
	needsSave = true
}
```

This preserves manual overrides, but it also keeps auto-generated policy hook rows synchronized with credential-derived command strings.

#### 7. Runtime log/error hardening from previous P4 work is now present

The earlier archived recommendation to harden runtime evidence/backfill appears implemented in current code:

- `backend/internal/task/runner.go:314-341` and `runner.go:396-411` log generic hook lifecycle text (`执行 pre-hook`, `执行 post-hook`) and do not echo the full hook command.
- `backend/internal/task/hook.go:23-25` sanitizes hook execution errors before returning them to the runner.
- `backend/internal/task/log_writer.go:60-66` sanitizes log messages before queue/storage/live publish.
- `backend/internal/api/handlers/task_run_handler.go:24-62` defines read-boundary sanitizers for task-run last errors, task logs, and drill error fields; `ListByTask`, `Get`, and `Logs` call them at `task_run_handler.go:116`, `task_run_handler.go:170-174`, and `task_run_handler.go:250`.
- `backend/internal/api/handlers/task_handler.go:792-820` sanitizes `/tasks/:id/logs` responses.
- `backend/internal/ws/hub.go:322-333` sanitizes WebSocket backfill messages with `runtimeevidence.SanitizeTaskRuntimeEvidence(item.Message)`.
- `backend/internal/ws/hub_test.go:74-156` verifies WS backfill sanitizes legacy messages without mutating stored rows.
- `backend/internal/api/handlers/task_run_handler_test.go:26-274` verifies task-run/list/detail/log read-boundary sanitization without DB mutation.

Compared with the prior archived research, the legacy task evidence and WS backfill candidates are no longer the remaining smallest slice.

#### 8. Task list/detail responses are an avoidable duplicate hook response surface

`backend/internal/model/models.go:296-303` defines `Task.Policy *Policy` as `json:"policy,omitempty"`. `Policy` includes JSON-visible `pre_hook` and `post_hook` fields.

`backend/internal/api/handlers/task_handler.go:132-169` preloads `Policy` for task list and returns `[]model.Task` directly. `backend/internal/api/handlers/task_handler.go:188-202` preloads `Policy` for task detail and returns `model.Task` directly. Because the nested policy is not response-shaped down to id/name, these task endpoints can serialize nested policy hook fields.

The current frontend mapper in `web/src/lib/api/tasks-api.ts:24-27` and `tasks-api.ts:113-118` only reads nested policy `id` and `name`; it does not consume nested `pre_hook` or `post_hook`. This makes task embedded policy hook output a smaller response-only sub-surface than policy list/detail hook behavior.

#### 9. RBAC makes AppCredential responses narrower than policy/task read responses

`backend/internal/middleware/rbac.go:9-79` grants `app_credentials:read` and `app_credentials:write` only to admin. It grants `policies:read` and `tasks:read` to admin, operator, and viewer.

`backend/internal/api/router.go:243-248` protects AppCredential routes with `app_credentials:*`. `router.go:275-301` protects policy, task, and task-run read routes with `policies:read` / `tasks:read`.

So the residual rendered-hook read surface is not the AppCredential CRUD route; it is the policy/task response surface that carries stored rendered hook text.

#### 10. Config export/import does not currently include this surface

`backend/internal/api/handlers/config_handler.go:153-175` exports policies with name/description/source/target/cron/exclude/bandwidth/retention/concurrency/enabled/template/node names. It does not include `pre_hook`, `post_hook`, `app_profile`, `app_credential_id`, or AppCredential records in the inspected export payload.

The import path at `config_handler.go:436-513` likewise reads only the exported policy fields shown there and does not import AppCredential-rendered hook fields.

### Prior Archived Research Comparison

| Prior research | Prior conclusion | Current comparison |
|---|---|---|
| `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/research/profile-hook-app-credential-flow.md` | No AppCredential/profile hook provider seam was observed; rendered hooks were persisted and task logs could expose full command text. | `profile/app_profile_access.go` now provides a local resolver seam and current runtime logs no longer echo full hook commands; persistence/response of rendered policy hooks remains. |
| `.trellis/tasks/archive/2026-05/05-24-p4-residual-security-review/research/legacy-hooks-residual.md` | Legacy task evidence read-boundary gaps and AppCredential hook persistence were the main residuals. | Task-run/list/detail/log, task logs, and drill error read boundaries now sanitize legacy evidence; AppCredential hook persistence/response remains. |
| `.trellis/tasks/archive/2026-05/05-24-p4-appcredential-hook-hardening/research/appcredential-rendered-hooks.md` | Recommended WebSocket task-log backfill sanitizer as smallest compatible slice; noted policy hook storage/API redaction or runtime re-rendering would be behavior-changing. | `ws/hub.go` now sanitizes backfill and has regression tests. The remaining smaller compatible sub-surface is task response embedding of full Policy hook fields, not the already-fixed WS/log read surface. |

### Smallest Local-Only, Behavior-Compatible Slice

The remaining large residual is that generated AppCredential hooks are stored in `policies.pre_hook` / `policies.post_hook` and returned by policy list/detail APIs. Fully eliminating that would require a behavior-changing policy hook redesign: either masking policy responses with roundtrip guards, or not persisting generated hook strings and re-rendering at execution time. Current tests/docs explicitly rely on rendered hook visibility, so that is not the smallest compatible slice.

A smaller compatible sub-surface exists: **sanitize or narrow the nested `Policy` object returned by task list/detail responses so `policy.pre_hook` and `policy.post_hook` are not serialized through `/tasks` and `/tasks/:id`.** This is local-only and response-only, does not change hook execution, policy creation/update, AppCredential CRUD, deployment, migrations, or frontend behavior observed in `tasks-api.ts` because the frontend task mapper only uses nested policy `id` and `name`.

This slice has limited scope: users with `policies:read` can still retrieve policy hook fields through policy endpoints under the current product contract. It removes an avoidable duplicate exposure from task read endpoints without taking on the broader policy storage/API redesign.

### External References

No external references were used. The requested scope was local/internal and behavior-compatible.

### Related Specs

- `.trellis/spec/backend/logging-guidelines.md` — Decrypted values from model hooks and command output that may contain credentials should not be logged.
- `.trellis/spec/backend/database-guidelines.md` — Secret-bearing model values need response sanitizers; credential/audit storage should avoid raw credentials and raw command/output evidence.
- `.trellis/spec/backend/quality-guidelines.md` — Sensitive management surfaces should fail closed; credential audit metadata must remain sanitized and bounded.
- `docs/admin/backup-recovery.md` — Current user-facing documentation states rendered hook scripts are visible to authorized users, while AppCredential passwords are encrypted and not returned by credential APIs.

## Caveats / Not Found

- Not found: AppCredential CRUD responses returning raw `password`.
- Not found: config export/import carrying AppCredential records or policy hook/app-profile fields.
- Not found: current runtime hook lifecycle logs echoing full pre/post hook commands.
- Already present: task-run/log/drill read-boundary sanitizers and WebSocket backfill sanitizer with regression tests.
- Still present by current contract: policy list/detail responses return raw `pre_hook` and `post_hook`; tests and docs currently treat rendered hook visibility as expected behavior.
- The recommended minimal slice does not solve policy hook storage or policy endpoint visibility; those require a deliberate larger behavior change if selected later.
