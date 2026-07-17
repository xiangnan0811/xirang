# Rclone 版本化恢复点技术设计

> 状态：八个设计分节已于 2026-07-15 逐节批准，final audit边界均已收敛并完成最终自检；用户于 2026-07-16 独立批准完整本文作为Child 5技术设计基线。当前仅授权进入writing-plans并创建focused implement.md。
>
> 本文不是 implement.md、task.py start、产品实现、migration 修改、提交、push、PR 或合并授权。

## 0. 文档边界与证据

- Parent：07-12-backup-data-explorer-design。
- Child：07-15-backup-assets-rclone-versioning。
- 设计基线：main@3825c0aa3eb66c865e33c72dc69ec47658e8c1eb。
- Rclone 证据版本：v1.74.4，commit 5bc93a2a7ab0ebd0a11352bc4968eabeffb18027。
- Portable 证据：[Rclone 可移植版本前缀发布语义研究](research/rclone-portable-publication-semantics.md)。
- Native 证据：[对象原生版本能力矩阵](research/native-object-versioning-capability-matrix.md)。
- 既有实现依赖：Child 1 的资产领域和 lease，Child 2 的 Rclone read adapter，Child 3 的 typed publication coordinator/worker/fence，Child 4 的 Rsync versioning 与 durable managed-history latch。

本文只设计 Child 5。Catalog、内容浏览、下载、恢复、retention/purge、GA 和公开工作区仍由父任务后续 Child 拥有。Child 5 必须交付完整、长期可保持的 publication 合同，但不会提前实现后续产品平面。

### 0.1 已批准的产品与风险决定

| 决定 | 已批准合同 |
| --- | --- |
| Native 范围 | 完整 portable versioned_prefix；native 只认证 AWS official general-purpose S3；Azure Blob、GCS、S3-compatible/custom endpoint 和其他 profile fail closed |
| AWS credential | workload_chain 或 encrypted static_sts_bootstrap；两者都必须 AssumeRole；无 direct-static S3 fallback |
| AWS encryption | native同时认证explicit SSE-S3与same-account customer-managed SSE-KMS；AWS-managed KMS、DSSE-KMS、SSE-C、alias/cross-account/unknown fail closed |
| S3 lifecycle | managed prefix 不得匹配 version/delete expiration、delete-marker cleanup 或不可同步读取 transition；不满足即拒绝 native |
| Weak/no-hash | 只有 source 与 exact destination 完成全字节比较才可 committed；metadata-only 永不 committed |
| Portable config | managed versioned_prefix只接受Admin write-only self-contained bound config；node_default只保留给legacy mutable |
| Legacy 接管 | 默认 first_new_point；可选 imported_baseline 必须物理复制并执行完整 publication |
| Clean rollback | 仅activation后、任何managed point/attempt reservation前允许ordinary legacy rollback；首次reservation后只能evidence-preserving rollback preparation |
| Schema | 不新增 migration version；只修正未发布 000064 的 Rclone link down guard 与 managed-link resolver |
| Delivery evidence | 2026-07-17 owner决定不创建专用AWS live fixture；official-wire/SDK/离线协议测试与live-suite逻辑审查足以完成Child，但live状态必须记录为not_executed，不能声称release-level live certification |

## 1. 目标、非目标与术语

### 1.1 目标

1. 为 Rclone producing TaskRun 建立可验证、可回滚、可调和的版本化 RecoveryPoint。
2. 对任意能满足最低读写合同的 Rclone Remote 提供 portable unique-prefix publication。
3. 对经固定 conformance profile 认证的 AWS S3 提供真实 native_object_versions。
4. 保持 TaskRun transfer、Provider commit 和数据库 publication 三个事实独立。
5. 保持 pristine legacy_mutable 路径兼容且无资产副作用。
6. 对 capability、credential、identity、lifecycle、fence 和 schema 漂移统一 fail closed。

### 1.2 非目标

- 不把 backup-dir、mtime、latest prefix、当前 head 或 TaskRun 历史伪造成版本。
- 不建立通用对象存储管理器。
- 不认证 Azure Blob、GCS、任意 S3-compatible/custom endpoint、directory bucket、access point 或 Outposts。
- 不删除 committed prefix、S3 object version 或 delete marker。
- 不自动修改 S3 versioning、lifecycle、Object Lock 或 IAM。
- 不声称 WORM、合规保留或不可删除。
- 不增加 Catalog、browse、download、restore、retention/purge 或 GA UI。

### 1.3 术语

| 术语 | 含义 |
| --- | --- |
| point | 一个稳定 opaque RecoveryPoint identity；同一 producing TaskRun 至多一个 |
| attempt | 同一 point 的一次发布尝试；retry 必须分配新 attempt identity |
| Provider commit | Remote 上可精确验证的 marker/version graph 事实，不等于数据库可见 |
| DB committed | RecoveryPoint 通过 worker verification 后的数据库事实，是唯一发现门 |
| managed root | 与 legacy locator 物理隔离、只由 managed binding 指向的 namespace |
| control prefix | Xirang 专属 attempt/manifest/commit 对象空间，源数据不能覆盖 |
| physical identity | exact prefix attempt 或 exact native commit marker VersionId 所代表的 Provider identity |
| capability revision | provider/profile/credential/versioning/lifecycle/identity 证据的不可混用版本 |

## 2. 方案选择与整体架构

### 2.1 Portable publication 方案比较

| 方案 | 优点 | 结构性缺陷 | 结论 |
| --- | --- | --- | --- |
| attempt-qualified final prefix 直写 | 一次数据写；不依赖 rename；retry 天然隔离；exact attempt 可调和 | 留下不可发现 orphan；commit 前必须完成强验证 | 采用 |
| staging 后逐对象 copy 到 fixed final | transfer 与 final namespace 分开 | 没有通用原子 promotion；多一轮 API/传输；Copy 可透明下载上传；两套 orphan | 拒绝 |
| fixed mutable head 后 clone | 可能节省源端重复上传 | clone 期间 head 可变；旧 runtime/外部 writer 可覆盖；重新引入 rollback 隔离风险 | 拒绝 |

backup-dir 只保存一次 sync 中被覆盖或删除的旧对象，缺少本次新增对象的历史状态，因此不属于可选方案。

### 2.2 架构

~~~mermaid
flowchart LR
  T["Task Manager / producing TaskRun"] --> P["PublicationService"]
  P --> L["Point reservation + lease/fence"]
  L --> E["Managed Rclone executor"]
  E --> S{"RclonePublicationStrategy"}
  S --> V["Portable prefix publisher"]
  S --> N["AWS native-version publisher"]
  V --> C["Typed ProviderCommit"]
  N --> C
  C --> D["Fenced DB transition to verifying"]
  D --> W["Shared publication worker"]
  W --> R["DB committed RecoveryPoint"]

  X["Legacy RcloneExecutor"] -. "legacy_mutable only" .-> T
  B["Encrypted managed binding"] --> P
  A["Admission / managed-history latch"] --> P
~~~

Child 5 不建立第二套 coordinator、worker 或状态机。Registry 对 ProviderRclone 注册一个 typed RclonePublicationStrategy；该 strategy 校验 attempt tag 与 mode 后，才分派 portable 或 AWS native 实现。Unknown provider/mode/schema 组合在 mutation 前失败。

### 2.3 责任边界

| 组件 | 拥有 | 不拥有 |
| --- | --- | --- |
| Task Manager | TaskRun transfer/cancel/exit 状态、同 Task 快速互斥 | RecoveryPoint/manifest/latch 事务 |
| PublicationService | point/attempt reservation、binding、lease/fence、DB state、audit、reconcile | Rclone/AWS 协议细节 |
| Managed Rclone executor | 有界命令生命周期、真实 exit、progress、close/join | DB publication、mode fallback |
| Portable publisher | prefix layout、manifest/marker、Remote readback | 用户可见状态、retention |
| AWS native publisher | STS session、S3 admission、version graph、exact read | Azure/GCS/S3-compatible、lifecycle mutation |
| Shared worker | exact locator reopening、manifest/control proof、committed CAS | transfer result 重写、latest/current 推断 |
| API/UI | safe typed setup/preflight/activation/status | secret、locator、object key、VersionId |

## 3. 类型合同与状态线性化

### 3.1 模式映射

| Task publication mode | Repository version mode | RecoveryPoint semantics | Immutability |
| --- | --- | --- | --- |
| legacy_mutable | mutable_head | mutable_head 或无 managed point | mutable |
| versioned_prefix | versioned_prefix | xirang_manifest；import 时 imported_baseline | xirang_managed |
| native_object_versions | native_object_versions | xirang_manifest；import 时 imported_baseline | backend_versioned |

native_snapshot 仍只属于 Restic。Rclone native 不得借用 Restic semantics。

### 3.2 Tagged attempt

PublicationLineageV1、PublicationConsistencyV1及其共享wire version保持不变；Rclone两种mode都使用TagCodecVersion=0。只在现有strict envelopes增加closed Rclone branches：TaggedPublicationAttempt.Rclone、ProviderCommit.Rclone、ManifestResult.Rclone和PublicationReconcileResult.Rclone。Restic/Rsync既有rows保持byte-shape compatible；old runtime遇到Rclone tag必须拒绝，不能把它解成其他Provider。

RcloneAttemptV1 的公共 immutable facts 至少包含：

- schema/layout/minimum-runtime version；
- provider、publication mode；
- repository/link/point/attempt/task/task-run opaque IDs；
- trigger、capture-start、prepared-at、absolute point deadline；
- task、binding、capability、credential revisions；
- repository identity digest、managed-root identity digest；
- child point_publication fence keyed digest；
- manifest/resource limits；
- imported-baseline flag与 legacy-origin evidence digest。

Portable variant 另含：

- exact attempt-qualified data/control prefix locator；
- attempt marker digest；
- 可选 exact parent point/commit digest；
- 可选 copy-dest candidate；
- expected consistency/hash profile。

Native variant 另含：

- profile code aws_s3_general_purpose_v1；
- bucket/prefix/region identity digest，不含公开 raw value；
- role/session identity digest与 expiry；
- versioning/lifecycle canonical digest和 stable-observed-at；
- encryption profile、bucket-encryption digest、active key digest、retained read-key set digest与KMS capability revision；
- B0 version-view digest；
- start/canary control identity。

