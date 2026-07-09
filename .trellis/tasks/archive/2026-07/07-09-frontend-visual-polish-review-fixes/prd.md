# Frontend visual polish and review fixes

## Goal

Improve the frontend console polish while preserving the operations-workbench
feel: compact, accessible, responsive, and predictable. Because the initial
implementation was done before a Trellis task existed, this task records the
requirements retroactively and uses the remaining work to verify, correct, and
document the change set.

## Confirmed Facts

- The branch is `fix/frontend-review-polish`, targeting `main`.
- The change set is frontend-only plus `.gitignore`.
- The work includes visual token tweaks, page shell polish, motion primitives,
  Stepper reuse, empty/loading state reuse, manual chunk updates, and locale
  updates.
- Review found three concrete issues that must be fixed before the branch can
  be considered ready:
  - Node detail tabs were clipped on narrow viewports.
  - `Card` applied hover lift globally instead of opt-in.
  - Node migration Stepper used the English fallback aria label.
- The repository frontend quality gate is `cd web && npm run check`.

## Requirements

- Keep all work on a dedicated branch, not `main`.
- Keep the visual polish restrained and suitable for an internal operations
  console.
- Node detail tabs must remain reachable on narrow/mobile viewports without
  causing page-level horizontal overflow.
- Manual tab controls must expose accessible tablist/tab/tabpanel
  relationships and keyboard navigation.
- Card hover-lift must be opt-in at call sites rather than applied globally by
  the shared `Card` primitive.
- Wizard step indicators must use localized accessible labels when embedded in
  product flows.
- Add or update regression tests for the reviewed issues.
- Preserve existing i18n, typed React, and frontend component conventions.

## Acceptance Criteria

- [x] Current branch is a dedicated work branch with task metadata pointing to
      `fix/frontend-review-polish` and base branch `main`.
- [x] Node detail tab row is horizontally scrollable on narrow viewports, has no
      page-level horizontal overflow, and supports arrow/Home/End keyboard tab
      navigation.
- [x] `Card` no longer includes `card-lift` by default, while call sites can
      still opt in with `className="card-lift"`.
- [x] Node migration Stepper has localized `aria-label` strings in zh and en.
- [x] Regression tests cover the node-detail tab behavior, Card opt-in lift,
      and node migration Stepper label.
- [x] `git diff --check HEAD` passes.
- [x] `cd web && npm run check` passes.
- [x] Browser smoke check confirms the fixed node detail tab behavior in a
      mobile-width viewport.

## Out Of Scope

- Backend API or database changes.
- New product workflows or new dependencies.
- Broad redesign of all navigation or all cards.
- Reworking existing demo-mode or status-page backend connectivity behavior.
