# Child 6 Atomic Catalog Implementation Plan

> **Authorization record:** The user explicitly approved schema option A, all three planning artifacts, their scope/validation contracts, and implementation/start on 2026-07-17. The total controller then ran Phase 1.4 `task.py start`; the task is `in_progress`, and this is now the active Phase 2 execution plan.

## 0. Execution contract

**Goal:** Implement atomic, ownership-filtered Catalog generations and repository/RecoveryPoint/entry/evidence/exact-two-point-diff boundaries for Restic, Rsync and Rclone without modifying Provider bytes or exposing a full UI.

**Base contract:** branch `codex/backup-assets-catalog`, base `main` at planning SHA `2edd795581f9368dbaacb27ad2d9f389848060fe`. Before future implementation, fetch/rebase only onto then-current merged `origin/main`; never depend on an unmerged sibling branch.

**Required methods when authorized:** load `trellis-before-dev`, relevant specs, `superpowers:test-driven-development`, `superpowers:executing-plans`, then `trellis-check`, `superpowers:verification-before-completion`, code review and `trellis-finish-work`. Phase 1 has loaded planning skills only.

**Approved schema decision:** The user selected design option A on 2026-07-17. Child 6 has no migration and uses a process-local per-repository lane plus durable per-point fence; it makes no cross-process same-repository/different-point serialization claim. If the user later reopens and selects B, stop before `task.py start` and amend PRD/design/this plan with an approved contiguous paired migration allocation, DDL, fixtures, compatibility/down guards and file manifest. Do not consume `000065…000071` under the selected A plan.

**Non-negotiable boundaries:**

- `backup_assets.enabled=false` remains the default.
- No Provider byte write/move/rename/version/delete; no Catalog-as-content/restore source.
- Handlers have no provider/runner/SSH/process imports and execute no commands.
- Command Provider remains typed unsupported.
- Frontend is raw DTO mapper/domain/API boundary only; no page/component/route/i18n/full UI.
- Parent task remains `planning`, unarchived and is never started.

## 1. Exact future file manifest

Any implementation-time need for an unlisted product/CI file pauses work for a plan amendment and review. Generated ignored Swagger JSON/YAML are validation artifacts, not staged files.

### 1.1 Create

| File | Purpose |
|---|---|
| `backend/internal/backupasset/catalog/contracts.go` | closed generation/coverage/staleness/availability, request/result/DTO and narrow dependency contracts |
| `backend/internal/backupasset/catalog/contracts_test.go` | enum, safe projection, zero/null and DTO serialization tests |
| `backend/internal/backupasset/catalog/identity.go` | RP-scoped keyed canonical-path entry identity and parent/root validation |
| `backend/internal/backupasset/catalog/identity_test.go` | stable ID, mutable type-change, cross-point, collision and path safety tests |
| `backend/internal/backupasset/catalog/cursor.go` | Catalog endpoint cursor codec/scope/stable tuples |
| `backend/internal/backupasset/catalog/cursor_test.go` | tamper/TTL/key overlap/scope/generation drift tests |
| `backend/internal/backupasset/catalog/indexer.go` | generation construction, batching, proof validation, activation/reconciliation |
| `backend/internal/backupasset/catalog/indexer_test.go` | state/zero-entry/manifest/fingerprint/fence/failure tests |
| `backend/internal/backupasset/catalog/ownership.go` | reusable current producing-lineage SQL scope |
| `backend/internal/backupasset/catalog/ownership_test.go` | shared repository and no-leak matrix |
| `backend/internal/backupasset/catalog/service.go` | repository/point/entry/status projections and service errors |
| `backend/internal/backupasset/catalog/service_test.go` | list/detail/offline/composite identity behavior |
| `backend/internal/backupasset/catalog/evidence.go` | layered safe exact evidence projection |
| `backend/internal/backupasset/catalog/evidence_test.go` | TaskRun/manifest/drill matching and sanitizer tests |
| `backend/internal/backupasset/catalog/diff.go` | exact two-point Catalog metadata/provider-evidence diff |
| `backend/internal/backupasset/catalog/diff_test.go` | two-point/subtree/sort/offline/provider-evidence tests |
| `backend/internal/backupasset/catalog/metrics.go` | low-cardinality Catalog worker metrics/no-op contract |
| `backend/internal/backupasset/catalog/metrics_test.go` | registration/cardinality/outcome tests |
| `backend/internal/backupasset/catalog/behavior_integration_test.go` | identical SQLite + mandatory real PostgreSQL transaction/ownership/order/cursor suite |
| `backend/internal/backupasset/provider/catalog.go` | Provider-specific canonical record/final-proof interface and validation helpers |
| `backend/internal/backupasset/provider/catalog_contract_test.go` | common Restic/Rsync/Rclone read/proof/no-mutation contract suite |
| `backend/internal/backupasset/repository/catalog_read.go` | Repository-owned exact/mutable Catalog session factory; private access/locator reconstruction |
| `backend/internal/backupasset/repository/catalog_read_test.go` | exact source, Command unsupported, private locator/cancel/close tests |
| `backend/internal/backupasset/runtime/catalog_worker.go` | startup/periodic scheduler, per-repository lane, concurrency/backoff/shutdown |
| `backend/internal/backupasset/runtime/catalog_worker_test.go` | fairness/no-overlap/lease loss/takeover/shutdown tests |
| `backend/internal/api/handlers/backup_asset_handler.go` | thin strict Catalog route handler + Swagger annotations |
| `backend/internal/api/handlers/backup_asset_handler_test.go` | bind/respond/error/RBAC/audit/command-boundary tests |
| `web/src/lib/api/backup-repositories-api.ts` | private raw repository DTO mapper/client |
| `web/src/lib/api/backup-repositories-api.test.ts` | repository mapper/request tests |
| `web/src/lib/api/recovery-points-api.ts` | private raw point/status/evidence DTO mapper/client |
| `web/src/lib/api/recovery-points-api.test.ts` | point/status/evidence mapper tests |
| `web/src/lib/api/backup-assets-api.ts` | private raw entry/diff DTO mapper/client |
| `web/src/lib/api/backup-assets-api.test.ts` | composite AssetRef/diff/cursor/request tests |

