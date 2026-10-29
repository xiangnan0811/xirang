# 数据库合同

## 查询、模型与事务

后端使用 GORM，默认 SQLite，支持 PostgreSQL。连接及池配置在 `backend/internal/database/database.go`，SQL 迁移由 `migrator.go` 内嵌并通过 golang-migrate 执行。模型归 `internal/model/`；敏感字段的持久化与脱敏约束见[凭据与访问合同](../domains/credentials-access.md)。

- 查询显式检查错误。有请求或服务 context 时使用 `WithContext(ctx)`；不要新增忽略 context 的服务查询。
- 根据响应图有意选择 `Preload`，不要无条件加载完整模型关系。未找到与数据库故障分开处理，HTTP 映射见[错误处理](error-handling.md)。
- 多行、多表、状态转换及其必要审计在同一事务内提交。锁顺序保持确定，跨实例授权以数据库事实为准，进程锁不能替代数据库锁。
- 空结果属于正常情况的查找可用 `Limit(1).Find`，例如设置解析，避免不必要的 record-not-found 日志。
- 表、列采用 snake_case，历史拼写必须显式映射，例如 `Policy.BwLimit` 对应 `gorm:"column:bwlimit"`，不能漂移成 `bw_limit`。索引名应说明表和作用。
- JSON 字段也是 API 合同；改名时同步前端边界 mapper。不能直接输出含秘密的模型或在 Handler 手工加解密。

## 流量查询索引

Overview 流量窗口查询依赖 `task_runs` 上的两个物理索引：`idx_task_runs_started_at(started_at)` 与 `idx_task_runs_status_finished_at(status, finished_at)`。修改时间窗口谓词、状态过滤或索引名称时，必须保留相应查询的索引支持，并同步 SQLite/PostgreSQL 定义、模型 tags 和配对 down 迁移；不能因查询有行数上限而忽略全表扫描和临时排序。当前定义见双引擎 `000061_task_runs_traffic_indexes` 与 `model/task.go`，回归需校验两侧索引及匹配的删除定义。

## 迁移配对与启动

每次 schema 变更必须同时提供 `migrations/sqlite/<version>_<name>.up.sql`、`.down.sql` 和 PostgreSQL 对应文件。数字版本递增，双引擎版本、字段语义、约束、索引及降级保护一致。唯一受脚本校验的最新版本声明在[后端 README](../../../backend/README.md)，其他文档不重复维护“最新迁移号”。

生产 schema 使用版本化 SQL，不以 `AutoMigrate` 替代。迁移考虑已有安装，按引擎能力使用 `IF EXISTS` / `IF NOT EXISTS` 并说明历史 drift 修复目的，但这些语法不能掩盖缺失的安全约束。仅在无法安全使用 SQL 时增加 Go 前置修复。SQLite 迁移事务由驱动管理，不在脚本中重复嵌套 `BEGIN`。

`RunMigrations` 在任何前置修复之前拒绝 dirty version，返回 `ErrMigrationDirty`；无论旧环境变量如何设置，都不能调用 `Force`、自动清理元数据或越过 dirty 重试。首次检查之后发生的 dirty 也必须在 `Up` 处拒绝。

干净版本先执行适用版本的最小 schema 验证，再进行前置检查/修复和 `Up`，随后再次验证。必须检查列、约束、索引、触发器/函数的真实语义；同名空操作对象是 `ErrMigrationSchemaDrift`，不是写入许可。Recovery、TaskRun、grant、drill 以及后续领域版本的具体检查和升级数据条件见[领域合同](../domains/README.md)。

离线 `recover-account` 使用只读 `VerifyCurrentSchema`：要求迁移元数据恰有一条、
clean 且等于当前内嵌迁移头，再验证适用 schema 与降级保护；不执行任何迁移或修复。
其 `OpenQuiet` 在建立 GORM 连接之前启用 discard logger，避免初始化或失败 SQL
泄露 DSN、密文或凭据；普通服务 `Open` 的日志行为不变。

不能安全表示的旧数据应在写 dirty 元数据前返回 `ErrMigrationPrecondition`；离线核对真实事实后允许重试。升级前备份数据库、密钥和备份数据，并按领域合同停止、排空旧写入者。不能猜测历史身份、补造证据或混跑不兼容的旧写入进程。

## 已使用 schema 的降级保护

有效数据保护要求可以规定“只允许未使用 schema 降级”，不要求破坏性可逆。永久安全标记、幂等凭据、账本或领域证据已使用时，保留状态并前向修复。

golang-migrate 在运行 down SQL 前先调用 `SetVersion(target, true)`。因此仅在 down 文件中拒绝操作不足以保留原来的干净版本：需要双引擎 `schema_migrations` 写入准入触发器，在目标版本低于保护版本且完整 used-state 条件成立时拒绝元数据更新。SQLite 的 DELETE+INSERT、PostgreSQL 的 TRUNCATE+INSERT 位于驱动事务中；拒绝 INSERT 后必须恢复原干净版本。

保护器允许向前迁移以及从更新版本退到保护版本。未使用的 down 在删除保护表前移除准入触发器；down SQL 自身仍保留独立保护。生产路径必须真正写入 guard 所依赖的使用标记，不能只在测试中手工种入。

回归须通过真实 `migrator.Steps(-1)`，不能只 `db.Exec` down SQL：

| 情形 | 要求 |
| --- | --- |
| 已使用状态、永久标记或明细被清空但使用证明仍在 | 拒绝；版本保持 clean，表、定义、索引、触发器、行内容不变 |
| 未使用 schema | 降级成功，前一版本 clean，保护器及对应 schema 正确移除 |
| 后续向前迁移或退至保护版本 | 元数据准入不误拦截 |

