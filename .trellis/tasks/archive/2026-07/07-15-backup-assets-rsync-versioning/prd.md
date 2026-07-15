# Rsync 版本化恢复点

## Goal

在已合并的备份资产领域、Provider 读取适配和通用 evidence publication seam 之上，为 Rsync 任务提供可预检、可回滚的版本化发布模式：每次可证明成功的运行发布一个全新、完整、不可变的目录树 RecoveryPoint，而不是继续覆盖同一个可变目标目录。

本任务必须诚实区分 Xirang 管理的不可变目录树与存储 WORM。优先使用空 staging tree + 前一 committed tree 的 `--link-dest` + 同文件系统原子 rename；硬链接能力或安全前置条件不成立时，只能显式使用完整新树模式，不能降低恢复点可信度或静默回退到 mutable mirror。

## Parent Contract

- Parent: `07-12-backup-data-explorer-design`.
- Child 1–3 已归档并合入 `main`；本任务基线为 `main@12791c9b3da9a9f041af375f3b23c11dd3e21afb`。
- 本任务对应父计划 Child 4 `backup-assets-rsync-versioning`，依赖 Child 1 的领域/lease 模型、Child 2 的 Rsync Repository read adapter，以及 Child 3 的 typed evidence executor、publication coordinator、worker、fencing/deadline、managed-history admission 与 legacy guard 边界。
- 用户于 2026-07-15 明确批准创建本 Child 并进入规划；该批准不等于批准 `task.py start` 或实施。

## Confirmed Facts

- 当前 Rsync 任务通常覆盖同一目标目录，只能表示 `mutable_head`，不能从 TaskRun 历史推断不可变恢复点。
- 领域枚举已经存在 `versioned_hardlink`、`versioned_full_copy`、`hardlink_tree`、`full_copy_tree` 和 `xirang_manifest` 映射；Child 4 需要实现真实 Provider publication，而不是再创建一套资产模型。
- 父设计已确定：新版本化运行从空 staging tree 开始；hardlink 模式只以同一仓库前一 committed tree 为 `--link-dest`；最终目录通过同挂载原子 rename 发布。
- `--inplace`、`--append` 及任何可能修改共享 inode/前一 committed tree 的参数必须在命令执行前失败关闭。
- `rsync -a` 不等同于 `-HAX`；实际 flags、文件系统能力和缺失元数据必须进入 typed fidelity evidence，不能宣称未验证的 ACL/xattr/hardlink 保真度。
- Provider 文件树提交、数据库 publication 和 TaskRun 结果是独立事实；Child 4 必须复用 Child 3 的异步 coordinator/worker 与 transaction/fence 约束。
- 用户于 2026-07-15 批准 schema 方案 A：Child 4 必须拥有 SQLite/PostgreSQL 配对的 `000064_backup_asset_rsync_publication_contract`；它建立可跨 repository/point/link 生命周期保留的通用 managed-history latch、回填既有 Restic managed history、为 managed tree 物理 source 增加唯一防线，并在 down 时对 managed history、versioned link 与活跃 publication/parent lease 失败关闭。父任务原 `000064…000070` reservation 必须整体顺延为 `000065…000071`。
- 用户于 2026-07-15 批准 legacy 接管语义：`imported_baseline` 只能用独立的 full-copy publication（全新 staging、manifest、验证和原子 rename）创建首个 managed point；禁止 metadata-only 登记、原地移动或与 legacy target 共享 inode。`first_new_point` 不导入旧树，只从下一次成功运行建立历史；legacy target 始终保留为 rollback locator。
- 用户于 2026-07-15 明确批准 focused design.md，并随后认可本 Child 的 `implement.md`。规划工件的审阅已完成；该认可仍不等于批准 `task.py start`、产品实现、迁移、提交、push 或 PR，必须另有明确启动授权。

## Requirements

### Versioned Publication

- 每个 producing TaskRun 使用稳定 opaque point identity、唯一 attempt staging/final path 和 Provider commit marker；自动重试不得复用可能污染的 staging。
- hardlink 模式必须先验证 staging/final/parent 位于同一文件系统、实际 hard-link probe 成功、link count/inode/容量满足边界，并在整个 copy/manifest/rename/cleanup 窗口持有前一 committed point 的 `rsync_parent` lease。
- full-copy 模式必须创建完整独立新树；hardlink 能力失败不得在同一 attempt 中静默改变已冻结的 publication mode。
- 新 staging 必须为空。源端删除的文件不得从 parent tree 泄漏到新点；前一 committed tree 在任何成功、失败、取消或 crash 路径中都不得被修改。
- Provider commit 必须使用同挂载原子 rename 和 versioned marker/manifest evidence；只有完整 canonical manifest 与最低验证通过后才允许 RecoveryPoint committed。

### Preflight And Migration

- 新任务只有在真实 preflight 成功后才推荐版本化模式；现有 legacy Rsync task 默认保持 `legacy_mutable`，不能被后台静默转换。
- preflight 必须覆盖 same-mount、hard-link、原子 rename、目标/源路径重叠、symlink/path escape、空 staging、空间/inode/link-count、权限和不兼容 flags。
- 迁移 dry-run 必须给出容量/inode 估算与 `imported_baseline | first_new_point` 两种明确选择；旧 target/locator 在显式生命周期接管前保留为 rollback locator。
- `imported_baseline` 必须作为独立的 `full_copy_tree` point 执行同一 Provider commit/manifest/fence/DB publication 合同；它不得读取 legacy history 的 mtime 进行猜测，不得在原 target 就地标记或共享 inode。`first_new_point` 必须保持 legacy tree 原样，只在下一次成功的 producing TaskRun 后创建第一个 managed point。
- 必须实施经批准的 paired `000064_backup_asset_rsync_publication_contract`，并同步更新父任务的 reservation：新增 durable managed-history latch、回填 Restic history、为 `xirang_manifest` 与物理已提交的 `imported_baseline` managed tree source 添加 `(repository_id, source_fingerprint)` partial unique 防线；down 必须在 latch、managed tree、versioned link、活跃 `point_publication` 或 `rsync_parent` lease 任一存在时拒绝且不改变 schema 或数据。
- downgrade/rollback 必须先暂停调度、关闭新 admission、排空 Provider command/lease，再 relink 保留的 legacy locator；不得删除已提交版本树来完成回滚。

