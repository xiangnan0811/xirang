# Phase 1 implementation evidence

Date: 2026-08-27

Scope: Phase 1 only — file-source projection and backend HTTP contract. No
frontend, preview/content, migration, deployment, commit, or release work is
included here.

## Repository-first insufficiency

The released selector in
`web/src/features/backup-assets/use-backup-assets-state.ts:374-424` makes one
`listBackupRepositories(..., { limit: 100 })` request, stores but does not consume
its `nextCursor`, then makes one
`listRecoveryPoints(selectedRepositoryId, { limit: 100, sort:
"captured_desc" })` request only after a repository is selected. The API wrappers
confirm the two repository-scoped routes in
`web/src/lib/api/backup-repositories-api.ts:496-509` and
`web/src/lib/api/recovery-points-api.ts:683-699`.

Therefore the released flow cannot construct an authoritative node -> Backup Set
-> version projection: repositories after page one are absent, points after the
selected repository's first page are absent, and discovering all points would
require at least one point-list request per repository (an N+1 walk). Treating
either partial page as complete could incorrectly render a valid node or Backup
Set as empty. The three dedicated projection routes remove that dependency.

## TDD transitions

All commands ran from `backend/` unless noted.

1. Task grouping across repository rotation
   - RED: `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceProjectionGroupsOneTaskAcrossRepositories$' -count=1`
   - Expected failure: undefined `ListFileSourceBackupSets`,
     `FileSourcePageRequest`, `FileSourceLineageTask`, and
     `ListFileSourceVersions`.
   - GREEN: the same command returned `ok` after the read-only projection was
     added. The fixture models the database's one-active-link rule by retaining
     an unlinked historical task/repository link before linking the task to the
     second repository.

2. Signed paging
   - RED/GREEN commands:
     - `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceSetCursorPaginatesWithoutDuplicates$' -count=1`
     - `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceNodeCursorPaginatesWithoutDuplicates$' -count=1`
     - `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceVersionCursorPaginatesWithoutDuplicates$' -count=1`
   - Expected REDs: each first page was truncated without a `next_cursor`.
   - GREEN: each endpoint now emits and resumes a signed, endpoint/resource,
     user/role, ordering, and projection-digest-bound cursor without duplicates.

3. Stale projection
   - RED: `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceCursorFailsClosedWhenProjectionChanges$' -count=1`
   - Expected failure: a changed projection resumed as an empty successful page.
   - GREEN: the same command returned `ok` after signed projection digests made
     membership/order changes return `ErrStaleCursor`. The independent review
     below extends this binding to every visible sanitized DTO fact.

4. Invalid retained time
   - RED: `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceProjectionRejectsZeroRetainedTimestamp$' -count=1`
   - Expected failure: corrupt zero `captured_at` was normalized and returned.
   - GREEN: the same command returned `ok` after optional retained timestamps
     were validated as non-zero before projection.

5. HTTP handlers
   - RED/GREEN commands:
     - `go test ./internal/api/handlers -run '^TestBackupFileSourceHandlerListsNodesWithStandardEnvelope$' -count=1`
     - `go test ./internal/api/handlers -run '^TestBackupFileSourceHandlerListsNodeBackupSets$' -count=1`
     - `go test ./internal/api/handlers -run '^TestBackupFileSourceHandlerListsBackupSetVersions$' -count=1`
   - Expected REDs: missing constructor, set handler, and version handler,
     respectively.
   - GREEN: all three use strict path/query parsing, current authorization scope,
     standard response helpers, and safe error/audit mapping.

6. Full-router RBAC
   - RED: `go test ./internal/api -run '^TestCatalogRoutesRequireAssetListPermissionBeforeFeatureGate$' -count=1`
   - Expected failure: all three new routes returned 404.
   - GREEN: the same command returned `ok`; unauthenticated requests stop at 401,
     Viewer/unknown at 403, and Admin/Operator reach the fail-closed feature gate
     under `backup_assets:list`.

## Authority and privacy evidence

- `TestBackupFileSourceProjectionAppliesCurrentOwnershipByRole` proves current
  node ownership for Operator, imported/taskless exclusion for Operator, Admin
  visibility, and Viewer/unknown/invalid-scope denial.
- `TestBackupFileSourceProjectionKeepsTaskAndImportedLineagesIsolatedAndSafe`
  proves node+task grouping, cross-task non-merge, repository-scoped taskless
  non-merge, closed list-only capabilities, and serialized response canaries for
  nodes, sets, and versions.
- `TestBackupFileSourceProjectionSummarizesPartialCatalogCoverage` proves partial
  is surfaced as partial rather than empty/complete.
- `TestBackupFileSourceCursorRejectsTamperAndAuthorizationReplay` proves invalid
  signatures fail as invalid and an equally authorized different user cannot
  replay another user's cursor.
- `TestBackupFileSourceProjectionFailsClosedAtCandidateLimit` proves 2,001
  candidates fail with `ErrOwnershipProjectionLimit` and no partial page.
- `TestBackupFileSourceProjectionRejectsUnknownClosedEnums` and
  `TestBackupFileSourceProjectionRejectsZeroRetainedTimestamp` prove corrupt
  closed-enum/time controls fail closed.
- `TestBackupFileSourceProjectionHasNoProviderCommandDependency` and
  `TestBackupFileSourceHandlerHasNoProviderCommandDependency` are static
  capability canaries: neither boundary imports Provider/runner/SSH/exec code or
  references Provider locator fields. The projection performs DB/Catalog reads
  only. No Provider-capable dependency exists to call.
- Version DTOs expose only opaque recovery-point/repository IDs, optional task ID,
  retained/lifecycle/Catalog/availability summaries, bounded counts/bytes, and
  `list=true`, `preview=false`, `download=false`. Serialized canaries reject raw
  locator, lineage, path, content, credential, repository label, and source
  snapshot values.

## Swagger and schema boundary

`make swag-init` first failed because the local `swag` executable was absent
(exit 127). Regeneration then used the repository-pinned dependency without
changing `go.mod`:

`go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/server/main.go -o internal/api/docs --parseDependency`

Tracked `backend/internal/api/docs/docs.go` contains all three new paths and DTO
definitions. No file under `backend/internal/database/` or a migration directory
was added or modified; Phase 1 derives Backup Set identity from the existing
entry-identity key domain and persists no new state.

## Final Phase 1 gates

- `go test ./internal/backupasset/catalog -count=1` — GREEN (`ok`, 6.585s).
- `go test ./internal/api/handlers -count=1` — GREEN (`ok`, 5.603s).
- `go test ./internal/api -count=1` — GREEN (`ok`, 0.333s).
- `go test ./internal/backupasset/runtime -count=1` — GREEN (`ok`, 10.826s).
- `go test ./internal/backupasset/catalog ./internal/api/handlers -run 'BackupFileSource|FileSource|BackupSet' -count=1` — GREEN.
- `go test ./internal/api -run 'BackupFileSource|FileSource|CatalogRoutesRequireAssetListPermission' -count=1` — GREEN.
- `gofmt` ran on every touched Go source/test file.
- `git diff --check` — GREEN.

## Independent Phase 1 quality review

The reviewer re-read the complete Phase 1 task artifacts and applicable
backend/API/guides specs, inspected the current worktree rather than relying on
the implementation handoff, and stopped at the backend HTTP/projection boundary.

### Findings fixed

1. **Important — task history followed the task's current node.** A task moved
   from node A to node B collapsed both recovery points into node B's set, rather
   than using the exact producing node recorded on each point. RED:
   `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceProjectionKeepsTaskHistoryIsolatedByExactProducingNode$' -count=1`
   returned one current-node set. The projection now keys task-backed sets by
   `ProducingNodeIDSnapshot + ProducingTaskID`; imported/taskless points remain
   keyed by producing-node snapshot plus repository lineage. Production-shaped
   fixtures preserve the separate current-ownership tests. GREEN is included in
   the focused and full Catalog gates below.

2. **Important — stale cursors covered IDs/order but not visible facts.** RED:
   `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceCursorFailsClosedWhenVisibleFactsChange$' -count=1`
   showed node, set, and version continuations succeeding after visible DTO facts
   changed. The signed projection digest now hashes the endpoint kind and the
   canonical JSON of the complete sanitized, deterministically ordered DTO slice;
   all three cases return `ErrStaleCursor`.

3. **Important — internal projection/service state was exposed as HTTP 400.**
   RED:
   `go test ./internal/api/handlers -run '^TestBackupFileSourceHandlerTreatsInvalidServiceStateAsInternalAndDoesNotAuditCursor$' -count=1`
   reproduced a 400 from a typed-nil Catalog service. The file-source handler now
   maps invalid service state and invalid internal projection contracts to the
   standard 500/internal-error response, does not echo or audit the cursor, and
   advertises 500 for all three Swagger operations.

4. **Minor — the tamper test itself was probabilistic.** The repeated Catalog
   gate isolated `TestBackupFileSourceCursorRejectsTamperAndAuthorizationReplay`
   with `tampered cursor error=<nil>`: replacing the final raw-base64url character
   could leave the token unchanged or alter only ignored padding bits. The test
   now changes the first signature character deterministically;
   `-count=50` passed.

5. **Minor — the backend API index omitted the new routes.** The injected
   documentation-freshness check warned for `router.go`. The three authenticated
   `backup_assets:list` file-source routes are now documented in
   `backend/README_backend.md`; the check is GREEN.

Additional reviewer coverage proves full-count/page semantics, resource and
endpoint replay rejection, expiry, null retained timestamps ordered last, and
visible-fact stale detection. Existing tests continue to cover Admin/Operator
ownership, Viewer/unknown/invalid-scope denial, unauthenticated router denial,
closed enums/IDs/times, candidate limits, disabled/unavailable behavior, privacy
canaries, and the absence of Provider-capable dependencies.

### Final reviewer verification

All Go commands used `GOTOOLCHAIN=go1.26.6` unless noted.

- `gofmt -w` on the four reviewer-touched Go source/test files — GREEN.
- `go test ./internal/backupasset/catalog -run 'BackupFileSource' -count=1` —
  GREEN (`ok`, 2.256s).
- `go test ./internal/backupasset/catalog -run '^TestBackupFileSourceCursorRejectsTamperAndAuthorizationReplay$' -count=50` —
  GREEN (`ok`, 6.011s).
- `go test ./internal/backupasset/catalog -count=3` — GREEN (`ok`, 21.489s);
  the reported transient full-Catalog failure did not reproduce after the
  deterministic test correction.
- `go test -race ./internal/backupasset/catalog ./internal/api/handlers -run 'BackupFileSource|FileSource|BackupSet' -count=1` —
  GREEN (Catalog 4.948s; handlers 1.577s).
- `go test ./internal/backupasset/catalog ./internal/api/handlers ./internal/api ./internal/backupasset/runtime -count=1` —
  GREEN (7.554s, 5.720s, 0.337s, and 10.917s respectively).
- `go test ./...` — GREEN across the complete backend; the final confirmation
  reported every package `ok`/`[no test files]`.
- `golangci-lint run ./...` — GREEN (`0 issues.`); `go vet` on the four affected
  packages and `go build ./...` both exited 0.
- `go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/server/main.go -o internal/api/docs --parseDependency` —
  exited 0; generated `docs.go` contains all three routes and their 500 mapping.
- `DOC_FRESHNESS_CHANGED_FILES=... bash scripts/check-doc-freshness.sh` — GREEN.
- `git diff --check` — GREEN. Status inspection found no frontend or migration
  changes; no Provider read/probe/content dependency was introduced.

## Phase 2 — Typed frontend projection and canonical Files shell

### RED / GREEN evidence

- API boundary RED: `backup-file-sources-api.test.ts` failed because the module
  did not exist. GREEN: 11 tests prove complete node/set/version mapping,
  atomic rejection of malformed IDs/enums/times/partial permissions/cursors,
  privacy-field dropping, central transport, AbortSignal propagation, and safe
  cursor paging.
- Projection RED: `backup-file-source-selection.test.ts` failed because the pure
  projection did not exist. GREEN: 5 tests cover the implicit sole set,
  multiple visible sets, imported task-less isolation, deterministic version
  order, blocked/partial states, mismatch non-selection, and primary labels.
- Route RED: the focused route suite failed all 3 tests for unknown nodeId /
  backupSetId, uncleared descendants, and missing reconciliation. GREEN: the
  new suite plus the existing route suite pass 48 tests and preserve legacy
  repository/task/recoveryPoint/entry links while clearing mismatches only.
- Canonical navigation RED: the page tests observed the old Overview redirect,
  Overview/Data/Recovery order, and Data label. GREEN: `/app/backups` replaces
  to `/app/backups/data`; tabs are Files, Overview, Recovery; the existing nav
  registry test still proves one first-level Backups entry.
- Shell interaction RED: focused split-pane, source-control, source-hook,
  breadcrumb, and workspace tests first failed on missing modules/roles. GREEN:
  they prove 42/58 initial layout, 20rem/30rem clamping, pointer and 2% keyboard
  adjustment, separator metadata/focus, exact ratio and trigger-focus restore,
  measured sequential fallback, mobile Back restoration delegation, implicit
  sole-set controls, exact version context, optional support context, and a
  navigable breadcrumb without rendering a raw path.
- Full-gate accessibility RED: the first complete test run found 2 stale
  expectations in `backups-page.a11y.test.tsx` that treated the directory tree
  and offline repository status as always-visible primary content. GREEN: the
  tests now open the optional asset-context dialog, verify its tree/offline
  semantics and portal accessibility, then verify Escape restores trigger focus;
  the focused file passes 13 tests.

### Phase 2 verification

All npm commands used
`PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:$PATH env -u NODE_ENV`.

- Focused affected Vitest gate (11 files) — GREEN: 11 files, 140 tests.
- `npm run typecheck` — GREEN (`tsc -b --noEmit`, exit 0).
- `npm run lint` — GREEN (`eslint .`, exit 0).
- `npm run build` — GREEN (`tsc -b && vite build`; 3230 modules transformed).
- `npm run check` — GREEN: typecheck and lint exited 0; 190 Vitest files and
  1,581 tests passed; aggregate V8 coverage was statements 71.72%, branches
  65.39%, functions 68.44%, and lines 74.50%; the production build transformed
  3,230 modules and exited 0. No configured coverage threshold failed.
- `git diff --check` — GREEN.

Planned-file equivalents were used where repository evidence required them:
the nav registry remains `web/src/components/layout/navigation.ts`, route state
remains `backup-assets-route-state.ts`, and API client composition remains the
lazy boundary in `web/src/lib/api/client.ts`. Split ratio/focused state is held
only in component memory; no route, localStorage, sessionStorage, analytics, or
logging writer receives presentation state, paths, content, tickets, proofs, or
tokens. Existing manual `onLoadPreview` behavior remains unchanged for Phase 4.

### Phase 2 independent quality review (2026-08-27)

- **Important — cursor pages were unreachable from the Files shell.** The hook
  exposed node/set/version continuation callbacks, but the page and controls did
  not consume them, so the first 100 records could appear complete. The Files
  shell now exposes Chinese-primary, minimum-44px continuation controls for all
  three collections; explicit route reconciliation follows pages only until the
  requested stable ID is found or the cursor is authoritatively exhausted. Sole
  set inference and mismatch clearing now wait for that exhaustion.
- **Important — source transport and projection were not strict enough at the
  boundary.** Mapping now validates calendar-correct UTC `Z` instants without
  losing sub-millisecond ordering, the backend's positive backup-set-count
  invariant, duplicate stable IDs, and requested-node consistency atomically.
  AbortSignals, stale-generation guards, and cross-page duplicate checks cover
  initial and continuation requests without allowing raw response objects or
  private fields into feature state.
- **Important — optional repository context was a hidden primary prerequisite.**
  Repository loading/error gates now apply only to the Repositories support
  view. Files browse/search remains usable when repository lifecycle data is
  loading or unavailable; the existing secure support and lifecycle surfaces
  remain available from their explicit view.
- **Important — split-pane measurement and restoration had edge-case gaps.** The
  pane now waits for an actual container measurement, clamps 42/58 against the
  20rem/30rem tracks after accounting for the separator, cleans pointer capture
  on cancel/loss, keeps keyboard movement at 2%, and avoids mounting/remounting
  preview content while layout is provisional. Focus mode restores its exact
  ratio/trigger, while sequential/mobile inspector flows restore the exact row,
  scroll position, and focus. Test rectangle overrides are reset per test so
  desktop measurements cannot leak into intermediate/mobile assertions.
- **Unfixed contract blocker — legacy deep-link reverse resolution.** Approved
  design section 4 requires existing repository/recoveryPoint/task links to
  resolve their exact authorized node and backup set, but the current Phase 1
  DTOs expose no recoveryPoint-to-backupSet lookup and no producing-task mapping
  on a backup set. `reconcileBackupAssetsSourceRoute` therefore cannot resolve a
  legacy link with no `nodeId`/`backupSetId` without guessing or scanning every
  set's version pages (an N+1). The reviewer intentionally left the opaque legacy
  route state intact and recorded this as an API/design contract gap rather than
  inventing either behavior.
- Final reviewer gate, using Node 22.23.1 with `NODE_ENV` unset: `npm run check`
  — GREEN. Typecheck and ESLint exited 0; all 190 Vitest files / 1,601 tests
  passed; production build transformed 3,230 modules and exited 0. The focused
  split/workspace/page-a11y regression gate also passed 3 files / 65 tests.
