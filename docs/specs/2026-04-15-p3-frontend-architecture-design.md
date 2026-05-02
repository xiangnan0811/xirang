# P3: Frontend Architecture — Lazy Loading, Component Splitting, Context Splitting

> Historical note: This dated design snapshot documents the plan at the time it was written. Treat it as implementation history, not current operating documentation; verify commands, paths, and workflow behavior against the current repo before acting.

Phase 3 of the Xirang improvement plan. Restructures the frontend for better performance, maintainability, and developer experience.

## Scope

One PR with three areas, implemented bottom-up:

1. **React.lazy dialog loading** — lazy-load 11 heavy dialog/wizard components
2. **Large component splitting** — extract sub-components from 6 pages >500 lines
3. **Context splitting** — break 59-property ConsoleOutletContext into 7 domain-specific contexts

## Out of Scope

- framer-motion replacement (only 2 files use it — not a real bundle liability)
- Test coverage expansion (deferred)
- Backend changes

## Part 1: React.lazy Dialog Loading

### Problem

11 dialog/wizard components (6,767 lines total) are eagerly loaded at startup. Most users never open half of them in a session.

### Solution

Wrap each dialog import with `React.lazy()` and `Suspense`.

```tsx
const TaskCreateDialog = React.lazy(() =>
  import("@/components/task-create-dialog").then(m => ({ default: m.TaskCreateDialog }))
);

// Usage wrapped in Suspense:
<Suspense fallback={null}>
  {showCreate && <TaskCreateDialog ... />}
</Suspense>
```

### Components to lazy-load

| Component | Lines | Parent Page |
|-----------|-------|-------------|
| `task-create-dialog.tsx` | 471 | tasks-page |
| `node-editor-dialog.tsx` | 466 | nodes-page |
| `integration-create-dialog.tsx` | 462 | settings-page.channels |
| `policy-editor-dialog.tsx` | 444 | policies-page |
| `nas-mount-wizard.tsx` | 444 | nodes-page |
| `setup-wizard.tsx` | 425 | app-shell |
| `integration-editor-dialog.tsx` | 420 | settings-page.channels |
| `ssh-key-batch-import-dialog.tsx` | 377 | ssh-keys-page |
| `node-migrate-wizard.tsx` | 353 | nodes-page |
| `report-config-dialog.tsx` | 322 | reports-page |
| `ssh-key-rotation-wizard.tsx` | 618 | ssh-keys-page |

### Fallback strategy

- Dialogs (hidden until opened): `fallback={null}` — no visual flash
- Wizards shown in-page (setup-wizard): simple loading spinner

### Export requirement

Each dialog component must have a **default export** or the lazy import must use `.then(m => ({ default: m.ComponentName }))` to map the named export. Check each file and use the appropriate pattern.

## Part 2: Large Component Splitting

### Problem

6 pages exceed 500 lines, mixing rendering, state management, and business logic in single files.

### Splitting plan

**logs-page.tsx (715 lines) → pages/logs/ subfolder:**
- `pages/logs/logs-page.tsx` — orchestrator (state + layout)
- `pages/logs/logs-filter-bar.tsx` — filter controls (task selector, level, search)
- `pages/logs/logs-viewer.tsx` — live WebSocket stream + terminal rendering
- `pages/logs/logs-history.tsx` — historical log browsing with pagination

**notifications-page.alert-center.tsx (636 lines) → pages/notifications/ subfolder:**
- `pages/notifications/alert-center.tsx` — orchestrator
- `pages/notifications/alert-filters.tsx` — status/severity/date filters
- `pages/notifications/alert-list.tsx` — alert cards with delivery badges
- `pages/notifications/alert-bulk-actions.tsx` — bulk acknowledge/resolve/retry toolbar

**ssh-key-rotation-wizard.tsx (618 lines) → components/ssh-key-rotation/ subfolder:**
- `components/ssh-key-rotation/ssh-key-rotation-wizard.tsx` — orchestrator (step state)
- `components/ssh-key-rotation/rotation-preview.tsx` — step 1: affected nodes
- `components/ssh-key-rotation/rotation-progress.tsx` — step 2-3: per-node status
- `components/ssh-key-rotation/rotation-summary.tsx` — step 4: results

**tasks-page.tsx (611 lines) → flat extractions:**
- `pages/tasks-grid.tsx` — card/grid view of tasks
- `pages/tasks-filters.tsx` — filtering + sorting controls

**overview-page.tsx (564 lines) → flat extractions:**
- `pages/overview-traffic-chart.tsx` — recharts traffic/throughput charts
- `pages/overview-recent-tasks.tsx` — recent task runs table

**policies-page.tsx (503 lines) → flat extractions:**
- `pages/policy-card.tsx` — individual policy card
- `pages/policy-filters.tsx` — template/active/cron filters

### Placement rules

- 3+ extractions → subfolder (logs/, notifications/, ssh-key-rotation/)
- 1-2 extractions → flat files alongside parent page

### Extraction principles

- Parent page becomes a thin orchestrator: state management + layout composition
- Extracted components receive data via props (not context) for explicit dependencies
- Each extracted component should be understandable in isolation
- Keep the same file naming conventions as the rest of the codebase

