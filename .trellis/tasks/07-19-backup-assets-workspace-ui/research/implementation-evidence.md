# Child 9 Implementation Evidence

## 1. Boundary and state

- Task: `.trellis/tasks/07-19-backup-assets-workspace-ui`
- Branch: `codex/backup-assets-workspace-ui`
- Base: `main` at `b744b116c6a11ef02998d6182d372e9efe97abc2`
- Worktree: `/home/murray/.codex/worktrees/bb05/xirang`
- Task state while this evidence was written: `in_progress`
- Product scope: frontend workspace, route state, core browse/search/preview,
  overlays, evidence/diff, legacy compatibility, i18n, a11y, and visual
  verification only.
- Out-of-scope files remained unchanged: backend product code, migrations,
  deploy, public docs, package manifests/lockfile, `types/domain.ts`, and public
  API contracts.

`HEAD`, local `main`, and `origin/main` were all the approved Child 8 merge
commit when implementation began. The registered detached Child 6-8 worktrees
were not used as a source or dependency.

## 2. Delivered behavior

- Nested `/app/backups/{overview,data,recovery}` routes with an index replace
  redirect and one existing Backups navigation entry.
- A closed, canonical route codec and a versioned, bounded preference codec.
  When route layout is omitted, the validated preference hydrates the view and
  a non-default grid value is canonical-replaced into the URL; an explicit
  route value wins. User layout changes update the route and the one preference
  record, while validated context/inspector widths drive desktop grid tracks.
- A route-local controller with AbortController plus sequence/key/selection
  guards for repository, recovery-point, entry, browse, search, overlay,
  content, evidence, and diff requests.
- A stable desktop workspace, continuous intermediate layout, mobile
  context/results/full-inspector flow, shared virtualized list/grid results,
  selection, cursor handling, and in-memory focus/scroll restoration.
- Current-main-compatible saved searches, favorites, tag definitions/direct
  assignment, recent items, generic conflict reconciliation, and composite
  `AssetRef` handling.
- Core Broker preview for escaped text, raster, PDF, audio, video,
  metadata/hex, and attachment download, with opaque URL handling, bounded
  renewal, local detach, and exact download step-up purpose.
- Evidence and exact two-point Catalog/Provider diff without a derived trust
  verdict; versions remain truthfully unavailable beyond the selected exact
  lineage/recovery point.
- Task-context links from legacy snapshot browser/search and task-run detail;
  legacy browse/search/restore remains intact and raw snapshot/path values do
  not enter the new URL.
- Chinese/default and English strings, tab/tree/list/grid/dialog semantics,
  live summaries, focus restoration, and axe smoke coverage.

## 3. TDD and implementation corrections

All product behavior was developed test-first. Five late integration,
visual, and interaction defects received explicit regression cycles:
defects received explicit regression cycles:

1. **Responsive track clipping**
   - RED: two workspace assertions proved a 1200 px browser entered the
     three-track layout even though the AppShell left only 896 px, clipping the
     inspector.
   - GREEN: desktop begins at 1280 px and uses
     `minmax(224px, 288px) minmax(420px, 1fr) minmax(300px, 416px)`.
   - Focused result: workspace suite 17/17; CDP at 1200 px renders one
     continuous inspector and at 1280/1366 px keeps the full right edge inside
     the workspace with no horizontal overflow.
2. **Inspector-tab keyboard focus**
   - RED: the controlled-tab test showed Arrow/Home/End changed route state
     while focus remained on the previous tab.
   - GREEN: stable tab refs plus a post-route animation-frame focus move.
   - Focused result: inspector suite 4/4; CDP ArrowRight ends on
     `backup-assets-inspector-tab-metadata`, with `aria-selected=true` after
     the controlled route render.
3. **Cross-repository overlay references**
   - RED: a workspace integration test showed opening a favorite/recent
     `AssetRef` could retain the previous repository/task/parent context.
   - GREEN: the open-ref transition clears repository, task, and parent,
     restores `scope=current`, and writes only the exact composite RP/entry.
   - Focused result: workspace suite 17/17.
