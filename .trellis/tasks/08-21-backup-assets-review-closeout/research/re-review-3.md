# Re-review 3 — uncommitted work on `feat/backup-assets-review-closeout`

Date: 2026-08-21. Independent defect-first review. Does not share the implementer conversation. `research/re-review-1.md`, `research/re-review-2-fixes.md`, and `research/local-gates.md` are implementer-authored and are **not** authority. They were read only to know what was claimed.

Compared to: `origin/main` / `dd2cb4a7` (HEAD equals origin/main; all product work is uncommitted). This pass reviews the **current working tree**, not the frozen re-review-2 snapshot.

## Snapshot

Frozen at this review start:

- Branch: `feat/backup-assets-review-closeout`
- HEAD: `dd2cb4a7` (`chore(main): release 0.50.1 (#445)`), equal to `origin/main`
- `git status`: dirty working tree + untracked Child 17 / leftover-410 / CI / frontend / SLO files (no product commits on the branch)
- `git diff --stat origin/main`: 37 tracked files, +903 / −489
- Untracked product files were read in addition to the tracked diff. `web/test-results/` was ignored and must not be committed.

New since re-review-2 (present in this tree, inspected):

- `backend/internal/alerting/backup_asset_slo.go` + test
- `backend/internal/api/handlers/backup_asset_feature_live_http_test.go`
- `backend/internal/api/legacy_snapshot_restore_matrix_test.go`
- `docs/admin/backup-assets-load.md`
- Playwright live fixture rewrite; global MSW listen; 10k catalog + SIGKILL owners; F7 re-prove tests; NewRouter requested-true / live-false HTTP matrix

Tests run by this reviewer (source review is authoritative; these are extra):

| Command | Result |
|---|---|
| `cd backend && go test ./internal/api/ -count=1 -run 'TestLegacySnapshotReadsGoneForAdminOperatorViewer\|TestNewAssetSearchStaysAvailableWhenLegacySnapshotSearchIsGone\|TestRequestedTrueFeatureLiveFalseClosesCatalogSearchOverlayContentHTTP\|TestRuntimeBackupAssetHandlerConfigSourceRequestedTrueLiveFalse\|TestFullRouterSnapshotRestoreLiveAndStepUpMatrix'` | PASS |
| `cd backend && go test ./internal/api/handlers/ -count=1 -run 'TestRequestedTrueFeatureLiveFalseClosesHandlerHTTP\|TestSnapshotRestoreRequiresFeatureLive\|TestAssetSearchAuditWriteFailureDoesNotReturnHits'` | PASS |
| `cd backend && go test ./internal/alerting/ -count=1 -run TestBackupAssetSLORulesGatedByRequestedSetting` | PASS |
| `cd web && npx vitest run src/features/backup-assets/use-backup-assets-state.test.tsx -t 're-prompts secret-reveal\|reuses the in-session'` | PASS (3 tests) |

Playwright, `npm run check`, load script, race, coverage floor, and `govulncheck` were **not** re-run here. `research/local-gates.md` is not proof of AC8 WebKit.

## Verdict

**fix-then-commit**

Six of the eight re-review-2 Importants are now actually closed in the current tree. P0 leftover snapshot **read** HTTP remains 410 for Admin / Operator / Viewer, with unauthenticated 401. Locks hold.

Two Importants remain: the SLO “hook” still cannot fire on any metric this binary emits, and the NewRouter AC4 matrix does not isolate FeatureLive for Catalog or Content. The ledger still marks `R-P1-handler-requested` `closed` as if those four doors were proven the same way.

Not **blocked**: CodeDefault is still `"false"`, `publish-images.yml` is untouched, parent stays `planning`, acceptance protocol is `not_executed`, no invented production walkthrough.

## Critical

None that reopen the leftover snapshot read leak.

The original P0 (Viewer `tasks:read` listing / searching / diffing via leftover snapshot HTTP) is still closed in code and covered by `TestLegacySnapshotReadsGoneForAdminOperatorViewer` (re-run PASS). Viewer/admin bypass `OwnershipTaskCheck`, so authenticated Viewer reaches 410 rather than a 403 ownership miss.

## Important

### 1. SLO hook PromQL does not match emitted series — `backend/internal/alerting/backup_asset_slo.go:15-37`