### 1.2 Modify

| File | Exact reason |
|---|---|
| `backend/internal/backupasset/provider/contracts.go` | extend narrow registration-facing types only where common Catalog proof needs existing reader facts |
| `backend/internal/backupasset/provider/contracts_test.go` | validate closed Catalog proof types, bounds and no locator serialization |
| `backend/internal/backupasset/provider/registry.go` | register/query typed Catalog proof reader capability; Command remains absent |
| `backend/internal/backupasset/provider/registry_test.go` | fail-closed incomplete registration and Command tests |
| `backend/internal/backupasset/provider/restic.go` | exact Catalog enumeration/final proof using Restic manifest-compatible codec |
| `backend/internal/backupasset/provider/restic_manifest.go` | expose/reuse canonical proof accumulator without changing publication digest semantics |
| `backend/internal/backupasset/provider/restic_test.go` | Restic Catalog exact/no-latest/offline/cancel fixtures |
| `backend/internal/backupasset/provider/restic_manifest_test.go` | empty/non-empty digest/count/completeness parity fixtures |
| `backend/internal/backupasset/provider/rsync.go` | Catalog canonical records/proof for managed and mutable reads |
| `backend/internal/backupasset/provider/rsync_manifest.go` | reuse publication-compatible proof accumulator |
| `backend/internal/backupasset/provider/rsync_test.go` | exact/mutable/race/no-follow/no-mutation fixtures |
| `backend/internal/backupasset/provider/rclone.go` | Catalog canonical records/proof and exact managed/mutable routing |
| `backend/internal/backupasset/provider/rclone_manifest.go` | reuse commit-manifest-compatible proof accumulator |
| `backend/internal/backupasset/provider/rclone_test.go` | exact/no-fallback/weak-proof/offline/cancel fixtures |
| `backend/internal/backupasset/provider/rclone_portable.go` | reopen/validate exact portable control graph and committed data root for Catalog proof |
| `backend/internal/backupasset/provider/rclone_portable_test.go` | portable exact-root/current-root-fallback rejection fixtures |
| `backend/internal/backupasset/provider/rclone_native_versions.go` | reopen exact native control key, VersionIDs and manifest chunks for Catalog proof |
| `backend/internal/backupasset/provider/rclone_native_versions_test.go` | native historic version/control mutation/no-current-version fallback fixtures |
| `backend/internal/backupasset/repository/service.go` | inject Catalog reader dependencies plus narrow authorized summary projector without a second graph/double audit |
| `backend/internal/backupasset/repository/query.go` | replace existing mixed-lineage visibility with shared pre-projection ownership scope |
| `backend/internal/backupasset/repository/query_test.go` | current/archived/deleted/imported/shared no-leak regression fixtures |
| `backend/internal/backupasset/service.go` | typed, bounded Catalog config resolution from settings foundation |
| `backend/internal/backupasset/service_test.go` | Catalog config defaults/dynamic bounds/cross-setting invariants |
| `backend/internal/backupasset/runtime/runtime.go` | compose Catalog service/indexer/worker, expose service, run/shutdown worker |
| `backend/internal/backupasset/runtime/runtime_test.go` | single graph, nil/failure, lifecycle ordering and feature-disabled tests |
| `backend/internal/api/router.go` | inject Catalog service/fail-closed stub and exact `/api/v1` routes with RBAC |
| `backend/internal/api/router_test.go` | full route/middleware/action coverage, Viewer pre-handler 403 |
| `backend/internal/api/backup_asset_rbac_test.go` | extend the existing dedicated asset Auth/RBAC-before-feature-gate matrix to every Child 6 route |
| `backend/internal/api/docs/docs.go` | tracked generated Swagger output |
| `backend/README_backend.md` | document runtime Catalog boundary and new routes/status semantics |
| `.github/workflows/ci.yml` | make real PostgreSQL Catalog behavior suite mandatory with `REQUIRE_POSTGRES_CATALOG_TEST=1` |
| `web/src/lib/api/client.ts` | compose three API factories |
| `web/src/types/domain.ts` | shared camelCase composite identity/status/evidence/diff domain types |

