# Child 15 Technical Design

## 1. Design boundary

Child 15 adds an installation/readiness authority in front of the existing
backup-asset graph. It does not replace Repository, Catalog, Search, Content,
Processing, Export, Recovery, Overlay, Retention, or Publication owners.

The live feature gate stays `backup_assets.enabled` with CodeDefault
`false`. This child changes **when** that gate is allowed to become true, and
what Admin sees before it does.

Parent `implement.md` §16 is not the file manifest. Live evidence is
`research/current-main-evidence.md`.

## 2. Core invariants

1. **Readiness before managed admission.** Requesting enablement is not
   enablement. `AdmissionManaged` requires a current passing readiness
   snapshot.
2. **Upgrades stay off.** CodeDefault remains `false`. Existing copies of
   `.env` are unchanged by a template edit. Only a *new* documented template
   may recommend `BACKUP_ASSETS_ENABLED=true`, and the gate still applies.
3. **Inventory never writes Provider bytes.** Classification uses Task,
   Repository, identity, and latch facts already on disk.
4. **Child 14 remains the only import/reconnect/purge path.** Conflicts from
   inventory are records plus links into those Admin flows.
5. **Recovery downgrade-readiness stays Recovery-owned.** GA readiness is a
   distinct command and schema.
6. **Worker is optional.** Missing Worker cannot block core GA. Public
   Worker publication is out of scope.
7. **No private material on public surfaces.** Locators, credentials,
   proofs, grants, tickets, fence tokens, and file contents stay off API,
   UI, audit, logs, and metrics labels.

## 3. Durable schema (`000071_backup_asset_ga`)

`backend/internal/model/backup_asset_migration.go` defines GORM parity
models; paired SQL is authoritative.

### 3.1 `backup_asset_installations`

One row per installation (singleton).

| Field | Contract |
|---|---|
| `id` | opaque 32-hex primary key |
| `class` | `fresh` or `existing` |
| `readiness` | `unknown`, `blocked`, `ready`, `acknowledged` |
| `inventory_digest` | hex digest of the last successful dry-run canonical document |
| `ack_actor_id`, `ack_at` | existing-class only; null on fresh |
| timestamps | UTC |

`acknowledged` is valid only for `existing` after Admin ack of the current
digest. `fresh` may move `ready` → enable without ack. Class is
recomputed on each inventory; it can change `fresh` → `existing` but never
the reverse once any file-backup Task, repository, or managed-history latch
exists.

### 3.2 `backup_asset_inventory_runs`

| Field | Contract |
|---|---|
| `id` | opaque 32-hex |
| `digest` | canonical inventory digest |
| `status` | `running`, `complete`, `failed` |
| `counts_json` | candidate/conflict/unsupported/capability-gap counts only |
| `error_category` | closed category; no raw locator |
| timestamps | UTC |

Rerun with the same inputs yields the same digest. Failed runs do not
promote readiness.

### 3.3 `backup_asset_repository_conflicts`

| Field | Contract |
|---|---|
| `id` | opaque 32-hex |
| `run_id` | inventory run |
| `kind` | closed: `shared_restic_identity`, `task_repository_mismatch`, `capability_gap`, `command_unsupported` |
| `task_ids` / `repository_id` | opaque IDs only |
| `stable_reason_code` | localized key input |

Conflict rows are Admin-only. They are not Catalog truth and do not create
RecoveryPoints.

### 3.4 Down admission

Pristine `000071` down is allowed. Used down fails while any installation
row is `ready`/`acknowledged`, any conflict row exists, or enablement has
ever succeeded with this schema. Do not erase the proof that later
rollbacks need. After use, retain additive schema and roll back
application behavior.

## 4. Inventory owner

New package `backend/internal/backupasset/ga/` (name stable; keep it
small):

- `InventoryService.DryRun(ctx) (InventoryDocument, error)`
- reads Tasks, `TaskRepositoryLink`, Repository identity, publication
  latches
- classifies Command as `command_unsupported`
- never opens Provider bytes, never calls Child 14 import/rebuild/purge
- writes run + conflict rows in one transaction

Shared Restic identity uses Child 3 evidence rules: same non-secret
repository identity consolidates a candidate; ownership stays per producing
Task. Mutable Rsync/Rclone without managed history stay `mutable_head`.

The document is the ack target. Changing Task/repository facts changes the
digest and invalidates a previous existing-install ack.

## 5. Enablement gate

Insert a readiness check **before**
`AdmissionController.TransitionFeature(true)`:

```text
requested true
  -> load installation row
  -> require latest inventory complete
  -> require readiness ready
  -> if class=existing, require ack for current digest
  -> require export root validate + key domains
  -> then existing drain + persist
```

