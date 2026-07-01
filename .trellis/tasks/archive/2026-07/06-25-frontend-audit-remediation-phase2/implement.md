# 前端审查整改第二阶段执行计划

## Preconditions

- Branch: `fix/frontend-audit-remediation`.
- Do not push.
- Do not amend or rewrite the previous first-stage commits.
- Start this task with `python3 ./.trellis/scripts/task.py start 06-25-frontend-audit-remediation-phase2` only after PRD/design/implement are in place.

## Batch 1: RED tests for desktop action grouping

Files:

- `web/src/pages/nodes-page.test.tsx`

Steps:

1. Add `within` from Testing Library if needed.
2. Force list view with `window.localStorage.setItem("xirang.nodes.view", JSON.stringify("list"))`.
3. Render `NodesPage`.
4. Locate the row containing `node-prod-1`.
5. Assert frequent actions remain inline: test connection, logs link, manual backup.
6. Assert a node-specific overflow trigger exists.
7. Assert secondary actions are not all rendered as inline row buttons before opening the menu.
8. Click the overflow trigger and assert menu items for Fleet Doctor, Web Terminal, File Browser, Edit, Migrate, and Delete.

Expected RED command:

```bash
cd web && npm run test -- src/pages/nodes-page.test.tsx
```

Expected failure before implementation: overflow trigger/menu assertions fail.

## Batch 2: Implement row overflow menu

Files:

- `web/src/pages/nodes-page.table.tsx`
- `web/src/i18n/locales/zh.ts` if a new label key is needed
- `web/src/i18n/locales/en.ts` if a new label key is needed

Steps:

1. Import `MoreHorizontal` from `lucide-react`.
2. Import existing dropdown menu primitives.
3. Keep primary inline actions: test connection, logs link, manual backup.
4. Add one overflow trigger button.
5. Move Fleet Doctor, Web Terminal, File Browser, Edit, Migrate, and Delete into the dropdown.
6. Add `DropdownMenuSeparator` before Delete.
7. Preserve all existing callbacks and disabled/loading states where applicable.

Validation:

```bash
cd web && npm run test -- src/pages/nodes-page.test.tsx
cd web && npm run typecheck
cd web && npm run lint
```

## Batch 3: Focused QA and full gate

Run:

```bash
cd web && npm run test -- src/pages/nodes-page.test.tsx
cd web && npm run typecheck
cd web && npm run lint
cd web && npm run check
```

Browser QA:

- Start demo dev server with `VITE_ENABLE_DEMO_MODE=true npm run dev -- --host 127.0.0.1 --port 5173`.
- Inspect `/app/nodes` at 1280x900, 768x1024, and 375x812.
- Desktop: verify primary actions are visible and overflow menu opens.
- Desktop: verify Delete is in the overflow menu and visually separated/destructive.
- Mobile/tablet: verify card/mobile behavior and toolbar More menu did not regress.

## Batch 4: Finish

1. Run `GIT_MASTER=1 git status --short`.
2. If checks pass, commit atomically in the same branch with git-master rules.
3. Archive the Trellis task and record the session only after code commits are complete.

## Explicit deferrals

- React Scan / react-grab / react-doctor installation.
- Broad render-performance architecture work.
- Arbitrary coverage-percentage improvement.
- Unknown or unrelated test warning cleanup.
- Backend/auth production QA.
