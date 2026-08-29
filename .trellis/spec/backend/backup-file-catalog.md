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

## Scenario: Task-Derived Preview Connection And Prompt Catalog Wake

### 1. Scope / Trigger

Use this scenario when a Task-derived repository connection creates or refreshes
an observed mutable recovery point and a caller needs prompt Files convergence.
The connection is still an explicit administrator action and a bounded read-only
Provider observation; the wake is only an internal best-effort scheduling hint.

### 2. Signatures

- Task UI connection request: `POST /api/v1/backup-repositories/connect` with
  `{ "task_id": <positive Task ID> }`. The task entry point does not accept a
  locator, credential, replacement flag, display name, or provider proof.
- Repository/runtime composition: `SetCatalogWake(CatalogWakeRequester) error`,
  where `CatalogWakeRequester` exposes `TryWake() bool` and is wired to the one
  lifecycle-owned production `CatalogWorker` instance.
- Connect response contains a sanitized repository projection plus an optional
  mutable recovery-point snapshot. Current clients accept one internally
  consistent envelope: emitted Go keys `Repository` / `MutablePoint` or
  normalized `repository` / `mutable_point`. Duplicate repository/point keys,
  mixed casing envelopes, or a present malformed point block the projection;
  an absent or null point remains valid for reconcile/disconnect.
- The mutable-point snapshot contains core RecoveryPoint fields only. It is not
  a full RecoveryPoint catalog projection; clients fetch catalog status by the
  exact returned point ID.

### 3. Contracts

- Connect derives repository access from authoritative Task, Node, and credential
  state, probes outside the transaction, then locks and revalidates lineage before
  commit. It must not enable managed Rsync/versioning or mutate Provider data.
- Request the catalog wake only after the transaction commits and only for a
  valid, observed, non-retired mutable point. Probe failure, validation failure,
  rollback, nil point, or retired point never requests a wake.
- Wake delivery is capacity-one, coalesced, and actually non-blocking: the send
  itself uses a `select` with `default`. Queue saturation, duplicate wake,
  stopped worker, or `TryWake()==false` never reverses a successful Connect.
- A wake received during an active scan remains pending for a follow-up pass. A
  wake queued before `Run` is absorbed by the initial pass. Wake-triggered passes
  do not reset or postpone the independent periodic deadline; a due periodic pass
  has priority after the active scan finishes, including when scan completion and
  the periodic timer become ready at the same scheduler boundary.
- The repository service copies the requester under its lock and calls
  `TryWake` after releasing the lock. Production composition rejects nil,
  typed-nil, or duplicate wiring, retains the original requester after a rejected
  duplicate, and never creates a second CatalogWorker for this path.
- Active-build gauge count changes and publications are serialized independently
  of the worker lifecycle lock, so concurrent completions cannot publish an older
  nonzero count after the exact zero observed when all builds join.
- The preview client polls only the exact returned point. Catalog readiness is
  complete generation + complete coverage + available content + list permission;
  preview permission and a positive entry count are not readiness requirements.
  Missing/malformed point data, failed/partial/unavailable Catalog state, or an
  expired foreground polling budget fails closed with a stable localized message.
- Closing the dialog or changing Task/token aborts the in-flight operation and
  clears its timer and prevents stale/late updates. A two-minute wall-clock
  deadline also aborts a catalog call that never settles and transitions only to
  background-timeout guidance; lifecycle/task/token/close aborts remain silent.
  UI errors remain a closed localized set; raw backend, database, Provider,
  locator, credential, proof, or exception detail is never rendered.
- Task preview eligibility requires a canonical Rsync legacy publication triple:
  `legacy_mutable` / `legacy` / `legacy`. Missing historical executor data may use
  the Rsync default, but an unknown nonempty executor or a blocked/malformed
  publication sentinel is never eligible.

### 4. Validation & Error Matrix

