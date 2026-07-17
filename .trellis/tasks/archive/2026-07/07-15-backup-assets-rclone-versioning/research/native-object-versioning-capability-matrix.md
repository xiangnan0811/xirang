# Rclone 原生对象版本能力与认证矩阵研究

日期：2026-07-15
范围：Child 5 `native_object_versions` 的 Rclone v1.74.4 可观察合同、AWS S3 / Azure Blob / Google Cloud Storage 原生 API 真实性、credential/control-plane 边界与 fail-closed 认证范围。本文不把品牌名、S3-compatible 或泛化 Rclone feature 当认证。

范围决定：用户于 2026-07-15 选择方案 B；本 Child 只正向认证 AWS official general-purpose S3，其他 native profile fail closed。

Credential 决定：用户于 2026-07-15 选择 AWS credential 方案 1；所有 actual S3 API 调用强制使用 STS AssumeRole temporary credentials，bootstrap 可来自 server workload/default chain 或 encrypted static AssumeRole-only key，不提供 direct-static S3 fallback。

## 结论摘要

Rclone v1.74.4 的公开通用 CLI/RC **不足以**实现父 PRD 要求的原生版本合同：它没有跨 backend 的真实 version ID/delete-state/lifecycle API；S3 的旧版本功能使用合成时间文件名，无法安全替代真实 ID；Azure Blob 与原生 GCS backend 当前甚至没有版本枚举/精确版本读路径。

AWS S3、Azure Blob 和 GCS 的 provider-native API 都能表达真实版本，但认证所需的 list/read/versioning/lifecycle 权限和身份平面不同。当前 Xirang 的 node-default Rclone config 只存在于受管节点/执行账户，服务端 binding 没有这些秘密；data-plane Rclone 凭据也经常没有 lifecycle/control-plane 权限。因此若本 Child 要真正启用任一 `native_object_versions` backend，必须新增显式、加密、provider-specific direct native binding（或等价的新 node-side SDK/helper runtime plane），并通过 canary 证明它与 Rclone 写入的 exact bucket/container/prefix 是同一物理目标。不能通过导出任意 Rclone config 猜测复用。

## Rclone v1.74.4 为什么不能直接认证

### 通用 features / `lsjson ID`

- `operations/fsinfo` 的 `Features` 只有通用 optional feature；`Command=true` 只表示 backend 注册了某些私有命令，不列出命令名，更不证明 version list/delete marker/lifecycle。
- `lsjson` 只有对象实现 `fs.IDer` 时才输出 `ID`（`fs/operations/lsjson.go`）。该 ID 的语义由 backend 决定，并非标准 object version ID。
- S3 `Object` 内部持有 `versionID`，但没有实现 `IDer`；`Metadata()` 也不返回 version ID。因此普通 `lsjson` / RC list 看不到真实 S3 version ID。

### S3 特例仍不够

