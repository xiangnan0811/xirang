# Research: AppCredential rendered hook residual security surface

- **Query**: Research the AppCredential rendered hook residual security surface for the active Trellis task in /Users/weibo/Code/xirang. Goal: identify where AppCredential/profile hook values are rendered, stored, logged, returned, audited, or persisted; determine the smallest local-only, behavior-compatible hardening slice. Constraints: no code modifications outside research directory; preserve API/deployment/UI behavior; exclude external Vault/KMS/SSH CA/session recording/command approval/WebAuthn/passkeys/device trust.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/profile/profile.go` | Defines application profiles and renders pre/post hook templates from AppCredential config. Templates intentionally interpolate host/user/password/container values into command strings. |
| `backend/internal/profile/app_profile_access.go` | Resolves decrypted `AppCredential.Config` from DB and exposes copied config to hook rendering; also defines safe metadata labels. |
| `backend/internal/model/models.go` | Defines encrypted `AppCredential.Config`, plaintext `Policy.PreHook`/`PostHook`, and JSON-visible `Task.Policy`. |
| `backend/internal/api/handlers/app_credential_handler.go` | Sanitizes AppCredential API responses, preserves password on edit when omitted, and cascades updated credential config into stored rendered policy hooks. |
| `backend/internal/api/handlers/policy_handler.go` | Renders profile hooks during policy create/update and returns `pre_hook`/`post_hook` in policy responses. |
| `backend/internal/task/runner.go` | Loads task policy, executes persisted pre/post hook commands, and writes sanitized runtime log/error evidence. |
| `backend/internal/task/hook.go` | Executes hook commands over SSH and sanitizes hook execution error details before returning. |
| `backend/internal/task/log_writer.go` | Sanitizes task log messages before DB persistence and live WebSocket broadcast. |
| `backend/internal/task/runtime_sanitize.go` | Central task runtime evidence sanitizer used for logs, last errors, and HTTP read sanitization. |
| `backend/internal/api/handlers/task_handler.go` | Task list/detail preload `Policy`; task log responses sanitize messages. Embedded preloaded policies may expose hook fields through task APIs. |
| `backend/internal/api/handlers/task_run_handler.go` | Sanitizes TaskRun `LastError`, task logs, and restore drill evidence on HTTP read. |
| `backend/internal/ws/hub.go` | Defines WebSocket log event shape and backfill path; backfill returns stored `TaskLog.Message` without read-boundary sanitization. |
| `backend/internal/middleware/audit.go` | HTTP audit middleware records request metadata only, not request/response bodies or hook content. |
| `backend/internal/credentialaudit/audit.go` | Credential audit metadata sanitizer rejects secret/config/output/command-like keys and values. |
| `backend/internal/task/executor/ssh_connect.go` | Runtime SSH credential audit records credential kind/source/purpose/stage, not rendered app hook commands. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import omits AppCredential records and policy hook/app-profile fields. |
| `backend/internal/database/migrations/sqlite/000022_policy_hooks.up.sql` | Adds plaintext `policies.pre_hook`, `post_hook`, and hook timeout fields for SQLite. |
| `backend/internal/database/migrations/postgres/000022_policy_hooks.up.sql` | Adds plaintext `policies.pre_hook`, `post_hook`, and hook timeout fields for PostgreSQL. |
| `backend/internal/database/migrations/sqlite/000051_app_credentials_and_policy_profile.up.sql` | Adds `app_credentials.config` and policy app-profile linkage for SQLite. |
| `backend/internal/database/migrations/postgres/000051_app_credentials_and_policy_profile.up.sql` | Adds `app_credentials.config` and policy app-profile linkage for PostgreSQL. |
| `backend/internal/middleware/rbac.go` | Shows app credential access is admin-only, while policy/task read permissions include operator/viewer. |
| `backend/internal/api/router.go` | Registers app-credential, policy, task, task-run, and config routes with RBAC/ownership middleware. |
| `web/src/pages/credentials-page.tsx` | Frontend credentials page consumes sanitized credential config and displays metadata/password-present state only. |
| `web/src/components/credential-editor-dialog.tsx` | Credential editor collects password in local React state for create/update; edit responses do not repopulate password. |
| `web/src/lib/api/credentials.ts` | Primary frontend AppCredential CRUD client and response types. |
| `web/src/lib/api/app-credentials.ts` | Secondary frontend AppCredential profiles/credentials client used through `apiClient`. |
| `web/src/components/policy-editor-dialog.tsx` | Policy editor fetches profiles/credentials and displays/sends `preHook`/`postHook` textareas. |
| `web/src/pages/policies-page.tsx` | Converts policy drafts to API inputs including hook and app-profile fields. |
| `web/src/lib/api/policies-api.ts` | Maps backend `pre_hook`/`post_hook` into frontend policy records and sends them on create/update. |
| `web/src/types/domain.ts` | Defines policy and app credential frontend types carrying hook and sanitized credential fields. |
| `web/src/components/layout/navigation.ts` | Credentials navigation is admin-only. |
| `web/src/router.tsx` | Registers credentials, credential audit/grants, and policy routes. |
| `.trellis/spec/backend/logging-guidelines.md` | Security logging contract: do not log decrypted values or raw command output that may contain credentials. |
| `.trellis/spec/backend/database-guidelines.md` | Database/security contract: credential audit must not store raw credentials, decrypted executor config, terminal streams, command output, or file contents. |
| `.trellis/spec/backend/quality-guidelines.md` | RBAC and credential audit contract for fail-closed sensitive surfaces and no raw secrets/full commands in audit events. |
| `docs/admin/backup-recovery.md` | Documents application-aware backup behavior and notes rendered hook scripts are visible to authorized users. |
| `backend/internal/profile/profile_test.go` | Tests intentionally assert rendered hooks contain passwords. |
| `backend/internal/api/handlers/integration_app_aware_test.go` | Integration tests assert policy responses and GET `/policies/:id` include rendered password-bearing hooks. |
| `backend/internal/api/handlers/app_credential_handler_test.go` | Tests encrypted AppCredential DB storage, sanitized credential responses, credential-update cascade into policy hooks, and override preservation. |
| `backend/internal/task/hook_test.go` | Tests hook execution errors are sanitized in task last error, task run last error, and task logs. |

### Code Patterns

#### 1. Rendering source: profile templates copy credential config into shell commands

`backend/internal/profile/profile.go:32-49` defines `RenderHooks(profileID string, config map[string]interface{})` and delegates to `renderTemplate` (`profile.go:51-60`), which runs Go `text/template` over the profile templates. There is no redaction at render time because the rendered command is what will be executed on the node.

Representative templates in `backend/internal/profile/profile.go` interpolate secret and endpoint fields directly:

```go
PreHookTemplate: `mysqldump{{if .user}} -u {{.user}}{{end}}{{if .password}} -p'{{.password}}'{{end}}{{if .host}} -h {{.host}}{{end}}{{if .port}} -P {{.port}}{{end}} --all-databases --single-transaction --routines --triggers > /tmp/xirang-mysql-backup.sql`,
```

```go
PreHookTemplate: `{{if .password}}PGPASSWORD='{{.password}}' {{end}}su - postgres -c 'pg_dumpall{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .user}} -U {{.user}}{{end}} > /tmp/xirang-pg-backup.sql'`,
```

```go
PreHookTemplate: `redis-cli{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .password}} -a '{{.password}}'{{end}} BGSAVE && sleep 2 && cp /var/lib/redis/dump.rdb /tmp/xirang-redis-backup.rdb`,
```

#### 2. AppCredential config is encrypted at rest and sanitized in credential API responses

`backend/internal/model/models.go` defines `AppCredential.Config` as `json:"-"`, encrypts it in `BeforeSave`, decrypts it in `AfterFind`, and removes `password` in `SanitizedConfig()` before API response construction.

Relevant snippets:

```go
type AppCredential struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:128;not null;uniqueIndex" json:"name"`
	Type        string    `gorm:"size:32;not null" json:"type"`
	Description string    `gorm:"size:255" json:"description"`
	Config      string    `gorm:"type:text;not null;default:'{}'" json:"-"`
	HasPassword bool      `gorm:"-" json:"has_password"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
```

