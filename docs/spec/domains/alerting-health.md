# 告警、诊断与健康

本文是告警投递、批量解决、Doctor、节点业务摘要、备份可信度及健康时间线的领域合同。凭据安全见[凭据与访问](credentials-access.md)，完成事实及 RPO 定义见[任务执行与恢复](task-execution-recovery.md)，运维配置见[监控与告警](../../admin/monitoring-alerting.md)。

## 告警身份与持久投递

运行时注入 `*alerting.Dispatcher`，使用 `SendAlert/SendProbe/RaiseTaskFailure/ResolveTaskAlerts` 等方法；包级 shim 仅兼容旧调用，不能扩散。构造 fallback 可供尚未注入的现有调用；消除 shim 的改动须有针对改动文件的边界回归，避免测试修改全局告警状态。
只保留仍有调用方的包级兼容入口；零消费者包装的删除不退役同名 `Dispatcher` 方法，也不改变普通运行与恢复运行的告警因果范围、投递或解决语义。边界回归以真实 Dispatcher 行为为准，不为保留包装层新增全局调用。

自动告警 replay 按 task/run/action 幂等，独立于通知 dedup window，已确认/手动解决的告警也不能重建。自动恢复仅对引发它的普通运行范围有效：迟到成功不解决较新失败，迟到失败不重开已被后来恢复覆盖的故障；restore 故障只被后续成功 restore 解决，与普通 backup 故障互不混解。手动解决是独立操作。

`000084_alert_delivery_intents` 分开持久化 Alert 决策、按 channel 的规范 intent 和带 attempt fence 的 lease；`000085_alert_delivery_success` 保存私有可空 `AlertDelivery.SentAt`。两引擎 schema 配对、启动验 drift、used-down 拒绝删除证据。

- Alert `delivery_decision` 为 `pending|direct|suppressed|escalated|no_channel|unknown`，与 Alert status 独立；reason 区分 silence、grouping、threshold/cooldown、no enabled channel、escalation。首次网络发送前提交 intent，重启继续 pending。历史 NULL 决策不是补发队列，已持久 suppression/escalation/no-channel 不因 replay 变 direct。
- 每级 escalation event 与该级 channel intent 同事务提交，失败不推进级别，提交后退出由 retry worker 续发。不同 event key 独立，即使复用 channel。分组决策写失败后同一持久告警可重放，不能把自己的重试当新重复告警。
- initial/自动/手动发送共用 DB 原子 claim 和有限 lease。仅匹配且仍 live 的 attempt 可提交结果及 success time，stale/expired 回执不覆盖新状态。不能用旧 model `Save` 覆写 Alert 决策或 delivery 结果。
- `sent_at` 是 Provider 成功完成事实，冷却按该时间排序，不能用 intent 创建时间或 model updated_at；历史 NULL 保持未知，不补为迁移时刚发送。所有 claim 入口共享 legacy blank-key 身份归一：可证 direct 重复归为规范意图，含歧义 escalation 历史保留 unknown，不盲删/重发，不合并不同事件。
- 飞书/钉钉/企业微信要求有界、可解析的业务成功确认，HTTP 200 不足；空、畸形、缺确认、超限响应均不 sent。generic webhook 维持 HTTP 2xx 语义。明确永久配置拒绝终止自动重试，暂时错误按退避；日志/错误不保留响应原文或 webhook token。
- 远端收到消息但 receipt commit 前进程退出仍可能重发；不得承诺无条件 exactly-once。已 resolved 的 Alert 可以完成既有 pending intent 而不 reopen；已确认 sent 不重复发送。已提交 intent 找不到 integration 为失败，resolver/DB 异常不能伪装终态 suppression。

API/前端区分 pending、sending、retrying、sent、failed 和兼容 unknown；等待或未知不等于成功。私有 DeliveryKey、AttemptID、SentAt 不直接公开。silence API 的 `match_node_id/match_tags/starts_at` 等在 wrapper 映射，组件不回退读 wire 字段；非法数组/数值/枚举采取安全非成功默认，不合成秘密配置。

回归覆盖 intent 先于网络、commit/receipt 失败重放、restart、historical NULL、全部终态 decision、resolved pending、sent 不重发、自动/手动并发 claim、stale/expired result、legacy blank key 与 escalation ambiguity、channel business ack、success-time cooldown、未知页不阻塞后续 retry，以及 SQLite/真实 PostgreSQL 竞争。`durable_delivery_test.go`、`dispatcher_test.go` 是当前关键证据入口。

### 通知配置来源与人工返回

通知页与设置页保持分工：管理员可从通知列表或具体告警进入通知渠道、静音规则和
升级策略配置。来源只携带列表标记或经校验的同一告警 ID，不携带告警正文、草稿或
任意返回 URL。告警导航接受正的 JavaScript 安全整数十进制 ID 及既有 `alert-` 前缀，
返回时规范为 `/app/notifications?alert=<ID>`；列表返回固定为 `/app/notifications`。
空白、前导零、超界或重复的 `alert` 参数均为无效目标，不发起单条告警读取。

- 保存成功或取消编辑均停留设置页；取消不提交草稿，保存不自动返回。合法来源在
  三个配置 tab 间保留，由用户显式选择“返回通知列表”或“返回原告警”。
- 来源只对当前文档和身份代次有效，同一身份下前进/后退可保留；刷新、新标签页或
  失效/篡改来源按无来源的直接设置访问处理，不提供虚构返回入口或外站跳转。
  身份变更（包括同 token 重新登录）、降权、登出或认证转换开始立即撤销旧来源，
  不因恢复旧身份或浏览器历史而重新生效；关联待处理 UI 回调不得导航、反馈或污染
  新身份数据。已提交到服务端的配置不因此撤销，也不自动重发。
- 具体目标保留在 URL，直到用户明确返回列表。核实成功的同一告警即使不在当前分页
  或普通筛选结果中也持续显示；目标读取失败不能由旧列表副本或另一条告警冒充成功。
  无效标识、加载中、原告警不存在或已删除、当前无权查看、暂时读取失败分别展示。
  不存在、无权限与其他读取失败可显式重试同一 ID，也可返回列表；无效目标只能返回
  列表。不自动重试或清除目标。
