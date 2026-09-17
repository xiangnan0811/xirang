# Production final-acceptance protocol (Child 18)

This record contains only evidence reported from Alan's Core-only production host. The v0.50.2 walkthrough failed before backup-assets enablement. No unexecuted feature check is treated as passed, and the parent archive gate remains closed.

Child 17's protocol remains historical and `not_executed`. This file is the authority for the failed v0.50.2 production attempt and the next release-acceptance gate.

## Binding

| Field | Value |
|---|---|
| Child | `08-21-backup-assets-release-acceptance` |
| Git SHA | `9f059b0b3283825b41462c76ea42259a2d9ab9dc` |
| GitHub Release | [v0.50.2](https://github.com/xiangnan0811/xirang/releases/tag/v0.50.2) |
| Image | `docker.io/linnea7171/xirang:v0.50.2` |
| Image digest | `sha256:baa59842fad12e920eb64a2b913191d7f717e630bb5bcafdbaec5f09c634d88b` |
| DB engine | `sqlite` |
| Provider mode | `core-only` |
| Restic | `declared_supported_not_executed` |
| Rsync | `declared_supported_not_executed` |
| Rclone Portable | `declared_supported_not_executed` |
| Rclone Native AWS | `excluded_this_ga` |
| Browser | `not_reported` |
| Acceptor | Alan |
| Recorder | weibo |
| `production_walkthrough` | `failed_pre_enable_migration` |
| `slo_rules_installed` | `not_executed_pre_enable` |
| `backup_cycle` | `not_started` |
| `docs_exclusion` | `verified` — `docs/admin/backup-recovery.md:62`; `docs/admin/backup-assets-load.md:23` |

## Result

**No-Go for v0.50.2 production acceptance.** The released image failed while migrating a clean v61 SQLite backup. Backup assets remained disabled; dry-run inventory, Admin ack, FeatureLive, Catalog / Search / Content, role-bound leftover reads, secret reveal, preview renewal, in-place recovery, feature-disable rollback, and backup-cycle observation were not executed.

The host was restored to v0.44.8 and the clean v61 pre-upgrade backup. At the final 2026-08-23 20:33:53 +08:00 observation, the container was running and healthy with `/healthz` OK, schema `61|0`, zero unsettled task runs, and no critical post-restart log match. This successful host recovery does not convert the v0.50.2 walkthrough into a pass.

## Confirmed failure chain

1. The pre-upgrade SQLite backup passed checksum verification and `PRAGMA integrity_check`, with `schema_migrations=61|0`.
2. Migration 69 added `task_runs.node_id_snapshot`, backfilled it only through the current `tasks` table, then rejected remaining null or non-positive values.
3. The production database contained 848 retained `task_runs` rows for a task that no longer existed. The corresponding node and task had been deleted together through the supported node-delete workflow; historical daily backups confirmed the task's last known positive node ID and the deletion window.
4. Migration 69 therefore failed its backfill guard and wrote a dirty migration state.
5. The instance-local `ALLOW_DIRTY_STARTUP=true` escape hatch then forced the dirty migration record clean. Migrations 70 and 71 ran while migration 69's remaining tables were absent, so the database reported `71|0` despite an incomplete recovery schema.
6. The v0.50.2 process became healthy at the container level but repeatedly logged missing recovery tables and node-write-admission failures. It was not accepted.
7. The escape hatch was removed before rollback. It must not be used for another attempt.

## Must-pass checks

- [x] Official CodeDefault and `.env.deploy` are still false in the repository / release image
- [ ] Dry-run inventory recorded; Admin ack recorded; process restart recorded if still required
- [ ] After enable: FeatureLive true; Catalog / Search / Content agree on this Core-only host
- [ ] Viewer leftover snapshot reads are 410; Viewer cannot restore
- [ ] Operator leftover snapshot reads are 410; Operator cannot restore
- [ ] Admin leftover snapshot reads are 410
- [ ] Secret reveal is Admin-only; Operator UI has no retry
- [ ] Preview renew does not re-prompt TOTP in the same session
- [ ] In-place recovery confirm works; disable returns FeatureLive false
- [ ] Worker remains unpublished; Core-only path works without Worker
- [x] Native AWS binding was not opened
- [x] Failures / waivers listed below with owner

Unchecked items were not executed and are not waivers.

## Failures / waivers

| ID | Result | Waiver? | Owner |
|---|---|---|---|
| `F-MIGRATION-69-ORPHAN-RUNS` | v0.50.2 cannot migrate retained runs whose task was removed by the supported node-delete workflow | No; release blocker | weibo |
| `F-DIRTY-STARTUP-AUTOCLEAN` | `ALLOW_DIRTY_STARTUP` converted a real dirty migration into apparent clean state and allowed later migrations over an incomplete schema | No; fail-closed repair required | weibo |
| `F-PRODUCTION-WALKTHROUGH` | Backup-assets walkthrough stopped before inventory and enablement | No; rerun only on a fixed release | Alan / weibo |
| `W-SLO-NOT-INSTALLED` | SLO PromQL was not installed because the attempt failed before enablement | Not a pass; execute or formally waive on the next attempt | Alan |

## Recovery and forensic evidence

- The stopped failed-upgrade database was preserved both as a raw DB/WAL/SHM set and as a logical SQLite copy; neither was deleted.
- The clean pre-upgrade backup was restored only after checksum, integrity, and migration-version verification.
- The rollback image is `docker.io/linnea7171/xirang:v0.44.8`, image ID `sha256:9696f896bc70df1b535acf8cc9740daa677d8a9be25dc9fb6c52290905957ffe`.
- Final rollback state: container `running/healthy`, `/healthz={"status":"ok"}`, schema `61|0`, zero unsettled runs, `ALLOW_DIRTY_STARTUP` absent.
- Alan manually logged out and successfully logged back in; previously visited pages and the fresh authenticated session behaved normally.
- A separate pre-existing node-log collector stall was found during observation. Collection was intentionally disabled on the affected instance, the container was restarted, and a longer-than-queue-fill observation reported zero queue-full, fetch-failure, or critical matches. That defect belongs to a separate repair task and is not a backup-assets pass condition.

## Rollback disposition

- [x] Backup assets remained disabled; no instance enablement needed reversal
- [x] Failed v0.50.2 state preserved for forensics
- [x] Restored v0.44.8 and clean schema v61 backup
- [x] Removed `ALLOW_DIRTY_STARTUP`
- [x] Confirmed healthy container, successful login, clean schema, and resumed task scheduling
- [ ] Confirm no Worker image was pulled from Docker Hub as official

## Next gate

Do not retry v0.50.2. A new release must first repair migration compatibility for retained orphan runs and make dirty startup fail closed. Only a newly bound release/digest may repeat this protocol. The parent task remains `planning`; parent archive is forbidden until a future production walkthrough passes and Alan separately authorizes archive.
