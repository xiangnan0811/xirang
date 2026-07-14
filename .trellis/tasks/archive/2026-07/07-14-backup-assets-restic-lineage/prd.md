# Restic 精确血缘与恢复点发布

## Goal

在 Child 1 的备份资产领域模型和 Child 2 的 Repository/Restic 只读访问边界之上，把一次成功的 Restic 备份运行与它实际创建的完整原生 snapshot ID 建立不可歧义、可审计、可重试的血缘关系，并且仅在精确快照清单与最低验证完成后发布可信 `RecoveryPoint`。

本任务要消除“任务执行成功但不知道究竟产生了哪个快照”以及“共享仓库中的其他快照被错误归属给当前 Task”的风险，为后续 Rsync/Rclone 发布、Catalog、预览、恢复和生命周期治理建立通用 evidence publication seam。

## User Value

- 用户以后看到的每个 Restic 恢复点都能追溯到唯一的 Task、TaskRun 和原生 snapshot，而不是由 `latest`、时间窗口或仓库全量扫描推测。
- 传输成功与资产发布成功会被诚实地区分；清单缺失、血缘不明或验证失败不会被包装成可浏览、可恢复的可信点。
- 多个任务共用同一 Restic 仓库时，任何任务都不会横向继承其他任务创建或导入的快照。
- 进程崩溃或数据库提交中断后，可以根据唯一运行证据安全对账，而不是重复发布、错误认领或删除 Provider 数据。

## Parent Contract

- 父任务：`07-12-backup-data-explorer-design`。
- 已满足依赖：Child 1 `backup-assets-domain-foundation` 与 Child 2 `backup-assets-provider-readers` 均已归档并合入 `main`。
- 当前基线：`main@e1a8f24c3c8b8b71581cedc148c5f32482c8ac0b`，即 PR #385 的 squash merge 结果。
- 规范来源：父任务 `prd.md`、`design.md`，以及 `implement.md` 的 Execution Contract、Child 3、Requirement Coverage Matrix、High-Risk Review Gates 和 Program Rollback Strategy。
- 本任务属于复杂的后端可信发布链变更；进入实施前必须完成并复核 focused `design.md` 与 `implement.md`，再由用户明确批准 `task.py start`。

## Confirmed Facts And Decisions

- 父方案已经确定 Restic 使用原生 snapshot 作为 `native_snapshot` RecoveryPoint；不得使用 `latest`、短 ID、时间邻近或“仓库中最新出现的快照”推断运行产物。
- 父方案已经确定分离的 `ProviderCommitEvidence` / `EvidenceExecutor` / publication coordinator 方向；Child 3 是该 seam 的首个生产实现，Child 4–5 将复用它发布 Rsync/Rclone 恢复点。
- 不可变 RecoveryPoint 只有在 Provider commit、精确 manifest 和最低验证一致后才能从 `preparing → verifying → committed`；Catalog 属于后续 Child 6，不能替代 manifest 或最低验证。
- TaskRun 的传输结果与 RecoveryPoint 发布结果是两个独立事实。Restic 命令退出成功但缺少精确 native ID 时，运行证据必须显示“传输成功、资产发布未提交”。
- 共享 Repository 不代表共享 Task lineage。只有带有本次 Xirang Task/Run 唯一证据的 snapshot 才能归属该运行。
- Child 2 已提供完整 Restic snapshot ID 的只读 list/ls/dump、Repository 原生身份、加密 access binding、受限 command runner 和 typed errors；Child 3 必须复用这些边界。
- `backup_assets.enabled` 在本任务中继续默认 `false`；不增加公开前端入口或通用资产浏览 API。
- 用户于 2026-07-14 批准创建本 Child 并进入规划阶段；该批准不等于批准实施。
- 用户于 2026-07-14 批准 schema 方案 A：Child 3 新增 paired `000063_backup_asset_publication_contract`，修正 Restic publication mode、增加 publication lease holder 与数据库唯一防线，并将父计划后续 migration reservation 整体顺延一位。
- 用户于 2026-07-14 明确批准 Child 3 完整设计，并确认除此之外没有异议；该 review gate 只批准 `design.md`，实施仍须在 focused `implement.md` 经审阅后获得单独的 `task.py start` 授权。
- 用户随后于 2026-07-14 明确批准 focused `implement.md` 并要求启动 Child 3；`task.py start` 已完成，任务进入 `in_progress`。该实施授权不改变已批准的产品、Schema 或安全边界。

