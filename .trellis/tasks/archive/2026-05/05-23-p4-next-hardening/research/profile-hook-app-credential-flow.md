# Research: profile-hook-app-credential-flow

- **Query**: Research backup policy profile/pre-hook/post-hook execution and any app credential usage in /Users/weibo/Code/xirang. Map files/functions that resolve credentials or inject env/commands for hooks/profiles, note leakage risks, existing tests, and whether a local-only AppCredential/profile hook provider seam is a good next P4 slice.
- **Scope**: internal
- **Date**: 2026-05-23

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Defines `Policy` hook/profile fields and `AppCredential`; encrypts/decrypts credential config with GORM hooks; sanitizes app credential API config. |
| `backend/internal/profile/profile.go` | Defines built-in app-aware backup profile templates and renders pre/post hook strings from credential config via `text/template`. |
| `backend/internal/api/handlers/app_credential_handler.go` | CRUD for app credentials; builds/stores credential config; sanitizes responses; cascades rendered policy hook updates after credential changes. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update resolves `AppCredential`, renders profile hooks, persists `pre_hook`/`post_hook`, and returns hook text in policy responses. |
| `backend/internal/api/router.go` | Registers `/app-credentials`, `/app-credentials/profiles`, `/policies`, and task trigger routes with RBAC/step-up/grant middleware. |
| `backend/internal/middleware/rbac.go` | Defines role permissions: app credentials are admin-only; policy read is available to admin/operator/viewer. |
| `backend/internal/task/runner.go` | Executes persisted pre/post hooks around task execution; logs hook command text and hook errors; attaches runtime credential-audit context. |
| `backend/internal/task/hook.go` | Runs hook commands over SSH with purpose `task_hook`; wraps hooks in sudo shell when node requires sudo; returns remote output on failure. |
| `backend/internal/task/manager.go` | Defines `hookRunFunc` test seam and wires it to `runSSHHook`. |
| `backend/internal/policy/sync.go` | Creates/syncs policy tasks with `PolicyID`; hooks remain on the policy and are loaded at runtime. |
| `backend/internal/task/executor/ssh_connect.go` | Resolves SSH auth for explicit purposes, dials SSH, and writes runtime credential audit events with safe metadata labels. |
| `backend/internal/sshutil/credential_provider.go` | Existing local SSH credential provider seam; resolves node password/private key and managed SSH key credentials. |
| `backend/internal/sshutil/scope.go` | Defines SSH credential-use purposes including `task_hook`; defines safe `ResolvedCredential` descriptor. |
| `backend/internal/credentialaudit/audit.go` | Sanitizes credential-audit metadata/errors and redacts output markers; applies to audit events, not task logs. |
| `backend/internal/task/executor/restic_repository_access.go` | Existing local-only restic repository access object with unexported password and safe metadata. |
| `backend/internal/task/executor/restic_executor.go` | Injects restic repository password into remote commands through `RESTIC_PASSWORD=...` command prefix. |
| `backend/internal/task/integrity_checker.go` | Builds restic/rclone integrity-check commands; restic uses `BuildResticEnvPrefix`. |
| `backend/internal/task/executor/rclone_executor.go` | Builds rclone sync command strings with shell-escaped source/destination/config values; no AppCredential use observed. |
| `backend/internal/task/executor/executor.go` | Provides `ShellEscape`, used by executor command construction. |
| `backend/internal/task/executor/sudo.go` | Provides `WrapWithSudoShell`, used for sudo hook execution. |
| `backend/internal/api/handlers/hook_templates_handler.go` | Deprecated built-in hook templates route; includes shell examples using environment variables. |
| `backend/internal/database/migrations/sqlite/000022_policy_hooks.up.sql` | Adds policy hook columns for SQLite. |
| `backend/internal/database/migrations/postgres/000022_policy_hooks.up.sql` | Adds policy hook columns for PostgreSQL. |
| `backend/internal/database/migrations/sqlite/000051_app_credentials_and_policy_profile.up.sql` | Adds `app_credentials` and policy app profile linkage for SQLite. |
| `backend/internal/database/migrations/postgres/000051_app_credentials_and_policy_profile.up.sql` | Adds `app_credentials` and policy app profile linkage for PostgreSQL. |
| `backend/internal/profile/profile_test.go` | Tests profile catalog/rendering; explicitly asserts rendered hook can contain password and notes shell injection is currently expected for rendered profile values. |
| `backend/internal/api/handlers/app_credential_handler_test.go` | Tests app credential create/update/delete/profile list/cascade behavior and API password sanitization. |
| `backend/internal/api/app_credential_rbac_test.go` | Tests app credential route RBAC: admin allowed; operator/viewer forbidden. |
| `backend/internal/task/hook_test.go` | Tests hook execution ordering, timeout behavior, failure behavior, empty hooks, and the `hookRunFunc` seam. |
| `backend/internal/task/executor/restic_executor_test.go` | Tests restic repository access safe metadata/JSON behavior and env prefix escaping. |
| `backend/internal/task/executor/executor_security_test.go` | Tests shell escaping behavior. |
| `web/src/components/policy-editor-dialog.tsx` | Frontend policy editor loads profiles/credentials and submits selected `app_profile`/`app_credential_id`. |
| `web/src/pages/policies-page.tsx` | Submits policy payload with `app_profile` and `app_credential_id`. |
| `web/src/lib/api/policies-api.ts` | Maps policy API fields, including hooks and app profile linkage. |
| `web/src/components/credential-editor-dialog.tsx` | Frontend credential editor builds app credential input and leaves password empty to preserve existing value on update. |
| `web/src/lib/api/credentials.ts` | App credential CRUD/profile API wrapper. |
| `web/src/lib/api/app-credentials.ts` | Lightweight app credential/profile API wrapper used by policy UI. |
| `web/src/types/domain.ts` | Frontend `AppCredential` and `ProfileSchema` types. |

