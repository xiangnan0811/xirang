# Child 12 Current-Main Evidence

## 0. Evidence Boundary

This file records read-only Phase 1 research for
`.trellis/tasks/07-22-backup-assets-export-archive`. It supports the focused
PRD/design/implementation plan; it is not product, migration, test, CI or
delivery evidence.

```text
evidence date/timezone:    2026-07-22 Asia/Shanghai
worktree:                 /home/murray/.codex/worktrees/7f06/xirang
branch:                   codex/backup-assets-export-archive
required merged baseline: 9ad2893c714c82781461f452030c25e0766eedd4
HEAD:                     9ad2893c714c82781461f452030c25e0766eedd4
main:                     9ad2893c714c82781461f452030c25e0766eedd4
origin/main:              9ad2893c714c82781461f452030c25e0766eedd4
merge-base:               9ad2893c714c82781461f452030c25e0766eedd4
task status:              in_progress (task.py start executed once on 2026-07-22)
parent status:            planning
delivered program state:  11/15
planning approval:        complete_approved (2026-07-22 controller user reply "批准")
workflow transition:      complete_approved (same approval + user clarification)
implementation approval: complete_approved (same approval + user clarification)
task.py start approval:   complete_approved (same approval + user clarification)
task.py start:            executed once on 2026-07-22
```

No product test, migration apply/down, live PostgreSQL, frontend gate, server,
browser, Docker, stage, commit, archive, journal, push, PR, CI, merge, release
or deployment command was run for this evidence. Source inspection below does
not claim that future Child 12 behavior passes.

## 1. Workflow, Guidance And Baseline

### 1.1 Required Inputs Loaded

Before planning, the session loaded `using-superpowers`, `trellis-start`,
`trellis-brainstorm`, `writing-plans`, `.trellis/workflow.md`, repository
`AGENTS.md`, and the authoritative backend/frontend/guides specs for directory
structure, database, errors, logging, quality, deployment runtime, components,
hooks, state, type safety, accessibility, branch workflow, reuse, cross-layer
thinking and documentation truth.

Focused parent review covered design §§9, 12-13, 17.3-17.4, 18, 20 and
implementation Child 12 plus §§17-20. Archived Children 7-11 planning and
delivery evidence were inspected to distinguish merged ports from future
intentions.

### 1.2 Fresh Git And Branch Proof

A fresh `git fetch origin --prune` completed before file-changing work. The
worktree was clean and HEAD/main/origin/main/merge-base all remained the
required Child 11 squash merge. There was no baseline drift or local-only main
commit. The dedicated branch was created/switched before task creation:

```text
codex/backup-assets-export-archive
```

No fetch result was used to merge/rebase another child and no remote mutation
followed.

### 1.3 Child Registration

The controller-authorized command ran exactly once:

```bash
python3 ./.trellis/scripts/task.py create "备份资产导出与归档" \
  --slug backup-assets-export-archive \
  --parent 07-12-backup-data-explorer-design
```

It created `.trellis/tasks/07-22-backup-assets-export-archive`, added that ID
once to the parent, and left both statuses `planning`. The generator initially
wrote `branch=null` and put the work branch in `base_branch`; Phase 1 metadata
was corrected to branch `codex/backup-assets-export-archive`, base `main`.

The parent now has 12 instantiated children, but only Children 1-11 are merged;
program delivery remains 11/15. Parent archival would be false.

## 2. Parent Contract And Focused Corrections

Parent design §12 freezes selection once, lists closed job/item states,
requires attempt/lease/fence/checkpoint, ZIP/TAR path rules, streaming limits,
per-export DEK/independent KEK, restart, absolute 24h/source-capped TTL and
revoke/key-delete/ciphertext-cleanup ordering. §13 requires bounded archive
index, malicious archive failure and member extraction bound to an outer
fingerprint plus member chain, with original-download or controlled-recovery
fallback for encrypted, unsupported and over-limit outcomes, including suspected
ratio bombs.

Parent design §9 already defines Admin-only `backup_assets:export`, distinct
`asset.export_create`/`asset.export_download`, typed audit actions and forbidden
raw path/query/member/ticket/JWT/locator logging. §§17.3-17.4 define create,
status, cancel and download-ticket with `202 + Location`, opaque DTOs and
idempotency. §§18/20 define settings/root/metrics and fail-closed errors.

Current main and closure review prove thirteen focused corrections:

1. Parent Child 12 Step 11 invokes `BackupAssetMigration067`, but merged main
   already uses paired 067 for Processing. The real selector is 068.
2. Parent lists step-up and credential-grant handlers. Exact export purposes
   already exist, and Export reads through Content rather than credentials.
3. Parent implies a chain, but Child 11/current Derived input has only a safe
   one-source, one-member contract. This plan uses a versioned one-hop array and
   rejects nested retrieval.
4. Parent does not account for 000066's closed single-asset delivery schema.
   Export needs independent 000068 rows while reusing route/ticket/Range code.
5. Browser reload can use one opaque job ID in existing route state without
   persisting selection/proof/ticket or adding navigation/history API.
6. Parent requires source leases through job termination/expiry. Current
   Foundation `AcquireTx` can make create-time protection atomic; releasing at
   ready or relying on source FKs would violate that contract. Parent implement
   Child 12 Step 9 also retains an older release-before-key-delete shorthand;
   focused correction 6 keeps the safer Child contract: fence/revoke/drain,
   destroy wrapped DEK/selection references, then release source leases/non-store
   reservations before physical cleanup/store release.
7. A direct ZIP/TAR writer cannot remove a member after read-after drift. An
   encrypted per-item spool is required to preserve honest partial results.
8. Current Content issue/Derived contracts cannot identify one archive-member
   request/artifact. Exact member delivery must use a strict 000068 resource
   binding, not the old Broker fallback.
9. Current TLS helper does not verify XFP's proxy peer. Shared trusted-proxy
   enforcement is required before the generic content route is reused.
10. Restart/partial/quota/idempotency truth requires explicit job-key,
    item-attempt, quota reservation and member receipt fields. Frontend current
    ports also make saved-search Export API-only and pre-create totals estimates.
11. `LeaseService.resolveAcquireDeadlineTx` treats zero deadlines as holder-
    isolated fresh acquisitions, but an explicit deadline compares with the same
    RecoveryPoint's latest historical lease regardless of holder/owner. Export
    must acquire with zero, persist each returned deadline and cap its lifecycle;
    `lease.go` and its tests stay unchanged.
12. Provider `CatalogRecord`, Catalog model/DTO and Content `SourceStat` expose no
    link target. Target-bearing output cannot be proved; every symlink/hardlink
    must be `skipped/link_metadata_unavailable` without byte read or inference.
13. Parent §13 fallback is only implementable here as the existing authorized
    Child 8 original Content download with fresh `asset.download`. The current
    Worker persists any capability `ErrInputLimit` only as generic
    `ProcessingErrorInputTooLarge`; ratio-bomb fallback must map that real stored
    code to the same closed limit product rather than depend on non-persisted
    `ReasonArchiveRatioLimit`. Controlled recovery stays capability-gated until
    Child 13, and denied/offline/unavailable states need one no-leak reason.

