# 备份资产领域基础：Focused Technical Design

- **状态：** implementation complete；PR #372 merged，post-merge gates verified
- **日期：** 2026-07-13
- **父契约：** 07-12-backup-data-explorer-design 的 prd.md、design.md 与 implement.md Child 1
- **实现基线：** main@4ea4c2b，当前分支 seed@743aa94

## 1. 目标与硬边界

Child 1 只建立后续十四个子任务共同依赖的领域、安全和持久化基础。它必须交付：

1. 独立于 Task/TaskRun 的 Repository、RecoveryPoint、Manifest、Catalog generation/entry 身份；
2. 不可绕过的状态机、复合 AssetRef、typed capability/error；
3. SQLite/PostgreSQL 同版本 schema 和真实 apply/down 门禁；
4. 四个独立 key domain、可 rewrap 的小型数据密钥 envelope；
5. 跨进程重启可协调的 RecoveryPointLease/fence；
6. action-bound step-up proof、完整既有 caller 迁移和专用 RBAC；
7. typed asset audit registry、keyed fingerprint、segment/checkpoint chain；
8. 默认关闭的 feature gate 与安全 settings。

本子任务明确不做：

- 不注册 backup repository、recovery point、asset、content、export 或 recovery 公共路由；
- 不读取、写入、移动、发布或删除任何 Provider 字节；
- 不从旧 TaskRun、目录时间或 SnapshotFileIndex 推断 RecoveryPoint；
- 不改变现有 Task 删除/retention 语义；只增加 nullable archived_at；
- 不增加资产 UI，不把 backup_assets.enabled 设为 true；
- 不实现 Provider adapter、Catalog builder、search、ticket、Worker、export、recovery 或 purge worker。

唯一会立即改变既有运行行为的部分是 step-up 安全收紧：旧通用 proof 被拒绝，后端和同镜像前端必须原子迁移。

## 2. 包边界

### 2.1 backend/internal/backupasset

该包拥有备份资产领域语言，不依赖 Gin/middleware，不执行 Provider I/O：

- domain.go：opaque ID、枚举、AssetRef、CapabilitySet、状态/组合校验；
- errors.go：sentinel errors 和安全错误分类；
- service.go：Task/provider capability 映射、显式 sanitized DTO 转换、feature gate 读取；
- keyring.go：wrapped-domain-key 的持久化生命周期；
- lease.go：RecoveryPointLease acquire/renew/release/takeover/validate/reconcile；
- authorization.go：permission 常量和恢复结果授权纯函数；
- audit_action.go：唯一资产 action/field registry；
- audit.go：sanitizer、fingerprint、segment writer/checkpoint verifier。

该包可以依赖 model、secure、settings 和 GORM；它不得依赖 api/handlers、middleware、task executor 或具体 Provider。

### 2.2 backend/internal/model

持久实体按职责拆为四个文件：

- backup_asset.go：Repository、AccessBinding、TaskRepositoryLink、RecoveryPoint、Manifest、WrappedDomainKey；
- backup_asset_catalog.go：CatalogGeneration、CatalogEntry；
- backup_asset_lease.go：RecoveryPointLease；
- backup_asset_audit.go：BackupAssetAuditEvent、BackupAssetAuditCheckpoint。

秘密/locator/wrapped-key 字段一律 json:"-"。Raw GORM model 不允许成为 API response；backupasset/service.go 提供显式 DTO。

### 2.3 auth 与 secure

- auth/step_up_action.go 是 step-up action allowlist 的单一后端来源；
- auth/jwt.go 只负责签发/解析 token class 和 action claim；
- handlers/step_up.go 将 expected StepUpAction 传给验证器，并继续负责 HTTP/audit envelope；
- secure/keyring.go 只实现小型随机 key 的 wrap/unwrap primitive，不导入 model 或 GORM；
- backupasset/keyring.go 才负责数据库版本、激活、overlap、lost 和 rewrap。

这样避免 model → secure → model 的 package cycle，也不把数据库生命周期塞进底层密码包。

## 3. 身份与领域类型

### 3.1 Opaque IDs

