# Research: Remaining control-plane surfaces

- **Query**: Research the next executable P3 control-plane hardening slice for Xirang after P2 terminal grants and first P3 config-import grants/batch-trigger no-op telemetry; compare sensitive config export, SSH key export, task trigger/restore, snapshot restore, and batch command creation; recommend one bounded P3 slice reusing `CredentialAccessGrant`, `RequireStepUp`, credential audit helpers, and frontend grant prompt patterns without P4 architecture work.
- **Scope**: internal
- **Date**: 2026-05-21

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | P3/P4 roadmap; first P3 slice was config-import grant plus batch-trigger no-op audit; follow-up list includes sensitive config export, SSH key export, task trigger/restore, snapshot restore, and batch command creation. |
| `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/prd.md` | P2 JIT grant baseline and follow-up roadmap for extending grants beyond terminal. |
| `.trellis/tasks/archive/2026-05/05-20-security-p2-credential-hardening/research/code-surfaces.md` | P2 code-surface research; identifies REST high-risk operations and safe grant/audit constraints. |
| `.trellis/tasks/archive/2026-05/05-20-p1-overall-security-review-before-p2/final-review-report.md` | P1 acceptance review; records existing auth/RBAC/step-up/audit coverage and the earlier batch-trigger no-op observation. |
| `.trellis/tasks/05-21-p3-control-plane-follow-up-hardening/prd.md` | Current task scope; config import and batch-trigger no-op telemetry are already covered, and next slice should be one bounded high-risk REST operation. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend contracts for SSH key scope, credential-use audit events, and credential access grants. |
| `.trellis/spec/frontend/state-management.md` | Frontend contract for grant prompt state: component-local, no grant material in browser storage, sanitized denial rendering. |
| `.trellis/spec/frontend/a11y-guidelines.md` | Frontend dialog/form accessibility requirements relevant to any new grant prompt. |
| `backend/internal/api/router.go` | Route registration and current middleware ordering for candidate operations. |
| `backend/internal/api/handlers/credential_access_grant.go` | Existing row-backed grant constants, request handlers, matching helper, config-import grant middleware, audit writers, reason/TTL sanitizer, and machine-readable denial shape. |
| `backend/internal/model/models.go` | `CredentialAccessGrant` model supports action, purpose, optional node/task/policy resource IDs, reason, status, TTL, and timestamps. |
| `backend/internal/api/handlers/step_up.go` | `RequireStepUp` / `RequireStepUpIf` and step-up audit/denial patterns. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import handlers; sensitive export can include secret-bearing categories; config import is already grant-gated at route level. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key export handler; exports public-key/fingerprint formats after step-up and SSH key scope filtering. |
| `backend/internal/api/handlers/task_handler.go` | Manual trigger, restore trigger, and batch-trigger handlers with step-up/audit behavior. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot restore handler; admin-only route, step-up gate, path safety check, and sanitized audit metadata. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation handler; ownership filtering, step-up, task creation/triggering, and sanitized counts-only audit. |
| `backend/internal/credentialaudit/audit.go` | Metadata/error sanitizer drops forbidden key/value categories and bounds audit fields. |
| `web/src/lib/api/core.ts` | Step-up header plumbing and `STEP_UP_REQUIRED` / `CREDENTIAL_GRANT_REQUIRED` detection. |
| `web/src/lib/api/credential-access-grants-api.ts` | Frontend grant request wrapper currently supports terminal and config import only. |
| `web/src/types/domain.ts` | Frontend grant action/purpose union currently supports terminal and config import only. |
| `web/src/components/config-export-import.tsx` | Existing config import grant prompt flow; default config export currently does not request secrets. |
| `web/src/components/web-terminal.tsx` | Terminal grant prompt/retry pattern and sanitized grant-required WebSocket handling. |
| `web/src/components/ssh-key-export-dialog.tsx` | Direct-download SSH key export flow with step-up retry. |
| `web/src/components/restore-confirm-dialog.tsx` | Task restore step-up flow. |
| `web/src/components/snapshot-browser.tsx` | Snapshot restore step-up flow. |
| `web/src/components/batch-command-dialog.tsx` | Batch command creation step-up flow. |
| `web/src/lib/api/config-api.ts` | Config export/import API wrapper; export supports `includeSecrets` and step-up proof, import now also accepts step-up proof. |
| `web/src/lib/api/ssh-keys-api.ts` | SSH key export direct-fetch helper sends step-up proof. |
| `web/src/lib/api/tasks-api.ts` | Task trigger/restore/batch-trigger wrappers accept step-up proof. |
| `web/src/lib/api/snapshots-api.ts` | Snapshot restore wrapper accepts step-up proof. |
| `web/src/lib/api/batch-api.ts` | Batch command wrapper accepts step-up proof. |

