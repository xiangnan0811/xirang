# Implementation plan: fenced provider-delete execution and idempotent settled audit

This plan is now authorized and active. Planning review is complete, task metadata records user authorization, and `task.py start` moved `task.json.status` to `in_progress`.

## 0. Gates and working rules

- [x] Read the mandatory lifecycle revision brief and current code evidence.
- [x] Replace the contradicted one-shot/claim-release/audit-scan design with the Coordinator-owned split protocol in `prd.md` and `design.md`.
- [x] Obtain a fresh independent planning review of the integrated three-artifact plan. GPT-5.6-Sol and Grok-4.6 approved the corrected plan; Gemini 3.8 Flash was unavailable because the harness failed before review.
- [x] After a clean fresh review and explicit user authorization, update/clear task-specific authorization and deferred metadata, then run `python3 ./.trellis/scripts/task.py start .trellis/tasks/09-02-backup-lifecycle-effect-audit-idempotency` as the sole mechanism that changes `task.json.status` to `in_progress`; completed in degraded session-pointer mode without manually changing status.

Implementation rules:

- Work only in the files listed in the phase currently authorized. Keep the migration additive and the provider-delete cutover clean; do not add compatibility aliases for the removed one-shot path.
- Preserve existing Revoke/Cleanup and publication contracts. Do not introduce generic outbox claims, change `LeaseOwnerID`, or change API/frontend behavior.
- Inject one Now into all clock-bearing services listed in design; secure IDs only.
- Transactions use fixed lock order. No-claim v76-valid rows are legal. Claimed matrix has in-flight provider_delete, uncertain provider_delete or three observer blocks, and proven+valid tombstone allowed phases. Proven without tombstone fails closed; no event scan or claim/slot delete.
- At each phase, add behavior tests that fail for a plausible race/partial-effect bug rather than tests of private field names. Do not weaken existing assertions to make the new path pass.

## 1. Phase 1 — migration and model foundation

**Files to add/update when implementation is authorized**

- `backend/internal/database/migrations/sqlite/000077_lifecycle_effect_claim_audit_slot.up.sql`
- `backend/internal/database/migrations/sqlite/000077_lifecycle_effect_claim_audit_slot.down.sql`
- `backend/internal/database/migrations/postgres/000077_lifecycle_effect_claim_audit_slot.up.sql`
- `backend/internal/database/migrations/postgres/000077_lifecycle_effect_claim_audit_slot.down.sql`
- `backend/internal/model/backup_asset_lifecycle.go`
- `backend/internal/database/backup_asset_migrations_integration_test.go`
- a focused migration test beside the existing migration tests, preferably `backend/internal/database/lifecycle_effect_claim_audit_slot_migration_test.go`
- `backend/internal/database/provider_native_version_reference_reason_migration_test.go` used-down trigger-preservation assertions;
- `backend/internal/database/migration_schema_contract.go` (mandatory version>=77 contract)

### 1.1 Add the claim model/table

Add `recovery_point_lifecycle_effect_claims` with these non-null fields and exact contracts:
Add model types `RecoveryPointLifecycleEffectClaim` and `RecoveryPointLifecycleAuditSlot` in the existing lifecycle model file, with table names pinned to these migration names and no accidental AutoMigrate-only weakening of constraints.

- secure lowercase 32-hex primary-key `id`;
- unique secure lowercase 32-hex `attempt_id`, FK to lifecycle attempts with `ON DELETE RESTRICT`;
- lowercase 32-hex `executor_id` and fresh lowercase 32-hex `execution_id`;
- positive `transition_revision`;
- lowercase 32-hex historical `lease_id`, `lease_attempt_id` and lowercase 64-hex `lease_fence_token_hash`, all intentionally stored without FKs as historical lease/fence snapshots;
- lowercase 64-hex immutable `target_identity_digest`;
- `state` in exactly `in_flight`, `uncertain`, `proven`;
- non-null `deadline_at`, `heartbeat_at`, `created_at`, `updated_at`.

Do not copy recovery point ID, operation or phase into the claim. The attempt FK and locked rows are authority. Lease snapshots intentionally have no FK: they are immutable within one acquisition, but `uncertain -> in_flight` takeover replaces them with current locked lease/fence evidence while rotating execution ID.

Add database checks for lengths, lowercase hexadecimal values, positive revision, non-null timestamps and legal state. Add an attempt lookup unique index and only a `(state,deadline_at)` index if the query plan needs it.

Add equivalent SQLite and PostgreSQL triggers/functions:

