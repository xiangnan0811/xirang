# Phase 3 RED/GREEN evidence — bounded transition and exact rollback

Date: 2026-08-24

Scope stayed inside Phase 3 runtime/settings behavior. Handler HTTP timeouts, PUT/DELETE/import integration,
deploy, migrations and node logs were not changed.

## Central deadline and operation context

- RED: `go test ./internal/backupasset/runtime -run '^TestFeatureTransitionContextUsesCentralCeilingAndEarlierCallerDeadline$' -count=1`
  failed to compile because `featureTransitionCeiling` and `newFeatureTransitionContext` were undefined.
- GREEN: the same selector passed after adding a 25-second centralized operation ceiling. It composes with an
  earlier caller deadline. Synchronous compensation has one shared four-second absolute cleanup deadline, so the
  complete operation plus cleanup ceiling is 29 seconds and remains strictly below the 30-second write timeout.
- RED: `go test ./internal/backupasset/runtime -run '^TestFeatureTransitionPropagatesOneBoundedContextThroughContentAdmissionAndPersist$' -count=1`
  showed Admission was unbounded.
- GREEN: the same selector passed after the state machine created one opCtx before Content and Admission.
- RED: `go test ./internal/backupasset/runtime -run '^TestBackupAssetSettingsTransitionPropagatesOneBoundedContextThroughAllRuntimeStages$' -count=1`
  showed Export and Recovery had no deadline.
- GREEN: the same selector passed after the settings transition reused the same opCtx for Content, Admission,
  persistence, Search, Export and Recovery.
- RED: the settings context selectors failed to compile because `UpdateContext` and `UpdateManyContext` did not
  exist. GREEN: `go test ./internal/settings -run '^TestUpdate(Many)?ContextCanceledBeforePersistenceMutatesNothing$' -count=1`
  passed (`0.007s`) after adding GORM `WithContext` persistence while preserving the old API adapters.

## Cancellation, drain and join

- RED: the cleanup-context selector first failed to compile because the bounded cleanup helper/reserve did not
  exist. After that helper existed, the real SearchWorker cancel test returned
  `context canceled` joined with `ErrFeatureTransitionCompensation`, proving compensation inherited the canceled
  operation context.
- GREEN: `go test ./internal/backupasset/runtime -run '^TestFeatureTransition(CleanupContextIsLiveAfterCancellationAndKeepsTotalBelowWriteTimeout|CancellationJoinsSearchAndCleanupOwnedWorkBeforeReturn)$' -count=1`
  passed (`0.099s`). Cleanup uses a detached four-second reserve, so the maximum 25s operation plus 4s cleanup is
  below 30s. No production goroutine was added. SearchWorker's existing owned build goroutine observes cancel,
  drains through its WaitGroup, reaches active=0, and cannot mutate after return.
- GREEN: `TestFeatureTransitionBlockingStagesTimeoutDrainAndJoinBeforeReturn` passed for Content, Admission,
  persistence, Export and Recovery. Every seam observed the earlier caller deadline and the transition returned
  only after the seam released and recorded its join.

## Exact rollback and failure matrix

- Search/stamp RED: `TestFeatureTransitionSearchFailureRestoresExactPriorSettingAndEmptyStamp` left
  `enablement_succeeded_at=2026-08-24T15:00:00Z` after Search failed. GREEN: the stamp moved after Search and is
  retained only after full success.
- Exact bundle RED: `TestBackupAssetSettingsSearchFailureRestoresEntirePriorBundleAndStamp` restored enabled=false
  but retained `backup_assets.export.worker_concurrency=3` instead of exact prior `2`. GREEN: reverse bundle
  compensation targets prior Export then prior Recovery, persists the exact prior overlay, restores Admission, and
  finally restores/disables Content. The test also preserves a non-empty prior success stamp exactly.
- Reverse-order persist RED: `TestFeatureTransitionCompensationFailureJoinsPrimaryAndStaysFailClosed` lacked
  `admission-transition-false` before Content disable. GREEN: Admission/settings compensation now precedes Content.
- Export RED: `TestExportTransitionFailureAfterPersistRestoresExactPriorSetting` failed to compile because the
  restore-aware Export seam did not exist. GREEN: prospective start/install failure restores persisted state and
  prior runtime state; the legacy method remains as a compatibility adapter.
- Runtime Export wiring RED: `TestBackupAssetSettingsExportFailureRestoresExactPriorOverlay` retained worker
  concurrency `3`. GREEN: the runtime selects the restore-aware production seam and restores exact prior `2`.