### Code Patterns

#### Existing grant primitive and constraints

- The current grant model is already general enough for system-scoped and task-scoped REST operations: `CredentialAccessGrant` has `Action`, `Purpose`, optional `NodeID`, optional `TaskID`, optional `PolicyID`, bounded `Reason`, `Status`, `RequestedTTLSeconds`, and UTC lifecycle timestamps (`backend/internal/model/models.go:446-471`).
- Existing action constants are only `terminal.open` and `config.import`; config-import uses purpose `config_import` (`backend/internal/api/handlers/credential_access_grant.go:32-34`). Frontend grant types mirror that same limited set (`web/src/types/domain.ts:603-629`; `web/src/lib/api/credential-access-grants-api.ts:42-50`).
- Grant creation is currently admin self-grant after step-up. `validateGrantRequest` calls the shared step-up verifier, revalidates requester role, sanitizes reason text, and bounds TTL (`backend/internal/api/handlers/credential_access_grant.go:173-195`).
- Active grant matching requires the current DB role to match the token role and requires `admin` (`backend/internal/api/handlers/credential_access_grant.go:304-318`). This is a key locality constraint: applying grants to operator-permitted operations would require role/ownership design beyond the current admin-only grant path.
- Matching already supports exact optional node/task/policy scoping via `credentialGrantMatch` and `applyCredentialGrantMatch` (`backend/internal/api/handlers/credential_access_grant.go:296-302`, `:366-383`). A system-scoped operation can use null resource IDs without schema work.
- Config import is already the REST grant pattern: route order is primary auth/admin, step-up, active grant check, then handler (`backend/internal/api/router.go:321-324`; `backend/internal/api/handlers/credential_access_grant.go:141-160`, `:243-262`).
- Grant audit writers use sanitizer-compatible metadata such as `stage`, `operation`, `status`, `ttl_seconds`, `grant_id`, `self_grant`, and optional `node_id` (`backend/internal/api/handlers/credential_access_grant.go:448-510`).
- Reason text rejects secret/output/host-sensitive markers and is capped at 240 runes (`backend/internal/api/handlers/credential_access_grant.go:394-420`, `:512-587`).

#### Existing step-up and audit primitives

- `RequireStepUp` / `RequireStepUpIf` enforce proof header `X-Xirang-Step-Up` and write step-up credential audit events on required/failed/satisfied paths (`backend/internal/api/handlers/step_up.go:36-75`, `:114-123`, `:125-150`).
- Credential audit sanitization bounds metadata, drops forbidden key/value categories, and redacts output-bearing error text (`backend/internal/credentialaudit/audit.go:144-177`, `:208-280`, `:291-310`). The backend spec explicitly forbids raw passwords, private keys, TOTP/JWT/recovery values, decrypted executor config, terminal streams, file contents, raw command output/text, exported payloads, endpoint/proxy values, and host-sensitive strings in audit/grant evidence (`.trellis/spec/backend/quality-guidelines.md:451-499`).
- Current P1/P2/P3 route stack preserves additive controls: secured routes are authenticated before route-level RBAC/role/ownership/step-up/grant middleware (`backend/internal/api/router.go:225-324`).

#### Frontend grant/step-up patterns