- reject claim DELETE in every state and reject every UPDATE of `proven`;
- keep `id`, `attempt_id`, `target_identity_digest` and `created_at` immutable across every transition;
- allow same-state `in_flight` renewal to change only heartbeat/deadline/update timestamps; `in_flight -> uncertain` and `in_flight -> proven` preserve the complete acquisition binding;
- allow only `uncertain -> in_flight` takeover to replace executor/execution/revision/lease snapshots and renewal timestamps, require a rotated execution ID, and preserve permanent identity;
- assert forbidden same-acquisition rebinding and permitted stale-fence takeover rebinding on SQLite and PostgreSQL.

### 1.2 Add the audit-slot model/table

Add audit slots with 32-hex id, restricted attempt FK, status width 32 (`identity_conflict` is 17 chars), and UTC timestamps.

Create:

- `UNIQUE(attempt_id,status)`;
- a partial unique terminal index for one of `deleted`/`already_absent` per attempt; and
- append-only triggers rejecting every UPDATE and DELETE.

Do not add `audit_event_id`: `AuditWriter.WriteTx` does not return one, and the slot is the durable idempotency record. If a model convention requires `updated_at`, initialize it equal to `created_at` and reject updates; preferably omit it.

### 1.3 Migration cutover, direct down guards and schema validation

Implement paired 000077 under quiescence. Reject scoped provider_delete without tombstone; ignore ordinary non-candidates. Backfill only exact current-producer events: repository_purge; correct outcome/status; attempt↔point↔repository; ItemCount+fields.item_count=1; stage=settled/source=attempt; terminal matches tombstone; chronological slot state machine. Near misses produce no slot and rollback if candidate stays ambiguous; infer terminal only tombstoning/complete+valid tombstone.

Use one `settledDeletionCandidate` contract in SQL/runtime: scoped operation plus valid receipt, or provider-deletion/active-hold block. lease_live/drain/cleanup/fence/mutable-retire and other phases are ignored.

ClaimUsedDown/SlotUsedDown each contain two engine-parity subtests: admission-intact real migrator.Steps(-1) rejection leaving clean v77, and admission-bypassed actual embedded down-body rejection. Assert version/rows/tables/indexes/all old/new triggers/functions. Pristine succeeds.

Both engines add Constraints, ClaimTransitionRebinding and UpgradeCutover roots. Upgrade fixtures vary every exact event field/relation/outcome, non-candidates, dedupe/order, inference, ambiguity and active provider_delete. Mandatory version>=77 schema contract verifies full object shape/semantics; clean-v77 drift fails. PostgreSQL fixtures use real migrator.

**Phase 1 proof:** cutover, exact backfill, both downgrade guards, constraints and startup drift pass on SQLite/PostgreSQL.


## 2. Phase 2 — split provider-delete boundary and fenced claim protocol

**Files to update when implementation is authorized**

- `backend/internal/backupasset/retention/coordinator.go` and coordinator/worker/behavior/audit tests
- `backend/internal/backupasset/runtime/retention_lifecycle.go` and its tests
- `backend/internal/backupasset/repository/lifecycle_delete.go` and its tests
- provider deletion/access/identity files and tests needed for provider-owned canonicalization of Restic, Rsync, Rclone prefix and Rclone native authority
- every live direct adapter/fake callsite found by references/search before editing

### 2.1 Replace the one-shot interface

Before editing exported symbols, run `lsp references` on `PointDeletion.DeleteRecoveryPoint` and on `NewRegistryPointDeletion`; migrate every live caller and test fake. Then:

1. Remove the production one-shot `PointDeletion`/`DeleteRecoveryPoint` bypass.
2. Add the internal split boundary on `RegistryPointDeletion`:
   - `Prepare(ctx,tx,profile,request,rows)` requires observer|execution; observer ignores old lease status/deadline/caller fence but validates repository/point/locator/provider under a future shared-Now deadline; execution validates fresh/current authority. Both are non-network and native-lazy;
   - `Execute` alone materializes clients/calls provider and reports stage truth;
   - `Verify` independently re-resolves stable authority.
3. shared provider projector; 4. shared Now across all named services.
5. Keep `prepareExternalEffect` exclusively for revoke/cleanup.

Update runtime construction to pass the split Registry adapter as the provider-delete port. Update direct adapter tests to exercise Prepare/Execute/Verify, and update lifecycle fakes so claims are actually acquired for claim acceptance tests. Do not leave a fake that bypasses the Registry adapter in AC tests.

### 2.2 Add secure identity and claim dependencies

Extend `CoordinatorDependencies` with:

- process-unique `EffectExecutorID` (securely generated when empty);
- `EffectClaimTTL` with default two minutes and accepted bounds `[1s,1h]`; and
- `EffectClaimAfter func(time.Duration) <-chan time.Time`, defaulting to `time.After`, for controlled renewer wakes.

Generate a fresh secure execution ID for every first acquisition/takeover. Claim and audit-slot IDs use separate secure generation. `NewID` remains the attempt-ID callback only. Reject malformed configured executor IDs and invalid TTLs.

