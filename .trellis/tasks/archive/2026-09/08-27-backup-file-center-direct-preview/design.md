# Design: unified backup file center and direct preview

## 0. Approval and delivery status

Phases 1-7 shipped in `v0.52.0`, but production product acceptance exposed four
P0 gaps. Sections 13-17 are the revised remediation design. The user's 45-minute
step-up choice initially resolved only one design question; the user later reviewed
and explicitly approved the complete revised PRD/design/plan. That approval covered
bounded implementation, not commit, push, PR, CI, merge, release, NAS upgrade,
production acceptance, collectors, or node-log work.

The node-log P1 is outside this task. Its collectors remain zero until this file
center passes real usable-content production acceptance.

## 1. Selected product model

The three user decisions are authoritative:

1. Keep one first-level Backups entry. /app/backups opens Files by default,
   /app/backups/data remains the canonical Files route, and the local tabs are
   Files, Overview, Recovery.
2. Use node -> conditional Backup Set -> version -> directory -> file. A Backup
   Set is one producing task/scope. Hide its selector when a node has one set and
   never interleave or merge versions across tasks.
3. Use a 42/58 resizable desktop browser/preview split, with focused reading and
   a sequential browser -> preview fallback when the minimum pane widths do not
   fit.
4. Reuse `asset.secret_reveal` step-up for 45 non-sliding minutes within one login
   session; source/file changes and refresh do not invalidate it.

The preview itself has two co-equal jobs: let the user confirm that a retained
backup contains the expected information, and let the user directly read that
information. A successful byte transfer or a hex dump of readable text does not
satisfy either job.

The resolved desktop shell is a file-manager workspace, not the released
repository/recovery-point three-column inspector:

- compact node, conditional Backup Set, and version controls above the browser;
- breadcrumb and directory results in the main browser pane;
- a large preview pane beside it;
- optional details in a drawer/sheet, where repository and task context may be
  shown for support;
- browser -> preview navigation with a clear Back action on narrow screens.

## 2. Architecture overview

The implementation extends the current Catalog and Content planes:

    authenticated Backups route
      -> sanitized file-source projection (list authority only)
          -> node
          -> one or more isolated Backup Sets
          -> cursor-paged retained versions
      -> existing Catalog entry listing by opaque AssetRef
      -> direct-preview state machine
          -> closed safe_preview_v1 selection intent
          -> existing ticket RBAC, ownership, classification and step-up
          -> server resolves one closed delivery product
          -> existing same-origin Issue/Serve transport
          -> faithful plain-text/native/hex renderer components

The file-source projection does not issue tickets or touch Provider bytes. The
Content broker remains the only authority that can classify bytes, resolve the
delivery product, persist a grant, and serve content.

## 3. Read-only file-source projection

### 3.1 Why a dedicated projection is required

The released web state loads only the first page (up to 100) of repositories and
then the first page of points for one selected repository. Client-side fan-out
cannot truthfully construct an all-node, all-set version selector, creates an
N+1 request pattern, and can mistake an incomplete page for an empty source.

Add a narrow read-only projection in the existing backup asset Catalog service:

- GET /api/v1/backup-file-sources/nodes
- GET /api/v1/backup-file-sources/nodes/:nodeId/sets
- GET /api/v1/backup-file-sources/sets/:backupSetId/versions

All three routes use primary authentication, the existing backup-assets list
permission, current role/ownership filtering, feature-live checks, stable signed
cursors, bounded page sizes, and standard response envelopes. They perform
database/Catalog reads only; no Provider probe, listing, content read, mutation,
step-up, or delivery-ticket side effect is allowed.

### 3.2 DTO boundary

Node items contain only node ID, sanitized display name, Backup Set count, and
safe latest-retained time/status summaries.

Backup Set items contain:

- a 32-hex opaque backupSetId;
- parent node ID;
- a sanitized display label derived from producing task/scope;
- safe version count/latest-retained summaries;
- a closed lineage kind such as task or imported.

Version items contain only the already-authorized recovery-point display facts
needed by the browser: opaque recoveryPointId, opaque repositoryId for the
existing route/API boundary, optional producingTaskId for legacy route
compatibility, retained timestamps, lifecycle/catalog availability, bounded
counts/bytes, and closed capability summaries. Repository/provider labels are
not required by the selector and remain in optional details.

