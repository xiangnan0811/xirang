# Frontend Workbench UX Wave 2

## Goal

Extend the first frontend workbench redesign to the next high-traffic console surfaces so Xirang feels like one mature operations workbench rather than a set of unrelated card-based pages. This wave focuses on consistent page headers, data surfaces, filter/toolbars, status summaries, and action grouping for logs, notifications/alerts, service monitors, policies, and reports.

## What I Already Know

- The user first requested a read-only frontend UX analysis, then agreed to proceed through Trellis repairs.
- The first redesign task `05-13-frontend-workbench-ux-redesign` is completed and is present in `main` through commit `6eac43b feat(web): refine console workbench UX`.
- Wave 1 added shared workbench primitives: `PageHero`, `DataSurface`, `DataSurfaceHeader`, `DataSurfaceToolbar`, `DataSurfaceContent`, `DataSurfaceFooter`, and a responsive `StatCardsSection`.
- Wave 1 already covered Overview, Nodes, Tasks, SSH Keys, app shell, core tokens, and primitive tests.
- Current `main` still has several second-wave pages using old page/header/card patterns:
  - `web/src/pages/logs/logs-page.tsx` uses a raw tab bar and wraps the task log tool inside `Card/CardContent`.
  - `web/src/pages/notifications-page.tsx` starts with a large five-item `StatCardsSection`, then `DeliveryStatsCard`, then an old card-wrapped `AlertCenter`.
  - `web/src/pages/notifications/alert-center.tsx` wraps filters/list/pagination in `Card/CardContent` instead of `DataSurface`.
  - `web/src/pages/service-monitors-page.tsx` uses an ad hoc header and a raw table card instead of `PageHero` + `DataSurface`.
  - `web/src/pages/policies-page.tsx` has a toolbar/filter/table inside `Card/CardContent`, with an additional inner table card on desktop.
  - `web/src/pages/reports-page.tsx` uses ad hoc tabs, an ad hoc SLA header, and report config cards.
- `web/src/pages/backups-page.tsx` already uses `PageHero`; it is not a primary target unless a small consistency fix is discovered while implementing.
- The project requires work on a dedicated branch, PR CI monitoring, merge only when green, and post-merge Release Please / Docker automation monitoring.

## Research Notes

- Reuse prior research from `05-13-frontend-workbench-ux-redesign/research/ux-reference-patterns.md`:
  - operational consoles should prioritize status/triage and inspectable data tools over decorative cards,
  - inventory and alert pages should share a predictable header + toolbar + table/list + pagination grammar,
  - route-changing controls should use link semantics where feasible,
  - icon-only action controls require accessible names.
- Current primitive source files:
  - `web/src/components/ui/page-hero.tsx`
  - `web/src/components/ui/data-surface.tsx`
  - `web/src/components/ui/stat-cards-section.tsx`
  - `web/src/components/ui/filter-panel.tsx`
  - `web/src/components/ui/search-input.tsx`
  - `web/src/components/ui/view-mode-toggle.tsx`
- Current primitive tests:
  - `web/src/components/ui/__tests__/page-hero.test.tsx`
  - `web/src/components/ui/__tests__/data-surface.test.tsx`
  - `web/src/components/ui/stat-cards-section.test.tsx`

## Assumptions

- This wave should reuse the existing primitive system instead of adding a new design-system dependency.
- The scope should remain frontend-only and avoid API, RBAC, backend, release, and deployment behavior changes.
- The task can make small shared primitive extensions only when they directly reduce repeated page boilerplate.
- Visual polish must not reduce density, keyboard access, URL state, or testability.
- Light and dark themes must remain coherent.

## Requirements

### R1. Logs workbench surface

- Add a compact `PageHero` for Logs that explains the active operational surface without becoming a marketing hero.
- Keep the existing URL-backed `tab`, `task`, `node`, and `q` behavior.
- Convert the task-log card wrapper to `DataSurface` with a toolbar/content/footer-style hierarchy where appropriate.
- Keep Task / Node / Alert tab behavior accessible with `role="tablist"` / `role="tab"` or improve it without changing route semantics.
- Preserve export, fullscreen, live connection, history loading, and progress behavior.

### R2. Notifications and alert center

- Convert the notifications page into a clearer alert triage workbench:
  - compact `PageHero` with alert/channel/delivery state metadata,
  - smaller metric strip or action-focused summaries,
  - `AlertCenter` as a `DataSurface` instead of a generic card.
- Preserve unread-count, delivery stats, deep-link highlight, filters, sorting, pagination, view mode, alert actions, and delivery retry behavior.
- Avoid making critical alert actions hover-only.

### R3. Service monitors inventory

