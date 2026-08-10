# Child 13 current-main evidence

## Baseline and scope

- Inspection baseline: `51771654a85967656fe1ca69686590b734ff9214` on
  `codex/backup-assets-controlled-recovery`, created from synchronized `main`.
- Child 12 is archived and its paired `000068_backup_asset_export` migration is
  present for SQLite and PostgreSQL. All four proposed `000069` recovery files
  are absent; `000070` and later remain reserved.
- This is planning evidence only. No product, test, migration, stage, commit,
  push, PR, or implementation action was run while collecting it.
- The independent security/state review is retained verbatim in
  `research/child13-security-state-review.md`. Its eight Important findings are
  correction inputs, not approval by themselves. Two independent read-only
  rereviews approved every corrected disposition and the exact 55-create +
  71-modify plan/manifest on 2026-07-28 with no open Critical/Important issue.
  The final immutable preflight then passed with 9 Phase-1, 55 create and 71
  modify paths; `task.py start` ran exactly once, leaving Child `in_progress`
  and the parent `planning`. Product/test/migration implementation had not yet
  begun at that milestone.

## Reusable frozen seams already delivered

### Domain, lease, authorization and audit

- `backend/internal/backupasset/domain.go` already registers
  `LeaseHolderRecoveryJob = "recovery_job"` and the exact composite
  `AssetRef{RecoveryPointID, EntryID}`. `LeaseService.AcquireTx`, `RenewTx`,
  release and fence checks are already transaction-capable; Child 13 should
  consume them rather than modify the foundation lease contract.
- `backend/internal/backupasset/authorization.go` already registers
  `backup_assets:recover`. Current RBAC maps it to Admin and does not grant it
  to Viewer. The new route matrix must preserve that closed role boundary.
- `backend/internal/auth/step_up_action.go` already registers
  `asset.recover`, `recovery.result_download`, and
  `recovery.result_retain`, with validation tests. Child 13 needs purpose-exact
  handler/service consumption, not new free-form proof strings.
- `backend/internal/backupasset/audit_action.go` already registers recovery
  plan, preflight, authorize, execute, cancel, verify, cleanup, retain and
  result-download actions plus `recovery_job_id`. The shared audit model also
  has the opaque recovery job field. Child 13 must use these registered values
  and the existing sanitizer instead of adding parallel audit tables or raw
  metadata.

### Content plane activation seam

- `backend/internal/backupasset/content/contracts.go` already defines the
  tagged `DeliveryResourceRecoveryResult`,
  `RecoveryResultRef{RecoveryJobID, ResultID}`, and a mutually exclusive
  `DeliveryResource`. Today `ValidateDeliveryResource` deliberately returns
  `ErrRecoveryResultUnsupported` for that valid-shaped arm; Child 13 owns the
  narrow activation and adapter.
- The current Broker is still asset-specific: its issuance request carries an
  `AssetRef`, authorization is through `AssetAuthorizer`, and product proof
  validation accepts only asset preview/download actions. Recovery delivery
  therefore needs an explicit typed adapter/product arm and must not weaken or
  reinterpret the existing backup-asset path.
- Child 12 already composes Content and Export logout revocation in runtime and
  main. Recovery result delivery must join the existing composite revocation
  boundary without changing Auth handler semantics or exposing a third
  independent logout path.

### Provider and repository boundaries

- Provider capabilities for Rsync, Restic and Rclone currently advertise
  `Restore`, but `backend/internal/backupasset/provider/contracts.go` contains
  read and publication ports only; there is no exact restore request/result
  contract or provider-owned restore executor.
- The new seam belongs in `backupasset/provider`: Provider must stay independent
  of Gin, runtime, Repository, Task and API packages. Rsync, Restic and Rclone
  request variants must be a closed tagged union so the recovery service never
  branches on executor strings or constructs arbitrary commands.
- Repository and runtime already provide exact RecoveryPoint/Catalog
  authorization, source fingerprint, manifest, keyring and lease facts. Child
  13 should introduce a narrow transaction-bound source-revision validator and
  reuse those facts; it must not read raw encrypted locators in the frontend or
  handler.

### Foundation/runtime composition

- `backend/internal/settings/service.go` exposes one ordered
  `BackupAssetFoundationSettingKeys()` set and an atomic
  `BackupAssetSettingsSnapshot()`. `FoundationService.atomicFoundationValues`
  requires every key to be present.
- `backend/internal/backupasset/repository/testutil_test.go` maintains a frozen
  copy of every foundation default and explicitly tests completeness. Every
  Child 13 foundation key therefore requires synchronized settings definitions,
  settings tests, Foundation parsing/tests, and this Repository fixture; partial
  maps must fail rather than silently using mixed revisions.