- API requests attach step-up proof via `X-Xirang-Step-Up`; `core.ts` distinguishes `STEP_UP_REQUIRED` and `CREDENTIAL_GRANT_REQUIRED` from session expiration (`web/src/lib/api/core.ts:49-60`, `:173-191`).
- `useStepUpAction` retries a protected action once after a step-up denial (`web/src/hooks/use-step-up-action.ts:5-25`).
- Terminal grant prompt stores only local dialog state, requests a step-up proof, requests a grant, then retries the operation (`web/src/components/web-terminal.tsx:49-51`, `:105-140`, `:208-215`, `:295-338`).
- Config import already duplicates the REST grant prompt pattern: reason dialog, parse-only precheck, `ensureStepUpProof`, grant request, then import with the proof; it does not write grant material to browser storage (`web/src/components/config-export-import.tsx:101-132`).
- The frontend grant API wrapper and domain unions only know terminal/config-import today (`web/src/lib/api/credential-access-grants-api.ts:94-129`; `web/src/types/domain.ts:603-629`), so any next grant-covered operation needs explicit mapper/type/API additions.

### Candidate Comparison

| Candidate | Credential / asset blast radius | Existing auth / RBAC / step-up / audit coverage | Feasibility / locality | UX impact | Secret-safety concerns |
|---|---|---|---|---|---|
| Sensitive config export: `GET /config/export?include_secrets=true` | Highest direct exfiltration blast radius among remaining candidates. With secrets enabled, export can include node password/private-key fields, SSH private keys, task executor config, and DB-backed settings values (`backend/internal/api/handlers/config_handler.go:126-149`, `:205-207`, `:217-225`). | Route is admin-only and conditionally step-up-gated for `include_secrets=true` (`backend/internal/api/router.go:321-323`). Handler has a defensive admin check and writes `config.export` audit with counts and `with_sensitive` only, not payload data (`backend/internal/api/handlers/config_handler.go:59-86`, `:228-243`). P1 final review marked this route pass for admin + conditional step-up (`.trellis/tasks/archive/2026-05/05-20-p1-overall-security-review-before-p2/final-review-report.md:48-49`). | Very high. Same system-scoped shape as config import: no resource ID and no migration needed. Add `config.export` / `config_export` grant request and middleware under the same `include_secrets=true` predicate before `configHandler.Export` loads/serializes DB records. Existing config-import route is the closest pattern (`backend/internal/api/router.go:321-324`; `backend/internal/api/handlers/credential_access_grant.go:141-160`, `:243-262`). | Low for current UI because `ConfigExportImport` only calls safe `exportConfig(token)` today (`web/src/components/config-export-import.tsx:28-39`). If a sensitive export button is added, it can reuse the config-import reason dialog pattern with clear labeling; default safe export can stay unchanged. | Do not store or display exported payload data, settings values, node addresses, endpoints/proxies, executor config, private keys, or passwords in grant rows, audit metadata, tests, logs, or UI errors. Grant metadata should be system-scoped counts/status only. |
| SSH key export: `GET /ssh-keys/export` | Lower direct secret blast radius in current code: export outputs public key/fingerprint-oriented formats, not private keys (`backend/internal/api/handlers/ssh_key_handler.go:729-777`). It still discloses key inventory and uses private key material server-side to derive public keys. | Route has `ssh_keys:read` RBAC and route-level step-up (`backend/internal/api/router.go:225-229`). Handler filters with `ValidateSSHKeyPurpose(..., ssh_key_export)` and writes counts-only `ssh_key.export` audit (`backend/internal/api/handlers/ssh_key_handler.go:705-727`). Frontend direct download sends step-up proof (`web/src/components/ssh-key-export-dialog.tsx:133-151`; `web/src/lib/api/ssh-keys-api.ts:101-121`). | Medium-high if system-scoped grant is accepted; lower if per-key grant is desired because current grant model has no `SSHKeyID` field. A system-scoped export grant would not need migration but would be broader than selected-ID export. | Moderate. Existing export dialog already has format/scope controls and direct download; a grant prompt would need to wrap a file-download flow without losing the selected options. | Do not persist export URLs, selected key names, public key material, fingerprints, or file contents in grant/audit metadata. The direct fetch helper would need grant-required handling equivalent to normal API requests. |
| Task manual trigger and task restore: `POST /tasks/:id/trigger`, `POST /tasks/:id/restore`, plus `POST /tasks/batch-trigger` | High operational blast radius: manual trigger can start backup or command tasks that later resolve stored credentials; restore can mutate node data. Batch trigger can fan out across multiple tasks. | Manual trigger has `tasks:trigger`, ownership check, route-level step-up, and credential audit with task/node/policy context (`backend/internal/api/router.go:284-290`; `backend/internal/api/handlers/task_handler.go:462-495`). Restore is admin-only with step-up and audit using booleans/counts only (`backend/internal/api/handlers/task_handler.go:609-652`). Batch trigger has `tasks:write`, ownership filtering, step-up only when at least one task is eligible, and now writes all-blocked/no-op telemetry (`backend/internal/api/handlers/task_handler.go:666-773`). | Mixed. `TaskID` is already present in grant model, so admin-only restore can be task-scoped without schema. Manual trigger and batch trigger are operator-permitted flows; current grant matching hard-requires `admin`, so enforcing grants there would require role/ownership-aware grant semantics beyond the current primitive (`backend/internal/api/handlers/credential_access_grant.go:304-318`). | Restore: moderate, single-task prompt. Manual/batch trigger: higher because operators and alert retry paths can invoke task triggers; one alert center path calls `triggerTask` without `useStepUpAction` today (`web/src/pages/notifications/alert-center.tsx:315-327`), relying on backend denial if step-up is required. | Do not store task command text, executor config, source/target paths, restore target, or run output in grant reasons/audit. Existing task audit metadata includes executor/source/policy IDs and must remain bounded (`backend/internal/api/handlers/task_handler.go:855-870`). |
| Snapshot restore: `POST /tasks/:id/snapshots/:sid/restore` | High asset mutation risk for backup data restore. It can cause restic restore of selected paths to a target path and may use task/restic credential context, but it is not a direct bulk secret export. | Route is admin-only and step-up-gated (`backend/internal/api/router.go:310-312`). Handler validates snapshot ID shape, validates target path safety, loads task, enforces restic executor, performs restore, and writes `snapshot.restore` audit with include count, target-set boolean, and short snapshot reference only (`backend/internal/api/handlers/snapshot_handler.go:184-227`, `:139-167`). Frontend restore uses `useStepUpAction` (`web/src/components/snapshot-browser.tsx:91-103`; `web/src/lib/api/snapshots-api.ts:43-50`). | High for a bounded admin-only task-scoped grant. Existing `TaskID` can scope the grant; no schema required. If snapshot-ID-level grants are required, the model lacks a string resource field, so that would be larger. Route middleware can parse `:id` and enforce before body parsing/restore execution. | Moderate. Existing snapshot restore is inline in `SnapshotBrowser`; adding a reason dialog is straightforward but must avoid including selected file paths or target path in grant state. | Do not persist selected paths, restore target, snapshot full ID, file names, or file contents in grant rows/audit. Current audit already avoids target path and truncates snapshot ID; grant should be task-scoped and reason-sanitized. |
| Batch command creation: `POST /batch-commands` | Very high execution blast radius: creates and triggers command tasks across multiple nodes and uses stored credentials at runtime. | Route has `tasks:write` RBAC. Handler validates command length and local dangerous-command policy, ownership-filters nodes, then enforces step-up before task creation/triggering (`backend/internal/api/router.go:296-298`; `backend/internal/api/handlers/batch_handler.go:56-115`). Audit stores batch ID and counts/retain flag, not raw command text (`backend/internal/api/handlers/batch_handler.go:117-182`). Frontend uses `useStepUpAction` (`web/src/components/batch-command-dialog.tsx:61-85`; `web/src/lib/api/batch-api.ts:48-61`). | Lower for the next bounded slice despite high risk. Current grant model has one `NodeID` but no multi-node set or batch ID before creation; current grant matching is admin-only while batch command supports ownership-filtered non-admin roles. A safe design likely needs role-aware and/or multi-resource grant semantics, which is closer to policy work than a small P3 slice. | High. A new prompt must fit a multi-node command dialog and not leak command text. Operators may be affected if grants become admin-only. | Never store command text/output, selected node names/hosts, or retained task command fields in grant/audit metadata. Reason sanitizer already rejects command/output-like text, which helps but makes UX wording important. |