### 1.3 Intentionally unchanged

- No file under `backend/internal/database/migrations/{sqlite,postgres}` under option A.
- No `backend/internal/model` schema/model field change; current Catalog models map `000062`.
- No `backend/internal/settings/service.go` or test change: Child 1 already registered the bounded Catalog/reconcile/provider/lease values. `FoundationService.CatalogConfig()` consumes those exact keys; fixed backoff/freshness/abandon derivations are tested algorithm invariants.
- No `backend/cmd/server/main.go`: current main already injects one Runtime into Router and runs/shuts it as one lifecycle worker. Catalog is composed and joined inside Runtime; adding a second main-owned worker would violate the single-root contract.
- No `web/src/pages`, `web/src/components`, frontend router or locale files.
- No deploy/Docker/Provider data path changes.

## 2. TDD execution steps

Checkboxes below are the Phase 2 execution ledger. Tasks 2–9 were completed with RED→GREEN evidence and the fresh verification record in §2.1; delivery operations that require a commit/PR remain tracked separately in §6.

### Task 1: Reconfirm authorization, base and schema gate

- [x] Read approved child docs, `trellis-before-dev`, backend/frontend/guides specs and high-risk parent gates.
- [x] Verify the recorded 2026-07-17 option-A decision is still current; the user has not reopened B.
- [x] `git fetch --prune`; verify the worktree contains only approved Trellis planning changes and `HEAD == main == origin/main == 2edd795581f9368dbaacb27ad2d9f389848060fe` with no sibling-only dependency.
- [x] Verify paired `000062…000064` remain the latest files in both engines and `000065…000071` remain unconsumed reservations.
- [x] Phase 1.4 `python3 ./.trellis/scripts/task.py start 07-17-backup-assets-catalog` was run by the total controller after explicit approval; it changed `planning -> in_progress` before product edits. Phase 2 still performs fresh base/spec/pre-development checks before its first product edit.

Commands after approval:

```bash
git status --short --branch
git fetch --prune
git rev-parse HEAD main origin/main
rg --files backend/internal/database/migrations | sort
python3 ./.trellis/scripts/task.py start 07-17-backup-assets-catalog
```

Expected: dedicated branch, clean worktree, approved base, option A recorded, task becomes in progress only then. **Rollback point:** before `task.py start`, no product change exists; any mismatch stops work.

### Task 2: Freeze closed contracts, composite identity and cursor (RED → GREEN)