- 配置往返只导航和读取；返回本身不保存、不发送通知、不重试投递、不重放告警或
  执行升级预演。配置管理仍限管理员，既有后端 RBAC 与 API 合同不变，不新增 API。

### 升级策略只读预演

管理员可在升级策略编辑器展开“预演（不保存）”，提交当前完整草稿及节点、错误码、
初始严重度、告警标签、UTC 触发时间与假设首次扫描时间；不需要先保存，同名草稿也可
预演。首次展开同时捕获两个 UTC 时间，此后编辑其他字段或等待不会推进场景时间。
已有告警在卡片和列表共用的投递面板提供“诊断升级（只读）”，必须显式点击，只提交
告警 ID；服务端按本次生成时刻读取当前状态、级别游标和绑定。

- `POST /api/v1/escalation-policies/preview` 接受严格单一 JSON 对象
  `{policy,scenario:{node_id,error_code,severity,tags,triggered_at,evaluation_at}}`。
  所有字段必需，拒绝 null、未知字段、尾随对象；不检查策略名称唯一性。
- `GET /api/v1/alerts/:id/escalation-preview` 仅接受正的 JavaScript 安全整数 ID，
  拒绝 query 和非空正文。两端要求 admin 和 `escalation:read`，已有告警另要求
  `alerts:read`，不要求 step-up/grant，不经过审计写入中间件。
- 两端使用 `Cache-Control: no-store`、64 KiB 正文限制、10 秒上下文预算及每分钟
  200 次限流。无效请求 400、超限正文 413、缺失资源 404、快照容量超限或取消/超时
  503，实际数据库错误安全返回 500；未登录/无权限分别 401/403。

所有查询在一个专用连接的只读快照内完成：SQLite 显式 `BEGIN`，不受
`_txlock=immediate` 升格为写锁；PostgreSQL 使用 `REPEATABLE READ READ ONLY`。
预演不保存策略、不更新告警、不创建事件、投递、授权或审计，不解密 integration，
也不调用发送、重试或真实升级引擎。渠道仅投影 ID、名称、类型和 enabled；
enabled 不表示可达。数据库错误不能伪装无策略或部分结果。

绑定沿真实 Task → Task.Policy → SLO → Node 惰性链解析；缺失关联资源继续回退，
真正查询错误失败。选中策略缺失返回 404，禁用策略不回退。已有 durable
`delivery_decision` 保留，不重算初始派发；open 告警即使原决定为 direct/suppressed，
仍按当前引擎扫描规则解释。历史策略不同仅给 warning，不重置游标或倒推历史事件。
若待推进级别已有提交事件，返回 `unsupported_state`，不预测唯一键不可能提交的事件。

结果基于 `current_snapshot`，分别包含 `generated_at`、60 秒后的 `expires_at`、
`evaluation_at`、固定 `tick_seconds=30`、请求告警 ID、绑定来源、原始下一等级条件、
安全策略/告警摘要、已有投递状态计数、固定 warning codes 和最多五个尚未推进等级。
`due_at` 始终为触发时刻加绝对延迟；`projected_at` 是从 evaluation_at 开始的最小
30 秒扫描网格点，每次最多推进一级，下一等级最早再隔 30 秒，**不是发送或送达时间**。
假设期间配置、告警状态与节点标签不变且每次提交成功，不包含重启、处理耗时、
数据库故障、Provider 网络与渠道重试。

投射复用引擎的严重度门槛、override、标签合并和真实 `MatchSilence`。
静默在每级 projected_at 按已保存的 `[starts_at,ends_at)` 窗口判断，标签匹配节点，
类别匹配精确值或 `类别-ASCII数字`；所有重叠命中 ID 排序展示，不赋予规则优先级。
规则读取最多 1000 条，超限失败，不能截断后声称完整。静默和无启用渠道仍假设推进
严重度、标签与游标，分别显示 `silenced`、`no_enabled_channels`，不等于 sent。
降级 override 使严重度低于策略门槛时，后续行分别为 `blocked_minimum/blocked_prior`，
不再推进或匹配静默。

顶层状态为 `projected|not_open|no_policy|disabled|below_minimum|complete|unsupported_state`。
已确认/解决告警不投射新等级，保留已有投递计数并说明既有意图不会被撤销。
无策略、禁用或低于门槛只说明当前不进入升级，直接通知由另一条链路决定，不能宣称
“不会通知”。预演不模拟直接通知的 grouping、threshold、cooldown 或 receipt/retry。
损坏持久状态、不安全数字/时间、退役来源返回 unsupported_state，无部分 policy、
alert、steps、next 或 delivery 数据；仍保留已验证的 requested_alert_id。
已完成且可安全表示的游标大于当前末级仍为 complete，不当作越界错误。

草稿每级最多 100 个渠道引用，标签每组最多 10 个、每项 32 UTF-8 字节，错误码
1–128 UTF-8 字节且无控制字符。延迟、时间差、网格和年份均显式检查溢出；
这些限制只用于预演，不更改现有保存/执行合同。持久标签的空字符串、空白、
SQL NULL 和 JSON null 兼容为空数组，非字符串元素及损坏 JSON 拒绝；
policy levels 的空/null 不代表有效配置。引擎仍使用原 elapsed-duration 判断。

客户端严格验证 DTO、请求模式与请求告警 ID，不以默认值修复坏响应。
快照新鲜度按服务端 lifetime 减往返耗时后以单调时钟计算；过期只提示重新预演，
不自动刷新，不是执行授权或配置锁。编辑、保存开始、折叠、刷新卸载、身份代次或
认证转换立即取消并清除旧结果；迟到成功、失败及 finally 不得写入新身份/草稿。
策略编辑器的渠道读取和保存也遵循同一所有权围栏，读取失败可显式重试，不伪装空列表。

