# Node Detail Auth Token Boundary Cleanup Implementation Plan

## Checklist

1. Load frontend and shared Trellis specs before editing.
2. Write the regression tests first:
   - add a static source-boundary test for direct token storage reads in
     node-detail production files;
   - update existing node-detail tests to pass explicit tokens instead of
     seeding `sessionStorage`.
3. Run the focused tests to confirm the current implementation fails the new
   boundary expectation.
4. Implement explicit token props and hook parameters:
   - `NodesDetailPage` reads `token` from `useAuth()`;
   - pass token through all node-detail tabs;
   - pass token into `useNodeStatus`, `useNodeMetrics`, and `useDiskForecast`;
   - remove `sessionStorage` access from node-detail feature files.
5. Re-run focused tests.
6. Run full frontend verification and source search:
   - `cd web && npm run check`
   - `rg -n 'sessionStorage\\.getItem\\("xirang-auth-token"\\)|localStorage.*xirang-auth-token|xirang-auth-token' web/src/features/nodes-detail web/src/pages/nodes-detail-page.tsx`
   - `git diff --check`
7. Run Trellis validation/check flow, update specs only if a durable new
   convention is discovered, then commit and archive the child task.

## Risky Files

- `web/src/pages/nodes-detail-page.tsx`: route-level props must pass token to
  every tab without changing tab selection behavior.
- `web/src/features/nodes-detail/use-node-status.ts`: status polling must keep
  abort and interval behavior intact.
- `web/src/features/nodes-detail/use-node-metrics.ts`: `fieldsKey` dependency
  must remain stable while adding token.
- `web/src/features/nodes-detail/log-config-tab.tsx`: save path must use the
  same explicit token as the load path.
- `web/src/features/nodes-detail/anomaly-tab.tsx`: removing direct `useAuth()`
  should preserve loading, empty, and toast behavior.

## Validation Commands

```bash
cd web && npx vitest run src/features/nodes-detail src/pages/nodes-detail-page.test.tsx
cd web && npm run check
rg -n 'sessionStorage\\.getItem\("xirang-auth-token"\)|localStorage.*xirang-auth-token|xirang-auth-token' web/src/features/nodes-detail web/src/pages/nodes-detail-page.tsx
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-04-node-detail-auth-token-boundary-cleanup
```

The `rg` command may still show test strings if run against tests. The required
acceptance check is that production node-detail files do not contain direct
browser storage auth-token reads.