These are approval-surface corrections, not implementation facts.

### 2.1 Parent Coverage Matrix

| Parent contract | Focused coverage |
|---|---|
| design §9 permission, step-up, audit | PRD §§4.7-4.8; design §§8-10, 12 |
| design §12 frozen/durable encrypted export | PRD §§4.1-4.5; design §§3-8 |
| design §13 restricted archive + fallback | PRD §§4.6, 4.8; design §§9, 12-13; implement Steps 7, 9 |
| design §§17.3-17.4 API/DTO/idempotency | design §§9-10, 12; implement Steps 6, 8-9 |
| design §18 settings/root/metrics | design §11; implement Steps 4-5, 8 |
| design §20 fail-closed/degradation | design §13; implement §§5, 7 |
| implement §17 requirement ownership | PRD acceptance and design §§1, 15 |
| implement §18 validation | implement Steps 1-10 and §§4-6 |
| implement §19 high-risk reviews | design §15 and explicit pauses in Steps 4, 6-8 |
| implement §20 rollback | design §13; implement §7 |

Scope deviations are limited to the thirteen corrections above. Recovery,
Lifecycle, GA/deploy/release/publication and reserved migrations remain assigned
to later children.

## 3. Migration, Model, Lease And Key Evidence

### 3.1 Migration Head And Reservation

Read-only enumeration found paired migrations through:

```text
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/sqlite/000067_backup_asset_processing.down.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.up.sql
backend/internal/database/migrations/postgres/000067_backup_asset_processing.down.sql
```

No 000068, 000069, 000070 or 000071 file exists for either engine. Child 12
owns only paired 068; 069 Recovery, 070 Lifecycle and 071 GA remain reserved.

The migrator embeds both engine directories. Integration tests expose 062-067
SQLite/PostgreSQL contracts; migration and Processing helpers only fail on a
missing DSN when `REQUIRE_POSTGRES_MIGRATION_TEST=1` and
`REQUIRE_POSTGRES_PROCESSING_TEST=1` respectively. CI sets both required envs
for its PostgreSQL 18 job and selects
`BackupAssetMigration0(62|63|64|65|66|67)Postgres` plus Catalog/Search/Overlay/
Content/Processing behavior. Future 068 must extend this exact job with Export
behavior and a new fail-closed `REQUIRE_POSTGRES_EXPORT_TEST=1` helper, not add a
publish workflow or accept skip as evidence.

`database.go`'s PostgreSQL connector already registers the configured
`ScanLocation` independently on pgx `timestamp` and `timestamptz` codecs. Child
12 therefore needs required 068 regression coverage for both scan types, not a
connector edit or a skipped parity row.

### 3.2 Existing Lease Contract

- `backend/internal/backupasset/domain.go` defines
  `LeaseHolderExportJob = "export_job"`.
- paired 000062 already permits `export_job` in both lease-holder CHECKs.
- `backupasset/lease.go` validates Export holders and supplies acquire, renew,
  release, stale takeover, absolute deadline and `ValidateFenceTx`.
- `resolveAcquireDeadlineTx` returns `now + configured AbsoluteDeadline` when
  the request deadline is zero. For a non-zero request it locks the most recent
  lease by `recovery_point_id` only, including released or different-holder rows,
  and requires exact equality with that historical deadline; a conflict/expired
  result is therefore possible before Export runs.

Therefore 068 must reference the existing contract and every Export `AcquireTx`
must pass zero `AbsoluteDeadline`, persist the exact returned value, and derive
execution/access/artifact caps from those values plus non-null RecoveryPoint
`RetentionUntil` and frozen limits. Renew/takeover preserves that value and must
never reacquire to extend it. Export compatibility regressions belong in the new
Export package; it must not edit 000062, `lease.go` or `lease_test.go`, or claim
to add the holder. No current Export service actually acquires a lease.

### 3.3 Missing Export Models And Key Domain

Current models cover Foundation, Catalog, Search, Overlay, Content, Processing
and audit. There is no `backup_asset_export.go`, export table/service/worker/GC,
Export root or delivery ledger. The audit model already has `export_job_id`.

The keyring currently defines Entry Identity, Cursor Signing, Audit Fingerprint,
Recovery Cleanup Ownership, Search Token and Derived Store. Boot-required
domains intentionally exclude Search/Derived. There is no `export_store`.

000067 extends `wrapped_domain_keys` to `derived_store`, and its down guard
refuses used state before restoring the prior CHECK. 068 must follow that
pattern: add only `export_store`, ensure it on demand, invalidate exports on
loss, and restore the full 067 CHECK on pristine down.

Because summary/GC can eventually purge all ordinary Export/member rows, row-
emptiness alone cannot prove a schema was never used. The focused thirteen-table
design therefore reserves the permanent global quota-bucket use latch in the
000068 singleton: first successful Export/member write ensures it and no cleanup path
deletes it. Pristine has no row; used and purge-to-empty remain permanently
blocked from down without a fourteenth table.

## 4. Content Source, Ticket And Delivery Evidence

### 4.1 Exact Source And Attempt Ports

`content/source_contracts.go` defines exact `AssetRef`, Catalog generation,
expected source/entry fingerprints, stat/sequential/range modes, byte limits,
`SourceSession.Revalidate/Close` and `SourceReader.ProviderBytes()`.

Repository `content_read.go` is the production resolver and keeps Provider
locator/config/admission/native reader private. Command Provider remains typed
`task_artifact_contract_missing`.

The production resolver rejects anything except `CatalogEntryFile`; directory,
symlink, hardlink and special metadata cannot be passed through this byte port.
Focused Export therefore uses `AttemptBroker` only for regular files and needs
an exact Catalog/source metadata-revalidation adapter for every non-file item.

Read-only type inspection also proves `provider.CatalogRecord`,
`model.CatalogEntry`, `catalog.EntryDTO` and `content.SourceStat` have no link-
target field. Child 12 cannot safely produce any target-bearing member.
Every symlink/hardlink is therefore `skipped/link_metadata_unavailable`; tests
must prove no target inference, Provider read or Provider mutation across all
three readable Providers. Directory output may remain an empty directory.

`content/attempt_broker.go` adds exact attempt binding, allowed modes, absolute
expiry, request/cumulative/in-flight budgets, conservative finalization,
revalidation and reader closure. Export can supply its own budget behind this
narrow port; it needs no Repository/Provider import.

### 4.2 Ticket, Range And Closed 000066 Schema

`content/ticket.go` exports CSPRNG ticket material, hash-only verification,
deadline resolution, exact asset-content cookie path, HttpOnly/Secure/
SameSite policy and strict parsing. `content/range.go` exports GET/HEAD
single-Range planning, If-Range, safe integer parsing, limits and stable errors.

The existing frontend `prepareDownload` path obtains
`STEP_UP_ACTIONS.assetDownload` with `persist:false/reuseCached:false` and issues
the typed `download/attachment/original_v1` request through
`backup-content-api.ts`; workspace gating already combines download permission,
Content availability and RecoveryPoint download capability. That is the only
Child 12 archive fallback available without a new path. It can be delegated by
the planned archive panel; denied/offline/unavailable remains closed, while
controlled recovery has no current Child 12 capability.

