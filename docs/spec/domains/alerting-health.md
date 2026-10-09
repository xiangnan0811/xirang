# 告警、诊断与健康

本文是告警投递、批量解决、Doctor、节点业务摘要、备份可信度及健康时间线的领域合同。凭据安全见[凭据与访问](credentials-access.md)，完成事实及 RPO 定义见[任务执行与恢复](task-execution-recovery.md)，运维配置见[监控与告警](../../admin/monitoring-alerting.md)。

## 告警身份与持久投递

运行时注入 `*alerting.Dispatcher`，使用 `SendAlert/SendProbe/RaiseTaskFailure/ResolveTaskAlerts` 等方法；包级 shim 仅兼容旧调用，不能扩散。构造 fallback 可供尚未注入的现有调用；消除 shim 的改动须有针对改动文件的边界回归，避免测试修改全局告警状态。

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

## 节点业务摘要与备份指标

`GET /api/v1/nodes/:id/summary` 要求认证、`nodes:read` 及节点 ownership，仅返回
`open_alerts`（该节点 status=open 的告警）与 `running_tasks`（status=running 且
`node_id_snapshot` 等于该正数节点 ID 的 TaskRun），不以 Task 当前状态或可变归属替代。
历史未知节点快照不计入；前端映射为 `openAlerts/runningTasks`，节点标题独立读取节点记录。
旧节点 metrics/status/metric-series/disk-forecast 接口、资源 DTO 与采样链均移除。

SLO 仅接受任务 `success_rate`，不接受 `availability`。异常仅保留 `snapshot_diff`
及其 Sigma、事件保留和通知链；不接受 EWMA/disk_forecast 查询筛选。报表保留任务结果、
RPO/RTO，移除 DiskTrend。到期提醒和维护窗口保持原有边界。

`GET /api/v1/overview` 只返回当前身份可见的 `activePolicies`。它不再嵌入节点计数、健康事件、最近任务、任务流量或备份指标；节点的 `open_alerts`/`running_tasks` 仍由独立的 `GET /api/v1/nodes/:id/summary` 提供，`/overview/backup-health`、`/overview/backup-confidence` 和 `/overview/storage-usage` 仍是独立接口。


## 备份可信度

`GET /api/v1/overview/backup-confidence` 要求 `tasks:read`，只读按 policy 汇总安全 DTO，不持久化 confidence 历史、不返回整 Node/Task/Policy 或执行配置。响应 `generated_at/summary/items`，状态为 `healthy|warning|at_risk|insufficient`；item 带 score、reasons、evidence、next_steps 和安全 target。

缺演练证据加 `drill_missing` 并至少 insufficient，已有更强失败可 at_risk；近期失败、RPO 超限、不合格演练、verify/integrity 告警/警告均参与理由。非 healthy 至少一条可执行 next step。无可见启用非模板 policy 或 operator 无 owned node 返回空汇总；缺 backup evidence 是理由，不是 500，真实 DB 错误返回标准 internal error。operator 演练证据须同时拥有 source/sandbox，见任务合同。

freshness 使用 verified completion，不把 command success、可变 Node 时间或 imported baseline 当证明。前端保留状态字符串 `at_risk`，只把 summary key 映射 `atRisk`；`nextSteps/observedAt/taskRunId/lastBackupAt` 等全部映射，items/reasons/evidence/targets 缺失为 []，ID/score/count 有限回退。原样呈现后端已脱敏理由，不解析补充主机/秘密。Backups 页面提供可见入口，回归覆盖健康/失败/RPO/缺 drill/无资格 drill/告警影响、下一步、权限及敏感字段排除。

## 健康事件时间线

`GET /api/v1/overview/health-incident-timeline` 要求 `tasks:read`；`window_hours` 默认 72，缺失/非数/非正用默认，超过 168 截为 168。仅聚合安全 DTO，不创建 Incident、不改告警/任务/节点、不重试或补救，不返回原始日志或 integration config。

响应含 `generated_at/window_hours/summary/groups`；group 包括稳定 id、severity、resource、last_seen_at、event_count、likely_cause、source_types、next_actions 和 signals。source 为 `alert|task_failure|notification_failure|anomaly|backup_stale|backup_degraded`，severity 为 `critical|warning|info`。不再生成 probe/metric 来源或查看节点指标动作；anomaly 来源保留备份快照异常。按资源/来源身份确定分组并按 last_seen_at 倒序，每组至少一个非空 href next action 和简短脱敏 cause。

父告警 `delivery_reason=feature_retired` 的投递是退役封存，不是当前通知故障；在通知失败来源查询的条数限制前排除，避免挤占真实故障。其它历史 `unknown` 决策不因该退役规则被一并隐藏，投递和审计历史仍保留。

TaskRun 关联 Task 读取任务名字/policy，节点归属使用不可变 `node_id_snapshot`，不能以可改 Task 当前 node 替代历史身份。operator 只看 owned node；无 owned node 为空，不公开 `node_id=0` 平台告警。平台资源对 admin/viewer 按现有读取权限可见。未知角色/ownership 查询错误失败关闭，任一源查询失败不返回假部分成功。

前端在共享 context 前全部 camelCase 映射；groups/sourceTypes/nextActions/signals 缺失为 []，非法可选 ID 为 undefined，count 为 0；未知 severity→warning、source→alert、resource→platform。过滤空 href，不用 credentials/config/raw log 扩充信息；请求失败不展示旧 groups 为当前结果。回归覆盖聚合、排序、严重度提升、TaskRun snapshot、各种来源、双端 drill 权限关联、next action、mapper 及 loading/empty/error UI。

## 服务监控退役

HTTP/TCP 周期探测、管理页面、公开状态页及其专属配置/采样表已退役。旧 `/app/service-monitors`、`/status` 地址使用现有未找到页；旧 service-monitors CRUD API 和 `/api/v1/status-page` 返回 404，不提供兼容跳转。历史服务告警按上述精确代码集合封存，不删除业务历史。

历史任务统计已迁入[任务合同](task-execution-recovery.md#历史任务统计)；可配置看板、panel-query 和通用指标目录已退役。备份健康、恢复校验、任务告警以及程序自身的 `/healthz`、`/readyz`、`/metrics` 保留。

周期节点 probe 与资源采集已退役。本地备份存储告警复用注入 Manager 的 Settings 服务，按数据库覆盖、环境变量、默认值读取阈值；设置调整后下一次空间检查可触发或解除同路径告警。任务历史清理按每轮固定的动态保留期限运行，优先级与安全跳过规则见[环境变量](../../env-vars.md#数据保留)。
