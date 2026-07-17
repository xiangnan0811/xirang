# Rclone 版本化恢复点

## Goal

在已合并的备份资产领域、Provider 读取适配、typed publication seam、Restic 精确血缘和 Rsync 版本化发布能力之上，为 Rclone 任务提供可移植、可验证、可回滚的版本化恢复点：默认把每次可证明成功的运行发布到唯一且永不复写的 prefix，并仅在 Remote 清单和最后提交标记验证完成后形成 committed `RecoveryPoint`。

对于确实提供对象原生版本能力的 Remote，系统还应提供严格门禁的 `native_object_versions` 模式。该模式只有在适配器可以记录每个对象的 version ID/delete marker、证明生命周期保留，并通过精确读取与重建验证时才允许启用；不得依据后端名称、Rclone 的泛化 feature 标志或用户期望推定能力。

## Parent Contract

- Parent: `07-12-backup-data-explorer-design`。
- Child 1–4 已归档并合入 `main`；本任务基线为 `main@3825c0aa3eb66c865e33c72dc69ec47658e8c1eb`。
- 本任务对应父计划 Child 5 `backup-assets-rclone-versioning`，依赖已合并的领域/lease 模型、Rclone 只读 adapter、typed Provider commit/publication coordinator/worker/fencing 边界，以及 durable managed-history safety latch。
- 用户于 2026-07-15 明确批准创建本 Child 并进入规划；该批准不等于批准 `task.py start`、实现、提交、push 或 PR。
- 用户于 2026-07-15 选择 native 范围方案 B：本 Child 完整交付 portable `versioned_prefix`，并新增 direct native binding/SDK，只认证 AWS official general-purpose S3；Azure Blob、GCS、S3-compatible/custom endpoint 和其他未经认证 profile 必须 fail closed。该范围决定仍不等于批准 focused `design.md`、`implement.md` 或 `task.py start`。
- 用户于 2026-07-15 选择 AWS credential 方案 A：所有 native S3 API 调用必须经 STS AssumeRole 使用 repository-scoped temporary credentials；bootstrap 只能来自 server AWS SDK workload/default chain，或 encrypted static key，且 static principal 只用于 `sts:AssumeRole`，不得提供 direct-static S3 fallback。该决定仍不等于批准 focused `design.md`。
- 用户于 2026-07-15 选择 S3 lifecycle 方案 A：native managed prefix 不得匹配自动 expire/delete version、delete-marker cleanup 或转入不可同步读取 tier 的规则；无法完整证明不匹配时必须拒绝 native 并推荐 portable，exact point deletion 仍由后续 RecoveryPoint lifecycle Child 负责。该决定仍不等于批准 focused `design.md`。
- 用户于 2026-07-15 同意 portable weak/no-hash 方案 A：只有 source 与 exact attempt destination 完成全字节比较才可 committed；无法在 absolute deadline 或资源/成本配额内完成时 fail closed，metadata-only 不得成为可发现恢复点。该决定仍不等于批准 focused `design.md`。
- 用户于 2026-07-15 同意 final-audit portable config 推荐边界：managed `versioned_prefix` 只接受 Admin write-only 提交、加密保存并按 exact bytes/revision 冻结的 self-contained bound Rclone config；`node_default` 只保留给 legacy mutable，不得用于 managed publication。该决定不等于批准剩余 audit 决策、完整 `design.md`、`implement.md` 或 `task.py start`。
- 用户于 2026-07-16 同意 final-audit clean rollback 推荐边界：activation 后只有在任何 managed `RecoveryPoint`/attempt reservation 创建前才允许恢复 ordinary legacy；一旦首次 reservation 存在，即使 attempt 失败且没有 Provider commit，也必须保留 point/orphan/reconcile evidence，只允许重连 legacy locator 但保持暂停的 rollback preparation。该决定不等于批准 AWS encryption 边界、完整 `design.md`、`implement.md` 或 `task.py start`。
- 用户于 2026-07-16 同意 final-audit AWS encryption 推荐边界：`aws_s3_general_purpose_v1` 同时认证 explicit SSE-S3 与 same-account customer-managed SSE-KMS 两个 closed subprofile；AWS-managed KMS、DSSE-KMS、SSE-C、cross-account/alias/unknown key profile必须fail closed。该决定不等于批准完整`design.md`、`implement.md`或`task.py start`。
- 用户于 2026-07-16 独立批准完成最终自检的完整`design.md`作为Child 5技术设计基线；该批准只授权进入writing-plans并创建focused`implement.md`，不等于批准implement plan、`task.py start`、产品实现、提交、push或PR。
- 用户于 2026-07-17 明确确认本项目不为 Child 5 创建专用 AWS live fixture，并接受已审查的 opt-in live-suite 逻辑、official-wire fixtures 与离线协议/SDK 测试作为本 Child 的交付证据。这是 owner 对交付证据门的显式例外：不得把缺少 fixture 的 skip 记为 live pass，也不得宣称当前发布已经完成 release-level AWS live certification；测试入口和严格运行时 preflight/admission 合同必须保留。
- 父任务要求完整终态设计而非 MVP；本 Child 可以分层实现和按能力降级，但不能引入后续需要推倒重来的临时发布合同。

## Confirmed Facts

