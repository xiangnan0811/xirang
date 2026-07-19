# Child 9 Current-Main UI/API Evidence

## 1. Research boundary

- Read-only product-code audit target:
  `/home/murray/.codex/worktrees/bb05/xirang` at
  `b744b116c6a11ef02998d6182d372e9efe97abc2`.
- Audit date: 2026-07-19 (Asia/Shanghai).
- Planning branch: `codex/backup-assets-workspace-ui`; PR base: `main`.
- A fresh `git fetch origin main` completed during Phase 1. `HEAD`, local
  `main`, and `origin/main` all resolve to the same Child 8 merge commit above.
- The worktree was clean before the Trellis child was created. Current changes
  are limited to the parent task's child registration and this new Child 9
  planning directory.
- This audit did not run `task.py start`, edit product code, run product tests,
  create a commit, archive the task, write a journal completion entry, push,
  open a PR, inspect CI, merge, or perform post-merge verification.
- External GitHub release/PR status reported by the parent controller is not
  used as Child 9 evidence. This file records only locally verified state.
- Both engines contain the merged paired
  `000066_backup_asset_content.{up,down}.sql`. A local filename audit found no
  `000067...000071` migration in either engine. Child 9 owns none of those
  numbers and must not create a migration.

## 2. Current Backups information architecture

| Surface | Current-main evidence | Child 9 consequence |
|---|---|---|
| `web/src/pages/backups-page.tsx` | One route-level component contains backup confidence, health, storage usage, and storage guidance. It has no asset workspace, nested outlet, or asset route state. | Move this content intact to the overview child page and make `BackupsPage` the shared route shell. |
| `web/src/router.tsx` | Only `/app/backups` exists. It renders the lazy `BackupsPage` directly. | Add nested `overview`, `data`, and `recovery` routes and redirect the index to overview. |
| `web/src/router-pages.tsx` | Only one lazy Backups export exists. | Keep one lazy shell export; child pages may be imported by the shell or exposed as focused lazy modules without adding a navigation entry. |
| `web/src/components/layout/navigation.ts` | There is exactly one Backups sidebar item at `/app/backups`. | Preserve one item. Pointing it at overview is allowed; no second asset/repository item is added. |
| `web/src/components/layout/app-shell.tsx` | Main content is bounded by `max-w-[1680px]` with responsive shell padding and animated route outlets. | The workspace must fit this shell, use a stable internal grid, and honor reduced motion instead of creating a full-bleed replacement shell. |

The approved route shape therefore remains:

```text
/app/backups                 -> replace redirect to /app/backups/overview
/app/backups/overview        -> existing confidence/health/storage/guidance
/app/backups/data            -> core asset workspace
/app/backups/recovery        -> existing drill/evidence context and future safe shell
```

Repository administration is a closed `view=repositories` state inside
`/app/backups/data`; `/app/backups/repositories` is not introduced.

## 3. Existing visual system and reusable primitives

- The application uses the existing Xirang theme tokens, Inter variable font,
  Tailwind utilities, shadcn-style primitives, Lucide icons, and restrained
  operational layouts. The workspace does not need a new brand, palette,
  gradient, illustration, hero, or card hierarchy.
- `DataSurface` is a bordered, rounded data tool. It is suitable for a truly
  framed repeated-data surface but not for wrapping every workspace column or
  nesting cards inside cards. The three workspace regions should be unframed
  grid tracks separated by borders.
- `SearchInput`, `ViewModeToggle`, `Badge`, `LoadingState`, `InlineAlert`,
  `EmptyState`, `Select`, `Checkbox`, `DropdownMenu`, `Dialog`, and existing
  button variants cover the planned controls. No new npm package is required.
- `ViewModeToggle` already has roving radio semantics and arrow-key behavior.
  Its domain value is `cards | list`; Child 9 may adapt `cards` to the route's
  canonical `grid` value at the feature boundary without leaking a third mode.
- `Tree` already exposes `tree/treeitem/group`, selection, expansion, lazy
  loading, Enter/Space, and Left/Right. It does not implement one-tab-stop
  roving focus or Up/Down/Home/End navigation. Child 9 must enhance the shared
  primitive and add regression tests before using it as the context tree.
- The repository has no shared Tabs, Sheet, or Tooltip primitive and does not
  depend on Radix Tabs/Tooltip. Route tabs and inspector tabs can use focused
  semantic `tablist/tab/tabpanel` markup; familiar icon buttons retain
  `aria-label` and `title`. A new dependency is not justified for this child.

## 4. Merged typed domain boundary from Children 6-8

`web/src/types/domain.ts` and the resource API modules already provide closed,
camelCase projections. Important current contracts are:

