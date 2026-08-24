# Phase 2 RED/GREEN evidence

Date: 2026-08-24

Baseline: `b50f6ddb8a72d06941dcbb1e759c940a56caeeff` on
`codex/backup-assets-enable-transition-deadlock`, preserving the Phase 0/1 worktree changes.

Unless a shorter socket-safe path is called out below, Go commands ran from `backend/` with:

```text
GOCACHE=/home/murray/.cache/xirang-enable-deadlock/go-build
GOTMPDIR=/run/user/1000/xirang-enable-deadlock
```

## Focused API REDs before production edits

Explicit Search startup first failed to compile:

```text
go test ./internal/backupasset/runtime \
  -run '^TestSearchWorkerStartupPassWithConfigDoesNotReadDynamicConfig$' \
  -count=1
```

Result: RED, exit 1:

```text
search_worker_test.go:33:19: worker.StartupPassWithConfig undefined
FAIL xirang/backend/internal/backupasset/runtime [build failed]
```

The next production-boundary RED supplied a fake whose Content enable and restore methods require typed
`ContentConfig` values and called the not-yet-existing config-aware transition:

```text
go test ./internal/backupasset/runtime \
  -run '^TestRuntimeConfigAwareContentEnableReceivesProspectiveConfig$' \
  -count=1
```

Result: RED, exit 1. Compilation rejected the typed fake because the current interface still required
`PrepareEnable(context.Context)` and reported `runtime.transitionFeatureWithConfigs undefined`.

These tests were added before the associated production seams. The existing genuine Phase 0 Content and Search
subprocess selectors remained the independent deadlock RED baseline documented in
`phase-0-red-evidence.md` and `phase-1-red-green-evidence.md`.

## Minimal GREEN contract

The Content manager boundary now receives the prospective typed Content config on enable and the prior typed
Content config on failed-disable restoration. `managedContentRuntime` delegates both paths to a helper that does
not read Foundation. The mutation owner builds the complete Phase 1 prior/prospective bundle first and passes it
through `transitionFeatureWithConfigs`.

Search has `StartupPassWithConfig`, which does not invoke the dynamic `Config` closure. Runtime maps the
prospective Search and Overlay bundle to `SearchWorkerConfig`; enable/live state additionally requires readiness
authorization and managed admission authority. The ordinary `StartupPass` and background `Run` retain the
dynamic config path outside settings mutations.

The focused contract selector covered typed Content enable/restore, explicit Search startup, dynamic background
Run, prospective readiness/admission authority, prospective Search/Overlay mapping, and an AST guard over the
config-aware transition functions:

```text
go test ./internal/backupasset/runtime \
  -run '^(TestRuntimeConfigAwareContentEnableReceivesProspectiveConfig|TestRuntimeConfigAwareContentRestoreReceivesPriorConfig|TestProspectiveFeatureLiveRequiresEnabledReadinessAndManagedAdmission|TestRuntimeSearchWorkerConfigMapsProspectiveSearchAndOverlayBundle|TestConfigAwareTransitionFunctionsDoNotReadCurrentFoundationSettings|TestSearchWorkerStartupPassWithConfigDoesNotReadDynamicConfig|TestSearchWorkerBackgroundRunStillReadsDynamicConfig)$' \
  -count=1 -v
```

Result: PASS; all seven tests passed, package time `0.066s`.

## Genuine deadlock selectors GREEN

```text
go test ./internal/backupasset/runtime \
  -run '^TestRuntimeEnableTransition(Content|Search)ConfigDoesNotReenterSettingsMutation$' \
  -count=10
```

Result: PASS in `1.735s`. The Content helper received the explicit prospective config and returned. The Search
helper retained the production dynamic Config closure as a trap, but explicit enable startup never invoked it;
the worker pass used a deterministic backend fake to avoid unrelated database fixtures.

## Repetition, race, owned packages and vet

```text
go test ./internal/backupasset/runtime \
  -run '^(TestRuntimeConfigAware|TestProspectiveFeatureLive|TestConfigAwareTransitionFunctions|TestSearchWorkerStartupPassWithConfig|TestSearchWorkerBackgroundRunStillReadsDynamicConfig)' \
  -count=50
```

Result: PASS in `0.347s`.

```text
go test -race ./internal/backupasset/runtime \
  -run '^(TestRuntimeEnableTransition(Content|Search)ConfigDoesNotReenterSettingsMutation|TestRuntimeConfigAware|TestProspectiveFeatureLive|TestConfigAwareTransitionFunctions|TestSearchWorkerStartupPassWithConfig|TestSearchWorkerBackgroundRunStillReadsDynamicConfig)' \
  -count=10
```

Result: PASS. A fresh `-count=1` confirmation reported package time `4.907s`.

```text
go test ./internal/settings ./internal/backupasset ./internal/backupasset/runtime -count=1
```

Result: PASS in `0.213s`, `0.695s`, and `7.749s` respectively. A separate complete runtime-package run passed
in `7.877s`.

```text
go vet ./internal/settings ./internal/backupasset ./internal/backupasset/runtime
git diff --check
```

Result: PASS with no output.

## Recursive sweep environment note

`go test ./internal/backupasset/... -count=1` was attempted with the required long `GOTMPDIR`. Runtime and the
other completed packages passed, but the aggregate command exited 1: one parallel catalog link reported a
transient `disk quota exceeded`, while local transport tests in `processing` and `processing/updater` exceeded the
Linux Unix-socket path limit after `t.TempDir` appended their long test names. No product assertion failed, and
the known unrelated `cache_root_unverified` failure did not appear.

Catalog subsequently passed in `5.349s`. With a shorter task-specific `GOTMPDIR=/tmp/xr-ed`, only the affected
local transport selectors were repeated:

```text
go test ./internal/backupasset/processing ./internal/backupasset/processing/updater \
  -run '^TestLocal' -count=1
```

Result: PASS in `0.055s` and `0.003s`. No shared cache was cleaned.

## Deliberate Phase 3 boundary

Phase 2 does not add a centralized transition timeout, opCtx propagation, join/drain ownership, the complete
failure/timeout compensation matrix, or exact enablement-stamp rollback. Existing transition compensation was
left in place except for passing prior typed Content config through the required restore seam. Handlers, deploy,
node logs, and migrations were not changed.