Both 000066 engines require one `backup_asset` resource with non-null RP,
Catalog generation and entry; action `preview|download`; Provider
`restic|rsync|rclone`; one content lease/fence; and closed renderer/profile/
classification/source fields. A multi-RP Export has no honest singular value
for those fields.

The focused design therefore adds Export-owned delivery grants/requests in 068
and a typed resolver mux behind the same generic content route. It reuses
primitives/middleware without weakening old SQL/model behavior.

`content.BudgetService` is not one of those generic primitives: it concretely
loads and updates 000066 `BackupAssetDeliveryUsage`, `BackupAssetDeliveryGrant`
and `BackupAssetDeliveryRequest`. Export delivery must own 000068 transaction/
CAS reserve/finalize/replay/crash accounting in `export/delivery.go`, with parity
tests, while `content/budget.go` and its tests remain unchanged.

Current `content.IssueRequest` contains only outer `AssetRef`, action,
renderer/profile and proof. `Broker.resolveDerivedRepresentation` returns no
Derived binding for download, and no request/job/artifact member identity exists.
Two member requests for one outer asset are therefore ambiguous and attachment
can fall back to the original. Focused 000068 must own an exact `archive_member`
grant and use a request-bound Derived resolver; `content/contracts.go`,
`broker.go`, its model and 000066 stay unchanged.

`backupContentEffectiveScheme` currently trusts a single
`X-Forwarded-Proto` value without checking `RemoteAddr` against configured
`TrustedProxies`. Router/Gin already receives the validated proxy CIDRs, but the
cookie helper does not. Export cannot repeat that assumption: the focused plan
injects the existing proxy set into a shared direct-TLS/trusted-peer scheme
policy and tests forged/multi-value/ambiguous headers for both branches.

`AuthHandler` accepts one `contentSessionRevoker`, and current `cmd/server` wires
only the Content Broker. The already planned runtime/main/router/content-handler
paths can provide a composite `BackupContentService`/revoker instead: it must
best-effort call both 000066 Content and 000068 Export ledgers even after one
error, aggregate a safe result and let runtime reconciliation repair partial
failure. No Auth handler path is required in the future modify manifest.

## 5. Processing, Derived And Archive Evidence

paired 000067 supplies processing job/interest/attempt/grant/upload, Worker
identity/capability, encrypted Derived store and updater metadata. Runtime wires
the coordinator, one-use grants, RecoveryPoint fences, Derived Store and
capability service.

Child 11 registered closed `archive.inspect/archive_index_v1` and
`archive.extract_entry/archive_member_v1`. Its bounded implementation:

- accepts ZIP/TAR and controlled gzip/xz/zstd paths;
- rejects encrypted/malformed archives, absolute/traversal/noncanonical or
  duplicate paths, links/devices/FIFO/socket, bomb/ratio/depth/count/size limits;
- computes a 32-hex opaque member ID from private ordinal/checksum evidence;
- resolves that ID to exactly one private ordinal and extracts one regular item;
- returns canonical member ID/display name/size/media metadata plus content.

Public Processing API exposes `archive_index` preview but no member list/job/
delivery route. Current Derived resolver validates archive-index JSON, while
`DerivedAttemptSourceResolver` only promotes exact complete text/OCR
`text/plain` artifacts into later Worker input. Arbitrary member output cannot
be a safe nested source. Child 12 must orchestrate the existing capability,
not add a parser/profile or claim multi-hop.

The 2026-07-26 planning amendment records a separate no-leak boundary: Child 12
promises attachment-only one-hop archive member delivery, not generic
Search/preview exposure. `archive.extract_entry/archive_member_v1` can emit text,
and the existing projection decision can otherwise publish generic Derived text or
OCR before the request is ready. A terminal authority/source invalidation removes
a running request's interest and marks that request failed, but generic Search has
no request-state join; ready
revocation calls unfenced `RevokeSet`, which fails once a projection is published.
The smallest containment is future non-projectable handling in
`processing/derived_manifest.go`, with focused coverage in its existing test file.
This is planning evidence for new-product prevention only, not proof of code/test
execution or broad durable cleanup of historical projections. This branch has not
shipped or merged, so the future code path closes the new-product route only.

`ReasonArchiveRatioLimit` exists only in capabilityspec diagnostics. The Worker
maps every `capabilities.ErrInputLimit` to persisted
`ProcessingErrorInputTooLarge`; Processing state/model carries no diagnostic
reason. The regression chain must therefore be a real ratio-bomb fixture ->
`ErrInputLimit` -> persisted `ProcessingErrorInputTooLarge` -> backend closed
limit product. No 067/schema/capabilityspec/worker-client edit should retain the
reason.

## 6. Authorization, Step-Up And Audit Evidence

Current main already contains:

```text
backupasset.PermissionBackupAssetsExport = backup_assets:export
auth.StepUpActionAssetExportCreate       = asset.export_create
auth.StepUpActionAssetExportDownload     = asset.export_download
frontend STEP_UP_ACTIONS assetExportCreate / assetExportDownload
LeaseHolderExportJob
AuditActionExportCreate / ExportCancel / ExportDownloadTicket / ExportDownload
AuditActionArchiveInspect / ArchiveMember
AuditFieldExportJobID
```

Backend tests enumerate these actions and frontend storage knows the exact
purposes. `auth-context-provider.tsx` supports
`ensureStepUpProof(action,{persist:false,reuseCached:false})`; Export must pass
both false because defaults otherwise permit reuse/persistence.

Credential grants authorize credential use. Export receives only Content bytes
and has no credential/locator, so the parent's grant edits are unjustified.
The existing typed asset audit sanitizer/hash chain remains the sink; Child 12
needs action wiring and raw-sentinel coverage, not a second registry.

## 7. Settings, Root And Runtime Evidence

`settings/service.go` registers `backup_assets.enabled=false`. Local/remote
Worker and updater are false. Derived root is
`/var/lib/xirang-asset-runtime/derived` and RequiresRestart; Content cache is a
separate `/var/cache` root. No Export root/key/quota/TTL/GC setting exists.

`backupasset/service.go` converts one atomically validated settings snapshot
into typed configs. Export must extend it rather than issue independent setting
reads. Static root/key is RequiresRestart; live limits cannot change an already
persisted absolute deadline.

`FoundationService.atomicFoundationValues` iterates the complete
`BackupAssetFoundationSettingKeys` and rejects any missing snapshot key.
Repository's test `BackupAssetSettingsSnapshot` copies only the explicit fixture
map, so `repository/testutil_test.go` must add this Child's frozen defaults and
retain the complete-snapshot regression; weakening the production contract is
not an option.

`repository/private_runtime_root.go` validates a clean absolute non-symlink
root and rejects `/data`, `/backup`, `/logs`, Task rsync roots and local
Repository binding overlaps without logging locators. Export can reuse it and
prove Content-cache/Derived-root separation.

`Makefile`'s `backend-build` target writes `backend/xirang-server`, and current
ignore rules do not cover that path. Phase 2 must delete that exact generated
binary after backend/full builds, assert it absent, and compare the final exact
manifest against the union of tracked, staged and untracked non-ignored paths.