| Condition | Connect result | Wake / preview behavior |
|---|---|---|
| Commit succeeds with valid observed mutable point | Sanitized repository + point snapshot | Request one best-effort wake, then poll that point. |
| Repeated Connect resolves the same valid point | Successful idempotent refresh | A repeated wake is allowed and may coalesce. |
| Probe/validation/transaction fails | Existing safe error contract | No wake and no catalog polling. |
| Commit succeeds but point is nil, retired, or invalid | Repository result may remain valid | No wake; task preview flow blocks without guessing a point ID. |
| Wake queue is full or worker is stopping | Connect remains successful | `TryWake` returns false immediately; periodic scan remains the fallback. |
| Catalog is complete, available, listable, and empty | Connection remains successful | Preview is ready and opens the exact empty point. |
| Catalog is failed, partial, unavailable, or the two-minute wall-clock budget expires (including a hung request) | Connection remains successful | Abort foreground work, show background-timeout/closed guidance as appropriate; never disconnect or expose raw details. |

### 5. Good/Base/Bad Cases

- Good: an administrator connects a legacy mutable Rsync Task with only its Task
  ID; commit creates an observed point, the non-blocking wake starts a prompt
  catalog pass, and the exact complete empty point opens as a valid workspace.
- Base: a previously connected Task is refreshed while the wake queue already
  holds a signal. Connect succeeds immediately, the wake coalesces, and the
  periodic scan remains scheduled as the eventual fallback.
- Bad: collect a locator or credential in the Task UI; wake before commit; call a
  potentially blocking channel send while holding the service lock; reset the
  periodic timer for every wake; cast the partial point to a full Catalog DTO;
  require preview permission or nonzero entries; disconnect after indexing
  failure; or render `err.Error()`/backend detail to the operator.

### 6. Tests Required

- CatalogWorker tests prove a long deadline is interrupted, duplicate wakes
  coalesce without blocking, a saturated channel cannot block the caller, wake
  during scan is retained, simultaneous scan completion/timer readiness and
  continuous wakes cannot starve a periodic pass, concurrent gauge updates finish
  at exact zero, a pre-Run wake folds into initial work, and shutdown cancels/joins
  then rejects later wakes. Run focused repeat and race variants.
- Repository tests prove wake occurs exactly once after a committed valid point;
  repeated Connect remains safe; probe/transaction failure and nil/retired points
  do not wake; nil/typed-nil/duplicate wiring is rejected; and a false/rejected
  wake cannot fail Connect. Production runtime tests assert the exact
  lifecycle-owned worker is injected once.
- API mapper tests cover `MutablePoint` and `mutable_point`, absent/null values,
  duplicate/mixed/malformed envelope fail-closed behavior, task-only request
  shape, and reuse of the strict recovery-point snapshot mapper. Task mapper tests
  distinguish unknown nonempty executors from absent historical data.
- UI tests cover administrator-only canonical legacy-Rsync eligibility in table
  and grid, paused/disabled eligibility, running/retrying/active-run disabling and
  accessible tooltip parity; table tooltips stay inside the horizontal scrollport
  for first and last rows in zh/en under keyboard focus and hover. Tests also cover
  safety copy, exact-point readiness including empty
  Catalog, failed/partial/unavailable state, a hung-request wall-clock timeout,
  Task/token/close/unmount cancellation with timer cleanup and late-result
  suppression, localized close names, closed errors, and exact deep-link navigation.

### 7. Wrong vs Correct

Wrong:

```go
tx.Create(&point)
worker.TryWake()           // transaction may still roll back
worker.wake <- struct{}{}  // can block the Connect request
```

Correct:

```go
if err := db.Transaction(persistPoint); err != nil {
    return err
}
if pointIsObservedAndWakeable(point) {
    _ = catalogWake.TryWake() // post-commit, best effort, non-blocking
}

func (worker *CatalogWorker) TryWake() bool {
    select {
    case worker.wake <- struct{}{}:
        return true
    default:
        return false
    }
}
```