- `backend/internal/backupasset/runtime/runtime.go` currently publishes
  Repository, Catalog, Search, Overlay, Processing, Content, Export and archive
  member facades, but no Recovery facade or worker exists. Runtime is the only
  composition root below `cmd/server`; the recovery graph must be installed and
  stopped there, then passed to Router/main through narrow facades.
- The Recovery Cleanup Ownership key domain is already ensured during runtime
  startup. Child 13 must use it for target marker authentication and define
  lost-key/mismatch fail-closed behavior; it must not repurpose Export or
  Content encryption keys.

## Frontend and API integration facts

- `web/src/pages/backups-page.recovery.tsx` is intentionally an evidence-only
  placeholder. Its test proves that no `Start recovery` control currently
  mounts. This is the correct Child 13 route surface to replace with a controlled
  plan/job experience.
- The asset workspace already owns exact route context, stable composite
  selection, selection replacement, bulk action controls and the asset
  inspector. Recovery should consume that typed selection rather than invent a
  second path/listing model. The parent manifest must include the workspace,
  bulk-action, route-state and browser tests when the selection handoff changes.
- Existing frontend API modules keep raw snake_case DTOs private and map them to
  closed camelCase unions. Child 13 needs a dedicated
  `backup-recovery-api.ts`; components must not call `fetch`, read DB-shaped
  fields, or coerce opaque revisions to JavaScript numbers.
- Current locale files already contain legacy restore and recovery-evidence
  strings. New plan/job/result copy must remain under the backup-assets/recovery
  namespace and must not reuse legacy phrases that imply immediate destructive
  restore.

## Database and CI gaps owned by Child 13

- `backend/internal/database/backup_asset_migrations_integration_test.go` stops
  at paired `000068`. It must add paired-file, apply/behavior, used/pristine down
  and timestamp scan-location coverage for `000069`.
- `.github/workflows/ci.yml` requires real PostgreSQL migration, Processing and
  Export selectors, but has no required Recovery behavior selector. Child 13
  must add a fail-closed `REQUIRE_POSTGRES_RECOVERY_TEST=1` helper and include
  `000069` in the real PostgreSQL parity command.
- Down must remain possible only for a truly pristine recovery aggregate. A
  durable plan/job/grant/checkpoint/result/lease/key/marker fact must block down;
  application rollback keeps the additive schema and cleanup worker alive.

## Focused planning corrections to the parent outline

1. Use target eligibility policy A: any non-archived registered node that passes
   exact credential/tool/root/capacity/source/conflict preflight; preselect the
   producing node only when eligible.
2. Preserve the frozen three-lifecycle split: RecoveryPlan, RecoveryJob outcome,
   and RecoveryResultSet plaintext lifecycle. Do not add cleanup states to the
   job.
3. Add the existing asset workspace, bulk action, route-state/browser and their
   tests to the exact modify manifest because they own selection-to-recovery
   handoff; the parent file list names only the recovery page and inspector.
4. Activation of `recovery_result` is a closed tagged-union extension, not a
   rewrite of existing asset delivery. Existing Content and Export products must
   keep their exact proof and budget semantics.
5. Keep legacy restore UI/routes gated through Child 15. This Child adds no GA
   default, deploy/release claim, reconnect, retention or purge behavior.

The original completed manifest audit kept the future create set at 55, including
the Child implementation-evidence ledger, and fixed the future modify set at 71,
including the existing database guideline whose migration-head truth advances
only after 000069 is proved. It excludes five unrelated generic proof/grant/RBAC
surfaces because Recovery consumes already registered seams. The eleven audited
replacements in `implement.md` §2.3 cover the established backup-asset RBAC
matrix, settings/config transitions, shared node dialer, ordinary Task writer
admission, Backups data-page split and page-level a11y test.

The exact rereview candidate union is nine current planning paths, 55 future
creates and 71 future modifies: 135 unique, disjoint paths. The ninth current
path is the immutable security/state review note. These planning and workflow
paths do not add Child 14 lifecycle/reconnect or Child 15 GA behavior.

Post-start Task 3 rereview amendment (2026-07-30): the newly discovered mutable
Task-node writer-identity race requires the already tracked
`backend/internal/model/task.go` in the modify manifest. The then-current union
was therefore 9 current + 55 create + 72 modify = 136 unique paths. The additive
`task_runs.node_id_snapshot` remains owned by paired `000069`; `000070+` stays
reserved and the twelve-table Recovery aggregate is unchanged. The preceding
55/71 paragraph remains the historical Phase 1 preflight truth.

### Task 4 B1 source-boundary correction (2026-07-30)