### Code Patterns

#### AppCredential storage, encryption, and sanitization

`backend/internal/model/models.go` defines `Policy` fields that persist hook/profile state:

```go
PreHook            string `gorm:"type:text;not null;default:''" json:"pre_hook"`
PostHook           string `gorm:"type:text;not null;default:''" json:"post_hook"`
HookTimeoutSeconds int    `gorm:"not null;default:300" json:"hook_timeout_seconds"`
AppProfile         string `gorm:"size:32;not null;default:''" json:"app_profile"`
AppCredentialID    *uint  `gorm:"index" json:"app_credential_id"`
```

The same file defines `AppCredential.Config` as non-JSON API output (`json:"-"`) and encrypts/decrypts it through hooks:

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

func (a *AppCredential) AfterFind(_ *gorm.DB) error {
    if a.Config != "" {
        decrypted, err := secure.DecryptIfNeeded(a.Config)
        if err != nil {
            return err
        }
        a.Config = decrypted
    }
    return nil
}
```

`SanitizedConfig` removes only the `password` key before app credential API responses:

```go
delete(raw, "password")
```

#### Profile templates and hook rendering

`backend/internal/profile/profile.go` defines `ProfileDefinition` with `PreHookTemplate` and `PostHookTemplate` excluded from JSON:

```go
PreHookTemplate  string `json:"-"`
PostHookTemplate string `json:"-"`
```

Built-in profiles observed: `mysql`, `postgres`, `mongodb`, `redis`, `docker-mysql`, `docker-postgres`, `docker-mongodb`, `docker-redis`.

Examples of credential-to-command interpolation include MySQL and PostgreSQL:

```go
PreHookTemplate: `mysqldump{{if .user}} -u {{.user}}{{end}}{{if .password}} -p'{{.password}}'{{end}}{{if .host}} -h {{.host}}{{end}}{{if .port}} -P {{.port}}{{end}} --all-databases --single-transaction --routines --triggers > /tmp/xirang-mysql-backup.sql`,
```

```go
PreHookTemplate: `{{if .password}}PGPASSWORD='{{.password}}' {{end}}su - postgres -c 'pg_dumpall{{if .host}} -h {{.host}}{{end}}{{if .port}} -p {{.port}}{{end}}{{if .user}} -U {{.user}}{{end}} > /tmp/xirang-pg-backup.sql'`,
```

Rendering uses `text/template` directly:

```go
func RenderHooks(profileID string, config map[string]interface{}) (preHook string, postHook string, err error) {
    p, ok := GetProfile(profileID)
    if !ok {
        return "", "", fmt.Errorf("未知的 profile: %s", profileID)
    }
    preHook, err = renderTemplate(p.PreHookTemplate, config)
    if err != nil {
        return "", "", fmt.Errorf("渲染 pre-hook 失败: %w", err)
    }
    postHook, err = renderTemplate(p.PostHookTemplate, config)
    if err != nil {
        return "", "", fmt.Errorf("渲染 post-hook 失败: %w", err)
    }
    return preHook, postHook, nil
}
```

`renderTemplate` parses and executes the template into a string; no shell escaping function was observed in this rendering path.

#### AppCredential API and cascade behavior

`backend/internal/api/handlers/app_credential_handler.go` accepts credential fields including `password` and `container_name`, then builds JSON config:

```go
func buildConfigJSON(req appCredentialRequest) string {
    cfg := map[string]interface{}{}
    if req.Host != "" {
        cfg["host"] = req.Host
    }
    if req.Port != "" {
        cfg["port"] = req.Port
    }
    if req.User != "" {
        cfg["user"] = req.User
    }
    if req.Password != "" {
        cfg["password"] = req.Password
    }
    if req.ContainerName != "" {
        cfg["container_name"] = req.ContainerName
    }
    b, _ := json.Marshal(cfg)
    return string(b)
}
```

Responses use `item.SanitizedConfig()`; update preserves an existing password when the update request omits password.

Credential updates cascade to policies referencing the credential if the existing policy hooks still match old rendered profile hooks:

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

This cascade writes newly rendered hook strings into policy rows.

#### Policy create/update profile rendering

`backend/internal/api/handlers/policy_handler.go` accepts both manual hook strings and app profile linkage. Manual hooks are limited to admin users and pass through `validateHookCommand`:

```go
if req.PreHook != "" || req.PostHook != "" {
    role, _ := c.Get("role")
    if roleStr, ok := role.(string); !ok || roleStr != "admin" {
        respondForbidden(c, "仅管理员可配置 hook 命令")
        return
    }
}
```

`validateHookCommand` rejects long commands, certain shell metacharacters, and selected programs such as `curl`, `wget`, `bash`, `sh`, `ssh`, and `base64`.

For app-aware policies, the handler loads `AppCredential`, unmarshals decrypted `cred.Config`, renders profile hooks, and assigns missing hook fields:

```go
var cred model.AppCredential
if err := h.db.First(&cred, *req.AppCredentialID).Error; err != nil {
    respondBadRequest(c, "指定的凭据不存在")
    return
}
var configMap map[string]interface{}
if err := json.Unmarshal([]byte(cred.Config), &configMap); err != nil {
    respondInternalError(c, err)
    return
}
renderedPre, renderedPost, err := profile.RenderHooks(appProfile, configMap)
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