4. **Preference codec integration**
   - RED: page/workspace tests showed the strict preference codec existed but
     `/app/backups/data` neither hydrated an omitted layout nor applied its
     validated panel widths.
   - GREEN: route omission resolves from preferences with canonical replace,
     explicit route layout retains precedence, layout changes preserve unrelated
     route state while writing the sole bounded record, and desktop tracks use
     the validated widths. Default tracks remain 288/416.
   - Focused result: page and workspace regression coverage is included in the
     26-file / 273-test focused suite; CDP measured stored 320/480 preferences
     as 320/420/396 tracks inside the constrained 1136 px workspace.
5. **A11y storage isolation**
   - RED: the accessibility suite teardown cleared all localStorage, which could
     erase state owned by unrelated tests/features.
   - GREEN: teardown removes only
     `xirang.backup-assets.preferences.v1`, preserving feature ownership.

## 4. API composition implementation note

The reviewed design expected direct import/spread composition of the merged
search, overlay, and content factories. The final implementation uses typed
dynamic lazy adapters in `web/src/lib/api/client.ts` instead. Each adapter's
type is still derived from the factory return type and forwards the exact
arguments, AbortSignal, idempotency key, and step-up proof unchanged. This is a
frontend-internal loading decision that preserves the existing API contract
and keeps the very tight main-bundle budget green. No dependency or public API
was added.

## 5. Fresh validation evidence

| Check | Executed result |
|---|---|
| Focused feature/page/a11y/API/legacy suite | `env -u NODE_ENV npx vitest run ...`: 26 files, 273 tests passed |
| Full frontend gate | `env -u NODE_ENV npm run check`: typecheck, lint, 157 files / 879 tests with coverage, and production build passed |
| Bundle budget | main JS 498.09/500.00 KiB; main CSS 104.21/105.00 KiB; passed |
| Backend cross-layer checkpoint | `make backend-test`: all Go packages passed |
| CI-equivalent dependency audit | `env -u NODE_ENV npm --prefix web audit --audit-level=moderate`: 0 vulnerabilities |
| Diff whitespace | `git diff --check`: passed after product and evidence edits; a cached-diff check remains required immediately before commit |
| Browser/CDP | See `research/visual-verification.md`; three viewport families and real routed interactions passed |

The first broad static type scan matched the valid search-domain literal
`field: "any"` and therefore exited non-zero. It was not used as passing
evidence. A refined syntax scan for `as any`, `: any`, `<any>`, `any[]`, and
`unknown as` passed, as did the direct-`fetch` scan. The only production
storage match is the guarded
`xirang.backup-assets.preferences.v1` localStorage codec. No scoped backend,
deploy, docs, package, public-domain-type, or API-core file appears in status.

## 6. Scope-gate audit

| Gate | Implemented disposition |
|---|---|
| G1 secret negotiation | No generic 400 inference and no unnecessary normal-preview step-up; unknown/secret remains fail-closed without a truthful challenge. |
| G2 saved-search rename | No synthetic display-name persistence or rename claim. |
| G3 tag assignment lookup | Direct mutation only; the UI does not claim complete assignment state after reload. |
| G4 versions | Exact selected lineage/RP only; no `latest` lookup or fabricated expansion. |
| G5 precise overlay errors | Mutation 409 stops, reconciles, and shows one localized generic conflict. |
| G6 content fallback reasons | Generic content errors are not relabeled as Range/cache/source/renderer facts. |
| G7 legacy exact links | Links carry only `taskId` context; native snapshot/path remains in the legacy dialog. |
| G8 sort parity | Only actual browse/search sort products reach typed wrappers. |
| G9 safe UI errors | Closed mapper returns translation keys/actions and never raw server/tool detail. |
| G10 ticket revoke | Preview close detaches locally and does not claim server revocation. |

Worker/Derived, export, controlled recovery, retention, reconnect/purge, GA
legacy removal, Command Provider support, and feature enablement remain absent
or truthfully unavailable. `backup_assets.enabled` remains server-owned and
default-false.

## 7. Delivery state

The final Trellis/spec review and fresh local verification have run. The
following delivery transitions remain pending: final staged-diff verification,
work commit, Child 9-only archive, concrete journal commit, push, single PR,
required CI, squash merge, post-merge automation verification, and main/branch
hygiene. None is recorded as passed here.
