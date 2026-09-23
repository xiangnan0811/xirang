# 备份仓库与恢复点发布

本合同覆盖 `backend/internal/backupasset/{provider,repository,publication,runtime}`、`fileaccess`、任务执行器以及版本化 API。保留与物理删除由[生命周期](backup-lifecycle.md)拥有；有效开关由[启用条件](backup-enablement.md)拥有。

## 包依赖与执行器映射

| 包 | 唯一职责及依赖边界 |
|---|---|
| `backupasset` 根包 | 只拥有领域类型，不承担运行时组装。 |
| `backupasset/provider` | 可依赖根领域类型；不得 import Gin handler、`task/executor`、runtime、Repository、Task 或 API 包。 |
| `backupasset/repository` | 编排领域服务和 Provider registry，消费类型化访问绑定。 |
| `backupasset/publication` | 定义与具体 Provider 无关的端口。 |
| `backupasset/runtime` | `cmd/server` 之下唯一的备份资产组合根。 |
| `fileaccess` | 独立于 Gin、GORM、model 以及 backup/task 包；不得查询数据库或读取 `FILE_BROWSER_ALLOW_ALL`。 |
| `sshutil` | 承载 Provider 与 task executor 共用的 SSH auth/dial/runner 机制，不在两侧重复实现。 |

Task executor 到 Provider 的映射只能在 `backupasset/repository/binding.go` 中发生。其他 Repository service 文件必须使用映射后的类型化 binding，不得根据 `Task.ExecutorType` 分支。扩展 Provider 时保持窄端口与此依赖方向，不能把 Task 配置解释散布到 Reader、reconcile 或 lifecycle 消费者。

包依赖和映射位置要有编译/源码边界回归：拒绝上述禁止 import，枚举 Repository 中 `Task.ExecutorType` 的分支并证明只在 `binding.go`，检查 `fileaccess` 无数据库和 `FILE_BROWSER_ALLOW_ALL` 依赖。仅接口行为测试不能替代这类边界检查。

## 身份、密钥与读取边界

- `entry_identity`、`recovery_cleanup_ownership`、`search_token` 是安装稳定密钥域，`Keyring.Rotate` 必须拒绝。KEK 轮换只能重新包裹，不能改变密钥材料。丢失时不得静默生成替代；Entry ID 支撑深链接与用户引用，更换需要明确兼容方案。Search、Entry、Cursor、Audit、Derived、Export 域不复用。
- Provider locator 和 access binding 只在进程内部使用并保持 `json:"-"`。HTTP 只接受不透明 Xirang ID，不接受路径、remote、仓库字符串、shell 片段或凭据。
- Restic 使用完整原生仓库/快照身份。可变 Rsync/Rclone 使用 Task 范围、带盐且域隔离的 HMAC 身份，不能跨 Task 自动合并。共享 Restic 仓库也不合并所有权。
- 保留的 active binding 必须仍指向未归档 Task，当前 Provider/Node 血缘须匹配加密绑定。漂移在 probe 前拒绝，保留原 Repository/link/binding；替换必须指定精确 Repository 并显式 `replace_access=true`。
- 窄端口为 `RepositoryProber`、`PointLister`、`EntryLister`、`EntryStatter`、`SequentialReader`、可选 `RangeReader`。每次读取携带绑定 Repository、capability revision、source revision 和私有访问的 `ReadSnapshot`。分页有界并签名；顺序读取要求 `MaxBytes>0`，Range 要求非负 offset 和正 length。
- 本地严格读取使用 `fileaccess.Tree.List/Lstat/OpenRegular/OpenRange` 的句柄相对约束，不跟随 symlink，不得先验路径后自行打开。SFTP 采用严格相对 locator，在打开前、打开时、结束后验证 containment/type/source；已验证 SSH/SFTP 服务端仍是基础设施信任边界。
- 绝对路径、NUL、dot-dot、歧义 locator 在 I/O 前拒绝；特殊文件只可按类型列出，不能读取。源/根/对象在读取期间改变返回 `mutable_source_changed`，不能把部分内容作为成功。
- Provider 命令仅使用服务端固定工具/操作枚举、独立引用的参数、有界输出/记录/条目/时间/并发和一次性秘密 stdin。流持有许可直到 close，并传播 wait/cancel/limit/invariant 错误。取消必须关闭自有句柄/会话并 join goroutine。
- Restic 密码只通过一次写入 `/dev/stdin` 传递，不能进入 argv、环境变量或临时文件。Restic snapshot `latest` 或前缀在命令执行前拒绝；Rclone Range 语义未证明时保持 `OpenRange=false` 并返回 `range_unavailable`，不能由通用 reader 猜测支持。
- `provider_operation_timeout`、`provider_max_concurrency`、`provider_metadata_limit_bytes` 从 `backup_assets` 动态配置读取；`RESTIC_BINARY`、`RCLONE_BINARY` 是重启生效的可执行文件选择。SSH purpose 为 `repository_probe|repository_list|repository_read`。
- 注册读适配器不赋予写权限。发布与 `PointDeleter` 必须单独注册并经过各自准入；远端 Rsync 目标没有独立目标凭据合同时保持 unsupported，不能假设可复用源 Node 凭据。
- 读适配器不能触达 Restic mutation 命令或 Rclone/Rsync copy、sync、delete、restore、publication 路径。关闭功能的仓库数据面在 Task/DB/keyring/audit/dial/command 副作用前拒绝；始终在线的归档与明确维护例外由各自合同授权。
- 可变 Rsync Repository 维护一个稳定的 observed head 并原位更新；disconnect 保留 observed/offline 事实，reconnect 推进 capability revision，不能把重连误作新的历史快照。