- Disable REDs: `TestFeatureDisableFailureRestoresPriorSearchReadiness` and
  `TestFeatureDisableContentFailureRestoresPriorContentAndSearch` left Search not-ready. Both are GREEN after
  prior-config Search and Content restoration.
- Compensation fence RED: `TestFeatureTransitionCompensationFenceRejectsFurtherMutation` returned nil and allowed
  new mutation after a fence. GREEN: fenced transitions fail with `backupasset.ErrInvalidState` before any stage.
- Export compensation RED: `TestExportTransitionCompensationFailureJoinsErrorsAndFencesReady` returned primary and
  restore errors but left Export ready/accepting. GREEN: it joins both errors and fences ready/accepting false.
- Additional GREEN matrix selectors cover Content failure, Admission/persist failure, stamp DB failure with exact
  prior nil stamp, Search failure, Export after-persist failure, Recovery after-persist failure, full success stamp,
  typed `ErrFeatureTransitionCompensation`, and fail-closed `FeatureLive`.

## Independent Phase 3 review fixes

- RED: `TestBackupAssetSettingsExportFailureRestoresPriorOverrideAbsence` found that compensation upserted the
  effective default instead of restoring the exact prior absence of a `system_settings` row. GREEN: the runtime now
  captures raw override rows before the transition and atomically restores each exact row or deletes keys that were
  absent. Direct settings tests prove rollback atomicity, post-commit cache invalidation, and preservation of the
  prior raw `updated_at` value.
- RED: `TestBackupAssetSettingsSearchFailureRestoresEntirePriorBundleAndStamp` additionally found Admission left in
  `managed` instead of the exact prior `pristine_legacy` mode. GREEN: reverse bundle compensation now quiesces and
  restores Admission around Export, Recovery, persisted overrides, Search and Content restoration.
- RED: `TestRecoveryTransitionFailureCleanupSharesFeatureDeadline` observed candidate cleanup and prior-graph
  restoration receiving successively later detached deadlines. GREEN: nested Export/Recovery cleanup derives from
  one feature-transition budget and reuses its first absolute cleanup deadline; a nested call cannot add another
  four-second reserve.
- RED: `TestRecoveryTransitionRestorationInstallFailureShutsDownCandidateBeforeFence` observed only two of three
  Recovery graphs shut down when installation of the rebuilt prior graph failed. GREEN: the uninstalled restoration
  candidate is synchronously shut down with the same cleanup deadline before the runtime enters its sticky fence.
- Compensation fences are deliberately restart-only under PRD R5: no online clear seam exists, `FeatureLive` stays
  false, publication is absent, and subsequent mutation/readiness calls reject before persistence. This is the
  documented fail-closed recovery boundary, not a successful transition.
- The success stamp has no production commit-after-error path: Search completes before the stamp transaction; after
  a successful stamp write no fallible transition stage remains. A stamp write error rolls its transaction back, and
  both exact prior nil and non-empty stamp states are covered without a false partial-commit fake.

## Verification

All commands used `GOCACHE=/home/murray/.cache/xirang-enable-deadlock/go-build` and
`GOTMPDIR=/run/user/1000/xirang-enable-deadlock`.

- `go test ./internal/backupasset/runtime -count=1` — PASS (`15.820s` final run).
- `go test ./internal/settings -count=1` — PASS (`0.192s`).
- Focused Phase 3 runtime matrix with `-count=50` — PASS (`9.271s`).
- Focused settings context selectors with `-count=50` — PASS (`0.204s`).
- Focused runtime cancellation/rollback selectors with `-race -count=10` — PASS (`4.054s`).
- Focused settings context selectors with `-race -count=10` — PASS (`1.122s`).
- `go vet ./internal/settings ./internal/backupasset/runtime` — PASS.
- `git diff --check` — PASS.

Independent review reran the full runtime package (`8.052s`), the full settings package (`0.199s`), the exact
row/deadline/Admission/Recovery cleanup selectors, the direct settings restoration transaction selectors, `go vet`,
`gofmt -l`, and `git diff --check`; all passed. A fresh scoped `golangci-lint` invocation was blocked by the installed linter binary panicking while loading a
newer Go file (`file requires newer Go version go1.27 (application built with go1.26)`), so the earlier successful
Phase 3 lint evidence was not replaced by a false local pass.

Phase 4 remains intentionally pending: no handler PUT/DELETE/config-import integration or HTTP timeout change was
made here.