Unknown fields, invalid IDs/times/enums, ownership uncertainty, stale cursors,
and projection-limit errors fail closed. A blocked or incomplete projection is
rendered as unavailable/incomplete, never as authoritative empty.

### 3.3 Backup Set identity and isolation

The server owns Backup Set identity:

- task-backed key: exact producing node plus producing task;
- task-less/imported key: exact producing node plus authoritative task-less
  repository lineage; it is never merged across repositories or guessed into a
  task-backed set.

backupSetId is a domain-separated opaque identity created by the existing
server-owned identity mechanism from those stable coordinates. It is never a raw
concatenation and never contains a Provider locator. Resolving the ID always
rechecks current ownership and lineage. No new database table or migration is
required.

One task may have points in more than one repository; those individual recovery
points may appear in the same task-backed set as separate versions. Points from
different tasks are never interleaved, and no same-time virtual merge is
constructed.

Versions sort by capturedAt descending, then committedAt, createdAt, and opaque
recoveryPointId as deterministic fallbacks. Null times receive a clear fallback
label rather than a guessed capture time.

## 4. Route and compatibility contract

- /app/backups redirects to /app/backups/data.
- /app/backups/data remains canonical and keeps existing opaque repository,
  recovery-point, parent-entry, entry, saved-search, and layout query state.
- Extend the typed route state with validated nodeId and backupSetId selectors.
  Do not store a raw path, breadcrumb string, filename, content, token, proof, or
  ticket URL.
- Existing opaque deep links that contain repository/recoveryPoint/task context
  are resolved to their exact authorized node/set. A mismatch is repaired by
  clearing the incompatible descendant selection; no replacement version is
  guessed.
- Deprecated/legacy query compatibility may remain read-only, but newly emitted
  links use the canonical typed state.
- The URL is the only durable “last valid” source selection. Component memory may
  hold transient focus/attempt state; localStorage and sessionStorage are not
  used.

### 4.1 Exact legacy recovery-point resolver

Legacy links that carry an opaque recoveryPointId but predate nodeId and
backupSetId use one metadata-only reverse resolver:

- GET /api/v1/backup-file-sources/recovery-points/:recoveryPointId/source
- authenticated plus the existing backup_assets:list permission and current
  ownership filtering;
- implemented by the same bounded Catalog projection in one database/Catalog
  pass, with no Provider probe, listing, content read, mutation, or ticket side
  effect;
- returns only the exact nodeId, backupSetId, repositoryId, optional
  producingTaskId, and the safe retained-version facts needed to validate the
  legacy route.

Stale, missing, or unauthorized recovery points are indistinguishable 404s.
Duplicate/colliding matches or invalid internal projection state fail closed as
500. The frontend calls this endpoint only when recoveryPointId exists while
nodeId or backupSetId is absent, validates any supplied repositoryId and taskId,
then patches the exact authorized source coordinates and continues the normal
paged hierarchy. A mismatch clears incompatible descendants without guessing.
Modern nodeId+backupSetId routes never call the resolver. Resolver requests use
AbortSignal plus the existing generation/latest-result guard, so superseded or
late responses cannot rewrite current route state.

Changing node clears set, version, directory, entry, content, and error. Changing
set clears all descendants. Changing version clears directory, entry, content,
and error. Directory navigation clears entry/content/error. These transitions do
not clear an unexpired session-owned `asset.secret_reveal` proof. Each transition
increments the existing selection generation so late results cannot attach to the
new context.

## 5. UI composition

### 5.1 Desktop

The Files tab renders:

1. Page heading and concise current-node/version context.
2. Node combobox, conditional Backup Set combobox, and version combobox.
3. Breadcrumb/navigation toolbar and view/sort controls.
4. Two principal panes: a directory browser and a preview region. The preview
   gets enough width for readable text/PDF/media and is not confined to the old
   inspector column.
5. Optional details drawer/sheet for task, repository, Catalog status, evidence,
   favorite/tag, or support context.

The current narrow right-side inspector is rejected. The selected desktop
contract is a balanced resizable split:

- CSS grid/flex starts at 42% browser and 58% preview.
- A focused BackupFileSplitPane component owns only transient ratio/focus-mode
  presentation state; it does not own asset selection, ticket state, or route
  serialization.