- Repository、AccessBinding、TaskRepositoryLink、RecoveryPoint、Manifest、CatalogGeneration、Lease 使用 16-byte crypto/rand，编码为 32 位小写 hex。
- ValidateOpaqueID 严格接受 32 位 [0-9a-f]；不接受数据库自增 ID、路径、Provider snapshot short ID 或用户自定义字符串。
- Catalog entry ID 使用 Entry Identity Key 对规范路径做 HMAC-SHA-256，保留完整 64 位小写 hex。
- RecoveryPoint ID 与 entry ID 的格式不同但都视为 opaque；调用方不得解析或推断。

AssetRef 的 wire contract 固定为：

    type AssetRef struct {
        RecoveryPointID string json:"recovery_point_id"
        EntryID         string json:"entry_id"
    }

ValidateAssetRef 要求两个字段同时存在且格式正确。任何 resolver、overlay、ticket 或 job API 都只能接收完整 AssetRef；entry ID 单独查询返回 ErrInvalidAssetRef。

### 3.2 冻结枚举

Child 1 冻结以下 typed values：

- RepositoryStatus：connecting、online、degraded、offline、disconnected、purging、purge_blocked；
- TaskPublicationMode：legacy_mutable、versioned_hardlink、versioned_full_copy、versioned_prefix、native_object_versions；
- VersionMode：native_snapshot、hardlink_tree、full_copy_tree、versioned_prefix、native_object_versions、mutable_head；
- PointVersionSemantics：native_snapshot、xirang_manifest、imported_baseline、mutable_head；
- RecoveryPointState：observed、retired、preparing、verifying、committed、degraded、expiring、expired、failed、purge_blocked；
- RetirementReason：cutover、withdrawn；
- ImmutabilityLevel：mutable、xirang_managed、backend_versioned、storage_worm；
- PhysicalAvailability：online、offline、missing、unknown；
- HoldState：none、active、released；
- ManifestCompleteness：complete、partial、unavailable；
- CatalogGenerationState：building、complete、partial、failed、superseded；
- CatalogEntryType：file、directory、symlink、hardlink、special、unknown；
- LeaseHolderType：rsync_parent、catalog_build、content_session、processing_job、export_job、recovery_job。

未知数据库值必须返回 ErrInvalidState；不得被当作默认 healthy/available。

### 3.3 三枚举映射

Task publication、Repository version mode 和 Point semantics 不能互换。合法映射为：

| Provider / Task mode | Repository version mode | RecoveryPoint semantics | 初始 state |
|---|---|---|---|
| Restic native snapshot | native_snapshot | native_snapshot | preparing |
| Rsync legacy_mutable | mutable_head | mutable_head | observed |
| Rclone legacy_mutable | mutable_head | mutable_head | observed |
| Rsync versioned_hardlink | hardlink_tree | xirang_manifest | preparing |
| Rsync versioned_full_copy | full_copy_tree | xirang_manifest | preparing |
| Rclone versioned_prefix | versioned_prefix | xirang_manifest | preparing |
| Rclone native_object_versions | native_object_versions | xirang_manifest | preparing |
| verified import | provider-appropriate immutable mode | imported_baseline | preparing |
| Command without artifact contract | none | none | ErrCapabilityUnavailable |

imported_baseline 必须创建新的 immutable RecoveryPoint；不得改写 mutable_head 行。Command 日志永远不是 manifest。

## 4. 状态机与组合不变量

### 4.1 Repository 状态

同状态写入是 idempotent no-op。其余合法边如下：

| From | To |
|---|---|
| connecting | online、degraded、offline、disconnected |
| online | degraded、offline、disconnected、purging |
| degraded | online、offline、disconnected、purging |
| offline | connecting、online、degraded、disconnected、purging |
| disconnected | connecting、purging |
| purging | purge_blocked |
| purge_blocked | purging |

成功物理 purge 后不保留一个虚假的 online 状态；Child 14 将完成受保护 tombstone/删除语义。Child 1 只允许进入 purging 和失败重试，不能自行宣称 Provider 已删除。

### 4.2 Immutable RecoveryPoint

合法边：

    preparing -> verifying | failed
    verifying -> committed | failed
    committed -> degraded | expiring
    degraded -> committed | expiring
    expiring -> expired | purge_blocked
    purge_blocked -> expiring

只有 Provider 精确删除对账成功才允许 expiring -> expired。committed -> preparing、failed -> committed、expired -> 任意活动状态全部拒绝。