- `git diff --check` — GREEN. Automated axe coverage passed within the full test
  gate. No authenticated seeded local backend/browser session was established,
  so this review makes no interactive visual-browser claim.

## Phase 2.5 — Exact legacy recovery-point source resolver

Date: 2026-08-27

Scope: the approved compatibility gap only. This slice adds one metadata-only
resolver and its exact legacy-route consumer. It does not enter Phase 3, change a
schema, read from a Provider, alter Issue/Serve transport, or modify preview
products.

### Contract and TDD transitions

Backend RED (from `backend/`):

`go test ./internal/backupasset/catalog ./internal/api/handlers ./internal/api -run 'ResolveBackupFileSource|BackupFileSourceHandlerResolves|BackupFileSourceHandlerRejectsInvalidInputs|CatalogRoutesRequireAssetListPermission' -count=1`

The RED failed to compile on the missing recovery-point DTO, Catalog method, and
handler method; the full-router test also had no registered resolver route. The
same selector is GREEN after implementation. The Catalog now builds an exact
recoveryPointId reverse map in the same bounded, currently authorized projection
pass used by nodes/sets/versions. Missing, stale, malformed-at-service-boundary,
and unauthorized IDs are indistinguishable not-found results; duplicate identity
state fails closed with `ErrIdentityCollision`.

Frontend RED (from `web/`, Node 22.23.1, `NODE_ENV` unset):

`env -u NODE_ENV PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:/usr/local/bin:/usr/bin:/bin npx vitest run src/lib/api/backup-file-sources-api.test.ts src/features/backup-assets/backup-assets-route-state.file-source.test.ts src/features/backup-assets/use-backup-file-sources.test.tsx`

The RED reported 3 failed files and 19 failed / 25 passed tests because the strict
mapper/API, exact route resolver, and hook orchestration did not exist. The final
same selector is GREEN: 3 files / 51 tests. It proves exact patching, repository,
task, and supplied-coordinate mismatch clearing, 403/404 handling, malformed DTO
blocking, AbortSignal and superseded-result behavior, normal pagination after
resolution, no set/version request before resolution, and zero resolver requests
for an explicit modern node/set route.

The first full frontend integration run exposed two test/runtime issues rather
than a product-contract expansion: the page test API mock lacked the new method,
and a render-local `onRoutePatch` identity in the workspace caused the resolver
effect to abort/restart after its own loading update. The stale runners were
terminated by exact PID and no Vitest/Vite process remained. The hook now reads
the latest callback through a ref while request identity stays keyed only to the
token and recovery point. Regression selector:

`timeout 60s env -u NODE_ENV PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:/usr/local/bin:/usr/bin:/bin npx vitest run src/pages/backups-page.test.tsx -t 'replace-repairs a repository/recovery-point mismatch'`

GREEN: 1 passed / 14 skipped, 1.33s. The final full frontend gate below also
proves there is no unresolved loop.

### Independent Phase 2.5 review

The independent review found one concrete orchestration defect. React StrictMode
effect replay started the same legacy resolver twice. The focused RED
`coalesces StrictMode effect replay into one legacy resolver request` failed with
two calls where one was required. The hook now keeps one keyed in-flight request
for the current token and recovery point, reuses it across replay, and aborts it
in a microtask only when the final subscriber remains gone. A changed token or
recovery point still aborts the stale request immediately. Latest-result guards
and the latest `onRoutePatch` ref prevent superseded results or callback identity
changes from restarting or patching stale state. Focused tests also prove final
unmount cancellation, token transitions, and modern node/set routes making zero
resolver calls.

The backend review found no implementation defect. It added focused coverage for
taskless imported lineage omitting `producing_task_id`, the direct resolver's
2,001-candidate fail-closed boundary, and handler 404/500 plus audit/privacy
mapping for hidden, collision, and unknown internal states. The resolver still
performs exactly one bounded Catalog projection, introduces no schema or Provider
dependency, and performs no per-row query.

Malformed resolver data remains blocked without changing the URL. Only a proven
coordinate mismatch or stable 403/404/stale result may clear descendants; an
invalid response has no authority to infer a replacement or stale hierarchy.

### Authority, privacy, dependency, and route evidence

- The resolver route is
  `GET /api/v1/backup-file-sources/recovery-points/:recoveryPointId/source`,
  behind the existing authenticated `backup_assets:list` router group.
- The response contains only nodeId, backupSetId, recoveryPointId, repositoryId,
  and optional producingTaskId. Catalog, handler, and frontend serialization
  canaries prove Provider locator, raw path, content, credential, token, and proof
  fields are not retained or returned.
- `TestResolveBackupFileSourceRecoveryPointReturnsExactAuthorizedCoordinates`
  cross-checks the returned node/set against the same authorized set and version
  projections. `TestResolveBackupFileSourceRecoveryPointHidesStaleAndUnauthorizedPoints`
  and the duplicate-projection test cover the fail-closed boundary.
- The pre-existing 2,001-candidate projection limit and static no-Provider-command
  dependency tests cover the resolver because it invokes the same projection
  once and adds no service dependency or Provider call.
- `TestCatalogRoutesRequireAssetListPermissionBeforeFeatureGate` includes the new
  route for unauthenticated, Admin, Operator, Viewer, and unknown roles.
- Legacy routes resolve only when recoveryPointId exists and nodeId or backupSetId
  is absent. Any supplied repository/task coordinates are compared exactly.
  Stale or unauthorized resolution clears only incompatible descendants; it
  never scans every set and never guesses a replacement.

Focused backend authority/privacy/dependency selector (from `backend/`):

`go test ./internal/backupasset/catalog ./internal/api/handlers ./internal/api -run 'ResolveBackupFileSource|BackupFileSource.*(Provider|Safe|Privacy)|BackupFileSourceHandlerResolves|BackupFileSourceHandlerRejectsInvalidInputs|CatalogRoutesRequireAssetListPermission' -count=1`

GREEN: Catalog 0.599s, handlers 0.070s, API 0.070s.

### Swagger and documentation freshness

`make swag-init` could not run because the workstation has no `swag` binary
(exit 127). The repository-pinned equivalent succeeded without changing
`go.mod`:

`go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/server/main.go -o internal/api/docs --parseDependency`

Generated `backend/internal/api/docs/docs.go` contains the exact resolver path and
`FileSourceRecoveryPointDTO`; `backend/README_backend.md` documents the same
authenticated route. The focused freshness command was GREEN:

`DOC_FRESHNESS_CHANGED_FILES=$'backend/README_backend.md\nbackend/internal/api/router.go\nbackend/internal/api/handlers/backup_file_source_handler.go\nbackend/internal/api/docs/docs.go\nweb/src/lib/api/backup-file-sources-api.ts\nweb/src/features/backup-assets/use-backup-file-sources.ts\nweb/src/features/backup-assets/backup-assets-route-state.ts' bash scripts/check-doc-freshness.sh`

No migration file was added or modified.

### Final Phase 2.5 gates

- Backend focused RED selector rerun — GREEN for Catalog, handlers, and full
  router API.
- `go test ./internal/backupasset/catalog ./internal/api/handlers ./internal/api -count=1`
  — GREEN (9.132s, 6.090s, 0.369s).
- `go test -race ./internal/backupasset/catalog ./internal/api/handlers ./internal/api -run 'BackupFileSource|CatalogRoutesRequireAssetListPermission' -count=1`
  — GREEN (8.351s, 1.657s, 1.633s).
- Focused frontend resolver suite — GREEN: 3 files / 51 tests, 1.71s.
- Node 22 `npm run check` — GREEN: typecheck and ESLint exited 0; 190
  Vitest files / 1,628 tests passed; V8 coverage was statements 72.02%, branches
  65.83%, functions 68.79%, and lines 74.79%; the production build transformed
  3,230 modules and exited 0.
- `go test ./...` — GREEN across the complete backend.
- Focused documentation freshness — GREEN.
- `git diff --check` — GREEN.

Phase 2.5 is frozen at this boundary. Phase 3 preview-intent work has not started.

## Phase 3 — Server preview intent and exact safe resolution

Date: 2026-08-27

Scope: Phase 3 only. This slice adds the closed server-side preview-selection
intent, faithful plain-text product, exact broker resolution, handler mapping,
generated Swagger, and backend tests. It does not enter the frontend direct-preview
state machine, add a schema migration, change Export/Recovery/Processing, deploy,
or perform production acceptance.

### TDD transitions

All Go commands ran from `backend/` unless noted.

1. Strict request union and compatibility
   - Initial REDs failed to compile on missing `PreviewIntent`,
     `PreviewIntentSafePreviewV1`, `RendererPlainText`, `ProfileTextV2`, and the
     required descriptor truncation field.
   - Handler REDs then showed the new intent returning 400 instead of reaching the
     service, renderer errors missing the closed 422 product, and explicit JSON
     `null` renderer/intent fields being accepted as absent.
   - GREEN: the presence-aware strict payload accepts exactly
     preview+safe_preview_v1, an existing exact preview pair, or the existing exact
     attachment/original_v1 download shape. Unknown, mixed, missing, null, extra,
     and download-auto shapes fail closed. Exact preview and download callers
     remain compatible.

2. Readable text and fail-closed encoding boundaries
   - Renderer REDs covered generic MIME UTF-8/UTF-16 configuration, JSON, YAML,
     TOML, logs, and source text; angle brackets, ampersands, quotes, Unicode,
     tabs, and CRLF fidelity; zero length; control/binary bytes; and truncation.
   - Two high-risk negative REDs reproduced arbitrary invalid/overlong UTF-8
     suffixes being mistaken for an incomplete terminal rune and isolated UTF-16
     surrogates being replaced rather than rejected.
   - GREEN: plain_text/text_v2 preserves validated decoded characters and line
     endings without HTML escaping or hex. Only a syntactically incomplete UTF-8
     terminal sequence or an incomplete UTF-16 boundary may be trimmed; arbitrary
     invalid encoding, isolated surrogates, and disallowed controls resolve to
     metadata_hex/hex_v1. Zero-length content is a non-truncated empty text plan.

3. Native selection and active content
   - REDs covered signature-proven raster, PDF, WAV/FLAC/ID3/Ogg audio, MP4/WebM
     video, active HTML/XML/SVG, MIME confusion, malformed provider MIME,
     raster/PDF/media limits, and missing Range capability. Generic-MIME ID3
     initially fell through to hex.
   - GREEN: supported native signatures run through existing MIME/signature,
     active-content, size/pixel, and final `Prepare` policy. ID3 resolves to the
     existing native audio product. Active markup is inert text/plain. A known
     native signature never downgrades to hex when a capability or validation
     requirement fails.

4. Broker order, cancellation, step-up, and persistence
   - The broker RED initially wrapped a canceled bounded prefix read as a generic
     source failure. GREEN propagates context cancellation without a grant.
   - Safe preview now authorizes the exact AssetRef, acquires the existing lease,
     performs one bounded sequential prefix read, classifies sensitivity,
     resolves an exact product, validates native Range and final `Prepare`, and
     only then persists a grant. No derived OCR/archive/text extraction is
     auto-started.
   - Tests prove independent Admin secret/unknown step-up, exact same-intent retry,
     Operator fail-closed behavior, resolved-only success grants/audits/Serve,
     intent-only pre-resolution failure audit, truncation propagation, and Serve
     rejection of a tampered intent-as-renderer grant.

5. HTTP error and descriptor contract
   - `ErrRendererUnsupported` and `ErrMIMEConfusion` map to HTTP 422 with exact,
     parameter-free `preview_renderer_unsupported`; existing capability,
     transport, rate, and internal mappings are unchanged.
   - Ticket JSON and generated Swagger require `truncated`; safe intent responses
     contain only the resolved exact renderer/profile. The same full-router test
     proves the trusted private-network scheme policy remains in force.

### Focused and full verification

- Focused content/handler/router package gate — GREEN:
  `go test ./internal/backupasset/content ./internal/api/handlers ./internal/api -count=1`
  (1.744s, 5.479s, 0.317s).
- Repeated focused selector — GREEN after making two SQLite test fixtures
  repeat-safe with per-test temporary databases:
  `go test ./internal/backupasset/content ./internal/api/handlers ./internal/api -run 'Test(RendererSafePreview|BrokerSafePreview|BrokerServeRejectsUnresolvedPreviewIntent|DeliveryProduct|BackupContentIssue|ValidIssuedBackupContentTicket|BackupContentRoutes|RouterInjectsTrustedProxySchemePolicyIntoBackupContent)' -count=3`
  (0.152s, 0.063s, 0.077s). The preceding failure was a shared in-memory SQLite
  username collision between repetitions, not a product assertion.
- The same focused selector with `-race -count=1` — GREEN (1.667s, 1.583s,
  1.607s), with no race report.
- `go test ./... -count=1` — GREEN across the complete backend.
- `go build ./...` — GREEN.
- `go vet ./...` — GREEN.
- The workstation's default Go 1.27 export data made the installed
  golangci-lint v2.11.4 binary (built with Go 1.26.4) panic before analysis.
  The module/CI-pinned command
  `GOTOOLCHAIN=go1.26.6 golangci-lint run ./...` completed GREEN with
  `0 issues.` No repository configuration or dependency was changed.

### Swagger, documentation, privacy, and scope gates

- The pinned generation command
  `go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/server/main.go -o internal/api/docs --parseDependency`
  exited 0. A second run produced the identical tracked `docs.go` SHA-256
  `8f78dc7a67fdf6f4663cfb1b9659a4e36b1c9e4005d8e5e0852a1e05f1c764be`.
  The generated contract contains safe_preview_v1, plain_text/text_v2, required
  truncation, and the 422 response.
- Focused `DOC_FRESHNESS_CHANGED_FILES=... bash scripts/check-doc-freshness.sh`
  — GREEN; `bash scripts/check-doc-freshness.test.sh` — GREEN.
- Focused diff scans found no newly serialized Provider locator, raw path,
  content-byte, command/output, credential, token/Cookie/TOTP/proof, or production
  asset-identity fields. Broker audit canaries prove pre-resolution failures
  contain only the closed intent and safe failure code; success contains only the
  exact resolved product.
- `git diff --name-only -- backend/internal/database backend/internal/model` and
  the corresponding Export/Recovery/Processing scan were empty. No migration or
  out-of-scope subsystem change was introduced by Phase 3.
- `git diff --check` — GREEN.

Phase 3 is frozen at this boundary. Phase 4 frontend direct-preview work has not
started in this slice.

## Phase 3 independent quality review — 2026-08-27

This reviewer pass re-opened only the server-side safe-preview intent/resolution
boundary. The following corrections and extra evidence supersede the narrower
claims above where they differ; no Phase 4 or unrelated subsystem was changed by
this pass.

### Additional RED-to-GREEN findings

1. **Secret/unknown step-up precedence**
   - RED: a safe-preview request without proof could reach product selection
     first, so a secret native asset missing Range returned a capability error
     and a secret MIME-confused asset returned the renderer-unsupported result
     before `ErrSecretRevealRequired`. That leaked product-resolution facts
     across the existing step-up boundary.
   - GREEN: after the single bounded prefix read and classification, secret or
     unknown safe-preview requests without proof stop at
     `ErrSecretRevealRequired` before selection, Range validation, or `Prepare`.
     The exact legacy preview/download paths retain their prior ordering.
     `TestBrokerSafePreviewRequiresStepUpBeforeResolvedProductErrors` asserts
     both cases, no grant, and no private product facts in failure audit.

2. **Ogg and ID3 signature ambiguity**
   - RED: the provider MIME alone could label an Ogg prefix as audio, generic-MIME
     Opus/Theora could miss native rendering, and arbitrary valid text beginning
     with `ID3` could be treated as native audio.
   - GREEN: Ogg selection parses the first BOS page and identifies the codec
     packet (Opus/Vorbis/Speex/Ogg-FLAC audio or Theora video); malformed or
     unknown Ogg remains unsupported and never falls back to hex. Provider MIME
     must remain compatible with the signature-selected product. ID3 now requires
     a structurally valid ten-byte ID3v2 header with supported version and
     synchsafe size bytes. The new renderer tests cover generic MIME, conflicting
     MIME, ambiguous codec, and text beginning with `ID3`.

3. **Required truncation fact and end-to-end equality**
   - RED: generated Swagger exposed `truncated` but did not list it as required.
   - GREEN: `TicketDescriptor.Truncated` carries the required binding annotation;
     the generated schema test requires the boolean field. A broker-to-Serve test
     additionally proves the same exact truncation value in the descriptor,
     persisted grant, and served response, including a UTF-8 boundary-truncated
     plain-text case.

### Order, read-count, and non-persistence audit

- The safe path is now: authorize the exact asset and lease, open the
  representation source once, perform one bounded `io.ReadFull`, classify,
  enforce secret/unknown step-up, select the exact renderer, validate Range and
  product capability, run final `Prepare`/`ValidateDeliveryProduct`, then grant.
- Classification reads the in-memory prefix; selection and preparation receive
  that same prefix. No second source open/read was introduced. Existing source
  request/open counters remain GREEN.
- Safe intent remains absent from grants, cacheable resolved products, Serve,
  and success audit. Cancellation and every checked failure exit without a
  grant; pre-resolution audit contains only the closed intent and safe failure
  code.

### Independent verification