Attempt 解码是 closed union：duplicate member、explicit null、trailing data、unknown field、unknown variant 或 cross-variant populated field 均拒绝。

### 3.3 Tagged Provider commit

PortableCommitV1 至少绑定：

- exact attempt/control/data identity；
- commit marker digest；
- canonical manifest index/chunk digests；
- entry count、logical bytes；
- source/destination observation digest；
- fidelity/cost/capability evidence digest；
- provider committed time、deadline与 fence digest。

NativeCommitV1 至少绑定：

- exact commit key、opaque S3 VersionId、content digest；
- manifest control object key+VersionId graph；
- final point-view digest；
- attempt mutation-ledger digest；
- B0/B1 version-view digest；
- exact read/range/content proof digest；
- versioning/lifecycle/encryption/key-set/session/capability revision；
- provider committed time、deadline与 fence digest。

VersionId 始终作为不透明 UTF-8 string 处理；不得进入 JavaScript number、数据库数值列、metrics label或日志。

### 3.4 三事实与线性化点

~~~mermaid
stateDiagram-v2
  [*] --> preparing: fenced point reservation
  preparing --> transfer_failed: known non-zero or canceled
  preparing --> provider_committed: authenticated exact marker/version
  provider_committed --> verifying: fenced DB ProviderCommit transaction
  verifying --> committed: exact worker verification and CAS
  preparing --> failed: proven pre-commit failure
  provider_committed --> quarantine: fence or outcome ambiguity
  verifying --> failed: exact evidence mismatch
~~~

provider_committed 是 Remote 事实，不是数据库 RecoveryPoint state。数据库继续使用 preparing、verifying、committed 和 failed；quarantine 表示 typed outcome-unknown/blocked evidence，具体持久状态沿用现有 consistency/failure contract，不增加未经 schema 设计的新枚举。

TaskRun.status 只表达 Rclone command 的真实 transfer/cancel/timeout/exit。Provider/manifest 后置失败不得把 transfer success 改成失败，也不得把 transfer failure改成成功。

## 4. Portable unique-prefix publication

### 4.1 Layout

~~~text
<managed-root>/v1/
  points/<opaque-point>/attempts/<opaque-attempt>/
    control/attempt.json
    data/<source-relative-objects...>
    control/manifest-000000.jsonl
    control/manifest-000001.jsonl
    control/manifest-index.json
    control/commit.json
~~~

point/attempt component 由服务端 opaque ID 派生，用户不能提供。Raw Remote、managed root和exact locator只存在于加密binding/locator或受控命令内。data 与 control 永不重叠，源端名不能覆盖控制对象。

### 4.2 Remote mutation 顺序

1. 在数据库事务中创建或精确重取 producing TaskRun 对应的 preparing point，持久化 attempt、locator、deadline、revisions和 fence。
2. 以CSPRNG生成至少128-bit entropy的opaque attempt ID，并由数据库唯一性和persisted attempt保证永不重用。对exact attempt prefix执行best-available stat/list；任何可见既有对象或marker都是collision并quarantine。Rclone没有通用conditional-create，unknown/eventual Remote上的absence不是存储层no-clobber证明；portable合同依靠不可预测ID、exclusive managed-root writer scope和authenticated ownership marker实现protocol-level uniqueness，不能宣称backend原子创建。
3. 写入 canonical attempt.json，并完整读回、验证认证摘要。
4. Rclone sync 只能写入exact attempt data prefix。所有managed sync/copy/copyto/check命令显式使用--retries 1，禁止Rclone默认high-level retry复用同一attempt；low-level retries由settings固定上限并受同一absolute deadline约束。
5. command exit 和全部 stdin/stdout/stderr/process/goroutine close/join 后执行完整验证。
6. 以有界 streaming parser、0600 spool/external sort生成 deterministic manifest chunks和index。
7. 上传并完整读回所有 manifest control objects。
8. 写入 commit.json，作为该 attempt 的最后一次 Remote mutation。
9. 完整读回 commit bytes并校验 marker、manifest、identity、deadline和 fence。
10. 产生 typed ProviderCommit；DB fenced transaction进入verifying；worker exact reopen后才CAS committed。

Remote 上仅有目录、attempt marker、manifest或commit marker都不自动获得发现资格。只有DB committed是Catalog/API的未来发现门。

### 4.3 Canonical manifest

每个entry包含规范relative name、entry type、logical size、可证明内容identity、mtime precision/metadata fidelity和必要的source/destination observation。规范化必须明确：

- separator、root和空路径规则；
- Unicode/byte-order排序规则；
- case sensitivity不被主观折叠；
- directory、regular file、symlink、special/unknown的closed type；
- size/count使用checked uint64，写入模型前验证int64上限；
- duplicate normalized name、路径逃逸、绝对路径、NUL和超深度记录拒绝。

Managed Rclone文件类型合同固定为：

- data sync使用--links，禁止--copy-links和--skip-links；symlink以Rclone .rclonelink wire representation传输，manifest仍记录logical symlink与exact target bytes。Regular file与symlink wire name发生.rclonelink碰撞时fail closed；不按名称猜类型。
- directory进入canonical manifest。Remote能表示empty directory时使用--create-empty-src-dirs并验证round trip；不能表示时以authenticated manifest-only directory恢复语义记录，future reconstruct必须显式创建，不能把“仅含空目录”误记为零entry source。
- device、socket、FIFO和其他special/unknown source type在mutation前fail closed。Hardlink topology、ACL、xattr、uid/gid等只有通过profile round trip的字段才宣称保留；其他字段记录明确fidelity downgrade，不影响已证明的file bytes。
- source/destination logical path必须是valid UTF-8且通过固定Rclone path codec的encode→decode→encode bijection；invalid UTF-8、codec collision、unknown encoded rune或无法反解的physical key在mutation前拒绝。Native profile把Rclone v1.74.4的InvalidUtf8|Slash|Dot encoder revision固定进capability evidence；SDK key必须经同一双向映射，不得直接把logical path拼成S3 key。

Chunk按固定canonical byte/entry上限切分，只有最后chunk可以较短。Index绑定有序chunk digest、entry count、logical bytes、generator/schema和fidelity。达到entry、record、manifest bytes、spool disk或deadline上限时返回manifest_limit_exceeded；不得截断成功。

空source是合法零entry point，但仍必须证明data为空、写完整index和commit marker。没有prefix不等于已提交空点。

### 4.4 Typed staged-payload transport

现有bound Rclone config占用secret stdin，rcat也占用stdin，不能同时传config与control payload。统一使用：

1. 受限SSH/SFTP writer在verified per-user/configured private temp root下创建0700 attempt目录，并写authenticated ownership marker；root和path不能由调用者提供；
2. 使用opaque filename、O_EXCL和0600写入有界canonical payload，streaming计算digest/byte count，close后stat验证；
3. Rclone config继续走SecretStdin；
4. allowlisted copyto从exact local temp上传到exact private Remote locator；
5. 上传结束并join后执行exact stat/full readback；
6. 只清除本attempt拥有且marker匹配的exact local temp，不扫描或递归删除未知路径。

Payload、config、credential、Remote和command不得进入argv中的secret字段、shell fragment、env、日志或审计。Remote locator不可避免地作为typed私有命令参数时，命令runner不得记录完整argv。

Startup和periodic hygiene只在该private root内有界枚举opaque names，并且只有authenticated owner、age threshold、no active attempt/handle和no symlink traversal全部成立时才清理local orphan。Staging/write/close在Rclone启动前失败是pre-provider rejection；Rclone lifecycle不确定是outcome_unknown并按exact destination调和；known Provider outcome后的local cleanup失败只产生stable hygiene audit/metric和bounded retry，不能重写TaskRun/RecoveryPoint事实，也不能用删除Remote数据补偿。

## 5. Portable verification、成本和调和

### 5.1 Bound config identity

Managed versioned_prefix只接受Rclone V3 portable binding的self-contained bound_config variant。Legacy node_default继续保持现有mutable行为，但不能被preflight导入、导出、attest或升级为managed credential。Admin迁移时必须通过write-only setup重新提交exact Remote、managed root和有界config bundle。

Portable binding冻结：

- exact config bytes的keyed fingerprint、binding/config revision和validation profile revision；
- exact target Remote与managed-root identity digest，raw locator只在encrypted binding/typed private command中存在；
- config内target Remote dependency closure；额外无关stanza、duplicate section/key、trailing/oversized payload拒绝；
- registered backend-specific self-contained credential profile。Direct secret或OAuth/short-token refresh bootstrap必须全部包含在bound bytes；只有profile证明refresh不改变account/namespace时才允许in-memory access-token refresh。env/default-chain/metadata/instance identity、credential/token command、external helper/file/keyring、交互登录和其他不能由exact bytes冻结的secret source拒绝；unknown backend/config source fail closed；
- sanitized command environment。清除RCLONE_CONFIG及provider credential env，所有sync/list/stat/check/copy/copyto命令都以同一exact bytes经--config /dev/stdin执行；不得将config写节点磁盘或允许Rclone中途刷新到另一个identity。

Config rotation先pause scheduler、close admission并drain全部command/read/lease，再以expected Task/binding revision CAS替换encrypted bytes；成功后增加binding/capability revision并强制重新preflight。旧revision仍在运行或存在outcome-unknown时不得rotate。Bound config无法在未来命令继续认证时返回credential_invalid，不回退node_default。

### 5.2 最低内容证明

传输前生成完整source observation S0。传输成功并join后重新生成source observation S1和exact destination observation D1：

- S0/S1对象集合和可证明identity必须稳定；观测到source drift即失败。
- destination完整集合必须与source的content-bearing wire entries一致，不能仅接受两次相同但可能缺项的Remote listing；manifest-only directories另由S0/S1稳定性、canonical manifest和worker reconstruct fixture证明。
- 对destination至少再执行一次完整canonical listing D2；D1/D2间隔使用capability profile settle interval，digest/count/bytes必须一致。
- unknown consistency只能记录observationally_stable，不能升级为provider strong consistency。

若每个对象都有已认证语义的collision-resistant公共checksum，source/destination同算法一致且control exact readback通过，可记录provider_strong_checksum。

其他所有weak/no-hash/empty-hash/ambiguous-hash情况必须执行rclone check --download或等价full-byte source与exact destination比较，记录download_verified_bytes。size、mtime、ETag、multipart ETag、MD5、CRC、空hash或metadata_only永不形成ProviderCommit。