- Clamp the browser to at least 20rem and the preview to at least 30rem. If the
  available work area cannot satisfy both plus the separator, switch to the
  sequential narrow-screen flow instead of shrinking the preview below its
  reading minimum.
- The separator supports pointer drag and keyboard Left/Right adjustment in
  two-percentage-point steps, exposes role=separator with vertical orientation and
  current/min/max values, and has a visible focus state.
- Focused reading expands the preview within the Backups work area. Exit restores
  the exact prior ratio and focus to the focused-reading trigger; the selected
  file, list scroll, and preview ticket stay current.
- Ratio and focused mode reset on reload. They do not enter route query,
  localStorage, sessionStorage, analytics, or logs.

Directories activate navigation. Files are single-select preview targets. The
row/card keeps the selected filename and loading state visible. There is no
first-run Load Preview button.

### 5.2 Mobile and touch

The default screen is the browser. File activation moves to a full-width preview
screen; Back restores the originating row and scroll/focus position when it
still exists. Node/set/version changes return to the browser root for the newly
selected version. The same sequential mode is used on any viewport where the
desktop minimum widths cannot both be met. Touch targets meet the existing
component guidelines.

### 5.3 Empty and unavailable states

The UI distinguishes:

- no authorized nodes;
- node with no backup sets;
- set with no retained versions;
- partial or building Catalog;
- unavailable content;
- permission denial;
- stale/deleted exact deep link;
- directory empty;
- current preview failure.

Only a proven complete page with no rows is described as empty.

## 6. Server-resolved readable preview

### 6.1 Request contract

Keep the existing exact renderer/profile request for download, renewal, derived
representations, and compatibility callers. Add one closed preview-only request
variant:

- schemaVersion: 1
- action: preview
- previewIntent: safe_preview_v1
- no renderer/profile fields

Strict validation requires exactly one request shape:

- preview + safe_preview_v1, or
- existing exact valid renderer/profile, or
- download + attachment/original_v1 plus its existing proof.

Unknown intent, intent plus renderer/profile, missing exact pairs, extra JSON
fields, and an auto intent on download fail closed with the existing safe bad
request response. The frontend API type is a discriminated union and maps
snake_case only at the API boundary.

The response and every persisted grant remain an exact closed delivery product.
Add plain_text/text_v2 for faithful readable text and keep escaped_text/text_v1
accepted for exact backward-compatible callers. “Auto” is a selection intent,
never a Renderer, Profile, grant value, cache key, Serve value, or successful
audit product.

### 6.2 Resolution algorithm

For safe_preview_v1 the broker authorizes the exact asset, acquires the existing
issue lease, reads one bounded prefix, classifies sensitivity, and resolves one
final product before persistence:

1. Recognize supported native binary signatures (safe raster, PDF, audio, video)
   and validate Provider MIME compatibility, active-content exclusion, size, and
   required Range capability through the existing renderer policy.
2. Otherwise try plain_text/text_v2 when the bounded bytes pass the backend
   decoder/control-byte policy. Textual MIME and filename families (text, JSON,
   YAML, TOML, XML, INI/CONF, env/config, logs, shell, and common source code) are
   hints only. Generic/unknown MIME is allowed to resolve to text when the bytes
   validate.
3. If no native signature is valid and byte validation rejects text, resolve
   metadata_hex/hex_v1.

plain_text/text_v2 normalizes a validated source encoding to UTF-8 but preserves
decoded characters and line breaks; it does not apply html.EscapeString or a hex
transform. Safety comes from the exact text/plain charset, X-Content-Type-Options
nosniff, empty iframe sandbox/CSP default-src none, no-store, byte limits, and
control-byte rejection. Active HTML/XML/SVG-like content may resolve only to this
inert plain-text product, never an active browser-native surface. A textual
filename cannot override invalid bytes.
A known native signature with missing Range/size capability returns its existing
typed capability/limit denial instead of disguising the file as hex. Derived OCR,
archive index, or text-extraction products remain explicit and are not launched
by safe_preview_v1.

The renderer still performs the final Prepare validation after selection. MIME
confusion and unsupported future products fail closed.