## Requirements

### Exact Run Attribution

- 每次 Restic 备份执行必须使用不可混淆的 Xirang Task/TaskRun 唯一标记，使同仓库并发、重试、手工快照和其他 Xirang Task 的快照可以确定性区分。
- Restic executor 必须解析真实形状的 JSON/NDJSON 输出，并只接受最终成功 summary 中的完整 native snapshot ID；进度行、重复行、未知兼容字段、缺失 summary、截断或 malformed 输出必须得到明确且可测试的结果。
- 禁止通过 `latest`、snapshot prefix、完成时间窗口、仓库列表差集或全量快照回填来猜测本次运行产物。
- 完整 native ID、Repository identity、producing Task/TaskRun 与唯一运行标记必须在整个 publication flow 中保持一致；任何漂移都必须失败关闭。
- 自动归属时，Provider 返回的 raw tag multiset 必须严格等于本次两个生成 marker，且 Restic `Snapshot.Original` 必须为空；任何额外/缺失/重复 tag 或 metadata rewrite 都必须隔离为 `provider_snapshot_rewritten`，不得把后来改写出的 native ID 当成本次 backup 产物。
- 预分配 tags 只证明 snapshot 归属该 attempt，不能证明 Restic 最终 exit 0；exit 3 也可能留下同 tags 的 snapshot。只有 `RecordProviderCommit` 已持久化的 exit-zero summary evidence 才允许自动进入 `verifying`，进程在退出码与 DB 事务之间崩溃形成的 snapshot 必须隔离为 completion-unproven，不能由 reconciliation 自动发布。
- stdout summary 缺失/损坏但命令已可靠 join 为 exit 0 时，可以只持久化 typed `known_exit_zero`；随后仅允许从唯一 exact-tag snapshot 自身持久化的有效 Restic `Snapshot.Summary` 重建 canonical commit evidence。不得把被拒绝的 stdout 片段升级为 evidence；stored summary 缺失/无效必须失败关闭。
- 相同 TaskRun 的重试/对账必须幂等；不同 TaskRun 即使内容相同，也不得错误复用运行血缘或重复认领同一 snapshot。

### Exact Manifest And Minimum Verification

- 只针对本次完整 snapshot ID 生成 canonical manifest evidence；不得扫描仓库中的其他快照或依赖旧全仓库索引。
- manifest 至少包含固定 algorithm、generator、schema version、digest、entry count、logical bytes、completeness 和 fidelity profile，并使用 Child 1 已冻结的 `complete | partial | unavailable` 合同；发布失败由 RecoveryPoint `failed` 表达，不能伪造第四种 manifest completeness。
- manifest 枚举必须有界、可取消并受 Child 2 timeout/output/resource contracts 约束；未知记录、截断、解析错误、locator drift 或不完整枚举不得产生 `complete`。
- 只有 complete manifest 与最低 Provider verification 同时匹配精确 Provider commit 时，RecoveryPoint 才能进入 `committed`。partial/unavailable 只能保留安全的失败或待对账状态，不得向后续资产 API 宣称可浏览可信。
- manifest digest 必须由稳定 canonical representation 计算；实现细节不得把原始路径、文件名、secret、Provider locator 或命令输出写入日志和资产审计。

### Generic Evidence Publication Seam

