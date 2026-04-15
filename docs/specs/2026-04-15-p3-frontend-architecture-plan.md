# P3: Frontend Architecture — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve frontend performance and maintainability by lazy-loading 11 dialogs, splitting 6 large pages into focused sub-components, and replacing the 59-property ConsoleOutletContext with 7 domain-specific contexts.

**Architecture:** Bottom-up approach — lazy-load dialogs first (independent, low risk), then split large components (reduces file size), then split contexts (rewire data flow with smaller, focused components). Each step is independently verifiable.

**Tech Stack:** React 18, TypeScript 5.8, Vite 7, React Router 6

---

## Lazy Loading Pattern

All dialog/wizard components use **named exports**. The React.lazy pattern for named exports:

```tsx
const ComponentName = React.lazy(() =>
  import("@/components/component-file").then(m => ({ default: m.ComponentName }))
);
```

Usage must be wrapped in Suspense:
```tsx
<Suspense fallback={null}>
  {showDialog && <ComponentName ... />}
</Suspense>
```

---

### Task 1: Lazy-load all 11 dialog/wizard components

**Files to modify** (each file imports one or more dialogs that need lazy-loading):
- `web/src/pages/tasks-page.tsx` — TaskCreateDialog (exported as `TaskEditorDialog`, re-exported as `TaskCreateDialog`)
- `web/src/pages/nodes-page.tsx` or `web/src/pages/nodes-page.state.ts` — NodeEditorDialog, NasMountWizard, NodeMigrateWizard
- `web/src/pages/settings-page.channels.tsx` — IntegrationCreateDialog, IntegrationEditorDialog
- `web/src/pages/policies-page.tsx` — PolicyEditorDialog
- `web/src/components/layout/app-shell.tsx` — SetupWizard
- `web/src/pages/ssh-keys-page.tsx` — SSHKeyBatchImportDialog, SSHKeyRotationWizard
- `web/src/pages/reports-page.tsx` — ReportConfigDialog

- [ ] **Step 1: Find each dialog import and its parent file**

For each of the 11 components, search for where it's imported:
```bash
cd web && grep -rn "import.*TaskCreateDialog\|import.*TaskEditorDialog\|import.*NodeEditorDialog\|import.*IntegrationCreateDialog\|import.*PolicyEditorDialog\|import.*NasMountWizard\|import.*SetupWizard\|import.*IntegrationEditorDialog\|import.*SSHKeyBatchImportDialog\|import.*NodeMigrateWizard\|import.*ReportConfigDialog\|import.*SSHKeyRotationWizard" src/
```

- [ ] **Step 2: Convert each import to React.lazy**

For each parent file, replace the static import with a lazy import. Add `React, { Suspense }` to imports if not already present.

Example for tasks-page.tsx:
```tsx
// Before:
import { TaskCreateDialog } from "@/components/task-create-dialog";

// After:
import React, { Suspense } from "react";
const TaskCreateDialog = React.lazy(() =>
  import("@/components/task-create-dialog").then(m => ({ default: m.TaskCreateDialog }))
);
```

For task-create-dialog.tsx specifically, note the export is `TaskEditorDialog` re-exported as `TaskCreateDialog`:
```tsx
export { TaskEditorDialog as TaskCreateDialog };
```
So the lazy import should use: `m.TaskCreateDialog`

For SetupWizard in app-shell.tsx, use a loading spinner fallback since it may show immediately:
```tsx
<Suspense fallback={<div className="flex items-center justify-center p-8"><RefreshCw className="size-5 animate-spin text-muted-foreground" /></div>}>
  <SetupWizard />
</Suspense>
```

For all other dialogs (conditionally rendered), use `fallback={null}`.

- [ ] **Step 3: Wrap each dialog usage in Suspense**

Find the JSX where each dialog is rendered and wrap it:
```tsx
// Before:
{showCreate && <TaskCreateDialog ... />}

// After:
<Suspense fallback={null}>
  {showCreate && <TaskCreateDialog ... />}
</Suspense>
```

- [ ] **Step 4: Verify**

Run: `cd web && npm run check`
Expected: typecheck + lint + tests + build all pass.

Check that the build produces separate chunks for each lazy-loaded dialog:
Run: `ls -la web/dist/assets/ | grep -i "dialog\|wizard" | head -15`

- [ ] **Step 5: Commit**

```bash
git add -u web/src/
git commit -m "$(cat <<'EOF'
perf(web): lazy-load 11 dialog/wizard components

Wrap all heavy dialog imports (>300 lines each) with React.lazy().
Total: 6,767 lines of dialog code now loaded on-demand instead of
at startup. Dialogs use fallback={null} since they're hidden until
user interaction.
EOF
)"
```

