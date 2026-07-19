# Backup Assets Workspace UI Design

## 0. Planning And Approval Boundary

```text
task status:       in_progress; implementation verified
planning base:     b744b116c6a11ef02998d6182d372e9efe97abc2
planning review:   approved by user on 2026-07-19
approved scope:    buildable frontend core + truthful unavailable states
product edits:     executed
product tests:     focused/full/browser passing
task.py start:     executed on 2026-07-19
implementation authorization: approved by user on 2026-07-19
delivery workflow: pending
```

This document freezes the approved Child 9 frontend design. The user approved
the recommended frontend-only scope and separately authorized implementation
on 2026-07-19. The current-main gaps in `research/scope-gates.md` are part of
the design and cannot be filled by inference or fixtures.

## 1. Design Goals And Invariants

### 1.1 Goals

1. Preserve one Backups entry and the existing overview while turning data
   investigation into a deep-linkable workspace.
2. Keep the desktop surface dense and stable enough for repeated scanning;
   keep narrow screens sequential and reversible.
3. Treat Repository, RecoveryPoint, Catalog, Content, Preview, Security, and
   Evidence as orthogonal state products.
4. Keep identity composite and opaque from route parsing through every action.
5. Make URL/storage privacy mechanically testable rather than relying on
   component discipline.
6. Abort obsolete work and independently reject late responses.
7. Present only current-main capabilities and server-evaluated permissions.
8. Use existing Xirang visual language and dependencies.

### 1.2 Non-negotiable invariants

- A bare `entryId` never reaches an API or overlay mutation; it is combined with
  the active exact `recoveryPointId` as an `AssetRef` first.
- A route never contains a path, name, temporary query, cursor, selection set,
  ticket URL, proof, reason, idempotency key, raw error, or content-derived text.
- A browser storage value never contains a repository/RP/entry/overlay ID or
  any source/user content.
- No response commits after its channel, key, or selection generation changes.
- No client role check creates an action. Role may affect existing shell
  navigation, but asset actions require server permissions/capabilities.
- No content element receives a Provider locator or client-composed content
  path. It receives only the opaque Broker URL returned for the active ticket.
- Unknown DTO/capability/error products fail closed and render a localized safe
  state.
- An evidence layer is displayed as recorded/unavailable/not-recorded/invalid;
  the UI never computes an additional trust verdict.

## 2. Page And Component Architecture

```text
BackupsPage (shared PageHero + route tablist + Outlet)
├── BackupsOverviewPage
│   └── existing confidence / health / storage / guidance content
├── BackupsDataPage
│   └── BackupAssetsWorkspace
│       ├── AssetContextPanel
│       │   ├── repository/task/recovery-point selectors
│       │   ├── directory Tree
│       │   └── saved/favorite/tag/recent sections
│       ├── AssetBrowser
│       │   ├── AssetSearch + filter/sort toolbar
│       │   ├── AssetList | AssetGrid (one shared virtual result model)
│       │   └── AssetBulkBar (selection summary + only real actions)
│       └── AssetInspector
│           ├── AssetPreview
│           ├── metadata/security facts
│           ├── AssetVersions
│           ├── AssetEvidence
│           ├── exact two-point diff
│           └── AssetOverlays actions/dialogs
└── BackupsRecoveryPage
    ├── exact RecoveryPoint drill/evidence context when selected
    └── legacy Tasks compatibility command; no recovery plan/job UI
```

`BackupsPage` owns only page chrome and route tabs. It does not own asset
requests. `BackupAssetsWorkspace` is the composition boundary. Feature
components receive camelCase view models and callbacks; they do not import raw
DTOs or call `fetch`.

The repository management view is `view=repositories` within
`BackupAssetsWorkspace`. It lists current Repository/capability/catalog facts
and real navigation commands. It does not introduce connect/reconcile/purge
mutations in Child 9.

## 3. Closed Route Contract

### 3.1 Domain model

```ts
type BackupsPageRoute = "overview" | "data" | "recovery";
type BackupAssetsDataView = "browse" | "search" | "repositories";
type BackupAssetsScope = "current" | "all_retained";
type BackupAssetsSortField = "relevance" | "name" | "size" | "modified_at";
type BackupAssetsSortDirection = "asc" | "desc";
type BackupAssetsLayout = "list" | "grid";
type BackupAssetsInspectorTab =
  | "preview"
  | "metadata"
  | "versions"
  | "security"
  | "evidence"
  | "diff";

type BackupAssetsRouteState = {
  page: BackupsPageRoute;
  view: BackupAssetsDataView;
  repositoryId?: string;
  taskId?: number;
  recoveryPointId?: string;
  parentEntryId?: string;
  entryId?: string;
  savedSearchId?: string;
  scope: BackupAssetsScope;
  types: CatalogEntryType[];
  tagId?: string;
  favoriteOnly: boolean;
  sort: BackupAssetsSortField;
  direction: BackupAssetsSortDirection;
  layout: BackupAssetsLayout;
  inspectorTab: BackupAssetsInspectorTab;
};
```

