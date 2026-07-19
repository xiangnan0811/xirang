# Backup Assets Workspace UI Implementation Plan

> **Execution mode:** inline only. Do not create implementation or check
> sub-agents. After explicit planning approval and separate implementation
> authorization, load `trellis-before-dev`, `frontend-design`,
> `superpowers:test-driven-development`, `superpowers:executing-plans`, and the
> `browser` skill. Before completion claims, load `trellis-check` and
> `superpowers:verification-before-completion`; finish with
> `trellis-finish-work`.

**Goal:** Deliver the current-main-compatible Backups nested routes and a
responsive, privacy-safe core asset workspace using merged Catalog, Search,
Overlay, Evidence/Diff, and Content Broker contracts, while leaving unsupported
future capabilities truthful and unavailable.

**Architecture:** Pure route and preference codecs own durable browser state. A
route-local controller reducer composes typed resource APIs, one abort/sequence/
key guard per request channel, shared virtual result state, focus/scroll
restoration, and an in-memory ticket/overlay lifecycle. Desktop renders a stable
unframed three-track workspace; intermediate/mobile render a reversible
context/results/inspector flow.

**Tech stack:** React 18, TypeScript 5.8 strict, React Router 7, Tailwind,
existing shadcn-style primitives, Lucide, i18next, TanStack Virtual, MSW,
Vitest/Testing Library, vitest-axe, Chrome CDP browser skill.

## 1. Execution Gate

### 1.1 Current state

```text
task:       .trellis/tasks/07-19-backup-assets-workspace-ui
status:     in_progress; implementation and local verification complete
plan review: approved by user on 2026-07-19
scope path:  buildable frontend core + truthful unavailable states
branch:     codex/backup-assets-workspace-ui
PR base:    main
worktree:   /home/murray/.codex/worktrees/bb05/xirang
baseline:   b744b116c6a11ef02998d6182d372e9efe97abc2
start:      executed on 2026-07-19
implementation authorization: approved by user on 2026-07-19
product:    executed
tests:      focused/full/browser passing
delivery:   pending
```

The user approved `prd.md`, `design.md`, `implement.md`, the recommended
scope-gate disposition, and separately authorized implementation on
2026-07-19. The approved-start preflight, `task.py start`, exact staging, and
the work commit have now run; archive and later delivery steps remain pending.

### 1.2 Future approved-start preflight

After authorization, reload the skills/spec context and run the following
read-only checks before `task.py start`:

```bash
cd /home/murray/.codex/worktrees/bb05/xirang
git fetch origin main
test "$(git branch --show-current)" = "codex/backup-assets-workspace-ui"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
test "$(git merge-base HEAD origin/main)" = "$(git rev-parse origin/main)"
git status --short
```

Expected pre-start changes are only the parent child registration and active
Child 9 planning directory. If `origin/main` moved, rebase/rebuild the branch
from the latest merged main using the repository workflow, re-audit Children
6-8 contracts, and amend/re-review the planning package. Do not copy from an
unmerged sibling or detached worktree.

Only after both approvals and a valid preflight run:

```bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-19-backup-assets-workspace-ui
```

If any G1-G8/G10 backend/API expansion is approved, update Sections 2-15 and
obtain focused amendment approval before start. No migration is owned by this
child; `000067...000071` remain untouched.

## 2. Exact File Manifest

Any product/spec file outside this manifest requires a focused plan amendment
and approval before edit. Do not use directory-wide or wildcard staging.

### 2.1 Initial create plan (superseded by final manifest below)

