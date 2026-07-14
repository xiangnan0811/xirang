# Quality Guidelines

> Code quality standards for backend development.

---

## Overview

Backend changes should match the existing Go/Gin/GORM style and keep the
security-sensitive server-management domain conservative. Run `gofmt` on edited
Go files. The standard backend gate is `cd backend && go test ./... && go build
./...`; repository CI also has a conservative `golangci-lint` configuration in
`backend/.golangci.yml`.

---

## Forbidden Patterns

- New ad hoc JSON response shapes in handlers. Use the helpers in
  `backend/internal/api/handlers/response.go`.
- Returning nodes, SSH keys, integrations, or executor configs without
  sanitizing sensitive fields.
- Adding routes under `/api/v1` without the correct `AuthMiddleware`, RBAC, and
  ownership middleware unless the route is intentionally public auth/captcha.
- Ignoring database, SSH, file-system, encryption, JSON marshal, or migration
  errors.
- Adding a setting outside `settings.Service`'s registry or reading a dynamic
  setting directly from the environment when an existing registry key exists.
- Adding SQLite-only or PostgreSQL-only schema changes.
- Introducing new dependencies for small helpers that the standard library or
  existing packages already cover.

---

## Required Patterns

- Return API data through `respondOK`, `respondCreated`, `respondMessage`,
  `respondPaginated`, or the error helpers.
- Keep sensitive data encrypted via model hooks and strip secrets from response
  structs. Example: `model.Node.Sanitized()`.
- When user-visible evidence, delivery errors, drill output, incident messages, or
  notification payloads may contain command output, run them through the shared
  sanitizer and cover full secret blocks such as PEM private keys, not just
  `key=value` tokens.
- Keep encryption-key rotation docs and implementation in lockstep:
  `DATA_ENCRYPTION_KEY` is the primary v2 write key, and
  `DATA_ENCRYPTION_LEGACY_KEY` must be honored by
  `backend/internal/secure/crypto.go` when documented for v1 decrypt/migration.
- Validate IDs with shared helpers such as `parseID` and validate user input
  before writes. Keep validation close to the owning handler/service.
- For cross-resource or multi-row writes, use GORM transactions.
- Use `logger.Module` for new structured backend logs.
- Keep docs in sync when changing API routes, models, env vars, migrations, or
  release/deploy behavior. `CONTRIBUTING.md` lists the current doc-sync rules.
- Prefer existing domain services and helpers before adding new abstractions.

### Convention: Alerting dispatcher dependency boundary

**What**: Runtime components that send alerts, probes, or delivery retries
should accept an injected `*alerting.Dispatcher` when one is available and call
dispatcher methods such as `SendAlert`, `SendProbe`, `RaiseTaskFailure`, or
`ResolveTaskAlerts`. Package-level alerting shim functions are compatibility
fallbacks and should not be copied into new runtime call sites.

**Why**: Dispatcher injection keeps delivery behavior tied to the server's
configured settings and escalation resolver, reduces package-global state, and
makes retry/probe paths easier to test without mutating alerting globals.

**Example**:

```go
func (h *AlertHandler) WithAlertDispatcher(d *alerting.Dispatcher) *AlertHandler {
    h.alertDispatcher = d
    return h
}

func (h *AlertHandler) getAlertDispatcher() *alerting.Dispatcher {
    if h.alertDispatcher != nil {
        return h.alertDispatcher
    }
    return alerting.NewDispatcher(h.db, nil, nil)
}
```

**Tests**: For cleanup slices that remove package-level shim calls, add a
source-boundary regression test scoped to the converted files. Keep constructor
fallbacks when existing tests or call sites do not yet inject a dispatcher.

---

## Testing Requirements

- Add or update package tests for behavior changes. The repo already has broad
  `*_test.go` coverage under `backend/internal/api/handlers/`,
  `backend/internal/task/`, `backend/internal/alerting/`,
  `backend/internal/dashboards/`, and related packages.
- Handler changes should verify status code and response envelope when feasible.
  See `backend/internal/api/handlers/response_test.go`.
- Database logic should cover both empty-result and error paths. Migration or
  schema compatibility fixes should include focused tests when they are not
  trivially verified by startup.
- Security-sensitive code such as SSH auth, path validation, encryption, RBAC,
  and ownership filtering requires explicit tests for denial cases.
- Before merging backend work, run at least `cd backend && go test ./...`; for
  broader changes also run `cd backend && go build ./...` and `make lint-backend`
  when `golangci-lint` is available.

### Scenario: Asynchronous task-run state assertions

#### 1. Scope / Trigger

- Trigger: testing code that creates a `TaskRun` and starts a goroutine, worker,
  scheduler, or executor that can update the run immediately after creation.
- Applies to task manager tests under `backend/internal/task/` and any backend
  package where state transitions can race with the test's first read.

#### 2. Signatures

- Creation signature: `runID, err := manager.Trigger*(...)` or equivalent method
  that persists a row and starts asynchronous execution.
- Read signature: `db.First(&run, runID)` after the trigger returns.
- Stable fields: `run.ID`, `run.TaskID`, `run.TriggerType`, `run.ChainRunID`, and
  other identifiers written before the goroutine starts.
- Volatile field: `run.Status` when the worker may transition it immediately.

#### 3. Contracts

- Tests may assert stable identifiers and trigger metadata exactly.
- Tests must not require a transient initial status such as `pending` if the
  implementation starts asynchronous execution before the test reads the row.
- For asynchronous paths, assert either a final state after explicit
  synchronization or a set of legal observable states.

#### 4. Validation & Error Matrix

- Row not created for returned `runID` -> test failure.
- Stable metadata mismatch -> test failure.
- Status outside the legal state machine -> test failure.
- Legal status observed earlier than expected because the goroutine ran quickly ->
  not a failure unless the test explicitly synchronized before reading.

#### 5. Good/Base/Bad Cases

- Good: after `TriggerDrill`, assert `TriggerType == "drill"`, matching `TaskID`,
  and `Status` in `{pending,running,success,failed}`.
