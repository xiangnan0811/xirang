# 统一备份文件中心与直接预览体验

## Goal

Make backed-up files feel like a familiar, daily-use file manager: users choose a
node, choose a backup set only when that node has more than one, select a retained
backup version, browse directories, and preview a file with a single click. The
preview has two equal product purposes: verify that the backup contains the
expected data, and let the user read the concrete backed-up content. Internal
storage concepts remain available only as supporting context, while the existing
security and content-delivery authority stays unchanged.

This is a new P0 product task. It is not a continuation of the already released
preview-authorization or private-network HTTP transport fixes, and planning
approval is not implementation approval.

## User Problem

Production acceptance on `v0.51.0` proved that private-network HTTP content
transfer works, but the preview experience is not product-acceptable:

- A text configuration asset whose catalog MIME was generic binary was rendered
  as metadata/hex instead of readable text.
- Selecting a file only changes the selected entry. The user must press a second
  "load preview" action, and later use a separate refresh action.
- The browser is buried inside the Backups page's repository/recovery-point
  workspace, so a common file-browsing task exposes storage implementation
  concepts before the user's node/version/file mental model.
- The current three-column desktop inspector constrains the actual preview and
  makes the ordinary action feel unusually guarded.
- A second production screenshot confirms the preview is treated as a narrow
  right-side inspector after source context and the file list consume most of the
  workspace. Its content is an offset-prefixed hexadecimal dump, not the readable
  file text a user needs to validate or inspect the backup.

The production screenshots were inspected read-only. No production asset name,
path, locator, content, proof, or credential is copied into this task.

## Confirmed Product Direction

- Make **Files** the default, daily-discoverable landing surface inside the
  existing first-level **Backups** entry rather than asking users to understand
  repositories before they can browse files.
- Present the primary hierarchy as **node -> conditional backup set -> version ->
  directory -> file**. A backup set is derived from one producing task/scope and
  is shown only when the selected node has more than one set. Versions from
  different tasks are never merged. Repository, provider, and recovery-point
  identities are support metadata, not primary vocabulary.
- Opening a directory navigates into it. Selecting a file immediately starts the
  authorized preview flow; there is no normal-path "load preview" confirmation.
- Keep a manual retry/refresh affordance only for an expired, failed, or explicitly
  stale preview, not as the first-run path.
- Render text, JSON, YAML, TOML, configuration, log, and source-code assets as
  bounded faithful plain text. Preserve decoded characters and line breaks
  instead of showing HTML entities or a hexadecimal transform. Keep safe raster
  images, same-origin PDFs, audio, and video on their existing native renderers.
  Use metadata/hex only after the content policy has established that the asset is
  genuinely binary or no safer readable renderer is valid.
- Treat the directory browser and preview as co-primary desktop work areas, not a
  main table plus narrow inspector. Support an explicit focused-reading mode and
  a sequential full-width preview on mobile.
- Preserve opaque route state. Raw backup paths, provider locators, ticket URLs,
  tokens, proofs, and content must not enter browser storage, analytics, logs,
  history, or user-visible error guidance.
- Support desktop, touch/mobile, keyboard, screen-reader, reduced-motion, and
  WCAG 2.1 AA workflows.

## Evidence And Existing Contracts

- `v0.51.0` is the current production baseline for this task; image, manifest,
  health, database, and service-log acceptance already passed.
- Catalog listing and content delivery are separate planes. Catalog list
  permission does not itself authorize a content ticket.
- Native preview remains limited to authenticated Admin or Operator sessions
  with Catalog list access, available content, a selected recovery point, and the
  exact source capability. The backend delivery-ticket RBAC check remains final.
- Secret or unknown preview remains Admin-only step-up. Operator must fail closed
  and must never be prompted for Admin proof.
- The delivery route remains same-origin, cookie-scoped, query-free, no-store,
  CSP-sandboxed, and constrained by ticket expiry, lease, request, byte, and
  concurrency budgets.
- The backend text renderer already validates/decodes the bounded content prefix,
  but its released escaped_text/text_v1 product HTML-escapes a text/plain payload.
  A faithful plain-text product must preserve decoded characters while retaining
  the existing text/plain, nosniff, CSP sandbox, no-store, and byte limits.