### 5.3 Cost 与 quota

Preflight返回预计source bytes、完整验证read bytes、API/storage/潜在egress类别和是否需要full download。数值跨API使用decimal string或已证明safe integer。

运行使用settings.Service注册的硬上限：

- manifest entries/bytes/record/depth；
- spool disk；
- command output；
- full-read verification bytes；
- Rclone/API并发；
- Remote observation次数；
- absolute point deadline。

Preflight能证明超限时在mutation前阻断；运行中才越界时取消、join并失败关闭。系统不在后台扩大用户批准的成本或deadline。

### 5.4 copy-dest 优化

只有满足以下条件才构造copy-dest：

- parent是同repository、同active link lineage的exact committed point；
- parent marker/manifest/capability revision精确匹配；
- source/destination Remote相同且prefix不重叠；
- preflight报告Copy capability；
- parent持有attempt-scoped、owner-isolated的point_publication read lease。

Parent lease使用service-issued deadline；effective provider deadline取child immutable deadline与parent deadline较早者。它不误用rsync_parent，也不新增holder type。

Copy=false时直接完整上传。copy-dest命令因capability失败时废弃当前attempt，以同point的新attempt完整上传。Rclone内部per-object server-side Copy失败后透明manual copy若最终成功可以接受；实际或unknown server-side count、upload/download bytes和fallback evidence只影响成本，不改变point、mode、manifest或fidelity。

dest-after不是可信manifest来源，也不参与commit。

### 5.5 Retry 与 reconciliation

- point identity和absolute deadline在首次prepare后不变。
- 普通pre-commit retry必须分配新attempt/prefix。
- 每个Rclone command固定--retries 1；只有bounded low-level request retry可留在同一command/attempt。Command退出后的outer retry绝不复用prefix。
- partial prefix永不续传、清空或在本Child删除。
- Provider outcome unknown抑制普通transfer retry。
- commit marker存在、DB仍preparing时，reconciler取得新child lease/fence作为DB写权，只观察persisted exact attempt；它验证旧attempt fence digest、marker authentication、manifest、revisions和provider committed time。
- 若此前记录lease loss/outcome unknown，或marker观测跨过takeover/deadline边界，进入quarantine，不自动promotion。
- eventual-consistency Remote上marker暂不可见时，在deadline内bounded observation；超时后记录availability failure并保留orphan。
- 多个有效marker只能认领数据库exact selected attempt；其他均为uncommitted orphan。
- DB committed后Provider暂不可读只标记degraded/at-risk；不回退legacy locator。

## 6. AWS native profile、binding 与 admission

### 6.1 唯一认证profile

Child 5只认证aws_s3_general_purpose_v1：

- AWS官方regional endpoint；
- general-purpose bucket；
- DNS bucket addressing；
- SDK/API revision由实现依赖和profile revision固定；
- S3强一致read-after-write/list合同；
- ListObjectVersions双marker分页；
- exact GetObject(versionId) full/range；
- versioning、完整lifecycle与GetBucketEncryption读取；
- closed encryption subprofile：sse_s3_v1或sse_kms_cmk_v1。两者都显式设置每次data/control write的encryption header，并以exact-version HeadObject证明实际算法/key identity。

以下稳定返回unsupported并推荐versioned_prefix：

- custom endpoint、endpoint override、path-style/custom resolver；
- S3-compatible、MinIO、Ceph、Cloudflare R2等；
- directory bucket；
- access point、Outposts及未认证寻址；
- Azure/GCS S3 interoperability；
- Rclone crypt或其他隐藏provider identity的wrapper；
- AWS-managed KMS、DSSE-KMS、SSE-C、KMS alias、cross-account key、custom/external key store与unknown encryption profile。

运行中不能从native自动降为portable或legacy。

### 6.2 Encrypted composite binding

单active repository_access_binding保存strict managed Rclone V3 document。Version 1已属于legacy task-derived binding，version 2已属于managed Rsync；Rclone不能复用或歧义解码既有version。V3 common fields包括：

- schema/profile/layout revision；
- repository/link/task/node identity；
- encrypted legacy/managed locator和identity digest；
- encrypted legacy V1 binding snapshot、legacy Task policy snapshot及其digest，用于activation-only clean rollback和later downgrade preparation；
- publication mode；
- binding/config/credential/capability revisions；
- preflight expiry、rollback facts与repository/control marker digest。

V3是closed portable|native variant。Portable variant只包含第5.1节的self-contained bound config bytes、exact Remote/dependency closure、self-contained credential profile和config fingerprint；任何node_default或native field populated都拒绝。Native variant包含：

- exact region/bucket/managed prefix和identity digest；
- role ARN、Xirang-generated external ID；
- bootstrap union：workload_chain或static_sts_bootstrap；
- encrypted static access key/secret，仅在对应variant出现；
- versioning/lifecycle digest与stable-observed-at；
- encryption_profile：sse_s3_v1或sse_kms_cmk_v1；
- canonical bucket-encryption digest、SSE-KMS effective bucket-key mode与actual canary encryption evidence；
- sse_kms_cmk_v1独有的encrypted active full key ARN、keyed digest、KMS capability revision，以及有界encrypted retained read-key ARN/digest set；sse_s3_v1出现任何KMS field即拒绝。

它不复用AppCredential。普通DTO只显示mode、status、revision、expiry和safe reason；不返回access key、secret、session token、role ARN、account/principal、bucket/prefix或provider raw error。

### 6.3 STS-only credential contract

1. Bootstrap credential只能实例化STS client；唯一例外是独立、不可注入publisher的BootstrapDenyProbe client，见第5项。
2. static bootstrap只加密存储，并且运维IAM合同只允许sts:AssumeRole。
3. 功能性S3 client factory只接受成功AssumeRole产生的temporary credential类型；BootstrapDenyProbe使用不同closed interface和constructor。
4. Xirang生成external ID，绑定exact role和session identity。Admission除正确external ID成功外，还必须证明missing与fresh wrong external ID均不能AssumeRole；任一negative call成功都拒绝profile并立即丢弃该session。
5. 对Xirang所需S3操作执行有界base-principal negative probe；任一意外成功拒绝admission。该probe是defense in depth，不宣称能数学证明全部IAM policy。
6. 每个AssumeRole请求附加Xirang生成的inline session policy：S3 statement只允许exact bucket、managed data/control prefix和required actions，并以s3:ResourceAccount约束target-role account；KMS subprofile另加入active/read-key exact ARN statements及第6.3节的ViaService/context限制。首个profile只认证same-account bucket/key；direct SDK所有支持的请求同时设置ExpectedBucketOwner。Cross-account bucket/key稳定unsupported。
7. 每个attempt在mutation前取得覆盖immutable point deadline加安全margin的一次role session。
8. 同一temporary session同时供服务端AWS SDK与节点Rclone使用，冻结credential identity/revision/expiry。
9. 服务端生成最小official S3 Rclone config，经SecretStdin传入；native mode禁止node-default、custom endpoint、bootstrap credential、argv/env/disk secret和命令中途换身份。
10. role最大session duration无法覆盖deadline时，native fail closed；不得让凭据过期后继续commit。Bootstrap credential为temporary/assumed-role时属于role chaining，DurationSeconds硬上限3600秒；effective point deadline必须小于其实际expiry与3600秒上限并预留安全margin，否则返回session_too_short。不得把目标role的12小时MaxSessionDuration误用于role chain。
11. 长期preflight/health可以提前refresh STS cache；refresh failure取消并joinProvider work。attempt内不换session identity。

Base-principal negative probe是唯一允许bootstrap credential触发的S3调用：它只通过dedicated BootstrapDenyProbe factory对reserved probe identity执行有界、read-only deny test，任何成功都使admission失败，且client/result不能进入publisher、reader或data/control evidence。所有功能性S3 list/read/write/delete-current请求仍只使用assumed-role temporary session。

Assumed repository role的Child 5最小权限矩阵为：

- bucket scope：s3:GetBucketLocation、s3:GetBucketVersioning、s3:GetLifecycleConfiguration、s3:GetEncryptionConfiguration、s3:ListBucket、s3:ListBucketVersions；
- managed data/control prefix：s3:GetObject、s3:GetObjectVersion、s3:PutObject、s3:DeleteObject；
- Rclone multipart按实际command profile所需：s3:ListBucketMultipartUploads、s3:ListMultipartUploadParts、s3:AbortMultipartUpload；
- lifecycle/tag profile未来若需要object version tags，必须显式增加s3:GetObjectVersionTagging；首个profile不以tag exemption放行destructive rule。

Child 5不调用s3:DeleteObjectVersion，也不要求它作为runtime权限。s3:DeleteObject只用于Rclone sync的current delete/delete-marker语义；future lifecycle Child必须独立审阅exact-delete permission、holds和跨point引用。角色拥有额外权限不会使backend_versioned升级为WORM。

Encryption权限分支为：

- sse_s3_v1不要求KMS permission；每次Rclone/SDK write显式设置AES256。
- sse_kms_cmk_v1的active key只允许exact ARN上的kms:DescribeKey、kms:GenerateDataKey和kms:Decrypt；retained read key只允许exact ARN上的kms:DescribeKey和kms:Decrypt。DescribeKey使用独立direct statement；cryptographic actions要求kms:ViaService=`s3.<region>.amazonaws.com`。`BucketKeyEnabled=false`时encryption-context `aws:s3:arn`约束exact managed object-prefix ARN；`true`时AWS只提供bucket ARN context，因此约束exact bucket ARN并继续由S3 session policy把object操作限制到managed prefix。Role IAM policy、AssumeRole inline session policy和KMS key policy三者都必须允许相同最小集合；context mode未知或与actual object不一致即fail closed。
- Child 5不要求或调用kms:CreateKey、PutKeyPolicy、CreateGrant、Enable/DisableKey、RotateKey、ScheduleKeyDeletion或CancelKeyDeletion。额外KMS权限不升级可信等级。

Xirang只直接调用kms:DescribeKey；GenerateDataKey/Decrypt由S3通过forward access session代表assumed role调用，并由actual Put/Get canary证明。应用不得直接请求或接触plaintext data key。

### 6.4 Admission

Native preflight依序证明：