The path owns `page`. The data subview uses query key `view`. `types` is encoded
as repeated, sorted `type` keys; every other key is singular. `tagId` is encoded
as `tag`, and `favoriteOnly=true` as `favorite=true`. Canonical defaults are
omitted by the serializer.

### 3.2 Query allowlist and validation

| Query key | Accepted value | Bound | Sensitivity/persistence |
|---|---|---:|---|
| `view` | `browse | search | repositories` | one | URL only |
| `repositoryId` | lowercase opaque 32 hex | one | URL only |
| `taskId` | positive safe integer | one | URL only |
| `recoveryPointId` | lowercase opaque 32 hex | one | URL only |
| `parentEntryId` | lowercase opaque 64 hex | one | URL only |
| `entryId` | lowercase opaque 64 hex | one | URL only |
| `savedSearchId` | lowercase opaque 32 hex | one | URL only |
| `scope` | `current | all_retained` | one | URL only |
| `type` | closed `CatalogEntryType` | <= 6 unique | URL only |
| `tag` | lowercase opaque 32 hex | one | URL only |
| `favorite` | exact `true` or omitted | one | URL only |
| `sort` | `relevance | name | size | modified_at` | one | URL only |
| `direction` | `asc | desc` | one | URL only |
| `layout` | `list | grid` | one | URL + validated preference |
| `inspectorTab` | closed tab set | one | URL only |

Unknown keys, empty present values, non-canonical numeric strings, duplicate
singular keys, duplicate type values, uppercase/incorrect-length IDs, extra
array members, or values beyond bounds make the whole parse invalid. The
caller uses `replace` to navigate to the page's safe canonical default and
does not copy any rejected key.

### 3.3 Coupled-state rules

- `overview` accepts no query keys. Any query produces a replace to canonical
  overview.
- `recovery` accepts only `taskId`, `recoveryPointId`, and an evidence-oriented
  `inspectorTab`. Asset browse/search/filter keys are invalid there.
- `view=repositories` accepts optional `repositoryId`, layout, and no
  RP/directory/entry/search/filter state.
- `parentEntryId` and `entryId` require `recoveryPointId`.
- If both repository and RP are present, the RP detail response must confirm
  the repository relation before child requests start. A mismatch blocks,
  clears RP/parent/entry, and returns to the repository context.
- `savedSearchId` requires `view=search` and excludes route scope/type/tag/
  favorite/sort/direction because the saved server AST/scope is authoritative.
- `scope=all_retained` excludes an exact route `recoveryPointId`; it may use
  repository/task context.
- Browse sort pairs are exactly `name/asc`, `name/desc`, `size/desc`, and
  `modified_at/desc`.
- Search sort pairs are exactly `relevance/desc`, `name/asc`, and
  `modified_at/desc`. Route `type` sorting and unsupported directions are
  invalid, not client-side approximations.
- `inspectorTab=preview|metadata|versions|security` is actionable only with an
  entry. `evidence|diff` is actionable with an RP. Keeping a default tab while
  the inspector is closed is valid; the serializer omits it.

### 3.4 Pure functions

```ts
parseBackupAssetsRoute(pathname, search):
  | { status: "valid"; state: BackupAssetsRouteState }
  | { status: "invalid"; safePath: string };

serializeBackupAssetsRoute(state): string;

updateBackupAssetsRoute(state, patch):
  | { status: "valid"; state: BackupAssetsRouteState; href: string }
  | { status: "invalid"; safePath: string };
```

The parser/serializer never reads storage, auth, API state, DOM, locale, or
time. The mutator starts from a valid state, applies one patch, and runs
dependency cleanup:

- repository change clears RP/parent/entry and inspector-dependent state;
- RP change clears parent/entry/diff comparison;
- parent change clears entry;
- switching view clears only fields incompatible with the new view;
- saved-search change clears temporary query memory through a controller event,
  not by serializing it;
- layout and unrelated filters survive all compatible updates.

Semantic API invalidation is separate from syntax parsing. It emits a typed
route repair passed through `replace`, never a raw response-derived URL.

## 4. Browser Preference And Privacy Design

### 4.1 Preference codec

One key is allowed:

```text
xirang.backup-assets.preferences.v1
```

