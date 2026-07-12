# PRD: P1 panel-query ownership

## 问题与风险

`POST /dashboards/panel-query` 仅有 `dashboards:read`，不对 operator 做节点 ownership 校验；空 `node_ids` 返回全舰队指标。

## 实际落地

- 在 handler 入口解析角色并通过 `ownershipNodeFilter` 获取 operator 的 owned node IDs。
- operator 显式请求任一未拥有节点时返回 403，不执行部分查询。
- operator 空 `node_ids` 时注入 owned 节点集合；owned 为空时返回空 series。
- task 指标通过 `tasks.node_id` 应用同一过滤；admin/viewer 保持原查询语义。
- provider 接口支持 handler 下发授权节点集合，避免在 provider 内重新推断权限。

## 验收矩阵

| 场景 | 预期 |
|---|---|
| operator 显式包含未拥有节点 | 403，且不返回 owned 子集 |
| operator 空过滤且有 owned 节点 | 仅返回 owned 节点数据 |
| operator 空过滤且无 owned 节点 | 成功返回空 series |
| task 指标 | 通过 task → node 过滤 |
| admin / viewer | 保持原行为 |

## 验证

- `panel_query_handler_test.go` 覆盖显式越权、空过滤、无 ownership、task metric 与角色行为。
- 后端完整测试、lint 与 build 已通过；Trellis/spec 更新后须重跑。

## 状态

实现完成，位于 `fix/07-11-audit-p1-security`；工作提交后归档。
