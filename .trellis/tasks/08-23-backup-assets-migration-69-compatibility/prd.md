# 迁移 69 孤儿运行兼容与脏状态失效保护

## Goal

让从备份资产 schema 之前版本升级的现有安装可以安全保留历史
`task_runs`，同时保证任何迁移 dirty 或“版本已前进但 schema 不完整”的状态都
拒绝启动。修复完成后，受支持的节点删除流程不再使迁移 69 失败，也不存在通过
环境变量自动把失败迁移标成 clean 的路径。

本任务是 Child 18 生产验收 No-Go 的 P0 修复，不代表备份资产功能验收通过。
修复发布后仍需回到 Child 18 重新执行绑定新版本和新镜像摘要的生产升级验收。

## Background

- v0.50.2 的迁移 69 假定每条旧 `task_runs.task_id` 都还能连接到当前
  `tasks`。受支持的节点删除流程会删除节点、任务和告警，但有意保留历史运行记录。
- 生产升级因此在 848 条终态历史运行上无法回填 `node_id_snapshot`，迁移 69
  正确地报错，却留下 dirty 版本。
- `ALLOW_DIRTY_STARTUP=true` 随后绕过 dirty 检查，调用 `migrate.Force` 将失败的
  69 标成 clean，再执行 70/71。最终数据库显示 `71|0`，但迁移 69 的恢复表并未
  存在。版本号不再能证明 schema 完整。
- 生产环境已从校验通过的迁移前备份恢复到 v0.44.8 / schema 61；失败数据库及
  WAL/SHM 已作为取证材料保留。本任务不得要求删除或覆盖这些材料。

详细、去标识化证据见 `research/root-cause.md`。

## Locked decisions

1. 历史运行记录必须保留；不得为了通过迁移而删除孤儿 `task_runs`。
2. 不从时间、流量、告警或其他旁表猜测已经删除的节点身份。
3. 对迁移前已经终态且无法回填的历史运行，`node_id_snapshot=0` 是唯一的
   `legacy_unknown` 哨兵；它不是节点 ID，也不得参与授权、恢复准入或节点互斥。
4. 新建运行和仍活跃的旧运行必须绑定一个正数、且与当前 Task 一致的节点快照。
5. 迁移 69 已公开发布，但必须兼容尚未越过 69 的安装；同时增加新的成对前向迁移
   以把已经成功越过 69 的安装收敛到同一最终约束，不能长期维护两套 schema。
6. 服务启动路径永久 fail closed：即使设置 `ALLOW_DIRTY_STARTUP=true`，也不得
   自动 `Force`、重试或继续迁移。
7. 对已经伪装成 clean 的不完整 69/70/71 schema，只诊断并拒绝启动；不在启动时
   猜测或自动重建。恢复校验备份或执行另行审核的离线修复才是允许路径。

## Requirements

### R1 — 迁移前历史兼容

- 修改成对的 000069 SQL，使现存 Task 的运行按 Task 当前 `node_id` 回填。
- 没有现存 Task 的运行只有在状态属于
  `success|failed|canceled|warning|skipped` 时才可写入 `legacy_unknown=0`。
- 没有现存 Task 的 `pending|running|retrying` 运行、未知状态运行、或现存 Task 的
  `node_id<=0` 必须使迁移事务失败；不得把活跃运行降级成历史未知。
- 回填必须保持 `task_runs` 行数、主键、任务 ID、状态、时间、错误和统计字段不变。
- 迁移日志和错误只报告聚合计数/版本/稳定原因，不输出 Task 名、Node 名、主机、
  命令、错误正文或其他生产证据。

### R2 — 新写入与历史哨兵隔离

- 000069 后的所有新 `TaskRun` 仍必须拥有正数 `node_id_snapshot`，并在创建时与
  Task 的节点一致；GORM hook 和两种数据库的触发器共同防御。
- `task_id` 与 `node_id_snapshot` 创建后不可变。
- `node_id_snapshot=0` 只允许迁移生成的终态旧行；它不得被改回活跃状态，也不得
  被普通 INSERT/UPDATE 创建。
- 所有恢复准入、节点写互斥和活跃运行查询必须显式只承认正数节点快照。零值只能
  表示“历史节点未知”，不能匹配任何真实节点。
- `TaskRun.NodeIDSnapshot` 继续保持内部字段（`json:"-"`）；本任务不新增公开 API
  字段，也不泄漏历史节点推断。

### R3 — 已发布 schema 的前向收敛

- 新增成对的下一版本迁移（规划名 `000072_task_run_snapshot_compatibility`），使：
  - 从 61/68 升级、会执行修订后 69 的安装；
  - 已用原 69 成功升级到 69–71 的安装；
  最终拥有相同的索引、约束、触发器和版本事实。
- PostgreSQL 必须规范化原来的 positive CHECK 与新的 nonnegative +
  legacy-terminal 约束；SQLite 必须用等价触发器实现相同写入合同。
- 000072 down 只有在不存在 `legacy_unknown=0` 行时才可恢复旧正数约束；存在哨兵
  时必须在 schema 和版本变化前 fail closed，并保持当前 clean 版本。
