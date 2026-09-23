# 备份资产生命周期

本合同拥有 Task 归档、保留策略、冻结、精确 purge、删除证明、源撤销和控制面灾难恢复。它与[仓库发布](backup-repository.md)、[Catalog 代次回收](backup-catalog.md)分工明确：代次回收不删除恢复点或 Provider bytes。

## 控制面归档与策略

- `ArchiveService.Archive(ctx,taskID)` 始终在线，不读取 feature flag。`ExistsLiveByID` 明确 `id=? AND archived_at IS NULL`；需要查看归档行的调用保留不筛选的 `ExistsByID`。
- 在 archive 同一事务先用 sqlite_master/pg_tables 检查 `backup_retention_policies`，不能用另开池连接的 `Migrator.HasTable` 导致 MaxOpenConns(1) 死锁。无表跳过 settle，archive/unlink 仍成功。
- 有表则同事务将 active Task-link policy 软删为 deleted、revision++、deleted_at，并写 `retention_policy_delete`。nil/失败 audit 回滚 Task/link/policy，不允许部分成功。
- depends_on_task_id 的 create/update 按 Task ID 升序锁定参与 Task，并在同一事务验证 live；不能依赖归档前的读。task 不 import retention（后者已 import task），用 narrow `AuditWriter.WriteTx`/ArchiveDependencies port。
- actor/correlation 从 HTTP context 和现有 RequestID 传入 `WithArchiveActor/WithArchiveCorrelationID`，没有则不编造。hold create/release、purge plan/execute 与 policy mutation 用相同 wrapper；operational hold expiry 在事务内写 hold_release。
- archive/leftover backfill 不写 GA installation/inventory/conflict rows。backfill 完整性由[处理与导出](backup-processing-export.md)拥有。

## 删除准入与源生命周期

当前 runtime 已分别注册 Restic、Rsync、Rclone `PointDeleter`，并装配 `RegistryPointDeletion` 与 retention Coordinator。不能继续把“完全没有 Provider deletion/retention/purge”写成当前事实；注册能力也不代表任意点可删除。

- `PointDeleter` 是独立可选能力，read adapter 注册不自动授予。请求绑定 exact Repository/capability/source revision/private locator/operation ID，结果闭合 `deleted|already_absent|blocked_worm`；前两者必须有合法 receipt digest。
- 只有经过有效 retention_expire/explicit_purge 协调准入、精确来源/绑定/身份验证、hold/WORM/能力检查、活 lease drain 和所有 owner cleanup 证明后，才能执行精确点副作用。不以 mutable current config、当前名称、空 lease 表或 Provider missing 推断授权/完成。
- retained deletion 使用不可变 point/link producing snapshots。unlink 后 nullable link TaskID 可为空，但不能改变 frozen authority；当前 Task/Node 名称只是展示，不应错误阻止合法历史点删除。访问所需 Task、Node、SSHKey 逐行显式锁定，不能指望 GORM Preload 继承 FOR UPDATE。
- 源生命周期先 revoke/drain Content、撤 Catalog、撤 Search、撤 Processing、expire Export、cancel Recovery interest，再完成各自 cleanup 与 Overlay reconcile。Overlay 有界 pass 必须证明 zero-result 完成，预算耗尽是 unproven，不是 success。Processing 删除衍生内容前调用 Search exact revocation proof。
- 缺 owner wiring、lease live/drain unproven、owner cleanup unproven、fence lost、Provider unavailable/identity conflict/unproven delete 失败关闭并保留重试事实。rollback/reconcile 不凭清门禁而任意 delete/prune；真实 provider delete 只能由 admitted lifecycle 拥有。
- legacy Rsync/Rclone mutable bytes 不是历史快照，禁止按 age 的破坏性 Simple cleanup。pristine legacy Restic 可用自己的 bounded retention；managed-history latch 阻 untagged forget/prune，必须经过受控生命周期。

## Rclone Native 版本依赖与 reservation

- 配对 `000075` 在 `recovery_point_rclone_native_versions` 保存 HMAC exact-version 事实。当前 locator 含 owned/reference count+digest，legacy v1 为同一 owned+referenced set，仍可读取。坏/混合 locator 或 HMAC 漂移失败关闭，缺行不是“无引用”证明。
- 删除仅 owned set。任一 live sibling reference 与之相交，则 `ErrDeletePointNativeVersionReferenced`/`provider_native_version_referenced`。preparing sibling 尚无 durable evidence 也等待 referenced，不能错判 identity_conflict。
- referenced 是副作用前依赖等待，不占 deletion-reservation blacklist，让 preparing sibling 可继续 `RecordProviderCommit`。identity_conflict、in-flight/unproven delete 仍保留 reservation 并阻后续 Prepare/commit，防止删除不确定时新发布穿越。
- `000076` 只扩 blocked-reason product；SQLite rebuild lifecycle attempts 保留行/check/FK/index/000070 trigger，PostgreSQL 仅替换命名 check。000075 表非空或 000076 reason 已使用时，direct down 与 metadata admission 原子拒绝，保留 clean version/rows。

