# Design: lifecycle provider-delete claim and settled-audit slots

## 1. Boundary and ownership

This change adds two durable facts beside the existing lifecycle attempt, tombstone and lease graph:

1. one fenced effect execution claim for `provider_delete`; and
2. one immutable settled-audit emission slot for each `(attempt_id, status)`.

It does not replace the selection claim, the lifecycle lease, publication reservation, tombstone semantics or audit-detail retention.

The current one-shot adapter is split because the Coordinator must keep ownership of the claim across the transaction boundary and the provider call:

```text
Worker.Advance
  -> Coordinator.deleteAndTransition
       -> Coordinator.prepareProviderDelete (Tx1)
            lock repository -> point -> attempt -> lease -> claim -> tombstone
            valid tombstone/proven wins; current live claim loses with zero mutation
            no claim: hold check -> ensure current fence -> Prepare(execution) -> insert claim
            takeover-eligible claim: hold/reference -> Prepare(observer)
              observer block: commit uncertain+reason with no lease mutation
              digest match: exact lease handling + Prepare(execution) + takeover CAS atomically
            revalidate all locked rows
          commit Tx1
       -> start Coordinator-owned renewer and child context
       -> RegistryPointDeletion.Execute(childCtx, prepared)  # no transaction
       -> stop + join renewer
       -> Coordinator.persistProviderDeleteReceipt (Tx2)
            lock repository -> point -> attempt -> lease -> claim -> tombstone
            RegistryPointDeletion.Verify (full authority + locator)
            receipt/tombstone and claim -> proven CAS
          commit Tx2
       -> Coordinator.emitSettledDeletionAuditTx (own Tx)
            lock repository -> point -> attempt -> lease -> claim -> tombstone -> slots
            derive current status, AuditSink.WriteTx, insert completed slot
```

The provider call is never made while a database transaction is open. Tx1 must commit the `in_flight` row before `Execute`; a process crash after that commit therefore leaves a durable fact instead of looking like an unclaimed attempt.

### 1.1 Ownership table

| Concern | Owner and rule |
|---|---|
| Base-row locking and claim classification; no-claim fence ensure; observer-first matched atomic lease handling/acquire/takeover | Coordinator; one repo-first transaction with the branch ordering above |
| Exact provider request materialization and freeze | `RegistryPointDeletion.Prepare` with required observer/execution profile; never commits or calls provider |
| Provider invocation | `RegistryPointDeletion.Execute`; Coordinator supplies the child context and owns its renewer |
| Post-effect exact authority/locator check | `RegistryPointDeletion.Verify` called by Coordinator inside the receipt transaction |
| receipt + tombstone + claim `proven` CAS and late-hold association | Coordinator, in one repo-first receipt transaction |
| pre-claim blocked transition | Coordinator; there is no claim row to release |
| post-acquisition failures/cancellation/uncertainty | Coordinator; leave `in_flight` or CAS to `uncertain`, never delete |
| settled-audit status derivation, slot state machine and `WriteTx` | Coordinator's `emitSettledDeletionAuditTx` |
| provider revoke/cleanup | Existing `prepareExternalEffect` path only; no durable claim in this task |

The old production `PointDeletion.DeleteRecoveryPoint` one-shot interface and bypass are removed. An internal provider-delete port may be used so the Coordinator can call the three methods without exposing the prepared value or provider credentials to other packages:

```go
type providerDeletePrepareProfile uint8
const (
    providerDeletePrepareObserver providerDeletePrepareProfile = iota + 1
    providerDeletePrepareExecution
)

type preparedPointDeletion struct { /* unexported, process-local, redacted */ }
type pointDeletionExecution struct { Result PointDeletionResult; ProviderCalled bool; Stage providerDeleteStage }

type providerDeletePort interface {
    Prepare(context.Context, *gorm.DB, providerDeletePrepareProfile, LifecyclePointRequest, lifecycleDeleteRows) (preparedPointDeletion, error)
    Execute(context.Context, preparedPointDeletion) (pointDeletionExecution, error)
    Verify(context.Context, *gorm.DB, LifecyclePointRequest, preparedPointDeletion, lifecycleDeleteRows) error
}
```

The exact interface spelling may remain package-internal, but the ownership and call boundaries are fixed. `preparedPointDeletion` must not implement a diagnostic or serialization representation that includes `Point.Native`, access bytes, credentials, commands, client handles or raw fence material.

`prepareExternalEffect` remains for `RevokeRecoveryPoint` and `CleanupRecoveryPoint`; it is not called by the provider-delete path.

## 2. Durable claim state and identity

### 2.1 Claim state machine

A missing row means no acquisition has been committed. The only legal persisted states are `in_flight`, `uncertain` and `proven`:

```text
(no row)
   -- first acquire (new target digest, new execution_id) --> in_flight
in_flight -- receipt+tombstone CAS while authority is current --> proven
in_flight -- deadline expires OR bound fence/revision becomes stale --> uncertain
uncertain -- takeover with same target digest, new executor/execution --> in_flight
proven -- immutable, prove-only
```

