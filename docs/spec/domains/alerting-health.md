# 告警、诊断与健康

本文是告警投递、批量解决、Doctor、备份可信度、健康时间线及仪表盘数据权限的领域合同。凭据安全见[凭据与访问](credentials-access.md)，完成事实及 RPO 定义见[任务执行与恢复](task-execution-recovery.md)，运维配置见[监控与告警](../../admin/monitoring-alerting.md)。

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

## 批量解决

`POST /api/v1/alerts/bulk-resolve` 需要认证和 `alerts:write`，路由在动态 alert 路由前注册。请求恰好一种目标：`alert_ids`（去重、过滤零）或正数 `node_id`。缺少、同时提交、过滤后为空均 400；任一明确 ID 不存在 404；任一目标节点无 ownership 403；全部验证后在一个事务只更新未 resolved 的行，置 `status=resolved,retryable=false,updated_at=now`。

不删除/重处理已解决行。响应 `resolved_count` 是实际更新数，`skipped_count` 是已授权目标数减更新数，前端映射为 `resolvedCount/skippedCount`。不能由前端循环单条请求造成部分更新。回归覆盖去重、零过滤、目标隔离、already-resolved 计数、任何未授权项时全不变、标准错误及真实 router。

## SSH Fleet Doctor

`POST /api/v1/nodes/:id/doctor` 要求认证、`nodes:test`、节点 ownership。只读诊断：不创建目录、改变节点状态、更新 key usage 或补救；拒绝任何非空（包括 chunked）body 及用户指定 command/check。远端命令和路径/工具参数受服务端 allowlist 控制。

响应 safe DTO 含 `node_id/node_name/generated_at/checks`，check 为 `check/status/evidence/suggestion`，status `pass|warn|fail|skip`；涵盖 SSH/auth/known_hosts、sudo、备份目录、空间、工具及可用 probe 状态。未知节点 404，DB 故障标准 500；SSH 配置/auth/network/handshake 失败返回结构化 fail/skip，不当成 500。证据简短脱敏，不含主机名、原始路径、命令/输出、凭据、proxy、SQL 或加密内部信息。

前端 wrapper 私有 wire DTO → `NodeDoctorResult`，缺 checks 为 []，未知 status 为 warn，非法 ID 为有限安全值，缺文本为空。失败保留当前 dialog/node context、显示错误并清除旧结果，不补充连接信息。回归覆盖 allowlist、无 body、常见故障分类、设置派生阈值、脱敏/长度、完整权限路由及 mapper/dialog。

## 备份可信度

`GET /api/v1/overview/backup-confidence` 要求 `tasks:read`，只读按 policy 汇总安全 DTO，不持久化 confidence 历史、不返回整 Node/Task/Policy 或执行配置。响应 `generated_at/summary/items`，状态为 `healthy|warning|at_risk|insufficient`；item 带 score、reasons、evidence、next_steps 和安全 target。

缺演练证据加 `drill_missing` 并至少 insufficient，已有更强失败可 at_risk；近期失败、RPO 超限、不合格演练、verify/integrity 告警/警告均参与理由。非 healthy 至少一条可执行 next step。无可见启用非模板 policy 或 operator 无 owned node 返回空汇总；缺 backup evidence 是理由，不是 500，真实 DB 错误返回标准 internal error。operator 演练证据须同时拥有 source/sandbox，见任务合同。

freshness 使用 verified completion，不把 command success、可变 Node 时间或 imported baseline 当证明。前端保留状态字符串 `at_risk`，只把 summary key 映射 `atRisk`；`nextSteps/observedAt/taskRunId/lastBackupAt` 等全部映射，items/reasons/evidence/targets 缺失为 []，ID/score/count 有限回退。原样呈现后端已脱敏理由，不解析补充主机/秘密。Backups 页面提供可见入口，回归覆盖健康/失败/RPO/缺 drill/无资格 drill/告警影响、下一步、权限及敏感字段排除。

## 健康事件时间线

`GET /api/v1/overview/health-incident-timeline` 要求 `tasks:read`；`window_hours` 默认 72，缺失/非数/非正用默认，超过 168 截为 168。仅聚合安全 DTO，不创建 Incident、不改告警/任务/节点、不重试或补救，不返回原始日志或 integration config。

响应含 `generated_at/window_hours/summary/groups`；group 包括稳定 id、severity、resource、last_seen_at、event_count、likely_cause、source_types、next_actions 和 signals。source 为 `alert|task_failure|notification_failure|anomaly|probe|metric|backup_stale|backup_degraded`，severity 为 `critical|warning|info`。按资源/来源身份确定分组并按 last_seen_at 倒序，每组至少一个非空 href next action 和简短脱敏 cause。

TaskRun 关联 Task 读取任务名字/policy，节点归属使用不可变 `node_id_snapshot`，不能以可改 Task 当前 node 替代历史身份。operator 只看 owned node；无 owned node 为空，不公开 `node_id=0` 平台告警。平台资源对 admin/viewer 按现有读取权限可见。未知角色/ownership 查询错误失败关闭，任一源查询失败不返回假部分成功。

前端在共享 context 前全部 camelCase 映射；groups/sourceTypes/nextActions/signals 缺失为 []，非法可选 ID 为 undefined，count 为 0；未知 severity→warning、source→alert、resource→platform。过滤空 href，不用 credentials/config/raw log 扩充信息；请求失败不展示旧 groups 为当前结果。回归覆盖聚合、排序、严重度提升、TaskRun snapshot、各种来源、双端 drill 权限关联、next action、mapper 及 loading/empty/error UI。

## 仪表盘与服务监控

`POST /api/v1/dashboards/panel-query` 认证加 `dashboards:read`。客户端可传 `node_ids/task_ids`，`OwnershipScoped/OwnershipNodeIDs` 是 `json:"-"` 服务端字段，绑定后计算，不受 payload 或 dashboard definition 覆盖。operator 的空 node filter 表示全部已拥有节点，空 task filter 保留聚合但 provider 经 `tasks.node_id` 子查询限定归属；无 owned node 为空。任一显式 node/task 缺失或不归属则整请求 403，不返回部分 series；角色/DB 错误不可无 scope 查询。admin/viewer 保持现有全局读取。回归真实 router、node/task 空/明确 filter、无节点、混合未授权 ID、provider 空 ownership 和故障路径。

service monitor 创建保留显式 disabled，HTTP headers 的写入专用及并发用途边界归[凭据与访问](credentials-access.md)。节点 probe 与 task retention 的注册项不能仅凭注册便宣称动态生效：当前启动注入仍直接使用 cfg，对应数据库 registry 修改未接入消费方；这是保留的实现差异，准确启动/覆盖范围见[环境变量](../../env-vars.md)。
