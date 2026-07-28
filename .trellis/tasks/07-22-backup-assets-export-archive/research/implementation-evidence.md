# Child 12 Implementation Evidence

## Execution Boundary

```text
task:       .trellis/tasks/07-22-backup-assets-export-archive
branch:     codex/backup-assets-export-archive
base:       9ad2893c714c82781461f452030c25e0766eedd4
child:      in_progress
parent:     planning
started:    2026-07-22
manifest:   56 create + 49 modify (initial historical checkpoint)
current manifest: 56 create + 67 modify
```

This file records Phase 2 commands and observed results. It does not broaden the
approved manifest or convert a skipped/unavailable gate into completion evidence.

The lower 49/54/56/61/62/64/65 counts are historical checkpoints. The current
67-modify manifest includes the existing `overlay/idempotency.go`: its live TTL/key reads
must share the already-approved Overlay service lock used by the runtime-settings
transition. This is a manifest reconciliation for an existing atomic-settings
contract, not a new endpoint, product capability, or correction. Its final two
paths are existing `processing/capabilities/runner.go` and `runner_test.go`,
added only for the documented `StdoutPipe`/`Wait` ownership race.

## Current Superseding Status

- Step 10 is `passed_with_explicit_dependency_risk_acceptance`. The approved
  manifest remains exactly `131`. In addition to the historical runner closure,
  current Config rollback and Processing dual-boundary retry RED/GREEN plus fresh
  focused/race/coverage/full/PostgreSQL gates are recorded at the end of this
  file.
- The unchanged package-tree audit still reports `1 moderate + 3 high`. The
  controller explicitly and temporarily accepts this pre-existing risk for
  Child 12 delivery. The vulnerabilities are not fixed and audit did not pass;
  no package edit, `--force`, unsafe override, or incompatible Node 20/React 18
  router migration is included. Separate post-merge Trellis follow-up remains
  required.
- PostgreSQL lock, crypto/AAD, archive-profile, lifecycle/runtime-stop and all
  other earlier focused closure rows remain valid only for their named boundary.
  Every earlier completion, final-gate, readiness or `passed_local` statement
  below is explicitly historical and is never current aggregate completion
  evidence. The P3 instrumentation coverage note remains a coverage limitation,
  not a product failure. The product scope and thirteen corrections are unchanged.
- Step 11 is `active_incremental_follow_up_pending`. Commit
  `94a15dc41634b096839ef6e661714a88db1f4c09` is pushed and draft PR #399 is
  open. The five product/spec in-manifest paths present at ledger-sync start plus
  the six assigned ledger paths are uncommitted and `staged=0`, with incremental
  review/commit/push/CI pending.
- Ready/merge/post-merge monitoring and Child archive/journal remain
  `not_executed`. The child remains `in_progress`, and the parent remains
  `planning` outside Child 12 archival scope.

## First Genuine TDD RED

Observed at `2026-07-22 16:00:38 CST` before any production, migration, model,
runtime, API, or frontend implementation edit.

```bash
cd backend
go test ./internal/database -run '^TestBackupAssetMigration068PairedFiles$' -count=1
```

Result: exit `1`. The package compiled and the test executed. Its four subtests
failed only because these approved migration files did not yet exist:

```text
migrations/sqlite/000068_backup_asset_export.up.sql
migrations/sqlite/000068_backup_asset_export.down.sql
migrations/postgres/000068_backup_asset_export.up.sql
migrations/postgres/000068_backup_asset_export.down.sql
```

This is the intended missing-behavior RED. It is not an environment, dependency,
PostgreSQL DSN, permission, syntax, or test-setup failure.

## Focused RED/GREEN Ledger

The following commands are focused TDD evidence only. They are not aggregate,
race, package-wide, full-project, CI, or delivery-gate evidence.

### Migration cleanup deadline independence

```bash
cd backend
go test ./internal/database -run '^TestBackupAssetMigration068SQLite/CleanupMayAdvanceAfterExecutionDeadlineAndRetentionCapsLease$' -count=1
```

- RED: the 000068 CHECK product incorrectly coupled cleanup timestamps and
  ready/source caps to the execution deadline.
- GREEN: exit `0` after removing those invalid couplings from both paired
  migration engines and retaining independent retention/deadline caps.

### Quota store-charge ordering

```bash
cd backend
go test ./internal/backupasset/export -run '^TestQuotaServiceSeparatesNonStoreAndStoreRelease$' -count=1
```

- RED: quota cleanup had no way to release non-store capacity while retaining
  store bytes through physical purge.
- GREEN: exit `0` for the direct service-level `ReleaseNonStore` followed by
  `ReleaseStore` transitions only. This test does not prove lifecycle,
  scheduler, purge, inventory, or accounting integration, and it does not close
  quota-conservation review.

### Durable lifecycle and restart cleanup

```bash
cd backend
go test ./internal/backupasset/export -run '^TestLifecycle' -count=1
```

- RED: cleanup ordering was process-local and could not resume from durable
  `revoking`/`purge_failed` boundaries.
- GREEN: exit `0` after persisting the cleanup machine and reconciling expiry
  with an injected clock without sliding TTL.

### Frozen attempt TTL

```bash
cd backend
go test ./internal/backupasset/export -run '^TestAttemptCoordinatorClaimUsesFrozenLeaseTTL$' -count=1
```

- RED: an attempt claim used the renew margin rather than the job's frozen
  lease TTL.
- GREEN: exit `0` after capping the claim by the frozen lease TTL and absolute
  job deadline.

### Atomic source/attempt heartbeat

```bash
cd backend
go test ./internal/backupasset/export -run '^TestAttemptCoordinatorHeartbeatAtomicallyRenewsAttemptAndSource$' -count=1
```

- RED: no atomic heartbeat renewed Foundation source leases and the current
  000068 attempt under the same transaction/fence.
- GREEN: exit `0` after adding the narrow renewal port, exact-deadline check,
  source heartbeat persistence, and attempt-expiry cap.

### Source-owner takeover before job-attempt takeover

```bash
cd backend
go test ./internal/backupasset/export -run '^TestAttemptCoordinatorTakeoverSourceLeasesPreservesDeadlineBeforeAttemptTakeover$' -count=1
```

- RED: build failed only because `TakeoverSourceLeases` and its typed request
  did not exist; dependencies and the test harness compiled through that API
  boundary.
- GREEN: exit `0` after atomically applying Foundation `TakeoverTx`, replacing
  the persisted source attempt/fence hash, and preserving the exact absolute
  deadline without changing job/item attempt projections.

Focused regression after GREEN:

```bash
cd backend
go test ./internal/backupasset/export -run '^(TestAttemptCoordinatorPersistsCheckpointAndRejectsOldFenceAfterTakeover|TestAttemptCoordinatorTakeoverSourceLeasesPreservesDeadlineBeforeAttemptTakeover|TestAttemptCoordinatorFailsBeforeClaimWhenSourceFenceDrifts|TestAttemptCoordinatorClaimUsesFrozenLeaseTTL|TestAttemptCoordinatorHeartbeatAtomicallyRenewsAttemptAndSource)$' -count=1
```

Result: exit `0`. This closes only the named attempt/source regressions.

### Persistent DB reload and recorded KEK version

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions$' \
  -count=1
```

- RED: no persistent attempt loader could reconstruct the current job, key,
  encrypted item metadata and immutable item-attempt sessions from 000068.
- GREEN: exit `0` after reload used the recorded `Keyring.ByVersion` material,
  unwrapped the job DEK, decrypted frozen paths and rejected a wrong raw attempt
  fence without consulting the current active KEK version.

### Purpose/fence-bound stream crypto

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestChunkAEADBindsPurposeFenceNonceAndAuthenticatedTrailer$' \
  -count=1
```

- RED: the stream format did not expose the persisted nonce prefix or bind the
  item/final purpose, attempt fence and authenticated totals trailer.
- GREEN: exit `0` after spool/final HKDF separation, explicit nonce-prefix
  encryption, fence/purpose/object AD and authenticated chunk/byte/digest trailer.

### Durable item-attempt budget

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^(TestAttemptBudgetUsesDurableItemSessionAndThreeRequestLimit|TestAttemptBudgetChargesUnknownReadConservativelyAndFinalizesIdempotently)$' \
  -count=1
```

- RED: the persistent worker had no 000068 reader reservation/finalization path
  bound to the durable item-attempt session.
- GREEN: exit `0` after three-request stat/read/revalidate accounting, ordered
  global/user CAS, physical Provider-byte evidence, conservative unknown charge
  and idempotent finalization.

### Locked encrypted spool/final store

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^(TestExportStoreSealsAuthenticatedItemSpoolWithoutPlaintext|TestExportStoreProcessLockIsExclusiveAndReleasable)$' \
  -count=1
```

- RED: the store lacked a process lock, no-follow reopen and distinct durable
  item-spool/final locators.
- GREEN: exit `0` after exclusive root locking, `.xrs`/`.xre` objects,
  `O_NOFOLLOW`, fsync/rename and authenticated reopen/purge behavior.

### Persistent encrypted pre-header spool

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^(TestPersistentWorkerSpoolsEncryptedBeforeArchiveHeaderWithDurableItemSession|TestPersistentWorkerDiscardsSpoolBeforeHeaderWhenPostReadSourceDrifts)$' \
  -count=1
```

- RED: Provider bytes could not be persisted as an authenticated spool under
  the durable item session before any archive header.
- GREEN: exit `0` after load/stat/sequential read/encrypt/finalize/revalidate/
  fence-reload/fsync/rename/persist ordering; the drift fixture leaves no spool
  or final artifact.

### Current-fence seal and ready publication

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerSealsAndPublishesCurrentFenceWithPartialReport$' \
  -count=1
```

- RED: build failed only because `PersistentWorker.SealArchive`,
  `PersistentSealRequest`, `PersistentWorker.PublishReady` and
  `PersistentPublishRequest` did not exist.
- GREEN: exit `0` after every `.xrs` was fully authenticated before archive
  assembly, archive plaintext streamed through the final AEAD with the persisted
  attempt nonce, the sealed artifact/current projections committed under all
  fences, and a second fenced transaction published ready without releasing
  source leases. A wrong publish fence performs zero mutation.

### Sealed-before-ready restart

The same focused test then received a new restart assertion.

- RED: build failed only because `PersistentWorker.ReconcileJob`,
  `PersistentReconcileRequest` and the closed `published` action did not exist.
- GREEN: exit `0` after a new worker graph reloaded the sealing attempt and
  authenticated artifact from DB/store state, then completed the existing
  fenced ready transaction.

### Restart orphan inventory

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerReconcileOrphansPurgesUnreferencedStageSpoolAndArtifact$' \
  -count=1
```

- RED: build failed only because `PersistentWorker.ReconcileOrphans` did not
  exist.
- GREEN: exit `0` after DB-referenced `.xrs`/`.xre` locators were frozen before
  the locked root inventory removed only unreferenced `.stage`, `.xrs` and `.xre`
  objects, fsyncing the root and rejecting unknown/symlink entries.

### Restart tamper/key-loss revocation

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerRestartRevokesTamperedArtifactAndLostKeyThroughOrderedLifecycle$' \
  -count=1
```

- RED: the persistent worker had no lifecycle dependency or closed `revoked`
  reconciliation action.
- GREEN: exit `0` for both artifact tamper and lost job-key fixtures after
  fail-closed reconciliation entered the already durable order: fence, revoke,
  drain, destroy key/selection, release source/non-store, purge, release store.

### Partial source-owner takeover

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestAttemptCoordinatorTakeoverSourceLeasesOnlyReplacesExpiredOwners$' \
  -count=1
```

- RED: the mixed two-source fixture returned `export attempt fence lost`
  because takeover incorrectly required every source owner to be expired.
- GREEN: exit `0` after active source fences/deadlines were retained byte-for-
  byte while only expired owners received a new Foundation fence. The
  sealed-restart regression also passes when that takeover precedes publication
  and leaves item projections unchanged.

### Versioned encrypted report member

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestWriteArchiveZIPSkipsLinksWithoutReadingAndReportsPartial$' \
  -count=1
```

- RED: the archive contained `xirang-export-report.json`, contradicting the
  approved `xirang-export-report.v1.json` contract.
- GREEN: exit `0` after correcting only the report member constant and both
  focused expectations.

### Permanent 000068 use latch for archive-member first write

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPermanentUseLatchCommitsWithArchiveMemberFirstWriteAndSurvivesPurgeToEmpty$' \
  -count=1
```

- RED: build failed only because the narrow transactional
  `EnsurePermanentUseLatchTx` seam did not exist.
- GREEN: exit `0`; a failed caller transaction leaves no latch, a successful
  archive-member first-write commits it atomically, and deleting the member back
  to an otherwise empty 000068 product leaves exactly the permanent global row.

### Focused Step 5 regression selections

```bash
cd backend
go test -race ./internal/backupasset/export \
  -run 'Worker|Checkpoint|Restart|Reconcile|TTL|GC|Orphan|Fault' -count=1
go test ./internal/backupasset/export -count=1
```

Both commands exited `0`. The first is the approved Step 5 focused race
selector; the second is one package regression. Neither is backend-wide,
frontend, full-project, required PostgreSQL, CI or delivery-gate evidence.

### Collision-safe typed Content/Export delivery mux

```bash
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestContentDeliveryMuxRoutesExactlyOneTypedMatchAndRejectsCollisions$' \
  -count=1
```

- RED: build failed only because the wished-for `newContentDeliveryMux`
  composition seam did not exist. The test requires both typed resolvers to be
  consulted, exactly one matched branch to serve, and zero/double matches to
  return the same `content.ErrContentNotFound` without invoking either server.
- GREEN: exit `0` after the mux consulted both typed branches before serving,
  invoked only the unique match, and collapsed zero/double matches to the same
  closed not-found result.

### Independent 000068 delivery budget and crash accounting

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryBudgetReserveFinalizeReplayAndConservativeCrash$' \
  -count=1
```

- RED: build failed only because the Export-owned delivery budget, reservation
  intents and closed request states did not exist. The DB fixture requires
  exact-intent reserve replay without double charging, different-intent replay
  rejection, idempotent finalize, and conservative full-reservation charging
  when restart reconciliation finds an unfinished request.
- GREEN: exit `0`; `export/delivery.go` now owns its 000068 row-lock/CAS
  counters and request ledger. Exact replay is idempotent, changed replay is
  rejected, finalization is idempotent, and an unfinished reservation is fully
  charged and closed as `reconciled/reconciled_crash` after restart.

### Exact ready-export ticket and cookie binding

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayIssueExportFreezesExactReadyArtifactAndCookieBinding$' \
  -count=1
```

- RED: build failed only because the Export delivery gateway, typed issue
  request/config and artifact digest-of-digests binding did not exist. The test
  requires one ready job/artifact/attempt/key tuple, current session validation,
  exact `asset.export_download` proof, hash-only cookie persistence, bounded
  expiry, and every immutable artifact/key/fence field frozen in 000068.
- GREEN: exit `0`; issuance locks and validates exactly one ready artifact,
  its sealed attempt and active job key, validates the current session and
  purpose, stores only the cookie hash, and freezes a versioned digest-of-
  digests plus all explicit tuple fields. The first post-implementation run
  exposed only a fixture mismatch (`default:true` on direct attempt insert);
  modeling publication's real explicit `is_current=false` transition made the
  same strict predicates pass without weakening them.

### Plaintext Range to authenticated encrypted chunks

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDecryptRangeAuthenticatesArtifactAndSelectedChunksBeforeWrite$' \
  -count=1
```

- RED: build failed only because the range metadata and `DecryptRange` API did
  not exist. The fixture crosses chunk boundaries and also tampers with bytes
  outside the selected records; it requires the full sealed-artifact digest to
  fail before any response byte and selected AEAD chunks to map back to the
  exact plaintext interval.
- GREEN: exit `0`; range delivery first verifies exact ciphertext size/digest,
  header and authenticated trailer before output, then authenticates each
  selected chunk before its bounded plaintext slice is written. Cross-chunk
  slicing is exact, and tampering anywhere in the sealed object produces zero
  response bytes.

### Export gateway authenticated Range serve

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayServesAuthenticatedPlaintextRangeWithIndependentBudget$' \
  -count=1
```

- RED: build failed only because the gateway had no locked Store/ByVersion key
  dependencies, typed delivery resolver, or Serve path. The test creates a real
  encrypted `.xre`, issues a cookie-bound grant, requests a cross-chunk
  plaintext Range, and requires 206 headers/body plus one independently
  reserved/finalized 000068 request and exact physical-byte accounting.
- GREEN: exit `0`; the typed Export branch reloads the full frozen tuple,
  validates session/cookie/key revision, unwraps only the recorded KEK version,
  opens the locked `.xre`, plans plaintext Range with the shared Content
  primitive, and delays 206 commitment until artifact/key authentication.
  The resulting 000068 request closes `succeeded` with plaintext and physical
  ciphertext evidence while its grant CAS returns reserved/in-flight to zero.

### Closed blocked-Range projection and ledger

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayServesAuthenticatedPlaintextRangeWithIndependentBudget$' \
  -count=1
```

- RED: the new multipart-Range assertion received
  `content.ErrContentNotFound`; no 416 response or 000068 blocked request was
  produced. The intended behavior is a safe `bytes */size` response and a
  terminal `blocked/invalid_range` row with zero reservation/in-flight and no
  key load or artifact open.
- GREEN: exit `0`; rejected plans now consume one request-count slot through a
  dedicated 000068 `RecordBlocked` transaction, persist only the closed
  failure code, and return the planner's 416/`bytes */size` response without
  reserving bytes, increasing in-flight, loading a key or opening the Store.

### Metadata-only HEAD delivery

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayServesAuthenticatedPlaintextRangeWithIndependentBudget$' \
  -count=1
```

- RED: the HEAD response and zero-byte ledger were correct, but the recorded
  `ByVersion` call count increased from one to two. This proved HEAD still
  entered key unwrap/Store code despite the frozen metadata-only contract.
- GREEN: exit `0`; after exact grant/session/tuple validation and a zero-byte
  000068 reservation, HEAD now commits only sealed metadata and finalizes the
  request without loading a KEK, unwrapping a DEK, opening `.xre` or emitting a
  body.

### Mid-stream revoke, drain and join

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayServesAuthenticatedPlaintextRangeWithIndependentBudget$' \
  -count=1
```

- RED: a two-chunk request paused after its first authenticated write, but
  logout never exposed the required `draining` state and the test timed out
  waiting for it. The gateway had no active-read registry/cancel/join protocol;
  its direct active-to-revoked update could race an already authorized stream.
- GREEN: exit `0`; GET now registers under grant/request/session while holding
  the same mutex used by revocation. Logout transitions matching grants to
  `draining`, cancels and joins each read through conservative finalization,
  then records `revoked`. The paused fixture emitted exactly its first already
  authenticated chunk, never the second, closed the request `canceled`, and
  returned reserved/in-flight counters to zero before revoke completed.

### Restart convergence for partial delivery revocation

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayRestartReconcilesPendingBudgetAndPartialRevocation$' \
  -count=1
```

- RED: build failed only because `ReconcileDeliveries` did not exist. The
  restart fixture freezes a prior logout at `draining` with one durable pending
  reservation and another active ticket; it requires conservative request
  charging first, then idempotent revocation of every stale authorizing grant.
- GREEN: exit `0` on two consecutive runs; startup reconciliation first closes
  every reserved/streaming request as fully charged `reconciled_crash`, then
  revokes all stale issued/active/draining grants as `process_restarted`.
  Repetition leaves terminal rows and counters unchanged.

### Existing Content issue delegation through the mux

```bash
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestContentDeliveryMuxKeepsExistingTicketIssueOnContentBranch$' \
  -count=1
```

- RED: build failed because the mux had no `Issue` method. The existing backup
  content handler must retain its exact 000066 Broker issuance path while only
  Serve/Revoke gain the typed Export branch.
- GREEN: exit `0`; mux `Issue` is a transparent one-call delegation to the
  existing Content issuer and cannot route ticket creation into Export.

### Safe best-effort Content+Export logout fan-out

```bash
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestContentDeliveryMuxRevokeSessionAlwaysFansOutWithSafeAggregate$' \
  -count=1
```

- RED: build failed only because the composite mux had no `RevokeSession` or
  safe aggregate sentinel. Content-only, Export-only and dual failure fixtures
  require both branches to be attempted exactly once and forbid the raw branch
  errors or session JTI from escaping.
- GREEN: exit `0`; the mux now validates the closed revocation request, always
  invokes Content and Export once in order even after the first error, and
  collapses any partial/dual failure to one stable sentinel with no wrapped raw
  error or identifier.

### Persistent exact-session Export ledger revocation

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayRevokeSessionClosesOnlyExactLedgerBindings$' \
  -count=1
```

- RED: build failed only because `DeliveryGateway.RevokeSession` did not exist.
  The fixture requires every active 000068 grant for one exact JTI to close as
  `revoked/logout`, leaves a foreign session byte-for-byte active, retains the
  typed resolver claim, and denies content after logout.
- GREEN: exit `0`; one bounded set update now closes only issued/active/draining
  grants for the exact JTI, records the closed reason/time and advances each
  version. The delivery ID remains claimed by the Export resolver, so the mux
  cannot fall through to Content, while Serve rejects the revoked row.

### Closed successful-download audit summary

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayRevokeEmitsSuccessfulReadSummaryWithoutRawData$' \
  -count=1
```

- RED: a successful authenticated Range read closed its request ledger, but
  revocation emitted no `export_download` audit summary.
- GREEN: exit `0`; request accounting commits before revocation, and the final
  event contains only owner role, export job, selection digest, item/byte/Range
  counts, format and a closed category. It contains no grant, proof, path,
  cookie, JWT, filename, member, key, locator or raw error.

### Durable audit failure and restart convergence

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayAuditFailurePersistsRetryAndRestartConvergesOnce$' \
  -count=1
```

- RED: Export grants had no durable audit summary/retry state, so a sink failure
  could not preserve both closed request truth and restart work.
- GREEN: exit `0`; revocation and byte/request accounting remain committed,
  the grant records `retry_wait` with capped backoff, restart emits the same
  closed projection, then records `emitted`. A later reconciliation does not
  emit it again.

### Order-independent audit category and full-request Range truth

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^(TestDeliveryAuditAggregatePrecedenceIsOrderIndependent|TestDeliveryGatewayFullRequestLimitAuditDoesNotClaimRange)$' \
  -count=1
```

- RED: reversing request rows changed the aggregate category, and a full GET
  rejected by the byte ceiling incorrectly claimed one Range.
- GREEN: exit `0`; category precedence is explicit and row-order independent,
  while durable `range_requested` distinguishes a real Range header from the
  resolved full interval.

### Runtime installs the exact Export branch into the existing mux

```bash
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestRuntimeInstallsExactExportDeliveryGatewayIntoExistingContentService$' \
  -count=1
```

- RED: build failed only because `Runtime.installExportDeliveryGateway` did
  not exist.
- GREEN: exit `0`; nil and second installs fail, while one exact gateway is
  installed into the optional branch already referenced by `ContentService()`.
  This is only the Step 6 composition seam; Step 8 still owns construction of
  the Export root/settings/worker graph.

### Post-sink receipt ambiguity preserves audit at-least-once

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayAuditReceiptAmbiguityRetriesAtLeastOnceWithoutGrantIdentity$' \
  -count=1
```

- ROOT CAUSE: the frozen Foundation sink commits outside the 000068 grant CAS
  and exposes no idempotency receipt. Adding a grant/delivery identifier to the
  event would violate the approved privacy projection. Exactly-once delivery
  therefore cannot be claimed at this boundary.
- RED: a transient at-most-once preclaim emitted only once after the injected
  post-sink receipt conflict, but could lose the audit if the process stopped
  before invoking the sink.
- GREEN: exit `0` after restoring the safer at-least-once disposition. A
  post-sink receipt conflict leaves the durable summary pending; restart emits
  it again, records `emitted`, and a later reconciliation does not emit a third
  time. The narrow ambiguous window may duplicate a redacted summary but cannot
  silently discard it.

### Paired 000068 delivery audit state constraints

```bash
cd backend
go test ./internal/database \
  -run '^TestBackupAssetMigration068DeliveryAuditReceiptStatesArePaired$' \
  -count=1
```

- RED: both up migrations used a broad non-empty failure-code predicate for
  `retry_wait|failed`, so the DB did not prove those states represented only
  explicit write/reconciliation failures.
- GREEN: exit `0`; SQLite and PostgreSQL both carry `range_requested`, the same
  closed audit states, and restrict retry/failure rows to
  `audit_write_failed|reconciliation_failed`.

### Focused Step 6 verification selections

```bash
cd backend
go test -race ./internal/backupasset/export ./internal/backupasset/content ./internal/api/handlers \
  -run 'ExportDelivery|AssetContent.*Export|Range|Cookie|TLS|Redact|Revoke' -count=1
go test -race ./internal/backupasset/runtime ./internal/api ./cmd/server \
  -run 'ContentDelivery|BackupContent|TrustedProxy|JWTManager.*ContentRuntime|ExportRouteRedacts' -count=1
go test -race ./internal/backupasset/export \
  -run 'Delivery|DecryptRange|CipherRange' -count=1
go test ./internal/database \
  -run '^(TestBackupAssetMigration068SQLite|TestBackupAssetMigration068PairedFiles|TestBackupAssetMigration068DeliveryAuditReceiptStatesArePaired)$' \
  -count=1
```

All four commands exited `0`. Together they are focused Step 6 evidence for
authenticated ciphertext parsing, overflow, GET/HEAD/Range, independent 000068
budget/CAS/replay/crash accounting, exact Export bindings, mux collision,
trusted-proxy scheme policy, dual-ledger fan-out/reconciliation seams and
redacted application/audit logs. They are not backend-wide, required
PostgreSQL, frontend, full-project, CI or final delivery-gate evidence.

### Durable archive-member create replay

```bash
cd backend
go test ./internal/backupasset/processing -run '^TestArchiveMemberCreate' -count=1
```

- RED: the package failed to compile because `NewArchiveMemberService`, its
  durable create DTO and exact index binding did not exist. This was the
  intended missing Step 7 behavior, not an environment or dependency failure.
