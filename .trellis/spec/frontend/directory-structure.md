# Directory Structure

> How frontend code is organized in this project.

---

## Overview

The frontend is a Vite + React + TypeScript app under `web/`. Source code
lives in `web/src/`, with application routing in `web/src/router.tsx` and the
React entry point in `web/src/main.tsx`.

Use the existing split between reusable UI, app-level components, pages,
contexts, hooks, API clients, and domain types before creating new folders.
Pages often start as a main `*-page.tsx` file and are split into sibling
`*-page.<part>.tsx` files when the page grows.

---

## Directory Layout

```text
web/src/
├── components/          # app-level reusable components and dialogs
│   ├── layout/          # app shell, sidebar, mobile navigation
│   └── ui/              # shared design-system primitives
├── context/             # React context providers for app-wide state
├── features/            # focused feature modules, e.g. nodes-detail
├── hooks/               # reusable custom hooks and console data operations
├── i18n/                # i18next setup and locale files
├── lib/                 # API clients, utilities, themes, ws helpers
│   ├── api/             # typed API wrappers and response mappers
│   └── ws/              # WebSocket client helpers
├── pages/               # route-level pages and page fragments
├── types/               # shared domain types
└── data/                # local/demo/mock data
```

---

## Module Organization

- Put route-level screens in `web/src/pages/` and wire routes in
  `web/src/router.tsx`. Examples: `overview-page.tsx`, `tasks-page.tsx`, and
  `settings-page.tsx`.
- Split large pages into sibling fragments instead of deeply nesting one-off
  folders. Examples: `nodes-page.table.tsx`, `tasks-page.dialogs.tsx`, and
  `overview-page.traffic.tsx`.
- Use `web/src/features/<feature>/` for cohesive feature modules that need
  their own hooks, tabs, charts, and tests. Current example:
  `web/src/features/nodes-detail/`.
- Put shared visual primitives in `web/src/components/ui/`. App-specific
  dialogs and panels belong in `web/src/components/` unless they are route-only.
- Put API wrappers in `web/src/lib/api/` and map backend snake_case payloads to
  frontend camelCase domain objects there.
- Put cross-page reusable hooks in `web/src/hooks/`; page-local hooks can live
  beside the page, as in `web/src/pages/dashboards/hooks/`.

---

## Naming Conventions

- Files use kebab-case: `node-editor-dialog.tsx`,
  `use-console-data.utils.ts`, `overview-api.ts`.
- React components are PascalCase exports from kebab-case files.
- Custom hooks use `use*` names and live in `hooks/`, feature folders, or
  page-local `hooks/`.
- Tests are colocated as `*.test.ts` or `*.test.tsx`.
- Shared domain types are centralized in `web/src/types/domain.ts`; API-specific
  raw payload types can stay private in the relevant `web/src/lib/api/*.ts`.

---

## Examples

- `web/src/components/ui/button.tsx` and `card.tsx` show shared primitive
  components with variant helpers and reusable styling.
- `web/src/pages/tasks-page.tsx` plus `tasks-page.*.tsx` shows the page-fragment
  split used for large route screens.
- `web/src/features/nodes-detail/` shows the preferred feature-module shape for
  a complex detail page.
- `web/src/lib/api/core.ts` shows the central request/envelope handling used by
  every API wrapper.
- `web/src/context/auth-context.tsx` shows provider-based app state with safe
  browser storage access.
