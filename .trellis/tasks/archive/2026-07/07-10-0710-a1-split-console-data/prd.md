# PRD: A1 — 拆分 god hook `useConsoleData`，按域下沉到各自 Provider

> 父任务：`07-10-0710-frontend-audit-remediation`（A1 子项，最大重构）
> 类型：复杂重构（需 `design.md` + `implement.md` 后方可 `task.py start`）
> 分支：`refactor/a1-split-console-data`（从最新 `main` 切出）

---

## 1. 问题与真实性
`web/src/hooks/use-console-data.ts`（529 行）是单体 god hook，单个 `ConsoleDataState` 接口约 55 个成员，集中持有 nodes / policies / tasks / alerts / integrations / sshKeys / overview 全部域状态与操作。它在 `web/src/components/layout/app-shell.tsx:67` 的 `AppShellInner` 内被调用。

**后果**：
- 任一域状态变化（含 30s 自动刷新的 `loading`/`lastSyncedAt`）都会重渲染整个 `AppShellInner`（顶栏、侧栏、7 个 context 的 memo 全部重算）。
- context 拆分虽已存在（app-shell 已把数据切片进 7 个 provider），但中心 hook 把所有域塞在一起，新增业务域必改中心文件，违反开闭原则。
- 与 A2 关联：中心 hook 的 `refresh` 不稳定会破坏拆分红利；A2 应先于本任务落地，作为渲染隔离基线。

**风险等级**：中（可维护性 + 渲染放大；改动面大）

**已具基础**：`app-shell.tsx` 已建立 7 个 context（Shared / Nodes / Tasks / Policies / Alerts / Integrations / SSHKeys）并各自 `useMemo` 切片。本任务把"状态持有 + 操作定义"从中心 hook 下沉到各 `*-context-provider`，中心 hook 仅保留跨域的 `SharedContext`。

---

## 2. 设计要点（design.md 需展开）
- **边界**：跨域数据 `overview` / `fetchOverviewTraffic` / `fetchHealthIncidentTimeline` 以及全局 `loading`/`warning`/`lastSyncedAt`/`refreshVersion`/`globalSearch`/`refresh` 留父壳（SharedContext）。其余逐域下沉。
- **每域 Provider 自治**：在 `nodes-context-provider.tsx` 等内 `useState` 持有本域数组，定义本域 create/update/delete/refresh（原 `use-console-node-operations` 等 hook 逻辑移项或保留为 Provider 内部调用）。
- **派生隔离**：`deriveOverview(nodes, policies, tasks, overviewSummary)` 在需要它的域/页面内 `useMemo`，不再由中心 hook 统一算。
- **轮询**：A2/P1/P2 的刷新/轮询修正在下沉后随各域 Provider 走，避免重复。
- **token 边界**：保持既有"auth token 经 `useAuth()` 显式下发"约定，各 Provider 内部 `useAuth()` 或接收 token prop。

---

## 3. 验收标准
- `use-console-data.ts` 不再集中持有全部域状态；`AppShellInner` 仅组装跨域 `SharedContext` 与各域 Provider 嵌套。
- 单个域（如 nodes）状态变化仅触发该域 context 消费者重渲染，不再放大至整壳（用渲染计数/快照测试佐证）。
- 全部页面（nodes / tasks / policies / alerts / integrations / sshKeys / overview / dashboards 等）行为无回归。
- `cd web && npm run check` 全量通过。
- 无新增 `any` / 不安全断言 / raw snake_case 进组件。

---

## 4. 执行顺序（implement.md 建议）
逐域迁移，每域独立可验、独立 `npm run check`：
1. Nodes（最大域）
2. Tasks
3. Policies
4. Alerts
5. Integrations
6. SSHKeys
7. 收口：精简中心 hook → 仅 SharedContext；删除/归档 `use-console-*-operations` 中被完全内联的逻辑（保留仍可复用部分）。

---

## 5. 依赖与顺序
- **必须在 A2 之后**（A2 建立渲染隔离基线，本任务验证目标）。
- 与 A3 / P1 / P2 无强依赖；建议 A3/P1/P2 先合并稳定，再启 A1。
- 分支：`refactor/a1-split-console-data`。
- **回滚点**：每域迁移后跑 `npm run check` + 相关页面冒烟；A1 合并前确保全量 CI 绿。
