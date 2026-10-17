# 任务执行与恢复

本文统一任务配置、调度、持久化执行身份、旧版备份恢复及演练合同。受管恢复点的 Provider 身份、发布与恢复权威见[仓库与发布](backup-repository.md)，授权与秘密见[凭据与访问](credentials-access.md)，面向运维的步骤见[备份与恢复](../../admin/backup-recovery.md)。

## 配置更新与目标归属

- Task 更新是部分更新，缺字段保留原值；必须提交当前字符串 `revision` 对应的 `expected_revision`。非法/缺失修订返回 400，并发变更 409，前端保留草稿并重新加载，不能自动重试覆盖。
- 显式空 `cron_spec` 表示手动任务，关联 policy 也不补回 Cron。任务持久化调度继承来源：显式 Cron（包括清空）建立 override，策略重命名、Cron 变化及停用/恢复都不抹掉 override。停用策略停止关联任务并清活动游标；恢复时手动仍手动，自定义用自己的 Cron，继承任务用当前策略 Cron。
- 配对迁移 `000088_task_cron_override` 仅把与当时策略有效计划不同的历史 Cron 标记为 override；相同值不猜测曾经覆盖。配置导入导出保留来源；使用后禁止降级抹除。
- Cron 在写入前验证；历史坏 Cron 只隔离该任务并留诊断，不阻断全部启动。配置已保存而即时同步调度失败时返回明确“已保存”的 503，不用旧全行回滚；周期调和恢复调度。
- API 只公开白名单 `executor_settings`、`executor_secrets_configured`，不输出原始 `executor_config`。Restic 排除规则 `[]` 显式清空；密码空值保留，非空替换保留原始字节（包括空白）。仅重命名等更新保留 Rclone 带宽/并发配置。
- policy、模板克隆和导入均保留显式 `enabled=false`；policy 还保留 `verify_enabled=false`、`max_retries=0`。模板克隆始终禁用；省略字段才使用默认，零重试不自动重跑；持久化仍执行加密 hook。
- 新 policy Rsync 目标为 `<backup-root>/.xirang/policies/<policy-id>/nodes/<node-id>`。持久化 ID 隔离 writer，不能靠 source basename 或可改节点名称。同步/改名不改已有 target；路径变化不证明历史数据已经迁移。Core 本地准入拒绝规范路径冲突、symlink alias 和祖先/后代归属重叠。
- 节点迁移要求相关任务禁用、无调度及无活动 durable run；只把独立归属的源复制到全新隔离目标，验证后在锁下重核归属/配置再切换 DB。共享源及已有目标拒绝合并，保留原源。导入补偿先在全局/policy/task 锁下捕获 before-image，再核对当前 claim 与导入后行；并发变化或旧目标的新 owner 均拒绝补偿覆盖，不强制恢复旧 locator。
- Restic `repository_version` 仅选择新仓库格式（默认/1/2），不是 append-only 或删除保护。旧 `append_only=true/false` 在加密配置边界分别幂等转为 2/默认，保留其他字段，冲突或非法值隔离诊断；旧字段只允许显式导入转换，不再作为运行时配置。删除保护须在存储后端独立验证。
- Rsync allowlist 依赖 Linux Landlock ABI 3、匹配本地/远端 helper 及私有 user/mount namespace；运行环境不满足时失败关闭，不能移除 allowlist 或自动授予 privileged。受管 hardlink staging 要预留完整树容量和传输空间。可选 Worker 的精确系统包版本影响工具链 fingerprint，Core/Worker 必须由同一发布源码重建并匹配；细节见[处理与导出](backup-processing-export.md)。

### Cron 展示与时间基准

任务表格/卡片对定时任务常显原表达式、保守自然语言解释及时间基准，手动任务不添加。
未带时区前缀的任务调度使用 UTC；`TZ=`/`CRON_TZ=` 表达式使用其指定时区。
服务端 `nextRunAt` 是下次运行依据，不用浏览器时区或前端估算覆盖。

自然语言只解释恰好五段且日/月均为 `*` 的确定模式：每分钟、1–59 分钟步长、
每小时指定 0–59 分钟、1–23 小时步长、每日固定 0–23 时/0–59 分，以及固定时间
或分钟步长配合 0–6 的纯数字 weekday 列表。其余日/月限制、范围、名称、分钟/小时
列表、越界、descriptor、时区前缀、六段均原样回退表达式；空白/null/undefined 显示
未配置。不把后端支持解析等同于前端支持解释，切换语言即时更新相同表达式。
策略沿用原说明位置，不添加重复信息。