- Fixture correction: the first compiled run failed closed because the new
  fixture used a 32-character Entry ID while current `AssetRef` requires 64
  lowercase hexadecimal characters. Correcting only that fixture preserved the
  production validation boundary.
- GREEN: exit `0`; same-key/same-intent returns the durable request before a
  second index or any Processing access, changed intent conflicts before index,
  the row stores only the domain-separated member digest plus ordinal, and the
  first archive-member transaction creates the permanent 000068 global latch.

### Succeeded archive index publication remains resolvable

```bash
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestRuntimeArchiveMemberIndexResolverKeepsSucceededNonCurrentJobDeadline$' \
  -count=1
```

- RED: exit `1` with `archive member unavailable`. Child 11 publishes a
  succeeded Processing job with `is_current=false`, while the new runtime
  archive-index resolver required `is_current=true`. The failure exercised the
  intended missing runtime behavior rather than an environment or setup error.
- Fixture correction before the RED: GORM applies the `default:true` tag to a
  zero-value boolean on create, so the fixture explicitly updated the succeeded
  job to `is_current=false` after insertion. This models Child 11's real
  publication state without weakening production validation.
- GREEN: exit `0` after the resolver selected the succeeded non-current
  publication. Its exact Processing deadline remains the archive-member
  request cap; stale/current in-flight jobs are not accepted as published
  indexes.

### Step 7 durable replay, binding and reconciliation regressions

The following regression probes passed against the existing Step 7
implementation and therefore required no additional production change:

- concurrent archive-member idempotency has one durable winner;
- reservation followed by binding mutation writes zero response bytes and
  charges conservatively;
- cookie/action/proof/path/session/subject drift is denied without creating a
  request row, and an Export tuple cannot contaminate a member grant;
- member audit retry state survives restart and converges after a successful
  retry; a reserved request is conservatively reconciled and terminal replay is
  rejected;
- malformed, cross-member, raw-path, size-mismatched and unknown-field Derived
  metadata fail closed;
- member Derived output is never eligible as nested Worker input, and
  sensitivity/malware reauthorization removes the exact Processing interest;
- encrypted, unsupported and generic-limit products expose original download
  only after the existing download authorization succeeds, while denied,
  offline and Content-unavailable probes all return the same closed no-leak
  reason. The two availability probes passed as characterization coverage and
  required no production change.

### Focused Step 7 verification selections

```bash
cd backend
go test ./internal/backupasset/processing/capabilities -run 'Archive' -count=1
go test ./internal/backupasset/processing -run 'Production(Archive|CompressedTAR)' -count=1
go test ./internal/backupasset/content \
  -run 'Archive(Member|Index)|DerivedAttemptResolverNeverUsesArchiveMember' -count=1
go test ./internal/backupasset/processing -run '^TestArchiveMember' -count=1
go test ./internal/backupasset/export -run 'ArchiveMember' -count=1
go test ./internal/backupasset/runtime -run 'ArchiveMember' -count=1
go test -race ./internal/backupasset/processing ./internal/backupasset/content ./internal/api/handlers \
  -run 'Archive(Member|Index|Extract|Nested|Malicious|Audit)' -count=1
go test -race ./internal/backupasset/export ./internal/backupasset/runtime \
  -run 'ArchiveMember' -count=1
```

These are focused Step 7 commands only. They exercise the existing Child 11
archive capability fixtures plus durable member idempotency/replay/conflict,
outer/index/member/Derived binding, one-hop rejection, ratio-limit persistence
and closed projection, exact 000068 member delivery, reconciliation and
redaction. They are not backend-wide, required PostgreSQL, frontend,
full-project, CI or final delivery-gate evidence; a package reporting no tests
matched by a shared `-run` expression is not counted as independent coverage.

## Step 8 Focused TDD Evidence

### Owner-bound Export create/status/cancel and encrypted item cursor

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestExportService(Create|Status|Cancel)' -count=1
```

- RED: the package failed to build because the use-case methods and request/DTO
  contracts did not exist. After their initial production slice was added, the
  same focused command still failed only on the missing closed-status and
  encrypted-cursor helpers, not on environment or fixture setup.
- GREEN: exit `0`; create resolves the explicit selection before the durable
  commit, status is owner-bound and paginated by an opaque AEAD cursor bound to
  the Export Store key version, job ID, selection digest and next ordinal, and
  cross-job cursor use fails closed. Cancellation uses a revision CAS and is
  owner-bound and idempotent. This is focused Step 8 evidence only, not an
  aggregate/backend/full-gate result.

### Strict Export and archive-member handler contracts

```bash
cd backend
TMPDIR=/dev/shm/c12 GOTMPDIR=/dev/shm/c12 \
  go test ./internal/api/handlers \
  -run '^TestBackup(AssetExport|Archive)Handler' -count=1
```

- RED 1: the package failed to compile because
  `NewBackupAssetExportHandler` and `NewBackupArchiveHandler` did not exist.
  This was the intended missing Step 8 handler behavior, not an environment or
  dependency failure.
- RED 2 after the minimal handlers compiled: the Export status request with the
  approved `items_limit` and `items_cursor` query returned 400 before invoking
  the service. Root-cause tracing showed the handler parsed the allowed query
  and then reused a management-only helper that rejects every query string.
- GREEN: exit `0` after the read-request guard was narrowed to reject request
  bodies and transfer encoding without rejecting the already validated paging
  query. The focused tests cover strict create/status/cancel parsing,
  `202 + Location`, owner/no-leak projection and typed audit redaction for the
  new Export and archive-member handlers. This is focused evidence only, not an
  API aggregate, RBAC, Swagger, backend or full-project gate.

### Exact step-up purposes and runtime accessors

```bash
cd backend
TMPDIR=/dev/shm/c12 GOTMPDIR=/dev/shm/c12 \
  go test ./internal/api/handlers \
  -run '^TestBackupAssetExportHandlerCreateRequiresExactExportCreatePurpose$' -count=1
TMPDIR=/dev/shm/c12 GOTMPDIR=/dev/shm/c12 \
  go test ./internal/backupasset/runtime \
  -run '^TestRuntimeExposesCurrentExportAndArchiveMemberServices$' -count=1
```

- Create-purpose RED: missing `asset.export_create`, `asset.download` and
  `asset.export_download` proofs all reached `Service.Create` and returned
  `202`. GREEN: only the exact typed `asset.export_create` proof reaches the
  service; the other three cases return the existing closed step-up response.
- Runtime-accessor RED: the package failed to compile because the public
  Export service, Export delivery and archive-member service accessors did not
  exist. GREEN: current ready manager values are exposed, while a nil runtime
  exposes none.
- The download-ticket purpose/session/Secure-cookie tables passed as
  characterization coverage: Export accepts only `asset.export_download`, and
  member delivery accepts only existing `asset.download`; both reject the
  other purposes before service/delivery access.

### Exact routes, RBAC and generated Swagger

```bash
cd backend
TMPDIR=/dev/shm/c12 GOTMPDIR=/dev/shm/c12 \
  go test ./internal/api \
  -run '^(TestRouterRegistersBackupAssetExportAndArchiveMemberRoutes|TestBackupAssetExportAndArchiveRoutesUseExactRoleAndPermissionMatrix)$' -count=1
TMPDIR=/dev/shm/c12 GOTMPDIR=/dev/shm/c12 \
  go run github.com/swaggo/swag/cmd/swag@v1.16.6 \
  init -g cmd/server/main.go -o internal/api/docs --parseDependency
```

- Route RED: all nine approved Export/archive routes were absent. The first
  wiring run then reproduced a Gin startup panic because the existing
  `/recovery-points/:id` tree cannot register the same wildcard under a second
  name. GREEN: the new routes use the existing `:id` wildcard, while preserving
  the same URL and handler identity semantics.
- RBAC GREEN: every Export route is Admin-only behind
  `backup_assets:export`; archive index/job routes allow only Admin/Operator
  through `backup_assets:preview`; member ticket uses existing
  `backup_assets:download`. Authorized requests against an absent runtime close
  as the same not-found response, while Viewer/unknown roles are rejected.
- The repository `make swag-init` wrapper first exited `127` because the global
  CLI is absent; this was environment setup, not product evidence. The pinned
  module command then produced a genuine annotation RED for the incorrect
  `export.*` alias. After changing annotations to the real `assetexport` alias,
  generation exited `0`. The untracked JSON/YAML outputs were removed because
  the approved repository contract tracks only `docs.go`.

### Step 8 focused race verification

```bash
cd backend
TMPDIR=/dev/shm/c12 GOTMPDIR=/dev/shm/c12 \
  go test -race ./internal/backupasset/export ./internal/backupasset/repository \
  ./internal/backupasset/runtime ./internal/settings ./internal/api/... \
  -run 'AssetExport|ArchiveMember|RBAC|StepUp|Audit|Runtime|Settings' -count=1
```

Exit `0` for Export, Repository, Runtime, Settings, API and handler packages.
This is the planned focused Step 8 verification only; it is not the Step 10
aggregate/backend/frontend/full-project/PostgreSQL gate.

## Step 9 Focused TDD Evidence

### Closed Export/archive API boundaries

```bash
cd web
env -u NODE_ENV npx vitest run \
  src/lib/api/backup-exports-api.test.ts \
  src/lib/api/backup-archive-api.test.ts --coverage=false
```

The initial mapper REDs were semantic assertion failures, not dependency or
environment failures. They proved that the first boundary slice still accepted
closed-state contradictions: `can_cancel` did not follow execution state,
attempt leases/checkpoints/state could disagree with the job, sealed counters
could omit frozen items, ready artifacts could have zero bytes, and empty jobs
were accepted. Later focused REDs proved whitespace-bearing proofs could reach
ticket requests, archive opaque parent digests were incorrectly required to be
member rows, sibling display names could collide, raw path-like display names
were accepted, and member tickets incorrectly inherited Export's single-Range
product. Each case became GREEN through atomic mapper/request rejection or the
exact member `range: "none"` projection; components receive only closed
camelCase products.

### Export/archive controller and workspace behavior

```bash
cd web
env -u NODE_ENV npx vitest run \
  src/features/backup-assets/use-backup-asset-export.test.tsx \
  src/features/backup-assets/use-backup-archive.test.tsx \
  src/features/backup-assets/export-job-panel.test.tsx \
  src/features/backup-assets/archive-member-panel.test.tsx \
  src/features/backup-assets/asset-bulk-bar.test.tsx \
  src/features/backup-assets/asset-preview.test.tsx \
  src/features/backup-assets/backup-assets-workspace.test.tsx \
  src/pages/backups-page.test.tsx \
  src/pages/__tests__/backups-page.a11y.test.tsx --coverage=false
```

The focused REDs established missing Step 9 behavior rather than setup noise:
explicit selection was not yet frozen before asynchronous step-up, a
`cancel_requested` response stopped reconciliation before terminal state, and
polling hidden by page visibility did not resume on `visibilitychange`.
Additional interaction REDs covered authoritative totals replacing estimates,
bounded two-page item rendering, partial per-item truth, quiet TTL updates,
automatic-retry wording without a manual command, fresh non-persisted
Export/member proofs, route push/replace/focus, and encrypted/unsupported/limit
fallback delegation only to the existing original-download action. The UI
contains no saved-search trigger and no Child 13 recovery action.

The final combined focused selection below was rerun from the current tree:

```text
Test Files  11 passed (11)
Tests       183 passed (183)
```

### Frontend aggregate and lazy-bundle evidence

```bash
cd web
env -u NODE_ENV npm run check
node scripts/check-bundle-budget.mjs
```

`npm run check` exited `0`: typecheck and ESLint passed, Vitest reported
`168` files / `1062` tests, and the production build completed. The first
post-implementation bundle probe was a genuine RED: main JS was
`498.14/500.00 KiB`, but main CSS was `105.52/105.00 KiB`. Reusing equivalent
already-emitted utilities in the two new panels reduced the fresh production
artifact without changing behavior. The final budget command exited `0` with
main JS `498.14/500.00 KiB` and main CSS `104.73/105.00 KiB`. The build lists
separate `backup-exports-api`, `backup-archive-api`, `export-job-panel`, and
`archive-member-panel` chunks; none was folded into the eager main chunk.

### Historical browser environment blocker (superseded)

At this historical checkpoint, the required local browser gate was `blocked`,
not passed. A local mock HTTP
listener failed with `listen EPERM: operation not permitted`, and three fresh
Chromium headless attempts (including disabled crash reporter/breakpad/crashpad
variants) exited `133` at Crashpad `setsockopt: Operation not permitted` before
any page could render. Unit/component axe, keyboard, route-focus and responsive
state coverage was GREEN above, but 1440/1200/390 CDP screenshots and real-canvas
inspection were unavailable in that sandbox and were not claimed at this
checkpoint. The later controller-session browser evidence below supersedes this
blocker and records completion of the required local gate.

## Historical Step 10 Cross-Engine And Runtime Checkpoints

### Migration-backed Export behavior fixture

```bash
cd backend
go test ./internal/backupasset/export -run '^TestExportBehaviorSQLite$' -count=1
go test ./internal/database \
  -run '^TestBackupAssetMigration068(SQLite|PairedFiles|DeliveryAuditReceiptStatesArePaired)$' -count=1
```

The first Export SQLite run failed because the new integration assertion
incorrectly compared the job execution cap directly with the later returned
source-lease deadline. The output showed the intended contract precisely: the
job was capped at `created_at + max_duration`, while the source lease retained
its later exact deadline. This was a test-assertion defect and is not recorded
as a product RED. The fixture was corrected to query
`BackupAssetExportSourceLease`, assert its deadline equals the returned lease,
and assert the job cap does not exceed it; the aging fixed calendar timestamp
was also replaced with one captured test-start instant. The fresh Export test
then exited `0`. The focused SQLite migration/paired-state command also exited
`0`. These are focused SQLite results, not PostgreSQL or aggregate evidence.

Required Export mode was independently proved fail closed:

```bash
cd backend
env -u TEST_POSTGRES_DSN REQUIRE_POSTGRES_EXPORT_TEST=1 \
  go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
```

It exited `1` with `TEST_POSTGRES_DSN is required when
REQUIRE_POSTGRES_EXPORT_TEST=1`. This proves missing PostgreSQL cannot be
counted as a skip; it is not PostgreSQL parity evidence.

### Historical archive-member Processing runtime-readiness checkpoint

```bash
cd backend
go test ./internal/backupasset/runtime \
  -run '^TestProcessingRuntimeArchiveMemberCoordinatorRequiresReadyAndRunning$' -count=1
```

- RED: the not-ready case failed because the archive-member adapter returned a
  coordinator as soon as the Processing control plane existed, even when no
  Worker runtime was ready. This was the intended missing behavior, not setup
  noise.
- GREEN: exit `0` after the adapter delegated to the Processing runtime's
  ready/running-gated accessor. A fresh focused regression including disabled,
  no-Worker and ready/stopped cases also exited `0`.

### PostgreSQL CI selector contract

Before the workflow edit, exact assertions for
`REQUIRE_POSTGRES_EXPORT_TEST`, migration `68` in the parity selector and
`TestExportBehaviorPostgres` each exited `1`. The CI job now sets all three
required migration/Processing/Export modes, selects migrations 062 through
068 plus the timestamptz regression, and runs the real Export behavior entry
point. The backend database spec records the same commands while retaining
both `TimestampCodec` and `TimestamptzCodec` ScanLocation requirements.

Before the three test-only amendments, the then-current parsed manifest and
dirty union matched their then-approved scope. The 2026-07-23 controller
amendments added two existing Repository Rsync tests and one existing Provider
preflight test without changing product behavior. At that dated checkpoint,
parity was exactly 8 Phase-1 + 56 create + 49 modify = 113 paths. Staged
remained empty. This is historical Step-10 evidence, not staging or delivery
proof.

### No-aging calendar fixture amendments

The Repository pre-amendment suite produced five genuine
`context deadline exceeded` failures because fixed publication/preflight
instants had aged past current time. The two approved existing fixtures now
capture one `time.Now().UTC().Truncate(time.Second)` instant and derive their
deadlines from it; no assertion or production path was weakened. The focused
Provider amendment applies the same test-start-relative rule to the expiring
preflight evidence. Its first local rerun on the persistent btrfs temporary
root failed closed only because that filesystem reports `FreeInodes=0`; with
both Go build temp and test temp on `/dev/shm`, the unchanged inode/capability
assertion passed.

The new archive-member test file was already inside the 56-create manifest but
still contained nineteen fixed calendar `now/start` values. The static
no-aging gate was the genuine RED:

```bash
! rg -n 'time\.Date\(' \
  backend/internal/backupasset/processing/archive_member_test.go
```

It exited `1` with nineteen matches. Replacing each with a single test-local
`time.Now().UTC().Truncate(time.Second)` preserved the injected-clock and
relative future/past assertions. The same scan then exited `0`, and these
fresh focused/race commands passed:

```bash
go test ./internal/backupasset/processing -run '^TestArchiveMember' -count=1
go test -race ./internal/backupasset/processing -run '^TestArchiveMember' -count=1
go test ./internal/backupasset/provider \
  -run '^TestRsyncTreePreflightBuildsBoundEvidenceFromTrustedRoot$' -count=1
go test -race ./internal/backupasset/provider \
  ./internal/backupasset/repository -count=1
```

The remaining Provider `time.Date` is the non-expiring managed-root
`CreatedAt` codec/layout fixture; Export tests and archive-member tests have no
fixed date literal.

### Aggregate-gate RED/GREEN corrections

The first post-amendment `env -u NODE_ENV npm run check` exposed one genuine
aggregate-only RED in `export-job-panel.test.tsx`: the stable second-page test
expected 200 rendered items but retained 100. The two page fixtures each called
`readyJob()` independently, so their job-level deadline/ready/expiry fields
could differ by a millisecond under aggregate load; the production mapper
correctly rejected that snapshot drift. The test now derives both pages from
one captured job snapshot without weakening the mapper. The focused panel run
passed 6/6 and the fresh full frontend check passed 168 files / 1062 tests plus
typecheck, ESLint and production build; the bundle budget remained unchanged.

The first post-frontend-fix `env -u NODE_ENV make check` then exposed a real
archive-member idempotency race in addition to the known sandbox failures: a
losing SQLite create attempt could observe `database table is locked` on the
immediate replay query while the durable winner committed. A deterministic
callback-injected regression first failed with exactly
`load archive member replay: database table is locked`:

```bash
go test ./internal/backupasset/processing \
  -run '^TestArchiveMemberCreateRetriesTransientReplayLock$' -count=1
```

`loadReplay` now uses the package's reviewed transient database-conflict
classification for a bounded 16-attempt, context-aware scheduler-yield retry;
authorization, index resolution and transaction scope are unchanged. The new
regression plus the durable concurrent-winner test passed, and the complete
`TestArchiveMember` race selection passed. A second fresh
`env -u NODE_ENV make check` had no archive-member/product failure: lint and all
runnable packages passed, and the command stopped only at the already recorded
TCP/Unix listener and seccomp socket sandbox boundaries. Backend build then
exited 0 and the generated `backend/xirang-server` was unlinked and proved
absent. These corrections remain inside existing create-manifest files and do
not change 56 create + 49 modify.

### Historical runnable Step 10 checkpoint

The following local gates passed from the amended tree:

- SQLite paired 068 migration and Export/Processing behavior selectors;
- `-race` Export, Overlay, Content, Runtime, Settings, Provider, Repository,
  archive-member, and focused API/RBAC/handler selections;
- backend lint (`0 issues`), backend build, explicit unlink plus absence proof
  for generated `backend/xirang-server`;
- `env -u NODE_ENV npm run check`: 168 test files / 1062 tests, typecheck,
  ESLint and production build; bundle budget remained JS `498.14/500.00 KiB`
  and CSS `104.73/105.00 KiB`.

The full repository command was run with `NODE_ENV` removed and writable build/
lint caches. It passed backend and frontend lint and all runnable Child 12
packages, then exited `2` in backend-test only because this sandbox forbids
TCP/Unix listeners or the required seccomp socket operation. The affected
existing packages were `alerting`, `api/handlers`, `processing`,
`processing/capabilities`, `processing/updater`, `metrics`, and `uptime`.
This is `blocked_local`, not product failure and not a full-gate pass.

All three required PostgreSQL selectors independently exited `1` with their
exact missing-`TEST_POSTGRES_DSN` fail-closed messages under
`REQUIRE_POSTGRES_MIGRATION_TEST=1`, `REQUIRE_POSTGRES_EXPORT_TEST=1`, and
`REQUIRE_POSTGRES_PROCESSING_TEST=1`. Real PostgreSQL parity, both timestamp
codec scans, browser/CDP and socket/seccomp evidence remain pending required
CI; a missing, skipped or failed CI job cannot authorize merge.

### Historical final-review checkpoint: Export KEK rotation delivery regression

Direct final review traced the persisted-key flow rather than treating a
rotation as a new-artifact-only event: `Keyring.Rotate` demotes the previous
`export_store` key to `verify_only`, `Keyring.ByVersion` continues to return
that usable material, and the persistent worker accepts both `active` and
`verify_only`. The delivery gateway alone rejected the latter, so an otherwise
ready artifact could become unavailable after a legitimate KEK rotation.

The regression was first made RED with a ready artifact and a
`deliveryKeySourceStub` changed to `DomainKeyVerifyOnly`:

```bash
cd backend
GOCACHE="$cache_root/cache" GOTMPDIR="$cache_root/work" TMPDIR="$cache_root/work" \
  go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayServesReadyExportWithVerifyOnlyKEKAfterRotation$' \
  -count=1 -v
```

It exited `1` with `content delivery not found`, proving the missing
delivery-side behavior rather than a setup error. The minimal GREEN change
accepts only `active` or `verify_only` for the exact persisted Export KEK
version; retired/lost/wrong-domain/wrong-version/key-length material remains
closed. The same focused command then exited `0`, followed by:

```bash
cd backend
GOCACHE="$cache_root/cache" GOTMPDIR="$cache_root/work" TMPDIR="$cache_root/work" \
  go test ./internal/backupasset/export -count=1
```

which exited `0`. These are focused final-review regression results only; they
do not replace the aggregate, PostgreSQL, browser or sandbox-required CI gates.

### Delivery-preflight frontend coverage rerun

The canonical `env -u NODE_ENV npm run check` test step uses Vitest's default
`web/coverage` report directory. In this sandbox, its full-suite coverage
provider run reached report collection and then rejected an internal write to
`web/coverage/.tmp/coverage-107.json` with `ENOENT`; no product assertion
failed. A one-file control run generated V8 coverage successfully when its
report directory was redirected outside the working tree. The complete
non-mutating retry used the same test configuration, a writable temporary
directory, one worker, and an isolated external report directory:

```bash
cd web
env -u NODE_ENV NODE_ENV=test TMPDIR="$cache_root/webtmp" \
  ./node_modules/.bin/vitest run --coverage --maxWorkers=1 --no-file-parallelism \
  --coverage.reportsDirectory="$cache_root/vitest-full-coverage"
```

It exited `0` with `168` test files and `1062` tests passed in `127.43s`.
Under the same temporary-directory setting, `env -u NODE_ENV npm run
typecheck`, `env -u NODE_ENV npm run lint`, `env -u NODE_ENV npm run build`,
and `node scripts/check-bundle-budget.mjs` each exited `0`; the resulting
eager bundle was JS `498.14/500.00 KiB` and CSS `104.73/105.00 KiB`. This
isolates the default coverage-report-directory lifecycle as a local sandbox
runner limitation. It is not a substitute for the canonical CI frontend gate,
which remains required before merge.

### Historical final-review checkpoint: archive collision regression

Direct review found that `WriteArchive` previously sanitized components without
NFKC/casefold collision allocation. Two distinct input names could therefore
be emitted as extraction-ambiguous ZIP/TAR members, contrary to the frozen
safe-path contract. The first RED was:

```bash
cd backend
GOCACHE="$cache_root/cache" GOTMPDIR="$cache_root/work" TMPDIR="$cache_root/work" \
  go test ./internal/backupasset/export \
  -run '^(TestWriteArchiveDisambiguatesNFKCCasefoldCollisions|TestSanitizeArchiveComponentsRejectsUnsafeInputs)$' \
  -count=1 -v
```

It exited `1`: `..` was silently rewritten instead of rejected, and fullwidth
`Ａ.txt` plus `a.txt` were emitted as distinct but NFKC/casefold-colliding
members. A second genuine RED proved the Worker discarded the frozen composite
RecoveryPoint/root identity before archive allocation, producing only a local
suffix instead of required cross-root namespacing.

The GREEN implementation stays inside existing Export archive/worker paths:
it rejects invalid/control/default-ignorable/separator/drive components,
normalizes NFKC, bounds long components with a digest suffix, uses the Child 11
canonical NFKC casefold key, sorts deterministically, writes safe member paths
into the encrypted report, adds stable numeric suffixes, and preserves frozen
RecoveryPoint/entry/root identity so cross-root collisions get stable
`rp-<short>/root-<ordinal>` prefixes. Focused GREEN and package regressions:

```bash
go test ./internal/backupasset/export \
  -run '^(TestSanitizeArchiveComponentsRejectsUnsafeInputs|TestWriteArchiveDisambiguatesNFKCCasefoldCollisions|TestWorkerPreservesCompositeRootForArchivePathDisambiguation)$' \
  -count=1 -v
go test ./internal/backupasset/export -count=1
go test -race ./internal/backupasset/export -count=1
```

All three commands exited `0`. These are focused final-review results; the
aggregate and required-CI gates remain distinct.

### Historical final-review checkpoint: reserved archive-report path regression

The same direct path-allocation review found one remaining extraction ambiguity:
the fixed encrypted report member was written after allocation, so a selected
file named `xirang-export-report.v1.json` produced two identical ZIP member
names. The minimal behavior test was first RED:

```bash
cd backend
TMPDIR="$cache_root/work" GOCACHE="$cache_root/cache" \
  go test -v ./internal/backupasset/export \
  -run '^TestWriteArchiveReservesInternalReportPath$' -count=1