Implement target identity exactly as `design.md` specifies:

- update Restic/Rsync/Rclone prefix/native resolvers to propagate persisted salt and all exact authority;
- build public canonical framing plus a salt-keyed private fingerprint;
- add provider-owned remote-command authority canonicalization: fingerprint Node endpoint/auth/credentials/SSH-key lineage/base+backup paths/sudo/tags/key policy before stripping opaque handles; exclude only Audit, pointer/client identity and health/telemetry/last-used fields;
- keep raw native/locator/config/secret/salt/credential/command values out of persistence/logging and fail closed on missing/unknown authority.

Fixed real-resolver vectors vary Restic/Rclone-prefix Host/Port/Username/AuthType/password/private key/SSHKey and policy/path/sudo fields; each must mismatch before provider call. Opaque client/Audit/telemetry-only changes remain equal. Keep full Tx2 authority Verify separate.

### 2.3 Implement Tx1 and claim state transitions

Refactor lifecycle-row locking into a provider-delete helper that locks repository → point → attempt → lease without prematurely requiring caller authority. While those locks are held:

1. lock claim and tombstone after repository → point → attempt → lease;
2. validate normalized relations and return/progress valid tombstone/`proven` truth before any fence renewal/adoption, expiry, retry or unproven mutation;
3. classify the locked non-proven claim against the still-locked attempt/lease snapshots;
4. return `ErrEffectClaimInFlight` with zero mutation for a current, unexpired in-flight claim before evaluating hold/resolver errors;
5. Only no-claim first acquisition may ensure first. Every takeover-eligible stale/uncertain claim in short-live, short-expired/absolute-live or absolute-expired state observer-classifies hold/native reference/digest before lease mutation. Block commits uncertain fact, then slot Tx. Match alone enters atomic exact lease handling + attempt CAS + execution Prepare/recheck + takeover.
6. future deadline required for insert/takeover.

Heartbeat proof/claims observe only; no-claim retains existing behavior. Claimed retryBlocked re-observes three blocks and resumes provider_delete only. Native block leaves publication reservation free.

Refactor `deleteAndTransition` so it does not perform an unlocked receipt scan or call `prepareExternalEffect`. It invokes the provider only after Tx1 commits, with a Coordinator-owned child context and renewer. Error handling must branch on `ProviderCalled`/stage:
Use explicit stage tags `pre_claim`, `claim_observe`, `claim_acquire`, `claim_renew`, `provider_invoke`, `verify`, `receipt_persist` and `audit_emit` around every boundary; preserve `errors.Is` through wrapping. `ProviderCalled` is set immediately before the provider call, not inferred from the returned error.

- pre-claim hold/resolver/freeze errors may use the existing blocked transition only when no claim exists;
- a live in-flight claim returns `ErrEffectClaimInFlight` as a pure loser observation: do not write `retry_at` and do not mutate attempt, lease, claim or tombstone; normal worker scheduling owns a later tick;
- all claimed hold/reference/digest cases commit only observer block before lease mutation;
- match lease handling/takeover is atomic; later failure rolls back;
- post-call errors remain uncertain;
- stale owner never mutates; claim never deletes.
Add deterministic barriers for a late loser racing winner receipt and for a late loser observing a completed takeover. Both must prove the loser leaves the proven claim/new acquisition, attempt phase/revision and `retry_at` unchanged.

Keep `providerDeletionBlockedReason`/`blockedResumePhase` only for no-claim pre-provider observations; claimed observer branches are explicit. Preserve wrapped sentinels; never infer no-effect from provider error.

### 2.4 Implement renew, takeover and receipt CAS

After Tx1 commit, start renewer before Execute and stop/join before Tx2. Full-lock renewal verifies binding, calls LeaseService.RenewTx and CASes deadlines; it never ensure/adopts. All clock-bearing services use shared Now and injected wakes; failure cancels only Execute child.

Tx2 locks repository → point → attempt → lease → claim → tombstone, then:

1. calls Registry `Verify` to re-resolve current rows, compare stable frozen/current semantic authority and stored digest (not raw DeepEqual), and validate result prerequisites;
2. requires matching in-flight execution/fence/revision/digest and `deadline_at > now`;
3. writes/validates the tombstone and receipt digest;
4. CASes claim to immutable `proven`; and
5. if a late hold exists, retain proof and move to the explicitly legal proven+blocked/active_hold combination.

Route valid proof around legacy fence logic. Heartbeat is no-op. Progress uses `settleLifecycleLeaseAfterProofTx`: exact active with short+absolute future→ReleaseTx; short expired (absolute future or expired)→exact active→expired proof CAS; exact released/expired idempotent. No ensure/adopt/block/unproven/provider. For non-proven claims Heartbeat is observe-only; no-claim Heartbeat remains existing behavior.