1. Bootstrap identity匹配binding；correct external ID成功，missing/fresh-wrong external ID均被trust policy拒绝；STS assumed-role/account keyed identity digest匹配binding。
2. endpoint、region、bucket kind和addressing符合profile；target-role account、s3:ResourceAccount session restriction、ExpectedBucketOwner与实际bucket owner一致，且bucket为same-account。
3. GetBucketEncryption完整读取并canonicalizealgorithm、KMS key ID、BucketKeyEnabled和BlockedEncryptionTypes。Bucket default只记录为capability/drift evidence；每次managed write仍显式指定selected subprofile，不能从default推定actual object algorithm或key identity。
4. sse_s3_v1生成固定`server_side_encryption=AES256` config，拒绝任何KMS/SSE-C field；sse_kms_cmk_v1生成固定`server_side_encryption=aws:kms`与full key ARN，拒绝alias、AWS-managed、cross-account/region、DSSE、SSE-C与unknown field。Rclone v1.74.4没有bucket-key请求选项，因此KMS variant把canonical bucket `BucketKeyEnabled`（缺省按AWS合同规范化为false）冻结为effective mode，direct SDK control writes显式镜像该值；pre/post config digest或任一exact-version Head实际值不同都使capability失效并quarantine。
5. KMS subprofile对active与retained read keys逐一DescribeKey：KeyManager=CUSTOMER、KeySpec=SYMMETRIC_DEFAULT、KeyUsage=ENCRYPT_DECRYPT、KeyState=Enabled、Origin=AWS_KMS、same account/region且无custom/external key store。Multi-Region key只接受DescribeKey证明的当前bucket region primary/replica full ARN，并把configuration digest纳入capability。验证active GenerateDataKey/Decrypt与historical decrypt-only最小权限；missing/disabled/pending deletion/permission unknown拒绝。
6. GetBucketVersioning严格等于Enabled，并证明MFA Delete不是Enabled/unknown；Suspended、empty、MFA status不可证明和unknown拒绝。
7. 首次或变化后的versioning状态至少稳定观察15分钟并再次读取。
8. GetBucketLifecycleConfiguration完整读取并canonicalize全部enabled rules/filter/action。
9. lifecycle digest首次或变化后至少稳定观察15分钟并再次读取；任一变化重置窗口。
10. managed prefix不可能匹配current/noncurrent expiration或expired-delete-marker cleanup。Matching Transition只允许AWS官方明确为millisecond/real-time access的STANDARD_IA、ONEZONE_IA或GLACIER_IR；GLACIER、DEEP_ARCHIVE和INTELLIGENT_TIERING拒绝，后者因可选Archive/Deep Archive Access配置可能变成异步读取。
11. 首个profile不依赖tag filter豁免overlapping destructive rule；无法完整证明不匹配即拒绝。
12. opaque-preflight-scoped reserved canary覆盖固定path codec的普通/Unicode/Slash/Dot encoded-rune边界，由临时STS Rclone写入；direct SDK以physical key和ListObjectVersions找到真实VersionId，HeadObject(versionId)证明actual encryption profile/key digest，再执行exact full/range read。它不是RecoveryPoint attempt；真实publication在第7节再次写入attempt-scoped start/canary marker。
13. delete-marker fixture通过双marker分页和指定marker读取行为证明，证据保留在managed control namespace，不在本Child无证据清理。
14. Canary前后再次读取bucket encryption digest；任何变化使capability revision失效。Rclone与SDK物理namespace、managed-prefix ownership、encryption identity和required permissions必须全部匹配。

AWS官方说明首次启用versioning最多可能需要15分钟传播；lifecycle policy change也存在最长15分钟的传播窗口。S3 API没有可绑定的lifecycle last-modified revision，因此Xirang使用首次观察时间+canonical digest+15分钟后重读作为保守admission revision。存在destructive rule时，等待不会使其通过。

### 6.5 Lifecycle边界

无lifecycle配置可以通过。有匹配destructive/offline action、权限不足、unknown filter/action、传播窗口未收敛时拒绝native。Child 5：

- 不自动修改或删除bucket lifecycle；
- 不根据Task simple/GFS retention猜测provider expiration horizon；
- 不接受管理员risk override；
- 不把Object Lock、retention或MFA Delete包装成WORM；
- 每次run和周期health重新读取capability/lifecycle digest；
- 已committed point遇到drift只进入degraded/at-risk，不改写历史manifest。

首个profile的同步读取transition allowlist固定为STANDARD_IA、ONEZONE_IA和GLACIER_IR，并在preflight/cost evidence中显示retrieval cost class。任何其他或unknown storage class fail closed；未来扩大allowlist必须新profile revision、官方证据和live exact-version GET conformance，不能就地放宽现有revision。

### 6.6 Encryption key lifecycle

sse_s3_v1与sse_kms_cmk_v1都是同一AWS profile下不可混用的closed subprofile。Attempt冻结encryption profile、bucket-encryption digest、active key digest、retained read-key set digest与KMS capability revision；运行中任何变化取消并join Provider work，不能换key后继续commit。

SSE-KMS key lifecycle：

1. Active write key的full ARN只在encrypted binding/private evidence中保存；普通DTO仅显示sse_kms_cmk、safe status与read-key count。
2. Automatic key-material rotation不改变key ARN，DescribeKey identity和binding revision保持有效；不需要重写历史manifest。
3. 切换到另一个key ARN必须pause/drain、以expected binding revision更新、对new active key和全部historical keys重新preflight，再增加binding/capability revision。新key只用于之后的data/control versions。
4. 每个committed/verifying point manifest记录其实际key-ARN keyed digest。仍被任一点引用的旧key进入bounded retained read-key set，只获DescribeKey/Decrypt；最后引用由后续lifecycle移除前不得撤权或从binding移除。
5. Retained set和imported-baseline临时source-key set都受settings count/bytes上限、AssumeRole inline policy 2048-character limit与PackedPolicySize安全阈值约束；达到任一上限即阻断key-ARN rotation/baseline并返回kms_key_ring_limit。不得改用wildcard ARN，也不得删除point、version或manifest腾空间。
6. Periodic health逐key DescribeKey，并对每个key至少保留一个exact referenced-version decrypt canary；KMS throttle/outage是availability，permission denied、disabled、PendingDeletion、missing或decrypt mismatch使新publication fail closed并将相关points标记at-risk。
7. Child 5不修改bucket encryption、KMS key、key policy、grant、rotation或deletion。Operator修复key状态/permission后必须重新preflight；系统不自动换成SSE-S3或其他key。

## 7. Native point capture 与精确版本图

### 7.1 Single-writer gate

Task进程锁只提供本机快速互斥。Prepare事务必须锁定BackupRepository与active TaskRepositoryLink行，并查询相同physical identity的unresolved Rclone points。另一个preparing、verifying或outcome-unknown point未收敛时，下一次native run不得修改current head。

PostgreSQL通过row-level locking串行prepare；SQLite通过write transaction串行。事务释放前已持久化新preparing point，因此后到者能看到并阻断。无需新增repository lease表或migration。

### 7.2 B0/B1 capture

1. 在任何data mutation前执行完整ListObjectVersions，消费KeyMarker与VersionIdMarker；完成后从头再做一次完整双marker枚举。两次full graph的canonical digest/count/bytes必须一致才形成B0。S3强一致不代表跨key或跨pagination snapshot原子性，单次分页结果不得称为canonical graph。
2. 冻结session、binding、capability、versioning、lifecycle、encryption profile/KMS key-set、deadline和fence。
3. 写入attempt-scoped start/canary control marker。
4. 使用同一STS session执行Rclone sync到固定managed data prefix。
5. exit=0且全部handle join后写transfer-end control marker。
6. 完整ListObjectVersions后从头再执行第二次完整双marker枚举；只有两次full graph digest/count/bytes一致才形成B1，并在S3强一致合同下exact重读control证据。
7. B1-B0必须由当前attempt解释；B0中任一object version或delete marker在B1消失，表示发生了本Child绝不会执行的DeleteObjectVersion或等价永久删除，必须quarantine。未知VersionId、额外delete marker、两次full graph不一致、分页marker漂移或其他external-writer evidence同样quarantine。

Start/end marker的S3时间与VersionId只用于capture interval和归属证据；不得按mtime单独认领版本。每个logical source path先经第4.3节固定Rclone codec映射到physical S3 key；B0/B1中unknown、non-bijective或映射碰撞key不得归入point view。

### 7.3 Point view 与 mutation ledger

Manifest包含两个相关但不同的集合：

Final point view：

- 每个live key记录object_version；
- 记录opaque VersionId、size、approved content digest、metadata fidelity、actual encryption algorithm、KMS key-ARN keyed digest或明确none、BucketKeyEnabled；
- 当前latest delete marker记录delete_marker和opaque VersionId，表示该point中的缺席；
- 未变化object可以精确复用前一点的VersionId。

Attempt mutation ledger：

- 记录B1-B0产生的每个object version和delete marker；
- disposition为referenced或superseded；
- 同attempt内Rclone retry/overwrite产生的中间version不得丢失；
- control object versions单独分类；
- 使未来lifecycle Child能计算跨point引用，而不按current head猜测。

S3 object version与delete marker是closed tagged union。Delete marker不能与零字节object混淆，也不能用nullable VersionId表示。
Encryption fields只允许object_version/control-object variant；delete_marker没有object payload，出现algorithm/key field即拒绝。

### 7.4 Exact content proof

每个final live entry必须先通过HeadObject(versionId)证明VersionId、size与selected actual encryption profile；SSE-KMS还必须匹配active/retained key digest。随后通过GetObject(versionId)精确读取。若对象具有profile认证的collision-resistant checksum，source与exact version同算法一致可作为强证明；否则完整读取source与exact version并计算强摘要。Range capability由exact-version canary和代表性边界fixture证明。

Source observation必须覆盖读取前后；source drift、GetObject current-head fallback、VersionId mismatch、encryption/key mismatch、KMS decrypt failure、short read、Range mismatch或content digest mismatch都阻止commit。

Delete marker通过ListObjectVersions的DeleteMarkers集合、VersionId、IsLatest及指定marker Get行为验证。恢复/reconstruct时跳过delete_marker并只读取object_version。

### 7.5 Native control commit

Manifest chunks、index和commit写入attempt-exclusive control prefix，每个control object都记录自己的S3 VersionId与actual encryption evidence。commit.json作为最后一次Remote mutation；写入后direct SDK：