- `GOTOOLCHAIN=go1.26.6 go test ./internal/backupasset/content ./internal/api/handlers ./internal/api -run 'Test(RendererSafePreview|BrokerSafePreview|BrokerServeRejectsUnresolvedPreviewIntent|DeliveryProduct|BackupContentIssue|BackupContentSwagger|ValidIssuedBackupContentTicket|BackupContentRoutes|RouterInjectsTrustedProxySchemePolicyIntoBackupContent)' -count=3`
  — GREEN.
- The same selector with `-race -count=1` — GREEN, with no race report.
- `GOTOOLCHAIN=go1.26.6 go test ./... -count=1` — GREEN across the complete
  backend.
- `GOTOOLCHAIN=go1.26.6 go build ./...` — GREEN.
- `GOTOOLCHAIN=go1.26.6 go vet ./...` — GREEN.
- `GOTOOLCHAIN=go1.26.6 golangci-lint run ./...` — GREEN (`0 issues.`).
- A further pinned Swagger generation completed successfully. The tracked
  `internal/api/docs/docs.go` SHA-256 was identical before and after:
  `0473c700a92f8d7963241e6d781fb201ccf41b9975ba8d8979a122e2e39b8fa9`.
- Focused doc freshness and its self-test — GREEN. `git diff --check` — GREEN.
  `git diff --name-only -- backend/internal/database backend/internal/model`
  remained empty; no migration or model persistence change was introduced.

Phase 3 server safe-preview intent/resolution is frozen after this independent
review. No commit, publish, deployment, or production action was performed.

## Phase 4 — Frontend direct-preview state machine

Date: 2026-08-27

Scope: Phase 4 only. This slice connects the released server intent to the
existing Files workspace, typed API boundary, latest-request owner, and safe
renderers. It does not enter the broader Phase 5 keyboard/focus/responsive polish,
change backend code, redesign Export/Recovery, deploy, or perform production
acceptance.

### TDD transitions and architecture boundary

All web commands used Node 22.23.1 with `NODE_ENV` unset.

1. Typed request union and resolved-product mapper
   - The API REDs rejected the missing `safePreviewV1` discriminant, accepted no
     resolved `plain_text/text_v2` product, and had no required truncation fact.
   - GREEN: ordinary preview encodes only
     `{schema_version:1, action:"preview", preview_intent:"safe_preview_v1"}`;
     exact preview and exact attachment download remain separate union members.
     Safe input accepts only a complete valid resolved exact preview product;
     exact input still requires the exact requested renderer/profile. Content
     type, Range, classification/proof, expiry, opaque same-origin content URL,
     and truncation all fail closed. Private response extras are not projected.
   - `npx vitest run src/lib/api/backup-content-api.test.ts` — GREEN: 34 tests.

2. Genuine pointer UI RED and exact-caller isolation
   - RED:
     `npx vitest run src/pages/backups-page.test.tsx -t 'issues one automatic safe preview'`
     failed because one generic-MIME file click issued zero tickets.
   - GREEN: the same real-page flow issues exactly one safe-preview request with
     no renderer/profile and exposes no Load Preview button. Ordinary workspace
     code cannot call the exact action. The former MIME selector is now named
     `selectBackupAssetExactPreviewProduct` and is reachable only through the
     explicit processing/derived/backcompat action; renewal derives the exact
     product from the current resolved ticket instead of re-running MIME choice.

3. Single owner, cancellation, and synchronous detach
   - The existing `useBackupAssetsState` remains the sole issue owner. A
     generation+exact AssetRef+intent+attempt started key makes StrictMode/effect
     replay issue one first attempt. Every newer file/node/set/version/directory
     key aborts the old signal, clears in-memory proof/content/error association,
     and ignores late completion.
   - A layout-effect RED captured the old ready ticket before passive detach when
     the directory selection changed. GREEN adds an auth+source+directory+entry
     owner key: a mismatched ticket is synchronously exposed as idle, so the old
     native renderer unmounts before passive cancellation or a successor attach.
   - A separate logout RED observed a pending Admin step-up proof issuing a second
     request with the old token after logout. GREEN binds pending proof resolution
     to both selection generation and the current auth/source owner key; the stale
     proof is dropped and no second request occurs.

4. Retry, step-up, and closed errors
   - Admin safe preview prompts once and performs one same-intent retry; Operator
     never prompts. Rejected proof and token/role changes fail closed. Proof,
     token, ticket, and content URL remain in memory only.
   - Manual Retry exists only for the current retryable typed error/blocked state
     and increments the attempt key. Automatic expiry/media renewal uses the
     current exact AssetRef and resolved renderer/profile. Selection abort is
     silent.
   - The exact 422 RED first mapped `preview_renderer_unsupported` to unknown.
     GREEN recognizes only the parameter-free reason in content-ticket context,
     emits a localized non-retryable state, and never exposes raw provider text.

5. Faithful safe rendering
   - `plain_text/text_v2`, compatibility `escaped_text/text_v1`, and hex use only
     the strict broker URL `/api/v1/asset-content/<opaque-id>` in a sandboxed
     iframe without `srcdoc`. The mapper requires `text/plain; charset=utf-8`, so
     text is not decoded as entities or executed as HTML. Image, sandboxed PDF,
     audio, and video retain the existing safe native components.
   - Old media `src` is removed and native media is unmounted on replacement.
     A localized status explains when a bounded preview is truncated. First-run
     Load and ready-state Refresh controls are absent; typed current Retry remains.

### Focused, accessibility, privacy, and full gates

- Phase 4 focused selector (API/error, hook, preview, workspace, real page, and
  page axe suites) — GREEN: 7 files / 230 tests.
- Hook full suite — GREEN: 68 tests. Preview component — GREEN: 40 tests.
  Workspace — GREEN: 43 tests.
- The first full gate exposed one obsolete 390px export axe step: it clicked a
  file to perform selection-only, but pointer click now correctly opens the mobile
  preview. The test now uses the pre-existing Space bulk-selection behavior and
  names that distinction explicitly. Desktop/intermediate/mobile axe cases are
  GREEN (3 tests).
- This does **not** claim Phase 5 keyboard activation completion. Space currently
  remains the explicit selection-only command and does not start preview. The
  approved Phase 5 Enter/Space activation and nested interactive-row contract
  still requires a deliberate product decision/test update; it must not silently
  turn the current bulk-selection Space gesture into preview activation.
- `npm run check` — GREEN: typecheck and ESLint exited 0; all 190 Vitest files /
  1,660 tests passed; V8 coverage was statements 72.21%, branches 66.07%,
  functions 68.93%, and lines 74.96%; production build transformed 3,230 modules
  and exited 0.
- Focused production-source scan across the changed API/hook/preview/workspace/
  row components found no direct `fetch`, localStorage, sessionStorage, history,
  cookie, console, analytics, or sendBeacon use. Existing raw-source privacy tests
  also passed in the full gate.
- Planned-file equivalents added only where the real interaction owner required
  them: `asset-list.tsx`/`asset-grid.tsx` own pointer activation,
  `backup-assets-error.ts` owns the exact closed 422 mapping,
  `backups-page.test.tsx` owns the genuine page RED, and the existing page axe test
  owns the bulk-selection regression. No backend, migration, deployment,
  production, commit, push, or PR action was performed by Phase 4.
- `git diff --check` — GREEN.

Phase 4 is frozen at this boundary. Phase 5 has not started in this slice.

## Independent Phase 4 quality review (2026-08-27)

The reviewer re-read the task PRD/design/implementation artifacts, Phase 4
evidence, applicable frontend specs, and the actual dirty-worktree diff. The
review did not enter Phase 5, modify backend code, or perform commit/push/PR or
deployment work.

### Findings reproduced RED and fixed GREEN

1. **Runtime exact-preview input was not closed before transport.** A runtime
   `preview + attachment/original_v1` object bypassed the TypeScript union,
   reached `request()`, and was only blocked while mapping the response. The new
   API RED expected pre-transport rejection and observed a resolved blocked
   projection instead. GREEN adds an explicit runtime non-attachment preview
   validator; the request mock remains untouched. The focused API suite passes
   34 tests.
2. **A non-null auth-token switch could issue from the previous token's ready
   entry.** Before the replacement `getBackupAsset` projection resolved, the
   automatic effect reused the old selected entry and issued a second ticket
   under the new token. GREEN binds selected-entry readiness to the exact
   auth/source owner, exposes only loading/idle while re-authorizing, and starts
   preview only after the current projection commits. Late/aborted entry results
   cannot restore the old owner.
3. **Admin proof retry lost its proof after a retryable failure.** The one-time
   step-up retry sent the proof, but `activePreviewRef` retained the original
   proofless safe intent. A later manual Retry therefore dropped the proof.
   GREEN records the proof-bearing active intent and its exact attempt, and
   accepts an asynchronous proof only when generation + AssetRef + intent/product
   + attempt still match. The regression proves one prompt, one proof retry, and
   a later typed retry with the same current proof.
4. **Leaving Preview did not own cancellation, and StrictMode cleanup detached a
   live mount.** A Preview-to-Metadata RED left the request signal active; an
   `AssetPreview` StrictMode RED called `onDetach` during effect replay. GREEN
   includes `inspectorTab` in the content owner, permits automatic safe intent
   only on Preview, aborts/detaches without a hidden attempt, and re-enters with
   exactly one fresh `safePreviewV1:0` attempt. Component cleanup is microtask-
   coalesced so StrictMode replay is ignored while a real unmount detaches once.
5. **Retry/renew could cross a source-owner transition before passive detach.**
   A layout-effect RED changed the node while retaining the same AssetRef and
   observed a second request from the previous failure. GREEN requires the raw
   content owner to equal the current auth/source/tab owner before Retry or
   Renew. Source changes also clear the in-memory reveal proof; the regression
   proves the next safe request is proofless and Admin is prompted again.
6. **The executable frontend spec still described exact-only request bodies.**
   `.trellis/spec/frontend/type-safety.md` now records the discriminated
   `safePreviewV1` request, pre-transport runtime rejection, server-resolved exact
   product, truncation/range coupling, and auth/source proof lifetime. No Phase 5
   checklist was changed.

### Independent verification

- Phase 4 focused API/error/hook/preview/browser/inspector/workspace/page gate —
  GREEN: 8 files / 235 tests.
- The complete Hook file was additionally run three times in parallel after
  replacing a call-order-dependent token mock with a token-identity mock — all
  three runs GREEN: 73 tests each.
- Final Node 22 `npm run check` — GREEN with clean typecheck and ESLint; all 190
  Vitest files / 1,666 tests passed. V8 coverage: statements 72.23%, branches
  66.14%, functions 68.95%, lines 74.99%. Production build transformed 3,230
  modules and exited 0.
- Focused production-source privacy scans found no direct `fetch`, localStorage,
  sessionStorage, history, cookie, console, analytics, sendBeacon, token/proof
  query construction, or body proof serialization. Opaque content URLs occur
  only in the intended sandboxed/native preview sinks and exact attachment link.
- `git diff --check` — GREEN after the review fixes and evidence/spec updates.

Independent Phase 4 review is GREEN. Phase 5 remains unstarted.

## Phase 5 — Responsive, accessibility, and boundary UX

Date: 2026-08-27

Scope: Phase 5 only. This slice completes the approved file-activation,
selection, focus, live-region, responsive, split-pane, and axe contracts. It does
not change backend code, the Phase 4 request coordinator, Export/Recovery
authority, deployment, production acceptance, or Phase 6/7 delivery work.

### RED-to-GREEN interaction semantics

1. **Activation and bulk selection are separate native controls.** The first
   list/grid RED selector failed seven assertions because Space was still a
   selection-only row gesture and the interactive row/card contract could not
   expose an independent checkbox. GREEN uses a native file activation button
   across the non-checkbox row/tile surface and a sibling native checkbox.
   Pointer, Enter, and Space each activate the file exactly once; checkbox Space
   changes only bulk selection and does not bubble into preview. List rows use
   list/listitem semantics; grid rows keep grid/row/gridcell semantics without a
   nested interactive card. Current preview and bulk selection are exposed
   independently through legal `aria-current`, `aria-selected`, and checked
   states. The focused list/grid suite is GREEN: 2 files / 11 tests.
2. **Keyboard and focus remain deterministic.** List and grid activation buttons
   use one roving tab stop plus Arrow/Home/End navigation and visible focus rings.
   Desktop activation leaves focus on the originating browser button rather than
   moving it to the inspector. Mobile and sequential activation focus the exact
   preview heading; Back restores the exact list or grid activation button and
   virtual scroll offset when it still exists, and otherwise falls back to the
   results container. Source/version/directory changes continue through the
   route-owned safe-root/reset tests in the complete feature selector.
3. **Focused reading does not hide keyboard focus.** A late focused RED reproduced
   entry focus remaining on the now-transparent Focused reading button. GREEN
   moves focus to the visible Exit focused reading button, keeps the browser DOM
   and scroll position mounted but hidden from layout/accessibility/tab order,
   and restores the exact 44% adjusted ratio and original trigger focus on exit.
   The exact RED command was
   `npx vitest run src/features/backup-assets/backup-file-split-pane.test.tsx -t 'enters focused reading'`;
   the final split-pane file is GREEN: 10 tests.
4. **Loading and errors are scoped live states.** A focused browser RED could not
   find a loading status. GREEN gives only the active empty browser load a polite,
   busy status while the existing bounded failure alert remains assertive. Source
   loading/empty/partial/blocked/permission states remain distinct status/alert
   products. Preview loading is polite and busy; the current typed failure is an
   alert with Retry only when allowed and never includes the display filename.
   Inspector tab changes retain a scoped polite announcement. The exact browser
   RED command was
   `npx vitest run src/features/backup-assets/asset-browser.test.tsx -t 'announces browser loading'`;
   browser plus split-pane GREEN is 2 files / 17 tests.

### Responsive, boundary, and accessibility coverage

- The split pane starts at 42/58, exposes a vertical value separator, moves in
  two-percentage-point keyboard steps, supports pointer drag/cancel, clamps both
  20rem/30rem panes, and uses measured-container sequential fallback. A simulated
  200% root font size forces the same browser-to-full-preview flow. Remount resets
  to 42 and a storage spy proves no layout write.
- Source selectors, pagination, mobile inspector navigation/tabs, bulk commands,
  sequential Back, and focused-reading controls have 44px mobile targets with the
  compact desktop variants retained. Long Chinese and English source labels stay
  present, source controls keep node/set/version order, and empty, partial,
  blocked, permission-denied, and incomplete cursor-chain states stay distinct.
- Large result sets remain virtualized to bounded list/grid DOM counts. Text
  preview retains one bounded reading viewport before and after load, preserves
  the server truncation notice, and does not regress native/hex render products.
- Existing global stylesheet tests prove reduced-motion transitions are
  effectively disabled and power-saving shortening remains limited to
  no-preference motion. Files integration covers 1440px desktop, 1200px
  intermediate, and 390px mobile behavior.
- Focused axe checks cover list, grid, source controls, desktop Files list/grid,
  mobile full-screen preview, optional dialogs, and explicit-checkbox Export at
  1440/1200/390. The 390px flow no longer relies on the ambiguous Phase 4 Space
  row gesture.

### Privacy, scope, and final verification

- A production-source scan over the Phase 5 owners found zero direct `fetch`,
  localStorage, sessionStorage, history/location writes, console, analytics,
  sendBeacon, cookie, `any`, or `unknown as` occurrences. The remaining
  snake-case comparisons are closed typed domain values. A broader token/ticket
  scan found only the pre-existing in-memory authenticated preview runtime and
  intended opaque content URL sinks; Phase 5 adds no route, log, error, storage,
  analytics, ticket, or proof writer.
- `RecoveryReviewF8` proves a temporary query, explicit checkbox selection,
  display fixture names, ticket path, and proof marker leave localStorage,
  sessionStorage, and history unchanged. The preview error regression separately
  proves the authorized display filename is absent from the alert. Their focused
  selector is GREEN: 2 files / 2 tests (53 unrelated tests skipped).
- Final Phase 5 focused selector, using Node 22.23.1 with `NODE_ENV` unset:
  `npx vitest run src/pages/backups-page.test.tsx src/pages/__tests__/backups-page.a11y.test.tsx src/features/backup-assets src/index-css.test.ts`
  — GREEN: 44 files / 632 tests.
- Final Node 22 `npm run check` — GREEN: typecheck and ESLint exited 0; all 190
  Vitest files / 1,674 tests passed. V8 coverage was statements 72.30%, branches
  66.20%, functions 69.02%, and lines 75.07%; the production build transformed
  3,230 modules and exited 0.
- `git diff --check` — GREEN. Phase 5 made no backend, migration, commit, push,
  PR, deployment, NAS, production, or collector change.

Phase 5 is frozen at this boundary. Real-browser visual contrast/zoom and assistive
technology checks remain appropriate independent/production acceptance evidence;
Phase 6 and Phase 7 have not been started by this slice.

## Independent Phase 5 quality review

This section supersedes the Phase 5 test totals above after an independent
findings-first review and its narrow RED-to-GREEN corrections. It does not advance
the Phase 6 checklist.

### Findings fixed

- Focused reading left its visually transparent trigger in the accessibility tree
  while the visible Exit control owned focus. A focused regression first proved the
  duplicate control; the trigger now uses the native `hidden` state while preserving
  the mounted browser, exact scroll/ratio restoration, and trigger-focus return.
