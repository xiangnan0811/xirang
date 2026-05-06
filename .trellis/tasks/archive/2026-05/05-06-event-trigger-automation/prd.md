# 事件触发-动作编排 (Event-Trigger Automation)

## Goal

通用规则引擎：当指定事件发生且满足过滤条件时，自动执行对应动作。将告警、异常、状态变化与 Policy/Task 控制串联，实现自愈运维。

## Requirements

### 数据模型（迁移 000056）
- 新增 `automation_rules` 表：
  - `id`, `name` (unique), `description`
  - `event_type` (string) — 事件类型
  - `event_filter` (text, JSON) — 过滤条件（如 `{"metric":"ransomware_pattern"}`）
  - `action_type` (string) — 动作类型
  - `action_config` (text, JSON) — 动作参数（如 `{"policy_id":"{{.PolicyID}}"}`）
  - `enabled` (bool)
  - `created_at`, `updated_at`

### 内置事件类型
| event_type | 描述 | filter 可用字段 |
|---|---|---|
| `anomaly_detected` | AnomalyEvent 产生 | detector, metric, severity |
| `backup_failed` | 备份 TaskRun 失败 | policy_id, node_id, executor_type |
| `backup_succeeded` | 备份 TaskRun 成功 | 同上 |
| `drill_failed` | 恢复演练失败 | policy_id |
| `node_offline` | 节点离线 | node_id |
| `node_disk_high` | 节点磁盘使用超阈值 | node_id |

### 内置动作类型
| action_type | 描述 | config 参数 |
|---|---|---|
| `pause_policy` | 暂停 Policy（SkipNext=true） | policy_id（支持模板变量） |
| `disable_policy` | 禁用 Policy | policy_id |
| `trigger_task` | 触发 Task 执行 | task_id |
| `send_notification` | 发送额外通知 | message（模板） |

### 规则执行
- 在各事件发射点（alerting dispatcher、anomaly raise、task runner 完成点），调 `automation.Dispatcher.Dispatch(event)` 
- Dispatcher 查匹配的 enabled rule → 检查 filter → 执行 action
- 执行日志写 `automation_rule_logs` 表（rule_id, event_type, action_type, result, error）

### API
- `GET/POST /automation-rules` — 列表/创建
- `GET/PUT/PATCH/DELETE /automation-rules/:id` — 详情/更新/部分更新/删除

### 前端
- 自动化规则管理页：规则列表 + 创建/编辑对话框（事件类型下拉 → 过滤条件 → 动作类型下拉 → 参数）
- 规则执行日志查看

### 兼容性
- 新表，不影响现有功能

## Acceptance Criteria

- [ ] 创建规则：anomaly_detected(metric=ransomware_pattern) → pause_policy(target={{.PolicyID}}) → 模拟 anomaly 触发 → Policy.SkipNext=true
- [ ] 创建规则：backup_failed → send_notification → 备份失败时额外通知
- [ ] 禁用规则 → 事件发生不触发动作
- [ ] 执行日志正确记录成功/失败

## Definition of Done

- 迁移 000056 SQLite+PostgreSQL 双轨
- `go test ./...` + `npm run check` 全绿
- 用户文档

## Out of Scope

- 复杂条件表达式（AND/OR，时间窗口）→ 后续
- Webhook action type → 后续
- 自定义事件（用户定义 event_type）→ 后续

## Decision

### D1: MVP 范围 — 通用规则引擎 (A)
**Decision**: 规则表 + CRUD + Dispatch 管线 + 前端规则管理页。
### D2: 模板变量
**Decision**: action_config 支持 `{{.PolicyID}}`/`{{.TaskID}}`/`{{.NodeID}}` 等模板变量，从事件上下文提取。

## Implementation Plan

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | 模型+迁移+规则 CRUD handler+Dispatcher 框架+单测 | 2.5 天 |
| **PR2** | 接入各事件发射点+action 执行(log/notification/pause)+集成测试 | 2 天 |
| **PR3** | 前端规则管理页+规则日志+文档 | 1.5 天 |

总计：**~6 天**