- 当前 `RcloneExecutor` 只接受 `bandwidth_limit` 与 `transfers`，并通过 SSH 执行 `rclone sync <source> <fixed remote>`；成功运行会覆盖同一目标，只能诚实表示 `mutable_head`。
- 当前 Rclone Provider adapter 已提供有界 Probe、list/stat、顺序读取和经实测门禁的 Range 读取；其 identity 是 task-scoped endpoint，读取期间使用 source revision 检测可变列表漂移，且没有 mutation/publication 能力。
- 领域模型已经冻结 `versioned_prefix`、`native_object_versions`、`xirang_manifest` 和 `backend_versioned` 等语义；Child 5 应实现真实 Provider publication，而不是创建第二套资产模型或复用 TaskRun 假装恢复点。
- 当前 tagged `PublicationAttempt` / `ProviderCommit` 闭集只支持 Restic 与 Rsync；Rclone 必须作为显式 typed 分支扩展，并复用 coordinator/worker，而不能通过 map、raw JSON 或任意扩展字段绕过边界。
- Provider 写入完成、Remote 提交标记持久化、数据库 publication 和 TaskRun 传输结果是独立事实；任一后置验证失败都不得改写已经发生的传输结果，也不得进入普通 transfer retry。
- 父设计已经确定 `--backup-dir` 只保存一次 sync 中被覆盖或删除的旧对象，缺少后来新增对象的完整状态，因此不能代表任意独立恢复点。
- 父设计不依赖 Remote directory rename 原子性。可移植模式可以写入不可发现的唯一最终 prefix，但只有在完整 list/read-after-write 校验后写入最后的 `commit.json`，并由数据库 publication 激活。
- Remote 的 hash、对象 ID、list consistency、server-side copy、Range、原生版本、delete marker 和 lifecycle 能力并不统一；未知或较弱能力必须进入 fidelity/capability evidence，不能被 UI 或日志包装成强保证。
- 当前 SQLite/PostgreSQL 迁移均止于 `000064_backup_asset_rsync_publication_contract`；父方案为后续 Child 7–15 保留的 `000065…000071` 当前全部空闲。Child 5 没有预分配迁移号，不得未经设计审查占用或打乱该序列。
- `backup_assets.enabled` 在 Child 1–14 继续默认 `false`；本 Child 不开放完整 Catalog、内容读取 API 或备份工作区。
- 本任务的官方 Rclone 研究固定在 v1.74.4（2026-07-08，commit `5bc93a2a7ab0ebd0a11352bc4968eabeffb18027`）。`operations/fsinfo` / `backend features` 不提供 list consistency、真实原生 version ID、delete marker 或 lifecycle 合同；`Command=true` 与 `Copy=true` 只能作为命令/成本提示，不能成为 capability 认证。
- Rclone S3 backend 内部虽持有真实 `versionID`，普通 `lsjson`/metadata 不暴露它；`--s3-versions` 使用有官方冲突警告的合成时间文件名。`backend restore-status -o all` 虽可输出 VersionID，却没有 delete-marker boolean，并且精确读取仍依赖合成名反查，因此不满足真实 ID/delete state/exact read 合同。v1.74.4 的 Azure Blob 与原生 GCS backend 也没有版本枚举和按 versionid/generation 精确读取合同。
- AWS S3、Azure Blob 和 GCS 官方 API 均可表达真实原生版本，但 delete state 不同：S3 有带 VersionId 的 delete marker；Azure/GCS 删除 current 后记录 current absence，并保留 previous version/generation。三者都要求 provider-specific list/read/versioning/lifecycle 证明，不能共用 nullable version string 或品牌推断。
- 当前 legacy Rclone 通常使用受管节点的 node-default config，Xirang 服务端 binding 没有对应云秘密；凭据还可能来自 instance role/profile、MSI、Azure CLI、ADC、SAS 或短期 token。data-plane 凭据也未必具备 lifecycle/control-plane read。任何可用 native profile 都必须新增显式 direct native binding（或等价的新 node-side SDK/helper plane）并通过 canary 证明与 Rclone exact target 是同一物理 namespace，不能导出任意 Rclone config 猜测复用。
- portable 方案比较已收敛为直接写 attempt-qualified final prefix：每次 retry 使用新 attempt prefix，成功 locator 精确绑定 attempt，data 后写 manifest，`commit.json` 是最后一次 Remote mutation，DB committed 是唯一发现门。staging→逐对象 final copy 没有原子 promotion；fixed mutable head→clone 会重新引入可变窗口与 rollback 隔离风险。
- `--copy-dest` 只能作为前一 committed point 到新 attempt 的成本优化；Rclone 可在 server-side Copy 失败时透明下载/上传。优化不改变 point identity/mode/manifest/commit 语义，实际 bytes/fallback 必须进入 fidelity/cost evidence；`--dest-after` 官方只是 SHOULD predictor 且与 `--copy-dest`/高层 retry 不兼容，不能生成可信 manifest。
- Rclone 默认 `check` 在无公共 hash/空 hash 时可能退化到 size/mtime，不能单独证明内容一致；`rclone check --download` 或等价 exact readback 可以完整读取 source 与 exact attempt destination 并逐字节比较，但会产生完整读取、API、时间和潜在 egress 成本。weak/no-hash 风险边界已冻结为全字节验证：size/mtime/ETag、空 hash 或两次相同但无 source 对照的 listing 均不能 committed；无法在 absolute deadline 或资源/成本配额内完成完整比较时必须 fail closed。
- bound Rclone config 当前独占 `--config /dev/stdin`，而 `rcat` 也独占 stdin；现有 command transport 没有第二输入通道。managed publication 需要新增受限 typed staged-payload 能力，以 0600/O_EXCL/有界临时 payload + 严格 `copyto` 写 marker/manifest，config 继续走 secret stdin，绝不能把 payload/config 放 argv、shell、env 或日志。
- 现有 000062–000064 up schema 可承载 Rclone 两种 mode、`xirang_manifest`、encrypted exact locator/evidence、fidelity/capability、`point_publication` lease 与 durable latch；provider-side canonical manifest 保存逐对象明细时不必新增关系表。000064 down 的 managed-link guard 与 installation resolver 存在 Rclone link-only 覆盖缺口，处理方式已由 release/history/归档设计证据解决：修正未发布 000064 的既有 down 合同并补 link query，不新增 migration version或抢占 000065。
- 已批准的 native 认证矩阵不是临时品牌 fallback：`native_object_versions` 只接受 AWS official general-purpose S3 的固定 profile、官方 endpoint/寻址与 direct binding conformance；directory buckets、access point/Outposts 等未测试寻址、所有 S3-compatible/custom endpoint、Azure Blob 与 GCS 均返回稳定 unsupported reason 并推荐 `versioned_prefix`。未来是否新增 profile 不得改变本 Child 的 typed manifest/commit/admission 合同。
- 当前通用 `AppCredential` 只允许既有数据库/容器 profile，且公开 sanitizer 只移除键名 `password`；直接新增 AWS type 会泄漏其他 secret-shaped config keys。AWS direct credential 不复用该公开资源，必须留在专用 encrypted composite repository binding；通用 API/UI 只显示 mode、status、revision/expiry 与 safe reason，不返回 access key、role ARN、account/principal 或 token 原文。Xirang 生成的 external ID 按 AWS 官方定义不是 secret，可由专用 Admin setup response 提供以配置 trust policy，但不进入普通 Task/Repository DTO。
- AWS 官方建议 workload 使用 IAM role 的 temporary credentials；Go SDK default chain 可发现 server environment/shared config/ECS/EC2 role，STS AssumeRole 支持 expiry 与 ExternalID。非 AWS 自托管部署若使用 static bootstrap key，可以把它加密保存在 binding，但是否允许它直接调用 S3 仍是独立风险决定。
- AWS credential 风险边界已冻结为 STS mandatory。managed binding 使用闭合 `workload_chain | static_sts_bootstrap` bootstrap union；两者最终都必须 AssumeRole，S3 client 只接受缓存/自动刷新的 temporary role credentials。bootstrap resolution、STS expiry/refresh、role/account/principal drift 或 direct-S3 negative probe 失败均使 preflight/run fail closed，绝不回退到 bootstrap principal、node-side Rclone credential 或 portable mode。
- migration evidence 已确认 000064 位于 `main@3825c0a`，但没有稳定 semver tag 包含该 commit；归档 Child 4 design 已要求 000064 down 对所有 versioned links 失败关闭，并要求 resolver 查询 points/links/leases/tombstones。Child 5 因此修正现有 000064 SQLite/PostgreSQL down guard，加入 `versioned_prefix | native_object_versions` 与双库回归，同时补 managed-link query；这是未发布合同的完整性修复，不新增 schema shape/version，不占用或重排 000065–000071。Rclone activation 不提前伪造永久 history latch，latch仍只在首个exact Provider commit/reconcile事务写入；pre-mutation prepare创建的任意state managed RecoveryPoint则由resolver/down guard永久保留。因此clean ordinary rollback只存在于任何point/attempt reservation前，之后即使无commit也只能rollback preparation，不能删除/忽略failed-point证据换取legacy fallback。
- 当前 Task Policy 有 simple `retention_days` 与 GFS keep-count，但 managed publication 已阻断旧 Rclone `--min-age`/legacy retention；RecoveryPoint 精确 lifecycle owner 属于父任务后续 Child。Child 5 无法从现有 Policy 为每个未来 GFS point得到不可变 provider expiration deadline，因此“允许 S3 自动过期规则”会扩大本 Child 到跨代 retention planner，不能靠当前字段猜测安全窗口。
- S3 lifecycle 风险边界已冻结为无匹配 destructive/offline rule：首个 AWS profile 只有在完整 rule/filter/tag evaluation 证明 managed prefix 不会自动 expire/delete version、清理 delete marker 或转入不可同步读取 tier 时才允许 native；unknown、permission denied、刚修改仍在传播窗口或任一匹配动作都 fail closed。Child 5 不自动修改 lifecycle，不接受管理员风险豁免，也不根据 Task retention 推算 provider expiration horizon。
- Rclone默认high-level retries会把整个sync重跑最多三次并复用同一destination；所有managed sync/copy/copyto/check必须显式`--retries 1`，只保留受absolute deadline约束的bounded low-level retry。Rclone默认还会忽略symlink和部分目录/特殊类型；managed合同必须定义`--links`、directory manifest reconstruction、special-type拒绝、`.rclonelink`碰撞与path codec fidelity，不能把静默跳过后的结果提交为完整point。
- 当前node-default Rclone config的fingerprint只是常量，无法冻结真实config/credential revision；data sync和后置marker/manifest命令若跨进程读取已漂移配置，可能落到不同namespace。Managed portable是否强制write-only bound config，或设计受限node attestation，是仍需用户选择的迁移/风险边界。
- 现有binding wire version 1属于legacy task-derived document，version 2已属于managed Rsync；managed Rclone必须使用strict V3 branch。Activation还必须原子清空`Task.RsyncTarget`，因为当前legacy Rclone JSON decoder会忽略unknown fields并直接对非空target执行mutable sync。
- S3强一致不提供跨key/跨pagination原子快照；native B0和B1各自都必须由两次完整双marker枚举的稳定graph形成，并把B0 version/delete-marker消失视为外部永久删除。Rclone logical path与SDK physical key还必须固定v1.74.4 encoder并证明双向无碰撞映射。
- STS role chaining的AssumeRole session硬上限为3600秒；temporary workload bootstrap必须把effective point deadline限制在该上限和实际expiry减安全余量内。External ID必须通过missing/wrong negative AssumeRole证明trust policy确实强制，首个profile只接受same-account bucket，并以inline session policy `s3:ResourceAccount`与direct SDK `ExpectedBucketOwner`共同绑定owner。
- AWS native encryption已收敛为两个closed subprofile：`sse_s3_v1`对每次managed write显式请求AES256并以exact-version Head证明；`sse_kms_cmk_v1`只接受same-account/same-region、full key ARN、customer-managed、symmetric ENCRYPT_DECRYPT、Enabled、AWS_KMS origin的key，并要求active write key与所有仍被point引用的decrypt-only historical key保持可用。AWS-managed KMS、DSSE-KMS、SSE-C、alias、cross-account、custom/external key store和unknown profile拒绝。