R10 / prior item 4: documented SLO **and** at least one configurable alert rule. Design allowed PromQL + a settings hook instead of a new alerting product.

The tree now has Go, not only markdown. That is not enough:

- `BackupAssetSLORules` is never called from `cmd/server`, `settings`, or the existing SLO subsystem (`model.SLODefinition` is tag-based success_rate/availability, not PromQL). The only caller is `backup_asset_slo_test.go`.
- Search 5xx / audit-fail expressions use `xirang_http_requests_total{path=...,code=...}`. Gin emits `http_requests_total` with labels `method`, `path`, `status` (`backend/internal/middleware/metrics.go:13-16`). There is no `xirang_` prefix and no `code` label.
- FeatureLive jitter uses `xirang_backup_asset_feature_requested` and `xirang_backup_asset_feature_live`. Those gauges do not exist. GA emits `xirang_backup_asset_ga_readiness_state` / installation class, not a requested-minus-live pair.
- The unit test only checks rule IDs, severity, and non-empty `Expr`. A tautology would pass.

An operator who copies `docs/admin/backup-assets-slo.md` into Prometheus will get empty series, not a working search-5xx / audit-fail / FeatureLive-jitter alert. That is still ideas, now in a function that looks like a hook.

Do not invent a Grafana product. Either emit the named series and use the real HTTP metric/label names, or point the expressions at series that already exist and record a ledger waiver that the “hook” is documentation-only.

### 2. NewRouter AC4 matrix does not isolate FeatureLive for Catalog or Content — `backend/internal/api/backup_asset_handler_config_test.go:59-114`

Prior item 5 asked for HTTP proof that **requested=true / FeatureLive=false** closes Catalog, Search, Overlay, and Content — via `NewRouter` or equivalent HTTP, not a source-string grep.

What is now true:

- Production Search/Overlay `Enabled` comes from `runtime.FeatureLive()` (`router.go:181-193`). `TestRuntimeBackupAssetHandlerConfigSourceRequestedTrueLiveFalse` proves the config source returns `Enabled=false` while `SearchOverlayConfig().Enabled` is true.
- `TestRequestedTrueFeatureLiveFalseClosesCatalogSearchOverlayContentHTTP` hits NewRouter and gets 503 on recovery-points, entries, asset-search, saved-searches, and delivery-tickets. Search and Overlay 503 go through that FeatureLive config source. That part is real.

What is not true:

- The runtime is `EnablementRuntime` + foundation only. `CatalogService()` is nil, so NewRouter keeps `NewFeatureDisabledBackupAssetCatalogService()` (`router.go:325-326`). Catalog 503 is the nil-service stub. The same request would 503 if FeatureLive were true.
- `dep.BackupContent` is nil, so NewRouter uses `NewFeatureDisabledBackupContentService()` (`router.go:267-270`). Delivery-ticket 503 is the nil-content stub, independent of FeatureLive.
- Catalog HTTP does not read handler `Enabled` at all (`NewBackupAssetHandler` has no config source). The handler-level `TestRequestedTrueFeatureLiveFalseClosesHandlerHTTP` still calls the catalog spy and maps `catalog.ErrFeatureDisabled`. That is not FeatureLive-vs-requested.
- Content’s real door is `liveFeature() && contentReady` on the broker (`runtime.go:885-888`). `TestBrokerIssueClosedWhenFeatureLiveFalse` is a broker unit test, not NewRouter HTTP with a live broker and requested=true.

The test name and ledger row claim a four-door FeatureLive matrix. Search/Overlay are proven. Catalog/Content are stub-closed. Flip FeatureLive to true on this fixture and Catalog/Content stay 503.

## Minor

