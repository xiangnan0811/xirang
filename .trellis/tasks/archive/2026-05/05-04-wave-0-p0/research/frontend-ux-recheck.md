# 前端 UX finding 复核

- **Date**: 2026-05-03
- **Scope**: 重新核验 5 条 P1 finding，逐项 Read 源码

---

## F-1 Dialog 小屏溢出

- **状态**: ⚠️部分真实
- **当前代码状态**:
  - `web/src/components/ui/dialog.tsx:42` className 包含 `"fixed left-1/2 top-1/2 z-50 w-full -translate-x-1/2 -translate-y-1/2"`
  - 第 47-50 行根据 size 设置 `max-w-[480/560/640px]`
  - **关键**：基类 `w-full` + `max-w-[Npx]` 的组合在 viewport <Npx 时会回落到 viewport 宽度（max-w 是上限，不是固定值），所以 480px 以上的 dialog 在 480px 屏宽下会贴满屏幕
  - 但贴满屏幕时**没有左右内边距**，且 `top-1/2 -translate-y-1/2` 在内容超过视口高度时**会导致顶部内容被裁切**（无 max-height 与 overflow-auto）
- **子代理偏差**: 子代理说"会溢出屏幕"过于绝对——水平方向 `w-full` 已经吃住了视口宽度，不会真正溢出；真正问题是 **(a) 紧贴屏幕边缘** 和 **(b) 高度无 max-height/overflow 兜底**，长内容会被居中裁切
- **正确修复方向**:
  - 加 `mx-4 sm:mx-0` 给水平留白，或在 base 上改成 `max-w-[calc(100vw-2rem)]`
  - 加 `max-h-[calc(100vh-2rem)] overflow-y-auto` 防止高内容溢出
  - 注意 DialogBody 已有 `px-6 py-3` 内边距，问题主要是容器外边距
- **工作量**: S（单文件单段 className 调整）

---

## F-2 删除/批量删除无二次确认

- **状态**: ❌误报
- **当前代码状态**:
  - 已存在通用资产 `web/src/components/ui/confirm-dialog.tsx`（基于 Radix AlertDialog）和 `web/src/hooks/use-confirm.tsx`（Promise 风格 confirm()，带队列）
  - `web/src/pages/nodes-page.state.ts:291-306` `onDeleteNode` 调用 `await confirm({title, description})`
  - `web/src/pages/nodes-page.state.ts:331-358` `handleBulkDelete` 同样调用 `await confirm({...})`
  - `web/src/pages/nodes-page.state.ts:464-480` `handleEmergencyBackup` 也走 confirm
  - `web/src/pages/tasks-page.tsx:329-346` `handleDelete(taskId)` 走 confirm；`handleBatchTrigger` (367) 同样
  - `web/src/pages/ssh-keys-page.state.ts:280-301` `handleDelete` 走 confirm
  - `web/src/pages/ssh-keys-page.tsx:112-137` `handleBulkDelete` 走 `state.confirm({...})`
  - `web/src/pages/policies-page.tsx:148-162` `onDelete` 走 confirm
- **子代理偏差**: 子代理完全没看实际函数体，把"删除按钮 onClick 回调"误判为直接删除。实际 4 个目标页面所有删除/批量删除路径都已二次确认
- **正确修复方向**: 无需修复。可顺手扫一下 `notifications-page.integration-manager.tsx:57-163`、`settings-page.escalation.tsx:108-192`、`users-page.tsx:187-383`、`reports-page.slo.tsx:66-157` 等次要页面的 handleDelete 是否走了 confirm（不在本次复核范围）
- **工作量**: 无（误报）

---

## F-3 长列表无虚拟化

- **状态**: ⚠️部分真实（实际风险被分页/cap 显著缓解）
- **当前代码状态**:
  - `web/package.json` 无 `react-window`/`@tanstack/react-virtual`/`react-virtuoso` 依赖
  - **logs-viewer**：`web/src/pages/logs/logs-viewer.tsx:50` 直接 `filteredLogs.map(LogEntry)`；**但** `web/src/hooks/use-live-logs.ts:48` 已硬 cap 在 400 条（`sorted.slice(0, 400)`），加上 history 200 (`logs-page.tsx:132 limit:200`)，合并去重后峰值约 ~600 条
  - **tasks-page**：`tasks-page.tsx:140` 用 `useClientPagination(filteredTasks)`，传给 table 的是 `pagedTasks`（行 471/491），表格只渲染当前页
  - **audit-page**：`audit-page.tsx:14, 353` 已用 `Pagination` 组件
  - **nodes-page.table**：`nodes-page.tsx:79, 96` 用 `useClientPagination(sortedNodes)`，table 收到 `pagedNodes`