The later inspection found that the Provider-local Rsync source capability is
not a safe authority boundary. `provider/restore.go` exposes an issuer that
accepts a validation containing a raw locator; `recovery/service.go` constructs
the managed root/locator validation; and `provider/rsync.go` validates the
managed tree, closes its descriptor, then returns a string path through
`RsyncCommittedPointRuntimeAccess`. `fileaccess.LocalTree` opens a fresh strict
root from that path for each operation. An arbitrary issuer can therefore forge
the locator shape, and a root/final-tree swap can occur between validation and
the later runner access.

The approved correction keeps Provider limited to portable closed
`RestorePort`/source-ref/source-resolver/target-writer contracts, registry kind
checks and typed sanitized errors. Repository implements the resolver contract
and owns the concrete Rsync port, derives
the producing task only from the RecoveryPoint, reuses the existing committed
point request decoder, revalidates plan, point, active complete catalog,
selection, source revision, manifest and locator digest before every phase, and
revalidates root/marker/point/manifest after the runner returns.
fileaccess owns a pinned strict no-follow tree capability which keeps the Linux
descriptor anchored through bounded declared-regular-entry use; a runner sees no
reconstructed source/root or remote path. Recovery creates only the scalar
`RsyncRestoreSourceRef`, and Runtime performs resolver/target-writer injection.

This adds exactly eight tracked modify paths and removes none:

```text
backend/internal/backupasset/provider/rsync.go
backend/internal/backupasset/provider/rsync_test.go
backend/internal/backupasset/repository/query.go
backend/internal/backupasset/repository/query_test.go
backend/internal/fileaccess/contracts.go
backend/internal/fileaccess/local_linux.go
backend/internal/fileaccess/local_other.go
backend/internal/fileaccess/local_test.go
```

The prior amended union was 9 current + 55 create + 80 modify = 144 unique,
disjoint paths. The correction adds no create path, migration, model/table,
`000070+`, `repository/binding.go`, or
`repository/rsync_publication_execution.go`. The scope amendment itself was not
Task 4 approval or completion.

### Task 4 B1 Catalog completion blocker amendment (2026-07-30)

Read-only source inspection found a deterministic blocker after the corrected
B1 boundary implementation and focused gates were otherwise complete.
`provider/catalog.go`'s generic `catalogReadSession.acceptEntry` maps `Entry`
into `CatalogRecord` but does not set `FingerprintStrength`. The generic
`Entry` contract carries no fingerprint fields, and committed managed Rsync
uses this generic session. Repository's `sealedCatalogReadSession` only encrypts
`ProviderLocator`, clears it, and preserves every other record field. The
Catalog indexer then calls `ParseFingerprintStrength(record.FingerprintStrength)`;
the closed parser accepts only `strong|weak|none`, so the empty value rejects the
record and no real immutable managed-Rsync Catalog can complete.

The minimal GREEN is Provider-owned: generic entries without proved fingerprint
evidence project an empty fingerprint with the explicit closed strength `none`.
Repository and Catalog remain strict and unchanged; neither may default an
empty strength. The genuine RED uses the real committed managed-Rsync fixture
in the already manifested `provider/rsync_test.go`, and the cross-layer
Repository→Catalog completion regression uses the real managed-tree fixture in
the already manifested `repository/query_test.go`. Inspection also covered
`provider/catalog_contract_test.go`, `repository/catalog_read_test.go`, and
`catalog/indexer_test.go`; no modification to those synthetic/wrapper/closed-
parser tests is required for the minimal fix.

Exactly one tracked modify path is added and none is removed:

```text
backend/internal/backupasset/provider/catalog.go
```

The current exact union is 9 current + 55 create + 81 modify = 145 unique,
disjoint paths. No create path, migration, model/table, `000070+`, or Task 5--10
path is added. At this dated inspection, no product test or product edit had
run; the focused RED, GREEN, Provider/Catalog/Repository/Task 4 gates, and
independent reviews were `not_executed`, and Task 4 was still open.

The later focused Task 4 closure is `complete_approved`. It preserves the
observed Provider missing-strength RED and real Repository immutable Rsync
Catalog point changed RED, rejects the detached synthetic factory/fingerprint
rewrite, uses authenticated `request.SourceFingerprint`, and proves the real
`Service.OpenCatalogRead` plus `catalog.Indexer` path under focused normal and
race selectors. The receipts are `SPEC APPROVED` and `QUALITY APPROVED`. The
inherited-GREEN follow-up is not a new RED. Broad Provider remains blocked by
host `IFree=0`; this is not Child/full/PostgreSQL/frontend/CI/PR/merge evidence.
Tasks 5--10 remain `not_executed`.