Add/update focused tests:

- `TestLifecycleEffectClaimDualAdvancePostgres`
- `TestLifecycleEffectClaimLateLoserCannotMutatePostgres`
- `TestLifecycleEffectClaimSameExecutorConcurrentPostgres`
- `TestLifecycleEffectClaimRenewsAcrossMultipleDeadlinesPostgres` (drive actual renewer with injected wakes)
- `TestLifecycleEffectClaimCrashTakeoverPostgres`
- `TestLifecycleEffectClaimStaleFencePostgres`
- `TestLifecycleEffectClaimReceiptDeadlineRacePostgres`
- `TestLifecycleEffectClaimProvenDeadlineFenceRacePostgres` (proof three lease-time cases)
- `TestLifecycleEffectClaimPartialWORMPostgres`
- `TestLifecycleEffectClaimInFlightDoesNotBlockPostgres`
- `TestLifecycleEffectClaimAbsoluteDeadlineAfterClaimPostgres` (short-live, short-expired/absolute-live, absolute-expired observer mismatch/reference/hold no-adoption; matches atomic)
- `TestLifecycleEffectClaimObserverHoldResumePostgres`
- `TestLifecycleEffectClaimIdentityConflictResumePostgres`
- `TestLifecycleEffectClaimNativeVersionReferencedResumePostgres`
- `TestLifecycleEffectClaimRealResolverDigestVectors`
- `TestLifecycleRcloneNativePrepareRenewExecutePostgres`
- `TestLifecycleLateHoldReceiptSettledAuditUsesRegistryPointDeletionPostgres`

Use synchronized provider fakes/channels or DB barriers, not sleeps. Assert provider invocation count, claim rows/state/execution IDs, tombstone count, phase/reason, wrapped sentinels and stale-owner non-mutation.

**Phase 2 proof:** focused SQLite and required PostgreSQL tests pass; dual Advance calls provider once, absolute mismatch blocks without adoption, same-digest adoption is atomic, production Heartbeat-first proof settles after expiry, Execute telemetry cannot break Verify, partial WORM stays uncertain, and no provider failure becomes unproven.

## 3. Phase 3 — transactionally idempotent settled-audit writer

**Files to update when implementation is authorized**

- `backend/internal/backupasset/retention/coordinator.go`
- `backend/internal/backupasset/retention/audit_test.go`
- `backend/internal/backupasset/retention/coordinator_test.go`
- `backend/internal/backupasset/retention/worker.go`
- `backend/internal/backupasset/retention/behavior_integration_test.go`
- `backend/internal/backupasset/runtime/retention_lifecycle.go`
- `backend/internal/backupasset/runtime/retention_lifecycle_test.go`

### 3.1 Make `WriteTx` the retention sink contract

Add `WriteTx(ctx, tx, input) error` to retention's `AssetAuditSink`. Keep the repository package's separate interface unchanged. Ensure `retentionAssetAuditAdapter` delegates to production `AuditWriter.WriteTx`, and every test sink either implements the method or is intentionally a non-retention stub. Use the same injected clock for slot `emitted_at` and AuditWriter event timestamps.

Implement one repo-first writer around `settledDeletionCandidate`. Non-candidate is a no-op; legal no-claim rows are accepted. Candidate derives status under locks, enforces due policy/state, WriteTx then inserts slot atomically.

Wire facts after commit. scheduler uses same candidate and only CASes retry_at. Worker first calls `flushDueSettledAuditBeforeHeartbeat`:
- non-candidate/existing slot/success→continue;
- candidate missing + retry_at future→pending and stop before Heartbeat/Advance;
- candidate missing + nil/due→one writer attempt; failure stops, success continues.
Every direct writer honors an existing future retry to prevent bypass. Stale callers rederive terminal truth.

Remove production `hasSettledDeletionAudit`, event-table scans, and any `HasSettledDeletion` proof dependency. Keep no alternate slot writer.

### 3.3 Test the status machine and atomicity

Add/update:

- `TestLifecycleSettledAuditConcurrentBlockedTicksPostgres`
- `TestLifecycleSettledAuditSuccessAndBlockedShareSlotWriterPostgres`
- `TestLifecycleSettledAuditWriteTxRollbackPostgres` (first failure, pre-retry Worker zero write/mutation, due exactly once)
- `TestLifecycleSettledAuditAttemptIsolationPostgres`
- `TestLifecycleSettledAuditStatusMatrixPostgres`
- `TestLifecycleSettledAuditStaleBlockedCallerReDerivesReceiptPostgres`
- `TestLifecycleSettledAuditDetailPurgePostgres` in runtime package

Failure tests use real Worker/shared fake Now, prove provider/fence zero mutation before due, and include non-candidate no-op plus runtime detail purge.

