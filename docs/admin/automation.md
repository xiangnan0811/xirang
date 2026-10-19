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
| `{{.node_id}}` | 事件中的节点 ID | `anomaly_detected`, `backup_failed`, `backup_succeeded` |

`anomaly_detected` 不包含 `policy_id`。针对异常暂停某个策略时，必须显式填入经确认的策略 ID，并按节点等条件限制匹配范围。

## Web UI

管理员登录后进入 `/app/automation-rules`，可执行：

- 创建规则。
- 编辑规则。
- 删除规则。
- 启用或禁用规则。
- 在规则列表下方查看执行历史，按规则和动作结果筛选、刷新及翻页；默认每页 30 条。桌面表格支持键盘横向滚动，移动端使用卡片。

operator、viewer 访问此页会返回概览，且不会请求规则或执行历史。后端权限校验仍是安全边界。

## API

自动化规则 API 需要管理员权限：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/automation-rules` | 列出规则 |
| POST | `/api/v1/automation-rules` | 创建规则 |
| GET | `/api/v1/automation-rules/:id` | 获取详情 |
| PUT | `/api/v1/automation-rules/:id` | 更新规则 |
| DELETE | `/api/v1/automation-rules/:id` | 删除规则 |
| GET | `/api/v1/automation-rule-logs` | 分页查询安全执行历史 |

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

## 执行记录

规则执行结果保存在现有 `automation_rule_logs` 表中。新记录在现有私有 `Details` 列的 `safe_execution` 对象内保存 v1 安全元数据；不新增表、迁移、索引、保留策略或历史关联回填。执行历史只读，不重放动作。

### 查询与安全字段

- `GET /api/v1/automation-rule-logs` 使用 `automation:read`，仅管理员可读；未登录为 401，operator/viewer 为 403。
- 可选 `rule_id` 必须为正整数，`result` 仅接受 `success` 或 `error`，省略表示全部；非法非空筛选返回 400。查询不要求当前规则仍存在，因此已删除规则的保留日志仍可读取，界面显示“规则 #ID（当前列表不可用）”。
- 分页沿用统一工具：默认 `page=1`、`page_size=30`；每页有效范围 1–500，无效值回退默认值，而非截断到 500。默认按日志 `id desc` 排序，可指定 `sort_order=asc`。响应为 `{code:200,message:"ok",data:[...],total,page,page_size}`；未匹配时 `data=[]`、`total=0`，超出末页时仍保留筛选总数。
- 安全 DTO 保留原字段 `id`、`rule_id`、`event_type`、`action_type`、`result`、`created_at`、`error_code`、`target_task_id`、`target_task_run_id`，并增加下表字段。事件和动作仅公开上表各四种值，其他值为 `unknown`；结果仅公开 `success`、`error`，其他值同样为 `unknown`，不能推断为成功。

| 新字段 | 含义与未知值 |
|---|---|
| `metadata_version` | 可读安全对象为整数 `1`，否则为 null |
| `execution_stage` | 下表允许的安全阶段；缺失、未知或非法组合为 `unknown` |
| `reason_code` | 已验证的稳定失败原因；成功、未知或非法组合为 null |
| `source_node_id`、`source_policy_id` | 来源事件确知的节点、策略历史 ID；各自未知时为 null |
| `source_task_id`、`source_task_run_id` | 来源事件确知的 Task/Run 对；不完整或无效时两者均为 null |
| `target_policy_id` | 成功策略动作确知的目标策略历史 ID；其他情况为 null |

`safe_execution` 只包含版本、阶段、闭合原因码和上述五个关联 ID，最大 1024 字节；这个限制不裁剪原私有 `Details` 或通知消息。读取只接受完整合法 JSON 中的指定对象及整数版本 `1`，不透传额外字段。旧记录没有对象、对象损坏／非对象／超限、未知版本时，新增字段均为未知：版本、原因和关联为 null，阶段为 `unknown`。不从旧 `Error`、顶层关联、当前规则配置或当前资源查询补造来源或阶段。

可读 v1 中，阶段／结果／动作／原因组合非法时，仅阶段和原因整体降为 `unknown`／null；版本仍可为 `1`，关联按事件和动作规则独立验证。已知版本不表示每个字段都已知。前端再次校验版本、枚举、组合及关联，未知值不作为翻译键或链接。

`result=error` 时旧 `error_code` 仍固定为 `ACTION_FAILED`，其他结果为 null；细分原因仅放在新 `reason_code`，不改变旧错误码语义。原始 `Error`、`Details`、通知消息、动作配置及 effect payload 不进入响应、页面状态或复制内容；旧错误可能包含完整配置，不能只靠通用脱敏正则保证安全。失败记录仍提示原始诊断未公开，并保留日志 ID 供管理员定位后台记录。

### 阶段与稳定失败原因

阶段表示本条记录能证明的执行边界，不是实时进度或完整执行时间线。结果 badge 仍区分“已记录／失败／未知”；旧记录或未知阶段显示“未记录安全执行阶段”，不能推断它曾成功经过某个阶段。

| `execution_stage` | 允许的动作／结果 | 含义 |
|---|---|---|
| `action_validation` | 任意失败动作 | 动作参数或执行能力检查失败 |
| `action_execution` | 策略或 trigger 动作失败 | 动作调用失败；不保证没有部分生效 |
| `policy_updated` | pause/disable 成功 | 策略控制调用成功，不表示所有执行已停止 |
| `task_trigger_accepted` | legacy trigger 成功 | 触发调用已接受，不表示最终运行成功 |
| `task_run_reserved` | durable trigger 成功 | 事务已预留运行，不表示执行器已启动 |
| `notification_recorded` | send_notification 成功 | 消息仅记录，未向通知渠道发送 |

失败原因是生产者在真实分支记录的闭合枚举，不按错误文本推测任务禁用、策略禁用、节点离线或数据库故障：

| `reason_code` | 阶段 | 含义 |
|---|---|---|
| `INVALID_TARGET` | `action_validation` | 目标标识缺失或无效 |
| `EXECUTOR_UNAVAILABLE` | `action_validation` | 任务执行器未初始化 |
| `TRANSACTIONAL_EXECUTOR_UNAVAILABLE` | `action_validation` | 任务执行器不支持事务预留 |
| `POLICY_CONTROLLER_UNAVAILABLE` | `action_validation` | 已注册的策略控制器不支持事务控制；未注册控制器仍使用现有 ControlService，不代表此错误 |
| `UNSUPPORTED_ACTION` | `action_validation` | 不支持的动作，仅允许未知动作类型使用 |
| `CONTEXT_CANCELED` | `action_execution` | 调用前已取消或调用错误链表明取消 |
| `DEADLINE_EXCEEDED` | `action_execution` | 调用前已超时或调用错误链表明超时 |
| `TARGET_NOT_FOUND` | `action_execution` | 错误链匹配目标未找到，表示目标当前不可用 |
| `ACTION_FAILED` | `action_execution` | 其他动作调用失败，不提供更细诊断 |

错误链依次按取消、超时、目标未找到、通用失败分类。成功记录不带原因；失败必须符合表中的阶段及动作约束，否则按未知处理。`action_execution` 失败明确提示“调用可能已部分生效；请核实，不自动重试”。

### 来源事件资源与动作目标

来源与目标分组展示，均为历史 ID，不是当前资源状态或名称快照。页面保留 Log/Rule ID，只把规则列表中的名称标为“当前名称”；不读取当前资源名称冒充历史。来源来自本次事件的固定允许名单：

| `event_type` | 可记录的来源 | 缺失或额外字段处理 |
|---|---|---|
| `backup_succeeded`、`backup_failed` | 节点、策略、Task/Run 对 | 节点和策略独立验证；Task/Run 任一无效则两者均省略 |
| `anomaly_detected` | 节点 | 忽略其他关联，即使 Context 中有同名字段 |
| `drill_failed` | 策略 | 现生产者只有孤立 Run、没有 Task，不公开孤立 Run，也不查表拼接 |
| 未知事件 | 无来源关联 | 不猜测资源 |

事件 ID 只接受正数、安全范围内的整数：Go `uint/uint64/int/int64`、无损安全整数 `float64` 或整数 `json.Number`；数字字符串、零、负数、小数、超界及其他类型省略。写入及读取的 JSON ID 必须是正十进制整数且不超过 `9007199254740991`，不接受字符串、小数或指数格式，不通过浮点舍入修复。API 再次执行事件允许名单及成对验证；缺失关联显示“未记录”，不是确认没有关联。

动作目标独立于来源，配置目标可以与事件来源不同，不互相替代：

- `target_policy_id` 仅在 `pause_policy`／`disable_policy` 调用成功后记录。渲染后的配置须为合法 JSON 的正整数或纯十进制数字字符串，处于安全范围，且严格读取值等于实际传给控制器的策略 ID。失败或非法目标为未知；不能用当前 Rule 配置回推旧记录，也不能把旧解析器截断小数得到的 ID 当作安全关联。
- `target_task_id`、`target_task_run_id` 保持旧 trigger 专属合同：仅 `trigger_task` 从顶层 `details.task_id`／`details.task_run_id` 读取；两者均须为合法正 JSON 十进制安全整数。任意一项缺失、字符串、非整数、超界或 JSON 损坏时两者同时为 null，其他动作始终为 null。即使缺少可读新对象，旧记录的有效目标对仍可读取，不以 `safe_execution` 推翻旧目标。
- 新 trigger 记录只在渲染配置的严格目标与实际调用 Task ID 一致，且返回 Run ID 为正数安全整数时写入完整顶层目标对。不完整或不可安全显示时两者均不写，成功阶段仍表示真实调用边界，不因缺 ID 重试已执行动作。

### 复制与精确导航

每行只复制当前身份已加载的安全标识，固定顺序为 `log_id`、`rule_id`、`task_id`、`task_run_id`、`target_policy_id`、`source_node_id`、`source_policy_id`、`source_task_id`、`source_task_run_id`，每行一个 `key=value`。非法项省略；`task_id`／`task_run_id` 仍只代表 trigger 动作目标，来源与目标两组 Task/Run 分别成对加入。没有有效标识时禁用复制。不复制阶段、原因、名称、时间、错误、Details 或通知正文，不额外读取数据，也不触发动作。

复制成功以浏览器 Clipboard API 完成为准，失败明确提示，不自动重试或使用下载等回退。筛选、分页、刷新、卸载或认证会话变化使旧复制反馈失效；身份不匹配、非管理员及认证切换期间不发起复制。已发出的系统剪贴板写入无法撤回。

- 有效来源 Task/Run 对提供“查看来源运行日志”：`/app/logs?task=<source_task_id>&run=<source_task_run_id>`。
- 有效 trigger 目标对提供“查看动作目标运行日志”：`/app/logs?task=<target_task_id>&run=<target_task_run_id>`。两组 ID 即使相同也保持各自含义；不跳最新 Run，不回退任务级日志。
- 有效来源策略和成功动作目标策略分别提供“查看来源策略”“查看动作目标策略”，复用 `/app/policies?policyId=<历史策略 ID>`。目的地维持现有授权、独立加载／失败／缺项状态及返回全部入口；缺项显示“未找到或不可见”，不自动选择其他策略。
- 节点 ID 仅展示／复制，不新增节点深链；没有真实 alert/delivery ID，不生成通知页链接。历史页不预请求目标，不自动打开链接，不触发业务写请求。目标删除或无权不移除源历史，不代表目标当前仍存在。

目标日志页先核对 Run 与 Task 配对，再读取指定 Run 的日志；非法链接、配对不符、403、404 和普通读取失败分别提示，不伪装成功空态。该视图是手动快照，即使运行尚未结束也不实时跟踪；“刷新”重新核对并读取，分页和导出仅涉及已关联到该 Run 的日志，不代表完整执行证据。关键词仅筛选已加载行；可通过“查看该任务全部日志”进入保留的任务级历史与实时视图，并用浏览器后退返回原 Run。任务列表缺项不证明删除或无权，权限继续由目标路由/API 判定；详见[任务日志读取合同](../spec/domains/task-execution-recovery.md#任务日志读取与历史入口)。

### 提交边界与不完整历史

`success` 只表示“动作已记录”，具体证明边界由安全阶段表达，不表示任务最终成功、备份可信或通知已投递。`send_notification` 仍仅渲染并私有记录消息，不向告警通知渠道外发；本项不改变事件生产协议、动作准入、事务、claim 或重试政策，也不启用恢复演练。

Legacy 直接派发失败会保留错误日志，但 controller 返回错误不保证数据库未修改；动作可能已部分生效，须核实，不自动重试。日志写入失败或提交状态未知也不能解释为业务修改未发生。durable 动作、日志和 effect 确认在同一事务中提交，事务失败整体回滚，不在事务外补写失败日志；提交后的策略调度调和失败不推翻已提交的成功记录。因此此视图不是所有事件、重试或未提交执行的完整清单，不从缺失记录推断未发生动作。

加载、无匹配记录和请求失败分别展示，失败可重试；筛选改变回到第一页。切换筛选、页码、刷新或身份后取消旧请求并隔离旧结果，不能把旧筛选行显示为新结果；失去管理员权限或认证切换时立即移除旧历史、字段与链接，迟到请求或复制反馈不得恢复它们。前端不将新关联额外持久化。实际部署环境的生命周期验收仍须单独进行。

## 当前限制

- 过滤条件只支持简单 key-value 等值匹配，不支持 AND/OR 表达式和时间窗口。
- 动作类型仅限当前内置动作。
- 不支持自定义事件类型。