Its exact JSON product is:

```ts
type BackupAssetsPreferencesV1 = {
  version: 1;
  layout: "list" | "grid";
  contextWidth: number;   // integer 224..360
  inspectorWidth: number; // integer 300..520
};
```

The codec rejects unknown/missing keys, non-integers, out-of-range values,
prototype-bearing/non-object JSON, and raw strings over 4096 bytes. Invalid
data is removed and defaults are used. Writes are caught and bounded. This
feature does not use generic `usePersistentState`.

An explicit route `layout` wins. When omitted, the preference may initialize
layout and the controller replaces the URL with a canonical explicit non-default
layout. A user layout change updates both route and preferences. Panel widths
never enter the URL.

### 4.2 Forbidden data audit

Tests snapshot all three browser channels before/after route navigation,
search, selection, preview, overlay mutation, error, and step-up:

```text
localStorage
sessionStorage
window.history.state
```

Only the exact preference key may change, and its decoded object must match the
closed schema. Session auth keys are pre-existing shell behavior and are
excluded from Child 9 writes; asset proofs use `persist:false` so no asset proof
key change is accepted.

React keys, analytics-like labels, toast details, console messages, and fixture
logs must also avoid ticket URL, proof, query/path/name content, and raw tool
errors.

## 5. State Ownership

| State | Owner | Lifetime | Persistence |
|---|---|---|---|
| Page/data view, opaque context IDs, non-sensitive filters, layout, tab | Router codec | deep link | URL only |
| Panel widths/default layout | preference codec | browser profile | one bounded localStorage record |
| Repository/RP/Catalog pages | request controller cache | mounted workspace/context key | none |
| Directory/search cursor pages | result controller | current canonical query key | none |
| Temporary search AST/text | search reducer | current mount/view | none |
| Selection | browser reducer keyed by result generation | current workspace flow | none |
| Focus/scroll anchors | restoration registry | current mount | in-memory Map only |
| Diff comparison RP | inspector reducer | current mount | none; route contract has no compare ID |
| Overlay drafts/idempotency attempts | overlay reducer | one dialog/logical attempt | none |
| Ticket descriptor/content URL | preview controller | active selection only | none |
| Step-up proof | current promise/request call | one exact action attempt | none |

No Context provider is added at AppShell level. The feature is route-local and
should not trigger global console re-renders. `BackupAssetsWorkspace` provides
a narrow feature context only if prop threading becomes materially worse;
request and state ownership remain in the controller hook.

## 6. Request Cancellation And Late-Response Rejection

### 6.1 Channels

```ts
type RequestChannel =
  | "repositories"
  | "recoveryPoints"
  | "recoveryPoint"
  | "directory"
  | "entry"
  | "search"
  | "savedSearches"
  | "favorites"
  | "tags"
  | "recent"
  | "overlayMutation"
  | "contentTicket"
  | "evidence"
  | "diff";
```

Each channel registry entry contains `{ sequence, key, controller }`.
`runLatest(channel, key, request)` aborts the previous controller, increments
the sequence, and captures both. A result commits only when:

```text
!signal.aborted
&& registry[channel].sequence === capturedSequence
&& registry[channel].key === capturedKey
&& current selection/context generation still matches
```

Abort errors are silent state transitions, never error toasts. Unmount aborts
all channels. A write request may already have reached the server when aborted;
the same logical retry reuses its idempotency key and then refetches the
affected overlay.

### 6.2 Dependency invalidation

```text
repository -> recovery points -> recovery point -> directory -> entry -> ticket
                                      |               |          |
                                      +-> evidence    +-> diff   +-> overlays

search context/saved query -> search pages -> selected hit -> entry/ticket
```

Changing an upstream key aborts and clears every downstream channel before the
route commit becomes visible. Content URL attributes are removed synchronously
before ticket state is cleared.

### 6.3 Stale/tombstone handling

- Directory/search cursor request returning a context-scoped 409 is treated as
  stale pagination: discard page chain, preserve filters, refetch page one,
  restore focus by exact `AssetRef` only if it returns.
- Overlay 409 cannot be classified precisely on current main: stop retry,
  refetch that resource/list, and show generic localized conflict.
- RP `retired/expired/failed/purge_blocked` or physical missing produces a
  typed blocked/tombstone view based on actual state. It never substitutes the
  next/newest RP.
- Entry detail 404 clears `entryId`, ticket, overlay draft, and inspector state.
  Parent 404 clears both parent/entry and returns to root. The list itself may
  remain if its exact generation is still valid.
- Unknown coupled projection blocks the whole affected region. It is not
  partially destructured into apparently valid fields.

