# Research: P1d step-up authentication code surfaces

- **Query**: Existing code surfaces for P1d step-up authentication for high-risk operations in Xirang
- **Scope**: internal
- **Date**: 2026-05-20

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/router.go` | Registers authenticated REST routes, RBAC/ownership middleware, and WebSocket routes; contains the high-risk route surfaces listed for this task. |
| `backend/internal/auth/jwt.go` | JWT claim shape, token generation, short-lived `2fa_pending` purpose token, JTI/token-version fields, revocation parsing. |
| `backend/internal/middleware/auth.go` | Bearer-token authentication for secured REST routes; rejects `2fa_pending` purpose tokens and checks `token_version`. |
| `backend/internal/api/handlers/realtime_auth.go` | WebSocket/in-protocol token authorization used by terminal/log streams; mirrors purpose and token-version checks. |
| `backend/internal/auth/service.go` | Login flow, TOTP-gated login result, full JWT issuance, token-version increments for password/role changes. |
| `backend/internal/auth/totp.go` | TOTP secret generation, TOTP code validation, recovery-code generation and constant-time recovery-code consumption. |
| `backend/internal/auth/login_lock.go` | Login failure lockout model access and DB-backed lock/unlock patterns by normalized username and client IP. |
| `backend/internal/api/handlers/auth_handler.go` | Login, TOTP setup/verify/disable, and `/auth/2fa/login` handlers. |
| `backend/internal/model/models.go` | `User`, `LoginFailure`, `CredentialAuditEvent`, `Task`, `Node`, and encrypted sensitive-field hooks. |
| `backend/internal/model/token_revocation.go` | Persistent token revocation model keyed by token/JTI hash with expiry. |
| `backend/internal/middleware/rbac.go` | Role/permission checks used on high-risk HTTP route registrations. |
| `backend/internal/middleware/ownership.go` | Node/task object ownership checks for non-admin/non-viewer users. |
| `backend/internal/middleware/audit.go` | Generic audit-log middleware for secured non-GET HTTP requests, backed by a hash chain. |
| `backend/internal/credentialaudit/audit.go` | Domain-specific credential audit writer, event shape, outcome constants, bounded metadata/error sanitization. |
| `backend/internal/api/handlers/helpers.go` | Handler helper `writeCredentialAuditFromGin`, credential audit outcome helper, and safe audit helpers. |
| `backend/internal/api/handlers/credential_audit_handler.go` | List/export handlers for credential audit events; re-sanitizes metadata and legacy errors on read/export. |
| `backend/internal/sshutil/scope.go` | SSH credential purpose constants and SSH key purpose/scope validation. |
| `backend/internal/sshutil/ssh_auth.go` | Builds SSH auth methods for a supplied purpose and resolves credential source metadata. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key export handler and credential-audit write for `ssh_key.export`. |
| `backend/internal/api/handlers/config_handler.go` | Config export handler, `include_secrets=true` behavior, and `config.export` credential audit. |
| `backend/internal/api/handlers/terminal_handler.go` | Terminal WebSocket open/auth flow and terminal credential audit events. |
| `backend/internal/api/handlers/task_handler.go` | Manual trigger, batch trigger, restore trigger handlers and corresponding credential audit writes. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation, command safety checks, task/run creation, and `batch_command.create` credential audit. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot restore handler and target-path validation. |
| `backend/internal/api/handlers/node_handler.go` | Related emergency backup trigger surface observed while mapping trigger-like routes. |
| `backend/internal/api/handlers/response.go` | Unified response envelope helpers for unauthorized/forbidden/error responses. |
| `backend/internal/database/migrations/sqlite/000011_user_totp.up.sql` | SQLite migration adding TOTP fields. |
| `backend/internal/database/migrations/postgres/000011_user_totp.up.sql` | PostgreSQL migration adding TOTP fields. |
| `backend/internal/database/migrations/sqlite/000019_user_token_version.up.sql` | SQLite migration adding `users.token_version`. |
| `backend/internal/database/migrations/postgres/000019_user_token_version.up.sql` | PostgreSQL migration adding `users.token_version`. |
| `backend/internal/database/migrations/sqlite/000020_login_failures.up.sql` | SQLite migration creating login failure lockout table. |
| `backend/internal/database/migrations/postgres/000020_login_failures.up.sql` | PostgreSQL migration creating login failure lockout table. |
| `backend/internal/database/migrations/sqlite/000031_token_revocations.up.sql` | SQLite migration creating token revocations table. |
| `backend/internal/database/migrations/postgres/000031_token_revocations.up.sql` | PostgreSQL migration creating token revocations table. |
| `backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql` | SQLite migration creating SSH key scope columns and credential audit table. |
| `backend/internal/database/migrations/postgres/000059_ssh_key_scope_credential_audit.up.sql` | PostgreSQL migration creating SSH key scope columns and credential audit table. |
| `web/src/context/auth-context-provider.tsx` | Frontend auth/session state provider using `sessionStorage`, with legacy `localStorage` cleanup. |
| `web/src/context/auth-context.shared.ts` | Shared auth context type contract and roles. |
| `web/src/lib/api/core.ts` | Central API wrapper, `ApiError`, envelope unwrapping, and 401 redirect/session-clear behavior. |
| `web/src/lib/api/auth-api.ts` | Login API wrapper handling normal token and `requires_2fa` login responses. |
| `web/src/lib/api/totp-api.ts` | TOTP setup/verify/disable and `/auth/2fa/login` API wrapper. |
| `web/src/lib/api/client.ts` | Merges resource API modules into `apiClient`. |
| `web/src/lib/api/ssh-keys-api.ts` | SSH key export URL builder. |
| `web/src/components/ssh-key-export-dialog.tsx` | SSH key export UI; uses direct `fetch` with bearer token to download files. |
| `web/src/lib/api/config-api.ts` | Config export/import API wrapper; export supports `includeSecrets`. |
| `web/src/components/config-export-import.tsx` | Config export/import UI; current export call uses default `includeSecrets=false`. |
| `web/src/lib/api/tasks-api.ts` | Task trigger, restore, and batch-trigger API wrappers. |
| `web/src/lib/api/batch-api.ts` | Batch command creation API wrapper. |
| `web/src/lib/api/snapshots-api.ts` | Snapshot restore API wrapper. |
| `web/src/components/web-terminal.tsx` | Terminal WebSocket client; sends `{ type: "auth", token }` after open. |
| `web/src/pages/nodes-page.tsx` | Node page terminal entry point. |
| `web/src/pages/nodes-page.dialogs.tsx` | Renders terminal dialog and batch command dialog. |
| `web/src/components/batch-command-dialog.tsx` | Batch command creation UI. |
| `web/src/pages/tasks-page.tsx` | Task trigger and batch-trigger UI handlers. |
| `web/src/pages/tasks-page.dialogs.tsx` | Task history, restore dialog, and snapshot browser surfaces. |
| `web/src/components/restore-confirm-dialog.tsx` | Task restore confirmation UI. |
| `web/src/components/snapshot-browser.tsx` | Snapshot browsing and snapshot restore UI. |
| `web/src/hooks/use-api-action.ts` | Generic API action wrapper for write operations. |
| `web/src/hooks/use-console-data.ts` | Shared write-operation error handling used by console hooks. |
| `web/src/hooks/use-console-task-operations.ts` | Task operation hook that calls `apiClient.triggerTask`. |
| `web/src/lib/api/credential-audit-api.ts` | Credential audit client mapper, query builder, export call, known action list, and client-side sanitizers. |
| `web/src/types/domain.ts` | Frontend credential audit domain types, including credential audit action union. |
| `.trellis/spec/backend/error-handling.md` | Backend envelope and sensitive-data error-handling constraints. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security constraints including auth/RBAC/ownership, migrations, and security tests. |
| `.trellis/spec/frontend/type-safety.md` | Frontend API wrapper/type-safety constraints. |
| `.trellis/spec/frontend/state-management.md` | Frontend session-state and server-state constraints. |
| `.trellis/spec/frontend/component-guidelines.md` | Frontend component conventions relevant to prompt/dialog placement. |
| `.trellis/spec/guides/cross-layer-thinking-guide.md` | Cross-layer mapping guide for data flow and boundary analysis. |

### Code Patterns

#### Current task and scope resolution

- Running `python3 .trellis/scripts/task.py current --source` reported no current task, so the explicitly requested task path was used: `.trellis/tasks/05-19-security-p1d-step-up-auth-high-risk`.
- The project context script reported a single-repo project with spec layers `backend` and `frontend`.
- Searches for existing step-up/reauth implementation terms (`step_up`, `step-up`, `reauth`, `re-auth`, `elevat`, `second factor`, `second_factor`, `mfa`, `webauthn`) returned no matches in the searched backend/frontend/spec paths.

#### Backend JWT, TOTP, token-version, and lockout patterns

- `backend/internal/auth/jwt.go` defines JWT claims with fields directly relevant to a short-lived proof: `Purpose`, `TokenVersion`, registered `ID`/JTI, issue time, expiry, and subject.

```go
// backend/internal/auth/jwt.go
type Claims struct {
    UserID       uint   `json:"uid"`
    Username     string `json:"username"`
    Role         string `json:"role"`
    Purpose      string `json:"purpose,omitempty"`
    TokenVersion uint   `json:"ver"`
    jwt.RegisteredClaims
}
```

- `backend/internal/auth/jwt.go` already has a short-lived token pattern: `Generate2FAPendingToken` issues a 5-minute JWT with `Purpose: "2fa_pending"` and the current user `TokenVersion`.

```go
// backend/internal/auth/jwt.go
// Generate2FAPendingToken 生成用于 2FA 验证步骤的短期令牌（5 分钟有效）。
func (m *JWTManager) Generate2FAPendingToken(user model.User) (string, error) {
    now := time.Now()
    tokenID, err := generateTokenID()
    if err != nil {
        return "", err
    }
    claims := Claims{
        UserID:       user.ID,
        Username:     user.Username,
        Role:         user.Role,
        Purpose:      "2fa_pending",
        TokenVersion: user.TokenVersion,
        RegisteredClaims: jwt.RegisteredClaims{
            ID:        tokenID,
            IssuedAt:  jwt.NewNumericDate(now),
            ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
            Subject:   fmt.Sprintf("%d", user.ID),
        },
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(m.secret)
}
```

- `backend/internal/middleware/auth.go` rejects `2fa_pending` tokens for normal REST access, validates the JWT with `JWTManager.ParseToken`, checks DB `token_version`, and sets context keys used by handlers and audit helpers.

```go
// backend/internal/middleware/auth.go
if claims.Purpose == "2fa_pending" {
    c.JSON(http.StatusUnauthorized, gin.H{"error": "需要完成两步验证"})
    c.Abort()
    return
}
// 校验 token_version：密码修改、角色变更、2FA 禁用后旧 token 自动失效
if db != nil {
    var user model.User
    if err := db.Select("token_version").First(&user, claims.UserID).Error; err != nil {
        c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在或已删除"})
        c.Abort()
        return
    }
    if user.TokenVersion != claims.TokenVersion {
        c.JSON(http.StatusUnauthorized, gin.H{"error": "token 已失效，请重新登录"})
        c.Abort()
        return
    }
}
c.Set(CtxUserID, claims.UserID)
c.Set(CtxUsername, claims.Username)
c.Set(CtxRole, claims.Role)
c.Set(CtxToken, parts[1])
```

- `backend/internal/api/handlers/realtime_auth.go` applies equivalent token checks for WebSocket flows, including `Purpose != "2fa_pending"`, DB token-version validation, and optional role/permission requirements.

```go
// backend/internal/api/handlers/realtime_auth.go
claims, err := jwtManager.ParseToken(strings.TrimSpace(token))
if err != nil {
    return nil, fmt.Errorf("token 无效或过期")
}
if claims.Purpose == "2fa_pending" {
    return nil, fmt.Errorf("需要完成两步验证")
}

if db != nil {
    var user model.User
    if err := db.Select("token_version").First(&user, claims.UserID).Error; err != nil {
        return nil, fmt.Errorf("用户不存在或已删除")
    }
    if user.TokenVersion != claims.TokenVersion {
        return nil, fmt.Errorf("token 已失效，请重新登录")
    }
}
```

- `backend/internal/auth/service.go` returns a `LoginResult` with `Requires2FA` and `LoginToken` when `user.TOTPEnabled` is true; otherwise it returns a full JWT.

```go
// backend/internal/auth/service.go
type LoginResult struct {
    Token      string
    User       *model.User
    Requires2FA bool
    LoginToken  string // 仅在 Requires2FA=true 时有效
}
```

```go
// backend/internal/auth/service.go
if user.TOTPEnabled {
    loginToken, err := s.jwt.Generate2FAPendingToken(user)
    if err != nil {
        return nil, err
    }
    return &LoginResult{Requires2FA: true, LoginToken: loginToken, User: &user}, nil
}

token, err := s.jwt.GenerateToken(user)
```

- `backend/internal/api/handlers/auth_handler.go` handles pending 2FA login tokens by parsing the token, requiring `claims.Purpose == "2fa_pending"`, validating TOTP or recovery code, and issuing a full JWT with `GenerateToken`.

```go
// backend/internal/api/handlers/auth_handler.go
claims, err := h.jwtManager.ParseToken(req.LoginToken)
if err != nil {
    respondUnauthorized(c, "登录令牌无效或已过期")
    return
}
if claims.Purpose != "2fa_pending" {
    respondUnauthorized(c, "登录令牌无效")
    return
}
```

- `backend/internal/auth/totp.go` contains the TOTP validation primitive and recovery-code consumption primitive.

```go
// backend/internal/auth/totp.go
func ValidateTOTP(secret, code string) bool {
    return totp.Validate(code, secret)
}
```

```go
// backend/internal/auth/totp.go
func ValidateAndConsumeRecoveryCode(storedJSON, code string) ([]string, bool) {
    var codes []string
    if err := json.Unmarshal([]byte(storedJSON), &codes); err != nil {
        return nil, false
    }
    remaining := make([]string, 0, len(codes))
    found := false
    for _, c := range codes {
        if subtle.ConstantTimeCompare([]byte(c), []byte(code)) == 1 {
            found = true
            continue
        }
        remaining = append(remaining, c)
    }
    if !found {
        return nil, false
    }
    return remaining, true
}
```

- `backend/internal/auth/login_lock.go` is the existing DB-backed authentication failure limiter. It is keyed by normalized username/client IP and has default threshold/duration behavior in code: 5 failures and 15 minutes, with settings overrides.

```go
// backend/internal/auth/login_lock.go
func (l *LoginFailureLocker) IsLocked(username, ip string, now time.Time) (time.Time, bool) {
    u, i := normalize(username, ip)
    var rec model.LoginFailure
    if l.db.Where("username = ? AND client_ip = ?", u, i).Limit(1).Find(&rec).RowsAffected == 0 {
        return time.Time{}, false
    }
    if rec.LockedUntil != nil && rec.LockedUntil.After(now) {
        return *rec.LockedUntil, true
    }
    if rec.LockedUntil != nil {
        l.db.Delete(&rec)
    }
    return time.Time{}, false
}
```

- `backend/internal/model/models.go` encrypts TOTP secrets and recovery codes through GORM hooks on `User`, and `backend/internal/model/token_revocation.go` provides a persistent token revocation table.

```go
// backend/internal/model/models.go
type User struct {
    ID            uint      `gorm:"primaryKey" json:"id"`
    Username      string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
    PasswordHash  string    `gorm:"size:255;not null" json:"-"`
    Role          string    `gorm:"size:32;not null;index" json:"role"`
    TOTPSecret    string    `gorm:"size:255" json:"-"`
    TOTPEnabled   bool      `json:"totp_enabled"`
    RecoveryCodes string    `gorm:"type:text" json:"-"`
    TokenVersion  uint      `gorm:"not null;default:0" json:"-"`
    Onboarded     bool      `gorm:"not null;default:false" json:"onboarded"`
    CreatedAt     time.Time `json:"created_at"`
    UpdatedAt     time.Time `json:"updated_at"`
}
```

```go
// backend/internal/model/token_revocation.go
type TokenRevocation struct {
    ID        uint      `gorm:"primaryKey" json:"id"`
    TokenHash string    `gorm:"uniqueIndex;size:128;not null" json:"token_hash"`
    UserID    uint      `gorm:"index;not null" json:"user_id"`
    ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
    CreatedAt time.Time `json:"created_at"`
}
```

#### Backend route surfaces likely requiring step-up

- `backend/internal/api/router.go` registers the listed high-risk HTTP routes under `secured`, after `AuthMiddleware`, `AuditLogger`, API rate limiting, and max-body middleware. Relevant route registrations observed:

```go
// backend/internal/api/router.go
secured.GET("/ssh-keys/export", middleware.RBAC("ssh_keys:read"), sshKeyHandler.Export)

secured.POST("/tasks/batch-trigger", middleware.RBAC("tasks:write"), taskHandler.BatchTrigger)
secured.POST("/tasks/:id/trigger", middleware.RBAC("tasks:trigger"), middleware.OwnershipTaskCheck(dep.DB), taskHandler.Trigger)
secured.POST("/tasks/:id/restore", middleware.RequireRole("admin"), taskHandler.Restore)

secured.POST("/batch-commands", middleware.RBAC("tasks:write"), batchHandler.Create)

secured.POST("/tasks/:id/snapshots/:sid/restore", middleware.RequireRole("admin"), snapshotHandler.Restore)

secured.GET("/config/export", middleware.RequireRole("admin"), configHandler.Export)
secured.POST("/config/import", middleware.RequireRole("admin"), configHandler.Import)
```

- Terminal WebSocket is registered outside `secured` because browser WebSocket APIs cannot set custom HTTP headers; auth is performed in the WebSocket protocol's first message.

```go
// backend/internal/api/router.go
// WebSocket 路由放在 secured 外部：浏览器 WebSocket API 无法设置自定义 HTTP 头，
// 因此无法通过 AuthMiddleware。认证改由 WS 协议内首条消息完成（含 RBAC 校验）。
v1.GET("/ws/logs", wsHandler.ServeWS)
v1.GET("/ws/terminal", terminalHandler.ServeTerminal)
```

- RBAC and ownership behavior for these routes is supplied by `backend/internal/middleware/rbac.go` and `backend/internal/middleware/ownership.go`. `RequireRole` returns 403 unless `CurrentRole(c)` equals the required role; `RBAC` checks the permission map; task/node ownership checks consult `node_owners` for non-admin/non-viewer access.

```go
// backend/internal/middleware/rbac.go
func RequireRole(role string) gin.HandlerFunc {
    return func(c *gin.Context) {
        if CurrentRole(c) != role {
            c.JSON(http.StatusForbidden, gin.H{"error": "权限不足"})
            c.Abort()
            return
        }
        c.Next()
    }
}
```

```go
// backend/internal/middleware/ownership.go
if err := db.Table("tasks").
    Joins("JOIN node_owners ON node_owners.node_id = tasks.node_id").
    Where("tasks.id = ? AND node_owners.user_id = ?", taskID, userID).
    Count(&count).Error; err != nil {
    ...
}
if count == 0 {
    c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该任务"})
    c.Abort()
    return
}
```

#### SSH key export surface

- `backend/internal/api/handlers/ssh_key_handler.go` handles `GET /ssh-keys/export` with `format`, `scope`, and optional `ids` query parameters. It uses `visibleSSHKeysQuery(c)` for role/ownership visibility, validates the export purpose with `sshutil.ValidateSSHKeyPurpose(item, sshutil.PurposeSSHKeyExport)`, exports derived public key formats, and writes a credential audit event.

```go
// backend/internal/api/handlers/ssh_key_handler.go
writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
    Action:           "ssh_key.export",
    Purpose:          sshutil.PurposeSSHKeyExport,
    CredentialKind:   "ssh_key",
    CredentialSource: "ssh_key_export",
    Outcome:          credentialAuditOutcome(len(exportable), 0, blockedCount),
    Metadata: map[string]any{
        "format":          format,
        "scope":           scope,
        "requested_count": len(items),
        "exported_count":  len(exportable),
        "blocked_count":   blockedCount,
    },
})
```

#### Config export with secrets surface

- `backend/internal/api/handlers/config_handler.go` handles `GET /config/export`; route registration requires admin role. `include_secrets=true` causes node passwords/private keys, SSH private keys, and task executor configs to be included in the exported payload. The handler writes blocked and success credential-audit events for config export.

```go
// backend/internal/api/handlers/config_handler.go
includeSecrets := c.Query("include_secrets") == "true"
```

```go
// backend/internal/api/handlers/config_handler.go
if includeSecrets {
    item["password"] = n.Password
    item["private_key"] = n.PrivateKey
}
...
if includeSecrets {
    item["private_key"] = k.PrivateKey
}
...
if includeSecrets && t.ExecutorConfig != "" {
    item["executor_config"] = t.ExecutorConfig
}
```

```go
// backend/internal/api/handlers/config_handler.go
writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
    Action:           "config.export",
    Purpose:          "config_export",
    CredentialKind:   "config_export",
    CredentialSource: "config.export",
    Outcome:          credentialaudit.OutcomeSuccess,
    Metadata: map[string]any{
        "stage":          "success",
        "with_sensitive": includeSecrets,
        "node_count":     len(exportNodes),
        "key_count":      len(exportKeys),
        "policy_count":   len(exportPolicies),
        "task_count":     len(exportTasks),
        "setting_count":  len(exportSettings),
    },
})
```

#### Terminal WebSocket open surface

- `backend/internal/api/handlers/terminal_handler.go` accepts a WebSocket connection, reads an auth message within 5 seconds, calls `authorizeRealtimeToken` with `Role: "admin"`, builds SSH auth for `sshutil.PurposeTerminal`, and writes terminal credential audit events for auth/open/failure/close stages.

```go
// backend/internal/api/handlers/terminal_handler.go
type terminalAuthMessage struct {
    Type  string `json:"type"`
    Token string `json:"token"`
}
```

```go
// backend/internal/api/handlers/terminal_handler.go
_, rawMsg, err := conn.ReadMessage()
...
var authMsg terminalAuthMessage
if err := json.Unmarshal(rawMsg, &authMsg); err != nil || authMsg.Type != "auth" {
    ...
}