### 4.3 Mutable head

- 每个 Repository 最多一行 semantics=mutable_head，由数据库 partial unique index 强制；
- 首次接入创建 observed，后续 observation 保持同一 RecoveryPoint ID；
- 成功 observation 只更新 source_fingerprint、observed_at、availability 和 active Catalog generation；
- disconnect 保持 observed，只把 availability/freshness 变为 offline/stale；
- 非破坏 cutover/withdraw 仅允许 observed -> retired，并要求 typed retirement_reason、retired_at 和已加密 rollback locator；
- retired 不可 reconcile、读取、发布或复活；
- 物理 purge 只走 observed|retired -> expiring -> expired，失败走 purge_blocked -> expiring；
- mutable_head 不适用普通 hold/retention，不得变为 committed、native_snapshot 或 imported_baseline。

ValidateRecoveryPointProfile 同时检查 repository VersionMode、PointVersionSemantics、State、ImmutabilityLevel、retirement fields 和 observed fields，避免只验证一个 enum。

### 4.4 Capability 与错误

CapabilitySet 使用显式 bool：

list、search_path、open_sequential、open_range、download、restore、diff、native_history。

CapabilityReason 只允许稳定 code 与 allowlisted params。初始 code registry：

- feature_disabled；
- task_artifact_contract_missing；
- repository_offline；
- repository_disconnected；
- provider_unavailable；
- point_not_committed；
- mutable_source_changed；
- catalog_unavailable；
- sequential_read_unavailable；
- range_unavailable；
- download_unavailable；
- restore_unavailable；
- diff_unavailable。

Params 只允许 safe enum、数值和 correlation_id；Provider 原文、locator、host、path、credential 或 err.Error 不得进入。

Sentinel errors：

- ErrNotFound、ErrForbidden、ErrConflict、ErrInvalidState；
- ErrInvalidAssetRef、ErrProviderUnavailable、ErrCapabilityUnavailable；
- ErrKeyUnavailable、ErrKeyLost、ErrKeyRotationProhibited；
- ErrLeaseHeld、ErrLeaseFenceLost、ErrLeaseDeadlineExceeded。

上下文包装一律使用 %w；API 层以后映射 sentinel，domain 不写 HTTP。

## 5. 000062 双数据库 schema

所有应用写入时间由 GORM UTC NowFunc 提供；新表不依赖本地时区默认。PostgreSQL 使用 TIMESTAMPTZ，SQLite 使用 DATETIME。Closed enum 列使用等价 CHECK；布尔使用 PostgreSQL BOOLEAN / SQLite INTEGER CHECK (0,1)。

### 5.1 表与关键列

| Table | Identity / important columns | Lifecycle owner |
|---|---|---|
| backup_repositories | id；provider_kind；nullable repository_identity；display_name/description；version_mode；status；capability_revision/capabilities_json；immutability_level；last_seen_at/last_reconciled_at；timestamps | repository service, Child 2/14 |
| repository_access_bindings | id；repository_id；binding_kind；encrypted_config；config_fingerprint；status active/revoked；revoked_at；timestamps | repository access service, Child 2 |
| task_repository_links | id；nullable task_id SET NULL；repository_id；task/node snapshots；publication_mode；encrypted_legacy_locator；linked_at/unlinked_at；timestamps | task/repository link service |
| recovery_points | id；repository_id；nullable producing_task_id/task_run_id SET NULL；safe immutable lineage_json；encrypted provider/rollback locators；semantics/state；capture/commit/observe times；source_fingerprint；manifest/count/bytes；consistency/fidelity/capability snapshots；immutability/availability/hold；retention/retirement fields；timestamps | RecoveryPoint service, later Provider/retention |
| recovery_point_manifests | id；recovery_point_id；revision；digest algorithm/digest；generator/version；completeness；entry_count/logical_bytes；fidelity_json；encrypted commit evidence；is_active；timestamps | publication service |
| catalog_generations | id；recovery_point_id；nullable manifest_id SET NULL；generation；state；is_active；source_fingerprint；expected/written counts/digests；safe error_code/correlation_id；start/finish/timestamps | Catalog service, Child 6 |
| catalog_entries | generation_id + entry_id composite PK；recovery_point_id；parent_entry_id；normalized_path/name；entry_type；size/mtime/mode/owner/mime；fingerprint/strength；encrypted provider entry locator；security_state；created_at | Catalog service, Child 6 |
| wrapped_domain_keys | id；domain；version；state active/verify_only/retired/lost；wrapped_key；wrap_algorithm；wrapping_key_fingerprint；activated/verify_until/lost/timestamps | domain keyring |
| recovery_point_leases | id；recovery_point_id；holder_type；owner_id；attempt_id；fence_token；status；lease_expires_at；absolute_deadline；heartbeat/released/timestamps | unified lease service |
| backup_asset_audit_checkpoints | segment_no PK；status open/closed/details_purged；previous_checkpoint_hash；first/last entry hash；entry_count；opened/closed/details_purged times；checkpoint_hash | asset audit writer/retention |
| backup_asset_audit_events | bigint ID；segment_no + segment_sequence；actor snapshots；typed action/outcome/resource IDs；counts/range summary；fingerprint key version + path/query fingerprint；step-up/grant IDs；failure code；safe fields JSON；prev/entry hash；created_at | asset audit writer |
| tasks | add nullable archived_at | Child 14 later changes semantics |

