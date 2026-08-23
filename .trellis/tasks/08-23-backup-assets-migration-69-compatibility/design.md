# Design — 迁移 69 孤儿运行兼容与脏状态失效保护

## Boundaries

本任务修改数据库迁移、迁移启动保护、TaskRun/节点删除回归测试、部署文档和相关
Trellis spec。它不读取生产数据库，不实现生产数据脚本，不启用备份资产。

## Final data contract

`task_runs.node_id_snapshot` 使用闭合语义：

| Value | Meaning | Allowed rows | Authorization/admission |
|---|---|---|---|
| `>0` | 创建运行时冻结的真实 Node ID | 所有新运行；可回填的旧运行 | 可按现有合同参与，但仍需其他授权/状态检查 |
| `0` | `legacy_unknown`，迁移前 Task/Node 已删除 | 仅迁移生成的终态旧运行 | 永不参与节点准入、互斥或恢复授权 |
| `NULL` / `<0` | 无效 | 不允许 | fail closed |

终态集合固定为 `success|failed|canceled|warning|skipped`；活跃集合固定为
`pending|running|retrying`。迁移遇到集合之外的孤儿状态时拒绝，避免把未知状态误判
为安全历史。

选择 0 而不是 NULL，是为了保持现有 Go 模型的非指针 `uint` 和内部查询合同，并
避免每个 TaskRun 读取面新增 nullable 扫描分支。0 已经是系统中的无效 Node ID，
因此可以作为明确、不可能授权到真实节点的哨兵。

## Migration sequence

### Path A — installation below 69

1. 修订 000069 的回填表达式：有 Task 时复制正数 Node；无 Task 时按状态决定 0 或
   原子拒绝。
2. 69 只建立能让后续迁移安全执行的兼容结构；新 INSERT 仍拒绝 0/NULL/错节点。
3. 70、71 按原顺序执行。
4. 新 000072 把触发器/约束规范化为最终合同。
5. 记录 72 clean；随后运行结构后验检查。

### Path B — installation already clean at 69–71

1. 启动前检查 69 最小结构存在。
2. 执行 000072，替换旧 positive 约束/函数/触发器为最终合同。
3. 原安装不存在 0 行，因此只改变约束定义，不改变历史数据。

### Path C — dirty or falsely clean/incomplete

- dirty：在构造会执行 `Up` 的路径前返回 `ErrMigrationDirty`；无变量绕过。
- clean `>=69` 但最小结构缺失：返回 `ErrMigrationSchemaDrift`，版本保持 clean、无
  前向写入。运营只能恢复校验备份或采用另行审核的离线修复。

## Paired SQL design

### SQLite

- 000069 `UPDATE` 用相关子查询回填；孤儿终态写 0。
- TEMP guard 同时拒绝：NULL、负数、现存 Task 节点非正数、孤儿活跃/未知状态。
- INSERT trigger 始终要求正数并与 Task 当前节点一致，因此普通写入不能制造 0。
- immutable trigger 禁止改 `task_id` / `node_id_snapshot`。
- 000072 增加/规范化 status trigger：`OLD.node_id_snapshot=0` 时禁止把终态改成任何
  其他状态。
- 000072 down 的 metadata admission trigger 与 body guard 都检查 0 行，确保拒绝发生
  在版本和 schema 改变前。

SQLite 无需重建 4GB 级 `task_runs` 表；兼容性由 backfill guard + triggers 保证。

### PostgreSQL

- 000069 在一个显式事务内回填，先对所有不安全行执行 guard，再 `SET NOT NULL`。
- 为兼容路径建立 nonnegative 基线；普通 INSERT 仍由触发器要求 `>0` 且匹配 Task。
- 000072 `DROP CONSTRAINT IF EXISTS task_runs_node_id_snapshot_positive`，再建立一个
  规范命名的最终 CHECK：正数，或 0 且状态为闭合集合。
- 触发器函数同时处理 INSERT、身份字段 UPDATE 和 legacy_unknown status UPDATE。
- 000072 down 仅在没有 0 行时恢复原 positive CHECK；used-down admission 保留 clean
  版本。

## Startup validation

新增一个窄的、只读 schema contract validator，顺序固定：