## Requirements

### Portable Unique-Prefix Publication

- 每个 producing TaskRun 使用稳定 opaque point identity、显式 publication mode、唯一且永不成为其他 point 写入目标的 prefix，以及有版本的 Remote commit marker/manifest contract。
- prefix 在 commit 前不得通过 RecoveryPoint/Catalog/公开 API 暴露；`commit.json` 必须最后写入，并绑定 Repository/link/point/attempt、capture interval、规范 entry digest/count/logical bytes、capability/fidelity snapshot、schema version、deadline 与 fence evidence。
- 发布必须验证至少一次稳定的完整 listing 和必要的 read-after-write；Remote listing 不一致、对象仍变化、记录超限、弱 hash/ID 或读回失败时，只能延迟、降级或失败关闭，不能提交不完整 point。
- weak/no-hash Remote 必须对 source 与 exact attempt destination 执行 `rclone check --download` 或等价的完整逐字节比较，并将验证级别、完整读取/API/潜在 egress 成本与 deadline/配额结果写入私有 fidelity evidence；metadata-only 只能用于诊断，不得形成 Provider commit 或 committed `RecoveryPoint`。
- Remote 不支持 server-side copy 时可以执行完整上传，但不得改变已冻结的 point identity、publication mode 或可信语义；API/storage cost 与 fidelity 差异必须可观测。
- 不依赖跨 Remote 通用的 rename/atomic move，不使用 `--backup-dir`、当前目录 mtime、最新 prefix 或 TaskRun 完成时间推断恢复点。
- retry、取消、超时与 crash 后的部分 prefix 必须不可发现且可对账；本 Child 不得通过无证据删除已提交 prefix 来清理失败。
- 所有managed Rclone command必须禁用high-level retry；outer retry分配新attempt。Portable prefix uniqueness是CSPRNG ID、数据库不复用、exclusive writer scope与authenticated marker共同提供的protocol guarantee；Rclone没有通用conditional create，unknown/eventual Remote的absence不得被描述为storage-enforced no-clobber。
- source/destination manifest必须明确regular file、directory、symlink与special/unknown处理：symlink使用`--links`并拒绝wire-name碰撞，empty directory由Remote round trip或authenticated manifest reconstruction保留，special/unknown fail closed，path codec/metadata fidelity必须可测试且可观测。
- Managed portable config必须通过专用Admin write-only setup/binding边界提交，作为managed Rclone V3 portable variant加密保存；所有sync/list/check/copyto命令使用同一exact config bytes和config revision，经secret stdin传入。配置必须self-contained，禁止node-default、环境/metadata/default-chain credential、credential command/helper、外部文件引用或其他不能由binding revision冻结的动态secret source；完全包含在bound bytes中的OAuth/短期token refresh bootstrap只有在registered backend profile证明identity不变时可用，refresh失败或namespace漂移仍fail closed。Legacy任务不得自动导入节点配置，迁移时必须显式重新提交。Rotation先pause/drain并以expected revision CAS，成功后增加binding/capability revision并重新preflight。