The validation-time dirty union is 61 paths: 59 are members of the current
145-path manifest, and exactly two are unrelated and preserved without edit:
`go.mod` and `recovery/testdata/rsync_local_to_remote.json`. The supplied
similarly named path
`backend/internal/backupasset/recovery/testdata/rsync_local_to_remote.json` was
absent at inspection time; it remains an unexecuted future-create manifest path
and was not used as an unrelated exclusion. No move, delete or scope admission
was performed.

## Evidence commands

The planning inspection used read-only `rg`, `rg --files`, `sed`, `git` and
Trellis validation commands. Key classification checks were:

```bash
git rev-parse HEAD origin/main
rg -n 'LeaseHolderRecoveryJob|RecoveryResultRef|StepUpActionAssetRecover|backup_assets:recover' backend/internal
rg -n 'AuditActionRecovery|AuditFieldRecoveryJobID' backend/internal/backupasset
rg -n 'RecoveryService|RecoveryResult|restore' backend/internal/backupasset/runtime backend/internal/backupasset/provider
rg -n 'IssueRsyncRestoreSource|RsyncManagedSourceRoot|RevalidateRsyncRestoreSource|RsyncCommittedPointRuntimeAccess' backend/internal/backupasset/provider backend/internal/backupasset/recovery
rg -n 'openStrictRoot|OpenRegular|OpenRange' backend/internal/fileaccess
rg -n 'FingerprintStrength|catalogReadSession|acceptEntry|sealedCatalogReadSession|ParseFingerprintStrength' \
  backend/internal/backupasset/provider/catalog.go \
  backend/internal/backupasset/provider/rsync_test.go \
  backend/internal/backupasset/repository/catalog_read.go \
  backend/internal/backupasset/repository/query_test.go \
  backend/internal/backupasset/catalog/indexer.go
git cat-file -e HEAD:backend/internal/backupasset/provider/catalog.go
git cat-file -e HEAD:backend/internal/backupasset/provider/rsync_test.go
git cat-file -e HEAD:backend/internal/backupasset/repository/query_test.go
rg --files backend/internal/database/migrations | sort
test ! -e backend/internal/database/migrations/sqlite/000069_backup_asset_recovery.up.sql
test ! -e backend/internal/database/migrations/postgres/000069_backup_asset_recovery.up.sql
rg -n 'recovery|Start recovery' web/src/pages/backups-page.recovery.tsx web/src/features/backup-assets
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-28-backup-assets-controlled-recovery
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
git diff --check
```

## Task 6 locator-contract planning reconciliation (2026-08-01)

An independent read-only design pass returned `DESIGN READY`. Controller
direction approved the result as a coherent clarification inside the existing
thirteen Task 6 corrections and exact manifest. This is planning research only:
it edits no product, test, model or migration file, supplies no RED/GREEN
evidence, and gives no Task 6 completion credit. Task 6 remains `in_progress`;
Tasks 7--10 remain open/not executed.

The controlling resolution preallocates the opaque job ID, every item ID and
isolated `jobs/<opaque>` workspace identity outside the execute transaction in
an in-memory prepared aggregate, without a row or remote reservation. Every
schema-v2 operation, including delete, carries the canonical
`target_relative_locator` and `SemanticTargetDigest`; the strict-joined final
`TargetObjectDigest` is distinct, and only that final digest may populate
`TargetObjectRef.TargetPathDigest`. Isolated jobs commit at workspace phase
`none` with the encrypted preallocated locator and immutable
`WorkspaceBindingDigest`, but empty marker/owner/fence/deadline facts;
`PrepareFirstWrite` must reuse that identity. In-place none-state rows keep all
workspace fields empty.

Each job item carries both digests, complete operation facts, recovery-local
AEAD locator ciphertext and explicit positive key/cipher versions. Generic
model encryption hooks do not own the item locator column; generic encryption
continues for `EncryptedOperationRows` and the job workspace locator. Existing
generic `enc:v2` denotes the encrypted preflight snapshot, not item AEAD.
`TargetLocatorEnvelopeBinding` length-frames the complete row/job/root/
workspace/digest/version/operation product and authenticates the exact item and
workspace locator plaintexts.

Execute preparation performs replay, decode, whole-product validation,
association resolution, identity allocation, exact-key selection, digesting and
encryption outside the transaction. The transaction then performs ordered
rechecks/locks, byte-for-byte prepared-aggregate recomputation and exact
`LockActiveTx`, makes grant CAS its first effect mutation, inserts the complete
aggregate plus receipt, and commits once. It performs no encryption, provider,
SSH, target, audit or reservation work. Preparation failure leaves no durable
state; transaction failure rolls back grant and aggregate together; post-commit
failure leaves a complete unreserved aggregate whose identity is reused, while
an unexpected remote directory fails closed.

