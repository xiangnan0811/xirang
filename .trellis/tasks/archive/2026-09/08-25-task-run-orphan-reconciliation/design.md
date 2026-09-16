# Design — formal cancellation of orphaned task runs

## Confirmed failure

The task manager stores cancellation ownership only in process memory. After a container replacement,
an authoritative `TaskRun` can remain in the closed active status set even though its runner and
transfer process no longer exist. The current `Manager.Cancel` running-state compatibility fallback
updates only the Task aggregate. In addition, an older `Pause(cancel_running=true)` can first disable
the schedule and then leave the Task terminal while the active TaskRun remains; a later formal Cancel
rejects terminal Tasks and therefore cannot repair that state.

Publication restart reconciliation is intentionally scoped to managed publication semantics. The
observed task uses legacy mutable publication, so expanding that worker would couple generic runtime
repair to a distinct backup-publication contract.

## Selected command boundary

Keep repair operator-controlled through the existing `Manager.Cancel` command and HTTP route. When a
live owner is present, retain the current signal-and-return path. When no owner is present:

```text
formal Cancel
  -> acquire temporary per-Task trigger barrier
  -> reload Task in a database transaction
  -> select the complete current-node authoritative active TaskRun set
  -> CAS each observed row to canceled
  -> if Task aggregate is active, CAS it to canceled
     else preserve its existing terminal status
  -> commit and release the barrier
```

If another trigger wins ownership before the barrier, Cancel must signal that owner and use the live
runner path. The barrier is removed on every exit. No remote operation, scheduler mutation, or
publication mutation is introduced by reconciliation.

## Durable authority and atomicity

- Select every row for the requested `task_id` whose status is in `model.TaskRunActiveStatuses()` and
  whose positive node snapshot exactly matches the reloaded Task node. Multiple matching rows are all
  operator-authorized orphans and must be reconciled together so none remains to block admission.
- Each TaskRun update must include its observed ID, Task ID, authority snapshot, and exact observed
  active status in the `WHERE` clause. A zero-row CAS is an error and rolls back the entire set.
- Task aggregate transition is required only when its reloaded status is active. It uses the exact
  observed status as a CAS guard. Existing terminal aggregate status is preserved.
- An active Task with no current-node authoritative active TaskRun is inconsistent and fails closed;
  the command must not manufacture a terminal run outcome by changing the aggregate alone.
- A terminal Task with no authoritative active row keeps the existing unsupported-cancel behavior;
  successful terminal-state Cancel is limited to actually reconciled orphan state.
- Every reconciled run receives `status=canceled`, one shared UTC `finished_at`, and static sanitized
  `last_error`. Pending rows retain nil start/zero duration; started rows preserve start evidence and
  receive a non-negative duration derived from the shared finish time.
- Rows with legacy-unknown or mismatched node snapshots are evidence of a different authority class
  and are never repaired by this path.

## Compatibility and privacy

There is no migration, API/Swagger/frontend change, new setting, status value, metric label, or raw
runtime error exposure. Existing live-owner compensation remains the source of truth for runs in the
current process. Durable text is a fixed product message and never includes paths, hosts, locators,
commands, credentials, or executor output.

## TDD and verification

The mandatory RED is the exact production state shape: a terminal/paused Task, an authoritative
running TaskRun, and an empty manager ownership map. Additional tests cover active aggregate/run
statuses, multiple orphan rows, non-authoritative rows, unrelated rows, already-terminal rows,
owner/barrier races, and rollback on CAS drift. The test must call `Manager.Cancel`, not a private helper, so the
formal product path is proved.

## Rollback and production sequence

The code has no schema change. The latest production evidence still shows the affected task as
`running` and enabled with the formal Pause result pending; collectors remain disabled. Do not claim
the task is paused until that result is verified. After a verified Pause, keep it paused until a
release containing both the Catalog batching fix and this runtime fix is available.
Upgrade retains the verified database backup. If startup or health checks fail, restore the compose
image reference and bring up the previous image; do not alter TaskRun rows manually. On the fixed
image, call formal Cancel once, prove zero active runs, then use Resume only if the schedule had been
formally paused. Only then
allow a new backup and continue Catalog/Search/UI acceptance.
