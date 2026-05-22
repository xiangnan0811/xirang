# Research: P3 credential/control-plane code gap audit

- **Query**: Research current code coverage for P3 credential/control-plane hardening. Inspect backend handlers/routes/middleware/tests and frontend grant flows related to credential access grants, step-up, task trigger, batch trigger, batch command, restore, terminal, config import/export, grant status/list. Identify likely gaps, regressions, or areas needing comprehensive review.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/router.go` | Authoritative REST/WebSocket route registration and middleware chains for grant-covered operations. |
| `backend/internal/api/handlers/credential_access_grant.go` | Central credential access grant request, validation, tuple lookup, enforcement, response mapping, and audit implementation. |
| `backend/internal/api/handlers/step_up.go` | Step-up proof middleware, `X-Xirang-Step-Up` header, and proof validation. |
| `backend/internal/api/handlers/auth_handler.go` | Step-up proof issuing endpoint backed by TOTP validation and credential audit. |
| `backend/internal/auth/jwt.go` | JWT purpose constants and generation of primary, 2FA-pending, and step-up tokens. |
| `backend/internal/middleware/auth.go` | REST primary authentication middleware; rejects purpose-scoped tokens. |
| `backend/internal/api/handlers/realtime_auth.go` | WebSocket/in-protocol primary token validation; rejects purpose-scoped tokens. |
| `backend/internal/middleware/rbac.go` | RBAC permissions used by grant request/list and task/batch routes. |
| `backend/internal/middleware/ownership.go` | Operator ownership checks for task/node-scoped operations. |
| `backend/internal/api/handlers/task_handler.go` | Manual task trigger, restore, batch trigger, and related credential audit emission. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation; ownership, step-up, grant enforcement before task creation. |
| `backend/internal/api/handlers/terminal_handler.go` | Terminal WebSocket gate order: primary auth, step-up, node ID parse, grant check, node/SSH credential load. |
| `backend/internal/api/handlers/config_handler.go` | Config import/export implementation and sensitive export fields. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot restore handler and target path safety validation. |
| `backend/internal/model/models.go` | `CredentialAccessGrant` model and sensitive model sanitization helpers. |
| `backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql` | SQLite credential grant table/index migration. |
| `backend/internal/database/migrations/postgres/000060_credential_access_grants.up.sql` | PostgreSQL credential grant table/index migration. |
| `backend/internal/api/handlers/credential_access_grant_test.go` | Broad backend grant creation, matching, enforcement, audit, route-helper tests. |
| `backend/internal/api/handlers/step_up_test.go` | Step-up issuance/validation, purpose-token rejection, terminal/config/snapshot audit tests. |
| `backend/internal/api/handlers/batch_handler_test.go` | Batch command ownership, grant-before-credential-use, all-node grant enforcement, redaction tests. |
| `backend/internal/api/router_test.go` | Route registration and selected full-router RBAC/step-up tests. |
| `backend/internal/sshutil/scope.go` | Stable SSH credential purpose strings, including terminal/task/batch/restore/snapshot. |
| `backend/internal/sshutil/ssh_auth.go` | Purpose-aware SSH credential resolution entry points. |
| `web/src/lib/api/core.ts` | API request wrapper, bearer/step-up headers, step-up/grant error-code helpers. |
| `web/src/lib/api/client.ts` | Composes credential access grants API into `apiClient`. |
| `web/src/lib/api/credential-access-grants-api.ts` | Frontend grant list/request client and snake_case-to-camelCase mapper. |
| `web/src/lib/api/tasks-api.ts` | Task trigger, restore, and batch-trigger API methods with optional step-up proof. |
| `web/src/lib/api/batch-api.ts` | Batch command API method with optional step-up proof. |
| `web/src/lib/api/config-api.ts` | Config import/export API methods with optional step-up proof. |
| `web/src/lib/step-up-storage.ts` | Session-storage backed step-up proof cache. |
| `web/src/context/auth-context-provider.tsx` | Auth state and `ensureStepUpProof()` dialog/cache behavior. |
| `web/src/hooks/use-step-up-action.ts` | Wrapper that retries actions after `STEP_UP_REQUIRED`. |
| `web/src/hooks/use-console-task-operations.ts` | Manual task trigger flow: request task grant then trigger task. |
| `web/src/pages/tasks-page.tsx` | Task page batch trigger flow and batch command dialog entry. |
| `web/src/components/batch-command-dialog.tsx` | Batch command flow: validate local input, request node-scoped grant, create batch command. |
| `web/src/components/restore-confirm-dialog.tsx` | Task restore flow: one-time step-up, task restore grant, restore action. |
| `web/src/components/snapshot-browser.tsx` | Snapshot browse and restore flow: one-time step-up, snapshot restore grant, restore action. |
| `web/src/components/web-terminal.tsx` | Terminal WebSocket step-up/grant prompt/retry flow. |
| `web/src/components/config-export-import.tsx` | Config import and sensitive export grant flows. |
| `web/src/pages/credential-access-grants-page.tsx` | Admin-only grant list/status/filter/details UI. |
| `web/src/router.tsx` | Frontend route registration for grant list page. |
| `web/src/components/layout/navigation.ts` | Admin-only navigation entry for grant list page. |
| `web/src/types/domain.ts` | Frontend grant action/purpose/status/domain types. |
| `web/src/lib/api/credential-access-grants-api.test.ts` | Frontend grant API mapping and request serialization tests. |
| `web/src/hooks/use-step-up-action.test.tsx` | Step-up retry wrapper tests. |
| `web/src/context/auth-context.test.tsx` | Auth/step-up cache and one-time proof behavior tests. |
| `web/src/components/web-terminal.test.tsx` | Terminal step-up WebSocket auth, grant-required prompt/retry/storage-safety tests. |
| `web/src/components/batch-command-dialog.test.tsx` | Batch command grant-before-create and storage-safety test. |
| `web/src/components/restore-confirm-dialog.test.tsx` | Task restore one-time proof, grant-before-restore, validation, storage-safety tests. |
| `web/src/components/snapshot-browser.test.tsx` | Snapshot restore grant-before-restore, browse-no-grant, validation, storage-safety tests. |
| `web/src/components/config-export-import.test.tsx` | Config import/sensitive export grant-before-action, validation, storage-safety tests. |
| `web/src/pages/tasks-page.test.tsx` | Batch trigger page flow test for grant-before-trigger; manual trigger is delegated to task operations hook. |
| `web/src/pages/credential-access-grants-page.test.tsx` | Grant list filters, details, admin-only, and storage-safety tests. |
| `web/src/pages/__tests__/credential-access-grants-page.a11y.test.tsx` | Grant list page accessibility smoke test. |
| `web/src/components/layout/navigation.test.ts` | Admin-only navigation visibility for grant list. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend grant, step-up, audit, and SSH purpose requirements. |
| `.trellis/spec/frontend/type-safety.md` | Frontend grant DTO/mapping/error-code requirements. |
| `.trellis/spec/frontend/state-management.md` | Frontend terminal grant prompt and step-up/grant storage-state requirements. |

### Code Patterns

#### Backend route coverage and middleware composition

`backend/internal/api/router.go` registers all current credential access grant request/list endpoints:

- grant list: `GET /credential-access-grants` behind `RequireRole("admin")` at `backend/internal/api/router.go:265`.
- admin grant requests: terminal/config-import/config-export/snapshot-restore/task-restore at `backend/internal/api/router.go:266-270`.
- operator-capable grant requests: task manual trigger, task batch trigger, batch command behind `RBAC("tasks:trigger")` / `RBAC("tasks:write")` at `backend/internal/api/router.go:271-273`.

High-risk operation routes are layered with existing controls plus step-up/grant enforcement:

- manual trigger: `RBAC("tasks:trigger")`, `OwnershipTaskCheck`, `RequireStepUp`, `RequireTaskManualTriggerCredentialGrant`, handler at `backend/internal/api/router.go:292`.
- task restore: admin role, step-up, task restore grant at `backend/internal/api/router.go:297`.
- snapshot restore: admin role, step-up, snapshot restore grant at `backend/internal/api/router.go:319`.
- sensitive config export: admin role, conditional step-up and conditional config export grant only when `include_secrets=true` at `backend/internal/api/router.go:328-332`.
- config import: admin role, step-up, config import grant at `backend/internal/api/router.go:333`.
- batch command creation: `RBAC("tasks:write")` route at `backend/internal/api/router.go:303`; ownership/step-up/grant checks live in the handler.
- terminal WebSocket is outside the secured REST group as `GET /ws/terminal` at `backend/internal/api/router.go:377`, with in-protocol authentication in the handler.

#### Step-up and purpose-scoped token handling

Step-up proof uses a dedicated header and JWT purpose:

- `StepUpHeaderName = "X-Xirang-Step-Up"` at `backend/internal/api/handlers/step_up.go:18`.
- `RequireStepUp` / `RequireStepUpIf` wrap operation routes at `backend/internal/api/handlers/step_up.go:36-46`.
- proof validation reads the header and calls `validateStepUpProof` at `backend/internal/api/handlers/step_up.go:61-67`.
- `validateStepUpProof` checks the proof JWT has `auth.PurposeStepUp` at `backend/internal/api/handlers/step_up.go:77-89` and also validates user/role/token state in the same function.
- JWT purpose constants include `Purpose2FAPending = "2fa_pending"` and `PurposeStepUp = "step_up"` at `backend/internal/auth/jwt.go:20-21`; step-up token generation is anchored at `backend/internal/auth/jwt.go:109`.
- REST auth rejects any token with a non-empty purpose at `backend/internal/middleware/auth.go:41`; realtime/WebSocket auth does the same at `backend/internal/api/handlers/realtime_auth.go:28`.
- `AuthHandler.StepUp` blocks when TOTP is unavailable/invalid and audits unavailable/failure/success states at `backend/internal/api/handlers/auth_handler.go:405-419`.

#### Credential access grant implementation

The backend grant handler defines the stable action strings:

- `terminal.open`, `config.import`, `config.export`, `snapshot.restore`, `task.restore_trigger`, `task.manual_trigger`, `task.batch_trigger`, and `batch_command.create` at `backend/internal/api/handlers/credential_access_grant.go:33-40`.

Grant request creation patterns:

- terminal/config/snapshot/task restore request methods call step-up validation before grant creation at `backend/internal/api/handlers/credential_access_grant.go:188`, `222`, `244`, `270`, and `312`.
- task manual trigger grants allow admin/operator roles and then authorize task targets at `backend/internal/api/handlers/credential_access_grant.go:344-348`.
- task batch trigger grants allow admin/operator roles and authorize all task IDs at `backend/internal/api/handlers/credential_access_grant.go:376-380`.
- batch command grants allow admin/operator roles and authorize node IDs at `backend/internal/api/handlers/credential_access_grant.go:412-416`.
- common validation enforces step-up, current requester role, bounded/sanitized reason, and normalized TTL at `backend/internal/api/handlers/credential_access_grant.go:480-492`.
- target authorization helpers are `authorizeTaskGrantTargets` and `authorizeNodeGrantTargets` at `backend/internal/api/handlers/credential_access_grant.go:556` and `588`.

Grant enforcement patterns:

- terminal WebSocket grant enforcement is `EnforceTerminalCredentialGrantForWebSocket` at `backend/internal/api/handlers/credential_access_grant.go:632-644`.
- config import/export grant middleware is anchored at `backend/internal/api/handlers/credential_access_grant.go:649-653`.
- snapshot restore/task restore/manual trigger middleware is anchored at `backend/internal/api/handlers/credential_access_grant.go:663-685`.
- batch trigger and batch command all-resource enforcement helpers are at `backend/internal/api/handlers/credential_access_grant.go:689-700`.
- active grant lookup checks user, role, action, purpose, resource tuple, status, and expiry in `findActiveCredentialGrant` at `backend/internal/api/handlers/credential_access_grant.go:795`.
- reason sanitization is centralized at `backend/internal/api/handlers/credential_access_grant.go:898`; response output sanitization is at `backend/internal/api/handlers/credential_access_grant.go:999` and `1222`.
- grant audit helpers are at `backend/internal/api/handlers/credential_access_grant.go:1014-1056`.

#### Task trigger, batch trigger, restore, and batch command handlers

Manual task trigger:

- handler loads task audit context, triggers the runner, and writes credential audit success/failure at `backend/internal/api/handlers/task_handler.go:462-495`.
- credential audit purpose differentiates command/batch command/backup purposes at `backend/internal/api/handlers/task_handler.go:841-854`.

Batch trigger:

- parses task IDs and builds `tasksToTrigger` after task existence and ownership filtering at `backend/internal/api/handlers/task_handler.go:666-715`.
- enforces step-up and all task grants only when at least one eligible task remains at `backend/internal/api/handlers/task_handler.go:717-720`.
- if no tasks remain eligible, writes a no-op credential audit and returns OK at `backend/internal/api/handlers/task_handler.go:735-755`.
- if tasks execute, writes aggregate credential audit at `backend/internal/api/handlers/task_handler.go:758-775`.

Task restore:

- route middleware applies admin + step-up + task restore grant; handler triggers restore and writes credential audit at `backend/internal/api/handlers/task_handler.go:609-651`.

Batch command:

- handler begins at `backend/internal/api/handlers/batch_handler.go:56`.
- ownership filtering occurs before grant checks at `backend/internal/api/handlers/batch_handler.go:95`.
- step-up is enforced before command task creation at `backend/internal/api/handlers/batch_handler.go:113`.
- all node-scoped batch command grants are enforced before task creation at `backend/internal/api/handlers/batch_handler.go:120`.

#### Terminal WebSocket gate order

`backend/internal/api/handlers/terminal_handler.go` follows the expected grant-before-credential-use order:

1. primary in-protocol token validation with admin requirement at `backend/internal/api/handlers/terminal_handler.go:202`;
2. step-up proof validation at `backend/internal/api/handlers/terminal_handler.go:217`;
3. `node_id` parse at `backend/internal/api/handlers/terminal_handler.go:228-241`;
4. terminal credential grant enforcement at `backend/internal/api/handlers/terminal_handler.go:247`;
5. only after that, node load and SSH auth resolution, including `sshutil.BuildSSHAuthForPurpose(..., sshutil.PurposeTerminal)`, at `backend/internal/api/handlers/terminal_handler.go:276`.

#### Config import/export and sensitive fields

`backend/internal/api/handlers/config_handler.go` distinguishes safe export from sensitive export:

- `includeSecrets := c.Query("include_secrets") == "true"` at `backend/internal/api/handlers/config_handler.go:60`.
- node password/private key are included only when sensitive export is requested at `backend/internal/api/handlers/config_handler.go:127-128`.
- SSH key private key is included only when sensitive export is requested at `backend/internal/api/handlers/config_handler.go:148`.
- task `executor_config` is included only when sensitive export is requested at `backend/internal/api/handlers/config_handler.go:206`.
- import entry point is `ConfigHandler.Import` at `backend/internal/api/handlers/config_handler.go:271`; private keys/passwords/executor config are imported at `backend/internal/api/handlers/config_handler.go:327`, `342`, `415-418`, and `542`.
- config handler includes marker checks for secret-like strings in audit/error safety helpers at `backend/internal/api/handlers/config_handler.go:706` and `718`.

#### Snapshot restore target validation

- dangerous restore path list is defined at `backend/internal/api/handlers/snapshot_handler.go:16-17`.
- `validateRestoreTargetPath` requires absolute non-root paths outside dangerous prefixes at `backend/internal/api/handlers/snapshot_handler.go:23-32`.
- snapshot restore handler starts at `backend/internal/api/handlers/snapshot_handler.go:184` and rejects unsafe target paths at `backend/internal/api/handlers/snapshot_handler.go:201`.

#### Models, migrations, and SSH purpose scoping

- `model.CredentialAccessGrant` is defined at `backend/internal/model/models.go:446-456` and includes requester, action, purpose, node/task/policy resource fields, reason, status, TTL, approval/revocation, and timestamps.
- node response sanitization removes password/private key and nested SSH key material at `backend/internal/model/models.go:46-47`.
- app credential config sanitization removes password-like values at `backend/internal/model/models.go:188-194`.
- grant migrations exist for both SQLite and PostgreSQL at `backend/internal/database/migrations/sqlite/000060_credential_access_grants.up.sql:1` and `backend/internal/database/migrations/postgres/000060_credential_access_grants.up.sql:3`.
- stable SSH credential purposes include `terminal`, `task_command`, `batch_command`, `task_restore`, and `snapshot` at `backend/internal/sshutil/scope.go:17-29`.
- purpose-aware SSH auth entry point is `BuildSSHAuthForPurpose` at `backend/internal/sshutil/ssh_auth.go:76-78`.

### Frontend grant and step-up flows

#### API client and error-code handling

- `request()` adds bearer auth and `X-Xirang-Step-Up` when `stepUpProof` is passed at `web/src/lib/api/core.ts:49-60`.
- `isStepUpRequiredError` and `isCredentialGrantRequiredError` use machine-readable `error_code` from 403 envelopes at `web/src/lib/api/core.ts:173-190`.
- grant API action/purpose mappers recognize all current backend tuples at `web/src/lib/api/credential-access-grants-api.ts:61-69`.
- grant list query serialization is implemented at `web/src/lib/api/credential-access-grants-api.ts:91-125`.
- grant list and request methods cover list, terminal, config import/export, snapshot restore, task restore, task manual trigger, task batch trigger, and batch command at `web/src/lib/api/credential-access-grants-api.ts:155-317`.
- task trigger/restore/batch trigger API calls accept optional step-up proof at `web/src/lib/api/tasks-api.ts:243-276`.
- batch command API accepts optional step-up proof at `web/src/lib/api/batch-api.ts:48-61`.
- config import/export API accepts optional step-up proof at `web/src/lib/api/config-api.ts:36-53`.

#### Auth step-up state

- step-up proof storage is session-only and clears expired/malformed values at `web/src/lib/step-up-storage.ts:40-67`.
- login/logout and disabling TOTP clear cached step-up proof at `web/src/context/auth-context-provider.tsx:151-202` and `211-217`.
- `ensureStepUpProof()` defaults to `persist=true` and `reuseCached=true`, reads cached proof, then opens the TOTP dialog if needed at `web/src/context/auth-context-provider.tsx:224-251`.
- successful step-up proof submission stores proof when `persist` is true at `web/src/context/auth-context-provider.tsx:278-287`.
- `useStepUpAction()` retries an action after `STEP_UP_REQUIRED`, then clears proof if retry still requires step-up at `web/src/hooks/use-step-up-action.ts:5-25`.

#### Manual task trigger

- task page calls context `triggerTask(taskId)` at `web/src/pages/tasks-page.tsx:248-252`.
- task operations hook wraps manual trigger in `withStepUp`, requests `requestTaskManualTriggerCredentialGrant`, then calls `apiClient.triggerTask` with the same proof at `web/src/hooks/use-console-task-operations.ts:107-116`.

#### Batch trigger

- task page validates selected tasks and confirmation, then uses `withStepUp` to request task-batch-trigger grants before `apiClient.batchTriggerTasks` at `web/src/pages/tasks-page.tsx:365-384`.

#### Batch command

- batch command dialog validates selected nodes and command length at `web/src/components/batch-command-dialog.tsx:61-73`.
- it then requests batch-command node grants and creates the batch command with the same proof at `web/src/components/batch-command-dialog.tsx:79-93`.

#### Restore flows

- task restore dialog requires a local reason, requests a non-persistent/non-reused proof, requests task restore grant, then calls restore with the same proof at `web/src/components/restore-confirm-dialog.tsx:55-80`.
- snapshot browser does not request restore grants while browsing; restore submission requests a non-persistent/non-reused proof, requests snapshot restore grant, then calls restore with the same proof at `web/src/components/snapshot-browser.tsx:137-166`.

#### Terminal flow

- terminal component requests a step-up proof before opening the socket and sends the first auth message containing `{ type: "auth", token, step_up_proof }` at `web/src/components/web-terminal.tsx:153-194`.
- `CREDENTIAL_GRANT_REQUIRED:*` close reasons are detected separately from generic policy-violation closes at `web/src/components/web-terminal.tsx:49-75` and `208-220`.
- grant-required close opens a local reason dialog, requests a terminal grant, clears only transient prompt state, then retries the terminal connection at `web/src/components/web-terminal.tsx:105-140`.

#### Config import/export

- safe config export calls `apiClient.exportConfig(token)` without grant prompt at `web/src/components/config-export-import.tsx:42-52`.
- import flow defers file parsing until grant submission, requests step-up and config import grant, then imports with the same proof at `web/src/components/config-export-import.tsx:119-131`.
- sensitive export flow requests step-up and config export grant, then calls `exportConfig(token, true, proof)` at `web/src/components/config-export-import.tsx:141-150`.

#### Grant status/list UI

- grant list page is admin-only: non-admin users are redirected to overview at `web/src/pages/credential-access-grants-page.tsx:207-209`.
- page loads grants through `apiClient.listCredentialAccessGrants` and supports filters/pagination at `web/src/pages/credential-access-grants-page.tsx:136-178`.
- details dialog renders read-only grant fields, including reason, status, resource IDs, and timestamps at `web/src/pages/credential-access-grants-page.tsx:377-407`.
- frontend route is registered at `web/src/router.tsx:108-110`.
- navigation entry is admin-only at `web/src/components/layout/navigation.ts:126-131`.

### Test Coverage Observed

#### Backend tests

`backend/internal/api/handlers/credential_access_grant_test.go` contains broad grant coverage:

- list filters/pagination/sort/sanitization: `TestCredentialAccessGrantListFiltersPaginationSortsAndSanitizes` at `backend/internal/api/handlers/credential_access_grant_test.go:80`.
- terminal/config/snapshot/task restore grant creation and safe audit tests at `backend/internal/api/handlers/credential_access_grant_test.go:210`, `284`, `354`, `440`, and `540`.
- grant request validation/admin/step-up/reason checks at `backend/internal/api/handlers/credential_access_grant_test.go:649`.
- active grant tuple matching for terminal/config/snapshot/task restore at `backend/internal/api/handlers/credential_access_grant_test.go:691`, `749`, `802`, and `1067`.
- operator-owned manual trigger grant request and unowned denial at `backend/internal/api/handlers/credential_access_grant_test.go:867` and `902`.
- batch grant rows per resource at `backend/internal/api/handlers/credential_access_grant_test.go:925`.
- operator role validity for owned resource operations at `backend/internal/api/handlers/credential_access_grant_test.go:981`.
- manual trigger route gate and batch all-or-nothing enforcement at `backend/internal/api/handlers/credential_access_grant_test.go:1000` and `1031`.
- inactive/wrong tuple/other operation rejection for task restore, snapshot restore, config export, and config import at `backend/internal/api/handlers/credential_access_grant_test.go:1318`, `1453`, `1773`, and `1889`.
- browse routes not requiring snapshot restore grant at `backend/internal/api/handlers/credential_access_grant_test.go:1572`.
- config import/export route grant-before-mutation/use tests at `backend/internal/api/handlers/credential_access_grant_test.go:1598`, `1625`, `1673`, and `1710`.
- terminal reason sanitization and terminal grant use/blocked audit at `backend/internal/api/handlers/credential_access_grant_test.go:2163` and `2180`.

`backend/internal/api/handlers/step_up_test.go` covers:

- step-up proof issuance and disabled/invalid TOTP rejection at `backend/internal/api/handlers/step_up_test.go:188` and `235`.
- missing/invalid/expired/wrong-user/stale proof validation at `backend/internal/api/handlers/step_up_test.go:279`.
- purpose-scoped token rejection for primary REST/WS auth at `backend/internal/api/handlers/step_up_test.go:341`.
- RBAC/ownership denials before step-up at `backend/internal/api/handlers/step_up_test.go:366`.
- config export step-up only when including sensitive values at `backend/internal/api/handlers/step_up_test.go:396`.
- terminal WebSocket step-up proof requirement at `backend/internal/api/handlers/step_up_test.go:423`.
- snapshot restore safe credential audit evidence at `backend/internal/api/handlers/step_up_test.go:510`.

`backend/internal/api/handlers/batch_handler_test.go` covers:

- operator unowned node rejection at `backend/internal/api/handlers/batch_handler_test.go:24`.
- missing grant does not decrypt inline credentials at `backend/internal/api/handlers/batch_handler_test.go:75`.
- all node grants required before creating tasks at `backend/internal/api/handlers/batch_handler_test.go:113`.
- batch status redacts executor config and operator batch ownership checks at `backend/internal/api/handlers/batch_handler_test.go:181`, `225`, and `271`.

`backend/internal/api/router_test.go` covers:

- route registration smoke test at `backend/internal/api/router_test.go:54`.
- static `/tasks/batch-trigger` route uses batch handler at `backend/internal/api/router_test.go:180`.
- full-router terminal grant route RBAC and step-up at `backend/internal/api/router_test.go:228`.
- full-router grant list RBAC at `backend/internal/api/router_test.go:302`.

#### Frontend tests

- grant API mapper and all grant request endpoints are covered in `web/src/lib/api/credential-access-grants-api.test.ts`, including config import/export at lines `225-293`, snapshot/task restore at `295-367`, terminal at `369-404`, task manual/batch trigger at `406-456`, and batch command at `458-484`.
- `useStepUpAction` retry/clear behavior is tested at `web/src/hooks/use-step-up-action.test.tsx:42-68`.
- auth step-up cache and one-time proof behavior is tested at `web/src/context/auth-context.test.tsx:95-160`.
- terminal WebSocket step-up auth, grant-required prompt/retry, sanitized close details, and no storage of grant state/reason are tested at `web/src/components/web-terminal.test.tsx:109-215`.
- batch command grant-before-create and storage-safety are tested at `web/src/components/batch-command-dialog.test.tsx:78-116`.
- task restore reason validation, one-time proof, grant-before-restore, and storage-safety are tested at `web/src/components/restore-confirm-dialog.test.tsx:69-123`.
- snapshot browse no-grant, reason validation, one-time proof, grant-before-restore, and storage-safety are tested at `web/src/components/snapshot-browser.test.tsx:63-130`.
- config import/sensitive export grant-before-action, local validation, error text rendering, deferred file parsing, and storage-safety are tested at `web/src/components/config-export-import.test.tsx:80-244`.
- task page batch trigger grant-before-trigger is tested at `web/src/pages/tasks-page.test.tsx:463-487`.
- grant list filters, pagination, read-only details, storage-safety, and non-admin redirect are tested at `web/src/pages/credential-access-grants-page.test.tsx:127-249`.
- grant list a11y smoke test is at `web/src/pages/__tests__/credential-access-grants-page.a11y.test.tsx:65-74`.
- admin-only grant navigation is tested at `web/src/components/layout/navigation.test.ts:23-27`.

### Likely Gaps / Areas Needing Comprehensive Review

1. **Full-router route registration assertions omit several grant request endpoints.**
   - `router.go` registers config-import, config-export, snapshot-restore, and task-restore grant endpoints at `backend/internal/api/router.go:267-270`.
   - `TestNewRouterRegisterRoutes` asserts grant list, terminal, task manual trigger, task batch trigger, and batch command grant routes at `backend/internal/api/router_test.go:91-104`, but the visible assertions do not include config-import, config-export, snapshot-restore, or task-restore grant routes.
   - Many route-enforcement tests for those operations use helper routers in `credential_access_grant_test.go`, so the implementation is still covered at middleware-helper level; the gap is specifically full-router registration smoke coverage.

2. **Batch trigger has an explicit no-eligible-task path that bypasses step-up/grant enforcement.**
   - `BatchTrigger` enforces step-up and grants only when `len(tasksToTrigger) > 0` at `backend/internal/api/handlers/task_handler.go:717-720`.
   - When every requested task is nonexistent or filtered out by ownership, it writes a no-op audit and returns OK at `backend/internal/api/handlers/task_handler.go:735-755`.
   - `router_test.go` includes a static route test that posts a nonexistent task ID and receives the batch handler response at `backend/internal/api/router_test.go:180-202`.
   - This appears intentional in code, but it is a control-plane semantics area to review because a malformed/all-ineligible request does not require step-up/grant.

3. **Grant list UI filter options are narrower than the grant API/domain tuple set.**
   - API/domain supports task manual trigger, task batch trigger, and batch command grant actions/purposes at `web/src/lib/api/credential-access-grants-api.ts:61-69` and `web/src/types/domain.ts:603-605`.
   - Grant list page action/purpose filter options visibly include terminal/config/snapshot/task restore only at `web/src/pages/credential-access-grants-page.tsx:32-47`.
   - The page can still display returned rows with those values because the mapper/domain supports them, but the filter dropdowns do not visibly include `task.manual_trigger`, `task.batch_trigger`, `batch_command.create`, `task_command`, or `batch_command`.

4. **One-shot frontend operations use mixed step-up proof persistence behavior.**
   - Auth provider defaults `ensureStepUpProof()` to persistent/reusable proof behavior at `web/src/context/auth-context-provider.tsx:224-251` and stores successful proofs when `persist` is true at `web/src/context/auth-context-provider.tsx:278-287`.
   - Task restore and snapshot restore request non-persistent/non-reused proofs at `web/src/components/restore-confirm-dialog.tsx:69` and `web/src/components/snapshot-browser.tsx:156`.
   - Config import/export use default `ensureStepUpProof()` at `web/src/components/config-export-import.tsx:125` and `145`.
   - Batch command and task trigger/batch trigger flows use `useStepUpAction`, whose retry path calls default `ensureStepUpProof()` at `web/src/hooks/use-step-up-action.ts:15`; batch command and batch trigger use it at `web/src/components/batch-command-dialog.tsx:79-93` and `web/src/pages/tasks-page.tsx:377-384`; manual trigger uses it at `web/src/hooks/use-console-task-operations.ts:107-116`.
   - Related frontend spec says one-shot operations should request a non-persistent proof and avoid cached proof material at `.trellis/spec/frontend/state-management.md:89`.

5. **Several backend operation-route enforcement tests use helper routers instead of full `NewRouter` wiring.**
   - Helper-router route enforcement is broad in `backend/internal/api/handlers/credential_access_grant_test.go`, for example task/snapshot/config route helpers at `backend/internal/api/handlers/credential_access_grant_test.go:1984-2016`.
   - Full-router tests currently cover terminal grant route RBAC/step-up and grant list RBAC at `backend/internal/api/router_test.go:228` and `302`.
   - This leaves some confidence dependent on static inspection of `backend/internal/api/router.go` for exact production middleware ordering on config import/export, task restore, snapshot restore, and manual/batch trigger routes.

6. **Task page manual trigger page-level test delegates grant coverage to the operations hook.**
   - Page handler calls `triggerTask(taskId)` at `web/src/pages/tasks-page.tsx:248-252`.
   - The grant-before-trigger sequence is implemented in `web/src/hooks/use-console-task-operations.ts:107-116`.
   - `web/src/pages/tasks-page.test.tsx:445-461` asserts the page calls `triggerTask(102)`; the grant sequence is covered in hook-level tests/search evidence rather than that page test itself.

7. **Grant status/list is admin-only while some grant request types are operator-capable.**
   - Backend list route is admin-only at `backend/internal/api/router.go:265`.
   - Frontend grant list page redirects non-admin users at `web/src/pages/credential-access-grants-page.tsx:207-209` and navigation is admin-only at `web/src/components/layout/navigation.ts:126-131`.
   - Backend operators can request owned task manual trigger, task batch trigger, and batch command grants at `backend/internal/api/handlers/credential_access_grant.go:344`, `376`, and `412`.
   - This may be intended for admin oversight; it is relevant to review under “grant status/list” coverage because operator-created grants are not visible through the frontend status/list surface for operators.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Credential access grants scenario applies to `credential_access_grants`, grant lifecycle handlers, terminal WebSocket open, and grant-covered operations including sensitive config import/export, restore, snapshot restore, task trigger, and batch command creation at lines `451-456`.
- `.trellis/spec/backend/quality-guidelines.md` — Terminal grant route/boundary and stable tuple requirements are listed at lines `460-463`.
- `.trellis/spec/backend/quality-guidelines.md` — Grants are row-backed authorization records, must not store secrets/tokens/commands/output/host-sensitive strings, and must be additive to existing controls at lines `467-470`.
- `.trellis/spec/backend/quality-guidelines.md` — Missing/wrong/expired/revoked grants must fail closed and write sanitized audit evidence at lines `472-479`.
- `.trellis/spec/backend/quality-guidelines.md` — Terminal tests must prove grant gate runs before node load/credential resolution/SSH dial at line `498`; audit tests must exclude secret/stream/command/output data at line `499`.
- `.trellis/spec/frontend/type-safety.md` — Grant mapping requirements apply to `credential-access-grants-api.ts`, domain types, API client exports, and UI rendering grant status/expiry/bindings/denials at lines `204-209`.
- `.trellis/spec/frontend/type-safety.md` — Step-up proof should be sent through request option/header, raw snake_case types should stay private, unknown status should degrade safely, reason should not be enriched with host/endpoint/credential/command/output data, and grant-required detection should use machine-readable error codes at lines `213-224`.
- `.trellis/spec/frontend/type-safety.md` — Mapper/UI/storage tests are expected for grant DTOs and grant-required detection at lines `244-247`.
- `.trellis/spec/frontend/state-management.md` — Terminal grant prompt state rules apply to `web-terminal.tsx`, grant API, auth step-up context, and future grant prompts at lines `71-76`.
- `.trellis/spec/frontend/state-management.md` — Frontend must not store grant IDs/material/reasons/status in browser storage; one-shot operations should use non-persistent proofs; terminal grant-required close must not be treated as session expiry at lines `86-91`.
- `.trellis/spec/frontend/state-management.md` — Terminal prompt test expectations include grant-required close handling, prompt opening, reason validation, grant request, retry, and storage-safety at lines `113-115`.

### External References

None. This was an internal codebase/spec research task; no external documentation was needed.

## Caveats / Not Found

- Static inspection only: no `go test`, `npm run check`, or targeted test command was run during this research pass.
- Several `rg` searches produced large persisted outputs in the local Claude tool-results directory; this report cites the relevant source paths/lines instead of those tool-result files.
- The report focuses on current coverage and review areas. It does not propose code changes or refactors.
