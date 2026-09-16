# Task reconciliation — 2026-09-16

## Current continuation: v0.55.15

See [current production evidence and bounded lease-recovery plan](research/v05515-lease-recovery.md). Fresh evidence confirms enabled/acknowledged/FeatureLive and active repositories, but Catalog/Search recovery is blocked by expired-heartbeat active index leases with future absolute deadlines. Acceptance remains open. Reuse this task for the narrowly scoped repair; do not recreate archived children. Node-log P1 has separately passed and existing collectors must be preserved.

This disposition supersedes stale current-state, delivery and execution instructions in this task; old evidence remains historical. User authorized evidence-based archival and consolidation on 2026-09-16.

Retain as the single owner of outstanding real-data production acceptance from ten delivered repair tasks. Current NAS version, enabled state and collectors were not inspected. Rebaseline before any live steps.

This task remains open; assessment does not authorize a production change or imply new implementation has run.

Full inventory and evidence: `.trellis/workspace/weibo/task-reconciliation-2026-09-16.md`. No product code, production configuration or Provider data was changed by this reconciliation. No new task was created.

## Consolidated outstanding acceptance — NOT PASSED

This is the single acceptance owner for the archived repair slices. Existing
implementation tests and GitHub checks remain delivery evidence. These live
criteria remain unchecked; archival of a source task does not satisfy them.

| Sources | Remaining evidence owned here |
|---|---|
| Migration 69, SQLite batch, orphan reconciliation | Current version/revision, clean expected schema and integrity, recoverable backup, actual task quiescence; ordinary supported execution without manual database repair. Historical v0.50.6 orphan acceptance is preserved, not rerun blindly. |
| Catalog race, mutable Search, startup isolation, asynchronous enable | Healthy startup; formal feature state and inventory acknowledgement; current repository/link/point; completed positive Catalog/Search generations; exact-point search and authorized metadata. Do not require old generation IDs or the historical 60,515 count. |
| Preview authorization, HTTP transport | Admin-selected transport policy, role-appropriate control, same-origin authorized delivery ticket and readable content over the intended LAN/HTTPS route. Preserve default rejection and setting-off rollback; use disposable evidence for denial/security probes rather than injecting production failures. |
| Direct file center and subsequent preview work | Representative text without Worker; readable full-height frame; exact 45-minute non-sliding secret-reveal reuse within the same login; logout/expiry invalidation; authorized retained-data source visibility; Up navigation; Retry and current mutable-source consistency. |
| All slices | Bounded final health/error observation, no sensitive evidence, task/collector state recorded and no automatic collector re-enable. |

- [ ] Obtain fresh read-only NAS/version/config/health evidence and identify the
  actual acceptance candidate. `v0.55.13` is the verified public release, not a
  claim that the NAS runs it. Do not apply old v0.50.x recovery commands or schema
  numbers to an unknown current installation.
- [ ] Establish recoverable persistent-volume/database backup and rollback before
  any separately authorized upgrade or setting change. No production mutation
  was performed or inferred as authorized in this bookkeeping session.
- [ ] Verify current feature readiness, including acknowledgement of the current
  inventory digest where required; enabled=true alone is not FeatureLive.
- [ ] Complete the table's current-point metadata, Search, transport and UI
  acceptance on representative authorized data, retaining only sanitized outcomes.
- [ ] Record zero unresolved acceptance findings or a concrete bounded follow-up.
  If any issue remains, keep this task and its program parent open.
- [ ] Only after evidence review, close this acceptance task and then the program.
  Node-log production restoration requires its own fixed release and single-node
  observation. Node-log code planning need not wait on an unrelated live preview.

The archive documents preserve all original local checklists. Exact historical
release/upgrade steps are superseded by the current-candidate preflight above;
they are not retroactively checked off. Optional content products (Export,
Archive, Recovery) retain their existing delivered automated transport coverage;
this consolidation neither enables them nor introduces new live mutations.

## Previous metadata note (historical)

Historical v0.50.2 No-Go remains in research/acceptance-protocol.md. Production core service is healthy on v0.50.6 with backup_assets.enabled temporarily disabled by a guarded one-row emergency CAS after a v0.50.7 Search startup crash loop. Task 3 remains paused/disabled with zero active runs; the online Rsync repository, active task link, observed mutable point, and active complete 60,515-entry Catalog remain intact. Ten Search generations failed before writing any document with stable code search_invalid_security_state. Child 08-26-backup-assets-search-startup-isolation owns the P0 TDD fix for candidate-local startup isolation and the exact Catalog sealed-state interoperability gap. No Catalog/Search/Provider row repair is permitted. Node-log collectors stay disabled and P1 cannot start until real-data metadata/content preview passes.