The same rendering pattern exists in update. `buildPolicyResponse` includes `pre_hook`, `post_hook`, `app_profile`, and `app_credential_id`, so persisted rendered hook text is returned by policy APIs.

#### Hook execution runtime

`backend/internal/policy/sync.go` creates/syncs policy tasks with `PolicyID`; it does not copy hook fields onto `Task`. Runtime uses the loaded policy.

`backend/internal/task/runner.go` executes pre-hook before the executor and post-hook after a successful executor run. It logs the full command text before execution:

```go
m.emitLog(taskID, runIDPtr, "info", "执行 pre-hook: "+taskEntity.Policy.PreHook, taskEntity.Status)
hookErr := m.hookRunFunc(hookCtx, taskEntity, taskEntity.Policy.PreHook)
```

Post-hook follows the same pattern:

```go
m.emitLog(taskID, runIDPtr, "info", "执行 post-hook: "+taskEntity.Policy.PostHook, taskEntity.Status)
hookErr := m.hookRunFunc(hookCtx, taskEntity, taskEntity.Policy.PostHook)
```

On pre-hook failure, task execution fails. On post-hook failure, the code logs a warning and does not change the successful backup result.

`backend/internal/task/hook.go` executes hooks through SSH and purpose `task_hook`:

```go
client, err := executor.DialSSHForNodePurpose(ctx, task.Node, sshutil.PurposeTaskHook)
```

If `executor.NeedsSudo(task.Node)` is true, the hook command is wrapped:

```go
command = executor.WrapWithSudoShell(command)
```