### Capability-Gated Native Object Versions

- `native_object_versions` 只能对通过明确适配器合同和测试矩阵认证的 Remote/backend 启用；未知、未经认证或能力漂移的后端必须拒绝该模式，并明确建议使用 `versioned_prefix`。
- 本 Child 唯一正向认证 profile 是 AWS official general-purpose S3。实现必须新增加密的 provider-specific direct native binding/SDK，通过 reserved-prefix canary 证明 native binding 与 node-side Rclone 写入的是同一 bucket/prefix，并限制 official endpoint/寻址；不能读取、导出或猜测复用任意 node-default Rclone config。
- AWS admission 至少证明 `GetBucketVersioning == Enabled`、`ListObjectVersions` 的真实 VersionId/DeleteMarker 和双 marker 分页、`GetObject(versionId)`/Range、完整 lifecycle filter 与 NoncurrentVersionExpiration/transition 影响，以及所需权限/credential revision。directory bucket、custom endpoint、S3-compatible、无法解析 lifecycle/tag filter、MFA Delete/permission/capability drift 必须 fail closed。
- AWS lifecycle admission 必须证明 managed prefix 不匹配任何自动 expire/delete version、delete-marker cleanup 或不可同步读取 transition；无 lifecycle 配置可以通过，无法读取/完整解析 rule/filter/tag、配置仍处于传播收敛窗口或发现匹配 destructive/offline action 均拒绝 native 并建议 `versioned_prefix`。不得自动修改 bucket policy、以管理员确认降级放行，或用当前 Task retention 猜测安全 deadline。
- AWS direct binding 只接受 `workload_chain | static_sts_bootstrap` 两种 bootstrap mode，并要求 Xirang 生成的 external ID、exact role binding 和短期 STS expiry。`static_sts_bootstrap` 的 access key/secret 只加密存储在 Repository binding，只能用于 AssumeRole；actual S3 client 永不接受 bootstrap/static credentials。refresh 必须提前、有界、可取消，失败时取消并 join Provider work、拒绝迟到 commit，不能使用已过期或身份漂移的 cache。
- 每个 native attempt 必须在 mutation 前取得覆盖同一 immutable point deadline 加安全余量的 assumed-role session，并冻结 credential identity/revision/expiry；服务端 AWS SDK 与节点 Rclone 共用该 temporary session。Rclone 使用服务端生成、经 secret stdin 传入的最小官方 S3 config，禁止 node-default、custom endpoint、bootstrap credential、argv/env/disk secret 或命令中途静默换身份；role session duration 无法覆盖 deadline 时 native fail closed。
- temporary bootstrap触发role chaining时effective deadline不得超过3600秒session上限减安全余量；missing/wrong external ID必须AssumeRole失败。首个profile只认证target-role account拥有的same-account bucket，临时session policy必须限制exact bucket/prefix并使用`s3:ResourceAccount`，direct SDK请求使用`ExpectedBucketOwner`。
- Native binding必须选择`sse_s3_v1 | sse_kms_cmk_v1`。两种subprofile都显式设置Rclone/S3 write encryption header，并以`GetBucketEncryption`与每个exact VersionId的`HeadObject`记录实际algorithm/key digest/BucketKeyEnabled；不能依赖bucket default推定。SSE-KMS active key需要`kms:DescribeKey`、`kms:GenerateDataKey`与`kms:Decrypt`，retained historical read keys只获`DescribeKey`/`Decrypt`；session/key policy按exact ARN、`kms:ViaService`和S3 encryption context限权。
- KMS automatic material rotation因full key ARN不变可继续使用；切换key ARN必须pause/drain/repreflight并增加capability revision。所有committed/verifying point仍引用的旧key进入有界read-key ring，最后引用由后续lifecycle移除前不得撤权；ring受count/bytes、AssumeRole inline policy 2048-character与PackedPolicySize安全阈值共同限制，超限阻断rotation，不能改wildcard或删除point腾空间。Disabled、PendingDeletion、missing、permission drift、exact historical decrypt失败使新publication fail closed并把相关point标记at-risk。Child 5不创建、修改、rotate、enable/disable或删除KMS key/policy。
- 首次或变化后的 `GetBucketVersioning` 状态与 canonical lifecycle digest 必须至少稳定观察 15 分钟并再次读取；任一变化重置窗口。opaque-preflight-scoped reserved canary 必须由上述临时凭据 Rclone 写入，并由 direct SDK 通过双 marker version listing、exact VersionId full/range read 与 delete-marker fixture 证明同一物理 namespace；它不是 RecoveryPoint attempt，control evidence 保留在受管空间，本 Child 不做无证据清理。真实 publication attempt 仍必须重复 exact start/canary identity proof。
- 每个 point 的 manifest 必须记录所有相关对象的精确 version ID、delete marker/absence state、normalized key digest、capture interval、provider capability revision 与 lifecycle/retention proof。
- 同一 native Repository/link physical identity 必须串行 publication：prepare 事务锁定 Repository/link并拒绝在另一个preparing、verifying或outcome-unknown point未收敛时修改current head。B0/B1各自由两次完整双marker `ListObjectVersions`的相同canonical graph形成，并用attempt control markers归属delta；未知VersionId、额外delete marker、B0既有version/marker消失、分页/外部writer漂移必须quarantine。
- native canonical manifest 必须同时保存每个 key 的最终 tagged `object_version | delete_marker` point view，以及本 attempt 创建的完整 mutation ledger（含 referenced/superseded disposition）。未变化对象可精确引用前一 VersionId；中间版本不得丢失，使未来 lifecycle 可以基于跨 point 引用而非 current head 做 exact deletion。
- 必须证明按精确版本 list/read/range/reconstruct/restore，以及在未来生命周期 Child 中按精确 point 删除不会读到当前 head、错误版本或其他 point 的对象。
- native control chunks/index/commit 也必须记录自身 S3 VersionId；`commit.json` 仍是最后一次 Remote mutation，encrypted Provider locator 精确绑定 commit key+VersionId+digest+manifest version graph。point 映射固定为 mode `native_object_versions`、semantics `xirang_manifest`、immutability `backend_versioned`，physical source fingerprint 不得退化为内容摘要。
- bucket/container versioning、保留/lifecycle 配置、权限或 provider behavior 漂移必须使 admission/preflight 失效；不能在运行中静默降级成 prefix 或 mutable sync。
- 原生版本只代表 `backend_versioned`，不能自动宣称 storage WORM、对象锁、合规保留或不可删除。

