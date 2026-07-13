# Asset Overlay, Derived Data, And Export Lifecycle

- **Date verified:** 2026-07-13
- **Scope:** Saved searches, favorites, user tags, recent access, archive indexes, derived previews, asynchronous batch exports, recovery-point expiry, and restart behavior.
- **Method:** The approved read-only asset-management scope was checked against current Xirang retention, task-run cleanup, encryption-key, audit, and periodic-worker behavior.

## Executive Conclusion

The lifecycle must keep five categories distinct:

1. **Backup truth:** `RecoveryPoint`, provider locator, manifest digest, commitment and verification evidence.
2. **Rebuildable Catalog/derived data:** file entries, search terms/snippets, OCR, thumbnails, converted documents, media derivatives, archive indexes.
3. **User overlay:** saved-query definitions, favorites, user tags, and recent access.
4. **Temporary exports:** an immutable selection plus its encrypted archive and delivery tickets.
5. **Audit/evidence:** sanitized security and operational events governed by their own retention.

User overlays never mutate provider data, count as verification evidence, or create an implicit retention/legal hold. Derived data inherits the source asset's authorization, sensitivity, expiry, and purge requirements.

## Current-Code Constraints

- Rsync retention currently removes directories by filesystem modification time, Restic invokes `forget --prune`, and Rclone deletes objects by minimum age (`backend/internal/task/retention.go`). None of these paths owns a `RecoveryPoint` state machine, leases, dependent-derived-data invalidation, or provider/database reconciliation.
- Task-run cleanup hard-deletes old `TaskRun` and `TaskLog` rows after clearing alert references (`backend/internal/task/manager.go`). Recovery-point lineage therefore needs a retained compact run header or copied immutable run facts rather than a mandatory FK to an indefinitely retained run row.
- Generic HTTP audit intentionally skips GET and HEAD (`backend/internal/middleware/audit.go`). Asset listing, preview, archive-member access, export creation, and download require explicit domain audit events; recent-access history is not an audit substitute.
- The reusable retention loop already performs an initial prune on startup and then idempotent periodic passes (`backend/internal/retention/worker.go`). Export, cache, Catalog, and orphan-artifact GC should reuse this lifecycle shape.
- Production already requires a stable `DATA_ENCRYPTION_KEY`; development may generate a process-only key that becomes unreadable after restart (`backend/internal/secure/crypto.go`). Existing string AES-GCM is suitable for wrapping a small per-export key, not encrypting a large archive as one database field.

## Recovery-Point Expiry Semantics

Normal expiry should proceed as an explicit state transition:

```text
committed/degraded → expiring → provider deletion → catalog purge → expired tombstone
```

- `expiring` rejects new previews, exports, restores, Worker jobs, and tickets.
- Active operations hold short renewable leases. Ordinary expiry may wait for a bounded grace period; an administrator security purge overrides leases.
- Provider deletion is idempotent and retryable. Failure produces `purge_failed`/`purge_blocked`, never a false `expired` state.
- File-level names, paths, snippets, OCR, thumbnails, converted documents, archive indexes, cached bytes, and outstanding tickets are purged when the source point expires.
- A safe recovery-point tombstone may retain opaque identity, task/provider lineage, capture/expiry times, manifest digest, aggregate counts, deletion outcome, and sanitized trust/audit references under a separate evidence-retention policy.
- Storage WORM or external lifecycle blocks must be surfaced explicitly. Xirang cannot claim successful physical deletion while provider bytes remain.

## User Overlay Rules

### Saved searches

- Persist a versioned query AST plus display name and owner; do not persist cached result membership or snippets.
- A saved query may continue matching future and other retained points.
- If it explicitly scopes an expired recovery point, show a broken/tombstoned scope and require the user to edit it. Never silently widen the search.

### Favorites and user tags

- Exact asset assignments bind opaque `RecoveryPoint` and entry identities.
- After expiry they may remain as tombstones containing only an opaque target ID and user-authored label/tag. Do not copy the old basename, path, MIME, hash, or snippet into the tombstone.
- Tombstoned assignments are excluded from normal results and never hold source bytes. Tag definitions survive independently until the user deletes them.

### Recent access

- Recent access is convenience state with a short rolling TTL (recommended default: 30 days) and a user-visible clear-history action.
- Expired/purged targets disappear immediately; there is no tombstone.
- Raw content, query strings, or secrets are never stored. The security audit remains independent and longer-lived where configured.