```go
func (a *AppCredential) BeforeSave(_ *gorm.DB) error {
	if a.Config != "" && !secure.IsEncrypted(a.Config) {
		encrypted, err := secure.EncryptIfNeeded(a.Config)
		if err != nil {
			return err
		}
		a.Config = encrypted
	}
	return nil
}
```

```go
func (a *AppCredential) SanitizedConfig() map[string]interface{} {
	raw := map[string]interface{}{}
	if err := json.Unmarshal([]byte(a.Config), &raw); err != nil {
		return map[string]interface{}{}
	}
	delete(raw, "password")
	return raw
}
```

`backend/internal/api/handlers/app_credential_handler.go` builds responses from `SanitizedConfig()`, so `/app-credentials` and `/app-credentials/:id` do not return cleartext passwords.

#### 3. Policy hooks are plaintext storage and can contain rendered credentials

`backend/internal/model/models.go` defines hook fields as plain JSON-visible text:

```go
PreHook            string `gorm:"type:text;not null;default:''" json:"pre_hook"`
PostHook           string `gorm:"type:text;not null;default:''" json:"post_hook"`
HookTimeoutSeconds int    `gorm:"not null;default:300" json:"hook_timeout_seconds"`
AppProfile         string `gorm:"size:32;not null;default:''" json:"app_profile"`
AppCredentialID    *uint  `gorm:"index" json:"app_credential_id"`
```