A claim row is never deleted, including for a pre-provider failure after acquisition. There is no release state and no fourth state. The first successful acquisition binds `target_identity_digest`; every later retry/takeover must equal it. `proven` is permanent and cannot be reset, reassigned, updated or deleted.
Stale-fence and expiry handling applies only to `in_flight` and `uncertain`; a `proven` claim is never taken over, refreshed, expired, fenced stale or rewritten.

No claim row is legal for any existing v76-valid phase/reason, including valid-tombstone tombstoning/complete. Claimed matrix: in_flight+provider_delete; uncertain+provider_delete or exactly blocked/active_hold, blocked/provider_identity_conflict, blocked/provider_native_version_referenced; proven+valid tombstone in provider_delete, late-hold blocked/active_hold, tombstoning or complete. Proven without tombstone is corruption. Every path first accepts legal no-claim rows, then validates claimed combinations.

### 2.2 IDs and snapshots

- `EffectExecutorID` is a process-unique, lower-case 32-hex value. `NewCoordinator` accepts an optional valid value for deterministic tests; an empty production value is generated from `crypto/rand`.
- Each first acquisition and takeover generates a fresh lower-case 32-hex `execution_id`, even when the executor is the same Coordinator. Same-Coordinator concurrent calls therefore cannot share a claim execution.
- Claim row IDs and slot row IDs independently use secure random 32-hex generation. None of these IDs use `CoordinatorDependencies.NewID`; that callback remains the attempt-ID source.
- Renew and `in_flight -> uncertain` retain the current execution ID. `uncertain -> in_flight` rotates executor (when applicable) and always rotates execution ID.
- A claim records the current transition revision and lease snapshots: `lease_id`, `lease_attempt_id` and `lease_fence_token_hash`. They intentionally have no foreign keys. They are immutable within one acquisition and preserve the fence evidence used to reject stale owners; only an `uncertain -> in_flight` takeover may replace them with the newly locked current lease/fence together with a rotated execution ID. No raw fence token is stored.
### 2.3 Exact target digest

Do not hash `provider.DeletePointRequest` wholesale: it mixes persisted authority with salts, raw secrets and opaque pointer/runtime values. Instead canonicalize every semantically relevant authority value and fingerprint private material before it reaches durable framing.

Build a domain-separated identity in two canonical layers:

1. **Public framing.** Use `backupasset.NewCanonicalSHA256`, with field labels and length framing, for `xirang/lifecycle/effect-target.v1`, repository ID, recovery-point ID, attempt ID, operation, repository identity, capability revision, source revision, expected source revision, provider, access repository ID/task ID/node ID, and canonicalized endpoint facts (stable sorted order). Include a schema version so adding a field cannot silently collide with the old framing.
2. **Private binding fingerprint.** Length-frame exact `Point.Native`, `Access.Locator`, raw Config/Secret, provider-specific authority, and a provider-derived remote-command authority fingerprint. Key with the exact 32-byte `IdentitySalt` through `DeriveConfigFingerprint`; add only the resulting 64-hex fingerprint to public framing. Never persist salt/private material.

Provider identity ownership is closed and fail-closed. Every real lifecycle resolver (Restic, Rsync, Rclone prefix and Rclone native) must propagate the persisted 32-byte identity salt and all required identity facts into the frozen deletion request. Missing/malformed salt or authority fails before provider invocation. Package-private authority is canonicalized inside `provider`, which returns only safe canonical bytes or a salt-keyed 64-hex fingerprint; retention never reflects or serializes private provider values.

- `RcloneNativeDeletionAccess`: type/version tag, `AuthorityDigest`, and the exact `(PhysicalKey, VersionID)` set in stable order; strip `Client`.
- `RclonePrefixDeletionAccess`: exact marker/backend/root/config/attempt/commit/private locator plus a command-authority fingerprint extracted from `RemoteCommandAccess.Node`; strip only Audit, pointer identity, live connection/client handles and telemetry.
- `RsyncPointDeletionAccess`: exact managed-root/marker/attempt/commit/source facts and any execution-authority values; strip only tree/runtime pointer handles after canonical extraction.
- Restic and future remote-command providers use the same provider-owned command-authority canonicalizer. Unknown authority, required nil values or unrepresentable fields fail before provider invocation.

Canonicalizers whitelist every value-bearing execution field and meaningful nil/present distinction, sort sets, and reject malformed data. Remote command binding includes Node Host/Port/Username/AuthType/Password/PrivateKey/SSHKeyID, BasePath/BackupDir/UseSudo/Tags and loaded SSHKey ID/Username/KeyType/PrivateKey/Fingerprint/Disabled/ExpiresAt/AllowedPurposes/AllowedNodeIDs/AllowedNodeTags. It excludes health/status/latency/disk/last-seen/last-used/audit timestamps and Audit context. Private values are keyed before leaving provider.

Tx2 replaces raw `reflect.DeepEqual` with a provider-owned stable semantic authority projector/comparator. Digest generation and Verify share one explicit whitelist/normalizer so endpoint, credentials, keys, policy, paths, nil/present distinctions and provider authority cannot drift; Verify nevertheless re-resolves current rows and independently compares frozen/current projections plus the stored digest. Audit/client identity, Node health/status/timestamps and SSH-key LastUsedAt/UpdatedAt are excluded consistently, including telemetry written by Execute itself.

