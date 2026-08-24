# Phase 5 — Verification And Break-Loop Evidence

Date: 2026-08-24

## Root Cause Classification

### Direct cause

The Foundation mutation owner held `settings.Service`'s serialization gate while
the enable transition entered Content and Search callbacks that dynamically read
Foundation settings. Those reads tried to acquire the same non-reentrant gate,
so the transition could not reach persistence or release ownership.

### Systemic causes

1. **Cross-layer contract gap** — settings owned the lock, but the settings →
   handler → runtime → Content/Search boundary did not say that mutation-inner
   work must consume a prospective immutable configuration rather than current
   getters.
2. **Test coverage gap** — seam fakes omitted production Content/Search config
   callbacks, so fake-only tests did not contain the lock-reentry edge.
3. **Implicit lock-order assumption** — code assumed a settings callback could
   call arbitrary runtime work while holding the mutation gate. The ownership
   and prohibited re-entry order were neither specified nor source-guarded.

This is not a Content-only defect. Fixing only `ContentConfig()` leaves the
Search worker's dynamic config callback able to re-enter the gate. Replacing a
production component with a no-op fake proves only the reduced fake graph, not
the composed transition.

## Corrected Contract

- The handler/config-import owner acquires the mutation gate exactly once and
  receives the complete current snapshot.
- Current and prospective Foundation configurations are derived from explicit
  value maps before runtime work. Content and Search receive explicit typed
  configs; mutation-inner code does not call Foundation getters.
- The operation context propagates across settings, handlers, runtime,
  Content/Search/Overlay/Export/Recovery, GORM persistence, and restore
  callbacks. Compatibility adapters cannot downgrade the context-aware seam.
- Forward work is bounded to 25 seconds. Compensation is detached from caller
  cancellation but shares one absolute 4-second cleanup deadline, so nested
  cleanup cannot multiply the total budget.
- PUT/DELETE rollback restores the exact previous override row or exact
  absence, plus the previous admission state, enablement stamp, readiness, and
  candidate lifecycle. Effective-value-only restoration is insufficient.
- Config import seals a post-persist undo journal inside its import transaction,
  installs it after commit, and uses it to restore the complete imported graph
  if a later runtime stage fails.
- Tests exercise the production-composed object graph, or a probe with explicit
  parity for every production callback and rollback edge.
- A failed compensation joins the primary and compensation errors, marks
  runtime not ready, and engages a sticky restart-only fence. No online clear
  is allowed because the partially compensated production graph is untrusted.

## Why Earlier Fix Shapes Fail

| Fix shape | Why it fails | Prevention |
|---|---|---|
| Pass explicit config to Content only | Search can still call its dynamic config closure under the same gate. | One complete prospective transition config and AST guard across every mutation-inner component. |
| Use fake transitioner/Content/Search only | The fake graph can omit the production getter callback and report false green. | Deterministic real-path handler/runtime regression plus production-graph parity probes. |
| Add a timeout around only the handler | Persistence and component work can ignore cancellation; cleanup can multiply fresh timeouts. | Context-bearing signatures end to end and one shared absolute cleanup deadline. |
| Restore the effective value | Loses whether an override row was absent and can change DB/env/default precedence after rollback. | Capture and restore exact raw rows or exact absence. |
| Roll back only settings after import | Leaves newly imported nodes/tasks/policies/relations behind after a later runtime failure. | Post-persist undo journal for the complete imported graph. |
| Return after partial compensation | Later online transitions operate on an object graph whose state is unknown. | Joined compensation error plus sticky restart-only fence. |

## Knowledge Capture

- Added the mandatory seven-section executable scenario **Foundation Settings
  Lock Order And Prospective Runtime Transition** to
  `.trellis/spec/backend/quality-guidelines.md`.
- Added the mandatory seven-section executable scenario **Foundation Transition
  Cancellation And Compensation Errors** to
  `.trellis/spec/backend/error-handling.md`.
- Added one checklist pointer in
  `.trellis/spec/guides/cross-layer-thinking-guide.md`; the guide does not
  duplicate the backend code/error contracts.
- Template sync is **not applicable**: repository search found no matching
  project-maintained spec template/generated mirror (only
  `.trellis/.template-hashes.json`). No `.codex` agent configuration was
  touched.

## Phase 5 Verification Ledger

Verification is recorded here only after the exact command completes. The
PostgreSQL integration row must remain fail-closed when `TEST_POSTGRES_DSN` is
absent; no local database or replacement infrastructure will be created.