## API 能力错误

能力原因必须通过 `ValidateCapabilityReason`，禁止把任意 Provider 文本放入参数。未支持 Provider/端口或缺少 Task artifact 映射 501；关闭、离线、断连、超时、资源上限映射 503，并附安全 correlation ID。输入错误为 400，Operator 无权与不存在的仓库同为 404，身份/绑定/状态冲突为 409，其他 DB/crypto/SSH/protocol 错误为通用 500。

`respondBackupCapabilityError` 只接受 501/503。内容 ticket 的更窄原因白名单见[内容交付](backup-content-delivery.md)。失败 probe 仅更新安全 offline/reason，保留最后成功身份、mutable observation、fingerprint 和时间。超预算不能返回部分成功；cursor 签名、范围、revision 或列表指纹漂移不得猜测续页。

## Restic 精确发布

- `publication.Coordinator.Prepare` 先产生已准入 execution；只有 `RecordProviderCommit` 可把 `preparing` 变为 `verifying`，`Defer/Reject/Fail` 不得代替提交证据。
- 自动恢复点只来自最终成功 summary 的完整 snapshot ID 和精确 Task/TaskRun 两个 tag。禁止 `latest`、前缀、时间窗口、仓库 diff、legacy index 推断。检查 `original`、原生身份、时间和 capability 漂移；已经记录的 locator 不被替换。
- TaskRun 传输成功、Provider 提交和数据库发布是独立事实。已知 exit zero 但 summary 缺失/重复/非最终/坏格式时保留传输成功，按稳定原因延迟发布；仅可从唯一有效存储 summary 恢复。
- 有界异步 reconciler 校验完整 Manifest 和最低验证后才进入 `committed`。Manifest 激活与所有状态写入在同一事务检查当前 lease fence；点的绝对 deadline 不可延长。部分/不可用清单仅为 inactive 诊断，不可浏览，也不投影 count/digest。
- 共享 Restic 仓库保留 Repository 范围绑定，但执行访问必须来自当前链接 Task 的 Node/config；不得借另一个 Task 的 node、secret、locator、ID 或审计上下文。
- 每条 Restic 命令在凭据/SSH/stream/handle 前取得 generation admission token，直到 join/close 和响应或状态投影结束才释放。切换开关须排空后持久化。
- managed history 是永久保护：安装级或任一当前 Task 活跃 Repository link 的 tombstone 均阻止关闭模式下 `legacy_backup`。禁止无 tag fallback、`restore latest`、仓库级 anomaly selector 和无 tag `forget --prune`。回退和调和不删除 Provider snapshot。
- 配对迁移 `000063` 保护 native snapshot、publication、producing-run/native-source 唯一性；有 durable history、tombstone 或 active publication lease 时不能破坏性降级。

## Rsync 版本化

legacy mutable 不自动升级；管理员暂停任务后显式选择 `versioned_hardlink` 或 `versioned_full_copy` 并运行预检。预检验证挂载、硬链接、原子提交、容量、inode 和路径安全；硬链接不满足时不得自动降级。

硬链接模式先传输保持源硬链接分组的完整 staging 树，再按内容、元数据和 inode 分组兼容性复用上一 committed 点，不能用 `--link-dest` 把旧链接关系带入新点。新建/断开硬链接关系必须准确保留；不兼容组用独立文件，前一点不能被修改。容量和流量按完整 staging 计算。