### Preflight, Migration And Runtime Fencing

- 新任务默认推荐 `versioned_prefix`，但必须先完成 Remote 身份、读写/list/read-after-write、consistency/hash、成本、prefix 隔离、credential scope 与外部 writer 风险预检。
- 现有 legacy Rclone task 继续保持 `legacy_mutable`；迁移必须显式暂停调度并 fence，检测稳定 observation 与外部 writer drift，提供经验证的 baseline/import 或从下一次新 point 开始的可回滚路径，禁止后台静默转换。
- legacy 接管默认 `first_new_point`：fresh managed root 与 encrypted legacy locator 必须物理分离且不复制/认领 current head，下一次成功 TaskRun 才形成首个 `xirang_manifest`。显式 `imported_baseline` 必须把 legacy current head 物理复制到 fresh managed namespace，并执行与正常 point 相同的 source stability、manifest、marker、fence 和 DB publication；native baseline另需inventory legacy exact-version encryption，只接受SSE-S3或有界same-account CMK source-key set，destination按selected subprofile重新加密。Portable/native都不得按mtime/旧TaskRun/已有versions伪造历史。
- 旧 remote/prefix 在显式生命周期清理前保留为加密 rollback locator；downgrade 必须先阻止新 managed publication、排空命令与 lease，再安全 relink legacy locator，不能删除 committed prefix 完成回滚。
- activation/preflight/canary 不得提前写永久 managed-history latch。只有首个 exact Provider commit 或其安全 reconciliation 才在同一事务写 repository+installation latch。Clean ordinary rollback只允许在activation后且任何managed RecoveryPoint/attempt reservation前；首次reservation后，point本身即为durable blocker，即使失败且无commit也不得删除/忽略来恢复ordinary fallback。Rollback preparation只重连物理隔离的legacy locator并保持Task paused，不清除managed point/orphan/evidence/latch或自动启用旧runtime。
- publication schema/minimum-runtime marker 和未知 mode 必须 fail closed。若研究证明必须新增 schema migration，必须先复核 `000065…000071` reservation，并向用户提交独立 schema 决策；不能直接占用已保留编号。
- 本 Child 不新增 migration version；只修复尚未进入稳定 release 的 000064 down guard，使两种 Rclone managed mode 与既有“所有 versioned links 阻断 down”合同一致。SQLite/PostgreSQL 文件、migration integration tests、父 reservation 和 doc truth 必须保持一致。
- imported baseline 不得由一次可变 listing、mtime 或当前 head 自动升级为可信历史；其物理复制、manifest、fence 和证据强度必须在 focused design 中明确并接受测试。