claims, err := authorizeRealtimeToken(authMsg.Token, h.jwtManager, h.db, realtimeAuthRequirements{Role: "admin"})
```

```go
// backend/internal/api/handlers/terminal_handler.go
authMethods, credential, err := sshutil.BuildSSHAuthForPurpose(node, h.db, sshutil.PurposeTerminal)
```

```go
// backend/internal/api/handlers/terminal_handler.go
h.writeTerminalCredentialAudit(c, claims, credentialaudit.Event{
    Action:           "terminal.open",
    Purpose:          sshutil.PurposeTerminal,
    CredentialKind:   credential.Kind,
    CredentialSource: credential.Source,
    SSHKeyID:         credential.KeyID,
    NodeID:           credentialaudit.PtrUint(node.ID),
    Outcome:          credentialaudit.OutcomeSuccess,
    Metadata: map[string]any{
        "stage":      "open",
        "session_id": sessionID,
    },
})
```

#### Task trigger, batch trigger, and task restore surfaces

- `backend/internal/api/handlers/task_handler.go` defines `TaskRunner` methods used by manual trigger and restore flows.

```go
// backend/internal/api/handlers/task_handler.go
type TaskRunner interface {
    TriggerManual(taskID uint) (uint, error)
    TriggerRestore(taskID uint, targetPath string) (uint, error)
    SyncSchedule(task model.Task) error
    RemoveSchedule(taskID uint)
    Cancel(taskID uint) error
    Pause(taskID uint, cancelRunning bool) error
    Resume(taskID uint) error
    SetSkipNext(taskID uint) error
}
```

- Manual trigger writes `task.manual_trigger` credential audit events for failure and success, including `TaskID`, optional `TaskRunID`, `NodeID`, purpose selected by task executor/source, and bounded metadata.

```go
// backend/internal/api/handlers/task_handler.go
runID, err := h.runner.TriggerManual(id)
if err != nil {
    writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
        Action:       "task.manual_trigger",
        Purpose:      auditPurpose,
        NodeID:       nodeID,
        TaskID:       credentialaudit.PtrUint(id),
        Outcome:      credentialaudit.OutcomeFailure,
        ErrorMessage: err.Error(),
        Metadata:     auditMetadata,
    })
    respondBadRequest(c, err.Error())
    return
}
writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
    Action:    "task.manual_trigger",
    Purpose:   auditPurpose,
    NodeID:    nodeID,
    TaskID:    credentialaudit.PtrUint(id),
    TaskRunID: credentialaudit.PtrUint(runID),
    Outcome:   credentialaudit.OutcomeSuccess,
    Metadata:  auditMetadata,
})
```

- Task restore writes `task.restore_trigger` credential audit events for failure and success, including `TaskID`, optional `TaskRunID`, `PurposeTaskRestore`, and `custom_target` metadata.

```go
// backend/internal/api/handlers/task_handler.go
runID, err := h.runner.TriggerRestore(id, req.TargetPath)
if err != nil {
    writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
        Action:       "task.restore_trigger",
        Purpose:      sshutil.PurposeTaskRestore,
        TaskID:       credentialaudit.PtrUint(id),
        Outcome:      credentialaudit.OutcomeFailure,
        ErrorMessage: err.Error(),
        Metadata: map[string]any{
            "custom_target": strings.TrimSpace(req.TargetPath) != "",
        },
    })
    respondBadRequest(c, err.Error())
    return
}

writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
    Action:    "task.restore_trigger",
    Purpose:   sshutil.PurposeTaskRestore,
    TaskID:    credentialaudit.PtrUint(id),
    TaskRunID: credentialaudit.PtrUint(runID),
    Outcome:   credentialaudit.OutcomeSuccess,
    Metadata: map[string]any{
        "custom_target": strings.TrimSpace(req.TargetPath) != "",
    },
})
```

- Batch trigger writes a single `task.batch_trigger` credential audit event summarizing requested task count and success/failure/blocked counts.

```go
// backend/internal/api/handlers/task_handler.go
writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
    Action:  "task.batch_trigger",
    Purpose: sshutil.PurposeTaskCommand,
    Outcome: credentialAuditOutcome(successCount, failureCount, blockedCount),
    Metadata: map[string]any{
        "task_count":    len(req.TaskIDs),
        "success_count": successCount,
        "failure_count": failureCount,
        "blocked_count": blockedCount,
        "purpose":       "mixed_task_trigger",
    },
})
```

#### Snapshot restore surface

- `backend/internal/api/handlers/snapshot_handler.go` handles snapshot restore. The handler validates `includes` and `targetPath`, blocks dangerous absolute target paths, calls `ResticExecutor.RestoreFiles`, and returns a message. No credential-audit write was observed in this handler during this research.

```go
// backend/internal/api/handlers/snapshot_handler.go
var dangerousRestorePaths = []string{
    "/bin", "/sbin", "/usr", "/lib", "/lib64",
    "/boot", "/dev", "/proc", "/sys", "/run",
    "/etc", "/var/run",
}