- Restore live-with-step-up on `legacy_snapshot_restore_matrix_test.go:48-56` only forbids FeatureLive 403 / 401 / 410. It does not assert a post-gate status (400/502 without a restic provider is fine; a grant 403 would also pass). Path is built from `closed.taskID` and reused on other in-memory DBs; it works only because each fixture’s first task is almost certainly ID 1.
- Playwright live search asserts a filename that also exists in the browse fixture. Browse rows are cleared on `view: "search"`, so a blocked search projection would fail the later `toBeVisible` retry — acceptable, but asserting the directory row is gone or `source: search` would be tighter. This reviewer did not run e2e.
- Coverage floor is `BACKUP_ASSET_COVERAGE_FLOOR:-55` inside `scripts/check-backupasset-coverage.sh`, not a checked-in `coverage-floors.txt`. Handlers are not in `-coverpkg`.
- `go test -race` covers `./internal/backupasset/...` and `./internal/api/handlers/`, not `./internal/api` (package-level 410 / restore-matrix / NewRouter AC4 tests are outside the race job).
- Console allowlist still includes `/Warning:/` and `/The above error occurred in the /` (`vitest.setup.ts:103-114`).
- AWS Native how-to remains in `docs/admin/backup-recovery.md` after a clear “不在本版本支持矩阵内” disclaimer.
- Search audit fail-closed is one shared write; there is no dedicated `asset.secret_reveal` audit-fail case. Same `writeSearchAudit` (`backup_asset_search_handler.go:202-205`).
- AC1 Catalog 410-independence is still only `POST /asset-search`, not catalog list/get (those paths are exercised for 503 in the AC4 test, not against leftover 410).
- Settings page still keeps `backup_assets` out of a dedicated “未上线” form banner. Workspace `feature_disabled` + Playwright closed test remain the AC5 door.
- `startSearchAfterEnable` returns success when `searchWorker` is nil (`runtime.go:2316-2318`). Production compose sets a worker; EnablementRuntime tests can “enable” without search.
- SIGKILL owner kills a `select {}` helper, then runs the existing reconcile fixture. Documented honestly; it is not crash-recovery of the gateway process under test.
- `npm-audit-allowlist.json` lists four toolchain GHSAs. Fine if the PR names them; empty allowlist is still preferred.

## AC coverage

| AC | Status | Evidence |
|---|---|---|
| AC1 leftover reads 410; Catalog/Search not 410 | **Implemented with tests** | `snapshot_legacy.go:8-10`; `legacy_snapshot_http_test.go` Admin/Operator/Viewer 410, unauth 401 (re-run PASS). Asset-search stays off 410. Catalog list not separately asserted vs 410. |
| AC2 Viewer/Operator cannot restore; Admin not-live fails; live still step-up | **Implemented with tests** | Full-router Viewer/Operator 403 in `legacy_snapshot_http_test.go`. Admin-not-live (nil runtime and live=false + proof + grant) 403. `TestFullRouterSnapshotRestoreLiveAndStepUpMatrix` covers Admin-live without step-up (403 需要二次验证) and Admin-live with step-up+grant (not FeatureLive 403 / 401 / 410). Handler `TestSnapshotRestoreRequiresFeatureLive`. Live-with-step-up success status is weak (Minor). |
| AC3 hot enable Search ready; failure leaves settings off | **Implemented with tests** | `runtime.go:1963-1973`, `startSearchAfterEnable` / `revertEnabledAfterSearchFailure`. `TestTransitionFeatureStartsSearchWithoutStartupPass`, `TestTransitionFeatureSearchFailureDoesNotPersist`. Not an HTTP Search 200 fixture. |
| AC4 requested true, FeatureLive false → HTTP closed | **Implemented, tests incomplete** | Config source + Search/Overlay NewRouter 503 are real. Catalog/Content 503 are nil-service stubs. Broker unit test covers FeatureLive=false issue, not NewRouter with a live broker. |
| AC5 settings / workspace not searchable until FeatureLive | **Implemented (inherited)** | Workspace `feature_disabled` → no searchbox (Playwright closed test). No new settings “未上线” banner. Relies on R4. |
| AC6 search audit fail-closed, including secret_reveal | **Implemented with tests** | `writeAudit` returns error; success path 503 (`TestAssetSearchAuditWriteFailureDoesNotReturnHits`, re-run PASS). Secret_reveal shares the same write. Nil audit fail-closed. Failed-search audit error still discarded — allowed. |
| AC7 same-session renew reuses proof; asset/logout re-prove | **Implemented with tests** | Renew reuse + token-change + asset-change tests (vitest subset re-run PASS). Cache cleared on `[token, role]` and asset owner-key mismatch. |
| AC8 Playwright three browsers, closed + live browse/search/preview | **Implemented in spec; CI evidence pending** | `playwright.config.ts` chromium/firefox/webkit. Closed path asserts 未启用 and no searchbox. Live path: valid mappable search product (same shape as working MSW handlers), required searchbox, waits for POST `/asset-search`, browse list, load-preview ticket, iframe preview text. No `main`-only fallback. CI installs all three browsers with deps. Local Arch WebKit host-lib failure is known and is **not** AC8. Ubuntu CI would exercise the path; this reviewer did not run e2e. |
| AC9 CI race / coverage / high audit / MSW+console fail red | **Implemented with limits** | Race + coverage floor + `govulncheck` (no `continue-on-error`) + `check-npm-audit.sh` + global MSW `onUnhandledRequest: "error"` + console trap. Codecov upload still `continue-on-error` (ledger waiver). Race job still omits `./internal/api`. |
| AC10 load CI executable; million documented; AWS Native out of matrix | **Implemented with limits** | `TestCatalogPaginatesTenThousandCommittedEntries` writes and pages 10k rows. Zip-bomb owners run in `ci-bounded`. `TestControlledProcessSIGKILLThenRestartReconciles` sends SIGKILL to a helper then reconciles. `million-catalog` reuses the 10k owner and says so. `docs/admin/backup-assets-load.md` exists. AWS Native marked unsupported in `backup-recovery.md:60-62`. |
| AC11 ledger covers review items; parent notes v0.50.1 | **Partial** | Ledger covers the review IDs. `R-P1-handler-requested` still over-closes (Important 2). `R-P1-ci-soft` / `R-P1-scale-cloud` / `R-P1-acceptance` now match evidence. Parent notes: formal release **v0.50.1**, Child 17 active, parent-final-acceptance not authorized. Parent `status` is `planning`. |
| AC12 acceptance protocol complete; production `not_executed` | **Implemented** | `acceptance-protocol.md`: fields present; `production_walkthrough` = `not_executed`; no invented SHA/digest. |
| AC13 CodeDefault false; no Worker publish; parent planning | **Implemented** | `settings/service.go:296` `CodeDefault: "false"`. `publish-images.yml` not in the diff / no Worker image. Parent `task.json` `status: planning`. |