- Result-list roving focus and the route-selected preview were conflated. A new RED
  proved ArrowDown could announce `aria-current` before activation. List and grid now
  receive the route-derived current key separately from their roving active key, so
  focus moves without claiming that a file was opened.
- Primary mobile/coarse-pointer controls were as small as 28--36px, and a large-screen
  breakpoint could shrink controls even on a coarse-pointer device. Browser,
  workspace, preview, split-pane, source, inspector, and bulk controls now reuse the
  pointer-aware `touch-target` contract: 44px by default/coarse pointer and 40px for
  fine-pointer desktop. Fixed-height parent containers were changed to compatible
  minimum heights so the larger targets do not collide, wrap rows, or change the
  42/58 split.
- The retained-version list/grid test data reused React keys and emitted duplicate-key
  warnings during the full suite. The fixtures now use distinct opaque row identities.
- A focused deletion regression now proves that when the exact mobile origin disappears,
  Back restores the captured result scroll and moves focus to the result container.
  The existing fallback behavior required no product-code change.

### Independent verification

- RED selectors failed for the hidden focused-reading trigger, pre-activation
  `aria-current`, undersized touch targets, and incompatible fixed-height containers;
  their focused selectors passed after each minimum correction. The deletion fallback
  regression passed 1/1, and list/grid warning cleanup passed 2 files / 11 tests.
- Node 22.23.1 focused Phase 5 selector — GREEN: 44 files / 636 tests.
- Final Node 22 `npm run check` — GREEN: typecheck and ESLint exited 0; all 190
  Vitest files / 1,678 tests passed. V8 coverage was statements 72.32%
  (13,920/19,247), branches 66.22% (11,818/17,845), functions 69.02%
  (3,460/5,013), and lines 75.09% (12,910/17,192). The production build
  transformed 3,230 modules and completed in 5.54s.
- Latest built CSS order is large-screen minimum-height rule, base/coarse
  `touch-target`, then fine-pointer override. This preserves the 44px coarse-pointer
  target at desktop widths while retaining 40px density for fine pointers.
- Production-source privacy scans found no direct `fetch`, console/telemetry writer,
  TypeScript `any` type, or `unknown as` cast in the reviewed Files surface. The only
  browser-storage owner is the existing bounded preferences module, whose strict
  record contains version, layout, and pane widths only; it does not persist file
  identity, name, path, content, delivery material, proof, or secret data. Existing
  route/API boundaries continue to use opaque identifiers and their privacy tests pass.
- The full suite still reports unrelated pre-existing React/Recharts warnings; the
Phase 5 duplicate-key warning is removed. No backend, Phase 4 state-machine,
Phase 6 checklist, commit, push, PR, deployment, NAS, or collector change was made.

## Phase 6 — Cross-layer verification and self-fix

Date: 2026-08-27

Scope: independent Phase 6 review, focused/full verification, and the smallest
TDD fixes required to make the approved implementation repeatable and fail
closed. This pass did not start Phase 7, commit, publish, deploy, operate the NAS,
or start collectors.

### Findings reproduced and fixed

1. **Repeated backend tests shared SQLite namespaces.** The required three-package
   `-count=3` gate exposed records from an earlier repetition in handler and
   content fixtures. RED was the failing repeated package selector. Shared
   test-only helpers now add an atomic unique suffix to every named in-memory DB;
   all affected fixtures retain their schema and production behavior. The final
   three-package `-count=3` and focused race gates are GREEN.
2. **Legacy exact native/download response mapping was over-constrained.** The
   frontend mapper required `range=single` for every native/exact product and
   attachment response, although the released exact contract legally allows
   `none` or `single`. A focused RED proved a valid legacy response was blocked.
   GREEN requires `single` only for server-selected safe-preview native products;
   renderer/profile/content-type matching remains exact for compatibility calls.
3. **Safe native capability loss became an undifferentiated 503.** Missing
   sequential-read or native Range capability was unwrapped to the sentinel before
   the handler could preserve the closed reason. Content now emits a typed,
   parameter-free error for only `sequential_read_unavailable` and
   `range_unavailable`; the Issue handler maps the closed retryable set to 503 and
   the closed unsupported set to 501. Restore/delete/diff or future codes remain a
   generic detail-free 503.
4. **Parameterized Repository capability data could escape through Content
   Issue.** RED
   `go test ./internal/api/handlers -run '^TestBackupContentIssueCapabilityStatusAndCodeSetAreClosed$' -count=1`
   returned a 503 body containing a canary in `reason.params.capability`. GREEN
   accepts a typed Repository reason at this endpoint only when `params` is empty;
   otherwise it returns the generic 503 with no `data`. The exact test passed
   three repetitions. The reusable restriction is recorded in
   `.trellis/spec/backend/error-handling.md`.
5. **Executable frontend guidance was stale.** The frontend quality and directory
   specs now describe the canonical Files route, exact single-activation intent,
   cancellation/step-up/native Range behavior, and feature ownership. The existing
   type-safety update remains the request/response mapper contract.
6. **Bulk formatting introduced unrelated test noise.** Baseline formatting was
   restored in the silence/snapshot fixtures while retaining only the unique-DB
   substitutions. No unrelated production formatting change remains.

### Final focused and full gates

All web commands used Node 22.23.1 with `NODE_ENV` unset. All Go commands used
`GOTOOLCHAIN=go1.26.6`; golangci-lint was v2.11.4, built with Go 1.26.4.

- Catalog/file-source focused selector — GREEN.
- Content/safe-preview focused selector — GREEN.
- `go test ./internal/backupasset/catalog ./internal/backupasset/content ./internal/api/handlers -count=3`
  — GREEN: 25.306s, 4.896s, and 18.607s.
- `go test -race ./internal/backupasset/catalog ./internal/backupasset/content ./internal/api/handlers -run 'BackupFileSource|PreviewIntent|SafePreview|DirectPreview|ContentTicket' -count=1`
  — GREEN: 6.547s, 1.818s, and 1.641s; no race report.
- Final frontend focused selector over the real page, complete backup-assets
  feature directory, file-source API, and content API — GREEN: 44 files / 674
  tests.
- Final standalone `npm run check` — GREEN: typecheck and ESLint exited 0; 190
  files / 1,679 tests passed; production build transformed 3,230 modules.
- Final post-fix `go test ./... -count=1` — GREEN across the complete backend.
  `go build ./...` and `go vet ./...` exited 0; pinned
  `golangci-lint run ./...` returned `0 issues.`
- Final `PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:$PATH GOTOOLCHAIN=go1.26.6 env -u NODE_ENV make check`
  — exit 0. It repeated backend lint/test/build and frontend lint, 190 files /
  1,679 tests, typecheck, and production build successfully.

### Swagger and documentation freshness

- Pinned `swag` v1.16.6 regeneration exited 0 repeatedly. A final before/after
  regeneration comparison was byte-deterministic:
  - `docs.go`: `712bacd36c9197349d9000e30666e3922fcdef2aaeb20bb342c224f22fe3c16a`
  - `swagger.json`: `f610a6705cde2572d909f2fbb2214082c3898d125f5d7ddb1cb9ccd98ca43a6d`
  - `swagger.yaml`: `1a7d5f571c93e0382c0bac702af2e995535a08f37703547d5cb9f987f349e59b`
- `go test ./internal/api/handlers -run Swagger -count=1` — GREEN.
- `bash scripts/check-doc-freshness.test.sh` — GREEN.
- The script's default dirty-worktree baseline examines `HEAD~1` and emitted two
  warnings, so that output was not treated as a task verdict. Re-running with
  `DOC_FRESHNESS_CHANGED_FILES` derived from the actual current status returned
  `✅ 文档新鲜度检查通过`.

### Privacy, compatibility, and scope audit

- Modified production web files (26 total) contain no direct `fetch`, storage,
  history/location writer, `unknown as`, TypeScript `any`, or content/ticket/proof
  persistence sink. Modified component/page files contain no raw snake_case API
  member access; closed enum strings remain boundary values only.
- Backend production scan found no Provider locator/raw backup path/content-byte/
  credential/command-output response or logging sink. Matches were only intended
  Cookie/proof ownership, bounded hex rendering, cursor tokens, classifier terms,
  and comments. Failure audits retain only the closed safe-preview intent and safe
  failure code before product resolution.
- Positive/negative source and content serialization canary selector — GREEN for
  exact source resolution, ownership isolation, invalid cursor/error privacy,
  MIME confusion, typed capability privacy, private source request/stat fields,
  and no private extras.
- Compatibility selector for exact preview, original download, Archive, Export,
  Recovery result, transport, and Serve paths — GREEN. Frontend tests separately
  prove legacy exact/download `range=none|single` compatibility while safe native
  remains `single` only.
- Production Export, Recovery, node-log, database, and model paths are unchanged;
  `web/package.json`, `web/package-lock.json`, `backend/go.mod`, and
  `backend/go.sum` are unchanged. No migration, dependency, lockfile, or tracked
  build artifact was added. Shared handler test DB substitutions include node-log
  tests but make no product or collector change.
- Pre-evidence tracked diff stat: 100 files, 5,009 insertions, 751 deletions.
  Intended untracked files are the task artifacts plus the new file-source,
  content test-helper/error, and Files feature/API modules. Workspace journal and
  user-owned `.codex/agents/trellis-research.toml` state were preserved.
- Final `git diff --check` — GREEN.

Phase 6 is GREEN and stops here. Phase 7 remains unchecked and unstarted.

## Phase 7 independent Trellis check (2026-08-27)

Scope: the first Phase 7 checklist item only. This was a fresh, independent
review of the complete current diff against every `check.jsonl` entry, followed
by RED-first fixes for each Important finding and fresh affected/full gates. It
did not commit, push, open or merge a PR, deploy, operate the NAS, start a
collector, or claim production acceptance.

### Findings reproduced and fixed

1. **Frontend route documentation contradicted the implemented information
   architecture.** The actual router has one `/app/backups/data` route and
   `backup-assets-route-state.ts` represents the repository view as
   `?view=repositories`; the directory spec called repositories a sibling route.
   That manual spec-to-router check was RED. The spec now records the real query
   view and no route or product code was added.
2. **The frontend type contract over-constrained legacy Range behavior.** The
   spec said every native ticket required `range=single`, contradicting the
   released exact preview/download mapper contract and its compatibility tests.
   The spec now requires `single` only for a native product resolved from
   `safe_preview_v1`; legacy exact native preview and attachment download remain
   closed `none|single` products.
3. **Typed Content capability responses omitted the required empty params
   object.** RED
   `go test ./internal/api/handlers -run '^TestBackupContentIssueMapsTypedContentCapabilityWithoutPrivateEvidence$' -count=1`
   decoded `params` as nil because `CapabilityReason.params` was omitted. The
   handler now uses a dedicated response DTO whose `params` member is required,
   normalizes nil to `{}`, and still emits only the closed
   `sequential_read_unavailable` / `range_unavailable` set at HTTP 501.
   Parameterized, future, restore/delete/diff, and otherwise out-of-scope reasons
   remain a detail-free 503. Handler JSON tests prove exact `{}`, code, status,
   correlation ID, and no private evidence; the frontend error-mapper test proves
   both 501 codes remain available as closed `capabilityCode` values. The Issue
   Swagger annotation already contains 501 and pinned regeneration remained
   byte-deterministic.
4. **The ticket mapper accepted an unresolved response intent echo.** RED
   `npx vitest run src/lib/api/backup-content-api.test.ts -t 'blocks an unresolved safe-preview intent echo'`
   returned `available`. The mapper now rejects own-property presence of
   `preview_intent` before projecting any ticket. The GREEN matrix covers safe,
   legacy exact preview, and attachment download responses with the valid
   literal, empty string, and null; all fail closed. Truly unrelated telemetry
   extras are still discarded rather than expanding the DTO.

No Critical finding was found. No Important finding remains open.

### `check.jsonl` traceable verdicts

1. **Frontend quality guidelines — PASS.** The real page and feature tests cover
   node/set/version loading, automatic single-activation preview, latest-owner
   cancellation, stale-result suppression, typed retry, one Admin step-up retry,
   Operator fail-closed behavior, native/text/hex rendering, truncation, and
   desktop/mobile states. The final frontend-focused gate covered 46 files / 706
   tests; the full gate covered 190 files / 1,682 tests.
2. **Frontend accessibility guidelines — PASS.** Existing focused keyboard,
   focus-restoration, separator, mobile drawer, live-state, and top-level Files
   axe suites ran in both the focused and full gates. No selection semantics were
   changed by the review fixes.
3. **Frontend type safety — PASS after findings 2 and 4.** Ticket input remains a
   discriminated safe-intent/exact-preview/download union. The response mapper
   admits only exact renderer/profile/MIME/range products, requires safe native
   `single`, preserves legacy exact/download `none|single`, rejects every
   unresolved-intent presence, canonicalizes times, and projects no private
   extras.
4. **Frontend backup-content transport — PASS.** All calls still use the central
   request wrapper with AbortSignal and in-memory proof forwarding. Opaque
   same-origin content URLs remain query-free and outside storage/history. The
   strict error mapper now consumes the exact parameter-free 501 envelope and
   never exposes raw server/provider detail.
5. **Backend error handling — PASS after finding 3.** Payload decoding remains
   strict; standard closed envelopes are used. Content capability mapping admits
   only two parameter-free codes at 501, retryable repository codes retain their
   established 503 mapping, and parameterized/future/out-of-scope reasons collapse
   to generic 503 without detail. No raw error, locator, path, content, command
   output, credential, proof, or asset identity reaches the response.
6. **Backend quality guidelines — PASS.** File-source routes retain authentication,
   `backup_assets:list`, role/ownership filtering, bounded projection, and safe
   DTOs; ticket Issue retains `backup_assets:preview`. Unique SQLite naming is
   confined to `_test.go` helpers and changes no production database behavior.
   Fresh lint, vet, repeated tests, race tests, full tests, and builds are GREEN.
7. **Backend backup-content transport — PASS.** Safe intent still travels through
   the existing authorize/classify/step-up/resolve/Prepare/grant/Issue/Serve path.
   Cookie, same-origin, lease, budget, audit, and product validation remain in
   force. Tests reject intent-as-grant, MIME confusion, missing native Range,
   private capability params, query/header authorization, and cross-site Serve;
   exact preview/download and Recovery-result compatibility remain GREEN.
8. **Branch workflow — PASS for the independent-check boundary; delivery remains
   intentionally incomplete.** Work is on dedicated branch
   `codex/backup-file-center-direct-preview` at reviewed base `5ea911338e56`.
   Commit, push, PR, CI, merge, release, post-merge, and production evidence are
   still unchecked and must exist before task completion.
9. **Cross-layer thinking guide — PASS.** The reviewed flow is opaque route state
   -> authorized Catalog source projection -> current AssetRef -> safe intent ->
   exact resolved ticket -> cookie-bound Serve. Node/token/source/directory/file/
   tab/attempt ownership controls cancellation and late-result rejection at each
   async boundary. No UI simplification widened Catalog, RBAC, step-up, grant, or
   transport authority.
10. **Documentation truth guide — PASS for implemented contracts.** README,
    router annotations, generated Swagger, and frontend/backend Trellis specs
    describe the additive file-source and safe-preview contracts. Pinned Swagger
    regeneration was deterministic and both documentation-freshness gates are
    GREEN. PR/release/production documents remain future Phase 7 evidence, not a
    present claim.

### Fresh verification evidence

All web commands used Node 22.23.1 with `NODE_ENV` unset. All Go commands used
`GOTOOLCHAIN=go1.26.6`.

- Backend affected package repetition:
  `go test ./internal/backupasset/catalog ./internal/backupasset/content ./internal/api/handlers -count=3`
  — GREEN: 33.299s, 10.735s, and 24.027s.
- Focused race gate over file-source/content-ticket/direct-preview selectors —
  GREEN: 12.266s, 2.204s, and 1.645s; no race report.
- Frontend real-page/feature/API focused gate — GREEN: 46 files / 706 tests.
- Post-fix API privacy/compatibility gate over file-source, content-ticket, and
  error mappers — GREEN: 3 files / 80 tests.
- Post-fix backend response/authority/privacy canary selector — GREEN across
  Catalog, Content, and handlers. A separate exact-preview/download/Archive/
  Export/Recovery/Serve compatibility selector was GREEN in Content and handlers.
- `go vet ./...` — GREEN.
- Final
  `PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:$PATH GOTOOLCHAIN=go1.26.6 env -u NODE_ENV make check`
  — exit 0: golangci-lint reported `0 issues`; frontend ESLint and typecheck
  passed; all backend packages passed; frontend passed 190 files / 1,682 tests
  (72.33% statements, 66.27% branches, 69.02% functions, 75.10% lines); backend
  and frontend production builds passed (3,230 frontend modules).
- Pinned `swag` v1.16.6 before/after hashes were identical:
  - `docs.go`: `712bacd36c9197349d9000e30666e3922fcdef2aaeb20bb342c224f22fe3c16a`
  - `swagger.json`: `f610a6705cde2572d909f2fbb2214082c3898d125f5d7ddb1cb9ccd98ca43a6d`
  - `swagger.yaml`: `1a7d5f571c93e0382c0bac702af2e995535a08f37703547d5cb9f987f349e59b`
