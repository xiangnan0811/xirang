# Implementation Plan

> Planning-only artifact. Do not run `task.py start`, modify product code, stage, commit, push or open a PR until the user separately approves implementation.

## Phase 0 — Authorize, Start, and Establish RED

1. Obtain explicit implementation approval for this PRD/design/plan.
2. Reconcile branch/worktree/base state and confirm no overlapping user changes.
3. Run `python3 .trellis/scripts/task.py start 08-29-backup-task-preview-connect-ui` from the isolated worktree.
4. Re-read the phase context and listed Trellis specs.
5. Add the smallest focused failing tests for CatalogWorker wake scheduling; run them and record genuine RED output before implementation.

Checkpoint: only task metadata and deliberately failing tests have changed; no production implementation precedes RED.

## Phase 1 — CatalogWorker Wake Scheduling

1. Add lifecycle-safe `TryWake` behavior with capacity-one non-blocking coalescing.
2. Refactor the run scheduler so an active-scan wake remains pending and triggers a follow-up pass.
3. Preserve an independent periodic deadline; reset it only after a periodic pass and prove continuous wakes cannot starve it.
4. Fold a pre-Run wake into the initial pass.
5. Preserve cancellation, join and active-metric cleanup semantics.
6. Run focused CatalogWorker tests with `-count=10` and `-race` where timing permits.

Checkpoint: worker scheduling contract is GREEN without any repository/UI changes.

## Phase 2 — Connect-to-Catalog Composition

1. Add a narrow catalog wake interface and optional setter to `repository.Service` without a runtime-package dependency.
2. Add Connect tests for success, repeated Connect, probe/transaction failure, nil/retired point and wake rejection.
3. Request wake immediately after a committed transaction only when a usable mutable point exists; ignore the best-effort outcome.
4. Wire the production CatalogWorker into the repository service in `runtime.go`.
5. Add a production-composition regression test proving the exact worker is wired once.
6. Run focused repository/runtime/API tests plus repeat/race checks.

Checkpoint: a committed Connect converges promptly while periodic scanning remains the fail-safe.

## Phase 3 — Typed Mutable-Point Response

1. Add failing domain/API tests for `MutablePoint`, `mutable_point`, null/absent and malformed values.
2. Extract a shared recovery-point snapshot mapper from the full recovery-point mapper.
3. Define a snapshot domain type that does not claim to contain catalog projection.
4. Extend `BackupRepositoryMutationResult` with nullable `mutablePoint` and keep the request body task-only.
5. Run focused API mapper/typecheck tests and scan for unsafe casts or raw snake_case leakage.

Checkpoint: UI code receives a validated point ID or an explicit null, never an asserted partial object.

## Phase 4 — Dialog and Task Entry Point

1. Add failing dialog tests for safety copy, connection, polling readiness, blocked/failed, empty complete, timeout, cancellation and precise navigation.
2. Implement `TaskPreviewConnectDialog` using existing UI primitives and a bounded two-second/60-attempt polling state machine.
3. Add shared task eligibility/disabled helpers and tests.
4. Wire one action through `TasksViewProps` into table and grid, with administrator/legacy-Rsync visibility and active-run disabled explanation.
5. Integrate dialog ownership into `TasksPage`/`TasksPageDialogs`.
6. Add complete zh/en strings and accessibility assertions (`aria-label`, `aria-live`, focus/close semantics).
7. Run focused component/page tests, typecheck and lint.

Checkpoint: both layouts expose the same safe action and all network effects stay in the focused dialog.

## Phase 5 — Whole-Task Verification

Run and preserve evidence for, at minimum:

```sh
cd backend
go test ./internal/backupasset/runtime ./internal/backupasset/repository ./internal/api/handlers -count=1
go test -race ./internal/backupasset/runtime ./internal/backupasset/repository -count=1
go test ./internal/backupasset/runtime ./internal/backupasset/repository -count=10
go test ./...
go vet ./...

cd ../web
env -u NODE_ENV npx vitest run src/lib/api/backup-repositories-api.test.ts src/lib/api/recovery-points-api.test.ts src/components/task-preview-connect-dialog.test.tsx src/pages/tasks-page.test.tsx
env -u NODE_ENV npm run check

cd ..
make check
git diff --check
```