- Replace the ad hoc service monitor header and raw table card with `PageHero` + `DataSurface`.
- Add compact status metadata for total/enabled/down/unknown monitors when derivable from loaded data.
- Keep create/edit/delete/toggle behavior and accessible names.
- Preserve loading and empty states.

### R4. Policies inventory

- Convert the outer policy page wrapper to `PageHero` + `DataSurface`.
- Remove the nested card-in-card desktop table shape by using `DataSurfaceContent` / `DataSurfaceFooter` or a flat inner surface.
- Preserve batch selection, filters, card view on small screens, desktop table, pagination, create/edit/delete/toggle/clone behavior, and loading/empty states.

### R5. Reports workbench

- Replace ad hoc report tabs/header with `PageHero` + a workbench-style tab switcher and `DataSurface` where it improves hierarchy.
- Preserve the `tab=sla|slo` URL behavior.
- Keep report config create/edit/delete/generate behavior and SLO panel behavior.
- Avoid expanding this wave into a full dashboard/panel redesign.

### R6. Accessibility and responsive behavior

- Maintain icon-only button accessible names and decorative icon `aria-hidden` where applicable.
- Keep focus-visible behavior intact.
- Ensure long text and dynamic counts do not overflow at mobile and desktop widths.
- Use native links for navigation and native buttons for commands.
- Do not introduce negative tracking, viewport-scaled font sizes, or card-in-card page sections.

### R7. Verification

- Add/update focused tests for changed page behavior and shared semantics.
- Run focused page tests for changed pages.
- Run `cd web && npm run lint`, `cd web && npm run typecheck`, and `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run test -- --run <focused tests>` during implementation.
- Run `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run check` before PR handoff.
- Perform browser-level visual verification for the changed pages on desktop and mobile if the local app can be served, with notes or screenshots stored under ignored `.tmp/` paths.

## Acceptance Criteria

- [x] Logs uses the shared workbench grammar and preserves URL-backed tabs/filters.
- [x] Notifications/AlertCenter uses the shared workbench grammar and preserves alert triage interactions.
- [x] Service Monitors uses `PageHero` + `DataSurface` and preserves CRUD/toggle behavior.
- [x] Policies uses `PageHero` + `DataSurface`, with no obvious nested card-in-card desktop data section.
- [x] Reports uses a compact workbench header and preserves SLA/SLO tab URL behavior.
- [x] Changed pages remain usable at mobile and desktop widths without text overlap or incoherent horizontal overflow.
- [x] Focused tests for changed pages pass.
- [x] `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run check` passes before PR.
- [ ] Work is delivered through PR, monitored until CI passes, merged, and post-merge automation is monitored.

## Out Of Scope

- Backend API, database, RBAC, release, and deployment behavior changes.
- A full redesign of dashboards, credentials, users, settings, backups, or every remaining route in one PR.
- Replacing the existing Tailwind/Radix/lucide stack.
- Adding a new charting or table library.
- Marketing-style hero sections, illustrations, or decorative backgrounds.
- Large copy rewrites unrelated to UI hierarchy.

## Implementation Plan

### Phase A. Page surface alignment

- Update Logs, Notifications/AlertCenter, Service Monitors, Policies, and Reports to use the shared workbench primitives.
- Make the smallest necessary primitive extensions if repeated boilerplate appears.

### Phase B. Tests and a11y checks

- Update focused page tests where DOM structure or semantics change.
- Add focused assertions for key workbench semantics where coverage is missing.

### Phase C. Visual verification and delivery

- Run focused checks, full frontend check, and browser visual checks.
- Commit, archive Trellis task, open PR, monitor CI, merge, and monitor post-merge automation.

## Verification Evidence

- Focused tests: `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run test -- --run logs-page reports-page reports-page.slo notifications-page service-monitors-page policies-page` passed with 9 test files and 39 tests.
- Full frontend gate: `cd web && TMPDIR=/Users/weibo/Code/xirang/.tmp/tmp npm run check` passed with typecheck, lint, 84 test files, 344 tests, and production build.
- Diff hygiene: `git diff --check` passed.
- Browser verification: served the app in demo mode and captured desktop screenshots for Logs, Notifications, Service Monitors, Policies, and Reports under `/tmp/xirang-wave2-screens-final/`.
- Accessibility/semantics verification: Logs and Reports tab lists expose `role="tablist"`, keyboard navigation, selected tab state, and tabpanel linkage; data surfaces and page headings were present on the changed pages.
- Spec update review: no `.trellis/spec/` update is required because the task applied existing frontend workbench primitives and accessibility conventions without adding new reusable contracts or cross-layer behavior.