func validateRestoreTargetPath(targetPath string) bool {
    cleaned := filepath.Clean(targetPath)
    if !filepath.IsAbs(cleaned) {
        return false
    }
    if cleaned == "/" {
        return false
    }
    for _, prefix := range dangerousRestorePaths {
        if cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/") {
            return false
        }
    }
    return true
}
```

```go
// backend/internal/api/handlers/snapshot_handler.go
type restoreRequest struct {
    Includes   []string `json:"includes" binding:"required"`
    TargetPath string   `json:"targetPath" binding:"required"`
}
...
if !validateRestoreTargetPath(req.TargetPath) {
    respondBadRequest(c, "恢复目标路径不安全，不允许恢复到系统目录")
    return
}
...
exec := &executor.ResticExecutor{}
if err := exec.RestoreFiles(c.Request.Context(), task, snapshotID, req.Includes, req.TargetPath); err != nil {
    respondInternalError(c, err)
    return
}
respondMessage(c, "恢复成功")
```

#### Batch command creation surface

- `backend/internal/api/handlers/batch_handler.go` handles batch command creation, command validation, task/run creation, and writes `batch_command.create` credential audit with counts and batch ID.

```go
// backend/internal/api/handlers/batch_handler.go
writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
    Action:  "batch_command.create",
    Purpose: sshutil.PurposeBatchCommand,
    Outcome: credentialAuditOutcome(successCount, failureCount, 0),
    Metadata: map[string]any{
        "batch_id":      batchID,
        "node_count":    len(nodes),
        "task_count":    len(taskIDs),
        "run_count":     len(runIDs),
        "success_count": successCount,
        "failure_count": failureCount,
        "retain":        retain,
    },
})
```

- The handler includes a dangerous-command safety net with patterns for operations such as destructive root removal, `mkfs`, `dd of=/dev/`, shutdown/reboot/halt/poweroff, and disk wiping.

#### Credential audit write helpers and bounded evidence constraints

- `backend/internal/credentialaudit/audit.go` defines domain-specific event shape and outcome constants. The model comment in `backend/internal/model/models.go` states that credential audit records must not contain raw secrets, terminal streams, command output, or executor config.

```go
// backend/internal/credentialaudit/audit.go
const (
    OutcomeSuccess     = "success"
    OutcomeFailure     = "failure"
    OutcomeBlocked     = "blocked"
    maxMetadataEntries = 16
)
```

```go
// backend/internal/credentialaudit/audit.go
type Event struct {
    UserID           uint
    Username         string
    Role             string
    Action           string
    Purpose          string
    CredentialKind   string
    CredentialSource string
    SSHKeyID         *uint
    NodeID           *uint
    TaskID           *uint
    TaskRunID        *uint
    PolicyID         *uint
    Outcome          string
    ErrorMessage     string
    Metadata         map[string]any
    ClientIP         string
    UserAgent        string
}
```

```go
// backend/internal/model/models.go
// CredentialAuditEvent stores domain-specific evidence that a credential was used
// or attempted for a high-risk operation. It must never contain raw secrets,
// terminal streams, command output, or executor config.
type CredentialAuditEvent struct {
    ID               uint      `gorm:"primaryKey" json:"id"`
    UserID           uint      `gorm:"index" json:"user_id"`
    Username         string    `gorm:"size:64;index" json:"username"`
    Role             string    `gorm:"size:32;index" json:"role"`
    Action           string    `gorm:"size:64;not null;index" json:"action"`
    Purpose          string    `gorm:"size:64;not null;index" json:"purpose"`
    CredentialKind   string    `gorm:"size:32;not null;index" json:"credential_kind"`
    CredentialSource string    `gorm:"size:64;not null" json:"credential_source"`
    SSHKeyID         *uint     `gorm:"index" json:"ssh_key_id,omitempty"`
    NodeID           *uint     `gorm:"index" json:"node_id,omitempty"`
    TaskID           *uint     `gorm:"index" json:"task_id,omitempty"`
    TaskRunID        *uint     `gorm:"index" json:"task_run_id,omitempty"`
    PolicyID         *uint     `gorm:"index" json:"policy_id,omitempty"`
    Outcome          string    `gorm:"size:16;not null;index" json:"outcome"`
    ErrorMessage     string    `gorm:"type:text;not null;default:''" json:"error_message,omitempty"`
    Metadata         string    `gorm:"type:text;not null;default:'{}'" json:"metadata,omitempty"`
    ClientIP         string    `gorm:"size:64" json:"client_ip"`
    UserAgent        string    `gorm:"size:255" json:"user_agent"`
    CreatedAt        time.Time `gorm:"index" json:"created_at"`
}
```

- `credentialaudit.Write` sanitizes user-facing strings, bounds field lengths, defaults empty outcomes to success, sanitizes metadata, serializes metadata as JSON, and writes `model.CredentialAuditEvent`.

```go
// backend/internal/credentialaudit/audit.go
func Write(db *gorm.DB, event Event) error {
    if db == nil {
        return nil
    }
    outcome := strings.TrimSpace(event.Outcome)
    if outcome == "" {
        outcome = OutcomeSuccess
    }
    metadata := sanitizeMetadata(event.Metadata)
    metadataJSON, err := json.Marshal(metadata)
    if err != nil {
        metadataJSON = []byte("{}")
    }
    record := model.CredentialAuditEvent{
        UserID:           event.UserID,
        Username:         bound(util.SanitizeMessage(event.Username), 64),
        Role:             bound(util.SanitizeMessage(event.Role), 32),
        Action:           bound(util.SanitizeMessage(event.Action), 64),
        Purpose:          bound(util.SanitizeMessage(event.Purpose), 64),
        CredentialKind:   bound(util.SanitizeMessage(event.CredentialKind), 32),
        CredentialSource: bound(util.SanitizeMessage(event.CredentialSource), 64),
        SSHKeyID:         event.SSHKeyID,
        NodeID:           event.NodeID,
        TaskID:           event.TaskID,
        TaskRunID:        event.TaskRunID,
        PolicyID:         event.PolicyID,
        Outcome:          bound(util.SanitizeMessage(outcome), 16),
        ErrorMessage:     sanitizeErrorMessage(event.ErrorMessage),
        Metadata:         string(metadataJSON),
        ClientIP:         bound(util.SanitizeMessage(event.ClientIP), 64),
        UserAgent:        bound(util.SanitizeMessage(event.UserAgent), 255),
    }
    return db.Create(&record).Error
}
```

- Metadata keys are dropped if they contain markers including `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, or `payload`. String values are dropped if they contain those markers plus bearer/authorization markers. Metadata is capped at 16 entries and list values are capped.

