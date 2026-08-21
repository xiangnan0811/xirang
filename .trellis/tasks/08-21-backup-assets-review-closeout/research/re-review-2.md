# Re-review 2 — uncommitted work on `feat/backup-assets-review-closeout`

Date: 2026-08-21. Independent defect-first review. Does not share the implementer conversation. `research/re-review-1.md` is implementer-authored and is **not** authority.

Compared to: `origin/main` / `dd2cb4a7` (HEAD equals origin/main; all product work is uncommitted).

## Snapshot

Frozen at review start:

- Branch: `feat/backup-assets-review-closeout`
- `git status`: dirty working tree + untracked Child 17 / leftover-410 / CI / frontend files (no commits on the branch)
- `git diff --stat origin/main`: 31 tracked files, +534 / −461
- Full diff saved for this review; untracked product files were read in addition to the tracked diff

**Post-snapshot drift (noted, not re-reviewed as a new pass):**

- `research/local-gates.md` appeared after the freeze (implementer gate log; not in the initial untracked list).
- `TestTransitionFeatureSearchFailureDoesNotPersist` was added to `ga_runtime_test.go` after the first read of that file.
- Full-router admin restore assertion was tightened from “not 200” to `403`.

This review uses the tree as last inspected, and calls out those additions so the parent does not treat a stale “missing rollback test” as current.

## Verdict

**fix-then-commit**

P0 leftover snapshot **read** HTTP is actually 410 for Admin / Operator / Viewer, with unauthenticated 401. Locks hold. That is not enough to call the closeout clean: AC8 / AC9 / AC10 still miss the letter of the PRD, AC4 HTTP proof is source-string only, and the implementer ledger over-closes several P1 rows.

Not **blocked**: CodeDefault is still `"false"`, `publish-images.yml` is untouched, parent stays `planning`, acceptance protocol is `not_executed`, no invented production walkthrough.

## Critical

None that reopen the leftover snapshot read leak.

The original P0 (Viewer `tasks:read` listing / searching / diffing via leftover snapshot HTTP) is closed in code and covered by a full-router test.

## Important

### 1. Playwright does not prove browse / search / preview — `web/e2e/backup-assets-gate.spec.ts:113-125`

AC8 requires Chromium / Firefox / WebKit: closed-state reject **and** live browse / search / preview on a fixture.

Closed test (`:105-111`) is real: seeded admin session, `feature_disabled` 503, asserts 未启用 / not-enabled copy and no searchbox.

Live test is not:

- `mockLiveFeature` (`:48-103`) returns an `asset-search` body that `mapBackupAssetSearch` will reject (missing `query_generation`, `indexes`, `coverage`, `capabilities`, `permissions`, `authoritative_empty`). A successful search product cannot appear as `nginx.conf`.
- Search is optional: if no searchbox, the test only asserts `main` is visible.
- Preview is never issued or asserted.

CI will run `npm run e2e` on three browsers. That can go green without proving the required path, or red if a searchbox exists and the invalid fixture is exercised. Neither outcome satisfies AC8.

### 2. Vitest MSW is not a global fail-closed gate — `web/vitest.setup.ts:91-96`

R9 / AC9: Vitest global `onUnhandledRequest: "error"`.

The setup file documents MSW as opt-in and explicitly **not** enabled globally. Console `error` / `warn` traps **are** installed (`:111-124`). That is only half of R9.

`web/src/pages/__tests__/backups-page.a11y.test.tsx` uses `server.listen({ onUnhandledRequest: "error" })` locally. That does not make the rest of the suite fail on unhandled requests.

### 3. Load CI path is not 10k catalog + SIGKILL — `scripts/test-backup-asset-load.sh:25-28,148-157`

R10 / AC10: CI-bounded executable (at least 10k catalog + zip-bomb reject + controlled SIGKILL recover); million as an operator mode that actually runs or fails; documented.

What the script does:

- CI catalog cap is `16`, and the owner test pages with `Limit: 1`.
- `million-catalog` reprints “operator 10k catalog stand-in” and re-runs the **same** pagination test.
- `million-catalog-full` still `fail`s (honest refuse).
- Zip-bomb / archive-ratio tests are real Go tests (good).
- `process-restart` is export + search restart owners, **not** SIGKILL.
- `docs/` has **no** `BACKUP_ASSET_LOAD` / `million-catalog` / 10k / SIGKILL operator instructions (grep empty).