```text
web/src/pages/backups-page.overview.tsx
web/src/pages/backups-page.data.tsx
web/src/pages/backups-page.recovery.tsx
web/src/pages/__tests__/backups-page.a11y.test.tsx

web/src/features/backup-assets/backup-assets-route-state.ts
web/src/features/backup-assets/backup-assets-route-state.test.ts
web/src/features/backup-assets/backup-assets-preferences.ts
web/src/features/backup-assets/backup-assets-preferences.test.ts
web/src/features/backup-assets/backup-assets-state.ts
web/src/features/backup-assets/backup-assets-state.test.ts
web/src/features/backup-assets/use-backup-assets-state.ts
web/src/features/backup-assets/use-backup-assets-state.test.tsx
web/src/features/backup-assets/backup-assets-presenters.ts
web/src/features/backup-assets/backup-assets-presenters.test.ts

web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/features/backup-assets/asset-context-panel.tsx
web/src/features/backup-assets/asset-context-panel.test.tsx
web/src/features/backup-assets/asset-browser.tsx
web/src/features/backup-assets/asset-browser.test.tsx
web/src/features/backup-assets/asset-list.tsx
web/src/features/backup-assets/asset-list.test.tsx
web/src/features/backup-assets/asset-grid.tsx
web/src/features/backup-assets/asset-grid.test.tsx
web/src/features/backup-assets/asset-search.tsx
web/src/features/backup-assets/asset-search.test.tsx
web/src/features/backup-assets/asset-bulk-bar.tsx
web/src/features/backup-assets/asset-bulk-bar.test.tsx
web/src/features/backup-assets/asset-inspector.tsx
web/src/features/backup-assets/asset-inspector.test.tsx
web/src/features/backup-assets/asset-preview.tsx
web/src/features/backup-assets/asset-preview.test.tsx
web/src/features/backup-assets/asset-overlays.tsx
web/src/features/backup-assets/asset-overlays.test.tsx
web/src/features/backup-assets/asset-evidence.tsx
web/src/features/backup-assets/asset-evidence.test.tsx
web/src/features/backup-assets/asset-versions.tsx
web/src/features/backup-assets/asset-versions.test.tsx

web/src/features/backup-assets/__tests__/handlers.ts
web/src/features/backup-assets/__tests__/test-utils.tsx
web/src/lib/api/__fixtures__/backup-assets.fixture.json

web/src/lib/api/backup-assets-error.ts
web/src/lib/api/backup-assets-error.test.ts
web/src/components/snapshot-search.test.tsx
```

Component files may remain smaller than the parent plan's conceptual list, but
none may absorb a second domain merely to reduce file count. If implementation
proves one listed file unnecessary, remove it from the reviewed manifest before
staging rather than creating an empty/pass-through file.

### 2.2 Initial modify plan (superseded by final manifest below)

```text
web/src/pages/backups-page.tsx
web/src/pages/backups-page.test.tsx
web/src/router.tsx
web/src/router-pages.tsx

web/src/components/layout/navigation.ts
web/src/components/layout/navigation.test.ts
web/src/components/ui/tree.tsx
web/src/components/ui/__tests__/tree.test.tsx

web/src/components/snapshot-browser.tsx
web/src/components/snapshot-browser.test.tsx
web/src/components/snapshot-search.tsx
web/src/components/task-run-detail.tsx
web/src/components/task-run-detail.test.tsx
web/src/pages/tasks-page.dialogs.tsx
web/src/pages/tasks-page.test.tsx

web/src/lib/api/client.ts
web/src/lib/api/client.test.ts
web/src/i18n/locales/zh.ts
web/src/i18n/locales/en.ts
```

`navigation.ts` keeps exactly one Backups item. Legacy components gain only a
task-context compatibility link and retain their existing browse/search/restore
behavior. `task-run-detail` must not turn a raw drill `snapshotRef` into an
exact asset link.