### Archive members

- A member identity binds the outer asset version/digest plus a normalized opaque member chain; a filename alone is insufficient.
- Archive indexes and extracted members are rebuildable sensitive derivatives. They disappear with the outer recovery point and never outlive it.

## Asynchronous Batch Export Contract

### Immutable selection

At authorization time, Xirang resolves and freezes:

- exact recovery-point IDs and manifest entry IDs;
- a canonical selection digest;
- requested archive format and deterministic collision policy;
- requester, permission scope, step-up proof class, and reason where required;
- logical bytes/item count estimates and the earliest source expiry deadline.

The job must never reinterpret a saved search later or silently substitute a newer `mutable_head`. Mutable assets are revalidated against the captured fingerprint immediately before and after reading; any change produces an explicit per-item failure.

### Encryption and delivery

- Each export receives a random data-encryption key (DEK) and uses streaming/chunked authenticated encryption. The encrypted archive lives in a dedicated quota-controlled location outside `/data`, `/backup`, and `/logs`.
- The small DEK is wrapped by a persistent application key-encryption key (KEK) if cross-restart durability is enabled. Deleting the wrapped DEK revokes the artifact cryptographically before asynchronous physical cleanup.
- A ready export receives a non-sliding absolute TTL (recommended default: 24 hours), capped by the earliest source recovery-point deadline.
- Download always requires reauthorization and a fresh action/resource-bound short ticket. A prior export job or stale step-up proof does not become a public link.
- Failed, canceled, or partial unpublishable output is cryptographically erased and queued for immediate idempotent deletion. Ready expiry revokes tickets and deletes the wrapped DEK before physical GC.

### Restart and recovery

Three coherent policies exist:

| Policy | Running jobs after restart | Ready artifacts after restart | Trade-off |
|---|---|---|---|
| Durable (recommended) | Reconcile lease/checkpoint and resume or safely retry | Remain usable until absolute TTL | Best large-export reliability; requires wrapped per-export DEKs and persistent job state |
| Ready-only durable | Running jobs fail and partial bytes are purged | Remain usable until TTL | Simpler execution recovery while avoiding needless loss of completed work |
| Process-ephemeral | All running and ready exports become unreadable and are purged | Unavailable | Smallest key lifecycle, but restarts waste work and surprise users |

This policy is intentionally separate from the approved preview cache: preview materialization continues using a per-process random key so crash remnants are unreadable.

### Job and GC state

```text
queued → running → sealing → ready → expiring → expired
   └──── failed / canceled / source_expired / purge_failed
```

- GC uses database state, heartbeat, attempt/fencing token, lease, and absolute deadline—not file age alone.
- Startup reconciliation finds orphan directories, lost leases, stale tickets, missing wrapped keys, and database/provider mismatches.
- Sanitized job outcome, item/byte totals, timestamps, and error categories may remain for about 90 days; per-item names/paths and the selection manifest are purged with the artifact unless an independently justified audit policy requires less revealing evidence.
- Downloaded client copies cannot be recalled; the UI and audit model must state this limitation.

## Legal/Security Purge Override

A privileged purge follows revoke-first semantics:

1. deny new access and revoke tickets;
2. cancel jobs and Worker grants;
3. destroy export/cache/derived keys;
4. purge derived content, Catalog/search/archive indexes, overlays, and provider bytes;
5. retain only legally required sanitized audit facts with opaque identities.

If external WORM/legal hold blocks a step, the point remains `purge_blocked`, raises an alert, and exposes the provider reason without leaking credentials.

## Decisions Carried Forward

1. Overlay, derived, export, and audit records are not backup truth and never extend recovery-point retention implicitly.
2. Saved searches store query definitions, not result snapshots; exact expired scopes fail closed.
3. Favorites/tags may retain opaque tombstones, while recent access is short-lived and removed immediately on expiry.
4. Exports freeze exact recovery-point entries, use per-export encryption, short delivery tickets, absolute TTL, explicit per-item outcomes, and startup/periodic GC.
5. Recovery-point state must become the owner of retention and invalidation before overlays/exports can be considered safe.
6. The user selected durable cross-restart exports: running jobs reconcile and resume/safely retry, while ready artifacts remain downloadable until their absolute TTL. This uses per-export DEKs wrapped by a persistent, rotation-aware KEK; preview cache keys remain process-ephemeral.