There is no Export Runtime field/accessor/worker. The existing shared Runtime
is the only Foundation/Repository/Catalog/Search/Overlay/Content/Processing
composition root and owns startup/transition/downgrade/run/shutdown. Child 12
must extend it. No durable Compose export volume exists; that remains Child 15.

## 8. API And Frontend Evidence

### 8.1 Current API Surface

The router mounts authenticated asset/search/overlay/content/processing routes,
GET/HEAD plus method rejection for `/asset-content/:deliveryId`, and preview-job
create/poll/cancel/state. There is no asset-export or archive-member route/
handler. Generated Swagger is `backend/internal/api/docs/docs.go`; cross-role
proof is `backend/internal/api/backup_asset_rbac_test.go`.

### 8.2 Selection, Bulk Bar And Lazy Patterns

`BackupAssetsState.selection` is an in-memory `Map<string, AssetRef>`.
Toggle/clear replace the Map; result/context transitions clear it. However,
`selectionGeneration` does not advance for toggle, clear or results replacement.
An async Export flow must clone the exact refs and assign its own action revision
before awaiting step-up.

Overlay `UseSavedSearch` reads through the service DB and accepts no caller
transaction or expected version. Therefore a mutation after the final Search
page but before Export create commit is not closed by frozen-item revalidation.
The saved-search final transaction race is therefore a real current-main gap.
The focused manifest adds only `overlay/service.go` and its test for a narrow
caller-Tx owner/state/version/query validator; Export's frozen selection also
binds the consumed Search generations and never reads the raw Overlay model.

`AssetBrowser` mounts `AssetBulkBar`; the bar currently has inspect-one and
clear only, with an explicit no-Export test. This is the focused integration
seam.

Backup API modules use central request, private raw DTOs and closed camelCase
mappers. The processing hook demonstrates lazy import, AbortController/action
revision, bounded poll cadence, transient/offline/visibility handling and
initial GET reconciliation. Export/archive modules can reuse the pattern.

The UI renders archive index only as a text-derived preview. It has no typed
hierarchy/member API/hook/panel. Route parsing has a strict query allowlist and
no export job ID. `CatalogPermissions` has only list/preview/download and cannot
be repurposed as Export authority.

## 9. Frozen Design Implications

1. Add a thirteen-table Export aggregate: durable job key, immutable item-
   attempt history, create-time source refs/leases, quota reservations and a
   strict temporary-artifact delivery union; its global quota-bucket singleton
   is the permanent first-write use latch. Never rewrite old rows/Provider.
2. Ensure `KeyDomainExportStore` on demand and keep defaults false. Create must
   persist the wrapped job DEK before encrypted selection can commit.
3. Acquire all RP leases in the create transaction with zero request deadlines,
   persist each exact returned deadline, cap execution/access/artifacts with the
   relevant returned/RetentionUntil/config minima, and retain leases through
   queued/running/ready until access terminal; do not use source FKs as holds or
   reacquire to extend a cap. At terminal/rollback, fence and revoke/drain first,
   destroy wrapped DEK/selection references before releasing source leases/non-
   store reservations, then complete physical cleanup and store-byte release.
4. Spool each regular item encrypted, revalidate before archive header, and
   retry the whole ZIP/TAR attempt after takeover with fresh spool/final nonce.
5. Use the existing asset-content path through a collision-safe mux. 000068
   exact grants and transaction/CAS budgets cover export and archive member;
   old Broker/000066 budget models stay closed. The same mux exposes a composite
   session revoker that always attempts both ledgers and reconciles partial error.
6. Fix shared TLS evidence to honor XFP only from configured trusted proxies;
   avoid Child 12 deploy/nginx edits.
7. Add typed one-hop archive member orchestration, durable request idempotency
   and exact Derived output binding; do not change Child 11 parser/profile.
8. Wire existing permissions/purposes/actions; no generic proof/reason/grant.
9. Frontend triggers explicit bulk only; saved-search remains typed API-only,
   expanded totals are authoritative after create, and status items paginate.
   Saved-search commit locks/revalidates typed owner/state/version/query plus
   frozen Search generations before any acquire or durable Export write.
10. Extend exact numeric settings/runtime/Swagger/RBAC/PostgreSQL selector and
    lazy frontend only at the future manifest. Clean and assert absent the
    Make-generated `backend/xirang-server`, then compare tracked/staged/untracked
    path union. Required PostgreSQL commands set migration/export/Processing
    fail-closed envs, and the full gate unsets `NODE_ENV`. Keep 069-071,
    Compose/Docker/public docs/release/publish untouched; Child 15 owns volume
    durability. Update the Repository frozen-default fixture for every new
    foundation key without weakening snapshot completeness. TTL/lease/expiry
    tests freeze/inject time or derive boundaries from test start, and 068
    required parity regresses both PostgreSQL scan types.
11. Treat every symlink/hardlink as `skipped/link_metadata_unavailable`; current
    metadata has no target, so do not add a seam/schema or guess/read one.
12. For encrypted/unsupported/limit archive outcomes, a real ratio-bomb fixture
    follows `ErrInputLimit -> persisted ProcessingErrorInputTooLarge`; map every
    such stored code to the closed limit product and delegate only the existing
    authorized original Content download. Otherwise render a no-leak reason and
    keep controlled recovery gated on Child 13.

Focused closure review further proved that whole-archive retry cannot retain
any old current-item projection, physical unlink failure cannot release occupied
store quota, and a durable job cannot reinterpret changed settings after
restart. The final design therefore resets every current projection/counter per
fresh attempt, separates execution outcome from cleanup/artifact state, retains
store-byte charges until unlink/fsync/locked inventory proof, and persists an
exact typed per-job limit snapshot. It also stores archive-member digest +
ordinal rather than ciphertext with no key owner, and assigns filesystem-
pristine proof to `ExportRuntime.PrepareSchemaDown`; paired 068 SQL down checks
only DB/lease/key state. These are consistency closures inside Child 12, not
new Recovery/Lifecycle/GA scope.

