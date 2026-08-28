# Backup File Catalog

## Scenario: Retained-Lineage Projection And Fair Reconciliation

### 1. Scope / Trigger

Use this scenario when changing the Files source/version projection, retained
backup attribution, publication/import reconciliation, or the worker that makes
retained data browsable after interrupted, disabled, archived, or deleted task
state. The HTTP projection remains a bounded database read; Provider observation
belongs only to the asynchronous reconciler.

### 2. Signatures

- File-source versions expose required closed `browse_state` values
  `browsable|indexing|unavailable`, a non-negative `retained_version_count`, and
  only a known parameter-free unavailable reason when state is `unavailable`.
- Exact attribution comes from durable RecoveryPoint producing-task/node
  snapshots, strict lineage JSON, immutable task-repository linkage snapshots,
  reviewed import/publication lifecycle, repository availability/capabilities,
  and the active complete Catalog generation. Mutable task state is not proof.
- Reconciliation batch is closed to `1..1000`; candidate selection is durably
  ordered by `updated_at,id`, and leases/attempt state/backoff remain database
  facts shared across restarts and instances.

### 3. Contracts

- Admin may see exact retained lineages after a producing task is interrupted,
  disabled, archived, or deleted, but only when durable identities and provenance
  agree. A mutable live point requires its durable producing-task ID to equal the
  strict lineage task ID and must not be inferred from JSON alone.
- Operator visibility additionally requires the current live non-archived task,
  a live current task node with current ownership, and exact agreement between
  the immutable link/point attribution snapshots. A legitimate task move does
  not rewrite those historical snapshots or transfer authority to the former
  node owner. Missing, deleted, archived, ambiguous, or conflicting authority
  fails closed.
- A public retained point plus an active complete Catalog generation is
  `browsable`. Exact retained provenance without complete Catalog truth is
  `indexing`. An offline repository or missing sequential capability is
  `unavailable`. A configured link with no retained point, an unreviewed import,
  or ambiguous provenance is not a Files selector.
- Cursor signatures/digests bind every visible sanitized fact, including state,
  count, reason, identities, and availability, so authorization or projection
  drift rejects replay. No locator, path, token, proof, or Provider detail enters
  the DTO, cursor, audit, or frontend state.
- File-source HTTP listing performs no Provider, SSH, command, or locator access.
  It uses bounded, fixed-shape database work and never repairs data inline.
- Reconciliation is asynchronous, cancelable, and bounded. Candidate selection
  uses a fixed number of database queries independent of batch size; one claimed
  point receives at most one reconciliation invocation. Each backend may perform
  its backend-specific fixed, bounded set of identity and provenance observations,
  but never an N+1 fan-out or unbounded retry/list loop. Repair requires exact
  backend-specific provenance; ambiguous, staging, rewritten, or incomplete
  evidence remains fail closed.
- Fairness is durable: backed-off low IDs, process restart, multiple instances,
  or sustained wake traffic cannot starve a later due candidate. The periodic
  timer survives coalesced wakes and resets only after a periodic pass.

### 4. Validation & Error Matrix

| Evidence/state | Admin Files result | Operator Files result |
|---|---|---|
| Public point + complete active Catalog | `browsable` | Same only with current task/node ownership. |
| Exact retained point, Catalog incomplete | `indexing` | Same only with current live authority. |
| Exact retained point, repository offline/no sequential read | `unavailable` with closed empty-params reason | Same authority rule; never expose capability detail. |
| Interrupted/disabled/archived/deleted producer with agreeing immutable snapshots | Retained lineage remains visible in the state derived from current truth. | Archived/deleted/missing current authority is omitted. |
| Configured linkage but no retained point | Omit. | Omit. |
| JSON-only mutable task ID, conflicting snapshots, unreviewed import, ambiguous provenance | Omit/fail closed; never guess. | Omit/fail closed. |
| Cursor replay after state/count/ownership/availability drift | Reject as stale/unauthorized. | Reject as stale/unauthorized. |
| Backed-off oldest prefix or continuous wake traffic | Durable scan progresses to later due work. | Not applicable to HTTP projection. |

### 5. Good/Base/Bad Cases

- Good: a deleted producing task remains visible to Admin because immutable
  point/link/lineage facts agree; its incomplete Catalog state is `indexing`, and
  the background worker later activates a complete generation using exact
  provenance.
- Base: an active task with a complete Catalog remains browsable through a
  database-only request, while Operator visibility is rechecked against current
  ownership.
- Bad: enumerate Providers in the Files request; treat configured tasks as
  retained data; derive task identity from JSON alone; N+1 query candidates;
  use a process-local fairness cursor; reset the periodic timer on every wake;
  or expose repository/provider internals in the state reason.

### 6. Tests Required

- Catalog tests cover active, interrupted, disabled, archived, deleted, no-data,
  offline, missing-capability, ambiguous, conflicting, imported, and Operator
  ownership cases. Operator regressions cover a legitimate task move (former
  owner denied, current owner allowed) plus archived or missing current nodes
  with stale ownership rows, across both point authorization and Repository/link
  projection. Include static dependency guards proving zero Provider/SSH/
  command/locator access in file-source projection.
- Cursor tests cover HMAC tamper plus user, role, ownership, identities, state,
  counts, reason, availability, and projection-generation drift.
- Reconciler tests assert batch validation, at most two candidate-selection DB
  queries independent of batch, cancellation/join, bounded concurrency, at most
  one reconciliation invocation per claimed point, durable backoff,
  restart/multi-instance fairness, and wake traffic that cannot starve the
  periodic scan. Backend fakes/spies assert their identity/provenance observation
  set stays fixed and bounded where the harness exposes those calls.
- Backend-specific proof tests cover exact Restic tags/summary, Rclone commit and
  manifest, and Rsync final marker; ambiguity, staging, missing proof, and
  rewritten evidence stay unrepaired.
- Frontend mapper/control tests require the closed state/count/reason contract,
  keep non-browsable lineages visible but disabled, and clear descendants until
  the exact selected version is browsable. Run repeat/race/full gates.

### 7. Wrong vs Correct

Wrong:

```go
for _, task := range configuredTasks {
    files = append(files, provider.Scan(task.Repository)) // HTTP Provider fan-out
}
timer.Reset(period) // every wake can starve the durable periodic scan
```

Correct:

```go
sources := projectRetainedLineagesFromDB(ctx, authorizedActor) // bounded, no Provider
candidates := selectDueCandidates(ctx, batch, "updated_at ASC, id ASC")
for _, claimed := range claimWithLease(candidates) {
    reconcileOnce(ctx, claimed) // fixed backend proof observations or fail closed
}
// Wake coalescing does not reset the already-running periodic timer.
```
