# Unified Backup File Center And Direct Preview Implementation Plan

> **For Trellis workers:** REQUIRED SUB-SKILLS: load trellis-before-dev before
> product edits, use test-driven-development for every behavior slice, and use
> the curated implement/check manifests. Track every step with its checkbox.

**Goal:** Build a daily backup file center with conditional Backup Sets,
single-activation preview, faithful readable text, safe native/binary rendering,
and a 42/58 resizable desktop workspace without widening authority.

**Architecture:** Add a sanitized read-only Catalog source projection and a
closed safe_preview_v1 ticket-selection intent. The backend resolves the exact
plain-text/native/hex product; the existing Issue/Serve grant boundary remains
final. The frontend composes the projection, route, browser, split pane, and
latest-selection state machine without persisting sensitive context.

**Production remediation:** Phases 1-7 are released history. `v0.52.0`
infrastructure acceptance passed but product acceptance failed. Phases 8-12 are the
approved remediation covering core text delivery, a 45-minute action-scoped session
proof, backup-bearing source discovery, and parent navigation. The user explicitly
approved implementation after reviewing the completed revision; delivery and
production actions remain separately gated.

**Tech Stack:** Go 1.26, Gin, GORM, React 18, TypeScript 5.8 strict, Vite 7,
Vitest, Tailwind CSS, Radix/shadcn primitives, and i18next.

---

## Preconditions and phase gate

- [x] Work only in /home/murray/.codex/worktrees/e0a9/xirang on
  codex/backup-file-center-production-remediation.
- [x] Confirm task.json was planning and the user explicitly approved
  implementation after reviewing the completed PRD/design/plan.
- [x] Only then run task.py start and load every entry in implement.jsonl before
  editing product code.
- [x] Reconfirm main/origin-main baseline and preserve unrelated/user-owned
  changes, especially .codex/agents/trellis-research.toml.
- [x] Keep node-log P1 stopped and production collectors at zero.

## Phase 1 — File-source projection RED and backend contract

- [x] Add production-shaped Catalog fixtures covering one node/one task, one
  node/multiple tasks, a task spanning repositories, task-less/imported lineage,
  ownership filtering, partial Catalog state, and more than one cursor page.
- [x] Add service REDs for three bounded read-only views: nodes, node Backup Sets,
  and Backup Set versions.
- [x] Prove the released repository-first APIs cannot satisfy the projection RED
  without incomplete first-page/N+1 behavior; record the selector and failure in
  research/implementation-evidence.md.
- [x] Implement the smallest Catalog projection service with deterministic
  server-owned backupSetId, signed scoped cursors, safe DTO validation, and
  captured/committed/created/opaque ordering.
- [x] Keep task-backed sets isolated by node+task and task-less sets isolated by
  authoritative repository lineage. Add negative tests proving no cross-task or
  cross-repository task-less merge.
- [x] Add authenticated/RBAC-protected handlers and router registrations for
  /backup-file-sources/nodes, /nodes/:nodeId/sets, and
  /sets/:backupSetId/versions. Use standard response helpers.
- [x] Add full-router Admin/Operator/Viewer/unknown and ownership tests, feature
  disabled/unavailable/stale cursor/projection-limit tests, zero Provider-call
  spies, and response canaries proving no locator/path/content/credential fields.
- [x] Update handler annotations and regenerate Swagger after the route contract
  is GREEN. No database migration is expected; stop and amend the design before
  adding one.

Planned files (adjust only when repository evidence requires an owning-file
equivalent, and record the change):

- backend/internal/backupasset/catalog/contracts.go
- backend/internal/backupasset/catalog/service.go
- a focused catalog file-source projection module and tests
- backend/internal/api/handlers/backup_file_source_handler.go and tests
- backend/internal/api/router.go and router tests
- backend/internal/api/docs generated Swagger files

## Phase 2 — Typed frontend projection and canonical Files shell

- [x] Add frontend API raw types/mappers for node, Backup Set, and version pages.
  Reject malformed IDs, enums, times, cursors, or partial shapes rather than
  guessing.