- [x] Write `contracts_test.go` for all closed enums, unknown fail-closed projection, state/coverage/staleness/availability separation, expected `0` versus `null`, sanitizer and composite response identity.
- [x] Write deterministic ID fixtures for root/parent, same RP/path across a mutable type change (same ID), same path across two RPs (different scoped ID), duplicate/collision, traversal, typed metadata independence and Entry Identity key loss.
- [x] Write cursor fixtures for exact sort tuple, user/role/endpoint scopes, repository/RP stable list tuples without unbounded generation claims, one-generation entry binding, two-generation/two-subtree diff binding, tamper, oversize, expiry, rotated verify-only key and stale bound generation.
- [x] Run RED tests before implementation; missing Catalog symbols were observed as the expected failure. Inline mode skips JSONL curation and does not count RED as pass.
- [x] Implement only contracts/ID/cursor needed to make focused tests pass; the focused GREEN command passes.

```bash
cd backend
go test ./internal/backupasset/catalog -run 'Test(CatalogContracts|CatalogEntryIdentity|CatalogCursor)' -count=1
```

RED expected: package/symbols absent. GREEN expected: focused tests pass. **Rollback point:** files are isolated pure/domain code; remove new package files if the approved identity/cursor contract cannot be met, with no DB/Provider effect.

### Task 3: Add three Provider Catalog proof contracts (RED → GREEN)

- [x] Write the common suite before adapter implementation. For each Provider cover canonical empty/non-empty records, page continuation, exact point, count/digest/completeness/source revision, duplicate/unsafe record, cancellation/Close, offline/timeout/resource mapping and zero mutations.
- [x] Restic: prove exact full snapshot/tag/run evidence; reject `latest`, prefix, rewritten/tag-only and incomplete manifest.
- [x] Rsync: prove committed marker/manifest and mutable pre/post revision; never follow symlink or expose root/locator.
- [x] Rclone: prove bound config + exact managed prefix/native version; reject mutable fallback and weak/unproven final proof.
- [x] Add Repository-owned factory tests that caller supplies only repository/RP IDs and Command is typed unsupported.
- [x] Run RED; then minimally extend Provider codecs/registry and repository facade without changing publication output or bytes.

```bash
cd backend
go test ./internal/backupasset/provider ./internal/backupasset/repository -run 'Test.*Catalog' -count=1
```

Expected GREEN: all three named Provider subtests pass and the test transport records only read/probe operations. A Provider without matching final proof cannot return complete. **Rollback point:** revert Catalog-only interfaces/facade; existing publication/read APIs remain compatible and Provider bytes were never changed.

### Task 4: Implement atomic indexer and real dual-engine behavior (RED → GREEN)

- [x] Write indexer tests for building/complete/partial/failed/superseded, zero-entry complete, batch boundary, abandoned build, old-active preservation and diagnostic rows never exposed.
- [x] Inject failure at every activation statement; prove transaction rollback and single active generation. PostgreSQL uses row locks; SQLite uses two real connections opened through production `database.Open`/real file DSN (`_txlock=immediate`, busy timeout) with checked CAS semantics and must not claim `FOR UPDATE` protection.
- [x] Test manifest ID/revision/count/digest/completeness and source-revision mismatch; assert `expected_digest` (publication) is never directly compared with `written_digest` (Catalog), while Provider manifest proof and independent Catalog proof are each verified; test mutable fingerprint before/after/tx race and observed_at change; transaction-bound retirement deactivates the active projection or rolls back the retirement.
- [x] Test stable point owner, acquire/renew/release/takeover, absolute deadline, `ValidateFenceTx`, old-fence late activation, and no observable zero/multiple-active window on either engine.
- [x] Write the same behavior fixtures for SQLite and PostgreSQL, including root NULL, explicit collation, generation sequence collision and transaction locking.
- [x] Run RED; implement indexer/batching/activation/reconciliation; run SQLite then real PostgreSQL.

```bash
cd backend
go test ./internal/backupasset/catalog -run 'TestCatalog(Indexer|Generation|BehaviorSQLite)' -count=1
if [ -z "${TEST_POSTGRES_DSN:-}" ]; then
  echo 'not_executed: TEST_POSTGRES_DSN unavailable'
else
  TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" REQUIRE_POSTGRES_CATALOG_TEST=1 \
    go test ./internal/backupasset/catalog -run 'TestCatalogBehaviorPostgres' -count=1
fi
```

Expected: both engines pass identical assertions. If DSN is unavailable locally, PostgreSQL status is recorded `not_executed`; it is never called pass. In CI, `REQUIRE_POSTGRES_CATALOG_TEST=1` makes missing DSN fail. **Rollback point:** disable/remove indexer; incomplete generations are rebuildable and old active/manifest/Provider data remain.