- `bash scripts/check-doc-freshness.test.sh` — GREEN. The actual-current-status
  `DOC_FRESHNESS_CHANGED_FILES` injection returned
  `✅ 文档新鲜度检查通过`.

### Privacy and intended-diff audit

- The 26 modified production web files contain zero direct `fetch`,
  localStorage, sessionStorage, sendBeacon, console writer, untyped
  history/location writer, `unknown as`, or TypeScript `any` occurrence.
  Modified production feature/page owners contain zero raw snake_case member
  access. Browser-state tests prove content URLs, tickets, and proofs are not
  persisted.
- Backend serialization/dependency canaries prove the source projection is
  Catalog/database-only and responses omit Provider locator, raw path/content,
  credential, token/proof, command/output, and private request evidence. Matches
  in the broader production scan were the intended internal cookie/proof/grant,
  classifier, budget, and Provider capability bindings; none is a new response
  or logging sink.
- There is no model, schema, migration, database package, Provider-locator
  package, dependency, lockfile, production node-log, or collector change.
  `node_log_config_handler_test.go` and `node_logs_handler_test.go` contain only
  the shared test-database name substitution. `go.mod`, `go.sum`, `package.json`,
  and `package-lock.json` are unchanged.
- Before this evidence append, the tracked diff was 100 files, 5,080 insertions,
  and 754 deletions; current status contained 28 actual untracked files, all in
  the task/workspace evidence or planned new file-source/content/Files modules.
  No unexpected path was found. The task workspace and user-owned configuration
  were preserved.
- The ignored `backend/xirang-server`, `web/coverage`, and `web/dist` outputs
  created by the fresh gate were removed through the repository `make clean`
  target. No tracked build artifact remains.
- Final `git diff --check` was GREEN after the evidence/checklist update; a
  separate trailing-whitespace scan of the two untracked task files was also
  empty.

### Browser acceptance boundary

The main-agent Vite browser check could only reach the login screen because the
local backend at `127.0.0.1:8080` was not running. No credential was entered,
read, or requested. Therefore this independent check makes no authenticated
Files visual claim and no production claim. Authenticated browser, LAN HTTP,
representative text/binary/native preview, rapid-switch, health/DB/log, and
collector-zero acceptance remain the later explicit Phase 7 steps.

The independent Trellis check is GREEN. Only the first Phase 7 checklist item is
complete; every commit/PR/release/production/collector item remains unchecked.

## Phase 7 — first CI browser-gate repair (2026-08-27)

### Initial ready-PR result and diagnosis

- Ready PR #472 was opened from `codex/backup-file-center-direct-preview` at
  commit `871470e9dfebda8ee842a1ba7fcd9caf505bd955`.
- CI run `33075494788` completed with every backend, PostgreSQL parity, Docker,
  worker runtime/build/scan, PR-title, migration-UTC, and documentation job
  GREEN. The sole failure was Frontend Test & Build job `98528485194`, in the
  backup-assets Playwright matrix on Chromium, Firefox, and WebKit.
- The browser test still asserted the pre-Phase-5 `listbox`/`option` roles,
  double-click activation, and a second `Load preview` click. Those selectors
  contradicted the approved native list/button and single-activation direct
  preview contract; this was a stale gate, not a product rollback signal.

### RED/GREEN repair

- The closed-feature scenario also exposed a genuine boundary: a blocked
  file-source projection still left the searchable workspace visible. A new
  page regression first observed the searchbox and result region (RED).
  `BackupsDataPage` now keeps source controls and their bounded status visible
  while omitting the workspace for `blocked` and `permission_denied` source
  states (GREEN).
- The Playwright fixture now uses the canonical node/set/version route, mocks
  the additive file-source projection, activates the file with one native
  button click, and proves exactly one request body of
  `{schema_version:1, action:"preview", preview_intent:"safe_preview_v1"}`.
  It also proves there is no `Load preview` button and accepts only the resolved
  `plain_text/text_v2` descriptor with required `truncated:false`.
- Shared MSW fixtures were brought to the same live contract for page and axe
  suites. The mobile inspector axe pass excludes opaque sandbox iframe
  traversal because jsdom cannot provide axe cross-frame messaging; focused
  renderer tests remain the owner of iframe sandbox/content safety.

### Verification before the repair commit

- Node 22 full `npm run check`: GREEN — typecheck, ESLint, 190 Vitest files /
  1,683 tests, coverage, and the 3,230-module production build.
- Focused page regression: 17/17 GREEN. Focused page axe: 13/13 GREEN.
- Playwright Chromium + Firefox: 4/4 GREEN for closed and live scenarios.
- Local WebKit could not launch because this Ubuntu host lacks the fallback
  browser's ICU 66, libxml2, and libffi 7 shared libraries. This is recorded as
  a local environment limitation; CI installs browser system dependencies and
  remains the required three-browser verdict.
- Targeted ESLint across all six repair files and final `git diff --check` are
  GREEN. No product content, path, ticket, proof, locator, or production asset
  identity was captured or written.

The Phase 7 commit/PR/CI checkbox remains unchecked until a repair commit is
pushed and the resulting required CI run is fully GREEN.

### Second CI boundary: bundle-budget root cause

- Repair commit `dfe817de0d25db1b5f0dd4f8c3ba8fbf045f8ab8` was pushed and
  CI run `33077699838` proved the Chromium, Firefox, and WebKit Playwright
  matrix GREEN. The frontend job's next step then exposed a separate CSS bundle
  budget failure; full frontend check and coverage remained GREEN.
- The failing checker was reproduced locally without changing inputs: main JS
  was 498.51/500 KiB and main CSS was 108.83/106 KiB. A detached build of the
  exact merge base `5ea911338e56d050f504b4d56fe6de1c114c179e` measured CSS at
  105.02/106 KiB, proving the prior budget had only about 0.98 KiB headroom.
- A rule-level comparison found 48 added Tailwind rules and a net 3,892 raw
  bytes for the approved file-center list/grid, split-pane, responsive state,
  and pointer-aware 44px touch-target contracts. The compressed CSS delta was
  only about 0.46 KiB; there was no dependency/lockfile change or unexpected
  main-JS growth.
- The minimal fix raises only the raw main-CSS ceiling from 106 to 110 KiB,
  leaving the 500 KiB main-JS ceiling unchanged and restoring about 1.17 KiB
  of raw-CSS headroom. The same checker is GREEN at 108.83/110 KiB; targeted
  ESLint and `git diff --check` are GREEN. The temporary detached baseline
  worktree was removed after measurement.

The required final CI run remains pending; no Phase 7 checkbox advances yet.

### Final code-head CI and prepared NAS runbook

- Budget-fix commit `bb8ccd2354ce25a5388429d22f8b4f1bd300af98` was pushed to
  ready PR #472. CI run `33078441208` completed GREEN with all 11 jobs:
  PR title, documentation freshness, migration UTC, frontend, backend,
  PostgreSQL parity, Docker, both runtime-closure architectures, and both
  Worker build/scan architectures.
- Frontend CI proved full check, 1,683 Vitest tests, production build,
  Chromium/Firefox/WebKit direct-preview matrix, 108.83/110 KiB CSS budget,
  coverage gate, and upload. Backend CI proved lint, all tests, race across the
  backupasset and handler packages, backupasset coverage floor, build,
  govulncheck, bounded load contract, and coverage upload. PostgreSQL proved
  migration plus Recovery/Catalog/Search/Overlay/Content/Processing/Export
  behavior parity. No required job was failing, pending, or missing.
- The two Worker jobs emitted only their existing non-blocking Go cache
  annotation because their workflow scope has no root `go.mod`; their actual
  builds, Compose smoke, and scans were GREEN.
- The recoverable production protocol is recorded in
  `research/nas-upgrade-acceptance-runbook.md`. It was derived from the proven
  v0.51.0 NAS sequence recovered through Trellis session insight, then stripped
  of all production identity. It fixes the compose root/file, old stable image,
  external 19927/internal 10761 health boundary, exact release digest/revision
  gate, verified database backup, compose snapshot, automatic rollback, bounded
  postflight, seven usable-preview booleans, and collector-zero stop boundary.
- The runbook contains no credential, token, proof, Provider locator, backup
  path, repository/recovery-point/entry ID, asset name, asset content, or raw
  log output. Target tag/digest/revision remain deliberately blank until the
  merged stable release and official image publication are verified.

The commit/ready-PR/required-CI and NAS-runbook Phase 7 checklist items are now
complete. The following documentation-only evidence commit must also pass its
triggered CI before squash merge; release, NAS write, production acceptance, and
collector-zero completion remain pending.

## Phase 8 — Production-shaped core preview diagnosis and repair

Date: 2026-08-28

Scope stopped at Phase 8. No schema migration, interactive Provider scan, node-log
work, collector change, runtime configuration edit, NAS command, commit, push,
PR, deployment, or Phase 9-12 work occurred.

### Reproduced failure and smallest repair

The production-shaped failure was reproduced at the bounded source-close seam.
For a longer object, Broker `safe_preview_v1` intentionally reads its bounded
classification/rendering prefix and closes before command EOF. The command-backed
Restic and Rclone paths treated the expected remote `Wait` error caused by that
intentional cancellation as a source failure; Broker then returned the generic
503 path even though the complete requested prefix was already available.

- First genuine RED:
  `go test ./internal/backupasset/provider -run '^TestBoundedReadHandleClosesAnUnreadPrefixAsIntentionalCancellation$' -count=1`
  failed with
  `intentional bounded-prefix close=FAKE_ABORTED_COMMAND_WAIT_FOR_TEST_ONLY`.
- Transport RED:
  `TestTransportReadHandleForwardsIntentionalPrefixClose` failed because the
  transport wrapper did not expose the intentional prefix-close contract.
- SSH RED:
  `TestCommandRunnerOpenSupportsIntentionalBoundedPrefixClose` failed because the
  command stream promoted the expected remote wait result to `ErrCommandFailed`.
- Adapter/Broker RED: the first actual-adapter matrix run reached Restic/Rsync but
  Rclone returned `backup asset content source read failure`, proving its
  invariant wrapper also failed to forward the close contract.

The repair is limited to an optional `ClosePrefix()` contract threaded through
the existing bounded Restic handle, transport handle, SSH command stream, and
Rclone invariant handle. Ordinary close behavior, output limits, parent
cancellation, timeout, background stream failures, connection release, and
Rclone post-read invariant verification retain precedence. Rsync local-file close
behavior is unchanged.

Independent review added a natural-failure RED before accepting that boundary:
`TestCommandRunnerOpenPrefixClosePreservesCompletedCommandFailure` initially
reported `completed command failure was suppressed: <nil>`. The command stream
now records whether `Wait` completed before bounded-prefix termination begins.
Only a wait result produced after intentional termination is suppressible;
pre-existing command failure, output-limit, cancellation, timeout, background
stream failure, and natural-EOF join results remain authoritative. The focused
SSH RED/GREEN selector and the race gate are GREEN.

### Adapter, Broker, failure-stage, and UI evidence

- `TestBrokerSafePreviewUsesActualProviderAdaptersThroughIssueGrantAndServe`
  runs actual Restic, Rsync LocalTree, and Rclone adapters with synthetic generic
  configuration text. Each resolves `plain_text/text_v2`, persists one grant,
  reports truncation, and Serves matching readable bytes. Each operation has one
  bounded source open/read: two total per case because Issue and Serve each
  re-open under their existing authority boundary. Restic and Rclone both prove
  intentional prefix-close rather than ordinary aborted close.
- `TestBackupContentSafePreviewCrossesActualRepositoryRsyncBrokerAndLiveIssueHandler`
  closes the remaining composition gap with an actual SQLite-backed Repository
  Service, actual Registry/Rsync adapter, Content Broker, live Gin Issue handler,
  persisted resolved grant, and Broker Serve. It uses generic-MIME synthetic
  configuration text, resolves `plain_text/text_v2`, and proves exactly two
  sequential opens: one bounded read for Issue and one for Serve. Repository
  Service selects adapters through the same closed Provider registry path; the
  three-adapter matrix above independently proves the provider-specific Restic,
  Rsync, and Rclone behavior. Repeating identical handler plumbing for all three
  providers would add no distinct production boundary.
- Existing renderer/Broker coverage remains GREEN for UTF-8 and UTF-16, short,
  exact, and truncated text, retained line endings, inert HTML characters,
  malformed text, binary hex, and signature-proven native formats without a
  derived Worker dependency.
- Broker failures now carry only the closed stages `open`, `read`, `changed`,
  `timeout`, `cancellation`, and `capability`. Handler responses use the standard
  503 envelope, an empty `params` object, and the request correlation ID. The
  opaque outer error string never includes the wrapped Provider cause.
- The strict frontend mapper accepts only the six exact reason codes, empty
  params, the exact response shape, and a bounded safe correlation ID. Preview
  renders localized source-specific guidance. Generic unavailable, capability,
  and renderer errors no longer render the optional Worker hint; Worker language
  remains owned by explicit derived-processing states.
- Cancellation stops the prefix reader before grant persistence. Existing broker
  audit/failure paths plus the new stage and handler canaries remain free of raw
  Provider evidence. Production source files contain none of the synthetic
  fixture markers or private canaries.

### Verification

All frontend commands used Node `v22.23.1`.

- Exact Phase 8 selectors across sshutil/provider/content/handlers, `-count=3`:
  GREEN.
- The same exact selector set under `go test -race`, `-count=1`: GREEN.
- Focused backend package gate across sshutil/provider/content/handlers,
  `-count=1`: GREEN.
- `go test ./...`: GREEN across the complete backend.
- `go vet ./...`: GREEN.
- `go build ./...`: GREEN.
- Focused frontend ESLint plus two affected Vitest files: GREEN, 2 files / 67
  tests.
- Full Node-22 `npm run check`: GREEN — typecheck, ESLint, 190 Vitest files /
  1,690 tests, coverage, and the 3,230-module production build.
- `git diff --check`: GREEN.
- The default documentation-freshness invocation exited 0 but emitted one
  release/deployment documentation reminder from its `HEAD~1` baseline; Phase 8
  changes contain no release, image, deployment, version, route, model, migration,
  or public-contract file requiring that rule's documentation update.

### Independent review closure

The required production-shaped boundary is now covered by complementary tests:
the actual three-adapter Broker matrix proves provider-specific behavior, and the
actual Repository/Rsync/Broker/live-handler vertical slice proves the previously
missing production composition seam. The first Phase 8 checkbox is therefore
complete without inventing a Phase 9 source-discovery dependency.

The affected-package `-count=3` gate also exposed a test-only SQLite namespace
collision in `internal/sshutil`: the credential fixture named a shared in-memory
database only from `t.Name()`, so repetitions observed retained unique node/key
rows. The fixture now uses a unique `t.TempDir()` database name and explicitly
closes its SQL pool. Production behavior is unchanged; `go test
./internal/sshutil -count=3` is GREEN.

#### Final core-transport recheck

A later findings-first review caught four precedence/closed-union gaps that the
original Phase 8 gate did not distinguish:

- RED: `go test ./internal/backupasset/provider ./internal/backupasset/repository
  -run 'TestBoundedReadHandle(ClosesAnUnreadPrefixAsIntentionalCancellation|OrdinaryEarlyClosePreservesUnderlyingFailure)|TestContentSourceExplicitPrefixClosePropagatesWithoutOrdinaryClose'
  -count=1` failed because `boundedReadHandle` exposed no explicit prefix close,
  ordinary early `Close` incorrectly inferred prefix success, and Repository did
  not propagate the intent. GREEN makes `ClosePrefix` explicit through Broker,
  Repository session/once wrappers, bounded Provider handles, and truncated
  Serve cleanup; ordinary close is strict again.
- RED: `go test ./internal/backupasset/content -run
  TestBrokerSafePreviewPreservesReadAndOrdinaryCloseFailuresWithoutLeakingThem
  -count=1` lost the ordinary-close cause. GREEN preserves both errors behind
  closed non-leaking source-stage wrappers.
- REDs: `TestRcloneInvariantPrefixClosePreservesCommandAndInvariantFailures` lost
  the Rclone post-read invariant, and
  `TestCommandRunnerOpenPrefixClosePreservesSessionCleanupFailure` returned nil
  for a failed session cleanup. GREEN joins the invariant with command-close
  evidence and gives session cleanup failure precedence over induced-wait
  suppression while retaining the generic sentinel boundary.
- Frontend RED: the focused `backup-assets-error.test.ts` run accepted inherited
  object keys such as `toString`, `constructor`, and `__proto__` as future source
  stages. GREEN requires an own member of the closed reason map.

Fresh GREEN evidence after the repairs:

- full affected backend packages once: sshutil, provider, repository, content,
  and handlers;
- the exact cross-layer prefix/source/handler selector at `-count=3`, plus the
  same selector under `go test -race -count=1`;
- `GOTOOLCHAIN=go1.26.6 golangci-lint run ./...`: `0 issues.`;
- `npm run typecheck`, full `npm run lint`, and focused error/preview tests:
  2 files / 70 tests;
- owned-path `git diff --check`: GREEN.

Fresh independent gates after these fixes are GREEN: provider, content,
repository, handler, and sshutil affected packages at `-count=3`; the focused
five-package race selector; complete backend tests; build; vet; pinned Go-1.26.6
golangci-lint (`0 issues`); deterministic pinned Swagger regeneration; Node
22.23.1 focused tests (67/67); and the full frontend check (190 files / 1,690
tests plus production build). The workstation's bare golangci-lint binary is
built with Go 1.26 and panics when pointed at the host Go 1.27 standard library;
the repository-pinned version executed under Go 1.26.6 is the authoritative
GREEN lint result.