Fixed-salt vectors cover all four real resolver modes. Restic/Rclone-prefix cases vary endpoint, auth type, password/private key, SSH-key lineage/policy, path and sudo fields and must mismatch before provider invocation; only opaque client/Audit handle or telemetry changes remain equal. Existing public/locator/config/secret/provider authority drift and unsupported representation also fail closed.

## 3. Coordinator claim protocol

### 3.1 Tx1 lock and observation order

`prepareProviderDelete` starts one transaction and uses a provider-delete variant of `lockLifecycleDeleteRowsTx` that does not require a caller-supplied authority before the locked attempt/lease are loaded. The complete order is:

1. resolve point's repository ID without treating it as authority;
2. lock repository;
3. lock recovery point;
4. lock lifecycle attempt;
5. lock lifecycle lease;
6. lock the attempt's claim row (if any);
7. lock the operation tombstone;
8. validate normalized point/attempt relations, then recognize valid tombstone/`proven` truth before any fence renewal/adoption, expiry handling, retry write or unproven classification;
9. classify the locked non-proven claim against the still-locked attempt/lease snapshots; a current live `in_flight` claim returns `ErrEffectClaimInFlight` with zero mutation;
10. Every takeover-eligible stale/uncertain claim—whether current lease short-live, short-expired/absolute-live, or absolute-expired—runs hold/native-reference classification and observer Prepare before ensure/TakeoverTx/AcquireTx. Observer ignores old lease authority and sets a future shared-Now deadline. A block commits only uncertain fact; slot follows separately. Digest match alone enters one transaction that validates/renews/takes over or expires/acquires the exact lease, CASes attempt, reruns execution Prepare and takes claim in_flight; any later failure rolls all authority changes back.

Lock order is repository → point → attempt → lease → claim → tombstone. Proof/live winner precede ordinary ensure, observer Prepare and adoption. Adoption is Advance-only; Heartbeat/renewer/retryBlocked cannot call it. Every proof-aware entry point bypasses ensure/adopt/block/unproven.

### 3.2 Tx1 truth and acquire cases

Within the transaction:

1. Validate the attempt operation/phase/point relation, then inspect proof without requiring or mutating current lease authority. A valid tombstone with a deletion receipt wins over stale claim/lease state and returns its validated outcome without provider work, lease renewal/adoption, revision change, retry scheduling or absolute-deadline rejection. A `proven` claim without a valid loadable tombstone is schema/data corruption and fails closed without provider work.
2. If an existing claim is `in_flight`, its deadline is evaluated only after the row is locked and with the single injected `Now`:
   - deadline in the future and fence/revision/lease snapshot current: return typed `ErrEffectClaimInFlight`; do not call `Prepare`, do not call provider, do not block, and do not modify the claim;
   - deadline expired or snapshots stale: mark stale acquisition uncertain only through a stored-binding WHERE; caller need not be old executor. Continue to hold/observer classification.
   - a current `uncertain` claim is takeover-eligible.
3. Live foreign and same-executor claims are observations; return `ErrEffectClaimInFlight`, candidate execution ID unpersisted.
4. Every existing stale/uncertain claim observer-classifies before lease mutation. Hold or native reference commits uncertain+block without adoption; native reservation is free for its sibling. retryBlocked re-observes and resumes provider_delete only.
5. Digest mismatch similarly blocks without adoption in all lease-time states. Match enters the atomic exact-lease mutation + execution Prepare + takeover transaction.
6. Only no-claim first acquisition may ensure before execution Prepare and insert a future-deadline in_flight binding.
7. Takeover rotates execution/snapshots only at uncertain→in_flight; observer blocks preserve history.

Tx1 commits before the provider call. A failed transaction rolls back an attempted first insert, so an error before acquisition leaves no row; once the insert/update commits, no error path may delete the row.

### 3.3 Renewer and execution

Prepare profiles are non-destructive. Observer uses `context.WithDeadline(min(parent, now+EffectClaimTTL))` and no lease validator; execution uses current/fresh absolute deadline and active-authority validator. Coordinator, LeaseService, Registry, repository.Service, publication and native lazy snapshot use the same Now. Execute alone may materialize network clients/provider work.

Each renewal transaction starts at repository, locks point -> attempt -> lease -> claim -> tombstone, rejects valid tombstone/proven truth, and verifies the complete execution/executor/fence/revision/digest binding. It calls `LeaseService.RenewTx` with the bound lease attempt/fence to validate and renew; it never calls `ensureLifecycleFenceTx` and never adopts/rotates a fence or transition revision. After refreshing the lease row and absolute authority deadline, it CASes both `heartbeat_at` and `deadline_at=min(now+EffectClaimTTL, refreshed lease absolute deadline)` under the same binding. The next wake uses `EffectClaimAfter` with a safe lead before the earliest remaining claim or lease-renewal deadline; production defaults to `time.After`, while tests inject controlled channels and advance the shared clock. A failure or absolute deadline cancels only the Execute child context and cannot authorize ordinary Heartbeat takeover.

Coordinator stops and joins the renewer before opening receipt Tx2, so renewal cannot race receipt CAS. It never cancels the parent or directly blocks the attempt. Parent cancellation before acquisition returns with no claim; after acquisition it returns the parent error without blocked/unproven classification and may best-effort CAS the still-matching claim to `uncertain` using a detached bounded persistence context.