- 在 Task executor 与 runner/manager 之间建立可由后续 Provider 复用的窄 evidence-aware 执行契约，同时保持现有不产出 evidence 的 executor 兼容。
- runner/manager 只推进并记录 Provider 传输退出状态与 TaskRun 状态；publication coordinator 通过 `RecordProviderCommit` 独立持久化 Provider commit 并只推进到 `verifying`，异步 publication worker 独占 manifest 构建、最低验证及后续 RecoveryPoint 状态推进。TaskRun 不等待 publication，任一 publication 失败不得篡改已经发生的 Provider 传输事实。
- 发布服务必须复用 Child 1 的 Repository、RepositoryTaskLink、RecoveryPoint、state validation、typed audit 和事务边界，不创建第二套资产模型。
- RecoveryPoint 必须保存或关联不可变的 producing Task/TaskRun 摘要，使后续 Task 归档不破坏历史血缘。
- 所有状态推进、幂等键和 audit action 都必须是 typed 的；运行日志和审计只记录 opaque IDs、stage、stable code 与安全计数。

### Crash Recovery And Reconciliation

- 覆盖至少两个关键 crash window：Provider snapshot 已存在但数据库 publication 未提交；数据库存在 preparing/verifying 记录但精确 snapshot 未出现或不可验证。
- reconciliation 可以用唯一 Task/Run 标记证明归属，但只有 durable exit-zero evidence 已存在时才能继续 manifest publication；仅有 snapshot/tags 而退出结果丢失时必须保存安全 locator/fingerprint、标记 completion-unproven 并失败关闭，等待 Child 14 显式人工导入，不能猜测成功。
- reconciliation 必须使用真实的 publication lease/fencing 合同，并在同一事务内校验 fence 与状态推进，保证旧 owner、迟到 manifest 或重复 runner 不能推进新状态；不能以独立 `ValidateFence` 后另起事务留下 TOCTOU 窗口。
- `verifying` RecoveryPoint 必须是异步 publication worker 的持久事实源；内存 wake 只能降低延迟。worker 的并发、退避、逐点绝对截止时间、优雅停机和重启续接都必须有界且可测试，新的 lease stage 不能重置逐点截止时间。
- rollback 和对账均不得执行 `forget`、`prune` 或删除任何 Restic snapshot。

### Legacy Snapshot Index Isolation

- 现有 snapshot indexer 不得再把共享仓库内的全部 Restic snapshots 标记为请求 Task 的资产，也不得把未经精确 publication 的记录注入新 RecoveryPoint/Catalog 链。
- 旧搜索/浏览兼容面在后续 Catalog/UI 切换前可以保留，但必须明确属于 legacy generation，不能成为新资产 API、可信状态或运行血缘的事实来源。
- `backup_assets.enabled=true` 时，旧 list/files/search/diff/snapshot-restore 必须经过 Task lineage guard；只允许当前 Task 已提交恢复点中的完整 ID，短 ID 只能在该 Task 已证明集合内唯一解析，且不得回退到仓库全量。
- `backup_assets.enabled=true` 时，异常检测必须比较当前精确 snapshot 与同一 Task 的前一个 committed snapshot；缺少任一端时跳过，禁止仓库级 `--latest 2`。
- `backup_assets.enabled=true` 时，旧 task-level `restore latest` 和无 Task tag 的仓库级 `forget --prune` 必须 fail closed，直至后续受控恢复与生命周期 Child 接管。
- 所有会触达 Restic credentials/SSH/command/read handle 的 backup、list/files/index/search/diff/restore、anomaly、retention、publication/manifest/reconcile 路径必须共享 generation admission token 并持有至 close/join；feature enable/disable、首次 managed point、downgrade 与 schema down 必须先关闭新 admission 并排空全部这些命令，不能只排空 backup。
- 隔离旧索引时不得破坏现有备份任务、现有恢复/差异能力或从未产生 managed publication 的 pristine feature-disabled 路径。

### Security, Compatibility And Feature Boundary

