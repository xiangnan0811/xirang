# State Management

> How state is managed in this project.

---

## Overview

State is managed with React Context, component state, URL/page-local state,
browser storage, and explicit API wrappers. There is no Redux, Zustand, React
Query, or SWR dependency in the current frontend.

Contexts under `web/src/context/` own shared application domains: auth, nodes,
policies, tasks, integrations, SSH keys, alerts, theme, command palette, and a
shared console provider. Page-local state stays inside the route page or a
page-specific state file.

---

## State Categories

- **Auth/session state**: `web/src/context/auth-context.tsx`, stored in
  `sessionStorage` with safe legacy migration from `localStorage`.
- **Theme/preferences**: `theme-context.tsx`, `use-user-preferences.ts`, and
  `use-persistent-state.ts`.
- **Domain collections**: context providers such as `nodes-context.tsx`,
  `tasks-context.tsx`, `policies-context.tsx`, and
  `integrations-context.tsx`.
- **Page view state**: filters, pagination, dialog visibility, draft form state,
  and view mode stay page-local or hook-local.
- **Derived UI state**: calculate from canonical domain state where feasible;
  avoid storing both source and derived values.
- **Streaming state**: logs and terminal-like streams use WebSocket helpers and
  dedicated hooks/components.

---

## When to Use Global State

Use context/global state when:

- Multiple routes or major panels need the same authenticated domain data.
- A mutation in one page must refresh or affect another part of the console.
- The value is truly app-wide, such as auth, theme, command palette, or shared
  console data.

Keep state local when:

- It only controls one dialog, table, filter panel, or form.
- It can be derived from props or an API result.
- It is temporary draft state. Use `use-dialog-draft.ts` for dialog drafts that
  follow the existing pattern.

---

## Server State

- Server state is fetched through typed API wrappers in `web/src/lib/api/`.
- Contexts expose refresh/mutation functions instead of hiding request side
  effects inside components.
- Paginated endpoints should use `unwrapPaginated` from `core.ts` so pages see
  `items`, `total`, `page`, and `pageSize`.
- Keep optimistic updates conservative. This admin console favors correctness
  and explicit refreshes over speculative UI updates for backup, SSH, security,
  and alerting operations.
- Map timestamps, numbers, enum-like status values, and optional fields in API
  wrapper functions before storing them.

---

## Common Mistakes

- Do not promote every page filter or dialog flag into context.
- Do not persist sensitive session data in `localStorage`; current auth state
  uses `sessionStorage` and removes old localStorage keys.
- Do not duplicate shared filter/search state in multiple layers. Past filter
  sync issues caused lists to appear empty while stats still showed data.
- Do not store both raw backend values and mapped domain objects in the same
  state tree.
- Do not add a new state library without an explicit architectural decision.
