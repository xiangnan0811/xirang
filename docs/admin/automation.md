# 自动化规则

自动化规则把平台事件与动作连接起来：当指定事件发生且匹配过滤条件时，系统自动执行配置好的动作。

## 工作机制

每条规则包含：

1. 事件类型：监听什么事件。
2. 事件过滤条件：可选的 key-value 条件。
3. 动作类型：触发后执行什么动作。
4. 动作配置：动作参数，支持从事件上下文提取模板变量。

```text
事件发生 → 匹配启用规则 → 检查过滤条件 → 执行动作 → 记录结果
```

## 事件类型

| event_type | 说明 | 可过滤字段 | 当前触发来源 |
|---|---|---|---|
| `anomaly_detected` | 异常事件产生 | `detector`, `metric`, `severity`, `node_id` | 异常检测器写入 `anomaly_events` 后触发 |
| `backup_failed` | 备份任务失败 | `policy_id`, `node_id`, `executor_type`, `task_id`, `task_run_id` | 任务执行失败时触发 |
| `backup_succeeded` | 备份任务成功 | `policy_id`, `node_id`, `executor_type`, `task_id`, `task_run_id` | 任务执行成功时触发 |
| `drill_failed` | 恢复演练失败 | `policy_id`, `task_run_id` | 恢复演练失败时触发 |
| `node_offline` | 节点离线 | `node_id` | 可创建规则的预留事件类型；当前节点探测路径未主动派发该事件 |
| `node_disk_high` | 节点磁盘使用率过高 | `node_id` | 可创建规则的预留事件类型；当前节点探测路径未主动派发该事件 |

## 动作类型

| action_type | 说明 | 配置参数 |
|---|---|---|
| `pause_policy` | 暂停策略下次执行（设置 `SkipNext=true`） | `policy_id` |
| `disable_policy` | 禁用策略 | `policy_id` |
| `trigger_task` | 创建一个 `trigger_type=auto` 的待执行任务运行记录 | `task_id` |
| `send_notification` | 渲染并记录通知消息到自动化执行日志；当前不会调用告警通知渠道外发 | `message` |

## 模板变量

动作配置支持 Go template 风格变量：

| 变量 | 说明 | 适用事件 |
|---|---|---|
| `{{.PolicyID}}` | 事件中的策略 ID | `backup_failed`, `backup_succeeded`, `drill_failed` |
| `{{.TaskID}}` | 事件中的任务 ID | `backup_failed`, `backup_succeeded` |
| `{{.NodeID}}` | 事件中的节点 ID | `node_offline`, `node_disk_high`, `backup_failed`, `backup_succeeded` |

## Web UI

登录后进入 `/app/automation-rules`，可执行：

- 创建规则。
- 编辑规则。
- 删除规则。
- 启用或禁用规则。

## API

自动化规则 API 需要管理员权限：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/automation-rules` | 列出规则 |
| POST | `/api/v1/automation-rules` | 创建规则 |
| GET | `/api/v1/automation-rules/:id` | 获取详情 |
| PUT | `/api/v1/automation-rules/:id` | 更新规则 |
| DELETE | `/api/v1/automation-rules/:id` | 删除规则 |

创建示例：

```json
{
  "name": "Ransomware pause policy",
  "event_type": "anomaly_detected",
  "event_filter": { "metric": "ransomware_pattern" },
  "action_type": "pause_policy",
  "action_config": { "policy_id": "{{.PolicyID}}" },
  "enabled": true
}
```

## 示例

### 勒索异常时暂停策略

```text
事件：anomaly_detected
过滤：{ "metric": "ransomware_pattern" }
动作：pause_policy
动作配置：{ "policy_id": "{{.PolicyID}}" }
```

### 备份失败时记录通知消息

```text
事件：backup_failed
动作：send_notification
动作配置：{ "message": "Backup failed for policy {{.PolicyID}} on node {{.NodeID}}" }
```

### 预留磁盘事件触发清理任务

```text
事件：node_disk_high
过滤：{ "node_id": "5" }
动作：trigger_task
动作配置：{ "task_id": "12" }
```

> `node_disk_high` 当前是可配置的预留事件类型；只有当系统派发该事件时，上述规则才会执行。

## 执行记录

规则执行结果记录在 `automation_rule_logs` 表中，包含规则 ID、事件类型、动作类型、结果和错误信息。当前没有独立的前端执行日志页面。

## 当前限制

- 过滤条件只支持简单 key-value 等值匹配，不支持 AND/OR 表达式和时间窗口。
- 动作类型仅限当前内置动作。
- 不支持自定义事件类型。
- 暂无前端执行日志查看器。