```

It exited `1` with duplicate names
`xirang-export-report.v1.json,xirang-export-report.v1.json`, proving the
allocation omission rather than an environment/setup failure. The minimal
GREEN reserves the one internal report path in the same deterministic
NFKC/casefold allocator and makes `writeReport` use that single constant; a
colliding source file is now allocated
`xirang-export-report.v1~1.json`. No raw source name leaves the encrypted
artifact/report boundary, and no manifest path or product contract changed.

The exact regression then exited `0`, followed by a fresh Export package
selection and race run:

```bash
cd backend
TMPDIR="$cache_root/work" GOCACHE="$cache_root/cache" \
  go test ./internal/backupasset/export -count=1
TMPDIR="$cache_root/work" GOCACHE="$cache_root/cache" \
  go test -race ./internal/backupasset/export -count=1
```

Both commands exited `0`. They are focused final-review evidence only; the
required aggregate, PostgreSQL, browser and sandbox CI gates remain distinct.

### Historical delivery-preflight checkpoint (before the Step 11 staging attempt)

The final direct review found one additional in-manifest report-contract gap:
`ArchiveReport` carried item/count/byte state but not the frozen canonical
selection digest required by the PRD. The report is part of the encrypted final
archive, so this is an internal export-artifact binding rather than an API or
audit expansion.

The smallest RED added a ZIP report assertion that proposed the required
`WriteArchive(..., selectionDigest, ...)` boundary and decoded the report
member. It failed at package build with both expected missing-behavior errors:
the writer accepted only five arguments and `ArchiveReport.SelectionDigest` did
not exist. This was a real missing-contract RED, not an environment failure.

The GREEN keeps the digest bound and fail closed throughout the existing
writer/worker boundary: `WriteArchive` rejects a non-canonical digest,
serializes `selection_digest` in the report, `PersistentWorker.SealArchive`
passes its persisted snapshot digest, and the non-persistent Worker request
requires the same value. The report still flows through the existing final
chunked-AEAD stream; no plaintext report, API field, audit field or manifest
path was added.

```bash
cd backend
GOCACHE="$cache_root/cache" TMPDIR=/tmp/c12-tmp \
  go test ./internal/backupasset/export \
  -run '^TestWriteArchiveBindsFrozenSelectionDigestInReport$' -count=1 -v
GOCACHE="$cache_root/cache" TMPDIR=/tmp/c12-tmp \
  go test ./internal/backupasset/export -count=1
GOCACHE="$cache_root/cache" TMPDIR=/tmp/c12-tmp \
  go test -race ./internal/backupasset/export -count=1
```

All three exited `0`. The full backend linter then caught one test-only
`errcheck` cleanup omission; handling the report-stream close error made the
focused linter and the same package/race selections green again.

Historical pre-delivery checkpoint facts after that correction:

- `env -u NODE_ENV npm --prefix web run check` exited `0`: TypeScript,
  ESLint, `168` test files / `1062` tests and production build all passed.
  `node web/scripts/check-bundle-budget.mjs` passed with eager main JS
  `498.14/500.00 KiB` and CSS `104.73/105.00 KiB`.
- `make lint-backend` with an isolated writable `GOLANGCI_LINT_CACHE` exited
  `0` with `0 issues`. `make backend-build` required a non-`/tmp` temporary
  directory because the sandbox `/tmp` linker space was insufficient; it then
  exited `0`, the generated `backend/xirang-server` was unlinked, and absence
  was asserted.
- Each required PostgreSQL selector remained deliberately fail closed without
  a DSN: Migration, Export and Processing each exited `1` with its exact
  `TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_*_TEST=1` message. No
  result is recorded as a PostgreSQL pass or skip.
- The canonical `env -u NODE_ENV make check` was rerun with a writable linter
  cache. In a space-sufficient external temporary directory, lint completed
  and `backupasset/export` passed; the remaining full-suite stops were the
  sandbox's denied TCP/Unix socket and seccomp operations, the fail-closed
  cache-mount verifier, and that external filesystem's `FreeInodes=0`
  capability fact. With `/tmp` the Provider fixture passes, but this sandbox
  applies a disk quota during the aggregate suite. These were local environment
  blockers at that evidence point, not product pass evidence; the later
  controller-session reruns below supersede them.
- Current exact-scope recomputation is `8` Phase-1 + `56` create + `49`
  modify = `113` unique paths, with no expected/actual delta and staged `0`.
  `git diff --check`, untracked whitespace/final-newline checks and Go format
  checks passed. The paired migration scan found exactly four `000068` files;
  `000069`--`000071`, generated binaries, frozen paths and forbidden delivery/
  release paths remain absent/unchanged.

### Historical child-session Step 11 preflight entry and delivery blocker

The approved exact staging command was attempted after the final preflight:

```bash
git add --pathspec-from-file=/tmp/c12-expected
```

It was rejected before staging with:

```text
fatal: Unable to create
'/home/murray/code/xirang/.git/worktrees/xirang3/index.lock': Read-only file system
```

The worktree's `.git` pointer resolves to that shared metadata directory. A
follow-up read-only check found no `index.lock` left behind and `staged=0`; no
partial index mutation occurred. This exact attempt is the sole Step 11 entry:
delivery preflight/staging attempt only, with no index mutation. This session's
filesystem authority permits the worktree contents but not the Git metadata
required for stage, commit, Trellis archive/journal auto-commits, push or PR
delivery. At that evidence point those actions remained `not_executed` and a
session with writable Git metadata was required to resume delivery. At the
later historical Step 10 reopen, that resumption path was superseded and Step 11
became `entered_preflight_then_suspended/no_index_mutation`. The current section
at the end of this ledger supersedes both historical states. The parent remained
`planning` and this Child remained `in_progress`.

### Historical controller-session blocker clearance and local-gate checkpoint

The fully writable controller session superseded the preceding environment
blocker without changing product code or scope. It safely cleared generated Go
and golangci-lint caches that had exhausted the previous runner's quota, then
ran the canonical gate from the repository root:

```bash
env -u NODE_ENV make check
```

It exited `0`. Backend lint reported `0 issues`; all backend packages passed;
the frontend reported `168` test files and `1062` tests passed; backend and
frontend production builds completed. The generated `backend/xirang-server`
was removed and its absence was asserted. This is the fresh local aggregate
evidence that replaces the earlier sandbox-only socket/seccomp/FreeInodes
blocker record; the historical failed attempts above remain execution truth.

The controller also bypassed Docker bridge/veth limitations with one isolated
PostgreSQL 18 container on host-only `127.0.0.1:57432`. It used a dedicated
database and test-only password, touched none of the existing Child 8/10 or
other long-running containers, and ran the exact required selectors:

```bash
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test -v ./internal/database \
  -run 'Test(BackupAssetMigration0(62|63|64|65|66|67|68)Postgres|PostgresTimestamptzScanUsesConfiguredUTC)' \
  -count=1
REQUIRE_POSTGRES_EXPORT_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test -v ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
REQUIRE_POSTGRES_PROCESSING_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go test -v ./internal/backupasset/processing -run '^TestProcessingBehaviorPostgres$' -count=1
```

All exited `0`: migration/UTC scan `78.851s`, Export `1.474s`, and Processing
`7.052s`. The disposable `xirang-child12-pg18` container was then stopped and
auto-removed. Missing-DSN fail-closed evidence above remains a valid negative
contract check, but PostgreSQL parity is now positively proven locally too.

Current CI has no browser, CDP or Playwright job: its frontend job is jsdom
Vitest plus build and bundle checks. The controller therefore completed the
required browser gate locally instead of mislabeling it as pending CI. Chromium
`150.0.7871.181` rendered the real Vite routes/components against a loopback-only
mock API built from the repository's existing synthetic backup fixture. No mock
or screenshot file was added to the repository. Exact checks were:

- `1440x900`: explicit two-item selection opened the Export review dialog;
  viewport mode was `desktop`, document width was `1440/1440`, and scoped real
  `axe-core` reported `0` violations;
- `1200x900`: the ready authoritative Export job rendered all counters, digest,
  per-item results, TTL and commands; viewport mode was `intermediate`, document
  width was `1200/1200`, and scoped axe reported `0` violations;
- `390x844`: the same ready job fit a `358px` dialog with document and panel
  scroll widths equal to their client widths; viewport mode was `mobile`, and
  scoped axe again reported `0` violations;
- `390x844`: the archive-member dialog rendered two safe member display names
  without raw paths or horizontal overflow; scoped axe reported `0` violations;
- eight native Tab events cycled only through Export close/format/profile/create
  controls, and native Escape returned focus to `Export selected`; archive
  Escape returned focus to `Browse archive contents`.

Temporary screenshot hashes were recorded for audit correlation only:

```text
1440 Export review  1e0c64bb3300e52723d2ab05538dd8a74c52da1f426aac527e77377f3b6fcfe5
1200 ready Export   3450605bbdaec938768c269b1a84085394074b1aab193b2521002e5987602ed6
390 ready Export    c3d40d296fe8e160215c0e250b04cecf5fed65ac09a812965d47bbdc0d7c2051
390 archive members a79fb01093d3bc4de09d13c0db7ee9be207850272963abae4ac3923c464e6a3d
```

The temporary Chromium instance, Vite, mock API and PostgreSQL process were all
stopped after the checks; an unrelated pre-existing CDP listener was left
untouched. The approved dirty union remains exactly `8` Phase-1 + `56` create
+ `49` modify = `113`; exact staging, commit, archive, journal, push, PR, CI,
merge and post-merge actions are still `not_executed` at this evidence point.

### Historical controller race rerun and archive-member create retry

The final controller-session focused race command found one genuine product
failure that the earlier aggregate run had not reproduced:

```text
TestArchiveMemberCreateConcurrentIdempotencyHasOneDurableWinner
ensure archive member use latch: database table is locked
```

The request path already retried a transient replay read after a failed create,
but it did not retry the atomic permanent-latch plus member-request transaction
itself. A loser could therefore see a transient SQLite table lock before the
winner's request row existed, find no replay row, and return the storage error.
`TestArchiveMemberCreateRetriesTransientUseLatchLock` deterministically injected
that lock at the first quota-bucket create and failed with the exact error above.

The production boundary now retries the whole latch plus request transaction at
most sixteen times, yields between attempts, and stops immediately on context
cancellation. Only transient lock, deadlock, and serialization errors qualify;
unique-key conflicts still use the existing durable winner/replay path. The
following fresh checks then exited `0`:

```text
go test ./internal/backupasset/processing \
  -run '^TestArchiveMemberCreate(RetriesTransientUseLatchLock|ConcurrentIdempotencyHasOneDurableWinner|RetriesTransientReplayLock)$' -count=1
go test -race ./internal/backupasset/processing -count=1
20 consecutive race runs of TestArchiveMemberCreateConcurrentIdempotencyHasOneDurableWinner
go test -race ./internal/backupasset/export ./internal/backupasset/repository \
  ./internal/backupasset/overlay ./internal/backupasset/processing \
  ./internal/backupasset/content ./internal/backupasset/runtime ./internal/api/... -count=1
```

After that correction, a fresh `env -u NODE_ENV make check` also exited `0`:
backend lint reported zero issues, all backend packages passed, frontend again
reported `168` files and `1062` tests passed, and both production builds
completed. The generated `backend/xirang-server` was unlinked and asserted
absent. The independent bundle budget remained `498.14 / 500 KiB` JavaScript
and `104.73 / 105 KiB` CSS. Because the correction touches the Processing
transaction path, the controller also started one final isolated PostgreSQL 18
instance and reran all three required selectors serially: migrations 062--068
plus UTC scan passed in `36.332s`, Export behavior passed in `0.649s`, and all
five Processing behavior subtests passed in `3.119s`. The container was then
stopped and auto-removed, with no listener left on its host port. This
correction changed only the already approved `archive_member.go`, its test, and
this evidence file; exact scope remains 113.

### Store root identity and descriptor-relative object hardening

The focused Store tranche first reproduced the configured-root replacement
write with no production change:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreRejectsRenamedRootReplacementWithoutWritesOrDeletes/create_staging$' \
  -count=1
--- FAIL: TestExportStoreRejectsRenamedRootReplacementWithoutWritesOrDeletes/create_staging
create staging error=<nil>, want ErrInvalidStore
replacement inventory gained .stage-<opaque>
```

The complete pre-implementation adversarial selector also failed for root
directory/symlink replacement, staging pathname swap, staging hardlinks and
sealed hardlinks. A separate required no-replace RED was:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreSealDoesNotReplaceExistingFinalLocator$' -count=1
--- FAIL: TestExportStoreSealDoesNotReplaceExistingFinalLocator
seal existing final locator error=<nil>
```

That proved the old `os.Rename` overwrote an existing final object. The special
entry RED likewise showed `DiscardStaging` deleting a pathname-swapped empty
directory. The new fixtures snapshot names, type, device, inode, link count and
contents so replacement/external trees must receive zero writes and zero
deletes.

`Store` now retains the root directory descriptor, device, inode and statx
mount ID for its lifetime. Every operation holds the Store mutex across a
fresh no-symlink configured-path identity check, a descriptor-relative
operation and a post-check. Child opens use `openat2` with
`RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS|RESOLVE_NO_XDEV`;
publication uses `renameat2(RENAME_NOREPLACE)`; deletion uses file-only
`unlinkat`; root inventory uses a fresh descriptor, rewinds and loops to EOF;
and mutation completion requires root-directory fsync plus locked absence
inventory. Staging, sealed and lock entries must be regular, same-device,
owner-private and single-link. Filesystem errors retain `ErrInvalidStore` and
the syscall cause while exposing only fixed operation labels, never the
configured root path.

The requested implementation self-audit is explicit:

1. `verifyPublishedEntryLocked` performs a real `Fstat` on the still-open
   staging file after rename and compares its device/inode/link/type against
   both the pre-publish identity and the final no-follow entry; there is no
   synthetic `Fstat(-1)` path.
2. Each `createStaging` post-open error path closes exactly once, while success
   transfers the sole open file handle to the caller; there is no overlapping
   deferred close.
3. If rename succeeds but final identity, directory fsync or post-root
   validation fails, `Seal` closes the descriptor and returns
   `ErrInvalidStore` without rolling back, overwriting or unlinking. The
   ciphertext-only final entry remains an unreferenced orphan for locked
   inventory reconciliation, and the caller receives no locator to publish.

Fresh GREEN evidence after the final self-review:

```text
go test ./internal/backupasset/export -run 'Store|Root|Purge' -count=1
ok  xirang/backend/internal/backupasset/export  0.227s

go test -race ./internal/backupasset/export \
  -run 'Archive|Path|Stream|Crypto|Chunk|Store|Root|Key|Purge' -count=1
ok  xirang/backend/internal/backupasset/export  2.531s

go test ./internal/backupasset/export \
  -run 'TestExportStore(RejectsRenamedRootReplacementWithoutWritesOrDeletes|RejectsRootSymlinkReplacementWithoutExternalMutation|SealRejectsStagingPathnameSwap|SealDoesNotReplaceExistingFinalLocator|SealAndDiscardRejectStagingHardlink|RejectsSpecialEntriesWithoutMutation|RejectsSealedSymlinkWithoutExternalMutation|RejectsSealedHardlinkWithoutExternalMutation|PurgeUnreferencedScansEntireDescriptorInventory)' \
  -count=20
ok  xirang/backend/internal/backupasset/export  0.517s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  1.430s

go vet ./internal/backupasset/export
# exit 0, no output
```

`gofmt -d` is empty. The source scan finds no pathname-based `os.Open*`,
`os.Rename`, `os.Remove` or `os.ReadDir` in `store.go`; its only
`filepath.Join(store.root, ...)` calls only construct/validate the private legacy
test-visible staging path and performs no I/O. The three authorized paths are
currently untracked (`??`) in the shared Child 12 worktree; nothing was staged,
committed, pushed or archived by this tranche.

This implementation intentionally depends on Linux `openat2`, `renameat2` and
statx mount-ID support. Unsupported kernels/filesystems fail closed with
`ErrInvalidStore`; there is no pathname fallback. Concurrent hostile writers
within the private `0700` root remain bounded by the process lock and repeated
identity checks, but Linux still offers no general unlink-by-file-descriptor
primitive, so deployment must preserve the root ownership/privacy contract.

### Anonymous staging and pinned-owner correction

This section supersedes only the named-staging and `renameat2` details in the
preceding historical Store tranche. A focused specification review required the
staging inode to be anonymous until publication. The earlier root-descriptor,
mount-identity, no-follow inventory and sealed-object hardening remains in
force.

The pinned-owner validator was first made genuinely RED before it was added:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreOwnershipValidatorsUsePinnedEffectiveUID$' -count=1
undefined: validExportStoreOwnedRoot
undefined: validExportStoreOwnedRegular
FAIL  xirang/backend/internal/backupasset/export [build failed]
```

After adding only those isolated validators, that selector exited `0`. The
anonymous lifecycle tests were then run against the still-named implementation:

```text
go test ./internal/backupasset/export \
  -run '^(TestExportStoreStagingIsAnonymousAndDiscardOnlyClosesDescriptor|TestExportStoreSealAndDiscardRejectLinkedAnonymousDescriptor|TestExportStoreSealsAnonymousDescriptorWithoutMutatingMaliciousStageEntries|TestExportStorePurgeUnreferencedRejectsMaliciousNamedStageWithoutMutation|TestPersistentWorkerReconcileOrphansPurgesOnlyPublishedSpoolAndArtifact)$' \
  -count=1
--- FAIL: TestExportStoreStagingIsAnonymousAndDiscardOnlyClosesDescriptor
store tree changed: after contained .stage-<opaque>
anonymous staging stat ... Nlink:1
--- FAIL: TestExportStoreSealAndDiscardRejectLinkedAnonymousDescriptor
pre-link staging nlink=1, want 0
linked staging nlink=2, want 1
--- FAIL: TestExportStorePurgeUnreferencedRejectsMaliciousNamedStageWithoutMutation/regular
purge malicious named staging count=2 err=<nil>
--- FAIL: TestPersistentWorkerReconcileOrphansPurgesOnlyPublishedSpoolAndArtifact
after close contained .stage-<opaque>
orphan reconciliation purged=3, want 2
FAIL
```

Those failures were the intended missing behavior: `CreateStaging` still made a
named `nlink=1` inode, external linking raised it to two, orphan reconciliation
counted the named stage, and a regular attacker-created `.stage-*` entry was
accepted and deleted.

The corrected Store now pins `uint32(os.Geteuid())` before any root creation or
mode mutation. Existing root and lock descriptors must match that owner before
`fchmod`; the pinned owner is then revalidated for the held/reopened root,
lock, anonymous staging descriptor, published ciphertext, sealed opens and
inventory. Staging uses exactly descriptor-relative
`O_WRONLY|O_TMPFILE|O_CLOEXEC` with mode `0600`; it intentionally omits
`O_EXCL` and has no named fallback. Creation and pre-publication validation
require `nlink=0`.

Seal fsyncs and revalidates that exact open inode, first calls
`linkat(stagingFD, "", rootFD, locator, AT_EMPTY_PATH)`, and falls back only
when that call returns `ENOENT` to
`linkat(AT_FDCWD, "/proc/self/fd/<fd>", rootFD, locator,
AT_SYMLINK_FOLLOW)`. Every other direct-link error is terminal. Publication
requires the held descriptor and final no-follow entry to match and both have
`nlink=1`; post-link errors never unlink the possible ciphertext orphan and
never return its locator. `DiscardStaging` validates `nlink=0` and closes only.
Any named `.stage-*` inventory entry is now unknown/malicious: Seal ignores it,
while orphan purge rejects the whole inventory before deleting anything.

The post-link directory-sync fault seam was added first without changing Seal;
the new test was genuinely RED because the seam was unused:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreSealPreservesPublishedOrphanWhenDirectorySyncFails$' -count=1
--- FAIL: TestExportStoreSealPreservesPublishedOrphanWhenDirectorySyncFails
post-link sync failure locator="6ca8e0feeec4571f88ad91ed1a00ed7a.xre" err=<nil>
FAIL
```

After wiring only the post-link directory fsync through the seam, the test
exited `0`. Injected `EIO` now returns `ErrInvalidStore`, no locator, closes the
staging descriptor, leaves the exact `nlink=1` ciphertext as a reconciliation
orphan, and does not change an unrelated sentinel.

Modern kernels exercise `AT_EMPTY_PATH` directly, so a second per-Store syscall
seam was added without behavior change and then tested before wiring. Its RED
showed `descriptor link calls=[]`, while every injected `EPERM`, `EACCES`,
`EINVAL`, `EEXIST`, `EXDEV` and `EIO` was ignored and Seal incorrectly
succeeded. After wiring both direct and procfs calls through the seam, the test
proved the exact two-call ENOENT sequence and exactly one call for every
non-ENOENT error.

Fresh final evidence for the anonymous correction:

```text
go test ./internal/backupasset/export \
  -run '^(TestExportStore(OwnershipValidatorsUsePinnedEffectiveUID|StagingIsAnonymousAndDiscardOnlyClosesDescriptor|SealDoesNotReplaceExistingFinalLocator|SealPreservesPublishedOrphanWhenDirectorySyncFails|SealFallsBackToProcDescriptorLinkOnlyAfterENOENT|SealDoesNotFallBackAfterNonENOENTLinkErrors|SealAndDiscardRejectLinkedAnonymousDescriptor|SealsAnonymousDescriptorWithoutMutatingMaliciousStageEntries|PurgeUnreferencedRejectsMaliciousNamedStageWithoutMutation)|TestPersistentWorkerReconcileOrphansPurgesOnlyPublishedSpoolAndArtifact)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.093s

go test ./internal/backupasset/export -run '<same focused selector>' -count=20
ok  xirang/backend/internal/backupasset/export  0.524s

go test -race ./internal/backupasset/export \
  -run 'Archive|Path|Stream|Crypto|Chunk|Store|Root|Key|Purge' -count=1
ok  xirang/backend/internal/backupasset/export  2.422s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  1.361s

gofmt -d internal/backupasset/export/store.go \
  internal/backupasset/export/store_test.go \
  internal/backupasset/export/worker_test.go
# exit 0, no output

go vet ./internal/backupasset/export
# exit 0, no output

golangci-lint run ./internal/backupasset/export/...
0 issues.
```

Purge remains descriptor-validated but necessarily name-based at the final
`unlinkat`. The pinned-euid-owned private `0700` root excludes other UIDs, and
the Store mutex plus advisory lock exclude in-process and cooperating-process
writers. A hostile same-credential writer is process compromise outside this
concurrency contract. Unsupported `O_TMPFILE`, direct/fallback link policy,
procfs, seccomp or filesystem behavior fails closed; there is no named staging
fallback. This correction changed only the four authorized Store tranche paths
and performed no stage, commit, push, archive or task-status transition.

### Store bounded-purge and publication-durability quality pass

The multi-entry purge regression uses an injected `openat2` seam and counts
only descriptors whose device/inode belongs to the test targets. It does not
change `RLIMIT_NOFILE` or rely on process-wide exhaustion. Against the old
all-open implementation it produced the intended behavioral RED:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreMultiEntryPurgeKeepsTargetDescriptorsBounded$' -count=1
--- FAIL: TestExportStoreMultiEntryPurgeKeepsTargetDescriptorsBounded/purge_batch
peak simultaneously open target descriptors=64, want <=1
--- FAIL: TestExportStoreMultiEntryPurgeKeepsTargetDescriptorsBounded/purge_unreferenced
peak simultaneously open target descriptors=64, want <=1
FAIL
```

Both purge paths now prevalidate every candidate without mutation while opening,
revalidating and closing one descriptor at a time. Only after the complete
prevalidation and root check do they reopen, validate, unlink, inspect and close
each target sequentially. Per-entry failures remain accumulated, successful
unlink counts remain exact, and directory fsync, expected-absence inventory and
final root verification still run after mutation attempts. The focused GREEN
was:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreMultiEntryPurgeKeepsTargetDescriptorsBounded$' -count=1
ok  xirang/backend/internal/backupasset/export  0.055s
```

The post-link file-sync seam was likewise added without changing Seal before
the regression. Injecting `EIO` then proved the missing durability step:

```text
go test ./internal/backupasset/export \
  -run '^TestExportStoreSealPreservesPublishedOrphanWhenPostLinkFileSyncFails$' -count=1
--- FAIL: TestExportStoreSealPreservesPublishedOrphanWhenPostLinkFileSyncFails
post-link file-sync failure locator="492bbc23ec738b99e0149ffbf9ba394c.xre" err=<nil>
FAIL
```

Seal now fsyncs the exact linked staging descriptor after link/identity
verification and before the parent-directory fsync. An injected failure returns
`ErrInvalidStore` and no locator, closes the descriptor, skips directory fsync,
preserves the matching `nlink=1` ciphertext orphan and leaves an unrelated
sentinel unchanged. The new file-sync test and prior directory-sync test passed
together in `0.054s`.

`TestExportStoreStagesSealsAndPurgesOpaqueObjects` now registers Store cleanup.
The remaining successful `OpenStore` test paths were audited: early-fatal
cleanup was added to the process-lock, schema-down and root-replacement tests,
and unexpected success in forbidden/symlink root rejection closes the returned
Store before failing. Existing explicit-close assertions remain intact.

Fresh quality-pass gates:

```text
go test ./internal/backupasset/export -run '<quality focused selector>' -count=1
ok  xirang/backend/internal/backupasset/export  0.108s

go test ./internal/backupasset/export -run '<quality focused selector>' -count=20
ok  xirang/backend/internal/backupasset/export  0.867s

go test -race ./internal/backupasset/export \
  -run 'Archive|Path|Stream|Crypto|Chunk|Store|Root|Key|Purge' -count=1
ok  xirang/backend/internal/backupasset/export  2.422s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  1.333s

gofmt -d internal/backupasset/export/store.go \
  internal/backupasset/export/store_test.go \
  internal/backupasset/export/worker_test.go
# exit 0, no output

go vet ./internal/backupasset/export
# exit 0, no output

golangci-lint run ./internal/backupasset/export/...
0 issues.
```

Per the focused review boundary, this pass did not add an `OpenStore` mutating
publication probe and did not split Linux implementation/build-tag files.
Disabled-runtime publication behavior and Linux-only deployment remain
intentionally unchanged. No file was staged, committed, pushed or archived.

### Crypto/AAD and delivery-binding focused correction

The first strict-TDD regression fixes the exact first length-prefixed final
chunk AAD domain and keeps the persisted archive profile raw. Before any
production edit, the focused test observed the old domain:

```text
go test ./internal/backupasset/export \
  -run '^TestChunkAssociatedDataUsesExactCanonicalDomainAndRawProfile$' -count=1
