# Task reconciliation — 2026-09-16

This disposition supersedes stale current-state, delivery and execution instructions in this task; old evidence remains historical. User authorized evidence-based archival and consolidation on 2026-09-16.

File center and repairs delivered through PRs 472, 474, 476, 478; later 480, 492, 502, 507 extend the released path. Final representative product acceptance transferred.

GitHub PR [#478](https://github.com/xiangnan0811/xirang/pull/478) is merged at `380725ca81f05c85ba4e652b9ef417cc1104dd03` and is an ancestor of baseline `1ec1b2b8621fb2ae86b5888247ab49c69c629764`. Its check rollup was inspected live. All 11 returned check runs succeeded.

Unfinished cross-task criteria are explicitly transferred to `.trellis/tasks/08-21-backup-assets-release-acceptance/reconciliation.md`; they are NOT marked passed. Archive means this delivery slice is closed, not production acceptance or completion of the receiving backlog.

Full inventory and evidence: `.trellis/workspace/weibo/task-reconciliation-2026-09-16.md`. No product code, production configuration or Provider data was changed by this reconciliation. No new task was created.

## Previous metadata note (historical)

The original user-approved design shipped in v0.52.0, but production product acceptance on 2026-08-28 failed: core text preview was unavailable and received misleading Worker guidance; secret-reveal TOTP repeated on file/source changes; the Recovery-Point-only projection hid backup-bearing task lineages; and there was no Up navigation. The user selected remediation option A with an exact 45-minute non-sliding asset.secret_reveal proof reusable across files, directories, versions, nodes, and refresh within one login session, then explicitly approved the complete revised PRD/design/plan. Those remediations shipped through v0.52.2 and production proved single-click readable plain-text rendering, but acceptance on 2026-08-29 exposed a new layout failure: frame-based content retained the browser's approximately 150px fallback height inside a much taller preview viewport. The user explicitly authorized a bounded height hotfix. No production asset identity or content is recorded. Collectors and node-log work remain separately gated.