All Phase 8 implementation checkboxes are complete. Work freezes here before
Phase 9.

## Phase 9 — 45-minute action-scoped secret-reveal session

Date: 2026-08-28

Scope stopped at Phase 9. No schema migration, Provider scan, node-log or
collector work, NAS command, commit, push, PR, deployment, release, or Phase
10-12 work occurred.

### Backend RED / GREEN and fixed policy

- TTL RED:
  `TestJWTManagerGenerateStepUpTokenUsesExactActionTTLPolicy` first failed to
  compile because the per-action policy selector did not exist;
  `TestAuthHandlerStepUpReportsExactActionPolicyTTL` then observed 300 seconds
  where `asset.secret_reveal` requires 2,700. GREEN introduces one closed
  selector: invalid actions map to zero, exact secret reveal maps to 45 minutes,
  and every other registered action maps to the existing five-minute TTL. The
  issuance response and safe credential-audit `ttl_seconds` use the same
  selector.
- Session-binding RED:
  `TestStepUpProofValidationBindsSessionAndExactIssuedLifetime` first failed at
  the validator interface because no presenting login session could be supplied.
  GREEN binds newly issued proofs to the authenticated primary JWT session ID
  and requires exact session equality plus a non-revoked session for
  `asset.secret_reveal`.
- Proof validation retains the signed purpose/action/user/role/token-version
  claims and now also requires subject, issued-at, expiry, exact fixed
  `exp - iat`, current DB user/role/token-version/TOTP state, and, for secret
  reveal, the current login session. Tests reject signature tamper, missing or
  future issued-at, wrong lifetime, subject, purpose/action, user/role/token
  version, disabled TOTP, unknown action, wrong session, and revoked/logout
  session. Repeated validation preserves the original expiry and therefore does
  not slide.
- `TestStepUpProofPairwiseCrossPurposeRejection` covers the complete 18-action
  matrix. Existing non-secret actions retain their prior five-minute issuance
  and reuse behavior; only exact `asset.secret_reveal` requires the new session
  binding and 45-minute lifetime.

### Frontend RED / GREEN and ownership

- First state-machine RED:
  `clears a rejected cached proof and permits only one fresh Admin prompt and
  retry` stopped after one cached helper result; it called the helper once where
  the contract requires cached reuse followed by at most one fresh prompt.
  GREEN performs exactly three bounded ticket attempts when both proofs are
  rejected: unproved, cached proof, fresh proof. It calls the central helper
  exactly twice with `{persist:true,reuseCached:true}` then
  `{persist:true,reuseCached:false}`, clears the rejected action before fresh
  issuance, clears a rejected fresh proof, and terminates without a loop.
- Forced-refresh invalidation RED:
  `强制刷新可持久 proof 时会先清除被拒绝的缓存` found the rejected marker still
  in session storage while the fresh TOTP dialog was open. GREEN makes
  `reuseCached:false` clear that action before prompting even when the replacement
  proof will be persisted.
- File-center state no longer contains `secretRevealProofRef` or
  `secretRevealProofOwnerRef`. A proof-bearing ticket input remains an ephemeral
  request value; after a successful central step-up it is retained only in the
  current in-memory active-preview attempt so exact manual retry can reuse the
  same authority. It is never persisted with ticket/content state. Search reads
  the same central action-keyed cache at request time, so later pages and saved
  searches share the proof without file/source ownership.
- Focused tests prove Admin first prompt, central proof reuse on safe/manual
  retry, preview renew, different asset/version, different node/source, later
  search pages, saved-search/directory reload, and hook remount/page refresh.
  Operator and Viewer never prompt, and ordinary non-secret previews never
  prompt. The cached-rejection/remount selectors passed three consecutive runs.
- The exact secret-reveal storage test preserves the server-supplied fixed expiry
  through intermediate reads, remains usable at `expiresAt - 1`, and removes the
  action at `expiresAt`; reads never extend expiry.

### Invalidation and privacy evidence

- The current-login session store remains a versioned action-keyed map containing
  only action, proof, and expiry. Login replacement (covering user/role/token
  replacement), logout, TOTP disablement, 401, expiry, and typed proof rejection
  clear the applicable proof or all proofs. The explicit 401 test clears both
  secret reveal and an unrelated action before redirecting to login.
- Production scans found no proof adjacent to `localStorage`, history,
  `URLSearchParams`, console, analytics/logging, content URL, expiry ticket state,
  or content-ticket persistence. Search and content clients transmit proof only
  through the central `X-Xirang-Step-Up` request option/header; it is absent from
  URLs, history, response DTOs, analytics, and logs. No content or ticket is
  persisted by the proof store.

### Verification

- Backend exact TTL/session/cross-purpose selectors at `-count=3`: GREEN.
- Backend exact selectors under `go test -race`, `-count=3`: GREEN.
- `GOTOOLCHAIN=go1.26.6 go test ./internal/auth ./internal/api/handlers`:
  GREEN. This affected gate found and fixed one test-matrix omission: the matching
  secret-reveal cell now supplies its presenting session ID.
- `GOTOOLCHAIN=go1.26.6 go test ./...`: GREEN across the complete backend.
- `GOTOOLCHAIN=go1.26.6 go vet ./...` and `go build ./...`: GREEN.
- Node `v22.23.1` affected typecheck/ESLint and four affected Vitest files:
  GREEN (107 tests before the final exact-expiry storage addition).
- Node `v22.23.1` full `npm run check`: GREEN — typecheck, full ESLint, 190
  Vitest files / 1,693 tests with coverage, and the 3,230-module production build.
- Privacy scans and `git diff --check`: GREEN.

All Phase 9 implementation checkboxes are complete. Work freezes here before
Phase 10.

### Phase 9 independent quality review addendum (2026-08-28)

- **Important — API and audit TTL facts could drift from the signed proof.** RED
  observed every returned `expires_at` retaining sub-second precision after JWT
  `NumericDate` truncated signed `exp`, the secret-reveal required envelope still
  advertising 300 seconds, and an unknown action inheriting 300 audit seconds.
  GREEN truncates issuance once before signing/returning and derives response and
  audit TTLs from the closed action policy: exact `asset.secret_reveal` is 2,700,
  every other registered action remains 300, and invalid/future actions are zero
  and fail closed.
- **Important — proof validation did not require the signed proof JTI.** RED
  accepted an otherwise valid proof with no `jti`. GREEN requires the generated
  32-character lowercase-hex proof ID in addition to the existing exact purpose,
  action, user/subject, role, token version, TOTP, `iat`/`exp`, login-session JTI,
  and revocation checks.
- **Important — typed search rejection did not invalidate the central action
  cache.** RED left the rejected `asset.secret_reveal` entry reusable after
  initial search, saved-search reload, or cursor failure. GREEN clears exactly
  that action on typed `secret_reveal_required` and remains fail-closed without
  prompting or looping.
- **Important — concurrent same-action callers could replace/orphan one another,
  and a late issuance response could repersist after an auth boundary.** RED made
  the first of two current callers reject while the second resolved. GREEN
  coalesces matching action/persistence requests onto one promise/dialog/result;
  login replacement, logout, TOTP disablement, and 401/token removal reject the
  pending owner, advance auth ownership, close the dialog, and prevent the late
  response from saving or resolving a stale proof. Selection-generation guards
  still prevent replay to a stale asset.
- **Important — an attached invalid proof was not a typed rejection on the live
  content/search paths.** Content returned the generic step-up envelope, which the
  file-center mapper could not use to clear and refresh a cached proof; optional
  search verification silently converted every invalid non-empty proof to no
  proof. Focused REDs reproduced both paths. GREEN preserves truly absent proof as
  optional, propagates non-empty invalid proof, and returns the closed
  parameter-free `secret_reveal_required` reason from search and secret-preview
  ticket issuance. No proof value enters the response.
- **Important — preview retry/renewal dropped valid session authority.** The
  successful step-up retry did not update the active in-memory request, and renewal
  rebuilt a proofless exact product. GREEN retains the proof-bearing active attempt
  for manual retry and adds the current central proof to renewal only when the
  server ticket classification is `secret` or `unknown`; `non_secret` renewal never
  receives a proof. The helper now generically preserves the exact preview-input
  discriminant, with no cast or union weakening.
- **Important — two exact binding/expiry edges were open.** Validation accepted an
  empty caller role because role comparison was conditional, and the browser
  derived `Date.now() + proof_ttl_seconds` when the server expiry was malformed.
  GREEN requires non-empty exact caller and current-DB roles, and persists only a
  finite server-supplied `expires_at`; malformed expiry can be used for the current
  request but cannot establish a sliding cache window.
- The backend error-handling and frontend quality/type-safety specs were corrected
  to record the fixed action TTL/session binding, central session store ownership,
  cross-owner reuse, exact invalidation boundaries, bounded rejection retry, and
  concurrency contract. No Phase 10 behavior was added.

Reviewer verification is GREEN: exact backend selectors at `-count=3` and under
`go test -race` at `-count=3`; complete `GOTOOLCHAIN=go1.26.6 go test ./...`,
`go vet ./...`, and `go build ./...`; Node `v22.23.1` focused auth/file-center
tests for 86 tests on each of three consecutive runs; and the final full
`npm run check` including typecheck, ESLint, coverage tests, and production build.
The final reviewer reran Node 22.23.1 typecheck, ESLint, and the four affected
auth/storage/client/file-center files (117 tests), plus complete auth and handler
Go packages; all passed.
`git diff --check` and production privacy scans are GREEN: proof values appear
only in the action-keyed `sessionStorage` store and the step-up transport header,
never in URL/history, localStorage, analytics, console/log output, raw errors, or
content-ticket products. No confirmed Critical or Important finding remains.

## Phase 10 — All backup-bearing source lineages

Date: 2026-08-28

Scope stopped at Phase 10. No schema migration, node-log or collector work, NAS
command, commit, push, PR, deployment, release, or Phase 11-12 work occurred.

### Existing-schema gate

The gate is satisfied without a migration. Existing `RecoveryPoint` rows carry
the immutable producing-node/repository/task/run snapshots, publication lineage,
publication consistency, provider-commit lifecycle, and imported-baseline state
needed to prove retained bytes and exact attribution. Existing
`TaskRepositoryLink`, `BackupRepository`, `CatalogGeneration`, import/rebuild, and
publication lifecycle facts express current authorization, repository sequential
capability/availability, and whether a complete public Catalog point exists.
Neither SQLite nor PostgreSQL schema changed.

### Projection RED / GREEN

- The initial retained-lineage RED did not compile because source DTOs had no
  closed browse state or retained-version count. GREEN enumerates exact active,
  interrupted, disabled, archived, and deleted lineages for Admin when durable
  point/link/provenance facts agree. Mutable task status does not gate durable
  bytes. Configured tasks with no retained point are absent; malformed,
  ambiguous, task-attributed imported, or otherwise unproven candidates are
  omitted rather than guessed.
- `TestBackupFileSourceProjectionEnumeratesRetainedLineagesIndependentOfMutableTaskState`
  proves five Admin sets and exactly the three currently owned, non-archived task
  lineages for Operator. Deleted and archived ownership fails closed for Operator;
  Admin retains snapshot-backed visibility after task deletion.
- `TestBackupFileSourceProjectionClosesUnavailableReasonsAndOmitsUnprovenLineages`
  proves `browsable` only for a public point with an active complete Catalog,
  `indexing` for proven retained bytes without that complete point, and
  `unavailable` for an offline repository or missing sequential-read capability.
  Reasons are closed and parameter-free; mixed node summaries use only the safe
  generic reason. No repository identity, locator, path, evidence, content,
  credential, token, or proof is serialized.
- Visible-fact cursor RED/GREEN now includes availability and browse state. Signed
  projection digests bind the complete sanitized DTO facts, authorization scope,
  endpoint/resource, order, and paging position; changing repository availability
  makes an old continuation return `ErrStaleCursor`.

### Database-only HTTP and bounded reconciliation

- `TestBackupFileSourceProjectionHasNoProviderCommandDependency` and
  `TestBackupFileSourceHandlerHasNoProviderCommandDependency` prove the projection
  and Files handlers contain no Provider/runner/SSH/exec dependency. Listing is a
  bounded database/Catalog projection and makes zero Provider calls.
- The first batching RED counted four database queries for three candidates.
  `TestListCandidatesUsesFixedDatabaseQueriesForBatch` is GREEN after replacing
  per-point lease lookups with one bounded point scan and one set-based live-lease
  query: at most two queries, independent of batch size.
- An independent restart-fairness RED created eleven unresolved points with ten
  recently backed off ahead of one due target; a fresh service returned an empty
  page. GREEN removes the process-local cursor and has
  `TestListCandidatesFreshServiceWalksPastBackedOffPrefix` select the durable
  oldest `updated_at, id` candidate on every restart. A successful fenced claim
  persists both `LastAttemptAt` and `updated_at`, rotating attempted work behind
  older unresolved rows consistently across processes and instances. The scan
  stays bounded, the live-lease lookup stays set-based, and request limits outside
  1..1000 fail closed.
- The existing publication worker remains the sole Provider-observation boundary:
  one bounded candidate page per pass, configured bounded concurrency, at most one
  reconciliation call per claimed point, context cancellation and joined shutdown,
  durable retry/backoff facts, and restart recovery from preparing/verifying rows.
  `TestPublicationWorkerWakeTrafficDoesNotStarvePeriodicScan` additionally proves
  that wake traffic cannot reset or indefinitely postpone the durable scan timer.
  Exact Restic tags plus stored-summary provenance, exact Rclone commit/manifest,
  and exact Rsync final-marker evidence may repair an interrupted publication.
  Missing, multiple, rewritten, staging-only, or provenance-drift observations
  fail closed and remain failed/quarantined instead of becoming Catalog truth.

### RBAC, API, frontend, and privacy RED / GREEN

- Admin continues to use the existing global list authority. Operator visibility
  is rechecked through the current non-archived task and current node owner; a
  missing/deleted/ambiguous owner, archived task, cross-user node, identity
  collision, or malformed provenance fails closed. Handler tests prove safe
  state/count/display/opaque-ID responses and standard envelopes without Provider
  calls or reason parameters.
- The strict frontend mapper/control RED first reported seven failures because the
  new fields and state controls did not exist. GREEN (36 tests) requires the exact
  browse-state union, non-negative retained count, known parameter-free unavailable
  reasons, and state/reason consistency while dropping raw extras. Non-browsable
  versions are visibly disabled and cannot invoke selection.
- The hook RED observed a non-browsable version reaching the selection callback;
  GREEN (20 tests) guards it. The route/page RED then reported three failures for
  retaining legacy repository/task/recovery/entry browsing state; GREEN (29 tests)
  preserves the safe node/set hierarchy but clears incompatible browse descendants
  until the exact version is `browsable`.
- The first complete frontend gate found nine stale test fixtures missing required
  browse truth. They were repaired as strict public-contract fixtures, including
  the unavailable accessibility state; the exact page/a11y gate is GREEN (30
  tests). Chinese/English primary controls use user-facing indexing/unavailable
  language and do not introduce Repository/Provider terminology.

### Verification

All backend commands used `GOTOOLCHAIN=go1.26.6`; all frontend commands used Node
`v22.23.1` with `NODE_ENV` unset.

- Catalog/handler/repository/runtime affected packages at `-count=3`: GREEN.
- The same affected package set under `go test -race`, `-count=1`: GREEN.
- Exact provider repair, candidate query-count, cursor-progress, import/rebuild,
  cancellation, retry/backoff, and worker selectors: GREEN; the final repository
  selector also passed `-count=3` and `-race`.
- Frontend mapper, controls, hook, selection, and route/page focused tests at
  `-count=3`: GREEN (73 tests per run).
- `GOTOOLCHAIN=go1.26.6 go test ./...`: GREEN across the complete backend after
  the cursor-progress change.
- `GOTOOLCHAIN=go1.26.6 go vet ./...`, `go build ./...`, and pinned
  golangci-lint v2.11.4: GREEN (`0 issues`). The bare host Go is 1.27 while the
  pinned linter was built with Go 1.26, so the authoritative lint invocation pins
  Go 1.26.6 as required by `go.mod`.
- Node-22 `npm run check`: GREEN — typecheck, ESLint, 190 Vitest files / 1,709
  tests with coverage, and the 3,230-module production build.
- Backend and frontend privacy canaries and production scans are GREEN. No raw
  locator/path/evidence/content/credential/token/proof or reason parameters cross
  the source DTO/control boundary.
- `git diff --check`: GREEN. No migration file was added or modified.

All Phase 10 implementation checkboxes are complete. Work freezes here before
Phase 11.

### Phase 10 independent quality review addendum (2026-08-28)

The reviewer reproduced and fixed three Important defects before the Phase 11
freeze:

1. A mutable point whose strict lineage JSON named a task but whose durable
   `producing_task_id` column was NULL was accepted for Admin projection. The RED
   returned no error; GREEN requires the live task ID to exist and exactly match
   the lineage, and rejects any mutable producing-run ID.
2. The process-local fairness cursor deterministically restarted at a backed-off
   prefix and was inconsistent across workers. The RED returned no due target on
   restart; GREEN uses the shared durable ordering and existing lease fencing
   described above, with no schema change.