This amendment first added exactly three existing paths to the future modify
manifest: the Repository fixture and Overlay service/test. A later 2026-07-23
controller-approved test-only amendments add the two Rsync repository fixtures
whose fixed calendar clock can age past the 168h lease deadline and the Rsync
Provider preflight fixture whose one-hour evidence expires. The exact future
scope was 56 create + 49 modify. A 2026-07-24 runtime publication review then proved
that the approved dynamically reloadable Export settings could not be applied atomically
without the existing Settings/Config mutation boundaries. The focused controller
amendment adds their five production/test paths, making the exact scope 56 create +
54 modify. A 2026-07-25 controller amendment adds the PostgreSQL dirty-state
search-path guard and regression test, making the exact scope 56 create + 55 modify.
A same-day controller amendment adds `database.go` and `database_test.go` so
SQLite DSN query enforcement replaces caller `_txlock`/`_busy_timeout` values
instead of appending duplicate keys; the scope was then 56 create + 57 modify.
A final scope-minimization audit removes unrelated `settings_handler_test.go`,
because dedicated live-settings coverage is in `settings_transition_test.go` and
`config_handler_test.go`. The 2026-07-26 controller-directed amendment adds the
two existing `processing/derived_manifest` paths. The subsequent source-reader
drain amendment adds only the existing `content/attempt_broker.go`. The later
shared Provider bounded-reader cancellation amendment adds only existing
`provider/restic.go` and `provider/runner_test.go`, making exact current scope
56 create + 61 modify. A final exact-dirty-union reconciliation found that the
already necessary `overlay/idempotency.go` live TTL/key read had not been named:
it is part of the existing atomic runtime-settings contract, uses the Overlay
service lock, and adds neither endpoint nor product behavior. The standing
controller authorization then recorded the precise scope as 56 create + 62
modify. The 2026-07-28 focused accessibility amendment adds only existing
tracked `web/src/index.css` and `web/src/index-css.test.ts`, making the then-current
scope 56 create + 64 modify. Those paths cover the approved global reduced-motion/
power-save behavior; the export-list inset focus ring remains owned by the
already-listed export panel files. The later byte-preserving route-cleanup
amendment adds existing tracked `web/src/lib/api/core.ts`, making the exact
then-current scope 56 create + 65 modify. A later final full gate exposed the
documented `StdoutPipe`/`Wait` ownership race in the existing streaming runner;
the focused amendment adds only
`processing/capabilities/runner.go` and `runner_test.go`, making the approved
target 56 create + 67 modify. These amendments change no product correction,
dependency, capability profile, migration, Provider behavior or frozen
production path beyond those two explicit runner exceptions.
`boundedReadHandle` held its mutex through an underlying
Read while Close needed the mutex before it could close that reader, so this is
a required drain-before-key/source-cleanup containment. It changes no Provider
public contract, bytes, locators, credentials, Content public contract, schema,
endpoint, deploy, release or product correction. It is a security/no-leak containment inside the existing
Child 12 attachment-only one-hop contract, not a new product feature or a
historical-output cleanup claim. It does not add a create path, endpoint, Content
budget, Auth handler, capabilityspec, Worker client, 000068 SQL, migration,
deploy, docs or release path, and does not claim code or test implementation.

## 10. Phase 1 Diff And Execution Truth

The intended final tracked Phase 1 set is exactly:

```text
M  .trellis/tasks/07-12-backup-data-explorer-design/task.json
?? .trellis/tasks/07-22-backup-assets-export-archive/check.jsonl
?? .trellis/tasks/07-22-backup-assets-export-archive/design.md
?? .trellis/tasks/07-22-backup-assets-export-archive/implement.jsonl
?? .trellis/tasks/07-22-backup-assets-export-archive/implement.md
?? .trellis/tasks/07-22-backup-assets-export-archive/prd.md
?? .trellis/tasks/07-22-backup-assets-export-archive/research/current-main-evidence.md
?? .trellis/tasks/07-22-backup-assets-export-archive/task.json
```

| Gate/action | Phase 1 status |
|---|---|
| fresh fetch/baseline/branch/task create | executed |
| parent/Children 7-11/current-main research | executed |
| focused planning artifacts | authored; planning checks recorded in §11 |
| focused PRD/design/implementation-plan + 56 create / 67 modify manifest reviews | `complete_approved`; prior amendments plus the 2026-07-28 runner stdout ownership amendment limited to existing `processing/capabilities/runner.go` and `runner_test.go` |
| thirteen focused corrections/deviations approval | `complete_approved`; 2026-07-22 controller user reply “批准” |
| planning workflow transition | `complete_approved`; same approval + 2026-07-22 user clarification |
| `task.py start` authorization | `complete_approved`; same approval + 2026-07-22 user clarification |
| Phase 2 implementation within exact manifest | `complete_approved`; same approval + 2026-07-22 user clarification |
| `task.py start` | `executed`; 2026-07-22 after fresh immutable preflight |
| status transition | `executed`; Child `in_progress`, parent `planning` |
| red tests/product code/test/migration files | `executed`；exact-manifest implementation and focused RED/GREEN ledger recorded |
| SQLite/PostgreSQL/restart/frontend/backend rows recorded before the final rerun | `historical_checkpoint` only; never current completion evidence |
| current Step 10 runnable gates | `passed_current_follow_up_amended`; approved exact manifest `131`; Config rollback and Processing dual-boundary retry RED/GREEN plus fresh full/race/PostgreSQL reruns passed |
| current Step 10 dependency audit | `risk_accepted_for_child_delivery`; unchanged package files still report `1 moderate + 3 high`; vulnerabilities are not fixed and audit did not pass |
| Step 11 workflow permission | `active_incremental_follow_up_pending`; initial commit/push/draft PR executed; five product/spec plus six ledger paths await review/commit/push/CI |
| exact staging/work commit | `initial_executed_follow_up_pending`; `94a15dc` pushed; current five product/spec plus six ledger paths uncommitted and `staged=0` |
| trellis finish/Child archive/journal/archive commit | `not_executed` |
| push/PR/required CI/squash merge/post-merge | initial push and draft PR #399 executed; follow-up push/CI, ready/merge/post-merge `not_executed` |
| local main sync/branch-worktree cleanup | `not_executed` |
| Release Please PR #386/release/deploy | `not_applicable`; forbidden/out of scope |

### 10.1 Historical Accessibility Focused Checkpoint

The 2026-07-28 controller-approved accessibility amendment adds only existing
tracked `web/src/index.css` and `web/src/index-css.test.ts`; the later route
cleanup added existing tracked `web/src/lib/api/core.ts`. The exact scope at
that checkpoint was `8` Phase-1 + `56` create + `65` modify = `129` paths.
The CSS amendment covers global reduced-motion/power-save behavior; the list
focus-ring classes remain in the already-listed panel path. All amendments
preserve the thirteen product corrections. The separate panel repair is
complete but is focused evidence only: its genuine RED was the export-job list
missing `tabindex` (`1` failed / `7` passed), and GREEN was `8/8`. TypeScript
passed; ESLint reported `0` errors and its one configured debt warning; the
scoped diff check passed. Independent spec review is `APPROVED`, and
independent code-quality review is `APPROVED` with no findings.

The controller ran the final read-only Chromium 150 check directly after the
subagent dispatcher failed; product implementation and both reviews remained
delegated. `/tmp/c12-browser-recheck/evidence/browser-evidence.json` records
desktop `1200x900` and mobile `390x844` screenshots, `tabindex` attribute and
property `0`, active `:focus-visible`, PageDown/ArrowDown list scrolling while
focus remained active, axe `0` violations / `0` incomplete in the isolated
harness with the project-consistent color-contrast exclusion, reduced motion
`0.01ms` and normal motion `80ms`, no horizontal overflow, zero console/API/
exception events, and a visible inset focus ring in both screenshots.

This was a focused historical checkpoint and did not close Step 10 at that
time. Section 13 records the later exact-129 rerun, §14 records the historical
runner-reopen checkpoint, and §15 is the authoritative current status. No
staging has occurred and `staged=0`.

### 10.2 Current Byte-Preserving Route And Lifecycle Checkpoints