- 列出exact commit key versions；
- 要求唯一authenticated candidate；
- exact GetObject(commit VersionId)完整读回；
- 校验manifest graph、point view、mutation ledger、encryption/key-set digest、revisions、deadline和fence。

Provider locator加密保存commit key+VersionId+digest及manifest version graph。source_fingerprint绑定这个physical commit identity，而不是内容digest，避免内容相同的两个合法points冲突。

Point映射固定为：

- mode native_object_versions；
- semantics xirang_manifest，baseline时imported_baseline；
- immutability backend_versioned。

Worker和未来reader/range/reconstruct/restore只能消费manifest exact VersionId；current、latest、synthetic timestamp name和Rclone s3-versions均禁止。

### 7.6 Native crash reconciliation

S3 object GET/PUT/DELETE/LIST强一致，因此只有在command completion已durable known且全部remote handles已join后，exact commit key缺失才可作为未发生该commit的证据。Outcome unknown时仍需先读取persisted exact attempt：

- 唯一authenticated commit version、valid manifest graph、matching revisions/fence/deadline可进入verifying；
- multiple commit versions、unknown control object、ledger drift、marker在takeover/deadline后出现或旧fence迟到均quarantine；
- commit缺失本身不能清除outcome unknown；必须先证明旧SSH command已终止且不再可能mutation，否则等待immutable deadline并保持quarantine；
- B0/B1或source proof不完整不得从current head重建成功；
- DB committed后version缺失只标记degraded/at-risk，不切换portable或legacy。

本Child不删除中间versions、delete markers、failed attempts或control evidence。

## 8. Shared orchestration、deadline 与 crash safety

### 8.1 Managed routing

RcloneTaskConfigV1是closed policy document：

~~~text
{
  "version": 1,
  "publication_mode": "legacy_mutable",
  "bandwidth_limit": "10M",
  "transfers": 4
}
~~~

publication_mode是closed union legacy_mutable、versioned_prefix或native_object_versions；bandwidth_limit是optional string，transfers是optional positive integer。示例只展示legacy wire shape，managed value由activation service写入。

Empty config和旧unversioned bandwidth_limit/transfers shape保持legacy_mutable。Generic Task create/update只能写legacy policy；managed mode只能由成功preflight后的activation service写入。Provider secret、Remote、managed root和preflight evidence不进入Task.ExecutorConfig；portable bound config只存在于encrypted Rclone V3 binding。

Executor factory保持两条不相交路径：

- legacy_mutable使用现有RcloneExecutor，保持当前command和restore兼容；
- managed mode使用RclonePublicationExecutor，并要求registry存在ProviderRclone strategy、PublicationService已准备typed attempt。

Managed executor若收到missing/mismatched attempt、wrong provider、unknown mode或binding revision不匹配，在启动command前失败。Legacy executor永远不能取得managed locator。

### 8.2 Prepare transaction

每个producing TaskRun的prepare：

1. 获取managed admission generation token。
2. 加载exact Task、active link、Repository和active binding。
3. 校验feature、mode、schema、task/binding/capability/encryption revision和managed-history guard。
4. 对native锁定Repository/link并检查single-writer unresolved points。
5. 创建或按producing_task_run_id精确重取RecoveryPoint。
6. 写immutable PublicationLineage和tagged attempt。
7. 取得child point_publication lease，absolute deadline写入lineage且永不延长。
8. 若portable copy-dest使用parent，取得parent point_publication read lease。
9. 提交短事务后才允许Provider mutation。

`first_new_point`的producing run使用上述prepare。`imported_baseline`的migration TaskRun与preparing RecoveryPoint/attempt已由第9.1节activation transaction原子reserve；executor只消费该exact reservation并取得对应lease，不能再创建第二个point/attempt。Duplicate prepare只有在point、run、link、mode、deadline和attempt evidence完全相同时幂等返回；任何差异冲突，不重放backup。

### 8.3 Lease 与 cancellation

Child lease覆盖transfer、verification、manifest、marker、ProviderCommit DB transaction。Parent lease覆盖copy-dest开始到Provider outcome确定并全部handle join。Worker/reconciler取得新fence时继承同一point deadline，不得因release/reacquire延长。

任一条件发生时立即取消Provider context：

- child/parent renew失败；
- fence变化；
- admission generation revoke；
- task cancel；
- STS session失效；
- absolute deadline；
- binding/capability revision drift；
- bucket-encryption digest、active/retained KMS key state或permission drift；
- shutdown。

固定释放顺序：

1. 停止启动新command/API page/read。
2. cancel Rclone和AWS contexts。
3. drain stdin/stdout/stderr、SDK body和manifest streams。
4. join process、reader、paginator、spool和heartbeat goroutine。
5. 在DB transaction内重新锁行并验证current fence。
6. 写typed outcome或拒绝迟到state change。
7. release leases和admission token。

不得在Provider goroutine仍可能mutation时通过defer提前释放lease。

### 8.4 ProviderCommit 与 worker

Normal path的ProviderCommit transaction：

- 锁定RecoveryPoint；
- 验证preparing state、lineage、attempt、current child fence和可选parent fence；
- 验证typed commit与binding/capability/encryption/key-set/deadline；
- 写encrypted exact locator、source_fingerprint、manifest/count/bytes、fidelity和consistency；
- 写首个managed history repository+installation latch；
- state推进到verifying；
- release当前publication leases；
- enqueue shared worker。

Worker以point ID作为opaque队列键，按persisted provider tag dispatch。Rclone worker：

- 重开exact portable prefix或native commit VersionId；
- 重读commit、index、chunks和完整listing/version graph；
- 验证ProviderCommit digest、fidelity/capability/encryption/KMS evidence和source fingerprint；
- 不读取Task.RsyncTarget、current、latest或legacy verifier；
- 在新worker fence下CAS verifying到committed。

Worker failure不得改写TaskRun transfer。Integrity mismatch使point failed/quarantined；temporary availability按deadline有界重试。

### 8.5 Crash/reconciliation matrix

| Persisted/Provider fact | 动作 |
| --- | --- |
| DB没有point，只有未知Remote prefix | 不认领；无exact persisted identity |
| DB preparing，attempt marker不存在 | portable在deadline内bounded observation；native只有durable completion且all handles joined时可凭强一致判定未commit，否则outcome unknown保持quarantine |
| DB preparing，partial data/manifest无commit | 保持不可发现；新retry使用新attempt；本Child不删除 |
| exact commit存在，DB preparing | 新reconcile fence下验证persisted attempt、旧fence digest、deadline、manifest和outcome；无歧义才进入verifying |
| exact commit在lease loss/takeover/deadline后出现 | quarantine；不自动promotion |
| multiple authenticated attempts/commit versions | 只认DB exact selected attempt；不明或multiple same-key commit version quarantine |
| DB verifying，exact control evidence缺失/变化 | failed/quarantine；不编造locator |
| DB committed，Provider暂不可达 | availability degraded；不回退legacy |
| DB committed，exact data/version永久缺失 | at-risk/integrity degraded；保留原manifest，不读取current替代 |
| TaskRun interrupted | 独立恢复真实run状态；不以point committed覆盖known transfer failure，也不以stale run推断Provider success |

Reconcile candidate query按persisted provider kind、semantics和strict attempt schema过滤，覆盖Restic、Rsync、Rclone；不得继续使用Restic/Rsync-only executor condition。

### 8.6 Startup 与 shutdown

Startup先加载latch/admission，扫描preparing/verifying managed points并执行exact reconciliation，再开放新的native writer。若存在同physical identity unresolved native point，相关Task保持blocked。

Graceful shutdown先关闭新admission，再cancel active work并等待全部command/read/manifest/heartbeat join。Forced termination留下exact owned attempt供restart reconcile；不做broad Remote cleanup。

## 9. Legacy migration、rollback 与 000064 safety

### 9.1 Preflight 与 activation

Existing Rclone Task始终保持legacy_mutable，直到Admin显式启动wizard。Preflight：

- 绑定expected Task revision、requested mode、binding revision和opaque preflight ID；
- 暂停调度并drain全部相关command/read/lease；
- 证明legacy与managed locator不重叠；
- 验证managed root fresh ownership和control marker；
- portable要求第5.1节bound config setup已完成，并以同一exact config revision验证Remote list/read/write/check/copy/cost；node_default或动态credential source在mutation前拒绝；
- native完成第6节全部STS/S3 admission；
- 记录capability revision、cost buckets、safe reason和UTC expiry；
- 不传输用户data，不创建RecoveryPoint，不写永久latch。

Activation使用expected-revision transaction重新验证preflight和所有revision，安装managed Rclone V3 binding和TaskRepositoryLink mode，并在同一事务把Task.RsyncTarget清空。`first_new_point`在该事务中不创建point。`imported_baseline`则必须在同一事务创建唯一migration TaskRun及其preparing RecoveryPoint/attempt reservation，冻结lineage、deadline与preflight revisions；任一reservation/lease前置事实无法建立时整个activation回滚，Provider mutation只在事务提交后开始。Legacy locator只保留在encrypted link/binding中；旧RcloneExecutor即使忽略unknown ExecutorConfig字段，也会因target为空在command前失败。任一配置只完成一半时Task保持paused/unscheduled；generic Task create/update/import不得为managed Task重新填入RsyncTarget，也不能经过legacy executor一次。

### 9.2 first_new_point

这是默认选择：

- legacy locator保持byte-for-byte不动并加密保留；
- managed root fresh且物理隔离；
- 不复制、rename、tag或原地标记legacy head；
- 不从mtime、TaskRun、listing或既有versions生成point；
- activation后下一次成功producing TaskRun才创建首个xirang_manifest；
- 首个point正常执行完整portable或native publication。

Activation/control canary不等于history，不能写永久latch。

### 9.3 imported_baseline

这是显式可选选择。它创建一个新point和新attempt：

- portable：从legacy current Remote物理copy到fresh attempt-qualified managed prefix；
- native：使用已认证STS binding从legacy exact prefix物理copy/sync到fresh native managed data prefix，从而创建新的可归属S3 versions。Baseline preflight必须对legacy current objects执行exact Head/Get encryption inventory；source只接受SSE-S3或same-account customer-managed SSE-KMS full-ARN key set，临时session policy仅为该migration attempt加入exact legacy prefix read和bounded source-key Decrypt。AWS-managed/DSSE/SSE-C/custom/unknown source拒绝；
- destination每个new version始终按selected managed encryption subprofile重新加密。Legacy source key digest只进入baseline origin evidence，baseline完成并证明不再需要source decrypt后不进入retained destination read-key ring；
- server-side copy与download/upload只改变成本，不改变语义；
- source在完整copy/verification期间必须稳定；
- 执行正常manifest、marker、fence、ProviderCommit和worker合同；
- semantics为imported_baseline，origin evidence明确表示只捕获一个current baseline；
- 不认领legacy历史VersionId，不制造多个point。