### Task 5: Implement ownership-filtered service, evidence and diff (RED → GREEN)

- [x] Seed one shared Repository with owned/unowned current producers, malformed/conflicting typed lineage, wrong TaskRun/link/repository binding, archived/deleted/nil-attributed and imported-baseline points; assert filtering before repository names/lineages/count/coverage/hasMore/cursor/evidence.
- [x] Assert Admin sees eligible points; Operator only current producing Task → NodeOwner; Viewer/unknown is rejected by service scope and later by router middleware.
- [x] Test two-stage ownership: SQL prefilter selects only control fields, strict Go lineage validation forms authorized IDs, bounded chunk scan-ahead returns `pageSize+1` visible anchors, and scan-budget exhaustion returns typed unavailable rather than false zero/hasMore.
- [x] Assert missing and unowned point/entry are the same Operator response; cross-point/repository entry replay fails.
- [x] Test offline immutable Catalog listing with `contentAvailability=false`; partial rows never list; retired has no projection.
- [x] Test evidence exact association only through non-null `RestoreDrillEvidence.source_task_run_id == point.producing_task_run_id`; nil/other drill `task_run_id` is not a fallback. Test per-layer status and never serialize TaskRun.LastError, RestoreDrillEvidence paths/errors, encrypted commit evidence or raw JSON.
- [x] Test exact two-point/different/subtree/offline metadata diff, fixed exclusion of unchanged rows, opaque-anchor cursor reloading and separate Provider evidence.
- [x] Run RED; implement SQL scopes/service/evidence/diff and repair existing repository query projection.

```bash
cd backend
go test ./internal/backupasset/catalog ./internal/backupasset/repository \
  -run 'Test(CatalogService|CatalogOwnership|CatalogEvidence|CatalogDiff|Repository.*CatalogOwnership)' -count=1
```

Expected: all no-leak and offline semantics pass. **Rollback point:** Catalog routes are not yet wired; revert service/query changes, retain no Provider changes beyond tested read seam.

### Task 6: Wire strict API, audit, Swagger and route documentation (RED → GREEN)

- [x] Write handler/router tests for every exact route in Design §8, strict query/body/size/opaque ID handling and response helpers.
- [x] Full Router matrix proves Auth + `backup_assets:list`; Viewer 403 before fake service call; disabled/nil runtime fails closed.
- [x] Assert action mapping: repository list, point list/detail/evidence/diff and asset list success/blocked/failure; scan audit payload for forbidden names/paths/cursors/native/provider/config/raw errors.
- [x] Source-boundary test rejects provider/runner/ssh/process imports, command literals and direct DB/provider work in handler.
- [x] Add service error mapping for invalid cursor 400, Viewer 403, no-leak 404, stale 409, Command 501, offline 503 and generic 500.
- [x] Keep existing `BackupRepositoryHandler`/`repository.Service` as the sole owner/auditor of repository GETs, with injected Catalog ownership/summary projection. Wire `BackupAssetHandler` only for point/status/evidence/entry/diff routes; update backend route docs and regenerate Swagger with idempotence/freshness checks.

```bash
cd backend
go test ./internal/api/... -run 'Test(BackupAsset|CatalogRoute|CatalogAudit)' -count=1
cd ..
make swag-init
before=$(git hash-object backend/internal/api/docs/docs.go)
make swag-init
after=$(git hash-object backend/internal/api/docs/docs.go)
test "$before" = "$after"
git diff --check -- backend/internal/api/docs/docs.go backend/README_backend.md
bash scripts/check-doc-freshness.sh
```

Expected: focused API tests and docs freshness pass; tracked `docs.go` contains exact routes/DTOs. Ignored `swagger.json`/`swagger.yaml` are inspected locally and not staged. **Rollback point:** remove new routes/handler/runtime accessor; service remains unreachable and feature stays false.

### Task 7: Add nonblocking worker/runtime lifecycle and mandatory PostgreSQL CI (RED → GREEN)