`RegistryPointDeletion.Execute` sets `ProviderCalled=true` immediately before calling `PointDeleter.DeletePoint`, returns `provider_invoke` stage with the result/error, and preserves `errors.Is` through `%w`. `Prepare`, Coordinator claim acquisition and `Verify`/receipt persistence add the corresponding pre-claim, claim and post-effect stages around that provider stage. A malformed result, provider WORM, native-version reference, unavailable error, cancellation or identity error returned after the call began is provider-stage uncertainty; it is not a definite no-effect proof. In particular, a multi-version Rclone WORM result may follow earlier successful deletes and must remain retryable uncertainty.

### 3.4 Error and stage handling in `deleteAndTransition`
Stage tags are explicit and preserved through wrapped errors: `pre_claim`, `claim_observe`, `claim_acquire`, `claim_renew`, `provider_invoke`, `verify`, `receipt_persist` and `audit_emit`. The Registry execution reports provider-stage truth; Coordinator adds claim/receipt/audit stages around it. Every error path is tagged exactly once at its boundary, and all wrappers preserve `errors.Is`.

`deleteAndTransition` must branch on explicit stage truth before the existing `providerDeletionBlockedReason` default:

| Stage / error | Coordinator action |
|---|---|
| valid tombstone or proven claim | Prove/reuse the provider result; never invoke provider or regress to unproven. Provider/fence retry is forbidden; only a failed slot emission may invoke the separate audit-only retry scheduler |
| `ErrEffectClaimInFlight` | Pure loser observation: return without writing `retry_at` and without mutating attempt, lease, claim or tombstone; no block, no `blockUncertainEffect`; normal worker scheduling owns a later tick |
| no-row pre-claim error | legacy block behavior. Any existing stale/uncertain claim first observer-classifies hold/native-reference/digest before lease mutation; block fact commits, then slot Tx |
| claimed lease short-live, short-expired/absolute-live, or absolute-expired | observer first; mismatch/reference/hold no-adopt block, match enters atomic exact lease mutation+execution Prepare+takeover; never ensure first |
| an error after acquisition but before provider invocation | Matching owner may retain/cas uncertain and schedule an effect retry only while the legal attempt combination remains `provider_delete`; never delete or block unproven |
| any `ProviderCalled=true` error/result-invalid/WORM/native/unavailable/identity error | Matching owner keeps/cases uncertain in `provider_delete`, schedules effect retry, never calls `blockAuthorized` |
| Tx2 Verify, receipt insert, deadline or CAS failure after provider invocation | Post-effect stage; leave claim `in_flight`/`uncertain`, never delete; stale owner cannot mutate current claim or attempt |
| valid result with receipt transaction authority current | `persistProviderDeleteReceipt`; then emit settled audit |

The stage wrapper must distinguish Tx1 identity/hold from Tx2 identity/persist; switching only on `ErrPointDeletionIdentityConflict` is forbidden because the existing mapper aliases both. `providerDeletionBlockedReason` remains usable for pre-provider blocked errors from the old lifecycle paths, but it is never the fallback for a provider-stage error.

Every takeover-eligible claim uses full stored binding to CAS stale→uncertain, then observer-classifies before any lease mutation in every lease-time state. On digest match, exact lease handling, attempt/execution Prepare and takeover commit together or roll back. Observer block commits only uncertain+reason; no generic unproven mapping.
Heartbeat behavior is scoped: valid proof returns success/no mutation; any provider-delete claim is observe-only (no ensure/adopt/Renew/block); no-claim phases—including Revoke/Cleanup—retain existing ensure+RenewTx. Claim-aware retryBlocked never calls legacy pre-claim resume/adopter. Audit retry is the only retry_at-only exception.

### 3.5 Receipt transaction

`persistProviderDeleteReceipt` opens a transaction with order repository → point → attempt → lease → claim → tombstone. It must:

1. re-read/lock current rows and call `RegistryPointDeletion.Verify` with the current locked rows and the original prepared value;
2. require claim `state=in_flight`, matching attempt/executor/execution/fence/revision/digest, and `deadline_at > now`;
3. insert the immutable provider tombstone or validate an existing matching tombstone (result and receipt digest must match);
4. CAS the claim to `proven` with all immutable identity fields unchanged; and
5. recheck active hold in the same transaction. If a late hold exists, keep the tombstone and proven claim, then block the attempt as `active_hold` without releasing the claim.

Receipt mismatch/expiry aborts stale-owner writes. Valid tombstone wins; proven without tombstone fails closed. `settleLifecycleLeaseAfterProofTx` is shared by Advance/tombstoneAndComplete, proven retryBlocked, confirmSettledProviderDelete post-audit and generic proof transition. For exact active lease: ReleaseTx only while both short expiry and absolute are future; if short expiry has elapsed (whether absolute is future or elapsed), proof-specific CAS exact active→expired. Exact released/expired is idempotent. A historical lease with a rebound owner, holder, attempt or fence is not current deletion authority: leave that lease unchanged while valid proof continues audit/tombstoning/completion. Proof recognition must precede lease identity checks as well as live/deadline checks; without proof, identity and authority checks remain mandatory. The audit candidate still requires a loadable lease (section 4.2); proof-first is not permission to repair missing relational data. Never ensure/adopt/block/unproven/provider; Worker Heartbeat-first is therefore safe.