### 5.2 FK 与删除

- repository_access_bindings.repository_id、task_repository_links.repository_id 和 recovery_points.repository_id 引用 backup_repositories；
- 删除 Task/TaskRun 时 task_repository_links.task_id、recovery_points.producing_task_id 和 producing_task_run_id 使用 ON DELETE SET NULL；
- immutable lineage_json 与 task/node/run snapshot columns 保留安全历史，不包含 executor_config、path、host 或 credential；
- Repository 在 RecoveryPoint 存在时 RESTRICT；Task 删除不得级联 Repository、RecoveryPoint、Manifest、Catalog 或 audit；
- Manifest/Catalog/Lease 是 RecoveryPoint 从属数据，可由显式 lifecycle owner 删除；数据库 FK cascade 只作为已授权 owner 删除后的完整性保障；
- audit resource IDs 不设外键，确保源实体清理后 checkpoint/evidence 仍能验证。

### 5.3 唯一约束与主要索引

- unique(provider_kind, repository_identity) WHERE repository_identity IS NOT NULL；
- 每 Repository 一个 active AccessBinding；
- 每非空 Task 一个 unlinked_at IS NULL 的 active TaskRepositoryLink；
- 每 Repository 一个 semantics=mutable_head 的 RecoveryPoint；
- 每 RecoveryPoint 一个 is_active manifest 和一个 is_active Catalog generation；
- unique(recovery_point_id, manifest revision)、unique(recovery_point_id, generation)；
- Catalog listing index：recovery_point_id, generation_id, parent_entry_id, name, entry_id；
- Domain key：unique(domain, version)，每 domain 一个 active key；
- Lease：当前 owner slot 唯一，另有 recovery_point_id/status/lease_expires_at 和 absolute_deadline 索引；
- Audit：unique(segment_no, segment_sequence)，action/created_at、repository_id/created_at、recovery_point_id/created_at 索引。

SQLite/PostgreSQL migration harness 必须验证表、列、CHECK、FK action、partial unique index、UTC round-trip 和 down 回到 000061。缺少 PostgreSQL DSN 的专用 CI job 必须 fatal，不得 skip。

## 6. Sanitized DTO

Child 1 提供但不路由：

- RepositoryDTO：opaque ID、provider kind、display fields、typed version/status/capability、immutability、safe timestamps；
- RecoveryPointDTO：opaque ID、safe lineage summary、typed state/semantics/availability/hold、counts/digests/timestamps、CapabilitySet；
- CatalogEntryDTO：只在未来 authorization 已完成后由 service 转换；包含 AssetRef 和允许展示的 metadata。

以下字段永不进入 DTO 或 JSON：encrypted_config、Provider/rollback/entry locator、wrapped_key、fence_token、raw audit fields、executor_config、credential reference 和底层 error。

测试同时执行 JSON marshal 负向断言和字段级 DTO 断言；仅靠 json:"-" 不视为 API boundary。

## 7. Domain keyring

### 7.1 Key domains