## Review-finding closeout (from `review-2026-08-21.md`)

| Finding | Status |
|---|---|
| P0 leftover snapshot read HTTP | **Implemented with tests** (410, routes kept) |
| P0/P1 leftover restore second plane | **Implemented with tests** on full `NewRouter` (live matrix present; success status weak) |
| P1 enablement not atomic / handler requested | **Code implemented**; Search/Overlay HTTP FeatureLive-vs-requested proven; Catalog/Content HTTP still stubbed |
| P1 no bound acceptance protocol / v0.50.1 notes | **Implemented** (protocol `not_executed`; parent notes corrected) |
| P1 CI soft | **Mostly implemented** — race/coverage/govulncheck/npm-high/MSW global/console blocking; Playwright spec would prove on Ubuntu CI; Codecov still soft |
| P1 scale / cloud / failure | **Implemented with limits** — real 10k + zip-bomb + helper SIGKILL; million documented as the same 10k; AWS Native out of matrix |
| P1 search audit fail-open | **Implemented with tests** |
| P2 F7 renew + preview chrome + Restic Range | **Implemented with tests** — renew reuse + token/asset re-prove + `min-h-[24rem]` + `assetRefKey` + `TestResticUnprovenRangeIssuesRangeNone` |
| P2 ledger / doc truth | **Partial** — parent notes honest; `R-P1-handler-requested` still over-closes |

## Prior Important items (re-review-2 → current tree)

