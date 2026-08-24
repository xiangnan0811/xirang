# Phase 4 RED/GREEN evidence

Date: 2026-08-24

## Genuine REDs

- Runtime persistence context: `go test ./internal/backupasset/runtime -run '^TestBackupAssetSettingsPersistenceReceivesCanceledOperationContext$' -count=1` failed with `runtime settings persistence callback is not context-aware`.
- Settings PUT persistence: `go test ./internal/api/handlers -run '^TestSettingsPUTPersistenceHonorsRuntimeOperationCancellation$' -count=1` returned HTTP 200 instead of the required generic 500 and zero writes.
- Export and Recovery seams: their focused runtime selectors reached the legacy persistence callbacks and failed with the closed test-only legacy-seam errors.
- Overlay persistence: `go test ./internal/backupasset/overlay -run '^TestOverlayIdempotencySettingsTransitionPassesOperationContextToPersistence$' -count=1` failed because the callback was not context-aware.
- Config import: `TestConfigImportPersistenceHonorsRuntimeOperationCancellationWithoutPartialImport` returned HTTP 200 with one node and two settings persisted after runtime cancellation.
- DELETE Content reset: `TestSettingsDELETEPersistenceHonorsRuntimeOperationCancellation` returned HTTP 200 because the previous live-settings filter bypassed the runtime contract for Content/Search Foundation keys.

## GREEN behavior

- Runtime, Export, Recovery and Overlay pass the bounded operation context through context-aware persistence and restoration callbacks.
- PUT and DELETE bind their GORM transaction and setting mutation to the runtime-supplied context.
- Config import keeps nodes, tasks, all settings and the backup-asset graph in one `WithContext` transaction. Runtime cancellation returns the standard generic 500 with zero node, Foundation-setting or non-Foundation-setting writes.
- Production-equivalent handler probes parse the complete prospective snapshot through `FoundationTransitionConfigFromValues`, `ContentConfigFromValues` and `SearchOverlayConfigFromValues` for PUT, DELETE and import.
- DELETE covers environment fallback true and false, exact override absence on success, and exact override preservation on cancellation.
- Readiness/ack fixtures retain the public transition contract and the existing typed 409 response.

## Verification

- Owned packages: settings `0.199s`, runtime `8.221s`, handlers `5.385s`, Overlay `0.399s`.
- Focused repetition `-count=50`: settings `0.565s`, Overlay `1.464s`, runtime `0.545s`, handlers `1.838s`.
- Focused race `-count=10`: settings `1.359s`, Overlay `1.968s`, runtime `1.896s`, handlers `2.559s`.
- `go vet ./internal/settings ./internal/backupasset/overlay ./internal/backupasset/runtime ./internal/api/handlers`: PASS.
- `git diff --check`: PASS before the final evidence update.

The first handler repetition link was environmentally blocked by the home-cache output quota. Re-running with the task-local tmpfs Go cache produced the recorded GREEN result. Phase 5 broad backend/build/lint/source-scan/task-validation and independent review remain pending.
