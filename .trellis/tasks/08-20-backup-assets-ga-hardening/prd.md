# Backup Asset GA Hardening

## Goal

Make the already-built backup-asset explorer generally available for new
installations, and give existing installations an explicit, fail-closed
migration/preflight path, without weakening backup, verification, or
recovery. This child is the program GA cutover, not a new feature plane.

Alan approved this planning summary on 2026-08-20; `task.py start` is
complete. Parent `implement.md` §16 remains superseded by this package
plus `research/current-main-evidence.md`.

## User outcomes

### Administrator

- See a truthful installation class: `fresh` or `existing`.
- Run an idempotent dry-run inventory that maps Restic/Rsync/Rclone Tasks to
  repository candidates, reports identity conflicts and capability gaps, and
  never moves, copies, or deletes Provider bytes.
- Enable backup assets only after dual-engine migrations, required key
  domains, durable export root, and inventory review pass. Worker absence is
  allowed and reported as optional.
- On an existing installation, acknowledge the preflight before enablement.
  There is no silent Provider conversion.
- After deep-link parity, use Tasks for config/schedule/run logs and the
  backup workspace for browse/preview/export/recovery. Legacy snapshot
  dialogs stop being the primary asset UX.

### Operator and viewer

- When the feature is disabled, the backup workspace states that clearly and
  does not invent assets.
- When enabled, keep the Child 1–14 permission model: Admin sees all;
  Operator lists/previews owned lineage and cannot download originals by
  default; Viewer sees health/status only.
- Never see inventory conflict internals, Provider locators, proofs, grants,
  tickets, or raw paths.

## Confirmed facts

- Children 1–14 are archived on `main` at `61cb68d`. Program delivery is
  14/15. `backup_assets.enabled` CodeDefault is `false`.
- Migrations `000062`–`000070` exist; `000071` is free.
- Admin can already persist `backup_assets.enabled=true` after admission
  drain. Nothing checks installation readiness first.
- Worker image, Compose Worker/derived volumes, and no-publish CI already
  exist. The export ciphertext root does not have a Compose named volume.
- `/app/backups/{overview,data,recovery}` exists and can render a
  `feature_disabled` empty state, but the UI never reads the setting and
  System settings hide category `backup_assets`.
- Legacy `SnapshotBrowser` / `SnapshotFileIndex` / Tasks restore routes are
  still live. New search does not use `SnapshotFileIndex`.
- Command tasks remain typed unsupported.
- Child 14 already owns reconnect/import/rebuild/purge. Child 13 owns
  Recovery downgrade-readiness.

## Key decisions

These are closed for this child. They refine the parent GA contract against
live `main`; they do not reopen Children 1–14.

1. **One child, internal waves.** Do not create Child 16+. Split only as
   numbered tasks inside this child.
2. **Do not flip `CodeDefault` to `true`.** A global default true would
   enable existing upgrades that still resolve to CodeDefault. New-install
   enablement is: readiness gate + optional `.env.deploy` template/docs for
   *new* copies + `000071` installation class. Existing installs stay
   disabled until Admin preflight/ack.
3. **Requested enablement is gated.** Settings, env, and config import that
   ask for `true` must fail closed when readiness is incomplete. The process
   must not become admission-managed without the gate.
4. **Inventory is dry-run classification.** Reuse Child 2/14 identity and
   import facts. Do not add a second connect/import/purge owner and do not
   mutate Provider bytes.
5. **Export volume is in; Worker publish is out.** Add durable Compose
   wiring for `/var/lib/xirang-asset-runtime/export`. Keep Worker CI
   no-publish. Core all-in-one GA does not require a public Worker image.
6. **Legacy UI redirects after parity; APIs may remain compatibility.**
   Remove or hide Tasks snapshot-browser callers once deep links cover
   list/search/restore entry. Keep legacy HTTP routes fail-closed and
   lineage-guarded until a documented removal release. Never feed
   `SnapshotFileIndex` into the new search contract.
7. **Load/security proofs are bounded scripts.** CI runs a small explicit
   scale. Million-entry / bomb / restart cases are local/scripted fixtures,
   not an unbounded CI dataset.
8. **Parent stays planning** until this child is complete_checked and the
   parent GA gate has been rerun. Archiving the parent is a later explicit
   step.

## Requirements

### R1. Installation class and persistent readiness

1. Persist installation class `fresh` or `existing`, readiness status, and
   repository-conflict records in paired `000071`.
2. `fresh` requires all of: no file-backup Task (Restic/Rsync/Rclone), no
   `BackupRepository` row, and no managed-history latch/tombstone.
   Otherwise the install is `existing`.
3. Readiness cannot pass unless dual-engine migrations through `000071`,
   required key domains, validated export root, and inventory completion
   succeed. Missing Worker is a reported optional gap, not a hard failure.
4. Down of `000071` is fail-closed once readiness or conflict rows exist
   that later children would need; pristine empty down may apply.

### R2. Dry-run inventory

1. Map existing Restic/Rsync/Rclone Tasks to repository candidates using
   live identity rules.