失败baseline保持Task paused并留下不可发现exact attempt；普通schedule不能在baseline未调和时开始。

### 9.4 Activation-only clean rollback

Clean ordinary rollback只存在于managed activation成功后、任何RecoveryPoint/attempt reservation创建前。ProviderCommit/latch不是唯一门：prepare在mutation前已创建xirang_manifest/imported_baseline point，而ManagedHistoryResolver与000064 down guard有意保留这些semantics的任意state。不得删除、改写或忽略failed/preparing point来换取legacy fallback。

Clean rollback transaction要求：

1. Task已paused，new admission关闭，全部command/read/worker/lease已drain；
2. 锁定Task、active link、Repository和binding，并证明没有任何managed RecoveryPoint、attempt、active publication lease、history latch或tombstone；
3. legacy locator与managed root物理隔离，encrypted legacy V1 binding/Task policy snapshot digest与activation记录一致；
4. 原子恢复Repository mutable_head/mutable、link legacy_mutable、legacy V1 binding、Task legacy policy和Task.RsyncTarget；
5. Task继续paused，transaction后重新运行legacy safety probe，只有operator显式enable才恢复schedule；
6. 保留preflight/canary control evidence，不删除Provider对象，也不把它们登记为history。

first_new_point在首次producing run进入prepare前拥有该窗口。imported_baseline activation会立即创建migration TaskRun并reservation point/attempt，因此activation transaction提交后不再有clean rollback窗口；UI必须在最终确认前明确显示这一不可逆安全门。

### 9.5 First-reservation 后 rollback preparation

任何managed RecoveryPoint/attempt reservation一旦存在，无论state、是否开始Remote mutation或是否存在ProviderCommit，都只能进入rollback preparation。若首个exact ProviderCommit或安全reconciliation发生，同一transaction另写repository+installation latch；latch永久保留，link unlink、Task delete、feature disable或point lifecycle不能清除。没有commit的failed/preparing point本身也继续作为durable managed-history blocker。

Rollback preparation：

1. pause scheduler；
2. close new managed admission；
3. cancel/drain全部Provider work/read/lease，并exact reconcile每个attempt；outcome_unknown保持blocked/quarantine；
4. 验证legacy locator不指向managed root；
5. 将Task.RsyncTarget重新连接到encrypted legacy rollback locator，但保留managed Repository/link/V3 binding和minimum-runtime marker；
6. 保持Task paused；
7. 保留所有point、marker、binding evidence、orphan和已有latch。

Current runtime通过active managed link、point resolver和可选latch继续拒绝ordinary mutable fallback。Older runtime只会看到paused Task和物理隔离legacy target；后续启用是workflow外的显式operator风险，不是自动fallback。恢复ordinary legacy若未来确有产品需求，必须由独立evidence-retirement schema/task重新设计，不能在Child 5隐式放行。

### 9.6 Binding compatibility

Legacy Rclone V1和managed Rclone V3是closed tagged union；V2保留给managed Rsync。Legacy reader只接受V1。Managed Rclone reader只接受V3和exact committed point locator。旧或不认识V3的component必须fail closed，不能把encrypted managed root当Task.RsyncTarget；activation清空Task.RsyncTarget作为old-runtime data-plane backstop。

Publication schema和control marker包含minimum-runtime revision。Older runtime无法理解时拒绝mutation/read，而不是忽略unknown field。

### 9.7 000064修复

不新增migration version，不占用或重排父计划000065至000071。现有000062至000064 up schema已经提供：

- versioned_prefix/native_object_versions link modes；
- xirang_manifest/imported_baseline semantics；
- encrypted locator、fidelity、consistency、capability fields；
- point_publication lease；
- producing TaskRun和managed physical source unique约束；
- durable managed-history latch。

Child 5只修复：

1. SQLite 000064 down guard加入versioned_prefix和native_object_versions links。
2. PostgreSQL 000064 down guard加入相同modes。
3. ManagedHistoryResolver在latch、points、leases、tombstones之外查询active managed links，覆盖activation后首点前状态。
4. 双数据库integration tests证明blocked down在任何DDL/DML前失败且schema/data不变。

Down在以下任一事实存在时拒绝：

- installation/repository latch；
- 任一state的native_snapshot、xirang_manifest或imported_baseline point；
- native_snapshot、versioned_hardlink、versioned_full_copy、versioned_prefix或native_object_versions active link；
- active point_publication或rsync_parent lease；
- future lifecycle tombstone。

Activation不预写latch，因此任何managed point/attempt reservation前完成第9.4节clean rollback并移除managed link后仍有真实down路径；首次reservation后，任意state point guard即永久阻断本Child内的down/ordinary fallback。

## 10. API、frontend 与用户合同

### 10.1 Safe summary

Task response增加rclone_publication：

~~~text
mode:
  legacy_mutable | versioned_prefix | native_object_versions

state:
  legacy | preflight_required | credential_setup_required |
  capability_settling | ready | preparing | verifying |
  committed | degraded | at_risk | failed | blocked |
  rollback_prepared

safe fields:
  reason_code
  task_revision
  binding_revision
  capability_revision
  consistency_class
  hash_fidelity
  estimated_read_bytes
  api_cost_class
  storage_cost_class
  egress_cost_class
  credential_expires_at
  encryption_profile:
    none | sse_s3 | sse_kms_cmk
  kms_key_status:
    not_applicable | ready | degraded | at_risk | blocked
  kms_read_key_count
  rollback_locator_present
  rollback_capability:
    clean_available | preparation_only | prepared
~~~

初始safe reason union固定包含：legacy、preflight_required、ready、credential_setup_required、capability_settling、preflight_expired、task_revision_changed、binding_revision_changed、preflight_mismatch、feature_disabled、unsupported_profile、repository_offline、provider_unavailable、provider_timeout、provider_resource_limit、session_too_short、versioning_disabled、lifecycle_conflict、encryption_unsupported、kms_key_unavailable、kms_permission_denied、kms_key_ring_limit、identity_mismatch、credential_invalid、verification_cost_limit、source_drift、external_writer_detected、unexpected_version、manifest_mismatch、marker_mismatch、admission_blocked、outcome_unknown和rollback_prepared。新增reason需要backend/frontend/i18n/tests同批更新；任一unknown wire value都使summary确定性投影为`state=blocked`与`reason_code=unsupported_profile`。

Internal binding profile `sse_s3_v1 | sse_kms_cmk_v1`只在private service/domain存在，API mapper显式投影为safe `sse_s3 | sse_kms_cmk`；`none`只用于非native mode，不能把unknown internal profile投影成`none`。Counts/bytes/revisions若可能超过JavaScript safe integer，以decimal string传输。Raw snake_case type只存在于API module；components只接收camelCase domain union。Unknown mode/state/reason/encryption/KMS status统一把enclosing summary映射为`blocked + unsupported_profile`，不猜成legacy或SSE-S3；unknown rollback_capability映射preparation_only，绝不误开放clean action。

TaskRun transfer status与publication summary独立显示。一个transfer success加publication failed是合法、必须可见的组合。

### 10.2 Privileged endpoints

~~~text
POST /api/v1/tasks/:id/rclone-versioning/portable-binding-setups
PUT  /api/v1/tasks/:id/rclone-versioning/portable-binding
POST /api/v1/tasks/:id/rclone-versioning/native-binding-setups
PUT  /api/v1/tasks/:id/rclone-versioning/native-binding
POST /api/v1/tasks/:id/rclone-versioning/preflights
POST /api/v1/tasks/:id/rclone-versioning/activate
POST /api/v1/tasks/:id/rclone-versioning/clean-rollbacks
POST /api/v1/tasks/:id/rclone-versioning/rollback-preparations
~~~

所有route位于authenticated /api/v1 group，要求Admin、registered RBAC permission、Task ownership和feature gate。Full-router tests覆盖Admin成功、Operator/Viewer拒绝、missing/unknown role拒绝和no-existence leak。

Portable与native binding都使用short-lived opaque setup ID和write-only PUT。Portable流程为：

1. setup endpoint绑定expected Task revision并返回opaque setup ID/expiry；
2. write-only PUT消费setup ID、expected Task/binding revision、exact private Remote/managed root和bounded self-contained config bundle；
3. response只返回safe revision/status，不回显config、Remote、stanza、backend credential source或fingerprint material。

Native流程为：

1. setup endpoint以expected Task revision创建short-lived opaque setup ID和external ID；
2. write-only PUT消费setup ID、expected Task/binding revision、role ARN、closed bootstrap union、`sse_s3_v1 | sse_kms_cmk_v1` encryption profile，以及仅KMS variant允许的full customer-managed key ARN。

所有portable/native secret只出现在request。Response不回显。External ID和KMS key ARN不是secret但属于sensitive operational identity：前者只在native setup response显示，后者只write-only提交，两者都不从普通GET恢复。任一binding/key-ARN rotation先pause/drain，使用expected binding revision CAS；成功后增加binding/capability revision，native另重置versioning/lifecycle settle并重新完成encryption/key-set preflight。

Preflight request只接受expected Task revision和requested mode。Activation另接受opaque preflight ID与migration choice。Clean rollback接受expected Task/binding revision并在同一transaction证明zero managed point/attempt；任一reservation已存在返回stable conflict并要求rollback preparation。Unknown field、duplicate、null、stale revision、expired/mismatched preflight均返回stable conflict/bad-request code。

Handlers使用response.go helpers；service返回sentinel/typed capability errors，handler统一映射400/403/404/409/501/503。Unexpected error走generic 500，不返回raw AWS/SSH/SQL/encryption error。

### 10.3 Frontend

Task detail使用独立Rclone versioning dialog，复用现有Dialog、Button、Badge、Alert、Tabs/segmented-control primitives，但Rclone domain type和mapper不复用Rsync enum。

Flow：