- [x] Write worker tests for asynchronous startup, startup-abandoned reconciliation, periodic scheduling, bounded global concurrency, same-repository no-overlap, cross-repository fairness, typed backoff/jitter and dynamic disable.
- [x] Test lease heartbeat/takeover, renewal loss cancels+joins, exact build deadline, shutdown mid-page/heartbeat/activation, repeated Shutdown and bounded stuck-provider error. Shutdown test unblocks a cancellation-ignoring reader after return and proves the durably revoked/local attempt cannot activate.
- [x] Test Runtime composes one service/indexer/worker with the same DB/keyring/registry/lease/admission and runs/joins it alongside existing workers.
- [x] Add `FoundationService.CatalogConfig()` tests using the already registered settings: batch/build/reconcile/provider concurrency/lease precedence, bounds and derived backoff/freshness/abandoned invariants.
- [x] Extend PostgreSQL CI job with `REQUIRE_POSTGRES_CATALOG_TEST=1` and exact Catalog behavior command; do not rely on backend `go test ./...` skip behavior.
- [x] Run RED; implement worker/Foundation Catalog config/runtime/CI.

```bash
cd backend
go test ./internal/backupasset/runtime ./internal/backupasset \
  -run 'Test.*Catalog' -count=1
if [ -z "${TEST_POSTGRES_DSN:-}" ]; then
  echo 'not_executed: TEST_POSTGRES_DSN unavailable'
else
  TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" REQUIRE_POSTGRES_CATALOG_TEST=1 \
    go test ./internal/backupasset/catalog -run 'TestCatalogBehaviorPostgres' -count=1
fi
```

Expected: worker is nonblocking, bounded and fully joined; mandatory PostgreSQL suite passes where service is supplied. **Rollback point:** Runtime stops scheduling and routes can be disabled; active Catalog metadata is harmless/rebuildable and no schema/Provider rollback is needed.

### Task 8: Add frontend raw DTO/domain/API boundary only (RED → GREEN)

- [x] Write mapper tests first for all closed enums, UTC RFC3339 string/null, known zero/null expectation, capability fallback, absence of raw fingerprint values and composite AssetRef on entries/breadcrumbs/diff sides.
- [x] Write request tests for exact route/query/body/cursor/AbortSignal shapes and absence of path/native locator parameters.
- [x] Implement private `Raw*` types and `map*` functions in three modules; add shared domain types and factory composition.
- [x] Scan new modules for `any`, `unknown as`, direct component fetch and snake_case leakage outside API modules.

```bash
cd web
env -u NODE_ENV npm run test -- src/lib/api/backup-repositories-api.test.ts src/lib/api/recovery-points-api.test.ts src/lib/api/backup-assets-api.test.ts
env -u NODE_ENV npm run typecheck
env -u NODE_ENV npm run lint
```

Expected: mapper/request/type/lint tests pass; no UI/route/component/locale files changed. **Rollback point:** remove API factories/types; backend remains hidden by feature gate.

### Task 9: Cross-layer verification and high-risk review

- [x] Run focused three-Provider, Catalog, repository, runtime, API and frontend tests from a clean command environment.
- [x] Run full backend/frontend/project gates; regenerate Swagger once more and ensure no unstaged generated drift.
- [x] Run security scans for direct Provider commands/locators/raw fields and product-scope file manifest.
- [x] Conduct the parent-required focused review for composite identity, keyring, RecoveryPoint lease/fence, shared Restic ownership, publication evidence boundary and asset audit retention. No parent-contract or schema deviation was found.
- [x] Complete the evidence-backed code review inline as required by this Child's explicit no implement/check-subagent mode; fix heartbeat joining, unknown evidence/generation enums, and retired projection reconciliation, then rerun affected and full gates.

```bash
cd backend
go test ./internal/backupasset/provider ./internal/backupasset/repository ./internal/backupasset/catalog ./internal/backupasset/runtime ./internal/api/... -count=1
cd ../web
env -u NODE_ENV npm run check
cd ..
make swag-init
make backend-test
env -u NODE_ENV make check
bash scripts/check-doc-freshness.sh
git diff --check
git status --short
```

Additional manifest/security inspection:

```bash
git diff --name-only
git diff --cached --name-only
git diff --name-only origin/main...HEAD
rg -n 'os/exec|exec\.Command|sshutil|CommandTransport|ProviderLocator|EncryptedProviderLocator' backend/internal/api/handlers/backup_asset_handler.go
rg -n '\bany\b|unknown as|fetch\(' web/src/lib/api/backup-repositories-api.ts web/src/lib/api/recovery-points-api.ts web/src/lib/api/backup-assets-api.ts
```