- [x] Add domain types and a pure grouping/selection projection with tests for one
  hidden set, multiple visible sets, task-less isolation, deterministic versions,
  blocked/partial states, and no internal vocabulary in primary labels.
- [x] Add nodeId and backupSetId to the validated opaque route model. Preserve
  existing repository/recoveryPoint/task/entry deep links and add mismatch tests
  that clear descendants without selecting a replacement.
- [x] Change /app/backups to default to /app/backups/data and order the local tabs
  Files, Overview, Recovery. Keep exactly one first-level Backups nav item.
- [x] Recompose the Files surface into source controls, browser/breadcrumb, large
  preview region, and optional details drawer. Remove Repository/Recovery Point
  as required first steps without deleting their support context or lifecycle
  surfaces.
- [x] Add a focused BackupFileSplitPane component with 42/58 initial columns,
  20rem/30rem minimums, pointer drag, role=separator keyboard adjustment in
  two-percentage-point steps, visible focus, and no new layout dependency.
- [x] Keep ratio/focused mode transient. Add focused-reading enter/exit with prior
  ratio, selected row, scroll, and trigger-focus restoration; prove route,
  localStorage, sessionStorage, analytics, and logs receive no presentation or
  content state.
- [x] Implement the shared sequential browser -> full-width preview fallback when
  both desktop minimums cannot fit. Preserve existing
  list/search/favorite/tag/evidence capabilities only where they fit the approved
  shell; do not redesign unrelated download/export/recovery flows.
- [x] Run focused route/page/workspace tests to GREEN before proceeding.

Planned files (adjust only when repository evidence requires an owning-file
equivalent, and record the change):

- web/src/navigation.ts
- web/src/router.tsx and web/src/router-pages.tsx
- web/src/pages/backups-page.tsx and tests
- web/src/features/backup-assets/backup-assets-route.ts and tests
- web/src/features/backup-assets/backup-assets-workspace.tsx and tests
- web/src/features/backup-assets/backup-file-split-pane.tsx and tests
- a focused file-source selector/projection module and tests
- web/src/lib/api/backup-file-sources-api.ts and tests
- web/src/types/domain.ts
- web/src/i18n/locales/zh.ts and en.ts

The split-pane boundary should remain presentation-only:

    interface BackupFileSplitPaneProps {
      browser: ReactNode;
      preview: ReactNode;
      previewLabel: string;
      narrow: boolean;
      previewActive: boolean;
      onLeavePreview: () => void;
    }

Selection, AssetRef, ticket, proof, and route state stay in
useBackupAssetsState/route ownership and are not accepted by this component.

## Phase 2.5 — Exact legacy recovery-point source resolution

- [x] Add a Catalog RED for resolving one opaque recoveryPointId through the
  existing bounded authorized projection into its exact nodeId, backupSetId,
  repositoryId, optional producingTaskId, and safe retained-version facts in one
  pass. Cover stale/unauthorized 404-equivalent results, collision/invalid-state
  failure, candidate limit, and zero Provider-capable dependencies.
- [x] Add the authenticated backup_assets:list GET resolver handler, standard
  response/error/audit mapping, full-router Admin/Operator/Viewer/unknown and
  ownership tests, Swagger annotations, README route documentation, and response
  privacy canaries. Add no schema migration and no Provider read.
- [x] Add a strict typed frontend resolver mapper and AbortSignal-aware API
  method. Reject malformed IDs, contradictions, and invalid optional task facts;
  discard private extras atomically at the API boundary.
- [x] When a legacy route has recoveryPointId but lacks nodeId or backupSetId,
  resolve once, validate any repositoryId/taskId, patch the exact authorized
  source, then continue normal paging/version selection. On mismatch, stale,
  unauthorized, abort, or superseded completion, clear incompatible descendants
  without guessing or scanning every set.