| Domain | Current contract | UI rule |
|---|---|---|
| Identity | `AssetRef = { recoveryPointId, entryId }`; recovery point IDs are opaque 32-hex values and entry IDs are opaque 64-hex values. Overlay IDs and cursors are opaque 32-hex values. | Never construct identity from a path/name or keep a bare entry ID without its recovery point. |
| Repository | Provider, version mode, immutability, status, lineages, catalog summary, capabilities, and permissions are mapped as closed products. | Provider, versioning, physical status, Catalog status, and permissions remain separate visual facts. |
| Recovery point | States cover `observed`, `retired`, preparation/verification, committed/degraded, expiring/expired, failed, and purge-blocked; physical availability and hold state are orthogonal. | Retired/expired/missing points invalidate dependent routes and render a tombstone/blocking state; entry IDs are never reused against another point. |
| Catalog | Generation, coverage, staleness, content availability, and `list/preview/download` permissions are separate. Unknown coupled DTO values become a blocked projection. | Offline is not corrupt, partial zero is not empty, and actions come only from server projections. |
| Entries | Name, type, size, modified time, mode, owner, MIME, fingerprint strength, breadcrumb, and composite refs are available. | Components consume only mapped types. Breadcrumb navigation uses opaque refs, never raw paths in the URL. |
| Evidence | Lineage, manifest, publication verification, and restore-drill evidence are available per exact recovery point. | Present recorded facts without deriving a stronger trust verdict. |
| Diff | Exact two-point Catalog diff and a distinct Provider evidence status are available; only `path_asc` is supported. | Never compare against `latest`; bind both exact points and label Provider evidence separately. |

`createBackupRepositoriesApi`, `createRecoveryPointsApi`, and
`createBackupAssetsApi` are already composed into `apiClient`. All list/detail
calls accept an `AbortSignal`. Recovery points are listed only within a known
repository; there is no global native-snapshot resolver.

Browse ordering is a closed backend contract:

```text
name_asc | name_desc | size_desc | modified_desc
```

`size asc`, `modified asc`, and type ordering cannot be produced by sorting a
single cursor page in the browser.

## 5. Search boundary

`createBackupAssetSearchApi` exists as a typed factory but is not composed into
`apiClient`. It provides:

- body-only versioned query ASTs; raw query text is not put in a URL;
- `current`, `all_retained`, and `exact_points` scope;
- `relevance`, `name_asc`, and `modified_desc` ordering;
- stable cursor, query generation, per-point Catalog/Search generation,
  projection revision, coverage, staleness, total relation, authoritative empty,
  suggestions, and server permissions;
- optional exact `asset.secret_reveal` proof; content/OCR facts are excluded
  without that proof;
- saved-search execution by opaque ID.

The server groups `all_retained` hits by producing lineage plus path-group token
and returns only the chosen hit. The response does not include a version count,
the hidden recovery-point refs, or an expansion cursor. The UI can truthfully
show a grouped current representative, but cannot expand its versions.

Temporary query AST/text must live in component/reducer memory only. A reload
of `view=search` without `savedSearchId` returns to an empty search input; it
does not reconstruct a query from browser storage or history.

## 6. Overlay boundary

`createBackupAssetOverlaysApi` is a typed factory and supports:

- saved-search list/create/get/update-query/delete;
- favorite list/add/remove;
- tag-definition list/create/rename/delete;
- tag assign/unassign for one composite `AssetRef`;
- recent list/clear;
- optimistic versions, mutation idempotency keys, tombstones, and broken or
  blocked saved-search state.

It does not provide a saved-search display-name field or a tag-assignment list
endpoint. A fresh UI session therefore cannot know all tags assigned to an
asset. Overlay errors such as broken scope, quota, idempotency conflict, and
optimistic conflict are collapsed by the handler into HTTP 409
`stale_state`; rate limiting has no stable asset-specific error projection.

All mutation retries must reuse one in-memory high-entropy idempotency key for
the same logical attempt and discard it after a terminal result. Keys, labels,
queries, and target sets are not persisted by the browser.

## 7. Content Broker boundary

`createBackupContentApi` is a typed factory and issues one exact ticket for a
composite asset and closed renderer/profile/action product. The returned URL is
same-origin, opaque to the component, and authorized only together with a
Path-scoped HttpOnly cookie. Core renderers are:

```text
escaped_text  safe_raster  same_origin_pdf  native_audio
native_video  metadata_hex attachment
```

Current frontend mapping enforces classification/proof coupling and accepts a
successful ticket only when `capabilityReason === null` and
`fallbackActions === []`. It does not expose a byte-reading JSON API; native
elements or a sandboxed same-origin frame consume the opaque URL directly.

