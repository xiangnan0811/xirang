# Alert Bulk Resolve Unresolved Alerts

## Goal

让告警中心在节点长期异常导致未处理告警堆积后，支持运维人员一次性处理当前确认要关闭的告警，减少逐条点击成本。MVP 聚焦“处理/解决未处理告警”，不删除告警历史，不改变已解决告警。

## What I already know

* 用户反馈：节点异常如果无人及时处理，告警中心会积累大量告警；问题修复后逐条点击处理很麻烦。
* 已澄清：批量处理语义仅作用于未处理告警；复用现有“处理/解决”动作；不删除历史；已处理告警不受影响。
* 后端现有告警状态为 `open` / `acked` / `resolved`，列表的 `status=unresolved` 表示 `status != resolved`。
* 现有单条接口：`POST /api/v1/alerts/:id/ack`、`POST /api/v1/alerts/:id/resolve`，均需要 `alerts:write`。
* 现有列表接口 `GET /api/v1/alerts` 支持 `node_id`、`status`、`severity`、`keyword`、分页与排序。
* 前端告警中心默认筛选 `status=unresolved`，已有单条确认/解决动作，但 `AlertBulkActions` 当前实际是行级操作组件，没有多选批量 UI。

## Assumptions (temporary)

* “处理完成异常”在这次需求中等同于把告警标记为 `resolved`，而不是仅标记 `acked`。
* 节点一键处理应按 nodeId 处理该节点所有未解决告警，不受当前分页限制。
* 多选批量处理只处理用户已勾选的未解决告警；如果勾选中包含已解决告警，后端应跳过且不报错。
* 仍沿用现有 RBAC 与节点 ownership 约束，用户不能批量处理自己无权操作的节点告警。

## Open Questions

* 无。

## Requirements (evolving)

* 支持在告警中心选择多条未解决告警并一键标记为已解决。
* 支持对某个节点的一键处理：将该节点所有未解决告警标记为已解决。
* 节点级一键处理入口放在每条未解决告警的更多操作菜单中，文案为“解决此节点未处理告警”。
* 批量处理仅影响未解决告警（`open` / `acked`），不删除历史记录，不修改已解决告警。
* 成功后刷新告警列表、未读统计和页面状态，避免已解决告警继续出现在默认未解决列表中。
* 批量操作应有明确的操作反馈，至少展示实际处理条数。
* 多选批量解决直接执行，不弹二次确认。
* 节点级一键解决需要二次确认，确认内容显示节点名并说明会解决该节点所有未处理告警。
* 批量入口必须可键盘操作并满足当前前端 a11y 基线。

## Acceptance Criteria (evolving)

* [ ] 用户可以在告警中心勾选多个未解决告警，点击一次将它们标记为已解决。
* [ ] 用户可以从某条未解决告警的行菜单发起节点级一键解决，并在确认后解决该节点全部未解决告警。
* [ ] 批量接口不会修改已解决告警；重复点击是幂等的。
* [ ] 非管理员/受 ownership 限制用户不能处理无权访问节点的告警。
* [ ] 批量完成后列表和未读统计刷新，默认“未处理”视图不再展示已解决告警。
* [ ] 后端测试覆盖按 ID 批量解决、按节点批量解决、权限过滤/拒绝关键路径。
* [ ] 前端类型检查与相关测试通过。

## Definition of Done (team quality bar)

* Tests added/updated (unit/integration where appropriate)
* Lint / typecheck / CI green
* Docs/notes updated if behavior changes
* Rollout/rollback considered if risky

## Out of Scope (explicit)

* 删除、归档或隐藏历史告警。
* 批量重发通知或批量重试任务。
* 新增告警静默、降噪、合并策略或通知升级策略。
* 自动判断节点是否已修复并自动关闭告警。
* 改造告警数据模型或迁移历史数据。

## Technical Approach (draft)

* 后端新增一个批量解决接口，复用 `alerts:write` 权限与现有响应 envelope。
* 请求支持两类目标：显式 `alert_ids` 多选目标，以及 `node_id` 节点级目标。
* 后端查询目标告警时限制 `status != resolved`，并执行节点 ownership 校验；按节点目标需确保当前用户有权操作该节点。
* 更新时将目标告警置为 `resolved` 并将 `retryable=false`，与单条 `Resolve` 行为保持一致。
* 前端 API 客户端新增 `resolveAlertsBulk`，AlertCenter 维护选择状态并将批量结果反馈到 toast。
* AlertList 在表格和卡片视图中暴露勾选控件；默认只允许选择未解决告警。
* 节点一键处理入口放在每条未解决告警的更多操作菜单中，文案为“解决此节点未处理告警”。
* 多选批量解决使用工具栏按钮直接执行；节点级一键解决使用现有确认对话框模式或轻量确认弹窗，避免误触。

## Decision (ADR-lite)

**Context**: 单条处理接口无法解决告警堆积后的操作成本；循环调用单条接口会增加请求数、权限/部分失败处理复杂度，也不适合节点级一键处理。

**Decision**: 新增服务端批量解决能力，由前端提供多选和节点级快捷入口。

**Consequences**: 后端需要明确定义部分可处理/不可处理时的结果统计；前端需要处理选择状态、分页刷新和批量按钮可访问性。

## Technical Notes

* Backend handler: `backend/internal/api/handlers/alert_handler.go`
* Backend routes: `backend/internal/api/router.go`
* Alert model: `backend/internal/model/models.go`
* Frontend center: `web/src/pages/notifications/alert-center.tsx`
* Frontend list/actions: `web/src/pages/notifications/alert-list.tsx`, `web/src/pages/notifications/alert-bulk-actions.tsx`
* Frontend API: `web/src/lib/api/alerts-api.ts`
* Frontend types: `web/src/types/domain.ts`
* A11y spec: `.trellis/spec/frontend/a11y-guidelines.md`