Also perform focused searches for raw locators, credentials, secrets, proofs, unsafe casts, direct component `fetch`, ad hoc JSON responses and unexpected route/schema/Swagger changes. Reconcile acceptance criteria against actual tests and diff; update `check.jsonl` if the final ownership set differs from the approved manifest.

Checkpoint: all focused and full gates are GREEN, privacy boundaries are explicitly checked, and no acceptance item relies only on a mock/spy when production wiring is required.

## Phase 6 — Review, PR, CI, and Acceptance

1. Run the Trellis quality review and fix all Critical/Important findings on the same branch.
2. Present the exact diff, verification evidence and any remaining risk before commit/publish actions that need authorization.
3. Create a conventional commit and PR only with approval; monitor every required CI job until success or a real external blocker.
4. After squash merge, sync local `main` to `origin/main` and monitor Release Please, Docker publish and README sync as applicable.
5. If no formal release is expected, record that explicitly. Do not reuse the recovered v0.52.3 production snapshot as fresh acceptance evidence.
6. For production validation, re-query current health, schema/integrity, task/repository links, catalog coverage, privacy-safe logs and the exact task/point navigation flow.

Checkpoint: distinguish implementation GREEN, PR/CI completion, merge, release and production acceptance; do not collapse them into one completion claim.

## Local Implementation Evidence — 2026-08-29

- RED was observed before each phase: missing `CatalogWorker.TryWake`, missing `Service.SetCatalogWake`, missing `mapRecoveryPointSnapshot`/`mutablePoint`, missing dialog module, and missing page action/helper.
- Focused CatalogWorker and Connect tests passed at `-count=10`; their race suites passed at `-race -count=1`. Runtime production composition is covered by an exact-instance assertion.
- Frontend focused verification passed with Node 22 (`4` files, `50` tests). `env -u NODE_ENV npm run check` passed (`191` files, `1762` tests, lint, typecheck, coverage, and build).
- Backend `go test ./...`, `go vet ./...`, and root `make check` passed. Root verification required the already-installed Go 1.26.6 toolchain declared by `backend/go.mod`; the default Go 1.27 environment made the Go-1.26-built linter panic before analysis.
- Privacy and diff scans found no production locator/credential/secret/proof leakage, unsafe cast, direct component `fetch`, ad hoc JSON response, or route/schema/Swagger/deploy expansion. The only locator match was a mapper-test fixture that asserts the private field is discarded.
- The planned package-wide `go test ./internal/backupasset/runtime ./internal/backupasset/repository -count=10` is not GREEN: three pre-existing runtime tests retain fixed in-memory database records across Go `-count` iterations. Task-focused repeated tests and single-run full gates are GREEN; the out-of-manifest isolation defect remains an AC11 concern.
- AC12 remains pending because commit, PR, CI, merge, release automation, and production acceptance are intentionally outside this implementation handoff.

## Second Quality Review Evidence — 2026-08-29

- Closed the dialog rejection path over the stable `requestFailed` i18n notice so backend `detail.error` and raw `Error.message` text cannot reach the preview-connect status surface; the regression was observed RED before the fix.
- Made the Catalog wake channel send explicitly nonblocking even if queue/state invariants drift, with a saturated-queue RED regression test before the fix.
- Repository and Catalog/runtime focused race suites passed repeatedly (`-race -count=10` and `-race -count=20` respectively); the changed backend packages passed once in full under Go 1.26.6.
- Frontend lint and typecheck passed, the focused suite passed (`4` files, `51` tests), and the full coverage suite passed (`191` files, `1763` tests). Go vet and `golangci-lint` passed for both changed backend packages under Go 1.26.6.
- Main-session fresh verification passed after both review fixes and the code-spec update: focused backend packages, focused frontend `4` files / `51` tests, task-scoped Go race `-count=10`, Trellis context validation, `git diff --check`, and the complete Go 1.26.6 + Node 22 `make check` gate (`191` files / `1763` frontend tests plus backend tests and both production builds).
- `.trellis/spec/backend/backup-file-catalog.md` now records the seven-section executable contract for Task-only Connect, post-commit best-effort wake, saturated-queue nonblocking behavior, periodic fairness, typed mutable-point projection, closed UI errors, and required regressions.