- [x] Prove modern node/set routes issue no resolver request; cover exact legacy
  resolution, repository/task mismatch, 404/permission/malformed response,
  cancellation/latest-result semantics, route privacy, and no N+1 behavior.
- [x] Run focused backend/frontend gates, generated Swagger/doc freshness,
  RBAC/privacy/dependency selectors, Node 22 typecheck/lint/build coverage, and
  git diff --check. Record exact RED/GREEN commands in implementation evidence,
  then freeze before Phase 3.

## Phase 3 — Server preview-intent RED and resolution

- [x] Add strict handler/API REDs for the discriminated request union:
  preview+safe_preview_v1, existing exact preview product, and existing exact
  download product. Reject unknown intent, mixed intent+renderer/profile, missing
  pairs, auto download, and extra fields.
- [x] Add broker/renderer REDs for generic-MIME UTF-8 and UTF-16 configuration,
  JSON, YAML, TOML, log, and source fixtures resolving to plain_text/text_v2.
- [x] Add content-fidelity REDs proving angle brackets, ampersands, quotes,
  Unicode, tabs, and retained line endings remain readable decoded characters,
  not HTML entities or hex output.
- [x] Add negative REDs for deceptive text extensions with binary/control bytes
  resolving to metadata_hex/hex_v1, plus zero-length text.
- [x] Add native REDs for generic-MIME signature-proven raster/PDF/audio/video,
  active HTML/XML/SVG escaping, MIME confusion, pixel/size limits, missing Range,
  and a native signature that must not silently fall back to hex.
- [x] Extend IssueRequest with a closed preview selection intent distinct from
  DeliveryProduct. Add the exact plain_text/text_v2 product while keeping
  escaped_text/text_v1 and the exact request path backward compatible.
- [x] Resolve the exact renderer/profile after one bounded prefix read and current
  sensitivity classification, then compute Range, call renderer.Prepare, validate
  the product, and only then persist the grant.
- [x] Ensure derived representations remain explicit and are not triggered by the
  safe preview intent.
- [x] Keep Admin secret/unknown step-up and Operator denial exact. Retry the same
  intent once after a typed Admin proof; never downgrade classification or infer
  proof from renderer.
- [x] Extend ticket descriptor with the existing RenderPlan truncation boolean and
  validate it through handler/client contracts.
- [x] Make success grants/audits/Serve/cache contain only resolved existing
  renderer/profile values. Add failure-audit tests proving unresolved intent is
  represented only as a safe closed intent/failure code and no fake delivery
  product or private evidence is written.
- [x] Map renderer unsupported/MIME confusion to the standard HTTP 422 envelope
  with exact reason preview_renderer_unsupported. Preserve existing transport,
  capability, rate-limit, and generic internal error products.

Planned files (adjust only when repository evidence requires an owning-file
equivalent, and record the change):

- backend/internal/backupasset/content/contracts.go and tests
- backend/internal/backupasset/content/renderer.go and tests
- backend/internal/backupasset/content/broker.go and tests
- backend/internal/api/handlers/backup_content_handler.go and tests
- backend/internal/api/router tests and generated Swagger
- model/audit validation tests only if the safe failure-audit shape requires it

## Phase 4 — Frontend direct-preview state machine

- [x] Replace MIME-based ordinary preview choice with safePreviewV1 in the typed
  API input union. Keep exact product requests for renew/download/explicit
  compatibility callers.
- [x] Update the response mapper: auto input accepts only one valid resolved
  existing preview product; exact input still requires an exact matching product;
  both validate content type, Range, classification, expiry, URL, and truncation.
- [x] Add a genuine UI RED: activating one generic-MIME text row must immediately
  issue exactly one safe-preview request without a Load Preview click.
- [x] Extend the existing reducer/latest-request coordinator with a started-key
  guard keyed by generation+AssetRef+intent+attempt. Prove one call under React
  StrictMode/effect replay.
- [x] On every newer file/node/set/version/directory selection, abort the old issue,
  detach old content, unmount native renderers, clear stale state/proof as designed,
  and ignore late results.
