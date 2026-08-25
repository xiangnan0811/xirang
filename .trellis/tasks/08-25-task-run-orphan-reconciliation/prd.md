# Task-run orphan reconciliation

## Goal

Allow the existing formal task-cancel operation to durably reconcile an authoritative active
`TaskRun` that survived a process or container replacement without a live in-process execution
owner. This restores the product-owned path for upgrading production, resuming the affected
schedule, and completing the parent backup-asset acceptance.

## Requirements

- Preserve existing live-owner cancellation behavior: when a runner owns cancellation, signal that
  owner and leave its atomic terminal update authoritative.
- When no live owner exists, establish a temporary process-local trigger barrier before inspecting
  or changing durable state so a new run cannot be mistaken for an orphan.
- Reconcile every active `TaskRun` whose positive `node_id_snapshot` exactly matches the current Task
  node. Each observed row uses its own exact status/identity CAS, and all rows commit or roll back as
  one set. Legacy-unknown, mismatched, already-terminal, or other-Task rows remain untouched.
- Support both interrupted-state shapes:
  - Task aggregate is `running` and its authoritative active run is orphaned.
  - Task aggregate is already terminal because an older release paused/canceled it, while its
    authoritative active run remains orphaned.
- Make the no-owner Task/TaskRun terminal changes transactional and compare-and-swap guarded. A
  concurrent status or authority drift must fail closed without partial terminalization.
- Use only static, sanitized cancellation text in durable runtime fields and logs.
- Keep the existing HTTP route, RBAC/ownership checks, response schema, status vocabulary, database
  schema, scheduler API, publication semantics, and frontend unchanged.
- Do not broaden managed-publication startup reconciliation to legacy mutable tasks and do not add a
  repair endpoint or production SQL procedure.
- The latest production evidence is `running`/enabled and the formal Pause result is pending; do not
  record the task as paused until verified. After a verified Pause, keep it paused until the fixed
  stable image is deployed and formal Cancel proves there are zero active runs. Resume only through
  the existing API after that proof.

## Acceptance Criteria

- [x] A pre-change manager regression reproduces a terminal Task with an authoritative `running`
  TaskRun and no live owner; `Cancel` fails and leaves the run active.
- [x] After the change, the same formal `Cancel` call returns success and durably marks the orphaned
  run `canceled` with `finished_at` and sanitized `last_error`, while preserving the Task's existing
  terminal aggregate.
- [x] A no-owner `running` Task and its authoritative active run are terminalized atomically.
- [x] Candidate validation uses the whole closed active set (`pending`, `running`, `retrying`) and
  atomically reconciles every current-node authoritative orphan, including multiple rows; legacy-
  unknown, node-mismatched, already-terminal, newer other-Task rows remain unchanged.
- [x] A trigger/cancel ownership race cannot let the fallback terminalize a newly owned run; existing
  live-owner and reservation cancellation tests remain green.
- [x] Transaction/CAS failure leaves both Task and candidate TaskRuns unchanged.
- [x] Focused tests pass repeatedly and under the race detector; task package tests plus backend
  lint, vet, test, build, and repository diff checks pass.
- [ ] PR CI is green, the fix and subsequent stable release are merged/published, and post-merge
  automation is observed.
- [ ] Production upgrade retains database integrity and container health; the existing formal Cancel
  produces zero active runs before Resume; a subsequent successful backup produces searchable
  Catalog data and the parent Search/UI preview acceptance passes without critical errors.
- [ ] Node-log collectors remain disabled until the parent real-data preview acceptance completes.

## Notes

- Production identifiers, paths, locators, credentials, content, and raw internal failures are not
  committed to task artifacts or emitted by diagnostics.
- Operator shell runbooks must not use the shell `test` command or change directory; they must keep
  the root shell and NAS SSH session stable.