```go
// backend/internal/credentialaudit/audit.go
func metadataKeyDenied(key string) bool {
    lower := strings.ToLower(key)
    for _, forbidden := range []string{"private", "password", "token", "secret", "credential", "config", "output", "stream", "command", "content", "payload"} {
        if strings.Contains(lower, forbidden) {
            return true
        }
    }
    return false
}

func metadataValueDenied(value string) bool {
    lower := strings.ToLower(value)
    for _, forbidden := range []string{"private", "password", "token", "secret", "credential", "config", "output", "stream", "command", "content", "payload", "bearer", "authorization:"} {
        if strings.Contains(lower, forbidden) {
            return true
        }
    }
    return false
}
```

- `backend/internal/api/handlers/helpers.go` wraps credential audit writes with Gin-derived actor and request metadata.

```go
// backend/internal/api/handlers/helpers.go
func writeCredentialAuditFromGin(c *gin.Context, db *gorm.DB, event credentialaudit.Event) {
    if err := credentialaudit.Write(db, credentialaudit.FromGin(c, event)); err != nil {
        logger.Module("credential_audit").Warn().Err(err).
            Str("action", event.Action).
            Str("purpose", event.Purpose).
            Msg("凭据审计事件写入失败")
    }
}

func credentialAuditOutcome(successCount, failureCount, blockedCount int) string {
    switch {
    case blockedCount > 0:
        return credentialaudit.OutcomeBlocked
    case failureCount > 0:
        return credentialaudit.OutcomeFailure
    case successCount > 0:
        return credentialaudit.OutcomeSuccess
    default:
        return credentialaudit.OutcomeFailure
    }
}
```

