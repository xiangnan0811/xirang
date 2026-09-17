# Fresh-session implementation handoff

## User authorization and execution boundary

- The user explicitly approved `prd.md`, `design.md`, and `implement.md` on 2026-08-26.
- Continue in a fresh Codex task because the originating task is intentionally ending before implementation.
- Do not reopen scope discovery or ask for task-creation consent: this P0 child task and its dedicated branch/worktree already exist and are approved.
- Run `trellis-start` / `trellis-continue`, then start the existing child task and follow the curated `implement.jsonl` context.
- Product code has not been changed. The first product action must be the Phase 1 production-equivalent behavioral RED.

## Active task and git state

- Task: `.trellis/tasks/08-26-backup-assets-preview-authorization-ui`
- Parent: `.trellis/tasks/08-21-backup-assets-release-acceptance`
- Branch: `codex/backup-assets-preview-authorization-ui`
- Worktree: `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui`
- Base: current `origin/main` at the v0.50.9 release commit when the worktree was created.
- Current changes are planning artifacts plus the parent child-task link only; do not discard them.

## Production state already accepted

- Stable production image: `linnea7171/xirang:v0.50.9`.
- Revision: `59c42319f09231be022b93103d987103eb8d4e58`.
- Published digest recorded by acceptance: `sha256:d7deae9b86262893c6386233105f92155b3a5cac1b7bf31fa5a6746722fd9793`.
- Container was running/healthy with restart count 0; healthz 200; SQLite schema `72|0`; integrity `ok`.
- `backup_assets.enabled=true`; Task 3 is canceled/disabled; global active task runs 0; node-log collectors 0.
- Repository is online with active access and active task link.
- Active Catalog generation 50 is complete with 60,515 persisted entries.
- Active Search generation 12 is complete with 60,515 persisted documents and exact Catalog 50/source lineage.
- A narrow exact query returned one real opaque file AssetRef with complete/fresh coverage.
- Production UI selection and real metadata display passed. Do not record the asset name, path, or content.
- Content preview did not pass: the Preview tab showed “内容操作不可用”.

## Proven root cause

- `BackupAssetsWorkspace.canPreview` currently requires `selectedCatalog.permissions.preview`.
- Backend Catalog repository/status projections intentionally emit list-only permissions, so production has `list=true`, `preview=false`, `download=false`.
- Parent PRD R3 explicitly says Catalog is list-only and native content preview authorization is independently accepted through the delivery-ticket/UI path.
- The formal ticket route independently enforces `RBAC(backup_assets:preview)`.
- Current closed role matrix: Admin and Operator own list+preview; Viewer/unknown own neither. Existing backend RBAC tests cover the route.
- The selected production asset can use the supported `metadata_hex` fallback when MIME is absent; it needs sequential read, which the recovery point exposes. Renderer selection is not the blocker.

## Approved correction

Use the selected design approach A only:

- UI preview eligibility requires token + Admin/Operator + Catalog list + content available + exact renderer sequential/range capability.
- Ignore Catalog `permissions.preview` for native ticket eligibility; do not fabricate or rewrite it.
- Server ticket RBAC remains final authority; existing safe 401/403/capability/secret-reveal mapping remains fail-closed.
- Admin secret/unknown preview may use the existing in-product step-up. Operator must not acquire Admin step-up behavior.
- Keep download/export/recover gating, backend API/schema/RBAC, Catalog data, renderer selection, and production rows out of scope.

## Required delivery sequence

1. Start the approved existing task in this worktree.
2. Dispatch Trellis implement by default.
3. Obtain a genuine RED using a production-like Catalog fixture with `list=true/preview=false`.
4. Apply the minimal workspace eligibility change and permanent spec update.
5. Run focused/repeated/full frontend gates plus the backend RBAC selector and privacy/diff checks.
6. Dispatch independent Trellis check and self-fix in scope.
7. Commit, push, create PR, monitor all required CI, fix failures on the same branch, and merge only when green.
8. Monitor Release Please, GitHub Release, Docker publishing, and relevant post-merge automation.
9. Provide production upgrade/acceptance commands using external port 19927 and container-internal port 10761.
10. Complete exact real content preview acceptance without recording secrets, names, paths, or content.
11. Only then start the already approved node-log P1 task; collectors remain disabled until then.

## NAS command constraints

- The user runs commands as root in `/volume2/docker/xirang`.
- Do not use shell commands named `test`, `[`, or `[[` in command blocks supplied to the NAS.
- Do not use `cd`, `su`, or `sudo`; do not change the user's identity or working directory.
- External HTTP port is 19927; container health target remains internal 10761 where applicable.
- Every production write needs read-only preflight, exact target, failure stop/rollback, and acceptance.
- Never request or print tokens, passwords, TOTP, proofs, Provider locators, backup paths, asset names, or content.

## Known non-blockers not to revisit

- Broad `type=file` searches can hit the Search service 10k resource limit; exact/narrow search already proves real Search data.
- Catalog generation 51 was reconciled to `failed/catalog_build_abandoned`; active Catalog 50 remains correct.
- One Catalog lease row remains status-active but is logically non-live because heartbeat/short expiry are stale; no building generation exists. Do not delete or repair it for this task.
- Do not redo earlier v0.50.4-v0.50.9 P0 fixes or reopen node-log collectors.