The existing tracked `web/src/lib/api/core.ts` is now part of the exact modify
manifest. A genuine RED (`1` failed / `244` passed) proved that rebuilding the
query normalized unrelated bytes (`%2f` to `%2F`, `%20` to `+`, a bare `flag`
to `flag=`, and an empty separator away). GREEN removes every decoded
`exportJobId` field while preserving all other raw query bytes, ordering,
duplicates, bare flags, empty separators and hash fragments. The focused suite
passed `245/245`, the API-core selection passed `264/264`, and typecheck,
targeted ESLint and scoped diff checks passed. The prior behavior review found
no product defect after the fix; its sole finding was the now-resolved manifest
omission.

The latest backend lifecycle/runtime-stop RED reproduced a false successful
shutdown when an earlier terminal non-purged cleanup row sat behind the ordinary
reconcile cursor. GREEN returns a durable blocker without rewinding the
persistent scheduler, while later actionable rows still progress. Fresh focused
verification passed 27 top-level tests (31 including subtests), focused race,
Export full/race, vet, formatting and whitespace checks. Runtime normal/race
passed except the unrelated host `cache_root_unverified` case. The four reviewed
hashes remained unchanged and staged paths remained zero. A separate reviewer
returned `SPEC APPROVED`; an independent `trellis-check` reviewer returned
`APPROVED` with no lifecycle/runtime-stop findings.

## 11. Phase 1 Planning Validation Evidence

This section records planning-document checks only. It must not be read as
product, migration, test, CI or delivery evidence. Final checks below ran on
the final narrative text only after this paragraph and every other planning edit
were complete. No repository file is edited after that rerun; the current-turn
handoff command outputs are the execution evidence.

### 11.1 Focused Closure Reviews

Three independent read-only reviews checked backend/current-main feasibility,
parent/frozen-contract coverage and frontend lifecycle closure. They first
found planning inconsistencies, which were corrected in the focused documents:

- every fresh active attempt resets all current item projections/counters,
  while ready source-lease takeover never creates an attempt or resets result;
- execution outcome is orthogonal to cleanup/artifact state, including pre-
  publication cleanup failure;
- store-byte accounting remains charged until unlink, parent fsync and locked-
  root inventory proof all succeed;
- jobs persist exact typed limit columns rather than reinterpret changed
  settings after restart;
- archive-member requests retain only digest + ordinal, with no ciphertext
  lacking a metadata-key owner;
- `ExportRuntime.PrepareSchemaDown` proves the locked filesystem root pristine
  before invoking DB/lease/key-only paired 068 SQL down.

A later controller/current-main review found three further planning blockers,
which are now incorporated in all four narrative artifacts:

- Export uses zero-deadline Foundation acquisition, persists each returned
  deadline and derives lifecycle caps without changing/reacquiring lease code;
- absent link-target metadata makes every symlink/hardlink a stable
  `skipped/link_metadata_unavailable` with no Provider read or invented member;
- encrypted/unsupported/limit archive fallback, including ratio bomb persisted
  as generic `ProcessingErrorInputTooLarge`, reuses only an authorized fresh
  original Content download, while controlled recovery remains Child 13-gated.

That controller clarification did not add a fourteenth correction or a manifest
path. It made ratio-bomb fallback inside correction 13 explicit, and separately
required Phase 2 to remove and assert absent the Make-generated
`backend/xirang-server` before exact manifest parity over the tracked/staged/
untracked union.

The subsequent validation-only review also left all thirteen product
corrections and the then-current manifests unchanged. It requires
`env -u NODE_ENV make check`,
required migration/export/Processing PostgreSQL helpers, and fatal missing-DSN
behavior so neither host production mode nor a skipped PostgreSQL row can count
as completion evidence.

The same approval surface now explicitly closes the parent Step 9 rollback-order
contradiction inside existing correction 6: key destruction precedes source-
lease release, and fault matrices reject every released-source/readable-key
window. This is a refinement, not a fourteenth correction.

Final validation discipline also rejects aging fixed-calendar TTL/lease/expiry
fixtures and requires non-skipped 068 proof that both current-main PostgreSQL
`timestamp` and `timestamptz` codecs retain their `ScanLocation` behavior. No
connector or ScanLocation-related manifest path is added.

A later current-main review confirmed four P1 and two P2 planning corrections.
The focused disposition keeps product correction numbering at thirteen while:

- adding the Repository foundation fixture plus Overlay service/test, then the
  three focused no-aging-calendar Rsync test fixtures, five existing
  relevant Settings/Config handler paths, the PostgreSQL dirty-state guard, and the
  SQLite enforced-DSN query replacement guard to the future modify manifest;
  a final scope-minimization audit removes unrelated `settings_handler_test.go`,
  then adds the two existing `processing/derived_manifest` paths for the
  archive-member non-projectable containment, then adds the narrow existing
  `content/attempt_broker.go` source-reader drain correction, then the two
  narrow shared Provider bounded-reader cancellation paths (`provider/restic.go`
  and `provider/runner_test.go`), then records the existing Overlay idempotency
  helper as part of the same live-settings lock domain, so it was then exactly
  56 create + 62 modify; the later 2026-07-28 accessibility amendment adds
  existing `web/src/index.css` and `web/src/index-css.test.ts`, making that
  exact scope 56 create + 64 modify; the later existing `web/src/lib/api/core.ts`
  route-cleanup amendment made the then-current exact scope 56 create + 65 modify;
  the final runner stdout ownership amendment adds only existing
  `processing/capabilities/runner.go` and `runner_test.go`, making the approved
  target 56 create + 67 modify;
- validating saved-search owner/state/version/query and frozen Search generations
  in the create transaction before any lease or durable Export row;
- making the global 000068 quota-bucket singleton a permanent first-write use
  latch so used/purge-to-empty down can never masquerade as pristine;
- replacing the impossible diagnostic pair with the real ratio-bomb
  `ErrInputLimit -> persisted ProcessingErrorInputTooLarge` product input;
- keeping Content `BudgetService` bound to 000066 while Export delivery owns
  parity-tested 000068 transaction/CAS accounting; and
- composing Content+Export `RevokeSession` in already planned mux/runtime/main
  paths, with best-effort dual calls and restart reconciliation, while Auth
  handler remains unchanged.

This review also freezes unchanged 067/schema/capabilityspec/worker-client and
Content budget files. These are planning corrections, not implementation proof.

Targeted follow-up review marked the original six resolved, and the controller
independently confirmed the earlier three facts/dispositions. Frontend
closure was 8/8 for
API-only saved search, estimate/authoritative truth, history/focus, quiet TTL,
paging/bounded DOM and automatic retry. An earlier read-only manifest audit
proved the then-current set; the then-final 56/54 set required fresh Step 10
parity before later amendments.

These are reviews of planning contracts, not proof that future code behaves as
designed.

On 2026-07-22 the user replied “批准” in the controller thread. That receipt
marks `prd.md`, `design.md`, `implement.md`, this evidence, the then-current exact
manifest and all thirteen corrections/deviations `complete_approved`; the
2026-07-23 controller decision approved the three-path test-fixture amendment,
and the user's standing implementation direction authorizes the controller's
2026-07-24 five-path runtime-settings amendment.
The same user's 2026-07-22 clarification confirms that this approval also grants
the planning workflow transition, `task.py start`, and Phase 2 implementation
inside the exact manifest. Approval covers all requests the controller presents
together unless the user explicitly excludes one; it does not cover genuinely
new scope, irreversible high-risk actions, or manifest deviations. The approved
`task.py start` command executed once on 2026-07-22 after the fresh preflight.