- Base: if the worker is blocked by a test-controlled channel, assert the exact
  pre-release status.
- Bad: call a trigger that launches a goroutine, then immediately require
  `Status == "pending"` without controlling scheduler timing.

#### 6. Tests Required

- Assert the returned `runID` loads a persisted `TaskRun`.
- Assert stable metadata exactly.
- Assert volatile status with either synchronization or a legal state set.
- Run async-state tests with `-count` when fixing timing-sensitive failures.

#### 7. Wrong vs Correct

Wrong:

```go
runID, _ := manager.TriggerDrill(policyID)
var run model.TaskRun
_ = db.First(&run, runID).Error
if run.Status != "pending" {
	t.Fatalf("expected pending, got %s", run.Status)
}
```

Correct:

```go
runID, _ := manager.TriggerDrill(policyID)
var run model.TaskRun
_ = db.First(&run, runID).Error
validStatus := map[string]bool{"pending": true, "running": true, "success": true, "failed": true}
if !validStatus[run.Status] {
	t.Fatalf("invalid status: %s", run.Status)
}
```

### Scenario: RBAC route permission keys

#### 1. Scope / Trigger

- Trigger: adding or changing any `middleware.RBAC("<permission>")` route under
  `/api/v1`, or making a frontend navigation item depend on that route.
- Applies to backend route registration, `rolePermissions`, and frontend
  surfaces that link to the protected feature.

#### 2. Signatures

- Route signature: `secured.<METHOD>("<path>", middleware.RBAC("<permission>"), handler)`.
- Permission matrix signature:
  `rolePermissions["<role>"]["<permission>"] = true`.
- Test signature: use a router that includes `AuthMiddleware` and `RBAC`, not a
  handler-only Gin route, when the behavior being changed is authorization.

#### 3. Contracts

- Every permission string used by a route must be granted to at least one
  intended role in `backend/internal/middleware/rbac.go`.
- Sensitive management surfaces such as saved credentials, system settings,
  recovery, and secret-bearing config should fail closed. Do not grant
  operator/viewer access unless the product contract explicitly says so.
- Frontend navigation must not expose a normal path to roles that the backend
  will always reject for that feature.

#### 4. Validation & Error Matrix

- Missing/expired token -> 401 from auth middleware.
- Known role without the route permission -> 403 `权限不足`.
- Unknown or missing role -> 403 `权限不足`.
- Intended role with the route permission -> handler status code.

#### 5. Good/Base/Bad Cases

- Good: a new `app_credentials:read` route is paired with admin permission,
  full-router admin/non-admin tests, and admin-only frontend navigation.
- Base: a new route reuses an existing permission whose role coverage already
  matches the feature.
- Bad: a route references a new permission key that is absent from
  `rolePermissions`, causing every authenticated role to receive 403.

#### 6. Tests Required

- Assert at least one intended role receives the handler response through the
  full router.
- Assert roles that should not access the feature receive 403 through the same
  middleware stack.
- For sensitive data routes, include a denial case for saved records or
  mutations, not just the public/schema endpoint.

#### 7. Wrong vs Correct

Wrong:

```go
secured.GET("/app-credentials/profiles", middleware.RBAC("app_credentials:read"), h.ListProfiles)
// rolePermissions has no app_credentials:read entry, so all roles fail.
```

Correct:

```go
secured.GET("/app-credentials/profiles", middleware.RBAC("app_credentials:read"), h.ListProfiles)

var rolePermissions = map[string]map[string]bool{
	"admin": {
		"app_credentials:read": true,
	},
}
```

### Scenario: SSH Key least-privilege scope

#### 1. Scope / Trigger

- Trigger: adding or changing managed SSH key metadata, SSH key create/update/export/test handlers, config import/export, or any backend code path that uses a managed SSH key to open an SSH/SFTP session.
- Applies to `model.SSHKey`, `backend/internal/sshutil/scope.go`, `sshutil.BuildSSHAuth*ForPurpose`, executor SSH helpers, SSH key handlers, node SSH tests, terminal, probes, file browser, Docker volume discovery, node logs, task executors, snapshots, retention, integrity checks, drills, and node migration preflight.

#### 2. Signatures

- Model fields on `model.SSHKey`: `disabled`, `expires_at`, `allowed_purposes`, `allowed_node_ids`, and `allowed_node_tags`.
- SSH key API request/response fields use the same snake_case names plus derived response-only `broad_scope`; private key material remains request-only and must never appear in normal responses.
- Purpose-aware auth signatures:
  - `sshutil.BuildSSHAuthForPurpose(node, db, purpose)`
  - `sshutil.BuildSSHAuthWithKeyForPurpose(node, db, purpose)`
  - `executor.DialSSHForNodePurpose(ctx, node, purpose)`
- Known purpose strings are stable API/storage values: `ssh_key_test`, `ssh_key_export`, `node_test`, `terminal`, `task_command`, `batch_command`, `drill`, `probe`, `file_browser`, `docker_volumes`, `node_logs`, `task_backup`, `task_restore`, `task_hook`, `snapshot`, `snapshot_diff`, `integrity_check`, `retention`, and `node_migration`.
- Config export/import must preserve scope metadata fields for SSH keys, independent of whether secret material is included.

#### 3. Contracts