### 6.3 Classification, step-up, audit, and truncation

Sensitivity classification remains independent from rendering. If the resolved
asset is secret/unknown and there is no valid proof, the broker returns the
existing typed secret-reveal-required result before grant persistence. Admin may
complete the existing UI step-up and retry the same safe_preview_v1 intent once;
Operator and all other roles fail closed without an Admin proof prompt.

Success grants and audits record only the resolved exact renderer/profile.
Pre-resolution failure audit records the closed preview intent and safe failure
code without inventing a renderer/profile or serializing names, paths, bytes, or
locators.

Expose the existing RenderPlan truncation fact as a required boolean on the
ticket descriptor and map it defensively. It is true only when the selected
bounded transformed representation consumed fewer source bytes than the source.
The UI shows a localized truncation notice; this task does not add download as a
fallback.

## 7. Direct-preview state machine

Extend the existing useBackupAssetsState orchestration rather than adding a
second request owner.

File activation updates the opaque route/selection. Once that exact selected
entry is ready and eligible, one effect starts safe_preview_v1 with a key made
from selection generation, exact AssetRef, intent, and retry attempt. The
latest-request coordinator owns AbortSignal cancellation. A started-key guard
prevents React StrictMode/effect replay from issuing the same first attempt more
than once.

States are:

- idle/no file;
- selecting entry;
- issuing ticket;
- step-up required;
- ready, optionally truncated;
- typed current-selection error;
- canceled/superseded (not rendered as an error).

Every newer file selection increments the generation, detaches the old content
URL/cookie association locally through the existing boundary, aborts the old
issue, clears old bytes/error, and ignores late completion. Native content
components unmount before the new ticket attaches, preventing old audio/video/PDF
or image content from flashing under the new filename.

Retry is shown only for the current failed/expired/stale selection. A retry
increments its attempt key. Expiry renewal uses the current ticket’s already
resolved exact renderer/profile and never reuses a superseded AssetRef. A
selection-caused abort is silent.

## 8. Capability and error mapping

The client may use Catalog facts to suppress impossible actions, but it no longer
chooses a renderer from MIME. The backend remains final.

For an auto request, sequential prefix access is the minimum probe capability.
After resolution, native Range/size requirements are rechecked server-side.
Typed outcomes cover permission denied, content unavailable, range/capability
denied, secure transport required, secret reveal required, rate limited, expired,
stale source, renderer unsupported/MIME confusion, and service unavailable.

Map ErrRendererUnsupported and ErrMIMEConfusion at the ticket handler boundary
to the standard HTTP 422 envelope with the exact parameter-free reason
preview_renderer_unsupported. The frontend recognizes only that closed reason;
malformed/future responses become the generic fail-closed state. Unexpected
internal failures remain generic 503 and client messages never include raw error
text.

## 9. Security and privacy

Unchanged authorities:

- Catalog feature-live gate, list RBAC, current ownership, signed cursor, and
  sanitized DTOs;
- ticket route RBAC, asset authorization, Admin-only secret step-up, session and
  proof validation;
- delivery grant, lease/fence, absolute/idle TTL, request/concurrency/byte
  budgets, audit backlog, origin/Fetch Metadata, and Issue/Serve transport;
- same-origin opaque content URL, exact-path HttpOnly SameSite cookie, no-store,
  CSP sandbox, signature/MIME checks, and content-route log redaction.

Forbidden in source projection, route/query, analytics, errors, logs, docs,
fixtures, and screenshots: Provider locator, raw backup path, content bytes,
production asset identity, credentials, token, Cookie, ticket secret/URL, TOTP,
proof, command/output, or private client evidence. Browser storage has one narrow
exception: the approved action-keyed `asset.secret_reveal` proof plus expiry may
live in current-login `sessionStorage`; no proof metadata or other sensitive value
may be stored.

The simplified UI never turns list permission into content permission and never
uses a client renderer guess as authorization.

## 10. Accessibility and responsive behavior

- Use existing Combobox/Select, Breadcrumb, Button, ScrollArea, Dialog/Sheet,
  tabs, and semantic list/table primitives.
- Directory/file activation has native keyboard behavior, visible focus,
  selected/current state, and accessible names. Do not make an entire complex
  row a nested-button trap.