| Domain | Rotation policy | Loss impact |
|---|---|---|
| entry_identity | installation-stable；只允许同版本 rewrap | 无法稳定生成/解析既有 entry IDs；必须显式重建迁移 |
| cursor_signing | 可轮换；旧 key verify_only 到 cursor max TTL | 旧 cursor 失效，不影响备份事实 |
| audit_fingerprint | 可轮换；event 保存 key version | 失去对应版本后不能重新关联旧低熵输入，但 chain 仍可验证 |
| recovery_cleanup_ownership | installation-stable；只允许同版本 rewrap | 无法证明清理 marker 所有权，cleanup fail closed |

### 7.2 Wrap envelope

secure/keyring.go 使用 AES-256-GCM 包装 32-byte domain key：

- envelope version：xirang-wrapped-key-v1；
- AAD：domain + NUL + decimal version + NUL + algorithm；
- 随机 nonce；
- envelope 保存非秘密 KEK fingerprint，禁止保存 KEK；
- unwrap 先尝试当前 DATA_ENCRYPTION_KEY，再尝试 DATA_ENCRYPTION_LEGACY_KEY 派生的上一把 v2 KEK；
- 普通 enc:v1/enc:v2 字段格式保持不变，避免扩大既有字段迁移风险。

RewrapAll 只替换 wrapped_key、wrap fingerprint 和 updated_at，不改变 domain version 或明文 domain key。稳定 domain 的 Rotate 返回 ErrKeyRotationProhibited。已有 row 解不开时返回 ErrKeyUnavailable，不静默生成替代 key；MarkLost 是显式动作。

Cursor rotation 在一个事务中把旧 active 改为 verify_only、设置 verify_until，并创建 version+1 active。验证按 token 内 key version 精确选 key，不遍历所有 key。

## 8. RecoveryPointLease

服务接口固定为 Acquire、Renew、Release、Takeover、ValidateFence、ReconcileExpired。所有方法接收 context、注入 Clock，并返回 Lease/Fence DTO；调用者不直接更新表。

不变量：

- owner_id、attempt_id、holder_type、recovery_point_id 均必填；
- lease duration 受 settings 限制，绝对 deadline 最长固定 168h；
- Renew 只在 status=active、fence 完全匹配、短租约和 absolute deadline 均未过期时成功；
- Takeover 只允许短租约已过期但 absolute deadline 未过期的同一 lease row；生成新 attempt/fence，保留原 absolute deadline；
- 旧 fence 的 Renew、Release、ValidateFence 全部返回 ErrLeaseFenceLost；
- 到达 absolute deadline 后不得 takeover/renew，必须由新作业显式 Acquire；
- ReconcileExpired 在启动和周期调用，把过期 active lease 标为 expired；不依赖进程内 map 或文件 age；
- SQLite 依赖现有 immediate transaction/busy timeout，PostgreSQL 使用 row lock/conditional update；RowsAffected != 1 即并发失败。

后续 publish/read/state advance 必须先 ValidateFence；Child 1 用 fake publisher test 证明 takeover 后旧 fence 不能发布。

## 9. Purpose-bound step-up

### 9.1 Allowed StepUpAction

既有动作：

- ssh_key.export
- terminal.open
- config.import
- config.export
- snapshot.restore
- task.restore_trigger
- task.manual_trigger
- task.batch_trigger
- batch_command.create

预注册资产动作：

- asset.secret_reveal
- asset.download
- asset.export_create
- asset.export_download
- asset.recover
- recovery.result_download
- recovery.result_retain
- repository.purge

StepUpAction 与 SSH credential purpose/grant purpose 是两个不同维度。前者防 proof 跨动作重放；后者继续约束 credential grant。不得把 task_command、terminal 或 config_import 当成 step_up_action。

### 9.2 JWT/API

- Claims 新增 StepUpAction，wire name 为 step_up_action，仅 step_up token 出现；
- GenerateStepUpToken(user, action) 在签名前验证 allowlist；
- POST /auth/step-up 请求为 code + step_up_action；缺失/未知 action 返回 400，不签 proof；
- validateStepUpProof(..., expectedAction) 同时验证 token class、exact action、user、role、token version、TOTP enabled 和 expiry；
- purpose=step_up 但缺少 claim 的旧 proof 必须拒绝；
- pairwise matrix 对全部 17 个 action 逐一签发，并断言只有同 action 接受；
- terminal WebSocket 固定 expected terminal.open。

### 9.3 Frontend