Restart adoption remains exactly
`AdoptInterruptedOperation(ctx, claim, jobItemID)` and has three boundaries:
short DB load/decrypt/validation, target I/O with no DB transaction held, then
final re-lock and fenced CAS. Paired `000069` alone must freeze the workspace
matrix, identities, both digests, ciphertext/versions, operation matrix,
uniqueness and terminal projection before any target I/O can use reconstructed
items.

The exact manifest remains 9 current + 55 create + 81 modify = 145 unique,
disjoint paths. This clarification adds no path, table, route, migration,
backfill or `000070`; only the already owned paired `000069` may later be
amended. At this artifact snapshot the dirty union is 82 paths: 80 manifest
paths plus exactly the two protected unrelated paths below. Staged paths remain
zero, branch remains `codex/backup-assets-controlled-recovery`, and HEAD remains
`51771654a85967656fe1ca69686590b734ff9214`.

```text
go.mod
sha256 b767fc9ef9376c651d0493329b710ac8dcf8d77e52686b847d244aff9f6d48fd

recovery/testdata/rsync_local_to_remote.json
sha256 2570bd4472541322d902c4cdf2fe43f247b69f19d335558830e749567f763892
```

Earlier dirty-count and execution-status paragraphs in this file are dated
historical evidence and are not rewritten by this later current reconciliation.

## Post-B3 controller-supplied reconciliation (2026-08-02; not a current-main rerun)

This dated amendment records controller-supplied Task 6 execution and review
facts. It did not rerun product tests or repeat the historical current-main
inspection, and it does not relabel any earlier command or historical status in
this file as a current pass.

The stable Task 6 batch ledger is B1 `IMPLEMENTED_UNPROVEN` for ordinary/
foundation Corrections 1--3, 5 and 7--13; B2 `IMPLEMENTED_UNPROVEN` for
exact-mirror/multi-delete Corrections 4 and 6 plus its delete row; and B3
`PROVED_COMPLETE_FOCUSED_ONLY` for Correction 14's unresolved remote outcome.
B1 and B2 receive no retroactive RED or review credit. B3 gives no
first-thirteen, whole Task 6, Child or delivery completion credit.

For B3, the sole bounded final writer changed only
`backend/internal/backupasset/recovery/executor_test.go` and
`backend/internal/database/backup_asset_migrations_integration_test.go`. The
controller-supplied evidence records required real PostgreSQL `000069` plus the
six-case behavior matrix passing with no skip; the bounded cancellation set,
focused race, affected exact-mirror regressions, vet, owned gofmt, diff,
manifest and staged-zero guards passing; and resources being cleaned.
Independent specification receipt
`019fc0c2-cfda-74e3-b218-246f3a425545` returned `APPROVED` and closed both prior
Important test-evidence findings. Controller-inline code-quality review found
no issue after fresh vet, owned `gofmt -d` and `git diff --check` exited zero,
with staged paths zero. A local reviewer rerun could not link because of host
disk quota and is not classified as pass or fail.

The remaining Task-6-owned ledger is the original-review F3/F4/F6 portions,
first-thirteen evidence closure, unchecked execution items, whole specification
and quality reviews, and whole gates. The original review is F1--F8; Design
Corrections 5--9 are among the approved first thirteen and are not review-
finding numbers. No Finding 9 exists. Task 6 owns preallocated workspace/
reservation and cleanup-only classification plus bounded restart adoption/
reconciliation. Task 7 owns publication, Content revalidation, revoking
takeover, cleanup node-lease behavior and RecoveryResultRef denial. Task 8 owns
startup/listener ordering and managed lifecycle. Tasks 7--10 remain
`not_executed`.

The next sequence remains F6, F3, bounded B1/B2 evidence closure, the
Task-6-owned F4 workspace/deadline/cleanup-only proof, whole Task 6 specification
and quality reviews, and every frozen/race/required-PostgreSQL/static/manifest
gate before Task 7. F6 itself remains unexecuted pending independent focused
planning/spec approval. The exact manifest remains 9 Phase-1 + 55 create + 81
modify = 145 unique, disjoint paths, with no path, migration, table, route, API,
context row or `000070` added or removed.

## F6 controller-supplied focused closure (2026-08-02; not a current-main rerun)

This later dated amendment records controller/writer F6 evidence without
rerunning the historical current-main inspection or product tests. It supersedes
only the current-status and next-order wording above; it does not rewrite any
earlier command, output or dated status.