任务/策略 CronGenerator 的编辑预估只使用浏览器时钟和 UTC，不覆盖服务端 `nextRunAt`，
也不保证实际执行。有限语法为恰好五段：分钟/小时支持 `*`、范围内整数及从字段 0
开始对齐的 `*/N`（分钟步长 1–59、小时步长 1–23）；日为 `*` 或 1–31，月为 `*`
或 1–12，周为 `*` 或 0–6 的整数逗号列表。日/周均受限时取 OR，否则只约束非 `*`
的一项；跳过不存在的日期，返回严格晚于浏览器当前时间的最早 UTC 整分钟。
仅搜索到当前 UTC 日历时间加五年（不含上界）；没有结果不表示永久不运行。
名称、范围、descriptor、显式时区、六段及其他不支持语法明确显示无法预估，
不因此拒绝服务端可接受的配置。预估按 UTC 标注，中英切换即时更新；可见页面每秒
采样分钟变化，恢复可见或聚焦时即使仍处于同一分钟也刷新，支持浏览器时钟前后跳；
隐藏期间的父级重渲染不得导致返回后预估持续空白；不逐分钟自动播报。

## 历史任务统计

`POST /api/v1/tasks/statistics/query` 经主认证与 `tasks:read`，请求仅接受
`metric/aggregation/filters.task_ids/start/end`，拒绝节点资源指标、`node_ids` 和客户端
ownership 字段。空 task_ids 聚合当前身份全部可读任务，不限于前端当前页；operator
按任务所属节点过滤，无 owned node 返回空结果。显式 task_ids 逐项检查归属，缺失或
任一未授权任务整请求 403，不返回部分结果或回退全量；admin/viewer 沿用全局读取权限。

窗口为 `[start,end)`，最长 30 天，单次最多读取 500000 行；响应为
`{series:[{name,points:[{ts,value}]}],step_seconds,truncated}`，无样本不补零，
触及行数上限提示缩小窗口。`task.success_rate` 仅支持 avg：分母为窗口内所有
finished_at 非空运行，分子为 status=success，不按当前 Task 状态或 verified completion
替代。`task.throughput` 支持 sum/avg，单位 Mbps，描述采样速率聚合而非累计传输量。
`task.duration` 支持 p50/p95/p99，仅统计正 duration_ms，按既有 nearest-rank
分位算法返回毫秒；不接受旧 `task.duration_p95` 别名。

任务页提供固定三图，默认最近 24 小时，可选 7 天/30 天；时长默认 p95，可切换
p50/p99。“全部可见任务/指定任务”与任务列表当前状态筛选独立。可配置看板、
面板 CRUD、指标目录及旧 dashboard URL/API 已退役，不提供兼容转发。
任务日志仅属于任务领域：`GET /api/v1/tasks/:id/logs`、`GET /api/v1/task-runs/:id/logs` 和 `/api/v1/ws/logs` 提供任务/运行日志，并继续使用 `tasks:read` 授权；安全审计日志由独立审计领域保留，不能用任务日志替代。

### 任务日志读取与历史入口

`/app/logs?task=<Task ID>` 是任务级历史与实时日志入口，不定位精确 Run。
URL 的 `task/node/q` 优先于持久化偏好；带 `task` 而没有 `node` 或 `q` 时，相应
筛选使用全部节点、空关键字，不根据当前任务列表暗加节点条件。没有这些参数的普通
首次访问沿用已保存偏好；用户修改筛选保留无关 URL 参数和关键字原始空格。

指定任务不在当前列表中时仍保留该 ID 并查询可读日志，不自动切换全部或其他任务；
列表未就绪只显示 ID，加载成功且缺项后才说明“当前列表不可用”，列表错误单独提示。
缺项不证明删除或无权，归档任务可有保留历史，200 空数组只表示此次查询没有日志。
缺失目标也不展示其他任务的聚合进度；聚合进度仅用于全部任务视图。

首批读取失败不伪装成功空态或“已到最早”，提供显式重试。403 提示当前账号无法
读取，404 提示日志当前不可用；其他错误使用安全通用提示，不公开原始错误。
分页普通失败保留已加载记录与游标，可再次加载同一页；只有成功空页才标记末页。
首批或分页遭遇 403/404 时清空历史缓存，隐藏当前结果、关闭其全屏可见状态并禁用
导出和全屏入口；重试进行中或重试再次失败不能恢复拒绝缓存，只有当前范围首批成功
或切换范围才解除阻断。普通失败未发生权限阻断时仍保留已有可读行。

同一范围的分页单飞；切换任务、令牌或角色、卸载以及认证会话代次变化后，旧请求的
成功、失败和结束回调均不得污染新范围，也不得解除新分页的锁。读取和显式重试均
不写任务或重放执行，既有 admin/viewer 全局读取及 operator 节点 ownership 合同不变。

`/app/logs?task=<Task ID>&run=<Run ID>` 是指定运行的手动日志快照入口。只要 URL
出现 `run`，就必须恰有一个显式 `task` 和一个 `run`；两者均须为无空白、无前导零的
正十进制安全整数。缺失、空值、重复或非法标识显示非法链接提示，不继承保存的任务，
不读取任务／运行日志，也不连接日志实时流。

精确入口先读取 Run 详情，确认返回的 Run ID 及所属 Task ID 均与链接一致，才读取该
Run 的日志；配对不符不继续请求日志，不查最新运行、不退回任务日志。详情及首批日志
均成功后才允许展示正常结果、导出和全屏，成功空数组不证明运行未发生。任务列表
加载、缺项或当前状态不决定历史 Run 的可读性；不以当前节点名称补充历史行，也不
展示当前任务的进度、错误、全局运行计数或连接状态。