### 3.6 Retry/prove table

| Locked fact | Action |
|---|---|
| legal no claim | existing phase behavior; valid tombstone proof progresses; non-candidate audit no-op |
| valid tombstone/proven | Heartbeat no-op; proof settle/audit/completion |
| live current in_flight | ErrEffectClaimInFlight; zero mutation |
| takeover-eligible any lease, observer block | uncertain+matching block then slot; no lease mutation |
| takeover-eligible any lease, digest match | atomic exact lease handling + execution Prepare + takeover |
| no-claim pre-effect error | legacy observational block |
| post-invocation error | bound uncertainty; never claim delete |

## 4. Settled-audit emission slots

### 4.1 Slot schema and status semantics

`recovery_point_lifecycle_audit_slots` stores one immutable completed row per `(attempt_id,status)`:

| Column | Contract |
|---|---|
| `id` | independent secure 32-hex primary key |
| `attempt_id` | 32-hex NOT NULL FK to attempts `ON DELETE RESTRICT` |
| `status` | `deleted`, `already_absent`, `blocked`, or `identity_conflict`; width 32; NOT NULL |
| `emitted_at` | UTC timestamp, NOT NULL; set for the completed row |
| `created_at` | UTC timestamp, NOT NULL |

`audit_event_id` is omitted because `AuditWriter.WriteTx` returns only an error. The slot is the permanent proof; detail rows and checkpoints are not deduplication evidence.

The state machine is:

- `(attempt, blocked)` and `(attempt, identity_conflict)` are observational slots, each at most once and allowed in either order;
- either observational status may be followed by at most one terminal success slot;
- `deleted` and `already_absent` are mutually exclusive terminal statuses;
- after either terminal status, no later status is allowed;
- an existing slot is never updated or deleted.

A duplicate `(attempt,status)` is an idempotent no-op only after validating the existing immutable row. Conflicting terminal status, status after terminal, malformed slot, or a slot that contradicts current receipt truth fails closed. If current locked truth has a valid receipt, a stale blocked caller derives `deleted`/`already_absent` instead of emitting `blocked`.

### 4.2 One writer and lock order

`settledDeletionCandidate` is the single predicate used by migration, writer, scheduler and Worker flush. It requires scoped operation and either valid tombstone/receipt (terminal) or blocked phase with `providerDeletionBlocked(reason)`/active_hold (observational). All other phases/reasons—including lease_live, lease_drain_unproven, owner_cleanup_unproven, fence_lost and mutable_retire—are non-candidate no-ops.

For a candidate, `emitSettledDeletionAuditTx(ctx, attemptID)` opens repository→point→attempt→lease→claim→tombstone→slots, accepts legal no-claim rows, derives truth and enforces one locked due check: future retry_at returns pending/no WriteTx; nil/due proceeds. It validates the matrix/state, calls WriteTx and inserts slot atomically. Existing slot is complete; missing candidate lease is corruption.

`scheduleSettledAuditRetry` re-derives the same candidate/status and only CASes retry_at. Worker flush maps writer outcome: nonCandidate/complete/succeeded continue; pending/failed stop before Heartbeat/Advance. Post-fact emit and Worker flush are the only production callers and both use this exact writer/check; there is no policy flag or bypass.

The retention `AssetAuditSink` interface gains `WriteTx`; nil sink preserves today's no-op/skip and does not create a slot. A non-nil sink must implement `WriteTx`. `retentionAssetAuditAdapter` already delegates to `AuditWriter.WriteTx`; `repository.AssetAuditSink` is a distinct interface and is unchanged. In-memory test sinks may implement the interface for non-regression tests, but AC proof uses the production writer/adapter and a synchronized or DB-backed test double.

### 4.3 Call-site boundaries

- success/block facts commit before writer; failure schedules only audit retry.
- Worker settleClaimed calls flush first. Before retry_at: zero WriteTx and zero lifecycle/provider mutation. At due: exactly one writer attempt; success/no-op may continue.
- claim-aware blocked paths re-observe; stale caller derives terminal/no-op.
- production event scanning/HasSettledDeletion is removed only after 000077 backfill. Slots survive detail purge.

## 5. Shared clock and lock invariants

One Now covers Coordinator, LeaseService, Registry, repository.Service, publication, native lazy snapshots, AuditWriter and fixtures.

Lock order is fixed. Legal no-claim v76 rows are accepted; claimed matrix has three observer blocks. Proof/live winner precedes fence. Every takeover-eligible claim observes before lease mutation.

Worker audit pending/failure stops before Heartbeat. Heartbeat is existing behavior for no-claim paths, observe-only for provider-delete claims/proof. Bound Execute renews; Advance alone takes over.

## 6. Schema migration `000077_lifecycle_effect_claim_audit_slot`

The latest existing migration is `000076_provider_native_version_reference_reason`; 000077 is additive and paired for SQLite/PostgreSQL. It must not rebuild `recovery_point_lifecycle_attempts`.

### 6.1 `recovery_point_lifecycle_effect_claims`

Required columns and constraints:

| Column | Contract |
|---|---|
| `id` | TEXT/VARCHAR(32) PK, NOT NULL, exact lowercase `[0-9a-f]{32}` |
| `attempt_id` | TEXT/VARCHAR(32) UNIQUE NOT NULL, exact lowercase ID, FK attempts `ON DELETE RESTRICT` |
| `executor_id` | TEXT/VARCHAR(32) NOT NULL, exact lowercase 32-hex |
| `execution_id` | TEXT/VARCHAR(32) NOT NULL, exact lowercase 32-hex |
| `transition_revision` | INTEGER/BIGINT NOT NULL, `> 0` |
| `lease_id` | TEXT/VARCHAR(32) NOT NULL, exact lowercase ID, historical lease snapshot without a FK |
| `lease_attempt_id` | TEXT/VARCHAR(32) NOT NULL, exact lowercase ID, historical fence snapshot without a FK |
| `lease_fence_token_hash` | TEXT/VARCHAR(64) NOT NULL, exact lowercase 64-hex; never raw token |
| `target_identity_digest` | TEXT/VARCHAR(64) NOT NULL, exact lowercase 64-hex and immutable |
| `state` | TEXT/VARCHAR(32) NOT NULL, CHECK `in_flight`, `uncertain`, `proven` |
| `deadline_at` | UTC DATETIME/TIMESTAMPTZ NOT NULL |
| `heartbeat_at` | UTC DATETIME/TIMESTAMPTZ NOT NULL |
| `created_at` / `updated_at` | UTC DATETIME/TIMESTAMPTZ NOT NULL |

Do not add redundant `recovery_point_id`, `operation` or `phase` columns. The attempt FK plus the locked attempt/point rows supplies those facts. `lease_id`, `lease_attempt_id` and `lease_fence_token_hash` are intentionally retained historical snapshots without FKs, used solely for stale-owner fencing; the current locked lease is still required for every claim decision.
Add an index supporting `(state, deadline_at)` only if query plans require it; all correctness reads are by unique `attempt_id`. Claim row IDs, target digest, execution/executor IDs and snapshots must never be nullable.

Database protections:

- claim `id`, `attempt_id`, `target_identity_digest` and `created_at` cannot change; DELETE is rejected in every state;
- `OLD.state='proven'` rejects every UPDATE;
- `in_flight -> in_flight` renewal may change only `heartbeat_at`, `deadline_at` and `updated_at`, preserving the complete acquisition binding;
- `in_flight -> uncertain` and `in_flight -> proven` preserve executor/execution/revision/lease snapshots/digest, changing only state and timestamps allowed by that transition;
- only `uncertain -> in_flight` takeover may replace executor, execution ID, transition revision, lease snapshots, heartbeat/deadline/updated timestamps; it must rotate execution ID and preserve id/attempt/digest/created_at;
- every transition revalidates exact lower-case identity formats, positive revision and NOT NULL timestamps.

Use SQLite `BEFORE UPDATE/DELETE` triggers with `RAISE(ABORT, ...)` and PostgreSQL trigger functions/triggers with equivalent fail-closed behavior.

### 6.2 `recovery_point_lifecycle_audit_slots`

Required columns and constraints:

| Column | Contract |
|---|---|
| `id` | TEXT/VARCHAR(32) PK, NOT NULL, exact lowercase 32-hex |
| `attempt_id` | TEXT/VARCHAR(32) NOT NULL, exact lowercase ID, FK attempts `ON DELETE RESTRICT` |
| `status` | TEXT/VARCHAR(32) NOT NULL, CHECK `deleted`, `already_absent`, `blocked`, `identity_conflict` |
| `emitted_at` | UTC DATETIME/TIMESTAMPTZ NOT NULL |
| `created_at` | UTC DATETIME/TIMESTAMPTZ NOT NULL |

Create unique `(attempt_id,status)` and a partial unique index on `attempt_id` where `status IN ('deleted','already_absent')`. Add append-only triggers rejecting every UPDATE and DELETE. There is no `updated_at` mutation in this append-only table; if a model includes one for GORM convention, set it equal to `created_at` on INSERT and still reject updates.

Database constraints enforce slot ordering; native-reference maps to blocked.

### 6.3 Quiesced v76→v77 cutover and exact backfill

Reject scoped provider_delete without valid tombstone. Ignore non-candidates. Exact retained event requires action=repository_purge; attempt source and recovery-point/repository relations; ItemCount and fields.item_count exactly 1; stage=settled; legal status; outcome blocked for blocked/identity_conflict and success for terminal; terminal status matching tombstone outcome. Validate chronological observational→terminal order and dedupe exact duplicates. Near misses create no slot and cause rollback if the candidate remains ambiguous. Only tombstoning/complete+valid tombstone permits terminal inference.

UpgradeCutover tests vary every signature field, wrong relations/outcome and terminal mismatch; cover normal no-claim phases/reasons as ignored, candidate ambiguity, dedupe/order, inference, and provider_delete rejection.

### 6.4 Independent downgrade guards and startup contract

ClaimUsedDown/SlotUsedDown each have (a) admission-intact real `migrator.Steps(-1)` rejection leaving version 77 clean and (b) admission-bypassed actual down-body rejection. Both preserve all rows/objects. Pristine succeeds. version>=77 schema validation checks exact tables/columns/FKs/indexes/guard semantics; clean-v77 drift fails startup. PostgreSQL fixtures use real migrator.