---

### Task 2: Split logs-page.tsx (715 lines)

**Files:**
- Create: `web/src/pages/logs/` directory
- Create: `web/src/pages/logs/logs-page.tsx` — orchestrator
- Create: `web/src/pages/logs/logs-filter-bar.tsx` — filter controls
- Create: `web/src/pages/logs/logs-viewer.tsx` — live WebSocket stream
- Create: `web/src/pages/logs/logs-history.tsx` — historical browsing
- Delete: `web/src/pages/logs-page.tsx` (original)
- Modify: router registration to point to new path

- [ ] **Step 1: Read the current logs-page.tsx and identify extraction boundaries**

Read `web/src/pages/logs-page.tsx` in full. Identify:
- Filter bar JSX and its local state (task selector dropdown, level filter, search input)
- Live viewer section (WebSocket connection, terminal-like output area)
- History section (paginated log list, load-more behavior)
- State that is shared between sections vs local to each

- [ ] **Step 2: Create the logs/ directory and extract components**

Create `web/src/pages/logs/` directory.

Extract each section as a component that receives its data/callbacks via props. The parent `logs-page.tsx` keeps all state and passes it down.

**logs-filter-bar.tsx** props:
- Selected task, task list, level filter, search text
- onChange callbacks for each filter

**logs-viewer.tsx** props:
- Log entries array, loading state
- Auto-scroll toggle, fullscreen toggle

**logs-history.tsx** props:
- Historical entries, pagination state
- onLoadMore callback

- [ ] **Step 3: Create the new orchestrator logs-page.tsx**

The orchestrator imports the 3 sub-components and composes them. It keeps all state (useOutletContext, useState, WebSocket hook) and passes data down via props.

- [ ] **Step 4: Update router import**

Update the lazy import in `web/src/router.tsx` to point to `pages/logs/logs-page` instead of `pages/logs-page`.

- [ ] **Step 5: Delete the old logs-page.tsx**

- [ ] **Step 6: Verify**

Run: `cd web && npm run check`
Expected: All pass. No visible changes in the UI.

- [ ] **Step 7: Commit**

```bash
git add web/src/pages/logs/ web/src/router.tsx
git rm web/src/pages/logs-page.tsx
git commit -m "$(cat <<'EOF'
refactor(web): split logs-page into focused sub-components

Extract LogsFilterBar, LogsViewer, LogsHistory from 715-line
monolith. Orchestrator keeps state, sub-components receive props.
EOF
)"
```

---

### Task 3: Split notifications alert-center (636 lines)

**Files:**
- Create: `web/src/pages/notifications/` directory
- Create: `web/src/pages/notifications/alert-center.tsx` — orchestrator
- Create: `web/src/pages/notifications/alert-filters.tsx`
- Create: `web/src/pages/notifications/alert-list.tsx`
- Create: `web/src/pages/notifications/alert-bulk-actions.tsx`
- Modify: `web/src/pages/notifications-page.tsx` (update import)
- Delete: `web/src/pages/notifications-page.alert-center.tsx`

Same pattern as Task 2:
1. Read the current file and identify boundaries
2. Extract filter bar, alert list, and bulk action toolbar
3. Create orchestrator that composes them
4. Update the parent notifications-page.tsx import
5. Delete old file
6. Verify with `npm run check`

- [ ] **Step 1: Read and plan extraction boundaries**
- [ ] **Step 2: Create notifications/ directory and extract components**
- [ ] **Step 3: Create orchestrator alert-center.tsx**
- [ ] **Step 4: Update notifications-page.tsx import path**
- [ ] **Step 5: Delete old notifications-page.alert-center.tsx**
- [ ] **Step 6: Verify** — `cd web && npm run check`
- [ ] **Step 7: Commit**

```bash
git add web/src/pages/notifications/
git rm web/src/pages/notifications-page.alert-center.tsx
git commit -m "$(cat <<'EOF'
refactor(web): split alert-center into focused sub-components

Extract AlertFilters, AlertList, AlertBulkActions from 636-line
monolith into pages/notifications/ subfolder.
EOF
)"
```

---

### Task 4: Split ssh-key-rotation-wizard (618 lines)

