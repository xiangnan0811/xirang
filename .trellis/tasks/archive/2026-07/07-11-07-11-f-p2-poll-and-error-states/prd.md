# PRD: 轮询与错误态（F-P2-3 + F-P3）

## 问题范围

- 后台 tab 继续轮询，浪费请求且扩大竞态窗口。
- node metrics/status 请求不能取消，快速切换节点时旧响应可覆盖新 state。
- SLO 与部分 Settings 页面把错误呈现为空数据/数据不足，或使用不可访问的 `window.confirm`。

## 实际落地

- `use-node-status` 使用 `useVisibilityPolling`，隐藏页暂停、恢复后立即刷新，并用 AbortController 清理请求。
- node metrics API 与 hooks 贯穿 `AbortSignal`；请求序号/abort 防止 stale response 覆盖。
- SLO 首屏明确 loading，列表错误与逐行 compliance 错误分别展示；失败不再伪装为 `insufficient_data`。
- SLO、escalation、silence 删除切换到共享 `useConfirm` dialog。
- escalation/silence list 在 effect cleanup 时 abort，避免卸载后更新 state/弹 toast。
- 节点状态、指标、forecast 等失败保留最后可信数据或显示明确错误，不误报 offline/empty。

## 验收

- document hidden 时不轮询；visible 后恢复一次刷新。
- node/token/时间窗变化或卸载会取消旧请求。
- stale response 不覆盖新节点数据。
- loading、empty、error、insufficient data 保持不同语义。
- destructive action 使用可访问 dialog；取消不会发送请求。
- 相关 Vitest 与完整 `npm run check` 通过。

## 验证

- `use-node-status.test.ts`、node metrics、node detail、SLO 与 Settings 测试覆盖新增路径。
- `env -u NODE_ENV npm run check` 已通过（127 files / 547 tests）；Trellis/spec 更新后须重跑。

## 状态

实现完成，位于 `fix/07-11-audit-p1-security`；工作提交后归档。