- Existing installations must stay compatible: `disabled=false`, `expires_at=null`, and empty `allowed_purposes`, `allowed_node_ids`, or `allowed_node_tags` mean unrestricted for that dimension.
- A disabled key or an expired key must be denied before private key material is used for the requested operation, including tests and exports.
- If `allowed_purposes` is non-empty, the normalized current purpose must be present. Unknown purpose values must be rejected at SSH key create/update normalization boundaries.
- If `allowed_node_ids` is non-empty, the target node ID must match exactly. If `allowed_node_tags` is non-empty, at least one normalized node tag must match exactly. When both node IDs and node tags are configured, both checks must pass.
- Empty purpose or node/tag scope is intentionally broad for compatibility; expose it as advisory risk through `broad_scope` and Settings risk summary rather than silently blocking existing keys.
- New managed-key SSH use sites must call a purpose-aware helper. Legacy no-purpose helpers are compatibility shims and should not be copied into new boundaries.
- Scope enforcement applies to managed `ssh_keys` credentials. Inline node password/private-key credentials are still credential-audited but are not controlled by SSHKey scope metadata.
- Denial errors returned to API clients must be concise and sanitized. Do not return private keys, passwords, usernames plus hosts, raw endpoints, executor config, command output, or stack/SQL/encryption details.
- `GET /ssh-keys/export` exports only keys allowed for `ssh_key_export`; blocked keys contribute to audit metadata/counts but are omitted from export payloads.
- Config export includes `disabled`, `expires_at`, `allowed_purposes`, `allowed_node_ids`, and `allowed_node_tags` for each SSH key. `private_key` remains gated by `include_secrets=true`.
- Config import should preserve valid exported scope metadata and normalize purpose/node/tag lists through the shared SSH scope helpers rather than duplicating parser logic.

#### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Key is disabled | Deny managed-key use before dialing/exporting; return sanitized client error. |
| `expires_at` is in the past or equal to now | Deny managed-key use before dialing/exporting. |
| Purpose list does not include the current purpose | Deny with a sanitized least-privilege error. |
| Node ID scope is set and target node ID is absent or different | Deny with a sanitized least-privilege error. |
| Tag scope is set and target node has no exact matching tag | Deny with a sanitized least-privilege error. |
| Existing key has empty scopes | Allow operation and surface advisory broad-scope risk. |
| Config export with `include_secrets=false` | Preserve scope metadata but omit private key material. |
| Config import receives valid exported scope fields | Persist normalized scope metadata on created/updated keys. |

#### 5. Tests Required

- Scope helper tests for compatibility defaults, disabled/expired denial, purpose denial, node-ID denial, tag denial, list normalization, deduplication, and unknown-purpose rejection.
- Handler tests for SSH key create/update patch semantics, explicit scope clearing, response `broad_scope`, private-key exclusion, export blocking, and credential-audit counts where applicable.
- SSH use-boundary tests for at least one shared `sshutil` path and one task/executor path so future callers cannot bypass scope enforcement.
- Config export/import tests proving valid scope metadata round-trips without exposing private keys when secrets are excluded.
- Settings risk tests proving broad, disabled-in-use, expired-in-use, stale, and reused-key examples are sanitized and bounded.

#### 6. Wrong vs Correct

Wrong:

```go
auth, err := sshutil.BuildSSHAuth(node, db) // new managed-key use site has no purpose
```

Correct:

```go
auth, credential, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeTerminal)
```

---

### Scenario: Settings Security Risk Summary

#### 1. Scope / Trigger

- Trigger: adding or changing the advisory security-health/risk summary under Settings.
- Applies to `GET /api/v1/settings/security-risk-summary`, risk example construction, Settings route registration, and any future risk categories that summarize credentials, nodes, SSH behavior, or deployment defaults.

#### 2. Signatures

- Route signature: `secured.GET("/settings/security-risk-summary", middleware.RequireRole("admin"), settingsHandler.SecurityRiskSummary)`.
- Handler signature: `func (h *SettingsHandler) SecurityRiskSummary(c *gin.Context)`.
- Response envelope data shape:

```go
type securityRiskSummaryResponse struct {
    GeneratedAt time.Time               `json:"generated_at"`
    Summary     securityRiskSummaryStat `json:"summary"`
    Items       []securityRiskItem      `json:"items"`
}

type securityRiskItem struct {
    Code        string   `json:"code"`
    Severity    string   `json:"severity"`
    Title       string   `json:"title"`
    Description string   `json:"description"`
    Count       int64    `json:"count"`
    Examples    []string `json:"examples"`
}
```

- Current risk codes: `root_ssh_users`, `reused_ssh_keys`, `sudo_enabled_nodes`, `broad_scope_ssh_keys`, `disabled_ssh_keys_in_use`, `expired_ssh_keys_in_use`, `stale_ssh_keys`, `recent_credential_operations`, and `weak_security_defaults`.

#### 3. Contracts

- Endpoint is read-only and admin-only. Do not expose it to operator/viewer roles unless a product decision changes the Settings security model.
- The response is advisory-only. It must not mutate nodes, SSH keys, settings, credentials, known_hosts, or remote machines.
- `items[].examples` must contain sanitized labels only: node names, SSH key names, human-readable setting labels, or aggregate credential-action labels. Never include hostnames, ports, usernames plus hosts, private keys, passwords, tokens, proxy URLs, raw endpoints, executor configs, command output, terminal streams, or file contents.
- Keep examples bounded (`maxSecurityRiskExamples` is the current cap) and return counts separately from examples.
- Risk categories should be stable string codes so frontend mappers can normalize unknown values safely.
- SSH key least-privilege categories are advisory summaries over managed key metadata: broad-scope keys (`broad_scope_ssh_keys`), disabled keys still referenced by nodes (`disabled_ssh_keys_in_use`), expired keys still referenced by nodes (`expired_ssh_keys_in_use`), and stale keys (`stale_ssh_keys`).
- `recent_credential_operations` summarizes recent high-risk credential audit action counts only. Examples should be action labels plus counts, not raw event metadata.
- If a risk has no findings, keep it in the response with `count=0`; use a non-success severity such as `info` only when the category is explicitly informational.

#### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Missing/expired token | 401 from auth middleware. |
| Authenticated non-admin role | 403 from `RequireRole("admin")`. |
| Database query failure while computing a risk | `respondInternalError`; do not return partial unsafely computed data. |
| Risk example source contains secret-shaped text or user-controlled labels | Sanitize with `util.SanitizeMessage` before returning. |
| More than the example cap is found | Return the real `count` and only the bounded example list. |
| SSH key scope metadata is broad for compatibility | Report advisory risk; do not mutate keys or block from this endpoint. |
| Credential audit event rows contain metadata/error text | Summarize by stable action label/count only; do not echo raw metadata. |
| Future risk category needs remediation | Add a separate explicit mutation endpoint; do not turn summary rows into one-click fixes. |