1. 打开底层 DB 并只读 `schema_migrations`。
2. dirty → 在任何 pre-migration fixup 或 schema 写入前立即失败。
3. clean 且版本 `>=69` → 在任何 fixup/Up 前验证 69 最小结构。
4. clean 且结构合同完整（或版本 `<69`）→ 才允许执行历史 pre-migration fixup。
5. 构造 migrator 并 `Up`。
6. `Up` 后再验证当前版本最小结构，才记录“迁移完成”日志。

最小 69 结构清单应集中定义并由测试锁定，至少包括：

- `task_runs.node_id_snapshot`
- `idx_task_runs_node_snapshot_status`
- TaskRun insert/immutable guards
- 迁移 69 核心恢复表（从现有 `backupAssetRecoveryTables` 权威清单派生）
- schema_migrations downgrade admission guard

validator 只判定存在性/闭合定义，不读取业务行内容，不自动建表。SQLite 使用
`sqlite_master`/`PRAGMA`；PostgreSQL 使用当前 search_path 可见的 catalog 查询。

## Dirty-state contract

`ALLOW_DIRTY_STARTUP` 从执行合同中删除。为防止旧部署文件仍设置该变量造成误解，
测试必须明确证明 `true`/`1` 仍拒绝 dirty。代码不保留“解析但暂时不用”的隐藏入口，
也不在服务进程中调用 `migrate.Force`。

运营文档区分两种情况：

- dirty：迁移事务可能未完成，先取证和确认失败版本。
- clean/schema-drift：版本元数据不可信，必须恢复备份或离线核验。

两者都不得以“把版本标 clean”作为通用修复。

## Query safety

实现阶段搜索所有 `node_id_snapshot` 消费点并建立以下规则：

- 活跃运行查询同时限定 status 与 `node_id_snapshot>0`，或按正数 node 参数匹配；
- 任何以快照授权/加锁的入口先拒绝 0；
- 历史列表可加载 0，但不得把它转换为某个当前节点；
- 字段继续 `json:"-"`，错误/日志只使用稳定原因代码。

## Test matrix

| Fixture | SQLite | real PostgreSQL | Expected |
|---|---:|---:|---|
| clean pre-69, live Task run | yes | yes | positive snapshot, 72 clean |
| clean pre-69, terminal orphan | yes | yes | snapshot 0, row preserved |
| mixed live + many terminal orphans | yes | yes | all preserved, deterministic counts |
| orphan active/unknown | yes | yes | atomic fail, no partial 69 |
| Task node nonpositive | yes | yes | atomic fail |
| original clean 71 schema | yes | yes | converge through 72 |
| clean 71 missing 69 objects | yes | yes | preflight schema-drift, no write |
| dirty 69, env unset/true/1 | yes | yes | ErrMigrationDirty, no Force |
| 72 down with sentinel | yes | yes | atomic refusal, 72 clean |
| 72 pristine down | yes | yes | 71 clean |

Node repository tests add single and batch delete coverage before/after snapshot introduction.

## Alternatives rejected

| Alternative | Rejection reason |
|---|---|
| Delete orphan TaskRuns | Destroys valid operational history and contradicts current deletion behavior |
| Infer Node from timestamps/traffic/alerts | Evidence can be absent or ambiguous; fabricated authority is unsafe |
| Recreate deleted Task/Node rows | Changes product state and ownership merely to satisfy a migration |
| Leave field NULL | Forces nullable model/API/query changes and makes authorization omissions easier |
| Only add migration 72 | Installations below 69 can never reach it because original 69 fails first |
| Only edit published 69 | Already-upgraded installations and fresh upgrades would keep divergent constraints |
| Keep escape hatch but add warning | Production incident proved warning is insufficient; Force can certify missing schema as clean |
| Auto-rebuild falsely clean schema | Startup lacks enough evidence to distinguish safe completion from partial application |

## Rollout and rollback

1. Deliver on a dedicated branch/PR with required real PostgreSQL evidence.
2. Publish a new stable semver image; never retag v0.50.2.
3. Child 18 binds the new git SHA/image digest and repeats backup, upgrade, health, schema and
   feature-disabled checks.
4. If production upgrade fails, stop, preserve DB/WAL/logs, and restore the verified pre-upgrade
   backup. Do not set `ALLOW_DIRTY_STARTUP`.
5. Only after the Core upgrade is clean may Child 18 proceed to inventory/enablement acceptance.
