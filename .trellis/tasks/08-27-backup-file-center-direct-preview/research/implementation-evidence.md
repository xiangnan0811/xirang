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