迁移显式选择新完整 baseline 或从下次成功运行开始。baseline 复制旧目标到新受管树，不原地标记、移动或硬链接旧数据；后者不追认历史，首个硬链接点仍为完整种子。激活后保持暂停，传输、目录提交、发布分别取证。受管目录不是 WORM，也不是源端时间点快照。

`task_revision` 与 `expected_task_revision` 必须是非零规范无符号十进制字符串，不能转换为 JavaScript number。preflight、activate、rollback-preparations 均使用 CAS。激活/回退准备提交后重新加载并返回持久化 summary；同一对话框后续请求使用最近响应 token，reset effect 依赖初始 prop revision，不能抹掉新 token。坏 token 或未知 mode/state/reason 禁止操作；不从 executor config、日期或旧 prop 猜测。安全 summary 不含根路径、locator、marker/manifest/fence digest、命令、输出或凭据。

准备回退停止准入并排空，恢复保存的 legacy locator 后仍暂停，保留所有已提交点与保护记录；受管历史不能因此重新获得 mutable fallback。配对 `000064` 的 history、versioned link、active lease 阻止 used-down。

## Rclone 版本化与加密

- legacy 默认 `legacy_mutable`。Portable `versioned_prefix` 为独立 namespace，每次运行规范化清单并最后写 commit marker；弱/无哈希须逐字节核对源与目标，超过字节/时限失败关闭。
- 受管 Portable 使用加密持久化的同一份 bound config bytes/revision；当前运行时必须精确通过 `rclone v1.74.4` 校验，旧版、新版及 prerelease 不能自动视为兼容。`node_default` 只属于 legacy；不自动导入节点配置。拒绝动态凭据、未知选项、未认证 backend/wrapper 和不闭合依赖。变更版本须同步路径 codec 和实际适配器验证，不从命令名称推断支持。
- Native 仅限官方 AWS 区域端点的通用 S3 bucket；directory bucket、access point/Outposts、自定义/S3-compatible、Azure/GCS 不可冒充已认证 Native。实际 live 认证状态与支持矩阵必须分开报告，存在 opt-in 测试不等于通过。
- Native 使用同账户专用 IAM Role、信任策略强制 external ID 和覆盖单次操作时限的 STS 会话，不能用节点静态身份替代绑定。external ID 只在短期 setup 显示一次。两次 versioning/lifecycle/身份/capability 稳定观察至少间隔 15 分钟，并通过精确版本 canary。
- 预检绑定 Task/binding/credential/capability/lifecycle/encryption revisions 和有效期。任一漂移必须重做，不静默降级 Portable/mutable。
- Native 仅支持 `sse_s3` 或同账户 customer-managed `sse_kms_cmk`。后者有一个 active write key 和有界 decrypt-only read key ring；先保留旧读 key 再切写 key。任何 committed VersionId 仍引用旧 key 时必须保留可解密性。校验账户、区域、状态、用途、来源和权限，DTO/日志/审计不公开 ARN。Xirang 不创建、修改或删除 KMS key。
- 与 namespace 重叠的 current/noncurrent expiration、delete-marker cleanup、未知 lifecycle 动作或未认证离线转换都阻止准入；不自动修改 bucket lifecycle。Native `backend_versioned` 不等于 Object Lock/WORM。
- `first_new_point` 不改变旧目标，在任何 managed reservation/history/point/lease 之前可 clean rollback；`imported_baseline` 完整复制稳定旧 current head，并立即建立 durable reservation，不伪造历史，因此没有 clean rollback 窗口。窗口关闭后仅准备回退，保留 committed/failed/orphan/manifest/audit/latch，不删除远端字节、不自动恢复旧执行。

### 安全摘要的闭合组合

| mode/profile | KMS 状态与数量 |
|---|---|
| `legacy_mutable` 或 `versioned_prefix` + `none` | `not_applicable`，0 |
| `native_object_versions` + `sse_s3` | `not_applicable`，0 |
| `native_object_versions` + `sse_kms_cmk` | `ready|degraded|at_risk|blocked`，非负安全整数 |

后端 `Validate/SafeRclonePublicationSummary` 和前端 mapper 都原子校验整个组合。坏值/缺失/未来值整体投影为 Native、blocked、unsupported_profile、SSE-KMS、blocked、count 0；不得逐字段修补。未知 rollback capability 单独保守为 `preparation_only`，不使其他合法字段丢失。私有 ARN、bucket、prefix、VersionId、config、digest 不上 DTO。

## 受控恢复权威与运行时发布

