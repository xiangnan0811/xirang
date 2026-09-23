# 备份资产启用与运行时切换

本合同拥有 Foundation 设置、GA 库存/确认、运行时准入和失败补偿。使用说明见[备份与恢复](../../admin/backup-recovery.md)。它不授予 Worker 发布或 Provider 数据变更权限。

## 请求值与有效开启

- `backup_assets.enabled` CodeDefault 永远为 `false`，按 DB > env > default 解析。`FoundationService.FeatureEnabled()` 仅请求值；产品面注入 `Runtime.FeatureLive()`，即请求 true 且 `ga.EvaluateEnablement(snapshot)==nil`。readiness 异常失败关闭，blocked/ack-required 返回 `(false,nil)`。
- `AdmissionController.Initialize` 从 disabled 开始；只有 `StartupPass` 授权后 `InitializeManaged`。请求启用但未 ready 时 Core 继续启动且 disabled，允许 Admin inventory/ack，不能 Fatal 或暗中 managed。
- readiness 校验真实 Foundation config、所需 key domains、迁移与有效 export root；非空路径字符串不构成就绪。fresh+ready 无需 ack；existing 必须 Admin 确认当前 64-hex inventory digest。class 仅 fresh→existing，不能反向。
- 开启前先授权，再 `PrepareEnable`/admission transition；`FeatureTransitioner` 返回 Runtime 而非绕过门禁的 AdmissionController。关闭仍排空，不需 readiness/ack。
- 持久化成功 callback 首次记录 `enablement_succeeded_at`。计算 passing readiness 必须通过 `MaterializeReadiness` 写 stored ready；DryRun persist 保持 unknown，不 stamp。门禁使用的 durable latch 必须由生产代码真实写入。

## 库存、API 与 UI

- 配对 `000071` 包含 singleton `backup_asset_installations(slot=1)`、inventory runs、repository conflicts。readiness 闭合 `unknown|blocked|ready|acknowledged`；conflicts 闭合 `shared_restic_identity|task_repository_mismatch|capability_gap|command_unsupported`。fresh ack 字段 null；counts/task IDs 必须非空 valid JSON，repository ID 空或 32-hex、digest 空或 64-hex。
- DryRun 只分类 Task/link/非秘密身份/managed-history latch。共享 Restic 身份可合并 Repository，不合并所有权；command unsupported，未链接且无存储身份的文件任务 capability_gap，不能按 locator 当身份。run/conflicts 同事务，`ProviderMutationSurface=forbiddenGAMutations`，不可调用 import/rebuild/purge 或触碰 Provider bytes。
- Auth + `backup_repositories:manage` + admin 的 `/settings/backup-assets/ga/{inventory,readiness,acknowledge}` 是独立入口；不复用 recovery downgrade-readiness。
- PUT enable、DELETE 恢复到 env true、config import 均执行同一门禁。blocked/ack-required 返回 409 `就绪检查未完成` 且不持久化 true；stale digest 为专门 409 `清单已变化，请重新核对`。其他运行时/取消/持久化/补偿故障通用 500，不返回 `err.Error()`。
- 公共 JSON 仅 schema 1、counts、closed kinds、opaque IDs/digests，不含 locator/proof/ticket/identity key/SnapshotFileIndex。前端未知字段丢弃，未知 schema/class/status 拒绝，坏 Repository ID 变空安全值；渲染 counts/i18n，不显示 raw candidate。Viewer/Operator 无 Admin CTA。
- GA 不加入通用 Settings `CATEGORY_ORDER`（security、node_monitor、retention、storage、alert、anomaly）；Admin readiness 面板单独拥有流程。Worker optional 是布尔事实，不是公共镜像承诺。
- 已认证遗留 snapshot list/files/search/diff HTTP 始终 410，替代为 Catalog/Search；legacy restore 保留注册但要求 FeatureLive + Admin + step-up。关闭 feature 不使退役读 API 复活。
- GA composition 在 `backupasset/runtime`，不向 server main 复制编排。官方镜像仍 `linnea7171/xirang`；Worker 本地可选且未发布，不因 GA 增加 Worker 公共发布。

## 设置锁序、前瞻配置与工作所有权