1. 选择portable或native，portable默认推荐。
2. 所选mode缺少binding时进入Admin write-only setup：portable重新提交self-contained Rclone config，native配置STS binding。
3. 运行preflight并显示expiry。
4. 展示consistency/hash、full-read bytes、API/storage/egress/KMS cost class、STS expiry、lifecycle/encryption profile、safe KMS status/read-key count、profile reason和rollback-present；不显示key ARN。
5. 选择first_new_point或imported_baseline，默认前者。
6. imported_baseline在最终确认前显示“activation立即创建point/attempt并永久结束clean rollback窗口”；first_new_point显示窗口持续到首次prepare。
7. expected revision activation。
8. 显示transfer与publication两个独立状态及rollback capability。
9. clean_available时可执行clean rollback；首次reservation后只提供rollback preparation。

Portable config或native binding未setup、capability未settle、preflight过期或revision drift时activation button disabled。Frontend不能提供risk override，也不能提供“沿用节点默认配置”开关。

Secret field：

- 不进入URL/query；
- 不进入localStorage/sessionStorage；
- 不进入shared draft cache；
- submit成功或失败后清空；
- error/toast不包含原值；
- role/account/bucket/KMS key ARN等敏感operational值不在普通summary渲染。

TypeScript要求：

- private Raw response types；
- explicit import type；
- no any、unknown-as-domain或direct fetch；
- snake_case到camelCase只在API mapper；
- invalid arrays/numbers/timestamps使用safe fallback；
- revisions/bytes保持decimal string。

UI同步zh/en keys；Radix Dialog有DialogTitle；input有label；decorative icon aria-hidden；focus-visible不移除；segmented control使用Radix或完整tab keyboard semantics；portal内容执行runAxe。

本Child不增加Catalog、file list、preview、download、restore或workspace navigation。

## 11. 安全、错误、资源与可观测性

### 11.1 Secret 与 locator隔离

不得进入公开DTO、TaskRun safe summary、log、audit details、metrics label或frontend persistent state：

- Rclone Remote name和config；
- AWS region/bucket/prefix/endpoint raw value；
- object key、relative filename、VersionId；
- role ARN、account、principal、KMS key ARN/alias、external provider request ID；
- access key、secret、session token、bootstrap credential；
- exact Provider locator、manifest/control key；
- command argv/output和raw provider error。

Encrypted binding/model hook承担at-rest encryption。New secret-shaped fields不得借用只过滤password键的AppCredential sanitizer。

### 11.2 Stable error taxonomy

| 类别 | 代表reason | 自动行为 |
| --- | --- | --- |
| capability/unsupported | unsupported_profile、versioning_disabled、lifecycle_conflict、encryption_unsupported、kms_permission_denied、credential_setup_required | mutation前block，建议portable |
| availability/retryable | repository_offline、provider_unavailable、provider_timeout、capability_settling | deadline内bounded retry |
| integrity/quarantine | source_drift、unexpected_version、external_writer_detected、kms_key_unavailable、marker_mismatch、manifest_mismatch | 不自动promotion/普通retry |
| resource/cost | provider_resource_limit、verification_cost_limit、session_too_short、kms_key_ring_limit | preflight block或cancel/join后fail |
| fence/control | admission_blocked、outcome_unknown | reject late commit；必要时quarantine |

Provider raw error只进入受限internal cause chain。Audit/response使用registered stable action/reason和correlation ID。Canceled/deadline query不刷错误日志。

Internal causes必须在API边界确定性映射到第10.1节closed safe reason union：provider/KMS throttled或temporary list/key-service unavailable映射provider_unavailable，manifest limit映射provider_resource_limit，lease lost或admission revoked映射admission_blocked，尚未发生Remote outcome ambiguity的deadline exceeded映射provider_timeout，mutation后无法证明Remote结果的timeout/transport loss映射outcome_unknown，external writer映射external_writer_detected，unsupported path codec/source type映射admission_blocked，wrong bucket owner或KMS key identity映射identity_mismatch，external-ID trust未强制映射credential_invalid，unsupported encryption/KMS profile映射encryption_unsupported，KMS AccessDenied映射kms_permission_denied，disabled/PendingDeletion/missing/decrypt failure映射kms_key_unavailable，retained set overflow映射kms_key_ring_limit。Internal cause本身不得穿透为未注册public reason。

### 11.3 Resource limits

所有潜在unbounded输入必须有独立上限：

- SSH command duration/stdout/stderr/line length；
- SecretStdin和staged payload bytes；
- temporary directory/file count和spool disk；
- manifest entries/record/depth/chunks/total bytes；
- AWS paginator pages、versions、delete markers和response bytes；
- KMS key ARN bytes、retained read-key count、AssumeRole policy/PackedPolicySize、DescribeKey/decrypt probes和response bytes；
- object key和VersionId bytes；
- concurrent Rclone transfers、SDK requests和exact reads；
- SDK retries/backoff/HTTP body read；
- full-byte verification bytes；
- preflight/point absolute deadline；
- audit/metrics cardinality。

达到上限是typed failure，不是partial success。No unbounded slice用于整个Remote或version graph；使用streaming parser、external sort和incremental canonical digest。

### 11.4 Observability

Metrics只使用低基数labels：

- provider rclone；
- mode；
- profile code；
- encryption profile与safe KMS state class；
- stage prepare/transfer/verify/commit/reconcile；
- outcome class；
- stable reason code。

不以Task ID、Repository ID、Remote、bucket、key、VersionId、KMS key ARN、error message作为label。Private evidence可记录bucketed S3/KMS API、read/upload/download/copy counts、retained-key count和cost class；公开summary只显示safe aggregate。

Audit actions沿用registered publication prepare/verify/commit/fail/reconcile，并新增或复用Rclone binding/key setup/rotate、preflight、activate、clean-rollback、rollback-preparation的registered actions。Audit reason必须sanitized，不能保存用户输入secret、KMS ARN或raw AWS diagnostic。

## 12. 模块边界与依赖

本节定义责任落点，不替代implement.md的逐文件执行顺序。

### 12.1 Provider layer

- 扩展provider/contracts.go的tagged Rclone attempt/commit/reconcile union。
- RclonePublicationStrategy是registry唯一Rclone publication entry。
- Portable publisher拥有layout、command plan、manifest、marker和Remote reconcile。
- Native publisher依赖小型S3Native与KMSKeyInspector接口，不让AWS SDK types穿过provider package边界。
- Existing RcloneAdapter读路径保持legacy compatibility；exact native reader是独立adapter，只接受point manifest和VersionId。
- Canonical writer、manifest limits、CommandCompletion和sanitizer优先复用Child 2至4既有实现。

### 12.2 Repository layer

- Managed Rclone binding V3 decoder/encoder和identity digest；shared binding decoder保持V1/V2 byte compatibility并对cross-provider version失败关闭。
- Portable/native write-only setup、self-contained config-source validation、KMS key-set/rotation、preflight/activation/rollback service。
- PublicationService的Rclone prepare/record/reconcile/worker dispatch。
- Repository/link row lock和unresolved native writer query。
- History latch transaction与resolver修复。

Repository service拥有DB lock order和transaction；Provider strategy不得直接使用GORM。

### 12.3 Task layer

- 向后兼容RcloneTaskConfigV1 parser。
- Managed Rclone executor与factory wiring。
- Manager route按persisted link mode选择legacy/managed。
- Interrupted TaskRun recovery识别Rclone typed point。
- Legacy verifier/retention/integrity path在managed link上fail closed，不读取mutable target。

### 12.4 API/frontend layer

- Task handler safe summary enrichment。
- Admin Rclone versioning handler/routes/full-router RBAC。
- Typed API module、domain unions、dialog和tests。
- Swagger/API documentation只描述实际safe contract，不发布secret schema值。

### 12.5 AWS dependency

使用AWS SDK for Go v2的最小模块集合：config/credentials、STS、S3、KMS及必要internal retry/transport依赖。版本锁定在go.mod/go.sum，禁止把generic AWS SDK object传给domain/API。依赖必须通过license、govulncheck和module graph审查。

AWS adapter构造函数接受S3/KMS interface clients、clock和endpoint/profile validator，便于official wire fixture和fake-clock测试；production factory禁止custom endpoint。Test-only endpoint injection不能出现在public setup/config API。KMS client只允许same-region official endpoint和exact full key ARN；不接受alias/custom endpoint。

## 13. Test 与 conformance matrix

### 13.1 Contract/unit

- Legacy empty/unversioned Rclone config兼容；managed activation清空Task.RsyncTarget且generic update/import不能恢复它。
- Managed config version/mode/bandwidth/transfers round trip；V3 portable/native cross-variant rejection。
- Native encryption_profile closed union、SSE-S3/KMS cross-field rejection、full key ARN digest和bounded retained read-key set canonicalization。
- Portable config setup exact-byte fingerprint、dependency closure、unrelated stanza、size/duplicate/trailing、node_default/env/metadata/helper/file/dynamic credential rejection和rotation drain/CAS。
- duplicate/null/unknown/trailing/cross-variant rejection。
- Tagged Rclone attempt/commit/reconcile exact variant。
- Opaque IDs、decimal strings、VersionId max/UTF-8 handling。
- Canonical path/type/order/chunk/digest和duplicate detection。
- Marker authentication、minimum runtime、fence/deadline/revision mismatch。
- Source fingerprint physical identity，不以content digest去重。

### 13.2 Portable fixtures

- Exact Remote mutation order；commit last。
- Empty source。
- Empty-directory manifest reconstruction、--create-empty-src-dirs capable/unsupported Remote。
- Symlink --links round trip、literal .rclonelink collision、special/unknown source fail closed和hardlink/metadata fidelity。
- Valid UTF-8 path codec round trip、invalid UTF-8、encoded-rune、normalization/physical-key collision。
- Oversized entry/list/record/depth/spool/output。
- Stable strong checksum。
- weak/no-hash full download。
- metadata-only永不commit。
- eventual/inconsistent listing和read-after-write delay。
- source mutation during transfer/check。
- copy-dest eligible/ineligible、manual fallback、parent lease loss。
- dest-after从未用作manifest。
- 所有managed commands固定--retries 1、bounded low-level retry；outer retry绝不复用attempt。
- visible attempt collision、eventual absence不被宣称storage no-clobber、exclusive-writer drift。
- bound config+staged payload双输入、temp ownership marker、startup/periodic aged-orphan cleanup、cleanup failure不改写Provider truth、cancel cleanup。
- partial upload、partial manifest、marker write unknown。
- retry新attempt、不复用prefix。
- multiple attempts、marker/DB crash、deadline/takeover/quarantine。