## 7. Responsive Workspace Layout

### 7.1 Desktop

At a browser viewport width of at least 1280px, use a stable grid inside the
existing AppShell. The larger threshold is intentional: the persistent desktop
navigation consumes part of the browser width, so the earlier 1080px threshold
could leave only 896px for three tracks and clip the inspector.

```css
grid-template-columns:
  minmax(224px, 288px)
  minmax(420px, 1fr)
  minmax(300px, 416px);
```

Columns are unframed, separated by 1px borders, share one stable workspace
height, and each own overflow. The page tab/header sits above the grid.

- Context header/selector row: 44px.
- Browser toolbar: stable minimum 44px with wrapped secondary controls below,
  never overlaid on results.
- List row estimate: 44px; grid tile has a stable min width and aspect/height.
- Inspector tab bar: 40px; preview viewport uses `min-height` plus bounded
  aspect ratio so loading/error/media cannot resize the grid.
- Selection summary reserves a fixed bottom slot only while selected and does
  not cover the last virtual row (matching padding is added).

### 7.2 Intermediate

Below the three-column threshold, context becomes a compact selector opening an
existing Dialog. The middle results remain primary. Inspector is a bounded
right-side or full-height layer depending on available width; it never leaves
three compressed tracks. Dialog focus returns to the context trigger.

### 7.3 Narrow/mobile

The route-local flow has three modes:

```text
context selector -> results -> full-screen inspector
```

Only one primary mode is visible. Opening the inspector records row/tile focus
ID and virtual scroll offset. Close/Back restores the virtualizer first, then
focuses the exact selected item; if removed, focus moves to the result container
and an aria-live tombstone summary is emitted. Previous/next updates the selected
route ref but not result scroll/filter state.

Use `100dvh`-aware bounds under the fixed AppShell header/mobile nav. Long text
uses `min-w-0`, wrapping or truncation with accessible full text; no font scales
with viewport width and letter spacing remains zero/default.

## 8. Result Model, Virtualization, And Restoration

Directory and search results normalize into one view model:

```ts
type BackupAssetResultRow = {
  ref: AssetRef;
  asset: BackupAsset;
  source: "browse" | "search";
  hitFields: AssetSearchHitField[];
  snippet: AssetSearchSnippet | null;
};
```

The ordered array and a `resultGeneration` hash/key are shared by list/grid.
Selection is a `Map<assetRefKey, AssetRef>` bound to that generation; an RP or
query-generation change clears it. Layout changes preserve the map.

TanStack Virtual owns windowing. The DOM contains only visible rows/tiles plus
overscan. `aria-rowcount` may describe known exact totals; lower-bound or
unavailable totals use a live textual summary rather than a false number.
Cursor append preserves the stable ordered array. Scroll restoration records
`{ contextKey, assetRefKey, index, offset }` in memory and never stores path/name.

The bulk bar supports selection count, clear, inspect-one, and only mutations
whose current backend and permission products are truthful. It does not expose
Child 12 export or Child 13 recovery. Multi-item fan-out is not presented as an
atomic batch endpoint.

## 9. Empty, Partial, Offline, And Failure Matrix

| Input state | Result surface | Inspector/action behavior |
|---|---|---|
| Feature disabled | Localized blocked data route; overview remains intact | No asset action or fake data; route can return overview |
| No authorized repositories | Authoritative empty repository context | Only real Tasks/config navigation command |
| Repository offline + complete Catalog | Existing Catalog rows remain browseable with offline status | Preview/download disabled from content capability; never label corrupt |
| Repository offline + no Catalog | Unavailable, not empty | Retry/reconcile command only if current API/permission supplies it; otherwise return context |
| Catalog building | Loading/progress state from real coverage | No authoritative empty claim |
| Catalog partial + rows | Rows plus partial live summary | Preview follows independent content capability |
| Catalog partial + zero | Partial/unavailable result state | Never “no files” |
| Catalog complete + zero | Authoritative empty directory | Parent/root navigation remains |
| Catalog stale | Rows plus distinct stale status | No automatic substitution; retry current generation |
| Search complete + authoritative zero | Search empty | Query remains in memory for editing |
| Search partial + zero | Partial coverage | No-result claim forbidden |
| Stale cursor | Clear appended pages and reload first page | Preserve filters/focus intent; announce refresh |
| RP retired/expired/missing | Tombstone/blocked exact point | Clear dependent route/ticket; never use latest |
| Entry disappeared | Remove exact inspector selection | Restore result focus or container focus |
| Worker absent | Native capability unchanged; enhanced preview not deployed | No preview job controls |
| Sensitivity unknown/secret without valid proof | Fail-closed content state | No reveal bytes; G1 determines whether prompt is possible |
| Unknown future DTO/code | Safe blocked fallback | No guessed action |