- 复用 Child 2 的 Restic binding、完整 snapshot ID 校验、purpose-scoped command runner、限时/限量/取消和 secret sanitization；不得重新引入 shell 拼接或裸 credential 路径。
- Restic command/test fixture 中的密码、仓库定位、源路径和真实文件名不得出现在公开错误、日志、审计、TaskRun 摘要或 HTTP DTO。
- 代码审计已确认 `000062` 的 RecoveryPoint/manifest/lineage 主体可复用，但缺少 Restic `native_snapshot` Task publication mode、`point_publication` lease holder，以及防止同一 TaskRun/原生 snapshot 被重复认领的数据库唯一防线。本任务按用户批准新增 paired `000063`：producing-TaskRun 唯一索引有意覆盖所有非 null semantics（一项 TaskRun 最多命名一个 point，mutable head 通常保持 null），并扩展现有双数据库 apply/down 与拒绝前置条件夹具。
- 不新增前端、公开资产导航、Catalog/search/content ticket/preview/download/restore/retention API，也不改变 feature 默认值。
- 对从未产生 publication RecoveryPoint 的安装/仓库，`backup_assets.enabled=false` 保持现有兼容行为且零资产副作用；任何本合同创建的 native RecoveryPoint 或后续保留 tombstone，无论当前 lifecycle state，也无论 Task/TaskRun FK 是否因归档变为 null，都永久触发 managed-history latch。关闭 feature 随即进入 rollback-safe 模式：停止新 publication，禁止该 Repository 的 Restic Task 无 tag 回退，并继续阻止跨 Task legacy 读取、`restore latest`、仓库级 anomaly 和无 tag retention。若安装已有 managed history 而某 Restic Task 无法用当前 link/binding 证明属于另一 Repository，也必须失败关闭。该持久安全锁存不依赖新增 schema。
- 除正常 Restic backup 创建 snapshot 及其唯一标记外，不增加 Provider mutation；禁止 forget、prune、delete、restore、repository init 或任意仓库修复操作。

## Constraints

- 遵循现有 Task manager/runner/executor 生命周期、response-independent domain boundaries、structured logging、sentinel errors、settings registry、sanitized DTO 和双数据库兼容规范。
- 不把 Restic 专用解析或命令细节泄漏到通用 Task manager；通用 seam 只承载 Provider-neutral publication evidence。
- 不让 `backupasset/provider` 反向依赖 Task manager、API handler 或 snapshot legacy package；依赖方向必须允许 Child 4–5 复用通用发布边界。
- 不得为实现便利随意扩表；本任务只实施经 schema review 批准的 `000063` contract repair，不能额外增加 publication outbox/attempt 表，也不能仅靠先查后插规避数据库约束。
- 任何会改变父任务领域、安全、生命周期或 Provider 能力边界的发现，必须先回写父规划并经过用户审阅。

## Acceptance Criteria