## Independent Review Fix Evidence — 2026-08-29

- Finding 1 RED: the deterministic simultaneous-ready scheduler test did not compile because `CatalogWorker.nextIdleScan` was missing. The first implementation made periodic readiness win without resetting cadence; self-review then exposed a second RED (`wake during overdue periodic Catalog scan was not coalesced with the pending wake`) because a periodic pass still consumed the pending wake. The final scheduler promotes an already-due periodic timer before wake selection and preserves the pending wake for a follow-up pass. Focused repeat `-count=20` and race `-count=10` are GREEN.
- Finding 2 RED: `SetCatalogWake` accepted a typed-nil requester (`typed-nil Catalog wake error=<nil>`). It now rejects nil/typed-nil and duplicate wiring while retaining the original narrow requester; focused tests passed at `-count=20`.
- Finding 3 RED: controlled concurrent decrements published zero before a delayed one and left `Catalog active-build gauge=1 ... want 0` after both builds joined. A dedicated active-build lock now serializes count changes and gauge publication without holding the lifecycle mutex; repeat and race tests are GREEN.
- Finding 4 RED: after 120000ms a never-settling catalog request still had `AbortSignal.aborted=false` and remained indexing. A two-minute wall-clock deadline plus abort-aware promise boundary now aborts even a non-settling call and enters only `timed_out_background`; lifecycle/task/token/close aborts remain silent and clear the timer.
- Finding 5 REDs: an unknown nonempty executor mapped to Rsync (`expected 'rsync' to be undefined`), and a blocked/malformed publication sentinel remained eligible (`expected true to be false`). The Task mapper now preserves unknown nonempty executors as unknown while absent historical data retains the Rsync default; eligibility requires the exact `legacy_mutable` / `legacy` / `legacy` triple. The authorized `tasks-api.ts` ownership expansion is recorded in `design.md` and does not change the backend Task API.
- Finding 6 RED: duplicate and mixed Pascal/snake mutation envelopes mapped as available. The mapper now accepts exactly one internally consistent casing envelope, blocks duplicates/conflicts/mixed casing, and preserves absent/null point behavior for reconcile/disconnect.
- Finding 7 RED: the Chinese dialog exposed only one close button named `关闭` because the header close remained hardcoded `Close`. The existing Dialog close API now receives a localized `aria-label`; zh/en parity tests pass without a new primitive.
- Finding 8 added deterministic task/token catalog abort, pending-deadline cleanup, late-result suppression, retrying eligibility, and table/grid disabled-tooltip parity coverage. These tests were GREEN on first run because findings 4/5 and the pre-existing accessible inline-tooltip implementation had already established the behavior; no product-only change was manufactured for a RED.
- Focused GREEN: Go 1.26.6 runtime/repository selection passed `-count=10`; focused race passed `-count=5`; the final fairness/gauge slice passed repeat `-count=20` and race `-count=10`. Node 22 frontend focused verification passed (`4` files, `64` tests), followed by typecheck and lint.
- Full GREEN: Node 22 `env -u NODE_ENV npm run check` and the final locked Go 1.26.6 + Node 22 root `make check` passed. The final full run reported `golangci-lint` 2.11.4 with zero issues, all backend tests, `191` frontend files / `1770` tests, and both production builds.
- Task validation passed. `git diff --check` and production-diff privacy scans found no unsafe cast, direct `fetch`, ad hoc JSON response, raw locator/credential/secret/proof/provider/database detail, or route/schema/Swagger/deploy expansion. This task intentionally updates `.trellis/tasks/08-21-backup-assets-release-acceptance/task.json` to establish the reciprocal parent/child metadata link to `08-29-backup-task-preview-connect-ui`; the parent JSON retains its terminating newline.
- The previously recorded package-wide `go test ./internal/backupasset/runtime ./internal/backupasset/repository -count=10` limitation remains: three sibling runtime fixture-isolation tests reuse fixed in-memory records across iterations. This task did not modify those tests; focused repeat/race and full single-run gates are GREEN.