- [`--s3-versions`](https://rclone.org/s3/#versions) 把旧版本映射为 `file-vYYYY-MM-DD-HHMMSS-mmm.ext` 合成名称。官方明确警告：真实对象名若符合这种格式，行为不可预测。
- 合成名由 LastModified 时间产生；它不是 opaque version ID，同秒/时间精度碰撞与真实文件名冲突使它不能成为永久 exact locator。
- `--s3-version-deleted` 把 delete marker 显示为大小 0 的合成文件，只允许删除；这不等同于带 `is_delete_marker` 的 manifest entry。
- `backend restore-status -o all` 的 JSON 虽可偶然输出内部 `VersionID`，但输出没有 delete-marker boolean、IsLatest、完整 lifecycle 语义；delete marker 与真实零字节版本无法区分。精确 read 仍要通过合成路径反查内部 ID，不能满足“按真实 ID 读/Range/重建”。
- S3 `backend versioning` 只能 Get/Put bucket versioning；没有 lifecycle/retention proof command。
- Rclone 文档还明确记录 GCS S3 compatibility 的 versions 分页错误；`type=s3` 或某 provider 字符串不能继承 AWS S3 认证。

### Azure Blob 与 GCS backend

- v1.74.4 Azure Blob list 没有 include versions 的通用 publication contract，对象 read/delete 不接受 versionid；现有 snapshot delete 配置不是 blob version list/read。
- v1.74.4 原生 GCS backend 对 `Objects.List` 不设置 `versions=true`，对象结构不保存 generation，Get/Delete 不绑定 generation。
- 两者均没有 backend command 提供 versioning + lifecycle + exact version reconstruction 合同。

源码固定于 [Rclone v1.74.4](https://github.com/rclone/rclone/tree/v1.74.4)：`backend/s3/s3.go`、`backend/azureblob/azureblob.go`、`backend/googlecloudstorage/googlecloudstorage.go`、`fs/operations/lsjson.go`。

## Provider-native 真实性矩阵

| 能力 | AWS S3 general-purpose bucket | Azure Blob primary endpoint | Google Cloud Storage JSON API |
| --- | --- | --- | --- |
| 枚举全部版本 | `ListObjectVersions`，返回 `Version` + `DeleteMarker`，两者都有真实 `VersionId`；分页同时使用 key marker 与 version-id marker | `List Blobs?include=versions` 返回 `VersionId`、`IsCurrentVersion`；soft-delete 字段需单独 include/解释 | `objects.list?versions=true`，每项 `generation`、`metageneration`、`timeDeleted`、size/hash |
| 删除语义 | 无 versionId 的 delete 新增 delete marker；manifest 记录 marker 的真实 VersionId | 删除 base 后原 current 变 previous，**不产生 S3 marker**，当前变为 absence | 删除 live 后留下 noncurrent generation，**不产生 S3 marker**，当前变为 absence |
| 精确整对象/Range | `GetObject?versionId=...` + Range；指定 delete marker 返回 405，current marker 通常 404 | `Get Blob?versionid=<opaque>` + Range，成功 Range 为 206 | `objects.get?generation=...&alt=media` + HTTP Range |
| 精确删除（未来 lifecycle child） | `DeleteObject?versionId=...`；MFA Delete/权限必须单独门禁 | `Delete Blob?versionid=...`；previous-version delete 有专用 data action | `objects.delete?generation=...`，配 `ifGenerationMatch` |
| versioning proof | `GetBucketVersioning == Enabled`；`Suspended`/empty 拒绝 | ARM blob service properties `isVersioningEnabled=true` | `buckets.get.versioning.enabled=true` |
| lifecycle/retention proof | `GetBucketLifecycleConfiguration`，解析 rule filter、NoncurrentVersionExpiration/transition；可选 Object Lock 只增强证据 | ARM `managementPolicies/default`，解析 previous-version delete/tier rules；versioning 与 HNS/account kind 也在 control plane | `buckets.get` 同时返回 lifecycle、retentionPolicy、softDeletePolicy |
| consistency | object GET/PUT/DELETE/LIST 强一致 | primary endpoint read/list 强一致和 snapshot isolation；secondary endpoint 最终一致，拒绝 | object read-after-write/list/delete 全球强一致；IAM grant/revoke 最终一致 |
| 主要排除项 | directory buckets 不支持 ListObjectVersions；初始认证排除 access point/Outposts/custom endpoint/S3-compatible | HNS/ADLS Gen2 不支持 versioning；拒绝 RA secondary、Azure Stack/custom endpoint；初始 profile 限 block blobs | HNS 不支持 Object Versioning；S3 interoperability/custom endpoint 拒绝；soft-deleted 对象不可作为在线可读版本 |

### AWS S3 官方证据

- [`ListObjectVersions`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectVersions.html)：Versions、DeleteMarkers、VersionId、双 marker 分页；权限 `s3:ListBucketVersions`；directory buckets 不支持。
- [`GetObject`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)：`versionId` 精确读取需要 `s3:GetObjectVersion`；指定 delete marker 返回 405，current delete marker 示例为 404。
- [`DeleteObject`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObject.html)：带/不带 version ID 的不同语义。
- [`GetBucketVersioning`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketVersioning.html)：`Enabled | Suspended | empty`。
- [`GetBucketLifecycleConfiguration`](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetBucketLifecycleConfiguration.html)：权限 `s3:GetLifecycleConfiguration`，rule filter 与 `NoncurrentVersionExpiration`。
- [Amazon S3 consistency model](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html#ConsistencyModel)：成功 write 后 read/list 强一致。
- [Versioning workflow](https://docs.aws.amazon.com/AmazonS3/latest/userguide/versioning-workflows.html) 与 [Enabling versioning](https://docs.aws.amazon.com/AmazonS3/latest/userguide/manage-versioning-examples.html)：首次启用 versioning 最多可能需要 15 分钟传播，AWS 建议在此期间不执行 PUT/DELETE。
- [Setting an S3 Lifecycle configuration](https://docs.aws.amazon.com/AmazonS3/latest/userguide/how-to-set-lifecycle-configuration-intro.html) 与 [Expiring objects](https://docs.aws.amazon.com/AmazonS3/latest/userguide/lifecycle-expire-general-considerations.html)：新建、更新、删除 lifecycle rule 存在传播延迟；AWS 对 rule policy change 记录了最长 15 分钟的传播窗口，且已满足条件的 action 异步执行。API 没有可绑定的 last-modified revision，因此研究建议首个/变化后的 canonical lifecycle digest 至少稳定观察 15 分钟并再次读取，任何 digest 变化重置窗口；这仍不能让有 destructive rule 的 prefix 通过。
- [Lifecycle transition considerations](https://docs.aws.amazon.com/AmazonS3/latest/userguide/lifecycle-transition-general-considerations.html) 与 [S3 storage classes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/storage-class-intro.html)：STANDARD_IA、ONEZONE_IA 与 GLACIER_IR 提供 millisecond/real-time access；GLACIER 与 DEEP_ARCHIVE 必须 restore 后读取。INTELLIGENT_TIERING 的可选 Archive Access/Deep Archive Access 也是异步 restore。因此首个 profile 只允许前三个 matching transition destination，拒绝 GLACIER、DEEP_ARCHIVE、INTELLIGENT_TIERING 和 unknown class；扩大 allowlist 必须新 profile revision 与 live exact-version GET conformance。

最低只读认证权限为 `s3:GetBucketVersioning`、`s3:GetLifecycleConfiguration`、`s3:ListBucketVersions`、`s3:GetObjectVersion`；若 lifecycle filter 依赖 object tags，需 `s3:GetObjectVersionTagging` 或拒绝该 profile。未来 exact delete 需 `s3:DeleteObjectVersion`；Object Lock/WORM 额外证明还需对应 config/retention/legal-hold read 权限。版本化与 lifecycle proof 不能自动宣称 storage WORM，手工 exact delete 仍可能存在。

### AWS encryption profile evidence（final audit）

官方与Rclone v1.74.4证据：

- [S3 bucket default encryption](https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucket-encryption.html)说明自2023-01-05起所有新object至少使用SSE-S3；bucket也可配置SSE-KMS或DSSE-KMS。`GetBucketEncryption`需要`s3:GetEncryptionConfiguration`，返回algorithm、KMS key ID、`BucketKeyEnabled`与`BlockedEncryptionTypes`；后者是2026年SSE-C默认阻断状态的真实控制面字段。
- [SSE-KMS](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingKMSEncryption.html)要求PutObject的`kms:GenerateDataKey`、GetObject的`kms:Decrypt`，multipart同时需要两者；KMS key必须与bucket同Region。Customer-managed key应使用full key ARN，alias可能在requester account解析到错误key。
- [`DescribeKey`](https://docs.aws.amazon.com/kms/latest/APIReference/API_DescribeKey.html)需要`kms:DescribeKey`并返回ARN、KeyState、KeyManager、KeySpec、KeyUsage、Origin与deletion date。只有same-account、symmetric、ENCRYPT_DECRYPT、Enabled且非pending deletion的customer-managed key可以进入首个认证profile。
- [KMS automatic rotation](https://docs.aws.amazon.com/kms/latest/developerguide/rotating-keys-enable.html)不改变key ID/ARN、policy或permission，既有ciphertext继续可解；full ARN因此是稳定identity。换成另一个key ARN则是capability revision变化，旧point仍依赖旧key。
- [KMS deletion](https://docs.aws.amazon.com/kms/latest/developerguide/deleting-keys.html)不可逆；PendingDeletion期间已不能执行cryptographic operation，删除后旧object不可恢复。Native health必须把disabled/pending-deletion/missing key立即标为at-risk并拒绝新commit。
- [KMS `kms:ViaService`](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html#conditions-kms-via-service)支持`s3.<region>.amazonaws.com`，session/key policy还可结合S3 encryption context约束使用范围。AWS明确规定未使用S3 Bucket Key时`aws:s3:arn`是object ARN，使用Bucket Key时则是bucket ARN；两者不能套用同一个prefix条件。
- [`AssumeRole`](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)将inline+managed session policy plaintext限制为2048 characters，并另有packed binary limit/PackedPolicySize；retained key ARN集合必须同时受count、serialized bytes与packed-size阈值约束，不能用wildcard绕过。
- [SSE-C](https://docs.aws.amazon.com/AmazonS3/latest/userguide/ServerSideEncryptionCustomerKeys.html)要求每次请求提供secret key、每个version可能使用不同key且key丢失即object丢失；AWS从2026-04起对新general-purpose bucket默认阻断SSE-C。它会引入per-version secret key registry和独立rotation/recovery plane，不应混入首个profile。
- Rclone v1.74.4 `backend/s3/s3.go`显式支持`server_side_encryption`、`sse_kms_key_id`和SSE-C字段，并在PutObject/CopyObject设置对应header；因此SSE-S3与SSE-KMS可由Xirang生成的最小config明确指定，不能只继承可能漂移的bucket default。该版本没有`BucketKeyEnabled`请求配置，SSE-KMS的effective bucket-key mode必须来自冻结并反复核对的bucket配置，direct SDK control writes镜像该值。Exact `HeadObject(versionId)`会返回`ServerSideEncryption`、`SSEKMSKeyId`与`BucketKeyEnabled`，可把实际per-version encryption identity写入private manifest evidence。

| 方案 | 认证范围 | 优点 | 代价/风险 |
| --- | --- | --- | --- |
| SSE-S3 only | 每个managed write显式AES256；exact Head必须为AES256 | 无KMS dependency/permission/cost/key-lifecycle；最小且完整的固定profile | 常见的customer-managed SSE-KMS bucket/policy无法启用native，只能portable |
| SSE-S3 + same-account customer-managed SSE-KMS | 两个closed encryption subprofiles；KMS使用full key ARN、DescribeKey、write key与retained read-key ring、exact Head与live suite | 覆盖AWS生产环境的主要key-control模式；不会把KMS bucket错误包装成unsupported generic S3 | 增加AWS KMS SDK、key/session policy、费用/限流、rotation/disable/delete health与历史key权限测试 |
| Generic bucket default / all encryption | 接受AWS-managed KMS、DSSE-KMS、SSE-C或unknown并依赖canary | 表面兼容最多 | 无统一key identity/secret/lifecycle合同；无法保证所有historical VersionId持续可解，拒绝 |

基于“完整终态设计、预算充足”的偏好，研究推荐第二项，用户于2026-07-16同意；最终合同保持边界闭合：

1. Native binding保存`encryption_profile = sse_s3_v1 | sse_kms_cmk_v1`。SSE-KMS保存active full key ARN的加密值/keyed digest，不接受alias、cross-account、AWS-managed key、DSSE-KMS、SSE-C或custom/external key store。
2. Xirang显式设置每个data/control write的encryption header。Preflight同时读取bucket encryption配置、DescribeKey和exact canary Head；bucket default只作为drift/cost evidence，不替代actual-object proof。
3. Active write key需要`kms:GenerateDataKey`+`kms:Decrypt`；所有仍被committed point引用的历史key需要`kms:Decrypt`。AssumeRole inline session policy和key policy使用exact ARN、`kms:ViaService`及受管prefix/bucket encryption context约束。
4. Manifest逐version记录algorithm、key-ARN digest与BucketKeyEnabled。Key ARN rotation先pause/drain/repreflight；旧key形成有界read-key ring，在最后引用point由后续lifecycle清除前不得撤权。Automatic key-material rotation因ARN不变可继续使用。
5. KMS permission/throttle/outage是typed availability；key disabled/pending deletion/missing或exact historical decrypt失败是at-risk/integrity gate。Child 5不创建、修改、rotate、enable/disable或删除KMS key/policy。
6. Official live conformance分别覆盖SSE-S3与customer-managed SSE-KMS single/multipart、exact-version full/range、automatic material rotation invariant、wrong key/permission、disabled/pending-deletion模拟边界。Mock只用于fault injection，不能认证profile。

### Azure Blob 官方证据

- [Blob versioning overview](https://learn.microsoft.com/en-us/azure/storage/blobs/versioning-overview)：每次 write 创建唯一 version ID；delete base 后 current 变 previous 且不再有 current；HNS 不支持。
- [`List Blobs`](https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs)：`include=versions`、`VersionId`、`IsCurrentVersion`、soft-delete fields。
- [`Get Blob`](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob)：`versionid` 是 opaque DateTime 值，支持 Range。
- [Azure Storage consistency](https://learn.microsoft.com/en-us/azure/storage/blobs/concurrency-manage)：primary 强一致与 snapshot isolation；[geo-redundant secondary](https://learn.microsoft.com/en-us/azure/storage/common/geo-redundant-design) 可能滞后。
- [Lifecycle overview](https://learn.microsoft.com/en-us/azure/storage/blobs/lifecycle-management-overview) 与 [ARM ManagementPolicies Get](https://learn.microsoft.com/en-us/rest/api/storagerp/management-policies/get)：policy 修改/停用最多约 24h 才完成执行收敛。

数据面身份可 list/read versions，但 versioning 状态与 lifecycle policy 是 ARM/control-plane 资源。container SAS 或 Storage Blob Data Reader 通常无 `storageAccounts/read`、`blobServices/read`、`managementPolicies/read`，所以一个通用 Rclone SAS 不能完成 admission proof。direct binding 必须提供并轮换 data-plane + ARM 能力，或使用同一 service principal 明确授予两面最小权限。

### GCS 官方证据

- [Object Versioning](https://cloud.google.com/storage/docs/object-versioning)：noncurrent generation、删除与 live/noncurrent 语义。
- [`objects.list`](https://cloud.google.com/storage/docs/json_api/v1/objects/list)、[`objects.get`](https://cloud.google.com/storage/docs/json_api/v1/objects/get)、[`objects.delete`](https://cloud.google.com/storage/docs/json_api/v1/objects/delete)：`versions=true`、exact generation read/delete。
- [`buckets.get`](https://cloud.google.com/storage/docs/json_api/v1/buckets/get)：versioning、lifecycle、retention、soft-delete metadata。
- [Lifecycle](https://cloud.google.com/storage/docs/lifecycle)：`daysSinceNoncurrentTime`、`numNewerVersions`；配置变化最多 24h 生效，旧规则仍可能动作。
- [Consistency](https://cloud.google.com/storage/docs/consistency)：object read/list/delete 强一致，IAM 变更最终一致。
- [Hierarchical namespace](https://cloud.google.com/storage/docs/hns-overview)：HNS 与 Object Versioning 的限制必须 fail closed。
- [JSON API discovery document](https://storage.googleapis.com/$discovery/rest?version=v1)：generation/metageneration/size 等 64 位数均以 JSON string 表达。

最低 proof 权限为 `storage.buckets.get`、`storage.objects.list`、`storage.objects.get`；未来 exact delete 需 `storage.objects.delete`。generation 必须在 Go evidence/JSON/frontend 始终按 opaque decimal string 保存，不能进 JS number。soft delete 与 Object Versioning 不是同一能力；soft-deleted object 不可直接精确读取，因此不能把 soft-delete retention 当作已引用 point 的在线版本保留证明。

## 明确认证 profile（fail closed）

“provider/backend 名称相似”不是认证。每个 profile 必须固定：

- provider kind、官方 endpoint/寻址模式、SDK/API revision 与支持的 bucket/account/object kind；
- exact list pagination、version/delete/absence DTO、generation/version ID string 规则；
- exact get/range/reconstruct/delete fixture；
- versioning/lifecycle/retention/soft-delete/Object Lock 解析规则与状态 digest；
- consistency class、settle/reprobe 窗口、permission matrix、credential revision/expiry；
- Rclone data path 与 native binding 的双向身份证据；
- 不支持项与 drift reason codes。

初始矩阵若认证 AWS，只能是 AWS official general-purpose S3 profile。所有 S3-compatible/custom endpoint、directory buckets、Azure/GCS S3 interop、未认证 Rclone wrapper（如 crypt 跨层隐藏 provider identity）均拒绝 native mode并推荐 `versioned_prefix`。

## direct native binding 与身份匹配

当前仓库事实：

- legacy Rclone task 通过 SSH 在受管节点执行，通常使用 node-default config；Repository binding 只有 node/task/locator/config-source facts，`Secret` 为空。
- config 可能来自节点环境、instance role/profile、file、MSI、Azure CLI、ADC、短期 token、SAS 或 chained Rclone remote。离开节点/执行用户后不可用；解析/导出整个 config 会扩大 secret plane。
- bound config 虽可通过 stdin给 Rclone，也不能证明它包含 lifecycle/control-plane 权限，且任意 backend/wrapper 无法安全映射成统一 SDK credential。
- backend `go.mod` 当前没有 AWS/Azure/GCS SDK；三云 direct support 是新的依赖、网络 egress、凭据轮换和 SSRF/endpoint allowlist 安全面。
- 现有通用 `AppCredential` 只允许数据库/容器 profile，公开 sanitizer 只删除键名 `password`。若直接塞入 `access_key_id`、`secret_access_key` 或 session token，当前 API 会把它们放进 sanitized config；因此不能复用现有 handler/model 而只添加一个 AWS type。AWS direct secret 必须留在专用 encrypted repository binding，公开 DTO 只返回 credential mode、状态、revision/expiry 和 safe reason。

如果启用 native profile，应使用 strict composite managed-Rclone binding：同一个 encrypted active binding 同时记录 Rclone command access、direct native credential reference/secret、provider profile revision、endpoint identity digest、managed prefix、preflight/capability revision 与 rollback facts。000062 的单 active binding + encrypted text 可以承载闭合 document，不需要为了“第二行 credential”放宽唯一索引。

preflight 通过 reserved prefix canary 证明同一物理目标：Rclone 写入高熵 nonce 对象，direct native adapter 用官方 API list/get 同一个对象并核对 bytes/version identity，Rclone再删除/清理 probe；所有 raw endpoint/bucket/key/version/nonce 只在受控内存/加密 evidence，日志/API只留 digest/reason。若 server 无法访问 provider endpoint、凭据 scope 不足、identity 不同或 lifecycle/control-plane read 失败，native admission 失败；不能静默降为 prefix 或复用旧 preflight。

### AWS credential source 方案

官方依据：

- [AWS SDK for Go v2 credential chain](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html) 可从环境、shared config、ECS task role、EC2 role 等来源发现 bootstrap credential。
- [AWS IAM best practices](https://docs.aws.amazon.com/IAM/latest/UserGuide/best-practices.html) 明确建议 workload 使用 IAM role 的 temporary credentials，仅在无法使用 role 时保留/轮换 long-term access keys。
- [`stscreds.AssumeRoleProvider`](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/credentials/stscreds) 返回有 expiry 的临时凭据并支持 `ExternalID`；[第三方 role 官方指南](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_common-scenarios_third-party.html) 要求用独立 external ID 防 confused deputy，且 external ID 本身不是 secret。

| 方案 | Binding 保存内容 | S3 调用身份 | 优点 | 代价 |
| --- | --- | --- | --- | --- |
| 1. 强制 AssumeRole，双 bootstrap | mode + role ARN + Xirang 生成 external ID；bootstrap 可来自 server SDK default chain，或 encrypted static access key 但只获准 `sts:AssumeRole` | 始终使用 cached/自动刷新 STS temporary credentials | 最小 S3 权限、短期凭据、role/external-ID 可审计；自建环境仍可用静态 bootstrap | 运维必须创建 trust policy/role；static bootstrap 仍需轮换，但不能直接读 S3 |
| 2. Role-first + direct-static fallback | 同上，另允许 encrypted static key 直接调用 S3 | role 或 long-lived IAM user | 兼容无法建 role 的环境 | 增加一条长期高权限路径、两套 rotation/expiry/evidence 和更多误配置状态 |
| 3. 只允许 server workload identity | role ARN/external ID，不存 AWS key | server default chain → STS role | Xirang 不持久化 AWS secret，安全面最小 | 非 AWS/未配置 workload federation 的自托管部署无法启用 native mode |

用户已选择方案 1。它不是“只支持 AWS 托管环境”：非 AWS 自托管安装仍可提供一对 encrypted bootstrap key，但该 principal 只允许 AssumeRole，所有 S3 list/get/versioning/lifecycle 操作都由短期 repository role 执行。preflight 对目标 bucket 执行有界的 base-principal negative probes，并记录 role/account/principal 的 keyed digest、credential source kind、expiry 与 refresh failure reason；不在通用 Repository/Task DTO、日志或审计中记录 ARN/key/token 原文。Xirang 生成的 external ID 按 AWS 官方定义不是 secret，可由专用 Admin setup response 显示以配置 trust policy，但不进入普通读取 DTO。

## native point capture 的最低合同

1. Task/link/credential/capability revision、managed prefix、versioning/lifecycle digest 与 absolute deadline 在 mutation 前冻结；旧 preflight、permission drift、endpoint drift 立即拒绝。
2. native prefix 必须是 Xirang 独占 writer namespace。外部 writer 不能被任何 provider API“证明不存在”；preflight/IAM isolation、Rclone attempt metadata、before/after version listing 和 capture interval 共同提供 attribution，发现未知 version/delete/absence 即 quarantine。
3. Rclone transfer exit=0 后，通过 provider-native强一致 list 构造每个 key 的 exact version ID/generation或 delete/absence state。S3 marker 与 Azure/GCS absence 是不同 tagged union，不能用一个 nullable string 猜测。
4. manifest 绑定 normalized key、exact version/generation、size/hash、current/delete/absence、capture interval、provider/profile/capability revision与 lifecycle proof digest；version IDs不进入公共 DTO/log。
5. commit 前按 manifest exact version 全量或有界策略执行 get/range/reconstruct proof；恢复/未来 delete 只接受 manifest exact ID，不读取 current head。
6. lifecycle proof 必须完整解析 rule/filter/tag 并证明 managed prefix 不匹配自动 expire/delete version、delete-marker cleanup 或不可同步读取 transition。无配置可以通过；unknown、permission denied、匹配 destructive/offline action 或仍在传播收敛窗口的刚修改配置必须拒绝，不能按 Task retention 推算一个可接受 deadline。
7. 每次 run 和周期 health reconcile 重读 capability/lifecycle digest。已 committed point 遇到 provider drift 进入 typed degraded/at-risk 状态，不改写历史 manifest，也不静默切到 portable。
8. `backend_versioned` 只说明 provider 保留精确版本；除非逐项证明 Object Lock/locked retention，否则不显示 WORM/合规不可删除。

### S3 lifecycle ownership 选项

当前 `Policy` 同时支持 simple `retention_days` 与 GFS keep-count，但 managed-history guard 已阻断 legacy Rclone retention；父任务把 exact RecoveryPoint selection/deletion 留给后续 lifecycle Child。GFS keep-count 依赖未来运行序列，不能在 Child 5 preflight 被诚实压成一个不变的“最晚删除日期”。AWS lifecycle policy 更新与旧规则执行还存在传播窗口。

| 选项 | Admission | 优点 | 风险/代价 |
| --- | --- | --- | --- |
| 1. 无匹配 destructive rule | managed prefix 不匹配 version expiration、delete-marker cleanup 或不可同步读取 transition；否则 native unsupported | 保证 point 引用的 version/delete marker 在 Xirang 删除前保持在线；不抢占后续 exact lifecycle owner | provider storage 持续增长；需要 operator 调整 bucket/prefix lifecycle |
| 2. 计算 Task retention horizon | 解析全部 prefix/tag/size rule，并证明 action 晚于 simple/GFS horizon + propagation/safety margin | 可与现有 bucket 成本策略共存 | GFS horizon 不是固定时间；Policy 后续变化会追溯影响已 committed points；把后续 lifecycle planner拉入本 Child |
| 3. 风险确认后 degraded | 记录 warning 后允许匹配 rule | 最宽松 | 已 committed exact version 可被 provider 提前删除，违反 fail-closed 与 exact reconstruction |

用户已选择选项 1。首个 AWS profile 将其冻结为 admission 硬门：完整证明 managed prefix 不匹配 destructive/offline lifecycle action，否则返回稳定 unsupported reason 并推荐 `versioned_prefix`；不自动修改 bucket lifecycle，不接受风险豁免，也不把后续 exact RecoveryPoint lifecycle planner拉入本 Child。允许仍在线、同步可读 storage-class transition 可以作为未来独立认证 profile，但 Child 5 的首个 AWS profile不把 archive restore workflow混入 publication commit。

## 本 Child 可选范围比较

| 选择 | 实际交付 | 优点 | 代价/风险 | 对当前 PRD 的诚实表述 |
| --- | --- | --- | --- | --- |
| A. 不扩大 credential plane | 完整 portable prefix；实现 native tagged contract/matrix/admission，但 certified profiles=0、UI 不可选择 native | 最小 secret/依赖/网络面；所有 Rclone remotes 都有可信默认 | native 只有封闭扩展点，不是可用功能 | 需要明确修订“本 Child 提供可用 native mode”的要求；不能把整个 native 终态称为完成 |
| B. direct binding + AWS S3 | 完整 portable prefix；新增 AWS SDK/credential/profile，认证 official general-purpose S3；Azure/GCS/S3-compatible 明确拒绝 | 至少一个真实 backend 端到端验证 native union、manifest、drift、lifecycle 与 exact read；AWS versioning/lifecycle 在同一 S3 API/IAM 面 | 新 secret plane、SDK、network/endpoint/canary/UI；仍需大量 conformance/security tests | **用户已选择** |
| C. 同时 direct AWS/Azure/GCS | 一次实现三 SDK、三 auth/lifecycle profile与 control-plane UI | 覆盖三大公有云 | Azure 双平面、GCS/Azure soft-delete/HNS、三套轮换/依赖/fixtures 显著扩大 Child；风险与审阅量远超 publication 主线 | 只有产品明确要求本 Child 三云原生覆盖时才选；应预期独立 provider workstreams，而不是当作简单 Rclone flag |

方案 A 可以是 portable 的完整终态，但不是当前父 PRD 所述 native 功能终态。方案 B 不是根据品牌猜测的 MVP：它是一个明确、可长期保持的认证矩阵——AWS official S3 支持，其他 profile 永久 fail closed，未来是否扩展不影响现有 typed contract。方案 C 覆盖最多，但把三种本质不同的 identity/control-plane 生命周期塞进同一个 Child，扩大 secret 与供应链风险。

## 对 focused design 的直接结论

1. 不得通过 `backend features`、`lsjson ID`、`--s3-versions`、`restore-status`、Rclone backend 名或 S3-compatible 声明 native 能力。
2. 任一可用 native profile 都必须有独立 direct native binding/SDK（或明确的新 node-side helper plane）、canary identity match、最小权限与 lifecycle/control-plane proof。
3. provider manifest 使用 tagged delete state：S3 `object_version | delete_marker`；Azure/GCS `object_version | current_absent`，generation/version ID 始终 string。
4. exact list/read/range/reconstruct 与 lifecycle retention 是 admission 硬门；unknown/permission drift/profile drift fail closed，推荐 portable prefix。
5. native 只声称 `backend_versioned`，不自动声称 WORM。
6. 方案 B、STS-mandatory credential 方案、lifecycle 选项 1，以及SSE-S3+same-account customer-managed SSE-KMS双subprofile均已批准；focused `design.md` 必须把 bootstrap union、AssumeRole identity/expiry/refresh、negative probe、无 fallback、无匹配 destructive/offline lifecycle与KMS key lifecycle admission写成闭合合同。
