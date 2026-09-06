# Research: current necessity review

- **Query**: Is leftover planning task `09-02-backup-lifecycle-effect-audit-idempotency` still necessary? Do the originally discovered problems still exist in current code? Is the PRD/handoff plan still reasonable?
- **Scope**: internal (current code + git refs + specs; handoff treated as claims to re-verify, not proof)
- **Date**: 2026-09-03

## Verdict

**Still necessary.** The four handoff gaps are still present in current code. Later work after 2026-09-02 thickened the baseline (native-version reservation, blocked-reason `provider_native_version_referenced`) but did not add a durable provider-effect claim, an uncertain-effect row for crash-after-effect/before-receipt, or a retention-proof settled-audit emission slot.

## Plan assessment

> 2026-09-03 later the same day: `prd.md` / `design.md` / `implement.md` now exist. Treat those files plus `research/planning-review.md` as the planning gate, not this section. This file remains a code-gap re-verification.

**Needs refinement before implementation (historical).** The two acceptance bars still matched remaining windows when this note was written. Independent review then required call-graph edits; those landed in `design.md` / `implement.md`.

Do not close or defer as obsolete. Do not start coding until the user approves the latest planning summary.

---

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/09-02-backup-lifecycle-effect-audit-idempotency/prd.md` | Deferred requirements; all AC unchecked |
| `.trellis/tasks/09-02-backup-lifecycle-effect-audit-idempotency/research/session-handoff.md` | Planning handoff from 2026-09-02; claims re-verified below |
| `.trellis/tasks/09-02-backup-lifecycle-effect-audit-idempotency/task.json` | `status=planning`, `implementation_authorized=false`, `next_gate=design_review` |
| `backend/internal/backupasset/retention/coordinator.go` | `DeleteRecoveryPoint`, `deleteAndTransition`, receipt + settled-audit path |
| `backend/internal/backupasset/retention/worker.go` | Shared `LeaseOwnerID="retention-worker"`; `settleClaimed` → `Advance` |
| `backend/internal/backupasset/runtime/retention_lifecycle.go` | Production `retentionAssetAuditAdapter` (Write/WriteTx only) |
| `backend/internal/backupasset/retention/audit.go` | `AuditRetention.PurgeEligibleDetails` deletes audit event rows |
| `backend/internal/backupasset/audit.go` | `AuditWriter.Write` / `WriteTx` / `PurgeSegmentDetails` |
| `backend/internal/model/backup_asset_lifecycle.go` | Attempt + tombstone; no effect-claim or emission-slot fields |
| `backend/internal/backupasset/repository/rclone_native_version_evidence.go` | Post-handoff publication reservation against in-flight/unproven deletes |
| `backend/internal/database/migrations/postgres/000076_provider_native_version_reference_reason.up.sql` | Latest lifecycle-adjacent migration; not a claim/slot schema |

### Task / branch state (re-verified)

- Current HEAD is `feat/backup-lifecycle-effect-audit-idempotency` at `e84be8b0cc10b696ea000443eaac805ee01b27da`.
- `main` is the same commit. The branch has **no unique commits**. Task artifacts are untracked; there is no `design.md` or `implement.md`.
- `.git/logs/HEAD`: after 2026-09-02, `feat/backup-native-version-evidence` landed (`c32ecf7e`, `f4bbab53`, then merged to main). This task branch was created from that updated main. Journal session 49 (2026-09-03) records “Follow-up 09-02 remains uncommitted.”
- No migration, model, or coordinator symbol named `effect_claim`, `emission_slot`, `audit_outbox`, or `LifecycleEffect` exists.

Handoff name drift: there is **no** `Coordinator.advanceProviderDelete`. Provider-delete advance is `Coordinator.Advance` → `deleteAndTransition`.

### Gap 1 — no durable exclusive effect claim before `DeletePoint`

**Still present.**

`RegistryPointDeletion.DeleteRecoveryPoint` (`coordinator.go`) still has two transactions around an out-of-transaction provider call:

1. Tx1 (`124:196`): `lockLifecycleDeleteRowsTx` → validate → freeze `ResolveDeletePoint` → re-lock/re-validate → resolve `PointDeleter`.
2. External effect (`201`): `deleter.DeletePoint(ctx, frozenRequest)`.
3. Tx2 (`210:243`): re-lock, re-validate locator/authority; comment at `227:229` says the provider effect has already settled and `persistProviderDeleteReceipt` will recheck hold later.

Nothing in those transactions inserts or CAS-updates a cross-process exclusive claim bound to executor + fence + phase + transition revision. `lifecycleEffectAuthority` (`60:67`) is an in-memory snapshot of existing lease fields, not a persisted in-flight claim row.

`deleteAndTransition` (`1147:1195`) is the caller:

1. `lookupProviderDeleteReceipt` — reuse tombstone if present.
2. `prepareExternalEffect` (`788:828`) — lock attempt/point, `ensureLifecycleFenceTx`, hold check, snapshot authority. This transaction **ends before** `DeleteRecoveryPoint`.
3. `DeleteRecoveryPoint` / `DeletePoint`.
4. `persistProviderDeleteReceipt` in a later transaction.

`Worker` uses a shared owner (`RetentionWorkerLeaseOwnerID = "retention-worker"`, `worker.go:23`). `ensureLifecycleFenceTx` (`2174:2232`) accepts any coordinator with that owner while the lease is live; it does not mint a per-process executor or an in-flight effect lock. Two `Advance` calls on the same `provider_delete` attempt can both miss the receipt, both leave `prepareExternalEffect`, and both invoke `DeletePoint`.

`ClaimedAt` on `RecoveryPointLifecycleAttempt` is the selection-claim timestamp, not an effect claim.

**Remaining window:** after Tx1 commit / `prepareExternalEffect` returns, before tombstone receipt exists — concurrent same-owner `Advance` (or overlapping live lease) can run the same frozen target delete twice.

### Gap 2 — crash-after-effect/before-receipt still has no persisted uncertain-effect protocol

**Still present for the crash window.** In-process deadline handling exists; it is not a substitute.

`lookupProviderDeleteReceipt` (`1197:1231`) reads `recovery_point_lifecycle_tombstones` by `(recovery_point_id, terminal_operation)` and requires `DeletionReceiptDigest`. Absence ⇒ `found=false` ⇒ `deleteAndTransition` prepares a new effect and calls the provider again.

`persistProviderDeleteReceipt` (`1234:1299`) is the first durable write of that receipt. It also rechecks hold and may `blockAttemptTx(active_hold)` **after** the provider call.

`blockUncertainEffect` (`973:1016`) exists and is used when `effectContext` cannot be built or the in-process effect deadline fires (`1162:1182`). It persist-blocks `fence_lost` only if the same authority/lease/deadline is still current. Tests:

- `TestLifecycleUncertainEffectDeadlineDurablyBlocksWithoutReplay` — in-process cleanup deadline, then **retry after new fence** (`cleanup.calls` becomes 2).
- `TestLifecycleUncertainEffectDoesNotOverwriteNewerAuthority` — stale deadline worker must not overwrite a newer fence.

Those cover live-process timeout, not process death after `DeletePoint` returns and before `persistProviderDeleteReceipt`. Crash leaves phase `provider_delete`, no tombstone, no `fence_lost` block. Restart/`takeover` treats the attempt as never executed.

`ensureLifecycleFenceTx` takeover (`2207:2230`) bumps `lease_attempt_id` / fence hash / `transition_revision` while the old owner may still be inside `DeletePoint`. There is no claim deadline that rejects the old owner’s late `persistProviderDeleteReceipt`.

**Remaining window:** `DeletePoint` succeeded (or outcome unknown) → process crash → no receipt → later executor retries `DeletePoint`. Late old-owner persist can race a new fence. `blockUncertainEffect` does not run on this path.

### Gap 3 — settled-audit scan-then-write is not a durable idempotency boundary

**Still present in production.**

`flushSettledBlockedAudit` (`1541:1571`): honor `RetryAt` → derive status → optional receipt override for late hold → `hasSettledDeletionAudit` → `writeSettledDeletionAudit`. Two separate operations.

`hasSettledDeletionAudit` (`1574:1600`):

- If `audit` implements `settledDeletionLookup.HasSettledDeletion`, use that.
- Else scan last 20 `backup_asset_audit_events` for `repository_purge` + `stage=settled` + matching `status` + `source=attempt.ID`.
- Lookup failure returns `false` (treat as missing).

Production adapter `retentionAssetAuditAdapter` (`retention_lifecycle.go:636:653`) implements `Write` and `WriteTx` only. It does **not** implement `HasSettledDeletion`. Coordinator tests implement it only on `recordingSettledAudit` / `flakySettledAudit`. Production therefore always uses the event-table scan.

`writeSettledDeletionAudit` (`1503:1534`) calls `audit.Write` (its own transaction inside `AuditWriter.Write`), not `WriteTx` with the lookup. `confirmSettledProviderDelete` (`1302:1320`) writes settled `deleted` / `already_absent` **without** calling `hasSettledDeletionAudit` first.

`AuditRetention.PurgeEligibleDetails` (`audit.go:47:93`) → `AuditWriter.PurgeSegmentDetails` (`audit.go:417:458`) **deletes** `backup_asset_audit_events` for closed segments older than `backup_assets.audit_detail_retention_days` (default 180). Checkpoints remain; event rows used as dedup evidence do not.

`TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit` second `Advance` is **before `RetryAt`**, so `flushSettledBlockedAudit` returns at `1544:1546` and does not prove post-retention or post-`RetryAt` uniqueness.

**Remaining windows:**

1. Two ticks both see “no event” and both `Write`.
2. After detail purge, scan misses and the next tick after `RetryAt` emits again.
3. Success-path settled audit has the same missing slot.

### Gap 4 — tests do not cover claim / takeover / crash / outbox / emission slot / migration

**Still present.** Existing tests cover Advisor #2/#4 baseline, not this PRD:

| Existing test | What it covers | What it does not cover |
|---|---|---|
| `TestLifecycleLateHoldReceiptSettledAuditUsesProviderResult` | Receipt reuse + settled status after late hold | Exclusive effect claim |
| `TestLifecycleUncertainEffectDeadlineDurablyBlocksWithoutReplay` | In-process deadline → `fence_lost` | Crash-after-effect; exclusive claim |
| `TestLifecycleClaimAndAdvanceUseOnePostgresLockOrder` | Selection claim + Advance lock order | Effect-claim table / publication reservation vs new rows |
| `TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit` | No rewrite before `RetryAt` | Concurrent ticks; detail purge |
| `TestSettledProviderDeleteWritesAuditBeyondClaimed` | One success emission | Emission slot / retention |

No PostgreSQL dual-executor barrier on `DeletePoint`. No used-down migration for a claim/slot schema (latest lifecycle-adjacent pair is `000076`).

### Git history after 2026-09-02

Later main work that **did** land, then became this branch’s base:

- `000075` / `000076`: rclone native version evidence; `blocked_reason=provider_native_version_referenced`; preparing siblings no longer occupy deletion reservation.
- `rejectManagedRcloneNativeDeletionReservationTx` (`rclone_native_version_evidence.go:35:69`) counts same-repository attempts in `provider_delete` or blocked with unproven/hold/WORM/unavailable/identity/fence_lost reasons. `provider_native_version_referenced` is intentionally **not** a reservation.

That work did **not** add effect-claim rows, emission slots, or outbox. It **does** change lock-order/design context: publication already observes lifecycle phase/reason while holding the repository row. A new claim table must stay consistent with that query and with `lockLifecycleDeleteRowsTx` / `lockAttemptAndPointTx` order (repository → point → attempt → lease).

### Reuse patterns elsewhere (not already wired here)

| Pattern | Where | What it is | Fit |
|---|---|---|---|
| Publication in-flight claim | `PublicationService.claimRclonePreparingPoint` / `claimRsyncPreparingPoint` | Tx: lock point, acquire/takeover lease, persist revision bump, then external work | Closest exclusive-claim shape; lifecycle-specific, not a shared library |
| Native deletion reservation | `rejectManagedRcloneNativeDeletionReservationTx` | Repo-wide “someone is deleting / unproven” gate for publication | Observes phase/reason; does not serialize `DeletePoint` |
| Overlay idempotency receipt | `overlay/idempotency.go` `loadIdempotency` / `createIdempotency` | Unique `(owner, action, key_hash)` row in the mutation tx | Caller-key replay, not worker-emitted audit |
| Export/recovery idempotency digest | `export/idempotency.go`; recovery authorization receipts | Domain-separated key digest + durable receipt replay | Same: user-supplied key, not lifecycle tick |
| Export lifecycle sweep | `export/lifecycle.go` `acquireLifecycleSweep` | Exclusive sweep row + takeover | Process mutex for a cursor, not per-effect |
| `AuditWriter.WriteTx` | `audit.go:126`; used by `MutationAuditor` | Same-tx audit write | Available for an emission slot; settled delete does not use it |
| Generic audit outbox | — | **Not found.** 2026-08-21 closeout recorded “no durable outbox”; fail-closed instead | Inventing a general outbox is new infrastructure |

`RecoveryPointLifecycleTombstone.DeletionReceiptDigest` is already the durable **success** receipt. It is not an in-flight/uncertain claim and does not cover blocked settled-audit emission.

### Related Specs

- `.trellis/spec/backend/database-guidelines.md` — paired SQLite/PostgreSQL migrations; latest version `000076`; used-down fail-closed; lock/tx conventions. Any new table is `000077+`.
- `.trellis/spec/backend/error-handling.md` — fail-closed conflict/uncertainty; no lifecycle-claim state machine yet.
- `.trellis/spec/backend/quality-guidelines.md` — paired engine tests, race, PostgreSQL parity.
- `.trellis/spec/guides/code-reuse-thinking-guide.md` (via guides index) — prefer existing claim/receipt/WriteTx shapes over a new queue.
- No spec document currently defines backup lifecycle effect claims or settled-audit emission slots.

### External References

None required. This is an internal necessity review.

---

## What is obsolete vs still open

| Handoff / PRD item | Status |
|---|---|
| Frozen target + late-hold + receipt reuse + identity recheck | Baseline; already true in current `DeleteRecoveryPoint` / `persistProviderDeleteReceipt`. Not this task’s deliverable. |
| “No uncertain-effect handling at all” | **Partially obsolete.** In-process deadline → `blockUncertainEffect(fence_lost)` exists. Crash-after-effect/before-receipt and exclusive claim do **not**. |
| Durable effect claim before `DeletePoint` | **Open.** |
| Crash window + safe takeover / late-owner reject | **Open.** |
| Settled-audit emission slot independent of event retention | **Open.** |
| Atomic emission + `WriteTx` | **Open.** Production still scan-then-`Write`. |
| PostgreSQL barrier / claim / purge / migration tests | **Open.** |
| Shared `LeaseOwnerID` as exclusive executor | **Not a substitute.** Same owner string on every worker. |

---

## PRD acceptance criteria — still the right bar?

**Yes for the remaining windows**, with two design-time clarifications (not AC deletions):

1. Dual-`Advance` / one `DeletePoint` / one claim / one terminal receipt still matches Gap 1.
2. New-fence takeover + reject late old owner still matches Gap 2.
3. Late-hold receipt reuse without a second delete is already largely implemented; keep it as a **non-regression** AC, not the new work.
4. Concurrent blocked tick uniqueness and post-detail-purge non-replay still match Gap 3.
5. Fail-closed status conflict (`deleted` / `already_absent` / `blocked` / `identity_conflict`) is still untested as a durable slot state machine.
6. Paired migrations + used-down + full suite remain required if new durable state is added.

Clarify in design (do not silently drop from AC):

- Success-path settled audit (`confirmSettledProviderDelete`) has the same missing slot as blocked flush.
- After `000076`, many blocked reasons collapse to audit status `"blocked"` via `settledDeletionStatus`. Decide whether the slot key is `(attempt, status)` as the PRD says, or `(attempt, blocked_reason)`.
- `blockUncertainEffect` today retries the external phase after a new fence. PRD “must not generate a new external effect identity” means reuse the frozen target, not “never call `DeletePoint` again.” Design must say when retry is allowed vs prove-only.

---

## Intended approach vs simpler existing mechanism

**Durable effect claim + independent audit emission slot is still the right design family.** There is no existing table that already closes both gaps.

What is **not** enough by itself:

- Lifecycle lease / `prepareExternalEffect` — shared owner, claim released before `DeletePoint`.
- Tombstone receipt — only after success; missing ⇒ retry.
- `HasSettledDeletion` interface — test-only; production scans deletable events.
- `RetryAt` — delays rewrite; does not survive purge or concurrency.
- Native deletion reservation — blocks publication, not concurrent deletes.
- Overlay/export/recovery idempotency — caller keys, not worker ticks.
- A new general audit outbox — repo has none; closeout previously rejected inventing one.

Simpler mechanisms that design should evaluate **inside** this family (not instead of the task):

- Effect claim: extra unique row vs CAS columns on `recovery_point_lifecycle_attempts` (phase + fence + in-flight executor + deadline). Tombstone stays the success receipt.
- Audit slot: unique `(attempt_id, status)` row (or column) + `AuditWriter.WriteTx` in the same transaction. Outbox only if emit must survive `Write` failure after slot insert; `confirmSettledProviderDelete` already stays on `provider_delete` and retries when `Write` fails.

---

## Concrete remaining design questions

1. **Claim state machine** — enumerate unclaimed / in-flight / proven / uncertain / takeover-eligible / terminal. Handoff forbids assuming the list.
2. **Schema** — new table vs attempt columns vs tombstone extension. Next migration is `000077+` with paired down + used-down guard.
3. **Executor identity** — today every worker is `"retention-worker"`. Claim “unique executor” needs a process-unique owner or claim token.
4. **Lock order** — unify `lockAttemptAndPointTx` / `lockLifecycleDeleteRowsTx` (repository → point → attempt → lease) with publication’s repository lock + `rejectManagedRcloneNativeDeletionReservationTx` (reads attempts). New claim rows must not invert that order.
5. **Crash protocol** — persist uncertain **before** `DeletePoint`, or infer uncertain from expired in-flight claim? Same frozen `DeletePointRequest` on retry; never new operation/target identity.
6. **Late persist** — reject `persistProviderDeleteReceipt` when fence/revision/claim owner no longer matches.
7. **Audit slot vs outbox** — slot+`WriteTx` vs slot+outbox+retry worker. No generic outbox exists.
8. **Emission key** — `(attempt, status)` vs `(attempt, blocked_reason)` after `provider_native_version_referenced`.
9. **Success + blocked paths** — one slot writer for `confirmSettledProviderDelete`, `blockAuthorized`, and `flushSettledBlockedAudit`.
10. **Scope** — PRD is provider-delete + settled purge audit only. `RevokeRecoveryPoint` / `CleanupRecoveryPoint` use the same `prepareExternalEffect` + external-call shape without a durable claim. Design must keep them out of scope or name them as a follow-up.
11. **Over-scope risk** — a general-purpose outbox or rewriting revoke/cleanup in the same change.
12. **Under-scope risk** — emission slot only for blocked ticks, leaving success-path `Write` undeduped; or claim without takeover/late-result rules.

Planning artifacts still missing: `design.md` (state machine, schema, lock order, crash protocol, slot vs outbox, `000077` rollback) and `implement.md` (migration → model → coordinator → runtime wiring → PostgreSQL tests).

---

## Recommended next action

**Refine PRD/design; stay in `planning`; do not implement; do not close.**

1. Keep `implementation_authorized=false` and `next_gate=design_review`.
2. Update PRD naming (`deleteAndTransition`, not `advanceProviderDelete`) and note post-`000076` reservation + `provider_native_version_referenced` as design inputs.
3. Write `design.md` answering the questions above, then `implement.md`.
4. Split into two implementation children only after design, if the review wants smaller PRs (claim/crash vs audit slot). Do not split before the lock-order and schema questions are answered — they share attempt rows and `000077`.
5. Reuse publication-claim + tombstone receipt + `WriteTx`; do not introduce a repo-wide outbox unless design proves slot+`WriteTx` cannot meet the AC.

## Caveats / Not Found

- Git history was read from `.git/HEAD`, `.git/logs/HEAD`, and refs (branch tip == `main` `e84be8b0`). Individual `git log` messages after merge were not re-parsed beyond the native-version commits named in the reflog and journal.
- `HasSettledDeletion` on the production adapter was not found. If another runtime wrapper implements it, it is not the adapter wired in `NewRetentionLifecycle`.
- Provider `DeletePoint` idempotency (same frozen target) was not re-proven per backend; even if backends are idempotent, concurrent/crash double-calls and late persist remain unclaimed.
