# Improve Frontend Workbench UX And Visual System

## Goal

Make the React console feel like a mature operations workbench: dense,
scannable, action-oriented, and visually calm. The first implementation should
repair the highest-impact shared patterns that currently make pages look soft,
card-heavy, and inconsistent, then apply those patterns to the primary daily
workflows.

## What I Already Know

- The user asked for a read-only frontend analysis first, then agreed that the
  next step should proceed through Trellis.
- The current visual problem is systemic, not one isolated bad page:
  `web/src/index.css`, shared primitives, page headers, stat cards, navigation,
  tables, filters, and row actions all contribute to the feel.
- The light theme uses a sage / beige / warm-brown palette. `primary` and
  `success` are both green-family values, which weakens operational status
  semantics.
- `StatCardsSection` forces `gridTemplateColumns: repeat(items.length, ...)`.
  This overrides responsive grid classes and can squeeze five metrics into a
  single row on small viewports.
- Many route pages start with large stat-card blocks. They consume first-screen
  attention but do not always answer "what needs action now?"
- Several surfaces use large cards as wrappers around toolbars, filters, data
  lists, and pagination, then nest more card-like items inside. This collapses
  hierarchy and makes everything feel equally important.
- Page headers are not consistent: some pages use `PageHero`, some start with
  metrics, and logs start with tabs. The naming and typography still read like
  a dashboard hero instead of an operational page header.
- Repeated row actions create visual noise, while some critical actions are
  hover-dependent. Primary action, secondary actions, and dangerous actions
  need clearer grouping.
- Navigation has too many top-level items, duplicated dashboard-like icons, and
  mobile compression through a tab bar plus more drawer.
- Some navigational interactions are implemented as buttons plus `navigate()`
  instead of real links, which weakens URL semantics and expected browser
  behavior.
- Xirang frontend specs already require an operations-console style, shared UI
  primitives, accessible controls, no negative / viewport-scaled text hacks,
  responsive route pages, and `cd web && npm run check`.

## Research References

- [`research/ux-reference-patterns.md`](research/ux-reference-patterns.md) —
  external workbench references, Vercel Web Interface Guidelines checkpoints,
  and Xirang-specific design implications.
- `.trellis/spec/frontend/component-guidelines.md` — local component,
  styling, Radix, navigation, and a11y conventions.
- `.trellis/spec/frontend/quality-guidelines.md` — local quality gate and
  forbidden frontend patterns.
- `.trellis/spec/frontend/a11y-guidelines.md` — axe/jsx-a11y baseline and
  known testing limitations.
- `.trellis/spec/guides/branch-workflow-guidelines.md` — PR, CI, merge, and
  post-merge automation expectations.

## Assumptions

- The first PR should improve the console's core product feel without changing
  backend contracts, RBAC behavior, or domain semantics.
- Xirang should borrow interaction patterns from mature operational tools, not
  their exact branding or colors.
- This work should prefer focused shared-primitives and high-traffic route page
  changes over a full app rewrite.
- Visual polish must not reduce density, keyboard access, URL semantics, or
  testability.
- Light and dark themes must remain supported.

## Requirements

### R1. Visual system and surface hierarchy

- Refine global UI tokens so the app is no longer dominated by sage / beige /
  warm-brown tones.
- Separate semantic colors for primary action, success, warning, destructive,
  info, muted text, and chart series.
- Keep cards and surfaces compact: radius should be no more than 8px unless an
  existing primitive requires otherwise.
- Reduce decorative shadows and use borders, spacing, headers, and density to
  communicate hierarchy.
- Avoid cards inside cards. Where a page needs a framed data tool, use one
  clear data surface with internal toolbar, table/list, empty state, and
  pagination.
- Remove negative tracking from compact operational text. Keep numeric values
  tabular and stable.

### R2. Shared workbench primitives

- Replace or evolve `PageHero` into a compact operational page header pattern
  with title, state summary, key metadata, and primary actions.
- Fix `StatCardsSection` so responsive behavior is controlled by explicit
  layout classes or component variants, not item-count inline grid styles.
- Add or refine shared primitives for:
  - compact KPI/metric strips,
  - data surfaces,
  - filter/toolbar composition,
  - row action grouping,
  - status summaries and operational alerts.
- Keep primitives composition-friendly and consistent with existing React 18,
  TypeScript, Tailwind, Radix, and lucide patterns.

### R3. Navigation and application shell

- Make desktop navigation easier to scan by clarifying group priority,
  duplicate dashboard/overview semantics, and admin-only visibility.
- Keep mobile navigation stable while reducing item overload.
- Ensure navigational actions that move to another route use link semantics
  where feasible.
- Prevent global warning / version surfaces from causing jarring layout shifts
  in normal use.

### R4. Primary workflow pages