**Phase 3 proof:** slot atomicity/state machine, Worker pre-Heartbeat ordering, and actual detail purge idempotency pass.

## 4. Phase 4 — fixtures, documentation, CI and full integration

**Files to update when implementation is authorized**

- all coordinator/worker/runtime/behavior test DB setup and model lists;
- all direct Registry adapter/fake callsites found by references/search;
- `backend/README_backend.md` current migration statement;
- `.trellis/spec/backend/database-guidelines.md` current version and baseline wording (historical 000076 entries remain historical);
- `scripts/check-backup-asset-migration.sh` and its existing checks/tests;
- `scripts/check-migration-version.sh` and `scripts/check-migration-version.test.sh` freshness checks;
- `.github/workflows/ci.yml` required PostgreSQL retention selector/runner;
- `backend/internal/database/ci_workflow_test.go` selector and required-runner assertions;
- `scripts/run-required-postgres-tests.sh` only if a small selector/contract extension is needed;
- run migration source/freshness validation after adding 000077; update the checks or fixtures if their actual latest-pair discovery requires it.

### 4.1 Fixtures and direct callers

Update all fixtures/models. Acceptance PostgreSQL fixtures run real 000077 migrator; AutoMigrate cannot prove triggers. Claim ACs use RegistryPointDeletion plus synchronized real PointDeleter fakes.
Use LSP references for exported interface/constructor changes, then search for `DeleteRecoveryPoint`, `PointDeletion`, `NewRegistryPointDeletion`, `AssetAuditSink`, `HasSettledDeletion`, `hasSettledDeletionAudit`, `Deleter:`, and all test DB model lists. No direct callsite may retain the old bypass.

### 4.2 Documentation and migration freshness

Document current version 000077, quiesced upgrade/old-worker drain, exact candidate/signature, ambiguous reconciliation, no mixed-version runtime and forward-only rollback after durable rows. Preserve history; update freshness checks and obsolete WORM expectations.

### 4.3 Required PostgreSQL CI

Keep broad `./internal/database` PostgreSQL wrapper unchanged and first in the `postgres-migration` job. Add three later DSN-required, zero-match-failing invocations in that same job:

- DB: `^TestLifecycleEffectClaimAuditSlotMigrationPostgres(PristineDown|ClaimUsedDown|SlotUsedDown|Constraints|ClaimTransitionRebinding|UpgradeCutover)$`
- retention: the lifecycle PostgreSQL acceptance slice in `.github/workflows/ci.yml`, including `TestLifecycleEffectClaimProofFirstRecoveryPostgres`; the CI contract test checks this selection against the actual acceptance test inventory.
- runtime: `^TestLifecycleSettledAuditDetailPurgePostgres$`

Workflow tests assert four invocations, order, job, package, DSN and zero-match failure. Retention coverage must be derived from the actual lifecycle PostgreSQL acceptance test set and checked against the required runner selection, not a second hand-copied selector that can omit the same AC. `TestLifecycleEffectClaimProofFirstRecoveryPostgres` is required.

### 4.4 Final validation

After all phase changes are integrated:

1. run source/freshness, UpgradeCutover/direct-down/schema-drift SQLite tests;
2. run broad plus exact DB/retention/runtime PostgreSQL wrappers;
3. run focused lifecycle normal/race tests with barriers;
4. run backend package/build/lint gates;
5. inspect zero-test protection and real migration fixture setup; and
6. re-read docs for stale claim-delete/event-scan/global-Heartbeat/mixed-version rules.

The fresh planning review and explicit user authorization occur before production edits and move the task to its repository-defined active status. The checks above gate completion/ready, not start. Do not commit unless the user explicitly asks for a commit.

**Phase 4 proof:** docs/checks target 000077; both admission/direct guards, exact selectors and real-schema fixtures are proven.

## 5. Rollback and completion checklist

- [x] 000077 up/down paired and direct constraints verified on both engines.
- [x] Existing 000076 attempt schema/triggers preserved; no copy/drop footgun.
- [x] Every one-shot provider-delete caller migrated to Prepare/Execute/Verify or Coordinator.Advance.
- [x] Claim is Coordinator-owned, no claim DELETE exists, proven is immutable, takeover rotates execution ID, and target digest never changes.
- [x] Provider invocation is outside transactions; explicit stage/provider-called truth classifies every error.
- [x] Renewers are Coordinator-owned, stop/join precedes receipt Tx2, and all claim transactions use the required lock order.
- [x] Receipt CAS requires matching execution/fence/revision/digest and `deadline_at > now`; late hold preserves proof.
- [x] Settled status is derived under lock; `AuditWriter.WriteTx` and slot insert share one transaction; no event scan remains.
- [x] Slot status matrix, uniqueness, append-only behavior and stale-caller re-derivation are tested.
- [x] Production AuditWriter/retention adapter and actual non-latest closed-segment purge are exercised.
- [x] All fixtures, model lists, docs, migration scripts and required PostgreSQL selectors are updated.
- [x] Focused tests, race tests, required PostgreSQL barriers and final backend gate pass locally for the repaired snapshot; the committed snapshot also passes required remote CI. See sections 6, 9 and 10.
- [x] Fresh independent planning review has no Important findings and explicit user start approval is recorded before implementation.
- [x] After every implementation check passes, set the repository-defined completion/ready state through the normal workflow; do not misuse the start state as a completion gate. The committed branch, PR and required remote CI evidence are recorded in section 10 and the task is ready for archival and protected merge.

