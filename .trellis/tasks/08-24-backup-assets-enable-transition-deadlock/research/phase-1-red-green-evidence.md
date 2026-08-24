# Phase 1 RED/GREEN evidence

Date: 2026-08-24

Baseline: `b50f6ddb8a72d06941dcbb1e759c940a56caeeff` on
`codex/backup-assets-enable-transition-deadlock`.

All Go commands below ran from `backend/` with:

```text
GOCACHE=/home/murray/.cache/xirang-enable-deadlock/go-build
GOTMPDIR=/run/user/1000/xirang-enable-deadlock
```

## Prospective parser and bundle RED

Before production edits:

```text
go test ./internal/backupasset \
  -run '^(TestContentConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestSearchOverlayConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestProspectiveConfigParsersRequireCompleteValidatedFoundationValues|TestFoundationTransitionConfigFromValuesBuildsCompleteTypedBundle)$' \
  -count=1 -v
```

Result: RED, exit 1. The package failed to compile with eight references to the missing
`ContentConfigFromValues`, `SearchOverlayConfigFromValues`, and
`FoundationTransitionConfigFromValues` APIs.

```text
go test ./internal/backupasset/runtime \
  -run '^TestFoundationTransitionConfigsFromValuesBuildsPriorAndProspectiveBeforeRuntimeWork$' \
  -count=1 -v
```

Result: RED, exit 1. The package failed to compile with two references to the missing
`foundationTransitionConfigsFromValues` coordinator primitive.

The tests require complete snapshots, whole-foundation validation, no input-map mutation, equality between
current getters and pure prospective parsers, exactly one atomic getter snapshot, and distinct prior/prospective
typed transition values.

## Context-aware mutation gate RED

Before production edits:

```text
go test ./internal/settings \
  -run '^(TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation|TestWithBackupAssetMutationFailureReleasesGateAndSnapshotSeesOnlyOldOrNewValues)$' \
  -count=1 -v
```

Result: RED, exit 1. The waiter test failed after 0.26 seconds with:

```text
canceled settings mutation remained blocked behind the current owner
```

The controlled failure/release and rollback-visibility test already passed in 0.04 seconds, preserving the
existing mutex contract while the cancellation RED isolated the missing behavior.

## Minimal GREEN

After adding the shared pure parsers, the complete typed transition bundle, the prior/prospective coordinator
builder, and the one-token context-aware mutation gate, the focused selector was:

```text
go test ./internal/backupasset ./internal/backupasset/runtime ./internal/settings \
  -run '^(TestContentConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestSearchOverlayConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestProspectiveConfigParsersRequireCompleteValidatedFoundationValues|TestFoundationTransitionConfigFromValuesBuildsCompleteTypedBundle|TestFoundationTransitionConfigsFromValuesBuildsPriorAndProspectiveBeforeRuntimeWork|TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation|TestWithBackupAssetMutationFailureReleasesGateAndSnapshotSeesOnlyOldOrNewValues|TestBackupAssetSearchConfigAndOverlayConfigSnapshotIsCompleteCopiedAndMutationAtomic)$' \
  -count=1 -v
```

Result: PASS. Package times were `0.016s` for `backupasset`, `0.055s` for `runtime`, and `0.077s` for
`settings`. The canceled waiter returned before owner release without invoking its callback; failure released
the gate; and readers blocked until they could observe either the rolled-back old snapshot or the fully committed
new snapshot.

Repeated selector:

```text
go test ./internal/backupasset ./internal/backupasset/runtime ./internal/settings \
  -run '^(TestContentConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestSearchOverlayConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestProspectiveConfigParsersRequireCompleteValidatedFoundationValues|TestFoundationTransitionConfigFromValuesBuildsCompleteTypedBundle|TestFoundationTransitionConfigsFromValuesBuildsPriorAndProspectiveBeforeRuntimeWork|TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation|TestWithBackupAssetMutationFailureReleasesGateAndSnapshotSeesOnlyOldOrNewValues|TestBackupAssetSearchConfigAndOverlayConfigSnapshotIsCompleteCopiedAndMutationAtomic)$' \
  -count=50
```

Result: PASS. Package times were `0.031s`, `0.068s`, and `3.685s` respectively.

Race selector:

```text
go test -race ./internal/backupasset ./internal/backupasset/runtime ./internal/settings \
  -run '^(TestContentConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestSearchOverlayConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestProspectiveConfigParsersRequireCompleteValidatedFoundationValues|TestFoundationTransitionConfigFromValuesBuildsCompleteTypedBundle|TestFoundationTransitionConfigsFromValuesBuildsPriorAndProspectiveBeforeRuntimeWork|TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation|TestWithBackupAssetMutationFailureReleasesGateAndSnapshotSeesOnlyOldOrNewValues|TestBackupAssetSearchConfigAndOverlayConfigSnapshotIsCompleteCopiedAndMutationAtomic)$' \
  -count=10
```

Result: PASS. Package times were `1.041s`, `1.569s`, and `2.295s` respectively.

Owned non-runtime package tests and diff check:

```text
go test ./internal/settings ./internal/backupasset -count=1
git diff --check
```

Result: PASS. Package times were `0.199s` and `0.608s`; `git diff --check` produced no output.

Focused vet:

```text
go vet ./internal/settings ./internal/backupasset ./internal/backupasset/runtime
```

Result: PASS with no output.

## Deliberately retained Phase 2 RED

After Phase 1 GREEN:

```text
go test ./internal/backupasset/runtime \
  -run '^TestRuntimeEnableTransition(Content|Search)ConfigDoesNotReenterSettingsMutation$' \
  -count=1 -v
```

Result: intentionally RED, exit 1. Content failed at 2.00 seconds with `deadlock-stage=content-config`; Search
failed at 2.01 seconds with `deadlock-stage=search-config`. Phase 1 does not modify transition APIs or handlers,
so those production re-entry paths remain the Phase 2 starting REDs.

The known unrelated runtime `cache_root_unverified` failure remains outside this phase and was not modified.
Full runtime-package GREEN is not claimed while the two intentional deadlock selectors remain RED.