F6 is `complete_approved` at focused live-mutation-permit scope only. Its sole
permanent delta is the recording fake near `worker_test.go:34` and
`TestRecoveryReviewF6LatchBeforeTargetMutation` near line 669. `worker_test.go`
moved from SHA-256
`a2452e6d5f01c4afb9fb5255ecc188b8790b695f0121430ac078a58cce373534` to
`352c31b6e5ced3f9f4a033a096ee90c5cd196be3bc4da65ab426bca18254ab3d`.
`target.go` was modified only for the controlled RED and restored byte-for-byte
to SHA-256
`8a0efaafc5bb08d3981790cc0fa27760936b80a58862f1910fd3e96dd5c64822`.

The genuine controlled RED bypassed only the `TargetMutationPermit` live-proof
callback. Every revoked latch/job/attempt/node/source case reached
`CreateOwnedJobDir` and produced
`revoked authority CreateOwnedJobDir error=<nil>, want ErrInvalidTargetPermit`;
compilation and quota failures are not RED evidence. GREEN admits
`CreateOwnedJobDir`, `CreateDirectory`, `WriteAtomic` and `Delete` under current
authority and rejects permanent latch loss plus current job, attempt-fence,
node-lease-fence and source-fence loss before fake mutation.
`RemoveOwnedJobDir` remains deferred to Task 7.

The writer's combined SQLite/model/recovery selector, F6 race selector at
`-count=10`, four frozen recovery regressions, `gofmt`, `go vet`, diff,
exact-manifest and staged-zero checks passed. Independent specification thread
`019fc136-feca-7fb0-82bc-3c33739aef12` returned `SPEC APPROVED`; independent
quality thread `019fc13c-0710-7343-b261-dd866382a8c0` returned `QUALITY APPROVED`
and confirmed deterministic isolated fixtures, reliable admission recording,
the frozen hashes, exact 145-path manifest and staged paths zero.

Required PostgreSQL gate thread `019fc13d-ea0e-7f93-b1c6-32aebcb7368e`
returned `POSTGRES GATE PASSED` for the exact
`TestRecoveryReviewF6UseLatchPostgres` command: exit 0,
`ok xirang/backend/internal/database 1.709s`, wall 31.032s, PostgreSQL 18.4 from
isolated `postgres:18-alpine` at loopback database `xirang_f6_gate`. The first
two compile attempts exhausted `/tmp` quota and did not reach tests; the passing
run used `/dev/shm` for Go/cgo temporary work. Created container/scratch were
removed and pre-existing resources were untouched.

This focused closure gives no F3, B1/B2, F4, whole Task 6, Child, delivery or
full-gate credit. Task 6 and the Child remain `in_progress`; the parent remains
`planning`. The exact manifest remains 9 Phase-1 + 55 create + 81 modify = 145
unique/disjoint paths and staged paths remain zero. The fixed next order is F3,
B1-E1/E2/E3, B2-E1/E2, Task-6-owned F4, whole Task 6 specification review,
whole quality review, all final gates, then Task 7.

## R0 stop-and-rebaseline audit (2026-08-02)

This entry was collected after the user stopped the old controller and paused
its goal. R0 performed read-only repository/thread/remote inspection, then
updated only existing Trellis planning/evidence paths. It did not edit product,
test, migration, CI or the two out-of-manifest files; did not stage, commit,
push, switch branch, create a worktree/task/PR, resume a goal, or begin F3.

### Git, manifest and remote baseline

```text
branch                         codex/backup-assets-controlled-recovery
HEAD/main/origin-main          51771654a85967656fe1ca69686590b734ff9214
HEAD ahead/behind main         0/0
dirty paths                    82 = 40 tracked modifications + 42 untracked
staged paths                   0
approved allow-list            9 Phase-1 + 55 create + 81 modify = 145 unique
dirty paths in allow-list      80
dirty paths outside allow-list 2
tracked diff                   15,221 additions / 497 deletions
untracked line count           49,044
pre-R0 tracked patch SHA-256   3cf2dad83af30e07cc567d6a400190cbb362b53d7b7ed8a2641bef96f16c5929
pre-R0 untracked SHA-256       0733f9e3435632e1b76f7dc2227dfe6bc6f8d49dd08710b0b3bcd2018c245972
```

The full hashes above freeze the state before R0's intended Trellis edits. To
make the post-R0 product/config candidate independently reproducible without a
self-referential evidence hash, the fingerprints excluding every `.trellis/`
path are:

```text
tracked non-Trellis patch SHA-256   0c7b8e1069e66aef68b10836c38e431f19dce8336c4f3e87894e516de54b2fce
untracked non-Trellis SHA-256       675f5e42fa00fa05afc76076b22cbf5ce62a6e7ef2b682918de0bc12380e1623
```

`git ls-remote --heads origin` confirms no remote Child 13 branch. GitHub has no
Child 13 PR. Release Please PR #386 remains open; latest delivered main is still
`5177165`, and the latest public release remains `v0.45.0`.