Expected: every command passes; handler scan has no forbidden command dependency; frontend `fetch(` is absent because central `request` is used; changed paths equal reviewed manifest. PostgreSQL evidence is separately captured from CI/real service, not inferred from `make check`.

After the product work commit, rerun `make swag-init` and require `git diff --exit-code -- backend/internal/api/docs/docs.go`; generated drift blocks staging/archive/PR.

**Rollback point:** stop before commit if any high-risk proof is incomplete. Disable/unwire routes and worker; leave Provider bytes/manifests/RPs untouched.

## 2.1 Fresh Phase 2 verification record (2026-07-18)

All entries below are fresh local evidence from the dedicated Child 6 worktree. RED runs were expected failures and are not counted as passing gates.

| Gate | Result | Evidence |
|---|---|---|
| focused backend | pass | Provider, Repository, Catalog, Runtime and API Catalog suites passed with `-count=1` |
| focused frontend | pass | 3 API-boundary files, 15 tests; subsequent whole frontend run reached 591 tests |
| race | pass | `go test -race ./internal/backupasset/catalog ./internal/backupasset/runtime -run 'Test.*(Catalog|Runtime)' -count=1` |
| SQLite behavior | pass | focused/full Catalog suites used production SQLite behavior fixtures, including activation rollback, fence and retirement cases |
| real PostgreSQL 18 | pass | `REQUIRE_POSTGRES_CATALOG_TEST=1` with a real local PostgreSQL 18 DSN; activation injection, zero-active visibility, concurrent generation, retired projection and ownership/order/cursor/diff parity all passed |
| Swagger | pass | existing `swag` v1.16.4 was invoked via temporary GOPATH/bin PATH; two consecutive generations were identical; tracked `docs.go` hash `6bd0d34bec5ae9ee804ad820e57650433463a42c` |
| backend full | pass | `make backend-test` |
| frontend full | pass | `env -u NODE_ENV npm run check`: 133 files, 591 tests, typecheck, lint and production build |
| project full | pass | `env -u NODE_ENV make check`; `golangci-lint` reported `0 issues`, all tests and builds passed |
| docs/diff | pass | `scripts/check-doc-freshness.sh` and `git diff --check` |
| scope/security | pass | actual paths match §1 plus task bookkeeping; no migration/model/UI/router/i18n/deploy files; handler command/import scan and frontend `any`/cast/direct-fetch scan were empty; default feature remains false |
| product commit/archive/journal | pending | performed only after explicit staging review |
| PR/required CI/merge/post-merge/main sync | not_executed | no PR exists yet; no CI or delivery pass is claimed here |

## 3. Verification matrix

| Contract | Primary proof | SQLite | PostgreSQL | Provider/frontend |
|---|---|---:|---:|---|
| zero-entry complete / no row-count inference | indexer + empty proof fixture | required | required | all 3 Providers |
| atomic active switch / old active preserved | injected transaction failures | required | required | n/a |
| lease takeover / late fence | concurrent activation fixture | required | required | runtime worker |
| mutable source race / retired projection | pre/post/tx mutation fixture | required | required | Rsync/Rclone |
| manifest count/digest/completeness | final proof mismatch suite | required | required | Restic/Rsync/Rclone required |
| state/coverage/staleness/availability | contracts/API/mapper fixtures | required | required | frontend mapper |
| composite identity / no path identity | ID/cross-point/handler request fixtures | required | required | frontend request |
| ownership before names/count/evidence | shared repo pagination fixture | required | required | router role matrix |
| offline immutable browse | service/API fixture | required | required | mapper capability |
| exact evidence/no trust promotion | evidence fixtures/sanitizer | required | required | mapper |
| exact two-point diff/cursor | diff pages + generation drift | required | required | request/mapper |
| startup/periodic/backoff/shutdown | fake clock/provider worker tests | required | DB parity not sufficient | runtime |
| Swagger/route/audit/handler command-free | router/docs/source tests | n/a | n/a | backend API |

No row may be marked pass from a skipped test. Local unavailable PostgreSQL is `not_executed`; PR merge requires the mandatory PostgreSQL CI job to pass.

## 4. Rollback plan

### 4.1 Pre-commit / pre-PR