- `backend/internal/api/handlers/credential_audit_handler.go` re-sanitizes metadata and errors on list/export. The same forbidden key/value markers are present there; error messages redact output markers, endpoints, bearer/token/private-key/password-style patterns, and stack-like errors.

#### SSH purpose and credential source patterns

- `backend/internal/sshutil/scope.go` defines purpose constants that already label several target operations: `ssh_key_export`, `terminal`, `task_command`, `batch_command`, `task_restore`, `snapshot`, and related operational scopes.

```go
// backend/internal/sshutil/scope.go
const (
    PurposeSSHKeyTest     = "ssh_key_test"
    PurposeSSHKeyExport   = "ssh_key_export"
    PurposeNodeTest       = "node_test"
    PurposeTerminal       = "terminal"
    PurposeTaskCommand    = "task_command"
    PurposeBatchCommand   = "batch_command"
    PurposeDrill          = "drill"
    PurposeProbe          = "probe"
    PurposeFileBrowser    = "file_browser"
    PurposeDockerVolumes  = "docker_volumes"
    PurposeNodeLogs       = "node_logs"
    PurposeTaskBackup     = "task_backup"
    PurposeTaskRestore    = "task_restore"
    PurposeTaskHook       = "task_hook"
    PurposeSnapshot       = "snapshot"
    PurposeSnapshotDiff   = "snapshot_diff"
    PurposeIntegrityCheck = "integrity_check"
    PurposeRetention      = "retention"
    PurposeNodeMigration  = "node_migration"
)
```

- `ValidateSSHKeyPurpose` checks disabled/expired keys and `AllowedPurposes` membership.