SQLite/PostgreSQL migrations `000022_policy_hooks.up.sql` add `pre_hook`/`post_hook` as plaintext `TEXT`. Migration `000051_app_credentials_and_policy_profile.up.sql` adds `app_credentials.config` and the policy link fields; encryption for `app_credentials.config` depends on the GORM model hook, while policy hooks have no equivalent encryption/redaction hook.

#### 4. Policy create/update renders missing hooks from decrypted AppCredential config

`backend/internal/api/handlers/policy_handler.go` create/update flows trim user hook inputs; when `app_profile` is set and either hook is omitted, they resolve the AppCredential and render hooks:

```go
access, err := profile.ResolveAppProfileAccess(h.db, *req.AppCredentialID)
if err != nil {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondBadRequest(c, "指定的凭据不存在")
		return
	}
	respondInternalError(c, err)
	return
}
renderedPre, renderedPost, err := profile.RenderHooks(appProfile, access.Config())
if err != nil {
	respondInternalError(c, err)
	return
}
if !userProvidedPre {
	preHook = renderedPre
}
if !userProvidedPost {
	postHook = renderedPost
}
```

`buildPolicyResponse` returns `pre_hook` and `post_hook` directly:

```go
"pre_hook":              p.PreHook,
"post_hook":             p.PostHook,
"hook_timeout_seconds":  p.HookTimeoutSeconds,
"app_profile":           p.AppProfile,
"app_credential_id":     p.AppCredentialID,
```

#### 5. AppCredential updates can cascade new secret values into existing stored policies

`backend/internal/api/handlers/app_credential_handler.go` compares each policy's current hooks with hooks rendered from old config. If they match, it replaces them with hooks rendered from new config:

```go
if p.PreHook == oldPre && newPre != oldPre {
	p.PreHook = newPre
	needsSave = true
}
if p.PostHook == oldPost && newPost != oldPost {
	p.PostHook = newPost
	needsSave = true
}
if needsSave {
	if err := db.Save(&p).Error; err != nil {
		return err
	}
}
```