## 10. Search And Saved-Search Design

- `AssetSearch` builds the existing version-1 AST in memory. Query text is
  submitted in the POST body through the typed wrapper.
- Route filters map only to server-supported AST/scope fields. `tagId` becomes a
  tag term without putting its display name in the URL.
- Search request keys include canonical AST, scope, server sort, limit, cursor,
  and proof-presence generation, but the key is kept in memory and never logged.
- `all_retained` grouped results show the selected exact point and lineage. The
  Versions tab uses G4 unavailable until an expansion API exists.
- Saved-search execution loads by opaque ID. `broken`/`blocked` state and reason
  are mapped to localized current state; scope is never silently widened.
- G2 prevents truthful rename. The current UI may show a neutral localized item
  label and timestamp, not a locally persisted synthetic display name.

## 11. Overlay Design

Feature controllers compose the existing overlay factory into the central
client. All targets remain composite refs.

| Overlay | Buildable behavior | Current limitation |
|---|---|---|
| Saved search | list/create/update query/delete/execute/broken or blocked state | no display-name rename (G2) |
| Favorite | list/add/remove/tombstone state | list may need bounded cursor accumulation to determine current toggle |
| Tag definition | list/create/rename/delete | assignment list absent (G3) |
| Tag assignment | direct assign/unassign response | cannot claim complete current assignment state after refresh (G3) |
| Recent | list/clear | no source tombstone is retained by design |

Mutation reducer states are `idle | pending | reconciling | failed`. Each
logical request creates `crypto.randomUUID()` or equivalently bounded
high-entropy key in memory. Retry after uncertain transport uses the same key;
a new user edit gets a new key. On 409, the reducer enters `reconciling`, reloads
the relevant item/list, and shows one generic conflict because G5 prevents a
more precise claim.

Overlay labels/query AST may appear in the active UI but never become route,
storage, log, or screenshot fixture values copied from real systems.

## 12. Preview And Ticket Lifecycle

### 12.1 Renderer selection

The selected entry's mapped MIME/type chooses a closed renderer/profile request:

| Known safe source kind | Requested core renderer | DOM consumer |
|---|---|---|
| text/config/log/code and HTML/XML/SVG source | `escaped_text/text_v1` | sandboxed same-origin frame displaying `text/plain` |
| safe raster MIME | `safe_raster/raster_v1` | `<img>` with filename/metadata alt |
| PDF | `same_origin_pdf/pdf_v1` | bounded sandboxed frame/object with fallback |
| supported audio | `native_audio/audio_v1` | `<audio controls preload="metadata">` |
| supported video | `native_video/video_v1` | `<video controls preload="metadata">` |
| unknown/binary/unsupported | `metadata_hex/hex_v1` | sandboxed plain-text frame |
| explicit authorized download | `attachment/original_v1` | same-origin opaque navigation/link |

This mapping selects a renderer, not sensitivity or safety. The Broker remains
authoritative and may reject it. SVG is never requested as raster or placed in
`img`; active source types use escaped text/metadata until a future sanitized
derived renderer exists.

### 12.2 Ticket state machine

```text
idle -> issuing -> ready -> renewing -> ready
             \-> blocked/failed
ready -> detached -> idle
```

Ticket state is bound to `{ AssetRef, selectionGeneration, action, renderer,
profile }`. Issuance aborts on any binding change. Ready commits only under the
request guard. The component treats `contentUrl` as an opaque string and does
not inspect delivery ID, cookie, query, or path segments.

Renewal is scheduled before the earlier idle/absolute expiry and may also be
triggered by a real media/frame load failure. It reissues only when the same
binding remains selected. Repeated failure falls back without loops. Replacing
or closing the preview first sets element `src/data` to an inert value, calls
`load()` for media teardown where needed, aborts issuance, and drops the
descriptor.

There is no per-ticket revoke route on current main. The UI records only local
detach/expiry, not server revoke (G10). Logout remains session-wide revocation.

### 12.3 Step-up

- Normal preview starts without proof only when the flow has a truthful
  non-sensitive path. It never proactively asks for a generic proof.
- Secret reveal uses exact `asset.secret_reveal`; download uses exact
  `asset.download`.
- Both call `ensureStepUpProof` directly with
  `{ persist:false, reuseCached:false }`. Proof is passed once to the typed API
  and then dereferenced in `finally`.
- Proof never enters component state beyond the active async stack, storage,
  URL, error detail, toast, analytics, screenshot fixture, or React dev output.