- totp-api.ts 导出与后端同值的 StepUpAction union/constant map；
- requestStepUpProof(token, code, action) 发送 step_up_action；
- ensureStepUpProof(action, options) 要求显式 action；
- useStepUpAction(action, options) 要求显式 action；
- sessionStorage 使用 v2 JSON map，keyed by action；旧的单 proof keys 只删除、不迁移；
- read/save/clear 都接收 action；logout、401、禁用 TOTP 可 clear all；
- proof 只能在同 action 且未过期时复用；one-shot caller 仍 persist=false/reuseCached=false。

Caller mapping：

| Caller | StepUpAction |
|---|---|
| ssh-key-export-dialog | ssh_key.export |
| web-terminal grant + WebSocket | terminal.open |
| config export | config.export |
| config import | config.import |
| snapshot-browser restore | snapshot.restore |
| restore-confirm-dialog | task.restore_trigger |
| use-console-task-operations + alert-center retry | task.manual_trigger |
| tasks-page batch trigger | task.batch_trigger |
| batch-command-dialog | batch_command.create |

## 10. Authorization foundation

Permission constants：

- backup_assets:list
- backup_assets:preview
- backup_assets:download
- backup_assets:export
- backup_assets:recover
- backup_repositories:manage
- backup_repositories:purge

Role matrix：

| Role | Permissions |
|---|---|
| admin | all seven |
| operator | backup_assets:list、backup_assets:preview |
| viewer/unknown/empty | none |

No implication graph exists：manage 不推出 purge，download 不推出 recover。CanDeliverRecoveryResult 纯函数要求 recover permission + requester 是 exact RecoveryJob owner + expected action recovery.result_download；download permission 既不充分也不是附加条件。

Child 1 不新增使用这些 permission 的 route；router tests 只验证既有 step-up caller 和 permission map。

## 11. Asset audit

### 11.1 Typed action registry

Registry 冻结父设计 §9.3 全集，并把重复的 download/recovery 子动作显式命名，避免同一 string 在两个资源域中产生歧义：

- repository_list、repository_connect、repository_reconcile、repository_disconnect、repository_import、repository_review、repository_purge_plan、repository_purge；
- recovery_point_list、recovery_point_detail、recovery_point_evidence、recovery_point_diff；
- asset_list、asset_search；
- saved_search_create、saved_search_update、saved_search_delete；
- favorite_add、favorite_remove；
- tag_create、tag_update、tag_delete、tag_assign、tag_unassign；
- recent_clear；
- preview_job、preview_ticket、preview_read；
- asset_download_ticket、asset_download；
- processing_policy_update；
- archive_inspect、archive_member；
- export_create、export_cancel、export_download_ticket、export_download；
- recovery_plan、recovery_preflight、recovery_authorize、recovery_execute、recovery_cancel、recovery_verify、recovery_cleanup、recovery_retain、recovery_result_download_ticket、recovery_result_download；
- retention_policy_create、retention_policy_update、retention_policy_delete；
- hold_create、hold_release。

Action 是 defined string type；NewEvent 拒绝未注册 action。未来 route 必须先在 registry 登记并通过 route-action coverage。

### 11.2 Sanitizer

Event API 只接受 typed AuditField；field registry 允许 stable stage/status/code、opaque IDs、counts、bytes、range summary、renderer/profile、correlation/grant/step-up IDs。禁止 field 名和值包含 path/name/query/snippet/content/ticket/cookie/jwt/token/secret/credential/config/output/stream/command/payload/provider_locator。

原始 path/query 只能通过 FingerprintInput 进入 writer：writer 用 Audit Fingerprint Key 做 HMAC-SHA-256，保存 key version + digest，随后丢弃输入。禁止 SHA-256(raw low-entropy value)。

### 11.3 Segment chain

- writer 串行化每段 sequence，在同一 transaction 读取/锁定 open checkpoint、计算 prev_hash/entry_hash 并插入 event；
- segment 达到 10,000 events 或 24h 时关闭，计算 checkpoint_hash，下一段保存 previous_checkpoint_hash；
- verifier 校验 retained segment 内部链、first/last hash、entry_count、checkpoint hash 和相邻 checkpoint；
- detail retention 到期后删除该段 events，将 checkpoint 标为 details_purged；anchor 保留到 checkpoint retention；
- media Range 通过未来 session summary 写单个 bounded event；Child 1 只提供 aggregation data shape，不接内容 route；
- audit 写失败不泄露 event payload；结构化 warn 只带 action、correlation_id 和安全错误分类。