The issuance flow has an unsatisfied negotiation problem: classification is
learned only while issuing. A no-proof secret/unknown request becomes generic
400, while a proof supplied for a non-secret preview is also rejected. The UI
cannot know whether to prompt without either unnecessary step-up or a failed
request whose response does not identify classification. There is also no
individual delivery-ticket revoke endpoint; logout/session revocation exists,
but closing a preview can only detach the URL and let the short grant expire.

Range/cache/materialization/source-change/renderer failures are mostly exposed
as generic request failures rather than the parent design's typed fallback
product. Child 9 must not invent a reason, request Provider bytes directly, or
derive a Provider locator.

Child 10/11 Worker, processing-job, Derived Store, scan, thumbnail/OCR, and
enhanced-renderer APIs do not exist on current main. UI states may safely say
`not_deployed` or `unsupported` only from real capability absence; no job or
derived-result fixture may be presented as production capability.

`backend/internal/settings/service.go` registers `backup_assets.enabled` with
code default `false`, and the merged settings tests lock that value. Child 9
does not introduce a frontend override or change the registry. Catalog also
explicitly blocks `ProviderCommand` without a typed artifact contract and maps
the reason as `task_artifact_contract_missing`; the UI must localize that real
reason rather than invent Command assets.

## 8. Authentication, step-up, and browser privacy

- `request()` is the central JSON boundary. It supports `AbortSignal`, exact
  step-up headers, and idempotency keys. Components must not call `fetch`.
- `ApiError.detail` is `unknown`. A feature-specific strict parser may map only
  known status/detail products to localized UI codes and must fall back safely.
- `useStepUpAction` defaults to persisted/reused proofs. Asset reveal/download
  flows must instead call `ensureStepUpProof(action,
  { persist: false, reuseCached: false })` so proofs remain only in the current
  promise/call stack and are cleared before prompting.
- The generic `usePersistentState` casts JSON to `T` without schema validation
  and writes every state change. It is not suitable for asset workspace state.
  Child 9 needs a dedicated versioned, exact-key, bounded preference codec.
- Only `layout` and validated panel widths may enter the asset preference key.
  Query AST/text, paths/names, selection, AssetRefs, cursors, tickets, content
  URLs, proof, reason, labels, and errors remain out of localStorage,
  sessionStorage, and `history.state`.

## 9. Legacy Tasks compatibility

- `SnapshotBrowser` and `SnapshotSearch` operate on legacy Restic snapshot IDs
  and raw paths inside the Tasks history dialog.
- Search results carry `snapshot_id + path`; the dialog passes those values back
  to `SnapshotBrowser`. Existing restore remains in that legacy surface.
- The merged Repository/RecoveryPoint APIs have no endpoint that resolves a
  legacy native snapshot ID plus raw path into an exact opaque
  `RecoveryPointID + EntryID`.

Consequently Child 9 must keep legacy browse/search/restore intact. It may add a
task-context link to `/app/backups/data?taskId=...`, but must not label that link
as an exact asset deep link. Exact compatibility requires a later approved API
resolver.

## 10. Test and visual-verification infrastructure

- `@tanstack/react-virtual` is already installed and used by `LogsViewer`.
  Existing tests patch real element dimensions because jsdom otherwise renders
  no virtual rows. Child 9 can reuse this pattern.
- MSW 2 is installed. Shared handlers cover only auth, captcha, and overview;
  asset fixtures/handlers should remain feature-local and synthetic.
- `vitest-axe` and `runAxe()` are established. Portal content must be scanned
  through `document.body`; browser review remains responsible for real color
  contrast.
- The browser skill supplies local CDP launch, navigation, evaluation, and
  screenshot scripts. Future visual execution must use synthetic fixtures and
  inspect 1440x900, 390x844, and one intermediate viewport, including console
  and network errors.
- `env -u NODE_ENV npm run check` is the complete frontend quality gate and
  includes typecheck, lint, tests/coverage, and build. CI then runs
  `node scripts/check-bundle-budget.mjs` against `dist`.

## 11. Planning conclusions

1. Child 9 is a frontend-only route/workspace delivery. No backend, migration,
   deploy, feature-enablement, public API, or public-doc edit is justified by
   the buildable current-main surface.
2. Existing dependencies are sufficient. The work should compose current
   factories into the central client, add only a strict frontend error mapper,
   and keep raw DTOs in `web/src/lib/api`.
3. The desktop workspace uses stable unframed tracks; tablet/mobile switch to
   a context selector and sequential list/inspector flow rather than compressing
   all columns.
4. Route and preference codecs must be pure, closed, reversible, and privacy
   tested before components are built.
5. Every request channel needs abort plus request-key/sequence protection.
6. Evidence and exact two-point diff are buildable. Version expansion, full tag
   assignment state, saved-search naming, exact legacy links, explicit ticket
   revocation, and several fallback/error distinctions require scope decisions
   recorded in `research/scope-gates.md`.