The two excluded paths remain untouched and require an explicit later
disposition:

| Path | Birth/mtime evidence | R0 disposition |
|---|---|---|
| `go.mod` | born 2026-07-30 13:59:33 +0800; 25 bytes; root-level duplicate module declaration | preserve; do not stage, admit, move or delete during R0 |
| `recovery/testdata/rsync_local_to_remote.json` | born 2026-07-30 13:59:21 +0800; 999 bytes; root-level recovery fixture | preserve; do not confuse with the approved future path under `backend/internal/backupasset/recovery/testdata` |

Their creation within twelve seconds and their wrong repository-relative
locations are consistent with a prior Child 13 command running from the
repository root, but R0 does not convert that inference into deletion authority.

### Branches and worktrees

The only active feature branch for current Child 13 work is the local
`codex/backup-assets-controlled-recovery`; it has no upstream. Local `main`
tracks `origin/main` at the same commit. Two stale local branches are retained:

- `codex/backup-assets-export-archive` has the same tree as merged PR #399
  squash commit `bd9572f`; its former upstream is gone.
- `codex/019f82831f427880b73e99fe0a7dbf08` is already contained in `main`.

No branch is deleted or switched during R0.

There are nine detached Codex worktrees in addition to the main checkout:

| Worktree | HEAD | Dirty status | Disposition |
|---|---|---:|---|
| `0103` | `5177165` | 0 | preserve clean |
| `31d1` | `5177165` | 44 = 17 tracked + 27 untracked | stale Child 13/Task 3 snapshot; preserve, not active |
| `613c` | `8cd6e51` | 0 | preserve clean |
| `7f06` | `9ad2893` | 0 | preserve clean |
| `b0de` | `a3c309a` | 0 | preserve clean |
| `b2b5` | `be6eebb` | 0 | preserve clean |
| `bb05` | `2ce7133` | 0 | preserve clean |
| `c893` | `be6eebb` | 0 | preserve clean |
| `eac2` | `9ad2893` | 129 = 66 tracked + 63 untracked | stale Child 12 snapshot; 116 product paths match merged PR #399 tree; preserve, not active |

The `31d1` owner task previously failed immediately with a 502 and made no new
edits. Neither dirty detached worktree contains an accepted deliverable beyond
the main-checkout candidate or merged Child 12 tree. Cleanup is deliberately a
separate future housekeeping decision, not part of restoring Child 13 progress.

### Parent and Child delivery state

The parent has thirteen instantiated children. Children 1--12 are merged and
archived; Child 13 is the sole active child; Children 14--15 do not yet exist.
The authoritative program denominator is the fifteen-child map in the parent
`implement.md`, so delivered program progress is `12/15 = 80%`. Trellis
`12/13 done` is mechanically correct for instantiated children but is not total
program completion.

Child 13 has no staged content, commit, remote branch, PR, CI, merge or archive
evidence. Its evidence-backed product ledger is:

| Child 13 unit | Status |
|---|---|
| Task 0 activation/reload | complete; checkboxes were stale and are reconciled by R0 |
| Tasks 1--3 | `complete_approved` at their recorded focused task scopes |
| Task 4 | `complete_approved`; seven stale unchecked rows reconciled by R0, no reimplementation |
| Task 5 | `complete_approved` at focused authorization-receipt scope |
| Task 6 B1/B2 | `IMPLEMENTED_UNPROVEN`; no retroactive RED or review credit |
| Task 6 B3 | `PROVED_COMPLETE_FOCUSED_ONLY` for Correction 14 |
| Task 6 F6 | `complete_approved` at focused live-mutation-permit scope |
| Task 6 remaining | F3, B1-E1/E2/E3, B2-E1/E2, F4, unchecked rows, whole reviews and whole gates open |
| Tasks 7--10 | not executed |
| Task 11 full verification | not executed |
| Task 12 delivery | not executed; the Task 5 receipt row was removed from its mechanical checkbox count |

Before R0, raw checkbox parsing reported `53/132`; after removing the misplaced
Task 5 receipt row and reconciling Task 0/4 historical completion, the
mechanical ledger is `62/131`. Neither number is a product completion
percentage. It underweights implementation quality and delivery gates while
mixing workflow, focused evidence and future tasks.

### Old controller and slow-progress diagnosis

Old controller task `019f6fe1-5133-7012-a18c-7c9dda469cb5` is `idle`; its last
turn is `interrupted`. The old goal is paused. The automation file for
`child-13-controller-heartbeat` is still present with
`FREQ=MINUTELY;INTERVAL=15`, but status is `PAUSED`. R0 preserves the task,
goal record and automation file as evidence and does not resume, rename, archive
or delete them.

