# Research: Private-network HTTP content-delivery design review

- Query: Independently review the task PRD, design, implementation plan, context manifests, current-state research, and task metadata against repository code and Trellis specs, with emphasis on private-LAN HTTP completeness, trusted forwarding evidence, issue/serve enforcement, compatibility, dynamic Foundation transition, UI accessibility, log privacy, rollback, and testable acceptance.
- Scope: internal
- Date: 2026-08-27

## Findings

### Critical

None after the latest planning revisions.

### Important

None after the final XFP gateway revision. The planning artifacts now explicitly
overwrite XFP with the all-in-one listener's `$scheme` on exact and shaped
content routes, remove their client-derived map dependency, and require the
runtime probe to observe `http` despite inbound `X-Forwarded-Proto: https`
(`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:71`,
`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:133`,
`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/implement.md:57`).

### Minor

None. The final revision explicitly adds handler/settings/runtime Foundation
selectors to normal and race gates
(`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/implement.md:123`),
names archive-member delivery in both the confirmation and persistent warning
(`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:188`),
and expands the task inventory across Nginx scripts, logging/runtime tests, typed
API/panel tests, and i18n resources
(`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/task.json:23`).

**Verdict: QUALITY_OK.** No unresolved Critical, Important, or Minor planning
finding remains in the reviewed snapshot.

## Resolved planning contracts

The latest artifacts resolve the earlier review gaps:

- Complete delivery scope explicitly includes preview/original download, Export, archive member, and Recovery result issuance (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:60`; `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:113`).
- JSON ticket transport denials have a closed parameter-free reason, with typed frontend handling and role-aware guidance across all five surfaces (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:96`; `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:212`).
- Application logging redaction is now method-independent and covers malformed/trailing/query/unsupported shaped requests (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:153`). This matches the router's explicit unsupported-method and trailing-slash registrations (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/backend/internal/api/router.go:421`) and the logging spec's every-shaped-path contract (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/spec/backend/logging-guidelines.md:109`).
- The exact route adds XFF only; privacy gates cover both Nginx and application logs, and the manifests now inject the logging guideline (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/implement.jsonl:6`; `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/check.jsonl:6`).
- The compatibility AC now separates old-loopback/new-private flags and trusted HTTPS combinations (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:116`). Existing released tests demonstrate why preserving this as an independent path matters (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/backend/internal/api/handlers/backup_content_handler_test.go:442`).
- The Foundation AC covers live issue/Serve visibility and failure rollback, while the Admin AC covers source/loading/saved/warning/deduplication/hash focus/dialog/keyboard/axe states (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:121`; `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:138`).
- A hermetic official-template Nginx runtime RED is now distinct from static/mutation checks (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:162`; `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/implement.md:65`).
- The exact and shaped Nginx content routes now sanitize XFP to their actual
  `$scheme`; AC5/AC7 include the inbound `X-Forwarded-Proto: https` negative, so a
  stale non-`Secure` ticket cannot turn an HTTP Serve into apparent HTTPS after
  the Admin disables the setting
  (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md:133`,
  `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md:135`).

## Acceptance-criterion testability

- AC1–AC13 are expressed as observable products after the revisions; no
  acceptance criterion remains blocked at the planning level.
- AC2 is pinned to explicit handler/settings/backupasset/runtime normal and race
  selectors, in addition to the full backend suite.