- G1 blocks a complete classify-then-prompt negotiation. Implementation may not
  convert generic 400 to an assumed secret challenge.

### 12.4 Fallback

Catalog capability reason can truthfully disable content. Ticket/gateway errors
use the strict frontend error mapper. G6 means generic errors cannot be labeled
range/cache/source/renderer failures. Available actions are the intersection of
real server permission and real returned fallback/capability; never a client
guess. No direct content-byte `fetch` is added to components.

## 13. Evidence, Diff, Versions, And Recovery Context

- `AssetEvidence` loads exact RP evidence independently from entry preview.
  Lineage, manifest, publication verification, and drills retain their source
  labels/status.
- Diff requires a second exact RP chosen from authorized points in the same
  relevant context. The pair is held in memory because the frozen route contract
  has no compare ID. Requests include exact RP IDs and optional exact parent
  refs. Switching either point invalidates cursor/pages.
- Provider diff evidence remains a separate status next to Catalog changes. It
  is not a boolean proof that the backup is healthy.
- `AssetVersions` displays exact producing RP/lineage facts available on the
  selected row. Expanded history remains unavailable under G4. It never calls
  `latest` or sorts hidden retained points client-side.
- `/recovery` may load RP evidence when `recoveryPointId` is present and link to
  the legacy Tasks surface. It does not instantiate RecoveryPlan/Job/Result
  types or controls.

## 14. Permissions, Feature Gate, And Error Mapping

### 14.1 Permissions

- Repository/RP Catalog projections supply `list`, `preview`, and `download`.
- Search supplies `list` and `secretReveal`.
- Overlay routes use the server's asset-list permission; UI shows a mutation
  only when the relevant current projection allows list/action context, and
  still treats server refusal as authoritative.
- Missing/blocked permission projection hides data/action and prevents request
  existence probes. Role is never substituted.

### 14.2 Feature gate

`backup_assets.enabled` remains server-owned and false by default. Child 9 does
not add a Vite enable switch or settings mutation. A typed feature-disabled API
response blocks the data route while overview and legacy Tasks remain usable.
Synthetic fixture mode is test/CDP-only and is never accepted as production
enablement evidence.

### 14.3 Error mapper

`mapBackupAssetsError(error: unknown, context)` accepts only:

- `ApiError` with known status;
- strictly parsed, bounded known `detail` fields/capability reason;
- finite positive `retryAfter`;
- the caller's non-sensitive request context (cursor fetch, overlay mutation,
  ticket issuance, etc.).

It returns a closed `BackupAssetsUIError` with localized key, retryability, and
safe action. Unknown shapes return `unknown`. It never returns raw message,
detail, path, query, Provider output, or proof. Caller context may infer stale
cursor only because the failed operation was a cursor fetch; it may not infer a
quota/source/classification reason from generic 409/400.

## 15. Accessibility And Focus Contract

### 15.1 Route and inspector tabs

- Container `role=tablist`; each route NavLink/button `role=tab` with
  `aria-selected`, roving tab index, and `aria-controls`.
- Outlet/inspector panel `role=tabpanel` with stable ID and accessible heading.
- Arrow/Home/End changes/focuses tabs; route tab activation preserves only
  compatible state.

### 15.2 Tree

Enhance the shared `Tree` to flatten currently visible items for focus movement:

- one treeitem button has `tabIndex=0`, others `-1`;
- Up/Down moves to previous/next visible item;
- Home/End moves first/last;
- Right expands or moves to first child; Left collapses or focuses parent;
- Enter/Space selects/toggles according to existing behavior;
- lazy-load completion does not steal focus; removing the focused node restores
  focus to the nearest visible parent/root.

Existing consumers and controlled/uncontrolled behavior receive regression
tests.

### 15.3 Result grid/list and inspector

- Results use grid/list semantics with stable row/cell names, selection
  checkboxes, `aria-selected`, and one roving focus target.
- Selection, total/coverage, stale refresh, tombstone, and preview load changes
  use a concise polite `aria-live` region. Destructive/blocked failures use
  existing alert semantics.
- Radix Dialog handles portal focus trap; tests scan `document.body` and assert
  trigger focus return. Mobile full-screen inspector uses explicit focus entry
  and virtualized result restoration.
- Every icon action has an accessible name and familiar symbol; unfamiliar
  icons also have a tooltip/title. Status always includes text/icon, not color
  alone.
- Motion uses existing reduced-motion utilities/hooks. No new continuous or
  decorative animation is introduced.

## 16. I18n Contract

One `backupAssets` namespace/object is added in both locale modules with exact
key parity for:

- route/view/tab/control labels;
- Repository/RP/Catalog/Content/Preview/Security/Evidence closed codes;
- Provider/version/immutability labels;
- empty/partial/offline/stale/tombstone/feature-disabled states;
- search coverage/total relation and overlay mutation states;
- safe error/fallback/action text;
- screen-reader summaries and icon labels.

Mapping functions return translation keys plus bounded params. Unknown codes
use one safe localized fallback and are never inserted as raw visible strings.
Synthetic fixture names are clearly fictitious in both languages.

## 17. API Composition And Type Boundary

The existing search, overlay, and content factories are exposed through
`apiClient` by typed dynamic lazy adapters. Each adapter derives its method
shape from the factory return type and forwards exact arguments; the factory
and raw mapper contracts remain unchanged. This implementation-level deviation
keeps the main bundle within its 500 KiB budget while preserving one typed
client surface. A new frontend-only strict error mapper lives in
`web/src/lib/api` because `ApiError.detail` is an API-boundary concern.

Route/preferences/reducer/view-model types remain feature-local. Existing
cross-product domain types in `types/domain.ts` are reused; Child 9 should not
add raw DTOs or future Worker/export/recovery types.

All requests use the typed client/factories. Native `img/audio/video/frame`
loading of an opaque Content Broker URL is the intentional content-plane
transport and is not a direct Provider fetch.

## 18. Visual Verification Design

Implementation uses the loaded browser skill and local CDP scripts. A synthetic
fixture set contains:

- multiple Provider/version/RP lifecycle combinations;
- complete/partial/offline/feature-disabled states;
- long unbroken and multilingual names;
- enough rows to prove virtualization;
- escaped text, raster, PDF/audio/video placeholder content with no real asset;
- overlay tombstone/broken states and evidence/diff facts;
- unknown future code fixture that must fail closed.

MSW integration tests and temporary CDP `Fetch` interception consume controlled
responses derived from this fixture. No real credentials/content are used or
written to screenshots.

At 1440x900, 390x844, and an intermediate viewport, walk:

1. root redirect and overview parity;
2. repository/RP/directory selection;
3. list/grid, search/sort/filter and cursor append;
4. selection and previous/next inspector navigation;
5. preview states and safe fallback;
6. overlay dialog/portal/focus return;
7. evidence/diff/versions unavailable semantics;
8. mobile context -> results -> inspector -> back restoration;
9. long text, scroll boundaries, fixed controls, reduced motion, theme/locale;
10. console and network error inspection.

Screenshots and DOM bounding-box checks must prove nonblank content and no
incoherent overlap. The final handoff gives the active dev URL only after this
execution actually occurs.

This verification was executed with synthetic data. The final results,
measurements, screenshots, focus checks, and clean post-load console/network
report are recorded in `research/visual-verification.md`.

## 19. Security And Privacy Threat Review

| Threat | Design control |
|---|---|
| URL/history leaks query/path/content | closed allowlist parser and forbidden-channel tests |
| Storage leaks selection/proof/ticket | one exact bounded preference codec; non-persistent step-up |
| Late response reveals wrong asset | abort + sequence/key/selection generation guard |
| Bare entry reused under another RP | composite `AssetRef` and dependency cleanup |
| HTML/SVG execution | escaped-text/metadata renderer only; sandboxed plain response |
| Client permission guess | server projection is sole action source |
| Secret prompt oracle | do not infer generic 400; G1 API gate |
| Ticket survives UI detach | detach immediately; do not claim revoke; G10 gate |
| Offline/partial displayed as corruption/empty | explicit state matrix |
| Evidence displayed as trust conclusion | layer-preserving evidence view |
| Provider path construction | opaque Broker URL only |
| Fixture leaks real data | committed synthetic fixture only; CDP uses no real assets |

## 20. Decisions And Rejected Alternatives

| Decision | Rejected alternative | Reason |
|---|---|---|
| Nested Backups routes with one sidebar entry | second asset/repository nav item | Violates approved IA and fragments the ops loop. |
| Pure closed route codec | ad hoc `useSearchParams` in components | Cannot prove coupled reset or privacy invariants. |
| Feature-specific validated preferences | generic `usePersistentState` | Generic JSON cast has no schema/key/size validation. |
| Abort plus sequence/key guard | AbortController alone | A resolved or non-cooperative request can still commit late. |
| Server cursor ordering | client sorting of loaded page | Produces false global order. |
| Stable unframed grid | nested cards and resizable floating panels | Adds noise and layout instability to a repeated-use tool. |
| Sequential mobile flow | squeezed three-column mobile layout | Breaks scanability, focus, and touch ergonomics. |
| Existing dependencies and semantic tabs | new Tabs/Tooltip/Sheet package | Current primitives/markup suffice; dependency cost is unjustified. |
| Truthful unavailable states | optimistic fake versions/tags/preview jobs | Current API cannot verify them and later Children own the domains. |
| Legacy task-context link | guessed exact RP/entry link | No current resolver exists. |
| Generic conflict after 409 + refetch | fabricated quota/idempotency message | Current handler collapses those errors. |