Run 视图不订阅实时日志、不轮询，即使目标仍在运行也只由用户“刷新”重新核对详情并
读取首批日志。刷新会清空旧快照、游标及已校验状态、关闭全屏并取消旧分页；失败只
允许显式重试。首批最多 200 条，旧页以最小持久日志 ID 为游标读取最多 120 条；非空
短页不表示完整，只有成功空页才标记末页。每页继续由服务端授权。403、404 或配对
不符清空快照并禁用导出／全屏；404 只表示当前不可用，不断言已删除。普通分页失败
保留已读行及游标，错误不展示原始诊断。认证过渡中不读取，令牌、角色、认证代次、
Run 范围变化或卸载均立即隔离旧行与旧请求；过渡结束且身份安装吻合后重新读取首批，
不重放被中断的分页或刷新。

Run 身份及筛选不写入普通任务日志偏好；仅使用 URL `q`（缺省为空）过滤已加载行，
忽略 `node`，重置只清关键字。“查看该任务全部日志”保留 Task ID，删除 `run/node/q`
并保留无关参数，以正常导航历史支持返回原 Run；非法链接的返回入口同时删除
`task/run/node/q`。查看器、全屏和导出使用同一已加载且筛选后的数组，不额外拉取全量
日志。Run 导出文件名包含 Task/Run ID，前两行标注身份及非实时快照性质；仅包含已
关联到该 Run 的日志，不代表完整执行证据。任务级导出格式保持不变。

两种入口均沿用当前 Task 节点的读取权限；Task 不存在时仅保留既有 admin 历史例外，
不改为按 Run 的节点快照授权，不新增执行、迁移或关联回填能力。

### 批量结果的已加载快照

批量结果弹窗允许 admin、operator、viewer 显式复制当前身份已读取的结果行与已加载日志，
不新增写权限要求、执行事实或完整运行证明。复制保留当前任务与日志顺序，逐节点标注真实
节点/任务 ID、执行或派发状态及已有错误；派发失败、尚未确认不能被解释为执行成功。
日志区分未加载、加载中、加载失败和成功加载的准确条数（含 0 条），失败提示不计入日志。
每任务沿用最多 50 条的请求限制，即使不足上限也不代表完整日志；折叠的已加载日志仍可复制。
复制不会展开任务、加载更多日志、分页、重试命令或改变任务执行、retain 与关闭清理语义。

角色变化保留结果行，但清空日志缓存、收起展开行；新角色状态读取成功前禁止复制，
之后重新展开才加载新角色可读日志。状态刷新失败、未知角色、身份不匹配或认证切换期间
禁止复制。批次、会话、令牌、角色变化，以及关闭或卸载会使旧复制完成反馈失效；
已发起的系统剪贴板写入无法撤销，不声称能够追回已写入内容。复制只提交点击时的本地
文本快照，等待 Clipboard API 成功后提示；失败可由用户再次操作，不自动重试或回退。

### 批量命令的显式重试来源

新建批次保存版本化、不可变的来源摘要；摘要绑定 Task/Batch/Node 身份和持久命令原始字节，
不包含名称、时间、错误或可轮换凭据。原命令仅保存在存活 Task 中，不增加长期正文副本。
历史批次只可查看和复制，不回填或猜测原命令。根任务预约前仍可编辑，但当前输入或任一运行
证据偏离原 Dispatch 后不能通过本功能重试；子任务预约和执行入口均绑定自己的不可变摘要。
显式重试入口不改变历史保留任务的普通触发行为；早于批次回执表、没有 BatchCommand/Dispatch
来源的历史任务仍按旧路径运行，不补造证据。已有永久派发或证据证明新来源却缺失必需关联时，
预约、执行绑定与终态仍失败关闭，不能降级为历史。

每次 v1 TaskRun 与 pending Evidence 同事务建立，执行入口核对预约快照，真实终态与证据
同事务提交。只有 SSH 明确非零、无 signal 的 exit-status，或可证 Start 之前未启动，才能记为
`exit_failure/not_started`；Start 错误、传输中断、取消、超时和缺失退出状态均不能证明安全重试。
既有自动重试次数和退避不变；同 Task 任一尝试为 pending、unknown、success 或来源不匹配，
即使后续明确失败也不具备资格。运行、日志及原任务清理不删除永久证据或已消费来源边，
也不能凭证据恢复已删除的命令。
链式跳过也须在同一事务内先建立 pending Run/Evidence，再写 skipped 终态与 unknown 证据及
下游 effects；不允许直接插入终态证据。根任务因依赖编辑产生的空摘要不能获得重试资格。
子批次即使走跳过路径仍受不可变来源约束，输入形状已变更时不得绕过预约校验。

`POST /api/v1/batch-commands/:batch_id/retry` 仅接受显式 `task_id/run_id` 来源集合及独立幂等键，
不接受命令、节点、名称或 retain 覆盖；写权限、专用 step-up/grant 见[凭据合同](credentials-access.md)。
服务端在 Batch→Task 有序锁内重核全部来源、当前归属及授权，整组失败整组回滚。
同一 Task 的同一失败 Run 最多关联一个新子 Task，不同请求者或不同 key 也不能重复消费；
再次明确失败应沿新子批次继续。同 requester/key 的同请求返回原子批次身份，不生成第二份任务或审计。
子批次继承 retain，提交后才派发，每次使用新 Batch/Task/Run 身份，不改变原批次。