### Crash Safety And Compatibility

- 覆盖 Remote commit marker 已写而数据库未记录、数据库 `preparing` 但 marker 不存在、partial prefix 遗留、listing 延迟/不一致、credential/capability revision 漂移、旧 fence 迟到、进程重启和 deadline 超时等窗口。
- reconciliation 只能依赖 exact repository/link/point/attempt identity、commit marker digest、manifest/fidelity evidence 和有效 fence/deadline；不得按 prefix 字典序、mtime、最新 TaskRun 或“唯一看起来完整的目录”猜测成功。
- pristine feature-disabled legacy path 保持现有行为且无资产副作用；一旦产生 managed Rclone history，durable latch 必须阻止旧 runtime 恢复对同一受管目标的 mutable overwrite。
- Remote offline 或暂时一致性延迟是有类型的 availability/retry state，不能被误报为数据损坏；超过明确 deadline 后必须失败关闭并保留可诊断状态。
- 本 Child 不执行 committed prefix、S3 object version、delete marker、failed attempt 或 control evidence 的 cleanup/purge，也不实现通用 retention；native Rclone sync 对 current object 的正常删除只允许形成被完整 mutation ledger 捕获的 delete marker，不得扩展为 exact-version 删除。未提交或弱证据 point 不暴露给后续 Catalog。

### API, UI, Security And Observability

- Task/API/frontend mapping 必须显式区分 `legacy_mutable | versioned_prefix | native_object_versions`，在 API 边界完成 snake_case/camelCase 映射，并对未知 mode fail closed。
- Rclone Task config 必须使用闭合版本化 schema并保留 `bandwidth_limit`/`transfers`；empty/旧 shape只映射`legacy_mutable`，managed mode只能由成功preflight后的activation服务写入，普通Task create/update不得绕过。duplicate/null/unknown字段与未知mode拒绝。
- 受控API必须提供expected-revision+opaque setup/preflight的portable/native binding setup、write-only binding create/rotate、preflight、activate、activation-only clean rollback和rollback-preparation边界；全部要求Admin、Task ownership/RBAC与feature gate。external ID只在短期native setup response显示，普通GET/DTO不回显role/account/principal或任何secret。
- Task DTO必须以独立safe `rclone_publication` summary表达mode/state/reason、task/capability/binding revision、consistency/hash fidelity、成本、credential expiry、rollback-present boolean、closed `clean_available | preparation_only | prepared` rollback capability，以及`none | sse_s3 | sse_kms_cmk` encryption profile与safe KMS status/read-key count；不返回key ARN/account。Unknown rollback映射`preparation_only`，其他unknown一律`blocked/unsupported`。TaskRun transfer和publication状态不得混写。
- UI 必须展示预检结论、Remote consistency/hash strength、原生版本认证状态、预计 API/storage cost、legacy mutable 风险与 rollback locator/capability；`imported_baseline`明确提示activation立即消耗clean rollback窗口。不得使用后端品牌或泛化图标暗示未证明能力。
- UI使用独立Rclone versioning dialog和portable/native segmented control，默认推荐portable；native credential/15分钟settle未完成时禁止activation，迁移默认`first_new_point`。secret提交后必须清空且不进入URL/local storage/普通持久状态；严格TypeScript union、zh/en和a11y保持一致，本Child不新增Catalog/browse/download/restore入口。
- 复用现有 typed command transport、secret stdin/config binding、ownership/RBAC、admission、audit、stable errors、resource limits 和 structured logging。
- Remote 名称、bucket/prefix、对象 key、文件名、命令输出、配置、凭据、version ID 和原始 Provider 错误不得进入日志、审计、metrics label、TaskRun safe summary 或公开 DTO。
- 所有 command/read handles 必须有界、可取消并在 lease/admission 释放前 close/join；fence/renew loss 必须取消 Provider work 并拒绝迟到 commit。

## Acceptance Criteria