### Crash Safety And Compatibility

- 覆盖 Provider tree 已原子发布但 DB 未记录、DB preparing 但 Provider commit marker 不存在、staging 遗留、旧 fence 迟到和 process restart 等窗口。
- reconciliation 只能依赖 exact point marker、Repository/link identity、commit digest 和 immutable deadline；不得按目录 mtime、最近目录或 TaskRun 结束时间猜测成功。
- publication failure 不得改写真实 TaskRun transfer 结果或错误进入 transfer retry；不确定 Provider outcome 必须延迟/隔离并失败关闭。
- pristine feature-disabled legacy path 保持兼容且无资产副作用；一旦产生 managed Rsync history，关闭 feature 仍保留防止 mutable overwrite、跨点读取和不受控 retention 的安全锁存。
- 本 Child 不删除 Provider tree，不实现通用 retention/purge，也不为 Catalog/search/content/UI 工作区开放公开资产能力。

### Security And Observability

- 复用 Child 2/3 的 typed command runner、access binding、secret handling、admission、audit、stable errors、resource limits 和 structured logging。
- 路径、文件名、locator、命令输出、凭据和原始错误不得进入日志、审计、metrics label、TaskRun safe summary 或公开 DTO。
- 所有 command/read handle 必须有界、可取消并在 lease/admission 释放前 close/join；任何 fence/renew loss 必须取消 Provider work 并拒绝迟到提交。

## Acceptance Criteria

- [ ] 完成基于最新 `main`、父规划和 Child 1–3 实现的 focused `design.md` 并经用户明确批准；仅此后才可调用 `writing-plans` 生成 `implement.md`，且 `implement.md` 仍须经单独审阅后才允许 `task.py start`。
- [ ] temp-filesystem/preflight fixtures 覆盖 same mount、真实 hard link、`EXDEV`/unsupported、atomic rename、空间/inode/link count、路径重叠、symlink escape、非空 staging 和 forbidden flags。
- [ ] hardlink publication 测试证明 changed file 独立、unchanged file 共享 inode、source deletion 不残留、parent tree 永不变；full-copy 模式证明没有跨点共享 inode。
- [ ] exact marker/manifest、atomic rename、Provider/DB 双写 crash、stale fence、restart reconciliation、deadline、lease renew loss/cancel/join 和 staging cleanup 测试通过。
- [ ] Task/API/UI mapping 明确区分 legacy/hardlink/full-copy，未知 mode fail closed，migration wizard 提供 baseline/first-new-point 与 rollback 语义。
- [ ] pristine disabled compatibility 与 post-publication safety latch 均有回归；任何回滚、失败或 down path 都不删除 committed tree。
- [ ] `000064` 在 SQLite/PostgreSQL 双引擎完成 apply/down：回填现有 Restic managed history、拒绝 managed Rsync history/活跃 parent lease 的 down、保持 UTC/迁移 parity，并将后续 parent reservation 一致顺延到 `000065…000071`。
- [ ] focused backend/frontend tests、race suites、双数据库相关回归、`make check`、doc freshness、security/dependency scans 与 `git diff --check` 通过。
- [ ] 正确完成同一分支流程：实现/验证 → Phase 3.4 工作提交 → `trellis-finish-work` 归档+journal 自动提交 → push/PR → required CI/merge → post-merge 监控 → main 同步。

## Out Of Scope

- Rclone 版本化、Catalog/search/content plane、预览/下载、Workspace UI、Worker、导出、受控恢复、retention/purge/GA。
- 将任意现有目录历史猜测成多个 RecoveryPoint，或自动把 mutable target 宣称为 immutable history。
- Provider tree 物理删除、仓库修复、跨文件系统非原子发布、未验证的 reflink/dedup/WORM 声明。

## Open Decisions

- 需要通过 focused 代码与文件系统语义研究确定 point marker/commit manifest 格式、staging/final 命名、hardlink/full-copy preflight 精确失败分类、shared coordinator 扩展方式和 crash reconciliation 线性化点。
- Schema 与 reservation 决策已解决：采用批准的 `000064` 方案 A；如果研究发现还需要扩大其用户可见迁移语义、允许新的 Provider mutation 或削弱既有 safety latch，必须暂停并一次只提交一个产品决策给用户。
- Legacy 接管决策已解决：`imported_baseline` 是物理 full-copy 发布，`first_new_point` 不追认旧 tree；若研究发现必须改变这两个路径的用户可见语义，必须暂停并提交单一新决策。

## Notes

- 本任务是复杂的跨 Provider/task/API/frontend/文件系统 publication 变更，必须具备 `prd.md`、`design.md`、`implement.md`，不能按 lightweight task 启动。
- 当前仍处于 planning；没有产品代码、migration、commit、push 或 PR。只有明确授权运行 `task.py start` 后才能进入实施。