This preserves manual user overrides, but it also means a password rotation updates plaintext rendered policy hook rows when those rows were still auto-rendered values.

#### 6. Task runtime logs are mostly sanitized, but persisted policy hooks remain the primary residual surface

`backend/internal/task/runner.go` logs generic hook lifecycle messages (`执行 pre-hook`, `pre-hook 执行成功`, `执行 post-hook`, `post-hook 执行成功`) and sanitizes failure strings before writing task state/log evidence.

`backend/internal/task/hook.go` passes the command to SSH execution, then sanitizes execution error text before returning:

```go
_, err = executor.RunSSHCommandOutput(ctx, client, command)
if err != nil {
	return fmt.Errorf("钩子执行失败: %s", sanitizeTaskLastError(err.Error()))
}
```

`backend/internal/task/log_writer.go` sanitizes before queueing/persisting/broadcasting live logs:

```go
entry := queuedTaskLog{
	taskID:    taskID,
	taskRunID: runID,
	level:     level,
	message:   sanitizeTaskLogMessage(message),
	status:    status,
}
```

`backend/internal/task/runtime_sanitize.go` handles output markers, URLs, command lifecycle lines, remote paths, absolute paths, Windows paths, IPv4/hostnames, and sensitive host fragments. HTTP task-log/task-run read paths call this sanitizer again in `task_handler.go` and `task_run_handler.go`.

#### 7. WebSocket backfill is a local residual read surface for legacy unsafe log rows

`backend/internal/ws/hub.go` live publish receives already-sanitized messages from `emitLog`, but the backfill loader maps stored DB rows directly into `LogEvent.Message`:

```go
events = append(events, LogEvent{
	LogID:     item.ID,
	TaskID:    item.TaskID,
	Level:     item.Level,
	Message:   item.Message,
	Status:    taskStatusByID[item.TaskID],
	Timestamp: item.CreatedAt,
})
```

Because HTTP read paths sanitize at read boundary and live logs are sanitized before storage/broadcast, applying the same read-boundary sanitizer to WebSocket backfill is the smallest identified local-only hardening slice that preserves schemas, deployment, and normal UI behavior while covering legacy/raw DB rows.

#### 8. Audit surfaces do not appear to persist rendered hook commands

`backend/internal/middleware/audit.go` creates `AuditLog` from method/path/status/client/user metadata only; it does not log request/response bodies.