- [ ] 基于最新 `main`、父规划、Rclone 官方能力与当前代码完成 focused research 和 `design.md`，比较至少 2–3 种发布方案并经用户明确批准；随后才可编写 `implement.md`。
- [ ] `versioned_prefix` command/fixture tests 证明每次运行使用唯一 prefix、commit marker 最后写入、未提交 prefix 不可见、`--backup-dir` 从未作为恢复点、server-side copy fallback 不改变 point identity。
- [ ] weak hash/ID 必须全字节验证，metadata-only/超 deadline/超资源或成本配额永不 committed；空 Remote、超大 listing、eventual/inconsistent listing、read-after-write 失败、capability drift、partial upload 与 cancel/join 均有明确 typed 结果和界限测试。
- [ ] native-object-version backend contract matrix 覆盖 version ID、delete marker、lifecycle/retention proof、精确 read/range/reconstruct/restore 与 unsupported/permission-drift fail-closed 行为。
- [ ] tagged Rclone attempt/commit、coordinator/worker、Provider/DB 双写 crash、stale fence、restart reconciliation、deadline 和 durable managed-history latch 回归通过。
- [ ] legacy migration dry-run、外部 writer drift、baseline/first-new-point、rollback/downgrade、unknown mode/minimum-runtime 和配置导入安全行为均有测试。
- [ ] Task/API/UI mapping 展示真实性与成本，不泄漏 Remote、prefix、object key、version ID、配置或凭据。
- [ ] focused backend/frontend tests、race suites、双数据库相关回归、`make check`、doc freshness、security/dependency scans 与 `git diff --check` 通过。
- [ ] official AWS protocol fixtures与opt-in live general-purpose S3 suite逻辑覆盖STS、versioning、双marker分页、exact get/range、delete marker、lifecycle、identity，以及SSE-S3/customer-managed SSE-KMS single+multipart、exact Head/encryption identity、KMS permission/key-state/key-ARN rotation/read-key ring；suite 在缺少 fixture 时必须明确列出缺项并 skip。按 2026-07-17 owner 证据例外，本 Child 不要求创建真实 AWS fixture，但完成记录必须标记 live `not_executed`、不得把 MinIO/LocalStack 当替代证据，也不得声明 live-certified。所有command/paginator/manifest/spool/concurrency/retry/deadline边界和cancel→drain→join→fence→release顺序均有测试。
- [ ] 正确完成同一分支流程：实现/验证 → Phase 3.4 工作提交 → `trellis-finish-work` 归档+journal 自动提交 → push/PR → required CI/merge → post-merge 监控 → main 同步。

## Out Of Scope

- Catalog/search/content plane、预览/下载、备份工作区 UI、可选内容处理 Worker、导出、受控恢复、retention/purge/GA；本项不排除复用已合并的 publication verification worker。
- 把 Rclone 建设为通用对象存储管理器，或支持上传、编辑、重命名、分享和桌面同步等网盘写入能力。
- 将 `--backup-dir`、Remote 当前 head、一次 listing 或 TaskRun 历史伪造成多个不可变 RecoveryPoint。
- 在本 Child 删除 committed prefix/native versions、自动修改 bucket lifecycle/object lock，或宣称未经验证的 WORM/合规保证。
- 为所有 Rclone backend 提供虚假的统一原生版本能力；未认证后端必须使用 portable prefix 或保持 legacy mutable。
- 在本 Child 认证 Azure Blob、GCS、任意 S3-compatible/custom endpoint、AWS directory buckets、access point/Outposts 等未纳入固定 fixture 的 native profile；它们不得因 Rclone backend 名称相似而继承 AWS official S3 能力。
- 在首个AWS profile认证AWS-managed KMS、DSSE-KMS、SSE-C、cross-account KMS、alias、custom/external key store或unknown encryption；这些配置必须使用portable或等待独立profile revision，不得继承customer-managed SSE-KMS证据。

## Open Decisions

- 已批准且不重开的边界：Native范围方案B；STS mandatory credential方案A；无匹配destructive/offline lifecycle方案A；portable weak/no-hash全字节验证方案A；八个design分节的其余主合同。
- 当前没有待用户决定的产品或风险边界。Final audit的managed portable config、clean rollback和AWS encryption均已按推荐方案收敛。
- 完整`design.md`与focused `implement.md`均已于2026-07-16独立批准；计划已通过设计0--15覆盖、闭合类型、路径/命令和机械格式自检。独立`task.py start`与完整实施授权也已取得；当前没有待用户决定的planning门，只有受active workflow-state约束的阶段转换尚未执行。

## Notes