- 更新迁移版本常量、paired-file 检查、真实 PostgreSQL selector、文档和 Trellis
  database spec，不能只加 SQLite 文件。

### R4 — dirty 永久拒绝启动

- 删除 `RunMigrations` 中由 `ALLOW_DIRTY_STARTUP` 控制的检查绕过、自动
  `migrate.Force` 和自动重试。
- `schema_migrations.dirty=1` 在 SQLite/PostgreSQL、变量未设/false/true/1 的
  所有情况下都返回 `ErrMigrationDirty`，且版本行和 schema 不变。
- 启动错误给出安全、可执行的恢复方向：停止服务、保存数据库/WAL、校验备份、
  对照失败版本确认 schema；不得再建议设置 escape hatch。
- 从 `.env` 示例和当前部署/环境变量文档移除该变量作为受支持恢复手段。历史变更
  记录不重写。

### R5 — clean-version/schema-drift 启动防线

- 在执行新迁移前，对记录版本 `>=69` 的数据库验证最小迁移 69 结构合同，至少包含
  `task_runs.node_id_snapshot`、核心恢复表、关键索引/触发器。
- 若版本为 clean 但合同缺失，返回独立、可识别的 schema-drift 错误；不得执行
  000072、不得修改版本、不得自动建表。
- 检查必须兼容 SQLite/PostgreSQL schema/search_path 规则，并有真实 PostgreSQL
  测试。错误信息不能输出 SQL、DSN 或数据内容。

### R6 — 节点删除回归合同

- 为单节点和批量节点删除增加回归测试，证明 Task 被删除而历史 TaskRun 被保留。
- 对迁移前删除留下的终态运行，升级成功且快照为 `legacy_unknown`。
- 对迁移后删除，原有正数快照保持不变；删除不得把历史行改写成 0。
- 本任务不改变产品的节点删除 API 语义，也不恢复已删除的 Task/Node。

### R7 — 发布与重新验收边界

- CodeDefault 和 `.env.deploy` 中 `BACKUP_ASSETS_ENABLED` 保持 false。
- 不发布 Worker，不启用备份资产，不触碰生产取证文件。
- 修复合并、CI、Release、Docker 多架构镜像完成后，Child 18 才能绑定新版本重新
  开始生产验收；v0.50.2 永久保持 No-Go 记录。

## Out of scope

- 自动修复任意 dirty migration 或任意 schema 漂移。
- 从旁表、日志或人工输入重建已删除 Node/Task 身份。
- 删除/压缩历史 TaskRun，或改变运行历史保留策略。
- 启用备份资产、发布 Worker、父任务最终归档。
- 修复节点日志采集卡死；该问题由独立任务
  `08-23-node-logs-collector-stall` 负责。

## Acceptance Criteria

- [ ] AC1：SQLite 从迁移前版本升级时，现存 Task 运行得到原节点正数快照；终态孤儿
  运行得到 0，所有历史行和业务字段保持不变，最终版本 clean。
- [ ] AC2：真实 PostgreSQL 运行同一合同并得到相同数据、约束、触发器与结果；缺少
  `TEST_POSTGRES_DSN` 在 required 模式下失败而不是 skip。
- [ ] AC3：孤儿 `pending|running|retrying`、未知状态、或非正数 Task 节点均使迁移
  原子失败，未留下部分列/表/触发器。
- [ ] AC4：普通 SQL/GORM 不能创建 0/NULL/错节点快照，不能改写 Task/节点快照，
  不能把 legacy_unknown 终态行改回活跃状态。
- [ ] AC5：迁移 69 前路径和原 69 已成功路径经过 000072 后，schema 定义比较一致。
- [ ] AC6：含 legacy_unknown 行的 000072 down 原子拒绝并保持当前 clean 版本；无哨兵
  的 pristine down 可回到上一 clean 版本。
- [ ] AC7：dirty 版本在 `ALLOW_DIRTY_STARTUP` 任意值下都返回
  `ErrMigrationDirty`；spy/source test 证明启动路径没有调用 `Force`。
- [ ] AC8：clean 71 但缺少迁移 69 关键结构的 fixture 在任何前向写入前返回
  schema-drift 错误，版本/schema/数据快照不变。
- [ ] AC9：单删/批删回归测试证明历史运行保留；迁移前删除可升级，迁移后删除保留
  原正数快照。
- [ ] AC10：迁移检查脚本、focused SQLite、required PostgreSQL、全 backend test、
  race、lint、build、文档 freshness、`git diff --check` 全绿。
- [ ] AC11：部署与环境变量文档不再把 `ALLOW_DIRTY_STARTUP` 作为支持手段；错误文案
  不再引导自动 force。
- [ ] AC12：修复发布证据完整，但 Child 18 仍保持 No-Go，直到新镜像真实生产重验；
  父任务继续 planning。

## User action after implementation

实现与发布完成后，用户需要做的生产动作会在 Child 18 单独列出。当前规划阶段不需要
改生产配置，也不要重新开启备份资产。
