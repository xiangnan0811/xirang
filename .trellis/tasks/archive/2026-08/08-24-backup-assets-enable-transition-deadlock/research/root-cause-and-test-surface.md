# Root cause and test surface

## Sanitized production evidence

- Version: v0.50.3, schema `72|0`, container healthy before the operation.
- Readiness: existing install, inventory complete/ready/current-digest acknowledged.
- Action: one Admin click on “启用备份资产”.
- Gateway result: `PUT /api/v1/settings` returned 499 after about 692 seconds.
- Durable result: `backup_assets.enabled` absent, `enablement_succeeded_at` empty, repositories/links/points all zero.
- Server logs: no fatal/panic/error for the request; absence is consistent with blocking before an error boundary.
- Mitigation: controlled restart; v0.50.3 returned healthy with restart count 0, feature disabled, schema clean,
  active TaskRuns 0 and no critical log.

Raw hosts, IPs, user agents, paths, secrets and inventory task names are intentionally omitted from this repository
evidence. The operator transcript remains outside project history.

## Exact first re-entry chain

1. `backend/internal/api/handlers/settings_handler.go:349` calls
   `settings.Service.WithBackupAssetMutation` for Foundation values.
2. `backend/internal/settings/service.go:1409` acquires `backupAssetMutationMu` and keeps it across the callback.
3. `transitionBackupAssetSettingsMutation` calls the real Runtime settings transition.
4. `backend/internal/backupasset/runtime/runtime.go:2115` routes an enabled overlay to `TransitionFeature`.
5. `backend/internal/backupasset/runtime/runtime.go:1951` calls real Content `PrepareEnable`.
6. `backend/internal/backupasset/runtime/runtime.go:2664` calls `foundation.ContentConfig()`.
7. `backend/internal/backupasset/service.go:600-603` calls `atomicFoundationValues()`.
8. `backend/internal/backupasset/service.go:969-979` calls `BackupAssetSettingsSnapshot()`.
9. `backend/internal/settings/service.go:1427` tries to acquire the same `backupAssetMutationMu`.

Go's `sync.Mutex` is not reentrant. The same goroutine blocks forever and cannot reach a context check, error log,
persistence or deferred outer unlock.

## Exact second re-entry chain

After Content and Admission/persistence, `TransitionFeature` calls `startSearchAfterEnable` and `startupSearch`.
`startupSearch` invokes `searchWorker.StartupPass`. The worker's production Config closure was composed at
`backend/internal/backupasset/runtime/runtime.go:736-746`; line 738 calls `foundation.SearchConfig()`, which calls
`SearchOverlayConfig()`, `atomicFoundationValues()` and the same locked snapshot.

This path is independently sufficient to self-deadlock. A Content-only patch is therefore incomplete.

## Affected entry points

- `PUT /api/v1/settings` → `persistSettingsMutation`.
- `DELETE /api/v1/settings/{key}` → `deleteSettingOverride` when fallback changes a Foundation value.
- `POST /api/v1/config/import` → shared Foundation transition/persistence path.
- Enable and disable failure restoration paths when they call a current-value getter while the mutation is owned.

Non-Foundation settings do not use this coordinator. Normal startup reads settings without already owning the
mutation lock and is not the observed self-deadlock, although its dynamic getter behavior must remain compatible.

## Atomicity that must not regress

`backend/internal/settings/service_test.go` already proves a concurrent snapshot reader blocks during a multi-key
mutation and sees no half-transition. The outer coordinator also serializes runtime drain/build/persist ordering.
Removing it or doing runtime work after unlock would fix the symptom by breaking the stronger data/runtime contract.

The fix must preserve:

- one mutation owner;
- reader visibility of either complete old or complete new settings;
- runtime drain/prepare coupled to the same mutation outcome;
- exact failure rollback before another mutation begins.

## Existing safe pattern to extend

`backupasset.RecoveryConfigFromValues` and `backupasset.ExportConfigFromValues` already parse a complete,
validated prospective snapshot without rereading settings. Content and Search/Overlay current getters still inline
parsing after `atomicFoundationValues`; they need matching `FromValues` helpers and shared private parsers.

## Existing false-GREEN seam

`setupSettingsEnablementHandler` in `settings_transition_test.go` builds
`assetruntime.EnablementRuntime(...)`. That helper injects `gaSilentContentManager` and `gaSilentExportManager`;
the Content manager returns nil without touching Foundation, and the lightweight runtime has no production Search
worker. These tests correctly verify handler status/order but cannot detect the production lock graph.

Many runtime transition tests use `runtimeContentManagerFake`, which has the same limitation unless explicitly
configured to call a real Foundation getter. The P0 needs at least one production-equivalent integration seam plus
a static guard so later fake refactors cannot hide a nested read again.

## Failure/rollback questions implementation must prove

- `recordEnablementSucceeded` currently runs before the supplied settings persist inside the admission callback.
  Tests must prove no stamp remains when persistence or later Search startup fails.
- Search failure currently reverts enabled through a separate settings update. The fixed path must restore the exact
  prior setting and stamp while the mutation remains exclusive, without opening a half-live window.
- Disable persistence failure currently calls Content `PrepareEnable` again; the real method rereads Foundation and
  can deadlock. Restoration must receive prior typed config.
- A request context cannot cancel `sync.Mutex.Lock`; concurrent wait cancellation needs a context-aware acquisition
  boundary, while normal snapshot readers keep blocking semantics.
- A timeout is only safe after every transition-owned resource/goroutine is stopped and joined.

## Required test surface

1. Isolated real self-deadlock RED for Content.
2. Separate Search closure self-deadlock RED.
3. Prospective/current parser equivalence and complete-snapshot validation.
4. PUT, DELETE fallback and config import production-equivalent success.
5. Existing install readiness/ack 409 matrix unchanged.
6. Atomic reader old/new visibility and canceled concurrent mutation waiter.
7. Content, Admission, persistence, stamp, Search key/pass and rollback failure matrix.
8. Disable restore with prior config and no Foundation read.
9. Bounded timeout/cancel with close/drain/join and no later mutation.
10. Static/source guard plus repeated/race/full-package/backend gates.

## Production safety boundary

Current mitigation is stable and requires no further action. Do not retry enable, directly insert the setting, edit
the success stamp or use a dirty/force escape hatch. The next production mutation is authorized only after a fixed
stable release and an explicit one-attempt acceptance runbook are available.