| Gate | Exact command | Result |
|---|---|---|
| Spec structure | `awk` each new scenario and match its numbered headings | PASS: both scenarios contain exactly the required seven sections. |
| Settings high repetition | `go test ./internal/settings -count=50` (plus the gate/context/override selector at `-count=50`) | PASS (`14.865s`; focused selector `2.553s`). |
| Focused runtime/settings/handler high repetition | `go test ./internal/backupasset ./internal/backupasset/runtime -run '<prospective/deadlock/context selector>' -count=50`; `go test ./internal/api/handlers ./internal/backupasset/overlay -run '<PUT/DELETE/import/Overlay selector>' -count=50` | PASS (`0.041s` / `9.518s`; `4.650s` / `0.793s`). |
| Race repetition | Same settings, prospective/runtime, PUT/DELETE/import/Overlay core selectors with `-race -count=10` | PASS: settings `2.247s`; backupasset `1.049s`; runtime `34.985s`; handlers `7.565s`; Overlay `1.509s`. |
| Owned package tests | `go test -p=2 ./internal/settings ./internal/backupasset ./internal/backupasset/overlay ./internal/backupasset/runtime ./internal/api/handlers -count=1` | PASS (`0.198s`, `0.605s`, `0.388s`, `8.105s`, `5.505s`). |
| Full backend tests | `go test -p=2 ./... -count=1` | Ran and failed only on filesystem-environment tests. With the short dedicated tmp root, `go test -p=2 ./... -skip '^TestLocal' -count=1` passed every package; the skipped local-socket selectors passed separately with `/tmp/x5`. See environment note below. |
| Build / vet / lint | `go build -p=2 ./...`; owned and full `go vet -p=2`; scoped and full `golangci-lint run` | PASS on Go 1.26.6; both lint runs report `0 issues`. |
| PostgreSQL integration | `env -u TEST_POSTGRES_DSN REQUIRE_POSTGRES_OVERLAY_TEST=1 go test ./internal/backupasset/overlay -run '^TestOverlayBehaviorPostgres$' -count=1 -v` | Expected fail-closed: `TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_OVERLAY_TEST=1`. No PostgreSQL pass claimed and no infrastructure created. |
| Source/AST/privacy scans | Exact mutation getter AST tests; generic-response rollback tests; added-line sink scan; `ALLOW_DIRTY_STARTUP` and `.codex` scans | PASS: no mutation-inner Foundation getter, context cancellation selectors pass, no added raw-error/sensitive sink, no dirty-startup escape, no `.codex` change. |
| Formatting/diff/task validation | `gofmt`; `gofmt -l <changed Go files>`; `git diff --check`; `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-24-backup-assets-enable-transition-deadlock` | PASS: no formatting/diff output; `implement.jsonl` and `check.jsonl` each validate with three entries. |

### Environment evidence

- The first combined race compile exhausted the task-local `/run/user/1000`
  tmpfs (`No space left on device`). Only the generated 959 MiB task cache was
  removed; no shared cache/state was cleaned. Race then passed from the
  task-local home cache in smaller package groups.
- A long home `GOTMPDIR` makes dedicated-mount/cache-root tests fail and exceeds
  the Linux UNIX-socket path limit. `/run/user/1000/x5` satisfies mount/root
  tests but remains too long for two Local socket selectors; `/tmp/x5` satisfies
  those socket selectors. The unmodified main worktree reproduces the runtime
  `cache_root_unverified` failure under the long home temp path.
- The required unmodified `go test ./...` invocation was retained as a failed
  environmental result. Broad coverage passed with only `^TestLocal` skipped
  under the short dedicated tmp root, and every skipped Local selector passed
  separately under the shorter task-local `/tmp/x5` root.
- The first build with the home task temp hit `disk quota exceeded`; the same
  `go build -p=2 ./...` passed with task-local `/run/user/1000/x5`.

### Lint closure

The first scoped lint found two unchecked test-only `tx.AddError` results and an
unused compatibility `ExportConfig` argument in the private transition helper.
The tests now explicitly ignore the injection helper return, and the private
argument is named `_` while the public compatibility signatures remain intact.
Focused regression tests, scoped lint, and full lint pass afterward.

## Exact Command Transcript

Unless a command overrides it explicitly, Go verification used:

```text
GOTOOLCHAIN=go1.26.6
GOCACHE=/home/murray/.cache/xirang-enable-deadlock-phase5-go126/go-build
GOTMPDIR=/run/user/1000/x5
TMPDIR=/run/user/1000/x5
```

High repetition:

```text
go test ./internal/settings -count=50
go test ./internal/settings -run '^(TestWithBackupAssetMutationWaitingWriterHonorsContextCancellation|TestWithBackupAssetMutationFailureReleasesGateAndSnapshotSeesOnlyOldOrNewValues|TestUpdateManyContextCanceledBeforePersistenceMutatesNothing|TestUpdateContextCanceledBeforePersistenceMutatesNothing|TestUpdateWithTxContextCanceledBeforePersistenceMutatesNothing|TestDeleteWithTxContextCanceledBeforePersistencePreservesOverride|TestBackupAssetOverrideSnapshotRestoresRawRowAbsenceAndInvalidatesCache|TestBackupAssetOverrideSnapshotFailureRollsBackAtomicallyAndKeepsCache)$' -count=50
go test ./internal/backupasset ./internal/backupasset/runtime -run '^(TestContentConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestSearchOverlayConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot|TestProspectiveConfigParsersRequireCompleteValidatedFoundationValues|TestFoundationTransitionConfigFromValuesBuildsCompleteTypedBundle|TestFoundationTransitionConfigsFromValuesBuildsPriorAndProspectiveBeforeRuntimeWork|TestRuntimeEnableTransition(Content|Search)ConfigDoesNotReenterSettingsMutation|TestRuntimeConfigAwareContentEnableReceivesProspectiveConfig|TestRuntimeConfigAwareContentRestoreReceivesPriorConfig|TestRuntimeCompatibilityTransitionBuildsOneCompleteAtomicConfigBundle|TestProspectiveFeatureLiveRequiresEnabledReadinessAndManagedAdmission|TestRuntimeSearchWorkerConfigMapsProspectiveSearchAndOverlayBundle|TestConfigAwareTransitionFunctionsDoNotReadCurrentFoundationSettings|TestSearchWorkerStartupPassWithConfigDoesNotReadDynamicConfig|TestBackupAssetSettingsPersistenceReceivesCanceledOperationContext)$' -count=50
go test ./internal/api/handlers ./internal/backupasset/overlay -run '^(TestSettingsPUTPersistenceHonorsRuntimeOperationCancellation|TestSettingsPUTUsesProspectiveContentAndSearchBundleBeforePersistence|TestSettingsDELETEPersistenceHonorsRuntimeOperationCancellation|TestSettingsDELETEEnabledFallbackUsesProspectiveBundleAndRemovesExactOverride|TestConfigImportPersistenceHonorsRuntimeOperationCancellationWithoutPartialImport|TestConfigImportPostPersistRuntimeFailureRestoresEntireImport|TestConfigImportPostPersistRuntimeFailureRestoresOverwritesAndCompleteAssetGraph|TestConfigImportUsesOneProspectiveContentSearchPersistenceBoundary|TestOverlayIdempotencySettingsTransitionPassesOperationContextToPersistence)$' -count=50
```

Race used the same three focused regexes above with `go test -race` and
`-count=10` (the prospective regex ran against backupasset + runtime, and the
PUT/DELETE/import regex against handlers + Overlay).

Owned and broad gates:

```text
go test -p=2 ./internal/settings ./internal/backupasset ./internal/backupasset/overlay ./internal/backupasset/runtime ./internal/api/handlers -count=1
go test -p=2 ./... -count=1
go test -p=2 ./... -skip '^TestLocal' -count=1
GOTMPDIR=/tmp/x5 TMPDIR=/tmp/x5 go test -p=2 ./internal/backupasset/processing ./internal/backupasset/processing/updater -run '^TestLocal' -count=1
go build -p=2 ./...
go vet -p=2 ./internal/settings ./internal/backupasset ./internal/backupasset/overlay ./internal/backupasset/runtime ./internal/api/handlers
go vet -p=2 ./...
golangci-lint run ./internal/settings ./internal/backupasset ./internal/backupasset/overlay ./internal/backupasset/runtime ./internal/api/handlers
golangci-lint run ./...
```

Fail-closed PostgreSQL and source/privacy checks:

```text
env -u TEST_POSTGRES_DSN REQUIRE_POSTGRES_OVERLAY_TEST=1 go test ./internal/backupasset/overlay -run '^TestOverlayBehaviorPostgres$' -count=1 -v
go test ./internal/backupasset/runtime ./internal/api/handlers -run '^(TestConfigAwareTransitionGuardFollowsDelegatedHelpers|TestConfigAwareTransitionFunctionsDoNotReadCurrentFoundationSettings|TestSettingsFailedTransitionLeavesEnabledOverrideUnchanged|TestSettingsFailedDeleteRestoreTransitionStaysInternalError|TestConfigImportPostPersistRuntimeFailureRestoresEntireImport|TestConfigImportPostPersistRuntimeFailureRestoresOverwritesAndCompleteAssetGraph|TestConfigImportTaskCreateFailureReturnsGenericInternalErrorAndRollsBack)$' -count=1
git diff --unified=0 -- backend | rg '^\+.*ALLOW_DIRTY_STARTUP'
git diff --unified=0 -- backend -- '*.go' | rg '^\+.*(err\.Error\(\)|\.Str\("(value|root|path|locator|secret|credential|proof|ticket|provider|evidence)"|respond[A-Za-z]+\([^\n]*err\.Error\(\)|\.Msgf\([^\n]*(value|root|path|locator|secret|credential|proof|ticket|provider|evidence))'
git status --short -- .codex
gofmt -l <the explicit changed-Go-file list recorded in the Phase 5 shell transcript>
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-24-backup-assets-enable-transition-deadlock
```

The three negative scans returned no matches. The PostgreSQL command returned
exit 1 with the required missing-DSN fatal message; that is expected fail-closed
evidence, not an integration pass.

## Independent Final Review Addendum

The final independent review found and fixed three P0 contract gaps with fresh
RED before production edits:

1. A real managed Content runtime could attach its cache and then fail
   `Broker.Resume`; the enable path returned the primary error without bounded
   cleanup. The production path now synchronously calls `PrepareDisable` with
   the shared cleanup context. A failed cleanup preserves `ErrBrokerClosed`,
   joins typed `ErrFeatureTransitionCompensation`, and engages the sticky fence.
2. The sticky fence was checked only by the global-enabled transition path, so
   a non-enabled Foundation PUT could still persist and run component work. The
   common Foundation settings entry now rejects with `ErrInvalidState` before
   snapshot, persistence, or runtime side effects. Ordinary non-Foundation PUT
   and DELETE continue to bypass the backup-asset transitioner.
3. A mixed settings PUT persisted Foundation and ordinary settings in one
   transaction, but the internal runtime snapshot covered only Foundation keys.
   A later runtime failure therefore left an ordinary setting partially
   committed. The handler now snapshots the complete registered request key set
   and restores exact raw value, timestamp, and prior row absence atomically
   with the runtime-supplied shared cleanup context; cache invalidation occurs
   only after the restore commits.

Fresh verification after those fixes used Go 1.26.6 and the task-local cache:

- The combined original deadlock/waiter and review-fix selector passed at
  `-count=50`: settings `1.907s`, runtime `9.989s`, handlers `0.337s`.
- The same selector passed with `-race -p=1 -count=10`: settings `1.759s`,
  runtime `35.173s`, handlers `1.704s`.
- Owned package tests passed: settings `0.196s`, backupasset `0.617s`, Overlay
  `0.379s`, runtime `8.165s`, handlers `5.569s`.
- The unskipped full backend command reproduced only the two known UNIX-socket
  path-length failures under `/run/user/1000/x5`. The full backend excluding
  `^TestLocal` passed, and every skipped Local selector passed separately under
  `/tmp/x5` (`0.054s` and `0.004s`).
- Full build and vet passed. Full `golangci-lint run ./...` reported `0 issues`.
- The PostgreSQL gate remained fail-closed because `TEST_POSTGRES_DSN` is absent;
  no PostgreSQL pass is claimed and no replacement infrastructure was created.
- AST/generic-response rollback tests, privacy/source scans, prohibited-scope
  scans, task validation, formatting, and diff checks passed after the addendum.

## Remaining Expansion Surface

- Any future Foundation key or runtime component must join the complete
  prospective parser and stage-by-stage rollback matrix before it can run
  inside the mutation gate.
- Any new config-import table/relationship must be added to both the sealed
  journal and the production-parity rollback test.
- Any proposed fence-clear operation requires a separate design proving full
  graph reconstruction; the current contract is restart-only.