- Existing latest-request cancellation, ticket detachment, expiry renewal, and
  native renderer components should be reused and strengthened rather than
  replaced.

## Requirements

### 1. Information architecture

- Keep exactly one first-level **Backups** application navigation entry at
  `/app/backups` and make it land on the **Files** surface by default.
- Keep `/app/backups/data` as the canonical Files route so existing opaque deep
  links remain valid. Change the `/app/backups` index redirect from Overview to
  Files rather than creating a new first-level route.
- Put Files first in the Backups route tabs and keep Overview and Recovery as
  adjacent secondary destinations. Administrative lifecycle panels remain under
  Overview or their existing scoped surfaces.
- Keep Backups overview, recovery, and administrative lifecycle surfaces
  reachable without duplicating their implementation in the file center.
- The page title, navigation label, empty states, and errors use user-facing file
  language, not Provider/Catalog terminology.

### 2. Source and version selection

- First select or restore the last valid node scope. Group its authorized recovery
  points into backup sets by producing task/scope, never across tasks. Show the
  backup-set selector only when the node has more than one set; otherwise select
  the sole set without adding a visible step.
- Introduce a bounded, cursor-paged, read-only file-source projection so the UI can
  enumerate authorized nodes, backup sets, and their retained versions without an
  N+1 walk over every repository. The projection reuses Catalog ownership and
  list RBAC, exposes only sanitized display metadata plus opaque identifiers, and
  does not grant content access.
- A task-less/imported lineage is isolated as its own server-projected backup set;
  it is never guessed into, or merged with, a producing task's set. Repository and
  Provider remain hidden from the normal selector.
- Order versions by a clear retained-time label and expose task/repository details
  only in an optional context/details surface.
- Switching node or version atomically clears incompatible directory selection,
  selected entry, ticket, preview, stale errors, and secret-reveal proof.
- Empty, partially indexed, unavailable, and permission-denied source states are
  distinct and do not imply authoritative emptiness when the Catalog does not.

### 3. Directory browsing

- Use familiar breadcrumb navigation, directory rows/cards, file rows/cards,
  sortable metadata, and a bounded page/cursor model.
- Directories are activated as navigation targets; files are selected as preview
  targets. Keyboard activation must match pointer activation.
- Keep selected state and directory state separate so a stale selected file does
  not survive a directory, node, or version change.
- Desktop should dedicate most of the work area to directory contents and a
  readable preview. Use the selected balanced resizable split described below.
  Mobile uses a sequential browser -> preview flow with a clear back action and
  restored focus.

### 4. Single-click direct preview

- A file activation immediately issues the narrow preview ticket when the user,
  Catalog projection, content state, and source capability are eligible.
- Latest selection wins. Each new file, node, version, or directory selection
  cancels or detaches the previous in-flight request and ignores late results.
- Loading is visible without obscuring the selected filename/context. A rapid
  switch must not flash old content in the new selection.
- Secret/unknown classification may interrupt the automatic path with the
  existing Admin step-up flow; this exception must be explicit, bounded, and tied
  to the exact asset/action.
- On expiry or a bounded native-media failure, renew only the current asset's
  exact renderer product. Never renew a superseded selection.

### 5. Readable renderer selection

- Renderer choice must be a closed, server-validated product. Filename extension,
  provider MIME, detected prefix/media signature, size, and policy limits are
  hints/evidence with an explicit precedence; none independently authorizes
  content.
- Known textual families include plain text, JSON, YAML, TOML, XML, INI/CONF,
  environment/config files, logs, shell and common source-code formats.
- Add a closed plain_text/text_v2 preview product. Text is strictly decoded by the
  backend, normalized to UTF-8 for display, and returned as
  `text/plain; charset=utf-8` without HTML-entity or hexadecimal transformation.
  Decoded characters and original line breaks remain visually faithful. The
  result is truncated at the configured bounded prefix and displayed with an
  explicit truncation/size state.
- Keep escaped_text/text_v1 accepted only for exact backward-compatible callers;
  the new automatic readable-preview intent resolves valid text to
  plain_text/text_v2.