If a later rollback is needed, preserve any claim/slot rows already written. Remove only unused migration/path code before data exists; once durable rows exist, repair forward rather than running a destructive down migration.

## 6. Repair evidence and review ledger (2026-09-06)

This section supersedes historical completion claims above. The reviewed base is
`e84be8b0cc10b696ea000443eaac805ee01b27da`; the head is the uncommitted working
snapshot on `feat/backup-lifecycle-effect-audit-idempotency`. The session-scoped
Trellis pointer `omp-backup-lifecycle-proof-first-20260906` targets this task.
The task remains `in_progress`; no completion or release-readiness claim is made.

### Local evidence

- New owner/holder proof-first regression expectations failed before the repair
  with `point deletion identity conflict: lifecycle lease authority changed`.
  After repair, owner/holder/attempt/fence and settled-status proof convergence
  passed normal and race runs without provider calls or proof/lease mutation.
- An isolated PostgreSQL 18 container used the exact image pinned by CI. The
  existing required runner executed all four workflow slices with a real DSN:

  | Required slice | Selected cases | Result |
  |---|---:|---|
  | Broad PostgreSQL migration parity | Workflow broad selector | PASS (752.227s) |
  | Lifecycle migration | 6 | PASS (115.339s) |
  | Lifecycle retention | 23, including enhanced ProofFirstRecovery | PASS (final F1 snapshot: 52.206s) |
  | Lifecycle runtime detail purge | 1 | PASS (2.327s) |

- `go test ./... -count=1`, `go build ./...`, and `go vet ./...` passed in
  `backend`, and were rerun successfully after the F1 test-only repair.
  `GOTOOLCHAIN=go1.26.6 golangci-lint run ./...` reported `0 issues`.
  The unpinned lint attempt crashed because the installed Go 1.27 toolchain was
  newer than the Go 1.26-built lint binary; no code warning was suppressed.
- Main reran the targeted proof-first/foreign-authority/PostgreSQL deadline
  regressions with `-race` (PASS), the dynamic CI inventory contract (PASS),
  and `scripts/run-required-postgres-tests.test.sh` (PASS). Omitting
  ProofFirstRecovery from the selector was also shown to fail the inventory test.
- All seven Go files changed in this repair are clean under `gofmt -l`, including
  the four files originally reported. Trellis context validation passed (with
  existing context-size truncation warnings); every relatedFiles path exists.

### One finding ledger

The independent discovery wave used the intended resolved models with no
fallback: `openai-codex/gpt-5.6-sol`, `xai-oauth/grok-4.6`, and
`google-antigravity/gemini-3.8-flash`. All three first passes completed before
repairs began. None found a new production-code defect. Minority findings were
retained and overlapping metadata findings were deduplicated below.

| ID | Origin / priority | Confirmed finding | Disposition |
|---|---|---|---|
| F1 | GPT / P2 | PostgreSQL proof-first due/completion checks omit full row invariance; holder is masked by owner mismatch, and the seed lease is already expired | RESOLVED: independent active rebound owner/holder cases preserve expired coverage and compare full lease/claim/tombstone after due flush and completion; normal/race PostgreSQL and final required runner PASS; all three original reviewers verified |
| F2 | GPT + Gemini / P3 | relatedFiles omits policy_test.go and task handoff/evidence does not describe the current gate and external limits | RESOLVED: added fixture and current local-only evidence, corrected handoff and gate; all three original reviewers verified; next gate is explicit commit/PR/CI authorization |
| F3 | User follow-up / P2 | Required PostgreSQL audit→tombstoning→complete proof recovery covers owner/holder but not attempt/fence rebinds | RESOLVED: isolated active attempt/fence cases now run the same full required PostgreSQL chain with full-row invariance; real required23 and focused PostgreSQL race PASS; all three original reviewers verified |
| F4 | User follow-up / P3 | Missing candidate lease corruption lacks explicit writer/retry zero-side-effect regression | RESOLVED for the stated candidate/entry matrix: terminal-proof, observational `blocked`, and observational `identity_conflict` each have independent emit/schedule missing-lease regressions and lease-present controls; all 12 cases pass normal/race with typed rejection, durable-row/absence invariance and no audit/slot/provider effects. Prior evidence was terminal-proof-only; current observational proof and its limits are recorded in section 8. |