## 删除副作用证明与结算审计

配对 `000077_lifecycle_effect_claim_audit_slot` 增 Coordinator-owned effect claims 与 immutable settled-audit slots。升级必须停止所有旧 retention worker，排空旧 writer，不可 mixed-version；先调和 scoped retention_expire/explicit_purge provider_delete 中无合法 receipt 的未决 attempt，迁移原子拒绝歧义。

- runtime 与 backfill 共用 `settledDeletionCandidate`：scoped operation 加合法 terminal tombstone/receipt，或 blocked 且 reason 是 active_hold/provider_worm/provider_unavailable/provider_identity_conflict/provider_native_version_referenced/provider_delete_unproven/deletion_unavailable。
- selected/revoking/draining/cleaning、lease_live/lease_drain_unproven/owner_cleanup_unproven/fence_lost 及 mutable_retire 不结算。不能把临时状态伪造为 settled audit。
- 精确 retained event 要 action=repository_purge、attempt→point→repository ID 一致、item_count=1 且 fields.item_count 是整数1、stage=settled、source=attemptID、合法 status；observational outcome=blocked，terminal outcome=success 且与 tombstone 相符。
- exact duplicate 去重；每种 observational slot 可各一次且顺序任意，至多一个互斥 terminal slot 在后。near miss/剩余歧义整体 rollback，先修原 event/tombstone 再重试，不删除历史绕过。
- claims append-only `in_flight|uncertain|proven`，slots 是永久 immutable 证明。retention detail purge 不能充当幂等依据。任一 claim/slot 非空即拒 direct down 与 schema metadata admission，只可 forward repair。
- Provider-delete 恢复先验证 locked attempt/point/tombstone 和 proven claim，再看当前 lease identity/fence/expiry。历史 owner/holder rebind 不使 committed proof 失效；只结算仍精确 owned 的 lease，不碰 rebound lease。missing candidate lease/corrupt proof 仍拒；无 proof 路径保留全部执行授权。

## 保留、重建与灾难恢复

配置/保留时间不构成物理存在或安全可删的证明。TaskRun 恢复源保留与当前代次保护由[任务执行与恢复](task-execution-recovery.md)拥有，不能以普通历史 retention 删除 unresolved/dirty/source references 后复活旧 success。

丢失控制面后，Provider 只能重建仍可验证的恢复点/Catalog 启动事实，且先由 Admin 有效重连/导入审查。用户 overlay、审计链、attempt/tombstone、policy/hold/Task 关系不可从 Provider 重建；访问绑定、hold reason、wrapped domain keys 还需原 DB 和匹配 DATA_ENCRYPTION_KEY。新 DB 未重连不可 import rebuild、不凭空产生策略/冻结；错/缺 key 失败关闭，不静默替换或假报成功。Catalog 不能反写 Provider bytes，Provider 丢失需原生或独立备份恢复。

## API 与前端

保留/hold/purge 的显示与 mutation 只使用闭合安全 DTO、opaque IDs 和现有权限/step-up，不能暴露 locator、identity、credential、provider output。请求 correlation 贯穿安全审计。Admin scan/rebuild/import queue 每次最多八页，可继续的 cursor 内存保存，不能把 panel 锁在无穷 Provider walk；未证明结果不显示 completed。

## 回归证据

archive 无 policy table 200、同 TX settle/audit、audit failure 原子 rollback、request ID 有/无、archived dependency create/update/race/lock order；retention fixtures 显式 APP_ENV=development 或 encryption key + ResetForTesting，不依赖 shell。

真实 runtime+Coordinator+owner ports 覆盖 revoke→drain→cleanup→Provider effect 的所有失败/取消/crash/resume，hold/WORM/identity/capability、frozen names 与 unlinked TaskID、各 Provider 精确删除 receipt。Native 组合必测 B preparing→delete A referenced 且不 reservation→B commit verifying，identity conflict 则占 reservation；坏/混合/HMAC evidence 拒绝。

000075/76/77 双引擎 direct down 和 `Steps(-1)` 证明 clean version/schema/rows 不变，quiesced upgrade unresolved/near miss/duplicates/observational order/terminal mutually-exclusive matrix，proof-first owner rebind 不再执行副作用且不改新 lease、corrupt proof 拒绝；审计 detail purge 不破幂等。

必需 PostgreSQL CI 选择器必须比对真实 acceptance test inventory 并执行，复制两份相同 selector 不证明全覆盖。运行 SQLite、真实 PostgreSQL、race/repetition 和源生命周期各 owner suites；缺 DSN/环境阻塞如实记未执行，不当成功。

源码入口：[删除能力](../../../backend/internal/backupasset/provider/deletion.go)、[精确来源验证](../../../backend/internal/backupasset/repository/lifecycle_delete.go)、[运行时协调](../../../backend/internal/backupasset/runtime/retention_lifecycle.go)、[持久副作用 claim](../../../backend/internal/backupasset/retention/effect_claim.go)。