- A generic `application/octet-stream` catalog MIME must not force hex when the
  bounded bytes are valid safe text and the closed policy resolves text.
- A filename that looks textual must not force plain text when byte validation
  fails. Genuine binary falls back to metadata/hex without exposing raw locator
  data.
- Existing raster/PDF/audio/video signature, MIME-confusion, size, Range, and
  sandbox checks remain authoritative.

### 6. Async, error, and large-file behavior

- Define explicit idle, selecting, loading, ready, truncated, step-up-required,
  permission-denied, capability-denied, transport-required, expired, canceled,
  rate-limited, unavailable, and renderer-unsupported states.
- Cancellation caused by a newer selection is silent; failure of the current
  selection is visible and retryable only when the typed policy permits it.
- Large text uses bounded preview plus a safe truncation notice. Large native
  types keep existing server size/range limits. This task does not add download
  as a workaround.
- Errors and guidance are localized, bounded, role-aware, and contain no asset
  identity beyond already authorized display metadata.

### 7. Accessibility and responsive behavior

- File/directory items use native interactive semantics, accessible names,
  selected state, and visible focus. Native buttons/links preserve their browser
  Enter/Space behavior; listbox-style items implement the matching ARIA keyboard
  pattern without nested interactive controls.
- Breadcrumbs, node/version controls, list/grid controls, preview region, loading,
  and errors have programmatic labels and predictable focus order.
- When mobile enters or leaves preview, focus moves to the preview heading or
  returns to the originating file item. Desktop selection does not steal focus
  from keyboard list navigation.
- Selection and status do not rely on color alone. Motion respects OS reduced
  motion and the application power-saving mode.

## Security And Privacy Invariants

- Reuse Catalog, ownership checks, RBAC, step-up, delivery-ticket, Issue/Serve,
  audit, transport, lease, and byte/request budgets. Easier interaction does not
  widen authority.
- Never request or print credentials, token, password, TOTP, proof, Provider
  locator, raw backup path, production asset name, or content in planning,
  diagnostics, task artifacts, client logs, route state, storage, or analytics.
- Ticket and content URLs remain opaque, same-origin, cookie-bound, query-free,
  and transient. Switching selection detaches them.
- Renderer negotiation must fail closed on future/unknown products and MIME or
  signature contradictions.

## Out Of Scope

- Unrelated download, export, archive, or recovery workflow redesign.
- Provider publication, backup scheduling, retention, restore orchestration, or
  Catalog schema redesign not strictly required by the file-center projection.
- Node-log collector work. The P1 remains stopped and collectors remain `0` until
  this task passes real usable-content production acceptance.
- Copying production asset identity or content into fixtures, screenshots, docs,
  logs, or planning artifacts.

## Key Decision 1: Canonical Entry And Page Shell

The user selected **B** on 2026-08-27:

- Keep exactly one first-level **Backups** navigation item.
- `/app/backups` opens Files by default; `/app/backups/data` remains the canonical
  Files route and opaque deep-link contract.
- Backups route tabs are ordered Files, Overview, Recovery.
- The Files surface is redesigned around node/version/directory/file language;
  repository and recovery-point details move out of the primary workflow.

## Key Decision 2: Conditional Backup Sets

The user selected **A** on 2026-08-27:

- The primary hierarchy is node -> conditional backup set -> version -> files.
- A backup set is a safe projection of one producing task/scope; Repository and
  Provider are never the normal user-facing selector.
- The selector is omitted when the selected node has exactly one authorized set.
- Recovery points from different tasks are never interleaved into one version
  list and are never merged into a virtual snapshot.
- Task-less/imported lineage remains isolated in a server-projected set instead of
  being guessed into a task-backed set.

## Key Decision 3: Balanced Resizable Preview

The user selected **A** on 2026-08-27:

- Desktop defaults to approximately 42% directory browser and 58% preview.
- A visible draggable divider lets users adjust the panes while enforcing usable
  minimum widths. The divider is keyboard operable and programmatically exposed
  as a vertical separator.
- A focused-reading action expands the preview to the work area. Exiting focused
  mode restores the prior split, selected file, scroll position, and focus.