- Remove/unwire Catalog routes and worker.
- Cancel build sessions, durably release/revoke owned `catalog_build` fences, then bounded-join Provider sessions; late unblocked work must fail activation.
- Delete only incomplete test/local Catalog generations if needed; never delete active manifests/RPs/Provider data.
- No migration down under option A.

### 4.2 Deployed application rollback

1. keep/restore `backup_assets.enabled=false` and stop accepting new Catalog work;
2. cancel work, durably release/revoke owned fences, then bounded-join runtime worker before an older binary starts;
3. revoke API exposure; active complete Catalog rows may remain unused;
4. rely on fence validation to reject late workers;
5. preserve Provider bytes, RecoveryPoints, manifests, audit and key domains;
6. ship a forward fix through normal PR/release flow; do not mutate release/Docker tags.

Catalog rollback never triggers Provider restore, rename, delete, versioning or content reads.

## 5. Commit, task, PR and release flow

Only after Task 9 passes:

1. compare actual changes to §1 manifest and stop on unrelated/user-owned changes;
2. stage explicit file paths only (never `git add backend` or `git add web/src`), inspect `git diff --cached --name-only` and `git diff --cached --check`;
3. create one coherent product work commit, proposed message `feat: add atomic backup asset catalog`;
4. run `trellis-check` and `trellis-finish-work`; archive **only Child 6**, update developer journal, keep parent planning/unarchived, and create a separate archive/journal commit;
5. push `codex/backup-assets-catalog`, open one PR with conventional title, migration statement “none (option A)”, security/Provider no-mutation proof and validation evidence;
6. monitor every required CI job, including mandatory PostgreSQL Catalog behavior, fix failures on the same branch, rerun/keep monitoring until green; do not merge with pending/missing/failing checks;
7. squash merge the single PR; do not merge Release Please PR #386;
8. monitor post-merge Release Please and applicable automation. A normal feature merge may update the open v0.46.0 candidate, but no formal GitHub Release or Docker publish is expected unless the Release Please PR is separately merged. `backend/README_backend.md` is not the root Docker Hub description, so Sync Docker Hub Description is not expected; record actual runs rather than assume;
9. fast-forward `/home/murray/code/xirang` main to `origin/main`, verify no local-only main commits, then remove/retain the feature worktree/branch per repository hygiene. Dependent Child 7 branches only from merged/synced main.

If CI repair requires work changes, preserve reviewability and the repository's squash-merge outcome; do not conceal failed evidence or rewrite someone else's changes.

## 6. Delivery status ledger

| Item | Current status | Evidence/meaning |
|---|---|---|
| Child task creation | complete | `.trellis/tasks/07-17-backup-assets-catalog`, status planning |
| dedicated branch/base | complete | `codex/backup-assets-catalog` at planning base `2edd795...` |
| focused research | complete | `research/current-main-evidence.md` |
| PRD/design/implementation plan | complete_approved | user explicitly approved all three artifacts and their contracts on 2026-07-17 |
| schema option A | complete | user selected no-migration/process-local-lane contract on 2026-07-17; deviation recorded |
| `task.py start` | complete | total controller ran Phase 1.4 after explicit approval; `planning -> in_progress`, with no product edits at transition |
| product implementation/tests | local_pass | Tasks 2–9 complete; fresh SQLite, real PostgreSQL 18, provider, race, Swagger, backend, frontend and project evidence is recorded in §2.1 |
| product work commit | pending | no commit created |
| Child archive/journal commit | pending | Child is in progress; parent remains planning and is never archived by Child 6 |
| push/PR | pending | no remote mutation |
| required CI/PostgreSQL | not_executed | no PR; no pass claim |
| merge | pending | no merge |
| post-merge automation | not_applicable | merge has not occurred |
| main sync/branch cleanup | pending | only after merge |

## 7. Final approval record

The user explicitly completed all four approvals on 2026-07-17:

1. all three focused documents;
2. the exact file/validation manifest and feature-disabled/frontend-boundary-only scope;
3. the three-Provider proof/read-session seam with no Provider mutation;
4. implementation/start.

No further product decision is open. Phase 1.4 `task.py start` is complete and the task remains `in_progress`; Phase 2 implementation and local validation are complete with fresh evidence in §2.1. Product commit, Child archive/journal, PR, required CI, merge, post-merge automation and main synchronization remain pending/not executed until their respective delivery steps occur.