#### 5. Good/Base/Bad Cases

- Good: root-node risk returns `count=4` and examples like `prod-a`, `db-b`, `cache-c` after sanitization, with no host or credential fields.
- Base: reused-key risk returns `count=1` and an example like `deploy-key（3 个节点）`; the key name is sanitized and no private/public key material is included.
- Base: recent credential-operation risk returns an example like `SSH Key 导出（2 次）`; it must not include event metadata, command text, terminal stream content, node host, or key material.
- Bad: returning `node.Host`, `node.Username`, `ssh_key.private_key`, `executor_config`, credential audit `metadata`, or a remediation button/link payload from the summary endpoint.

#### 6. Tests Required

- Handler test for summary content: counts, stable risk codes, bounded/sanitized examples, and no raw secret-bearing fields in the response body.
- Full-router RBAC test: admin succeeds; operator/viewer fail with 403 through the real middleware stack.
- If new risk categories query models with encrypted fields or credential audit metadata, add tests proving decrypted secrets, raw event metadata, terminal streams, command output, and host-sensitive fields are not returned.
- SSH key least-privilege categories must have tests for counts, bounded examples, and zero-count category retention.
- Frontend mapper/UI tests must cover snake_case mapping, invalid numeric fallback, unknown code/severity fallback, the P1 risk codes, and advisory-only rendering.

#### 7. Wrong vs Correct

Wrong:

```go
item.Examples = append(item.Examples, fmt.Sprintf("%s@%s", node.Username, node.Host))
respondOK(c, gin.H{"items": items, "fix_url": "/app/nodes?filter=root"})
```

Correct:

```go
item.Examples = append(item.Examples, util.SanitizeMessage(node.Name))
respondOK(c, securityRiskSummaryResponse{GeneratedAt: time.Now().UTC(), Items: items})
```

### Scenario: Credential-use audit events

#### 1. Scope / Trigger

- Trigger: adding or changing high-risk operations that use, test, export, or attempt credentials, or adding fields to `credential_audit_events`.
- Applies to `backend/internal/credentialaudit`, `model.CredentialAuditEvent`, SSH key test/export, node connection test, terminal open/failure/close, task manual/restore/batch triggers, batch command creation, task runtime SSH credential use, and restore drill trigger/phase evidence.

#### 2. Signatures

- Table/model: `credential_audit_events` / `model.CredentialAuditEvent`.
- Writer API: `credentialaudit.Write(db, credentialaudit.Event)`, `credentialaudit.FromGin(c, event)`, `credentialaudit.WithRuntimeContext(ctx, db, event)`, and `credentialaudit.WriteRuntime(ctx, event)`.
- Outcome strings: `success`, `failure`, and `blocked`.
- Common fields include `user_id`, `username`, `role`, `action`, `purpose`, `credential_kind`, `credential_source`, optional `ssh_key_id`, `node_id`, `task_id`, `task_run_id`, `policy_id`, `outcome`, sanitized `error_message`, sanitized JSON `metadata`, `client_ip`, `user_agent`, and `created_at`.
- Current P1/P1b action strings: `ssh_key.test_connection`, `ssh_key.export`, `node.credential.test_connection`, `terminal.open`, `terminal.failure`, `terminal.close`, `task.manual_trigger`, `task.restore_trigger`, `task.batch_trigger`, `batch_command.create`, `task.credential.use`, `drill.trigger`, `drill.phase`, `file_browser.list`, `file_browser.preview`, `docker_volumes.discover`, `config.export`, `node.doctor.run`, `node_migration.preflight`, `probe.ssh`, `probe.metrics`, and `node_logs.collect`.

#### 3. Contracts

- Credential-use audit is a domain event table, not a replacement for hash-chained HTTP `audit_logs`. It can be written for GET, WebSocket, and background/runtime flows where `AuditLogger` does not have enough domain context.
- Events must identify who or what used which safe credential source for which purpose/resource, without storing raw passwords, private keys, TOTP/JWT/recovery codes, decrypted executor config, terminal input/output, SFTP payloads, file contents, raw command output, Docker output or volume names, diagnostic evidence, exported config payloads, or full command text.
- `credential_kind` and `credential_source` must be safe labels such as `ssh_key` / `ssh_key_id=<id>`, `password` / `node.password`, `node_private_key` / `node.private_key`, or a route-specific non-secret label. Never place secret values in either field.
- `metadata` is for small, sanitized, bounded context such as counts, stage names, format/scope labels, latency, path hashes, run IDs, or booleans. Keys or values containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, or `payload` must be dropped rather than persisted.
- Error messages must be run through the shared sanitizer and redact text after output markers such as `输出:`, `output:`, `stdout:`, or `stderr:`.
- Audit writes should be best-effort from API handlers and runtime paths: log a warning with `logger.Module("credential_audit")` when the audit write fails, but do not expose the audit-storage failure to the end user or block the primary operation unless the product contract explicitly changes.
- Runtime task/executor writes should use context propagation (`WithRuntimeContext` / `WriteRuntime`) so task, run, policy, node, purpose, and actor/system context stay correlated.
- Settings risk summaries may aggregate high-risk credential-audit action counts, including file browser, Docker volume discovery, config export, node Doctor, migration preflight, probe, metrics, and node-log actions, but must not echo raw event metadata or error messages.
- Background system credential audit must stay bounded: record meaningful blocked/failure outcomes and sparse repeated probe failures, not every successful probe or SSH dial.

#### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Event omits `outcome` | Writer defaults it to `success`. |
| Event metadata contains forbidden keys or values | Drop those entries before persisting. |
| Error message contains remote output after an output marker | Persist only the prefix plus `[REDACTED_OUTPUT]`. |
| Writer receives nil DB | Return nil/no-op for best-effort call sites. |
| Runtime context is absent | `WriteRuntime` returns nil/no-op. |
| JSON marshal of metadata fails | Persist `{}` rather than failing with raw metadata. |
| Audit insert fails in a handler | Log warning; primary response follows the operation result. |

#### 5. Tests Required

- Writer tests must prove forbidden metadata keys/values are dropped, long or user-controlled fields are bounded, and output-bearing error messages are redacted.
- Handler tests for P1/P1b operations should assert an event is written with action, purpose, outcome, safe IDs/sources, and no raw secret material, file content, Docker output, diagnostic evidence, or exported payload material.
- Runtime task/executor tests should assert `WriteRuntime` merges context with operation-specific fields and preserves task/run/node/policy correlation.
- Terminal tests must assert open/failure/close events do not include terminal input or output.
- Drill tests must assert phase events include policy/task/run/evidence context and sanitized phase errors.

#### 6. Wrong vs Correct

Wrong:

```go
credentialaudit.Write(db, credentialaudit.Event{
    Action: "task.credential.use",
    Metadata: map[string]any{"command": task.Command, "output": output},
})
```

Correct:

```go
credentialaudit.WriteRuntime(ctx, credentialaudit.Event{
    Action: "task.credential.use",
    Purpose: sshutil.PurposeTaskCommand,
    Outcome: credentialaudit.OutcomeSuccess,
    Metadata: map[string]any{"stage": "dial", "latency_ms": latencyMs},
})
```

---

### Scenario: Credential access grants

#### 1. Scope / Trigger

- Trigger: adding or changing backend grants that authorize credential-use operations, or enforcing grants at a credential-use boundary.
- Applies to `credential_access_grants`, grant lifecycle handlers, terminal WebSocket open, and future grant-covered operations such as SSH key export, sensitive config export/import, restore, snapshot restore, task trigger, and batch command creation.

#### 2. Signatures

- Table/model: `credential_access_grants` / `model.CredentialAccessGrant`.
- Terminal grant route: `POST /api/v1/credential-access-grants/terminal` behind primary auth, `RequireRole("admin")`, and step-up proof validation.
- Terminal WebSocket boundary: first-message primary token/admin validation, step-up proof validation, `node_id` parse, then grant check before node load, SSH auth resolution, or SSH dial.
- Stable terminal grant tuple: `requester_user_id`, `requester_role`, `action="terminal.open"`, `purpose="terminal"`, `node_id`, `status`, and `expires_at`.

#### 3. Contracts

- Grants are row-backed authorization records, not bearer tokens. Do not issue a signed grant token or store grant material in client-controlled state.
- Store only bounded safe fields: requester/approver IDs and safe labels, action, purpose, optional resource IDs, status, UTC expiry, TTL seconds, and sanitized reason text.
- Never store secrets, tokens, step-up proofs, OTP/recovery values, commands, terminal streams, command output, file contents, exported payloads, raw SQL, endpoint/proxy values, or host-sensitive strings in grant rows, responses, audit events, or logs.
- A grant is additive to existing controls. Primary auth, purpose-scoped token rejection, admin RBAC, ownership where applicable, step-up, and SSH key scope checks must still run.
- Enforcement must happen before credential resolution when the gate protects both managed SSH keys and inline node password/private-key credentials.
- Missing, expired, revoked, denied, wrong-user, wrong-role, wrong-operation, wrong-purpose, or wrong-resource grants must fail closed with a sanitized machine-readable denial.
- Grant lifecycle and use/block paths should write credential/grant audit evidence with sanitizer-compatible metadata keys such as `stage`, `operation`, `status`, `ttl_seconds`, `node_id`, `grant_id`, and booleans/counts.

#### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Missing active matching grant | Deny before node lookup/SSH auth; return machine-readable grant-required signal and write blocked audit evidence. |
| Grant exists for another user or role | Deny; do not disclose the other grant. |
| Grant exists for another action, purpose, or node | Deny; do not authorize a broader operation. |
| Grant status is `revoked`, `denied`, or `expired` | Deny with sanitized status detail where safe. |
| Grant `expires_at` is at or before `now.UTC()` | Deny and treat as expired. |
| Reason contains password/token/key/output/command/host/endpoint/proxy-shaped text | Reject or sanitize before storage; never persist raw reason text. |
| Audit/log write fails | Log a sanitized warning only; primary authorization result remains fail-closed for the protected operation. |

#### 5. Good/Base/Bad Cases

- Good: terminal open validates primary admin token and step-up proof, parses `node_id`, checks an active `(user, role, terminal.open, terminal, node_id)` grant, writes a safe use audit event, then loads the node and resolves SSH credentials.
- Base: a grant request with a bounded reason and TTL creates an active self-grant after step-up in a single-admin/self-hosted deployment.
- Bad: loading a node, decrypting inline credentials, resolving a managed SSH key, dialing SSH, or logging an SSH error before the grant check succeeds.

#### 6. Tests Required

- Migration tests or safety checks must cover paired SQLite/PostgreSQL grant migrations and rollback.
- Handler tests must cover creation/self-activation, TTL bounds, reason rejection/sanitization, response DTOs, admin RBAC, and step-up composition.
- Matching tests must cover expiry, revoked/denied statuses, wrong user, wrong role, wrong node, wrong action, and wrong purpose.
- Terminal WebSocket tests must prove the grant gate runs after primary auth/step-up but before node load, credential resolution, and SSH dial.
- Audit tests must assert grant request/activation/use/block metadata stays sanitizer-compatible and excludes raw secrets, terminal streams, commands, output, endpoint values, and host-sensitive strings.

#### 7. Wrong vs Correct

Wrong:

```go
node := loadNode(nodeID)
auth, _, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeTerminal)
if err := requireGrant(userID, nodeID); err != nil { return err }
```

Correct:

```go
if err := enforceTerminalCredentialGrant(c.Request.Context(), db, claims, nodeID); err != nil {
    return err
}
node := loadNode(nodeID)
auth, _, err := sshutil.BuildSSHAuthForPurpose(node, db, sshutil.PurposeTerminal)
```

---

### Scenario: Deprecated API route removal

#### 1. Scope / Trigger

- Trigger: removing a deprecated `/api/v1` route or deciding that a deprecated
  route should no longer be advertised as available.
- Applies to `backend/internal/api/router.go`, the owning handler file,
  generated Swagger docs under `backend/internal/api/docs/`, frontend API
  wrappers and domain types under `web/src/`, and current public/admin docs.

#### 2. Signatures

- Route registration signature:
  `secured.GET("/old-path", middleware.RBAC("<permission>"), handler.Method)`.
- Handler constructor signature:
  `handlers.New<DeprecatedThing>Handler(...)`.
- Frontend wrapper signature:
  `apiClient.<deprecatedMethod>(token, ...)`.
- Documentation signatures: route tables, admin docs, and generated Swagger
  path blocks such as `"/old-path": { ... }`.

#### 3. Contracts

- Remove the backend route registration and the handler constructor together.
- Delete the handler file only when no other live route or package imports it.
- Remove frontend API wrapper methods, domain types, state, effects, and UI
  controls that exist only to consume the deprecated route.
- Keep replacement routes and unrelated internal helpers intact. If an internal
  helper contains similar names but backs the replacement flow, do not delete it
  just because a broad text search matched it.
- Public docs must describe the current supported path. Do not leave wording
  that says a removed endpoint is still available.
- Regenerate Swagger with `make swag-init` when `swag` is installed. If the
  local tool is unavailable, remove only the generated path block that came from
  the deleted handler annotation and record the tooling gap in the task.

#### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Router still lists the deprecated path | Router registration test fails. |
| Frontend still calls the deprecated wrapper | Component/API test or typecheck fails. |
| Generated docs still include the old path | Source search fails before commit. |
| Replacement route accidentally removed | Router/API tests for the replacement route fail. |
| `swag` command unavailable locally | Manual generated-doc edit is allowed, with the failed command recorded. |

#### 5. Good/Base/Bad Cases

- Good: remove `/hook-templates`, preserve `/app-credentials/profiles`, delete
  the frontend insert-template UI, update docs, and add tests for old-path
  absence plus replacement route presence.
- Base: a deprecated route has no frontend consumer; still remove generated
  docs and public route tables.
- Bad: deleting internal app-aware profile template fields because they contain
  `HookTemplate` in the name even though they render the supported replacement
  flow.

#### 6. Tests Required

- Router registration tests must assert the deprecated route is absent and the
  replacement route remains registered when there is one.
- Frontend tests must cover the user-facing consumer that previously requested
  the deprecated method, or a focused API/client test when there is no component
  consumer.
- Run a source search for the literal route path, frontend wrapper name, handler
  constructor/type, and stale i18n keys. Exclude archive/build/coverage output
  and avoid matching intentionally retained replacement internals.
- Run backend tests, frontend `npm run check` when frontend code changes, doc
  freshness checks when docs change, and `git diff --check`.

#### 7. Wrong vs Correct

Wrong:

```go
// Route removed, but docs and frontend still advertise/call it.
// backend/internal/api/router.go
// secured.GET("/old-path", ...)
```

Correct:

```go
replacementPath := "/api/v1/app-credentials/profiles"
deprecatedPath := "/api/v1/" + "hook" + "-templates"
if !hasRoute(routes, http.MethodGet, replacementPath) {
	t.Fatalf("replacement route missing")
}
if hasRoute(routes, http.MethodGet, deprecatedPath) {
	t.Fatalf("deprecated route still registered")
}
```

---

### Test fixture credential naming

Test fixtures that simulate secrets/credentials (passwords, tokens, keys,
bearer tokens, base64-encoded auth headers, webhook URLs with embedded tokens)
must be named so that **both human reviewers and automated secret scanners**
(GitGuardian, gitleaks, ggshield, trufflehog) immediately recognize them as
non-real test data.

**Rule**: use the prefix `FAKE_` and the suffix `_FOR_TEST_ONLY` for any
literal string that resembles a secret. Compute matching base64 / hex / hash
values from the same fake string when needed (don't reuse pre-computed values
from external examples or Stack Overflow snippets).

**Why**: GitGuardian and similar ML-based scanners flag entropy-rich strings
even in `*_test.go` / `*.test.tsx` files. False positives block PR merges and
require either ggshield ignore comments (per-scanner syntax, fragile) or
admin override (loses signal). Naming the fixture obviously fake is the only
robust solution. Examples that have triggered scanners on this repo:

| Forbidden in fixtures | Why scanners flag | Replacement |
|---|---|---|
| `hunter2` | XKCD password meme; on every scanner's blocklist | `FAKE_PASSWORD_FOR_TEST_ONLY` |
| `secret-metrics-token` | Hyphen pattern + the word "secret" looks like real token | `FAKE_METRICS_TOKEN_FOR_TEST_ONLY` |
| `c2VjcmV0LW1ldHJpY3MtdG9rZW4=` | base64 of the above; same problem | recompute from new fake string |
| `SECRETXYZ` / `ABCD-1234-EFGH` | Looks like a real API key by entropy | `FAKE_TOKEN_FOR_TEST_ONLY` |
| `replace-with-strong-random-secret` (in `.env*.example`) | Was OK historically but newer scanners may flag | Prefer `<set-strong-random-token>` style |

**How to apply**:

- Before committing any test fixture or `.env*.example` placeholder that
  contains a secret-shaped string, mentally check: would a stranger reading
  this think it's a real secret? If yes, rename.
- For base64-encoded auth headers (`Authorization: Basic ...`), encode the
  fake plaintext fresh: `echo -n "FAKE_FOO_FOR_TEST_ONLY" | base64`.
- The `pre-commit` hook does not currently lint fixture names. Reviewers and
  the AI assistant share responsibility for catching these in PR review.