### 13.3 AWS native protocol fixtures

- workload/static bootstrap union、AssumeRole correct/missing/wrong external ID、identity digest。
- Bootstrap S3 client construction impossible和negative probe。
- STS expiry/margin/cache refresh/fake-clock cancel；temporary bootstrap role-chain 3600秒上限和session_too_short。
- Official endpoint/general-purpose profile；custom/path-style/directory/access-point rejection。
- Same-account bucket、inline session policy s3:ResourceAccount、ExpectedBucketOwner和wrong-owner rejection。
- GetBucketEncryption AES256/aws:kms/aws:kms:dsse、BlockedEncryptionTypes、BucketKeyEnabled absent/false/true、default drift和permission denied。
- Generated Rclone config显式SSE-S3或full-ARN SSE-KMS header；SSE-C/AWS-managed KMS/alias/cross-account/custom/external/unknown rejection；Multi-Region exact regional member acceptance与wrong-region rejection。
- DescribeKey customer/symmetric/encrypt-decrypt/enabled/AWS_KMS/same-region-account；disabled/PendingDeletion/missing/wrong spec/origin/manager rejection。
- KMS active DescribeKey+GenerateDataKey+Decrypt、retained DescribeKey+Decrypt、kms:ViaService、bucket-key disabled时object-prefix context、enabled时bucket context+S3 prefix restriction，以及session/key-policy denial。
- Rclone继承冻结bucket-key mode、SDK control write显式镜像、config pre/post drift；exact-version HeadObject actual algorithm/key digest/BucketKeyEnabled、data/control encryption mismatch和KMS decrypt failure。
- KMS full-ARN rotation、automatic material-rotation same-ARN invariant、retained read-key references、count/bytes/2048-char/PackedPolicySize ring limits和historical key permission drift。
- Imported-baseline SSE-S3/KMS source inventory、temporary source-key decrypt policy、destination re-encryption与source-key set release。
- GetBucketVersioning Enabled/Suspended/empty。
- First-enable15-minute settle reset。
- Lifecycle no-config、prefix/and/tag/size filters、expiration/noncurrent/delete-marker、同步transition allowlist、archive/Intelligent-Tiering拒绝和unknown action。
- Lifecycle15-minute digest settle/reset。
- ListObjectVersions KeyMarker+VersionIdMarker分页，Versions/DeleteMarkers交错。
- Opaque VersionId、zero-byte object与delete marker区分。
- Exact GetObject full/range和marker error semantics。
- Canary physical identity。
- B0/B1各自double-full-list stability、unchanged/new/overwrite/delete/multiple-rewrite、cross-page writer drift和B0 version permanent removal。
- Mutation ledger referenced/superseded。
- Unknown external version、pagination drift和writer concurrency。
- Native commit control VersionId、multiple commit versions和crash windows。
- Exact reader拒绝current/latest/synthetic version names。

Wire fixtures固定AWS official API shape和error codes，但不把模拟器行为当认证。

### 13.4 Opt-in official AWS live conformance

若要对某个发布声明 aws_s3_general_purpose_v1 已完成 release-level live certification，必须运行opt-in live suite。该suite使用dedicated AWS official general-purpose S3 test bucket/prefix和最小权限AssumeRole环境：

- official regional endpoint；
- versioning enabled并完成15分钟settle；
- no matching destructive lifecycle；
- Rclone temporary STS write；
- SDK双markerlist、exact full/range read；
- overwrite/delete/delete-marker；
- B0/B1 capture和version graph；
- role/session identity、credential expiry；
- sse_s3_v1 single/multipart writes、exact Head/full/range和AES256 proof；
- sse_kms_cmk_v1使用预配置same-account customer-managed keys执行single/multipart、exact Head/full/range、KMS cost/permission proof；
- 从一个full key ARN rotation到第二个preconfigured key，旧point仍通过retained decrypt-only key读取；wrong/ungranted key fail closed；
- cleanup由独立test-admin fixture按exact test prefix执行，不进入产品runtime。

Live suite不创建、disable、schedule-delete或修改KMS key/policy；disabled/PendingDeletion等破坏性state使用official wire fixture验证。MinIO、LocalStack、Ceph或mock server只用于fault injection，不能替代live AWS/KMS evidence。

当前Child按用户2026-07-17的明确决定不创建上述fixture。`TestRcloneAWSLiveConformance`仍须保留、编译并在无fixture时明确列出所有缺失变量后skip；offline official-wire fixtures、SDK adapter、STS/S3/KMS admission、version graph、exact read和双加密profile逻辑测试须全部通过。该owner证据例外允许Child交付继续，但完成记录只能写live `not_executed`，不能把skip或fixture通过写成live pass，也不能声称当前发布已完成release-level AWS live certification。

### 13.5 Persistence、race 与 migration

SQLite和PostgreSQL覆盖：

- repository/link row locking与native single writer；
- producing TaskRun uniqueness；
- ProviderCommit transaction和exact replay；
- first exact commit/reconcile latch；
- activation不写latch；
- first-new/baseline/rollback；
- activation后zero-reservation clean rollback、first reservation race、failed/no-commit point仍阻断clean rollback、imported-baseline activation立即消耗窗口；
- managed activation原子清空Task.RsyncTarget，old runtime/generic update/import不能恢复mutable target；
- every 000064 down blocker；
- blocked down schema/data snapshot不变；
- managed-link resolver；
- active parent/child lease；
- reservation 000065至000071未占用。

Race/cancel suites覆盖lease renewal loss、admission drain、shutdown、SDK body close、command join、spool cleanup和stale-fence late writer。

### 13.6 API/frontend/security

- Standard response envelope和stable HTTP mapping。
- Full router Auth/RBAC/ownership/feature gate。
- Admin success；Operator/Viewer/missing/unknown role denial。
- Setup/preflight/activation expected-revision和opaque ID。
- Clean rollback zero-reservation proof、concurrent prepare conflict、rollback_capability unknown-safe mapping和rollback-preparation after failed attempt。
- Portable/native write-only secret round trip不回显；config/Remote不进入URL、response、log、audit或persistent frontend state。
- Native encryption setup SSE-S3/KMS cross-variant validation；KMS full ARN只write-only，safe summary仅返回profile/status/read-key count。
- Sanitized DTO/log/audit/metrics。
- Frontend raw mapper、unknown-blocked、decimal string和invalid values。
- Dialog mode/setup/settle/encryption/KMS cost+key-status/migration/rollback states。
- Secret clear/no persistence。
- zh/en parity、keyboard/focus、DialogTitle、labels、runAxe。

### 13.7 Full gates

Implementation完成后至少运行：

~~~text
cd backend && go test ./...
cd backend && go test -race ./internal/backupasset/... ./internal/task/...
cd backend && go build ./...
cd web && npm run check
make check
bash scripts/check-doc-freshness.sh
git diff --check
~~~

同时运行repository CI对应的golangci-lint、govulncheck、npm audit、bundle/coverage、Swagger freshness、migration UTC safety和Docker build。具体命令与文件映射由design批准后的implement.md冻结。

## 14. Rollout、documentation 与兼容性

### 14.1 Feature rollout

- backup_assets.enabled在Child 1至14继续默认false。
- Existing Rclone tasks不自动迁移。
- New managed activation必须成功preflight。
- Portable是UI默认推荐；native activation必须通过目标bucket/role/key的完整运行时preflight。当前Child没有release-level AWS live certification，公开文档和完成记录不得作相反声明。
- Unknown runtime/schema/profile/encryption或KMS status保持blocked。
- Startup先reconcile并完成KMS retained-key health，再开放native writer。

### 14.2 Compatibility

- Empty/legacy Rclone config行为保持。
- Legacy RcloneExecutor和restore path在legacy link上保持。
- Managed portable始终使用encrypted bound config exact revision；node_default永不自动导入或成为fallback。
- Managed link阻断legacy verifier、retention和integrity checker读取mutable target。
- Feature disable不清除latch、不恢复ordinary mutable fallback。
- Provider/API暂不可达不使exact point变成current-head fallback。
- Minimum-runtime marker防止old binary误写managed namespace。
- Existing native point的encryption/key digest不可因binding rotation改写；KMS unavailable只标记at-risk，不回退current、SSE-S3或portable。

### 14.3 Documentation truth

本Child只更新实际发生变化的Swagger/API、管理员安全配置和maintainer dependency/migration truth。公开README/用户文档不提前宣称backup-assets GA、三云native、WORM、release-level AWS live certification或official S3以外的支持。完整GA文档仍由父任务后续Child负责。

### 14.4 正确交付流程

实施获得独立授权后，最终同一分支/PR流程必须是：

1. 实现与验证；
2. Phase 3.4工作提交；
3. 同分支trellis-finish-work，归档和journal自动提交；
4. push；
5. 单一PR，不把功能与Trellis归档拆成两个PR；
6. required CI全部通过；
7. merge；
8. post-merge监控Release Please、可能的release、Publish Docker Images，以及README/release docs涉及时的Sync Docker Hub Description；
9. sync local main到origin/main。

本设计不授权上述任何动作。

## 15. 设计完成条件与剩余授权门

产品/风险决定已全部收敛。完整设计的不可弱化条件如下：

- no metadata-only commit；
- no directory rename assumption；
- no direct-static S3；
- no native brand inference；
- no bucket-default encryption inference or unknown KMS profile；
- no loss of historical KMS decrypt permission to make rotation pass；
- no lifecycle risk override；
- no current/latest exact-read fallback；
- no attempt prefix reuse；
- no node_default or dynamic credential fallback for managed portable；
- no clean ordinary rollback after first managed point/attempt reservation；
- no TaskRun/publication outcome conflation；
- no migration number consumption；
- no automatic legacy fallback after latch；
- no Provider cleanup/purge or exact-version/delete-marker deletion in Child 5；native sync的current delete-marker mutation必须完整入ledger。

完整本文已于2026-07-16获得独立批准。剩余授权门继续严格分离：

1. 使用writing-plans创建focused implement.md；
2. 用户单独审阅并批准implement.md；
3. 用户再单独授权task.py start；
4. 之后才可修改产品代码、migration、spec或公开文档。