--- FAIL: TestChunkAssociatedDataUsesExactCanonicalDomainAndRawProfile (0.00s)
    crypto_test.go:27: first associated-data field=0000000000000023786972616e672e6261636b75705f61737365742e6578706f72742e6368756e6b2e7631..., want length-prefixed "xirang.export.chunk.v1"
FAIL
```

This is focused behavioral evidence only; aggregate verification follows after
all RED/GREEN cycles in this correction.

The job-DEK envelope test next declared the closed binding API covering export
ID, selection digest, KEK version, and persisted V1 wrap algorithm. With the
old loose `(exportID, version)` API still in production, the expected RED was:

```text
go test ./internal/backupasset/export \
  -run '^TestJobDEKEnvelopeIsExportAndVersionBound$' -count=1
# xirang/backend/internal/backupasset/export [xirang/backend/internal/backupasset/export.test]
internal/backupasset/export/crypto_test.go:123:13: undefined: JobKeyBinding
internal/backupasset/export/crypto_test.go:127:20: undefined: JobKeyWrapAlgorithmV1
internal/backupasset/export/crypto_test.go:129:44: not enough arguments in call to WrapJobDEK
FAIL xirang/backend/internal/backupasset/export [build failed]
```

Selection metadata then received a real cryptographic RED. The regression
opened freshly persisted metadata with the root job DEK and the exact existing
item/job/selection AD; the old implementation incorrectly authenticated it:

```text
go test ./internal/backupasset/export \
  -run '^TestSelectionMetadataUsesBoundHKDFSubkeyInsteadOfRootDEK$' -count=1
--- FAIL: TestSelectionMetadataUsesBoundHKDFSubkeyInsteadOfRootDEK (0.00s)
    crypto_test.go:187: root job DEK opened selection metadata; want a domain-separated subkey
FAIL
```

Create-commit corruption was injected after the job/key inserts but before the
transaction returned. Before the persisted tuple was reloaded, every case
incorrectly committed:

```text
go test ./internal/backupasset/export \
  -run '^TestExportCommitRejectsCorruptedPersistedJobKeyTupleAndRollsBackEverything$' -count=1
--- FAIL: TestExportCommitRejectsCorruptedPersistedJobKeyTupleAndRollsBackEverything
    --- FAIL: .../wrapped_dek: CommitCreate error=<nil>, want ErrCipherTampered
    --- FAIL: .../envelope_nonce: CommitCreate error=<nil>, want ErrCipherTampered
    --- FAIL: .../wrap_algorithm: CommitCreate error=<nil>, want ErrCipherTampered
    --- FAIL: .../kek_version: CommitCreate error=<nil>, want ErrCipherTampered
    --- FAIL: .../selection_digest: CommitCreate error=<nil>, want ErrCipherTampered
FAIL
```

A real worker-produced sealed artifact was then published, ticketed, and read
through the delivery gateway (full GET followed by Range). The old worker
bound `format + ":" + profile` while delivery used the raw persisted profile,
so the integrated path produced the expected RED:

```text
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile$' -count=1
--- FAIL: TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile (0.04s)
    delivery_test.go:1555: serve worker artifact: content delivery not found
