# Quality Guidelines

> Code quality standards for frontend development.

---

## Overview

Frontend work should match the existing React 18 + TypeScript + Vite +
Tailwind-style utility approach. The standard gate is `cd web && npm run check`,
which runs typecheck, lint, tests with coverage, and build.

This is an operations console, so correctness, clear state, accessible controls,
and predictable repeated workflows matter more than decorative UI.

---

## Forbidden Patterns

- Direct `fetch` calls in components for normal API requests. Use typed API
  wrappers under `web/src/lib/api/`.
- New ad hoc UI primitives when an equivalent exists in `web/src/components/ui/`.
- Raw backend snake_case payloads in React components or contexts.
- Unlabeled icon-only buttons, inaccessible dialogs, or controls without
  keyboard behavior.
- Negative or viewport-scaled text hacks that can make dashboard/control text
  overflow. Keep labels compact and layout-constrained.
- New external dependencies without an explicit need and review.

---

## Required Patterns

- Run `npm run check` before merging frontend behavior changes.
- Preserve the API envelope contract handled by `web/src/lib/api/core.ts`.
- Add or update tests for behavior changes in pages, hooks, API mappers, and UI
  primitives. The repo uses Vitest and Testing Library.
- Use existing i18n helpers for user-facing strings when editing localized UI.
- Keep route pages, dialogs, and tables responsive across desktop and mobile.
- Use shared status, date, chart, and theme utilities instead of duplicating
  formatting logic.

---

## Testing Requirements

- Page behavior tests belong beside the page, for example
  `overview-page.test.tsx`, `nodes-page.test.tsx`, and
  `settings-page.test.tsx`.
- Utility and API mapper tests belong beside the module, for example
  `overview-api.test.ts`, `tasks-api.test.ts`, and `date-utils.test.ts`.
- UI primitive tests live under `web/src/components/ui/__tests__/` or beside the
  primitive when appropriate.
- For accessibility-sensitive UI, test roles, labels, disabled states, dialogs,
  keyboard-visible states, and empty/error variants.
- For async pages, cover loading, success, empty, error, and stale/refresh
  behavior when the code path is user-facing.

---

## Code Review Checklist

- Does the change use typed API wrappers and mapped domain data?
- Are loading, empty, error, and permission states handled explicitly?
- Are interactive controls accessible and keyboard friendly?
- Did the change reuse existing UI primitives, hooks, and utilities?
- Are tests updated for changed behavior?
- Does `npm run check` pass or is any skipped gate clearly justified?
- If the change touches API/domain contracts, are backend docs and frontend
  domain types synchronized?