On SSH command failure, `runSSHHook` returns both error and remote output:

```go
return fmt.Errorf("钩子执行失败: %s, 输出: %s", err, output)
```

That error is logged by `runner.go`.

#### SSH credential resolution and audit path

`backend/internal/task/executor/ssh_connect.go` centralizes purpose-aware SSH dialing:

```go
authMethods, credential, err := resolveSSHAuthMethodsForPurpose(node, purpose)
```

It writes runtime credential audit events for blocked/failure/success outcomes. Event fields include safe descriptors such as purpose, credential kind/source/provider, SSH key ID, node ID, and operation metadata.

`backend/internal/sshutil/credential_provider.go` defines an existing local credential provider interface for SSH credentials and `LocalCredentialProvider`. The password case returns a `ResolvedCredential` with safe metadata:

```go
credential := ResolvedCredential{Kind: "password", Source: "node.password", Provider: CredentialProviderLocal}
```

`backend/internal/sshutil/scope.go` defines stable SSH purposes including:

```go
PurposeTaskHook = "task_hook"
```

and the safe credential descriptor:

```go
type ResolvedCredential struct {
    Kind     string
    Source   string
    Provider string
    KeyID    *uint
}
```

#### Restic/rclone env and command injection surfaces

Restic repository access has a local-only access object in `backend/internal/task/executor/restic_repository_access.go`:

```go
type ResticRepositoryAccess struct {
    password string
    Provider string
    Kind     string
    Source   string
}
```

`BuildResticEnvPrefix` injects the repository password into remote shell commands:

```go
if access.password == "" {
    return "RESTIC_PASSWORD=''"
}
return "RESTIC_PASSWORD=" + ShellEscape(access.password)
```

`backend/internal/task/executor/restic_executor.go` includes that prefix in each restic command, with sudo preserving environment through `sudo env`:

```go
if NeedsSudo(node) {
    return fmt.Sprintf("sudo env %s %s", envPrefix, bin)
}
return fmt.Sprintf("%s %s", envPrefix, bin)
```

`backend/internal/task/integrity_checker.go` uses the same restic env prefix for periodic integrity checks:

```go
access := executor.ResolveResticRepositoryAccessOrEmpty(task.ExecutorConfig)
envPrefix := executor.BuildResticEnvPrefix(access)
cmd := fmt.Sprintf("%s %s check -r %s --json 2>&1",
    envPrefix, resticBin, shellEscape(repo))
```

`backend/internal/task/executor/rclone_executor.go` builds rclone commands from source/destination/config and shell-escapes variable arguments. No AppCredential/profile usage was observed in rclone command construction.

#### Frontend policy/profile/credential flow

`web/src/components/policy-editor-dialog.tsx` loads both profile schemas and credentials:

```tsx
apiClient.getProfiles(token).then(setProfiles).catch(() => {});
apiClient.getCredentials(token).then(setCredentials).catch(() => {});
```

It filters credentials by selected profile credential type:

```tsx
const selectedProfileMeta = profiles.find((p) => p.id === draft.app_profile);
const filteredCredentials = credentials.filter(
  (c) => !draft.app_profile ? false : c.type === selectedProfileMeta?.credential_type
);
```

`web/src/pages/policies-page.tsx` submits `app_profile` and `app_credential_id` in policy payloads. `web/src/lib/api/policies-api.ts` maps policy responses including hook fields and app profile linkage.

`web/src/components/credential-editor-dialog.tsx` renders credential schema fields, sends `password` only when non-empty, and for Docker profiles sends `container_name`.

### Leakage and Exposure Notes

- `AppCredential.Config` is encrypted at rest and the app credential API removes `password` from credential responses.
- Profile rendering converts decrypted app credential config into shell command strings and persists those strings in `policies.pre_hook` / `policies.post_hook`.
- Policy API responses include `pre_hook` and `post_hook`. RBAC gives `policies:read` to admin/operator/viewer, while `app_credentials:*` is admin-only. This means app credential routes are narrower than policy read surfaces.
- `Manager.runTask` logs full hook command text before execution. If rendered hook strings contain passwords, task logs can contain those rendered values.
- `runSSHHook` returns remote command output on failure (`输出: ...`), and `runner.go` logs the error. Credential-audit error sanitization redacts output markers for audit events, but this task-log path is separate.
- Profile templates interpolate credential fields directly into shell commands through `text/template`; no shell escaping helper was observed in the profile rendering path.
- `profile_test.go` explicitly encodes current behavior that a password appears in a rendered hook and that shell injection-like password text is output directly.
- Restic repository password injection is also command/env based, but it uses a dedicated `ResticRepositoryAccess` object with unexported password and safe metadata tests. The command string still includes an env assignment for remote execution.