The prior audit measured approximately 122,062,554 goal tokens and 369,339
seconds (about 102.6 active hours), with a 460,159,119-byte controller rollout
and about 315 context compactions. These measurements diagnose coordination
cost; they are not progress metrics.

The slow pace has multiple concrete causes:

1. The goal covered an enormous remaining program with no natural milestone
   exit, while the heartbeat repeatedly woke the same controller every fifteen
   minutes. "Continue until complete" therefore turned normal unfinished work
   into continuous execution rather than a reason to stop and review.
2. The controller's delegated-only role made small planning, writing,
   PostgreSQL, specification-review, quality-review and ledger steps separate
   tasks. Each handoff reloaded a very large contract and returned another long
   evidence packet. This preserved independence but imposed substantial
   orchestration and context cost.
3. Task 6 experienced legitimate security/state discovery: fourteen controlling
   corrections, multiple real race/transaction findings and required dual-engine
   proof. Repeated focused fixes improved correctness, but the plan/checklist was
   not reconciled promptly, so completed focused work still looked open and the
   controller repeatedly remapped status.
4. Context compaction, `/tmp` and inode/quota failures, one 502 owner-task
   failure, unavailable PostgreSQL context in some tasks, and long generated
   handoffs added environment/recovery overhead. Those failures were correctly
   not counted as RED or PASS, but each triggered more coordination work.
5. The controller optimized for continuous local activity and micro-gate closure
   rather than bounded user-visible delivery checkpoints. The repository shows
   real Task 1--6 progress, but no Child-level commit/PR/CI feedback loop, so
   elapsed time grew without a corresponding delivery milestone.

The conclusion is not that goals, controller/child tasks or subagents are bad.
The failure mode was combining a huge goal, repeated heartbeat wakeups,
unbounded continuation and insufficient milestone observability. Future use is
therefore bounded and purpose-specific; only the 15-minute heartbeat ban is a
hard task-specific rule.

### F3 approved bounded adjudication (2026-08-02)

The completed read-only scope task
`019fc163-15de-7ca3-be2b-1c1cb42284ce` approved a no-migration plan: use the
existing stateless eligibility scan, skip expected claim-time fence losers, and
limit changes to `worker.go`, `worker_test.go`, `service.go` and
`service_test.go`. It identified behavioral RED seams for authority-only
pre-write drift terminalization, frozen-intent execute replay, same-call
progress past a fence loser, and the real PostgreSQL two-worker transaction.

A competing read-only plan requires persistent claim/takeover cursor facts in
the existing `000069` evidence product to prove restart-stable progress past a
candidate that remains SQL-eligible and persistently fails after selection. The
tiebreak task `019fc16d-fa98-7c13-9aed-7f94b447a233` was interrupted after
noting that the existing restart test covers only rows excluded before `LIMIT`,
not an eligible persistently failing prefix. It returned no verdict.

The bounded inline adjudication subsequently inspected the controlling
`design.md`/`implement.md` contract, the current worker queries and claim/
takeover transactions, the existing restart tests, both paired `000069`
migrations and the durable Export lifecycle precedent. It established that the
third class is required and real: a candidate can remain SQL-eligible after a
post-selection conflict/fence failure, while neither the candidate nor any
existing durable scheduler fact records traversal progress. The current restart
tests only make the old prefix ineligible before `LIMIT`.

The user approved persistent Plan A+ on 2026-08-02. The existing
`backup_asset_recovery_evidence` product will own exactly two fixed, closed
`scheduler_state` rows for claim and takeover cursor/high-water/revision facts.
Selection durably pre-advances one candidate in a short scheduler transaction
before the candidate-local claim/takeover transaction. Conflict/fence loss
changes scheduler metadata only; database-wide failure fails the pass; a crash
delays one candidate until sweep wrap without starving later work. The paired
down guards ignore only those exact two scheduler rows and remain fail-closed for
the latch and all real Recovery state. No new table or `000070` is authorized.

Plan B is rejected because same-call skipping loses progress across restart.
Per-job/per-attempt backoff is rejected because a stale fence loser cannot mutate
a domain row for scheduling. Plan A+ also includes the stateless analysis's two
valid findings: atomic authority-only pre-write-drift terminalization, and
receipt-linked execute replay from frozen intent rather than mutable current
source facts.

The exact ten product paths and four selectors are frozen in `design.md` §23.1
and `implement.md` F3. This adjudication changed Trellis decision/evidence only;
it ran no product RED/GREEN or PostgreSQL gate and grants no F3 implementation,
Task-6, Child or delivery credit. The next milestone is the separate bounded F3
TDD writer, which must stop after its focused evidence checkpoint before B1/B2.