## Part 3: Context Splitting

### Problem

`ConsoleOutletContext` has 59 properties mixing 6 domains. Every data change re-renders all 8 consuming pages.

### New architecture

```
AppShell
├── SharedContextProvider (refresh, loading, globalSearch, lastSyncedAt, overview)
│   ├── NodesContextProvider (nodes + CRUD)
│   ├── TasksContextProvider (tasks + CRUD)
│   ├── PoliciesContextProvider (policies + CRUD)
│   ├── AlertsContextProvider (alerts + CRUD)
│   ├── IntegrationsContextProvider (integrations + CRUD)
│   └── SSHKeysContextProvider (sshKeys + CRUD)
│       └── <Outlet />
```

### Context definitions

**SharedContext** — cross-cutting concerns:
- `loading: boolean`
- `lastSyncedAt: string | null`
- `refreshVersion: number`
- `globalSearch: string`
- `setGlobalSearch: (s: string) => void`
- `refresh: () => void` — triggers all domains to reload
- `overview: OverviewData` — summary stats for dashboard

**NodesContext:**
- `nodes: Node[]`
- `createNode`, `updateNode`, `deleteNode`, `deleteNodes`
- `testNodeConnection`, `triggerNodeBackup`
- `refreshNodes: () => void`

**TasksContext:**
- `tasks: Task[]`
- `createTask`, `updateTask`, `deleteTask`
- `triggerTask`, `cancelTask`, `retryTask`
- `pauseTask`, `resumeTask`, `skipNextTask`
- `refreshTask`, `fetchTaskLogs`
- `refreshTasks: () => void`

**PoliciesContext:**
- `policies: Policy[]`
- `createPolicy`, `updatePolicy`, `deletePolicy`
- `togglePolicy`, `updatePolicySchedule`
- `refreshPolicies: () => void`

**AlertsContext:**
- `alerts: Alert[]`
- `retryAlert`, `acknowledgeAlert`, `resolveAlert`
- `fetchAlertDeliveries`, `fetchAlertDeliveryStats`
- `retryAlertDelivery`, `retryFailedAlertDeliveries`
- `refreshAlerts: () => void`

**IntegrationsContext:**
- `integrations: Integration[]`
- `addIntegration`, `removeIntegration`, `toggleIntegration`
- `updateIntegration`, `patchIntegration`, `testIntegration`
- `refreshIntegrations: () => void`

**SSHKeysContext:**
- `sshKeys: SSHKey[]`
- `createSSHKey`, `updateSSHKey`, `deleteSSHKey`
- `refreshSSHKeys: () => void`

### File structure

```
context/
├── auth-context.tsx          (existing)
├── theme-context.tsx         (existing)
├── shared-context.tsx        (new)
├── nodes-context.tsx         (new)
├── tasks-context.tsx         (new)
├── policies-context.tsx      (new)
├── alerts-context.tsx        (new)
├── integrations-context.tsx  (new)
└── ssh-keys-context.tsx      (new)
```

### Migration strategy

1. Create all 7 new context files with their providers and hooks
2. Each context provider calls its own API functions directly (from `lib/api/`) — no intermediary hook. The data-fetching logic from `use-console-data.ts` is decomposed into each provider.
3. Update `app-shell.tsx` to nest the providers instead of using a single outlet context
4. Update each page to import from domain-specific contexts instead of `useOutletContext`
5. After all pages are migrated, delete the old `ConsoleOutletContext` type and the monolithic `use-console-data.ts` (or reduce it to a thin composition)

### Page → context mapping

| Page | Contexts needed |
|------|----------------|
| overview-page | SharedContext, NodesContext, TasksContext, AlertsContext |
| nodes-page | SharedContext, NodesContext, SSHKeysContext |
| tasks-page | SharedContext, TasksContext, NodesContext, PoliciesContext |
| logs-page | SharedContext, TasksContext |
| policies-page | SharedContext, PoliciesContext, NodesContext |
| notifications-page | SharedContext, AlertsContext, IntegrationsContext |
| settings-page.channels | SharedContext, IntegrationsContext |
| ssh-keys-page | SharedContext, SSHKeysContext, NodesContext |

### Re-render optimization

Each domain context only triggers re-renders for subscribed pages. When alerts change, the nodes page won't re-render.

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Context split breaks data flow | Medium | High | Migrate one page at a time, test after each |
| Lazy dialog flickers on slow network | Low | Low | null fallback for hidden dialogs |
| Component extraction breaks props | Medium | Medium | Type-check after each extraction (tsc) |
| use-console-data.ts decomposition is complex | Medium | High | Keep old hook as fallback until all pages migrated |

## Implementation Order

1. React.lazy for 11 dialogs (independent, low risk)
2. Split logs-page.tsx (largest, good test for the pattern)
3. Split remaining 5 large pages
4. Create 7 new context files with providers
5. Update app-shell.tsx with nested providers
6. Migrate pages to domain contexts (one by one)
7. Clean up old ConsoleOutletContext
8. Final verification