One batched repair-and-verification cycle completed. The original GPT-5.6-Sol,
Grok-4.6 and Gemini 3.8 Flash reviewers each resolved F1/F2 with no new repair
findings. Model-change records confirm the intended families without fallback
through discovery and verification. No repeat discovery wave was needed.

### Delivery boundary

No commit, push, PR, or required remote-CI result exists for this exact working
snapshot. Commit and publication require explicit user authorization under this
task workflow. Real Restic/Rsync/Rclone/NAS end-to-end acceptance was not run;
registered deleter fakes and the real PostgreSQL schema are not evidence of those
external environments. AC9 and the final completion/ready checklist remain open
until the committed snapshot and required remote CI are evidenced. Do not merge
or describe the task as delivered on the strength of local checks alone.

## 7. Historical follow-up coverage verification (F3/F4 terminal-proof scope)

This follow-up changes only `retention/lifecycle_acceptance_test.go`,
`retention/audit_test.go` and task records. Production source, schemas, migration
files and CI selectors are intentionally unchanged. Prior broad build, migration
and `make check` results remain prior evidence, not fresh runs in this follow-up.

- A new isolated PostgreSQL 18 instance used the exact CI-pinned image. With
  `GOTOOLCHAIN=go1.26.6` and a real `TEST_POSTGRES_DSN`, the workflow-derived
  required retention runner selected all 23 top-level cases and passed (51.633s).
- `TestLifecycleEffectClaimProofFirstRecoveryPostgres` passed under `-race` with
  all six subcases: `in_flight`, `uncertain`, `active_rebound_owner`,
  `active_rebound_holder`, `active_rebound_attempt`, and `active_rebound_fence`.
  Every active rebind reaches audit → tombstoning → complete without changing
  the rebound lease or proof rows, repeating provider work, or duplicating audit.
- `TestSettledAuditMissingCandidateLeaseFailsClosed` passed normally and under
  `-race`. Its lease-present control emits a terminal deleted audit/slot; its
  independent missing-lease writer and scheduler cases reject with typed identity
  conflict and leave all durable state (including retry state) unchanged. These
  were terminal-proof writer/scheduler cases only; they did not prove the
  observational `blocked`/`identity_conflict` missing-lease branches for emission
  and scheduling. Each guard was separately removed through a temporary Go
  overlay: the corresponding terminal-proof subtest failed with a nil error,
  proving sensitivity for that coverage only. Overlays were removed.
- Main reran `go test ./internal/backupasset/retention -count=1`, the dynamic CI
  inventory contract, retention `go vet`, and retention-scoped `golangci-lint`
  with Go 1.26.6: all passed; lint reported `0 issues`. Both edited Go files are
  clean under `gofmt -l`.
- The original GPT-5.6-Sol, Grok-4.6 and Gemini 3.8 Flash lanes performed bounded
  verification of F3/F4 only, each resolving both with no new repair finding.
  Their F4 acceptance was limited to the terminal-proof writer/scheduler
  evidence and overlays above; it did not prove the observational
  `blocked`/`identity_conflict` missing-lease branches for emission and
  scheduling. Their model records still show the intended families without
  fallback. This completes the second batched repair-and-verification cycle; no
  new discovery wave or production change was needed.

## 8. Current observational missing-lease follow-up (locally verified)

This test/documentation-only follow-up changes `retention/audit_test.go` and task
records, not production logic, migrations, CI selectors or public contracts.
The existing test now uses three candidate forms crossed with two independent
entry points, each with a fresh lease-present control and missing-lease fixture:

| Candidate form | emit control + missing lease | schedule control + missing lease |
|---|---|---|
| terminal-proof (`deleted`) | PASS, normal and race | PASS, normal and race |
| observational `blocked` (`provider_worm`) | PASS, normal and race | PASS, normal and race |
| observational `identity_conflict` (`provider_identity_conflict`) | PASS, normal and race | PASS, normal and race |

Observational fixtures have no claim or tombstone; they are advanced to
`provider_delete` and then blocked with the corresponding legal reason. Each
positive emit control proves the expected status and one audit event/slot; each
schedule control independently proves a future persisted retry with no existing
slot. Missing-lease fixtures have nil RetryAt and no slot, so no retry gate or
duplicate slot masks the missing-lease rejection. Both entry points must return
typed `provider.ErrDeletePointIdentityConflict`, preserve the whole attempt
(including RetryAt/revision/heartbeat), point and claim/tombstone rows or their
absence, leave the referenced lease absent, and perform zero sink/slot/provider
prepare/execute/verify work.