明确未派发仅限 failed Dispatch、零 RunID、从未预约或运行且仍为初始 pending 的存活任务。
执行失败必须是最新存活 failed Run、永久 Evidence 的最新 Run，且全部历史均为同源已知失败；
活动任务、未完成 effects、禁用/归档、危险命令、来源变化或缺历史均拒绝。最新 Run 被清理后，
不得退回更早失败或从永久证据反造运行。GET 的 `retry_sources` 是只读状态提示，提交时仍须重核；
`retry_links` 保留全部已消费 task/run 来源对及子批次墓碑，用于未知提交后核对身份。

新操作创建与必要 `batch_command.retry` 审计同事务提交；审计只记录来源/子批次、计数和 retain，
不记录命令、摘要、凭据或 proof。已使用来源/证据永久保留，禁止擦除降级；升级必须排空旧写入者。

结果弹窗仅向 admin/operator 提供当前身份已核验的合格来源选择，同一节点不能重复选择。
确认展示全部目标、来源批次、失败类型和继承的 retain，多节点须输入数量，并提醒命令可能
已有部分效果、再次执行是新操作。确认后先重读整组来源；任一变化均停止，不自动缩减目标。
专用 proof/grant 与新幂等键只在本次操作内使用，不持久化命令或授权材料。

POST 已发出后遇传输失败、5xx 或无效成功响应，结果保持未知，不自动重放或换 key。
用户先读取原批次，通过冻结的 task/run 对与 `retry_links` 核对：同一存活子批次可打开，
已清理子批次不能重建，混合来源冲突禁止续交；确认无链接后才可明确续交同 key、同来源，
并重新获取授权。读取失败或来源已清理不能证明未执行。未知状态关闭时保留临时原批次，
关闭不代表取消远端执行；提交期间禁止关闭，身份变化立即阻止后续旧身份请求。

子结果使用独立批次身份轮询、日志、复制和 retain 清理；打开子结果不关闭或清理父批次，
关闭父结果前须先处理子结果。viewer 及缺少可信重试 metadata 的旧服务端响应仍仅可查看、复制。

### 通知页失败任务摘要

`GET /api/v1/tasks/failure-summary` 是只读的固定 24 小时摘要，经主认证和
`tasks:read` 授权；不接受窗口、筛选或分页参数。每次请求只读取一次服务端 UTC
`now`，按 `TaskRun.finished_at` 统计半开窗口 `[now-24h, now)`。仅
`status=failed` 且 `finished_at` 非空的行参与，对授权过滤后的行按 `task_id`
`COUNT(DISTINCT ...)`；同一任务多次失败仍计 1，窗口内失败不会被随后成功抵消。
warning、canceled、skipped、success 以及窗口外、未来或未完成的运行不计入。
成功响应使用统一信封
`{"code":200,"message":"ok","data":{"failed_tasks":0,"window_hours":24}}`（数字随统计结果变化），
并设置 `Cache-Control: private, no-store`；请求 body 和额外 query 参数均忽略，不改变固定窗口
或授权。ownership 查询或统计查询失败返回安全 500，不返回部分结果；请求 context 取消或
超时沿既有 API 约定返回 499。

摘要只统计当前仍存在的 `TaskRun`，不保证审计完整性；清理或显式删除历史（包括
创建很早但最近完成的运行）可能使数字减少，不恢复缺失历史，也不改变 retention、
显式删除策略或增加汇总表。查询不 JOIN 当前 `Task`，不使用 `Task.status`、
`Task.node_id`、`last_run_at` 或任务存活状态过滤，不返回任务 ID、名称或明细链接；
运行类别、触发类型及归档状态也不增加额外过滤。

admin/viewer 查看全部合格的现存历史，包含 `node_id_snapshot=0` 的
`legacy_unknown` 行。operator 只查看当前 owned node 集合与运行时
`node_id_snapshot` 的交集，且快照必须为正数；空 owned 集合成功返回零，绝不回退
全局查询。任务从节点 A 迁移到 B 后，旧 A 运行仍由当前拥有 A 的 operator 看到，
当前拥有 B 的 operator 不因此获得 A 的历史；同一任务在 A、B 都有合格失败时两方
各可计 1，而全局仍计 1，因此不同用户的数字不可相加。

该摘要的权限按执行节点快照解释历史，不使用单任务当前节点的 ownership 检查。
既有 `POST /tasks/statistics/query` 继续按当前 `Task.node_id` 授权，健康时间线的
既有 source/window/cap 口径保持不变；本摘要不恢复已退役的 overview.failedTasks24h。

## 持久化执行与调度