回归覆盖纯计算与真实 durable intent 入队的引擎一致性、严格请求与权限、全表零写入、
SQLite WAL/真实 PostgreSQL 快照和取消清理、前端乱序身份隔离及真实后端 Chromium
两种入口。PostgreSQL 必须用独占测试库的 `TestEscalationPreviewPostgres` 验证，
无 DSN 的 skip 不是双引擎通过；浏览器夹具不创建 pending 告警/可重试投递。

### 外部 cron 数据库备份健康

外部数据库自备份不伪造 TaskRun。启用 `CRON_DB_BACKUP_STATE_DIR` 后，Core 的
CronBackupWorker 启动立即观察，之后每 60 秒串行处理；运行记录的来源与故障介质
边界归[部署运行时](../backend/deployment-runtime.md#cron-作业执行记录)。
未配置或配置无效时不注册来源、不创建告警。

`cron_backup_health` 以规范绝对状态目录加 NUL 加实际引擎的 SHA256 为 source_key，
永久保存 enrollment、首次合法 source_id、最高 revision、当前故障周期及精确 alert_id。
首次 enrollment 与 `cron_backup_health_usage` 的固定 id=1 永久使用标记同事务写入。
删除业务 cursor 不能清除使用证明；已使用 schema 禁止 down，元数据准入拒绝须保持
版本 clean。告警代码使用 `XR-CRON-DB-BACKUP-` 加完整 source_key；PostgreSQL
error_code 容量扩展到 128，存在超过旧 64 字符容量的代码时同样禁止降级。

生产锁顺序固定为 `state.lock → 数据库 source 行`；enrollment 独立先提交再获取
文件锁，不反向持锁。取得文件锁后重新读取记录与 run.lock，并在读完状态后采样
检查时间；事务内使用 PostgreSQL 行锁或 SQLite 写事务重核源身份及 high-water。
单次数据库操作期限 5 秒；运行器最终发布的文件锁等待预算为 10 秒，不能短于此期限。
state.lock 缺失、不可访问或锁超时不创建/解决告警，也不刷新首次宽限；
这表示无法得到可信快照，需独立外部监控，不能用缓存错误覆盖刚提交的成功。

锁内发现损坏/不支持版本、源身份改变、revision 回退，或已观察后文件缺失，
均为 state_invalid，不自动重新注册。首次无记录/never_run 的宽限从 enrolled_at 与
有效 initialized_at 的较早者起算，超过共用 max_age 才进入 stale。
failed、interrupted、overdue_running、stale、state_invalid、clock_anomaly 开始故障；
普通 running 不恢复故障，仅最新新鲜 success、格式/时钟/身份正常才恢复。

每个 source 一个连续故障周期：首次故障与 cursor 同事务创建 warning/open、
NodeID=0、NodeName=localhost、Retryable=false、DeliveryDecision=pending 的 Alert。
消息仅含固定安全文案、引擎与故障类型。重复观察、Core 重启、故障类型变化或人工解决
都不重建/重开该周期。新鲜成功仅条件解决 cursor.alert_id 的 open/acked 告警并关闭
周期；人工已解决时不重复恢复通知，后续新的故障才能创建新 Alert。
事务失败不能只推进 Alert 或 cursor 的一方。

网络发送完全交给原 RetryWorker 的持久 pending 续发、静默、分组、升级与 intent
围栏，不在文件锁或数据库事务内发请求。已 resolved 的 pending intent 可以按原合同
完成而不 reopen。只保留最近尝试、最近成功及当前故障周期，不补造离线逐次历史，
也不逐条重放停机时已经恢复的失败。Core/数据库停机时不能实时通知；状态文件和数据库
恢复必须保留匹配的 source 身份与 revision，不能单独回退文件后宣称健康。

### 静默窗口的本地时间

创建静默规则的日期控件显示浏览器本地时间，提交时转换为对应 UTC 瞬间，不截取 UTC
字符串作为本地输入。默认窗口和 1/4/24 小时预设只捕获一次当前瞬间，向下舍入到分钟后
按实际毫秒时长计算结束时间；24 小时不是下一日历日。预设保留原瞬间，即使夏令时回拨
导致本地时间重复，也不重新解析为另一次出现的时间。

手动编辑只使该端的预设瞬间失效；输入必须是有效的本地年月日时分，且转换后能逐字段
往返一致。非法日期、空时间和夏令时跳过的时间不得提交。重复的手填时间使用 JavaScript
本地解析得到的瞬间，每端紧邻显示实际 `UTC±HH:mm` 供提交前核对。名称不能为空，结束
必须严格晚于开始；错误关联对应输入并聚焦首个错误，成功反馈包含实际窗口摘要。
这些交互不修改后端 matcher、半开时间窗或告警状态，也不增加二次验证要求。

回归使用独立进程的 UTC、Asia/Singapore、America/New_York 时区，覆盖预设时长、
跨日、跳时与回拨；真实浏览器验证本地控件、UTC 请求和后端读回的一致性。

### 通知页快捷静音

通知页仅向 admin 提供通知渠道、静音规则与升级策略的设置入口，以及卡片和表格共用的
“静音此类告警”操作。快捷规则限定为**可靠的正整数节点 ID 与明确类别**，不限定为
当前单条告警；它只影响匹配通知，不确认或解决既有告警，不撤回已经投递的消息。
创建成功只显示规则入口和实际时间窗口，不刷新或伪造告警状态、投递状态及统计。

快捷来源只接受以下类别后接 `-` 和非空 ASCII 数字的完整错误码：
`XR-EXEC`、`XR-VRFY`、`XR-NODE-EXPIRY`、`XR-RETN`、`XR-INTG`、
`XR-DRILL-drill_sandbox_unreachable`、`XR-DRILL-drill_verify_failed`、
`XR-DRILL-drill_restore_failed`、`XR-SNAPSHOT-CHURN`、`XR-SNAPSHOT-RANSOM`。
节点 0 为平台来源；缺失、非正或非安全整数节点、任何关联 SLO、裸类别、未知或退役
来源均不生成快捷规则，只显示原因及规则设置链接。平台 cron 的哈希后缀不能用现有
数字实例匹配器精确表达，不得把节点 0 降级为 `null` 或清空类别来创建跨节点规则。

快捷表单复用设置页创建对话框及本地日期逻辑，默认一小时。节点、类别只读，标签固定
为空数组；先按 ID 读取并核对当前节点，读取失败、节点不存在或 ID 不符时保留范围并
拒绝创建，可明确重试。提交再次验证固定范围，不能回退到自由表单的通配状态。
设置页普通模式仍允许用户显式选择节点、类别和标签，包括原有通配选项。

创建、节点核对、规则列表和撤销响应均绑定挂载实例、本地请求代次与认证身份代次。
关闭、卸载、降权、身份切换或认证转换开始使旧操作失效，A→B→A 不恢复旧草稿，
迟到成功、错误及 finally 不得刷新新列表、关闭新弹窗或显示旧提示。创建请求不自动
重试，关闭不能撤销已提交的服务端操作。列表失败保留同身份已成功读取的规则，
没有成功读取前不得声称规则为空。同一设置面板一次只结束一条规则，等待期间禁用
所有结束操作，避免另一行的请求使已提交操作的反馈或刷新丢失。
关闭快捷或限制说明弹窗后，焦点返回同一告警当前可见的更多操作；断点切换或列表刷新
使原按钮不可用时重新定位，告警已不在列表则返回可用的告警工具栏控件。

回归覆盖快捷允许集与拒绝边界、固定范围 POST、节点核对失败重试、权限与身份迟到
响应、原有日期/DST 行为，以及真实 matcher 的节点、类别和半开时间窗口。
真实 UI/API 验收使用独立 SQLite 实例，验证读回规则、告警状态不变及非 admin 的
写入拒绝；这不构成新增 PostgreSQL 并发语义的验证。

<a id="退役来源告警封存与投递围栏"></a>
## 退役来源告警封存与投递围栏

两次不可逆退役迁移各自在删除来源记录前物化自己的告警 ID 集合；它们共用投递围栏机制，但来源边界不能合并解释。

### 节点监控职责收敛来源

已发布的 `backup_focus_retirement` 仅匹配以下集合：

- `^XR-NODE-[0-9]+$`（`XR-NODE-<纯数字>`）；历史手动连接测试失败若沿用同一代码也属于该集合；
- `XR-NODE-DISK-FULL`；
- EWMA / `disk_forecast` 事件关联的告警，以及确定的 CPU、MEM、LOAD、DISKFORECAST 错误码；
- `slo_id` 指向 availability 定义的告警。

服务监控告警不属于这次迁移的集合；它不删除服务监控配置/采样表。其历史 SQL 与验收仍保留这一边界，不能将后续退役语义追溯到它。

### 服务监控退役来源

后续独立的 `service_monitor_retirement` 仅匹配 `^XR-SERVICE-DOWN-[0-9]+$`（纯数字服务 ID），包括监控项已删除的孤立告警，不要求节点 ID 为零；空尾、非数字尾及近似前缀不属于集合。它封存自己的集合后才删除两个服务监控专属表，不重新定义前一次迁移的来源。

### 两次迁移共用的投递围栏

`XR-NODE-EXPIRY-*` 到期告警明确排除；不能用宽泛的 `XR-NODE-*` 把到期或其它仍有效来源一起封存。备份快照异常、任务/恢复/执行、success-rate SLO 和节点到期告警继续按各自合同处理。

集合中的 Alert 行、升级历史、delivery attempt 历史和已发送证据保留。`open`/`acked` 告警改为 `resolved`，其余状态不变，并设 `retryable=false`、`delivery_decision=unknown`、`delivery_reason=feature_retired`；只在原决定时间为空时填入迁移时间。`status <> sent` 的 delivery 改为 `status=failed`、`decision=unknown`，清空 lease/`next_retry_at` 并更新 `updated_at`，保留 attempt count、错误、`sent_at` 和历史标识；`status=sent` 的行完全不修改，即使其历史 `sent_at` 为空。该 unknown 围栏阻止自动 retry、升级和手动 claim，不能把 resolved 当作可重发许可；其它来源的投递不受影响。

迁移的数据删除边界、备份资产/任务/审计保留项和灾难恢复要求见[备份、恢复与快照的升级章节](../../admin/backup-recovery.md#升级与灾难恢复)；本节是告警状态与投递事实的主文。

### 投递统计与窗口状态

`GET /api/v1/alerts/delivery-stats` 继续按 delivery `created_at` 和既有 hours 默认值/上限定义窗口。总量与逐通道聚合使用同一可见关系：保留所有 `sent` 成功事实（包括退役父告警与历史 `sent_at=NULL`），排除父告警 `delivery_reason=feature_retired` 的所有非 `sent` 行。仅退役封存的通道不生成零成功率警告；非退役 failed、其它 unknown、NULL/空 reason 及 admin/viewer 原本可见的孤立父记录保留原统计语义，operator 仍只见 owned node 的父告警。普通 pending-only 通道表现不变，任一聚合查询失败不返回部分结果。

已认证统计请求失败必须传播给页面，不能返回伪造的全零成功。卡片结果绑定实际请求窗口；切换、刷新或重试先清空旧数字，失败显示可重试错误，折叠摘要也不显示旧窗口数值。A→B→A 重新请求 A；只允许当前请求代次提交，窗口/身份回调变化及卸载使旧结果失效，不声称取消网络请求。返回窗口与请求不符同样视为失败。顶部 24h 统计在加载、失败或无身份时保持未知，不使用成功零值；成功后才恢复数字与对应状态。

回归覆盖 SQLite/真实 PostgreSQL 的退役成功保留、非成功过滤、孤立/NULL/ownership 矩阵，以及真实 API mapper 到页面的失败、重试、刷新与乱序响应；全局总量及逐通道使用同一口径。

### 通知页未读告警统计

`GET /api/v1/alerts/unread-count` 返回当前身份可见的未读总数、严重数和警告数。前端 wrapper 接受可选 `AbortSignal` 并传给该请求；不传 signal 的既有调用保持原来的请求。合法全零计数是成功。HTTP 500，以及仍在进行的请求被 abort，都必须抛出，不能伪装成全零成功。

通知页只更新既有的待处理总数和严重数，不新增警告卡片。两处与顶部未读徽章绑定同一次请求代次。加载、失败或无身份时保持破折号和未知态，不使用成功零值；失败显示可重试错误，重试与告警变更共用同一个刷新回调。成功后才恢复数字与对应状态。同一 token 刷新、告警确认、单条解决、批量解决、按节点解决、身份切换和卸载都会使旧结果失效。只允许当前代次提交：较新的成功 7 不会被更早的成功 0 或失败覆盖；A→B→A 重新请求 A，迟到的成功和失败都不提交。代次已经结束或 signal 已经中止时不提交；仍有效的请求若收到 AbortError，仍按失败处理。

回归覆盖 wrapper 的全零成功、HTTP 500 和未完成请求 abort 拒绝，以及页面加载未知态、失败后重试得到 9/3/6、同一 token 刷新、身份 ABA 的旧成功与旧失败、退出、卸载、严格模式（mock 故意忽略 abort）、确认/恢复/批量恢复的可见失效与新值，以及与全局刷新重叠。

## 批量解决

`POST /api/v1/alerts/bulk-resolve` 需要认证和 `alerts:write`，路由在动态 alert 路由前注册。请求恰好一种目标：`alert_ids`（去重、过滤零）或正数 `node_id`。缺少、同时提交、过滤后为空均 400；任一明确 ID 不存在 404；任一目标节点无 ownership 403；全部验证后在一个事务只更新未 resolved 的行，置 `status=resolved,retryable=false,updated_at=now`。

不删除/重处理已解决行。响应 `resolved_count` 是实际更新数，`skipped_count` 是已授权目标数减更新数，前端映射为 `resolvedCount/skippedCount`。不能由前端循环单条请求造成部分更新。回归覆盖去重、零过滤、目标隔离、already-resolved 计数、任何未授权项时全不变、标准错误及真实 router。

## SSH Fleet Doctor

`POST /api/v1/nodes/:id/doctor` 要求认证、`nodes:test`、节点 ownership。只读诊断：不创建目录、改变节点状态、更新 key usage 或补救；拒绝任何非空（包括 chunked）body 及用户指定 command/check。远端命令和路径/工具参数受服务端 allowlist 控制。

响应 safe DTO 含 `node_id/node_name/generated_at/checks`，check 为 `check/status/evidence/suggestion`，status `pass|warn|fail|skip`；涵盖 SSH/auth/known_hosts、sudo、备份目录、空间及工具，不依赖周期 probe。未知节点 404，DB 故障标准 500；SSH 配置/auth/network/handshake 失败返回结构化 fail/skip，不当成 500。证据简短脱敏，不含主机名、原始路径、命令/输出、凭据、proxy、SQL 或加密内部信息。

前端 wrapper 私有 wire DTO → `NodeDoctorResult`，缺 checks 为 []，未知 status 为 warn，非法 ID 为有限安全值，缺文本为空。失败保留当前 dialog/node context、显示错误并清除旧结果，不补充连接信息。回归覆盖 allowlist、无 body、常见故障分类、设置派生阈值、脱敏/长度、完整权限路由及 mapper/dialog。

Doctor 每次打开、换节点或重新运行均清空旧结果并建立独立操作代次；关闭、卸载、
令牌/角色变化及认证转换开始会取消请求并作废旧完成。即使底层忽略取消，旧成功、
错误与 finally 也不能改变新操作；响应 node ID 不匹配时显示错误且不可复制。
取消不承诺撤销服务端已开始的只读诊断。

admin/operator 可复制当前成功响应（包括结构化 fail/skip 和零检查结果）的安全文本：
本地化标题、生成时间、检查汇总、检查名称/状态及 DTO 内 evidence/suggestion；
不追加节点名称、地址、原始路径、命令或 SSH 输出。仅当前已确认身份、匹配节点、
非加载/错误状态可复制；系统剪贴板成功后才提示，缺失或拒绝明确失败，不自动重试。
关闭、重开、换节点、重新运行或身份失效后的迟到复制不提示、不改变新状态。

“前往连接测试”只关闭 Doctor 并聚焦当前视图中同节点可见、可用的既有连接测试按钮，
不打开编辑器、不自动连接或信任主机。筛选/分页移走或目标不可用时保持对话框并说明；
关闭动画期间目标消失则回焦当前库存容器，身份变化取消意图。必须由用户随后明确
按 Enter 或点击原按钮才执行测试；viewer 不提供 Doctor 或此导航入口。

## 节点业务摘要与备份指标

`GET /api/v1/nodes/:id/summary` 要求认证、`nodes:read` 及节点 ownership，仅返回
`open_alerts`（该节点 status=open 的告警）与 `running_tasks`（status=running 且
`node_id_snapshot` 等于该正数节点 ID 的 TaskRun），不以 Task 当前状态或可变归属替代。
历史未知节点快照不计入；前端映射为 `openAlerts/runningTasks`，节点标题独立读取节点记录。
旧节点 metrics/status/metric-series/disk-forecast 接口、资源 DTO 与采样链均移除。

SLO 仅接受任务 `success_rate`，不接受 `availability`。异常仅保留 `snapshot_diff`
及其 Sigma、事件保留和通知链；不接受 EWMA/disk_forecast 查询筛选。报表保留任务结果、
RPO/RTO，移除 DiskTrend。到期提醒和维护窗口保持原有边界。

节点创建及更新的 `status` 只接受 `online`、`warning`、`offline` 三个非空字符串，区分大小写。省略、传入 `null` 或空字符串时，新节点使用 `offline`，更新保留原状态，包括历史数据中的其他值。明确提交其他字符串时返回 400，节点配置不写入。

连接测试结果与节点配置分别维护。节点清单和属性页标明上次手动测试的结果和适用配置，
不会据此推断实时在线状态或生成周期探测告警。探测受理、结果匹配及历史展示见
[凭据与访问合同](credentials-access.md#ssh-主机密钥信任)。

节点创建与更新接受可选的 `maintenance_start`、`maintenance_end` 和 `expiry_date` 字符串。省略或传入 `null` 时，创建不设置该值，更新保留原值。空字符串清除；其他值必须可按 RFC3339 解析，错误返回 400 且节点配置不写入。新写入的时间以 UTC 和微秒精度保存，小数秒超出部分向下截断。到期时间独立于维护窗口，可设置为过去的时刻。

新值转换到 UTC 后的年份必须在 `0000` 至 `9999` 之间。节点及嵌套节点的响应以 UTC 输出这些时间，不受数据库连接时区影响。历史值若只能以原偏移表达，则保留该偏移。

更新维护窗口时，在节点写锁内把提交的端点与当前已存值合并。变更后的窗口必须两端皆空，或两端齐全且结束时刻在微秒精度上严格晚于开始。按同一时刻重复提交等同于保留原值，未更改的历史不完整或倒序窗口不会阻止无关编辑。前端省略未修改的端点，并按已存的实际时刻校验另一个端点。

`GET /api/v1/overview` 只返回当前身份可见的 `activePolicies`（只统计启用且非模板的策略 `enabled=true AND is_template=false`；前端无摘要回退时仅在策略列表已加载后按非模板策略统计，未加载时保持未知 `null` 而不伪造为 `0`）。它不再嵌入节点计数、健康事件、最近任务、任务流量或备份指标；节点的 `open_alerts`/`running_tasks` 仍由独立的 `GET /api/v1/nodes/:id/summary` 提供，`/overview/backup-health`、`/overview/backup-confidence` 和 `/overview/storage-usage` 仍是独立接口。`/overview/backup-health` 的 `summary.total_policies` 与 `degraded_policies` 返回仅针对启用非模板策略的统计，并同时暴露当前可见节点范围的 `latest_verified_backup_at` 与有效阈值 `stale_threshold_hours`。

operator 的降级策略必须仍关联其当前拥有的节点，评估连续失败时只读取归属其当前可见节点快照的运行。七天趋势按服务端时区的本地日历日分桶，各日使用相邻两个午夜作为半开区间。

`/overview/storage-usage` 先读取数据库元数据，再在独立超时预算内执行文件系统扫描。`mount_points` 在 Linux 上通过 `STATX_MNT_ID` 关联 `/proc/self/mountinfo` 超级块 `major:minor`（旧内核按解析路径匹配，其他平台回退 `st_dev`）去重，避免同卷子目录和 Btrfs 子卷重复展示整卷容量。任务目录统计覆盖 Core 本地 Rsync 目标，包括禁用策略下保留的目标。Restic 和 Rclone 的目标由远端节点或存储解释。

扫描超时或读取出错时返回 `incomplete: true`。挂载点扫描中断或出错时附加 `mount_points_incomplete: true`，未完成的 `per_node` 条目标记 `incomplete: true`。当前目标根路径返回 `ENOENT` 时，只有保留的普通 Rsync 备份运行同时匹配任务、执行节点快照和当前配置指纹，并已开始、结束且记录为 `success`/`verified` 或 `warning`/`dirty`，才把目录消失标为不完整。其他 `ENOENT` 按空目录处理，包括首次运行前取消、仅在写入前登记 `dirty`、改址后缺少当前目标证据，以及历史指纹缺失或已不匹配的情况。前端 `StorageUsagePanel` 单独提示和列出未完成目录，部分用量向下截断至两位小数作为 `≥` 下界。

目录扫描遇到目标根路径为符号链接时标记该目标不完整。根目录下的符号链接不计入目录用量。

后台存储空间监控、完整性检查与保留清理仅扫描 `enabled=true AND is_template=false` 的策略，存储监控按脱敏错误码汇总超阈状态后再对恢复正常的活动错误码解除告警；由于历史 `XR-STORAGE-LOW` 告警记录不保存策略来源，不在无可靠来源依据时猜测自动关闭既有告警（避免误关已删除或已禁用普通策略的告警）。内置策略模板逐条初始化为禁用状态（单个同名冲突不回滚其余模板），启动过程不猜测改写既有模板的 `enabled` 状态（不覆盖用户显式启用或配置导入的模板）。

`GET /api/v1/overview/traffic` 要求 `tasks:read` 与节点 ownership 过滤（operator 吞吐样本按 `node_id`、任务开始/失败事件按不可变 `TaskRun.node_id_snapshot` 过滤，空 owned node 返回按同一窗口生成的全零时间桶），支持 `window=1h|24h|7d`（默认 `1h`，桶宽分别为 5m/30m/3h）。吞吐量按 `SampleWriter` 的 10 秒采样切片聚合（同切片同任务取均值后再按不重复任务求和，最后对桶内活跃切片取均值），同时保持 `sample_count` 为桶内活跃分钟数，避免单任务多样本按分钟叠加放大；当服务端时间处于当前桶内（`now > currentBucketStart`）时，响应保留末尾进行中时间桶 `[currentBucketStart, now]` 并标记 `partial: true`（已完成历史桶省略 `partial`）。

采样、开始和失败查询分别优先保留最新记录。任一来源超过行数上限时返回 `truncated: true`。从窗口起点到该来源最新被截去记录所在桶，分别标记 `samples_incomplete`、`started_incomplete` 或 `failed_incomplete`。各标记只在为真时返回。兼容字段 `incomplete` 表示至少一个来源不完整。这些标记可与末尾桶的 `partial: true` 同时出现。

前端保留所有桶的时间位置。吞吐只使用采样完整的桶，活动柱 `max(started_count, active_task_count)` 要求开始事件和采样均完整，失败柱仅要求失败事件完整。摘要按各自来源的完整桶汇总开始和失败计数，完全缺少可靠桶的计数显示为不可用。旧响应只有桶级 `incomplete` 时留空该桶的全部指标，仅有整窗 `truncated` 而无桶级标记时留空全部桶。

采样完整的桶中，`sample_count = 0` 表示该桶未测到吞吐。前端以 `null` 表示缺测，吞吐曲线断开，提示框省略吞吐值。`sample_count > 0` 的完整采样桶保留数值吞吐，包括四舍五入后为 `0` 的值。全部为测得 `0` 时仍显示吞吐曲线和左轴单位（纵轴沿用 `0..1` 范围），两侧都是缺测桶的单个测得点以圆点标出。峰值和是否有真实样本只按采样完整且有样本的桶计算。摘要区分无吞吐采样和因截断而不可用的吞吐。

前端 `overview-api.ts` 映射 `/overview/backup-health` 时，将 7 天总运行数为 0 的 `summary.successRate7d` 保留为 `null`（`BackupHealthPanel` 渲染为中性 `—`，不伪造绿色 `100%`），并透传 `latestVerifiedBackupAt` 与 `staleThresholdHours`。`/app/overview` 路由页作为唯一认证与数据编排入口，通过 `useOverviewPosture` 并行读取 `/overview/backup-confidence` 与 `/overview/backup-health`（带 `AbortController`、请求序号守卫与 token 切换重置），同时响应手动刷新 `refreshVersion` 与概览专属定时轮询信号 `overviewPollVersion`（后台定时刷新不推进全局 `refreshVersion`，避免重置通知中心勾选状态或备份文件分页）；以 `StatCardsSection` 扁平 4 卡片展示保护态势（无外层套壳卡片，静态卡片不带悬停抬升误导；“最近已验证备份”仅以 `/overview/backup-health` 为唯一可见范围权威，不混入策略级可信度时间戳，健康接口失败时显示 `—`/暂不可用，显式返回 `null` 时显示暂无；“已启用策略”在未知状态 `null` 时显示 `—`/暂不可用而不伪造为 `0`；滞后阈值按服务端 `staleThresholdHours` 动态呈现，不将完整 `BackupHealthPanel`/`BackupConfidencePanel`/`StorageUsagePanel` 复制到概览页）；流量趋势图默认展示 `24h` 窗口，切窗加载期间保留上一响应对应的窗口标题与读屏名称并单独提示目标窗口加载中，纵轴事件计数使用整数刻度，无真实吞吐采样但存在开始/失败事件时仍保留活动与失败图层并标注进行中末尾桶，仅在采样、开始与失败均为 0 时渲染 `EmptyState`。

页面底部展示最近同步任务（最多 8 条，按最近完成/运行时间排序，展示真实耗时、完成时间及全部 8 种 `TaskStatus`，不展示伪造的 `speedMbps` 或以 `updated_at` 冒充完成时间）。存在健康事件、来源截断或加载错误时显示健康事件面板（最多展示 3 组，顶部右上角统一提供前往 `/app/notifications` 的“全部告警”入口）。`xl` 及以上两面板等高并排，任务表切换为紧凑五列。低于 `xl` 时事件面板位于任务表下方，任务表始终显示六列（含下次运行），超宽时在表格区域内横向滚动。成功返回空结果且未报告截断时隐藏健康事件面板，最近同步任务占满全宽并显示下次运行时间。

定时轮询跳过同范围内尚未完成的请求，手动刷新、切换范围和卸载仍可取消请求。备份可信度与健康各自准备就绪后即可显示。两项同时失败时，仍展示已知的启用策略数和策略入口。

趋势图已有数据且在同一时间窗口刷新时，标题图标显示更新状态并保持尺寸，读屏状态提示独立于标题。切换时间窗口时仍显示目标窗口的文字提示，标题和图表读屏名称继续对应当前显示的数据。

## 备份可信度

`GET /api/v1/overview/backup-confidence` 要求 `tasks:read`，只读按 policy 汇总安全 DTO，不持久化 confidence 历史、不返回整 Node/Task/Policy 或执行配置。响应 `generated_at/summary/items`，状态为 `healthy|warning|at_risk|insufficient`；item 带 score、reasons、evidence、next_steps 和安全 target。

缺演练证据加 `drill_missing` 并至少 insufficient，已有更强失败可 at_risk；近期失败、RPO 超限、不合格演练、verify/integrity 告警/警告均参与理由。非 healthy 至少一条可执行 next step。无可见启用非模板 policy 或 operator 无 owned node 返回空汇总；缺 backup evidence 是理由，不是 500，真实 DB 错误返回标准 internal error。operator 演练证据须同时拥有 source/sandbox，见任务合同。

freshness 使用 verified completion，不把 command success、可变 Node 时间或 imported baseline 当证明。前端保留状态字符串 `at_risk`，只把 summary key 映射 `atRisk`；`nextSteps/observedAt/taskRunId/lastBackupAt` 等全部映射，items/reasons/evidence/targets 缺失为 []，ID/score/count 有限回退。原样呈现后端已脱敏理由，不解析补充主机/秘密。Backups 页面提供可见入口，回归覆盖健康/失败/RPO/缺 drill/无资格 drill/告警影响、下一步、权限及敏感字段排除。

## 健康事件时间线

`GET /api/v1/overview/health-incident-timeline` 要求 `tasks:read`；`window_hours` 默认 72，缺失/非数/非正用默认，超过 168 截为 168。仅聚合安全 DTO，不创建 Incident、不改告警/任务/节点、不重试或补救，不返回原始日志或 integration config。

响应含 `generated_at/window_hours/summary/groups`；group 包括稳定 id、severity、resource、last_seen_at、event_count、likely_cause、source_types、next_actions 和 signals。source 为 `alert|task_failure|notification_failure|anomaly|backup_stale|backup_degraded`，severity 为 `critical|warning|info`。不再生成 probe/metric 来源或查看节点指标动作；anomaly 来源保留备份快照异常。按资源/来源身份确定分组并按 last_seen_at 倒序，每组至少一个非空 href next action 和简短脱敏 cause。

父告警 `delivery_reason=feature_retired` 的投递是退役封存，不是当前通知故障；在通知失败来源查询的条数限制前排除，避免挤占真实故障。其它历史 `unknown` 决策不因该退役规则被一并隐藏，投递和审计历史仍保留。

TaskRun 关联 Task 读取任务名字/policy，节点归属使用不可变 `node_id_snapshot`，不能以可改 Task 当前 node 替代历史身份。operator 只看 owned node；无 owned node 为空，不公开 `node_id=0` 平台告警。平台资源对 admin/viewer 按现有读取权限可见。未知角色/ownership 查询错误失败关闭，任一源查询失败不返回假部分成功。

告警、任务失败、通知失败和异常事件各最多聚合 300 条，陈旧备份最多检查按 ID 升序排列的 300 个可见未归档节点。四类事件按各自查询时间倒序、同时间按 ID 倒序。这五个受限来源均在权限和业务过滤后读取最多 301 条，遇到第 301 条时只处理前 300 条，并在成功响应顶层返回可选的 `truncated: true`。`summary` 和 `event_count` 统计已聚合结果。每组最多返回 5 条信号，概览最多预览 3 组，展示上限不触发来源截断标记。

`backup_degraded` 按策略独立判定：启用且非模板策略的 eligible 尝试（`ClassifiedAttemptPredicate`，operator 先按 `node_id_snapshot` 过滤 owned node）在数据库内按策略分区、`created_at DESC, id DESC` 排名，每个策略只取最新 3 次，无全局行数上限，其他策略的历史不能挤掉后续策略。同一 `created_at` 时较大 ID 视为更新。3 次均为失败（verified 完成为成功，无 verified 证据的 success 为未完成）才产生信号，信号时间、任务和节点取排名第一的尝试。该判定使用保留历史，不受 `window_hours` 限制。

前端在共享 context 前全部 camelCase 映射；groups/sourceTypes/nextActions/signals 缺失为 []，非法可选 ID 为 undefined，count 为 0；未知 severity→warning、source→alert、resource→platform。过滤空 href，不用 credentials/config/raw log 扩充信息；请求失败不展示旧 groups 为当前结果。回归覆盖聚合、排序、严重度提升、TaskRun snapshot、各种来源、双端 drill 权限关联、next action、mapper 及 loading/empty/error UI。

概览只认字面量 `truncated: true` 并显示覆盖范围提示，保留已读的事件和摘要。截断且无分组时仍显示面板，说明已检查的数据中未发现事件。加载失败继续显示错误及重试入口。更新后的成功响应或身份变化使旧提示失效。

公开 DTO 另有可选 `cause_code/cause_params`，系统摘要与动作按客户端语言显示，
非空 `likely_cause` 始终保留为可展开的“来源诊断”，只以普通文本呈现。用户名称和
原诊断不翻译；旧 DTO、未知 code 使用通用系统摘要，不丢原诊断。

| cause_code | 来源与参数白名单 |
|---|---|
| `alert_triggered` | 告警，无参数 |
| `task_execution_failed` | 任务失败，无参数 |
| `notification_delivery_issue` | 通知投递，`status=failed\|retrying` |
| `snapshot_churn_anomaly` | snapshot_diff / snapshot_churn，`observed_value/baseline_value` |
| `snapshot_ransomware_pattern` | snapshot_diff / ransomware_pattern，同上 |
| `anomaly_detected` | 其他备份异常，同上 |
| `verified_backup_missing` | 无已验证完成，无参数 |
| `verified_backup_stale` | 已验证完成超时，`elapsed_hours` |
| `policy_backup_failures` | 最新三次 eligible 策略尝试均失败，无参数 |
| `health_attention_required` | 初值或未知类别的通用摘要，无参数 |

小时数为非负安全整数；观测值/基线为有限数且绝对值不超过 9007199254740991，
合法零值保留，非法值省略而非补零。参数不得包含名称、地址、路径、命令或任意错误
上下文；mapper 按 code 白名单裁剪，错误参数不拒绝整个事件。测量参数不完整只显示
类别摘要，完整时显示一位小数。原因原文/code/params 按严重度优先、同严重度时间
优先原子选择，不受最多五条 signals 截断影响；空白原文不覆盖已有原因。

健康动作仅允许站内绝对只读目标：通知中心（可含 `alert=alert-<id>`）、任务日志
（`task=<id>`）、节点告警（`nodes/<id>?tab=alerts`）、任务、策略和备份概览。
ID 必须是正安全整数；拒绝反斜杠、控制字符、外站、退役 metrics 和未知目标。
按原序选首个合法动作；未知 code 的合法地址仍可用，但只显示通用动作名，不信任
服务端 label。旧 confidence `/app/backups` 规范为 `/app/backups/overview`。
无合法动作时仅根据 resource 自身 type/id 回退：node→节点告警，task→任务日志，
有效 policy→策略，其余→通知中心；不借关联 ID、名称或 group ID 猜目标。
以上导航不预取或绕过后端 ownership，不新增精确 Run 查询合同。

## 服务监控退役

HTTP/TCP 周期探测、管理页面、公开状态页及其专属配置/采样表已退役。旧 `/app/service-monitors`、`/status` 地址使用现有未找到页；旧 service-monitors CRUD API 和 `/api/v1/status-page` 返回 404，不提供兼容跳转。历史服务告警按上述精确代码集合封存，不删除业务历史。

历史任务统计已迁入[任务合同](task-execution-recovery.md#历史任务统计)；可配置看板、panel-query 和通用指标目录已退役。备份健康、恢复校验、任务告警以及程序自身的 `/healthz`、`/readyz`、`/metrics` 保留。

周期节点 probe 与资源采集已退役。本地备份存储告警复用注入 Manager 的 Settings 服务，按数据库覆盖、环境变量、默认值读取阈值；设置调整后下一次空间检查可触发或解除同路径告警。任务历史清理按每轮固定的动态保留期限运行，优先级与安全跳过规则见[环境变量](../../env-vars.md#数据保留)。