```go
// backend/internal/sshutil/scope.go
func ValidateSSHKeyPurpose(key model.SSHKey, purpose string) error {
    if key.Disabled {
        return fmt.Errorf("SSH Key 已禁用")
    }
    if key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC()) {
        return fmt.Errorf("SSH Key 已过期")
    }
    purpose = NormalizePurpose(purpose)
    if purpose != "" && !scopeContains(key.AllowedPurposes, purpose) {
        return fmt.Errorf("SSH Key 不允许用于当前操作")
    }
    return nil
}
```

- `backend/internal/sshutil/ssh_auth.go` resolves SSH auth methods and credential source metadata for a supplied purpose.

```go
// backend/internal/sshutil/ssh_auth.go
func BuildSSHAuthForPurpose(node model.Node, db *gorm.DB, purpose string) ([]ssh.AuthMethod, ResolvedCredential, error) {
    return BuildSSHAuthWithCredential(node, db, purpose)
}
```

#### Frontend auth context and API patterns

- `web/src/context/auth-context-provider.tsx` stores auth state in `sessionStorage` under `xirang-auth-token`, `xirang-username`, `xirang-role`, `xirang-user-id`, and `xirang-totp-enabled`; it removes legacy `localStorage` values during login/logout.

```ts
// web/src/context/auth-context-provider.tsx
const AUTH_TOKEN_KEY = "xirang-auth-token";
const AUTH_USERNAME_KEY = "xirang-username";
const AUTH_ROLE_KEY = "xirang-role";
const AUTH_USER_ID_KEY = "xirang-user-id";
const AUTH_TOTP_ENABLED_KEY = "xirang-totp-enabled";
```

```ts
// web/src/context/auth-context-provider.tsx
safeSetItem(sessionStorageRef, AUTH_TOKEN_KEY, nextToken);
safeSetItem(sessionStorageRef, AUTH_USERNAME_KEY, nextUsername);
...
safeSetItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY, String(totpEnabledValue));
safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
```

- `web/src/context/auth-context.shared.ts` exposes `token`, `username`, `role`, `userId`, `totpEnabled`, `isAuthenticated`, `login`, `logout`, and `setTotpEnabled`.

```ts
// web/src/context/auth-context.shared.ts
export type AuthRole = "admin" | "operator" | "viewer";

export type AuthContextValue = {
  token: string | null;
  username: string | null;
  role: AuthRole | null;
  userId: number | null;
  totpEnabled: boolean;
  isAuthenticated: boolean;
  login: (
    token: string,
    username: string,
    role?: AuthRole,
    userId?: number,
    totpEnabled?: boolean
  ) => void;
  logout: () => void;
  setTotpEnabled: (enabled: boolean) => void;
};
```

- `web/src/lib/api/core.ts` defines `ApiError`, central `request<T>()`, envelope unwrapping, and 401 behavior. For non-public auth paths, any 401 clears session storage and redirects to login before throwing `ApiError(401, "session expired", payload)`.

```ts
// web/src/lib/api/core.ts
export class ApiError extends Error {
  status: number;
  detail?: unknown;

  constructor(status: number, message: string, detail?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.detail = detail;
  }
}
```

```ts
// web/src/lib/api/core.ts
const AUTH_PUBLIC_PATHS = ["/auth/login", "/auth/captcha", "/auth/2fa/login"];
if (response.status === 401 && !AUTH_PUBLIC_PATHS.includes(path)) {
  try {
    sessionStorage.removeItem("xirang-auth-token");
    sessionStorage.removeItem("xirang-username");
    sessionStorage.removeItem("xirang-role");
    sessionStorage.removeItem("xirang-user-id");
    sessionStorage.removeItem("xirang-totp-enabled");
  } catch { /* ignore */ }
  if (typeof window !== "undefined") {
    window.location.href = buildLoginRedirectPath(window.location);
  }
  throw new ApiError(401, "session expired", payload);
}
```

- `web/src/lib/api/auth-api.ts` treats login response as either a full token response or a `requires_2fa` response. `web/src/lib/api/totp-api.ts` sends pending login token plus TOTP code to `/auth/2fa/login`.

```ts
// web/src/lib/api/auth-api.ts
if (!("token" in result) && !("requires_2fa" in result)) {
  throw new ApiError(500, i18n.t("login.errorLoginFormat"), result);
}
return result;
```

```ts
// web/src/lib/api/totp-api.ts
async totpLogin(loginToken: string, totpCode: string): Promise<TOTPLoginResponse> {
  return request<TOTPLoginResponse>("/auth/2fa/login", {
    method: "POST",
    body: { login_token: loginToken, totp_code: totpCode },
  });
}
```

#### Frontend high-risk UI surfaces and call paths

- SSH key export uses `web/src/lib/api/ssh-keys-api.ts` to build a URL and `web/src/components/ssh-key-export-dialog.tsx` to issue direct `fetch` with an `Authorization` header for file download.

```ts
// web/src/lib/api/ssh-keys-api.ts
getExportUrl(format: "authorized_keys" | "json" | "csv", scope: "all" | "in_use", ids?: string[]): string {
  const params = new URLSearchParams({ format, scope });
  if (ids?.length) {
    const numericIds = ids.map((id) => parseNumericId(id, "key"));
    params.set("ids", numericIds.join(","));
  }
  return `/api/v1/ssh-keys/export?${params.toString()}`;
},
```

```ts
// web/src/components/ssh-key-export-dialog.tsx
const response = await fetch(url, {
  headers: { Authorization: `Bearer ${token}` },
});

if (!response.ok) {
  throw new Error(`HTTP ${response.status}`);
}
```

- Config export wrapper supports `includeSecrets`, while the current UI call in `web/src/components/config-export-import.tsx` uses the default `includeSecrets=false`. Search did not find a frontend call currently passing `includeSecrets=true`.

```ts
// web/src/lib/api/config-api.ts
async exportConfig(token: string, includeSecrets = false): Promise<ConfigExportPayload> {
  const query = includeSecrets ? "?include_secrets=true" : "";
  return request<ConfigExportPayload>(`/config/export${query}`, { token });
},
```

```ts
// web/src/components/config-export-import.tsx
const data = await apiClient.exportConfig(token);
```

- Terminal UI opens `web/src/components/web-terminal.tsx`, which connects to `/api/v1/ws/terminal?node_id=...` and sends `{ type: "auth", token }` in `onOpen`.