`backend/internal/credentialaudit/audit.go` denies metadata keys containing terms such as `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, and `payload`; values containing the same secret-like terms are also denied.

`backend/internal/task/executor/ssh_connect.go` runtime credential audit records task SSH credential use with normalized purpose/source/kind/stage and a generic safe error (`"<stage> failed"`), not rendered app hook values.

#### 9. Config export/import currently omits app credentials and hook/app-profile policy fields

`backend/internal/api/handlers/config_handler.go` exports policies with fields such as name, source/target path, cron, exclude rules, bandwidth, retention, enabled/template status, and node names. It does not export `pre_hook`, `post_hook`, `app_profile`, `app_credential_id`, or AppCredential records. Import likewise does not import app credentials or app-aware hook fields.

#### 10. Frontend credential UI uses sanitized configs; policy UI displays/sends hook strings

`web/src/pages/credentials-page.tsx`, `web/src/components/credential-editor-dialog.tsx`, and `web/src/lib/api/credentials.ts` handle sanitized AppCredential responses: password is collected only from local form input and is not populated from backend responses.

`web/src/components/policy-editor-dialog.tsx`, `web/src/pages/policies-page.tsx`, `web/src/lib/api/policies-api.ts`, and `web/src/types/domain.ts` carry `preHook`/`postHook` through the policy UI and API. Existing persisted rendered hooks can therefore appear in policy editor advanced textareas and be sent back on update.

#### 11. RBAC boundary is narrower for AppCredential APIs than for policy/task reads

`backend/internal/middleware/rbac.go` grants `app_credentials:read`/`write` only to admin, but `policies:read` and `tasks:read` are available to admin, operator, and viewer roles. `backend/internal/api/router.go` applies app-credential RBAC on `/app-credentials` and policy/task RBAC on `/policies`, `/tasks`, and `/task-runs` routes. Since rendered hooks are returned through policy responses and likely embedded preloaded task policies, this broadens the read surface beyond app credential APIs.

### External References

No external references were needed. The requested scope and constraints were local/internal: preserve existing API/deployment/UI behavior and exclude external Vault/KMS/SSH CA/session recording/command approval/WebAuthn/passkeys/device trust.

### Related Specs

- `.trellis/spec/backend/logging-guidelines.md` — Requires that decrypted model-hook values, passwords, private keys, tokens, endpoints, and raw command output that may contain credentials are not logged.
- `.trellis/spec/backend/database-guidelines.md` — Requires credential audit events not store raw credentials, decrypted executor config, terminal streams, command output, or file contents; requires response sanitizers for secret-bearing model values.
- `.trellis/spec/backend/quality-guidelines.md` — Requires sensitive management surfaces to fail closed and credential audit events not store raw passwords, decrypted config, raw command output, or full command text.
- `docs/admin/backup-recovery.md` — Documents current product behavior: app credentials are encrypted and API responses do not return cleartext passwords, but rendered hook scripts are visible to authorized users and should be controlled through RBAC.

### Tests Establishing Current Contract

- `backend/internal/profile/profile_test.go` explicitly asserts passwords appear in rendered hooks and treats that as the current design because hooks execute in plaintext on target nodes.
- `backend/internal/api/handlers/integration_app_aware_test.go` asserts policy responses and `GET /policies/:id` include rendered password-bearing pre-hooks.
- `backend/internal/api/handlers/app_credential_handler_test.go` asserts AppCredential config is encrypted in DB, API responses omit password, credential updates cascade new password values into policy hooks, and manual hook overrides are preserved.
- `backend/internal/task/hook_test.go` asserts hook runtime errors are sanitized in `Task.LastError`, `TaskRun.LastError`, and `TaskLog.Message`.

## Smallest Local-Only, Behavior-Compatible Hardening Slice

Recommended slice: sanitize WebSocket task-log backfill messages with the existing task runtime evidence sanitizer and add a regression test for legacy/raw `TaskLog.Message` rows containing command/output/path/host/secret-like evidence.

Why this is the smallest compatible slice:

1. It is local-only: it changes only the server read boundary for WS backfill and tests.
2. It preserves API/deployment/UI schemas and flows: `LogEvent.Message` remains a string; no route, payload shape, deployment dependency, or UI state model changes are needed.
3. It aligns WS backfill with existing behavior: HTTP task-log APIs already sanitize on read, and live WS logs are already sanitized before publish.
4. It avoids contract-breaking changes: redacting policy `pre_hook`/`post_hook`, stopping persistence of rendered hooks, or changing profile rendering would contradict current docs/tests and visible UI/API behavior.
5. It targets a real residual surface: legacy or manually inserted raw task log rows can currently be replayed through WS backfill without the read-boundary sanitizer used elsewhere.

## Caveats / Not Found

- Not found: any AppCredential API response path returning cleartext `password`; responses use `SanitizedConfig()` and `Config` has `json:"-"`.
- Not found: config export/import including AppCredential records or policy hook/app-profile fields.
- Not found: general HTTP audit middleware persisting request/response bodies or rendered hook commands.
- Not found: credential audit production code writing rendered app hook commands; metadata sanitization rejects credential/config/output/command-like keys and values.
- Existing policy/task response behavior is intentionally broad: rendered hooks can be stored in plaintext policy rows and returned by policy APIs; tests/docs currently rely on this. Treat policy hook response redaction or non-persistence as behavior-changing unless the product contract is revised.
- WebSocket backfill hardening addresses log replay residuals, not the larger documented plaintext policy-hook persistence surface.