## 12. Settings 与 feature boundary

新增 registry：

| Key | Env | Default | Validation |
|---|---|---:|---|
| backup_assets.enabled | BACKUP_ASSETS_ENABLED | false | bool |
| backup_assets.catalog_batch_size | BACKUP_ASSETS_CATALOG_BATCH_SIZE | 2000 | int 1..100000 |
| backup_assets.catalog_build_timeout | BACKUP_ASSETS_CATALOG_BUILD_TIMEOUT | 30m | duration 1m..24h |
| backup_assets.repository_reconcile_interval | BACKUP_ASSETS_REPOSITORY_RECONCILE_INTERVAL | 15m | duration 1m..24h |
| backup_assets.audit_segment_max_events | BACKUP_ASSETS_AUDIT_SEGMENT_MAX_EVENTS | 10000 | int 100..1000000 |
| backup_assets.audit_segment_max_age | BACKUP_ASSETS_AUDIT_SEGMENT_MAX_AGE | 24h | duration 1h..168h |
| backup_assets.audit_detail_retention_days | BACKUP_ASSETS_AUDIT_DETAIL_RETENTION_DAYS | 180 | int 1..3650 |
| backup_assets.audit_checkpoint_retention_days | BACKUP_ASSETS_AUDIT_CHECKPOINT_RETENTION_DAYS | 2555 | int 180..36500 |
| backup_assets.lease_duration | BACKUP_ASSETS_LEASE_DURATION | 5m | duration 30s..30m |
| backup_assets.lease_heartbeat | BACKUP_ASSETS_LEASE_HEARTBEAT | 60s | duration 10s..5m；必须小于 duration |
| backup_assets.lease_absolute_deadline | BACKUP_ASSETS_LEASE_ABSOLUTE_DEADLINE | 168h | duration 5m..168h |

SettingDef 增加 MaxDuration，并在 init/Validate 测试。上述项无 secret/path，因此 Sensitive=false、RequiresRestart=false；未来 path/key 项必须显式标记。

FoundationService.Enabled 只读 backup_assets.enabled。Child 1 后没有任何公开资产 route，Provider 调用计数保持零，环境示例显式保持 false。

## 13. 兼容、发布与回滚

- 000062 是 additive；旧 binary 忽略新表和 nullable tasks.archived_at；
- step-up API 是安全性 breaking change：旧 proof、旧请求和未迁移 API client 失败关闭。官方 all-in-one 同时发布后端/前端，因此不提供通用 proof 兼容旁路；
- generic session proof 在前端升级时被删除，用户只需重新输入 TOTP；
- backup_assets.enabled=false 不能替代 step-up caller migration，但保证没有资产 route/Provider 行为；
- down migration 只允许在任何 child 写入 Repository/RecoveryPoint/domain-key/audit row 前执行；
- 一旦基础表被后续 child 使用，应用回滚保留 schema 并 forward-fix，不 down；
- 普通 KEK 轮换要求同时提供旧 KEK，完成 domain-key rewrap 验证后才移除 DATA_ENCRYPTION_LEGACY_KEY；
- rollback 不删除 Provider 数据，因为 Child 1 从未接触 Provider。

## 14. Focused review gates

在 task.py start 前，用户需确认：

1. composite identity 永远是 RecoveryPoint ID + entry ID；
2. mutable-head stable singleton 与 retired/purge 分离；
3. 11 张基础表 + tasks.archived_at 的 FK/index/down 边界；
4. 四 key domains、stable-vs-rotatable policy 和 KEK rewrap envelope；
5. lease takeover/fence/absolute deadline；
6. 17 个 step-up actions 和九个既有 caller 映射；
7. 七个权限的 role matrix 与 recovery-result rule；
8. typed audit registry、keyed fingerprint 和 segment checkpoint retention；
9. feature flag 默认 false，且无公开 asset route/Provider mutation。

本设计没有剩余产品范围问题。若 review 改变上述任一父级安全/领域边界，必须同时修订父 design.md 与本 focused package；实现中不得静默偏离。