### Recommendation

Recommend the next bounded P3 slice: **sensitive config export JIT grant for `GET /config/export?include_secrets=true`**.

Why this slice is the best next executable step:

1. It has the largest direct credential exfiltration blast radius among remaining candidates because it can serialize multiple secret-bearing categories from the control plane.
2. It is already admin-only and conditionally step-up-gated, so a grant check is additive and does not require role/ownership redesign.
3. It is system-scoped like the just-delivered config-import grant, so the existing `CredentialAccessGrant` schema can represent it without migration or P4 architecture work.
4. It can be enforced before the export handler loads and serializes secret-bearing records by adding a route predicate parallel to the current `RequireStepUpIf` predicate.
5. It provides a second REST grant pattern while avoiding the multi-resource/operator complexity of task trigger and batch command creation.

### Recommended Acceptance Criteria

- `GET /config/export?include_secrets=true` requires primary admin auth, existing TOTP step-up, and an active system-scoped credential access grant for `(user, role, action="config.export", purpose="config_export")` before `configHandler.Export` loads or serializes secret-bearing records.
- `GET /config/export` without `include_secrets=true` remains unchanged: admin-only, no step-up/grant requirement beyond existing route behavior, and no secret-bearing fields in the response.
- Add a config-export grant request endpoint following the existing config-import pattern: admin-only, step-up-required, bounded TTL, sanitized reason, active self-grant, safe DTO response, no exported payloads or secret-bearing values stored.
- Missing, expired, revoked, denied, wrong-user, wrong-role, wrong-action, or wrong-purpose grants return a machine-readable `CREDENTIAL_GRANT_REQUIRED` denial and do not trigger frontend session-expiry handling.
- Grant request/activation/use/block audit events use only sanitizer-compatible metadata such as `stage`, `operation`, `status`, `ttl_seconds`, `grant_id`, and `self_grant`; config export audit continues to store only booleans/counts and never payload material.
- Frontend grant API/domain mapping adds `config.export` / `config_export`. If a sensitive export UI is exposed, it must request a bounded reason, obtain `ensureStepUpProof()`, request the grant, then call `exportConfig(token, true, proof)` without writing grant ID, reason, status, exported payload, or sensitive export state to `localStorage` or `sessionStorage`.
- Backend tests cover grant request success/denial/expiry/wrong-user/wrong-action/wrong-purpose, route enforcement before handler execution, safe audit metadata, and unchanged safe export behavior.
- Frontend tests cover mapper/API wrapper changes and, if UI is exposed, reason validation, step-up + grant + export flow, sanitized errors, and storage safety.
- No schema migration, SSH CA, Vault/KMS, session recording, command approval/inspection, configurable grant policy engine, or stored-credential model change is introduced in this slice.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md:224-290` — SSH key least-privilege scope and config export/import scope contracts.
- `.trellis/spec/backend/quality-guidelines.md:380-447` — credential-use audit event contracts and sanitizer constraints.
- `.trellis/spec/backend/quality-guidelines.md:451-499` — credential access grant contracts.
- `.trellis/spec/frontend/state-management.md:71-115` — grant prompt state/storage-safety contracts.
- `.trellis/spec/frontend/a11y-guidelines.md:20-49` — labeled inputs, dialog titles, icon hiding, and focus-visible requirements.

### External References

None. This research is repo-internal and does not require external library/API decisions.

## Caveats / Not Found

- No frontend component currently calls `exportConfig(token, true, proof)`; `ConfigExportImport` uses the default safe export path only (`web/src/components/config-export-import.tsx:28-39`). The backend should still protect direct API clients if `include_secrets=true` is requested.
- SSH key export exists, but current code exports public-key/fingerprint-oriented data rather than private keys (`backend/internal/api/handlers/ssh_key_handler.go:729-777`). Its inventory exposure is still sensitive, but its direct credential exfiltration risk is lower than sensitive config export.
- Current grant matching is admin-only (`backend/internal/api/handlers/credential_access_grant.go:304-318`). Grant-enforcing operator-permitted flows such as task manual trigger or batch command creation would need additional role/ownership semantics and are less bounded.
- Snapshot restore is a strong follow-up candidate after sensitive config export because it is admin-only and can use existing `TaskID` grant scoping, but it is asset-mutation focused rather than direct secret export.