- mutation owner 恰一次调用 `settings.Service.WithBackupAssetMutation(ctx, callback)`，从 supplied snapshot 得到完整、不可变 current/prospective bundle，再调 runtime。外部 reader 可用阻塞 `BackupAssetSettingsSnapshot`；内部 Content/Search/Overlay/Export/Recovery/admission/persist/compensation（含 disabled 组件）都不可重入 Foundation getter/gate。
- 使用 `ContentConfigFromValues`、`SearchOverlayConfigFromValues`、`ExportConfigFromValues`、`RecoveryConfigFromValues`、`FoundationTransitionConfigFromValues`。缺项/非法 bundle 在副作用前失败。
- runtime 使用 `TransitionBackupAssetSettingsContext[WithRestore]`，persist/restore callback 接收 runtime context。`UpdateContext/UpdateWithTxContext/UpdateManyContext/DeleteContext/DeleteWithTxContext` 保持同一 context 进 GORM，不能内降级为 Background 或无 context adapter。
- `Content.PrepareEnable` 接受显式 config；Search `PrepareWithConfig` 只校验 config/key、reconcile abandoned/overlay、list candidates，不同步 Build。候选 projection 归已有生命周期 worker 和 Search timeout，不继承 HTTP/settings deadline，不另起无 owner goroutine。
- hot enable 仅在 persist、durable stamp、Content ready 和全部 fallible stage 成功后 `TryWake`。容量一、非阻塞、合并；失败/补偿无 wake；cold startup 无额外 wake。wake dequeue 后重读 committed config，disabled 时不做 reconcile/list/Build。Run 前 wake 吸收，重复 wake 不并行 pass，shutdown cancel/join woken Build、gauge 0。
- gate waiter canceled 时 callback 不进入；forward work 全用一个有界 operation context。返回前取消/join 自有异步工作。

## 精确回滚与失败围栏

- 变更前保存 exact raw override rows 和精确 absence，含 timestamp/value、admission、success stamp、readiness/candidate 状态。mixed PUT 保存全部同事务 ordinary+Foundation keys，不只 Foundation 子集。恢复同事务提交后失效 cache，不能仅还 effective value。
- config import 的事务 commit 后安装 sealed undo journal；后续 runtime 故障按依赖逆序恢复完整 imported DB graph/关系与 settings，不能只有设置回滚。生产等价测试必须包含全部真实 callback/graph edge。
- 补偿可脱离 caller cancel，但所有 cleanup 共用从 bounded transition 预算保留的一个绝对 deadline，嵌套 restore 不重开 timeout。恢复前必须停止候选；无穷 detached cleanup 不可。
- `%w`/`errors.Join` 保留 context、primary 和 `ErrFeatureTransitionCompensation` identity。任一补偿失败，先 runtime not ready，再 sticky restart-only fence；后续 readiness/transition 返回 `ErrInvalidState`，不可在线 clear/retry。
- 错误、日志和响应只含结构化安全字段，不含 raw setting/root/secret/locator/credential/proof/ticket/Provider evidence。只有已识别 GA sentinel 是 409，其他故障通用 500。

## 迁移与观测

`000071` 的 ready/acknowledged、任意 conflict 或 success stamp 均拒 used-down；未知/blocked 且无冲突/stamp 的未使用盘点可 pristine down。既防 down-body，也在 `schema_migrations` 写 dirty/target 前拒绝，保留 clean version/schema/data；empty pristine down 移除表和 trigger。semantic PostgreSQL migration runner 必须覆盖真实测试，不能靠手写版本白名单。

`Dispatcher.BackupAssetSLORules()` 在启动读取 requested setting 决定规则集合；FeatureLive gauges 每次 door check 更新。requested true/live false 表示 pending enable，不自动等于 outage。Search 5xx/503 必须结合安全原因/审计失败日志解释，不能以 503 唯一归因。

## 回归证据

覆盖 fresh/existing/current/stale ack、四种冲突、默认 false、startup blocked 仍 Core ready、PUT/DELETE/import 不落 true、FeatureLive 注入所有门、inventory 无 Provider mutation、真实 ready/stamp 写入、GA mapper/A11y/Admin mount、非 Admin secret proof 拒绝、退役 410。

配置测试必须真实或证明与生产等价：Content/Search deadlock subprocess watchdog、gate cancellation/高重复、AST 禁内部 getter/context 降级、各 stage failure（含 imported graph）精确恢复、mixed PUT old row/absence/timestamp/cache、错误 identity、deadline 与 sticky fence。blocked candidate 证明 enable 可先返回；failed stage 零 wake，committed disabled wake 零 backend call；race/repeat 全覆盖。

运行配对 `BackupAssetMigration071*`（ready/ack/conflict/stamp/pristine）、`scripts/check-backup-asset-migration.sh`、required PostgreSQL、Compose export 隔离与相关完整门禁。源码检查或缺 DSN skip 不能作为真实 gate 通过。