## Second Independent Review Fix Evidence — 2026-08-29

- Tooltip positioning RED: the original table tooltip classes were `bottom-full right-0 mb-2`, so the new scrollport-safe regression failed. The first candidate `right-full top-0 mr-2` fixed the first-row direction but did not establish the last-row boundary; a second focused RED required `right-full bottom-0 mr-2` and rejected both `top-0` and `bottom-full`. The final focused test is GREEN.
- Real-browser geometry GREEN: Node 22 Playwright Chromium passed zh/en cases for both the first and last table rows under keyboard focus and hover. Each case requires `aria-describedby` / `role=tooltip`, verifies `overflow-x:auto` with actual horizontal overflow, and bounds all four tooltip edges inside the scrollport rectangle. Grid positioning was not changed and no Tooltip primitive was added.
- Evidence correction: the parent task JSON change is the intentional reciprocal parent/child link for this task, not an unrelated pre-existing edit; its EOF newline was restored.

## Final Frozen-Code Verification — 2026-08-29

- Main-session Go 1.26.6 focused runtime/repository selectors passed both normal and race execution at `-count=10`, covering CatalogWorker scheduling/lifecycle, Connect wake boundaries, requester wiring, and production composition.
- Main-session Node 22 focused Vitest passed `5` files / `72` tests. The focused Playwright Chromium gate passed `2` zh/en cases and verified first/last-row tooltip geometry under keyboard focus and hover.
- Node 22 `env -u NODE_ENV npm run check` passed with typecheck, lint, `191` files / `1770` tests with coverage, and the production Vite build.
- The final locked Go 1.26.6 + Node 22 root `make check` passed with `golangci-lint` zero issues, all backend tests, the full frontend coverage suite, and both production builds.
- The shared code-spec update satisfies the seven-section cross-layer contract required by `trellis-update-spec`; task validation, manifest/scope/privacy checks, and `git diff --check` are rerun after this evidence update before commit.
- The package-wide runtime `-count=10` sibling-fixture limitation recorded above remains the only non-GREEN planned command. It is outside this task's manifest and does not affect focused repeat/race or the full single-run project gate, so AC11 remains deliberately unchecked rather than overstated.

## PR CI Repair Evidence — 2026-08-29

- PR #480's first `Backend Test & Build` job (`33255408469` / `99108111157`) failed only `TestStepUpProofValidationBindsSessionAndExactIssuedLifetime`: its arbitrary last-character replacement sometimes changed only unused Base64URL padding bits, so the permissive JWT decoder recovered the original signature bytes and correctly verified them. The same failure reproduced on unchanged `main` and about ten times per 100 focused iterations; none of the original task files touched the auth/Step-up boundary.
- Deterministic RED: `TestJWTManagerRejectsNonCanonicalBase64URLSignature` constructs a different signature text whose decoded 32 bytes exactly equal the canonical signature, then observed that `ParseToken` accepted it (`非规范 Base64URL 签名被接受`). This proves a real fail-closed parser gap rather than relying on the probabilistic handler fixture.
- Minimal GREEN: the shared JWT parser now uses the existing library's `jwt.WithStrictDecoding()` option. Valid-token generation, claims, TTL, revocation, routes, schemas and response contracts are unchanged; non-canonical Base64URL encodings are rejected. The new regression passed `-count=100`, the original failing Step-up test passed `-count=100`, and both passed under race at `-count=10`.
- Package and full gates after the production change are GREEN under locked Go 1.26.6 and Node 22: `internal/auth` plus `internal/api/handlers`, `golangci-lint` with zero issues, all backend tests, the frontend `191` files / `1770` tests coverage suite, backend build, and Vite production build. The first full-gate invocation did not enter analysis because its restricted PATH omitted the installed linter; adding the verified `/home/murray/.local/bin/golangci-lint` path produced the complete successful run.
- `.trellis/spec/backend/error-handling.md` now records canonical JWT segment encoding and the deterministic byte-equivalent negative test. `design.md` authorizes exactly `internal/auth/jwt.go` and `jwt_test.go` as the repository-required same-branch CI repair; no unrelated handler test or public contract was changed.