- [x] Remove the first-run Load Preview action. Show manual retry only for the
  current typed failed/expired/stale state. Renewal uses the current resolved exact
  product and exact AssetRef.
- [x] Preserve the existing Admin step-up helper and in-memory proof ownership.
  Test Admin one-prompt/one-retry, Operator no prompt, rejected proof, logout/token
  change, and no proof persistence.
- [x] Render faithful plain text, compatibility escaped text, hex, image, PDF,
  audio, and video through safe components. Add a clear bounded-preview
  truncation notice.

Planned files (adjust only when repository evidence requires an owning-file
equivalent, and record the change):

- web/src/lib/api/backup-content-api.ts and tests
- web/src/features/backup-assets/use-backup-assets-state.ts and tests
- web/src/features/backup-assets/asset-preview-model.ts and tests
- web/src/features/backup-assets/asset-preview.tsx and tests
- web/src/features/backup-assets/backup-assets-workspace.tsx and tests
- web/src/types/domain.ts and i18n files

## Phase 5 — Responsive, accessibility, and boundary UX

- [x] Add pointer plus Enter/Space activation tests without nested interactive-row
  conflicts.
- [x] Add labels, selected/current states, visible focus, loading/error live
  regions, and deterministic focus order for source controls, breadcrumb, browser,
  preview, retry, and details.
- [x] Test desktop selection keeps browser focus; mobile preview focuses its
  heading and Back restores the originating file when present.
- [x] Test supported breakpoints, touch targets, 200% zoom layout, reduced motion,
  power-saving behavior, long localized labels, empty/partial/blocked states, and
  large bounded text.
- [x] Test the 42/58 default, pointer and keyboard resize, two-percentage-point
  steps, 20rem/30rem clamps, separator semantics, sequential fallback, focused
  reading, exact focus/scroll/ratio restoration, and reload reset. Never persist
  filenames, paths, content, or ticket data.
- [x] Add a top-level Files axe smoke and focused component axe tests where the
  test harness supports them.
- [x] Confirm filenames and authorized display metadata remain visible only in the
  intended UI, never in errors/logs/route fields beyond existing opaque IDs.

## Phase 6 — Focused and full verification

Run the smallest selector after every RED/GREEN slice. Once all phases are GREEN,
run and record exact output in research/implementation-evidence.md:

    cd backend && go test ./internal/backupasset/catalog ./internal/api/handlers -run 'BackupFileSource|FileSource|BackupSet' -count=1
    cd backend && go test ./internal/backupasset/content ./internal/api/handlers -run 'PreviewIntent|SafePreview|Renderer|ContentTicket' -count=1
    cd backend && go test ./internal/backupasset/catalog ./internal/backupasset/content ./internal/api/handlers -count=3
    cd backend && go test -race ./internal/backupasset/catalog ./internal/backupasset/content ./internal/api/handlers -run 'BackupFileSource|PreviewIntent|SafePreview|DirectPreview|ContentTicket' -count=1
    cd web && env -u NODE_ENV npx vitest run src/pages/backups-page.test.tsx src/features/backup-assets src/lib/api/backup-file-sources-api.test.ts src/lib/api/backup-content-api.test.ts
    cd web && env -u NODE_ENV npm run check
    cd backend && go test ./...
    cd backend && go build ./...
    bash scripts/check-doc-freshness.sh
    make check
    git diff --check

Use Node 22 for all web commands. If final filenames/selectors differ, record the
actual focused commands rather than preserving stale placeholders.

Privacy and compatibility checks:

- [x] Scan modified web code for direct fetch, localStorage, sessionStorage,
  untyped history/location writes, raw snake_case component use, any, unknown-as
  casts, and content/ticket/proof persistence.
- [x] Scan modified backend/API/docs/tests for Provider locator, raw backup path,
  content bytes, token/Cookie/proof/TOTP, private request evidence, command/output,
  or production asset identity.
- [x] Serialize positive/negative source and ticket responses with canaries and
  prove only approved safe fields occur.
