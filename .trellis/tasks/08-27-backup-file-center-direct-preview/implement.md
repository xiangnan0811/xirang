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

**Tech Stack:** Go 1.26, Gin, GORM, React 18, TypeScript 5.8 strict, Vite 7,
Vitest, Tailwind CSS, Radix/shadcn primitives, and i18next.

---

## Preconditions and phase gate

- [x] Work only in /home/murray/.codex/worktrees/e0a9/xirang on
  codex/backup-file-center-direct-preview.
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
- [ ] Squash merge when green, sync local main, then monitor Release Please,
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

No destructive data migration is planned. If implementation discovers that a
schema change or Provider read is required, stop, update PRD/design, and obtain a
new explicit plan approval before expanding scope.