- The selector labels and version timestamps remain programmatically associated.
- Preview heading, loading, truncation, and current error use appropriately
  scoped status/alert regions without announcing every rapid canceled selection.
- Desktop file selection keeps focus in the browser; mobile preview moves focus
  to its heading and Back restores the originating item.
- Layout works at supported desktop/mobile breakpoints, 200% zoom, reduced
  motion, power-saving mode, and WCAG 2.1 AA contrast.
- Add a top-level axe smoke plus focused keyboard/focus-restoration tests.

## 11. Test architecture

Backend RED/GREEN matrices:

- file-source node/set/version pagination, deterministic ordering, single/multiple
  sets, taskless isolation, cross-task non-merge, ownership/RBAC, stale cursor,
  partial/blocked facts, zero Provider calls, and privacy serialization;
- strict exact-vs-intent payload validation and backwards compatibility;
- generic-MIME UTF-8/UTF-16 config, JSON/YAML/TOML/log/code -> faithful
  plain_text/text_v2;
- angle brackets, ampersands, quotes, Unicode, tabs, and retained line endings
  remain readable characters instead of HTML entities or hex;
- deceptive textual suffix plus binary bytes -> hex;
- generic-MIME real raster/PDF/audio/video signatures -> native product;
- active content -> inert plain text only;
- MIME confusion, range/size limits, zero-length, truncation descriptor, secret
  step-up, Operator denial, cancellation, audit failure, and no unresolved intent
  in grants/audits/Serve.

Frontend RED/GREEN matrices:

- Backups default route/tab order and old deep-link repair;
- node with one set (hidden selector), multiple sets (visible selector), taskless
  set, version ordering, partial and permission states;
- directory/breadcrumb navigation and descendant-state clearing;
- one click/keyboard activation -> exactly one ticket request, including
  StrictMode; rapid A -> B cancellation with no stale render;
- Admin step-up once, Operator no prompt, retry/renew current exact product;
- readable/native/hex/truncated render states, typed failures, desktop/mobile
  transitions, focus restoration, and axe smoke;
- no direct fetch, storage/history content persistence, raw snake_case, any, or
  unsafe casts.

## 12. Delivery, rollback, and task decomposition

This remains one end-to-end P0 Trellis task because the projection, selection
intent, state machine, and new shell must ship together to satisfy the production
acceptance. Splitting them into separately releasable child tasks would create
intermediate contracts that either retain the unusable UI or expose a new API
without its consumer. Implementation is divided into independently testable
phases in implement.md; no child task is created during planning.

Phases 1-7 required no schema migration. Remediation reuses the existing schema by
default; Phase 10 may request a separately approved paired SQLite/PostgreSQL
migration only if a genuine RED proves durable attribution cannot be represented.
Rollback is otherwise a normal application-image rollback: old clients continue
using exact escaped_text/text_v1 or other renderer/profile tickets, old routes
remain valid, and the additive read-only projection, safe_preview_v1 intent, and
plain_text/text_v2 product are unused. Do not remove the exact ticket request path.