### 不可逆退役迁移

退役迁移必须按各自独立边界删除数据：已发布的 `backup_focus_retirement` 只处理列明的节点采样/系统日志/看板表、专用列、forecast 字段和专用 Settings，不删除服务监控表或封存服务告警；后续 `service_monitor_retirement` 只删除 `service_uptime_samples`、`service_monitors`，并封存精确纯数字服务代码集合，不改写前一次迁移及其验收语义。备份资产及 Provider 元数据、任务/TaskRun 与任务日志、审计、snapshot-diff 事件和仍有效的告警投递事实必须保留。各迁移在删除自己的来源记录前分别物化告警 ID，再按[告警与健康合同](../domains/alerting-health.md#退役来源告警封存与投递围栏)的对应来源边界及共用围栏处理 Alert 和未发送 delivery；不能混合两个集合或宽匹配仍有效的到期、任务/恢复来源。服务来源不依赖存活监控项或节点归属。

此类迁移的 down 文件可以明确失败，不能伪造空历史或提供“自动回滚”。执行失败（包括 migrate 驱动在 down 前尝试写旧版本号）后，保护器必须让 `schema_migrations` 版本、dirty 状态、触发器/约束和业务数据保持新版本的 clean 状态。日常回退只能恢复升级前数据库备份，并使用匹配的旧版二进制、完整旧配置、`DATA_ENCRYPTION_KEY` 及适用的历史解密密钥；不得手工修改迁移元数据或删除保护器。删除边界和 SQLite/PostgreSQL 灾难恢复步骤见[备份、恢复与快照](../../admin/backup-recovery.md#升级与灾难恢复)。

启动时还必须验证退役版本保护器的真实语义：SQLite 的 INSERT、UPDATE 触发器，以及 PostgreSQL 的触发器附着、启用状态和函数体均须保留版本下限拒绝逻辑。缺失或同名空操作保护器返回 `ErrMigrationSchemaDrift`，不能因为版本已经 clean 就跳过，也不自动重建或绕过损坏的保护器。

## 时间与连接一致性

GORM `NowFunc` 使用 UTC。SQLite DSN 对每条物理连接固定 WAL、`_busy_timeout=5000`、外键、`_synchronous=NORMAL`、`_txlock=immediate` 和 `_loc=UTC`；用户传入别名不能绕过这些选项。时间迁移 SQL 的 UTC 安全另由仓库检查脚本验证。

PostgreSQL DSN 未指定 timezone 时补 UTC；显式时区保留，并加载为扫描时区。非法时区在建立 GORM 数据库前失败。每条 pgx 物理连接的 `AfterConnect` 同时注册 `TimestampCodec` 和 `TimestamptzCodec` 的 `ScanLocation`，只配置前者不足以避免 `TIMESTAMPTZ` 被扫描为 `time.Local`。GORM 必须使用 `openPostgresSQLDB` 返回的同一连接池。

在未限定 schema 的读取前，`schema_migrations` 存在性检查使用 `pg_catalog.to_regclass('schema_migrations')`，与后续查询遵循同一 `search_path`。不能先统计未限定范围的 `information_schema.tables` 再读取另一个 schema。

## 双引擎回归证据

迁移回归覆盖 apply、未使用 down、已使用 down 原子拒绝、模型/UTC、外键、CHECK、唯一约束、索引和启动 schema drift。真实 PostgreSQL 唯一库存为 `scripts/required-postgres-tests.json`；`bash scripts/run-required-postgres-tests.sh --list-only` 校验库存、active Go AST census 与原生顶层声明，不需 DSN。执行使用同一入口，可传 `--evidence-dir <目录>`，必须显式提供独占可销毁实例的 `TEST_POSTGRES_DSN`；旧 suite/package/regex 位置参数不再支持。

库存逐包声明精确顶层测试及必须执行的 PostgreSQL 根/静态叶路径，混合引擎不能仅凭父测试通过。工具清除继承 `GOFLAGS`，设置完整 required 环境开关，按包串行执行精确转义并锚定的选择。database 组默认30分钟，其他组20分钟；显式 `POSTGRES_TEST_TIMEOUT` 必须为正 duration 并覆盖全部组。非零进程退出、坏/超长JSON事件、错误包身份、缺 package pass、登记顶层或required路径缺run/pass、required根的任意已观察后代skip/fail均失败；其他optional skip单独报告。事件日志保留协议元数据、隐藏Output正文，stderr脱敏，逐组保存exit与计数，不能以包级ok代替闭包证据。

新增真实PG fixture遵守可静态发现的同包helper/函数值/闭包、DSN literal或engine循环约定；新动态间接机制须同时扩展census及负例，不得用exclusion逃避真实PG。具体SQLite-only或非PG误报可用源码理由逐项排除，不允许包/前缀豁免。`TestPostgresTimestamptzScanUsesConfiguredUTC` 需在非UTC进程TZ下校验Location与时间值；dirty/search_path回归须证明兄弟schema不干扰当前连接可见的dirty状态。

共享数据库的破坏性 PostgreSQL fixture 按包串行运行。跨引擎 fixture 绑定 Go `bool`，不用 SQLite 的 `0/1` SQL 字面量代替 PostgreSQL 布尔值。重复执行的共享内存 SQLite DSN 加每次打开的唯一序号，不能只依赖 `t.Name()`。需要行锁时锁定实际行，不能使用 PostgreSQL 不支持的 `COUNT(*) ... FOR UPDATE`。
