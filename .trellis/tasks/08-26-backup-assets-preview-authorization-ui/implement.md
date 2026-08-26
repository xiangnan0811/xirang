# Implementation plan

## Phase 1 — Contract and genuine RED

- [x] Re-read this PRD/design, selected Trellis specs, parent acceptance contract, and current workspace/ticket/RBAC code.
- [x] Add production-equivalent list-only Catalog fixtures without changing production code.
- [x] Add exact Admin and Operator preview-eligibility selectors plus Viewer/token/list/content/capability negatives.
- [x] Run the exact selector and record a behavioral RED proving the Load Preview action is hidden only because Catalog preview=false.

## Phase 2 — Minimal implementation

- [x] Change only the workspace native-preview eligibility boundary: authenticated Admin/Operator + Catalog list + content + renderer capability.
- [x] Keep ticket API, backend RBAC, Catalog mapper/DTO, download/export/recover gates, and renderer selection unchanged.
- [x] Run the exact RED to GREEN, then the focused matrix repeatedly.
- [x] Add/update the permanent frontend quality and cross-layer contracts described in design section 6.

## Phase 3 — Trellis implement verification

- [x] Focused workspace tests once and repeated.
- [x] Relevant ticket/error/state and backend RBAC selectors.
- [x] `cd web && env -u NODE_ENV npm run check`.
- [x] Applicable source-boundary/privacy scan, formatting, and `git diff --check`.
- [x] Record exact outputs and any genuine environment blocker in `research/implementation-evidence.md`.

## Phase 4 — Independent Trellis check

- [x] Re-read current artifacts/specs and inspect the final diff independently.
- [x] Self-fix Critical/Important findings inside task scope and rerun affected selectors.
- [x] Verify role/token/list/content/capability negatives, server RBAC, no permission-field fabrication, and no sensitive storage/log/URL changes.
- [x] Record findings-first evidence in `research/check-evidence.md`.

## Phase 5 — PR, CI, merge, and release

- [ ] Commit on `codex/backup-assets-preview-authorization-ui`, push, and open a ready PR against main.
- [ ] Monitor all required CI; fix failures on the same branch and continue until green.
- [ ] Squash merge only when required checks are green.
- [ ] Sync main and monitor Release Please, GitHub Release, Docker image publishing, and any relevant post-merge automation.

## Phase 6 — Production content-preview acceptance

- [ ] Provide a root-safe upgrade protocol using external port 19927 and no shell `test`/`[`/`[[`/`cd`/user-switching commands.
- [ ] Verify image version/revision/digest, DB backup, health, schema, exact active Catalog/Search lineage, task/collector quiescence, and rollback target before upgrade.
- [ ] Upgrade the published stable image and verify running/healthy/restarts0.
- [ ] Open the exact opaque AssetRef route; verify metadata remains visible and Load Preview is enabled.
- [ ] If required, user completes step-up only in the product UI; verify a supported renderer displays real content without recording it.
- [ ] Record HTTP/status/opaque evidence, health/error window, and collectors=0 in the parent acceptance evidence.
- [ ] Only after all acceptance checks pass, mark this child complete and start the separately approved node-log P1 task.
