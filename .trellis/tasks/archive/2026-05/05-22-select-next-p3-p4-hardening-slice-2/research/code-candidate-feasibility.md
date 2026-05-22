# Research: code candidate feasibility

- **Query**: Inspect Xirang backend/frontend code for high-risk P3/P4 hardening candidates after completed JIT grants for config import/export and snapshot restore. Focus on actual routes/components for manual task trigger/restore trigger, batch command execution, SSH key export/download, terminal, and any remaining high-risk credential/control-plane actions. Include route names, likely files, existing guards (RBAC/role/step-up/ownership), whether JIT grant pattern can be reused, and one-PR feasibility.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/router.go` | Authoritative route registration and middleware guard matrix for the examined operations. |
| `backend/internal/api/handlers/credential_access_grant.go` | Existing row-backed JIT grant request/enforcement implementation for terminal, config import/export, and snapshot restore. |
| `backend/internal/api/handlers/step_up.go` | Reusable step-up proof middleware and helper enforcement. |
| `backend/internal/api/handlers/task_handler.go` | Manual task trigger, restore trigger, and batch trigger handlers. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation handler and per-node ownership/step-up checks. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key export/download and SSH key test connection handlers. |
| `backend/internal/api/handlers/terminal_handler.go` | Terminal WebSocket auth, step-up, and JIT grant enforcement reference implementation. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot file restore handler already covered by JIT grant middleware. |
| `backend/internal/api/handlers/config_handler.go` | Config import/export handlers already covered by JIT grants for import and sensitive export. |
| `backend/internal/api/handlers/node_handler.go` | Node credential/control-plane handlers including node test connection and emergency backup. |
| `backend/internal/api/handlers/policy_handler.go` | Restore drill trigger handler. |
| `backend/internal/api/handlers/system_handler.go` | System backup DB handler and other admin control-plane endpoints. |
| `backend/internal/api/handlers/helpers.go` | Ownership helper functions used by batch command and policy/task flows. |
| `backend/internal/middleware/rbac.go` | Role-to-permission mapping for admin/operator/viewer reachability. |
| `backend/internal/middleware/ownership.go` | Object-level node/task ownership middleware. |
| `backend/internal/model/models.go` | `CredentialAccessGrant` model fields; currently supports NodeID, TaskID, and PolicyID resource binding. |
| `backend/internal/sshutil/scope.go` | SSH credential-purpose scope constants and validation helpers. |
| `web/src/lib/api/credential-access-grants-api.ts` | Frontend API client for existing JIT grant request endpoints. |
| `web/src/lib/api/tasks-api.ts` | Frontend API client for task trigger, restore, and batch trigger; methods accept optional step-up proof. |
| `web/src/lib/api/batch-api.ts` | Frontend API client for batch command creation; method accepts optional step-up proof. |
| `web/src/lib/api/ssh-keys-api.ts` | Frontend API client for SSH key export/download; supports optional step-up proof header. |
| `web/src/hooks/use-step-up-action.ts` | Frontend step-up retry wrapper used by several high-risk flows. |
| `web/src/components/web-terminal.tsx` | Frontend terminal JIT grant prompt/reference flow. |
| `web/src/components/config-export-import.tsx` | Frontend config import/sensitive export JIT grant flows. |
| `web/src/components/snapshot-browser.tsx` | Frontend snapshot restore JIT grant flow. |
| `web/src/components/batch-command-dialog.tsx` | Frontend batch command dialog; step-up only, no JIT grant prompt. |
| `web/src/components/restore-confirm-dialog.tsx` | Frontend task restore dialog; step-up only, no JIT grant prompt. |
| `web/src/components/ssh-key-export-dialog.tsx` | Frontend SSH key export/download dialog; step-up only, no JIT grant prompt. |
| `web/src/pages/tasks-page.tsx` | Frontend manual trigger and batch-trigger UI wiring. |
| `web/src/pages/notifications/alert-center.tsx` | Alert retry path that calls task trigger without the step-up wrapper. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security contract for credential access grants, credential-use audit, and additive guard requirements. |

### Route / Component Candidate Matrix

| Operation | Route / UI | Existing guards observed | JIT grant status / reuse notes | One-PR feasibility |
|---|---|---|---|---|
| Terminal open | `GET /api/v1/ws/terminal`; `web/src/components/web-terminal.tsx` | Custom WebSocket first-message auth; admin role; step-up proof; row-backed terminal grant; grant enforced before node load and SSH credential resolution; SSH purpose `terminal`; manual audit and credential-use audit. | Already uses existing JIT grant pattern with `NodeID`. Not a next candidate except as reference implementation. | Already covered. |
| Config import | `POST /api/v1/config/import`; `web/src/components/config-export-import.tsx` | Admin role; step-up; row-backed config import grant; credential-use audit; import validation. | Already uses existing JIT grant pattern with system-level action/purpose and no resource ID. | Already covered. |
| Sensitive config export | `GET /api/v1/config/export?include_secrets=true`; `web/src/components/config-export-import.tsx` | Admin role; conditional step-up only when secrets are included; conditional config export grant; SSH key purpose metadata enforcement for key material export paths. | Already uses existing JIT grant pattern for sensitive export. Non-secret export is not grant-covered by predicate. | Already covered for sensitive export. |
| Snapshot restore | `POST /api/v1/tasks/:id/snapshots/:sid/restore`; `web/src/components/snapshot-browser.tsx` | Admin role; step-up; row-backed snapshot restore grant; task-bound via `TaskID`; restic executor validation; credential-use audit. | Already uses existing JIT grant pattern with `TaskID`. Recent pattern is directly relevant for task restore. | Already covered. |
| Manual task trigger | `POST /api/v1/tasks/:id/trigger`; `web/src/pages/tasks-page.tsx` | `RBAC("tasks:trigger")`; `OwnershipTaskCheck`; step-up action `task.manual_trigger` with SSH purpose `task_command`; credential-use audit. Operators can reach owned tasks per RBAC/ownership. | No row-backed JIT grant observed. Backend grant infrastructure can match `TaskID`, but existing grant request routes are admin-only, so reuse is straightforward only if the operation remains/admin-only or grant requester policy is extended for operators. | Medium: route/handler/frontend are localized, but operator access semantics make it more than a mechanical copy of snapshot restore. |
| Task restore trigger | `POST /api/v1/tasks/:id/restore`; `web/src/components/restore-confirm-dialog.tsx` | Admin role; step-up action `task.restore_trigger` with SSH purpose `task_restore`; credential-use audit. No ownership middleware observed on route. | No row-backed JIT grant observed. Existing grant infrastructure can match `TaskID`; snapshot restore is the closest reference. | High: admin-only, task-bound, already step-up; likely the cleanest one-PR JIT-grant candidate among examined uncovered flows. |
| Batch task trigger | `POST /api/v1/tasks/batch-trigger`; `web/src/pages/tasks-page.tsx` | `RBAC("tasks:write")`; handler-level operator ownership filtering; handler-level `EnforceStepUp` with action `task.batch_trigger` and purpose `task_command`; credential-use audit. | No row-backed JIT grant observed. Grant model supports `TaskID`, but batch operations include multiple task IDs; current model has no multi-resource grant. Could use per-task grants or a broader operation grant, each with different UI/API shape. Existing grant request routes are admin-only while operators can trigger owned tasks. | Medium/low for one PR if exact per-resource semantics are required; medium if a broad batch grant is acceptable. |
| Batch command creation | `POST /api/v1/batch-commands`; `web/src/components/batch-command-dialog.tsx` | `RBAC("tasks:write")`; command length validation; dangerous-command safety-net patterns; selected node ownership authorization; handler-level `EnforceStepUp` with action `batch_command.create` and SSH purpose `batch_command`; credential-use audit. Operators can reach owned nodes. | No row-backed JIT grant observed. Grant model supports `NodeID`, but batch command can target many nodes and currently has operator access. Existing terminal grant pattern is node-bound; applying it to multiple nodes would require per-node grants or a broader operation grant. | Medium: high-risk and localized, but multi-node/operator semantics require a product/security decision. |
| SSH key export/download | `GET /api/v1/ssh-keys/export`; `web/src/components/ssh-key-export-dialog.tsx` | `RBAC("ssh_keys:read")`; step-up action `ssh_key.export`; visibility/scope/id filtering; SSH purpose `ssh_key_export`; credential-use audit. Exports public key material in authorized_keys/json/csv formats, not private keys. Operators/viewers have `ssh_keys:read` in RBAC. | No row-backed JIT grant observed. Current `CredentialAccessGrant` model lacks `SSHKeyID`; exact per-key grant would need schema/model/migration support or a broader system-level export grant. | Medium: broad export grant is feasible in one PR; per-key grant is larger due to missing `SSHKeyID`. |
| SSH key test connection | `POST /api/v1/ssh-keys/:id/test` or equivalent router entry; backend `ssh_key_handler.go` | `RBAC("ssh_keys:write")` observed; uses selected SSH key for node tests through SSH purpose `ssh_key_test`; credential-use audit. No step-up/JIT grant observed in inspected handler notes. | No row-backed JIT grant observed. Could be key-bound or node-bound, but model lacks `SSHKeyID`; node-bound matching exists if tests are tied to node IDs. | Medium/low depending on exact resource binding and frontend coverage. |
| Node test connection | node test route in `router.go`; backend `node_handler.go` | `RBAC("nodes:test")`; `OwnershipNodeCheck`; uses node credentials for SSH dial/probe; updates status/latency/disk metrics; credential-use audit action `node.credential.test_connection`. No step-up/JIT grant observed. | No row-backed JIT grant observed. Existing `NodeID` grant matching can fit this operation. | Medium/high: node-bound and localized; not already step-up, so frontend/backend step-up UI work is needed. |
| Emergency backup | node emergency backup route in `router.go`; backend `node_handler.go` | `RBAC("tasks:trigger")`; `OwnershipNodeCheck`; triggers policy backup tasks for node. No step-up/JIT grant observed. | No row-backed JIT grant observed. Could use `NodeID` or per-task grants depending intended semantics. | Medium: node-bound route exists, but operation fans out to multiple tasks/policies. |
| Restore drill trigger | policy drill route in `router.go`; backend `policy_handler.go` | `RBAC("tasks:trigger")`; handler-level `authorizePolicyOwnership`; requires `policy.DrillEnabled`; writes `drill.trigger`. No step-up/JIT grant observed. | No row-backed JIT grant observed. Existing grant model supports `PolicyID`; SSH purpose `drill` exists. | Medium/high if drill is selected; policy-bound grant field already exists. |
| System backup DB | system backup route in `router.go`; backend `system_handler.go` | Admin-only route observed; creates SQLite backup via `VACUUM INTO`, writes `.sha256`, performs retention cleanup. No step-up/JIT grant observed. | No row-backed JIT grant observed. Existing system-level grant shape could be reused, but this is not directly an SSH credential-use path. | Medium: backend route is localized; frontend/UX and whether it belongs in credential grant model need confirmation. |

### Code Patterns

#### Existing step-up pattern

`backend/internal/api/handlers/step_up.go` exposes reusable middleware/helper functions:

```go
func RequireStepUp(db *gorm.DB, jwtManager *auth.JWTManager, action, purpose, operation string) gin.HandlerFunc
func RequireStepUpIf(db *gorm.DB, jwtManager *auth.JWTManager, action, purpose, operation string, predicate func(*gin.Context) bool) gin.HandlerFunc
func EnforceStepUp(c *gin.Context, db *gorm.DB, jwtManager *auth.JWTManager, action, purpose, operation string) bool
```

Observed behavior: step-up requires `X-Xirang-Step-Up`, validates proof purpose `auth.PurposeStepUp`, user/role match, token version, current DB role, and TOTP enabled. Missing/invalid proof returns `STEP_UP_REQUIRED` with `proof_ttl_seconds: 300`.

Frontend retry wrapper:

```ts
export function useStepUpAction() {
  const { ensureStepUpProof, clearStepUpProof } = useAuth();

  return useCallback(async <T,>(action: (stepUpProof?: string) => Promise<T>): Promise<T> => {
    try {
      return await action();
    } catch (error) {
      if (!isStepUpRequiredError(error)) {
        throw error;
      }
      const proof = await ensureStepUpProof();
      try {
        return await action(proof);
      } catch (retryError) {
        if (isStepUpRequiredError(retryError)) {
          clearStepUpProof();
        }
        throw retryError;
      }
    }
  }, [clearStepUpProof, ensureStepUpProof]);
}
```

#### Existing row-backed JIT grant pattern

`backend/internal/api/handlers/credential_access_grant.go` defines current actions:

```go
CredentialGrantActionTerminalOpen    = "terminal.open"
CredentialGrantActionConfigImport    = "config.import"
CredentialGrantActionConfigExport    = "config.export"
CredentialGrantActionSnapshotRestore = "snapshot.restore"
```

Existing grant request endpoints in `router.go`:

```go
secured.POST("/credential-access-grants/terminal", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestTerminalGrant)
secured.POST("/credential-access-grants/config-import", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestConfigImportGrant)
secured.POST("/credential-access-grants/config-export", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestConfigExportGrant)
secured.POST("/credential-access-grants/snapshot-restore", middleware.RequireRole("admin"), credentialAccessGrantHandler.RequestSnapshotRestoreGrant)
```

Existing enforcement helpers include:

```go
func EnforceTerminalCredentialGrantForWebSocket(c *gin.Context, db *gorm.DB, claims *auth.Claims, nodeID uint) (*model.CredentialAccessGrant, error)
func RequireConfigImportCredentialGrant(db *gorm.DB) gin.HandlerFunc
func RequireConfigExportCredentialGrantIf(db *gorm.DB, predicate func(*gin.Context) bool) gin.HandlerFunc
func RequireSnapshotRestoreCredentialGrant(db *gorm.DB) gin.HandlerFunc
```

Grant matching currently supports single optional resource IDs:

```go
type credentialGrantMatch struct {
    Action   string
    Purpose  string
    NodeID   *uint
    TaskID   *uint
    PolicyID *uint
}
```

`backend/internal/model/models.go` has corresponding fields on `CredentialAccessGrant`: `NodeID *uint`, `TaskID *uint`, and `PolicyID *uint`; no `SSHKeyID` field was observed.

#### Existing frontend grant API shape

`web/src/lib/api/credential-access-grants-api.ts` currently maps only:

```ts
return raw === "terminal.open" || raw === "config.import" || raw === "config.export" || raw === "snapshot.restore" ? raw : "unknown";
```

and purposes:

```ts
return raw === "terminal" || raw === "config_import" || raw === "config_export" || raw === "snapshot" ? raw : "unknown";
```

Existing methods:

- `requestTerminalCredentialGrant(token, { nodeId, reason, requestedTtlSeconds }, stepUpProof?)`
- `requestConfigImportCredentialGrant(token, { reason, requestedTtlSeconds }, stepUpProof?)`
- `requestConfigExportCredentialGrant(token, { reason, requestedTtlSeconds }, stepUpProof?)`
- `requestSnapshotRestoreCredentialGrant(token, { taskId, reason, requestedTtlSeconds }, stepUpProof?)`

Completed frontend grant prompts use local reason dialog state with a max reason length of 240 and a TTL of 600 seconds in terminal and snapshot restore flows.

#### RBAC / ownership facts relevant to candidate feasibility

`backend/internal/middleware/rbac.go` role mapping observed:

- Admin has all relevant permissions.
- Operator includes `nodes:read`, `nodes:test`, `tasks:read`, `tasks:write`, `tasks:trigger`, and `ssh_keys:read`.
- Viewer includes read-only permissions, including `ssh_keys:read`.

`backend/internal/middleware/ownership.go` observed object checks:

- `OwnershipTaskCheck`: admin/viewer pass; operator must own the task's node; unknown/missing role fails closed.
- `OwnershipNodeCheck`: admin/viewer pass; operator must own the node; unknown/missing role fails closed.

This matters because current grant request routes are admin-only, while several uncovered candidate operations are intentionally reachable by operators with ownership.

### Candidate-Specific Notes

#### Manual task trigger

Route: `POST /api/v1/tasks/:id/trigger`.

Observed backend route guards: `RBAC("tasks:trigger")`, `OwnershipTaskCheck`, and `RequireStepUp(..., "task.manual_trigger", sshutil.PurposeTaskCommand, "task_run")`.

Observed handler behavior: `task_handler.go` calls `h.runner.TriggerManual(id)` and writes credential audit action `task.manual_trigger`.

Frontend likely files: `web/src/pages/tasks-page.tsx`, `web/src/lib/api/tasks-api.ts`, and any context/hook wrapping `triggerTask`.

JIT reuse: task-bound grants can reuse `TaskID` matching, but current grant request endpoints require admin; manual trigger currently permits operators on owned tasks.

#### Task restore trigger

Route: `POST /api/v1/tasks/:id/restore`.

Observed backend route guards: `RequireRole("admin")` and `RequireStepUp(..., "task.restore_trigger", sshutil.PurposeTaskRestore, "task_restore")`.

Observed handler behavior: `task_handler.go` calls `h.runner.TriggerRestore(id, req.TargetPath)` and writes credential audit action `task.restore_trigger`.

Frontend likely files: `web/src/components/restore-confirm-dialog.tsx`, `web/src/lib/api/tasks-api.ts`.

JIT reuse: this is admin-only and task-bound, matching the existing snapshot restore grant shape closely.

#### Batch command creation

Route: `POST /api/v1/batch-commands`.

Observed backend route guards/handler checks: `RBAC("tasks:write")`, handler-level selected node ownership authorization, handler-level `EnforceStepUp(..., "batch_command.create", sshutil.PurposeBatchCommand, "batch_run")`, command max length validation, and dangerous-command safety-net checks.

Frontend likely files: `web/src/components/batch-command-dialog.tsx`, `web/src/lib/api/batch-api.ts`.

JIT reuse: node-bound matching exists for one node, but this operation targets multiple nodes; route also permits operators with owned nodes while current grant request routes are admin-only.

#### SSH key export/download

Route: `GET /api/v1/ssh-keys/export`.

Observed backend route guards: `RBAC("ssh_keys:read")` and `RequireStepUp(..., "ssh_key.export", sshutil.PurposeSSHKeyExport, "ssh_export")`.

Observed handler behavior: `ssh_key_handler.go` filters by visibility/scope/ids, validates purpose with `sshutil.ValidateSSHKeyPurpose(item, sshutil.PurposeSSHKeyExport)`, exports public-key data in authorized_keys/json/csv formats, and writes credential audit action `ssh_key.export`.

Frontend likely files: `web/src/components/ssh-key-export-dialog.tsx`, `web/src/lib/api/ssh-keys-api.ts`.

JIT reuse: a broad system-level grant can reuse current no-resource grant shape; exact per-key grants would need `SSHKeyID` support because `CredentialAccessGrant` currently has no key resource field.

#### Terminal

Route: `GET /api/v1/ws/terminal`.

Observed backend behavior: `terminal_handler.go` upgrades WebSocket, reads first auth message with token and `step_up_proof`, requires admin realtime token, validates step-up, parses `node_id`, enforces terminal grant before node load and SSH credential resolution, then calls `sshutil.BuildSSHAuthForPurpose(node, h.db, sshutil.PurposeTerminal)`. It writes terminal lifecycle audit and credential-use audit.

Frontend likely files: `web/src/components/web-terminal.tsx`, `web/src/lib/api/credential-access-grants-api.ts`.

JIT reuse: already grant-covered with `NodeID`; use as implementation reference.

### Related Specs

| Spec Path | Description |
|---|---|
| `.trellis/spec/backend/quality-guidelines.md` | Defines credential access grant expectations: additive to auth/RBAC/ownership/step-up/scope checks; current and future grant-covered operations include terminal, SSH key export, sensitive config import/export, restore, snapshot restore, task trigger, and batch command creation; enforcement should happen before credential resolution for credential-backed operations; grant rows must not store secrets or sensitive operational payloads. |

### External References

No external references were used; this was an internal codebase/spec inspection.

## Caveats / Not Found

- No code files were modified for this research.
- The exact router line numbers are not included in this report because the route file is large; paths and route strings above are based on inspected route registrations and handlers.
- `CredentialAccessGrant` currently has `NodeID`, `TaskID`, and `PolicyID` fields; no `SSHKeyID` field was found, which limits exact SSH-key-bound grant reuse.
- Existing grant request routes are admin-only, while manual task trigger, batch task trigger, batch command creation, node test, and SSH key export are reachable by non-admin roles under current RBAC/ownership rules. This is a feasibility constraint for direct reuse of the current grant pattern.
- `web/src/pages/notifications/alert-center.tsx` contains an alert retry path that calls `apiClient.triggerTask(token, alert.taskId)` without the step-up wrapper in the inspected notes; backend step-up is expected to reject missing proof, so this path may show an error instead of prompting.
- This report intentionally describes existing code and feasibility shape only; it does not modify implementation or propose a design beyond mapping reuse constraints.
