# Hook Guidelines

> How hooks are used in this project.

---

## Overview

Custom hooks are used for reusable UI state, console data orchestration, API
action wrappers, preferences, filtering, pagination, live logs, and feature
logic. This project does not use React Query or SWR; server data is currently
managed by explicit context providers and hook-level fetch/update functions.

---

## Custom Hook Patterns

- Name every custom hook with `use*` and keep it in `web/src/hooks/`, a feature
  module, or a page-local `hooks/` directory.
- Keep pure helper functions separate from hooks when possible. Example:
  `use-console-data.utils.ts` contains testable non-React logic used by
  `use-console-data.ts`.
- Hooks that wrap mutating API calls should expose stable action functions and
  consistent loading/error handling. Existing pattern:
  `web/src/hooks/use-api-action.ts`.
- Prefer small operation-specific hooks over one large hook when a page has
  distinct domains. Existing examples:
  `use-console-node-operations.ts`,
  `use-console-policy-operations.ts`,
  `use-console-task-operations.ts`, and
  `use-console-integration-alert-operations.ts`.

---

## Data Fetching

- Centralize request mechanics in `web/src/lib/api/core.ts`; hooks and contexts
  should call typed API wrappers rather than `fetch` directly.
- API wrappers should normalize payloads into domain types before hooks store or
  render them. Examples: `overview-api.ts`, `tasks-api.ts`, and
  `node-metrics-api.ts`.
- Use `AbortSignal` for cancellable page requests when the underlying API
  wrapper supports it. Existing example: overview API methods accept
  `options?: { signal?: AbortSignal }`.
- Real-time log streaming uses WebSocket helpers under `web/src/lib/ws/` and
  hooks such as `use-live-logs.ts`; do not reimplement socket handling inside
  page components.

---

## Naming Conventions

- `usePersistentState` and `useUserPreferences` are the established patterns for
  local preference persistence.
- `usePageFilters` owns reusable page filter state; prefer it over creating
  new ad hoc filter state if the behavior matches.
- Feature-local hooks may live with feature files, for example
  `features/nodes-detail/use-node-metrics.ts`.
- Hook tests should be colocated and named `*.test.ts` or `*.test.tsx`.

---

## Common Mistakes

- Do not call `fetch` directly in components or random hooks when a typed API
  wrapper exists or should exist.
- Do not store backend raw payloads in contexts. Store frontend domain shapes.
- Do not make a hook responsible for unrelated domains just to avoid passing
  props; split by operation or feature when responsibilities diverge.
- Do not read/write browser storage without guarding for unavailable storage.
  Follow the safe access pattern in `auth-context.tsx`.
- Do not omit cleanup for subscriptions, timers, abortable requests, or
  WebSocket listeners.