### Existing Tests

| File Path | Coverage observed |
|---|---|
| `backend/internal/profile/profile_test.go` | Profile count/list/get; rendering for MySQL/PostgreSQL/MongoDB/Redis/Docker profiles; unknown profile error; no-password render; Docker templates; password appears in rendered hook; shell injection-like values are directly output. |
| `backend/internal/api/handlers/app_credential_handler_test.go` | App credential create encrypts config at rest and omits password in API response; update preserves password when omitted; Docker credential requires container name; invalid type rejected; delete blocked while referenced; profile list hides templates; credential update cascades rendered policy hooks; user override hooks are preserved. |
| `backend/internal/api/app_credential_rbac_test.go` | App credential routes allow admin and forbid operator/viewer. |
| `backend/internal/task/hook_test.go` | Pre-hook success/failure/timeout; post-hook failure does not affect task success; empty hooks/no-policy skip hook execution; zero hook timeout defaults; pre/post order; uses `hookRunFunc` seam. |
| `backend/internal/task/executor/restic_executor_test.go` | Restic config parsing; restic env prefix escaping; `ResticRepositoryAccess` safe metadata; JSON does not expose password; invalid config errors do not include raw password/config. |
| `backend/internal/task/executor/executor_security_test.go` | `ShellEscape` behavior against adversarial shell input. |

### Local-only AppCredential/profile hook provider seam assessment

Existing local-only/provider-like patterns were observed in two places:

1. SSH credentials: `sshutil.CredentialProvider` / `LocalCredentialProvider` resolves node credentials and returns safe `ResolvedCredential` descriptors.
2. Restic repository access: `ResticRepositoryAccess` stores password in an unexported field, labels it with `Provider: local`, and exposes `SafeMetadata()` without the secret.

For app credential/profile hooks, no equivalent provider seam was observed. Current flow resolves `AppCredential` directly in HTTP policy handlers, renders profile templates at create/update time, and persists the rendered hook commands on the policy. At runtime, the task runner reads and executes those persisted command strings; it does not resolve AppCredential/profile data through a local-only provider.

Based on the observed code structure, a local-only AppCredential/profile hook provider seam is a coherent next P4 slice to examine because it aligns with existing local-provider patterns and the current app profile path crosses three surfaces: decrypted app credential config, persisted rendered commands, and task log output. This assessment is limited to the requested P4-slice suitability question; it does not describe an implementation plan.

### External References

No external references were used for this internal codebase research.

### Related Specs

| Spec Path | Relevance |
|---|---|
| `.trellis/spec/backend/quality-guidelines.md` | Notes sensitive-surface/RBAC expectations, SSH key scope purposes including `task_hook`, credential-use audit expectations, and credential access grant guidance. |
| `.trellis/spec/backend/logging-guidelines.md` | States logs must not contain passwords/private keys/tokens/decrypted values/raw command output that may contain credentials. |
| `.trellis/spec/backend/database-guidelines.md` | States sensitive fields should use model hooks for encryption/decryption and credential audit events must not store raw credentials/decrypted executor config. |
| `.trellis/spec/frontend/hook-guidelines.md` | Frontend conventions for hooks/API wrappers. |
| `.trellis/spec/frontend/component-guidelines.md` | UI guidance related to not enriching credential-related cards with secret metadata. |

## Caveats / Not Found

- No current local-only AppCredential/profile hook provider seam was found.
- No AppCredential usage was observed in rclone command construction.
- No external web documentation was consulted; the task was satisfied from internal source/spec/test files.
- The frontend filters credentials by profile credential type, but no backend enforcement of credential type matching the selected profile was observed in the inspected policy handler flow.
- Profile-generated hooks were observed being rendered separately from manual hook validation. Manual hooks go through `validateHookCommand`; the profile rendering path was not observed calling that validator.
