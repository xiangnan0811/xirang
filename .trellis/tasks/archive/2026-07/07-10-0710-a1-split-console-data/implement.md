# A1 实现清单：拆分 god hook `useConsoleData`

> 设计见 `design.md`。原则：行为零回归；每步跑 `cd web && npx tsc -b --noEmit --force` + 相关单测，最后 `npm run check` 全量。

## 步骤

- [ ] **S0 稳定上下文**：新建 `web/src/context/console-setters-context.tsx`
  - `ConsoleSettersValue`：6 个 `XxxRef: MutableRefObject<...[]>` + 全部 `setXxx` setter + `setOverviewSummary`/`setWarning`/`setLoading`/`setLastSyncedAt`/`setRefreshVersion`/`setGlobalSearch` + 4 个稳定辅助回调。
  - `ConsoleSettersProvider`（dumb，收 `value`）+ `useConsoleSetters()`（取不到抛错）。
  - value 引用恒定 → 消费它零重渲染耦合。

- [ ] **S1 协调者 `useConsoleDataCore(token)`**（改写 `use-console-data.ts` 导出）
  - 自持：`overviewSummary`/`loading`/`warning`/`lastSyncedAt`/`refreshVersion`/`globalSearch`（`useState`）。
  - 稳定辅助：`markInventoryMutated`/`markTasksMutated`/`ensureDemoWriteAllowed`/`handleWriteApiError`（`useCallback`，`demoModeEnabled=import.meta.env...` 为构建期常量→引用恒定）。
  - 组装 `setters` 对象（含全部 setter + 辅助回调）。
  - `loadData`（用 setters：`alerts`+`overviewSummary` 真实；demo 全量）、`refresh`（bump `refreshVersion` + `loadData`）、`fetchOverviewTraffic`/`fetchHealthIncidentTimeline`（跨域，留协调者）。
  - 响应式自动刷新（P2 已做）：`useRefreshInterval()` + `useVisibilityPolling(loadData, ms, {enabled:!!token, immediate:false})`。
  - 返回 `{ setters, global:{loading,warning,lastSyncedAt,refreshVersion,globalSearch,setGlobalSearch}, overviewSummary, refresh, fetchOverviewTraffic, fetchHealthIncidentTimeline, demoModeEnabled }`。

- [ ] **S2 操作 hook 最小改动**
  - `useNodeOperations`：`policies`/`tasks` 参数改 `policiesRef`/`tasksRef: MutableRefObject<...[]>`；内部 `policies.find`/`buildDemoBackupTask(...,tasks,...)`/`createNode` demo 改读 `.current`。
  - `useTaskOperations`：`nodes`/`policies`/`alerts` 参数改对应 `*Ref`；内部 `alerts.filter`/`buildDemoTask(...,nodes,policies,...)` 改读 `.current`。

- [ ] **S3 逐域 Provider（自治）**
  - `nodes-context-provider.tsx`：自持 `nodes`；`useEffect` 把 `nodes` 写回 `nodesRef.current`；调 `useNodeOperations`（收自有 `nodes` 值 + `policiesRef`/`tasksRef`/`setTasks`/`setAlerts`/`setSSHKeys` 经 `useConsoleSetters()` + 辅助）；自持 `sshKeys` 并写回 `sshKeysRef`；调 `useNodeOperations` 取 `createSSHKey`/`updateSSHKey`/`deleteSSHKey`；自持 `refreshNodes`；提供 `NodesContextValue` + `SSHKeysContextValue`。
  - `tasks-context-provider.tsx`：自持 `tasks` 写回 `tasksRef`；调 `useTaskOperations`（收 `nodesRef`/`policiesRef`/`alertsRef`/`setAlerts` + 自有 `tasks` + 辅助）；提供 `TasksContextValue`。
  - `policies-context-provider.tsx`：自持 `policies` 写回 `policiesRef`；调 `usePolicyOperations`（收 `setTasks`/`setAlerts` + 辅助 + 自有 `policies`）；提供 `PoliciesContextValue`。
  - `alerts-context-provider.tsx`：自持 `alerts` 写回 `alertsRef`；自持 `integrations` 写回 `integrationsRef`；调 `useIntegrationAlertOperations`（收 `setAlerts`/`setIntegrations`/`setWarning` + 辅助 + 自有 `alerts`/`integrations` + `retryTask`）；自持 `refreshIntegrations`；提供 `AlertsContextValue` + `IntegrationsContextValue`。

- [ ] **S4 SharedBridge**（树内，推导 overview）
  - 收 `NodesContext`/`TasksContext`/`PoliciesContext` 值（响应式重算 overview）+ 协调者下发的 `global`/`overviewSummary`/`refresh`/`fetch*` 作为 props。
  - `overview = deriveOverview(nodes, policies, tasks, overviewSummary)`。
  - 提供 `SharedContextValue` 包裹 `{children}`。

- [ ] **S5 接线 `app-shell.tsx`**
  - `AppShellInner` 调 `useConsoleDataCore(token)` 得协调者；以 `<ConsoleSettersProvider value={core.setters}>` 包裹，顺序：Nodes → Tasks → Policies → AlertsIntegrations → SharedBridge(提供 SharedContext) → `{children}`。
  - 删除原 `consoleData.xxx` 取切片逻辑（已被 Provider 取代）。

- [ ] **S6 测试改写 `use-console-data.test.tsx`**
  - 渲染 Provider 树（同 app-shell 嵌套）+ 用 `useNodesContext()` 等断言既有 9 个用例行为不变（尤其：stale 覆盖防护、refreshTask 覆盖、demo updateTask 重算、refreshVersion bump、demo mock 数据）。
  - 关键：断言路径从 `result.current.nodes` 改为经 `useNodesContext` 渲染的 harness。

- [ ] **S7 全量 `npm run check`**（typecheck+lint+test+build）须绿；无新 `any`/不安全断言。

## 回滚点
每步完成跑 `npx tsc -b --noEmit --force`；S3-S6 每域后跑相关 page/provider 单测。全量 CI 绿方可归档。

## 实际落地（已执行，与上方步骤的差异）

上方 S0–S7 的「Provider 自持 state + `ConsoleSettersContext` + `SharedBridge`」方案**未采用**，改以更低风险、行为零回归的方式完成拆分（详见 `design.md` §8）：

- [x] 新增 4 个 domain hook：`use-console-nodes-domain.ts` / `use-console-tasks-domain.ts` / `use-console-policies-domain.ts` / `use-console-alerts-integrations-domain.ts`，各自自持 `refreshX` 与 abort 控制，透传协调者 state/setter/helper 给既有操作 hook。
- [x] 重写 `use-console-data.ts` 为协调者：保留全部全局 UI state、`loadData`/`refresh`/`fetchOverviewTraffic`/`fetchHealthIncidentTimeline`、响应式轮询（`useRefreshInterval` + `useVisibilityPolling`），并调用上述 4 个 domain hook 组装原 `ConsoleDataState`。
- [x] **操作 hook 签名未改**（仍传值），公共 API 与 `app-shell.tsx`、两个测试文件零改动。
- [x] 验证：`npx tsc -b --noEmit` 通过；`use-console-data.test.tsx`（9）+ `app-shell.test.tsx`（1）原样全绿；`npm run check`（typecheck+lint+test+build）全量通过。

> 注：`app-shell.tsx` 的 7 个 memoized 切片继续承担运行时重渲染隔离（本次未动）。「Provider 完全自治」作为独立后续任务跟踪。