3. Every wake recreated the publication worker timer, so sustained wake traffic
   could starve periodic database recovery. The RED observed zero list calls;
   GREEN holds the timer deadline across wakes and resets it only after the
   periodic pass.

All three focused selectors passed `-count=3` and `go test -race -count=3`.
`GOTOOLCHAIN=go1.26.6 go test ./... -count=1`, `go vet ./...`, `go build ./...`,
and golangci-lint v2.11.4 (`0 issues`) passed. Node 22.14 directly ran TypeScript
typecheck, ESLint, 190 Vitest files / 1,709 tests with coverage, and the 3,230-module
production build. Focused backend privacy/RBAC/dependency selectors passed
`-count=3`; `git diff --check` passed and the model/database/migration diff remained
empty. A full runtime-package `-count=3` diagnostic exposed three unrelated
repeat-run/global-state tests; those exact tests pass together at `-count=1`, and
the authoritative complete backend count=1 gate is green.

The existing schema is sufficient: no duplicate inventory or migration is
justified. No confirmed Critical or Important Phase 10 finding remains; Phase 10
is complete and remains frozen before Phase 11.

### Phase 10 retained-lineage reconciliation addendum (2026-08-28)

A later production-shape review reproduced and repaired four further Important
projection-boundary defects:

1. Managed immutable points are persisted with physical availability `unknown`.
   Even when the repository was online and exact provider-commit/repository
   identity evidence was present, the Files projection classified those retained
   points as unavailable forever. The RED now uses that production-shaped state;
   GREEN treats exact managed retained evidence as content-available while keeping
   explicit offline, missing, non-sequential, and ambiguous states fail closed.
2. The frontend mapper accepted `browsable` or `indexing` while the same version's
   `content_availability.available` was false. The malformed DTO RED is now blocked;
   reachable non-unavailable browse states require available retained content.
3. Backend DTO validation permitted that contradictory state and also permitted
   unavailable content without a closed reason. The producer-side RED now rejects
   both forms, preventing malformed availability from becoming an HTTP product.
4. Restic verifying points were accepted with generic non-empty commit/identity
   digests even when the required tag digest and capture interval were absent.
   The backend-specific RED now requires the exact Restic consistency shape and
   exact managed Rclone/Rsync consistency shape, including matching point
   capability revision and closed success state, before treating physical
   `unknown` as retained availability.

The Catalog spec was also reconciled with the approved implementation contract:
HTTP performs zero Provider work; one claimed point receives at most one
reconciliation invocation. A backend may make its fixed, bounded set of exact
identity/provenance observations inside that invocation. The prohibition is on
N+1 or unbounded observation/retry loops, not on the multiple fixed observations
needed to prove identity.

Fresh focused file-source and dependency/privacy selectors passed `-count=3`; the
affected race selector passed without a race report. Complete backend `go test
./...`, build, vet, and golangci-lint passed. Frontend typecheck, ESLint, production
build, and the five-file file-source slice passed (75 tests on each of three runs).
An initial full frontend test gate exposed seven stale cross-owner UI/i18n
expectations in six non-file-source suites. After their owning reviewer reconciled
those fixtures, a fresh Node-22 `npm run check` passed typecheck, ESLint, all 190
Vitest files / 1,743 tests with coverage, and the 3,230-module production build.

A cross-owner Important authority defect was also reproduced and repaired:
Operator authorization joined current Task ownership but did not require the
current Node row itself to be live, so an archived current node with a stale owner
could authorize a retained point. The dedicated ownership fix joins the live,
non-archived current Node while preserving immutable lineage validation and Admin
provenance visibility. Archived and missing current-node REDs plus the current-task
migration and repository visibility selectors passed `-count=3` and race.

## Phase 11 — Explicit Up navigation and revised browser UX

Date: 2026-08-28

Scope stopped at Phase 11. No schema migration, Provider scan, node-log or
collector work, NAS command, commit, push, PR, deployment, release, or Phase 12
work occurred.

### RED to GREEN evidence

- Backend EntryPage compile RED proved the page had no required directory
  context. GREEN adds `directory:{current,parent,breadcrumb}` on root, nested,
  empty, and every cursor page. Catalog tests cover current directory type,
  same recovery-point/active-generation lookup, bounded acyclic directory-only
  ancestry, parent/current contradictions, missing ancestors, cursor-page
  identity, and stale rename rejection through a directory-context digest.
- The handler serialization RED showed an invalid root response with
  `breadcrumb:null`. GREEN serializes `current:null`, `parent:null`, and
  `breadcrumb:[]`; generated Swagger requires the directory object and all three
  members.
- The frontend mapper RED rejected none of the missing/malformed contexts. GREEN
  atomically rejects missing members, malformed/blank names, cross-point refs,
  cycles, parent/current contradictions, and malformed items while stripping
  unknown raw/normalized path fields.
- Reducer/UI REDs showed missing cursor context, no native Up affordance, and a
  directory transition that did not detach preview. GREEN binds append pages to
  identical directory context, fails closed on contradiction, replaces the
  standalone Root action with a native 44px Up control, retains root/ancestor
  crumbs, and detaches preview then clears selection before the opaque route
  patch.
- Browser/workspace tests cover pointer activation, list/grid keyboard behavior,
  empty nested directories, exact deep-link reload, rapid A-to-B navigation
  abort with late-A suppression, root/direct/deep Up, ancestor jumps, origin
  scroll and row-focus restoration at 390px and 1440px, 200% zoom layout,
  animation-free focus/scroll restoration, localized accessible names, and axe.
  Existing direct-source preview tests continue to prove Worker guidance is not
  shown unless the state explicitly describes a Worker-derived enhancement.

### Verification

- Catalog Phase 11 selectors at `-count=3`: GREEN.
- Focused Catalog `go test -race`: GREEN.
- Handler serialization and Swagger selectors at `-count=3`: GREEN.
- `go test ./...`: GREEN across all backend packages.
- Pinned `swag` v1.16.6 regeneration: byte-deterministic. SHA-256 remained
  `43bb796a...` for `docs.go`, `65fec3fb...` for `swagger.json`, and
  `fba646ab...` for `swagger.yaml`.
- Node 22 focused backup-assets suite: 41 files / 609 tests GREEN; final targeted
  runs were workspace 47/47, state hook 77/77, and strict API mapper 14/14.
- Node 22 `npm run check`: GREEN — typecheck, ESLint, 190 Vitest files / 1,726
  tests with coverage, and the 3,230-module production build.
- `git diff --check`: GREEN. Production-addition privacy scan found no raw path,
  normalized path, Provider locator, localStorage, or sessionStorage addition in
  the Phase 11 surfaces. Directory restoration state remains in memory and uses
  only opaque composite references and numeric scroll offsets.

All Phase 11 implementation checkboxes are complete. Work freezes here before
Phase 12.

## Phase 11 independent reviewer remediation

Date: 2026-08-28

The independent Phase 11 check reproduced and repaired five Important contract
gaps with focused RED-to-GREEN tests:

- `catalog.ErrInvalidCatalogContract` was exposed as caller error HTTP 400 even
  when it represented malformed persisted directory ancestry or a DTO contract
  violation. The shared handler mapping now leaves this internal sentinel on the
  generic, detail-free HTTP 500 path; request syntax/ref/cursor errors retain
  their existing 400 mapping.
- `BreadcrumbDTO` members were optional in generated Swagger, and the entry-list
  operation omitted its generic 500 response. All three breadcrumb fields are
  now required and the route documents 500. Pinned Swagger was regenerated.
- The frontend page mapper validated directory context but accepted an available
  item whose `parentRef` belonged to a different directory. It now atomically
  requires every item to use the exact recovery point and exact requested parent
  (`null` at root).
- Reducer append equality returned early for root contexts and ignored their
  breadcrumb. It now compares current, parent, and every breadcrumb item for
  both root and non-root pages, failing closed and clearing mixed results and
  selection on any contradiction.
- The authoritative specs had no executable Phase 11 directory-context,
  navigation, or accessibility contract. Backend error handling plus frontend
  type-safety, quality, and a11y specs now record the exact API, cursor, mapper,
  navigation, focus, privacy, and test requirements.

Additional boundary tests prove exact 256-level directory ancestry succeeds and
257 fails, and that an entry cursor rejects user, role, direction, sort,
Repository, recovery point, generation, parent, directory-digest changes, and
HMAC tamper.

### Independent final gates

- Catalog/handler Phase 11 selectors: GREEN at `-count=3`; the same focused Go
  selectors are GREEN under `go test -race -count=3`.
- Node 22.23.1 navigation/API/state selector: 5 files / 162 tests GREEN in each
  of three independent runs.
- `GOTOOLCHAIN=go1.26.6 go test ./... -count=1`: GREEN across every backend
  package after the final handler/Swagger change. `go vet ./...` and
  `go build ./...` are GREEN.
- `GOTOOLCHAIN=go1.26.6 golangci-lint run ./...`: GREEN, `0 issues.` The first
  unpinned invocation inherited ambient Go 1.27 and panicked because the linter
  binary was built with Go 1.26; pinning the repository toolchain removed that
  environment mismatch without any source change.
- Node 22.23.1 `npm run check`: GREEN — typecheck, ESLint, 190/190 Vitest files
  and 1,729 tests with coverage, plus the 3,230-module production build.
- Two consecutive pinned `swag` v1.16.6 generations are byte-identical:
  `2ac3308a270803079e2ed4fa62fe9a225c9677f164030eb32739b6ff29d56046`
  (`docs.go`),
  `70f0fabe42b4339ae84955b2f62a24b07de193cac13a1247a6fb7210f09e9e0d`
  (`swagger.json`), and
  `35882dc18d0e35f4c403cebb706dcd0987f62cc7c9e4bd14e2fc858dc7aa51fc`
  (`swagger.yaml`).
- Task context validation and `git diff --check` are GREEN. Production-addition
  scanning found no raw/normalized path, Provider locator, browser-storage, or
  cookie sink in Phase 11 surfaces. Package manifests, Go modules, and database/
  migration paths have no diff.

Phase 11 is complete after reviewer remediation and remains frozen before Phase
12. No schema/Provider scan, node-log/collector, NAS, commit, push, PR, deploy,
or release action was performed by this review.

## Phase 12 independent full-scope verification

Date: 2026-08-28

The independent Phase 12 reviewer loaded the complete Phase 8-11 task artifacts,
package specs, nested `AGENTS.md` files, and the actual dirty diff. The review
traced Repository Service/provider adapters through Broker Issue/grant/Serve,
bounded-prefix close precedence and closed source-failure products; the exact
45-minute secret-reveal policy, signed session/JTI binding, central cache
concurrency and invalidation; retained-lineage/RBAC/cursor truth, fixed-query
listing and the durable publication reconciler; and required directory context,
strict frontend mapping/reduction, cancellation, focus, accessibility, and
deep-link compatibility.

### Finding repaired with RED -> GREEN

- **Important — the live Playwright fixture had drifted from the Phase 10/11
  required API contracts.** `web/e2e/backup-assets-gate.spec.ts` omitted
  `retained_version_count`, `browse_state`, and `EntryPage.directory`, so the
  real Chromium live flow failed before reaching the list. The focused live
  Chromium command reproduced one failure. The fixture now supplies the strict
  file-source and directory-context fields, models an empty nested directory,
  and adds a 390x844, 200%-text-zoom keyboard/Up regression that proves return
  to root and origin-row focus restoration. Chromium and Firefox are now 3/3
  GREEN. No production code change was required.

No other Critical or Important finding remained after complete cross-layer and
privacy review. The closed error responses expose only enumerated reason/code
and safe correlation ID; no raw/normalized path, Provider locator, command
output, cause, proof, ticket, content, or private evidence enters those errors,
audit, or logs. Public DTO/Swagger additions expose no private source evidence;
the established opaque content URL remains the only ticket-bearing response
contract. Frontend additions contain no `any`, unsafe `unknown as` cast, or
component-level `fetch`; proof caching and transmission remain behind the
central authenticated API boundary and are invalidated by the reviewed session
events. Existing preview, download, and opaque deep-link contracts remain
covered.

### Fresh verification gates

- Go 1.26.6 focused Phase 8-11 selectors: 24 selectors across auth, handlers,
  catalog, content, provider, repository, runtime, and sshutil are GREEN at
  `-count=3` and under focused `go test -race -count=1`.
- `GOTOOLCHAIN=go1.26.6 go test ./... -count=1`, `go build ./...`, and
  `go vet ./...`: GREEN. Pinned golangci-lint 2.11.4 reports `0 issues.`
- Pinned `swag` v1.16.6 regenerated twice with no byte drift. SHA-256 remains
  `2ac3308a270803079e2ed4fa62fe9a225c9677f164030eb32739b6ff29d56046`
  (`docs.go`),
  `70f0fabe42b4339ae84955b2f62a24b07de193cac13a1247a6fb7210f09e9e0d`
  (`swagger.json`), and
  `35882dc18d0e35f4c403cebb706dcd0987f62cc7c9e4bd14e2fc858dc7aa51fc`
  (`swagger.yaml`). Documentation freshness is GREEN.
- Node 22.23.1 focused auth/API/browser/preview/page suite: 15 files / 343 tests
  GREEN in each of three independent runs. Typecheck and ESLint are GREEN.
  `npm run check` is GREEN: 190/190 Vitest files, 1,729 tests with coverage,
  and the 3,230-module production build. Axe selectors report no violations;
  existing non-fatal React `act(...)` warnings remain in test output.
- Playwright Chromium: 3/3 GREEN. Playwright Firefox: 3/3 GREEN. The nine-test
  matrix includes closed/live flow plus mobile 390x844/200%-text-zoom Up/focus.
- Playwright WebKit: externally blocked before test execution because the host
  lacks `libicudata.so.66`, `libicui18n.so.66`, `libicuuc.so.66`,
  `libxml2.so.2`, and `libffi.so.7`. No browser or system dependency was
  downloaded or installed. The Phase 12 frontend/browser checkbox therefore
  remains open.
- Exact repository gate
  `PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:$PATH GOTOOLCHAIN=go1.26.6 env -u NODE_ENV make check`:
  GREEN, including lint, full Go/frontend tests, and both production builds.
- Task-context validation, documentation freshness, dependency/manifest,
  migration/schema, workflow/deploy, collector/node-log, response/privacy,
  and TypeScript escape scans are GREEN. `git diff --check` is GREEN.
  `TEST_POSTGRES_DSN` is unavailable, but no database model, migration, schema,
  or database path changed, so PostgreSQL parity was not affected by Phases
  8-11.

Phase 12 verification is complete except for the explicitly blocked WebKit
matrix. Delivery, CI/merge/release, NAS upgrade, production acceptance, and
collector/node-log steps remain unchecked and require separate authorization.
No commit, stage, push, PR, merge, deploy, release, NAS, production, Provider
scan, collector, or node-log action was performed by this review.

## Phase 11 final directory-contract audit

Date: 2026-08-28

A final independent audit reproduced and repaired four additional Important
directory API/state defects with focused RED-to-GREEN tests:

- Entry cursors were decoded only after the requested directory lookup. A valid
  old-generation cursor could therefore return 404 when the new active
  generation lacked that directory, and a tampered cursor could also reach the
  lookup first. Cursor authentication and all non-directory scope checks now run
  before directory lookup; authenticated point/generation/parent/sort/user/role
  drift returns 409, while malformed or unauthenticated tokens return 400.
- Unexpected storage failures while loading the requested directory were
  collapsed to not-found. Only an actual missing directory now maps to not-found
  (or stale cursor); storage failures remain internal and reach the handler's
  generic, detail-free 500 response path.
- The strict frontend page mapper applied bounded, non-blank directory names to
  context members but not page items. Item names now use the same validation and
  atomically reject blank, NUL-containing, or over-4096-byte values without
  exposing raw/normalized paths.
- `results_replaced` accepted a response for an obsolete request key and could
  overwrite the newest route/result. Replacement and append actions now both
  require the currently active request identity; late responses are ignored.

### Fresh audit gates

- Focused Catalog/handler selectors: GREEN at `-count=3` and under
  `go test -race -count=1`, including stale generation-before-lookup, tamper,
  missing directory, storage failure, exact 256/257 depth, ancestry, Swagger,
  and HTTP privacy cases.
- `GOTOOLCHAIN=go1.26.6 go test ./... -count=1`, `go vet ./...`, and
  `go build ./...`: GREEN. Pinned golangci-lint reports `0 issues.`
- Node 22.23.1 focused API/state/route/hook suite: 4 files / 116 tests GREEN;
  ESLint is GREEN. The repository-wide typecheck was temporarily blocked by a
  concurrent translation-literal mismatch in
  `web/src/pages/__tests__/backups-page.a11y.test.tsx`; the final frontend audit
  replaced the inferred English-literal helper with the exact English/Chinese
  resource union, and the current repository typecheck is GREEN.
- Two consecutive pinned `swag` v1.16.6 generations are byte-identical:
  `2ac3308a270803079e2ed4fa62fe9a225c9677f164030eb32739b6ff29d56046`
  (`docs.go`),
  `70f0fabe42b4339ae84955b2f62a24b07de193cac13a1247a6fb7210f09e9e0d`
  (`swagger.json`), and
  `35882dc18d0e35f4c403cebb706dcd0987f62cc7c9e4bd14e2fc858dc7aa51fc`
  (`swagger.yaml`). The explicit required directory-context Swagger selector is
  GREEN at `-count=3`.