### 11.2 Final Pre-Start Planning Command Results

| Planning-only check | Fresh result |
|---|---|
| `task.py validate .trellis/tasks/07-22-backup-assets-export-archive` | valid; `implement.jsonl` 12 entries and `check.jsonl` 12 entries |
| JSON/JSONL parsing | both task JSON files and all 24 JSONL rows valid |
| branch/ref/merge-base | dedicated branch; HEAD/main/origin/main/merge-base all `9ad2893c714c82781461f452030c25e0766eedd4`; ahead/behind `0/0` |
| task tree | pre-start check found child `planning`, parent `planning`, parent pointer exact, registration count 1, 12 instantiated children, delivery truth 11/15; the approved start then moved only the child to `in_progress` |
| Phase 1 scope parity | 8/8 exact paths; 1 modified parent registration + 7 untracked child planning files; 0 staged/missing/extra |
| future manifest classification | historical pre-start result: 56 unique create paths all absent and 49 unique modify paths all tracked/existing; the later historical 56/54 scope required fresh Step 10 parity after the runtime-settings amendment |
| format | `git diff --check` clean; all 7 untracked Child files have trailing whitespace 0 and final newline; all 8 Phase 1 files have final newline |
| `[P]LACEHOLDER`/approval/stale-contract scan | pre-start scan found 0 unfinished marker; planning package/manifest/deviations plus workflow/start/Phase 2 authorization consistently `complete_approved` from the 2026-07-22 controller user reply and clarification; 0 stale prior-count/raw-ratio/explicit-deadline/safe-link-target/release-before-key-delete contract; correction count remains 13; the approved `task.py start` subsequently executed once |
| amended contract presence | all 4 narrative artifacts cover saved-search final Tx validation, Repository foundation fixture, permanent global quota-bucket use latch, real `ErrInputLimit -> ProcessingErrorInputTooLarge` boundary, independent 000068 delivery budgeting and dual-ledger logout reconciliation; frontend consumes only closed limit product |
| generated-binary/scope hygiene presence | generated `backend/xirang-server` cleanup/absent and tracked+staged+untracked manifest union remain planned; implement has 2 build cleanup commands, 3 absent assertions and 1 untracked-union command |
| validation-command presence | Step 10 has no naked `make check`; full gate is `env -u NODE_ENV make check`; migration, Export and Processing each use an exact independent required-mode selector, matching CI and preventing a no-tests-matched false pass |
| time/PostgreSQL validation discipline | injected/frozen or test-start-relative TTL/lease/expiry fixtures are required; aging fixed-calendar fixtures are forbidden; required 068 parity covers both `timestamp` and `timestamptz` ScanLocation registrations without a connector edit or skip |
| migration ownership | current paired 067 present for both engines; current 068-071 absent; future manifest exactly paired four-file 068; future 069-071 absent/reserved |
| forbidden current/future paths | no Phase 1 product/delivery path; no future deploy/release/Recovery/retention/GA path; no Provider production path beyond the approved bounded-reader lock-scope correction in `provider/restic.go` and approved Provider test paths, old Content Broker/budget/schema, Auth handler, capabilityspec/Worker-client or 062-067 migration edit |

The first final-format run exposed only the task generator's missing newline on
the already in-scope parent `task.json`; that newline was added without changing
JSON content, and the full planning check set was rerun afterward. No product,
migration, frontend, backend, server, browser, Docker or remote check was
executed; their truth remains exactly §10.

## 12. 2026-07-28 CI-Equivalent Dependency Audit Delta

This current-main delta was discovered during final Child 12 verification. It
does not change the product contract or exact manifest. HEAD still owns clean
`web/package.json` and `web/package-lock.json` bytes.

Both local Node 24/npm 11 and CI-equivalent Node 20/npm 10 reproduce the current
moderate/high audit failure. A disposable lockfile-only `npm audit fix` probe
updates `react-router`/`react-router-dom` to `7.18.1`, `postcss` to `8.5.23`,
`nanoid` to `3.3.16`, and the two `brace-expansion` resolutions to `1.1.16` and
`5.0.8`. Re-audit still fails on later advisories: the maintenance
`brace-expansion` line has no compatible published fix, and the only clean
React Router line is `8.3.0`, which requires Node `>=22.22` and React/ReactDOM
`>=19.2.7`. The repository CI/runtime contract is Node 20 plus React 18.

Therefore the proposed `web/package-lock.json` amendment is rejected rather
than recorded as false closure. No `--force`, vulnerable downgrade, hidden
major override, package edit, stage or Git mutation occurred. Exact Child 12
scope at this audit checkpoint was `8` Phase-1 + `56` create + `65` modify =
`129` because of the independent `core.ts` amendment; the audit row is
an upstream `blocked_external` Step 10 gate.

## 13. 2026-07-28 Prior Exact-129 Step 10 And Delivery Truth

This section records the prior exact-129 task status from fresh command evidence
recorded in `research/implementation-evidence.md`; it does not recast this
Phase-1 research file as the executor of those product gates. Every earlier
`passed_local`, final-gate, readiness or completion-like row in this file is a
historical checkpoint. Section 14 records the subsequent historical runner
reopen, and §15 supersedes both aggregate current-status claims.

All runnable Step 10 boundaries passed that rerun: child and parent Trellis
validation; JSON/JSONL parsing; exact manifest/static/gofmt; focused/API/core and
full frontend (`168 files / 1388 tests`); main JS `498.48/500 KiB` and CSS
`104.94/105 KiB`; lifecycle/runtime-stop, Export and Runtime normal/race;
`go vet`; backend lint/build; required real PostgreSQL migration, Export and
Processing selectors with zero skips; and the corrected short-TMP
`env -u NODE_ENV make check`. The first full-gate attempt failed only because
its temporary Unix-socket path was too long; the `/tmp/xc12` rerun passed. The
generated `backend/xirang-server` was removed and confirmed absent.

Structured manifest comparison proved the Git-visible union is exactly
`8 Phase-1 + 56 create + 65 modify = 129`, comprising `66 tracked + 63
untracked`, with missing, extra, overlap, duplicate and staged counts all zero.
The product scope and thirteen corrections were unchanged. At that checkpoint
Step 10 remained `blocked_external`, not completed/pass-all, because the unchanged
dependency manifests still report `1 moderate + 3 high`. The brace-expansion
family needs a compatible upstream release or separately scoped lint-tool
migration; Router needs a 7.x backport or a separately approved Node `22.22+` /
React `19.2.7+` / Router 8 migration. No `--force`, unsafe override or package
edit is authorized by this child.

Narrow workflow-only permission at that checkpoint allowed exact staging/coherent commit, push,
and a draft PR solely as a CI-validation channel. Hosted CI's dependency audit
is warning-only, so otherwise-green CI does not close the blocker. The required
order is exact stage/commit; draft PR/CI/fixes; dependency-risk closure; only
then ready/merge/post-merge monitoring; and only afterward Trellis archive/
journal. The PR must stay draft and cannot support a completion claim before
compatible remediation or an explicit later risk-policy disposition. No such
delivery mutation has executed yet, `staged=0`, and the parent remains
`planning` and outside Child 12 archival authority.

