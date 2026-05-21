# Select next P3/P4 hardening slice

## Goal

从剩余 P3/P4 control-plane hardening 路线中选取下一个可执行、边界清晰、能复用现有安全原语的加固切片，为后续实施任务提供明确范围。

## Current State

- P1/P2 已完成：SSH key least-privilege、credential audit、risk summary、TOTP step-up、terminal JIT grant。
- 已发布的 P3 切片：
  - `config.import` / `config_import` JIT grant。
  - `config.export` / `config_export` JIT grant for `include_secrets=true`。
  - `tasks/batch-trigger` all-blocked/no-op sanitized telemetry。
- 剩余候选包括 SSH key export、task manual trigger/restore、snapshot restore、batch command creation、policy-driven grants、grant status/list UI，以及 P4 架构项。

## Decision

**选择下一个可执行 P3 切片：snapshot restore 任务级 JIT grant。**

推荐实现形态：对 `POST /tasks/:id/snapshots/:sid/restore` 增加短时、row-backed、任务级 `CredentialAccessGrant` 校验，授权元组为：

- `action`: `snapshot.restore`
- `purpose`: `snapshot`
- `task_id`: 路由中的 `:id`

该 grant 仍然是 additive control：不替代主认证、admin role、TOTP step-up、现有 request validation、restore target guardrail、credential audit 或 REST audit。

## Why This Slice

- **安全收益高**：snapshot restore 是远端文件系统写入/覆盖类操作，控制面破坏半径高于普通只读查询。
- **边界清晰**：当前路由已是 admin-only 且已有 step-up，可在现有顺序中追加 grant gate。
- **模型可复用**：`CredentialAccessGrant` 已支持 `TaskID` 精确匹配，无需 migration。
- **风险低于更大候选**：batch command / task manual trigger 牵涉 operator ownership、多资源集合和命令文本策略；SSH key export 当前主要导出 public-key/fingerprint inventory；P4 项需要架构设计。
- **自然延续 P3**：在 config import/export 后，把 JIT grant 从“敏感配置读写”推进到“高风险恢复写入”。

## Recommended Requirements for Implementation

1. Backend adds snapshot restore grant constants and request endpoint, likely `POST /credential-access-grants/snapshot-restore`.
2. Grant request requires admin primary auth, valid step-up proof, non-empty local reason, bounded TTL, and a valid restic task ID.
3. Snapshot restore route enforces, in order:
   - primary auth
   - admin role
   - TOTP step-up for `snapshot.restore`
   - active matching grant for `(requester_user_id, requester_role, action=snapshot.restore, purpose=snapshot, task_id=:id)`
   - existing snapshot restore handler validation/execution
4. Missing, expired, revoked, denied, wrong-user, wrong-role, wrong-action, wrong-purpose, or wrong-task grants fail closed with the existing `CREDENTIAL_GRANT_REQUIRED` envelope.
5. Grant denial happens before restore executor invocation and before any remote restore side effect.
6. Successful grant use writes sanitized credential audit metadata only; no snapshot file contents, target contents, hostnames, command output, credentials, step-up proofs, or exported/imported payloads are logged or persisted.
7. Frontend snapshot restore flow collects a local-only reason, obtains step-up proof, requests the task-scoped grant, then invokes restore with the same proof.
8. Frontend does not store grant ID/material/reason/status, snapshot file lists, include paths, target path, proofs, or restore payload in `localStorage` or `sessionStorage`.
9. Existing snapshot list/browse behavior remains unchanged and does not require a JIT grant.

## Acceptance Criteria

- [ ] Admin can request a snapshot restore grant for one restic task with reason and TTL after step-up.
- [ ] `POST /tasks/:id/snapshots/:sid/restore` without a matching active grant returns `CREDENTIAL_GRANT_REQUIRED` and does not call the restore handler/executor.
- [ ] A valid grant for the same user, role, action, purpose, and task ID allows the existing restore path to proceed.
- [ ] Grants for a different task, user, role, action, purpose, or terminal/config operation do not authorize snapshot restore.
- [ ] Expired, revoked, denied, or otherwise inactive grants do not authorize snapshot restore.
- [ ] Snapshot browsing/listing remains unchanged and does not require a grant.
- [ ] Backend tests cover success, denial matrix, route-before-handler execution, and sanitized audit behavior.
- [ ] Frontend tests cover reason validation, step-up + grant + restore ordering, safe error rendering, and no browser-storage persistence.
- [ ] User-facing security docs mention snapshot restore as a high-risk temporary authorization surface.

## Out of Scope

- Per-snapshot-ID, include-path, or target-path grant binding; current slice is task-scoped only.
- Reworking snapshot restore to create `TaskRun`, use task manager restore locking, or add progress UI.
- Changing SSH key purpose taxonomy to split read-only snapshot browse from destructive snapshot restore.
- Adding grant requirements to task manual trigger, task restore, batch trigger, or batch command creation.
- Adding grant list/status/admin approval UI.
- P4 architecture work: SSH CA, Vault/KMS, terminal/session recording, command-level approval, WebAuthn/passkeys, device trust, configurable policy UI.

## Implementation Plan

1. Backend grant plumbing:
   - Add snapshot restore grant constants, request DTO, request handler, operation label, and route.
   - Reuse existing grant validation, TTL bounds, reason sanitization, and active self-grant creation helpers.
2. Backend enforcement:
   - Add a task-scoped grant middleware/helper for snapshot restore.
   - Insert it after existing `RequireStepUp` and before the snapshot restore handler.
3. Backend tests:
   - Cover grant creation validation and exact task-scoped matching.
   - Cover missing/mismatched/inactive grants failing before handler execution.
   - Cover successful active grant use and sanitized audit records.
4. Frontend API/types:
   - Add action/purpose mapping and `requestSnapshotRestoreCredentialGrant` wrapper.
5. Frontend UX/tests:
   - Add a reason prompt to snapshot restore.
   - Ensure step-up → grant → restore ordering.
   - Assert no sensitive/grant/restore material enters browser storage.
6. Docs/checks:
   - Update lean admin/backend security docs.
   - Run targeted backend/frontend tests, full backend test, full frontend check, doc freshness, `git diff --check`, and Trellis context validation.

## Technical Notes

- This selection derives from archived P3/P4 roadmap and follow-up research: sensitive config import/export grants are complete; snapshot restore was previously identified as the strongest bounded remaining P3 candidate.
- The current grant model already supports `TaskID`, which keeps this slice migration-free.
- Using grant purpose `snapshot` preserves existing SSH key purpose semantics; separating read-only snapshot browse from destructive restore should be considered a later, broader compatibility decision.
