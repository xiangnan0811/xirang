# A1 设计文档：拆分 god hook `useConsoleData`

> 父任务：`07-10-0710-frontend-audit-remediation`（A1 子项）
> 分支：`fix/frontend-audit-remediation`（与 A3/P1/P2 同分支，先完成再统一 PR）

## 0. 现状与边界（务必先读）

- `web/src/hooks/use-console-data.ts`（~533 行）集中持有 6 个域数组 `useState` + 全局 UI 状态 + `loadData`/`refresh`/`overview` 推导 + 4 个操作 hook（`useNodeOperations`/`useTaskOperations`/`usePolicyOperations`/`useIntegrationAlertOperations`）的接线。
- `app-shell.tsx` 已建立 **7 个 context provider**，并用 `useMemo` 把 `consoleData` 切片进各 provider（`sharedContextValue`/`nodesContextValue`/…）。**运行时“单域变化放大至整壳”的风险已被这 7 个切片缓解**：`setNodes` 只重建 `nodesContextValue`，仅 Nodes 消费者重渲染；`loadData`（自动刷新）只 `setAlerts`+`setOverviewSummary`，仅 Alerts/Shared 切片重建。
- 因此 A1 的**剩余价值是代码组织**（把 `useState`/刷新/操作接线下沉到各 Provider），而非修一个严重运行时 bug。拆分必须**行为零回归**。

## 1. 关键障碍：跨域读取构成嵌套死循环

逐域 Provider 自治要求每个 Provider 调用自己的操作 hook。但操作 hook 的**跨域只读依赖**形成环：

- `useNodeOperations` 读 `tasks`（`buildDemoBackupTask`/`createNode` demo）、`policies`（`triggerNodeBackup`）**值**。
- `useTaskOperations` 读 `nodes`/`policies`/`alerts`**值**（`retryTask` 读 `alerts.filter`、`buildDemoTask` 读 nodes/policies）。
- `usePolicyOperations` 仅用 `setTasks`/`refreshTasks`（函数，非值）；`useIntegrationAlertOperations` 仅用 `retryTask`（函数）+ `prev` 闭包。

Nodes 需 Tasks/Policies 值、Tasks 需 Nodes/Policies/Alerts 值 → **互为祖先不可能**，线性嵌套无解。

## 2. 设计：稳定 `ConsoleSettersContext`（值/引用分离）

引入单一稳定上下文，其 value **只含稳定引用**，消费它**不触发任何重渲染**：

```ts
interface ConsoleSettersValue {
  // 稳定 MutableRef：跨域只读走 .current，打破嵌套环
  nodesRef: MutableRefObject<NodeRecord[]>;
  policiesRef: MutableRefObject<PolicyRecord[]>;
  tasksRef: MutableRefObject<TaskRecord[]>;
  alertsRef: MutableRefObject<AlertRecord[]>;
  integrationsRef: MutableRefObject<IntegrationChannel[]>;
  sshKeysRef: MutableRefObject<SSHKeyRecord[]>;
  // 稳定 setter（useState/useCallback 产出，引用恒定）
  setNodes; setPolicies; setTasks; setAlerts; setIntegrations; setSSHKeys;
  setOverviewSummary; setWarning; setLoading; setLastSyncedAt; setRefreshVersion; setGlobalSearch;
  // 稳定辅助回调（demo 模式下注入；demoModeEnabled 为构建期常量，引用恒定）
  markInventoryMutated; markTasksMutated; ensureDemoWriteAllowed; handleWriteApiError;
}
```

- value 引用恒定 → Provider 消费它**零重渲染耦合**。
- 跨域**读**：操作 hook 改收 `XxxRef`（而非值数组），内部读 `.current`。
- 跨域**写**：仍走稳定 setter（不变）。
- 这样所有 Provider **只消费 `ConsoleSettersContext`（稳定），不再消费兄弟域 context 的值** → 嵌套死循环消除。

## 3. 各 Provider 自治边界

| Provider | 自持 state | 调用的操作 hook | 跨域读（经 ref） | 跨域写（经 setter） |
|---|---|---|---|---|
| NodesProvider | `nodes` | `useNodeOperations` | `policiesRef`,`tasksRef` | `setTasks`,`setAlerts`,`setSSHKeys` |
| TasksProvider | `tasks` | `useTaskOperations` | `nodesRef`,`policiesRef`,`alertsRef` | `setAlerts` |
| PoliciesProvider | `policies` | `usePolicyOperations` | —（仅 `refreshTasks` 函数） | `setTasks`,`setAlerts` |
| AlertsIntegrationsProvider* | `alerts`+`integrations` | `useIntegrationAlertOperations` | —（仅 `retryTask` 函数） | （hook 内 `setAlerts`/`setIntegrations`） |
| SharedBridge | 无 | — | `nodes`/`tasks`/`policies` 值（推导 overview，需响应式） | — |

> \* 因 `useIntegrationAlertOperations` 同时产出告警与集成操作（且 `retryTask` 来自任务 hook），Alerts 与 Integrations **强耦合**，合并为一个 Provider 同时提供 `AlertsContextValue` 与 `IntegrationsContextValue`（诚实反映耦合，避免重复调用 hook）。