`validateMinimumRecoverySchema` is mandatory for version>=77: validate both tables, columns/types/nullability, attempt FKs, unique/partial indexes, claim transition/no-delete/proven triggers, slot append-only triggers and stacked admission objects. Constraints tests include clean-v77 drift for each critical class. AC fixtures apply real 000077 through migrator; AutoMigrate is not trigger evidence.

## 7. Runtime wiring and compatibility

- Add `CoordinatorDependencies.EffectExecutorID`, `EffectClaimTTL`, and `EffectClaimAfter func(time.Duration) <-chan time.Time`; empty executor securely generates process identity, empty TTL means 2 minutes, invalid TTL is rejected, and empty After defaults to `time.After`. Controlled channels are the deterministic renewer-wake seam.
- Pass one Now through Coordinator, LeaseService, Registry, repository.Service, publication, native lazy snapshots, AuditWriter and fixtures. Prepare profiles set real deadlines from it.
- Provider canonicalizers/resolvers propagate salt/exact authority; runtime keeps LeaseOwnerID.
- Runtime constructs split Registry, and all direct adapter/fake callsites migrate. ACs use real Registry/resolver/deleter.
- Add retention WriteTx only; repository sink unchanged. Worker calls slot flush before Heartbeat.
- Deployment is maintenance/quiesced v76→v77 only; mixed old/new retention workers fail the documented precondition. Revoke/Cleanup/API/frontend/publication reservation predicates stay unchanged.

## 8. Risks and rollback

| Risk | Mitigation |
|---|---|
| Same shared lease owner permits fence takeover during a live provider call | independent executor/execution IDs plus actual lease+claim renewal; wake before earliest deadline and strict late CAS |
| Coordinator starts renewer too late or cannot extend a Go deadline | Prepare freezes only the non-renewable absolute deadline; post-commit renewer starts immediately and extends DB claim/lease deadlines while cancellation enforces failures |
| Provider error is mistaken for no effect | explicit `ProviderCalled`/stage truth; every post-invocation result is uncertain unless a future explicit proof exists |
| Old owner overwrites new owner | execution + executor + fence + revision + digest predicates on renew/uncertain/receipt/owner attempt writes; only locked observer facts bypass owner binding |
| Target/access drift leaks or changes identity | real resolvers propagate persisted salt; provider-owned canonicalization yields only safe keyed material; full Verify in Tx2 |
| Slot-first transaction deadlocks with publication | every claim/slot transaction starts repository-first and locks slot last |
| Detail purge removes dedup evidence | immutable slot table is never purged by `PurgeEligibleDetails` |
| Failed audit rolls back lifecycle fact | block and receipt facts commit before the second slot transaction; failed WriteTx schedules only audit retry |
| Migration down destroys durable evidence | stacked 000077 admission and claim/slot direct guards; no attempt-table rebuild |

Rollback before migration lands: remove only 000077 migration/model/test/documentation changes. After claim path: revert split claim helpers/tests while keeping the additive schema if rows exist; do not delete claim rows. After audit path: revert only slot writer/tests, preserving rows and requiring a forward repair; never use down on used data.

## 9. Required test design

Existing tests are non-regression only; they do not prove this design unless their fixtures are cut over. `newClaimedExpiryFixture` currently uses `lifecycleDeletionFake`, which never writes a claim or calls `DeletePoint`; AC tests must use `RegistryPointDeletion` and `registryPointDeleterFake` (or an equivalent claim-aware fixture). All race fakes must synchronize calls or use a DB-backed writer.

### 9.1 Claim and provider tests