---

### Scenario: Backup Repository Read Adapters

#### 1. Scope / Trigger

- Trigger: adding or changing Repository probing, Provider listing/stat/read,
  access bindings, safe filesystem access, Provider commands, or Repository
  SSH credential use.
- Applies to `backupasset/provider`, `backupasset/repository`, `fileaccess`,
  the shared `sshutil` dial/command runner, and Repository route wiring.

#### 2. Signatures

- Narrow ports are `RepositoryProber`, `PointLister`, `EntryLister`,
  `EntryStatter`, `SequentialReader`, and optional `RangeReader`; consumers
  request only the port they need.
- Every read receives a `provider.ReadSnapshot` binding opaque Repository ID,
  capability revision, source revision, and private access binding.
- Listings use `provider.PageRequest{Limit, Cursor}` and signed scoped cursors.
  Sequential reads require `provider.ReadRequest{MaxBytes > 0}`; Range requires
  `provider.ByteRange{Offset >= 0, Length > 0}`.
- Dynamic settings are
  `backup_assets.provider_operation_timeout`,
  `backup_assets.provider_max_concurrency`, and
  `backup_assets.provider_metadata_limit_bytes`.
- Managed SSH purposes are `repository_probe`, `repository_list`, and
  `repository_read`.

#### 3. Contracts

- Provider point/entry locators and binding material are internal `json:"-"`
  values. HTTP accepts opaque Xirang IDs, never arbitrary paths, remotes,
  repository strings, shell fragments, or credentials.
- Restic uses a validated full native repository/snapshot identity. Mutable
  Rsync/Rclone identities are task-scoped, salted, domain-separated HMACs and
  never auto-merge across Tasks.
- A retained active binding must still reference a non-archived Task whose
  current Provider and Node lineage match the encrypted document. Drift is a
  conflict before probe; a shared Restic link must not make an unusable retained
  binding appear online. Replacement requires an exact Repository target and
  `replace_access=true`.
- Local strict Provider reads use handle-relative containment and never follow
  symlinks. SFTP uses strict relative locators plus pre/open/post
  containment/type/source checks; the verified SSH/SFTP server remains a
  documented infrastructure trust boundary.
- `fileaccess.Tree` owns `List`, `Lstat`, `OpenRegular`, and `OpenRange`.
  Callers must not validate a path and later open it independently.
- Provider commands use fixed server-owned tool/operation enums, separately
  quoted operands, bounded stdout/stderr/records/items/time/concurrency, and
  one-write secret stdin. Content streams hold their permit until close and
  surface wait/cancel/limit/invariant errors from `Close`.
- Restic/Rclone operation limits and shared concurrency are re-read at runtime.
  `RESTIC_BINARY` and `RCLONE_BINARY` are restart-time executable selections.
- Adapter registration is read-only. Restic mutation commands and Rclone/Rsync
  copy/sync/delete/restore/publication paths are unreachable. A remote Rsync
  target remains typed unsupported until a separate target-credential contract
  exists; never reuse the source Node credential by assumption.

#### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| Absolute/NUL/dot-dot/ambiguous strict locator | Reject before filesystem or Provider I/O. |
| Symlink escape or special-file open | Reject; symlink/special may be typed for listing only. |
| Source/root/object changes before close | Return `mutable_source_changed`; do not report successful partial content. |
| Cursor list fingerprint no longer matches | Return stale cursor; do not silently skip or duplicate entries. |
| Restic snapshot ID is `latest` or a prefix | Reject before command execution. |
| Rclone Range semantics are unproven | Keep `OpenRange=false` and return `range_unavailable` if requested. |
| Metadata/output/item/time/concurrency cap is exceeded | Terminate/close/join and return a typed limit/timeout error. |
| Request context is canceled | Close local/remote handle, SFTP/SSH session as owned, and join command goroutines. |
| Retained binding Task is archived or changes Provider/Node lineage | Return conflict before Provider probe; preserve Repository/link/binding rows. |
| Feature is disabled | No Task/DB/keyring/audit/dial/command side effect. |

#### 5. Good/Base/Bad Cases

- Good: Restic lists only full snapshot IDs through a bounded parser and sends
  its repository password once through `/dev/stdin` without argv/env/temp-file
  exposure.
- Base: a mutable Rsync Repository exposes one stable observed head and updates
  that row in place; disconnect keeps it observed/offline and reconnect advances
  capability revision.
- Bad: using `validatePath(path)` followed by `os.Open(path)`, or constructing
  `session.Run("rclone cat " + userPath)`.

#### 6. Tests Required

- Compile/source-boundary tests proving Provider does not import handlers or
  `task/executor`, repository executor mapping stays in `binding.go`, and
  `fileaccess` does not query DB or read `FILE_BROWSER_ALLOW_ALL`.
- Local/SFTP tests for traversal, symlink/static races, pre/post source changes,
  special files, root rename, bounded enumeration, Range, cancellation, and
  handle-close errors.
- Executable fake tests for strict Restic/Rclone argv, schemas, malformed and
  oversized output, exact locators, secret stdin, cancellation, and mutation
  command absence.
- Repository tests for probe-first no-write failure, idempotency, shared Restic
  lineage filtering, retained-binding drift, explicit replacement, disconnect/
  reconnect revision, transaction rollback, and uniqueness races.
- Race/repetition suites for cancellation, resource limits, cursor/list
  mutation, dynamic settings, and concurrency changes.

#### 7. Wrong vs Correct

Wrong:

```go
safePath, _ := validatePath(root, input)
file, _ := os.Open(safePath)
```

Correct:

```go
handle, stat, err := tree.OpenRegular(ctx, root, locator, fileaccess.ProviderPolicy)
if err != nil {
    return err
}
defer handle.Close()
_ = stat // metadata belongs to the opened handle
```

---

## Scenario: Restic Exact Recovery-Point Publication

### Scope / Trigger

- Trigger: changing Restic backup execution, publication/reconciliation,
  managed-history admission, legacy snapshot access, or backup-asset runtime
  wiring.