```ts
// web/src/components/web-terminal.tsx
const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
const wsURL = `${protocol}//${window.location.host}/api/v1/ws/terminal?node_id=${nodeId}`;

socket = new ReconnectingSocket({
  url: wsURL,
  binaryType: "arraybuffer",
  heartbeatIntervalMs: 0,
  onOpen: (ws) => {
    ws.send(JSON.stringify({ type: "auth", token }));
  },
```

- Task trigger and batch-trigger UI handlers live in `web/src/pages/tasks-page.tsx`, using `triggerTask` from hooks and `apiClient.batchTriggerTasks`.

```ts
// web/src/pages/tasks-page.tsx
const handleTrigger = async (taskId: number) => {
  try {
    setPendingAction({ id: taskId, action: "trigger" });
    await triggerTask(taskId);
    toast.success(t("tasks.triggerSuccess", { id: taskId }));
  } catch (error) {
    toast.error(getErrorMessage(error));
  } finally {
    setPendingAction(null);
  }
};
```

```ts
// web/src/pages/tasks-page.tsx
const result = await apiClient.batchTriggerTasks(authToken!, selectedTaskIds);
```

- Task restore uses `web/src/components/restore-confirm-dialog.tsx` calling `apiClient.restoreTask`; snapshot restore uses `web/src/components/snapshot-browser.tsx` calling `apiClient.restoreSnapshot`.

```ts
// web/src/components/restore-confirm-dialog.tsx
const result = await apiClient.restoreTask(
  token,
  taskId,
  targetPath.trim() || undefined
);
```

```ts
// web/src/components/snapshot-browser.tsx
await apiClient.restoreSnapshot(
  token,
  taskId,
  selectedSnapshot.id,
  Array.from(selectedPaths),
  restoreTarget
);
```

- Batch command creation uses `web/src/components/batch-command-dialog.tsx` calling `apiClient.createBatchCommand`.

```ts
// web/src/components/batch-command-dialog.tsx
const result = await apiClient.createBatchCommand(
  token,
  selectedNodeIds,
  command.trim(),
  name.trim() || undefined,
  retain
);
```

#### Frontend credential audit mapping

- `web/src/lib/api/credential-audit-api.ts` includes known action strings for the high-risk surfaces already represented in credential audit: `ssh_key.export`, `terminal.open`, `terminal.failure`, `terminal.close`, `task.manual_trigger`, `task.restore_trigger`, `task.batch_trigger`, `batch_command.create`, and `config.export`.

```ts
// web/src/lib/api/credential-audit-api.ts
const knownActions = new Set<string>([
  "ssh_key.test_connection",
  "ssh_key.export",
  "node.credential.test_connection",
  "terminal.open",
  "terminal.failure",
  "terminal.close",
  "task.manual_trigger",
  "task.restore_trigger",
  "task.batch_trigger",
  "batch_command.create",
  "task.credential.use",
  "drill.trigger",
  "drill.phase",
  "file_browser.list",
  "file_browser.preview",
  "docker_volumes.discover",
  "config.export",
  "node.doctor.run",
  "node_migration.preflight",
  "probe.ssh",
  "probe.metrics",
  "node_logs.collect",
]);
```

- `web/src/types/domain.ts` defines the matching `CredentialAuditAction` union, with a fallback `"other"`.

#### Response envelope and error constraints

- `backend/internal/api/handlers/response.go` centralizes the response envelope as `{ code, message, data }`. Unauthorized and forbidden helpers use 401/403 status codes and localized message strings.

```go
// backend/internal/api/handlers/response.go
type Response struct {
    Code    int         `json:"code"`
    Message string      `json:"message"`
    Data    interface{} `json:"data"`
}

func respondUnauthorized(c *gin.Context, msg string) {
    c.JSON(http.StatusUnauthorized, Response{Code: http.StatusUnauthorized, Message: msg, Data: nil})
}

func respondForbidden(c *gin.Context, msg string) {
    c.JSON(http.StatusForbidden, Response{Code: http.StatusForbidden, Message: msg, Data: nil})
}
```

### External References

- None. This research was internal codebase/spec research only.

### Related Specs

- `.trellis/spec/backend/error-handling.md` — backend API responses use a unified envelope; errors must not expose secrets, command output, file content, exported config payloads, or stack-like errors.
- `.trellis/spec/backend/quality-guidelines.md` — backend route changes must retain correct auth/RBAC/ownership checks; sensitive fields must be sanitized; schema changes require SQLite and PostgreSQL migrations; security-sensitive code requires denial tests.
- `.trellis/spec/frontend/type-safety.md` — raw API wire types stay private to API wrappers; `request<T>()` unwraps envelopes and throws `ApiError`; normal JSON API calls use the central request wrapper.
- `.trellis/spec/frontend/state-management.md` — auth/session state belongs in `sessionStorage`; no sensitive session data in `localStorage`; backup/SSH/security operations use conservative server-state updates.
- `.trellis/spec/frontend/component-guidelines.md` — UI changes follow existing component/dialog conventions and accessibility patterns.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — use cross-layer data-flow and boundary mapping for backend/frontend feature changes.

## Caveats / Not Found

- No existing step-up/reauth/MFA proof implementation was found by searches for `step_up`, `step-up`, `reauth`, `re-auth`, `elevat`, `second factor`, `second_factor`, `mfa`, or `webauthn` in the searched backend/frontend/spec paths.
- `GET /config/export?include_secrets=true` exists in the backend wrapper/handler path, but the current frontend config export UI observed in `web/src/components/config-export-import.tsx` calls `apiClient.exportConfig(token)` with default `includeSecrets=false`; no frontend call passing `includeSecrets=true` was found.
- `POST /tasks/:id/snapshots/:sid/restore` was found as a high-risk route and handler, but no credential audit write was observed in `backend/internal/api/handlers/snapshot_handler.go` during this research.
- Terminal WebSocket authentication is not covered by the secured Gin group; it uses first-message in-protocol auth through `authorizeRealtimeToken`.
- The central frontend `request<T>()` currently treats non-public-path HTTP 401 responses as session expiration and redirects to login, which is part of the existing error behavior for any future 401-based prompt flow.
- Credential audit metadata sanitization drops keys containing `token`, `credential`, `config`, `command`, `content`, `payload`, and related markers; bounded step-up evidence keys would need to fit within the existing accepted key/value marker rules if written through the current helper.