- AC6 is now testable at the DOM and API-spy boundaries: accessible names, pending mutation deduplication, live regions, persistent warning, and hash-target focus are all observable. The a11y spec requires a dialog title and whole-body axe scan for portal content (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/spec/frontend/a11y-guidelines.md:32`, `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/.trellis/spec/frontend/a11y-guidelines.md:82`).
- AC7's runtime probe is testable and catches both the missing exact-route XFF
  directive and the released client-derived XFP behavior. Handler tests remain
  separately responsible for issue/Serve policy and zero-service-call ordering.

## Files found

- `.trellis/tasks/08-27-backup-assets-private-network-http-content/prd.md` — product requirements and AC1–AC13.
- `.trellis/tasks/08-27-backup-assets-private-network-http-content/design.md` — setting, policy, Nginx, UI, error, compatibility, and rollout design.
- `.trellis/tasks/08-27-backup-assets-private-network-http-content/implement.md` — RED sequence, implementation slices, verification, delivery, and production acceptance.
- `.trellis/tasks/08-27-backup-assets-private-network-http-content/{implement,check}.jsonl` — implementation/check spec injection manifests.
- `.trellis/tasks/08-27-backup-assets-private-network-http-content/research/current-state.md` — production failure and reusable-boundary evidence.
- `.trellis/tasks/08-27-backup-assets-private-network-http-content/task.json` — task identity, worktree, branch, parent, and related-file inventory.
- `deploy/nginx/templates/default.conf.template` and `scripts/check-asset-content-nginx.sh` — current exact/fallback/generic proxy and static privacy enforcement.
- `backend/internal/api/handlers/backup_content_handler.go` and `backend/internal/api/router.go` — current scheme policy, issue/Serve entrypoints, trusted-proxy injection, and shaped-route registrations.
- `backend/internal/middleware/structured_logger.go` — current safe path shaping plus unconditional `client_ip` emission to be changed.
- `backend/internal/settings/service.go`, `backend/internal/backupasset/service.go`, and `backend/internal/api/handlers/settings_handler.go` — Foundation key set, complete snapshot parser, dynamic mutation, and rollback seams.
- `web/src/pages/backups-page.overview.tsx`, `web/src/components/ui/{switch,inline-alert}.tsx` — current Admin overview mount point and reusable accessible status/control primitives.

## Code patterns

- Forwarding trust is evaluated at the backend's immediate socket peer. The
  released exact gateway currently transforms client XFP before that decision;
  the plan correctly makes the official gateway authoritative by overwriting it
  with `$scheme`, and tests the proxy and handler hops separately.
- Serve currently reaches the content service after browser-request validation (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/backend/internal/api/handlers/backup_content_handler.go:397`). The planned transport/config rejection must occur before delivery-ID/Broker/export-ledger access and be asserted with zero-call spies for GET and HEAD.
- Foundation mutation captures exact raw overrides and passes a restore callback for PUT (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/backend/internal/api/handlers/settings_handler.go:368`). The new key must participate in the complete immutable content snapshot, not be read separately from the UI or handler.
- `StructuredLogger` currently shapes the route and then unconditionally appends `client_ip` (`/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/backend/internal/middleware/structured_logger.go:15`, `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui/backend/internal/middleware/structured_logger.go:25`); the revised method-independent shaped-path branch is the correct implementation boundary.

## External references

None. This review is repository- and spec-backed; no external behavior or version claim was needed.

## Related specs

- `.trellis/spec/backend/deployment-runtime.md:96` — exact/fallback gateway, Host/proto, redacted log, static checker, and official-image contracts.
- `.trellis/spec/backend/logging-guidelines.md:90` — StructuredLogger and every content-shaped path privacy contract.
- `.trellis/spec/backend/quality-guidelines.md:1419` — production Foundation transition, lock ordering, prospective config, exact rollback, and required tests.
- `.trellis/spec/frontend/a11y-guidelines.md:25` — accessible naming, dialog title, focus visibility, live UI testing, and portal axe scan.

## Caveats / Not Found

- The official all-in-one content routes will no longer preserve an outer
  client/upstream XFP value. The plan intentionally treats the official listener's
  actual `$scheme` as authoritative; external TLS that terminates elsewhere must
  use a separately trusted topology rather than client-derived XFP through this
  public HTTP listener.
- No code was changed and no product tests were run. The review evaluates planning completeness and current source contracts only.
- Planning files were being revised concurrently. Anchors and conclusions above refer to the post-revision snapshot inspected on 2026-08-27.