- **子代理偏差**: 子代理把"map 渲染"等同于"无虚拟化高风险"。实际 3 个表格已分页（每页通常 20-50 条），logs 也有 600 条硬上限，常规场景下 DOM 节点远低于触发性能瓶颈的阈值（约 1000+ 行才显著卡顿）。真正可能受影响的只有 logs-viewer 在峰值 400-600 行 + 频繁追加时
- **正确修复方向**:
  - 不需要全面引入虚拟化库
  - 仅 logs-viewer 可酌情考虑：① 把 cap 降到 200，或 ② 引入 react-window 仅给 logs-viewer 用
  - 表格类页面已分页，无需任何改动
- **工作量**: S（如需对 logs-viewer 引入虚拟化）/ 无（如保持现状）

---

## F-4 WebSocket 定时器未清理

- **状态**: ❌误报
- **当前代码状态**: `web/src/lib/ws/logs-socket.ts`
  - `disconnect()` (152-172): 调用 `clearReconnectTimer()` (154) 和 `stopHeartbeat()` (155)，并 `removeVisibilityListener()` (156)
  - `clearReconnectTimer()` (281-286): `if (this.reconnectTimer) clearTimeout(...)`
  - `stopHeartbeat()` (312-317): `if (this.heartbeatTimer) clearInterval(...)`
  - `socket.onclose` (237-248): 关闭时 `stopHeartbeat()`（241），仅在非手动关闭时 `tryNextCandidateOrReconnect()`
  - `open()` (211-223): 重新打开时 `clearReconnectTimer()` + `reconnectAttempts = 0`
  - `scheduleReconnect()` (266-279): 设新 timer 前先 `clearReconnectTimer()` (271)
  - `addVisibilityListener()` (319-339): 重连场景里 `clearReconnectTimer()` (334) 防止双调度
- **子代理偏差**: 全部清理路径都覆盖到了。子代理没有指出具体哪条路径漏清理
- **正确修复方向**: 无需修复。如果想更稳，可在 `useLiveLogs` 卸载时（`use-live-logs.ts:106 client.disconnect()`）已正确调用，没有泄漏
- **工作量**: 无（误报）

---

## F-5 batch-result-dialog setInterval 泄漏

- **状态**: ❌误报
- **当前代码状态**: `web/src/components/batch-result-dialog.tsx`
  - `intervalRef` (52) + `stopPolling` (55-60) `useCallback` 内 `clearInterval` 并置 null
  - useEffect (81-98):
    - 入口分支：`!open || !batchId` 时清状态并 `stopPolling()` 直接 return（89）
    - 正常分支：`intervalRef.current = setInterval(..., 3000)` (93)
    - **return stopPolling** (97) — 卸载/依赖变更时清理
  - `fetchStatus` (62-78) 内任务全部完成时也调 `stopPolling` (73)
  - `handleClose` (136-145) 关闭时通过 onOpenChange→useEffect 触发 (open=false 分支) → stopPolling
- **子代理偏差**: 子代理担心"快速开关漏掉清理"，但 useEffect 的依赖是 `[open, batchId, fetchStatus, stopPolling]`，每次 open 切换都会先执行上一次的 cleanup（return 函数），React 已保证不会泄漏。`intervalRef` 是单 ref，新 setInterval 会先经过 stopPolling cleanup
- **正确修复方向**: 无需修复
- **工作量**: 无（误报）

---

## 总体结论

- **真实**: 1/5（F-1 部分真实，需小改）
- **误报**: 4/5（F-2/F-4/F-5 完全误报，F-3 风险被现有分页/cap 显著缓解，可视为不必紧急修）
- **优先修复建议**:
  1. **F-1（S，~30 分钟）**: dialog.tsx 加 `mx-4 sm:mx-0` + `max-h-[calc(100vh-2rem)] overflow-y-auto`，覆盖小屏与超长内容兜底
  2. F-3 视后续真实卡顿反馈再决定是否给 logs-viewer 上虚拟化
  3. F-2/F-4/F-5 不要动，子代理误判，避免引入回归
- **核心教训**: 上次子代理报告 5 项里 4 项误报，不能再相信"路径模式匹配 → 没看代码就下结论"的子代理输出。下次直接 Read 关键函数体再判定