## 21. Scope And Rollback

The implementation manifest is frontend plus Trellis task files only. Any need
for backend, migration, deploy, Nginx, public API/docs, new npm dependency, or
feature enablement pauses execution for an approved amendment.

Frontend rollback removes/hides data/recovery subroutes and restores the current
Backups overview route. Legacy Tasks snapshot browse/search/restore remains
throughout, so rollback needs no Provider/Catalog mutation or data migration.
The preference key may be ignored or removed; it contains no source identity or
content. Server feature gate remains the primary safety switch.

## 22. Parent Contract Coverage And Focused Deviations

| Parent contract | Child 9 planning coverage |
|---|---|
| Implement §0 Execution Contract | Dedicated branch/base, reviewed start gate, inline-only execution, and single-PR/CI/post-merge flow in `implement.md` §§1/19. |
| Design §10.1 routes | `design.md` §3 freezes overview/data/recovery, index redirect, data-local repositories view, one sidebar entry, and closed route codec. |
| Design §10.2 three-column/narrow workspace | `design.md` §§2/7/8 freeze stable desktop tracks, intermediate selector, sequential mobile flow, cursor virtualization, scroll/focus restoration, and privacy ownership. |
| Design §10.3 orthogonal states | `design.md` §§6/9/14 freeze RP/Catalog/Content/Preview/Security separation, tombstones, safe unknowns, permissions, and feature gate. |
| Design §10.4 a11y/i18n | `design.md` §§15/16 and `implement.md` §14 cover tabs/tree/grid, focus/portals/live regions/reduced motion/color independence and zh/en code mapping. |
| Design §11 overlays | `design.md` §§10/11 cover current/all-retained/saved scope and composite saved/favorite/tag/recent lifecycles; G2/G3/G5 record current API gaps without inventing state. |
| Design §17 API/DTO | `research/current-main-ui-api-evidence.md` §§4-7 and `design.md` §§12-14/17 bind exact merged typed APIs, camelCase boundary, opaque refs/cursors, proof and capability rules. |
| Design §19.3 compatibility | `design.md` §13 and `implement.md` §13 preserve legacy Tasks and add only truthful task-context navigation under G7. |
| Design §20 degradation | `design.md` §9 plus G1/G5/G6/G10 distinguish offline/partial/Worker absent/source/ticket limits and prohibit false fallback reasons. |
| Design §21.2 frontend verification | `implement.md` §§3-16 specify mapper, route, storage, race, virtualization, preview, a11y, full check/bundle and three-viewport browser evidence. |
| Implement §10 Child 9 | `implement.md` §§2-15 refines the parent file/step list into exact current-main files and TDD ordering; §§17-19 add gate audit, rollback and repository delivery contract. |
| Implement §17/18 coverage gate | Product acceptance is checked from fresh evidence; `implement.md` §§15-17 require frontend full gate, bundle, backend cross-layer test, browser evidence and scope-gate audit before repository delivery. |

Focused deviations are evidence-driven, not silent scope reduction:

1. Parent pseudotype `savedSearchId?: number` is replaced by current-main opaque
   32-hex `string`.
2. Parent route sort set is narrowed to actual browse/search cursor pairs;
   unsupported type/ascending size/modified ordering is rejected under G8.
3. Parent exact legacy deep links are reduced to task-context links until G7
   provides a native snapshot/path resolver.
4. Parent saved-search rename, complete tag assignment state, version expansion,
   precise overlay errors, complete Content fallback reasons, secret-preview
   negotiation, and individual revoke are gated by G1-G6/G10.
5. Parent Worker/Derived preview states remain truthful unavailable because
   Children 10-11 have not merged; export/recovery/retention/GA remain later
   child ownership.
6. The desktop breakpoint moved from the planned 1080px effective width to a
   1280px browser viewport after a real AppShell measurement exposed inspector
   clipping at 1200px. Intermediate widths now use the continuous inspector.
7. Direct factory import/spread composition became typed dynamic lazy adapters
   to preserve the existing API surface while keeping the 500 KiB main-bundle
   budget green. This adds no dependency or public contract.

No deviation authorizes backend/API work. Approving such work requires a
focused amendment to all Child 9 artifacts and the exact manifest.
