# Phase 0 RED evidence

Date: 2026-08-24

Baseline: `b50f6ddb8a72d06941dcbb1e759c940a56caeeff` on
`codex/backup-assets-enable-transition-deadlock`.

All Go commands below ran from `backend/` with:

```text
GOCACHE=/home/murray/.cache/xirang-enable-deadlock/go-build
GOTMPDIR=/run/user/1000/xirang-enable-deadlock
```

## Content self-reentry

Command:

```text
go test ./internal/backupasset/runtime -run '^TestRuntimeEnableTransitionContentConfigDoesNotReenterSettingsMutation$' -count=1 -v
```

Result: RED, exit 1 after 2.00 seconds.

```text
content enable-transition helper exceeded its deadline; settings mutation re-entered its snapshot gate
deadlock-stage=content-config
```

The subprocess owns the real `settings.Service.WithBackupAssetMutation` gate and calls
`Runtime.TransitionBackupAssetSettings`. Its production-equivalent Content manager then calls the real
`FoundationService.ContentConfig`; the marker is emitted immediately before that call. The child does not
return because `ContentConfig` reaches `BackupAssetSettingsSnapshot` and attempts to reacquire the same gate.
The parent deadline kills and joins the child, so no stuck goroutine remains in the package test process.

## Search self-reentry independent of Content

Command:

```text
go test ./internal/backupasset/runtime -run '^TestRuntimeEnableTransitionSearchConfigDoesNotReenterSettingsMutation$' -count=1 -v
```

Result: RED, exit 1 after 2.01 seconds.

```text
search enable-transition helper exceeded its deadline; settings mutation re-entered its snapshot gate
deadlock-stage=search-config
```

This helper uses the real settings mutation and Runtime enable transition. Content is intentionally replaced
by a silent manager so the first re-entry cannot mask Search. `runtime.New` constructs the real Search worker;
the test wraps, but does not replace, its production Config closure and emits the marker immediately before
calling that closure. The real keyring reaches Search startup first, then the closure calls
`FoundationService.SearchConfig` and blocks reacquiring the owned snapshot gate. The keyring's initial
`record not found` lookup is its normal create-on-miss path and occurs before the Search marker.

The combined selector reproduced both distinct failures in one parent process:

```text
go test ./internal/backupasset/runtime -run '^TestRuntimeEnableTransition(Content|Search)ConfigDoesNotReenterSettingsMutation$' -count=1 -v
```

Result: RED, exit 1; Content failed at 2.00 seconds and Search failed at 2.01 seconds.

The final three-RED command was:

```text
go test ./internal/settings ./internal/backupasset/runtime -run '^(TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation|TestRuntimeEnableTransition(Content|Search)ConfigDoesNotReenterSettingsMutation)$' -count=1 -v
```

Result: RED, exit 1. The settings waiter failed at 0.26 seconds, Content at 2.00 seconds, and Search at
2.01 seconds with the same failure messages and stage markers recorded above.

## Canceled mutation waiter

Command:

```text
go test ./internal/settings -run '^TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation$' -count=10
```

Result: RED, exit 1; all ten runs failed after approximately 0.25 seconds with:

```text
canceled settings mutation remained blocked behind the current owner
```

The owner remains inside its callback while the second mutation first observes its context and then is canceled.
A test context makes that ordering deterministic for either an `Err` pre-check or a cancellation-aware wait on
`Done`. Current `sync.Mutex.Lock` cannot observe cancellation, so the waiter only returns after the test releases
the owner. The observation wait is bounded and its failure path releases and joins both goroutines. The test
verifies `context.Canceled`, verifies the waiter callback was not called, and verifies the mutation gate is
reusable before reporting RED.

## Preserved baseline contract and compile check

Command:

```text
go test ./internal/settings -run '^TestBackupAssetSearchConfigAndOverlayConfigSnapshotIsCompleteCopiedAndMutationAtomic$' -count=1 -v
```

Result: PASS in 0.04 seconds. The existing atomic snapshot reader remains blocked until its mutation finishes
and then observes the complete update.

Command:

```text
go test ./internal/settings ./internal/backupasset/runtime -run '^$' -count=1
```

Result: PASS; both packages compile with the RED tests present.

An independent Phase 0 review hardened two attribution boundaries before repeating the selectors: the canceled
waiter now observes either `Err` or `Done`, so the RED is not coupled to the current pre-lock implementation, and
the subprocess parent now requires the helper's exact `deadlock-stage` marker before attributing a deadline to
snapshot-gate re-entry. After those test-only changes, the ten-run waiter selector, both individual runtime
selectors, the combined runtime selector, and the final three-RED selector reproduced the same intended failures.
The atomic snapshot selector and a no-test compile check still passed. Focused `go vet` also passed for both
packages.

## Test-design boundary

The Content test uses a narrow production-equivalent manager adapter instead of constructing the full Content
cache/reconciler graph; its `PrepareEnable` calls the exact real Foundation getter used by
`managedContentRuntime`. The Search test constructs the production worker and Config closure via `runtime.New`
but replaces unrelated Content, admission, inventory, Export, and Recovery stages to make Search the only
possible snapshot re-entry. Later handler/integration phases still need the broader production-equivalent graph
required by R6; these Phase 0 tests prove the two lock-order failures without expanding fixtures prematurely.