This is no longer “print refusal and exit 0”. It is still not the PRD floor. Ledger `closed-with-limits` is closer than `closed`, but “10k stand-in” is not honest.

### 4. SLO “alert hook” is ideas only — `docs/admin/backup-assets-slo.md:8-12`

R10: documented SLO **and** at least one configurable alert rule (search 5xx, audit write fail, FeatureLive jitter). Design allowed PromQL + a settings hook if the alerting package has no static-rule slot.

The new doc has an “Alert idea” column. There is no alert YAML, no settings-backed rule, no PromQL snippet wired to `internal/alerting`. Design said do not invent a new alerting product — that waiver needs to be explicit on the ledger, not implied by a markdown table.

### 5. AC4 HTTP proof is a source-string test — `backend/internal/api/backup_asset_handler_config_test.go:9-23`

Wiring is correct: `runtimeBackupAssetHandlerConfigSource` sets `Enabled: live` (`router.go:181-193`). Catalog / Search / Overlay construction injects `liveFeature` (`runtime.go:499-515,650-673`). Content broker uses FeatureLive ∧ content-ready (`runtime.go:885-888`).

What is missing: an HTTP test that **requested=true / FeatureLive=false** closes Catalog, Search, Overlay, and Content. Existing search/overlay tests stub `Enabled: false`; they do not prove the production config source. `implement.md` Wave 2 checkbox “Handler tests: requested-true / FeatureLive-false → closed” is not met.

### 6. Full-router restore matrix is still incomplete — `backend/internal/api/legacy_snapshot_http_test.go:91-102`

Design asked for Viewer, Operator, Admin-not-live, Admin-live-without-step-up, Admin-live-with-step-up.

Present:

- Viewer / Operator restore → 403 (RBAC).
- Admin without FeatureLive (nil runtime on this router) → 403 (post-snapshot tighten).
- Handler-only `TestSnapshotRestoreRequiresFeatureLive` → 403, no provider I/O.
- Isolated `step_up_test.go` still wires `WithFeatureLive(true)` for the live + step-up path.

Absent through the **full** router: Admin + FeatureLive + no step-up, and Admin + FeatureLive + step-up + grant. Code order is Auth → Admin → step-up → grant → handler FeatureLive. That is stricter than “FeatureLive before step-up”, but AC2’s live-with-step-up case is not proven on `NewRouter`.

### 7. F7 logout / asset-change re-proof is untested

Reuse on renew is implemented and tested (`use-backup-assets-state.ts:1304-1347`, test name `reuses the in-session secret-reveal proof on preview renew`). Cache is cleared on `token` / `role` (`:369-372`) and on asset change in `loadPreview` (`:1324-1326`).

AC7 second half (“换资产或登出后必须重新证明”) has no test. Design key `{userId, sessionEpoch, assetRef, action}` is approximated as `{recoveryPointId, entryId, action}` plus a token/role effect. Acceptable if token changes on logout; not proven.

### 8. Implementer ledger over-closes P1 rows — `research/risk-ledger.md`

See honesty table below. `R-P1-ci-soft` as `closed` and `R-P1-scale-cloud` as “10k stand-in” are the main mismatches.

## Minor

- Coverage floor is `BACKUP_ASSET_COVERAGE_FLOOR:-55` inside `scripts/check-backupasset-coverage.sh`, not a checked-in `coverage-floors.txt`. Handlers are not in `-coverpkg`. Reviewable, weaker than the design example.
- `go test -race` covers `./internal/backupasset/...` and **all** of `./internal/api/handlers/`, not `./internal/api` (so `legacy_snapshot_http_test.go` is not raced). Extra handler race is fine; the package-level 410 test is outside the race job.
- Console allowlist includes `/Warning:/` and `/The above error occurred in the /` (`vitest.setup.ts:98-109`), which can swallow real failures.
- AWS Native how-to remains in `docs/admin/backup-recovery.md` after a clear “不在本版本支持矩阵内” disclaimer. Matrix claim is met; leftover procedure text can still be read as supported.
- Search audit fail-closed is one shared path; there is no dedicated `asset.secret_reveal` audit-fail case. Same `writeSearchAudit` (`backup_asset_search_handler.go:202-205`).
- Catalog 410 independence is only asserted for `POST /asset-search`, not catalog list/get.
- Settings page still keeps `backup_assets` out of `CATEGORY_ORDER`. Workspace `feature_disabled` + GA panel inherit Child 16. No new “未上线 / 需完成启用条件” copy on the settings form itself. After R4 this should agree; it is not a new R5 surface.
- Content HTTP has no `Enabled` flag on the ticket config (`main.go:341-348`); the broker FeatureLive predicate is the door. Fine if documented as such.