普通 TaskRun 创建冻结正数 `node_id_snapshot`，必须等于当时 Task 节点；`task_id` 与 snapshot 不可变。执行、恢复、演练、发布与准入同时核对任务 ID、节点快照及预期状态，不能只查询任意成功历史。snapshot 0 仅是迁移保留的 terminal orphan `legacy_unknown`：允许 `success|failed|canceled|warning|skipped`，状态不可变，所有可执行消费方拒绝它。活动/未知状态 orphan 或非正/不匹配 live Task 使迁移原子失败。

配对迁移 000069/000072 使旧升级和已有 69–71 安装汇合到此合同；含 legacy-zero 的 used-down 在修改 migration version 前拒绝。启动不 Force dirty；clean >=69 先后核对最小 Recovery schema，>=72 核对最终 trigger/PostgreSQL constraint，假同名空操作对象仍是 drift。通用迁移流程见[数据库规范](../backend/database-guidelines.md)。

执行租约、原子结束和 TaskRunEffect 持久化副作用保证重启可调和；批次幂等和派发回执避免重复边。历史重复 `(task_id, upstream_task_run_id)` 在标记 dirty 前拒绝迁移，须离线核实真实历史，不能猜测删除。使用过的 effect/批次回执禁止擦除降级。

- `000082` 保存私有不可变 cron occurrence 与执行备份配置 fingerprint，历史 NULL/空值保持未知。普通备份恢复要求匹配节点和 policy 执行输入；预约与执行入口使用同一锁定策略快照。
- `000086` 在本地、节点、资源、策略准入前先持久化唯一 `(task_id, scheduled_at)` occurrence；不同到期时间不合并。遇满额/busy 保留 queued intent，重启仍通过相同安全准入排空；在线调和和重启保留已知过期 `next_run_at`，不猜整个系统停机历史。
- 仅有持久身份、仍安全 pending 且旧 lease 已过期的 occurrence 可重新认领。running 或未知远端结果不能盲目重放。skip-next 只在 cron 执行入口事务消费，包括入队前/排队中设置；手动不消费，各 policy task 独立。相同 occurrence 重放不能在消费 skip 后再创建一次执行。
- 新 retry effect 的投递时刻用 `TaskRunEffect.NextAttemptAt`，普通 Cron 游标保持 `Task.NextRunAt`；旧未标记 effect 保持兼容解释。retry 拒绝也执行完整次数递增/耗尽逻辑，配置变化和取消不覆盖标记 retry 的普通游标。
- 优雅 shutdown 先封本地准入再等待，释放尚未启动 durable run 供重启；显式用户取消与停机释放分开。认领拒绝/启动前错误只释放当前 local owner。执行前拒绝、取消和普通终结均 Task→TaskRun 加锁并重新验证身份。
- 下游任务忙时链式 effect 可重试，不确认不存在的 child；禁用/归档下游保留 skipped child，重试耗尽仍是 failed effect。重放不复制同一上下游边。
- policy `max_concurrent` 在 DB policy lock 下跨节点/Core 统计普通 pending/running reservation，并在执行入口重核；全局 semaphore 独立。手动忙请求不生成延期 occurrence，Cron intent 不占配额但等待可用 slot；完成释放容量，禁用 policy 取消 pending 普通预约并记录 missed occurrence。旧非正限制保守按 1，API 拒绝负值；restore/drill 单独准入。

## 恢复页空态与导航

未选择恢复点且没有已有 plan/job 时，恢复页说明先浏览并选择可用恢复点，并提供备份概览入口。当前恢复页没有可信的功能可用性信息，默认导向无查询参数的概览；不从角色、URL 或一次 403 推测恢复已启用，也不为探测能力发送数据、恢复计划或作业请求。

带合法 `taskId` 时，admin/operator 可沿既有路由构造器打开对应任务的数据工作台；viewer、未知角色及身份切换期间不提供该数据入口，改为概览及证据引导。恢复 URL 只接受现有 task/point/plan/job/inspector 字段，不增加 repository 查询参数；非法参数仍安全重置。已有恢复点元数据和 plan/job 向导保持原行为，空态导航不创建计划或启动任务，实际恢复继续要求现有权限、step-up、grant 和显式确认。

## 旧版 Rsync 恢复

Legacy Rsync/Rclone 是可变当前树，不是历史恢复点；retention 拒绝破坏性的按年龄删除并记录安全理由。受管恢复点与 Restic snapshot 保留各自边界。升级或补证前先保全数据库、密钥、TaskRun 及独立备份副本；共享过的树可能已无法由任一历史 manifest 描述，不删除 dirty/失败证据或覆盖唯一可救副本来恢复资格。

Rsync 恢复必须找到当前配置 fingerprint、node snapshot 相符的成功普通备份及 verified capture generation。`000083` 记录 directory-self、directory-content、single-file 布局、由源端选择的 manifest 和恢复 source-run 绑定；不能统一补斜杠或换根重套排除模式，不能把节点同名路径当 Core 来源。缺源、枚举/哈希失败不是空成功备份。

恢复先在 Core 私有临时目录复制被选来源，验证暂存的内容、文件类型和 link target，完成前不创建/修改节点目标；传输仅用 verified staging，不重读可变来源。使用内容比较修复同大小/mtime 的损坏，不因此删除节点文件；恢复后仍核对 Core 与节点证据。空间不足或校验失败在目标写入前结束。