- [ ] focused `design.md` 与 `implement.md` 基于最新 `main`、父规划、Child 1–2 实现和相关 Trellis specs 完成并经用户复核。
- [ ] real-shaped Restic NDJSON fixtures 覆盖进度、最终 summary、完整 native ID、缺失 summary、malformed/truncated 输出、未知兼容字段与 secret/output redaction。
- [ ] executor 只从本次成功 summary 返回完整 snapshot ID；任何 `latest`、prefix、时间窗口或仓库差分归属路径均被测试禁止。
- [ ] 共享仓库测试证明两个 Task、并发/重试运行、手工或无归属 snapshots 不会交叉发布 RecoveryPoint；`restic tag` add/set-same/add-then-remove rewrite 通过 exact tag set + `original` 被识别且绝不归属原运行。
- [ ] exact snapshot manifest 的 canonical digest、entry/logical-byte 计数、completeness/fidelity、取消、超时、资源上限和 locator drift 测试通过。
- [ ] generic evidence seam 对 evidence/non-evidence executors 均兼容，并通过 compile-time、runner、manager 和 timeout/cancellation 回归测试。
- [ ] transfer success 与 publication success 独立持久化；TaskRun 不等待 manifest，`preparing → verifying → committed` 的合法/非法、幂等和 audit 状态转换测试通过。
- [ ] crash-window reconciliation 证明 durable exit-zero evidence 已提交但 wake/manifest 缺失时可安全续接；stdout evidence defect + durable `known_exit_zero` 只能从唯一 exact-tag snapshot 的有效 stored summary 重建；只有 tags 而退出结果丢失的 snapshot 被精确归属但隔离为 completion-unproven，绝不自动发布；缺失/歧义/无有效 stored summary 的 snapshot 失败关闭，旧 fence/迟到发布不能提交；stage lease 轮换不重置逐点 deadline，worker 退避、取消、停机和重启续接均通过测试。
- [ ] legacy snapshot index 不再把仓库全量 snapshot 归属当前 Task，且不能进入新资产链；feature-enabled 的旧 list/files/search/diff/restore、anomaly 与 retention 均有精确 lineage/fail-closed 防线；从未发布时 feature-disabled 兼容回归通过，发布历史存在后关闭 feature 仍保持持久安全锁存；enable/disable/down 与每类已运行 Restic command 的竞态都证明全生命周期 admission drain。
- [ ] `000063` 在 SQLite/PostgreSQL 完成 apply/down、旧 Restic link 数据修正、全 semantics 非 null producing-TaskRun 唯一约束、native source 唯一约束、UTC parity，以及 active command/lease/任意 state/FK-null history/tombstone 的 down 拒绝夹具；除此之外没有额外 migration、前端、公开资产 API、feature enablement 或 destructive Restic command。
- [ ] secret/path/locator/output 扫描、typed error/audit 测试与依赖方向检查通过。
- [ ] focused suites、`make backend-test`、backend lint/build、全仓 `make check`、doc freshness、migration parity/UTC 与 `git diff --check` 通过。
- [ ] 按同一分支正确顺序完成：实现/验证 → Phase 3.4 工作提交 → `trellis-finish-work` 归档+journal 自动提交 → push/PR → required CI/merge → post-merge 自动化与 main 同步；不得拆分功能 PR 和归档 PR。

## Out Of Scope

- Rsync hard-link/full-copy versioning、Rclone unique-prefix/native-object-version publication（Child 4–5）。
- Catalog generation、provider reconciliation listing、搜索和 overlay（Child 6–7）。
- Content ticket、HTTP Range、预览/下载、Worker、导出、受控恢复、retention/purge 与 GA（Child 8–15）。
- 新 Repository 管理 UI、备份工作区 UI、公共分享或通用网盘写入能力。
- 从任意现有 Restic 仓库猜测、批量导入或回填历史 lineage；完整 import/rebuild 属于 Child 14。
- Restic forget/prune/delete/restore/init、任意 snapshot 物理清理或保留策略执行。

## Open Decisions

- 无尚未决定的产品范围问题：父任务的完整设计与 Child 3 的分层协调器方向已经过用户批准。
- schema review 已解决：用户批准 paired `000063_backup_asset_publication_contract`，把 Restic Task link 从错误的 `native_object_versions` 修正为 `native_snapshot`，增加 `point_publication` lease holder 与两个 partial unique indexes，并将父计划原 `000063…000069` reservation 整体顺延为 `000064…000070`。
- focused 设计通过代码与 Restic 官方证据固化唯一 tag 编码和重试语义、JSON 兼容策略、manifest canonicalization、publisher/transaction owner、crash reconciliation 触发点，以及 legacy surface 的 feature-gated 精确隔离面；这些均为技术合同，不再需要新的产品决策。
- 如果这些技术事实要求改变用户可见语义、schema reservation 或父任务安全边界，必须暂停并提交单一高价值决策给用户；否则不重复询问已批准的产品选择。

## Notes

- 用户于 2026-07-14 明确批准创建 Child 3 并进入规划阶段。
- 用户于 2026-07-14 明确批准 schema 方案 A。
- 用户于 2026-07-14 明确回复“批准 Child 3 设计，除此之外没有异议”；设计 review gate 已通过。
- 用户随后于 2026-07-14 明确批准 focused `implement.md` 并要求启动实施；实施计划 review gate 已通过，Child 3 当前为 `in_progress`。