The same predicate is used by settings PUT, settings DELETE-restore,
config import, and startup. If env/DB request `true` but readiness fails,
effective feature stays false, readiness stays `blocked`, and a stable
error/status is recorded. Do not partially switch admission.

Disablement keeps the current drain path. Latches from `000063`/`000064`
remain.

Do not change `CodeDefault`. `.env.deploy` may document a new-install
recommendation only after the gate exists; copied existing env files are
not rewritten by this child.

## 6. Export volume

Add Compose named volume `asset-worker-export-store` (name exact in the
implement manifest) mounted read-write on Core at
`/var/lib/xirang-asset-runtime/export`. `asset-worker-init` mounts the same
volume and `deploy/worker/entrypoint.sh` `initialize_volumes` fixes
mode/owner using the same pattern as derived (`0700` + Core UID `10000`).
Parser and updater must not mount it. Do not change the Worker Dockerfile,
seccomp profile, or image publication.

`scripts/check-compose-config.sh` and `scripts/test-asset-worker.sh` gain
the same mutation/persistence assertions Child 11 added for derived.
Child 12 root validation already rejects `/data`, `/backup`, `/logs`,
content, and derived overlap; keep that guard.

Preview cache stays ephemeral. Derived volume stays as shipped.

## 7. API and UI

Admin-only JSON:

- `POST /api/v1/settings/backup-assets/ga/inventory`
- `GET  /api/v1/settings/backup-assets/ga/readiness`
- `POST /api/v1/settings/backup-assets/ga/acknowledge`

Reuse existing Admin + `backup_repositories:manage`. Do not grant
Operators inventory payloads.

Frontend:

- typed mapper in `web/src/lib/api/`
- Admin panel on backups overview or a focused settings fragment, not
  `CATEGORY_ORDER` dumping every `backup_assets.*` key
- keep `feature_disabled` workspace region; Admin gets a CTA to the panel

Legacy Tasks: after parity tests, replace SnapshotBrowser/Search primary
entry with `BackupAssetsTaskContextLink` (already exists) plus recovery
deep links. Leave handler files in place.

## 8. Observability and docs

Search currently has `NoopMetrics` only. Add Prometheus for that existing
interface (latency/result labels stay forbidden; closed build/scan outcomes
only). Do not invent a parallel search metrics contract.

Add GA-only low-cardinality gauges/counters:

- installation class
- readiness state
- last inventory result (`complete`/`failed`)
- conflict counts by `kind`
- enablement reject reason category
- export-root probe ok/fail

Do not add a new product in `backend/internal/alerting`. GA-critical
signals are metrics plus operator docs: requested-enabled but blocked,
inventory failed, export root unusable. Worker missing stays info.
Repository online/degraded gauges and dedicated mutable-head age gauges
remain named residuals unless an approved amendment pulls them in.

Docs: `docs/admin/backup-recovery.md`, `docs/deployment.md`,
`docs/env-vars.md`. Record export volume. Keep the live contract that the
Worker is unpublished to Docker Hub and GitHub Release. Do not publish
Trellis planning artifacts. README gets a short user-facing GA paragraph
only after the gate exists and matches tests.

## 9. Compatibility and rollback

- Old binaries ignore `000071` tables.
- New binaries with `enabled=true` requested and missing readiness fail
  closed rather than skip the gate.
- Application rollback: set enabled false (already drained). Keep additive
  schema and committed Provider points.
- Never mass-delete new Provider data as rollback.
- Recovery downgrade-readiness remains the Child 13 schema-down fence.

## 10. Testing strategy

- Genuine RED then same-selector GREEN for every implementation task.
- Dual-engine `BackupAssetMigration071` including used-down admission.
- Enablement matrix: settings, import, env, startup × fresh/existing ×
  ready/blocked × ack missing/present.
- Inventory idempotence and no Provider command fixtures.
- Compose export volume persistence and Worker non-mount.
- Frontend mapper, Admin panel a11y, disabled workspace, Tasks redirect.
- Bounded load/security script with an explicit CI scale constant.
- Full parent GA command set at the end, with feature true only in that
  GA configuration.

## 11. Task 11 amendment — Recovery delete-handoff race fixture

Task 11 `-race` proved
`TestManagedRecoveryWorkerResumesSameActiveClaimFromOneShotDeleteHandoff`
can observe `executor.paused` before
`recordDeleteAuthorizationPause`. That is a fixture sync gap, not a GA
inventory/enablement change. The test may wait for the recorded pause
map before offering the exact one-shot handoff. Do not change Recovery
production handoff semantics or Child 13 schema-down readiness.
