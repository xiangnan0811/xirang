# Pages

> Quick reference for `web/src/pages/` — route-level screens and page fragments.
> Full conventions: [Frontend directory structure](../../../.trellis/spec/frontend/directory-structure.md) · [Component guidelines](../../../.trellis/spec/frontend/component-guidelines.md) · [Type safety](../../../.trellis/spec/frontend/type-safety.md)

---

## What Lives Here

78 files implementing route-level screens. Pages start as a single `*-page.tsx` file and are split into sibling `*-page.<part>.tsx` fragments when they grow. Tests are colocated as `*.test.tsx`. Some pages have a `hooks/` subdirectory for page-local hooks.

---

## Page-Fragment Split Pattern

Large pages are decomposed into sibling fragments instead of deeply nested one-off folders. The main `*-page.tsx` file imports and composes the fragments.

Reference example: `nodes-page.tsx` + fragments

```
nodes-page.tsx           # main — imports fragments, composes layout
nodes-page.state.ts      # useNodesPageState() hook — all page state
nodes-page.table.tsx     # NodesTable component
nodes-page.grid.tsx      # NodesGrid component (card view)
nodes-page.toolbar.tsx   # NodesPageToolbar component
nodes-page.dialogs.tsx   # NodesPageDialogs component
nodes-page.utils.ts      # shared utility functions
nodes-page.utils.test.ts # utils tests
nodes-page.test.tsx      # page integration test
```

Common fragment suffixes: `.dialogs`, `.grid`, `.state`, `.table`, `.toolbar`, `.utils`, `.hero`, `.filters`, `.bulk-bar`, `.traffic`, `.log-entry`, `.fullscreen-dialog`.

Small pages (e.g. `more-page.tsx`, `login-page.tsx`) stay as a single file — do not split prematurely.

---

## Naming Conventions

- Files: kebab-case with `-page` suffix — `nodes-page.tsx`, `credential-audit-page.tsx`
- Fragments: `<page-name>.<part>.tsx` — `nodes-page.table.tsx`, `logs-page.utils.tsx`
- Components: PascalCase exports — `NodesPage`, `NodesTable`, `NodesPageToolbar`
- Hooks: `use*` naming — `useNodesPageState` (in `.state.ts` fragment)
- Tests: `*.test.tsx` colocated, `*.test.ts` for pure utils

---

## Route Wiring

Routes are defined in `web/src/router.tsx` (route object construction only). Lazy component exports live in `web/src/router-pages.tsx` to keep the router module clean and enable Fast Refresh.

```tsx
// router-pages.tsx — lazy exports
export const LazyNodesPage = lazy(() => import("./pages/nodes-page").then(m => ({ default: m.NodesPage })));

// router.tsx — route tree
{
  path: "/app/nodes",
  element: <ProtectedRoute><LazyPage><LazyNodesPage /></LazyPage></ProtectedRoute>,
}
```

Protected routes are wrapped in `<ProtectedRoute>` (auth guard) and `<LazyPage>` (suspense fallback).

---

## Import Rules

- Import UI primitives from `@/components/ui/` — never create ad hoc primitives here
- Import API wrappers from `@/lib/api/` — never `fetch` directly in pages
- Import shared types from `@/types/domain` — never use `any` for API data
- Import hooks from `@/hooks/` (cross-page) or local `hooks/` subdirectory (page-local)
- Use `import type` for type-only imports

---

## Testing

- Colocated: `nodes-page.test.tsx` next to `nodes-page.tsx`
- Utils tests: `nodes-page.utils.test.ts` for pure functions
- Grouped tests: `__tests__/` subdirectory when multiple test files cover one page
- Use `@testing-library/react` + MSW for API mocking
- axe accessibility checks via `vitest-axe` matchers

---

## Forbidden Here

- Direct `fetch` calls — use typed API wrappers from `@/lib/api/`
- Ad hoc UI primitives — use `@/components/ui/`
- Raw `snake_case` API payloads — map to `camelCase` at API boundary
- `any` for component props or API responses — use proper types
- Creating a new folder for a one-off component — use a sibling fragment instead
- Premature splitting — single-file pages are fine until complexity grows