| Test (new unless marked) | Engine | Proof |
|---|---|---|
| `TestLifecycleEffectClaimDualAdvancePostgres` | required PostgreSQL | two concurrent Advance calls, one provider invocation/claim/receipt; loser `ErrEffectClaimInFlight` and no retry/blocked/unproven or other mutation |
| `TestLifecycleEffectClaimLateLoserCannotMutatePostgres` | required PostgreSQL | barrier interleavings place a loser after winner receipt and after a stale-claim takeover; loser cannot modify proven claim, new acquisition, retry time, or transitioned attempt |
| `TestLifecycleEffectClaimSameExecutorConcurrentPostgres` | required PostgreSQL | same Coordinator's concurrent acquisitions have distinct candidate executions and only winner persists |
| `TestLifecycleEffectClaimRenewsAcrossMultipleDeadlinesPostgres` | required PostgreSQL | provider call crosses multiple lease+claim renewals, including TTL greater than initial lease remaining; no takeover |
| `TestLifecycleEffectClaimCrashTakeoverPostgres` | required PostgreSQL | seeded committed in-flight/no tombstone, deadline takeover, same digest, new execution and refreshed lease snapshots; old persist rejected |
| `TestLifecycleEffectClaimStaleFencePostgres` | required PostgreSQL | shared lease owner/fence takeover while provider is blocked; old renew/cancel/late result cannot mutate winner |
| `TestLifecycleEffectClaimReceiptDeadlineRacePostgres` | required PostgreSQL | receipt lock wait crosses deadline; CAS rejected and claim remains uncertain/in-flight, no tombstone |
| `TestLifecycleEffectClaimProvenDeadlineFenceRacePostgres` | required PostgreSQL | Worker Heartbeat→Advance at live, short-expired/absolute-live and absolute-expired proof; late-hold retry; no ensure/adopt/block/provider |
| `TestLifecycleEffectClaimProofFirstRecoveryPostgres` | required PostgreSQL | valid tombstone/proven proof survives stale/rebound lease authority through audit, tombstoning and completion; no repeated provider call, proof mutation or foreign-lease settlement; corrupt proof still fails closed |
| `TestLifecycleEffectClaimPartialWORMPostgres` | required PostgreSQL | post-call multi-version WORM remains uncertain |
| `TestLifecycleEffectClaimInFlightDoesNotBlockPostgres` | required PostgreSQL | claimed Heartbeat/loser observe only; unclaimed Heartbeat still renews |
| `TestLifecycleEffectClaimAbsoluteDeadlineAfterClaimPostgres` | required PostgreSQL | observer validator ignores old lease/caller fence, mismatch no-adopt blocks, match atomic adoption, second-Prepare rollback |
| `TestLifecycleEffectClaimObserverHoldResumePostgres` | required PostgreSQL | claimed hold resumes provider_delete, not revoking |
| `TestLifecycleEffectClaimAbsoluteDeadlineAfterClaimPostgres` | PostgreSQL | all lease-time observer profile cases; short-expired mismatch/reference/hold no adoption; match atomic; second Prepare rollback |
| `TestLifecycleEffectClaimObserverHoldResumePostgres` | PostgreSQL | all lease-time hold no adoption, resume provider_delete |
| `TestLifecycleEffectClaimIdentityConflictResumePostgres` | PostgreSQL | all lease-time drift no adoption; telemetry Verify once |
| `TestLifecycleRcloneNativePrepareRenewExecutePostgres` | required PostgreSQL | injected wakes drive multiple full-binding renewals; frozen absolute deadline and registered deleter remain identical |
| `TestLifecycleLateHoldReceiptSettledAuditUsesRegistryPointDeletionPostgres` | required PostgreSQL | late hold through Registry; one provider call, proof retained, outcome-derived settled audit |
| existing cleaning uncertain-effect tests | SQLite | unchanged cleaning/revoke behavior; not provider-delete evidence |

Update all direct `DeleteRecoveryPoint` adapter tests to call Prepare/Execute/Verify or Coordinator.Advance and assert stage/provider-called truth. Update runtime/coordinator/worker fixtures and every `Deleter:` assignment; a fake returning `ErrPointDeletionWORM` through a one-shot interface is no longer a valid provider-delete fixture.

### 9.2 Audit and slot tests

| Test (new unless marked) | Engine | Proof |
|---|---|---|
| `TestLifecycleSettledAuditConcurrentBlockedTicksPostgres` | required PostgreSQL | two barriers, one `(attempt,blocked)` slot and one WriteTx |
| `TestLifecycleSettledAuditSuccessAndBlockedShareSlotWriterPostgres` | required PostgreSQL | success, blocked and late-hold paths all use the same transaction writer |
| `TestLifecycleSettledAuditWriteTxRollbackPostgres` | required PostgreSQL | first failure schedules; pre-retry Worker tick zero write/mutation; due one retry then proceeds; non-candidate no-op |
| `TestLifecycleSettledAuditAttemptIsolationPostgres` | required PostgreSQL | same status on two attempts yields two slots/events |
| `TestLifecycleSettledAuditStatusMatrixPostgres` | required PostgreSQL | observational order, duplicate no-op, one terminal, dual-terminal/post-terminal rejection |
| `TestLifecycleSettledAuditStaleBlockedCallerReDerivesReceiptPostgres` | required PostgreSQL | stale blocked request derives terminal/no-op and never emits blocked after success |
| `TestLifecycleSettledAuditDetailPurgePostgres` | required PostgreSQL in `internal/backupasset/runtime` | package-local real `retentionAssetAuditAdapter`, AuditWriter, closed non-latest purge and post-RetryAt no second write |
| existing healthy-blocked/reason-change tests | SQLite | non-regression only |

The detail-purge test lives in package `runtime` so it can construct the unexported production adapter without an import cycle or test-only API. Other slot tests remain in retention. No event scan/direct deletion counts as proof.

### 9.3 Migration, fixture and gate tests

- Extend latest migration/schema contract to 77; AC fixtures use real migrator, not AutoMigrate for trigger proof.
- latest/schema contract 77; real migrator fixtures.
- six migration roots on both engines. Claim/Slot UsedDown each contain admission-intact migrator and bypassed direct-body subtests; UpgradeCutover includes full-signature near misses and non-candidate rows.
- broad database wrapper stays first; exact DB includes six roots; retention/runtime wrappers stay in postgres-migration job.
- workflow asserts four calls/order/package/selector/DSN/zero-match.
- document quiesced upgrade and reconciliation; run focused/race/full gates.

Durable claims for `RevokeRecoveryPoint` and `CleanupRecoveryPoint`, a general audit outbox, and any lease-owner redesign require separate planning and review.