Production acceptance uses the already established NAS deployment constraints:
root /volume2/docker/xirang, external 19927, internal 10761, and no test, [, [[,
cd, su, or sudo commands. It records only version/health/status evidence, never
secrets or asset identity/content. Collectors remain zero until a representative
generic-MIME text/config asset is visibly readable, a true binary stays non-text,
single-click switching works, and the security/privacy checks pass.

## 13. `v0.52.0` production-remediation boundary

The infrastructure deployment is healthy, but product acceptance failed. The
following observations are proven by production UI plus current source:

- the text/config preview request reaches a generic unavailable state; the exact
  Provider failure stage is not yet observable safely;
- `AssetPreview` currently shows optional Worker guidance for every unsupported or
  temporarily-unavailable error, including core preview failures;
- `asset.secret_reveal` currently asks the global helper for a non-persistent,
  non-reusable proof, clears its local copy on selection changes, and the server
  issues every step-up action with a five-minute lifetime;
- file-source nodes are projected only from already-public Recovery Points;
- entry-list pages contain children and a cursor, but not explicit current/parent
  directory context.

These findings constrain the remediation but do not prove the exact production
source-open failure. Phase 8 must first reproduce the real Repository Service ->
Provider adapter -> Broker -> Issue/Serve path with synthetic fixtures and add
sanitized stage evidence before selecting the minimal content-path fix.

## 14. Backup-bearing source discovery and projection

### 14.1 Inclusion truth

The primary selector represents **authorized retained data**, not active task
rows and not only public Recovery Points:

1. An existing public Recovery Point with an authorized lineage is `browsable`
   when it has a complete active Catalog.
2. A task/repository lineage is included as `indexing` when bounded Provider
   observation plus existing managed publication evidence proves retained bytes
   and exact node/task attribution, but the point/Catalog is not ready.
3. The same proven lineage is `unavailable` when its repository or required
   sequential capability is currently unavailable.
4. A configured task with no durable retained-data evidence is omitted. An
   ambiguous or unattributable Provider candidate stays in the existing Admin
   import-review boundary and is never guessed into a task-backed set.

Task enabled/running/success/archived/deleted status is presentation metadata only.
The stable TaskRepositoryLink and RecoveryPoint node/task snapshots preserve
identity after the mutable task row changes. Admin visibility follows the existing
global list authority. Operator visibility is rechecked against current node
ownership; missing or ambiguous ownership fails closed even when bytes exist.

### 14.2 Bounded reconciliation

Extend the existing repository import/rebuild and backup-asset lifecycle runtime,
not the node-log collector, with a bounded cursor-driven reconciliation pass:

- enumerate only configured/authorized repositories and managed task lineages;
- use the existing Provider native-point listing and managed-manifest proof rules;
- repair a publication that reached durable Provider commit but lost the final
  control-plane transition by creating/finalizing the exact Recovery Point and
  scheduling the existing Catalog build;
- preserve interrupted-run attribution only when task/run/link snapshots and
  managed evidence agree; otherwise quarantine or leave unavailable;
- never perform an N+1 Provider scan from a Files HTTP request. The selector reads
  the resulting database projection; discovery is asynchronous, bounded,
  cancellable, observable, and rate limited.

Reuse existing RecoveryPoint and import-candidate truth wherever it is sufficient.
If durable attribution state cannot be represented without a schema change, add a
paired SQLite/PostgreSQL migration only after a Phase 10 RED proves the need; do
not add a speculative duplicate inventory table.

### 14.3 Source DTO evolution

Evolve the source DTOs additively with a closed `browse_state` of `browsable`,
`indexing`, or `unavailable`, plus a parameter-free safe reason and safe retained
version counts. Nodes and sets may therefore remain visible before any version is
browseable. Cursor projection digests include state/count/availability facts.
No DTO exposes a Provider locator, raw path, credential, proof, content, or raw
discovery evidence.

## 15. Core text-preview diagnosis and error truth

The automatic preview pipeline remains one exact owner:

    authorize exact AssetRef
      -> open one bounded sequential source
      -> revalidate stat/fingerprint/capabilities
      -> read one bounded prefix
      -> classify sensitivity
      -> resolve plain_text/native/hex
      -> prepare exact product
      -> persist grant
      -> Serve the same exact product

Add production-shaped integration fixtures for Restic, Rsync, and Rclone source
adapters. Each must prove generic-MIME UTF-8/UTF-16 text reaches
`plain_text/text_v2`, Issue/grant/Serve agree, short reads and truncation are
faithful, and no Worker dependency is touched. Negative fixtures cover source
open/read/close, stat/fingerprint drift, sequential capability, timeout,
cancellation, malformed text, native signature, and true binary hex fallback.

Replace the generic failure collapse at the API boundary with a closed preview
source reason set sufficient to distinguish `open`, `read`, `changed`, `timeout`,
and `capability` without returning raw errors. Responses carry the normal request
correlation ID and empty params. Structured server logs/audits may include only
that closed stage, result code, correlation ID, and safe provider kind; never a
locator, raw path, asset identity/content, token, proof, or command output.

The frontend Worker hint is rendered only for an explicit derived-processing
Worker capability/status. Core source and renderer errors get direct localized
guidance such as retry/current-source-unavailable and the safe correlation ID.

## 16. 45-minute `asset.secret_reveal` session proof

Do not change the TTL or reuse policy of unrelated step-up actions. Replace the
single global TTL decision with a server-owned action policy: default/current
lifetimes remain unchanged, while `asset.secret_reveal` is exactly 45 minutes.
Claims remain signed and validate purpose, action, user, role, token version,
issued-at, expiry, and token/session state on every content Issue request.

The file-center state machine no longer owns a per-asset proof. On a typed Admin
secret-reveal requirement it calls the central helper with
`persist: true, reuseCached: true`; a cached proof returns without opening the
dialog, and the same safe preview intent is retried once. The central action-keyed
store keeps only proof+expiry in `sessionStorage`, so refresh works but closing the
login session does not persist authority.

Selection, directory, version, set, and node changes detach content but retain the
valid action proof. Login replacement, logout, TOTP disablement, role/user/token
version change, 401, expiry, or a typed proof rejection clears it. A rejected
cached proof permits at most one fresh dialog and one retry; proof rejection must
never create a prompt loop. Operator/Viewer continue to fail closed without an
Admin dialog, and ordinary non-secret preview never prompts.

## 17. Explicit parent-directory navigation

Extend `EntryPage` with a required safe directory context:

    directory: {
      current: { recovery_point_id, entry_id, name } | null,
      parent: { recovery_point_id, entry_id } | null,
      breadcrumb: [{ recovery_point_id, entry_id, name }]
    }

At root, current and parent are null and breadcrumb is empty. For a non-root page,
the Catalog validates that current is a directory in the same active generation,
loads its parent chain within the existing depth bound, and returns the context
even when `items` is empty. The cursor scope/digest binds this directory context.
The frontend mapper rejects malformed, cross-point, cyclic, contradictory, or
missing context atomically.

The browser replaces the Root button with a native 44px **Up one level** button.
It navigates to the server-provided parent, never derives a path string, and is
disabled/absent at root. The first breadcrumb remains the direct root jump and
later crumbs jump to ancestors. Up/crumb navigation aborts and detaches the
current preview, clears selected/bulk descendants, ignores late completion, and
restores focus to a deterministic browser target on desktop/mobile.

## 18. Remediation test and release gates

In addition to the existing matrices, require:

- adapter-real core text-preview tests for all supported repository providers and
  sanitized stage/correlation error serialization;
- 45-minute exact action TTL, non-sliding expiry, cached refresh reuse, cross-file
  and cross-source reuse, server rejection, logout/login/token-version/role/user
  clearing, ordinary-file no-prompt, and unrelated-action TTL isolation;
- active, interrupted, disabled, archived, deleted-task, no-data, ambiguous,
  operator-owned/unowned, multi-page, indexing, unavailable, and reconcile-repair
  source fixtures with bounded Provider-call counts;
- root/non-root/empty/deep directory envelopes, stale cursor, Up/breadcrumb,
  cancellation, focus restoration, 390px/desktop/200%-zoom, keyboard, and axe;
- no Worker copy for core errors and explicit Worker copy only for derived Worker
  states.

Release and NAS acceptance are a new remediation cycle. `v0.52.0` remains the
rollback image. Collectors stay zero until a newly released image proves all four
reported defects fixed over the authorized LAN path without printing sensitive
production identity or content.

## 19. `v0.52.2` preview-height hotfix

Production proved that single-click `plain_text/text_v2` rendering is readable and
non-hex, but its iframe occupies only the browser fallback height inside a taller
preview viewport. The workspace and split pane already provide available block
size; focused reading changes only the grid columns. The failure is local to the
frame layout: percentage height cannot resolve through the current flex/min-height
chain, and centering makes the short frame appear between large blank regions.

Keep `AssetPreview` as the renderer owner. Give frame-based products a dedicated
stretching wrapper/layout contract that consumes the available preview viewport
without depending on an unresolved percentage height. Keep image, audio, video,
loading, empty, and error bodies on their existing centered/native presentation.
Do not change ticket, URL, sandbox, content, selection, route, proof, or authority
contracts. Focused reading remains a width-only presentation state and inherits
the corrected frame height automatically.

The RED must run in a real browser and assert rendered geometry, not only Tailwind
class strings: after preview readiness, the frame height tracks the preview
viewport (with only the intentional inset) in both ordinary desktop and focused
reading. Focused component tests continue to cover the structural split between
stretching frame products and centered non-frame bodies.