| # | Prior item | Now |
|---|---|---|
| 1 | Playwright live path browse + search + preview, no `main`-only fallback, mappable search | **Fixed** — `web/e2e/backup-assets-gate.spec.ts:179-211`. Fixture matches working MSW search/ticket shape; `mapBackupAssetSearch` / `mapBackupContentTicket` accept it. Searchbox + POST + preview iframe required. |
| 2 | Vitest global `server.listen({ onUnhandledRequest: "error" })` | **Fixed** — `web/vitest.setup.ts:93-95`. Local backups-page listen/close removed so the global server is the gate. |
| 3 | Load CI 10k + zip-bomb + SIGKILL; million must not claim a literal million | **Fixed** — 10k owner is real; million mode reprints that it reuses 10k; `docs/admin/backup-assets-load.md` matches. |
| 4 | SLO alert hook is real code | **Still open** — function exists, PromQL does not match emitters, never called from server/settings. |
| 5 | AC4 HTTP requested=true / FeatureLive=false on Catalog/Search/Overlay/Content | **Partial** — Search/Overlay NewRouter is real; Catalog/Content are feature-disabled stubs. |
| 6 | Full-router restore matrix Viewer/Operator/Admin-not-live/live-without/live-with step-up | **Fixed** — combined `legacy_snapshot_http_test.go` + `legacy_snapshot_restore_matrix_test.go` (re-run PASS). |
| 7 | F7 renew reuses proof; logout/token and asset-change re-prove tested | **Fixed** — tests exist and passed in this review. |
| 8 | Ledger honesty for ci-soft / scale-cloud / acceptance / handler-requested | **Partial** — first three now match; `R-P1-handler-requested` is still overstated. |

## Ledger honesty

| ID | Ledger status | Honest? |
|---|---|---|
| R-P0-legacy-read | closed | **Yes** |
| R-P0-legacy-restore | closed | **Yes** — FeatureLive gate is real; full-router live+step-up exists (success status weak, not a status lie) |
| R-P1-enable-atomic | closed | **Yes** — success + persist-revert tests exist |
| R-P1-handler-requested | closed | **No** — Search/Overlay HTTP is real; Catalog/Content HTTP evidence is stub 503. Keep `closed-with-limits` until a live catalog/broker NewRouter case exists |
| R-P1-acceptance | open | **Yes** |
| R-P1-ci-soft | closed-with-limits | **Yes** — MSW global and Playwright spec are in tree; WebKit proof is Ubuntu CI; Codecov still non-blocking |
| R-P1-scale-cloud | closed-with-limits | **Yes** — 10k is 10k; million is the same owner; SIGKILL helper + reconcile; AWS unsupported |
| R-P1-search-audit | closed | **Yes** |
| R-P2-f7-renew | closed | **Yes** — renew + token + asset tests exist |
| R-P2-preview-chrome | closed | **Yes** |
| R-P2-doc-truth | closed | **Yes** for parent notes / v0.50.1 |
| R-LOCK-* | closed | **Yes** — locks held, not “done shipping” |

## Locks

| Lock | Status |
|---|---|
| Parent `.trellis/tasks/07-12-backup-data-explorer-design` stays `planning` | Held (`task.json` `status: planning`; not archived) |
| `backup_assets.enabled` CodeDefault `"false"` | Held (`settings/service.go:296`) |
| Do not publish Worker / do not edit `publish-images.yml` | Held (not in the diff; no Worker image in that workflow) |
| Wide GA / default-on | Held (No-Go) |
| Do not invent production walkthrough | Held (`acceptance-protocol.md` `not_executed`) |
| Leftover snapshot **read** routes: 410 for every authenticated role | Held |
| Search audit fail-closed, no new outbox | Held |
| Native AWS stays unsupported until a live suite | Held |

## Ready to commit?

**With fixes**

Do not treat this branch as commit-ready until:

1. SLO expressions use series this process actually exposes (or the ledger waives the hook as documentation-only and stops implying a configurable alert). Do not leave `xirang_http_requests_total{code=...}` or non-existent FeatureLive gauges as the contract.
2. Add NewRouter (or equivalent HTTP) cases where Catalog and Content are the **production** services with requested=true / FeatureLive=false — not `FeatureDisabled` stubs. Search/Overlay can stay as they are.
3. Set `R-P1-handler-requested` to `closed-with-limits` (or close it only after item 2).

P0 leftover 410, audit fail-closed, atomic enable + rollback, F7 renew/re-prove, Playwright spec, global MSW, 10k/zip-bomb/SIGKILL load honesty, parent notes, and lock compliance can stay. They do not make R10’s alert hook or AC4’s Catalog/Content HTTP proof true.

Do not commit `web/test-results/`.