- Pane ratio and focused mode are transient presentation state and reset safely on
  reload; filenames, paths, content, tickets, and proofs are never persisted.
- When the viewport cannot preserve both minimum pane widths, the layout switches
  to browser -> full-width preview -> Back rather than recreating a narrow
  inspector.

## Acceptance Criteria

- [ ] Selecting the existing Backups navigation entry lands directly on Files;
  Files is the first tab, and a daily user never has to select a repository or
  understand Provider concepts before browsing.
- [ ] A node with one authorized backup set proceeds directly to its retained
  versions, while a node with multiple sets shows a clear Backup Set selector;
  neither flow exposes Repository or Provider as required vocabulary.
- [ ] Versions are grouped by one producing task/scope, task-less/imported lineage
  is isolated, and no cross-task interleaving or virtual merge occurs.
- [ ] The file-source projection is bounded, cursor-paged, RBAC/ownership filtered,
  and does not trigger Provider reads or grant content access.
- [ ] A user can choose a node, conditionally choose a backup set, choose a
  retained version, browse directories, and activate a file using pointer or
  keyboard.
- [ ] Desktop opens with an approximately 42/58 browser/preview split; pointer and
  keyboard resizing preserve usable minimum widths, focused reading fills the
  work area, and exiting restores the prior split and focus.
- [ ] A viewport too narrow for both pane minimums uses the sequential preview
  flow and never collapses the preview back into a narrow inspector.
- [ ] Activating an eligible ordinary file automatically starts preview exactly
  once; no initial load/refresh button is required.
- [ ] Rapidly selecting file A then file B cancels/detaches A, ignores late A
  results, and never renders A in B's preview context.
- [ ] JSON, YAML, TOML, configuration, log, and representative code fixtures with
  generic binary MIME render as bounded plain_text/text_v2 when backend byte
  validation succeeds.
- [ ] Synthetic text fixtures containing angle brackets, ampersands, quotes,
  Unicode, tabs, and mixed retained line endings display as readable decoded text,
  not HTML entities or a hexadecimal dump.
- [ ] Binary fixtures cannot be coerced into plain text by filename or MIME and
  resolve to metadata/hex (or a closed unsupported state) according to policy.
- [ ] Raster image, PDF, audio, and video previews continue to use their existing
  signature-validated, sandboxed/native products and Range/size limits.
- [ ] Node/version/directory changes clear stale selection, ticket, content,
  error, and exact step-up proof state.
- [ ] Admin, Operator, Viewer/unknown, list/content capability, secret/unknown,
  transport, rate-limit, expiry, and unavailable matrices fail closed and match
  backend authority.
- [ ] Canonical/deprecated routes preserve opaque deep links without raw path,
  content, ticket, proof, or locator persistence.
- [ ] Desktop and mobile flows have explicit loading/empty/error/truncation states,
  correct focus restoration, and a top-level Files-page axe smoke.
- [ ] Frontend and focused backend tests pass, `env -u NODE_ENV npm run check`
  passes under Node 22, focused Go gates pass, privacy source scans pass, and the
  implementation PR/CI/post-merge evidence is recorded before release completion.
- [ ] Production acceptance on the authorized NAS proves: a representative
  generic-MIME text/config asset is readable, a real binary remains non-text,
  single-click preview works across switches, no sensitive values are printed,
  and node-log collectors remain `0` until that acceptance succeeds.

## Notes

- Planning source: user-approved handoff from the production acceptance session,
  current `origin/main` at the `v0.51.0` release commit, repository source, current
  Trellis specs, related task artifacts, and two read-only production screenshots.
  No production asset name or content is copied into this task.
- The earlier backup explorer decision to avoid a second first-level navigation
  entry is retained, but production evidence changes the Backups default from
  Overview to Files and requires a full file-manager page-shell redesign.
- This is a complex cross-layer task. `design.md`, `implement.md`,
  `implement.jsonl`, and `check.jsonl` must be complete before the user is asked
  for implementation approval.
- Do not run `task.py start`, dispatch implementation agents, edit product code,
  commit, push, or open a PR during planning.