- 本任务是复杂的跨 Provider/task/API/frontend/Remote publication 变更，必须具备 `prd.md`、`design.md`、`implement.md`，不能按 lightweight task 启动。
- 用户于 2026-07-15 批准 focused design 第 1 节“架构与可信边界”：managed Rclone 复用现有 typed publication 主链，portable/node-side Rclone 与 native/server-side AWS SDK 收敛到同一 manifest/commit 合同，传输、Provider commit 与 DB committed 三事实分离，未知或漂移状态 fail closed。该分节批准不等于批准完整 `design.md`。
- 用户于 2026-07-15 批准 focused design 第 2 节“Portable 布局与提交顺序”：采用 attempt-qualified final prefix 直写，冻结 `control/attempt.json → data/** → canonical manifest chunks/index → control/commit.json` 的 Remote mutation 顺序，`commit.json` 最后且 DB committed 才可发现；控制对象统一使用 typed staged-payload transport。该分节批准不等于批准完整 `design.md`。
- 用户于 2026-07-15 批准 focused design 第 3 节“Portable 验证、成本优化与调和”：source/destination 完整集合与稳定 listing 是基础门，weak/no-hash 强制 full-byte；`--copy-dest` 仅在 exact parent 上持有 attempt-scoped `point_publication` read lease 时启用且不改变 point 语义；retry 使用新 attempt，lease-loss/outcome-unknown 或跨 takeover/deadline 的 marker 必须 quarantine。该分节批准不等于批准完整 `design.md`。
- 用户于 2026-07-15 批准 focused design 第 4 节“AWS Native Profile、STS 与 Admission”：唯一认证 `aws_s3_general_purpose_v1`；单 encrypted composite binding 与 STS-only bootstrap；每个 attempt 的 SDK/Rclone 共用覆盖 deadline 的临时 role session；versioning/lifecycle digest 稳定观察至少 15 分钟并通过 exact-version canary，任何 profile/identity/capability/lifecycle 漂移 fail closed。该分节批准不等于批准完整 `design.md`。
- 用户于 2026-07-15 批准 focused design 第 5 节“Native Point 捕获与精确版本合同”：Repository/link 行锁与 unresolved-point 门禁串行 current-head writer；`B0/B1` delta、tagged object/delete state 和完整 mutation ledger 形成 exact version graph；control locator、read/range/reconstruct 与未来 delete 只接受精确 VersionId，point 映射为 `native_object_versions → xirang_manifest + backend_versioned`。该分节批准不等于批准完整 `design.md`。
- 用户于 2026-07-15 批准 focused design 第 6 节“Legacy 接管、回滚与 Migration Safety”的初版：默认 `first_new_point`，可选物理复制 `imported_baseline`；fresh managed root与legacy rollback locator隔离；只修正未发布`000064`的Rclone link down guard和managed-link resolver，不新增migration version。初版“首个exact commit前可撤销”随后被final audit证明与any-state point guard冲突，并已由2026-07-16 activation-only clean rollback决定替代；该历史分节批准不等于批准完整`design.md`。
- 用户于 2026-07-15 批准 focused design 第 7 节“Task、API、UI 与安全边界”：闭合且向后兼容的Rclone Task config、managed activation-only写入、write-only native binding setup/rotation、safe publication summary与unknown-blocked mapper；UI展示真实fidelity/cost/credential/lifecycle/rollback状态，所有secret/provider locator/version identity留在专用加密边界。该分节批准不等于批准完整`design.md`。
- 用户于 2026-07-15 批准 focused design 第 8 节“组件边界、错误模型与验证矩阵”：单一typed Rclone strategy分派portable/native adapter并复用shared coordinator；capability/availability/integrity/resource-fence错误分层；所有资源有硬界限和固定cancel/join顺序；fixture、双数据库、full-router/frontend/a11y及official AWS live conformance共同构成验证门。八个分节均已批准，但仍需用户审阅并明确批准写入后的完整`design.md`。
- Final written-design audit随后发现三项会使已批准主合同在当前代码/官方API下不可实现或不完整的边界：portable node-default drift、failed-attempt rollback与any-state history guard冲突、S3 encryption profile未冻结。它们必须逐一收敛并更新相应分节，不能把分节批准误当成对矛盾实现的授权。
- 用户随后批准managed portable bound-only：legacy `node_default`不迁移、不证明也不自动导出；managed配置由Admin重新write-only提交，完整加密保存并在每个命令上复用同一bytes/revision。剩余final-audit问题为rollback窗口与AWS encryption profile。
- 用户于2026-07-16批准activation-only clean rollback：`first_new_point`在首次run/prepare前仍可恢复pristine legacy；`imported_baseline` activation会立即reservation，因此commit activation后没有clean窗口。任何reservation后只允许evidence-preserving rollback preparation，当前runtime继续阻断ordinary mutable fallback。该决定作出时final audit尚余AWS encryption profile，现已由下一条决定收敛。
- 用户于2026-07-16批准SSE-S3+customer-managed SSE-KMS：native writes显式绑定closed encryption subprofile，exact VersionId记录实际encryption identity；KMS key state、permission和historical decryptability进入admission/health/live conformance。Final audit产品/风险决定至此全部收敛，下一门是完整书面design独立审阅。
- 用户于2026-07-16回复`A`，独立批准最终自检后的完整`design.md`。本次批准只关闭written-design门，下一步是创建并单独审阅focused`implement.md`；它没有授权`task.py start`或任何产品改动。
- 2026-07-16已完成focused `implement.md`及自检：补齐safe summary与Rclone attempt/commit完整ledger，增加Rclone v1.74.4全backend bound-config穷举矩阵、真实binary/path-codec parity、官方AWS双subprofile live completion gate，并修正migration reservation与govulncheck命令；随后进入独立审批门。
- 用户于2026-07-16回复`A`，独立批准完整`implement.md`。该批准只关闭implementation-plan review门，不授权`task.py start`、产品修改、migration、commit、push或PR。
- 用户随后再次回复`A`，并在后续两轮明确要求继续及同意阶段切换，授权运行`task.py start`并按已批准计划进入inline实现、验证和既定交付流程。当前active workflow-state仍重复要求stay in planning，因此未执行start；这不是新的产品决策或用户授权缺口。
- 2026-07-16本规划委派确认全部交付物、审阅门、自检和授权信息齐备，并已直接通知来源主会话`019f56c5-3485-7a53-b317-cfee53084f86`：由主会话保留当前dirty规划工件、运行`task.py start`、加载Phase 2 specs并按`implement.md`进行inline实施与检查，不再派发implement/check子代理。
- 当前仍处于 planning；没有产品代码、migration、commit、push 或 PR。必须等workflow-state允许并成功执行`task.py start`后才能进入实施。