FAIL
```

Issue-time ticket issuance next received a KEK/envelope preflight matrix. It
requires one exact `export_store` `ByVersion` lookup, accepts only active or
verify-only 32-byte material, authenticates the recorded full job-DEK envelope,
and permits no grant or success audit for missing/retired/lost/mismatched or
malformed/tampered inputs. Before the production preflight existed, every case
showed that issuance never consulted the key source:

```text
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope$' -count=1
--- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope (0.04s)
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/active (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/verify_only (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/missing (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/retired (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/lost (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/wrong_domain (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/wrong_version (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/malformed_key (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/wrong_key (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/malformed_nonce (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/tampered_nonce (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/malformed_wrapped_DEK (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/tampered_wrapped_DEK (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
    --- FAIL: TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope/wrong_wrap_algorithm (0.00s)
        delivery_test.go:422: ByVersion calls=0, want 1
FAIL
FAIL	xirang/backend/internal/backupasset/export	0.091s
FAIL
```

The next delivery regression mutates one logical field after ticket issue and
reloads the binding. Existing explicit checks already rejected selection,
fence, revision, version, size, and artifact-digest drift, but the artifact-only
digest left ten full-binding fields unprotected. The observed RED was:

```text
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing$' -count=1
--- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing (0.07s)
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/archive_format (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/archive_profile (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/artifact_locator (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/artifact_nonce_tuple (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/artifact_chunk_count (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/KEK_version (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/wrap_algorithm (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/envelope_nonce (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/wrapped_DEK (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
    --- FAIL: TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing/same_revision_valid_rewrap (0.00s)
        delivery_test.go:2361: binding after drift error=<nil>, want ErrNotFound
FAIL
FAIL	xirang/backend/internal/backupasset/export	0.122s
FAIL
```

The sealed-artifact reconciliation regression then changed both persisted job
and artifact chunk sizes while leaving the valid ciphertext/header untouched.
Because `DecryptStream` authenticated but discarded the header chunk size,
restart reconciliation incorrectly published the mismatched artifact:

```text
set -o pipefail
go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerRestartRevokesAuthenticatedHeaderChunkSizeMismatch$' -count=1 2>&1 | \
  sed -n '/--- FAIL/,$p'
--- FAIL: TestPersistentWorkerRestartRevokesAuthenticatedHeaderChunkSizeMismatch (0.04s)
    worker_test.go:1235: header/DB chunk-size reconciliation={Action:published ArtifactID:f712300f6d07273be478b4410799b08c ExpiresAt:2026-07-23 23:34:04 +0000 UTC} err=<nil>
FAIL
FAIL	xirang/backend/internal/backupasset/export	0.093s
FAIL
```

The paired crypto-contract RED declared that both encryption and decryption
must return the written/authenticated header value. The old result type had no
such field:

```text
go test ./internal/backupasset/export \
  -run '^TestCipherResultCarriesAuthenticatedHeaderChunkBytes$' -count=1
FAIL	xirang/backend/internal/backupasset/export [build failed]
FAIL
# xirang/backend/internal/backupasset/export [xirang/backend/internal/backupasset/export.test]
internal/backupasset/export/crypto_test.go:105:15: encrypted.ChunkBytes undefined (type CipherResult has no field or method ChunkBytes)
internal/backupasset/export/crypto_test.go:105:53: decrypted.ChunkBytes undefined (type CipherResult has no field or method ChunkBytes)
internal/backupasset/export/crypto_test.go:106:71: encrypted.ChunkBytes undefined (type CipherResult has no field or method ChunkBytes)
internal/backupasset/export/crypto_test.go:106:93: decrypted.ChunkBytes undefined (type CipherResult has no field or method ChunkBytes)
```

The GREEN implementation now authenticates the full persisted job-DEK envelope
before grant insertion, computes and revalidates the canonical full delivery
binding, carries the authenticated header chunk size through `CipherResult`,
and compares that value with both the frozen job snapshot and artifact during
sealed-artifact authentication/restart reconciliation. The touched ZIP
fixtures use the raw closed `zip_deflate_v1` profile. Issue-time preflight adds
one expected `ByVersion` lookup before the existing serve-time lookup; the
directly affected Range assertions now require both calls.

The first focused lint run exposed one test-only `errcheck` violation at the
callback that intentionally injects a GORM transaction error:

```text
golangci-lint run ./internal/backupasset/export
internal/backupasset/export/service_test.go:202:16: Error return value of `tx.AddError` is not checked (errcheck)
				tx.AddError(test.corrupt(tx))
				           ^
1 issues:
* errcheck: 1
```

The callback now explicitly discards the already-injected aggregate return,
matching the package's existing fault-injection pattern. Its directly affected
regression and the rerun lint were GREEN:

```text
go test ./internal/backupasset/export \
  -run '^TestExportCommitRejectsCorruptedPersistedJobKeyTupleAndRollsBackEverything$' \
  -count=1
ok  	xirang/backend/internal/backupasset/export	0.162s

golangci-lint run ./internal/backupasset/export
0 issues.
```

Fresh final verification for this correction was run locally on 2026-07-23.
These are focused/local package results only: they are not evidence of staging,
commit, push, PR, independent CI, merge, archive, release, or deployment.

```text
go test ./internal/backupasset/export \
  -run '^(TestChunkAssociatedDataUsesExactCanonicalDomainAndRawProfile|TestJobDEKEnvelopeIsExportAndVersionBound|TestSelectionMetadataUsesBoundHKDFSubkeyInsteadOfRootDEK|TestExportCommitRejectsCorruptedPersistedJobKeyTupleAndRollsBackEverything|TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile|TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope|TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing|TestPersistentWorkerRestartRevokesAuthenticatedHeaderChunkSizeMismatch|TestCipherResultCarriesAuthenticatedHeaderChunkBytes)$' \
  -count=1
ok  	xirang/backend/internal/backupasset/export	0.374s

go test ./internal/backupasset/export \
  -run 'Crypto|Chunk|Key|Delivery|Range|Worker|Restart|Reconcile' -count=1
ok  	xirang/backend/internal/backupasset/export	0.990s

go test ./internal/backupasset/export \
  -run '^(TestChunkAssociatedDataUsesExactCanonicalDomainAndRawProfile|TestJobDEKEnvelopeIsExportAndVersionBound|TestSelectionMetadataUsesBoundHKDFSubkeyInsteadOfRootDEK|TestExportCommitRejectsCorruptedPersistedJobKeyTupleAndRollsBackEverything|TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile|TestDeliveryGatewayIssueExportPreflightsRecordedKEKAndJobDEKEnvelope|TestDeliveryGatewayRejectsCanonicalFullBindingDriftBeforeServing|TestPersistentWorkerRestartRevokesAuthenticatedHeaderChunkSizeMismatch|TestCipherResultCarriesAuthenticatedHeaderChunkBytes)$' \
  -count=10
ok  	xirang/backend/internal/backupasset/export	2.664s

go test -race ./internal/backupasset/export \
  -run 'Crypto|Chunk|Key|Delivery|Range|Worker|Restart|Reconcile' -count=1
ok  	xirang/backend/internal/backupasset/export	3.726s

go test ./internal/backupasset/export -count=1
ok  	xirang/backend/internal/backupasset/export	1.844s

gofmt -d internal/backupasset/export/crypto.go \
  internal/backupasset/export/crypto_test.go \
  internal/backupasset/export/service.go \
  internal/backupasset/export/service_test.go \
  internal/backupasset/export/worker.go \
  internal/backupasset/export/worker_test.go \
  internal/backupasset/export/delivery.go \
  internal/backupasset/export/delivery_test.go
# exit 0, no output

go vet ./internal/backupasset/export
# exit 0, no output

golangci-lint run ./internal/backupasset/export
0 issues.

git diff --check
# exit 0, no output
```

The final `git diff --check` covers Git-visible tracked/staged changes; the
owned Export package and this evidence file are currently untracked. The
explicit `gofmt -d` command above covers all eight owned Go/test files. No file
was staged, committed, pushed, archived, or otherwise delivered by this
focused correction.

## PostgreSQL Lock-Order, Context And Idempotency Closure

At this historical checkpoint, the focused PostgreSQL tranche was `closed`
inside the then-reopened Step 10
(controller fresh required `TestExportBehaviorPostgres` PASS `11.527s`), and the
focused crypto/AAD review immediately above is also `closed`; the double review
concluded spec `✅` and quality `APPROVED` with zero findings. It did not at that
time close the then-remaining non-PostgreSQL/deferred lock-order review or
another Step 10 boundary. Current disposition is recorded in the superseding
status and final runnable-gate section.

- The unit RED captured the old lock order as `IA -> I -> A -> J`.
- Real PostgreSQL RED runs reproduced `SQLSTATE 40P01` deadlocks in loader,
  heartbeat, and spool paths.
- The GREEN implementation uses the canonical order
  `Q(global,user) -> J -> A -> I -> IA`.
- Loader cancellation and the safe idempotency-collision fallback are covered.
- The controller-required real-PostgreSQL `TestExportBehaviorPostgres` selector
  passed in `11.527s`.
- The affected package tests, race tests, `go vet`, `gofmt -d`, and
  `git diff --check` passed.
- Focused spec review: `✅`. Focused quality review: `APPROVED`, zero findings.

These historical results are limited to PostgreSQL lock ordering, context
handling, and idempotency. The separately closed archive-profile sub-boundary
was not proven by this tranche. At that checkpoint, spool/cipher sizing, quota
conservation, lifecycle/restart/KEK-loss, runtime/scheduler publication,
non-PostgreSQL/deferred lock ordering and cross-layer/full gates were not proven.

## Closed Archive-Profile And Deterministic Archive Review

At the historical 2026-07-23 checkpoint, this focused archive-profile boundary
was `closed`. The spec
re-review concluded `✅ Spec compliant`, and the independent boundary/security
quality review found zero Critical, Important, or Minor findings. Fresh backend
evidence and the controller-owned NODE_ENV-cleared frontend gate, plus the
bundle budget gate, passed with the evidence below. At that time, spool/cipher
sizing, quota conservation, lifecycle/restart/KEK-loss, runtime/scheduler
publication, lock review and other Step 10 boundaries remained unresolved, and
no staging, commit, push, archive or delivery action was performed. The current
disposition is recorded only in the superseding status and final section.

The implemented contract has one Go owner, `ValidArchiveProfilePair`, for the
three legal pairs:

```text
zip + zip_deflate_v1
tar + tar_none_v1
tar + tar_gzip_v1
```

`WriteArchive` itself now requires the profile. There is no implicit-profile
compatibility API. The same closed pair is validated before selection or
durable work in create/commit/idempotency, when persisted status is projected,
at handler request and response boundaries, during persistent/direct worker
loads and writes, and before delivery issue or serve. The frontend validates
both create requests and mapped job responses against the same matrix.

The paired SQLite/PostgreSQL 000068 up migrations encode the exact matrix.
Delivery metadata is closed as follows:

```text
zip + zip_deflate_v1 -> application/zip   + .zip
tar + tar_none_v1    -> application/x-tar + .tar
tar + tar_gzip_v1    -> application/gzip + .tar.gz
```

`tar_gzip_v1` uses Go gzip level 6 with zero `ModTime` and OS byte `255`.
The TAR writer closes before the gzip writer and both close failures are
preserved with `errors.Join`. The uncompressed TAR path does not place a typed
nil closer behind `io.Closer`. Deterministic byte equality and archive
round-trip coverage exercises ZIP, TAR and TAR.GZ. For every legal profile the
test now opens and reads the regular-file and report members, compares the
regular-file bytes with the source payload, decodes
`xirang-export-report.v1.json`, and compares the complete report, including its
selection digest, result and items, with an independently constructed expected
report. The gzip regression also checks the header and exact Go level-6
encoding.

The frontend ticket MIME allowlist includes exact `application/gzip`, export
create options are required and runtime-validated, and download filenames use
the first 16 job-ID characters plus the profile-derived suffix. The focused
hook tests restore spies between cases and the ticket handoff uses a click spy,
so the final focused run contains no jsdom navigation warning.

No pre-production RED stdout/stderr was preserved in this artifact for the
following archive-profile selectors: the four idempotency/service/status
selectors, deterministic archive bytes/round-trip, layered close ordering,
persistent TAR.GZ propagation, persistent-pair tamper rejection, delivery
MIME/suffix mapping, delivery issue rejection, delivery HEAD rejection, the
handler rejection selector, or the SQLite closed-pair selector shown below.
No pre-GREEN output was preserved here for the frontend MIME allowlist,
required create options, 16-character filename/suffix, or closed request/job
pair cases either. Therefore this section does **not** claim a genuine RED for
any of those cases; the commands below are GREEN regression checkpoints only.

Preserved focused GREEN checkpoint:

```text
cd backend
go test ./internal/backupasset/export -run 'Test(CreateIntentDigestAcceptsOnlyClosedArchivePairs|ExportServiceCreateRejectsInvalidArchivePairBeforeSelectionResolution|ExportCommitCreateRejectsInvalidArchivePairBeforeDurableWork|ExportServiceStatusRejectsInvalidPersistedArchivePair|WriteArchiveProducesDeterministicClosedProfileBytes|CloseArchiveLayersClosesTARBeforeGzipAndJoinsErrors|PersistentWorkerUsesPersistedTARGzipProfileForDeterministicArchiveBytes|PersistentAttemptLoaderRejectsTamperedPersistedArchivePair|ExportDeliveryHeadersUseClosedArchivePairMIMEAndSuffix|DeliveryGatewayIssueExportRejectsClosedStateMatrix|DeliveryGatewayRejectsTamperedArchivePairBeforeHEADServe)$' -count=1
ok   xirang/backend/internal/backupasset/export  0.522s

go test ./internal/api/handlers -run '^TestBackupAssetExportHandlerRejectsInvalidArchivePairBeforeService$' -count=1
ok   xirang/backend/internal/api/handlers  0.058s

go test ./internal/database -run '^TestBackupAssetMigration068SQLite/ArchiveFormatProfilePairIsClosed$' -count=1
ok   xirang/backend/internal/database  0.073s
```

Historical package, race and static GREEN checkpoints recorded before the
formal review follow-up:

```text
cd backend
go test ./internal/backupasset/export -count=1
ok   xirang/backend/internal/backupasset/export  2.220s

go test -race ./internal/backupasset/export -count=1
ok   xirang/backend/internal/backupasset/export  7.996s

go vet ./internal/backupasset/export
# exit 0, no output

gofmt -d internal/backupasset/export \
  internal/api/handlers/backup_content_handler.go \
  internal/api/handlers/backup_content_handler_test.go
# exit 0, no output

git diff --check
# exit 0, no output
```

Historical NODE_ENV-cleared frontend focused checkpoint:

```text
cd web
env -u NODE_ENV node_modules/.bin/vitest run \
  src/lib/api/backup-exports-api.test.ts \
  src/features/backup-assets/use-backup-asset-export.test.tsx \
  --reporter=verbose

Test Files  2 passed (2)
Tests       79 passed (79)
Duration    622ms
```

Historical non-qualifying full frontend checkpoint:

```text
cd web
npm run check

typecheck: exit 0
lint:      exit 0
Test Files  168 passed (168)
Tests       1077 passed (1077)
build:      3214 modules transformed; built in 4.56s
command:    exit 0
```

The bare `npm run check` command above ran in a shell whose ambient
`NODE_ENV=production` can change dependency and test behavior. It is retained
only as a historical local checkpoint and is not the required frontend gate.
Fresh controller-owned qualifying frontend gate on 2026-07-23:

```text
cd web
env -u NODE_ENV npm run check

typecheck: exit 0
lint:      exit 0
Test Files  168 passed (168)
Tests       1077 passed (1077)
build:      exit 0; 3214 modules transformed
command:    exit 0

node scripts/check-bundle-budget.mjs
JS:         498.14 / 500.00 KiB
CSS:        104.73 / 105.00 KiB
command:    exit 0
```

Within code/test sources, the formal round-trip review correction changed only
`backend/internal/backupasset/export/archive_test.go`; this section is the sole
accompanying evidence-file correction. The stronger assertions did not expose
a production failure, so no production file changed. Current post-correction
checks are:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestWriteArchiveProducesDeterministicClosedProfileBytes$' -count=1
ok  	xirang/backend/internal/backupasset/export	0.051s

go test ./internal/backupasset/export -count=1
ok  	xirang/backend/internal/backupasset/export	2.301s

gofmt -d internal/backupasset/export/archive_test.go
# exit 0, no output

git diff --check
# exit 0, no output
```

Archive-profile closure is `closed` on 2026-07-23: spec re-review is
`✅ Spec compliant`; the independent boundary/security quality review found
zero Critical, Important, or Minor findings; the fresh backend evidence,
controller-owned NODE_ENV-cleared frontend gate, and bundle gate above all
passed. The missing genuine RED stdout/stderr remains an explicit
evidence-provenance limitation, and the bare ambient-`NODE_ENV=production`
frontend checkpoint remains non-qualifying.
This historical closure did not at that time resolve spool/cipher sizing, quota
conservation, lifecycle/restart/KEK-loss, runtime/scheduler publication, lock
review or another Step 10 boundary. Staging, commit, push, archive and delivery
were unperformed at that checkpoint.

The final callsite audit found no implicit `WriteArchive` invocation: every
production/test call supplies an explicit profile or a request carrying one.
One direct-DB lifecycle test fixture still contains `ArchiveProfile:
"balanced"`; the full Export package and race run both pass, so it was left
unchanged under the controller's failure-driven fixture instruction. The raw
crypto-store test value `"zip:balanced"` was also intentionally untouched.
Neither value is a production/default profile or an implicit archive-writing
API.

## 2026-07-23 Exact V1 Ciphertext Physical Sizing TDD

This focused tranche closes only the V1 ciphertext physical-size primitive and
its shared range-layout/counter boundary. It does not claim Create reservation,
persisted loader validation, quota reserved-to-used settlement, takeover
cleanup, orphan reconciliation, PostgreSQL parity, aggregate/full-project gates
or delivery completion.

The first genuine RED added the exact layout table, invalid/overflow/counter
matrix, and a real `EncryptStreamWithNonce`/`CipherResult`/buffer/range-layout
cross-check before adding production code:

```text
cd backend
go test ./internal/backupasset/export -run '^TestCiphertextSizeV1' -count=1

internal/backupasset/export/crypto_test.go:125:16: undefined: ciphertextSizeV1
internal/backupasset/export/crypto_test.go:136:14: undefined: ciphertextSizeV1
internal/backupasset/export/crypto_test.go:154:17: undefined: ciphertextSizeV1
internal/backupasset/export/crypto_test.go:179:15: undefined: ciphertextSizeV1
FAIL xirang/backend/internal/backupasset/export [build failed]
```

After the sizing helper and shared metadata/range layout were minimally added,
a second genuine RED isolated the reserved trailer-counter mutation boundary:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestWriteCipherRecordRejectsTrailerCounterBeforeMutation$' -count=1

--- FAIL: TestWriteCipherRecordRejectsTrailerCounterBeforeMutation (0.00s)
    crypto_test.go:215: trailer counter error=<nil>, want ErrArchiveLimit
FAIL xirang/backend/internal/backupasset/export 0.051s
```

The resulting V1 helper implements `K=0` for empty plaintext and otherwise
`K=1+(P-1)/C`, then `S=P+20*K+88` with checked `int64` arithmetic. It rejects
negative plaintext, non-positive chunk size, `K > MaxUint32` and size overflow,
while allowing `K == MaxUint32`. Both full and partial stream reads now reach
the same pre-write record guard: data counters `0..MaxUint32-1` are legal and
counter `MaxUint32` remains reserved for the authenticated trailer. Range
metadata and physical layout reuse the same exact-size calculation.

Fresh GREEN evidence:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^(TestCiphertextSizeV1|TestWriteCipherRecordRejectsTrailerCounterBeforeMutation)' -count=1
ok  xirang/backend/internal/backupasset/export  0.052s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  2.407s

go test -race ./internal/backupasset/export \
  -run 'CiphertextSizeV1|WriteCipherRecord|Chunk|CipherRange' -count=1
ok  xirang/backend/internal/backupasset/export  1.688s

git diff --check
# exit 0, no output
```

### Spec-review correction: V1 chunk-size boundary and width reuse

The independent spec review found that the first helper accepted a chunk size
above the V1 stream maximum, including for empty plaintext. The isolated
review-finding RED was:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestCiphertextSizeV1RejectsOversizedV1Chunk$' -count=1

--- FAIL: TestCiphertextSizeV1RejectsOversizedV1Chunk (0.00s)
    --- FAIL: TestCiphertextSizeV1RejectsOversizedV1Chunk/empty (0.00s)
        crypto_test.go:172: ciphertextSizeV1(0, oversized chunk) error=<nil>, want ErrArchiveLimit
    --- FAIL: TestCiphertextSizeV1RejectsOversizedV1Chunk/nonempty (0.00s)
        crypto_test.go:172: ciphertextSizeV1(1, oversized chunk) error=<nil>, want ErrArchiveLimit
FAIL xirang/backend/internal/backupasset/export 0.051s
```

The correction introduces one `maxCipherChunkBytesV1` boundary and validator
shared by sizing, encryption, decryption and range metadata. It also removes
independent header/trailer/record-width arithmetic from the stream and range
layout paths in favor of the V1 width constants.

Fresh correction GREEN evidence:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^(TestCiphertextSizeV1|TestWriteCipherRecordRejectsTrailerCounterBeforeMutation)' -count=1
ok  xirang/backend/internal/backupasset/export  0.051s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  2.231s

go test -race ./internal/backupasset/export \
  -run 'CiphertextSizeV1|WriteCipherRecord|Chunk|CipherRange' -count=1
ok  xirang/backend/internal/backupasset/export  1.679s
```

After the final width-constant reuse refactor, the immutable focused rerun was:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^(TestCiphertextSizeV1|TestWriteCipherRecordRejectsTrailerCounterBeforeMutation)' -count=1
ok  xirang/backend/internal/backupasset/export  0.051s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  2.222s

go test -race ./internal/backupasset/export \
  -run 'CiphertextSizeV1|WriteCipherRecord|Chunk|CipherRange' -count=1
ok  xirang/backend/internal/backupasset/export  1.659s

git diff --check
# exit 0, no output
```

### Create exact final-plus-spool reservation

On 2026-07-23 a focused service tranche replaced the one-item plaintext
reservation approximation with the exact encrypted peak for the frozen
selection. This scope covers `CommitCreate` and its service-config boundary
only; it does not claim persisted loader validation, quota reserved-to-used
settlement, takeover/orphan cleanup, settings/SQL cross-field constraints, or
any broader Step 10 closure.

The first genuine RED used the real `CommitCreate` harness. It required both
global and user buckets plus their two store-reservation rows to equal
`MaxCiphertextBytes + sum(ciphertextSizeV1(regular.LogicalSize, ChunkBytes))`.
The selection contained two non-empty regular files, one zero-byte regular file
and directory/link/special entries; only regular files contribute, including
the exact 88-byte empty V1 ciphertext. A second test required a regular item
above `MaxItemBytes` to fail before any source lease or durable/quota row:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^(TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays|TestExportCommitCreateReservesExactFinalAndRegularItemCiphertext|TestExportCommitCreateRejectsRegularItemOverMaxBeforeDurableWork)$' \
  -count=1 -v

--- FAIL: TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays (0.03s)
    service_test.go:102: invalid store reservation: {... ReservedStoreBytes:4194304 ...}
--- FAIL: TestExportCommitCreateReservesExactFinalAndRegularItemCiphertext (0.03s)
    service_test.go:205: store reservation=4194304, want exact final+spools 3211631
--- FAIL: TestExportCommitCreateRejectsRegularItemOverMaxBeforeDurableWork (0.02s)
    service_test.go:230: CommitCreate error=<nil>, want ErrSelectionLimit
FAIL
FAIL xirang/backend/internal/backupasset/export 0.140s
```

The isolated service-config RED then proved that the old plaintext-only
cross-field check accepted a user store quota that could not hold the maximum
regular item in its V1 ciphertext form:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestValidServiceConfigRequiresFinalPlusMaxItemCiphertext$' -count=1 -v

--- FAIL: TestValidServiceConfigRequiresFinalPlusMaxItemCiphertext (0.03s)
    service_test.go:263: plaintext-only boundary=4194304 accepted below exact encrypted boundary=4194712
FAIL
FAIL xirang/backend/internal/backupasset/export 0.082s
```

The GREEN implementation computes the checked selection peak after idempotency
replay lookup but before key generation, source lease acquisition, quota
mutation or durable Export writes. It reuses the V1 physical-size helper for
every regular item, accepts zero logical bytes, rejects a regular item above its
frozen per-job maximum, and propagates counter/arithmetic overflow closed. The
same exact value feeds both quota buckets and both store reservation rows. A
replay returns the committed result before this path and the test proves bucket
and reservation totals remain unchanged. `validServiceConfig` now also requires
the user store boundary to hold the configured final maximum plus the exact V1
ciphertext size of `MaxItemBytes`; the exact boundary is accepted and the old
plaintext-only boundary is rejected.

Fresh focused, package and race evidence:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^(TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays|TestExportCommitCreateReservesExactFinalAndRegularItemCiphertext|TestExportCommitCreateRejectsRegularItemOverMaxBeforeDurableWork|TestValidServiceConfigRequiresFinalPlusMaxItemCiphertext)$' \
  -count=1 -v
PASS
ok   xirang/backend/internal/backupasset/export 0.154s

go test ./internal/backupasset/export -count=1
ok   xirang/backend/internal/backupasset/export 2.331s

go test -race ./internal/backupasset/export \
  -run '^(TestExportCommitZeroDeadlinePersistsExactLeaseAndReplays|TestExportCommitCreateReservesExactFinalAndRegularItemCiphertext|TestExportCommitCreateRejectsRegularItemOverMaxBeforeDurableWork|TestValidServiceConfigRequiresFinalPlusMaxItemCiphertext)$' \
  -count=1
ok   xirang/backend/internal/backupasset/export 1.756s
```

### Settings exact V1 max-item spool boundary

On 2026-07-23 the focused settings tranche replaced the plaintext
`max_ciphertext_bytes + max_item_bytes` global-store inequality with the exact
V1 physical ciphertext size of the maximum item. This tranche is limited to
the settings cross-field contract; it does not claim Export selection
reservation, loader, quota settlement, takeover/orphan cleanup, SQL, or broader
Step 10 closure.

The test-only RED used independent frozen V1 layout constants and exercised
both registry chunk boundaries. The old plaintext-only boundary was accepted
for both 64 KiB and 8 MiB chunks:

```text
cd backend
go test ./internal/settings \
  -run '^TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary$' -count=1

--- FAIL: TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary (0.00s)
    --- FAIL: TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary/65536 (0.00s)
        service_test.go:519: plaintext-only spool boundary must not satisfy the V1 ciphertext reservation
    --- FAIL: TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary/8388608 (0.00s)
        service_test.go:519: plaintext-only spool boundary must not satisfy the V1 ciphertext reservation
FAIL
FAIL  xirang/backend/internal/settings  0.005s
FAIL
```

The GREEN settings helper is deliberately local, avoiding a reverse dependency
from `settings` into the Export feature package. It freezes
`K=0` for empty plaintext and otherwise `K=1+(P-1)/C`, then
`S=P+20*K+88`, validates the 64 KiB through 8 MiB registry chunk interval,
allows `K == MaxUint32`, and fails closed for counter or checked-`int64`
overflow. Global store quota now must cover `max_ciphertext_bytes + S` while
the existing user-store/global ordering remains unchanged. Direct helper tests
cover zero, exact and partial chunks, both chunk bounds, the uint32 limit, and
invalid/overflow inputs.

Fresh focused, package, race, format and diff evidence:

```text
cd backend
go test ./internal/settings \
  -run '^(TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary|TestBackupAssetExportCiphertextSizeV1SettingsContract)$' \
  -count=1
ok  xirang/backend/internal/settings  0.004s

go test ./internal/settings -count=1
ok  xirang/backend/internal/settings  0.081s

go test -race ./internal/settings \
  -run '^(TestBackupAssetExportStoreQuotaUsesExactV1SpoolBoundary|TestBackupAssetExportCiphertextSizeV1SettingsContract|TestBackupAssetExportCrossSettingBoundaries)$' \
  -count=1
ok  xirang/backend/internal/settings  1.025s

gofmt -w internal/settings/service.go internal/settings/service_test.go
git diff --check -- backend/internal/settings/service.go backend/internal/settings/service_test.go
# exit 0, no output
```

### Persistent loader frozen-selection and job-limit tamper closure

On 2026-07-23 a focused loader tranche closed persisted job/item tampering at
the stable post-key-lookup reload boundary. This tranche is limited to the
worker loader snapshot; it does not inspect quota reservations, change spool or
SQL behavior, or claim quota settlement, takeover/orphan cleanup, runtime, or
broader Step 10 closure.

The genuine table-driven RED applied successful SQL updates after a real create
and attempt claim. The old loader accepted every changed job/item tuple and
returned a usable snapshot:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits$' \
  -count=1

--- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits (0.19s)
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/regular_item_exceeds_max_item_bytes (0.03s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/item_logical_size_changes_digest (0.03s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/item_type_changes_digest (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/max_items_below_persisted_count (0.01s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/max_source_points_below_persisted_sources (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/max_logical_bytes_below_persisted_aggregate (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/invalid_chunk_bytes (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/max_provider_bytes_below_persisted_logical_aggregate (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/invalid_max_ciphertext_bytes (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
    --- FAIL: TestPersistentAttemptLoaderRejectsTamperedPersistedSelectionAndLimits/peak_store_sizing_overflows (0.02s)
        worker_test.go:944: tampered persisted selection/limits error=<nil>, want ErrUnavailable
FAIL
FAIL  xirang/backend/internal/backupasset/export  0.250s
FAIL
```

The GREEN path keeps the existing two-load security tuple and recorded-version
job-key unwrap semantics. Only after the second stable load does it decrypt all
persisted path components, reconstruct every `FrozenItem`, call
`FreezeSelection` with the persisted `MaxItems`, `MaxSourcePoints` and
`MaxLogicalBytes`, and require exact digest, item count and canonical item order.
Regular files, including zero-byte files, are checked against the persisted
`MaxItemBytes`. Persisted provider/cipher/chunk cross-fields fail closed, and
the existing `createPeakStoreBytes` helper closes per-spool counter and checked
aggregate overflow without duplicating V1 sizing arithmetic. The returned
snapshot now carries `MaxItemBytes`; the recorded-key-version loader regression
uses a zero-byte regular item and proves that boundary remains accepted.

Fresh focused, package, race, format and diff evidence:

```text
cd backend
go test ./internal/backupasset/export \
  -run 'PersistentAttemptLoader|PersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions|PersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.407s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  2.416s

go test -race ./internal/backupasset/export \
  -run 'PersistentAttemptLoader|PersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions|PersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence' \
  -count=1
ok  xirang/backend/internal/backupasset/export  2.615s

gofmt -w internal/backupasset/export/worker.go internal/backupasset/export/worker_test.go
git diff --check -- backend/internal/backupasset/export/worker.go \
  backend/internal/backupasset/export/worker_test.go \
  .trellis/tasks/07-22-backup-assets-export-archive/research/implementation-evidence.md
# exit 0, no output
```

#### Review correction: frozen archive ciphertext boundary

On 2026-07-23 the independent spec review found one Important gap in the
loader closure above: a positive persisted `MaxCiphertextBytes` could pass even
when it was below the approved frozen archive boundary. The isolated test used
the real persisted job, changed only that column to the positive exact boundary
minus one, and reproduced the acceptance:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentAttemptLoaderEnforcesFrozenCiphertextArchiveBoundary$' \
  -count=1

--- FAIL: TestPersistentAttemptLoaderEnforcesFrozenCiphertextArchiveBoundary (0.03s)
    worker_test.go:982: ciphertext boundary-1 error=<nil>, want ErrUnavailable
FAIL
FAIL  xirang/backend/internal/backupasset/export  0.084s
FAIL
```

The corrected loader now checks, with guarded `int64` multiplication and
addition, the exact persisted inequality
`MaxCiphertextBytes >= MaxLogicalBytes + MaxItems*1024 + 64*1024*1024`.
Counter-conversion or arithmetic overflow fails closed. The same test then
updates the column to the exact boundary and proves the snapshot loads with
that exact frozen value. Worker-local service fixtures now persist the same
approved boundary and sufficient test quota, so loader/worker regressions do
not rely on the old 3 MiB unit-only fixture.

Fresh correction verification:

```text
cd backend
go test ./internal/backupasset/export \
  -run 'PersistentAttemptLoader|PersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions|PersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.428s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  2.425s

go test -race ./internal/backupasset/export \
  -run 'PersistentAttemptLoader|PersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions|PersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence' \
  -count=1
ok  xirang/backend/internal/backupasset/export  2.678s
```

### Persistent loader exact store-reservation binding

On 2026-07-23 a focused loader tranche bound every running/sealing worker
snapshot to the two durable store reservations created for its frozen
selection. This scope is limited to `PersistentAttemptLoader`; it does not
change quota settlement, bucket aggregate accounting, SQL migrations,
takeover/orphan cleanup, spool behavior or runtime orchestration.

The first genuine table-driven RED used the real create-and-claim harness and
performed six SQL-legal mutations. The old loader did not read quota rows, so
it returned a usable snapshot for every tamper:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentAttemptLoaderRejectsTamperedStoreReservationsAndBuckets$' \
  -count=1 -v

--- FAIL: TestPersistentAttemptLoaderRejectsTamperedStoreReservationsAndBuckets (0.15s)
    --- FAIL: .../store_reservation_amount_changes
        worker_test.go:1041: tampered persisted store reservation error=<nil>, want ErrUnavailable
    --- FAIL: .../store_reservation_is_missing
        worker_test.go:1041: tampered persisted store reservation error=<nil>, want ErrUnavailable
    --- FAIL: .../store_reservation_is_purge_pending
        worker_test.go:1041: tampered persisted store reservation error=<nil>, want ErrUnavailable
    --- FAIL: .../store_reservation_lease_owner_changes
        worker_test.go:1041: tampered persisted store reservation error=<nil>, want ErrUnavailable
    --- FAIL: .../store_reservation_carries_cipher_bytes
        worker_test.go:1041: tampered persisted store reservation error=<nil>, want ErrUnavailable
    --- FAIL: .../user_bucket_subject_changes
        worker_test.go:1041: tampered persisted store reservation error=<nil>, want ErrUnavailable
FAIL
FAIL  xirang/backend/internal/backupasset/export  0.212s
FAIL
```

A second genuine RED used the existing recorded-version key-lookup barrier.
Concurrent key rewrap plus either reservation-amount drift or user-bucket
identity drift was incorrectly treated as a safe key-only change; both cases
opened the source and persisted an encrypted spool instead of failing closed:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence/(store_reservation|store_bucket_identity)$' \
  -count=1 -v

--- FAIL: TestPersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence (0.07s)
    --- FAIL: .../store_reservation
        worker_test.go:1199: security tuple drift result={...} err=<nil>, want ErrUnavailable
    --- FAIL: .../store_bucket_identity
        worker_test.go:1199: security tuple drift result={...} err=<nil>, want ErrUnavailable
FAIL
FAIL  xirang/backend/internal/backupasset/export  0.130s
FAIL
```

Each stable-load transaction now locks the immutable `id/scope/subject`
projection of every bucket referenced by the current job's complete `store`
reservation set, then locks the complete reservation rows. After the recorded
key version unwraps, the loader reuses the validated frozen selection to
recompute the exact final-plus-spools peak. It requires exactly one active
global/global row and one active user/canonical-owner row, each with exact
job/bucket/lease-owner/deadline binding, nil attempt/release fields, zero
non-store counters and the same positive exact peak. Missing, extra, wrong
amount/state/scope/subject/owner/deadline and other-byte shapes all fail closed.

Production includes complete reservation rows and bucket identity tuples in
both stable equality and the key-rewrap-only predicate. The original barrier
RED/GREEN above exercises representative reservation-amount and bucket-identity
drift; it is not a claim that every reservation field was independently raced.
Mutable bucket aggregate counters, timestamps and transition revision are
deliberately neither projected nor compared: a positive barrier regression
changes them during a valid rewrap and still loads the new key version, so
another job's quota activity cannot create a false security-tuple conflict.

Fresh focused, package, race, format and diff evidence:

```text
cd backend
go test ./internal/backupasset/export \
  -run 'PersistentAttemptLoader|PersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions|PersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.922s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  3.184s

go test -race ./internal/backupasset/export \
  -run 'PersistentAttemptLoader|PersistentWorkerReloadsAttemptWithRecordedKeyVersionAndItemSessions|PersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence' \
  -count=1
ok  xirang/backend/internal/backupasset/export  3.954s

gofmt -d backend/internal/backupasset/export/worker.go \
  backend/internal/backupasset/export/worker_test.go
git diff --check -- backend/internal/backupasset/export/worker.go \
  backend/internal/backupasset/export/worker_test.go \
  .trellis/tasks/07-22-backup-assets-export-archive/research/implementation-evidence.md
# exit 0, no output
```

#### Review coverage correction: SQL-legal terminal and canonical-owner states

On 2026-07-23 an independent spec review found test-coverage gaps, not a new
production failure. The stable tamper table now includes the SQL-legal user
subject `096` for owner 96, coherent released and expired rows, and an extra
released historical row with valid bucket/job/lease bindings. The historical
case replaces the earlier active clone, which the real 000068 partial unique
index would reject before the loader could observe it. The rewrap barrier also
now exercises a complete reservation-row state change from `active` to
`purge_pending`; the existing shared assertions prove `ErrUnavailable`, zero
source opens, an unchanged store tree and an unchanged pending item attempt.

Both additions passed on their first execution against the already-correct
production validation, so this is recorded as coverage-first evidence rather
than a fabricated RED:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentAttemptLoaderRejectsTamperedStoreReservationsAndBuckets$' \
  -count=1 -v

--- PASS: TestPersistentAttemptLoaderRejectsTamperedStoreReservationsAndBuckets (0.39s)
    --- PASS: .../extra_historical_store_reservation_exists
    --- PASS: .../store_reservation_is_released
    --- PASS: .../store_reservation_is_expired
    --- PASS: .../user_bucket_subject_is_noncanonical
PASS
ok  xirang/backend/internal/backupasset/export  0.448s

go test ./internal/backupasset/export \
  -run '^TestPersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence$/^store_reservation_state$' \
  -count=1 -v

--- PASS: TestPersistentWorkerRejectsSecurityTupleDriftBeforeSpoolPersistence (0.03s)
    --- PASS: .../store_reservation_state (0.03s)
PASS
ok  xirang/backend/internal/backupasset/export  0.089s
```

## 2026-07-24 Lifecycle source-release correctness correction

This is focused lifecycle evidence only. It does not claim package-wide or
Step 10 completion.

### Source rows before every Foundation access

```bash
cd backend
ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export \
  -run '^TestExportBehaviorPostgres$/LifecycleLockOrder/ReleaseSourcesBeforeFoundationLease$' \
  -count=1 -v
```

- Earlier old-order RED: exit `1`; while the exact Export source row was held,
  the independent Foundation `FOR UPDATE NOWAIT` probe failed with typed
  PostgreSQL SQLSTATE `55P03` (`lock_not_available`). The failing assertion was
  `PostgreSQL releaser ... touched Foundation lease ... before locked source ...`.
- Strengthened reconciliation-eligible RED against the first implementation:
  exit `1`, package `1.230s`. The target Foundation and Export source absolute
  deadlines were set to the injected `now` without changing attempt/fence/
  owner binding. `pg_blocking_pids` proved the releaser blocked on the held
  source row, but the third-PID probe observed the target Foundation tuple
  change from `status=active` to `status=expired`. The exact failing assertion
  was `PostgreSQL pre-lock reconciliation mutated the target Foundation lease
  before the ordered source lock`.
- GREEN after removing global pre-lock reconciliation: exit `0`, package
  `1.251s`. The third backend NOWAIT probe locks and observes the complete
  Foundation tuple unchanged while the releaser is source-blocked. After the
  holder releases, the same source-first transaction changes only Foundation
  `status/updated_at` and commits both the Foundation lease and Export source as
  `expired`; Foundation `released_at` remains nil.

#### Coverage-first query-attempt trace correction

A subsequent quality review observed that held-lock and tuple probes alone
could miss a pre-source nonlocking Foundation read or a Foundation lock acquired
and released before the probe. No new production RED was found or fabricated.
The PostgreSQL barrier now tags only the releaser context and records GORM query
attempts for `backup_asset_export_source_leases` and `recovery_point_leases`
under a race-safe ordered trace. It requires the source `FOR UPDATE` attempt
before `pg_blocking_pids` evidence, proves no Foundation query attempt occurred
while the releaser is source-blocked, and after joining the workers requires the
first Foundation `FOR UPDATE` attempt to follow the source attempt. Cleanup
cancels and opens gates, joins all started workers with a bounded wait, and only
then removes the callback.

The strengthened proof passed on its first execution against the already-correct
production; the genuine RED results above remain unchanged:

```text
exact PostgreSQL source-release barrier, first coverage execution
ok  xirang/backend/internal/backupasset/export  1.191s

exact PostgreSQL source-release barrier, -count=10
ok  xirang/backend/internal/backupasset/export  11.365s

exact PostgreSQL source-release barrier, -race -count=1
ok  xirang/backend/internal/backupasset/export  2.803s

full required TestExportBehaviorPostgres
ok  xirang/backend/internal/backupasset/export  5.820s
```

Terminal-source coverage was RED because `released|expired` Export source rows
still invoked global Foundation reconciliation and returned its injected
failure. It is GREEN after narrowing the local lease interface and skipping all
Foundation query/update/release/takeover paths for terminal rows:

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentLifecyclePortReleaseSourcesTerminalRowsSkipFoundationAccess$' \
  -count=1 -v
# GREEN: exit 0; released and expired subtests pass
```

### Preserved infrastructure and typed lease errors

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentLifecyclePortReleaseSourcesPreservesUnderlyingErrors$' \
  -count=1
```

- RED: Foundation query failures, `context.Canceled`, and typed `TakeoverTx`
  errors such as `backupasset.ErrLeaseDeadlineExceeded` were flattened to
  `ErrAttemptFenceLost`. Typed `ReleaseTx` errors already preserved
  `errors.Is` identity before this tranche; the new assertion is regression
  coverage, while current `%w` wrapping adds operation context without breaking
  that identity.
- GREEN: exit `0`, package `0.226s`. Foundation query, context-cancellation,
  typed takeover, and exact-expiry update errors retain `errors.Is` identity;
  typed release errors remain preserved with operation context, while a
  zero-row expiry CAS remains `ErrAttemptFenceLost` and rolls both rows back.

### ID-only unfinished-attempt materialization

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentLifecyclePortFenceAttemptsLocksAndCASesObservedLineage$/unfinished_children_use_bounded_update_batches$' \
  -count=1
```

- RED: the old full-model lock query failed the focused observation with
  `itemAttemptSelectsOnlyID:false`.
- GREEN: exit `0`, package `0.098s`; the ordered full set remains locked with
  an ID-only projection and exact update batches `[400, 1]`.

Final sequential focused verification:

```text
go test ./internal/backupasset/export -run '^(TestLifecycle|TestPersistentLifecyclePort)' -count=1
ok  xirang/backend/internal/backupasset/export  0.684s

go test -race ./internal/backupasset/export -run '^(TestLifecycle|TestPersistentLifecyclePort)' -count=1
ok  xirang/backend/internal/backupasset/export  3.468s

go test ./internal/backupasset/export -run '^TestExportBehaviorSQLite$' -count=1
ok  xirang/backend/internal/backupasset/export  0.152s

required PostgreSQL source-release barrier
ok  xirang/backend/internal/backupasset/export  1.251s

required TestExportBehaviorPostgres
ok  xirang/backend/internal/backupasset/export  5.972s

gofmt -w <three owned Go files>
git diff --check
# exit 0, no output
```

Unresolved later-tranche RED retained: the standalone
`TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile` still
fails at `delivery_test.go:1655` with `err=export unavailable`. Its worker has
no lifecycle/source coordinator attached and the failure occurs in persistent
attempt reload before either corrected lifecycle method is invoked. It was not
rerun or changed in this correction, and no full-package completion is claimed.

## 2026-07-24 Bounded lifecycle reconciliation tombstone correction

This is focused lifecycle evidence only. It does not claim Step 10 or package
completion.

### RED: inert tombstones consumed the bounded candidate window

```bash
cd backend
go test ./internal/backupasset/export \
  -run '^TestLifecycleReconcileDoesNotLetPurgedTerminalRowsConsumeLimit$' \
  -count=1 -v
```

- Exit `1`, package `0.092s`: `newer actionable job cleanup=none; older inert
  tombstone consumed the bounded candidate`. The old failed job was created and
  cleaned through the real lifecycle machine to `failed/purged`; with `limit=1`
  the old query counted that no-op tombstone as work and left the newer
  `failed/none` job untouched.

The same pre-fix failure was migration-backed on both engines:

```text
TestExportBehaviorSQLite/LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows
FAIL, package 0.234s: sqlite newer actionable cleanup=none; older inert tombstone consumed limit

TestExportBehaviorPostgres/LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows
FAIL, package 1.176s: postgres newer actionable cleanup=none; older inert tombstone consumed limit
```

### Minimal predicate correction and preserved crash boundaries

Candidate selection retains the same unlocked advisory query, order
`updated_at ASC, id ASC`, limit, error propagation, and external-I/O loop. It
subtracts only the following inert tuples from the existing candidate set:

| Cleanup state | Execution state | Candidate after correction |
|---|---|---|
| `purged` | `failed`, `source_expired`, `canceled` | no |
| `purged` | `cancel_requested`, `expiring` | yes; finalize by existing CAS |
| `revoking`, `purging`, `purge_failed` | any existing candidate execution | yes |
| `none` | `failed`, `source_expired`, `canceled` | yes; cleanup remains actionable |
| any | expired `ready` row | yes |
| `none` | future `ready` or active nonterminal execution | no, unchanged |

Focused tests prove `purged/cancel_requested -> purged/canceled` and
`purged/expiring -> purged/expired` each use zero lifecycle-port calls and
increment the transition revision exactly once. A context-scoped injected
revision winner proves a stale transition returns `ErrInvalidTransition`,
reports zero processed work, and performs zero lifecycle-port calls. The GORM
callback is removed with `t.Cleanup`; this tranche starts no goroutines.

### GREEN

```text
focused unit selector, first GREEN
ok  xirang/backend/internal/backupasset/export  0.144s

focused unit selector, -count=100
ok  xirang/backend/internal/backupasset/export  6.051s

focused unit selector, -race -count=1
ok  xirang/backend/internal/backupasset/export  1.731s

go test ./internal/backupasset/export -run '^(TestLifecycle|TestPersistentLifecyclePort)' -count=1
ok  xirang/backend/internal/backupasset/export  0.717s

go test ./internal/backupasset/export -run '^TestExportBehaviorSQLite$' -count=1
ok  xirang/backend/internal/backupasset/export  0.250s

exact SQLite lifecycle subtest
ok  xirang/backend/internal/backupasset/export  0.237s

required TestExportBehaviorPostgres
ok  xirang/backend/internal/backupasset/export  6.779s

exact required PostgreSQL lifecycle subtest
ok  xirang/backend/internal/backupasset/export  1.217s
```

The unrelated standalone
`TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile`
`ErrUnavailable` RED described above remains unchanged and outside this
correction; no delivery/worker or package-completion claim is made.

### Coverage-first complete inert tombstone matrix

A subsequent quality review found a Minor proof gap: the bounded-candidate
tests did not directly place every member of the already-correct inert
predicate ahead of actionable work. No production defect was found, so this
follow-up does not fabricate a RED. The expanded tests passed on their first
execution and are recorded as coverage-first evidence.

Both the unit test and the shared migration-backed SQLite/PostgreSQL subtest now
create real lifecycle tombstones for all three excluded tuples:
`purged/failed`, `purged/source_expired`, and `purged/canceled`. Every tombstone
has an older `updated_at` than the actionable `none/failed` row. With `limit=1`,
the actionable row must still be cleaned, each inert row must remain exactly
equal to its pre-reconcile snapshot, and a drained reconcile must report zero
work and zero lifecycle-port calls. The separate transition-only coverage for
`purged/cancel_requested -> purged/canceled` and
`purged/expiring -> purged/expired` remains unchanged and continues to require
one CAS revision plus zero cleanup I/O.

Fresh sequential evidence from 2026-07-24:

```text
focused unit matrix, first coverage execution
go test ./internal/backupasset/export \
  -run '^(TestLifecycleReconcileDoesNotLetPurgedTerminalRowsConsumeLimit|TestLifecycleReconcileFinalizesPurgedTransitionalRowsWithoutCleanupIO)$' \
  -count=1 -v
ok  xirang/backend/internal/backupasset/export  0.139s

focused unit matrix, -count=100
ok  xirang/backend/internal/backupasset/export  4.767s

focused unit matrix, -race -count=1
ok  xirang/backend/internal/backupasset/export  1.755s

exact migration-backed SQLite lifecycle subtest
go test ./internal/backupasset/export \
  -run '^TestExportBehaviorSQLite$/LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows$' \
  -count=1 -v
ok  xirang/backend/internal/backupasset/export  0.242s

full TestExportBehaviorSQLite
ok  xirang/backend/internal/backupasset/export  0.252s

exact required real-PostgreSQL lifecycle subtest
ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export \
  -run '^TestExportBehaviorPostgres$/LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows$' \
  -count=1 -v
ok  xirang/backend/internal/backupasset/export  1.275s

full required TestExportBehaviorPostgres with the same environment
ok  xirang/backend/internal/backupasset/export  6.480s

go vet ./internal/backupasset/export
# exit 0, no output

gofmt -d backend/internal/backupasset/export/lifecycle_test.go \
  backend/internal/backupasset/export/behavior_integration_test.go
# exit 0, no output

git diff --check -- backend/internal/backupasset/export/lifecycle_test.go \
  backend/internal/backupasset/export/behavior_integration_test.go \
  .trellis/tasks/07-22-backup-assets-export-archive/research/implementation-evidence.md
# exit 0, no output
```

This closes only the quality-review Minor test-matrix gap. Production code was
not changed, and no broader Step 10 or package-completion claim is made.

### Cancellation heartbeat/source-expiry correctness RED

Fresh TDD RED evidence from 2026-07-24 before production edits:

```text
go test ./internal/backupasset/export \
  -run 'TestAttemptCoordinatorHeartbeatRejectsCommittedCancellationWithoutRenewingAnyLease|TestExportServiceCancelLocksJobRowBeforeTransitionValidation|TestLifecycleFailSourceExpiredCompletesAuthoritativeCancelWithoutReclassification' \
  -count=1
FAIL
```

The package compiled and all three focused tests executed. Exact behavioral
failures were:

```text
heartbeat after committed cancellation error=<nil>, want ErrAttemptFenceLost
Cancel loaded the job without a FOR UPDATE row lock
invalid export transition
```

The first failure demonstrates that a committed `cancel_requested` state still
renewed the attempt/source/Foundation leases. The second demonstrates that
`Service.Cancel` did not lock the shared job row before transition validation.
The third demonstrates that `Lifecycle.FailSourceExpired` rejected the
authoritative cancellation state instead of running ordered cleanup and
finalizing it as `canceled`. This is focused RED evidence only; no aggregate or
Step 10 completion claim is made.

Focused GREEN evidence from 2026-07-24:

```text
go test ./internal/backupasset/export \
  -run '^(TestAttemptCoordinatorHeartbeatRejectsCommittedCancellationWithoutRenewingAnyLease|TestExportServiceCancelLocksJobRowBeforeTransitionValidation|TestExportServiceCancelIsOwnerBoundAndIdempotent|TestLifecycleFailSourceExpiredCompletesAuthoritativeCancelWithoutReclassification|TestLifecycleFailsClosedImmediatelyWhenSourceLeaseIsLost|TestLifecycleReconcileCompletesCancelRequestedAfterCleanup)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.240s
wall 2.239s

go test ./internal/backupasset/runtime \
  -run '^(TestManagedExportWorkerMaintenanceFinalizesCancellationSelectedBeforeLeaseLock|TestManagedExportWorkerStartupMaintainsQueuedAndReadySourceLeases)$' \
  -count=1
ok  xirang/backend/internal/backupasset/runtime  0.057s
wall 1.166s

go test ./internal/backupasset/export -run '^TestExportBehaviorSQLite$' -count=1
ok  xirang/backend/internal/backupasset/export  0.268s
wall 1.045s

go test -race ./internal/backupasset/export \
  -run '^(TestAttemptCoordinatorHeartbeatRejectsCommittedCancellationWithoutRenewingAnyLease|TestExportServiceCancelLocksJobRowBeforeTransitionValidation|TestLifecycleFailSourceExpiredCompletesAuthoritativeCancelWithoutReclassification|TestLifecycleFailsClosedImmediatelyWhenSourceLeaseIsLost)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export  1.903s
wall 5.690s

go test -race ./internal/backupasset/runtime \
  -run '^TestManagedExportWorkerMaintenanceFinalizesCancellationSelectedBeforeLeaseLock$' \
  -count=1
ok  xirang/backend/internal/backupasset/runtime  1.532s
wall 8.172s
```

Required real-PostgreSQL selectors were run sequentially with
`ALLOW_DIRTY_STARTUP=true`, `REQUIRE_POSTGRES_EXPORT_TEST=1`, and
`TEST_POSTGRES_DSN=postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable`:

```text
go test ./internal/backupasset/export \
  -run '^TestExportBehaviorPostgres$/CancellationSerialization/HeartbeatHoldingJobRowPrecedesCancel$' \
  -count=1 -v
ok  xirang/backend/internal/backupasset/export  1.320s

go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
ok  xirang/backend/internal/backupasset/export  7.087s
wall 7.904s
```

The PostgreSQL barrier used two distinct backend PIDs and
`pg_blocking_pids`: Heartbeat paused after acquiring the Export job row,
Cancel attempted the same `FOR UPDATE` row lock and was observed blocked by
the Heartbeat session, Heartbeat committed successfully, and then Cancel
committed `cancel_requested`. The separate unit regression proves a Heartbeat
started after that cancel commit returns `ErrAttemptFenceLost` without changing
the attempt expiry, Export source heartbeat/deadline, or Foundation lease
expiry/heartbeat/fence fields.

Additional checks:

```text
go vet ./internal/backupasset/export ./internal/backupasset/runtime
# exit 0, wall 1.175s

gofmt -d backend/internal/backupasset/export/worker.go \
  backend/internal/backupasset/export/worker_test.go \
  backend/internal/backupasset/export/service.go \
  backend/internal/backupasset/export/service_test.go \
  backend/internal/backupasset/export/lifecycle.go \
  backend/internal/backupasset/export/lifecycle_test.go \
  backend/internal/backupasset/export/behavior_integration_test.go \
  backend/internal/backupasset/runtime/export_runtime_test.go
# exit 0, no output

git diff --check
# exit 0, no output
```

The broader command `go test ./internal/backupasset/export
./internal/backupasset/runtime -count=1` was also run. Runtime passed in
`2.901s`; Export failed in `3.715s` on the already recorded, unrelated
delivery/worker publication and persisted-profile `export unavailable`
fixtures, including
`TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile` and
`TestPersistentWorkerSealsAndPublishesCurrentFenceWithPartialReport`. This
tranche does not modify those paths or claim the aggregate package/full Step 10
gate. Its production scope is limited to Heartbeat active-state fencing,
Cancel job-row serialization, and cancellation-aware source-maintenance
cleanup; `runtime/export_runtime.go` remains unchanged.

### Cancellation runtime fixture repetition correction

The controller quality pass then ran the new runtime regression with
`-count=50`. Its first iteration passed, but every later iteration failed with
`UNIQUE constraint failed: backup_asset_export_jobs.id`. This was a test-fixture
RED: `exportRuntimeKeyringFixture` used a named shared in-memory SQLite database
derived from `t.Name()` without closing its connection pool, so the same test
name retained rows across repeated executions.

The fixture now closes its underlying `sql.DB` with `t.Cleanup`. No production
code or test behavior changed. Fresh GREEN:

```text
go test ./internal/backupasset/runtime \
  -run '^TestManagedExportWorkerMaintenanceFinalizesCancellationSelectedBeforeLeaseLock$' \
  -count=50
ok  xirang/backend/internal/backupasset/runtime  0.101s
```

### Retired item-spool evidence reload correctness

The controller reproduced the previously recorded worker-to-delivery RED and
traced it to the persistent item-attempt validator. `SealArchive` intentionally
removes the consumed `.xrs` locator while retaining the immutable digest and
size observation, but `validPersistedTerminalSpool` accepted only all-present or
all-empty tuples. Persistent reload therefore returned `ErrUnavailable` before
sealed `.xre` authentication or ready publication.

Fresh REDs before the production correction:

```text
go test ./internal/backupasset/export \
  -run '^(TestDeliveryGatewayServesWorkerProducedArtifactWithRawPersistedProfile|TestPersistentWorkerSealsAndPublishesCurrentFenceWithPartialReport|TestPersistentWorkerRestartTakesOverExpiredSourceOwnerBeforePublishingSealedArtifact|TestAttemptCoordinatorMaintainsReadySourceLeaseWithoutResettingResult)$' \
  -count=1
FAIL: all four selectors returned export unavailable during persistent reload

go test ./internal/backupasset/export \
  -run '^TestValidPersistedTerminalSpoolAcceptsOnlyClosedEvidenceStates$' \
  -count=1 -v
FAIL: retired spool evidence was rejected; the other ten closed-matrix rows passed
```

The validator now accepts exactly three terminal shapes: never-spooled empty
evidence, complete live `.xrs` evidence, and valid retained digest/size with an
empty retired locator. Missing digest/size pairs, invalid digest, oversize, and
wrong live locator type remain rejected. The `read` state remains stricter and
still requires the complete live tuple. The seal regression also proves digest
and size survive locator retirement.

Fresh GREEN:

```text
focused worker/restart/ready/delivery selectors, -count=20
ok  xirang/backend/internal/backupasset/export  2.425s

same focused selectors with -race -count=1
ok  xirang/backend/internal/backupasset/export  2.022s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  3.770s

go test ./internal/backupasset/runtime -count=1
ok  xirang/backend/internal/backupasset/runtime  2.837s

ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
ok  xirang/backend/internal/backupasset/export  7.266s

go vet ./internal/backupasset/export
# exit 0
```

No schema, migration, delivery, or lifecycle production path changed in this
correction, and it does not close the remaining Step 10 tranches.

The controller quality pass added a negative-size corruption row after the
first GREEN. It produced a focused RED because the new branch distinguished
only `size == 0`; the validator now requires a positive retained size and the
full 12-row matrix, focused `-count=20`, and focused race suite pass again
(`2.400s` and `2.020s`).

### Ready source-lease maintenance uses the access deadline

The next focused RED advanced a published ready job to one second after its
execution `AbsoluteDeadline`, while keeping the job `ExpiresAt` and the exact
source deadline in the future. `MaintainSourceLeases` returned
`ErrAttemptFenceLost`, proving that ready access was still incorrectly coupled
to the completed execution window.

The coordinator now selects the deadline by durable execution state: queued,
running, retry-wait, and sealing maintenance remains strictly before the
execution deadline, while ready maintenance is strictly before the non-null
ready `ExpiresAt`. The source row's exact returned `AbsoluteDeadline` and
optional `RetentionUntil` remain independent strict caps. No lease deadline is
rewritten or reacquired.

Additional regression rows prove that maintenance performs zero source or
Foundation mutation at/after ready expiry, with a malformed missing ready
expiry, at the non-ready execution boundary, or at either source cap. The
existing ready result, item projection, attempt lineage, and exact source
deadline survive both active-owner renewal and expired-owner takeover after the
execution deadline. A shared migration-backed behavior subtest runs the same
takeover and expiry boundary through SQLite and PostgreSQL.

Fresh focused GREEN evidence from 2026-07-24:

```text
go test ./internal/backupasset/export \
  -run '^(TestAttemptCoordinatorMaintainsReadySourceLeaseWithoutResettingResult|TestAttemptCoordinatorSourceLeaseMaintenanceStopsAtExecutionAndReadyBoundaries|TestAttemptCoordinatorReadySourceLeaseMaintenanceStopsAtSourceCaps)$' \
  -count=20
ok  xirang/backend/internal/backupasset/export  5.166s

go test -race ./internal/backupasset/export \
  -run 'SourceLease|ReadySourceLease' -count=1
ok  xirang/backend/internal/backupasset/export  2.776s

go test ./internal/backupasset/export ./internal/backupasset/runtime -count=1
ok  xirang/backend/internal/backupasset/export  4.895s
ok  xirang/backend/internal/backupasset/runtime  3.039s

go vet ./internal/backupasset/export ./internal/backupasset/runtime
# exit 0

ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
ok  xirang/backend/internal/backupasset/export  7.822s
```

The next tranche remains open: planned ready expiry, malformed ready state,
execution deadline, true source-cap expiry, stale authority, and database or
context errors still collapse to `ErrAttemptFenceLost` and require typed
classification before runtime reconciliation can choose the correct terminal
path.

### Typed source-maintenance and heartbeat failure classification

The next REDs proved that the coordinator and managed runtime collapsed five
different conditions into `ErrAttemptFenceLost` and then unconditionally called
`FailSourceExpired`: normal ready expiry, execution deadline, a malformed ready
aggregate, true source-cap expiry, and stale or unavailable authority. Database,
context, and Foundation errors were also erased, while unclassified runtime
errors were silently dropped.

The coordinator now exposes closed `ErrExecutionDeadlineReached`,
`ErrReadyExpired`, and `ErrSourceDeadlineReached` sentinels. Job/source/Foundation
query errors retain their original cause, Foundation renewal/takeover errors stay
inspectable with `errors.Is`, and only `ErrLeaseDeadlineExceeded` receives the
additional source-cap classification. A future source cap plus stale identity
remains `ErrAttemptFenceLost`; an expired short lease with future caps remains a
legal takeover.

Runtime routes only the true source-cap sentinel to `FailSourceExpired`. Planned
ready expiry falls through to the same-pass lifecycle `ready -> expiring ->
expired` path. Execution deadline and malformed ready state use ordered
`FailUnpublishable` cleanup with `deadline` and `internal_failure`; a published
ready row follows `expiring -> expired` rather than the forbidden `ready ->
failed` transition. Concurrent `cancel_requested` is reloaded first and still
finishes cancellation without reclassification. Unrecognized fence and
infrastructure errors are returned instead of being silently consumed.

Active Heartbeat applies the same split. A source cap invokes lifecycle cleanup
without deleting attempt objects before the revoke/key/source order; execution
deadline persists the closed `deadline` category with retry disabled. Status now
accepts the valid split contract where ready expiry is later than execution
deadline, while preserving strict `readyAt < expiresAt` validation.

Fresh RED highlights:

```text
TestManagedExportWorkerClassifiesSourceMaintenanceFailures:
execution/ready/corrupt called source-expired; fence/infrastructure returned nil

TestManagedExportWorkerReadyMaintenanceUsesExpiredAndCorruptLifecycleOutcomes:
missing ready expiry returned ErrUnavailable + ErrInvalidTransition

TestAttemptCoordinatorHeartbeatClassifiesDeadlinesAndPreservesUnderlyingErrors:
execution deadline and Foundation errors returned ErrAttemptFenceLost

TestManagedExportWorkerRoutesTypedHeartbeatDeadlines:
source cap made no lifecycle call; execution deadline category was archive_failed

TestExportServiceStatusAcceptsReadyExpiryAfterExecutionDeadline:
valid published ready status returned ErrUnavailable
```

Fresh GREEN evidence from 2026-07-24:

```text
focused Export typed deadline/error/status selectors, -count=20
ok  xirang/backend/internal/backupasset/export  10.792s

focused Runtime classification/lifecycle/heartbeat selectors, -count=20
ok  xirang/backend/internal/backupasset/runtime  0.347s

go test -race ./internal/backupasset/export ./internal/backupasset/runtime \
  -run 'SourceLease|SourceMaintenance|HeartbeatClassifies|TypedHeartbeat|ReadyMaintenance|ReadyExpiry' -count=1
ok  xirang/backend/internal/backupasset/export  3.760s
ok  xirang/backend/internal/backupasset/runtime  1.620s

go test ./internal/backupasset/export ./internal/backupasset/runtime -count=1
ok  xirang/backend/internal/backupasset/export  5.329s
ok  xirang/backend/internal/backupasset/runtime  3.081s

go vet ./internal/backupasset/export ./internal/backupasset/runtime
# exit 0

ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
ok  xirang/backend/internal/backupasset/export  7.759s
```

### Paired ready lifecycle timestamp CHECK invariants

The migration-backed shared contract now exercises the ready timestamp product
against both engines. Before either up migration changed, these focused commands
were genuine REDs:

```bash
cd backend
go test ./internal/database \
  -run '^TestBackupAssetMigration068SQLite/ReadyLifecycleTimestampProductIsClosed$' \
  -count=1 -v

ALLOW_DIRTY_STARTUP=true \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
go test ./internal/database \
  -run '^TestBackupAssetMigration068Postgres/ReadyLifecycleTimestampProductIsClosed$' \
  -count=1 -v
```

Both commands exited `1` for the same missing database behavior. SQLite and
PostgreSQL each accepted `ready`, `expiring`, and `expired` rows with both
`ready_at`/`expires_at` absent, accepted those states with only `expires_at`, and
accepted zero-duration `ready_at = expires_at`. The existing CHECK already
rejected the complementary missing-`expires_at` rows. All positive controls
passed, including queued/running/retry-wait/sealing/cancel-requested/failed/
source-expired/canceled rows with no ready timestamps and cancel-requested/
canceled rows retaining an ordered ready history.

The paired up migrations now require non-null `ready_at` and `expires_at` for
`ready|expiring|expired`, and require `created_at <= ready_at < expires_at`
whenever `ready_at` exists. The CHECK deliberately has no relationship between
the execution `absolute_deadline` and ready expiry. Each ready-state positive
control uses an expiry ten minutes after the execution deadline.

Fresh focused GREEN:

```text
TestBackupAssetMigration068SQLite/ReadyLifecycleTimestampProductIsClosed
PASS; ok  xirang/backend/internal/database  0.072s

TestBackupAssetMigration068Postgres/ReadyLifecycleTimestampProductIsClosed
PASS; ok  xirang/backend/internal/database  0.508s
```

Fresh sequential migration gates:

```bash
cd backend
go test ./internal/database \
  -run '^TestBackupAssetMigration068(SQLite|PairedFiles|DeliveryAuditReceiptStatesArePaired)$' \
  -count=1
# ok  xirang/backend/internal/database  0.975s

ALLOW_DIRTY_STARTUP=true \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
go test ./internal/database \
  -run 'Test(BackupAssetMigration0(62|63|64|65|66|67|68)Postgres|PostgresTimestamptzScanUsesConfiguredUTC)' \
  -count=1
# ok  xirang/backend/internal/database  42.860s
```

This is paired migration/database evidence only. It does not close the broader
reopened Step 10 or any runtime, frontend, delivery, CI, or release gate.

The broader SQLite-only migration selector was also rerun after the paired
change:

```bash
cd backend
go test ./internal/database -run 'TestBackupAssetMigration0(62|63|64|65|66|67|68)SQLite' -count=1
# ok  xirang/backend/internal/database  4.178s
```

The migration directory scan contained exactly the four paired `000068` files;
no `000069`, `000070`, or `000071` file was present.

### Delivery issuance rejects malformed ready-artifact metadata

The integrity audit found that `IssueExport` treated any positive artifact
format version as supported, did not bind the job's frozen chunk size to the
artifact, did not validate the v1 chunk/count/ciphertext-size geometry, and did
not validate that the persisted locator was a canonical final `.xre` store
object. Those malformed rows reached key lookup, ticket activation, success
audit, and a durable grant.

The focused table-driven RED covered unsupported format version, job/artifact
chunk mismatch, an oversized v1 chunk, impossible chunk count, impossible
ciphertext size while preserving the old job-size equality, a spool `.xrs`
locator, a path locator, and a noncanonical uppercase locator:

```text
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayIssueExportRejectsMalformedArtifactMetadataWithoutLedgerMutation$' \
  -count=1 -v
FAIL

all 8 subtests: issue=<nil> with a live descriptor and cookie
```

`validReadyDeliveryArtifact` now requires exact cipher/format version 1,
job/artifact chunk equality, the existing final-store canonical locator shape,
and the existing v1 cipher metadata validator's chunk limit, calculated chunk
count, calculated ciphertext size, digest syntax, and 8-byte nonce prefix. The
attempt nonce still must match exactly. This validation finishes before any key
lookup, audit, delivery grant, request, or grant-budget counter can change. It
does not open or read the artifact; metadata-only HEAD and GET's later full
ciphertext authentication remain unchanged.

The delivery fixture now uses `cipherChunkCountV1` and `ciphertextSizeV1`
instead of an internally inconsistent hard-coded ciphertext size. Fresh GREEN:

```text
go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayIssueExportRejectsMalformedArtifactMetadataWithoutLedgerMutation$' \
  -count=1 -v
ok  xirang/backend/internal/backupasset/export  0.072s

go test ./internal/backupasset/export -run '^TestDeliveryGatewayIssueExport' -count=1
ok  xirang/backend/internal/backupasset/export  0.142s

go test ./internal/backupasset/export \
  -run '^TestDeliveryGatewayIssueExportRejectsMalformedArtifactMetadataWithoutLedgerMutation$' \
  -count=20
ok  xirang/backend/internal/backupasset/export  0.544s

go test ./internal/backupasset/export -run 'Delivery' -count=1
ok  xirang/backend/internal/backupasset/export  0.525s

go test -race ./internal/backupasset/export -run 'Delivery' -count=1
ok  xirang/backend/internal/backupasset/export  2.820s

go build ./internal/backupasset/export
# exit 0

gofmt -d internal/backupasset/export/delivery.go \
  internal/backupasset/export/delivery_test.go
# exit 0, no output
```

Every malformed GREEN row also asserts `ErrNotFound`, no descriptor/cookie,
zero key-source calls, zero success-audit events, and zero 000068 delivery grant
or request rows. This isolated tranche does not claim the full Export package
gate; the controller owns that rerun after the concurrent lifecycle tranche
reaches GREEN.

### Expired-attempt retirement and sealing handoff

This tranche closes the recovery boundary between an expired or superseded
attempt and a fresh worker claim. At this historical tranche, the then-current
approved implementation manifest was `56 create + 54 modify`; no path outside
that manifest was added here.

The focused REDs were observed before the recovery correction:

```text
AttemptClaim.SupersededAttemptID undefined
TestPersistentWorkerDiscardsSupersededSealingArtifactAndRetainsSpoolEvidence:
  pre-ready artifact rows=1, want=0 after ciphertext purge
TestPersistentWorkerDiscardsFailedAttemptCiphertextBeforeRetry:
  discard erased immutable spool digest/size evidence
TestPersistentWorkerRestartReturnsExpiredSealingAttemptForFreshRebuild:
  ReconcileJob returned ErrAttemptLeaseExpired and stopped before the fresh claim
TestManagedExportWorkerStartupRetriesRetiredAttemptCleanupAfterFailure:
  startup never invoked failed retired-attempt cleanup
```

The correction now returns the exact superseded attempt ID from `Claim`, retires
only the requested closed attempt's spool/staging/pre-ready artifact locators,
deletes matching pre-ready artifact rows transactionally, clears locator
references while retaining immutable spool digest/size evidence, and rejects
ready-artifact retirement through closed-attempt and `expires_at IS NULL`
guards. Managed startup includes `sealing` jobs, maintains source ownership
before claiming, treats an expired sealing reconciliation as a rebuild handoff,
discards the exact superseded attempt before spooling, and retries durable
closed-attempt retirement on a later startup.

Fresh GREEN for the exact six recovery selectors:

```bash
cd backend
go test -race ./internal/backupasset/export ./internal/backupasset/runtime \
  -run '^(TestAttemptCoordinatorPersistsCheckpointAndRejectsOldFenceAfterTakeover|TestPersistentWorkerDiscardsFailedAttemptCiphertextBeforeRetry|TestPersistentWorkerDiscardsSupersededSealingArtifactAndRetainsSpoolEvidence|TestPersistentWorkerRestartReturnsExpiredSealingAttemptForFreshRebuild|TestManagedExportWorkerStartupTakesOverSourceBeforeRebuildingExpiredSealing|TestManagedExportWorkerStartupRetriesRetiredAttemptCleanupAfterFailure)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export    1.840s
ok  xirang/backend/internal/backupasset/runtime   1.572s
```

The runtime fixture migration repair was then verified independently:

```text
go test ./internal/backupasset/runtime -count=1
ok  xirang/backend/internal/backupasset/runtime  2.810s

go vet ./internal/backupasset/export ./internal/backupasset/runtime
# exit 0

gofmt -d internal/backupasset/export/worker.go \
  internal/backupasset/export/worker_test.go \
  internal/backupasset/runtime/export_runtime.go \
  internal/backupasset/runtime/export_runtime_test.go
# no output

git diff --check -- backend/internal/backupasset/export/worker.go \
  backend/internal/backupasset/export/worker_test.go \
  backend/internal/backupasset/runtime/export_runtime.go \
  backend/internal/backupasset/runtime/export_runtime_test.go
# no output
```

The combined package command was also run. Runtime passed; Export remained
non-green only in unrelated lifecycle work owned by the concurrent lifecycle
tranche (`TestExportBehaviorSQLite/LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows`,
the two `TestLifecyclePurgeFailureStillFinalizesExecutionOutcome` subtests, and
`TestPersistentLifecyclePortCryptographicallyRevokesBeforePhysicalPurgeAndStoreRelease`).
Those fixtures and production paths are outside this recovery tranche and were
not changed.

The configured PostgreSQL selector was exercised as well:

```bash
cd backend
ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
```

The database was reachable and migrated to version 68. The selector failed only
in the same out-of-scope lifecycle CHECK fixture and the existing
`LifecycleLockOrder/FenceAttemptsVersusClaim` barrier timeout; no recovery
selector failed. No staging, commit, push, migration, or lifecycle edit was
performed in this tranche.

Owned paths changed in this tranche:

```text
backend/internal/backupasset/export/worker.go
backend/internal/backupasset/export/worker_test.go
backend/internal/backupasset/runtime/export_runtime.go
backend/internal/backupasset/runtime/export_runtime_test.go
.trellis/tasks/07-22-backup-assets-export-archive/research/implementation-evidence.md
```

### Archive member path bounds after scope prefixing and collision suffixing

The archive-path review found two final-name boundary gaps. `prepareArchiveEntries`
validated a source path before adding the cross-root `rp-<short>/root-<ordinal>`
scope, so a valid 16-component path could become depth 18 and exceed the member
byte limit. `appendArchiveSuffix` also preserved an arbitrarily large
`path.Ext`, allowing a 255-byte dotfile and a one-byte basename with a 253-byte
extension to exceed the 255-byte component limit after adding `~N`.

The new RED fixtures use two identical 16-component paths made from
`strings.Repeat("界", 85)` (255 UTF-8 bytes), distinct valid RecoveryPoint/root
scopes, and three casefold-colliding suffix rows: a 255-byte dotfile, a one-byte
basename with a huge extension, and a UTF-8 basename near the byte limit. They
write both ZIP and TAR, compare physical member names with the returned and
embedded reports, assert depth/component/member byte and UTF-8 bounds, and
compare ItemID-to-member mappings under reversed input order.

Fresh RED:

```text
go test ./internal/backupasset/export \
  -run '^TestWriteArchiveBounds(CrossRootScopePathsAndPreservesZIPTARMapping|CollisionSuffixesAcrossPathShapes)$' \
  -count=1 -v
FAIL

cross-root row: member depth=18 exceeds 16
dotfile row: component bytes=257 exceeds 255
huge-extension row: component bytes=256 exceeds 255
```

The GREEN implementation compacts scoped source paths deterministically when
the two generated scope components would exceed the depth budget, preserving
leading components and the leaf and replacing omitted middle components with a
domain-labelled SHA-256 component. A final member validator now runs after
scope prefixing and every collision retry. Suffix allocation treats a full
dotfile as having no preservable extension and drops an extension whenever it
cannot leave room for a non-empty basename plus the explicit suffix marker.

Fresh focused GREEN:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestWriteArchiveBounds(CrossRootScopePathsAndPreservesZIPTARMapping|CollisionSuffixesAcrossPathShapes)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.060s

go test ./internal/backupasset/export -run 'Archive|Sanitize|Collision|Suffix|CrossRoot' -count=1
ok  xirang/backend/internal/backupasset/export  0.640s

go test -race ./internal/backupasset/export -run 'Archive|Sanitize|Collision|Suffix|CrossRoot' -count=1
ok  xirang/backend/internal/backupasset/export  3.374s

go vet ./internal/backupasset/export
# exit 0

gofmt -d internal/backupasset/export/archive.go \
  internal/backupasset/export/archive_test.go
# exit 0, no output
```

The full export-package selector was also run, but remains blocked by two
unrelated concurrent lifecycle fixture failures before archive-path behavior:
`TestExportBehaviorSQLite/LifecycleReconcile/BoundedCandidatesSkipInertPurgedTerminalRows`
fails the lifecycle execution-state CHECK during setup, and
`TestPersistentLifecyclePortCryptographicallyRevokesBeforePhysicalPurgeAndStoreRelease`
fails with `cleanup state=revoking err=fence attempts: export attempt fence lost`.
No lifecycle files were changed by this archive slice; the focused archive and
race selectors above are the relevant fresh verification.

### 2026-07-24 lifecycle projection and purge-finalization repair

The earlier lifecycle RED was reproduced from the current branch before the
fixture repair. `FenceAttempts` correctly requires a terminal locked job
outcome for an active/sealing attempt, but the legacy fixtures still called it
with `execution_state=running`; the callbacks therefore never reached the
intended CAS/update assertions. The RED also exposed that the public item
projection was not included in rollback coverage and that the two child update
streams needed independent bounded batches.

Fresh RED:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestPersistentLifecyclePortFenceAttemptsLocksAndCASesObservedLineage$' \
  -count=1 -v
FAIL

active_attempt_with_zero_unfinished_children: ErrAttemptFenceLost
current_sealing_attempt: ErrAttemptFenceLost
unfinished_children_use_bounded_update_batches: callback not injected
child/attempt CAS cases: callback not injected
final Job CAS cases: callback not injected
```

The repaired tests set the job to `cancel_requested` (or the explicit failed /
source-expired matrix), mirror public and immutable item state, set
`item_count=401`, verify `Job -> Attempt -> Item -> ItemAttempt` lock order,
assert `[400,1]` batches for both child tables, and prove public-item as well
as immutable-row rollback on CAS loss. The sealed `cancel_requested` lineage
continues to preserve its observations and projections.

The parent-requested orthogonal cleanup RED was then added:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^TestLifecyclePurgeFailureStillFinalizesExecutionOutcome$' \
  -count=1 -v
FAIL

cancel_requested: execution remained cancel_requested/cleanup purge_failed
expiring: execution remained expiring/cleanup purge_failed
```

`Lifecycle` now finalizes `cancel_requested -> canceled` or
`expiring -> expired` once cleanup reaches either `purge_failed` or `purged`,
while still returning the purge error and retaining `purge_failed` for retry.
Earlier revocation/key/source failures remain transitional and do not bypass
the revoke-first boundary.

Fresh GREEN evidence:

```text
cd backend
go test ./internal/backupasset/export \
  -run '^(TestLifecyclePurgeFailureStillFinalizesExecutionOutcome|TestPersistentLifecyclePortFenceAttemptsPreservesSealedCancelRequestedLineage|TestPersistentLifecyclePortFenceAttemptsClosesActiveLineageFromLockedJobOutcome|TestPersistentLifecyclePortFenceAttemptsLocksAndCASesObservedLineage)$' \
  -count=1
ok   xirang/backend/internal/backupasset/export  0.7s

go test -race ./internal/backupasset/export \
  -run '^(TestPersistentLifecyclePortFenceAttemptsPreservesSealedCancelRequestedLineage|TestPersistentLifecyclePortFenceAttemptsClosesActiveLineageFromLockedJobOutcome|TestPersistentLifecyclePortFenceAttemptsLocksAndCASesObservedLineage|TestLifecyclePurgeFailureStillFinalizesExecutionOutcome)$' \
  -count=1
ok   xirang/backend/internal/backupasset/export  2.670s

go test ./internal/backupasset/export -run '^TestExportBehaviorSQLite$' -count=1
ok   xirang/backend/internal/backupasset/export  0.422s

ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export -run '^TestExportBehaviorPostgres$' -count=1
ok   xirang/backend/internal/backupasset/export  8.153s

go test ./internal/backupasset/export -count=1
ok   xirang/backend/internal/backupasset/export  4.260s

go vet ./internal/backupasset/export
# exit 0

gofmt -d backend/internal/backupasset/export/lifecycle.go \
  backend/internal/backupasset/export/lifecycle_test.go \
  backend/internal/backupasset/export/behavior_integration_test.go
# no output

git diff --check
# no output
```

The migration-backed projection subtest runs through both SQLite and real
PostgreSQL 18. The PostgreSQL `FenceAttemptsVersusClaim` barrier now starts
from `cancel_requested`, proves the competing claim waits on the job row and
then returns `ErrAttemptNotClaimable` without creating a replacement attempt.

### 2026-07-24 exact retained ExportStore KEK loss

The keyring loss path previously accepted only an `active` row. Because
`Keyring.Rotate` demotes retained ExportStore versions to `verify_only`, a job
holding an older `BackupAssetExportKey.KEKVersion` could not be invalidated
without touching a newer healthy key. The regression uses three exact Export
KEK versions, asks to lose the oldest retained version, and verifies that the
invalidator sees the retained row before loss, receives that exact version in
`RebuildableKeyTransition.PreviousVersion`, and leaves the newer retained and
active rows unchanged. Version `0` and a missing version remain fail-closed
without invoking the callback.

Fresh RED:

```text
cd backend
go test ./internal/backupasset \
  -run '^TestExportStoreLossTargetsExactRetainedVersion$' -count=1 -v
FAIL

keyring_test.go:277: MarkRebuildableLost retained export version:
backup asset key unavailable: domain key version is not active
```

The minimal GREEN keeps the existing API shape:
`MarkRebuildableLost(ctx, domain, version, invalidate)`. Active rows remain
eligible for every rebuildable domain; only `KeyDomainExportStore` additionally
accepts the exact `verify_only` version. The callback still runs in the locked
transaction before the row is marked `lost`, and its `PreviousVersion` is the
precise version the caller supplied. SearchToken and DerivedStore retain their
active-only loss semantics.

Fresh GREEN and focused quality checks:

```text
cd backend
go test ./internal/backupasset \
  -run '^TestExportStoreLossTargetsExactRetainedVersion$' -count=1 -v
ok   xirang/backend/internal/backupasset  0.031s

go test ./internal/backupasset \
  -run '^(Test(EnsureRequiredDomains|SearchToken|DerivedStore|ExportStore|DomainScopedRewrap|StableDomains|CursorRotation|AuditFingerprint|Rewrap|MissingOrLost|ConcurrentEnsure))' \
  -count=1
ok   xirang/backend/internal/backupasset  0.543s

go test -race ./internal/backupasset \
  -run '^(Test(EnsureRequiredDomains|SearchToken|DerivedStore|ExportStore|DomainScopedRewrap|StableDomains|CursorRotation|AuditFingerprint|Rewrap|MissingOrLost|ConcurrentEnsure))' \
  -count=1
ok   xirang/backend/internal/backupasset  2.466s

go vet ./internal/backupasset
# exit 0, no output

gofmt -d internal/backupasset/keyring.go internal/backupasset/keyring_test.go
git diff --check -- backend/internal/backupasset/keyring.go \
  backend/internal/backupasset/keyring_test.go
# both exit 0, no output
```

No worker, runtime, lifecycle, quota, schema, staging, commit, push, or PR
operation was performed in this tranche.

### 2026-07-25 verified ready-token source-lease maintenance

Ready reconciliation now returns an opaque in-memory proof over the locked
job/attempt/artifact/key/source tuple. Runtime passes that exact proof into
ready source-lease maintenance, which re-locks and revalidates the tuple before
calling Foundation renew/takeover. Regression barriers cover artifact locator
and ciphertext-digest drift, a real job-key rewrap/revision change, and a
sealing request consumed after ready publication; the real-coordinator spy
records zero renew/takeover calls and source/Foundation rows remain unchanged.

Fresh RED and GREEN evidence:

```text
go test ./internal/backupasset/export \
  -run '^TestReadyIntegrityTokenTreatsMissingReadyExpiryAsInvalid$' -count=1
FAIL: panic in newReadyIntegrityToken at worker.go:1759 (nil ExpiresAt)

go test ./internal/backupasset/export ./internal/backupasset/runtime \
  -run 'Ready|SourceLease|Integrity|Barrier|Reconcile' -count=1
ok   xirang/backend/internal/backupasset/export   1.053s
ok   xirang/backend/internal/backupasset/runtime  0.148s

go test ./internal/backupasset/export ./internal/backupasset/runtime -count=1
ok   xirang/backend/internal/backupasset/export   4.828s
ok   xirang/backend/internal/backupasset/runtime  2.875s

go test -race ./internal/backupasset/export ./internal/backupasset/runtime \
  -run 'Ready|SourceLease|Integrity|Barrier|Reconcile' -count=1
ok   xirang/backend/internal/backupasset/export   4.744s
ok   xirang/backend/internal/backupasset/runtime  1.721s

ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='<local PostgreSQL test DSN>' \
go test ./internal/backupasset/export \
  -run '^TestExportBehaviorPostgres$/ReadySourceLease/MaintainsVerifiedReadyAfterExecutionDeadline$' -count=1
ok   xirang/backend/internal/backupasset/export   1.281s

go vet ./internal/backupasset/export ./internal/backupasset/runtime
gofmt -l internal/backupasset/export/worker.go \
  internal/backupasset/export/worker_test.go \
  internal/backupasset/export/behavior_integration_test.go \
  internal/backupasset/runtime/export_runtime.go \
  internal/backupasset/runtime/export_runtime_test.go
git diff --check
# all exit 0; gofmt and diff checks produced no output
```

No quota, lifecycle, settings, schema, staging, delivery, commit, push, or PR
operation was performed in this tranche.

### 2026-07-25 ready-integrity capability and resource hardening

Ready integrity tokens now use shared pointer-backed single-use state, bind the
complete authoritative job/attempt/artifact/key/ordered-source tuple, reject
non-canonical timestamps and UTF-8, and defer plaintext DEK loading until the
token is consumed. Consumption pins and reauthenticates the sealed artifact
before entering the GORM/Foundation transaction. The pin is released after the
transaction on every return and error-valued panic path, with close errors
preserved. Eager ready authentication and lazy pinned verification share the
same recover-safe ownership helper, so both close descriptors on panic and
join operation/panic failures with descriptor close failures.

Runtime classifies physical ready drift as `artifact_tampered`, plain ready
fence/integrity loss as ordered `source_expired` cleanup, and joined
`ErrAttemptFenceLost + ErrUnavailable` key loss as `internal_failure` before
the fence-only branch. Context cancellation and untyped infrastructure errors
remain returned for retry.

The focused RED sequence was observed before the corresponding fixes:

```text
go test ./internal/backupasset/export \
  -run '^(TestReadyIntegrityDigestBindsEveryAuthoritativeTupleField|TestReadyIntegrityTokenAliasesShareSingleUseState|TestReadyIntegrityTokenDefersJobDEKLoadUntilConsumption|TestAttemptCoordinatorPanicJoinsReadyArtifactReleaseError)$' \
  -count=1
FAIL
# 44 authoritative tuple fields were not bound; a copied token replayed;
# no consumption-time key lookup occurred; panic lost the pin close error.

go test ./internal/backupasset/runtime \
  -run '^TestManagedExportWorkerClassifiesSourceMaintenanceFailures$' -count=1
FAIL
# ready fence/tamper failures were returned without the required cleanup.

go test ./internal/backupasset/export \
  -run '^TestReadyIntegrityDigestDoesNotCollapseInvalidCanonicalMaterial$' -count=1
FAIL
# distinct out-of-range timestamps collapsed to the same digest.

go test ./internal/backupasset/export \
  -run '^TestReadyIntegrityVerificationClearsCallerOwnedKeyMaterial$' -count=1
FAIL
# caller-owned Export KEK bytes remained nonzero.

go test ./internal/backupasset/runtime \
  -run '^TestManagedExportWorkerClassifiesSourceMaintenanceFailures$/ready_key_unavailable$' -count=1
--- FAIL: TestManagedExportWorkerClassifiesSourceMaintenanceFailures/ready_key_unavailable
    export_runtime_test.go:826: source-expired calls=[00000000000000000000000000000016] want=false
FAIL

# Mutation check: replace the ownership helper with direct operation return.
go test ./internal/backupasset/export \
  -run '^TestWithReadyArtifactReleasePreservesOperationAndCloseFailures$' -count=1
--- FAIL: TestWithReadyArtifactReleasePreservesOperationAndCloseFailures
    worker_test.go:4101: ready verification error=export ciphertext authentication failed, want operation and close failures
FAIL
```

Fresh focused GREEN after restoring the complete implementation:

```text
go test ./internal/backupasset/export \
  -run '^(TestReadyIntegrityDigestBindsEveryAuthoritativeTupleField|TestReadyIntegrityDigestDoesNotCollapseInvalidCanonicalMaterial|TestReadyIntegrityTokenAliasesShareSingleUseState|TestReadyIntegrityTokenDefersJobDEKLoadUntilConsumption|TestReadyIntegrityVerificationClearsCallerOwnedKeyMaterial|TestAttemptCoordinatorPanicJoinsReadyArtifactReleaseError|TestReadyArtifactVerifierReleasesStorePinOnPanic|TestWithReadyArtifactReleasePreservesOperationAndCloseFailures)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.213s

go test ./internal/backupasset/runtime \
  -run '^TestManagedExportWorkerClassifiesSourceMaintenanceFailures$' -count=1
ok  xirang/backend/internal/backupasset/runtime  0.062s

go test ./internal/backupasset/export \
  -run '^TestWithReadyArtifactReleasePreservesOperationAndCloseFailures$' -count=1
ok  xirang/backend/internal/backupasset/export  0.051s
```

Fresh migration-backed PostgreSQL, package, and race evidence:

```text
ALLOW_DIRTY_STARTUP=true REQUIRE_POSTGRES_EXPORT_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/backupasset/export \
  -run '^TestExportBehaviorPostgres$/(ReadySourceLease|ReadyIntegrity)' -count=1
ok  xirang/backend/internal/backupasset/export  5.535s

go test ./internal/backupasset/export ./internal/backupasset/runtime -count=1
ok  xirang/backend/internal/backupasset/export   6.420s
ok  xirang/backend/internal/backupasset/runtime  2.996s

go test -race ./internal/backupasset/export ./internal/backupasset/runtime \
  -run 'Ready|SourceLease|Integrity|Pin|Reconcile|Barrier' -count=1
ok  xirang/backend/internal/backupasset/export   6.219s
ok  xirang/backend/internal/backupasset/runtime  1.752s

go test -race ./internal/backupasset/export ./internal/backupasset/runtime -count=1
ok  xirang/backend/internal/backupasset/export   18.559s
ok  xirang/backend/internal/backupasset/runtime  5.707s

go vet ./internal/backupasset/export ./internal/backupasset/runtime
# exit 0, no output

gofmt -l internal/backupasset/export/worker.go \
  internal/backupasset/export/worker_test.go \
  internal/backupasset/export/behavior_integration_test.go \
  internal/backupasset/runtime/export_runtime.go \
  internal/backupasset/runtime/export_runtime_test.go
# exit 0, no output
```

Static lock-order review found one `pinSealed` caller. `consumeAndPin` completes
the Store pin, KEK lookup and ciphertext authentication before
`AttemptCoordinator.MaintainSourceLeases` opens its GORM transaction. No owned
production path acquires the Store mutex while holding a GORM row lock. Eager
and lazy caller-owned KEKs and unwrapped DEKs are cleared on returns and panics;
tokens retain no plaintext key material.

The independent resource audit separately reported runtime shutdown graph-loss
and ignored startup `Store.Close` errors in `export_runtime.go`. Those concerns
are outside this ready-integrity tranche and were not modified here; the
controller received them for follow-up. No quota, Store implementation,
settings, lifecycle, schema, staging, commit, push, or PR operation was
performed in this tranche.

All six owned files are currently untracked, so the final whitespace gate used
`git diff --no-index --check /dev/null <file>` for each file and accepted only
the expected status `1` with empty output; the aggregate wrapper exited `0`
with no diagnostics. The ordinary owned-path `git diff --check -- <files>` also
exited `0` with no output. No index mutation was performed.

## 2026-07-26 Direct Step 10 Boundary Acceptance

This controller-session evidence is a current focused acceptance record only.
It does not close Step 10, authorize Step 11, or replace the required
cross-engine and full-project gates.

### Worker/delivery fence lifecycle

The worker/delivery fence repair was independently accepted from the current
four-file snapshot. The following targeted rejection, lifecycle, and source
takeover selectors passed, followed by the same fence/takeover group under
`-race -count=20`:

```text
go test ./internal/backupasset/export -run '^(TestPersistentAttemptLoaderRejectsForgedFenceDigestBeforeKeyLookup|TestPersistentWorkerReconcileRejectsForgedSealingFenceBeforeKeyWorkOrSourceTakeover|TestPersistentWorkerReconcileRejectsFenceDigestDriftBeforeSourceTakeover|TestPersistentWorkerReadyReconcileRevokesForgedFenceBeforeSourceOrArtifactPreflight|TestDeliveryGatewayIssueExportRejectsForgedAttemptFenceDigestBeforeSideEffects|TestDeliveryGatewayServeRejectsForgedAttemptFenceDigestBeforeDeliveryRequest|TestAttemptCoordinatorTakeoverSourceLeasesPreservesDeadlineBeforeAttemptTakeover|TestAttemptCoordinatorTakeoverSourceLeasesOnlyReplacesExpiredOwners|TestAttemptCoordinatorMaintainsReadySourceLeaseWithoutResettingResult|TestPersistentWorkerRestartRevokesTamperedArtifactAndLostKeyThroughOrderedLifecycle|TestPersistentWorkerReadyReconcileRoutesMissingTamperAndLostKey|TestPersistentWorkerReadyReconcileRevokesSourceFenceLoss)$' -count=1
ok  xirang/backend/internal/backupasset/export  0.687s

go test -race ./internal/backupasset/export -run '^(TestPersistentAttemptLoaderRejectsForgedFenceDigestBeforeKeyLookup|TestPersistentWorkerReconcileRejectsForgedSealingFenceBeforeKeyWorkOrSourceTakeover|TestPersistentWorkerReconcileRejectsFenceDigestDriftBeforeSourceTakeover|TestPersistentWorkerReadyReconcileRevokesForgedFenceBeforeSourceOrArtifactPreflight|TestDeliveryGatewayIssueExportRejectsForgedAttemptFenceDigestBeforeSideEffects|TestDeliveryGatewayServeRejectsForgedAttemptFenceDigestBeforeDeliveryRequest|TestAttemptCoordinatorTakeoverSourceLeasesPreservesDeadlineBeforeAttemptTakeover|TestAttemptCoordinatorTakeoverSourceLeasesOnlyReplacesExpiredOwners|TestAttemptCoordinatorMaintainsReadySourceLeaseWithoutResettingResult)$' -count=20
ok  xirang/backend/internal/backupasset/export  16.448s
```

Direct source/test inspection confirmed forged sealing attempts make no
artifact/key/Foundation/source-takeover mutation; forged ready attempts enter
ordered revoke/drain/key-destroy/source-release/purge cleanup before source,
artifact, or KMS preflight; and ticket/serve rejection occurs before key lookup
or 000068 delivery-ledger mutation. A fresh package run, race package run, and
`go vet ./internal/backupasset/export` also passed; the race package completed
in `40.461s`.

### Archive root identity

An independent low-level archive-writer review found that a malformed or
missing `ArchiveEntry` root identity was rejected only after a cross-root
collision. The first RED was:

```text
go test ./internal/backupasset/export -run '^TestPrepareArchiveEntriesRejectsInvalidRootIdentityWithoutCollision$' -count=1
FAIL
# missing recovery point, invalid entry, negative root ordinal, and uppercase
# recovery point each returned nil instead of ErrArchiveSource.
```

`prepareArchiveEntries` now validates the complete `backupasset.AssetRef` and
nonnegative root ordinal before allocation, retaining `ErrArchiveSource` as the
closed error. Existing archive writer tests now use an explicit test-only valid
root/entry fixture for valid-input cases; the invalid-root test bypasses that
fixture. Focused archive selectors, the full Export package, and its race run
passed:

```text
go test ./internal/backupasset/export -run '^(TestWriteArchive|TestPrepareArchiveEntries|TestArchiveSuffixSeries)' -count=1
ok  xirang/backend/internal/backupasset/export  0.881s

go test ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  12.636s

go test -race ./internal/backupasset/export -count=1
ok  xirang/backend/internal/backupasset/export  40.461s
```

For current-status reconciliation, the archive-profile sub-boundary is
`closed`: the root-validation record above and the scoped-path/collision
allocator/limits record earlier in this evidence retain their exact focused
selectors and passing results. The remaining P3 instrumentation coverage
limitation is a coverage limitation, not a product failure. This sub-boundary
closure does not close Step 10, authorize Step 11, or replace its remaining
cross-engine and full-project gates.

## 2026-07-27 Lifecycle, Runtime And Retry Truth Reconciliation

This section records a historical current-snapshot documentation reconciliation
from before the final runnable-gate rerun. It changed no product code, migration,
test, task status or delivery state. Its then-current Step 10/Step 11 wording is
superseded by the status at the top and the final section below; no new reviewer
approval was claimed here.

### State, worker authority and recoverable spool boundary

Current state code and `TestExecutionTransitionsAreClosedAndMonotonic` allow
`sealing -> retry_wait`. The design transition table was stale and now records
that edge. `TestPersistentWorkerRestartReturnsExpiredSealingAttemptForFreshRebuild`
proves the narrow meaning: expired sealing reconciliation moves the job to
`retry_wait`, fails and detaches the old attempt, removes its artifact/staging
object, and preserves immutable item-attempt history. A later claim uses a new
attempt ID, fence and nonce prefix and resets the mutable job/item projection to
byte zero.

`TestPersistentWorkerSealArchiveDurablyClaimsNonceBeforeConcurrentEncryption`
observes the final staging locator already durable while the winning seal is
paused before final encryption; the competing seal loses the fence before it
can encrypt. Both seal persistence and ready publication invoke live metadata
revalidation for every frozen item inside their authority transactions, covered
by `TestPersistentWorkerSealArchiveRejectsLiveMetadataDriftInAuthorityTransaction`
and `TestPersistentWorkerPublishReadyRejectsLiveMetadataDriftInAuthorityTransaction`.
Authenticated spool tamper/reopen loss before an item header is purged and may
be represented as one failed item in a partial archive; fence, deadline, quota,
cancellation and other attempt-fatal markers remain fatal. The focused
regressions are
`TestPersistentWorkerRecoversTamperedSpoolBeforeHeaderAsPartialArchive` and
`TestPersistentWorkerTreatsAuthenticatedSpoolReopenLossAsPartial`.

### Durable lifecycle scheduler and ordered KEK cleanup

Every persisted Export job owns one immutable, globally unique
`LifecycleEnqueueSequence`. The permanent global quota-bucket latch carries
`lifecycle_next_sequence`, `lifecycle_sweep_cursor`,
`lifecycle_sweep_high_water`, `lifecycle_sweep_revision` and
`lifecycle_sweep_lease_expires_at`. Legitimate first-write transactions ensure
the latch and allocate a sequence atomically. A pristine schema does not create
the latch and returns no lifecycle work; jobs without it fail closed.

`Lifecycle.Reconcile` acquires a 30-second revision-fenced logical lease,
captures a finite high-water and persists cursor progress across new
`Lifecycle` instances. New arrivals above that high-water wait for the next
sweep. Candidate scanning is bounded by `limit * 32`, capped at `10000`.
Scheduler CAS writes use `UpdateColumns`, leave quota accounting,
`transition_revision` and `updated_at` untouched, and release the global row
transaction before cleanup callbacks. Current lifecycle/service unit coverage
and the paired SQLite/PostgreSQL `000068` migration contract exercise these
properties.

Ordered cleanup remains fence attempts -> revoke deliveries -> drain streams
-> destroy the job key and encrypted selection -> release sources/non-store ->
purge ciphertext -> release store. The source-expiry crash regression proves
`MarkKeyVersionLost` resumes a durable
`source_expired`/`cleanup=revoking` job without rewriting execution state or its
safe error, destroys the exact job key/selection, marks the exact Export KEK
version lost and completes the same cleanup order.

### Runtime, API and frontend controller truth

Startup reconciles delivery, budget, source, retired, sealing, lifecycle and
orphan metadata without claiming ordinary `queued` jobs. It may finish existing
`running/retry_wait/sealing` recovery before publication. `StopAccepting` is
sticky across concurrent publication, and drain explicitly fences joined
`running/sealing` attempts with retryable `worker_unavailable` before restart
cleanup. Create notification, the 100 ms active-queue wake, GC cadence and the
source-lease heartbeat ticker remain independent mechanisms.

Current routing contains all nine approved Export/archive paths. Export routes
are Admin plus `backup_assets:export`; archive index/job routes are
Admin/Operator plus `backup_assets:preview`; the member ticket uses
`backup_assets:download`. Exact step-up purposes remain
`asset.export_create`, `asset.export_download` and `asset.download`. Generated
Swagger contains the nine paths and the exact proof descriptions. Focused typed
audit coverage still proves digest-only archive audit metadata and maps an
undeployed service failure to the closed unavailable result.

The frontend create controller freezes one input before its first proof. Each
transport attempt takes a fresh non-persisted proof. A live retry reuses the
same input object, idempotency key and original request signal. A first proof
failure sends no request and leaves no pending intent. A definitive `429`
backoff also clears pending ambiguity; dismissal or a retry-proof failure does
not reconcile it. A `503`, other 5xx, network failure or `AbortError` is
ambiguous: retries retain the frozen input, and exhaustion/proof failure leaves
that exact intent for the next reconciliation rather than adopting later UI
selection or archive options. Dismissal during an ambiguous backoff/request
uses a fresh proof and new non-aborted signal to replay the same input/key, then
cancels any durable returned job without restoring the route. The hook's
`initial`, `definitive_retry` and `ambiguous_retry` provenance is explicit and
the current 50-test file covers the proof, backoff, request and cancellation
boundaries.

### Fresh focused commands

The required real-PostgreSQL migration selector ran first against the existing
isolated PostgreSQL 18 fixture:

```text
cd backend
REQUIRE_POSTGRES_MIGRATION_TEST=1 \
TEST_POSTGRES_DSN='postgres://xirang:xirang_test@127.0.0.1:55470/xirang_test?sslmode=disable' \
go test ./internal/database \
  -run '^(TestBackupAssetMigration068.*Postgres|TestRunMigrationsPostgresDirtyCheckUsesSearchPath)$' \
  -count=1
ok  xirang/backend/internal/database  14.140s
```

The current state/worker/lifecycle selector then passed:

```text
TMPDIR=/home/murray/.cache/codex-xirang-tmp \
GOTMPDIR=/home/murray/.cache/codex-xirang-tmp \
go test ./internal/backupasset/export \
  -run '^(TestExecutionTransitionsAreClosedAndMonotonic|TestLifecycleMarksKeyVersionLostAfterSourceExpiredCrashBoundary|TestLifecycleReconcileAdvancesPastBoundedPersistentFailures|TestLifecycleReconcileAdvancesSameRevokingLaneAfterRestartDespitePersistentFailureAndNewerArrivals|TestLifecycleReconcilePersistsProgressPastAFullFailureWindowAcrossRestarts|TestLifecycleReconcileRejectsAJobWithoutThePermanentGlobalLatch|TestLifecycleReconcilePristineSchemaDoesNotCreatePermanentGlobalLatch|TestLifecycleReconcileGlobalLogicalLeaseExcludesConcurrentReconcilers|TestLifecycleSweepExpiredLeaseTakeoverRejectsStaleRevision|TestLifecycleReconcileDefersArrivalsAboveFiniteSweepHighWater|TestLifecycleReconcileReleasesGlobalRowBeforeCleanupCallback|TestLifecycleReconcileAdvancesInteriorExpiryAcrossRestartDespitePersistentFailuresAndNewerArrivals|TestPersistentWorkerSealArchiveRejectsLiveMetadataDriftInAuthorityTransaction|TestPersistentWorkerPublishReadyRejectsLiveMetadataDriftInAuthorityTransaction|TestPersistentWorkerRecoversTamperedSpoolBeforeHeaderAsPartialArchive|TestPersistentWorkerTreatsAuthenticatedSpoolReopenLossAsPartial|TestPersistentWorkerRestartReturnsExpiredSealingAttemptForFreshRebuild|TestPersistentWorkerSealArchiveDurablyClaimsNonceBeforeConcurrentEncryption)$' \
  -count=1
ok  xirang/backend/internal/backupasset/export  0.558s
```

Runtime and API boundary selectors passed independently:

```text
TMPDIR=/home/murray/.cache/codex-xirang-tmp \
GOTMPDIR=/home/murray/.cache/codex-xirang-tmp \
go test ./internal/backupasset/runtime \
  -run '^(TestManagedExportWorkerStartupReconcilesMetadataWithoutExecutingQueuedExports|TestManagedExportRuntimeStopAcceptingStaysStickyAcrossConcurrentStartupPublication|TestManagedExportWorkerDrainFencesJoinedActiveAttemptsForImmediateRestart|TestManagedExportWorkerRunWakesQueuedWorkBeforeGCCadence|TestManagedExportWorkerContinuesAfterRecoverablePreHeaderSpoolFailure|TestManagedExportWorkerContinuesAfterRecoverablePreHeaderSealFailure|TestManagedExportWorkerRunMaintainsSourceLeasesAtHeartbeatCadence)$' \
  -count=1
ok  xirang/backend/internal/backupasset/runtime  0.107s

TMPDIR=/home/murray/.cache/codex-xirang-tmp \
GOTMPDIR=/home/murray/.cache/codex-xirang-tmp \
go test ./internal/api/... \
  -run '^(TestRouterRegistersBackupAssetExportAndArchiveMemberRoutes|TestBackupAssetExportAndArchiveRoutesUseExactRoleAndPermissionMatrix|TestBackupAssetExportHandlerCreateRequiresExactExportCreatePurpose|TestBackupAssetExportHandlerDownloadTicketRequiresExactExportDownloadPurpose|TestBackupArchiveHandlerDeliveryTicketRequiresExactAssetDownloadPurpose|TestBackupArchiveHandlerAuditsOnlyIndexAndMemberDigests|TestBackupArchiveHandlerListAuditsUndeployedFailureAsUnavailable|TestBackupAssetProtectedSwaggerDocumentsAuthFailures|TestBackupAssetSwaggerDocumentsStrictRequestDTOs)$' \
  -count=1
ok  xirang/backend/internal/api           0.072s
?   xirang/backend/internal/api/docs      [no test files]
ok  xirang/backend/internal/api/handlers  0.075s
```

The exact frontend hook file passed with the repository-required non-production
React environment:

```text
cd web
env -u NODE_ENV TMPDIR=/home/murray/.cache/codex-xirang-tmp \
  npx vitest run src/features/backup-assets/use-backup-asset-export.test.tsx
Test Files  1 passed (1)
Tests       50 passed (50)
Duration    1.31s
```

An initial parallel attempt did not execute the Go tests because concurrent
linkers exhausted the remaining `/tmp` tmpfs (`No space left on device`); its
Vitest import also hit the same write limit. After moving temporary outputs to
the repository filesystem, a Vitest invocation that failed to unset the shell's
`NODE_ENV=production` loaded React's production build and rejected `act()` in
all tests. Neither environment-only attempt is product evidence. The canonical
sequential commands above address both causes and are the recorded results.

These are focused current-snapshot selectors and documentation truth only. They
do not replace the remaining race, aggregate, cross-layer, full-project,
browser, CI or delivery gates; do not close Step 10; and do not authorize Step
11, staging, commit, push or PR activity.

## 2026-07-28 Focused Scroll-Focus Accessibility Checkpoint

The controller-approved accessibility amendment adds only the existing tracked
`web/src/index.css` and `web/src/index-css.test.ts` for the global reduced-motion/
power-save behavior. The list focus ring remains owned by the already-listed
export panel files. The later route-cleanup amendment adds existing tracked
`web/src/lib/api/core.ts`. The exact current scope is `8` Phase-1 + `56` create
+ `65` modify = `129`; the product scope and thirteen corrections are unchanged.

The export-job list scroll-focus repair recorded a genuine TDD RED for missing
`tabindex` (`1` failed / `7` passed), followed by GREEN `8/8`. TypeScript
passed, ESLint had `0` errors and one configured debt warning, and the scoped
diff check passed. Independent spec review was `APPROVED`; independent
code-quality review was `APPROVED` with no findings.

After the subagent dispatcher failed, the controller directly ran the final
read-only Chromium 150 gate captured in
`/tmp/c12-browser-recheck/evidence/browser-evidence.json` and the `1200x900`
and `390x844` screenshots. Both viewports had `tabindex` attribute/property
`0`, active `:focus-visible`, PageDown and ArrowDown scrolling while focus
remained active, no horizontal overflow, and a visible inset focus ring. Axe
reported `0` violations and `0` incomplete with the project-consistent
color-contrast exclusion in the isolated harness; motion was `0.01ms` under
reduced motion and `80ms` under no preference; console/API/exception events
were all zero.

This was a focused browser/accessibility checkpoint only. At that time the final
exact-129/static/full frontend/full-project/review rerun had not yet occurred.
The final section supersedes that status; the historical checkpoint left
`staged=0`.

## 2026-07-28 CI-Equivalent Dependency Audit Blocker

### Fresh RED

The required command failed on the unchanged HEAD package files:

```text
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
4 vulnerabilities (1 moderate, 3 high)
```

The findings were `brace-expansion`, `postcss`, `react-router`, and
`react-router-dom`. The same command inside `node:20-bookworm-slim` using Node
`v20.20.2` and npm `10.8.2` reproduced the exact four-vulnerability result, so
this is not a local npm 11 discrepancy.

### Minimal compatible probe and root cause

A disposable `/tmp` copy of only `package.json` and `package-lock.json` ran
`env -u NODE_ENV npm audit fix --package-lock-only --ignore-scripts`. It changed
only the copied lockfile and resolved the older versions to:

```text
react-router-dom 7.18.1
react-router     7.18.1
postcss          8.5.23
nanoid           3.3.16
brace-expansion  1.1.16 / 5.0.8
```

Fresh re-audit still failed with eight high-severity dependency nodes. The two
remaining advisory families have no compatible published closure: the
maintenance brace-expansion resolution is still covered by the new advisory,
and React Router's only clean release is `8.3.0`. `react-router@8.3.0` requires
Node `>=22.22.0`, React `>=19.2.7`, and ReactDOM `>=19.2.7`; this repository and
CI intentionally use Node 20 and React 18. `react-router-dom` itself has no 8.x
release. npm's suggested `--force` path is therefore neither a lockfile-only
fix nor a compatible Child 12 change.

### Historical Disposition

At that historical audit checkpoint, the proposed lockfile-only manifest
amendment was rejected by fresh evidence.
Neither `web/package.json` nor `web/package-lock.json` was edited; their SHA-256
values remain `db0be2d10de74a3c0489dff41944b2de2bf21b9d019a82661aefa88f1118e571`
and `c59e50da9e648ecab0bfc435b14d32051f1a7d63bc9062275b07dd9cedcb9be6`.
No `--force`, vulnerable downgrade, hidden major override, staging or delivery
action ran. The independent `core.ts` amendment makes exact scope
`8 + 56 + 65 = 129`. Step 10 could continue every
other gate, but dependency audit remained `blocked_external` until upstream
publishes a Node 20/React 18-compatible fix or the repository separately
authorized a major platform/router migration. The current controller risk
acceptance at the end of this file supersedes that workflow disposition without
altering the audit result.

## 2026-07-28 Byte-Preserving Export Route Cleanup

The approved narrow correction changes only existing
`web/src/lib/api/core.ts` and the already-listed
`web/src/lib/api/backup-exports-api.test.ts`. It removes every decoded
`exportJobId` query field without reconstructing the unrelated query string.

The genuine RED was `1` failed / `244` passed. The former reconstruction
normalized `%2f` to `%2F`, `%20` to `+`, a bare `flag` to `flag=`, and removed
an empty separator. GREEN preserves unrelated raw bytes, order, duplicates,
bare flags, empty separators and the hash fragment while removing all matching
decoded fields. Fresh focused tests passed `245/245`; the API-core selection
passed `264/264`; typecheck, targeted ESLint, scoped diff and whitespace checks
passed. The independent behavior review found no remaining behavior defect; its
sole finding was that `core.ts` was absent from the exact manifest. Adding it as
the 65th modify path resolves that drift without changing product scope or the
thirteen corrections.

## 2026-07-28 Lifecycle Runtime-Stop Final Repair

The final regression
`TestLifecycleTerminalizeForRuntimeStopReportsEarlierTerminalCleanupBehindOrdinaryCursor`
recorded a genuine RED: runtime-stop returned `Complete=true` even though an
earlier terminal, non-purged cleanup row remained behind the ordinary reconcile
cursor. GREEN reports the durable blocker without rewinding the persisted
scheduler, so persistent earlier failure cannot be hidden while later actionable
rows still progress.

Fresh independent verification passed 27 top-level focused tests (31 including
subtests), the focused race selection, Export full tests and full race, vet,
gofmt, diff and whitespace checks. Runtime normal/race passed except the
unrelated host filesystem case
`TestRuntimeAuthenticatedCacheUsesSharedContentMetrics`, which fails closed as
`cache_root_unverified`. The four reviewed file hashes were unchanged from the
reviewer's initial snapshot and staged paths remained zero. A separate backend
reviewer returned `SPEC APPROVED`; the independent `trellis-check` reviewer
returned `APPROVED` with no lifecycle/runtime-stop findings.

These were focused current-snapshot facts, not aggregate Step 10, CI or delivery
evidence. Their then-current status is superseded by the final runnable-gate and
limited-delivery disposition below; the checkpoint left the Git index empty.

## 2026-07-28 Prior Exact-129 Runnable Gate And Limited Delivery Disposition

This was the exact-129 aggregate record. The historical runner reopen below
superseded its then-current status, and the current runner-amended section after
that supersedes both. This checkpoint superseded every earlier statement
that exact-129 parity, lock review, whole-change review, full frontend,
full-project, race, cross-engine or other runnable Step 10 work remained pending.
Older `passed_local`, readiness and completion-like rows are historical only.

### Exact scope, validation and static truth

Fresh child and parent `task.py validate` commands passed. JSON/JSONL parsing,
manifest extraction/classification, static scans, gofmt and diff/whitespace
checks passed. The approved scope remains exactly:

```text
8 Phase-1 + 56 create + 65 modify = 129
Git-visible union: 66 tracked + 63 untracked = 129
missing: 0
extra: 0
create/modify overlap: 0
duplicates: 0
staged: 0
```

The thirteen product corrections remain unchanged. No dependency manifest,
package lock, deploy, release or other out-of-scope path was added. The
Make-generated `backend/xirang-server` was removed after build/full-gate work
and its absence was asserted before final parity.

### Frontend and bundle truth

The focused API/core/frontend selections passed, followed by the repository
full frontend gate:

```text
Test Files  168 passed (168)
Tests       1388 passed (1388)
main JS     498.48 / 500 KiB
main CSS    104.94 / 105 KiB
```

Typecheck, lint, test and build all completed successfully. The main JS and CSS
remain under their existing hard budgets; no dependency file changed to obtain
these results.

### Backend, race, cross-engine and full-project truth

Fresh normal and race runs passed for the lifecycle/runtime-stop selection, the
full Export package and the Runtime package. `go vet`, backend lint and backend
build passed. Required real PostgreSQL migration, Export behavior and Processing/
archive-member selectors all executed against PostgreSQL and passed with zero
skips; missing required-mode coverage was not counted as success.

The first full-project attempt failed only because its temporary directory made
a Unix-domain socket path exceed the platform limit. It did not report a product,
test, lint, build or dependency failure. The corrected short-root rerun was:

```text
TMPDIR=/tmp/xc12 GOTMPDIR=/tmp/xc12 env -u NODE_ENV make check
exit 0
```

The generated backend binary was removed again and confirmed absent. Therefore,
at that checkpoint, every runnable local/review Step 10 gate was
`passed_current_runnable`.

### Dependency blocker

The unchanged dependency tree still fails the required audit command:

```text
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
4 vulnerabilities (1 moderate, 3 high)
```

There is no compatible complete Child 12 remediation. `brace-expansion` needs
an upstream-compatible release or a separately scoped lint-tool migration.
React Router needs a 7.x backport or a separately approved Node `22.22+` / React
`19.2.7+` / Router 8 migration. `web/package.json` and `web/package-lock.json`
remain unchanged; no `--force`, unsafe override or vulnerable downgrade ran.
At that checkpoint Step 10 remained `blocked_external`, not completed/pass-all,
solely on this dependency-risk row. The runner reopen below adds a current
in-repository gate without changing the external blocker.

### Limited delivery authority and order

Narrow workflow-only permission now covers exact staging/coherent commit, push,
and a **draft** PR as a CI-validation channel. Hosted CI's dependency audit is
warning-only, so otherwise-green CI does not close this blocker. The draft PR
must not become ready, merge or support an implementation/task completion claim
until compatible remediation or an explicit later risk-policy disposition.

The frozen order is:

1. exact stage and coherent commit;
2. push, draft PR, CI monitoring and same-branch fixes;
3. dependency-risk closure;
4. only then ready/merge and post-merge monitoring;
5. only afterward Trellis Child archive/journal work.

No delivery mutation in that list has executed yet and `staged=0`. The parent
remains `planning`; Child 12 must not archive it.

## 2026-07-28 Historical Runner Stdout Pipe Step 10 Reopen

This section preserves the runner-reopen evidence exactly as a historical
checkpoint. A later final full-project rerun captured a real failure in the
then-unchanged, previously frozen capabilities runner:

```text
TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation
err=read |0: file already closed
```

Focused normal repetition (`-count=100`) and race repetition (`-count=20`) did
not reproduce the low-probability schedule, but source and standard-library
contract review identified the root cause. `RunInputStream` obtains
`command.StdoutPipe()`, starts `command.Wait()` in one goroutine, and starts the
stream consumer in another. Go documents that Wait closes a StdoutPipe after
the command exits and that calling Wait before all reads complete is incorrect.
A fast leader exit can therefore close the pipe before a delayed consumer
observes EOF. Delaying Wait until consumer completion is rejected because a
descendant may inherit stdout after the leader exits and prevent the cleanup
path from joining the process group.

The approved focused amendment adds only existing
`backend/internal/backupasset/processing/capabilities/runner.go` and
`runner_test.go`. The exact target at that checkpoint became:

```text
8 Phase-1 + 56 create + 67 modify = 131
target union: 68 tracked + 63 untracked
```

The TDD contract at that historical evidence boundary was to add a deterministic
delayed-consumer regression and first observe the exact old-code
`file already closed` RED; then use a parent-owned `os.Pipe`, assign its writer
to `command.Stdout`, close the parent's writer copy after successful Start,
retain concurrent Wait/process-group cleanup, and run focused normal/race plus
the corrected short-TMP full gate. At that historical checkpoint, no runner
code or test had yet been edited, so RED/GREEN and fresh exact-131 results were
pending. Step 10 was `reopened_in_progress`; Step 11 was
`suspended_pending_runner_rerun`. The dependency audit remained independently
`blocked_external` at `1 moderate + 3 high`, and staged remained zero.

## 2026-07-28 Historical Pre-Commit Runner-Amended Runnable Closure

This is the historical final pre-commit aggregate record. The deterministic
`TestRunnerStreamsToolStdoutToConsumerAndJoinsOnCancellation` regression first
failed on the old `StdoutPipe` implementation with exit `1` and:

```text
read |0: file already closed
```

GREEN changed only the approved pipe-ownership boundary: `RunInputStream`
creates a parent-owned `os.Pipe`, assigns its writer to `command.Stdout`, closes
the parent's writer copy after successful `Start`, leaves the consumer as reader
owner, and retains concurrent `Wait` plus process-group cleanup. Fresh focused
verification passed the exact regression, the full capabilities package, the
package under `-race`, the regression at `-count=20`, and:

```text
go vet ./internal/backupasset/processing/capabilities
```

Fresh structured manifest and static verification passed with:

```text
8 Phase-1 + 56 create + 67 modify = 131
Git-visible union: 68 tracked + 63 untracked = 131
missing: 0
extra: 0
create/modify overlap: 0
duplicates: 0
staged: 0
```

The corrected full-project command passed:

```text
TMPDIR=/tmp/xc12 GOTMPDIR=/tmp/xc12 env -u NODE_ENV make check
exit 0
```

Backend lint reported `0 issues`, and all backend packages passed. Frontend
typecheck, lint, test and build passed with `168` files / `1388` tests; ESLint
reported `0` errors plus one configured warning. Bundle budgets passed at main
JS `498.48/500 KiB` and CSS `104.94/105 KiB`.

Required PostgreSQL 18 selectors ran with zero skips and passed: migration in
`15.194s`, Export in `13.624s`, and Processing/archive-member in `4.781s`.
`backend/xirang-server` was removed and confirmed absent. Every runnable Step 10
gate is therefore `passed_current_runner_amended`.

At that checkpoint, the sole remaining Step 10 row was the unchanged dependency
audit, which exited `1` with `1 moderate + 3 high`. There was no complete compatible Node 20/React 18
remediation; neither package file changed. Overall Step 10 remained
`blocked_external`, not completed/pass-all. Step 11 was
`authorized_limited_pending`: exact-131 staging/coherent commit, push and a
draft PR/CI are permitted only as the validation channel. Warning-only green CI
cannot close dependency risk, and the PR must not become ready, merge or support
a completion claim before compatible remediation or an explicit later
risk-policy disposition. No stage, commit, push, PR, CI, merge, archive or
journal action had run; `staged=0`, and the parent remained `planning`. The
following section is the authoritative current ledger.

## 2026-07-28 Current Commit, CI RED/GREEN And Delivery Ledger

### Delivered checkpoint and exact scope

The approved manifest remains exactly:

```text
8 Phase-1 + 56 create + 67 modify = 131
```

Commit `94a15dc41634b096839ef6e661714a88db1f4c09` with subject
`feat: add backup asset export and archive` is current `HEAD`, is pushed to
`codex/backup-assets-export-archive`, and is published through draft PR #399:
<https://github.com/xiangnan0811/xirang/pull/399>. Earlier `not_executed` claims
for commit, push, PR, and initial CI are historical only.

At the start of final ledger synchronization, the product/spec dirty follow-up
was exactly five paths already included in the approved 131-path manifest:

```text
backend/internal/api/handlers/config_handler.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/backupasset/processing/derived_manifest.go
backend/internal/backupasset/processing/derived_manifest_test.go
.trellis/spec/backend/database-guidelines.md
```

This increment remains uncommitted and unstaged. The ledger synchronization
adds only its six assigned task-artifact paths, so the resulting unstaged
worktree is eleven approved-manifest paths total. It does not expand scope.

### CI RED/GREEN 1: Config import transaction rollback

RED showed that Config import swallowed Task Create errors, returned HTTP 200,
and could commit nodes without their tasks. GREEN propagates the transaction
error so the handler returns the generic HTTP 500 envelope without leaking the
injected error, and the transaction rolls back nodes and tasks in full. The
test fixture now also isolates and resets the secure global cache around its
explicit test encryption key.

Fresh focused and sequencing coverage passed. The handlers package coverage was
`57.9%`; handlers `-race`, `go vet`, gofmt, and scoped diff checks passed.

### CI RED/GREEN 2: Processing dual-boundary conflict retry

RED showed `CommitManifest` could encounter SQLite locks either while reading
projection evidence or while committing the atomic publication transaction;
the concurrent deterministic case produced successes=0. GREEN applies the
existing bounded, context-aware conflict retry to both boundaries. The prepared
publication remains idempotent across a rolled-back transient transaction, and
exactly one durable winner remains; semantic validation/fence errors are not
made retryable.

Fresh deterministic focused tests, count `50`, race count `20`, Processing
coverage `74.0%`, and the full Processing race passed.

### Fresh aggregate and cross-engine verification

After both fixes, exact manifest verification and both child and parent
`task.py validate` passed. The fresh full gate completed with:

```text
env -u NODE_ENV make check
exit 0
backend lint: 0 issues
frontend: 168 files / 1388 tests
frontend lint: 0 errors + 1 approved a11y debt warning
main JS: 498.48 / 500 KiB
main CSS: 104.94 / 105 KiB
```

Required PostgreSQL 18 selectors used the loopback test service at
`127.0.0.1:55470`, ran without skip, and passed:

```text
migrations / UTC / dirty search path: 49.561s
Export:                                13.353s
Processing / archive member:           4.679s
```

### Explicit dependency-risk acceptance

Fresh npm audit still reports four vulnerabilities (`1 moderate + 3 high`) in
brace-expansion, postcss, and react-router. The controller explicitly and
temporarily accepts this unchanged pre-existing dependency risk for purposes of
Child 12 delivery. This disposition closes the Child dependency-risk gate but
does not claim remediation or an audit pass. `web/package.json` and
`web/package-lock.json` remain unchanged. No `--force`, unsafe override, or
incompatible Node 20 + React 18 router migration is permitted. A compatible
upstream fix or Node/React/Router migration must be tracked in a separate
Trellis task and branch after Child 12 merge.

### Current workflow boundary

Step 10 is `passed_with_explicit_dependency_risk_acceptance`. Step 11 is
`active_incremental_follow_up_pending`: the initial coherent commit, push, and
draft PR are executed; the five product/spec plus six ledger paths still require
incremental review, commit, push, and required CI monitoring. The PR remains
draft. Ready, merge, post-merge automation monitoring, Child archive, and journal work are
not executed. Child 12 remains `in_progress`; the parent remains `planning`.
Nothing in this ledger completes the parent, 07-11, or P3 work. The Git index
remains empty.
