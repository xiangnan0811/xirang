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

## 备份任务 Cron 与持久化调度

备份任务的 Cron 回调会先把调度意图写入 `task_cron_occurrences`，再进入节点、并发和执行器准入。`(task_id, scheduled_at)` 是持久化唯一键，因此两个 Core 同时收到同一时刻的回调也只会产生一条待处理意图；手动或自动触发不会伪造 Cron occurrence。任务忙、并发配额不足、节点或资源准入暂不可用时，Cron 意图保持 `queued`，由后续扫描重试，而不是静默丢失。

### 重启与错过的时刻

- Core 启动和周期扫描都会投递 `queued` 意图。调度租约过期后，其他 Core 可以接管未完成的投递；已经绑定 TaskRun 的 occurrence 不会重复执行。
- 在线调和或 Core 重启会将已持久化、但逾期的 `next_run_at` 保存为排队意图；迟到回调与调和使用同一唯一键。逾期不等于系统曾停机，不会自动生成停机跳过记录，也不承诺还原停机期间所有未知时刻。
- 任务配置已提交但即时调度同步失败时，API 返回 HTTP 503 并明确提示配置已保存。周期调和会从持久配置恢复调度；不要把请求失败当作配置回滚，也不要盲目覆盖后续修改。
- 禁用或归档任务、取消任务，或禁用所属策略时，尚未投递的 occurrence 会在同一持久化边界标记为 `canceled` 并清除租约；重新启用后只等待新的调度时刻。正在执行的 TaskRun 仍按任务取消和执行租约规则收敛，不会因删除本地调度器记录而被假定停止。

### Legacy Rclone 的共享目标占用

Legacy Rclone 写入可变 Remote。相同节点和 Remote 的并发写入由数据库持久化占用保护；`writing` 或 `unknown` 事实会继续阻止新的写入，Core 重启、租约到期或本地 SSH 关闭都不等于远端写入已经停止。管理员必须先暂停任务并确认远端及外部写入者停止，再按[显式人工协调流程](backup-recovery.md#显式人工协调)处理指定 TaskRun。完整调度合同见[任务执行与恢复](../spec/domains/task-execution-recovery.md)。

## 事件类型

| event_type | 说明 | 可过滤字段 | 当前触发来源 |
|---|---|---|---|
| `anomaly_detected` | 异常事件产生 | `detector`, `metric`, `severity`, `node_id` | 异常检测器写入 `anomaly_events` 后触发 |
| `backup_failed` | 策略关联普通任务失败 | `policy_id`, `node_id`, `executor_type`, `task_id`, `task_run_id`, `status` | 关联策略的普通运行最终失败时产生；等待重试的中间失败不立即产生 |
| `backup_succeeded` | 策略关联普通任务成功 | `policy_id`, `node_id`, `executor_type`, `task_id`, `task_run_id`, `status` | 关联策略的普通运行成功时产生；事件名本身不等于已有可信恢复点 |
| `drill_failed` | 恢复演练失败 | `policy_id`, `task_run_id` | 已定义事件；当前生产恢复演练不可执行，不应依赖它触发新动作 |
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

动作配置支持 `{{.字段名}}` 字符串替换，字段名区分大小写，必须与事件上下文一致；这不是完整的 Go template 表达式引擎。缺失变量保持原样，不能当作可用资源 ID：

| 变量 | 说明 | 适用事件 |
|---|---|---|
| `{{.policy_id}}` | 事件中的策略 ID；任务须关联策略 | `backup_failed`, `backup_succeeded`, `drill_failed` |
| `{{.task_id}}` | 事件中的任务 ID | `backup_failed`, `backup_succeeded` |
| `{{.node_id}}` | 事件中的节点 ID | `anomaly_detected`, `backup_failed`, `backup_succeeded`；预留节点事件需实际派发上下文 |

`anomaly_detected` 不包含 `policy_id`。针对异常暂停某个策略时，必须显式填入经确认的策略 ID，并按节点等条件限制匹配范围。

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
  "name": "指定节点勒索异常时暂停策略",
  "event_type": "anomaly_detected",
  "event_filter": "{\"metric\":\"ransomware_pattern\",\"node_id\":\"5\"}",
  "action_type": "pause_policy",
  "action_config": "{\"policy_id\":\"12\"}",
  "enabled": true
}
```

`event_filter` 和 `action_config` 的 API 类型都是 JSON 对象编码后的字符串。示例中的节点 `5` 和策略 `12` 必须替换为实际 ID。

## 示例

### 勒索异常时暂停策略

```text
事件：anomaly_detected
过滤：{ "metric": "ransomware_pattern", "node_id": "5" }
动作：pause_policy
动作配置：{ "policy_id": "12" }
```

### 备份失败时记录通知消息

```text
事件：backup_failed
动作：send_notification
动作配置：{ "message": "节点 {{.node_id}} 的任务 {{.task_id}} 备份失败" }
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