2. Shared Restic repositories consolidate identity without merging
   cross-task ownership.
3. Mutable mirrors remain `mutable_head`. Legacy `SnapshotFileIndex` is
   never trusted complete.
4. The dry-run prints/stores counts, conflicts, and capability gaps. Rerun
   is idempotent. Provider bytes are unchanged.
5. Command Tasks appear as unsupported with a stable reason.

### R3. Enablement gate

1. `TransitionFeature(true)`, settings PUT, config import, and env-driven
   effective `true` must consult readiness.
2. Incomplete readiness keeps the feature disabled and returns a stable
   code. Admission must not become managed.
3. Existing installs additionally require Admin acknowledgment of the
   current inventory digest. Ack is audited with opaque IDs/counts only.
4. Disablement keeps Child 3/4 managed-history latches. Closing the gate is
   not a downgrade of Provider history.

### R4. Durable export volume

1. Official Compose mounts a dedicated named volume at the export root,
   outside `/data`, `/backup`, `/logs`, content cache, and derived store.
2. Container replacement preserves ready export ciphertext. Child 12
   process restart/takeover tests remain authoritative for jobs; this child
   proves volume durability.
3. Parser/updater must not mount the export volume. `check-compose-config`
   and Worker/core smoke scripts cover the new mount.

### R5. Admin migration UX and disabled workspace

1. Add an Admin-only readiness/inventory panel gated by existing
   `backup_repositories:manage`. Do not dump the entire `backup_assets.*`
   registry into System settings.
2. Data/recovery views stay reachable but show the existing
   `feature_disabled` empty state plus a link to the Admin panel when the
   caller is Admin.
3. Operators/Viewers never see conflict payloads or enable controls.

### R6. Legacy snapshot UX

1. Prove deep-link parity for task context, snapshot-equivalent recovery
   point, path/search, and restore entry into `/app/backups`.
2. After parity, Tasks dialogs stop mounting `SnapshotBrowser` /
   `SnapshotSearch` / legacy restore as the primary asset path; they link
   into the workspace.
3. Legacy HTTP snapshot routes remain gated, lineage-guarded, and unused by
   new UI until a later documented removal.

### R7. Docs, observability, and GA gate

1. Public docs match runtime: default still false in CodeDefault; how new
   vs existing enablement works; export volume; Worker still optional and
   unpublished; DR still needs DB + `DATA_ENCRYPTION_KEY`.
2. Metrics cover readiness-failed-while-requested, inventory conflicts
   (counts only), export-volume probe failures, and Prometheus for the
   existing search `Metrics` interface (currently Noop-only). Reuse Child
   14 retention/publication metrics for purge/offline signals. Do not add a
   new `internal/alerting` product. No path/entry/user labels.
3. Bounded load/security scripts cover pagination/search, Range, concurrent
   preview, export/recovery restart, malformed content, ticket replay, and
   audit redaction at an explicit small scale in CI.
4. Rerun the parent GA command set with the feature enabled in the GA test
   configuration and disabled/default-compatible in migration fixtures.

## Explicit exclusions

- Recreating Worker Dockerfile/entrypoint/seccomp, derived/updater volumes,
  or no-publish CI jobs that already exist.
- Publishing `xirang-asset-worker` to Docker Hub or adding it to
  `publish-images.yml`.
- Changing `backup_assets.enabled` CodeDefault to `true`.
- A second reconnect/import/purge/retention/recovery owner.
- Automatic Provider conversion, fake history, or trusting
  `SnapshotFileIndex` as Catalog truth.
- Dependency/lockfile upgrades except a proven blocking vulnerability in
  this child's own diff.
- Editing `.codex/**` or Child 1–14 historical research.
- Archiving the parent before this child is complete_checked and the GA
  gate has been observed.

## Acceptance criteria

- [ ] `000071` apply/down/used-down pass on SQLite and required PostgreSQL.
- [ ] Fresh vs existing classification fixtures pass; mixed history cannot
      be classified fresh.
- [ ] Inventory dry-run is idempotent, conflict-safe, and byte-preserving.
- [ ] Enablement without readiness fails closed for settings, import, and
      transition; enablement with readiness succeeds only after drain.
- [ ] Existing-install ack is required; fresh install does not require that
      ack but still requires readiness.
- [ ] Compose export volume survives container recreation; Worker processes
      cannot see it.
- [ ] Admin panel and disabled workspace meet typed API, i18n, and a11y
      bars; Viewer/Operator cannot enable.
- [ ] After parity tests, Tasks no longer presents legacy snapshot UX as
      the asset entry; deep links land in `/app/backups`.
- [ ] Public docs/env/compose claims match tests. Worker remains unpublished.
- [ ] Parent GA command set passes in the GA test configuration.
- [ ] `backup_assets.enabled` CodeDefault remains `"false"` on `main` until
      and unless a later approved amendment changes this PRD.

## Open questions

None that block planning. Remaining work is implementation sequencing inside
`implement.md`.