- Apply the new patterns to at least these first-pass pages:
  - Overview: lead with operational health and abnormalities, not generic KPI
    cards; keep traffic and matrix content secondary but usable.
  - Nodes: make node health, filters, table/card view, and row actions scan as
    one coherent fleet-management workflow.
  - Tasks: make running/failed/paused queues and bulk actions more legible than
    generic cards; keep retry/cancel/pause actions discoverable.
  - SSH keys: use the same page header, filter/data surface, and action
    hierarchy as other inventory pages.
- Preserve existing data dependencies and route behavior unless a UI bug
  requires a narrowly scoped correction.

### R5. Accessibility and Web Interface Guidelines

- Icon-only buttons must keep accessible names. Decorative lucide icons must be
  `aria-hidden`.
- Controls must retain visible focus states and keyboard behavior.
- Long text, translated labels, and dynamic counts must not overflow or overlap
  at desktop or mobile widths.
- Use semantic links for navigation and native buttons for actions.
- Keep state in URLs where the existing page pattern already supports it; do
  not make browser back/forward behavior worse.
- Manually review color contrast when changing global tokens because the local
  axe helper intentionally disables jsdom color-contrast checks.

### R6. Verification

- Add or update focused tests for changed shared primitives and page behavior.
- Run `cd web && npm run check` before PR handoff.
- Run browser-level visual verification on desktop and mobile viewport sizes.
  Capture screenshots or document concrete observations for:
  - overview,
  - nodes,
  - tasks,
  - ssh keys,
  - light theme,
  - dark theme if feasible.
- Verify no unrelated backend, release, or deployment behavior is changed.

## Acceptance Criteria

- [x] `StatCardsSection` no longer forces all metrics into `items.length`
      columns on small viewports.
- [x] The first screen of Overview prioritizes actionable health / abnormal
      state before generic metrics.
- [x] Nodes, Tasks, and SSH Keys share the same compact page-header and
      data-surface grammar.
- [x] Repeated row actions have a clearer primary/secondary/danger grouping and
      critical actions are not hover-only.
- [x] Navigation surfaces remain role-aware and do not expose admin-only items
      to non-admin users.
- [x] Changed navigational controls use link semantics where feasible.
- [x] The light theme reads as a professional operations console rather than a
      one-note sage/beige SaaS template.
- [x] Dark theme remains coherent and usable.
- [x] No text overlap, clipped buttons, or incoherent horizontal overflow is
      visible in the verified desktop and mobile viewports.
- [x] Focused frontend tests pass.
- [x] `cd web && npm run check` passes before PR.

Notes:

- The role-aware navigation registry was preserved; this PR did not broaden
  navigation availability or expose new admin-only routes.
- Desktop/mobile screenshots were captured under `.tmp/screenshots/` for
  Overview, Nodes, Tasks, SSH Keys, and dark-mode Nodes. The screenshots are
  local ignored verification artifacts.
- A pre-existing demo-mode caveat remains: when a fake login token is present
  without a backend, the app shows the backend data-load warning even with
  `VITE_ENABLE_DEMO_MODE=true`. That behavior is data-loading semantics and is
  intentionally left outside this visual-system PR.

## Out Of Scope

- Backend API, database, RBAC, release, and deployment behavior changes.
- A complete redesign of every page in the console in one PR.
- Replacing the existing UI stack or adding a new design-system dependency.
- Marketing-site treatment, hero sections, illustrations, or decorative
  backgrounds.
- Full E2E / Playwright infrastructure unless the existing repo already has a
  suitable path available during implementation.
- Rewriting domain copy beyond labels needed for clearer UI hierarchy.

## Implementation Plan

### Phase A. Foundation

- Read relevant UI primitives and route-page fragments before editing.
- Refine token palette, radius, shadows, and status/chart semantics.
- Evolve shared page header, stat cards, data surface, filter/toolbar, and row
  action primitives.
- Add primitive-level tests where behavior or semantics change.

### Phase B. Shell and navigation

- Tighten desktop sidebar and mobile navigation scan patterns without changing
  route availability.
- Keep role-aware navigation registry behavior intact.
- Review global warning/version surfaces for stable layout behavior.

### Phase C. Primary pages

- Update Overview, Nodes, Tasks, and SSH Keys to use the shared workbench
  grammar.
- Preserve current loading, empty, error, permission, pagination, selection,
  dialog, and refresh behavior.
- Convert route-changing buttons to links where feasible.

### Phase D. Verification and polish

- Run focused tests during implementation.
- Run `cd web && npm run check`.
- Start the local frontend dev server and inspect desktop/mobile screenshots.
- Fix visible layout, contrast, text overflow, or interaction regressions found
  during review.

## Definition Of Done

- Trellis task is started before code edits beyond planning.
- Implementation stays on branch `feat/frontend-workbench-ux-redesign`.
- PR targets `main`.
- Required CI is monitored after PR creation; failures are fixed on the same
  branch.
- After merge, post-merge automation is checked. This UI-only change is not
  expected to create a formal GitHub Release or Docker image publish unless the
  release automation decides otherwise; record the observed outcome.