**Files:**
- Create: `web/src/components/ssh-key-rotation/` directory
- Create: `web/src/components/ssh-key-rotation/ssh-key-rotation-wizard.tsx` — orchestrator
- Create: `web/src/components/ssh-key-rotation/rotation-preview.tsx` — step 1
- Create: `web/src/components/ssh-key-rotation/rotation-progress.tsx` — steps 2-3
- Create: `web/src/components/ssh-key-rotation/rotation-summary.tsx` — step 4
- Delete: `web/src/components/ssh-key-rotation-wizard.tsx`
- Update: parent page import + lazy import path

Same extraction pattern:
1. Read current file, identify step boundaries
2. Extract each step as a component receiving state/callbacks via props
3. Orchestrator manages step state and composes step components
4. Update imports in parent (ssh-keys-page) and lazy loading (Task 1 paths)
5. Delete old file

- [ ] **Step 1: Read and plan step extraction**
- [ ] **Step 2: Create ssh-key-rotation/ directory and extract step components**
- [ ] **Step 3: Create orchestrator**
- [ ] **Step 4: Update imports (parent page + lazy path)**
- [ ] **Step 5: Delete old file**
- [ ] **Step 6: Verify** — `cd web && npm run check`
- [ ] **Step 7: Commit**

```bash
git add web/src/components/ssh-key-rotation/
git rm web/src/components/ssh-key-rotation-wizard.tsx
git commit -m "$(cat <<'EOF'
refactor(web): split ssh-key-rotation-wizard by step

Extract RotationPreview, RotationProgress, RotationSummary from
618-line wizard into components/ssh-key-rotation/ subfolder.
EOF
)"
```

---

### Task 5: Split tasks-page, overview-page, policies-page (flat extractions)

**Files:**
- Create: `web/src/pages/tasks-grid.tsx`, `web/src/pages/tasks-filters.tsx`
- Create: `web/src/pages/overview-traffic-chart.tsx`, `web/src/pages/overview-recent-tasks.tsx`
- Create: `web/src/pages/policy-card.tsx`, `web/src/pages/policy-filters.tsx`
- Modify: `web/src/pages/tasks-page.tsx`, `web/src/pages/overview-page.tsx`, `web/src/pages/policies-page.tsx`

For each page (tasks, overview, policies):
1. Read the current file
2. Identify the heaviest rendering sections
3. Extract as flat sibling files (1-2 per page)
4. Parent imports and composes them
5. Verify

- [ ] **Step 1: Split tasks-page.tsx** — extract TasksGrid and TasksFilters
- [ ] **Step 2: Split overview-page.tsx** — extract TrafficChartSection and RecentTasksSection
- [ ] **Step 3: Split policies-page.tsx** — extract PolicyCard and PolicyFilters
- [ ] **Step 4: Verify** — `cd web && npm run check`
- [ ] **Step 5: Commit**

```bash
git add web/src/pages/tasks-grid.tsx web/src/pages/tasks-filters.tsx
git add web/src/pages/overview-traffic-chart.tsx web/src/pages/overview-recent-tasks.tsx
git add web/src/pages/policy-card.tsx web/src/pages/policy-filters.tsx
git add -u web/src/pages/
git commit -m "$(cat <<'EOF'
refactor(web): split tasks/overview/policies pages

Extract TasksGrid, TasksFilters, TrafficChartSection,
RecentTasksSection, PolicyCard, PolicyFilters from 3 large pages.
EOF
)"
```

---

### Task 6: Create 7 domain context files

**Files:**
- Create: `web/src/context/shared-context.tsx`
- Create: `web/src/context/nodes-context.tsx`
- Create: `web/src/context/tasks-context.tsx`
- Create: `web/src/context/policies-context.tsx`
- Create: `web/src/context/alerts-context.tsx`
- Create: `web/src/context/integrations-context.tsx`
- Create: `web/src/context/ssh-keys-context.tsx`

Each context file follows this pattern:

```tsx
import { createContext, useContext, type ReactNode } from "react";

interface NodesContextValue {
  nodes: NodeRecord[];
  createNode: (input: NewNodeInput) => Promise<number>;
  updateNode: (nodeId: number, input: NewNodeInput) => Promise<void>;
  deleteNode: (nodeId: number) => Promise<void>;
  deleteNodes: (nodeIds: number[]) => Promise<{ deleted: number; notFoundIds: number[] }>;
  testNodeConnection: (nodeId: number) => Promise<{ ok: boolean; message: string }>;
  triggerNodeBackup: (nodeId: number) => Promise<void>;
  refreshNodes: () => void;
}

const NodesContext = createContext<NodesContextValue | null>(null);

export function useNodesContext(): NodesContextValue {
  const ctx = useContext(NodesContext);
  if (!ctx) throw new Error("useNodesContext must be used within NodesContextProvider");
  return ctx;
}

export function NodesContextProvider({ children, value }: { children: ReactNode; value: NodesContextValue }) {
  return <NodesContext.Provider value={value}>{children}</NodesContext.Provider>;
}
```