Fresh main-session checks with `GOTOOLCHAIN=go1.26.6`:

- `go test ./internal/backupasset/retention -run ^TestSettledAuditMissingCandidateLeaseFailsClosed$ -count=1 -v`: PASS, all 12 controls/rejections.
- The same exact test with `-race`: PASS, all 12 controls/rejections.
- `go test ./internal/backupasset/retention -count=1`: PASS.
- `go vet ./internal/backupasset/retention`: PASS.
- `golangci-lint run ./internal/backupasset/retention/...`: PASS, `0 issues`.
- `gofmt -l backend/internal/backupasset/retention/audit_test.go`: no output.

A temporary Go overlay bypassed only the missing-lease rejection in both layers:
the early `validateProviderDeleteLeaseIdentity` branch and the later
`requireProviderDeleteLease` guard. All four observational missing-lease
subcases failed with `error=<nil>`, not a compilation/setup failure. This proves
contract sensitivity without modifying shared production files; it does not
claim that bypassing either single defensive layer alone defeats the other.
The overlay and its temporary directory were removed after verification.

Main reviewed the named matrix and the direct admission/writer/scheduler paths.
No further model-review cycle or discovery wave was run for this narrow
test/documentation repair; prior model conclusions are historical, not proof of
the newly covered observational cases. Prior PG18/full-backend/Node `make check`
results remain prior evidence; unaffected PostgreSQL/frontend checks were not
rerun in this follow-up.

No PostgreSQL instance is started in this follow-up; the prior isolated instance
was already removed. Task status
remains `in_progress`; AC9 and commit/PR/required remote-CI evidence remain open.
Real Restic/Rsync/Rclone/NAS E2E was not executed.

## 9. Final pre-delivery verification (2026-09-06)

The user explicitly authorized the normal commit, PR, required-CI, merge,
post-merge, and cleanup workflow. No new Trellis task was created.

- A fresh independent read-only review found no P0-P3 production, migration, or
  regression-test finding after F4 was completed.
- `GOTOOLCHAIN=go1.26.6 make check`, `go vet ./...`, the focused lifecycle race
  packages, migration source/version/self-tests, changed-file `gofmt`, and
  `git diff --check` passed.
- An isolated PostgreSQL 18 container using the exact CI image ran the required
  broad migration (63 selected), lifecycle migration (6), retention (23), and
  runtime detail-purge (1) slices successfully. The disposable container was
  removed afterward.
- The local CI-parity gate exposed an existing unsafe repository-internal
  `TMPDIR`: cache-mount validation failed and Linux Unix-socket paths exceeded
  their length limit. Controlled runs failed under that directory and passed
  under `/tmp`; the script now forces the short system temporary root and the
  complete local parity gate passes.
- `npm audit` then exposed `@humanfs/node` 0.16.7 through ESLint. The lockfile was
  minimally refreshed to the patched 0.16.8 release; `npm ci`, `npm audit`, and
  the full frontend gate pass.

At this pre-delivery checkpoint, AC9 and the final completion item remained open
until the committed snapshot passed the repository's required remote checks.
Real provider/NAS E2E had not been executed and was not represented as
repository-delivery evidence.

## 10. Committed snapshot and required remote CI (2026-09-06)

Pull request #497 targets `main` from
`feat/backup-lifecycle-effect-audit-idempotency`. The committed feature snapshot
is `e1080feda00bb60c0836974aa618c092326e64dd`.

- The first complete CI run, `34036766485`, passed PostgreSQL parity, frontend,
  Docker, both Worker runtime closures, both Worker build/scan jobs, migration
  UTC safety, documentation freshness and PR-title validation. Its sole failure
  was the backend race step: the concurrent different-intent recovery-plan test
  could exhaust immediate SQLite lock retries and then run global secure cleanup
  while a spawned caller was still active.
- The repair is test-only. That case now uses the production SQLite writer
  serialization options without changing the fault-injection fixtures, drains
  every caller before assertion/cleanup, and avoids fatal early exit while
  results remain outstanding. The five focused concurrency/retry tests passed
  locally under `-race -count=10`; the full recovery package race and the exact
  CI cross-package race command also passed.
- CI run `34038232448` passed all 11 jobs on `e1080fed`: backend test/build/race,
  PostgreSQL migration and behavior parity, frontend test/build/Playwright,
  Docker build and smoke, both Worker runtime closures and both architecture
  build/scan jobs, plus PR title, documentation and migration safety checks.
- GitHub reports the PR ready, non-draft and merge-clean with all checks complete
  and no unresolved review requirement.

This closes repository delivery acceptance, including AC9. Real
Restic/Rsync/Rclone/NAS E2E was not executed; it remains separate live-provider
acceptance and is not represented by these local or remote repository gates.
