# Node Detail Auth Token Boundary Cleanup Design

## Boundary

`NodesDetailPage` is the route-level owner for node-detail authentication
context. It will import `useAuth()`, read `token`, and pass that token through
props to node-detail tabs and through hook parameters to page-level status
loading.

Node-detail feature files should no longer read auth tokens from browser
storage. The auth context remains the only code in this task that owns how the
token is persisted or restored.

## Data Flow

Before:

1. `NodesDetailPage` renders tabs with only `nodeId`.
2. Individual hooks and tabs call
   `sessionStorage.getItem("xirang-auth-token")`.
3. Tests seed `sessionStorage` to make fetches run.

After:

1. `NodesDetailPage` calls `useAuth()` once and reads `token`.
2. `NodesDetailPage` passes `token` to `useNodeStatus`, `OverviewTab`,
   `MetricsTab`, `TasksTab`, `AlertsTab`, `ProfileTab`, `LogConfigTab`, and
   `AnomalyTab`.
3. `OverviewTab` passes `token` to `useNodeStatus`, `useNodeMetrics`, and
   `DiskForecastCard`.
4. `DiskForecastCard` passes `token` to `useDiskForecast`.
5. Hooks and tabs skip network calls when `token` is nullish or empty, matching
   current missing-token behavior.

## Contracts

- `token` props use `string | null` so they match the auth-context contract and
  make the missing-token path explicit.
- Hook signatures become explicit about auth:
  - `useNodeStatus(nodeId, token)`
  - `useNodeMetrics({ nodeId, token, ... })`
  - `useDiskForecast(nodeId, token)`
- Tab props become explicit about auth:
  - `{ nodeId: number; token: string | null }`
- Existing API client wrappers continue receiving `string` tokens only after
  each caller has checked that `token` is present.

## Compatibility

This task does not alter token storage, migration from legacy localStorage, API
client authorization headers, backend routes, or node-detail route URLs. Users
should see the same node detail screens and request behavior after login.

The only intentional internal change is the dependency direction: feature code
depends on a token value passed by the page instead of depending on a browser
storage key.

## Testing Strategy

- Update component tests to pass `token="test-token"` and stop mutating
  `sessionStorage`.
- Update mocked hook tests to assert token is supplied where useful.
- Add a static source-boundary test under `web/src/features/nodes-detail` that
  scans production `.ts`/`.tsx` files in that directory and rejects direct auth
  token storage reads.
- Run focused node-detail tests first, then the full frontend gate.

## Rollback

Rollback is a single frontend-only commit revert. Because API contracts and auth
storage are unchanged, reverting restores the previous direct storage reads
without requiring database, backend, or migration cleanup.