## AC coverage

| AC | Status | Evidence |
|---|---|---|
| AC1 leftover reads 410; Catalog/Search not 410 | **Implemented with tests** | `snapshot_legacy.go:8-10`; `legacy_snapshot_http_test.go:80-89` (Admin/Operator/Viewer 410, unauth 401); `TestNewAssetSearchStaysAvailableWhenLegacySnapshotSearchIsGone`; handler-level 410 without provider I/O. Catalog list not separately asserted. |
| AC2 Viewer/Operator cannot restore; Admin not-live fails; live still step-up | **Implemented, tests incomplete** | Handler FeatureLive 403 (`snapshot_handler.go:156-159`, `TestSnapshotRestoreRequiresFeatureLive`). Full-router Viewer/Operator 403; Admin-not-live 403. Live-without / live-with step-up not on full `NewRouter`. |
| AC3 hot enable Search ready; failure leaves settings off | **Implemented with tests** (post-snapshot rollback test) | `runtime.go:1963-1973`, `startSearchAfterEnable` / `revertEnabledAfterSearchFailure`. Success: `TestTransitionFeatureStartsSearchWithoutStartupPass`. Failure persist revert: `TestTransitionFeatureSearchFailureDoesNotPersist` (added after freeze). Not an HTTP Search 200 fixture. |
| AC4 requested true, FeatureLive false → HTTP closed | **Implemented, tests incomplete** | Config source + `liveFeature` injection. Source-string test only. No HTTP requested-true / live-false matrix for Catalog / Search / Overlay / Content. |
| AC5 settings / workspace not searchable until FeatureLive | **Implemented (inherited)** | Workspace `feature_disabled` → no searchbox (Playwright closed test + existing workspace tests). No new settings “未上线” banner. Relies on R4. |
| AC6 search audit fail-closed, including secret_reveal | **Implemented with tests** | `writeAudit` returns error; success path 503 and no hit leak (`TestAssetSearchAuditWriteFailureDoesNotReturnHits`). Secret_reveal shares the same write. Nil audit fail-closed. Failed-search audit error still discarded (`:191-194`) — allowed for unsuccessful searches. |
| AC7 same-session renew reuses proof; asset/logout re-prove | **Implemented, tests incomplete** | Renew attaches cached proof; TOTP once in the renew test. Clear-on-token/role/asset exists; no re-prove test. |
| AC8 Playwright three browsers, closed + live browse/search/preview | **Missing** | Config has chromium/firefox/webkit. Closed path OK. Live path does not prove search or preview. |
| AC9 CI race / coverage / high audit / MSW+console fail red | **Partial** | Race + coverage floor + `govulncheck` (no `continue-on-error`) + `check-npm-audit.sh` + console trap are blocking. MSW global missing. Codecov upload still `continue-on-error` (ledger waiver). Local race hit tmpfs quota (`local-gates.md`); CI proof pending. |
| AC10 load CI executable; million documented; AWS Native out of matrix | **Partial** | CI calls `BACKUP_ASSET_LOAD_LOCAL=ci-bounded`; zip-bomb owners run. Not 10k, not SIGKILL. Million mode is the same Limit:1 test. No operator docs in `docs/`. AWS Native marked unsupported in `backup-recovery.md:60-62`. |
| AC11 ledger covers review items; parent notes v0.50.1 | **Partial** | Ledger exists and covers the review IDs. Several statuses over-close (below). Parent notes correctly say formal release **v0.50.1**, Child 17 active, parent-final-acceptance not authorized. Parent `status` is `planning`. |
| AC12 acceptance protocol complete; production `not_executed` | **Implemented** | `acceptance-protocol.md`: fields present; `production_walkthrough` = `not_executed`; no invented SHA/digest. |
| AC13 CodeDefault false; no Worker publish; parent planning | **Implemented** | `settings/service.go:296` `CodeDefault: "false"`. `publish-images.yml` not in the diff / no Worker image. Parent `task.json` `status: planning`. |