`RecoveryEligibilityAuthority.ObserveRecoveryAuthority` 与 `RecoveryAuthorityRevalidator.RevalidateRecoveryAuthorityTx` 分别拥有有界外部观察和 caller 事务内的耐久事实重验。流程为短事务 capture → 事务外 SSH/SFTP/Repository pinned-tree/Processing artifact 观察 → 短锁定事务 revalidation。外部 I/O 不得放 DB 事务内。

每次 issuance 最终事务及后续 effect 事务都重验在 owner 返回后仍可变的事实。opaque source namespace observation 保留私有 closed durable snapshot 和 transaction-only revalidator，覆盖 Task source、source Node、source credential；Repository source revision 不能替代它。sealed digest 还绑定 target/policy/finding/reserve/overlap 产品，format/JSON/error 不泄露 locator/credential/revision/proof/依赖原值。

enabled Recovery 仅当 Repository、target 和真正 ready 的 Processing malware evidence lifecycle 存在，且 metadata reconciliation 完成后才发布。Processing disabled/control-plane-only/stopped/无 installed reader 时不得发布 admission graph；非空对象指针不够。disabled Recovery 可不依赖 Processing 发布 cleanup/logical reconciliation/receipt reaping 等 maintenance，但不能 admission/effect。已发布后 reader 故障使 effect fail closed，maintenance 仍可用。

`StartupWithConfig`、`TransitionSettingsWithRestore`、`TransitionCurrentWithRestore` 必须保持 graph owner。current-config mutation 仅已启动并安装 config/graph 后可用，启动前在 validate/persist/build/publish 前拒绝，zero config 不能隐式成为 disabled installation。target-root facade 仅给安全 `RecoveryTargetRootSummary`，exact encrypted row/absence rollback 用 Recovery 私有 opaque token。

旧 graph shutdown/join 无法证明时 unpublish、清 graph、sticky fence，跳过 persist/build/install。root mutation 成功但 candidate install 失败时先恢复 exact encrypted row/absence 再重建旧 graph；任一恢复/join 无证明就保持 nil publication 与围栏。只有 persistence 和 graph 都精确恢复才可重新准入。

回归覆盖两处漂移窗口（namespace observation 后、sealed issuance 后）× Task source/Node/credential，断言每 owner 外部观察仅一次、事务仅 durable comparison、零私有 canary；覆盖 Processing disabled/control-plane/ready/reader-failure 的生产组合、pre-start/current/disabled mutation、validate/drain/persist/construct/reconcile/install 各失败、不可 join owner、exact row/absence restore 与 sticky failure。执行 SQLite、required no-skip PostgreSQL authority selectors、normal/race 及 privacy/static 边界检查。

## 快照异常比较

Restic anomaly 在成功备份后的有界异步路径执行，不改传输结果。managed 模式只比较同 Task 精确血缘的相邻 committed 恢复点，不允许仓库 latest 推断；managed-history 关闭模式仍保持此边界。churn 使用排除当前 diff 的最近 10 条历史统计，至少已有 3 条历史 diff 后才比较均值加 3σ；勒索扩展名检查不等待该基线。事件默认记录，通知由 `anomaly.alerts_enabled` 明确控制。回归分别证明 insufficient baseline、当前样本排除、ransom 独立触发和共享仓库跨 Task 隔离。

## 回归证据

变更边界须覆盖：registry 未知/重复/缺端口；400/404/409/501/503/500 与隐私扫描；probe-first 无写入、绑定漂移、显式替换、断连/重连 revision、事务回滚与唯一性竞争；本地/SFTP traversal、symlink/race、特殊文件、root rename、Range、限额、取消和 close 错误；真实命令替身的严格 argv/schema/secret stdin 与无越权变更。

发布回归覆盖 Restic 最终 summary/tag/original、stored reconstruction、fence/deadline/backoff/worker shutdown、共享仓库隔离、Repository-only latch、所有 legacy 消费者；Rsync staged 硬链接关系和无旧点修改、迁移 CAS 超安全整数及同窗 activation→rollback；Rclone 所有合法/非法摘要组合及 safe projection。双引擎迁移、必需真实 PostgreSQL、race/重复测试不得以静态文本或 SQLite 成功替代。

Provider 回归还须单独证明固定版本正例与旧版/新版/prerelease 否定、动态配置/并发限额变更、未知 Range 能力拒绝、可变 observed head 原位更新，以及所有 stream 的 close 错误和取消能到达最终 owner。

源码入口：[Provider](../../../backend/internal/backupasset/provider/registry.go)、[Repository](../../../backend/internal/backupasset/repository/service.go)、[发布协调器](../../../backend/internal/backupasset/publication/contracts.go)、[版本化前端边界](../../../web/src/lib/api/tasks-api.ts)。