- [ ] **Step 1: Read the full ConsoleDataState interface** from `use-console-data.ts` lines 41-105 to get exact types and method signatures for each domain.

- [ ] **Step 2: Read the existing operation hooks** to understand the data flow:
- `hooks/use-console-node-operations.ts` — node CRUD ops
- `hooks/use-console-task-operations.ts` — task ops
- `hooks/use-console-policy-operations.ts` — policy ops
- `hooks/use-console-integration-alert-operations.ts` — alert + integration ops

- [ ] **Step 3: Create shared-context.tsx**

SharedContext holds cross-cutting state: `loading`, `lastSyncedAt`, `refreshVersion`, `globalSearch`, `setGlobalSearch`, `refresh`, `overview`, `warning`.

- [ ] **Step 4: Create the 6 domain context files**

Each context file: interface + createContext + useXxxContext hook + XxxContextProvider component. Use the exact types from `ConsoleDataState`.

- [ ] **Step 5: Verify compilation** — `cd web && npx tsc --noEmit`

- [ ] **Step 6: Commit**

```bash
git add web/src/context/shared-context.tsx web/src/context/nodes-context.tsx web/src/context/tasks-context.tsx web/src/context/policies-context.tsx web/src/context/alerts-context.tsx web/src/context/integrations-context.tsx web/src/context/ssh-keys-context.tsx
git commit -m "$(cat <<'EOF'
feat(web): create 7 domain-specific context definitions

SharedContext + NodesContext + TasksContext + PoliciesContext +
AlertsContext + IntegrationsContext + SSHKeysContext.
Providers accept value prop — wiring comes in next task.
EOF
)"
```

---

### Task 7: Wire contexts in app-shell.tsx

**Files:**
- Modify: `web/src/components/layout/app-shell.tsx`

- [ ] **Step 1: Read current app-shell.tsx** to understand how `useConsoleData` result is passed to `AnimatedOutlet`.

Currently:
```tsx
const consoleData = useConsoleData(token);
// ...
<AnimatedOutlet context={consoleData as ConsoleOutletContext} />
```

- [ ] **Step 2: Nest domain providers around the Outlet**

Replace the single `useOutlet(context)` pattern with nested providers. The `useConsoleData` hook still provides all the data — we're just distributing it through domain-specific providers instead of a single outlet context.

```tsx
<SharedContextProvider value={{ loading: consoleData.loading, ... }}>
  <NodesContextProvider value={{ nodes: consoleData.nodes, createNode: consoleData.createNode, ... }}>
    <TasksContextProvider value={{ tasks: consoleData.tasks, ... }}>
      <PoliciesContextProvider value={{ ... }}>
        <AlertsContextProvider value={{ ... }}>
          <IntegrationsContextProvider value={{ ... }}>
            <SSHKeysContextProvider value={{ ... }}>
              <AnimatedOutlet context={consoleData as ConsoleOutletContext} />
            </SSHKeysContextProvider>
          </IntegrationsContextProvider>
        </AlertsContextProvider>
      </PoliciesContextProvider>
    </TasksContextProvider>
  </NodesContextProvider>
</SharedContextProvider>
```

**IMPORTANT:** Keep the old `AnimatedOutlet context={consoleData}` working during this step. Pages still use `useOutletContext` — we'll migrate them in the next task. Both context delivery mechanisms work simultaneously.

- [ ] **Step 3: Verify** — `cd web && npm run check`

- [ ] **Step 4: Commit**

```bash
git add web/src/components/layout/app-shell.tsx
git commit -m "$(cat <<'EOF'
feat(web): wire domain context providers in app-shell

Nest 7 context providers around Outlet. Old useOutletContext
still works — pages will be migrated incrementally.
EOF
)"
```

---

### Task 8: Migrate pages to domain contexts

**Files to modify (8 pages + their state files):**
- `web/src/pages/overview-page.tsx` — SharedContext, NodesContext, TasksContext, AlertsContext
- `web/src/pages/nodes-page.state.ts` — SharedContext, NodesContext, SSHKeysContext
- `web/src/pages/tasks-page.tsx` — SharedContext, TasksContext, NodesContext, PoliciesContext
- `web/src/pages/logs/logs-page.tsx` — SharedContext, TasksContext
- `web/src/pages/policies-page.tsx` — SharedContext, PoliciesContext, NodesContext
- `web/src/pages/notifications-page.tsx` — SharedContext, AlertsContext, IntegrationsContext
- `web/src/pages/settings-page.channels.tsx` — SharedContext, IntegrationsContext
- `web/src/pages/ssh-keys-page.state.ts` — SharedContext, SSHKeysContext, NodesContext