### 2.3 Initial workflow plan (superseded by final manifest below)

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-19-backup-assets-workspace-ui/**
.trellis/tasks/07-19-backup-assets-workspace-ui/research/implementation-evidence.md
.trellis/tasks/07-19-backup-assets-workspace-ui/research/visual-verification.md
.trellis/tasks/archive/2026-07/07-19-backup-assets-workspace-ui/**
.trellis/workspace/weibo/index.md
.trellis/workspace/weibo/journal-*.md
```

The two evidence files are created only when implementation/verification really
runs. They must record commands/results as executed, never pre-mark future gates
passing. `trellis-finish-work` owns the active-to-archive move and its
auto-commit. The concrete journal update is a later separate commit.

### 2.4 Final exact current-worktree manifest

The following is the exact set observed before staging. It supersedes the
conceptual lists in §§2.1-2.3; no directory wildcard is part of the work
commit. Archive and journal paths are generated by later Trellis workflow steps
and are intentionally not in this current manifest.

```text
.trellis/tasks/07-12-backup-data-explorer-design/task.json
.trellis/tasks/07-19-backup-assets-workspace-ui/check.jsonl
.trellis/tasks/07-19-backup-assets-workspace-ui/design.md
.trellis/tasks/07-19-backup-assets-workspace-ui/implement.jsonl
.trellis/tasks/07-19-backup-assets-workspace-ui/implement.md
.trellis/tasks/07-19-backup-assets-workspace-ui/prd.md
.trellis/tasks/07-19-backup-assets-workspace-ui/research/current-main-ui-api-evidence.md
.trellis/tasks/07-19-backup-assets-workspace-ui/research/implementation-evidence.md
.trellis/tasks/07-19-backup-assets-workspace-ui/research/scope-gates.md
.trellis/tasks/07-19-backup-assets-workspace-ui/research/visual-verification.md
.trellis/tasks/07-19-backup-assets-workspace-ui/task.json
web/src/components/layout/navigation.test.ts
web/src/components/snapshot-browser.test.tsx
web/src/components/snapshot-browser.tsx
web/src/components/snapshot-search.test.tsx
web/src/components/snapshot-search.tsx
web/src/components/task-run-detail.test.tsx
web/src/components/task-run-detail.tsx
web/src/components/ui/__tests__/tree.test.tsx
web/src/components/ui/tree.tsx
web/src/features/backup-assets/__tests__/handlers.ts
web/src/features/backup-assets/__tests__/test-utils.tsx
web/src/features/backup-assets/asset-browser.test.tsx
web/src/features/backup-assets/asset-browser.tsx
web/src/features/backup-assets/asset-bulk-bar.test.tsx
web/src/features/backup-assets/asset-bulk-bar.tsx
web/src/features/backup-assets/asset-context-panel.test.tsx
web/src/features/backup-assets/asset-context-panel.tsx
web/src/features/backup-assets/asset-evidence.test.tsx
web/src/features/backup-assets/asset-evidence.tsx
web/src/features/backup-assets/asset-grid.test.tsx
web/src/features/backup-assets/asset-grid.tsx
web/src/features/backup-assets/asset-inspector.test.tsx
web/src/features/backup-assets/asset-inspector.tsx
web/src/features/backup-assets/asset-list.test.tsx
web/src/features/backup-assets/asset-list.tsx
web/src/features/backup-assets/asset-overlays.test.tsx
web/src/features/backup-assets/asset-overlays.tsx
web/src/features/backup-assets/asset-preview-model.ts
web/src/features/backup-assets/asset-preview.test.tsx
web/src/features/backup-assets/asset-preview.tsx
web/src/features/backup-assets/asset-search.test.tsx
web/src/features/backup-assets/asset-search.tsx
web/src/features/backup-assets/asset-versions.test.tsx
web/src/features/backup-assets/asset-versions.tsx
web/src/features/backup-assets/backup-assets-preferences.test.ts
web/src/features/backup-assets/backup-assets-preferences.ts
web/src/features/backup-assets/backup-assets-presenters.test.ts
web/src/features/backup-assets/backup-assets-presenters.ts
web/src/features/backup-assets/backup-assets-route-state.test.ts
web/src/features/backup-assets/backup-assets-route-state.ts
web/src/features/backup-assets/backup-assets-state.test.ts
web/src/features/backup-assets/backup-assets-state.ts
web/src/features/backup-assets/backup-assets-task-context-link.tsx
web/src/features/backup-assets/backup-assets-workspace.test.tsx
web/src/features/backup-assets/backup-assets-workspace.tsx
web/src/features/backup-assets/use-backup-assets-state.test.tsx
web/src/features/backup-assets/use-backup-assets-state.ts
web/src/i18n/locales/en.ts
web/src/i18n/locales/zh.ts
web/src/lib/api/__fixtures__/backup-assets.fixture.json
web/src/lib/api/backup-assets-error.test.ts
web/src/lib/api/backup-assets-error.ts
web/src/lib/api/client.test.ts
web/src/lib/api/client.ts
web/src/pages/__tests__/backups-page.a11y.test.tsx
web/src/pages/backups-page.data.tsx
web/src/pages/backups-page.overview.tsx
web/src/pages/backups-page.recovery.tsx
web/src/pages/backups-page.test.tsx
web/src/pages/backups-page.tsx
web/src/router-pages.tsx
web/src/router.tsx
```

Count check: 62 frontend paths plus 11 Trellis/task paths, 73 total before
archive/journal output.

### 2.5 Explicitly unchanged without an approved amendment

```text
backend/**
deploy/**
docs/**
README.md
CHANGELOG.md
.github/workflows/**
web/package.json
web/package-lock.json
web/vite.config.ts
web/src/main.tsx
web/src/types/domain.ts
web/src/lib/api/core.ts
web/src/lib/api/backup-assets-api.ts
web/src/lib/api/backup-assets-boundary.ts
web/src/lib/api/backup-asset-search-api.ts
web/src/lib/api/backup-asset-overlays-api.ts
web/src/lib/api/backup-content-api.ts
web/src/lib/api/backup-repositories-api.ts
web/src/lib/api/recovery-points-api.ts
web/src/lib/step-up-storage.ts
backend/internal/database/migrations/{sqlite,postgres}/000067_*
backend/internal/database/migrations/{sqlite,postgres}/000068_*
backend/internal/database/migrations/{sqlite,postgres}/000069_*
backend/internal/database/migrations/{sqlite,postgres}/000070_*
backend/internal/database/migrations/{sqlite,postgres}/000071_*
```

No new npm dependency is planned. Existing semantic markup, Dialog,
DropdownMenu, Select, TanStack Virtual, MSW, and vitest-axe are sufficient.

## 3. Task 1: Route And Preference Contracts

**Files:** route-state and preference modules/tests.

- [x] Write RED table/property tests for all allowed route keys, opaque ID
  lengths, repeated type canonicalization, unknown/duplicate rejection, page/
  view coupling, sort pairs, saved-search exclusivity, and semantic repair
  inputs.
- [x] Prove `parse(serialize(validState))` equality and deterministic ordering.
- [x] Prove one-field mutations retain unrelated valid state and clear only
  dependent fields.
- [x] Add forbidden payload cases containing raw query/path/name, cursor,
  selection IDs, ticket URL, proof, and reason; assert safe reset contains none.
- [x] Write RED preference tests for exact keys/version/ranges, 4096-byte cap,
  unknown keys, malformed/prototype-like JSON, storage failures, and route
  precedence.
- [x] Implement the pure route codec and feature-specific preference codec.
- [x] Integrate omitted-layout initialization, canonical replace, user layout
  persistence, explicit-route precedence, and validated desktop panel widths.

Focused command after GREEN:

```bash
cd web
npx vitest run src/features/backup-assets/backup-assets-route-state.test.ts \
  src/features/backup-assets/backup-assets-preferences.test.ts
```

## 4. Task 2: Nested Backups Routes And One Navigation Entry

**Files:** route pages, current Backups page/tests, router files, navigation
files/tests.

- [x] Write RED MemoryRouter tests for root replace redirect, overview parity,
  data/recovery route panels, route-tab semantics, unknown route reset, and one
  Backups navigation item.
- [x] Move the current page body intact into `BackupsOverviewPage`.
- [x] Convert `BackupsPage` to compact shared PageHero/tablist/Outlet shell.
- [x] Add nested routes and lazy boundaries without a second sidebar item.
- [x] Add feature-disabled data state and evidence-only recovery shell. Do not
  create future recovery controls.
- [x] Verify AppShell pathname animation remounts between overview/data/recovery
  as expected while query-only workspace interactions keep the data page
  mounted.

Focused command:

```bash
cd web
npx vitest run src/pages/backups-page.test.tsx \
  src/components/layout/navigation.test.ts
```

## 5. Task 3: API Composition And Closed Error Mapper

**Files:** client files/tests, new error mapper/tests.

- [x] Write RED tests proving search, overlay, and content methods are available
  through `apiClient` and still pass AbortSignal/idempotency/proof options to the
  existing typed wrappers.
- [x] Write RED error tests for known Catalog capability detail, feature
  disabled, permission, not-found, cursor-context 409, overlay-context 409,
  retry-after, malformed/oversized detail, and unknown fallback.
- [x] Compose existing factories through typed dynamic lazy adapters that
  preserve exact method types/options and keep the main bundle within budget.
- [x] Implement a strict `ApiError.detail` parser returning closed localized
  codes. Never expose `error.message` or raw detail to components.
- [x] Keep G5/G6 distinctions unavailable; caller context may distinguish only
  operation kind, not hidden backend cause.

Focused command:

```bash
cd web
npx vitest run src/lib/api/client.test.ts \
  src/lib/api/backup-assets-error.test.ts
```

## 6. Task 4: Reducer, Request Guard, And Restoration State

**Files:** state/controller/presenter modules/tests and synthetic fixture.

- [x] Write RED reducer tests for repository/RP/parent/entry dependency changes,
  result generations, selection retention/clear, cursor reset, tombstones,
  temporary search memory, overlay attempts, and ticket detach.
- [x] Write RED hook tests using deferred promises: every request channel must
  abort its predecessor, and a late resolved predecessor must still be ignored
  by sequence/key/selection generation.
- [x] Cover unmount, RP mismatch, retired/expired/missing point, parent/entry
  404, search generation change, cursor 409, overlay 409 refetch, and content
  ticket selection change.
- [x] Add in-memory focus/scroll anchors keyed only by opaque context/ref. Prove
  no write to browser storage/history.
- [x] Implement presenter mappings for orthogonal states and safe unknown i18n
  fallback.
- [x] Add a synthetic raw API fixture under `lib/api/__fixtures__`; it contains
  no real hostname/path/content/credential and includes long/unknown states.

Focused command:

```bash
cd web
npx vitest run src/features/backup-assets/backup-assets-state.test.ts \
  src/features/backup-assets/use-backup-assets-state.test.tsx \
  src/features/backup-assets/backup-assets-presenters.test.ts
```

## 7. Task 5: Tree Accessibility Contract

**Files:** shared Tree and its existing tests.

- [x] Extend RED tests for one-tab-stop roving focus; Up/Down/Home/End; Right
  expand/child; Left collapse/parent; lazy completion; removal focus fallback;
  controlled and uncontrolled selection/expansion.
- [x] Implement visible-node flattening and stable refs without mutating item
  props or stealing focus after lazy load.
- [x] Re-run every existing Tree test and inspect all current consumers for
  changed click/toggle behavior.

Focused command:

```bash
cd web
npx vitest run src/components/ui/__tests__/tree.test.tsx
```

## 8. Task 6: Workspace Shell And Context Panel

**Files:** workspace/context/browser composition and tests.

- [x] Write RED layout/state tests for desktop three tracks, intermediate
  context Dialog, mobile context/results/inspector mode, stable toolbar/preview
  slots, and no nested DataSurface/card structure.
- [x] Write context tests for repository/task/RP selection, lifecycle/
  immutability/catalog/content facts, lazy directory tree, saved/favorite/tag/
  recent sections, and feature/permission blocked states.
- [x] Implement the unframed grid within AppShell's width and height contract.
- [x] Use existing Select/Dialog/Tree/Badge/InlineAlert/LoadingState primitives
  and Lucide icon buttons with names/titles.
- [x] Ensure repository/RP changes route through the pure mutator and controller
  invalidation rather than local shadow state.

Focused command:

```bash
cd web
npx vitest run src/features/backup-assets/backup-assets-workspace.test.tsx \
  src/features/backup-assets/asset-context-panel.test.tsx
```

## 9. Task 7: Virtualized Browse, Grid, Search, And Selection

**Files:** asset browser/list/grid/search/bulk components and tests.

- [x] Patch jsdom element dimensions in focused tests and write RED proof that
  a 1000-item fixture renders a bounded number of DOM rows/tiles.
- [x] Test shared ordering/selection across list/grid, roving keyboard focus,
  previous/next, cursor append, stale cursor reload, and in-memory scroll anchor.
- [x] Test exact browse sort pairs and reject size/modified asc and type sort;
  do not client-sort cursor pages.
- [x] Test temporary query refresh loss, saved-search opaque execution,
  current/all-retained scope, complete authoritative empty, partial zero,
  lower-bound totals, grouped exact hit, and secret content exclusion.
- [x] Implement `AssetBulkBar` with count/clear/inspect-one and only real
  permission-backed current operations. No export/recovery button or faux batch
  transaction.

Focused command:

```bash
cd web
npx vitest run src/features/backup-assets/asset-browser.test.tsx \
  src/features/backup-assets/asset-list.test.tsx \
  src/features/backup-assets/asset-grid.test.tsx \
  src/features/backup-assets/asset-search.test.tsx \
  src/features/backup-assets/asset-bulk-bar.test.tsx
```

## 10. Task 8: Overlay Interactions Within Current API

**Files:** overlays component/test plus controller/state files as already listed.

- [x] Write RED tests for saved-search create/update-query/delete/execute/broken
  state, favorite toggle/tombstone, tag definition CRUD, recent list/clear,
  permission block, pending lock, abort, idempotent retry, 409 refetch, and
  generic safe errors.
- [x] Assert idempotency keys remain stable for one uncertain retry and change
  for a new edit; assert they never enter URL/storage/history.
- [x] Implement only buildable current-main operations. Render G2 rename, G3
  complete assignment state, and G5 precise errors as unavailable/absent, not
  successful local-only controls.
- [x] Scan Radix portals with `document.body`; assert cancel/submit returns focus
  to the invoking control.

Focused command:

```bash
cd web
npx vitest run src/features/backup-assets/asset-overlays.test.tsx
```

## 11. Task 9: Inspector, Evidence, Exact Diff, And Versions Boundary

**Files:** inspector/evidence/versions components and tests.

- [x] Write RED tablist/tabpanel tests for preview, metadata, versions, security,
  evidence, and diff with route preservation and keyboard movement.
- [x] Test evidence layer labels/values without a computed trust verdict.
- [x] Test exact two-point selection, pair inequality, parent ref binding,
  cursor reset, Catalog/Provider evidence separation, and unavailable Provider
  evidence with retained Catalog results.
- [x] Test versions displays exact selected RP/lineage only and a G4 unavailable
  expansion state; assert no `latest` request/string path.
- [x] Test previous/next keeps result scroll/filter/selection while updating the
  exact inspector ref.

Focused command:

```bash
cd web
npx vitest run src/features/backup-assets/asset-inspector.test.tsx \
  src/features/backup-assets/asset-evidence.test.tsx \
  src/features/backup-assets/asset-versions.test.tsx
```

## 12. Task 10: Core Preview And Ticket Lifecycle

**Files:** preview component/test plus controller/state tests.

- [x] Write RED tests for each closed renderer/profile and stable viewport,
  safe raster alt, PDF/audio/video native elements, escaped HTML/XML/SVG as
  plain sandboxed content, metadata/hex fallback, and attachment action.
- [x] Test issuance abort/late response, binding mismatch, near-expiry renewal,
  media error retry bound, source detach, selection change, unmount, and no
  content URL/proof/storage leakage.
- [x] Test permissions and exact purposes. Mock
  `ensureStepUpProof(action, { persist:false, reuseCached:false })`; ordinary
  non-sensitive preview must not call it.
- [x] Test secret/unknown without a truthful challenge fails closed under G1;
  generic 400 must not open step-up. Test close reports local detach/expiry, not
  server revoke under G10.
- [x] Test G6 generic fallback does not claim range/cache/source/renderer cause
  and never calls direct Provider/content-byte fetch from a component.

Focused command:

```bash
cd web
npx vitest run src/features/backup-assets/asset-preview.test.tsx \
  src/features/backup-assets/use-backup-assets-state.test.tsx
```

## 13. Task 11: Legacy Tasks Compatibility

**Files:** snapshot browser/search, task-run detail, Tasks dialogs, and tests.

- [x] Write RED tests that legacy browse/search/restore remains behaviorally
  unchanged and still accepts native snapshot ID/raw path only inside its
  existing dialog flow.
- [x] Add a task-context link to `/app/backups/data?view=browse&taskId=<id>` using
  the route serializer/helper. Never place legacy path/snapshotRef in that URL.
- [x] Label the command as task context, not exact asset/RP/entry navigation.
- [x] Keep full asset explorer UI out of Tasks dialogs and retain every legacy
  close/back/restore behavior.

Focused command:

```bash
cd web
npx vitest run src/components/snapshot-browser.test.tsx \
  src/components/snapshot-search.test.tsx \
  src/components/task-run-detail.test.tsx \
  src/pages/tasks-page.test.tsx
```

## 14. Task 12: I18n And Accessibility Integration

**Files:** locale modules, page a11y test, all affected component tests.

- [x] Add exact zh/en key parity and a focused test/assertion covering every
  route/control/state/Provider/capability/error/aria label used by the feature.
- [x] Write axe smoke tests for overview/data/recovery, desktop workspace,
  partial/offline states, open overlay portal (`document.body`), and mobile
  inspector.
- [x] Test route/inspector tab roles, tree/list/grid keyboard flows, selection
  aria-live summary, icon names, focus-visible, Dialog return, virtualized focus
  restore, and reduced-motion classes/behavior.
- [x] Ensure status uses text/icon in addition to color and no raw code/tool
  error is visible in either locale.

Focused command:

```bash
cd web
npx vitest run src/pages/__tests__/backups-page.a11y.test.tsx \
  src/features/backup-assets
```

## 15. Task 13: Full Verification And Browser/CDP Evidence

No gate may be marked passing from planned commands. Execute and record exact
output only after implementation.

### 15.1 Static/privacy review

```bash
cd /home/murray/.codex/worktrees/bb05/xirang
git diff --check
if rg -n --glob '!*.test.*' --glob '!**/__tests__/**' "fetch\(" \
  web/src/features/backup-assets web/src/pages/backups-page*.tsx; then exit 1; fi
if rg -n --glob '!*.test.*' --glob '!**/__tests__/**' \
  "unknown as|\bas\s+any\b|:\s*any\b|<any>|\bany\[\]" \
  web/src/features/backup-assets web/src/pages/backups-page*.tsx; then exit 1; fi
rg -n --glob '!*.test.*' --glob '!**/__tests__/**' \
  "localStorage|sessionStorage|history\.state" web/src/features/backup-assets || true
```

Inspect matches rather than treating raw grep output as proof. The preference
codec/tests are the only expected storage matches; raw snake_case is allowed
only in the `lib/api` fixture/boundary and never in product components.

### 15.2 Full frontend and cross-layer gate

```bash
cd /home/murray/.codex/worktrees/bb05/xirang/web
env -u NODE_ENV npm run check
node scripts/check-bundle-budget.mjs
cd ..
make backend-test
git diff --check
```

`npm run check` includes typecheck, lint, tests/coverage, and production build.
The separate bundle command consumes that build. Fresh output was 157 test
files and 879 tests. The bundle script reported main JS 498.09/500.00 KiB and
CSS 104.21/105.00 KiB. `make backend-test` passed every Go package and satisfies
the parent after-Child-9 cross-layer checkpoint despite no backend edits. The
CI-equivalent audit reported 0 vulnerabilities.

### 15.3 Browser setup with synthetic data

1. Select an unused local port and start Vite; if a port is occupied, use
   another. Keep the session running until all viewport checks finish.
2. Launch a fresh Chrome CDP profile with the browser skill.
3. Use a temporary, uncommitted CDP `Fetch` interceptor backed by the synthetic
   fixture/MSW response set. It must fulfill auth/overview/asset JSON and opaque
   Broker media with no real content.
4. Seed only a synthetic session token/role in the fresh profile so the normal
   ProtectedRoute runs. Do not use a real user profile or credential.
5. Navigate the actual `/app/backups/...` routes; do not render isolated test
   components as visual evidence.

Example server command, with the actual free port recorded in evidence:

```bash
cd /home/murray/.codex/worktrees/bb05/xirang/web
npm run dev -- --host 127.0.0.1 --port 4173
```

### 15.4 Required viewports and checks

| Viewport | Required path/flow | Required evidence |
|---|---|---|
| 1440x900 | overview -> data, three columns, list/grid, search, inspector tabs, overlay Dialog | nonblank; stable tracks/toolbars; long names; portal/focus; virtual scroll; preview bounds; no overlap |
| intermediate (for example 900x900) | context Dialog -> results -> side/full inspector -> return | no compressed three columns; trigger focus return; scroll/selection retained |
| 390x844 | context -> results -> full-screen inspector -> previous/next -> back | no horizontal clipping; fixed controls fit; focus and scroll restored; long text wraps/truncates safely |

At every viewport inspect light/dark theme as relevant, zh/en representative
strings, reduced motion, console exceptions/warnings, failed/unexpected network
requests, image/media/frame load, portal stacking, sticky controls, and body/
column scroll ownership. Use screenshot plus DOM bounding-box/pixel checks to
prove the primary scene is nonblank and elements do not overlap.

Record commands, URL, viewport, fixture case, screenshots reviewed, console/
network results, focus target, scroll state, defects/fixes, and final result in:

```text
.trellis/tasks/07-19-backup-assets-workspace-ui/research/visual-verification.md
```

Create `implementation-evidence.md` with the focused/full command results and
privacy/static audit. This was executed in
`.trellis/tasks/07-19-backup-assets-workspace-ui/research/implementation-evidence.md`.
Give the user the active dev URL only after the server is actually running and
verified; the active synthetic URL is recorded in the visual evidence file.

## 16. Validation Matrix

| Contract | Unit | Integration | Axe/a11y | Browser/manual |
|---|---|---|---|---|
| Route canonical/privacy | parser/serializer/property tables | MemoryRouter + storage/history snapshots | route tab semantics | deep-link/back/forward at all viewports |
| Preferences | schema/size/storage failure | route precedence/reload | n/a | reload and corrupt preference repair |
| Abort/late response | reducer/request guard | deferred MSW races | live summary | fast repository/RP/entry switching |
| Browse/virtualization | sort/cursor/result generation | 1000 rows, list/grid, selection | grid/list keyboard | scroll/layout/long text |
| Search | AST/scope/coverage mapping | partial/complete/grouped/saved | result summaries | temporary reload and saved deep link |
| Overlay | mutation/idempotency reducer | CRUD/conflict/refetch/portal | body axe/focus return | menu/Dialog stacking and narrow layout |
| Preview | renderer/ticket state machine | proof/renew/detach/error | names/alt/frame title | real opaque fixture media and no overlap |
| Evidence/diff/version | presenter/pair rules | exact RP APIs and G4 unavailable | tab panels/status text | dense inspector scanability |
| Legacy links | route helper | snapshot/task dialogs unchanged | link names/focus | Tasks -> task-context workspace |
| i18n/unknown | key/code mappings | zh/en renders | axe in both representative locales | long Chinese/English/future-code fallback |

## 17. Risk And Scope-Gate Audit Before Completion

- [x] G1 secret negotiation was not guessed; only approved behavior exists.
- [x] G2 saved-search rename is absent/unavailable unless API amendment merged.
- [x] G3 tag assignment state is not presented as complete without lookup API.
- [x] G4 versions never use latest or fabricated history.
- [x] G5 generic 409 is not relabeled quota/rate/idempotency without evidence.
- [x] G6 generic Content failure is not relabeled range/cache/source/renderer.
- [x] G7 legacy link is task context, not exact RP/entry.
- [x] G8 only real browse/search sort pairs reach the server.
- [x] G9 error mapper never returns raw message/detail.
- [x] G10 preview close is local detach, not claimed server revoke.
- [x] Worker/Derived/export/recovery/retention/GA/Command capability remains
  absent or truthful.
- [x] `backup_assets.enabled` default and server ownership are unchanged.
- [x] No backend, migration, deploy, docs, package dependency, or sibling branch
  entered the diff.

## 18. Rollback

This child has no schema or Provider mutation. Frontend rollback is:

1. remove/hide the data and recovery child routes/tabs behind the existing
   server feature-disabled behavior;
2. restore `/app/backups` to the overview page or preserve its overview
   redirect;
3. keep every legacy Tasks snapshot browse/search/restore surface;
4. ignore/remove `xirang.backup-assets.preferences.v1` (it contains only layout
   and widths);
5. revoke no Provider data and run no migration/down command.

Do not call a UI hide a complete runtime rollback if any future API amendment
was separately approved; that amendment needs its own rollback contract.

## 19. Exact Staging And Delivery Workflow

The implementation, local verification, exact staging, and work commit below
are complete. Remaining repository delivery steps are not represented as
successful until they actually run.

### 19.1 Work commit

- [x] Confirm `git status --short` contains only the reviewed manifest and
  workflow-owned Trellis changes.
- [x] Stage each exact changed product/planning/evidence path explicitly. Do not
  run `git add web/src`, `git add .`, or a wildcard.
- [x] Run `git diff --cached --name-only`, compare it to the final manifest, and
  run `git diff --cached --check`.
- [x] Create one coherent work commit, expected conventional subject:

```text
feat: add backup asset workspace
```

### 19.2 Finish-work archive and journal

- [x] Run `trellis-check` and verification-before-completion from fresh command
  output; resolve every real issue on the same branch.
- [ ] Run `trellis-finish-work`. Let it archive Child 9 and create its automatic
  archive commit. Do not archive the planning parent.
- [ ] Write a concrete developer journal entry with baseline, scope-gate
  disposition, implementation commits, test/browser evidence, risks, PR plan,
  and no template placeholders.
- [ ] Stage only the actual journal/index files and create a separate journal
  commit.

### 19.3 Push, one PR, and required CI

- [ ] Push `codex/backup-assets-workspace-ui` and open one non-stacked PR to
  `main` with conventional title, scope-gate disclosure, screenshots/visual
  summary, exact validation, rollback, and feature-disabled status.
- [ ] Monitor every required CI job: PR title, backend lint/test/coverage/build/
  govulncheck, frontend npm ci/audit/check/bundle/coverage, Docker build,
  doc-freshness, and migration UTC safety.
- [ ] Fix failures on the same branch, push, and continue monitoring. Do not
  merge with failing, pending, missing, or canceled required checks.

### 19.4 Squash merge and post-merge

- [ ] Squash merge only after all required checks are green and review gates
  are resolved.
- [ ] Monitor the merge's Release Please activity. A feature PR normally updates
  or creates the release PR; it does not itself prove a GitHub Release.
- [ ] If a formal stable semver release is created, monitor `Publish Docker
  Images` and applicable release automation to success. Docker Hub remains the
  only official image source; GitHub Release remains the public version source
  of truth.
- [ ] README/release docs are not in this manifest, so Docker Hub description
  sync is normally not expected; record the actual workflow outcome rather than
  assuming it.
- [ ] If no formal release occurs, explicitly record that no GitHub Release or
  Docker image publish was expected from this merge.
- [ ] Fast-forward local `main` to `origin/main`, prove equality, confirm a clean
  main worktree, and clean up the merged branch/worktree according to repository
  policy before another dependent child starts.

## 20. Phase 1 Status Ledger

```text
task create:                 executed (planning task only)
branch/base setup:           executed
current-main research:       executed
prd/design/implement review: approved by user on 2026-07-19
scope-gate decision:         approved recommended frontend-only path
implementation authorization: approved by user on 2026-07-19
task.py start:               executed on 2026-07-19
product implementation:      executed
focused/full tests:          executed and passing (26/273 focused; 157/879 full)
browser/dev server:          executed and passing (Vite 4174 + CDP 9223)
work commit:                 executed (`863ea8f`)
finish-work/archive:         not_executed
journal completion commit:   not_executed
push/PR/CI:                  not_executed
merge/post-merge/main sync:  not_executed
migration/backend/deploy:    not_applicable unless an approved amendment changes scope
```