## 14. 2026-07-28 Historical Runner Stdout Pipe Reopen

This section preserves the runner-reopen state exactly as a historical
checkpoint. A later final `make check` invalidated the aggregate-current claim
in §13 by capturing a genuine failure in the then-unchanged
`processing/capabilities/runner.go`:

```text
TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation
read |0: file already closed
```

The standard-library contract states that `Cmd.Wait` closes `StdoutPipe` and
must not run before all pipe reads complete. The streaming runner starts Wait
and its consumer concurrently, so a fast command leader can let Wait close the
pipe before a delayed consumer observes EOF. The focused approved amendment
adds only existing `runner.go` and `runner_test.go`; the target at that
checkpoint became
`8 Phase-1 + 56 create + 67 modify = 131` (`68 tracked + 63 untracked`).

At this historical checkpoint, Step 10 was `reopened_in_progress` pending
deterministic delayed-consumer RED, parent-owned-pipe GREEN, capabilities
normal/race and fresh exact/full-project reruns. The unchanged dependency audit
remained separately `blocked_external` at `1 moderate + 3 high`. Step 11
permission was `suspended_pending_runner_rerun`; no stage, commit, push, PR or
other delivery mutation could run until the runnable gates closed. Product
scope, dependencies, migrations and the thirteen corrections were unchanged.

## 15. 2026-07-28 Historical Pre-Commit Runner-Amended Step 10 Truth

This section preserves the final pre-commit status and supersedes the aggregate
status in §§13-14 only at that historical checkpoint. The deterministic delayed-consumer regression first failed
on the old runner with exit `1` and `read |0: file already closed`. GREEN uses a
parent-owned `os.Pipe`, assigns its writer to `command.Stdout`, closes the
parent's writer copy after successful `Start`, leaves reader ownership with the
consumer, and preserves concurrent `Wait` plus process-group cleanup.

Fresh verification then passed the exact regression, the capabilities package
normal and race runs, the regression at `-count=20`, and
`go vet ./internal/backupasset/processing/capabilities`. Structured scope
verification proved the exact Git-visible union is:

```text
8 Phase-1 + 56 create + 67 modify = 131
68 tracked + 63 untracked = 131
missing: 0
extra: 0
create/modify overlap: 0
duplicates: 0
staged: 0
```

The corrected full gate
`TMPDIR=/tmp/xc12 GOTMPDIR=/tmp/xc12 env -u NODE_ENV make check` exited `0`.
Backend lint reported `0 issues`; all backend packages passed. Frontend
typecheck, lint, test and build passed with `168` files / `1388` tests; ESLint
reported `0` errors and one configured warning. Bundle sizes remained within
the existing limits at JS `498.48/500 KiB` and CSS `104.94/105 KiB`.

Required PostgreSQL 18 selectors ran with zero skips: migration passed in
`15.194s`, Export in `13.624s`, and Processing/archive-member in `4.781s`.
`backend/xirang-server` was removed and confirmed absent. Therefore every
runnable Step 10 boundary is `passed_current_runner_amended`.

At that historical checkpoint, overall Step 10 remained `blocked_external` solely because the unchanged
dependency audit exits `1` with `1 moderate + 3 high`; no complete compatible
Node 20/React 18 remediation exists, and package files remain unchanged. Step 11
was `authorized_limited_pending`: exact-131 staging/coherent commit, push and a
draft PR/CI are permitted as the validation channel. The PR must remain draft,
and no ready/merge/completion claim is permitted until dependency-risk closure.
No stage, commit, push, PR, CI, merge, archive or journal action had executed at
that checkpoint; `staged=0`, and the parent remained `planning`. Section 16 is
the authoritative current delivery and verification ledger.

## 16. 2026-07-28 Current Commit, CI Follow-Up And Risk Disposition

The approved scope remains exactly `8 Phase-1 + 56 create + 67 modify = 131`.
Commit `94a15dc41634b096839ef6e661714a88db1f4c09` (`feat: add backup asset
export and archive`) is the current `HEAD`, is pushed on
`codex/backup-assets-export-archive`, and backs draft PR #399:
<https://github.com/xiangnan0811/xirang/pull/399>. The child remains
`in_progress`; parent `07-12-backup-data-explorer-design` remains `planning`.
Child 12 completion must not be read as completion of the parent, 07-11, or P3
work.

At the start of this final ledger synchronization, the product/spec dirty
increment was exactly five already-approved manifest paths:
`config_handler.go`, `config_handler_test.go`, `derived_manifest.go`,
`derived_manifest_test.go`, and `.trellis/spec/backend/database-guidelines.md`.
They remain uncommitted and unstaged at this evidence boundary. The ledger sync
itself modifies the six assigned task artifacts, so the resulting unstaged
worktree contains eleven paths total; every path is already in the approved 131
manifest.

CI RED/GREEN 1 proved that Config import swallowed Task Create failures, could
return 200, and could partially commit nodes. Production now returns the
transaction error so the handler emits a generic 500 and the complete import
transaction rolls back; the fixture also isolates the secure global cache.
Focused and sequencing tests, handlers coverage `57.9%`, handlers `-race`, vet,
gofmt, and diff checks passed.

CI RED/GREEN 2 proved that Processing `CommitManifest` could hit SQLite locks at
the projection-evidence read or atomic-publish transaction and leave concurrent
successes at zero. Both boundaries now use the existing bounded,
context-aware conflict retry and retain exactly one durable winner. Focused
deterministic tests, count `50`, race count `20`, Processing coverage `74.0%`,
and full Processing race passed.

Fresh local gates after both fixes passed: exact `131`; child and parent task
validation; `env -u NODE_ENV make check` exit `0`; backend lint `0`; frontend
`168 files / 1388 tests`, lint `0` errors plus one approved a11y debt warning;
bundle JS `498.48/500 KiB` and CSS `104.94/105 KiB`. Required PostgreSQL 18
selectors ran without skip against loopback `127.0.0.1:55470` and passed:
migrations/UTC/dirty-search-path `49.561s`, Export `13.353s`, and
Processing/archive-member `4.679s`.

The fresh npm audit still reports four vulnerabilities (`1 moderate + 3 high`)
in brace-expansion, postcss, and react-router. The controller explicitly and
temporarily accepts this unchanged pre-existing dependency risk for Child 12
delivery. This closes the Child 12 dependency-risk gate but does not mean the
vulnerabilities are fixed or the audit passed. Package files stay unchanged;
no `--force`, unsafe override, or incompatible Node 20 + React 18 router
migration is authorized. Track a compatible upstream fix or a Node/React/Router
migration in a separate Trellis task and branch after Child 12 merge.

Step 10 is therefore `passed_with_explicit_dependency_risk_acceptance`. Step 11
is `active_incremental_follow_up_pending`: initial commit, push, and draft PR
creation are executed; incremental review/commit/push/CI for the five product/
spec plus six ledger paths remain. Ready, merge, post-merge monitoring, Child
archive, and journal are not executed. The Git index remains empty.