- Applies to `backupasset/provider`, `backupasset/repository`,
  `backupasset/runtime`, Task Manager/executors, legacy snapshot handlers, and
  paired migration `000063`.

### Signatures

- Publication entry point: `publication.Coordinator.Prepare(context.Context,
  publication.Run) (publication.Execution, error)`.
- Evidence terminal operations: `Execution.RecordProviderCommit`, `Defer`,
  `Reject`, and `Fail`; only `RecordProviderCommit` can move a point from
  `preparing` to `verifying`.
- Reconciliation entry point: `publication.Reconciler.ProcessPoint(ctx,
  pointID)` and bounded `ListCandidates`; the worker never invents a point ID
  or calls a Provider command without an admitted operation.
- Schema contract: paired SQLite/PostgreSQL
  `000063_backup_asset_publication_contract` migrations own
  `native_snapshot`, `point_publication`, and the producing-run/native-source
  unique defenses.

### Contracts

- Attribute an automatic Restic point only from the final successful summary's
  full native snapshot ID plus the exact generated Task/TaskRun tag set. Never
  infer it from `latest`, a prefix, a time window, a repository diff, or a
  legacy index.
- TaskRun transfer success and RecoveryPoint publication success are separate
  facts. A durable commit moves a point only to `verifying`; a bounded async
  worker performs manifest and minimum verification before `committed`.
- Manifest publication and reconciliation validate the current lease fence in
  the same state-changing transaction, retain the immutable point deadline,
  and activate only complete manifests. Partial or unavailable diagnostics are
  inactive and never become browseable truth.
- All Restic command paths acquire a generation admission token before
  credentials, SSH, command streams, or provider handles, and hold it through
  join/close and response or state projection. Feature transitions drain those
  paths before persisting the effective enabled value.
- A Restic access binding is Repository-scoped. For a shared native Repository,
  validate the retained binding origin and derive execution access from the
  current linked Task's Node/config; never run one Task's source command with
  another Task's Node, locator, secret, Task ID, or audit context.
- Managed history is a permanent safety latch. When it exists, disabled mode
  is rollback-safe: legacy unscoped backup fallback, `restore latest`,
  repository-wide anomaly selection, and untagged `forget --prune` stay
  blocked. Rollback/reconciliation never delete Provider snapshots.
- Before a disabled `PublicationService.Prepare` returns a compatibility
  backup session, it must query both installation-level history and every
  active Repository link of the current Task. A Repository tombstone is an
  independent permanent latch: it blocks `legacy_backup` even when the
  installation-level tombstone source reports no history.

### Validation & Error Matrix

| Condition | Required behavior |
|---|---|
| Missing, malformed, duplicate, or non-final backup summary after known exit zero | Preserve TaskRun transfer success, defer publication with a stable evidence code, and recover only from one valid stored summary. |
| Full ID, exact two tags, `original`, identity, time, or capability drift | Fail closed as a stable publication/rewrite or identity error; never replace the recorded native locator. |
| Stale lease fence or elapsed immutable point deadline | Join/cancel work and reject the state mutation; a later owner may retry only with a fresh fence and the original deadline. |
| Partial/unavailable manifest | Store only an inactive diagnostic; do not project counts/digest or mark the RecoveryPoint committed. |
| Feature transition or managed-history latch blocks an operation | Stop before credential, SSH, or Provider access, record only typed bounded audit/metric facts, and keep the previous admission generation on failed drain/persistence. |
| Disabled Task has a linked Repository with managed-history tombstone | Return `legacy_fallback_blocked`, close the admission token, and make no Provider, credential, lease, or RecoveryPoint mutation. |

### Good / Base / Bad Cases

- Good: a Restic backup emits one final valid summary; the exact two opaque tags
  and absent `original` match, the commit reaches `verifying`, and an async
  worker activates a complete manifest before `committed`.
- Base: a pristine installation with the feature disabled retains legacy backup
  behavior while every command still acquires and closes an admission token.
- Good: a Repository-only history tombstone blocks a disabled Task's legacy
  backup even when the global history source is empty.
- Bad: a caller selects `latest`, accepts a short ID outside the committed
  same-Task set, or runs untagged `forget --prune` after managed history exists.

### Wrong vs Correct

Wrong:

```go
snapshotID := latestSnapshotID(ctx, task) // repository-wide inference
```

Correct:

```go
result, err := evidenceExecutor.RunWithEvidence(session.Context(), request, logf, progressf)
if err == nil && result.Completion == backupasset.CompletionKnownExitZero {
    _, err = session.RecordProviderCommit(ctx, result.ProviderCommit)
}
```

The correct path carries the exact attempted tags, full ID, Repository identity,
and current fence through the publication transaction; it never falls back to a
repository-wide selector.

### Tests Required

- Exercise final-summary parsing, exact tag/original rewrite rejection,
  stored-summary reconstruction, fencing, deadlines, backoff, worker shutdown,
  and shared-Repository cross-Task isolation.
- Verify feature transition drain, pristine disabled compatibility, managed
  rollback-safe guards, including a Repository-only tombstone that blocks
  disabled `Prepare` before Provider access, and every legacy
  list/files/search/diff/restore/anomaly/retention call site.
- Run paired SQLite/PostgreSQL migration apply/down safety checks, source
  dependency checks, redaction scans, race/repetition suites, and full backend
  gates before committing.

---

## Code Review Checklist

- Are route middleware, RBAC permissions, and ownership checks correct?
- Are API responses still using the unified envelope and existing helpers?
- Are secrets encrypted at rest and removed from response payloads?
- Are SQLite and PostgreSQL migrations paired and reversible?
- Are all DB/SSH/file/encryption errors checked and mapped safely?
- Are background workers and goroutines cancelable or shutdown-aware when the
  surrounding package requires it?
- Are docs updated for API, model, migration, env var, or deployment changes?
- Did the change reuse existing packages/helpers instead of duplicating local
  logic?