- 每个 Provider 用 `useEffect` 把自有数组写回自有 `XxxRef.current`，使兄弟 hook 经 ref 读到最新值。
- 顶层 `useConsoleDataCore`（协调者，在 Provider 树**之外**）自持全局 UI 状态（`loading`/`warning`/`lastSyncedAt`/`refreshVersion`/`globalSearch`）+ `overviewSummary`，产出上述稳定 setter/helper，并以 `<ConsoleSettersProvider>` 包裹整树。
- `loadData`/`refresh`/`fetchOverviewTraffic`/`fetchHealthIncidentTimeline` 留在协调者（跨域加载本就在此）。`overview` 推导放在树的**内部** `<SharedBridge>`：消费 Nodes/Tasks/Policies 值（响应式重算 overview）+ 协调者下发的 `overviewSummary`/全局/`refresh`/`fetch*`，组装 `SharedContextValue`。

## 4. 嵌套结构（无值环，因为跨域只读全走 ref）

```
<ConsoleSettersProvider value={stable}>
  <NodesProvider>            // 自有 nodes + node+ssh 操作
    <TasksProvider>          // 自有 tasks + task 操作
      <PoliciesProvider>     // 自有 policies + policy 操作
        <AlertsIntegrationsProvider>  // 自有 alerts+integrations + 告警/集成操作
          <SharedBridge ...>  // 消费域值推导 overview，提供 SharedContext
            {children}
          </SharedBridge>
        </AlertsIntegrationsProvider>
      </PoliciesProvider>
    </TasksProvider>
  </NodesProvider>
</ConsoleSettersProvider>
```

> 注：SSH 密钥操作本属 `useNodeOperations`（createNode 内联建 key、deleteSSHKey 查 nodes 占用），故随 NodesProvider 走，经 `setSSHKeys` 写回 SSHKeysContext。

## 5. 操作 hook 签名改动（最小面）

- `useNodeOperations`：`policies: PolicyRecord[]` → `policiesRef: MutableRefObject<PolicyRecord[]>`；`tasks: TaskRecord[]` → `tasksRef`。内部 `policies.find`/`buildDemoBackupTask(...,tasks,...)` 改为 `.current`。
- `useTaskOperations`：`nodes`/`policies`/`alerts` 值 → 对应 `*Ref`；内部读取改 `.current`。
- `usePolicyOperations`/`useIntegrationAlertOperations`：**不改签名**（仅用 setter/函数，无跨域值读）。
- 还原 demo 行为：`buildDemoTask(input, nodesRef.current, policiesRef.current, tasksRef.current)` 等保持原语义。

## 6. 验收

- `use-console-data.ts` 不再集中持有域数组；`AppShellInner` 仅组装稳定 setter + 逐域 Provider 嵌套。
- 单域变化只触发该域 context 消费者重渲染；告警/集成仍随其操作变化更新（经稳定 setter）。
- 全部页面行为零回归（`use-console-data.test.tsx` 改写为渲染 Provider 树 + 既有 9 个用例行为不变）。
- `cd web && npm run check` 全量通过；无新增 `any`/不安全断言/raw snake_case 进组件。

## 7. 执行顺序（implement.md 落实，每域 `npm run check`）

1. 稳定上下文 `console-setters-context.tsx` + 协调者 `useConsoleDataCore`。
2. 改 `useNodeOperations`/`useTaskOperations` 签名为 ref。
3. 逐域 Provider：Nodes(+SSH) → Tasks → Policies → Alerts+Integrations。
4. `SharedBridge` 推导 overview。
5. 改写 `app-shell.tsx` 嵌套 + `use-console-data.test.tsx` 为 Provider 树测试。
6. 全量 `npm run check`。

## 8. 实际落地方案（与 §2–§4 的差异及理由）

实施时**未采用** §2–§4 的「Provider 自持 state + 稳定 `ConsoleSettersContext` 打破嵌套环 + `SharedBridge`」方案，改为**更低风险、行为零回归**的拆分：

- **协调者 `useConsoleData` 仍集中持有 6 个域数组 + 全局 UI state**，但把每域的接线（4 个操作 hook 调用 + 各自 `refreshX` + abort/version 戳逻辑）下沉到 4 个独立 domain hook：
  - `use-console-nodes-domain.ts`（节点 + SSH 密钥，因 `useNodeOperations` 同时产出 SSH 密钥操作）
  - `use-console-tasks-domain.ts`
  - `use-console-policies-domain.ts`
  - `use-console-alerts-integrations-domain.ts`（告警与集成强耦合，合并一域）
- **操作 hook 签名完全不动**（仍传值，不引入 ref 环）；domain hook 仅把协调者已有的 state/setter/helper 透传下去——逻辑逐字保留。
- `app-shell.tsx`、两个测试文件**零改动**；公共 `useConsoleData` 返回结构不变。

**理由**：
1. 运行时重渲染风险已被 `app-shell.tsx` 的 7 个 memoized 切片消除（§0 已承认），A1 的剩余价值只是代码组织。
2. §2–§4 方案需建立「协调者 `loadData` 写入 Provider 自持 state」的**注册式 setter 通道**，在 demo 全量加载 / abort / version-stamp 时序下较脆弱；与本次硬门槛「确认无误、没有引入新的问题之后才允许提 PR」冲突。
3. 本方案把 533 行 god hook 拆成「~330 行协调者 + 4 个 ~70 行聚焦 domain hook」，达成审计「拆分过大/耦合的 god hook」的代码组织目标，且回归面最小（仅纯搬迁）。

若后续仍要追求「Provider 完全自治」，应作为**独立任务**另行设计、单独验证，不在本次一并推进。