For each page:
1. Replace `useOutletContext<ConsoleOutletContext>()` with domain-specific hooks
2. Only import the contexts the page actually uses (see page → context mapping in spec)
3. Destructure only what's needed

Example for nodes-page.state.ts:
```tsx
// Before:
const { nodes, createNode, updateNode, deleteNode, deleteNodes, testNodeConnection,
        triggerNodeBackup, sshKeys, refreshNodes, loading, globalSearch, refresh
} = useOutletContext<ConsoleOutletContext>();

// After:
const { nodes, createNode, updateNode, deleteNode, deleteNodes, testNodeConnection,
        triggerNodeBackup, refreshNodes } = useNodesContext();
const { sshKeys } = useSSHKeysContext();
const { loading, globalSearch, refresh } = useSharedContext();
```

- [ ] **Step 1: Migrate overview-page.tsx**
- [ ] **Step 2: Migrate nodes-page.state.ts**
- [ ] **Step 3: Migrate tasks-page.tsx**
- [ ] **Step 4: Migrate logs-page (logs/logs-page.tsx)**
- [ ] **Step 5: Migrate policies-page.tsx**
- [ ] **Step 6: Migrate notifications-page.tsx**
- [ ] **Step 7: Migrate settings-page.channels.tsx**
- [ ] **Step 8: Migrate ssh-keys-page.state.ts**
- [ ] **Step 9: Verify all pages** — `cd web && npm run check`
- [ ] **Step 10: Commit**

```bash
git add -u web/src/pages/ web/src/hooks/
git commit -m "$(cat <<'EOF'
refactor(web): migrate all pages to domain-specific contexts

Replace useOutletContext with useNodesContext, useTasksContext,
usePoliciesContext, useAlertsContext, useIntegrationsContext,
useSSHKeysContext, useSharedContext across 8 page files.
EOF
)"
```

---

### Task 9: Clean up old ConsoleOutletContext

**Files:**
- Modify: `web/src/components/layout/app-shell.tsx` — remove old outlet context passing
- Modify: `web/src/hooks/use-console-data.ts` — remove `ConsoleDataState` export if no longer used externally
- Delete or deprecate: `ConsoleOutletContext` type

- [ ] **Step 1: Remove old outlet context from AnimatedOutlet**

In app-shell.tsx, change `AnimatedOutlet` to no longer pass context:
```tsx
// Before:
<AnimatedOutlet context={consoleData as ConsoleOutletContext} />

// After: Just use Outlet directly (no context prop needed)
```

If `AnimatePresence` still needs the outlet, use `useOutlet()` without context.

- [ ] **Step 2: Remove ConsoleOutletContext type export from app-shell.tsx**

- [ ] **Step 3: Verify no remaining useOutletContext usage**

Run: `grep -rn "useOutletContext" web/src/`
Expected: Zero matches.

Run: `grep -rn "ConsoleOutletContext" web/src/`
Expected: Zero matches (or only in the deprecated type file).

- [ ] **Step 4: Verify** — `cd web && npm run check`

- [ ] **Step 5: Commit**

```bash
git add -u web/src/
git commit -m "$(cat <<'EOF'
refactor(web): remove old ConsoleOutletContext

All pages now use domain-specific contexts. Remove outlet context
passing from AnimatedOutlet and ConsoleOutletContext type.
EOF
)"
```

---

### Task 10: Final verification

- [ ] **Step 1: Run full frontend check**

Run: `cd web && npm run check`
Expected: typecheck + lint + tests + build all pass.

- [ ] **Step 2: Verify lazy loading produces separate chunks**

Run: `ls web/dist/assets/ | wc -l` — should show more chunks than before (dialogs are now separate).

- [ ] **Step 3: Verify no old patterns remain**

```bash
grep -rn "useOutletContext" web/src/           # should be 0
grep -rn "ConsoleOutletContext" web/src/       # should be 0
grep -rn "sanitizeNode" web/src/               # should be 0 (from P2)
```

- [ ] **Step 4: Verify bundle size didn't regress**

Run: `cd web && node scripts/check-bundle-budget.mjs`
Expected: Pass (main bundle should be smaller due to code splitting).

- [ ] **Step 5: Review git log**

Run: `git log --oneline` — verify 9 clean commits.