v2 manifest 对路径、逻辑根、link target 用 Base64 保留字节，哈希和路径分离、链接名和目标分离；DB root sidecar 按版本编码，不把非 UTF-8 文件名写入 PostgreSQL text。中文、换行、非 UTF-8、反斜杠、字面转义、链接箭头及目标末尾换行不改变身份。v1 manifest/raw root 继续按 v1 读取，不重写/补猜历史；旧 writer 不理解 v2，升级前排空。

取证独立于可选 policy sampling，`verify_enabled=false` 不授权无证据代次。取证上限/失败本身不阻断普通传输，但完成后只能 warning、不可自动恢复；只读 capture 期间取消且从未开始写入不弄脏前代。真实写入失败/中断保留不确定代次，不能借旧 success 背书。

Rsync 取证命令失败须保留失败阶段、底层原因、退出码（已启动进程时）及有界、脱敏的 stderr 诊断；任务历史和日志应能区分源文件消失、权限拒绝、空间不足、启动失败及取消/超时，不能统一抹成 `evidence copy failed`。错误链保留供调用者识别；stdout 文件清单不作为错误详情。输出超限须明确标注，凭据及完整或被截断的 PEM 私钥仍须脱敏。

普通 legacy Rsync 备份先按原源布局与排除规则复制到本次私有暂存树，由同一次复制的
Rsync 操作记录确定选择集合。记录本身不证明完成：仅在退出码为零、记录与树双向匹配、
类型和哈希验证通过后生成 manifest；目录根可按已确认布局补齐，不能以遍历目标补猜缺失子项。
正式传输使用同一暂存树并按内容比较，不重读活动源、不换根重套排除规则，也不改变任务
配置指纹、节点身份或历史恢复语义。暂存权限仅授予本次所属目录，目标 allowlist 继续生效；
成功、失败及取消完成后均释放暂存树。准备副本同样受任务超时、带宽和证据大小限制；带宽限制
只作用于读取来源的准备副本，暂存树到目标的 Core 本地复制不再限速。
目标已是真实目录时，先把它按目标 allowlist 读取、复制到本次 0700 私有目录作为快照，再以该快照为
准备副本的只读 `--copy-dest` 增量基准：未变文件本地复制、变更文件只传差异，不能退化为每次全量下载。
不得直接以可被他人修改的目标作基准（rsync 复制基准文件后不复核字节）。有基准时须加 `--checksum`
（同大小/mtime 的损坏不得进入证据），无基准时不额外全量校验；快照失败只记 warning 并改为全量捕获，
取消/超时仍按捕获中止处理。操作记录须用双重 `--itemize-changes` 列出含基准命中在内的完整选择集合；
隔离模式下基准须为固定描述符的目录且仅授予读取，`--copy-dest` 必须按参数语法恰好一次等于该基准。
复制中源文件消失等非零退出仍保留错误，不能把部分副本标为成功；取证失败后的普通传输与
warning 合同保持不变。副本保证传输字节与本次证据一致，不代表活动数据库的事务一致性快照。
捕获过程耗尽执行时限应记为 failed 并保留超时及底层诊断，不能记为用户取消；用户取消仍走
原取消补偿。两者在正式传输尚未启动时均不得弄脏前代，未开始任何工作的入口补偿语义不变。
准备副本期间按固定间隔记录已接收字节与耗时；尚未写入数据时记 info（比对文件清单/校验和），
已有数据后间隔内无新数据记 warning，不能只留一条开始日志；停止心跳不得等待完整目录遍历。
Rsync SSH 传输须设置连接超时和 keepalive，静默断开的连接应在分钟级失败并保留诊断，不能挂到任务超时。

## 旧版 Rclone 可变代次

Legacy Rclone 没有 immutable snapshot，也不使用 Rsync manifest。Prepare/前置条件完成后，在调用变更 executor 前才持久化 `writing`；明确失败成为 `dirty`，模糊完成、crash 或未知停止保留 `unknown` hold，完整成功才成为当前 `verified` head，仍不代表历史对象版本。

只有明确未启动证据能写 `no_start`：含 Prepare 失败/取消及可证明的 dial/lookup/start 前失败，也含同 owner 已 running 但未调用 Provider 的证明。不能根据 canceled 状态或错误文字回填；restore 只跳过可证 no_start，不跨越模糊历史 head。SSH 取消请求停止、必要时宽限后关闭所属 transport 并等待，但连接关闭/lease 到期不是远端 writer 已停止证明。

未决写按不可变 resource identity 划定冲突域；共享 Remote/namespace/node 证据的其他 Task 也阻断，独立资源可运行。缺可证 resource key 的历史保留节点/资源不确定 hold，不从后来配置或标签补猜；改配置或另建 Task 不能绕过。

