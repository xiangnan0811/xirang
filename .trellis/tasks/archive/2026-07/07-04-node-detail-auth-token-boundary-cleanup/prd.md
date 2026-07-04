# Node detail auth token boundary cleanup

## Goal

Remove direct auth-token reads from the node detail feature so the feature uses
the existing auth context boundary instead of reaching into browser storage.
This keeps token ownership centralized and reduces duplicated token lookup logic
inside node-detail hooks and tabs.

This child task supports the parent slimming/refactor goal by reducing repeated
frontend auth plumbing without changing backend APIs or visible node detail
behavior.

## Confirmed Facts

- `NodesDetailPage` already sits at the route boundary and can read the current
  auth token with `useAuth()`.
- Several node-detail hooks and tabs currently call
  `sessionStorage.getItem("xirang-auth-token")` directly:
  - `use-node-status.ts`
  - `use-node-metrics.ts`
  - `use-disk-forecast.ts`
  - `tasks-tab.tsx`
  - `alerts-tab.tsx`
  - `profile-tab.tsx`
  - `log-config-tab.tsx`
- `AnomalyTab` already uses `useAuth()` directly, creating a mixed token-source
  pattern inside the same feature area.
- Existing node-detail tests seed `sessionStorage` to make feature components
  fetch data.

## Requirements

- Move node-detail feature data loading away from direct `sessionStorage` or
  `localStorage` auth-token reads.
- Use one token source for the node detail page: `NodesDetailPage` reads
  `token` through `useAuth()` and passes it to node-detail tabs and hooks.
- Preserve current API wrappers, endpoint contracts, request parameters,
  polling intervals, filtering behavior, loading states, and visible copy.
- Preserve the current behavior of skipping network requests when the token is
  missing or the node id is invalid.
- Update tests so node-detail feature tests pass tokens through props or hook
  params instead of mutating `sessionStorage`.
- Add a regression check that fails if production files in
  `web/src/features/nodes-detail` reintroduce direct auth-token storage reads.
- Do not change the auth context storage implementation or broader API client
  token behavior in this task.

## Acceptance Criteria

- [ ] `web/src/features/nodes-detail` production files contain no direct
      `sessionStorage.getItem("xirang-auth-token")` or equivalent
      `localStorage` token reads.
- [ ] `NodesDetailPage` obtains `token` from `useAuth()` and passes it to
      `OverviewTab`, `MetricsTab`, `TasksTab`, `AlertsTab`, `ProfileTab`,
      `LogConfigTab`, `AnomalyTab`, and the page-level `useNodeStatus` call.
- [ ] Node-detail hooks accept an explicit token argument or option and skip
      fetching when the token is absent.
- [ ] Existing node-detail rendering, filtering, save, export, and empty-state
      tests still pass after removing `sessionStorage` setup from feature tests.
- [ ] A source-boundary test covers the no-direct-storage-read rule for
      node-detail production files.
- [ ] `cd web && npm run check` passes locally, or any environment-only blocker
      is recorded.

## Notes

- This is a frontend-only cleanup. Backend behavior, API response shapes, auth
  persistence, and route access control are out of scope.
