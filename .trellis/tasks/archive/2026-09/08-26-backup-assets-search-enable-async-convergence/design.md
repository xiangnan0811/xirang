# Design — Search Enable Async Convergence

## Root cause

`Runtime.startupSearchWithConfig` currently performs key preparation, marks
Search ready, and then calls `SearchWorker.StartupPassWithConfig`. That method
runs reconciliation, candidate enumeration, and the complete candidate Build
fan-out synchronously. During a settings mutation the enclosing feature
transition has a 25-second operation ceiling.

The first valid production projection contains 60,515 Catalog rows. Generation
11 committed eight 1,000-row batches and was canceled at 24,979 ms, producing
the exact inactive `search_build_timeout` evidence. Runtime then correctly
compensated the setting to false. Increasing the ceiling would only make a
data-dependent workload part of an API transaction and would remain unsafe for
larger Catalogs.

## Selected boundary

### Infrastructure-only preparation

Add an explicit SearchWorker preparation operation that uses an already
validated config and performs the pass-level checks currently preceding
candidate fan-out:

1. validate enabled worker bounds;
2. reconcile abandoned Search generations;
3. reconcile overlays;
4. list candidates using the active Search key.

It returns infrastructure/context errors exactly as the full pass does but does
not call `Build`. `Runtime.startupSearchWithConfig` uses this operation after
key rewrap/ensure and before setting Search ready. The existing full
`StartupPassWithConfig` and dynamic `Run` pass retain complete candidate fan-out
semantics for their direct contracts.

### Owned background wake

SearchWorker owns one buffered wake channel. A non-blocking `TryWake` coalesces
requests. The worker's existing single `Run` loop drains a pending wake before
its immediate pass and selects on wake, timer, or cancellation between passes.
No new goroutine or second worker loop is created by enablement.

`startupSearchWithConfig` only prepares infrastructure and marks Search ready;
it never signals candidate work. On hot enable, Runtime signals the worker only
after the setting is persisted, the enablement-success stamp is durable,
Content is ready, and every fallible transition stage has completed. A sleeping
worker then runs immediately and re-reads committed dynamic config. Persistence,
stamp, Content-readiness, or compensation failure emits no wake. If a queued
wake observes committed disabled config, it performs no backend work.

Cold startup emits no explicit wake. The lifecycle-owned worker's existing
initial pass performs convergence after startup has completed, avoiding a
second pending pass or concurrent candidate loop.

### Failure and rollback semantics

Infrastructure/key/readiness failures still return before successful enablement
and use the existing compensation path. Candidate Build errors occur only in
the background worker, which already records metrics and durable failed staging
evidence while isolating point-local failures. Caller cancellation still stops
and joins candidate work. Search coverage may report building/unavailable until
atomic activation; repository and Catalog availability remain independent.

No Search generation or staging row is reused. Production generation 12 will be
created normally, project all 60,515 active Catalog entries from the beginning,
and activate only after the existing physical-count and frozen-source checks.

## Alternatives rejected

- Increase the 25-second transition ceiling: data-size dependent, exceeds the
  HTTP write budget, and only postpones failure.
- Run candidate Build in a detached enablement goroutine: creates unowned work,
  duplicates the existing worker lifecycle, and weakens shutdown/join authority.
- Treat Search as ready without infrastructure preparation: hides config, key,
  reconciliation, or candidate-query defects until after enablement.
- Delete/reuse generation 11 staging rows: bypasses immutable failure evidence
  and complicates activation authority.

## Verification design

Use deterministic channels, not long sleeps:

- a blocking candidate backend proves current hot-enable/cold-startup coupling;
- a spy preparation backend proves every pass-level stage and failure boundary;
- a worker with an hour-long timer proves `TryWake` triggers immediate fan-out;
- repeated/race tests cover coalescing, cancellation, shutdown, and metrics;
- existing v0.50.8 candidate-isolation and real Catalog-producer Search tests
  remain regression gates.