管理员 `POST /tasks/:id/reconcile-legacy-rclone` 仅记录操作员确认，不负责远端探测/停止。必须有 task-write 权限、暂停任务、精确 `task_run_id`、`remote_stopped=true` 和 1–1024 字符安全 reason，实际确认原/外部 writer 停止并保留独立副本。事务拒绝 live/unbounded owner、active sibling、不匹配或非 eligible writing/unknown；放弃的 active writing 还要求明确 expired lease。按 Task→TaskRun 锁定，fence 旧 owner，必要时结为失败，改选中代次为 dirty 并原子保存 actor/confirmation 审计。审计失败回滚，原诊断保留；不自动恢复、重试、选旧 success 或标 verified。逐条清除 hold 后显式重新运行完整备份才可建立当前恢复来源。

历史 cleanup 在同一事务锁 Task 并重核谓词，保留每任务/节点最新非空代次（含 dirty）、所有未决 writing/unknown、restore source binding 和 active drill source；活动 successor 不能淘汰前一 final generation，避免 no-start 清除临时 dirty 后丢失前代。预约在同锁下重新解析来源；不能清除较新 dirty 令旧 success 复活。仅无引用且被替代历史按 retention 清理。

## 任务历史动态保留

任务执行与流量历史的清理期限通过同一 Settings 服务每轮动态解析，整轮固定 UTC cutoff，沿用一小时成功清理节流。读取失败或非法值时不删除、不推进成功清理时间，下一次既有调度可重试。任务执行记录的清理继续保留活动记录、未决 effect、代次、恢复与演练引用保护；流量样本沿用自身的分批删除条件，不共享任务执行记录的代次锁。默认值、范围、`0` 禁用及 DB → 环境 → 默认优先级见[环境变量](../../env-vars.md#数据保留)。
只要 `RecoveryPoint.producing_task_run_id` 持有某个 TaskRun 的引用，该 TaskRun 就优先于普通任务历史 retention 保留：自动清理不得删除该 TaskRun，也不得删除其 TaskLog 或解除其 Alert 关联。保护不按 RecoveryPoint 的 semantics、state、retention_until 或 hold_state 缩小；没有 producer-run 引用（包括 `NULL`）的 RecoveryPoint 不保护任何 TaskRun。无引用历史继续按上述普通规则清理。

## 恢复演练准入与证据

**当前生产演练传输未启用。** `POST /policies/:id/drill-trigger` 返回 unavailable，不创建演练 TaskRun、不启动计划演练；UI 不提供手动触发/启用执行入口。以下是现有存储/准入/证据和未来启用仍须满足的合同，不代表生产执行已验收。

演练 reservation 对 source/sandbox ID 排序去重，同事务锁全部节点边界、检查两个 Recovery lease 并建立 TaskRun/Evidence。执行入口从 immutable TaskRun 获取 source，锁相同节点集、复查 lease，并原子 pending→running 与 evidence start。Recovery 准入拒绝任一端上的 active drill；分裂 pair 保持关闭直到持久调和。source=sandbox 只锁一次；无 admission 实现/节点边界即 unavailable，即使 transport 存在也不例外。

`restore_drill_evidences` 每 TaskRun 唯一，身份含 policy/task/run、可选 source run/snapshot ref、sandbox node/name/path。保存 top-level status、failed step、confidence eligible、start/finish/duration，以及 restore、verify、post-verify、cleanup 的独立状态、时间及脱敏错误。policy `latest_drill` 仅扫描摘要，TaskRun detail `drill_evidence` 返回结构化详情；非 drill 或历史无记录返回 null/省略，不能 500。

只有完整成功且 cleanup 成功或跳过可 `confidence_eligible=true`；failed/canceled/pending、post-verify/cleanup 失败及异常终止均 false。危险 restore path `/`、`/etc`、`/usr`、`/bin`、`/sbin`、`/boot`、`/dev`、`/proc`、`/sys`、`/run`、`/var/run` 及其子路径在 restore/cleanup 前拒绝；记录对应 `restore_path/cleanup_boundary`。restore、pre_verify/verify、post_verify、cleanup 失败保留独立 failed step，不伪装成功。

operator 触发和读证据须同时拥有 source task node 与 sandbox，共享 policy 不授权未拥有 source。`allowedSourceNodeIDs=nil` 仅表示内部/admin/cron 不限；非 nil 空数组表示无授权源且失败关闭。只拥有一端不得泄露另一端 TaskRun ID/证据。

证据写入和响应都脱敏，包括 legacy task/task-run log、detail、WebSocket backfill；命令 helper 对外包装错误前隐藏非空 stdout/stderr。前端映射 `latestDrill/drillEvidence`，保留 trigger type `drill`，详情必须加载 detail endpoint，不拿列表缺字段宣称无证据；缺字段安全回退，phase/i18n 同步。

配对 000074 保证同 task 最多一条 `pending|running|retrying` Drill；历史重复在 dirty 前原子拒绝，TaskRun/Evidence 成对核实修复后可重试，used-down 不可删证据。回归须覆盖两端权限、危险路径、全部 phase 失败、无 evidence、真实详情加载；双引擎 reservation/start 与 Recovery 两顺序竞态要求持锁的真实阻塞证据：SQLite BUSY/LOCKED，PostgreSQL Lock wait 且 `pg_blocking_pids` 含精确 holder。共享 start channel/预置 lease 不代替并发证据；预约后出现的 out-of-band lease 与正常 Recovery 准入分开测试。

## 完成事实、RPO 与回归

freshness 只来自可证 legacy transfer completion 或已提交、严格 Task/TaskRun/node lineage 和 Provider evidence 的 managed RecoveryPoint。普通 command/维护成功、pending/warning/失败、restore/drill、imported baseline 及不可证明历史不建立 verified backup completion。`000087` 清旧 `Node.last_backup_at` 投影后只从可证 committed point 重建，未知历史保留 unverified 引用，不猜测/丢弃；node health 按 `(completed_at,id)` 在 DB 选择每授权节点最新 verified completion，不删 immutable history。

SLA 报告的 RPO：每个设置正数目标的策略，合并其关联任务最近 20 条 verified completion，按 `completed_at` 排序，取相邻最大间隔的整数分钟；不足两条为未知，不可当零且该目标不达标。RTO：该策略最新成功 `trigger_type=restore` 的 `duration_ms/60000` 整数分钟。报告 actual 取有关策略最差值，全部有目标策略都 `actual<=target` 才达标，无任何目标返回 null。当前实现先用报告节点作用域选策略，再读该策略所有任务；这两项不按报告 period 起止时间筛选，也不是距现在的 freshness。这些事实来自 `reporting/generator.go`，不能再描述为 TaskRun started_at 的间隔。

SLA 历史报告展开后在失败排行之前显示实际 RPO/RTO（分钟）与达标状态，并说明上述统计口径。实际值为零时显示 `0`，缺失时显示“未知”；有实际值且 compliance 为 true 才显示“达标”，false 显示“未达标”，缺失 compliance 显示“未评估”。实际值缺失且 compliance 为 false 时附“缺少足够证据，未判定达标”；实际值缺失但 compliance 为 true 的不完整响应只能显示“未知／未评估”，不得显示通过状态。历史 null 不等同于“未配置目标”，页面不补零、不额外计算或请求另一套指标。

升级前备份数据库、密钥和独立备份树，停止并排空旧 Core/executor/publication/reconciliation worker；同 migration version 也不能证明旧 writer 可理解新证据。used schema 禁止以删除事实、强制版本或旧 binary 降级绕过 hold。后端 focused 回归覆盖 CAS/override/显式零值/导入补偿、cron 去重/跳过/重启/配额/链式 effect、immutable snapshot、旧版 capture/byte path/staging、Rclone no-start/未知/跨任务 hold/原子 reconcile、cleanup 来源引用、完成事实与 RPO；涉及持久化竞争须双引擎与 race，前端覆盖配置冲突、安全 DTO 和证据 detail。实现验收、CI 与真实部署恢复各自举证。

### 报表调度预估

`GET /api/v1/report-configs/schedule-preview?cron=…` 继承主会话认证、API 限流及
`reports:write`（当前仅 admin）权限。它只读取表达式和服务端当前时间，不查询或写入
报告配置、报告、审计、proof/grant，不生成或投递报告。唯一 `cron` 参数须恰好出现
一次、原始解码值不超过 512 字节、trim 后非空且为五段；未知、重复或非法 query 返回
安全 400。合法五段但不可匹配返回 200 `no_match`，不增加配置保存校验。
取消/超时在 handler 返回安全 503，内部失败返回通用 500；handler 响应均 `no-store`，
认证、权限及限流前置拒绝沿用既有中间件合同。

报表保留每分钟 ticker 的服务端本地时区与现有简化 matcher，而不是任务/策略的标准
Cron：月份必须是字面 `*`；其余字段为 `*` 或现有 `fmt.Sscanf("%d")` 数字前缀，
日/周同时限制按 AND。步长不按标准 Cron 解释，数字开头的列表等仍保持历史部分 token
语义。预估不修改 matcher、ticker、编辑预设或保存行为。

成功信封中的 `evaluated_at/expires_at/timezone/search_until` 给出评估时间、当前分钟
结束的失效边界、服务端 Location 标识和五年搜索上界（不含）。`match` 的空 reason
配合 `[window_start,window_end)` 一分钟窗口；当前分钟匹配时返回当前窗口，否则返回
最早未来窗口。`no_match` 的两个窗口均 null，原因仅为 `unsupported_month`、
`unmatchable_field`、`horizon_exhausted`。所有时间保留服务端 RFC3339 偏移；
Location 为 `Local` 时不得推断 IANA 时区。窗口不代表 ticker 精确秒相位、启用状态、
执行次数或生成/投递保证。

前端只在显式点击后请求，单飞且不自动重试；404 显示服务端不支持，不使用浏览器
估算回退。响应整体验证后保留原时间字符串，语言切换仅重译。有效期使用
`expires_at-evaluated_at` 扣除整次请求 RTT 后的单调时钟，不由浏览器绝对时间决定。
到期、隐藏或失焦立即清除窗口；表达式、配置、开关状态、令牌、角色、会话代次或
认证过渡变化使旧结果失效并取消请求，迟到响应不得污染新会话。恢复可见、聚焦或
认证过渡结束不自动重发；预估成功与否不阻止编辑或保存。