- [x] Prove old exact preview tickets, original download, Export, Archive,
  Recovery, transport settings, Catalog authority, and node-log settings remain
  compatible and unchanged outside the explicit additive contract.
- [x] Record git diff --stat, intended paths, generated Swagger freshness, and
  absence of an unexpected migration.

## Phase 7 — Independent check, PR, release, and production acceptance

- [x] After implementation gates pass, run an independent Trellis check against
  check.jsonl. Resolve Critical/Important findings on the same branch and rerun
  affected plus full gates.
- [x] Commit, push, open a ready PR, and monitor every required CI job. Do not
  merge while any required job is failing, pending, or missing.
- [x] Squash merge when green, sync local main, then monitor Release Please,
  GitHub Release, Docker image publication, and relevant post-merge workflows.
- [x] Prepare a recoverable NAS upgrade/rollback runbook that uses root
  /volume2/docker/xirang, external 19927, internal 10761, and no test, [, [[, cd,
  su, or sudo commands. Do not request or print secrets or content identity.
- [ ] Production acceptance must prove over the authorized LAN HTTP path: one
  representative generic-MIME text/config asset is readable, one real binary
  remains non-text, one native format still renders safely, one file activation
  starts preview without a second click, rapid switching never flashes stale
  content, typed errors remain bounded, and health/DB/log checks remain clean.
- [ ] Keep collectors at zero throughout. Only after usable-content acceptance
  succeeds may the separately planned node-log P1 be considered for its own
  explicit start approval.

`v0.52.0` was subsequently released and infrastructure acceptance passed, but the
usable-content production acceptance above failed. Do not mark those acceptance
items complete. Continue only through the newly approved remediation phases below.

## Phase 8 — Production-shaped core preview diagnosis and repair

- [x] Create the first genuine RED through Repository Service plus each supported
  Restic/Rsync/Rclone source adapter, Content Broker safe_preview_v1, handler Issue,
  persisted grant, and Serve using synthetic generic-MIME text/config fixtures.
- [x] Prove core UTF-8/UTF-16 text preview succeeds without any derived Worker port;
  cover short/exact/truncated content, retained line endings, HTML-inert text,
  malformed text, true binary hex, and signature-proven native files.
- [x] Add closed non-leaking source failure stages for open, read, changed, timeout,
  cancellation, and capability. Preserve standard envelopes, empty params, and a
  safe correlation ID; never expose raw Provider errors or identity.
- [x] Reproduce the production-shaped 503 at the narrowest layer, record the exact
  RED, and implement only the smallest proven source/descriptor/handler fix. Do
  not guess the Provider root cause from the screenshot.
- [x] Restrict optional Worker guidance to typed derived-processing Worker states.
  Core preview unavailable/capability/renderer errors receive their own localized
  guidance and never imply that a Worker is required.
- [x] Prove exactly one bounded source open/read, exact Issue/grant/Serve product
  agreement, cancellation, audit/log sanitization, and no locator/path/content/
  proof leakage.

## Phase 9 — 45-minute action-scoped secret-reveal session

- [x] Add backend REDs for per-action proof TTL policy: `asset.secret_reveal` is
  exactly 45 non-sliding minutes; every unrelated action preserves its existing
  TTL and one-shot/reuse contract.
- [x] Bind and revalidate the proof to purpose, exact action, user, role, token
  version/session, issued-at, and expiry. Cover tamper, expired, logout/revocation,
  user/role/token-version change, and future/unknown action failure.
- [x] Replace file-center one-shot calls with the central helper's
  `persist: true, reuseCached: true` path. Remove per-asset/source proof ownership
  and never clear a valid proof merely because selection changes.
- [x] RED/GREEN Admin first prompt then cross-file/directory/version/node reuse,
  page-refresh reuse, exact 45-minute expiry, rejected-cache clear plus at most one
  fresh prompt, retry-loop prevention, Operator/Viewer no prompt, and ordinary
  non-secret no prompt.
- [x] Keep only action+proof+expiry in current-login `sessionStorage`; prove no
  localStorage, URL/history, analytics, console, log, or content-ticket persistence.

## Phase 10 — All backup-bearing source lineages

- [x] Add RED fixtures for active, interrupted, disabled, archived, and deleted
  task lineages whose provider-proven retained bytes remain, plus configured
  no-data tasks and ambiguous/unattributable candidates.
- [x] Extend the bounded projection with closed `browsable`, `indexing`, and
  `unavailable` states. Task status must not gate retained-data visibility; no-data
  tasks stay absent and incomplete discovery is never reported as empty.
- [x] Reuse TaskRepositoryLink/RecoveryPoint snapshots and the existing
  import/rebuild lifecycle. Add a cursor-bounded asynchronous managed-lineage
  reconciler that repairs Provider-committed/interrupted publications only when
  exact managed provenance agrees; ambiguous data remains quarantined/import-only.
- [x] Keep Files HTTP listing database-only. Prove zero Provider calls from the
  selector, fixed/bounded calls from reconciliation, no N+1, cancellation,
  retry/backoff, cursor stability, and projection digests over all visible facts.
- [x] Preserve Admin/list RBAC and recheck Operator against current node ownership.
  Add cross-user, archived-task, deleted-task, missing-node, identity-collision,
  malformed-evidence, offline-repository, and privacy canaries.
- [x] Use existing schema if it can express durable attribution truth. If the RED
  proves otherwise, pause product edits, amend this design with the exact minimal
  model, and add paired SQLite/PostgreSQL migration/parity/rollback tests.
- [x] Update strict frontend DTOs and source controls so all authorized nodes/sets
  are paged and reachable, with clear indexing/unavailable states and no internal
  Repository/Provider vocabulary in the primary workflow.

## Phase 11 — Explicit Up navigation and revised browser UX

- [x] Extend the entry-page contract with required current directory, parent, and
  breadcrumb context; return it for root, non-root, empty, and every cursor page.
- [x] Validate same-point/generation identity, directory type, bounded acyclic
  ancestry, stale cursors, and strict frontend mapping. No raw/normalized path is
  added to the API or route.
- [x] Replace the standalone Root button with a native 44px Up button. Disable or
  omit Up at root; retain breadcrumb root/ancestor jumps.
- [x] On Up or breadcrumb navigation, abort/detach preview, clear incompatible
  selected/bulk descendants, ignore late results, preserve list origin where
  valid, and restore deterministic focus on desktop and mobile.
- [x] Add pointer, Enter/Space, empty-directory, deep-link/reload, rapid navigation,
  list/grid, 390px/desktop/200%-zoom, reduced-motion, screen-reader, and axe tests.
- [x] Update localized source/preview/navigation/error copy. Prove Worker guidance
  appears only for explicit derived Worker states.

## Phase 12 — Independent verification, release, and production re-acceptance

- [x] Run focused backend provider/content/catalog/handler selectors after every
  RED/GREEN slice, then `-count=3`, focused `-race`, `go test ./...`, build, vet,
  pinned golangci-lint, Swagger deterministic regeneration, doc freshness, and
  SQLite/PostgreSQL parity where affected.
- [x] Run Node 22 focused API/hook/browser/preview/page tests, typecheck, lint,
  `npm run check`, production build, Playwright Chromium/Firefox/WebKit, supported
  viewports/zoom, accessibility, and privacy/type-escape scans.
- [x] Run `make check`, dependency/migration/scope scans, response canaries, and
  `git diff --check`; complete an independent Trellis review and resolve every
  Critical/Important finding before PR.
- [ ] Commit/push/open a ready PR only after the user separately authorizes
  implementation delivery; monitor all required CI, squash merge, sync main, and
  monitor Release Please, GitHub Release, Docker publication, and post-merge jobs.
- [ ] Upgrade the NAS only through the approved recoverable protocol. Reconfirm
  health 200/200, schema/integrity, clean bounded logs, active runs 0, and
  collectors 0 without printing any sensitive identity or content.
- [ ] Real production product acceptance must prove: readable representative core
  text without Worker, true binary non-text, native preview, one step-up followed
  by 45-minute cross-file/refresh reuse, every known backup-bearing lineage visible
  with truthful state, Up navigation including an empty directory, rapid-switch
  cancellation, and bounded non-leaking errors.
- [ ] Keep collectors at zero after acceptance evidence is reviewed. Node-log P1
  still requires its own explicit start approval; this task never starts it.

## Phase 12 hotfix — plain-text grant schema propagation

- [x] Reconcile the production `safe_preview_v1` evidence with schema version 72
  and prove the persisted grant boundary, rather than the source read or lease,
  rejects the resolved `plain_text/text_v2` product.
- [x] Add a real migration RED for the exact 17,861-byte
  `plain_text/text_v2/range=none` grant and invalid cross-product combinations.
- [x] Add paired forward/down migration 000073 for SQLite and PostgreSQL. Preserve
  every other grant/request constraint, row, index, Recovery trigger, function,
  and foreign key; block used downgrade before metadata or schema mutation.
- [x] Make startup reject falsely-clean version 73 schemas missing either the
  exact grant checks or the downgrade admission trigger, including a same-named
  trigger/function whose body no longer enforces the used-downgrade guard.
- [x] Propagate 000073 into the migration checker and required PostgreSQL CI
  selector, with a static regression owner for both paths.
- [x] Run focused SQLite forward/invalid/used-down/pristine-down checks, migration
  checker/UTC gates, full database/backend tests, vet, build, lint, and diff checks.
- [ ] Execute the required PostgreSQL migration contract locally. This remains
  blocked because `TEST_POSTGRES_DSN` is absent; the fail-closed owner was run with
  `REQUIRE_POSTGRES_MIGRATION_TEST=1` and failed rather than skipping. Required CI
  is configured to execute `TestBackupAssetMigration073Postgres` against PostgreSQL.

## Phase 13 hotfix — usable frame height

- [x] Add the first genuine RED in a real browser: a ready frame-based preview must
  consume the available preview viewport height in ordinary desktop and focused
  reading rather than remain at the browser's approximately 150px fallback height.
- [x] Add focused component coverage that distinguishes stretching frame products
  from centered image/audio/video/loading/error bodies without asserting only a
  misleading `h-full` class.
- [x] Implement the smallest `AssetPreview` layout correction. Preserve iframe
  title, sandbox, referrer policy, same-origin URL ownership, renderer selection,
  ticket lifecycle, and every security/privacy boundary.
- [x] Run the focused preview/split/workspace tests, the real-browser regression,
  Node 22 typecheck/lint/full frontend check, privacy scan, and `git diff --check`.
- [x] Record RED/GREEN evidence without production asset identity or content, then
  dispatch an independent Trellis check before commit/delivery.

## Risky boundaries and rollback

- File-source identity/ownership: wrong grouping could expose or merge another
  task's history. Roll back the new projection and Files consumer together.
- Content broker product resolution: a wrong precedence could execute active
  content or misclassify binary. Keep exact products and renderer final
  validation; image rollback restores the old exact-request behavior.
- Audit/grant validation: never persist an unresolved intent as a renderer.
- React orchestration: effect replay or late completion can duplicate issues or
  flash stale content. Generation, AbortSignal, detachment, and started-key tests
  are release gates.
- Canonical route compatibility: preserve existing opaque deep links and clear
  mismatches without guessing.
- Responsive shell: details must not reclaim the preview width or break mobile
  focus restoration.

No destructive data migration is planned. For Phases 8-12, bounded Provider reads
are authorized only inside the approved diagnostic integration tests and
asynchronous managed-lineage reconciler described above, never in an interactive
source-list request. Any Provider read outside those boundaries, or any schema
change not first justified by the Phase 10 RED and approved as an amended plan,
requires an immediate stop and new explicit approval.