## Review-finding closeout (from `review-2026-08-21.md`)

| Finding | Status |
|---|---|
| P0 leftover snapshot read HTTP | **Implemented with tests** (410, routes kept) |
| P0/P1 leftover restore second plane | **Implemented**; full-router live+step-up matrix incomplete |
| P1 enablement not atomic / handler requested | **Code implemented**; HTTP FeatureLive-vs-requested test missing; rollback test added after freeze |
| P1 no bound acceptance protocol / v0.50.1 notes | **Implemented** (protocol `not_executed`; parent notes corrected) |
| P1 CI soft | **Partial** — race/coverage/govulncheck/npm-high/console blocking; MSW global and real-browser proof missing |
| P1 scale / cloud / failure | **Partial** — executable CI owners; not 10k/SIGKILL; AWS Native out of matrix |
| P1 search audit fail-open | **Implemented with tests** |
| P2 F7 renew + preview chrome + Restic Range | **Mostly implemented** — renew reuse + `min-h-[24rem]` + `assetRefKey` + `TestResticUnprovenRangeIssuesRangeNone`; logout/asset re-prove untested |
| P2 ledger / doc truth | **Partial** — parent notes honest; Child ledger over-closes CI/scale |

## Ledger honesty

| ID | Ledger status | Honest? |
|---|---|---|
| R-P0-legacy-read | closed | **Yes** |
| R-P0-legacy-restore | closed | **Mostly** — FeatureLive gate is real; live+step-up full-router cases not shown |
| R-P1-enable-atomic | closed | **Yes, after drift** — success + rollback tests now exist. Was overstated at freeze. |
| R-P1-handler-requested | closed | **Overstated** — wiring + source-string test, not HTTP requested-true / live-false |
| R-P1-acceptance | open | **Yes** |
| R-P1-ci-soft | closed | **No** — MSW global missing; Playwright does not prove the path; “pending CI run” belongs on an open or `closed-with-limits` row |
| R-P1-scale-cloud | closed-with-limits | **Partly** — zip-bomb/restart owners are real; “10k stand-in” is the same Limit:1 test; SIGKILL absent; million undocumented in `docs/` |
| R-P1-search-audit | closed | **Yes** |
| R-P2-f7-renew | closed | **Mostly** — renew reuse tested; logout/asset re-prove not |
| R-P2-preview-chrome | closed | **Yes** |
| R-P2-doc-truth | closed | **Yes** for parent notes / v0.50.1 |
| R-LOCK-* | closed | **Yes** — locks held, not “done shipping” |

## Locks

| Lock | Status |
|---|---|
| `backup_assets.enabled` CodeDefault `"false"` | Held (`settings/service.go:296`) |
| Do not publish Worker / do not edit `publish-images.yml` | Held (not in the diff) |
| Parent stays planning | Held |
| Do not invent production walkthrough | Held (`not_executed`) |
| Wide GA / default-on | Held (No-Go) |

## Ready to commit?

**With fixes**

Do not treat this branch as commit-ready until:

1. Playwright live fixture is a valid search/catalog/preview product and asserts browse + search + preview (no `main`-only fallback).
2. Vitest enables MSW `onUnhandledRequest: "error"` globally, or the ledger explicitly waives it with a reason.
3. Load CI matches the PRD floor **or** the ledger/PRD are amended: state the actual catalog N, drop the 10k claim, document million/SIGKILL as operator-not-executed.
4. Add HTTP tests for requested=true / FeatureLive=false on Catalog, Search, Overlay, and Content.
5. Correct ledger statuses (`R-P1-ci-soft`, `R-P1-scale-cloud`, `R-P1-handler-requested`) to match the branch.

P0 leftover 410, audit fail-closed, FeatureLive handler wiring, F7 renew reuse, parent notes, and lock compliance can stay. They do not make AC8–AC10 true.