- `git diff --check` is GREEN. Phase 11 production surfaces add no raw path,
  normalized path, Provider locator, browser-storage, IndexedDB, or cookie sink.

No schema/Provider scan, node-log/collector, NAS, commit, push, PR, deploy, or
release action was performed by this audit.

## Final release-readiness audit

Date: 2026-08-28

### Findings and task truth

- An Important final ownership RED proved that Operator point, Repository, and
  link-lineage visibility trusted the current Task plus a stale `node_owners` row
  without requiring the Task's current Node to exist and remain non-archived.
  Catalog and Repository now join the live non-archived current Node before
  applying current ownership. Regression coverage proves legitimate Task moves
  transfer authority to the current owner, deny the former owner, and fail closed
  for archived or missing current Nodes even when stale ownership evidence
  remains. The migration/live-node selectors pass at `-count=3` and under race.
- PRD, design, task metadata, implementation plan, and the production-remediation
  research note now distinguish the historical product-choice pause from the
  user's later explicit implementation approval. The implementation plan names
  the actual remediation branch, and task metadata includes the Phase 8-11
  production touchpoints. Delivery, production, NAS, collector, and node-log
  authority remain separate and ungranted.
- The backup-file Catalog spec now states the intended authority split exactly:
  current live Task/Node ownership grants present authority, while immutable
  link/point snapshots must agree with each other as historical attribution and
  are not rewritten by a legitimate Task move. Required tests cover migration,
  archived/missing current Nodes, and stale ownership rows across point and
  Repository/link projections.

### Stable verification

- Exact repository gate
  `env -u NODE_ENV PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:$PATH GOTOOLCHAIN=go1.26.6 make check`:
  GREEN. Backend golangci-lint reports `0 issues`; all backend tests and the
  production binary build pass. Frontend ESLint, typecheck, 190/190 Vitest files
  and 1,743/1,743 tests with coverage, and the 3,230-module production build pass.
- Sequential Playwright is GREEN on Chromium 3/3 and Firefox 3/3, including the
  390x844/200%-zoom Up/focus case. WebKit remains externally blocked before test
  execution because this host lacks `libicudata.so.66`, `libicui18n.so.66`,
  `libicuuc.so.66`, `libxml2.so.2`, and `libffi.so.7`; no dependency was installed.
  The Phase 12 frontend/browser checkbox therefore remains open until CI or a
  supported host runs the three WebKit cases.
- Task validation passes with 19 implementation and 15 check entries. Its only
  notices are known context-injection size warnings for specs that were read in
  full by this review. Dirty-diff documentation freshness, 88-file migration UTC
  safety, JSON parsing, gofmt, scope/dependency/config scans, privacy/secret scans,
  and `git diff --check` are GREEN.
- Branch `codex/backup-file-center-production-remediation` is based exactly on
  `origin/main` at `85082407c0873566125b4b13660ab2dabce0e4d9` with ahead/behind
  `0/0`. All task work remains dirty and uncommitted. Candidate PR title
  `fix(backup-assets): remediate file center production gaps` passes policy. A
  squash with that `fix` title should feed the next patch Release Please PR from
  current manifest `0.52.0`; it does not itself publish a public release or image.

### Readiness boundary

The code is ready to proceed to a separately authorized commit/ready PR, where
required CI must provide the supported WebKit result. This task is not complete:
commit/push/PR/CI/merge, Release Please and any release/image automation, NAS
upgrade, production product acceptance, collectors, and node logs remain open.
No such action was performed by this audit.

## Ready-PR browser and CI remediation checkpoint

Date: 2026-08-28

- The user explicitly authorized the delivery workflow after independent review.
  Commit `ef371d5` was pushed on
  `codex/backup-file-center-production-remediation`, and ready PR #474 was opened.
- The first required CI browser matrix executed all nine Playwright cases across
  Chromium, Firefox, and WebKit successfully. This closes the external local
  WebKit-host-library blocker and the Phase 12 frontend/browser verification item.
- The same first CI run exposed two deterministic delivery-gate failures after
  browser success. The backend Phase 8 vertical fixture inherited a local
  development environment and lacked an explicit test encryption key in CI. It
  now supplies only a deterministic fake test key and resets the secure test
  cache before and after the test; production behavior is unchanged.
- The frontend startup chunk was 697 bytes over its fixed 500 KiB budget. The
  core backup-assets API now uses the existing cached dynamic-import boundary;
  its public asynchronous methods are unchanged. Node 22 production output is
  493.85 KiB for the startup chunk, 6,297 bytes below budget, with a dedicated
  6.96 KiB raw backup-assets API chunk. The budget itself was not increased.
- Focused frontend tests, Node 22 `npm run check` (190 files / 1,743 tests), the
  exact bundle-budget script, and the CI-shaped backend fixture selector are
  GREEN. Required CI must rerun on the remediation commit before merge; the
  compound Phase 12 delivery/merge checkbox remains open.

## Production-blocking plain-text grant schema hotfix

Date: 2026-08-28

Production evidence showed `safe_preview_v1` successfully resolving a
17,861-byte source to `plain_text/text_v2`, with its nearest `content_session`
lease created and released in the same second, but no delivery grant in the
surrounding window and a generic 503. The clean production schema was version 72;
migration 000066 admitted only `escaped_text/text_v1`. This localized the failure
to a cross-layer schema propagation gap at grant persistence.

### TDD transitions

All Go commands ran from `backend/`.

1. Real migration contract RED:
   `go test ./internal/database -run '^TestBackupAssetMigration073SQLite$' -count=1`
   failed all four initial contract subtests because migration 73 did not exist:
   `no migration found for version 73 ... file does not exist`.
2. Startup drift RED:
   `go test ./internal/database -run '^TestBackupAssetMigration073SQLite/CleanVersionMissingFinalContractIsRejected$' -count=1`
   returned a successful startup for falsely-clean version 73 instead of
   `ErrMigrationSchemaDrift`.
3. Release propagation RED:
   `go test ./internal/database -run '^TestBackupAssetMigration073ReleasePropagation$' -count=1`
   reported both the migration checker and PostgreSQL CI selector missing 000073.

The smallest fix is paired migration
`000073_backup_asset_plain_text_content` for both engines. It adds only the
`plain_text` renderer, `text_v2` profile, their exact `range_policy='none'`
pairing, and the matching bounded-representation arm. PostgreSQL replaces the
four named CHECK constraints. SQLite rebuilds the grant/request pair while
preserving request rows, six grant indexes, two request indexes, all three
Recovery Content triggers, the migration-69 Recovery downgrade trigger, and all
foreign keys. Both engines add downgrade admission, and the down body also has a
direct guard; any persisted `plain_text` or `text_v2` fact blocks downgrade
before metadata or schema changes.

The integration contract accepts the exact production-shaped 17,861-byte grant,
preserves legacy products and all unrelated schema/data invariants, and rejects
`plain_text/text_v1`, `escaped_text/text_v2`, plain text with a single-range
policy, inconsistent non-truncated representation bytes, and the pre-existing
invalid attachment-preview security product. Pristine down restores the exact
version-72 schema; used down remains clean at 73 and unchanged. Startup now checks
the 73 downgrade trigger and all four engine-specific constraint definitions,
with separate drift tests for missing checks and a missing trigger.

### Verification and blocker

- `GOTOOLCHAIN=go1.26.6 go test ./internal/database -run '^(TestBackupAssetMigration073SQLite|TestBackupAssetMigration073PairedFiles|TestBackupAssetMigration073ReleasePropagation|TestRunMigrationsClean072Applies073SQLite)$' -count=1` — GREEN (`ok`, 1.007s).
- `GOTOOLCHAIN=go1.26.6 go test ./internal/database -count=1` — GREEN (`ok`, 28.039s).
- `bash scripts/check-backup-asset-migration.sh` — GREEN; paired 71/72/73
  files, static constraint/trigger ownership, and SQLite used-down contracts pass.
- `bash scripts/check-migration-utc-safety.sh` and its self-test — GREEN; 92
  migration files scanned and all safety fixtures passed.
- `GOTOOLCHAIN=go1.26.6 go test ./...`, `go vet ./...`, and the server build —
  GREEN. Pinned golangci-lint 2.11.4 reports `0 issues.`
- `REQUIRE_POSTGRES_MIGRATION_TEST=1 go test ./internal/database -run '^TestBackupAssetMigration073Postgres$' -count=1` — BLOCKED, not skipped:
  `TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_MIGRATION_TEST=1`.
  The required PostgreSQL CI job now selects migration 73 and supplies both the
  DSN and required-test flag.
- `git diff --check` — GREEN.

No product content/broker/frontend/auth/catalog/repository code changed. No
commit, push, PR, merge, release, deployment, NAS, production, collector, or node
log action was performed by this hotfix.

### Independent hotfix review

The reviewer inspected the complete 066/069/070-073 migration chain and current
worktree rather than relying on the implementation handoff. Three gaps were fixed:

1. **Important — startup trusted only the 073 admission trigger name.** RED:
   `go test ./internal/database -run '^TestBackupAssetMigration073SQLite/CleanVersionMalformedAdmissionTriggerIsRejected$' -count=1`
   completed startup successfully after the real trigger was replaced by a
   same-named no-op. Startup now reads and validates the SQLite trigger definition
   and, for PostgreSQL, both the catalog-owned trigger definition and its actual
   trigger-function definition. Missing and malformed admission objects return
   sanitized typed schema drift without mutation. The focused 073 contract is
   GREEN with the added regression on both engine fixtures.
2. **Important — the database source-of-truth still ended at 000072.**
   `.trellis/spec/backend/database-guidelines.md` now records 000073 as latest,
   extends PostgreSQL parity through 73, and captures the exact
   `plain_text/text_v2/range_policy=none` and fail-closed downgrade contracts.
3. **Minor — CI propagation could pass from inert text.** The release regression
   now parses active `run:` commands, compiles their `-run` expression, and proves
   it actually selects `TestBackupAssetMigration073Postgres`. A dedicated unit
   table rejects comment-only and non-matching selectors.

Independent verification (all Go commands used `GOTOOLCHAIN=go1.26.6`):

- Focused SQLite/paired/release/startup selectors — GREEN with `-count=3`
  (`ok`, 3.264s); focused SQLite/startup `-race` — GREEN (`ok`, 3.037s).
- Full `internal/database` — GREEN (`ok`, 28.198s); full backend
  `go test ./... -count=1` — GREEN on final confirmation. One earlier full-backend run exposed an
  unrelated intermittent step-up proof test failure; its exact selector passed
  `-count=3` before the clean full rerun.
- `go vet ./...`, `go build ./...`, pinned `golangci-lint run ./...` (`0 issues`),
  migration checker, UTC checker/self-test, doc-freshness checker/self-test, and
  `git diff --check` — GREEN.
- Required PostgreSQL 073 remains BLOCKED, not skipped: with the DSN explicitly
  absent and `REQUIRE_POSTGRES_MIGRATION_TEST=1`, the test failed at fixture
  admission with `TEST_POSTGRES_DSN is required when
  REQUIRE_POSTGRES_MIGRATION_TEST=1`.

## Phase 13 — usable frame-height hotfix

Date: 2026-08-29

### Real-browser and component RED

- Node 22.23.1 Chromium command:
  `env -u NODE_ENV PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:/usr/local/bin:/usr/bin:/bin npm run e2e -- e2e/backup-assets-gate.spec.ts --project=chromium --grep "usable height"`
  failed the new geometry assertions before the product edit. Both ordinary split
  view and focused reading reported an iframe/viewport height ratio of `0.390625`,
  proving the browser's approximately 150px iframe fallback rather than a class-name
  proxy.
- Node 22.23.1 component command:
  `env -u NODE_ENV PATH=/home/murray/.nvm/versions/node/v22.23.1/bin:/usr/local/bin:/usr/bin:/bin npx vitest run src/features/backup-assets/asset-preview.test.tsx -t "dedicated stretching layout"`
  failed because the dedicated stretching frame layout did not exist.

### Minimal GREEN

`AssetPreview` now wraps only iframe-based text, compatibility-text, metadata/hex,
and PDF renderers in a `min-h-0` Flex child that stretches across the viewport's
cross axis. The iframe also uses Flex stretching instead of an unresolved percentage
height. Its title selection, empty sandbox, same-origin opaque URL, existing referrer
behavior, error renewal, ticket lifecycle, and renderer selection are unchanged.
Raster, audio, video, loading, empty, and error bodies retain the existing centered
native/status layout, with focused component coverage for both layout families.

### Verification

- The exact Chromium RED command above is GREEN after the layout edit. The same
  geometry regression is GREEN in Firefox; WebKit could not start locally because
  the host lacks its required ICU 66, libxml2, and libffi 7 shared libraries. The
  existing CI Playwright matrix remains the WebKit owner.
- Node 22.23.1 focused Vitest command:
  `npx vitest run src/features/backup-assets/asset-preview.test.tsx src/features/backup-assets/backup-file-split-pane.test.tsx src/features/backup-assets/backup-assets-workspace.test.tsx`
  — GREEN: 3 files / 106 tests.
- Node 22.23.1 `npm run typecheck` and `npm run lint` — GREEN.
- Node 22.23.1 `npm run check` — GREEN on the clean confirmation run: 190
  Vitest files / 1,747 tests with coverage plus the production build. An initial
  coverage run had one unrelated state-hook timing failure; its exact selector and
  the complete confirmation run both passed without an out-of-scope product edit.
- Added-production-line privacy scan found no direct `fetch`, browser storage,
  history/location, cookie, console/analytics/beacon writer, `URLSearchParams`,
  `unknown as`, or TypeScript `any` addition.
- `git diff --check` — GREEN.

No backend, API, schema, migration, authority, content transport, ticket, proof,
storage, analytics, NAS, collector, node-log, deployment, or production-data change
was made. Independent Trellis review, commit, delivery, release, and NAS verification
remain outside this implementation handoff.

### Independent Phase 13 quality review

The independent review found no product-code defect in the Flex correction. The
preview viewport remains the shared centered layout, while the frame-only wrapper
overrides cross-axis alignment and gives its iframe the resolved content-box block
size. Text v2, compatibility text, metadata/hex, and PDF share that branch; raster,
audio, video, loading, empty, and error bodies remain outside it. Iframe title,
empty sandbox, same-origin opaque URL ownership, referrer behavior, error renewal,
ticket lifecycle, responsive split/sequential ownership, and focus behavior are
unchanged.

Two test-quality findings were fixed:

1. **Important — the browser assertion permitted more blank space than the design
   contract and used one-shot geometry reads.** The 90-percent ratio could admit a
   growing gap on a tall viewport, and a future asynchronous layout transition could
   make the focused measurement timing-sensitive. The helper now polls the rendered
   geometry after each layout state, derives the intentional vertical inset from the
   viewport's computed padding, and requires the iframe to match the remaining
   content-box height within one CSS pixel.
2. **Minor — the new centered native-renderer table left audio/video mounted until
   after the media mock was restored.** Focused Vitest still passed but jsdom printed
   two `HTMLMediaElement.load` not-implemented errors during cleanup. Each case now
   unmounts explicitly while the mock is active; the same 106-test gate is clean.

Fresh reviewer verification used Node 22.23.1 with `NODE_ENV` unset:

- Chromium and Firefox usable-height geometry repeated five times per browser:
  GREEN, 10/10. Every split and focused-reading measurement matched the computed
  viewport content-box height within one CSS pixel.
- WebKit remained externally blocked before test execution by the host's missing
  ICU 66, libxml2, and libffi 7 shared libraries. No dependency was installed; the
  required CI three-browser matrix remains the WebKit owner.
- Focused preview/split/workspace Vitest: GREEN, 3 files / 106 tests, with no media
  cleanup error output.
- Standalone TypeScript typecheck and ESLint: GREEN.
- Full `npm run check`: GREEN — 190/190 Vitest files, 1,747 tests with coverage,
  and the 3,230-module production build.
- Bundle budget: GREEN — main JS 493.85/500.00 KiB and main CSS
  108.91/110.00 KiB.
- Task context validation: GREEN, 19 implementation and 15 check entries; only the
  established context-injection size notices remain.
- Added production lines contain no direct fetch, storage/history/cookie,
  console/analytics/beacon, URL rewrite, `unknown as`, or TypeScript `any` sink.
  No backend, dependency, lockfile, schema, migration, deployment, collector, or
  node-log path changed. Final `git diff --check` is GREEN.

No Critical or Important finding remains. Phase 13 is ready for the main-session
Trellis spec update and commit/delivery workflow; CI must supply the WebKit result
before merge.

### Main-session spec and pre-commit verification

- The reusable Flex/iframe contract and real-browser geometry requirement are now
  recorded in `.trellis/spec/frontend/quality-guidelines.md`; no API, schema, or
  cross-layer signature changed.
- Fresh Node 22.23.1 `npm run e2e -- e2e/backup-assets-gate.spec.ts
  --project=chromium --project=firefox --grep "usable height"` — GREEN: 2/2.
- Fresh Node 22.23.1 `npm run check` — GREEN: typecheck and lint exited 0; all
  190 Vitest files / 1,747 tests passed with coverage; the production build
  transformed 3,230 modules and exited 0.
- `git diff --check` — GREEN. No production asset identity or content was recorded.
