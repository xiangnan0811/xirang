# 备份资产受控恢复

## Goal

把已交付的备份资产调查、预览、下载与导出能力延伸为受控基础设施写入流程：用户先冻结
精确来源和目标，系统完成只读预检与影响报告，再经 Admin fresh step-up、理由和 plan-bound
授权创建持久 `RecoveryJob`。默认只恢复到目标节点的安全隔离目录；任何原路径覆盖或删除都
必须是单独、显式、可审计的高风险阶段。

本 Child 是父任务 `07-12-backup-data-explorer-design` 的第 13 个交付项。Child 1–12 已在
`main` 交付并归档；当前基线为 `51771654a85967656fe1ca69686590b734ff9214`，Child 12
占用 paired `000068`，本 Child 独占 paired `000069`。父任务和后续 Child 14–15 仍保持
`planning`。

## Confirmed Product Decisions

- 恢复来源只接受一个 Repository、一个精确 RecoveryPoint 和显式 `AssetRef` 集合；禁止
  `latest`、执行时重跑保存搜索或把可变搜索结果重新解释为新内容。
- 不可变点以 RecoveryPoint 引用、domain-separated locator digest 与 manifest digest 绑定；locator
  本身只允许作为 Repository 内的 encrypted `json:"-"` copy。Recovery 与 Provider public contract
  只能携带 closed scalar ref，不能携带 raw locator/root。`mutable_head` 绑定
  `source_fingerprint + catalog_generation_id + observed_at`。该 typed revision 进入 selection、plan、
  grant、job 和 checkpoint，并在 preflight、write-authority、首次写入及 resume/takeover 边界前后重验。
- 目标节点采用资格策略 A：任何未归档 registered node 只要通过 credential-purpose、工具、
  safe-root、容量/inode、source-access 与 node-write conflict 预检即可；生产来源节点有效时
  仅作为默认预选，不限制灾难恢复到其他合格节点。
- `target_mode` 是 closed `isolated | in_place`。默认目标是目标节点配置/probe 通过的 recovery
  root 下新建 job 专属隔离目录；不得默认回写原路径。默认建议 `/var/tmp/xirang-recovery`，但
  配置值不是安全证明，配置 root locator 也按敏感 setting 加密且不得进入 API/日志。
- 原路径恢复只允许 `fail_on_conflict`（默认）、`skip_existing`、
  `overwrite_selected` 或 `exact_mirror`。plan/preflight/job 固化 canonical bounded operation-set 与
  delete-set digest；一次性 `write` authority 与一次性 `exact_mirror_delete` authority 严格分离。
  后者只能在 worker 暂停于第二 checkpoint、重新验证 node/root/fence/target 后签发和消费，不能
  成为 Provider 的隐式 destructive sync。
- malware/security 决策是 closed product：finding 默认阻断；只有 policy 明确标记可 override 的
  已知 finding 才允许 Admin 以单独 fresh `asset.recover` proof、单独加密理由和独立 sanitized
  audit 覆盖。unknown finding 与 non-overridable policy 永久 fail closed。
- job 创建后、任何 remote mutation 被 durable arm 之前发现 drift，必须以一个 guarded transaction
  完成 `executed -> superseded`、零写入 job 终止及 lease 释放；一旦 mutation 被 arm 或发生部分写入，
  不再走无写入 supersede 路径，而是验证 checkpoint 或进入 `needs_attention`。
- `RecoveryJob` 只表达恢复执行 outcome；明文结果的 retain/revoke/cleanup 由独立
  `RecoveryResultSet` 生命周期表达，清理失败不得改写已成功/降级/失败的 job outcome。
- 恢复明文先存在 execution-owned、不可下载的 unpublished workspace。marker binding 与 immutable
  absolute plaintext deadline 必须在第一个恢复内容字节前持久化；只有 `isolated` 且 job 已进入
  `succeeded|degraded` 非写入终态时，verified regular-file/report rows 才可原子 publish 为 `ready`。
  canceled/failed/needs_attention partial workspace 一律 cleanup-only，in-place 路径永不成为
  `RecoveryResultRef`。
- paired `000069` 保持十二张表；永久 use latch 是 recovery evidence 表中的 distinguished
  `schema_use_latch` row，由双引擎约束/trigger 禁止更新或删除，并在任何 remote mutation 前提交。
  同 binary feature disable 与 pre-Child binary downgrade 是两个合同；首次使用后只能 forward-fix。

## Requirements

### 1. Selection And Plan Freeze

- `RecoveryPlan` 必须冻结 Repository、RecoveryPoint、全部 canonical AssetRefs、selection digest、
  typed source revision、`target_mode`、目标 node/root/path digest、target base revision、Provider
  capability revision、冲突策略、canonical operation-set/delete-set digest、closed security decision、
  预计 items/bytes 和 preflight revision/expiry。raw source/target locator 不得进入 authority DTO。
- 所有选择项必须属于同一 RecoveryPoint 与同一 Catalog generation；目录选择在授权前展开
  为有界、可验证的显式集合。symlink、hardlink 和 special entry 不得被猜测或 dereference。
- source/target/capability/policy/security finding 在任何授权或写入边界漂移时使尚未 arm mutation 的
  plan `superseded`；若已 arm 或发生部分写入，则停止后续 mutation、保留 completed-operation
  evidence，job 进入 `needs_attention`，不得把 crash ambiguity 当作零写入。
- Idempotency 必须绑定 requester、endpoint 和完整 plan intent；同 key 同 intent 回放同一
  durable object，同 key 不同 intent 稳定冲突。intent 必须包含 target mode、operation/delete digest、
  security decision digest 和 authority category；一个 plan 只能拥有一个 durable job。
- Task 2 已交付的 plan-create idempotency 仍只属于 plan create。Task 5 的四个授权性 mutation
  (`security-override`、write-authorize、exact-mirror-delete-authorize、execute) 必须另以 evidence
  中的 closed `authorization_receipt` 实现 endpoint/requester-scoped idempotency；不得把 plan row
  或可清理 audit 当作 proof consumption/回放账本。

### 2. Read-Only Preflight And Target Safety

- preflight 不得写目标数据。它验证节点在线、credential purpose、Provider/restore capability、
  SSH/tool 可用性、source access、safe-root realpath、父目录/目标 symlink 与 mount 边界、空间、
  inode、目标冲突、现有文件、malware/policy finding 及预计 create/overwrite/delete/skip。
- finding product 必须区分 clean、blocked 与 Admin override；只有 allowlisted known category 可
  override，且 finding-set/policy revision 的任一变化都使 override 与后续 authority 失效。
- safe-root 检查必须在服务端基于 canonical/real path 和目标节点证据完成；客户端路径、旧 probe、
  prefix 字符串或 marker 文件单独都不能授权删除/覆盖。
- preflight 是版本化且短时有效的 durable snapshot。授权、首次写入、删除阶段与 takeover 必须
  重验 relevant revision；过期或漂移返回 typed conflict，不静默刷新并继续。
- target revision 使用 mutation-aware chain：每个受权 operation 消费 expected prior revision 并产生
  checkpointed next revision；只有与 expected operation evidence 完全一致的变化可归为本 job 写入，
  其他变化均为 external drift。crash 后只能 verify/adopt exact intended operation 或 fail closed。
- 同一目标节点的恢复与普通写入任务进入 durable node-write coordination；只读预览仍可在
  Provider 配额内进行。
- 普通/legacy restore writer 的 `TaskRun` 必须在 durable reservation 时冻结不可变目标
  `node_id_snapshot`。pending→running 只能在同一共享 node boundary transaction 内重验 active
  Recovery lease 并以 `status=pending` CAS 成功后进入 executor；取消、lease 冲突或 CAS 0 行都
  必须保持 terminal/no-executor。Recovery 的冲突查询只使用该 snapshot，不能通过可变
  `tasks.node_id` 重新归属仍在旧节点执行的 writer。

### 3. Authorization, Execution And Provider Semantics

- 四个授权性 mutation 的首次 effect 都要求 Admin、`backup_assets:recover`、fresh exact
  `asset.recover` proof、middleware 当前 login-session binding 与 endpoint-scoped idempotency key。
  它们以同一 `authorization_receipt` row 原子记录 proof 的一次性消费、请求 intent、session binding、
  replay deadline 和 effect；同 key+同 intent 只能在同一仍有效 presenting session 中回放已持久化
  effect（即使原 proof 随后过期），同 key+不同 intent 为稳定 conflict，已被另一 plan/category/endpoint
  消费的 proof 为稳定 proof-used conflict。generic step-up JWT 只证明 action/user/role/version；不得
  声称其由当前 login session 签发或要求两者 JTI 相等。
- receipt 的 full intent 使用 domain-separated digest，至少覆盖 closed operation/category、endpoint
  template、private subject/effect binding、expected revision、所有 plan/job/checkpoint/fence/revision
  inputs、reason digest 及适用 grant-secret hash。receipt 私有地保存 requester、subject linkage、
  domain-separated one-way step-up-JTI digest、proof expiry、presenting-session JTI digest/token-version/
  role/expiry、replay expiry 和 exact effect references；raw proof、raw JTI、raw reason 与 raw secret
  不得持久化或进入 DTO/audit/log。global nonempty proof digest 只能出现一次，requester+endpoint+
  idempotency digest 只能出现一次。
- receipt lifetime 必须满足 `proof_expires_at <= replay_expires_at <=
  presenting_session_expires_at`；write/delete receipt 还必须满足 grant expiry 不晚于 replay expiry。
  若当前 login session 剩余寿命或 settings snapshot 不能同时满足这些边界，首次授权必须在 effect/
  proof consumption 前 fail closed，不能缩短 receipt 后让仍有效 proof 重新可用。receipt replay 仍可在
  proof expiry 后、同一 presenting session 与 replay window 内读取既有 effect。
- `write` 与 `exact_mirror_delete` grant 均是 hash-only bearer authority：client 用 CSPRNG 生成一个
  canonical unpadded base64url 43-character value（decode 后恰好 32 bytes），在 issue request 中暂态
  提供；服务端拒绝任何非 canonical/非 256-bit shape，只保存 category/subject-domain-separated hash，
  并把该 hash 纳入 intent。初始和 same-intent replay response 只返回 durable grant metadata，绝不
  回显、加密保存或可逆保存 secret；client 保留原 secret 供后续 write/delete consumption。security
  override 没有 grant secret。
- `write` authority 绑定 plan/selection/source/target/capability/policy/security/preflight/operation/delete
  revisions；`exact_mirror_delete` authority 由独立 endpoint 在第二 checkpoint 后签发，额外绑定
  job/attempt/fence/delete-set/current-target revision。execute 必须以匹配的 persisted hash 消费 write
  grant，并在 receipt transaction 中创建唯一 job/items/attempt、恰好一个 plan-RecoveryPoint-bound
  `recovery_job` source lease、target-node lease 及 plan
  transition；same-key execute replay 只返回该 job，绝不第二次消费或复制 lease。delete grant 的后续
  fenced consumption 仍属于 Task 6，必须要求 client-retained secret 的 transient matching handoff；
  任一 authority 都不能被下载、retain、cleanup 或另一 category 复用。
- 本 Child 的一个 plan 固定一个 RecoveryPoint，因此 execute effect 必须恰有一个 non-null
  `recovery_job` source lease。execute receipt 以单一 lease ID 与 singleton binding digest 精确绑定该
  plan RecoveryPoint、job 和 initial attempt；缺失、额外、跨 point/job/attempt substitution 均由 paired
  SQL linkage guard 与 service validation 在 commit 前拒绝，不使用 JSON/list/blob 模拟 lease 集合。
- job 创建与首次写入之间若发生 drift，当前 fence owner 必须在同一 transaction 将 plan 从
  `executed` guarded 转为 `superseded`、job 终止为 `failed/pre_write_drift`、撤销未用 authority 并
  释放 source/node leases；same-key execute replay 仍返回该唯一 job。SQLite/PostgreSQL 都必须覆盖
  job-commit、claim 与 first-write crash/two-worker barrier。
- 每个 job 对每个 distinct source RecoveryPoint 持有 `recovery_job` lease；写 attempt、job
  transition、target checkpoint 和 restart takeover 都使用 durable fence，旧 attempt 不能继续
  写目标或状态。
- Rsync 只携带 Provider-owned `RsyncRestoreSourceRef{plan binding, repository/point/catalog IDs,
  selection, source-revision, manifest digests}`；Repository implements the portable
  `RsyncRestoreSourceResolver` and at every preflight/execute/verify/reconcile runner
  调用前锁定并重验 plan、point、catalog、selection、revision、manifest 与 managed root，并在 runner
  返回后重验 root、marker、point identity 与 manifest；随后才通过
  fileaccess pinned strict no-follow descriptor 向 target writer 提供 declared regular-file stream。
  Rsync runner 永不收到重建的 source/root path、raw locator 或 raw remote path。Restic 只恢复完整
  exact snapshot ID 与冻结 includes；Rclone 只拉取 committed prefix，禁止对未声明目标做
  destructive sync。Provider contract 返回 typed operation/checkpoint evidence，不把 executor
  stdout 当授权。
- immutable managed-Rsync point 在进入上述 resolver 前必须能由真实 committed-tree session
  建成 active complete Catalog。generic Provider Catalog projection 对没有可证明 fingerprint 的
  entry 必须显式写 closed `fingerprint_strength=none` 且保持 fingerprint 为空；空 strength 仍是
  非法合同。Repository wrapper 只负责加密 locator，Catalog indexer 继续 fail closed，二者都不得
  用默认值掩盖 Provider omission。
- 逐项持久化 success/failed/skipped、bytes、exact lowercase SHA-256 content identity verification 与
  sanitized failure category。对 Task 6，fidelity 仅指 content digest + byte count；不得声明 mtime、
  mode、owner、MIME 或其他 metadata fidelity。mutation arm 前的 invalid verification product 必须遵守后续
  §7 的 pre-arm/zero-mutation exact zero-effect contract；mutation arm 后的 `verification_mismatch` 必须按 §8
  append exactly one terminal `operation_unresolved`（可为 sequence 1）并原子投影 current item
  `failed/remote_outcome_unresolved`、job `needs_attention/remote_outcome_unresolved`、failure evidence、attempt
  closure 与 lease release，绝不能 `degraded`、publish、隐藏为 success 或推进 target chain。
- queued/preflight 可无副作用取消；写入开始后的取消是 best effort，必须报告可能的部分状态。
  非幂等写入在重启后不得盲目 replay，只能依据新 fence、source revalidation 和 validated
  checkpoint 选择 resume、verify 或人工处理。
- `schema_use_latch` 必须在 current job/attempt/node fence 下先行提交；所有会改变远端目录、marker、
  文件或清理状态的 TargetPort 调用都必须携带并重验 latch permit。允许“latch 已存在但首写失败”，
  禁止“remote mutation 已发生但 latch 不存在”。

### 4. Isolated Results, Download And Plaintext Lifecycle

- 默认隔离目录的 encrypted relative locator、HMAC marker binding、workspace owner/fence 与 immutable
  absolute plaintext deadline 必须在写入任何恢复内容前持久化；随后原子创建 marker，绑定
  installation ID、job ID、root revision 与随机 nonce。临时名称永不登记，数据库不接受客户端提交
  的任意绝对结果路径。
- `RecoveryResultRef{recovery_job_id,result_id}` 只能解析已通过 publish barrier 的 isolated job tree
  中 owned regular file 或 verification report；目录、in-place path、symlink、hardlink、special file
  与 path-like opaque ID 全部拒绝。
- 结果下载要求 `backup_assets:recover`、精确 job ownership 和 fresh
  `recovery.result_download` proof；`backup_assets:download` 单独不足，也不额外叠加为必需。
  复用 Content plane 的 typed ticket、Range、累计预算、撤销与 redacted audit，但使用恢复专属
  SSH purpose 和 result adapter。
- `RecoveryResultSet` 状态固定为 `ready | revoking | cleaned | cleanup_failed`。默认 plaintext
  absolute TTL 为 24 小时；Admin 可用 fresh `recovery.result_retain` proof 延长到 hard cap 内
  的新 deadline，不能无限期或 revive revoking/cleaned 数据。
- `RecoveryResultSet` 不新增公开 WIP state。unpublished workspace 由 job/attempt durable phase 管理；
  success/degraded 后才原子创建 `ready` row/results。failed/canceled/needs_attention partial files 不发布，
  只进入 fenced cleanup；Content 在 ticket issue、reauthorize 与 open 时都重验 terminal job + publish
  barrier + current result/cleanup fence。
- 清理顺序固定：事务性进入 `revoking` 并增加 fence → 拒绝 retain/新票并撤销现有 grant/ticket
  → 有界 drain/关闭旧流 → 重新验证 safe root、non-symlink marker、job FK/tombstone → 只删除
  精确 job 目录 → 写 `cleaned` tombstone。任何失败进入/保持 `cleanup_failed` 并由同一幂等序列
  重试；未知/伪造/无法对账目录只能 quarantine/alert，不能自动删除。
- 每次 published 或 unpublished remote cleanup attempt 都必须按 `job -> target node lease -> result/workspace`
  锁顺序重新取得 node-wide writer lease/fence，并在 revoke/drain/validate/delete/tombstone 全程 renew、
  每个 outcome release。expired cleanup owner 可用 fresh fence 做 `revoking -> revoking` takeover 并从
  durable cleanup phase 续跑；旧 cleanup/node fence 不得执行任何新 remote/DB mutation。

### 5. API, UI, RBAC And Audit

- API 提供 plan create/status/preflight/security-override/write-authorize/execute/cancel、job status、
  exact-mirror-delete-authorize、result download-ticket、retain、cleanup 与 downgrade-readiness；每条
  `/api/v1` 路由均有 Auth、RBAC、ownership、typed error 和注册 sanitized audit。
- 前端向导固定为 selection → target → preflight → security decision → impact → write reason/step-up →
  progress → exact-mirror delete checkpoint/reason/step-up（仅需要时）→ verification → result
  download/retain/cleanup。API boundary 把 closed snake_case DTO 映射为 closed camelCase unions；
  组件不得直接 `fetch` 或推断自由字符串状态。
- UI 明确区分 Job outcome 与 ResultSet lifecycle，展示 source/target drift、partial writes、
  destructive impact、TTL/cleanup failure，并满足键盘、focus、screen-reader、reduced-motion、中文/
  英文、窄屏与有界 DOM 要求。
- 新 UI 只调用 plan APIs；legacy `latest`/default-source restore UI/route 继续 fail closed 或 gated，
  直到 Child 15 GA hardening 明确接管。
- audit 不记录 raw paths、理由正文、credential、proof、ticket、marker nonce 或 secret；只记录
  stable opaque IDs、authority category、security decision category、sanitized outcome、数量/字节与
  policy category。raw source locator、configured root locator 及其可逆表示不得进入 Swagger、audit、
  log、metric、failure evidence 或 frontend DTO。
- `authorization_receipt` 是授权 effect 的 durable, retention-bounded sanitized audit source of truth，
  必须与 effect 同 transaction commit；既有 generic audit/step-up/credential-grant handlers 保持冻结，
  它们的 post-commit、可 details-purge projection 不能替代 receipt。projection 在 receipt commit 后失败
  时不得 rollback/duplicate effect、不得把 raw failure/proof/reason/secret 暴露给 client；same-key retry
  仍回放 receipt 所指 effect。

### 6. Schema, Settings And Delivery Discipline

- paired SQLite/PostgreSQL `000069_backup_asset_recovery` 持久化 plan/job/item/grant/checkpoint/
  evidence/result/result-lifecycle/fencing 及必要不变量、索引和 down-pristine latch；两引擎行为一致。
  十二表合同不变：distinguished `schema_use_latch` 永久 row 位于 recovery evidence 表，remote mutation
  前先提交，constraint/trigger 与服务共同禁止更新、删除或普通 cleanup 命中。
- `backup_asset_recovery_evidence` 在不增表的前提下新增 closed immutable
  `authorization_receipt` row kind；normal verification evidence、receipt 与 fixed `schema_use_latch` 的
  nullable/linkage arm 必须互斥。paired SQL 以 partial unique indexes、CHECK/FK/trigger 和有界 indexed
  reads 强制 receipt key/proof/linkage/effect/expiry parity；receipt 任何 update 都拒绝，pre-replay-expiry
  delete（包括 parent cascade/direct SQL）拒绝，过期后也只能由 bounded reaper 在所有 linkage guards 满足时
  删除。现有 down first-statement guard 必须把 receipt 当 evidence：未到 replay expiry 的 receipt 使 down
  原子拒绝；到期且安全 reaped 后仍仅在十二表及 Recovery Content/lease state 全空且 use latch 缺失时才可
  pristine down。
- Recovery runtime 必须拥有 receipt retention：default-disabled 时仍运行的 bounded UTC maintenance
  owner 以 `(replay_expires_at,id)` 的 indexed stateless keyset 查询只选择已过 replay/proof/grant deadline
  且 exact linkage guard 可删除的 receipt，并在同一 transaction 删除；normal evidence、仍受保护 receipt
  与 `schema_use_latch` 永不成为 candidate。因为不可删除的旧行在 SQL eligibility 中被排除，重启和
  persistent protected rows 不能占满每一 bounded pass；全局 DB failure 才允许整轮失败并重试。runtime
  disable 继续 reaper，shutdown/schema drain 必须 cancel/join 它。
- settings 通过现有 Foundation/settings registry 原子提供 safe roots、preflight validity、job
  concurrency、lease/heartbeat、result TTL/retain cap、cleanup cadence/drain 与 bounded limits；
  Repository frozen defaults fixtures 必须保持 complete。
- feature 默认继续关闭；same-binary disable 停止新 plan/authority/job/infrastructure writes，但保留
  result revoke/cleanup/orphan reconciliation；retained plaintext 可以处于 disabled runtime，却使 binary
  downgrade 保持 unready。
- binary downgrade readiness 必须在当前 binary 的 sticky admission fence 下证明 use latch 缺失，且
  queued/running jobs、unconsumed grants、leases/attempts、non-cleaned ResultSets、Recovery Content
  grants/tickets/streams 与 reconciliation backlog 全为零，才允许 pristine down 后运行 pre-Child binary。
  use latch 一旦存在，无论行是否清空都只允许 forward fix；不得依赖旧 binary 理解 `000069`。
- 所有产品、测试、migration 与 Trellis 变更必须落在最终候选的 9 current + 55 create + 81 modify
  exact manifest（145 unique paths）。第九个 current path 是 immutable security/state review evidence；
  workflow/spec paths 不扩张产品范围。`000070` 及以后编号保留给 Child 14–15。

### 7. Task 6 Restart-Adoption And Locator Product Amendment

本节是 later controlling Task 6 contract，只在早期 worker/reconcile 文字不够精确或与本节冲突时
取代该文字。它冻结 Task 6 的前十三项 product corrections。2026-08-01 independent fidelity
researcher 返回的 evidence-backed clarification 已由 controller direction 批准，作为 focused Task 6
planning amendment 解决 fidelity ambiguity；它本身不计作独立 correction。随后独立只读 design research
返回 `DESIGN READY`，controller 批准其 coherent locator/execute clarification；该 clarification 仍在前
十三项 correction 与 exact 145-path manifest 内。后续 §8 单独增加 controller-approved 第十四项
unresolved-remote-outcome correction，因此当前 Task 6 product correction 总数为十四。Task 6 保持
`in_progress`。Post-B3 ledger 将前十三项 implementation 分为 B1/B2；当前 B1-E1（Corrections
1--3、5）、B1-E2（Corrections 7--10）与 B1-E3（Corrections 11--13）已在各自 focused scope
达到 `complete_checked`，但 B1 aggregate 仍是 partial。B2-E1（Correction 4 plus delete row）与
B2-E2（Correction 6）均在 focused scope 达到 `complete_checked`；B2 aggregate 仍为 partial，
combined/whole evidence 不从 focused closure 追认 RED/review credit。第十四项 B3 仅在 focused correction
scope 达到 `PROVED_COMPLETE`，不代表 Task 6 整体完成。

- 本 Task 6 slice 中，`fidelity` 的唯一产品含义是 exact content identity digest 加 byte count。
  present expectation 与 present observation 必须携带完全匹配的 lowercase 64-hex SHA-256 content
  identity 和 `bytes >= 0`。不得声明 mtime、mode、owner、MIME 或其他 metadata fidelity，不得增加
  independent fidelity digest、parallel fidelity field 或 synthetic absence digest。更广的 metadata fidelity
  必须由未来独立的 source-freeze + target-observation contract amendment 才能引入。
- 每个 operation snapshot 和 job item 都必须冻结同一 operation product：`create` 要求 prior absent、
  nonempty post SHA-256 digest、post bytes `>= 0`，且 prior-byte field 为 `-1`；`overwrite` 要求 prior
  present 且 post digest/bytes 为 exact SHA-256 + `>= 0`；`skip` 要求 prior present、post digest 等于
  frozen prior digest、post-byte field 为 `-1`，并单独冻结 prior-target bytes `>= 0`，outcome 只能为
  `skipped`；`delete` 要求 prior present、empty post digest、prior/post 两个 byte fields 均为 `-1`，并做
  exact absence verification。delete empty post field 仍是 canonical length-framed input；禁止 absence digest。
- `create|overwrite` 只有在 freshly revalidated frozen `RestoreEntry` 与 persisted post digest/bytes
  完全相等时才可执行或 adoption。`skip` 独立重验 source，但只依据 frozen prior-target digest/bytes
  验证目标 exact unchanged，并始终投影 `skipped`，绝不投影 `succeeded`。`delete` 要求 explicit exact
  `AbsentObservation` 与 durable delete authority。
- `TargetPort.Verify` 使用 closed `present|absent` expectation/observation union。present arm 只接受 exact
  lowercase SHA-256 content identity + `bytes >= 0` 并逐字段相等；absent arm 只接受 closed exact absence
  evidence。permission、timeout 或 ambiguous missing 都不是 absence。`ObservedRevision` 是 bounded、
  nonempty、opaque、strong、target-derived revision，不得要求或伪装成 SHA-256 shape。target chain 以独立
  domain 绑定 absence observation；expected-post field 仍为空。
- execute preparation 必须在 effect transaction 外预分配 opaque job ID、每个 item ID，并为 `isolated`
  预分配 immutable workspace identity `jobs/<opaque>`。这些值先只存在于 in-memory prepared aggregate；
  不创建 DB row，也不做 remote reservation、目录创建或 target I/O。
- canonical schema-v2 的每个 operation row（包括 `delete`）都保存 exact
  `target_relative_locator` 与 `SemanticTargetDigest`。in-place locator 是 canonical target-root-relative；
  isolated locator 是 deterministic preflight-frozen workspace-relative suffix。`SemanticTargetDigest` 只绑定
  mode、exact root product 与 canonical item locator。缺失 item locator 时，plan-level 或 plan-item locator
  均不得成为 fallback。
- `TargetObjectDigest` 是与 semantic digest 分离的 final root-relative object binding：in-place 绑定 exact
  root-relative locator；isolated 绑定预分配的 `jobs/<opaque>/<suffix>`。execute preparation 只计算 expected
  `TargetObjectDigest` 并写入 prepared aggregate，不能因此取得 target authority。
  `TargetObjectRef.TargetPathDigest` 只承载 `TargetObjectDigest`，绝不承载 `SemanticTargetDigest`。
- grant CAS 后插入的 complete job 对 `isolated` 必须是 `workspace_phase=none`，同时已有 nonempty generic-
  encrypted workspace locator 与 immutable `WorkspaceBindingDigest`；marker、workspace owner/fence 与
  plaintext deadline 全为空。`in_place + none` 的全部 workspace fields 都为空。后续
  `PrepareFirstWrite` 锁定 exact persisted workspace/item 后复用预分配 identity，执行 `none -> reserved`
  并填充 reservation fields，禁止重写、rename 或重新分配 workspace identity。
- 每个 job item 保存 `SemanticTargetDigest`、`TargetObjectDigest`、recovery-local AEAD ciphertext、positive
  `TargetLocatorKeyVersion`、positive local cipher version，以及完整 operation prior/post/presence/byte
  product。item locator column 不使用 generic model `BeforeSave`/`AfterFind` encryption hook；只有 Recovery
  可按 persisted versions 通过 `KeyDomainRecoveryCleanupOwnership`、HKDF-SHA256/AES-256-GCM 和
  `xirang/recovery/job-item-target-locator/aead/v1` 打开它。generic model encryption 继续只服务
  `EncryptedOperationRows` 与 job workspace locator；现有 generic `enc:v2` 是 encrypted preflight snapshot
  envelope，不是 explicitly versioned item AEAD，也不得被解释为 item cipher version。
- `TargetLocatorEnvelopeBinding` 对 codec/cipher versions、job/item/plan/nullable plan-item/source-row IDs、
  mode/node/root/root digest、workspace identity/`WorkspaceBindingDigest`、separate semantic/full-object digests、
  operation/presence/digest/byte facts 以及 explicit key/cipher versions 全部 length-frame。AEAD plaintext
  同时包含 exact canonical item locator 与 workspace locator（in-place workspace arm 为 canonical empty）。
  任一 cross-row/job/root/item/workspace/key/cipher/product substitution 必须在 target I/O 前失败。
- adoption API 固定为 `AdoptInterruptedOperation(ctx, claim, jobItemID)`，不得接受 caller locator、
  identity、revision、operation 或其他 target facts。adoption 分成三个边界：短 DB transaction
  load/lock exact durable job/item/plan/attempt/workspace/authority、decrypt 并验证全部 immutable product；
  不持有 DB transaction 的 target Verify I/O；最后重新 lock/revalidate 并以 fenced CAS 原子投影适当的
  success 或 skipped、在适用时 append sequence 1、推进 chain 并关闭 attempt。worker 只有在锁定 exact
  persisted workspace/item、decrypt locator、strict-join、重算并比较 `TargetObjectDigest` 后，才能构造
  `TargetObjectRef`/permit。只有发生在 authorized durable checkpoint 前的 validation failure、stale 或
  takeover-loser path 才是零 false success、checkpoint 或 chain advance；mutation arm 后的 invalid/
  contradictory remote outcome 必须改走后续 §8 的 `operation_unresolved` terminal projection。
- cleanup key loss 在 runtime fatal boundary 返回前先处理：permanent `ErrKeyLost|ErrKeyUnavailable`
  触发 bounded idempotent DB-only reconciliation。current post-arm work 进入 sanitized
  `needs_attention|cleanup_due` 并关闭 attempt；pre-arm、terminal、stale work 不变。该路径不得 target
  I/O、decrypt、checkpoint、success、skip 或 chain advance；startup 仍返回原始 fatal error。
- execute transaction 外按顺序完成 receipt replay lookup、canonical snapshot decode、whole-product
  validation、association resolution、job/item/workspace ID allocation、explicit Active cleanup-key selection、
  both target digests 与全部 AEAD/generic ciphertext materialization，形成 immutable prepared aggregate。
  transaction 内先 recheck replay/proof，再依次 lock plan、preflight/plan items、grant、exact cleanup-key row、
  source/catalog 及既有 source/node/attempt resources；从 locked facts 重算 prepared aggregate 并要求
  byte-for-byte equality，且 `LockActiveTx` 必须匹配 transaction 外选择的 key。CAS-consume grant 是第一个
  effect mutation；随后才 insert complete job/items/source lease/node lease/attempt/plan transition/receipt，
  一次 commit。transaction 内禁止 encryption/provider/SSH/target I/O/audit/remote reservation。
- preparation 失败不留任何 state；transaction 失败原子 rollback grant 与 aggregate；commit 后 crash 留下
  complete-but-unreserved aggregate。首次 reservation commit 前的 retry 使用同一预分配 identity，commit 后
  的 retry 继续复用它；unexpected remote directory 一律 fail closed，绝不 rename/reallocate。
- 只修改尚未发布的 paired `000069`。`isolated + none` 必须有 workspace ciphertext/binding digest 且
  marker/owner/fence/deadline 为空；`reserved+` 必须保留同一 immutable identity 并要求 marker/owner/fence/
  deadline。in-place none 的 workspace product 全空。paired CHECK 与 insert/immutable/one-way projection/
  checkpoint triggers 必须冻结 job identity、两个 item digest、ciphertext/versions、完整 operation matrix、
  per-job semantic/final digest uniqueness、insert facts、`delete` 仅允许 `in_place+exact_mirror`、terminal
  product，并先执行 down guard。SQL 不能认证 ciphertext 内容，因此每次 service/worker load 必须在 I/O 前
  重建 full item set。禁止 `000070`、backfill、新 table 或新 path。
- Task 6 的严格 named matrix 在 `implement.md` 冻结，覆盖 canonical locator/whole-product tamper、prepared
  aggregate grant-first order、ciphertext binding、preallocated first-write identity、durable adoption derivation、
  operation verification、permanent key loss、paired migration parity、race one-winner 与 plaintext scan。
  其中 B3 的五个 Correction 14 selectors、B1-E1 的
  `TestRecoveryVerifyOperationProductMatrix`/paired ordinary job-item selectors，以及 B1-E2 的五个
  canonical-locator/aggregate-envelope/item-AEAD/preallocated-workspace/no-leak selectors 已在各自 focused scope
  完成证明；B1-E3 的 prepared-aggregate/adoption/key-loss/paired-immutable selectors 也已在其
  focused scope 完成证明。B2-E1 的 exact absence/delete-row/durable-authority arms 与 B2-E2 的
  absence-chain/ordered multi-delete/restart arms 也分别完成 focused proof。F4 的两个 Task-6-owned
  selector 另已完成 workspace/deadline/cleanup-only focused proof；whole Task 6 gates/reviews 与 unchecked
  execution items 仍未获得完成 credit。
  只有语义上发生在 authorized durable checkpoint 之前的 pre-arm/zero-mutation negative 才断言 zero
  sequence-1 checkpoint、item success/skipped、job success、chain advance、forbidden target I/O 与 raw
  locator leak。后续 §8 unresolved matrix 改为禁止当前 item 的 success/adoption checkpoint，并要求恰好一个
  terminal `operation_unresolved`；该 checkpoint 可以是 sequence 1，也可以保留并跟随 earlier valid history。
- exact manifest 保持 9 current + 55 create + 81 modify = 145。Task 6 implementation 保持 `in_progress`。
  F6 live-mutation-permit、F3、B1-E1、B1-E2、B1-E3、B2-E1/E2 与 Task-6-owned F4 已在各自 focused
  scope 关闭，但互不提供 broader credit。原始 review 的编号只有 F1--F8；当前剩余的是 unchecked
  execution items、whole specification/quality reviews 与 whole gates。不存在 Finding 9。Tasks 7--10
  保持 `not_executed`。

### 8. Task 6 Post-Arm Unresolved Remote Outcome Correction

本节是 later controlling、controller-approved 的第十四项 Task 6 product correction。B3 已按冻结合同完成
focused proof：先取得 independent specification approval，再保留 selector、恢复 narrow pre-feature
baseline、观察 genuine RED、恢复/修正最终行为并以相同 selectors 证明 GREEN；该 chronology 不给 B1/B2
retroactive credit。只要 remote mutation 已 arm，任何 invalid 或 contradictory write/verification outcome
都必须先持久化 closed evidence，再原子终止为 `needs_attention`；不得投影 success、盲目 replay 或推进
target revision chain。

- 新 terminal checkpoint phase 固定为 `operation_unresolved`；job 和当前 item 使用稳定 failure category
  `remote_outcome_unresolved`。unresolved category 只能是 `revision_disagreement`、
  `verification_mismatch`、`write_result_invalid` 或 `observation_invalid`，未知值 fail closed。
- unresolved checkpoint 的 private facts 恰为 `job_item_id`、`unresolved_category`、
  `write_result_digest`、`write_target_revision`、`observation_digest`、
  `observed_target_revision`、`observed_presence` 与 `source_revalidation_outcome`。
  `observed_presence` 只能是 empty、`present` 或 `absent`；`source_revalidation_outcome` 只能是
  `matched|drifted|failed`。所有既有 checkpoint phase 必须把这些字段保持 neutral empty 值。
- category-specific field legality 是 closed contract：`revision_disagreement` 要求 valid write/observation
  digest 与 revision 都存在且两个 revision 不相等；`verification_mismatch` 要求 valid observation，`skip`
  的 write fields 必须 neutral，而 `create|overwrite` 必须有 valid write digest/revision，现有 B2 delete
  contract 不变；`write_result_invalid` 只保存 invalid raw result 的 sanitized length-framed digest，不能保存
  structured write revision 或任何 observation facts；`observation_invalid` 在 applicable 时保留 valid write
  facts，但 invalid observation 只保存 sanitized length-framed digest，structured observation revision/presence
  必须 neutral。`source_revalidation_outcome=drifted|failed` 可以共存，不能取代 remote-outcome category。
- `operation_unresolved` 必须绑定 current job/item、exact operation digest、当前 prior target revision、
  current attempt/fence、node lease/fence、source、preflight 与 applicable authority fences，以及 sanitized
  length-framed remote facts；`next_target_revision` 必须为空。合法 predecessor 允许它作为 sequence 1，或在
  `workspace_reserved`、earlier operation、`delete_authority_consumed` history 后出现（按当前 mode/operation
  适用）。该 phase 是 terminal，之后不能追加 operation、verification、delete-authority 或另一 checkpoint，
  也永不改变 job 的 target-chain revision。
- 一个短、fenced transaction 必须原子完成全部 disposition：append exactly one unresolved checkpoint；把仍为 neutral
  的当前 item 标记为 `failed/remote_outcome_unresolved`；把当前 job 标记为
  `needs_attention/remote_outcome_unresolved`；写一条只含 sanitized bindings 的 failure evidence；关闭当前
  attempt；释放 source lease 和 node lease。transaction 必须重验 source/preflight/applicable-authority fences；
  ordinary failure evidence 必须 cross-bind unresolved checkpoint、
  job、item、attempt 与 node fences。任何一步失败全部 rollback。当前 item 不得有 success/skipped/adoption
  checkpoint，已完成 item 的 success/skipped facts、当前 item 的 success/skipped fields、job success fields
  与 target-chain fields 均保持不变；terminal disposition 后不得继续 remote writes。
- `WriteResultDigest` 与 `ObservationDigest` 是 domain-separated、length-framed、sanitized evidence
  bindings。它们只证明当时收到的 bounded write-result/observation product，绝不是 content fidelity digest、
  absence digest、plaintext/raw error storage，也不能把 invalid revision/presence 当作可信 chain fact。
- schema 只修改现有尚未发布的 SQLite/PostgreSQL
  `000069_backup_asset_recovery.up.sql`；两个 down migrations 保持不变。其余 product work 只落在 manifest
  已有的 model/state/executor/worker 与对应 tests。不得增加 path、table、migration、backfill、`000070`、
  target/contracts interface、keyring domain 或 crypto primitive；exact manifest 仍为
  9 current + 55 create + 81 modify = 145。
- exact frozen RED contract 是
  `TestStateOperationUnresolvedProductsAreClosedAndTerminal`、
  `TestBackupAssetRecoveryCheckpointCarriesPrivateUnresolvedOutcomeProduct`、
  `TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix`、
  `TestBackupAssetMigration069UnresolvedOperationOutcomeSQLite` 与 required-real-PostgreSQL
  `TestBackupAssetMigration069UnresolvedOperationOutcomePostgres`。B3 evidence 记录 SQLite legality/parity
  helpers `TestBackupAssetMigration069SQLite` 与 `TestBackupAssetMigration069PairedFiles`、required real
  PostgreSQL `TestBackupAssetMigration069Postgres` 和六类 behavior matrix 全部 PASS 且无 skip；它只关闭
  Correction 14 focused scope，不能替代 first-thirteen 或 whole Task 6 gates。

### 9. Post-B3 Task 6 Batch, Ownership And Next-Gate Requirements

- Task 6 batch status 固定为：B1 是 Corrections 1--3、5、7--13 的 ordinary/foundation implementation；
  其中 B1-E1（Corrections 1--3、5）、B1-E2（Corrections 7--10）与 B1-E3（Corrections 11--13）
  均为 `complete_checked`，但 B1 aggregate 仍是 partial、不是 complete。B2 是 Corrections 4、6 与其
  delete row 的 exact-mirror/multi-delete implementation；B2-E1（Correction 4 plus delete row）与
  B2-E2（Correction 6）均为 `complete_checked`，但 B2 aggregate 仍是 partial。未完成 combined/whole work 均无 retroactive
  RED/review credit。B3 是 Correction 14 unresolved remote outcome，只有 focused scope 为
  `PROVED_COMPLETE`。
- Task 6 拥有 preallocated workspace identity、`none -> reserved` reservation、deadline 以及失败/
  取消/needs-attention workspace 的 cleanup-only classification；Task 7 独占 publication、Content
  revalidation、`revoking` takeover、cleanup node-lease behavior 与 `RecoveryResultRef` denial 的实现和证明。
- Task 6 拥有 bounded restart adoption/reconciliation semantics；Task 8 独占 startup/listener ordering 与
  managed lifecycle。Tasks 7--10 当前全部保持 `not_executed`。
- F6 live-mutation-permit TDD 已在 focused scope `complete_approved`。valid current latch/authority 允许
  `CreateOwnedJobDir`、`CreateDirectory`、`WriteAtomic` 与 `Delete`；permanent latch、current job、
  attempt、node-lease 或 source-fence authority 丢失时均在 fake mutation 前拒绝。
  `RemoveOwnedJobDir` 仍属于 Task 7。
- Task-6-owned F4 workspace/deadline/cleanup-only proof 已在 focused scope `complete_checked`。后续顺序
  现为 whole Task 6 specification review、whole Task 6 quality review、全部 final gates，最后才进入
  Task 7。F6、F3、B1-E1/E2/E3、B2-E1/E2、B3 与 F4 的 focused closure 都不替代这些 whole gates。

## Acceptance Criteria

- [ ] 完整 `prd.md`、`design.md`、`implement.md`、current-main 与 immutable security/state research
      经独立 rereview 明确批准，且 exact create/modify manifest 无重复、无 overlap、create 在 HEAD
      缺失、modify 在 HEAD tracked；当前 correction 本身不得被表述为 approval。
- [ ] genuine RED 先覆盖 plan freeze/source-target drift、target preflight、Provider exact restore、
      conflict policies、fencing/restart、ResultSet cleanup/revoke、API/RBAC/audit 和 frontend wizard。
- [ ] paired `000069` 在 SQLite 与真实 PostgreSQL 上通过 up/behavior/pristine-used down parity；
      latch-before-remote-mutation、crash-before/after-latch、used/purge-to-empty atomic down refusal 均
      通过，`000070` 及以后保持不存在。
- [ ] Task 5 authorization receipt 在 SQLite 与 required real PostgreSQL 上先有 genuine RED，再证明四个
      endpoint 的同 requester+endpoint+key+intent 回放同一 effect、同 key 不同 intent conflict、跨 plan/
      category/endpoint proof reuse conflict；concurrent callers 恰有一个 durable winner，loser 只读回
      winner，不留 orphan grant/job/item/attempt/source lease/node lease。
- [ ] receipt expiry/reaper matrix 证明 `proof <= replay <= presenting session`、write/delete grant expiry
      `<= replay`、近 session-expiry 首次授权零 effect、still-valid proof 在 reaper race 后仍不可复用；
      bounded stateless keyset 在重启、protected old rows 与 normal/latch rows 存在时仍推进 later eligible
      receipts，且 disable/shutdown ownership 明确。
- [ ] receipt row 对 security override plan CAS、write/delete grant create、execute grant consumption+
      job/items/attempt/source lease/node lease+plan transition 具有 exact private effect references，并和
      effect 在同一 transaction commit；fault injection 在 commit 前 rollback 全部，在 commit 后 audit
      projection failure 时保留唯一 effect/receipt、success/replay contract 与 sanitized response。
- [ ] write/delete client-secret contract 先 RED 后 GREEN：仅接受 canonical 43-char unpadded base64url
      32-byte CSPRNG value，persisted `grant_hash` 只为 domain-separated one-way hash，intent 包含 hash；
      lost-response retry 以相同 key/intent/secret 仅返回既有 metadata，改 secret/key conflict，response/
      DB/audit/log/Swagger/frontend 永不含 raw/reversibly encrypted secret。security override 不接受 secret。
- [ ] frontend 只用 Web Crypto `getRandomValues` 生成 32 bytes 并 canonical base64url 编码；CSPRNG
      unavailable 时 fail closed，绝不 fallback 到 `Math.random`。network/5xx ambiguity 保留同一 endpoint
      key+secret；write secret 只内存 handoff 到 execute，delete secret 只内存 handoff 到 fenced delete，
      definitive consumption/context/session change 后清除，URL/storage/log/DOM snapshot 均无 secret。
- [ ] direct SQL 和 model/service tests 在两引擎证明 receipt requester+endpoint+key unique、global
      nonempty proof-digest unique、operation/category/linkage/effect CHECK/FK、immutable update、pre-expiry
      cascade/delete rejection、expiry-only bounded retention、down refusal/parity 与 latch permanence；proof
      JWT/JTI、presenting-session JTI、reason plaintext/ciphertext、grant secret 均不泄露。
- [ ] 任何 source/target/policy drift 都不会在旧 grant 下开始或继续写；partial-write race 保留证据
      并进入 `needs_attention`。
- [ ] 默认隔离恢复、验证、结果下载/retain/cleanup 与重启 reconciliation 在 normal/race 模式通过，
      old fence、伪造 marker、symlink/path escape、lost key 与 cleanup failure 均 fail closed。
- [ ] 原路径四种 conflict policy 与 `exact_mirror` 独立高风险授权/第二 checkpoint 有完整影响矩阵，
      write/delete authority one-use/category/binding、mutation-aware revision chain 与 in-place result-ref
      denial 全部通过，不存在隐式 destructive sync。
- [ ] security decision matrix 证明 default block、known overridable Admin override 的独立 proof/reason/
      binding/audit，以及 unknown/non-overridable fail closed。
- [ ] unpublished workspace deadline/publication barrier、partial cleanup-only disposition、revoking takeover
      phase/fence 与 cleanup node-writer lease在 crash/race/fairness selectors 上通过。
- [ ] feature disable 与 binary downgrade matrix 通过；new frontend/old backend fail closed、old frontend/new
      backend 保持无恢复入口、retained plaintext downgrade-unready、first-use 后 forward-fix-only。
- [ ] source/target locator substitution 与 ciphertext-at-rest/no-leak tests 通过；raw locator/root 不进入
      API、Swagger、audit、log、metric、failure evidence 或 frontend DTO。
- [ ] Task 6 operation product 在 snapshot/job-item/paired SQL/worker/Verify/target-chain 中逐字段一致：
      present arm 只有 exact lowercase SHA-256 content identity + bytes `>= 0`；create 是 prior absent、post
      digest/bytes、prior bytes `-1`，overwrite 是 prior present、post digest/bytes，skip 是 prior present、post
      digest 等于 frozen prior digest、post bytes `-1`、separate prior-target bytes `>= 0` 且只投影 skipped，
      delete 是 prior present、empty post digest、两个 byte fields `-1` 与 exact absence。无 metadata/
      independent fidelity field，也永不使用或持久化 absence digest。
- [ ] restart adoption 只能通过 `AdoptInterruptedOperation(ctx, claim, jobItemID)` 从 durable rows 内部派生
      locator/identity/revision/operation/fence；DB load/lock+decrypt/validation、transaction-free target I/O 与
      final re-lock/fenced CAS 三段之间不跨 target I/O 持有 DB transaction。row-bound locator ciphertext、
      key/cipher version、workspace/root/item binding 与 key-loss DB-only reconciliation 的全部 negative path
      均为 zero false success/checkpoint/target-chain advance/plaintext leak。
- [ ] mutation arm 后的 invalid/contradictory write/verification outcome 只允许 append terminal
      `operation_unresolved`，使用四个 closed unresolved categories 与完整 private checkpoint facts，并在一个
      short fenced transaction 内投影 item failed、job `needs_attention/remote_outcome_unresolved`、sanitized
      failure evidence、attempt close 和 source/node lease release；category-specific write/observation field
      legality、source-revalidation coexistence、source/preflight/authority fences 与 ordinary evidence cross-binding
      全部关闭；`next_target_revision` 为空，既有 success/skipped/job-success/target-chain fields 不变，所有旧
      phase 的新增字段均为 neutral。它可以是 sequence 1 或跟随适用的 workspace/operation/delete-authority
      history，且当前 item 不得有 success/skipped/adoption checkpoint 或后续 remote write。
- [ ] B3 的五个 frozen selectors、genuine RED、相同-selector GREEN、non-skipped required PostgreSQL
      `000069`/six-case matrix 和 independent focused approval 已有 ledger evidence；whole Task 6 acceptance
      仍须把该 focused proof 与 B1/B2 bounded evidence closure、F3/F4/F6、whole reviews 和全部 final gates
      一起通过，不能把 B3 relabel 为 first-thirteen 或 whole-task credit。
- [ ] execute prepared aggregate 在 transaction 外预分配 job/item IDs 与 isolated `jobs/<opaque>` identity，
      冻结 `SemanticTargetDigest` 和独立 `TargetObjectDigest`，并完成所有 encryption。transaction 内从 locked
      facts byte-for-byte 重算后，以 grant CAS 作为 first effect mutation，再一次性插入 complete aggregate；
      isolated job 初始 `workspace_phase=none` 已有 immutable locator/binding，但无 marker/owner/fence/deadline，
      `PrepareFirstWrite` 只允许复用 identity 做 `none -> reserved`。
- [ ] snapshot decode 在 full approved operation-product context 拒绝 duplicate-source、policy-invalid 与
      self-consistent-invalid schema-v2 payload；每行（含 delete）有 canonical `target_relative_locator`，且
      `TargetLocatorEnvelopeBinding` length-frame workspace identity/binding、semantic/final digests、row identities、
      operation facts 和 explicit versions。opaque `ObservedRevision`、closed union、exact match、operation
      sentinel/digest mutation 与 locator-envelope fact tamper selectors 必须先保存 RED，再以同一 selector 在
      SQLite 与 required real PostgreSQL 上通过。paired `000069` 是唯一 migration，manifest 仍为
      9 + 55 + 81 = 145，且不得把本 planning approval 记作 RED/GREEN。
- [ ] Rsync closed-ref/registry-kind, Repository resolver, all-four-phase source/root drift,
      fileaccess pinned descriptor and sanitized-error RED→GREEN gates all pass: a forged ref,
      ciphertext/point/catalog/selection/revision/manifest/root swap, legacy/mutable source or
      mismatched port produces zero runner calls; runner receives only declared regular-entry
      streams and a fenced typed target writer. Task 8 separately proves runtime injection.
- [ ] existing `provider/rsync_test.go` 与 `repository/query_test.go` 中的真实 managed-Rsync
      committed-tree fixture 先观察 empty `FingerprintStrength` 导致 Catalog completion 失败的 RED，
      再在不修改该 selector 的前提下证明 generic Provider record 发出 closed `none`、Repository
      仅封装 locator、Catalog indexer 建成 active complete generation。不得放宽 empty strength、
      在 Repository/indexer 中静默 default，或把本 correction 计入 Tasks 5–10。
- [ ] API route matrix、ownership、step-up purpose、generic 500/redaction、audit 和 Swagger truth 通过；
      legacy restore gate 未被绕过。
- [ ] `env -u NODE_ENV npm run check`、bundle budget、backend aggregate/race、`env -u NODE_ENV make check`、
      required PostgreSQL selectors、Docker/Worker closure 与 exact-manifest/static gates 全部通过。
- [ ] 独立 specification 与 code-quality reviews 无未关闭 Critical/Important findings；专用 PR required
      CI、合并后 CI/Release Please 及预期 release/image disposition 均记录后才归档 Child 13。
- [ ] Child 13 归档后父任务仍为 `planning`，随后才创建 Child 14；不得提前声明总任务完成或启用 GA。

## Out Of Scope

- Child 14 的 Repository reconnect/import/rebuild、Task unlink、retention、legal hold、purge 与灾难重建。
- Child 15 的 GA enablement、legacy UX removal、部署/升级/回滚总演练和父任务最终归档。
- 通用跨 Provider 自动 rollback、任意目标 shell、客户端任意路径读取、无限期明文 retain、恢复内容执行。
- 修改备份 Provider 数据以伪造恢复成功，或从 Catalog/Derived/report 推断 Provider truth。

## Planning And Authorization State

- 用户要求总控自主推进总任务、routine 技术/流程决策无需逐项询问，并允许把多个请求视为同意；
  该 standing direction 覆盖本 Child 的 task creation 与 planning。
- 本文件仍不是实施证据。2026-07-28 两个独立只读 rereview 已分别批准安全/状态修订与 exact
  55-create + 71-modify plan/manifest，未发现未关闭 Critical/Important；最终 immutable preflight
  通过后 `task.py start` 已单次成功执行，Child=`in_progress`、parent=`planning`、program=`12/15`。
- Controller execution record: Tasks 1 (`000069`/model/contracts) and 2
  (exact selection/source revision/plan idempotency) are `complete_approved`.
  Task 3 target/preflight, purpose-exact SSH and durable node-write coordination
  are implemented. Its first independent specification review returned one
  Critical security-decision finding and three Important findings covering
  purpose-exact permits, Task 3A/3B RED chronology, and permanent PostgreSQL
  behavior coverage. The two product defects now have genuine RED-to-GREEN
  remediation; `TestRecoveryBehaviorPostgres` closed the coverage gap with an
  honest immediate GREEN against real PostgreSQL 18.4. The second independent
  specification review found two further Important node-write races: a canceled
  pending `TaskRun` can be resurrected before executor entry, and mutable
  `tasks.node_id` can hide a still-running writer from Recovery after node
  migration. Their atomic no-executor compensation and immutable writer-node
  remediation are implementation_done with genuine RED-to-GREEN evidence. A
  follow-up review found the PostgreSQL `/deadline` harness used a one-second
  wall-clock before `commitEntered`; its test-first deterministic run-context
  seam is also implementation_done. A fresh bounded quality verifier then found
  one Critical target-authority/path-safety finding and two Important durability
  findings. Those corrections, including the final legacy early-cancel
  terminal-overwrite race, are closed by observed RED-to-GREEN evidence. A fresh
  independent Task 3 specification rereview returned `APPROVED`, and the
  controller-inline quality recheck passed affected normal/race, SQLite, real
  PostgreSQL, static, format, manifest, and staged-zero gates. The detailed
  chronology is recorded in `research/implementation-evidence.md`. Task 3 is
  `complete_approved` at task scope. Task 4 B1 is now `complete_approved`:
  Provider missing-strength RED and the real Repository immutable Rsync Catalog
  point changed RED were observed, the detached synthetic factory/fingerprint
  rewrite was rejected, production uses authenticated `request.SourceFingerprint`,
  and the real `Service.OpenCatalogRead` plus `catalog.Indexer` path passed
  focused normal/race selectors. The independent receipts are `SPEC APPROVED`
  and `QUALITY APPROVED`; the inherited-GREEN follow-up is not a new RED.
  This is focused Task 4 evidence only: broad Provider remains blocked by host
  `IFree=0`, with no Child/full/PostgreSQL/frontend/CI/PR/merge claim. Tasks
  5--10 remain `not_executed`. The
  latest independent Task 1 specification review found four technical Important
  gaps: Content CHECK NULL/proof/down parity, consumed-grant binding
  immutability, the exact PostgreSQL F6 selector, and the rejected-down
  mutation-arm trigger/function snapshot. Focused corrective RED-to-GREEN work
  closed those gaps; the evidence records full SQLite 000069, paired files,
  database package, model/recovery regressions, `go vet`, and real PostgreSQL
  18 `TestBackupAssetMigration069Postgres` plus
  `TestRecoveryReviewF6UseLatchPostgres` passing, with the disposable service
  removed. This is focused Task 1 evidence only, not Child or full-gate
  completion. The final independent specification re-review and a fresh
  live-worktree quality re-review both returned `APPROVED` on 2026-07-29.
  The latter confirmed that the older armed-attempt finding was stale against
  the current paired migrations and cross-engine regression matrix.
- Task 2 closed eight focused review findings. Findings 1--7 retain observed
  RED-to-GREEN evidence; Finding 8 was a test-coverage gap whose new matrix was
  immediate GREEN, so no fake RED or unnecessary production change is claimed.
  The final controller-inline specification and quality passes found no open
  Task 2 finding; focused/full packages, two 10x race suites, vet, golangci-lint,
  formatting, diff and staged-zero checks passed. This is Task 2 evidence, not
  Child/full-gate completion.
- 2026-07-30 Task 3 second-rereview remediation adds one tracked modify path,
  `backend/internal/model/task.go`, so paired `000069` can add and model an
  immutable `task_runs.node_id_snapshot`. The prior amended exact manifest was
  9 current + 55 create + 72 modify = 136 unique paths. This focused amendment
  does not allocate `000070`, add a Recovery table or broaden product scope;
  `000070+` remains reserved for Children 14–15.
- 2026-07-30 Task 4 B1 controller scope correction added eight tracked modify
  paths: Provider `rsync.go`/test, Repository `query.go`/test, and fileaccess
  contracts/Linux/other-platform/test paths. It is required because the prior
  Provider-local Rsync issuer admitted forged raw locators and reconstituted a
  source path after validation. The corrected contract keeps Provider portable,
  makes Repository own the descriptor resolver/concrete Rsync port, and makes
  fileaccess retain an anchored no-follow descriptor through runner use. No path
  is removed, no create path/migration/table is added, and the current exact
  prior manifest was 9 current + 55 create + 80 modify = 144 unique paths. The
  scope amendment itself was not Task 4 approval or completion.
- 2026-07-30 focused Catalog blocker amendment adds exactly one tracked modify
  path, `backend/internal/backupasset/provider/catalog.go`. Inspection proved
  `catalogReadSession.acceptEntry` omits `FingerprintStrength`; committed Rsync
  uses that generic session, Repository's sealed session changes only locator
  representation, and `catalog.Indexer` rejects the empty strength through
  `ParseFingerprintStrength`. The real managed-Rsync RED stays in the already
  manifested `provider/rsync_test.go`, with the cross-layer completion regression
  in the already manifested `repository/query_test.go`; no extra test path is
  required. The current exact manifest is therefore 9 current + 55 create + 81
  modify = 145 unique paths. At this dated ledger amendment, no product test or
  implementation had executed: RED, GREEN, focused gates, and independent
  reviews for this correction were `not_executed`; Task 4 was still open and
  Tasks 5–10 remained `not_executed`.
- The original Task 1 model/state/contracts implementation and the original
  Task 3A/3B product implementation did not observe or preserve an executed RED
  before GREEN. Under the standing user authorization, the controller accepts
  those irreversible historical process deviations; neither is a passed TDD
  gate and no RED is claimed retroactively. Task 3C, every Task 3 review fix and
  Tasks 4--10 must retain strict observed RED-to-GREEN chronology. The permanent
  PostgreSQL behavior test is explicitly an immediate-GREEN coverage correction,
  not a product-defect RED.
- Tasks 1--3 are complete_approved at their individual task scopes. Task 3's
  fresh Critical target-authority/path-safety and Important durability findings,
  plus the final legacy early-cancel terminal-overwrite race, are closed by
  observed RED-to-GREEN corrections. Its independent specification rereview
  returned `APPROVED`, and the controller-inline quality recheck passed the
  affected normal/race, SQLite, real PostgreSQL, static, format, manifest, and
  staged-zero gates. This does not close Child 13 or any full gate. Task 4 B1
  is `complete_approved` after its focused Catalog/redaction chronology and
  `SPEC APPROVED` / `QUALITY APPROVED` receipts; the inherited-GREEN follow-up
  is not a new RED. Tasks 5--10, staging, commit, push, PR, CI, and merge remain
  `not_executed`. The broad Provider package is locally environment-blocked by
  host IFree=0 and is not claimed as a product pass.
- 本 amendment 验证时 actual dirty union 为 61：59 条属于当前 145-path manifest，另有且仅有
  两条显式 unrelated path：`go.mod` 与
  `recovery/testdata/rsync_local_to_remote.json`。起始说明中命名的
  `backend/internal/backupasset/recovery/testdata/rsync_local_to_remote.json`
  当时不存在；它仍是 manifest 内 future-create path，不作为 unrelated exclusion。总控不得移动、
  删除或把实际 root-level fixture 纳入本 Child scope。
- 当前没有需要用户补充的产品意图。若 current-main research 暴露新 scope、高风险外部动作或与父合同
  实质冲突，总控才重新请求方向。
- 2026-07-31 Task 5 authorization-receipt focused amendment is controller-approved under the user's
  standing authorization. Two independent read-only reviews found the prior Task 5 wording not
  implementation-ready because generic proof validation did not durably consume JTI, the four
  authorization mutations had no durable idempotency receipt, post-commit audit details were
  purgeable, and encrypted reasons did not participate in a unique full-intent contract. This
  amendment adopts the existing-evidence-table receipt design only: no new path, Recovery table,
  migration number, task-state change, RED, GREEN, product implementation, test execution or
  implementation-evidence claim. The exact manifest remains 9 current + 55 create + 81 modify =
  145, Tasks 1--4 remain `complete_approved`, and Tasks 5--10 remain `not_executed`.
- A follow-up independent read-only review then found one Critical lifetime gap and six Important
  execution-plan gaps: proof re-open after early receipt reaping, plural execute-lease linkage despite
  the singleton RecoveryPoint contract, missing runtime reaper owner, incomplete selectors/settings
  REDs, non-executable frontend CSPRNG lifecycle, and missing required PostgreSQL rollback faults.
  The present focused correction closes those planning contracts within the same 145 paths; it is not
  itself product RED/GREEN, Task 5 approval, implementation evidence, or a Child/full-gate pass.
- Historical Task 5 implementation disposition on 2026-07-31, before the later
  independent-review closure: the backend authorization-receipt scope was
  `implementation_done_pending_independent_reviews`. The evidence-table receipt arm, paired 000069
  guards, four atomic receipt/effect operations, proof/session/grant deadline ordering, hash-only grant
  secrets, exact operation-row snapshot, settings, bounded stateless reaper and standalone runtime owner
  are implemented. Fresh focused SQLite, repeated race and required real PostgreSQL selectors pass.
  Historical REDs and immediate-GREEN coverage additions are distinguished in implementation evidence;
  no missing failure output is reconstructed. At that checkpoint this did not complete the frontend
  CSPRNG lifecycle, Tasks 6--10, independent reviews, Child/full gates or any delivery action.
- Current Task 5 independent-review closure (2026-07-31): later than the dated
  implementation disposition above, Task 5 is `complete_approved` at focused
  authorization-receipt scope only. Specification receipt
  `019fb71a-75df-7770-a17d-9b3d8647d99d` returned `SPEC APPROVED` after
  independently checking exact Steps 7/11/12, SQLite/PostgreSQL races, the full
  PostgreSQL `000069` matrix, and manifest/static/Trellis/index gates; it confirmed
  full Task 8 graph wiring is intentionally deferred. Quality receipt
  `019fb73d-03b6-7111-baf3-83e1ae2e3f8b` returned `QUALITY APPROVED: READY` with
  no Critical, Important, or Minor finding after focused/eight-package tests,
  SQLite race x10, runtime-owner x50, PostgreSQL winner x10/direct-SQL/rollback,
  vet/format/diff/Trellis/manifest/index checks. The exact scope remains 9 + 55 +
  81 = 145 and the only excluded unrelated paths remain `go.mod` and
  `recovery/testdata/rsync_local_to_remote.json`. At that Task 5 closure,
  Tasks 6--10 and the full Task 8 graph were `not_executed`; frontend/Child/full/
  CI/delivery gates remain open. Task 6 is now `in_progress`; Child remains
  `in_progress` and parent remains `planning`.
- Task 6 restart-adoption persistence amendment (2026-07-31): controller-approved
  and independently `SPEC APPROVED` as a planning-only correction. The initial
  independent review returned 3 Critical + 2 Important. The first revision was
  rejected because it invented a delete absence digest. The corrected controlling
  revision removed that digest, then received 2 Important findings for conflating
  skip source identity with prior-target identity and for inserting the aggregate
  before grant consumption; both corrections were adopted. The final independent
  result was `SPEC APPROVED`. Its nonblocking clarification records that skip's
  separately frozen prior-target bytes are immutable and that the exact key/version
  lock is transaction-scoped through grant CAS plus aggregate insert. The later
  2026-08-01 evidence-backed fidelity clarification is controller-approved as a
  focused planning amendment and is not a separate correction among the first thirteen: fidelity is only
  exact lowercase SHA-256 content identity plus byte count, with no metadata,
  parallel digest or synthetic absence claim. That dated amendment itself supplied
  no RED/GREEN. Subsequent bounded closures now classify F6, F3, B1-E1, B1-E2,
  B1-E3, B2-E1, B2-E2 and B3 as complete only at their exact focused scopes. B1 and B2
  aggregates remain partial. Tasks 1--5 retain their approvals; original-review F4,
  unchecked execution items and whole Task 6
  gates/reviews remain open. The original review contains only F1--F8. The first
  thirteen controlling product corrections add no
  path: exact scope remains 9 current + 55 create + 81 modify = 145, paired
  `000069` only, with `000070+` reserved.
- Task 6 focused locator-contract repair (2026-08-01): independent read-only
  design research returned `DESIGN READY`, and controller direction approved its
  coherent preallocated-workspace, dual-digest, recovery-local item-AEAD,
  grant-first prepared-aggregate and three-phase adoption clarification. It
  refines the then-existing first thirteen corrections without itself increasing their count and
  authorizes no new path/table/route/migration/backfill/`000070`. The exact
  named RED matrix is frozen in `implement.md` but was not run by this
  artifact-only change; Task 6 stays `in_progress`, Tasks 7--10 stay open, and
  no RED/GREEN or implementation completion is claimed.
- Task 6 unresolved-remote-outcome B3 closure (post-2026-08-01): Correction 14 is
  `PROVED_COMPLETE` at focused scope only. The bounded final writer changed only
  `backend/internal/backupasset/recovery/executor_test.go` and
  `backend/internal/database/backup_asset_migrations_integration_test.go`. Required
  real PostgreSQL `000069` plus the six-case behavior matrix passed with no skip;
  bounded cancellation, focused race, affected exact-mirror regressions, vet,
  owned gofmt, diff, manifest and staged-zero gates passed, and resources were
  cleaned. Independent specification receipt
  `019fc0c2-cfda-74e3-b218-246f3a425545` returned `APPROVED` and closed both prior
  Important evidence findings; controller-inline quality review found no issue.
  A local reviewer rerun failed to link because of host disk quota and is not
  recorded as pass or fail. Task 6 remains `in_progress`; B1/B2 and whole-task
  credit remain open, Tasks 7--10 remain `not_executed`, staged remains zero, and
  the exact 9 + 55 + 81 = 145 manifest does not change；本 correction 只允许两个
  `000069` up files，down files 保持不变。
- The next authorized sequence is independent focused planning/spec approval for
  F6, F6 live-mutation-permit TDD, F3, bounded B1/B2 evidence closure batches,
  Task-6-owned F4 workspace/deadline/cleanup-only proof, whole Task 6 specification
  and quality reviews, then all frozen/race/required-PostgreSQL/static/manifest
  gates before Task 7. Task 7 owns publication, Content revalidation, revoking
  takeover, cleanup node-lease behavior and RecoveryResultRef denial. Task 8 owns
  startup/listener ordering and managed lifecycle.
- At this later artifact snapshot, the actual dirty union is 82 paths: 80 are
  members of the unchanged 145-path manifest and exactly two are protected
  unrelated paths, `go.mod` and
  `recovery/testdata/rsync_local_to_remote.json`. Staged paths remain zero. The
  earlier 61-path count above is the explicitly dated Catalog-amendment
  snapshot, not current accounting.

## Task 6 F6 Focused Closure (2026-08-02)

F6 is `complete_approved` at focused live-mutation-permit scope only. The sole
permanent delta is the recording fake near
`backend/internal/backupasset/recovery/worker_test.go:34` and
`TestRecoveryReviewF6LatchBeforeTargetMutation` near line 669. The starting
`worker_test.go` SHA-256 was
`a2452e6d5f01c4afb9fb5255ecc188b8790b695f0121430ac078a58cce373534`; its
final SHA-256 is
`352c31b6e5ced3f9f4a033a096ee90c5cd196be3bc4da65ab426bca18254ab3d`.
`target.go` was modified only for the controlled RED and restored byte-for-byte
to SHA-256
`8a0efaafc5bb08d3981790cc0fa27760936b80a58862f1910fd3e96dd5c64822`.

The genuine controlled RED bypassed only the `TargetMutationPermit` live-proof
callback. Every revoked latch, current-job, attempt-fence, node-lease-fence and
source-fence case reached `CreateOwnedJobDir` and produced
`revoked authority CreateOwnedJobDir error=<nil>, want ErrInvalidTargetPermit`.
Compilation or quota failures are explicitly not RED evidence. GREEN admits
`CreateOwnedJobDir`, `CreateDirectory`, `WriteAtomic` and `Delete` under current
authority, while every listed permanent/current-authority loss rejects before
the recording fake mutates. `RemoveOwnedJobDir` remains deferred to Task 7.

The writer's focused combined SQLite/model/recovery selector, the F6 selector
under `-race -count=10`, the four frozen recovery regressions, `gofmt`, `go vet`,
`git diff --check`, exact-manifest guard and staged-zero guard passed.
Independent specification thread `019fc136-feca-7fb0-82bc-3c33739aef12`
returned `SPEC APPROVED`; independent quality thread
`019fc13c-0710-7343-b261-dd866382a8c0` returned `QUALITY APPROVED`, confirming
deterministic isolated fixtures, reliable admission recording, frozen hashes,
the 145-path manifest and staged paths zero.

Required PostgreSQL gate thread `019fc13d-ea0e-7f93-b1c6-32aebcb7368e`
returned `POSTGRES GATE PASSED` for:

```bash
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go test ./internal/database -run '^TestRecoveryReviewF6UseLatchPostgres$' -count=1
```

The result was exit 0, `ok xirang/backend/internal/database 1.709s`, wall
31.032s, against PostgreSQL 18.4 in an isolated `postgres:18-alpine` container
at loopback database `xirang_f6_gate`. The first two compile attempts exhausted
`/tmp` quota and never reached tests; the passing run moved Go and cgo temporary
work to `/dev/shm`. Created container/scratch resources were removed and
pre-existing resources were untouched.

This closure gives no credit to F3, B1/B2, F4, whole Task 6, Child, delivery or
full gates. Task 6 and the Child remain `in_progress`; the parent remains
`planning`. The exact approved manifest remains 9 Phase-1 + 55 create + 81
modify = 145 unique/disjoint paths and staged paths remain zero. At that F6 checkpoint the fixed next
order is F3, B1-E1/E2/E3, B2-E1/E2, Task-6-owned F4, whole Task 6 specification
review, whole quality review, all final gates, then Task 7.

## R0 Execution Governance Rebaseline (2026-08-02)

本节后写优先，只修正后续执行授权、进度口径与恢复节奏，不改变任何产品需求、验收标准、
145-path manifest、paired `000069` 所有权或既有 focused evidence。

- 旧总控任务 `019f6fe1-5133-7012-a18c-7c9dda469cb5` 已停止执行，最后一轮为
  `interrupted`，当前为 `idle`；其 goal 已暂停。旧任务、旧 goal 和旧子任务只作为证据保留，
  不再构成继续执行的 standing authority。
- 本任务硬性禁止再次使用 15 分钟 heartbeat。现有
  `child-13-controller-heartbeat` 保持 `PAUSED`，不得恢复，也不得用另一个 15 分钟 automation
  替代。若未来确有自动化需求，必须是不同的、显式 bounded 用途，不能重建无人值守的持续循环。
- goal、总控/子任务、用户可见任务和 subagent 仍是可用工具，不作全面禁止。只有在目标单一、
  范围与 owner 明确、交接可观察、停止条件清楚时才使用；不得再用一个 goal 覆盖 Child 13
  剩余工作及 Children 14--15，也不得因为“仍有工作”就无限续跑。
- 当前默认同一时间使用一个活动 feature branch、一个实际工作的 implementation worktree 和一个
  product PR；这是降低共享状态冲突的默认策略，不是绝对限制。只有独立性和额外协调收益可证明时
  才扩大并发。现存 detached worktrees 在 R0 中不清理，也不视为正在推进的实现 worktree。
- 本次异常不能推导出一套适用于所有任务的银弹式规则。R0 只为当前大任务建立可恢复的 bounded
  milestone；后续每个 milestone 根据实际风险、依赖和验证成本重新选择 inline、task 或 subagent。
- 进度不得只按耗时、token、代码量、dirty path 或 checkbox 计算。对外同时报告：父任务已合并/
  已归档 deliverable、Child 内准确 task/batch 状态、focused 与 whole-gate evidence、Git delivery
  状态，以及仍未裁决的范围/风险。

按该口径，父任务真实交付进度仍为 `12/15 = 80%`；Trellis `12/13 done` 只表示当前已实例化的
13 个 Child 中有 12 个归档。Child 13 的 Tasks 1--5 为 `complete_approved`，Task 6 为 partial，
Tasks 7--10 未执行，Tasks 11--12/full delivery 未执行；因此不能把“80% 父任务交付”解释为
“Child 13 已接近 80%”，也不能从当前 82 个 dirty path 推导完成率。

R0 只允许整理 Trellis 与证据；其后的 bounded inline F3 裁决已于 2026-08-02 获用户批准，结论为
持久化 **Plan A+**。F3 必须在现有 `backup_asset_recovery_evidence` 产品内维护 claim 与 takeover
两个 distinguished scheduler state，不新增第十三张 Recovery 表或 `000070`：claim 以
`(recovery_job.updated_at,recovery_job.id)`、takeover 以
`(attempt_row.lease_expires_at,attempt_row.id)` 持久化 cursor/high-water 和单调 scheduler revision。
每个候选先在短 scheduler transaction 中被选择并 durable pre-advance，提交后才进入独立的
claim/takeover transaction；因此 crash 或 SQL-eligible persistent candidate-local failure 最多把该候选
延迟到下一 sweep，不能在进程重启后持续饿死 later work。expected conflict/fence loss 只推进 scheduler，
不得修改 Job/Attempt/Lease/target；database-wide failure 失败本 pass，不伪装候选成功。

同一 F3 batch 还必须闭合 authority-only pre-write drift 的 guarded atomic terminalization，以及使用
原始 immutable receipt/frozen execute intent 的 same-key replay，不能从已变化的 source/current facts
重新计算 intent。paired SQLite/PostgreSQL `000069` 可在该精确范围内修改；down guard 只可忽略两个
固定、shape-checked scheduler rows，任何 latch 或真实 Recovery state 仍须 fail closed。

该批准只授权下一次独立 F3 TDD milestone，不代表 F3 产品完成。本次裁决本身不修改产品文件。
F3 writer 结束 focused RED/GREEN、SQLite/real-PostgreSQL、race/static/manifest/staged-zero 验证与证据记录后
必须停止汇报，不得顺带开始 B1/B2。当时后续仍按 F3、B1-E1/E2/E3、B2-E1/E2、F4、whole Task 6
的依赖顺序推进，每一项都需要独立停止点，不能一次性授权剩余全部工作无人值守运行。

## Task 6 B1-E1 Focused Closure (2026-08-02)

B1-E1（Corrections 1--3、5）在其精确范围内为 `complete_checked`。冻结 selector
`TestRecoveryVerifyOperationProductMatrix` 现同时证明 ordinary create/overwrite/skip 的 digest/byte
sentinel、snapshot/job-item persistence、create/overwrite freshly revalidated source、逐 operation source
revalidation、skip source/target separation，以及 closed present Verify 的 exact digest/bytes 与 opaque
revision。SQLite 与 required-real-PostgreSQL 的 paired ordinary job-item matrix 均通过且 PostgreSQL 未
skip。

本轮发现并修正的实际缺陷是 skip 曾把 frozen prior-target digest/bytes 当作 source identity：当前
source size 只与 frozen `EstimatedBytes` 比较，source capability 可携带独立有效 digest；target Verify
仍只与 frozen prior-target digest/bytes 比较并只投影 `skipped`。create/overwrite 继续要求 materialized
source digest/bytes 与 persisted post product 完全相等，且每次 operation 前后继续 revalidate source。

该 focused closure 不给 B1-E2/E3、B2、F4、whole Task 6、Child、delivery 或 final gates credit。
Task 6 与 Child 仍为 `in_progress`，parent 仍为 `planning`；下一停止点是 B1-E2，不在本轮启动。

## Task 6 B1-E2 Focused Closure (2026-08-02)

B1-E2（Corrections 7--10）在其精确范围内为 `complete_checked`。五个冻结 selector 证明 canonical
schema-v2 locator/semantic mapping、whole-product snapshot 重建、preallocated isolated workspace 与
distinct final-object digest、full row-bound recovery-local item AEAD，以及 DB-external no-plaintext-leak。
generic `enc:v2` 仍只表示 encrypted preflight snapshot/job workspace；item ciphertext 使用独立
`recovery:aead:v1:` envelope 与 persisted positive key/cipher versions。

冻结 selector 在继承实现上首先为 GREEN，仅记作覆盖验证。随后受控基线临时移除 schema-v2 row
locator product、AEAD AAD 的 `JobItemID` 以及 isolated final locator 的 workspace prefix；完全不变的
selector 分别因 snapshot 无法重建、authenticated digest 未绑定 row identity、execute preparation
拒绝错误 final-object product 而产生 genuine RED。三处生产实现随后逐字节恢复，生产文件哈希与基线
完全一致，相同 selector 达到 GREEN。

focused normal/race、recovery 全包、五个受影响包、`go vet`、SQLite 与 required-real-PostgreSQL
operation-snapshot/job-item/locator-product companion、format/diff/Trellis/scope gates 均通过，PostgreSQL
未 skip。migration companion 只作 B1-E2 支撑回归，不给 B1-E3 immutable-enforcement credit。

该 focused closure 不给 B1-E3、B2、F4、whole Task 6、Child、delivery 或 final gates credit。Task 6
与 Child 仍为 `in_progress`，parent 仍为 `planning`；下一独立停止点是 B1-E3，本轮不启动。

## Task 6 B1-E3 Focused Closure (2026-08-02)

B1-E3（Corrections 11--13）在其精确范围内为 `complete_checked`。六个冻结 selector 证明 execute
prepared aggregate 的 replay/preparation-zero-effect/exact-key/grant-first/rollback product、只从 durable
state 派生的三边界 adoption、永久 cleanup-key loss 的 bounded DB-only reconciliation、确定性 takeover
one-winner，以及 SQLite/required-real-PostgreSQL 对 workspace、dual digest、locator versions、operation
sentinel、one-way projection、terminal rewrite、delete legality 与 per-job digest uniqueness 的 paired
immutable enforcement。

这些 selector 在继承实现上首先为 GREEN，只记作覆盖确认。随后五个受控基线分别移除 adoption durable
digest equality、把 cleanup-key reconciliation 候选反转为 pre-arm、阻止 grant CAS 持久化
`consumed_at`、绕过 exact `LockActiveTx` key equality，并从 paired immutable trigger 移除
`semantic_target_digest`。完全不变的 selector 分别观察到 false adoption、漏掉 current post-arm attempt、
execute 在 aggregate 完成前被拒绝、mismatched key 被接受，以及 SQLite/PostgreSQL 均允许带合法
`pending -> failed` projection 的 immutable rewrite。生产与 migration 文件随后恢复到受控基线哈希；
永久增量只保留三份测试文件中的 B1-E3 coverage 与三处 focused lint 修正。

focused normal/race、完整 recovery、runtime startup cleanup-key 边界、六个受影响包、同范围 `go vet`、
SQLite full/paired companion 与 required-real-PostgreSQL locator/adoption/rollback/full-000069 companion 均通过，
且 PostgreSQL 未 skip。B1-E3 新增/增强行的 lint 问题已清零；recovery package 的完整
`golangci-lint` 仍报告七条本里程碑前既有的 Task 6 问题，因此不宣称全包 lint 通过。

在 B1-E3 checkpoint，该 focused closure 不给 B2、F4、whole Task 6、Child、delivery 或 final gates
credit。B1 aggregate 仍为 partial；Task 6 与 Child 保持 `in_progress`，parent 保持 `planning`。当时的
下一独立停止点是 B2-E1。

## Task 6 B2-E1 Focused Closure (2026-08-02)

B2-E1（Correction 4 plus delete row）在其精确范围内为 `complete_checked`。永久增量仅为既有
`backend/internal/backupasset/recovery/contracts_test.go`：没有新增顶层 selector、接口、model、table、
migration number、crypto domain 或 manifest path。冻结 selector 现在明确绑定 delete 只能属于 durable
`in_place + exact_mirror`，prior 必须为合法 SHA-256 present fact，post digest 必须为空、prior/post bytes
均为 `-1`、plan item/source 必须为空，且不得存在 synthetic absence digest。

运行时在没有 durable exact-mirror delete authority 时必须暂停且不得 delete；成功只接受 explicit
`AbsentObservation{Evidence: exact}`。`permission_denied`、timeout、unsupported stat、transport failure、
ambiguous missing 均不能满足 absence。继承实现先通过增强后的 selector，只记 coverage。随后四处受控
行为移除分别放宽 arbitrary nonempty absence、绕过 empty-grant pause、允许 synthetic delete post digest、
以及在 paired migration 中允许 isolated delete；完全不变的 frozen selector 均产生对应 RED。受保护的
`contracts.go`、`executor.go` 与两份 `000069` up migration 随后恢复到冻结哈希并达到最终 GREEN。

focused normal/race x10、完整 recovery、六个受影响包、同范围 `go vet`、SQLite 与 required-real-
PostgreSQL locator-product companion、format/diff/Trellis/scope gates 均通过，PostgreSQL 未 skip。完整
recovery package lint 仍是此前七条且没有指向本轮 owned delta，因此不宣称 whole-package lint pass。

successful-delete 支撑回归同时观察到了 absence-chain，但该继承断言不给 Correction 6 或 B2-E2
completion credit。B2 aggregate 仍为 partial；Task 6 与 Child 保持 `in_progress`，parent 保持
`planning`。下一独立停止点是 B2-E2，本轮不启动。

## Task 6 B2-E2 Focused Closure (2026-08-02)

B2-E2（Correction 6）在其精确范围内为 `complete_checked`。没有新增顶层 selector；既有
`TestRecoveryVerifyOperationProductMatrix` 现在纳入单删除 absence-chain、同次 multi-delete、跨重启
authority reuse 与 consumed-authority absence reconciliation。永久增量仅在
`contracts_test.go`、`executor_test.go` 与 `testutil_test.go`，没有改变产品接口、model、table、migration、
crypto domain、manifest path 或 B3 product。

同次 multi-delete 现在精确绑定 delete call ordinal、一次 `delete_authority_consumed`、每个 delete 的
operation checkpoint、literal `xirang/recovery/target-absence-chain/v1` revision、前一步 next 作为后一步
prior，以及 terminal job chain 等于最后一次 delete revision；B3 unresolved fields 必须保持中性。
required/consumed durable checkpoint pair 在同次第二个 delete 或 restart 后继续授权，不要求再次提交 bearer
secret、不重复消费 grant，也不重复删除已经完成的 item。

两个受控行为移除让完全不变的 frozen selector 产生 genuine RED：把 absence-chain domain 混同为普通
present-target chain 会破坏 single/multi-delete exact revision；忽略 durable consumed-delete pair 会让同次
第二次 delete、restart continuation 与 consumed-absence reconciliation 全部 fence-lost。`executor.go` 与
`worker.go` 随后恢复到冻结哈希并达到最终 GREEN。

真实 PostgreSQL production-`000069` fixture 还暴露并修复了一处 test-only clock floor：migration 的
scheduler row 使用 subsecond database `CURRENT_TIMESTAMP`，共享 fixture 则按整秒冻结 Go clock；fixture
现在只在 migration 路径把时钟推进到该 durable floor 之后。原 trigger rejection 不算 product RED；修复后
PostgreSQL 子测试连续 5 次通过，完整 frozen selector 在 required mode 下无 skip 通过。

focused normal/race x10、完整 Recovery、六个受影响包、`go vet`、production SQLite 与 required-real-
PostgreSQL multi-delete、format/Trellis/manifest/staged-zero gates 均通过。完整 Recovery lint 仍为此前七条，
且没有指向 B2-E2-owned 行，因此不宣称 whole-package lint pass。

B2 aggregate 仍为 partial，Task 6 与 Child 仍为 `in_progress`，parent 仍为 `planning`。F4、unchecked Task 6
rows、whole reviews/gates、Child delivery 与全部 Git delivery action 保持 open。下一独立停止点是
Task-6-owned F4，本轮不启动。

## Task 6 F4 Focused Closure (2026-08-02)

F4 在 Task-6-owned workspace/deadline/cleanup-only 精确范围内为 `complete_checked`。永久增量仅在
`backend/internal/backupasset/recovery/executor_test.go`，新增两个稳定 selector：
`TestRecoveryReviewF4WorkspaceDeadlineAndPublication` 与
`TestRecoveryReviewF4PartialWorkspaceCleanupOnly`。它们不实现或证明 Task 7 的 publication、Content
revalidation、`revoking` takeover、cleanup node lease 或 `RecoveryResultRef` denial。

第一个 selector 证明 execute 只预分配并加密保存 `jobs/<opaque>` identity；`none -> reserved`、HMAC
marker binding、owner/fence、24 小时 immutable plaintext deadline、reservation checkpoint 与永久 latch
均在 `CreateOwnedJobDir` 和首个内容字节前提交；成功执行只进入 `sealed` 且不创建 ResultSet/result row；
unexpected remote directory 失败后的 retry 复用同一 locator、binding、marker、deadline 与唯一 checkpoint。
第二个 selector 证明 pre-arm failure 与 queued cancellation 保持 `workspace_phase=none`，而 armed
cancellation 与 post-arm unresolved outcome 保留 deadline 并进入 `needs_attention|cleanup_due`；所有路径均
保持 unpublished。

两个独立受控行为移除产生 genuine RED：把 deadline 改为 `24h - 1s` 使第一个 selector 失败；移除 armed
cancellation 的 `cleanup_due` projection 使第二个 selector 观察到非法残留 `reserved`。临时产品改动随后
恢复，`worker.go` 与 `executor.go` 分别回到冻结 SHA-256
`75e4c4a2beb421a6f76d6d9b752f7afb47cc034a6609d00dd0760b66e2798972` 和
`23762c8e435a553d1e0da1dc346b8f03bef0e100003cd8530036e37bb6d913a9`。

fresh focused normal/race x10、完整 Recovery 与五个相关 backend package、SQLite 三个 migration
companion、required-real-PostgreSQL 三个 companion、同范围 `go vet`、owned `gofmt -d`、diff、Trellis、
manifest 与 staged-zero gates 均通过，PostgreSQL 未 skip。完整 Recovery lint 仍恰好是此前七条，且没有
指向 F4 新增测试区间，因此不宣称 whole-package lint pass。

该 closure 不给 B1/B2 aggregate、unchecked Task 6 rows、whole Task 6 reviews/gates、Child、delivery 或
Task 7 credit。Task 6 与 Child 保持 `in_progress`，parent 保持 `planning`；下一停止点是 whole Task 6
specification review，本轮不启动。

## Whole Task 6 Specification Review Corrections 15--17 (2026-08-03)

本节是 whole Task 6 specification review 发现阻断项后的 later-controlling 修正。用户已批准
方案 A：保留现有四个 unresolved category 和十二表/paired `000069` 边界，通过既有 checkpoint、
evidence、workspace phase 与 Provider source resolver 闭合合同，不新增状态字段、表、migration、
`000070`、crypto domain 或 manifest path。既有 F6、F3、F4、B1-E1/E2/E3、B2-E1/E2 与 B3 focused
credit 全部保留，但 whole Task 6 specification review **未通过**，直至以下 Corrections 15--17
完成 RED/GREEN、双引擎验证和 whole review。

### Correction 15: Post-Arm Calls Without A Trustworthy Product

- mutation arm 后，`WriteAtomic`、`Delete` 或 `Verify` 返回 error 时，调用可能已在远端产生 effect，
  因而必须进入现有 `operation_unresolved` terminal disposition，不能把 error 直接返回后保留一个可
  blind replay 的 running attempt。
- `write_result_invalid` 有且只有两个合法 arms：调用返回一个 bounded 但非法 product 时，保存其
  sanitized length-framed digest；调用只返回 error、没有可信 product 时，write digest/revision 与全部
  observation facts 保持 empty。`observation_invalid` 对非法 returned observation 保存 sanitized digest；
  对只有 Verify error、没有可信 observation 的情况，observation digest/revision/presence 全部 empty。
  两种 empty-product arms 都只由进程内明确的 call-failed signal 产生，不能把零值结构体伪装成调用失败。
- `verification_mismatch` 继续要求 valid observation。ordinary create/overwrite/delete 在有 valid write
  result 时必须保存 write facts；restart adoption 因 crash boundary 没有原始 write result 时允许 write
  facts empty。`skip` 仍保持 write facts empty。paired SQL 接受该 adoption shape，Recovery service 必须
  区分 ordinary 与 adoption 来源并 fail closed。
- category、terminal checkpoint、item/job failure category、attempt/lease closure、no-chain-advance 与
  sanitized evidence 仍完全复用 Correction 14；raw error、stdout/stderr、locator 或 serialized result
  不得进入 checkpoint、evidence、log 或 API。

### Correction 16: Provider Revalidation And Completed-Operation Evidence

- `WorkerCoordinatorDependencies` 注入现有 `provider.RsyncRestoreSourceResolver`。restart adoption 从
  locked durable plan 通过 `NewRsyncRestoreSourceRef(plan)` 构造 closed ref，在无 DB transaction 时解析、
  revalidate 并关闭 Provider source；DB-only `SourceValidator.RevalidatePlanTx` 仍保留，但不能再被描述成
  Provider source revalidation。
- adoption 即使 source revalidation 为 `drifted|failed`，仍可执行只读 target Verify 以确定 remote
  outcome。invalid/error/mismatch observation 进入 Correction 15 的 unresolved disposition，并携带真实
  `source_revalidation_outcome`；valid matching observation 不能伪装为正常完成，而按下述 completed-
  operation + source-failure 原子 disposition 处理。
- 每个 valid verified operation 都有 normal `operation` checkpoint，并通过现有 `job_item_id` 精确绑定
  item。create/overwrite/delete 按既有 domain 推进 target chain；`skip` checkpoint 的
  `next_target_revision == prior_target_revision`，表示已验证 unchanged target，而不是伪造 mutation。
  normal operation checkpoint 的 unresolved fields 保持 neutral。
- target write/absence/skip observation 已严格验证后，若紧随其后的 source revalidation 为
  `drifted|failed`，一个 short fenced transaction 必须同时：保存 item `succeeded|skipped`、normal
  operation checkpoint 与应有 chain advance；写 sanitized source-failure evidence；关闭 attempt；释放
  source/node leases；并将 job 置为 `needs_attention/source_revalidation_failed`。isolated workspace 同时
  进入 `cleanup_due`。不得丢弃已完成 remote operation evidence，也不得把 source failure 误归类为
  remote-outcome ambiguity。
- 若后续 operation 前的 Provider revalidation 失败，已持久化的 earlier item/checkpoint/chain 必须原样
  保留，并用最后一个 exact item-bound operation checkpoint 完成同一 needs-attention/attempt/lease
  terminalization；不得继续 target mutation或让 persistent source drift 无限卡住队首。

### Correction 17: Durable Workspace Marker Phase

- isolated `PrepareFirstWrite` 仍先提交 `none -> reserved`、marker binding、owner/fence、24-hour deadline、
  sequence-zero reservation checkpoint 与 schema-use latch。`CreateOwnedJobDir` 返回合法 owned result 后，
  必须在另一个 short fenced transaction 中 CAS `reserved -> marker_created`，然后才能把 item-level
  mutation permit 用于 `CreateDirectory`/`WriteAtomic`。
- reserved permit 只允许 workspace-level `CreateOwnedJobDir` 或幂等 marker validation；item-level mutation
  只允许 `marker_created|writing`。crash 发生在 remote marker 成功与 CAS 之间时，DB 保持 `reserved`，
  retry 只能对同一 immutable locator/binding 幂等 revalidate/create 后再完成 CAS，不能写内容、rename
  或重新分配 workspace。
- ordinary first operation 与 restart adoption 必须从 durable `marker_created`（后续可为 `writing`）加载
  workspace；第一个 committed operation 执行 `marker_created -> writing`。paired SQLite/PostgreSQL
  `000069` 必须添加完整 one-way workspace phase guard：`none -> reserved -> marker_created -> writing ->
  sealed -> published`，以及每个已批准 active phase 到 `cleanup_due`、`cleanup_due -> workspace_cleaned`
  的精确边；禁止 `reserved -> writing`、逆向、跳跃或 terminal rewrite。

### Frozen Acceptance For Corrections 15--17

- [ ] `TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved` 证明 create/overwrite `WriteAtomic` error、
  exact-mirror `Delete` error 及 create/overwrite/skip/delete `Verify` error 都只产生一个 terminal unresolved
  disposition；无 product 时相应 digest/revision/presence empty，returned-invalid product 仍保留 sanitized
  digest。
- [ ] `TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation` 证明 valid remote result 后 source drift
  保存 item/checkpoint/chain 后进入 `needs_attention/source_revalidation_failed`，以及 later pre-operation
  drift 保留 earlier evidence、关闭 attempt/leases、停止后续 target I/O。
- [ ] `TestRecoveryAdoptInterruptedOperationUsesProviderSourceAndTerminalDisposition` 证明 adoption 只用 durable
  Rsync ref 调用 injected resolver，Provider source 被 revalidate/close，error/mismatch 不再返回裸 fence-lost，
  matching target + untrusted source 不会成为 normal success。
- [ ] `TestRecoveryPrepareFirstWritePersistsMarkerCreatedBeforeContent` 证明合法 owned marker result 后 durable
  phase 为 `marker_created`，content fake 在 phase CAS 前不可达，retry 复用同一 identity，而首个 committed
  operation 只走 `marker_created -> writing`。
- [ ] `TestBackupAssetMigration069WholeTask6ClosureSQLite` 与 required-real-PostgreSQL
  `TestBackupAssetMigration069WholeTask6ClosurePostgres` 证明 no-product unresolved arms、adoption no-write
  mismatch、item-bound normal operation/source-failure evidence、skip no-chain checkpoint 与完整 workspace
  one-way transition parity；`TestBackupAssetMigration069PairedFiles` 继续证明 paired text contract。
- [ ] 相同 selectors 先在当前实现上观察预期 RED，再做最小 GREEN；随后运行 whole Recovery normal/race、
  paired SQLite/required-real-PostgreSQL、`go vet`、recovery package lint remediation、`gofmt`、diff、Trellis、
  exact-manifest 与 staged-zero gates。whole Task 6 specification/quality review 仅在这些证据完成后重开。

## Whole Task 6 Review Correction 18: Isolated Adoption Continuation (2026-08-03)

本节是 whole Task 6 review 在 Correction 16 实际多项接管路径上发现的 later-controlling 修正。用户批准
同一 takeover attempt 续跑方案；本修正不新增表、migration、字段、状态、公开 API、Provider 合同、
crypto domain 或 manifest path，也不启动 Task 7。

- isolated workspace 的 `workspace_owner/workspace_fence` 是远端 marker 的不可变创建 provenance，并由
  已持久化 marker HMAC 绑定；takeover、adoption 和后续普通 operation 不得改写这两个字段。当前写权限
  继续由 current attempt/source/node fences 与 latch 独立授权，不能把 marker creator 当成当前 worker。
- adoption 必须接受 `workspace_reserved` sequence zero 后的完整、连续 normal operation 历史。每个后续
  checkpoint 必须唯一映射一个 durable completed item，匹配 item operation digest 与全部 immutable
  preflight/authority facts，保持 unresolved/delete facts neutral，并从 `preflight_target_revision` 精确链到
  当前 `target_chain_revision`；skip 保持 revision，create/overwrite 必须推进 revision。selected item 必须
  pending 且未出现在历史中，完整历史必须进入 adoption durable digest。
- exact observation/source matched 的 adoption 投影后若仍有 pending item，current attempt 与 source/node
  leases 必须保持 active，job 保持 `running|writing`；不得关闭 attempt 后留下不可 claim/takeover 的 job。
  若没有 pending item，则必须沿普通完成路径关闭 attempt、释放 leases，并把 isolated job 原子推进到
  `succeeded|sealed`。
- `ExecuteClaim` 的初始路径仍只接受当前 attempt 创建的唯一 reservation。续跑路径只在完整 isolated
  history 通过、workspace 已为 `writing`、且最后一个 normal operation checkpoint 精确绑定 current
  attempt ID/fence/node fence 时开放；这证明当前 takeover 已先解决 ambiguous remote operation。仅有旧
  attempt checkpoint 的 takeover 必须在任何新 write/skip 前 fail closed。
- 合法续跑只 materialize/执行仍 pending 的 frozen items，复用原 workspace、marker、deadline、latch 与
  target chain；不得再次执行已 adopted/completed item，不得重新调用 workspace creation，不得改写 marker
  provenance。source drift、authority drift、invalid history 或 fence loss 继续使用既有 fail-closed/terminal
  disposition。

### Frozen Acceptance For Correction 18

- [ ] `TestRecoveryAdoptsLaterIsolatedOperationAfterPriorCheckpoint` 先证明 takeover 在 adoption 前不能通过
  `ExecuteClaim` 盲 replay；再证明 later item adoption 保留 earlier checkpoint/chain 与 marker provenance，
  pending 时保持 attempt/leases active，随后同 claim 只执行剩余 pending item 并达到 `succeeded|sealed`。
- [ ] 既有 sequence-zero create、out-of-order skip、concurrent adopter、Provider-source、post-terminal rewrite
  与 atomic rollback selectors 按 pending-aware disposition 更新并保持 GREEN；last-pending adoption 必须证明
  attempt/lease/job terminalization 原子完成。
- [ ] focused normal/race repetition、whole Recovery、paired SQLite/required-real-PostgreSQL、`go vet`、lint、
  `gofmt`、diff、Trellis、manifest、protected-hash 与 staged-zero gates 全部通过后，才能再次请求 whole
  Task 6 specification/quality review。

## Whole Task 6 Review Correction 19: Durable Marker Validation Takeover (2026-08-03)

本节是 controller 已批准的 later-controlling marker takeover 修正。它只修复以下 crash boundary：旧 attempt
已经提交 `reserved`，远端 `CreateOwnedJobDir` 已成功，但 `reserved -> marker_created` 的 fenced CAS 未提交，
随后旧 attempt 过期并由新 attempt 接管。不可变 marker 创建 provenance 与完成 marker validation 的 attempt
是两个不同产品，不能通过改写 `workspace_owner/workspace_fence` 或接受任意旧 marker creator 来混同。

- isolated job 增加三个私有持久字段：`workspace_marker_validation_attempt_id`、
  `workspace_marker_validation_attempt_fence`、`workspace_marker_validation_node_fence`。三者记录成功提交
  `reserved -> marker_created` 的 exact attempt/node fence product；它们不是 marker 创建者，也不是当前
  `writing` continuation authority。
- `none|reserved` 必须保持 validation tuple 全空/零；`marker_created|writing|sealed|published` 必须保持完整
  tuple；`cleanup_due|workspace_cleaned` 允许保留来源 phase 的空 tuple 或完整 tuple。tuple 只能在
  `reserved -> marker_created` 同一事务中首次赋值，赋值后不可改写。in-place job 永远保持全空/零。
- `reserved` takeover 可以在 current attempt/source/node/latch fences 下获得 workspace-only permit，对同一
  immutable locator、workspace binding 和 marker HMAC 做幂等 `CreateOwnedJobDir` validation；合法 returned
  product 后，short transaction 原子提交 `marker_created` 与 current validation tuple。原始
  `workspace_owner/workspace_fence`、marker binding、deadline 和 reservation checkpoint 全部保持不变。
- item permit 在 `marker_created` 只接受与 validation tuple 完全相同的 current attempt/fences。不同 attempt
  接管一个已经是 `marker_created` 的 job 时，first item 可能已经发生但尚未 checkpoint，因而 ordinary
  `ExecuteClaim` 必须在任何 workspace/item mutation 前 fail closed，并先走既有
  `AdoptInterruptedOperation`。进入 `writing` 后仍只由 current operation checkpoint admission 续跑，不能用
  marker validation tuple 代替 operation adoption。
- paired SQLite/PostgreSQL `000069` 必须具有相同列、shape CHECK、赋值边与赋值后 immutability；不得新增
  table、migration number、公开 API、Provider contract、crypto domain、路径或 `000070`。down migration 仍由
  现有 whole-table drop 负责，无新增独立 schema object。

### Frozen Acceptance For Correction 19

- [ ] `TestRecoveryReservedMarkerTakeoverPersistsValidationBeforeFirstItem` 先复现 old claim 的 marker target
  success + CAS failure，证明 job 留在 `reserved` 且 validation tuple 为空；takeover 复用完全相同的远端
  marker identity，保持 marker creator provenance 不变，在任何 item I/O 前提交 current validation tuple，
  随后只执行 frozen pending items。
- [ ] `TestRecoveryMarkerCreatedTakeoverRequiresAdoptionBeforeReplay` 证明 old claim 已提交 `marker_created`
  后，新 takeover 的 ordinary `ExecuteClaim` 在 workspace/create/write/delete 前返回 fence lost；validation
  tuple 不被接管改写，ambiguous first item 只能进入现有 adoption/terminal contract。
- [ ] `TestBackupAssetMigration069WholeTask6ClosureSQLite` 与 required-real-PostgreSQL companion 证明 paired
  字段、phase shape、`reserved -> marker_created + tuple` 原子赋值、非法 partial tuple、same-phase 注入和
  post-assignment rewrite 全部同构；`TestBackupAssetMigration069PairedFiles` 保持文本 parity。
- [ ] 两个 Recovery selector 与 paired migration selector 必须在 production change 前观察 genuine RED，
  最小 GREEN 后运行 Corrections 15--19、whole Recovery、dual-engine、race、vet/lint/format/diff/Trellis/
  manifest/protected-hash/staged-zero gates。Task 7 与全部 Git delivery action 仍不得开始。

## Whole Task 6 Review Correction 20: Consumed Exact-Mirror Takeover (2026-08-03)

本节是用户已批准并完成 focused RED/GREEN 的 later-controlling exact-mirror 修正。它只修复以下
takeover boundary：旧 attempt 已原子持久化 `delete_authority_required`、
`delete_authority_consumed` 与 consumed grant，随后在某个 remote delete 成功但对应 operation checkpoint
提交前失效；新 attempt 必须先 exact-absence adoption，之后才能继续剩余 delete。Corrections 1--19 的
产品边界、paired `000069`、十二表、公开 API、Provider/Repository/runtime 合同、crypto domain 与 exact
145-path manifest 均不扩张。

- consumed exact-mirror authority 是 immutable historical product。required/consumed checkpoint 必须拥有同一
  valid historical attempt ID、positive attempt fence 与 positive node fence；consumed grant 的
  `delete_attempt_id`、`delete_attempt_fence` 与 `delete_node_fence` 必须与该 pair 完全一致。grant/job/plan/
  delete-set/target-revision/binding/expiry/consumed-time 约束保持不变。
- fresh takeover claim 不是该 historical product 的创建者，因而不得要求 historical tuple 等于 current
  claim；也不得改写、重新消费或重新签发已经 consumed 的 grant。该分离只适用于 historical authority
  自校验，不能放宽 current live attempt/source/node/latch fences。
- ordinary execution 在 takeover 后仍必须通过 `currentFirstWritePermitPathTx` 的 latest-current-operation-
  checkpoint gate。尚未 adoption 的 fresh claim 在任何 workspace/write/delete/verify 调用前 fail closed；
  exact-absence adoption 必须先追加 current-claim operation checkpoint，随后 bearer-free continuation 才可
  复用单个 historical consumed grant 完成 remaining delete set。
- 永久产品与测试变更只允许落在既有 manifest 的 `recovery/worker.go`、`recovery/executor.go` 与
  `recovery/executor_test.go`。不得增加 model/DDL/migration、Provider、Repository、runtime、API、frontend、
  Task 7、`000070`、新表、backfill、crypto domain 或 manifest path。当前 Task 6 product correction 总数为
  二十；focused Correction 20 closure 不替代 whole Task 6 specification/quality review 或 final gates。

### Frozen Acceptance For Correction 20

- [x] `TestRecoveryExactMirrorConsumedAuthorityFreshTakeoverRequiresAdoption` 证明 fresh takeover 的 ordinary
  execution 在 adoption 前零 target call，并保持 historical checkpoint/grant tuple 不变。
- [x] 同一 selector 证明 exact-absence adoption 追加 current-claim operation checkpoint；存在 pending delete
  时 attempt/source/node leases 保持 active，随后同一 claim 只执行 remaining delete 一次并正常结束。
- [x] historical required/consumed checkpoint pair 与 grant tuple 自校验，current live fences、latch 与 latest-
  current-operation-checkpoint 独立授权；grant 只消费一次且不被 takeover 改写。
- [x] focused normal/race、whole Recovery normal/race、model/database、required-real-PostgreSQL no-skip、vet、
  backend lint、owned `gofmt -d`、diff、Trellis、manifest、protected-hash 与 staged-zero evidence 已记录；
  whole Task 6 reviews/final gates 仍须本轮重新执行，Task 7 与 Git delivery action 仍不得开始。

## Whole Task 6 Review And Gate Closure (2026-08-03)

Task 6 现在在 whole scope 为 `complete_checked`。controller-inline specification review 将 F6、F3、
Task-6-owned F4、B1/B2/B3、Corrections 1--20、paired `000069` 与 Task 7/8 ownership split 作为一个产品
审查；补入遗漏的 Correction 20 PRD 合同并修正 current whole-review correction count 后，无 open
Critical/Important specification finding。controller-inline quality review 对完整 Task 6 delta、关键 transaction/
fence/data-flow 与当前 evidence chronology 未发现 open Critical/Important finding。

fresh frozen selectors、whole Recovery normal/race、affected package normal/race、paired SQLite 与 required-real-
PostgreSQL、vet、full backend lint、owned format/diff、privacy/scope、Trellis/JSON/JSONL、exact manifest、保护文件、
branch/HEAD/remote-main 与 staged-zero gates 全部通过。B1 与 B2 aggregate 因本次 combined/whole evidence 从
partial 转为 `complete_checked`；B3 保留其 focused historical credit，同时纳入当前 whole review。

Corrections 15--18 的实现与稳定 selector 可由当前 fresh GREEN 证明，`task.json` 也保留先前 RED/GREEN
摘要；但其 exact historical RED output 没有独立进入 `research/implementation-evidence.md`，因此对应历史空
复选框不做事后补勾，也不伪造 chronology。Corrections 19--20 的独立 ledger evidence 保留不变。共享
code-spec 不再增加 Task-6-private recovery fencing 细节：现有 backend database spec 已覆盖本任务产生的 paired
`000069` 与 used-down admission 可复用合同，其余是尚未发布的 task-local product contract。

Child 13 仍为 `in_progress`，parent 仍为 `planning`，program delivery 仍为 12/15。下一步是 Task 7；本次
closure 未启动 Task 7，也未 stage、commit、push、创建 PR、运行 CI 或执行任何 Git delivery action。

## Task 7 Production Target-Root Registry And Plan Snapshot (2026-08-03)

用户已批准 R1：按 root 分行的私有 typed registry。本批只关闭生产 target-root
注册与 plan locator 快照缺口；不将它解释为 Task 8 runtime composition、Task 9
管理 API、SSH/SFTP adapter、marker codec 或删除授权。

### Requirements

- 一个非归档注册节点可拥有多个 bounded target roots。每个 root 使用节点内
  不可混淆的 opaque ID 和可安全展示的 label；真实 locator 是敏感数据。
- 只接受严格的 canonical POSIX 绝对 locator。不解析 symlink/mount 的活体安全性；
  该责任仍属于后续 target probe/adapter。
- root 定义必须使用现有 `settings.Service` 和数据加密密钥整体加密落库；
  不新增表、列、migration number、backfill 或 `000070`。
- root 定义不得进入普通 settings registry、`GetAll`、`GetEffective`、BatchUpdate、
  reset、config import/export、结构化日志、审计、metrics 或前端 DTO。即使管理员请求
  sensitive config export，整个私有 root 记录也必须缺席。
- 新 plan 必须在自身创建事务内解析当前注册的
  `NodeID + RootID + RootLocatorDigest`。缺失、归档节点、非 canonical、解密/文档失败、
  key/payload 替换或 digest 不等均必须在 plan/item 写入前关闭失败。
- 解析成功的 locator 必须快照到现有 plan 私有字段并由已有 model hook 加密。
  响应、intent/audit/log 只使用已批准的 opaque IDs 和 digest。
- 已成功创建的 plan 使用自身不可变 locator 快照。后续 registry 旋转或删除不改写
  旧 plan，幂等 replay 不重新解析当前 registry，但必须重新验证快照自身与已持久
  root digest 一致。
- 修改 root 的运行时 validate/drain/install/rollback 编排仍由 Task 8 拥有；专用管理
  route、RBAC 和公开 safe-summary 响应仍由 Task 9 拥有。

### Acceptance

- [x] 私有 registry 能在同一节点下注册、解析、旋转和删除多个 root，并且 DB
  原始值只是当前 v2 ciphertext。
- [x] 非 canonical path、越界 ID/label/locator、归档/缺失节点、明文/旧 v1/损坏
  ciphertext、未知/重复文档字段与 key/payload 替换全部关闭失败。
- [x] 普通 settings 读写/reset 和 config export/import（含 `include_secrets=true`）既不返回也
  不接受私有 root 记录；可识别的测试 locator/ciphertext 不出现在 JSON 或错误中。
- [x] plan 新建在一个事务内精确解析 root 并持久加密快照；任何替换或解析
  失败对 plan/items 都是零写入。
- [x] 幂等 replay 验证已持久快照且不查询当前 registry；root 旋转后新 plan 只能
  绑定新 digest，旧 plan/replay 仍只能绑定旧快照。
- [x] focused settings/recovery/config-export tests、完整 settings 与 Recovery packages、race、vet、
  lint、format/diff、Trellis/JSON/JSONL、exact manifest、protected hash 和 staged-zero gates 通过。
- [x] 停在 root registry + encrypted plan snapshot。`RemoveOwnedJobDir` 仍不可达，不做
  SSH/SFTP I/O、marker 读写、runtime publication、API/frontend、删除/tombstone 或 Git delivery。

## Task 7 Recovery Workspace Marker Codec Interoperability (2026-08-04)

用户已批准下一批采用“marker document/codec + 现有 TargetPort 创建/验证契约互操作”。本批只关闭
远端 owned-workspace marker 的私有文档、认证、严格解析和 creator-bound 请求缺口；不实现 SSH/SFTP
transport、远端文件名/权限/原子写、目录创建、删除、tombstone、orphan/quarantine 或 runtime composition。

### Requirements

- marker 文档必须包含唯一 schema/key version、安装身份、job/root identity、root revision、随机 ownership
  nonce、已持久化 marker binding 和独立 authentication tag。不得包含 raw root locator、workspace locator、
  credential、proof、source identity 或当前 cleanup owner。
- 安装身份从不可轮换的 `recovery_cleanup_ownership` domain key 通过独立 HMAC domain 派生，不新增
  setting、表、列、migration number、backfill 或 `000070`。同一 key 的 envelope rewrap 不改变身份；
  key 丢失/替换后旧 marker 必须关闭失败。
- ownership nonce 使用 CSPRNG 生成 32 bytes，并以 canonical unpadded base64url 编码。nonce 只存在于
  marker bytes 内，不得进入 DB、API、audit、log、metric、error、TargetPort result 或 frontend DTO。
- marker authentication 使用同一 installation-stable ownership key 和不同于 DB marker binding 的独立
  HMAC domain。codec 必须根据 permit、private workspace object 和 immutable marker creator/fence 重新计算
  现有 DB marker binding，不能把调用方给出的 64-hex 字符串直接签名为 owned marker。
- `CreateOwnedJobDirRequest` 与 cleanup validation authority 必须私有绑定 immutable marker creator/fence。
  takeover 继续使用原 creator provenance；current attempt/cleanup owner 不能替换它。
- decoder 只接受一个有界 JSON object，所有字段恰好一次。empty/oversized、unknown/duplicate/missing field、
  trailing JSON、unsupported version、noncanonical nonce、wrong key/installation/job/root/revision/creator/object/
  binding/tag 和 cross-resource substitution 全部关闭失败。
- permit/request mismatch 使用现有 closed target-permit product；invalid/tampered marker 与 key/entropy
  unavailable 使用稳定 sanitized codec errors。仅 `context.Canceled`/`context.DeadlineExceeded` 保留 identity；
  raw crypto/key/JSON/random error 不得外泄。

### Acceptance

- [x] 真实 RED 证明当前没有 marker codec、strict document、nonce、installation identity 和 creator-bound
  create/cleanup authority；fixture 或无关 compile failure 不计入 RED。
- [x] 同一 codec 创建的 marker 能在独立 cleanup permit 下验证；reserved takeover 保留 creator provenance，
  existing-marker validation 不生成或返回第二个 nonce。
- [x] 严格 ambiguity/tamper/key-substitution matrix 全部 fail closed，且 key/raw marker/nonce/private locator/
  creator 不出现在 JSON、errors 或 logs。
- [x] Worker 创建请求和 ResultLifecycle cleanup 请求/permit 精确携带同一 immutable creator/fence；现有
  `reserved -> marker_created` 和 `drained -> validated` durable transitions 保持不变。
- [x] focused、full Recovery normal/race、required real PostgreSQL、vet/lint/format/diff、Trellis、manifest、
  protected hash 和 staged-zero gates 通过。
- [x] 停在 codec + closed contract interoperability。不得选择 remote marker filename、打开 SSH/SFTP、
  写远端 marker、调用 `RemoveOwnedJobDir`、进入 `delete_started|deleted|tombstoned|cleaned`，也不得执行
  stage/commit/push/PR/CI/merge。

## Task 7 A2a Exact-Plan Regular-File Verification (2026-08-04)

用户已批准将 A2 按信任边界继续拆分，并先交付 A2a。A2a 只关闭 exact executed-plan observation
authority 与 concrete regular-file present `Verify`；它不是整个 A2，也不授权任何 mutation。

### Requirements

- `TargetVerifyPermit` 必须携带 package-private、不可结构化伪造的 proof。proof 精确绑定一个已执行且在
  durable load transaction 内锁定、经 model hook 解密的 immutable plan snapshot，以及当前 job、target
  mode、完整 observation permit 字段和 expiry。permit/proof/session/object 任一字段替换都必须在 node
  resolver、SSH 或 SFTP 之前返回 `ErrInvalidTargetPermit`。
- ordinary execution、delete pause/observation 与 restart adoption 的 durable handoff 必须在锁内生成同一
  exact-plan session binding；事务关闭后才可签发短时 verify permit 并执行 target I/O。A2a 不允许从当前
  target-root registry 重新解析 locator，也不允许从未锁定 plan 或调用方字段重建 authority。
- concrete adapter 只为 present regular-file `Verify` 打开 `recovery_verify` SSH/SFTP session，credential
  resolver 必须精确重验 plan snapshot 中的 node revision 与 credential revision，credential audit correlation
  只使用 safe job ID。preflight、write、result-read 或 cleanup purpose 不得替代 verify purpose。
- target object 必须是 permit proof 所绑定 root 下的 exact canonical POSIX relative object。adapter 在读取前后
  重验 absolute root 的每个 prefix、全部 parent directory 和 final object；任何 missing、alias、symlink、
  non-directory parent、non-regular final object、canonical drift 或可见 replacement 都关闭失败。isolated mode
  还必须把 object 限制在 exact `jobs/<jobID>/...` namespace，并保持既有 private `0700` workspace parent
  contract；in-place parent mode 不被当作 fidelity。
- present Verify 仅声明 Task 6 已冻结的 exact SHA-256 content identity + byte count。实现必须流式读取，不按
  文件大小分配内存；最多消费 expected bytes 加一字节以证明 exact EOF，并比较 pre-open、opened 与
  post-read 的 bounded stat facts。digest、bytes 或可见 stat/path drift 返回 target changed；不得声明 mtime、
  mode、owner、inode、device、hardlink、MIME 或其他 metadata fidelity。
- unchanged regular content 返回 closed present observation 和不超过 64 字节、非 SHA-256-shaped 的
  `ObservedRevision`。revision 使用独立 domain 绑定 node/root/root revision、exact private relative locator、
  literal regular kind、content digest 与 byte count；它是稳定 target observation token，不是 target-chain
  revision、inode identity 或额外 fidelity product。后续 `WriteAtomic` 必须复用同一公式，但不属于 A2a。
- invalid/mismatched authority、object 或 expectation 返回 `ErrInvalidTargetPermit`；target shape/content/drift
  返回 `ErrRecoveryTargetChanged`；resolver、SSH/SFTP、stat/open/read/close 或 ambiguous transport failure 返回
  `ErrRecoveryTargetUnavailable`；调用方 cancellation/deadline 保留原 identity。所有错误必须 sanitized，
  不得包含 node、host、credential、root/object locator、SFTP status、content、digest input 或 raw dependency error。
- A2a 的 concrete `Verify` 对 valid absent expectation 仍在任何 session 前返回 closed unavailable。
  `ProbeRoot`、`Lstat`、`CreateDirectory`、`WriteAtomic`、`Delete`、`OpenOwnedResult` 和
  `RemoveOwnedJobDir` 继续零 session、closed-unavailable。A2b--A2e、A3、runtime/main composition、
  orphan/quarantine 和全部 Git delivery action 都不属于 A2a。

### Acceptance

- [x] genuine RED 证明结构化 verify permit、proof/plan/job/mode/expiry/object substitution 以及非锁定 authority
  不能打开 resolver/SSH/SFTP；GREEN 后所有四个现有 verify-permit issuance flow 都只使用 locked exact-plan
  handoff。
- [x] exact `recovery_verify` purpose、node/credential revision、safe job correlation、open/close/cancellation ordering
  通过 focused tests；wrong purpose/revision、resolver/dial/SFTP/close failure 均 sanitized 且不泄露 locator 或
  credential。
- [x] regular-file matrix 覆盖 zero-byte、bounded streaming、short/extra bytes、digest mismatch、missing、parent/
  final symlink、directory/special final、canonical alias、pre/open/post replacement 与 isolated namespace/mode
  drift；只有 exact digest + bytes 成功。
- [x] successful observation 使用 `sftp1:` 加 SHA-256 raw-url-base64 token，domain 与字段顺序完全冻结，长度
  不超过 64 且不为 SHA-256-shaped；相同对象内容稳定，不同 root/path/content/bytes 不相等。
- [x] absent Verify 与七个 deferred concrete method 全部在 session 前保持 unavailable；A1 已交付的
  `CreateOwnedJobDir`/`ValidateOwnedJobDir` 行为保持不变；无 remote mutation、marker change、registry
  lookup、DB/schema/runtime/API/frontend 或新 path。
- [x] focused normal/race、whole Recovery normal/race、required-real-PostgreSQL existing behavior、vet/backend lint、
  owned format/diff、privacy/static、Trellis/JSON/JSONL、exact 145-path manifest、protected hash 和 staged-zero gates
  通过；只将 A2a 标记为 focused complete，不外推 A2/Task 7/Child 13 完成。

## Task 7 A2b Approved Product Direction (2026-08-04)

用户批准方案 A 作为 A2b 的产品边界：现有 exact selection 继续只持久化常规文件叶子，不把目录或目录
metadata 扩展为新的恢复 operation，也不恢复空目录。`isolated` 恢复可以从已锁定常规文件的 exact final
locator 派生并逐级创建缺失父目录，所有新建父目录固定为 `0700`，既存目录只验证、不 chmod 或修复。
`in_place` 恢复不得发明目录权限：全部父目录必须已存在、为 canonical real directory；任一缺失、alias、
symlink 或非目录父组件均关闭失败，且不创建父目录。

A2b 的常规文件 create 必须保持 no-overwrite。最终目标只要已经存在，无论类型、内容或 digest 是否与
source 相同，都不得 adopted、truncated、repaired 或视为幂等成功。A2b 不实现 overwrite、absent/Lstat、
preflight、result read、delete、cleanup、empty-directory fidelity 或任何 metadata fidelity；这些边界继续按
既有 A2c--A3 拆分处理。具体 capability、原子写协议、错误分类与 TDD/验证矩阵仍需设计批准后才冻结。

用户随后批准 A2b 的 capability、directory 与 no-overwrite write protocol。item create authority 必须从已
锁定的 executed-plan handoff 签发，私有绑定 exact session snapshot、job/mode、最终 object、literal
`create`、expected-absent、预期 digest/bytes 与当前 target-chain revision；当前 item permit 重封时丢失
session binding 的缺口必须关闭。`WriteAtomic` 是 A2b 唯一打开的 payload mutation arm，且只接受上述
regular-file create proof；overwrite 与独立 public `CreateDirectory` concrete arm 继续在 session 前关闭。

`isolated` 父目录创建是 `WriteAtomic` 同一 `recovery_write` session 内的私有阶段：只从 exact final object
派生，逐组件创建、chmod 并验证为 `0700`。`in_place` 只验证既存 canonical real parents。文件使用同目录
private random temp、`O_WRONLY|O_CREATE|O_EXCL` 与固定 `0600`，完成 exact bounded stream/hash/EOF、
`Sync`、close、reopen verification、authority/parents/final-absence revalidation 后，只能通过 standard SFTP
`Rename` 发布；`PosixRename`、truncate、existing-final adoption 和 chmod repair 全部禁止。成功 result 必须
复用 A2a 的 regular-file `sftp1:` observation formula，使紧随其后的 concrete Verify 返回同一 revision。

### A2b Requirements

- item write permit 必须携带 package-private exact-item proof；proof 绑定 locked handoff session snapshot、job、
  target mode、operation/expected-prior、exact final object、expected post digest/bytes 和完整 mutation permit。
  valid exact overwrite proof 在 A2b 仍 closed-unavailable；伪造或 substituted proof 返回 invalid permit。
- `WriteAtomic` 必须在任何 resolver/session 前验证 context、依赖、permit/proof/request/content shape 和 exact
  request-to-proof parity。target I/O 只发生在 durable load transaction 关闭之后，且只使用 exact
  `recovery_write` purpose、node/credential revisions 与 safe job-ID correlation。
- `isolated` 只允许 exact `jobs/<jobID>/<item...>` object。既有 `jobs`、job 与更深 parent 都必须为 canonical
  real `0700` directory；缺失的更深 parent 才可逐组件创建、chmod 并验证为 `0700`。`in_place` 的全部
  parent 必须既存、canonical、real directory，但 mode 不构成 fidelity。
- final object 在 temp open 前和 rename 前都必须 exact absent。任何既存 regular/directory/symlink/special，
  包括相同 digest/bytes 的 regular file，都返回 target changed，且不得 adopted、truncated、chmodded 或删除。
- temp basename 使用固定 private prefix 加 32-byte CSPRNG 的 canonical unpadded base64url；entropy 必须在首个
  remote mutation 前成功。temp 必须与 final 同目录，以 `O_WRONLY|O_CREATE|O_EXCL` 创建并固定 `0600`。
- content 必须 bounded streaming 到 expected bytes，同时计算 SHA-256，再读取至多一字节证明 exact EOF；
  short/extra/digest drift 均不得 rename。temp 必须 `Sync`、close、reopen 并按相同 digest/bytes 完整验证。
- final publish 只能使用 standard SFTP v3 `Rename`；禁止 `PosixRename` 或 fallback。rename 后必须完整验证
  canonical final、`0600`、digest/bytes、permit currentness 和 exact A2a `sftp1:` revision，session close 成功后
  才可返回 closed `TargetWriteResult`。
- 失败只 best-effort 删除当前调用以 `O_EXCL` 确认拥有的 exact temp；不得删除/回滚 parent 或 final。任何
  call error/invalid product/close ambiguity 继续进入现有 unresolved disposition，不推进 chain 或 blind retry。
- errors、JSON、audit、log 和 captured session products 不得包含 raw dependency error、node/host/username/
  credential、root/object locator、content、temp name/nonce、SFTP status 或 digest input；context identity 优先。

### A2b Acceptance

- [x] genuine RED 证明当前 ordinary item permit 丢失 locked-plan session binding，且 proof/session/job/mode/
  operation/prior/object/post-facts/expiry substitution 在 resolver/session 前关闭；GREEN 后仅 locked handoff issuer
  产生 concrete adapter 接受的 exact item permit。
- [x] isolated nested parent matrix 证明 only-missing create、`0700`、component order、lost mkdir race revalidation、
  no repair；in-place missing/alias/symlink/non-directory parent 全部零 mutation。
- [x] exact create protocol 证明 CSPRNG-before-mutation、same-directory private temp、exclusive flags、`0600`、
  bounded zero/ordinary/short/extra stream、digest、Sync、close/reopen、revalidation、standard no-overwrite Rename。
- [x] existing-final 和 concurrent-final matrix 对相同/不同 regular、directory、symlink、special 全部不覆盖、不采用；
  ambiguous rename/close 不返回成功，最多清理当前 exact owned temp。
- [x] valid final result 的 bytes/digest/revision 与随后 concrete present Verify 完全相同；invalid/call-error result
  只进入既有 unresolved terminal disposition且不推进 target chain。
- [x] cancellation/resource/privacy matrix 保留 context identity、at-most-once close、zero raw/private leakage；valid
  overwrite 与 `CreateDirectory`/Probe/Lstat/result-read/Delete/RemoveOwnedJobDir 继续零 session unavailable。
- [x] focused normal/race、A1+A2a+A2b combined、whole Recovery normal/race、required-real-PostgreSQL no-skip、vet、
  backend lint、owned format/diff/static/privacy、Trellis/JSON/JSONL、exact 145-path manifest、protected hashes 与
  staged-zero 全部通过；只将 A2b 标记 focused complete，不外推 A2/Task 7/Child 13 完成。

## Task 7 A2c Approved Split Preflight Direction (2026-08-04)

用户批准方案 A：A2c 不允许 concrete SFTP adapter 回显或硬编码 Provider source access、capability、policy、
security finding 或 protected-root overlap。A2c 按 evidence ownership 拆为 A2c1 与 A2c2；A2c1 先关闭
draft-plan authority 和 target-owned read-only observation，A2c2 再组合独立 source/policy evidence 并完成
durable preflight persistence。两个切片都不得把 planning approval 记作 RED/GREEN 或完成证据。

### A2c1 Requirements

- `TargetPreflightPermit` 必须由 package-private proof 封装，不再接受只有 public fields 的结构化 authority。
  proof 绑定一次数据库读取得到的 exact `draft` plan、plan binding/transition revision、target mode、node/
  credential/root/path/filesystem/target revisions、private root/relative locator、完整 `TargetProbeRequest` 与 expiry。
  任一替换或非 draft plan 必须在 resolver/SSH/SFTP/command 前返回 `ErrInvalidTargetPermit`。
- `PreflightService` 只可在确认 requester、plan ID、expected transition revision 和 caller input 与 observed plan
  完全相等后私有签发 proof；target I/O 在数据库 transaction 外完成。后续 transaction 仍重新锁定 plan、重验
  source 与完整 commit product，不得跨 target I/O 持有 transaction。caller-facing `TargetPreflightInput.Permit`
  继续只是结构化请求；service 仅在 canonical local copy 的 unexported `targetPermit` 中保存 sealed capability，
  evaluator 必须验证两者完全一致。caller 不得提交或覆盖 sealed capability。
- concrete probe 只使用 exact `recovery_preflight` purpose 和 safe plan-ID correlation。preflight session 使用独立
  draft binding/open path，不得复用 executed-plan Verify/Write authority，也不得使一般 session opener 接受任意 purpose。
- target adapter 只返回 `TargetRootProbeFacts`：target 端 observed/expiry、credential revision、root/filesystem/
  target observation revision、fixed-tool availability、canonical/real/symlink/device/mount/owner/mode checks、free
  bytes/inodes 与 target existence。source/capability/policy/finding/overlap/reserve facts 不属于该 product。
- 为使 A2c1 在上述 return-type split 后仍可独立编译并保留现有 evaluator 行为，A2c1 同时冻结
  `PreflightExternalEvidencePort` 的 closed scalar request、external facts 和 package-private proof 形状；
  `TargetPreflightEvaluator` 必须同时要求 target port 与 external-evidence port。A2c1 只允许同 package `_test.go`
  中的 deterministic issuer/fake 提供已证明 evidence；production 不得有 external issuer/adapter、Provider/
  Repository evidence lookup、成功默认值或 request echo。nil、unproved 或 substituted external evidence 必须 fail
  closed。真实 recovery-owned issuer/adapter 仍属于需单独批准的 A2c2。
- root/path probe 使用 SFTP `Lstat`/`RealPath`/`StatVFS` 和共享 bounded `sshutil.CommandRunner` 的固定 `id -u`/
  `id -G`；不得暴露 generic command、接受 caller binary/argv 或执行 mutation。root 必须是 canonical real
  directory、非 world-writable，且 POSIX owner/group bits 必须证明当前 SSH principal 对目录有 write+execute。
- 每个 existing root/target prefix 在 observation 前后重验；symlink、alias、non-directory parent、filesystem ID
  变化或可见 replacement 形成 closed negative facts/conflict，permission/transport/ambiguous errors 为 sanitized
  unavailable。`StatVFS.Bavail*Frsize` 与 `Favail` 使用 overflow-safe int64 转换。
- root/filesystem/target revisions 分别使用 `sftpr1:`、`sftpf1:`、`sftpt1:` 加 raw-url-base64 SHA-256，domain 为
  `xirang/recovery/sftp-{root,filesystem,target}-observation/v1`。stable identity fields 不含 free counters；target
  token 显式绑定 absent 或 exact observed kind/size/mode/uid/gid/mtime，属于 drift evidence 而非 metadata fidelity。
- cancellation/deadline 保留原 identity；其他 resolver/dial/SFTP/command/stat/close 错误统一 sanitized unavailable。
  error/JSON/audit/log/captured output 不得含 host、username、credential、root/object locator、UID/GID list、raw stat/
  command output 或 dependency error。整个 A2c1 为零 remote mutation。

### A2c1 Acceptance

- [x] genuine RED 证明当前 unsealed preflight permit 可构造且 concrete method closed；GREEN 后仅 exact observed
  draft plan issuer 可打开 resolver，plan/state/revision/root/path/request/expiry substitution 全部零 session。
- [x] exact purpose、node/credential revision、safe plan correlation、transaction-free I/O 与 session/command/SFTP
  at-most-once close/cancellation ordering通过 focused normal/race tests。
- [x] root/path matrix 覆盖 canonical real directory、prefix symlink/alias/non-directory/replacement、world-writable、
  owner/group permission、different filesystem、StatVFS unavailable/overflow、zero/ordinary capacity 和 target
  absent/regular/directory/symlink/special observations，且没有 remote mutation call。
- [x] 三个 observation token 的 domain/field order、稳定性、差异性、长度和 non-SHA-shaped contract 冻结；任何
  private target/identity/command material 均不经 JSON/error/log 泄漏。
- [x] target/external facts 在类型上分离，evaluator 缺少任一 port 或收到无 private proof/错 request binding 的
  external evidence 均 fail closed；现有 reason/snapshot tests 只通过 `_test.go` deterministic issuer/fake 保持
  可构建，不得出现 production issuer、compatibility echo 或隐式 success facts。
- [x] focused normal/race、whole Recovery normal/race、required PostgreSQL existing behavior、vet/backend lint、owned
  format/diff、Trellis/JSON/JSONL、exact manifest、protected hashes、branch/HEAD 和 staged-zero 通过；只将 A2c1
  标记 focused complete，不外推 A2c/Task 7/Child 13 完成。

### A2c2 Requirements And Acceptance

- A2c2 在 A2c1 已冻结的 `PreflightExternalEvidencePort` 与 proof contract 上实现 recovery-owned production
  issuer/adapter。它只接收 plan/source/capability/policy/target-observation 的 closed scalar bindings，从现有
  Provider/Repository authority 取得 source access、capability/policy/finding revisions/disposition、protected/
  source-root overlap 与 reserve bytes/inodes，并签发 bounded private proof；不得把 raw Provider/root locator、
  command output 或 credentials 交给 target，也不得复用 A2c1 test issuer。
- evaluator 组合 `TargetRootProbeFacts` 与 external facts，拒绝 observed/expiry/binding drift，重建现有 eligible/
  reasons/snapshot product。source 与 target I/O 均在 transaction 外；commit transaction 重锁并逐字段重验后才
  插入 encrypted operation snapshot 和 `draft -> preflight_ready` CAS。
- [x] A2c2 必须有独立 genuine RED/GREEN、focused review 与批准；A2c1 完成不得自动授权 A2c2 implementation。
- [x] A2c2 结束后仍停止在 A2d result read、A2e overwrite/Lstat/absence、A3 destructive cleanup/delete、runtime/
  main、orphan/quarantine 和全部 Git delivery 之前。

### A2d Requirements And Acceptance

A2d 只打开已发布 isolated regular-file result 的 concrete SFTP 顺序读取。现有 Content ticket/grant/budget/
revoke/drain 与 durable `RecoveryResultResolver` 保持唯一外层入口；stat-only 请求继续不打开 target session，Range
继续关闭。A2d 不新增 API、公开 locator、model、table、migration、setting、dependency 或 manifest path。

- resolver 只有在精确 owner、terminal job、published workspace、ready ResultSet、zero cleanup fence、无 active
  attempt、有效 marker validation、未过期 effective deadline 与 executed plan 全部成立时，才产生 unexported
  result-read authority。authority 必须绑定 exact session snapshot、job/result-set/result、publication revision、
  cleanup fence、workspace marker binding/creator/fence、result object/locator digest、size/content digest 与 plaintext
  deadline；调用方复制 public DTO、结构化 `TargetObservationPermit` 或替换任一 request field 均不能签发权限。
- `NewTargetResultReadPermit` 只接受 recovery-owned issuer 密封的 private proof。concrete target 在任何 resolver/
  SSH/SFTP I/O 前重验 exact request/proof/expiry，并且只打开 `recovery_result_read` purpose；write、verify、preflight
  或 cleanup session 不得替代它，wrong node/credential revision 必须在 SFTP 前关闭失败。
- target 只允许 `jobs/<jobID>/...` 下 proof 绑定的 exact canonical POSIX relative regular file。每次打开先重验
  root、private `jobs`/job directory、authenticated ownership marker 与所有 parent；随后执行 bounded full-file
  SHA-256/byte-count scan，并在 unchanged file snapshot 上重新打开 delivery handle。不得把整文件缓存在内存，
  不得接受 symlink/alias/non-regular/size/digest/marker drift。
- delivery reader 只顺序读取 proof 绑定的字节数并独立累计 SHA-256。读满或零字节 close 时必须确认 exact EOF、
  digest、file snapshot、canonical parents、marker 与 live permit 仍一致；partial consumer close 可以只释放资源，
  但不能把未验证结果升级为成功。file、SFTP、SSH 按该顺序各至多关闭一次，任何 close ambiguity 阻止成功。
- adapter 在 target open 后、向 Content 返回 source 前继续用 durable resolver 重验 publication/fence/deadline；
  Content 的既有 active-read cancellation/drain 负责关闭 revocation race。context cancellation/deadline 保留 identity；
  forged authority 为 `ErrInvalidTargetPermit`，可见 marker/path/file drift 为 closed target-change，其他 resolver/
  SSH/SFTP/read/stat/close ambiguity 为 sanitized unavailable。任何错误、JSON、audit 或 log 都不得包含 locator、
  marker、credential、host/user、content、digest input、SFTP status 或 raw dependency error。

### A2d Acceptance

- [x] genuine RED 证明 structural result-read permit 当前可构造且 concrete method closed；GREEN 后只有 resolver-bound
  exact published authority 能打开 session，owner/publication/fence/deadline/marker/object/size/digest substitution 均
  零 session。
- [x] purpose-exact session 覆盖 resolver/dial/SFTP failure、node/credential drift、cancellation 与 close ordering；
  correlation 只使用 safe job ID，所有非 result-read purpose 均不能替代。
- [x] concrete target 覆盖 valid/zero-byte/partial-close、marker tamper、root/parent/final alias/symlink/type/size/content/
  snapshot drift、short/extra/read/stat/open/EOF/close failure，并证明无 mutation call、无全文件内存缓冲。
- [x] delivery adapter 的 stat-only zero-session、sequential exact source、post-open durable drift close、retain deadline、
  Content revoke/drain compatibility 和 private-data scan 通过。
- [x] focused normal/race、whole Recovery normal/race、required PostgreSQL normal/race、vet/backend lint、owned format/
  diff、Trellis/JSON/JSONL、exact manifest、protected hashes、branch/HEAD 和 staged-zero 全部通过；只将 A2d 标记
  focused complete，不外推 A2/Task 7/Child 13 完成，并停止在 A2e/A3/runtime/main/Git delivery 前。

## Task 7 A2e Planning Re-entry (2026-08-05)

A2d focused closure 后重新检查 A2e，确认既有批准只冻结了“A2e 负责 overwrite 以及 delete-oriented
Lstat/absence observation”的边界，并未冻结这两类能力的完整协议。A2e 在具体设计和用户批准前仍未开始，
不得把本节的规划事实记作 RED、GREEN 或完成证据。

已由当前代码、测试和任务历史确认：

- 两个 delete 调用点已经从 locked executed-plan handoff 获得 sealed `TargetVerifyPermit`，但 concrete
  `Lstat` 仍在任何 session 前返回 unavailable。`TargetLstatResult` 的现有 closed shape 是
  `kind + identity_digest + target_revision`，missing 不得携带 synthetic identity digest。
- A2a 已冻结 regular-file content SHA-256/bytes 与 `sftp1:` observation；A2c 已冻结 metadata-only
  `sftpt1:` absent/present revision。A2e 必须复用这些既有 domain，只有 directory/symlink/special 的
  exact prior identity 仍缺少统一公式。
- A2b 已从 locked handoff 密封 create/overwrite item proof，但 concrete `WriteAtomic` 只接受 create +
  expected-absent。现有 SFTP facade 只有 standard `Rename`，没有 compare-and-swap、exchange rename 或
  target-side lock primitive。
- 对已经存在的 final 执行“重新验证后直接 replace rename”不能原子证明被替换的仍是 exact prior；
  外部写入若发生在最后一次验证和 rename 之间会被静默覆盖。该风险与现有 fail-closed target-drift
  contract 冲突，不能仅靠增加一次 `Lstat` 消除。
- 将 final 先 no-overwrite rename 到 execution-owned sidecar、验证捕获的 prior、再把已验证 temp
  no-overwrite rename 到 final，可以避免静默覆盖窗口内的外部对象，但引入短暂 final absence、崩溃恢复、
  sidecar ownership 和 orphan/quarantine 语义；这些语义当前尚未冻结，且与 A3 有依赖。

因此默认建议把 A2e 拆成两个独立可验证切片：A2e1 先交付 sealed delete-oriented Lstat 与 exact absence
observation，保持零 mutation；A2e2 仅在 overwrite 的并发和失败恢复策略另行批准后开始。A2e1 完成不自动
批准或完成 A2e2，也不打开 A3 `Delete`/`RemoveOwnedJobDir`、runtime/main、orphan/quarantine 或 Git delivery。

当前唯一阻塞产品决策是：A2e2 是否接受“捕获 prior 到私有 sidecar 后再发布”的 fail-closed 协议及其短暂
absence/恢复成本；或者保持 overwrite closed，直到引入更强的 target-side CAS/exchange 能力。直接 replace
rename 虽然简单且 final 始终有名字，但不满足 exact-prior 并发安全，默认不推荐。

## Task 7 A2e1 Approved Delete Observation Direction (2026-08-05)

用户批准将 A2e 拆为 A2e1/A2e2。A2e1 只打开 sealed delete-oriented `Lstat` 和 exact absent `Verify`，
全程零 remote mutation；A2e2 overwrite 继续关闭，直到并发、sidecar ownership 和 crash-recovery 协议另行
评审批准。A2e1 的实现批准仍须在本节、design 43 和对应 implementation plan 完成书面复核后单独给出。
用户随后批准了该实现边界；V12 评审发现并修复 operation/prior 未进入私有 proof 的权限缺口后，本节只记录
A2e1 focused closure，不改变 A2e2 或 A3 的关闭状态。

### A2e1 Requirements

- 复用 locked executed-plan handoff 已签发的 sealed `TargetVerifyPermit` 及其 exact `recovery_verify` session；
  不增加第二类 delete authority，不允许 public DTO 或结构化 permit 自行打开 resolver/SSH/SFTP。
- concrete `Lstat` 只接受该 sealed permit 和 exact object。present 必须区分 `regular`、`directory`、
  `symlink`、`special`；missing 的 `IdentityDigest` 必须为空，且只返回 exact A2c `sftpt1:` target revision。
- present `IdentityDigest` 使用 length-framed SHA-256 domain
  `xirang/recovery/sftp-delete-entry-identity/v1`，按固定顺序绑定 root revision、private relative locator、kind、
  size、完整 mode、UID/GID、Unix-second mtime 和 kind-specific payload fact。regular payload fact 为 bounded
  full-content SHA-256；symlink 为 exact `ReadLink` bytes；directory/special 为空。digest 不得暴露任一输入。
- `TargetRevision` 对 absent/present 都逐字段复用 A2c 已冻结的 `sftpt1:` 公式和字段顺序，不引入第二种
  metadata revision。identity 与 revision 用途不同：前者证明可删除 prior 的 exact entry，后者证明 target
  observation parity。
- 每个成功结果必须来自同一 purpose-exact session 内两次相同的完整 observation。regular 的 bounded content
  read、symlink 的 `ReadLink` 以及 surrounding `Lstat` 必须在两次 observation 中稳定；任何可见变化返回
  target changed。只有两次完整 observation 都得到 exact not-exist 才是 absence；permission、unsupported、
  transport、timeout、close 或其他 ambiguous error 永远不得降级为 absence。
- sealed permit 下 `Verify(ExpectedPresent=false)` 使用相同的 observation path。exact missing 时返回
  `AbsentObservation{Evidence: exact}`，其 target revision 必须与同对象 missing `Lstat` 完全相同；present 或
  两次 observation 不一致均 closed failure，不得返回 synthetic absence。
- cancellation/deadline 保留 context identity；forged/substituted authority 返回 `ErrInvalidTargetPermit`；
  visible drift 返回 target changed；其他 resolver/SSH/SFTP/read/stat/readlink/close ambiguity 统一 sanitized
  unavailable。error、JSON、audit、log 和 captured products 不得泄漏 host/user/credential/root/object locator、
  content、link target、UID/GID、digest input、SFTP status 或 raw dependency error。
- A2e1 不打开 valid overwrite、`CreateDirectory`、`Delete`、`RemoveOwnedJobDir`、A3、runtime/main、
  orphan/quarantine 或任何 Git delivery；不新增 public route、model、migration、setting、dependency 或 manifest
  path。

### A2e1 Acceptance

- [x] genuine RED 证明 sealed delete permit 当前仍无法打开 concrete `Lstat`，并冻结 exact permit/object/session/
  expiry substitution 全部在 dependency 前关闭；GREEN 后只允许 exact `recovery_verify` session。
- [x] present matrix 覆盖 ordinary/zero-byte regular、directory、symlink 和 special；逐项冻结 identity domain、
  length framing、字段顺序、payload fact、`sftpt1:` parity、稳定性与差异性，并证明没有 mutation call。
- [x] absence matrix 覆盖两次 exact not-exist、first/second observation 出现/消失、permission/unsupported/
  transport ambiguity；missing `Lstat` 与 absent `Verify` 返回完全相同 revision，missing identity 始终为空。
- [x] drift/cancellation/resource/privacy matrix 覆盖 regular content、symlink target、metadata、root/parent/final、
  read/stat/readlink 和 SFTP/SSH close；保持 context identity、at-most-once close、sanitized errors 和 zero private
  leakage。
- [x] focused normal/race、A1--A2e1 combined、whole Recovery normal/race、required-real-PostgreSQL no-skip、vet、
  backend lint、owned format/diff/static/privacy、Trellis/JSON/JSONL、exact 145-path manifest、protected hashes、
  branch/HEAD 和 staged-zero 全部通过；只将 A2e1 标记 focused complete，不外推 A2e、Task 7 或 Child 13 完成。
- [x] V12 后停止；A2e2 必须保持 closed，直到其并发与 crash-recovery 协议另行书面批准。

## Task 7 A2e2 + A3a Approved Overwrite Sidecar Protocol (2026-08-05)

用户批准方案 A：A2e2 使用同目录、no-overwrite、认证绑定的私有 sidecar 协议，并同时打开最小 A3a
successful-overwrite residue reconciliation。该批准接受在捕获后的 exact prior 验证期间 final 暂时不存在；
absence 按 expected prior bytes、调用 context/deadline 和 SSH/SFTP 吞吐有界，但不承诺固定墙钟时长。该批准
不等于产品代码已经开始或完成；本节、design 44 和对应 implementation plan 书面复核后仍需明确实现批准。

### A2e2 + A3a Requirements

- concrete `WriteAtomic` 只对 sealed exact-plan `overwrite + ExpectedTargetPresent` 打开该协议，并要求 literal
  in-place mode。create 继续使用 A2b no-overwrite 路径；isolated overwrite、结构化 permit、复制的 public
  fields、wrong item/operation/prior/post/object/key version 或过期 authority 必须在 entropy/resolver/SSH/SFTP
  前关闭。worker issuer 必须从 locked immutable job item、operation digest、target-locator historical cleanup
  key 和当前 attempt/node/source fences 重建全部私有 binding。
- 同目录 artifact component 使用 cleanup-ownership historical key 上的 HMAC-SHA-256 token，只暴露固定前缀、
  token 和 closed suffix：`intent`、`prior`、`post`、`published`。token 必须绑定 job/item/operation、plan/root/
  object、exact prior/post facts 和 key version；不得包含 raw job/item ID、target locator、host/user/credential 或
  digest input。所有 artifact 必须与 final 位于同一 canonical parent，禁止跨目录 rename、symlink/alias parent、
  caller-supplied artifact path 和 overwrite-style rename。
- `intent`/`published` 是 bounded deterministic authenticated documents。document 只携带 schema/key version、
  closed phase、private binding digest 和 authentication tag；不得携带 raw locator、content 或标识符。`post`
  以 `O_CREATE|O_EXCL`、0600、bounded exact bytes/digest/EOF、Sync、close/reopen verification 创建；合法重试只能
  复用 exact authenticated marker 与 exact post，不得采纳不匹配或无法认证的现有 artifact。
- mutation 顺序固定为：验证 exact prior 和 canonical parents；准备并验证 post；no-overwrite capture
  `final -> prior`；在 final absent 时重新完整验证 captured prior；再次 revalidate live permit；no-overwrite
  publish `post -> final`；验证 exact post；创建并验证 authenticated `published` marker；删除已证明 owned 的
  prior/post/intent；返回 exact A2a `sftp1:` post revision。直接 replace rename、PosixRename、额外 Lstat 后覆盖
  或先发布再验证 prior 均禁止。
- captured prior mismatch 只有在当前无歧义 session 刚刚成功完成 `final -> prior` 并立即观察到 winner 不匹配
  时，才可在 final exact absent 下 no-overwrite restore `prior -> final`，并验证 restored object 与本次 captured
  observation 相同。re-entry 时发现 mismatched prior、capture result ambiguous、final 被外部对象占用、restore
  ambiguous、artifact 漂移或状态不能唯一解释，必须保留用户对象和全部恢复证据，返回 closed unresolved/
  needs-attention，不得自动覆盖或删除。
- remote state machine 必须可重入并覆盖每个 crash boundary：fresh、intent、post prepared、prior captured、post
  published、published acknowledged、prior restored 和 externally conflicted。只有 exact authenticated state 可以
  前进；`final=post + published=exact` 可以在进程重启后重放 success，`final=absent + prior=exact` 可以继续发布或
  安全 restore，其他组合全部 fail closed。dependency error 或 ambiguous rename 不得仅凭可见 final 推断成功。
- successful `WriteAtomic` 必须留下 exact `published` marker 作为 remote-before-DB crash proof，不得在 operation
  checkpoint 之前删除它。worker 先以既有 immutable operation checkpoint 持久化 item outcome/target revision，
  job 和 execution attempt/node/source leases 保持 active；随后从 locked checkpoint mint fresh private overwrite
  finalize proof。concrete target 在 purpose-exact `recovery_write` session 中再次验证 final exact post、canonical
  parent、artifact exact absence/published marker 和 live proof，只删除该 marker，并证明 exact absence。
- overwrite finalize 在 checkpoint 后、job completion 前执行；crash after checkpoint、before/after marker remove
  必须由 takeover idempotently 重放。all completed overwrite checkpoints 都已 reconcile 后，才允许既有 job
  completion transaction close attempt、release source/node leases 并进入 terminal outcome。source post-revalidation
  drift/failure 也必须先 durable-project operation checkpoint、finalize owned marker，再使用既有 completed-operation
  failure projection；若在 checkpoint 后、failure projection 前崩溃，takeover 必须重新执行 durable source
  revalidation，不能从 checkpoint 推断 source matched。不得留下一个已 terminal 但仍需要 execution permit 才能
  清理的成功 marker。
- A3a 只拥有 successful overwrite `published` marker 的 checkpoint-bound reconciliation。external conflict、
  forged/tampered/unknown artifact、无法 restore 的 captured prior 和 terminal orphan/quarantine 仍由完整 A3 处理；
  A3a 不打开 `Delete`、`RemoveOwnedJobDir`、result/workspace tombstone、general orphan scan 或 cleanup scheduler。
- cancellation/deadline 保留 context identity；forged/substituted authority 为 `ErrInvalidTargetPermit`；visible
  target/artifact drift 为 target changed/unresolved；resolver/SSH/SFTP/read/write/sync/rename/remove/close ambiguity
  为 sanitized unavailable。任何 error、JSON、audit、log 或 captured product 都不得泄漏 private binding、token、
  artifact name、path、marker、content、digest input、SFTP status 或 raw dependency error。
- 本切片复用现有 model、paired `000069`、operation checkpoint、job item encrypted locator、cleanup-key version、
  target port 和 recovery_write purpose；不增加 table/column/checkpoint phase/migration number/public route/setting/
  dependency/manifest path。现有 `target.go`/`worker.go`/`executor.go` 及对应 tests 是唯一产品边界。

### A2e2 + A3a Acceptance

- [x] genuine RED 冻结 overwrite authority、historical-key artifact binding、closed filenames/documents 和全部
  substitution-before-dependency behavior；GREEN 后 create/isolated/cross-purpose 行为完全不扩宽。
- [x] post preparation、capture、captured-prior verification、publish、published acknowledgement 和 exact result
  覆盖 zero/ordinary/bounded-large files、short/extra/digest mismatch、collision、permission/type/parent drift 以及
  standard no-overwrite Rename，证明没有 replace rename 或静默覆盖窗口。
- [x] exhaustive state/crash matrix 在每个 remote call 前后重入；exact known state resume/restore，external/unknown
  state preserve evidence and fail closed。final absence 只存在于 capture 到 publish/restore 的 bounded interval。
- [x] durable checkpoint/finalize matrix 覆盖 crash before/after checkpoint、before/after marker remove、last-item job
  completion、takeover、source drift/failure 和 repeated cleanup；terminalization 永远晚于 successful marker absence。
- [x] normal/race focused gates、A1--A2e2 combined、whole Recovery normal/race、required-real-PostgreSQL normal/race
  no-skip、vet/backend lint、owned format/diff/static/privacy、Trellis/JSON/JSONL、exact unchanged 145-path manifest、
  protected hashes、branch/HEAD 和 staged-zero 全部通过；只记 A2e2+A3a focused closure，不外推 A3/Task 7/Child。
- [x] V13 后停止在完整 A3 `Delete`/`RemoveOwnedJobDir`/tombstone、terminal cleanup lease release、general orphan/
  quarantine、runtime/main、whole Task 7 review 和全部 Git delivery action 之前。

## Task 7 Full A3 Planning Re-entry (2026-08-05)

V13 后重新进入可重复规划，只收敛完整 A3，不撤销 A2e2+A3a 的 focused closure，也不开始产品实现。
仓库已确认以下事实：

- `recoverySFTPTarget.Delete` 与 `RemoveOwnedJobDir` 仍是 closed-unavailable；`ResultLifecycleService` 只推进到
  durable `validated`，尚无 `delete_started -> deleted -> tombstoned`、`cleaned|workspace_cleaned` 和成功终态
  cleanup node-lease release 方法。
- paired `000069` 与 closed `CleanupPhase` 已预留上述 one-way transitions、published/workspace tombstone shape、
  current-owner retryable release shape和 terminal tombstone immutability；完整 A3 应优先复用，不新增表、列、
  migration number 或公开 WIP state。
- 当前 SFTP facade 只有 path-based `Lstat`、standard no-overwrite `Rename` 和 `Remove`，没有原子
  compare-and-delete。直接 `Lstat(expected) -> Remove(final)` 存在误删观察后换入对象的竞态，不能成为安全
  exact-mirror delete 协议。
- exact-mirror delete row 的 closed display class 仍覆盖 regular、directory、link 和 special。directory 删除
  只能对 exact empty directory 使用非递归 remove；未经逐项冻结的递归内容不属于 delete row authority。
- A2e2 已证明同目录、private authenticated、standard no-overwrite capture 可以避免静默替换；完整 A3 可以
  复用其原则，但必须使用 delete-specific binding/state machine，不能把 overwrite proof 或 artifact 直接当作
  delete authority。
- Task 7 仍拥有一般 orphan/quarantine reconciliation 产品；Task 8 仍拥有 startup/listener ordering、managed
  scheduler 和 runtime/main composition。A3 规划不得借用 Task 8 完成度。

本轮提交用户决策的问题是：in-place exact-mirror `Delete` 是否采用 delete-specific 同目录认证 sidecar，
以 no-overwrite `final -> captured` 先取得 mutation-instant 对象，再验证 captured identity；只有 exact match 才
删除 captured，same-invocation mismatch 仅在 final exact absent 时 no-overwrite restore，re-entry/ambiguous/
external conflict 全部保留证据并进入 unresolved/quarantine。直接 path delete 与静默递归删除保持拒绝。

### Full A3 Delete Direction Approval (2026-08-05)

用户批准方案 A。完整 A3 的 in-place exact-mirror `Delete` 使用 delete-specific、同目录、认证绑定的
no-overwrite capture protocol；不使用 `Lstat -> Remove(final)`，也不继续永久关闭 concrete Delete。普通文件在
captured identity 复验期间允许原路径暂时缺失，该窗口受 captured observed size、调用 context/deadline 和
SSH/SFTP 吞吐约束，不承诺固定墙钟时长。directory 只允许删除已捕获并证明 exact empty 的目录；禁止把单个
delete row 扩大为未逐项冻结的递归删除。same-invocation mismatch 只在 final exact absent 时 no-overwrite
restore；re-entry、ambiguous dependency result、external winner、non-empty directory 或 unknown/forged artifact
全部保留对象/证据并 fail closed。该批准只冻结产品方向，不等于完整 A3 设计、书面计划或实现已经获批。

### Full A3 Orphan/Quarantine Planning Facts (2026-08-05)

- paired `000069` 的 `recovery_cleanup` node lease 必须绑定一个真实 Recovery job；当前不存在 node/root-scoped
  orphan mutation lease，也不存在可保存 unknown remote locator 的 orphan row。`backup_asset_recovery_evidence`
  的 normal rows 同样必须绑定完整 job/plan/checkpoint/grant/attempt/source/node product。
- 因此 unknown/DB-unmatched/forged workspace 若做 physical rename/move，必须新增 node-level durable authority 与
  orphan persistence contract；借用任意 job cleanup lease 或只靠进程锁会越权并破坏现有 fence 语义。
- 不变更 schema 的 closed product 是 logical quarantine：只在 purpose-exact read-only root scan 中分类，保留
  remote object 不动，返回 bounded sanitized finding/fingerprint 并 audit/alert；后续 pass 可重新发现，Task 8
  负责 managed cadence，downgrade-readiness 可把仍存在的 finding 视为 blocker。

本轮提交用户决策的问题是 quarantine 的含义：采用上述 non-mutating logical quarantine，还是扩张为 physical
remote move 并承担新的 node-level lease、durable orphan registry 与 paired-schema 合同。

### Full A3 Orphan/Quarantine Direction Approval (2026-08-05)

用户批准方案 A。完整 A3 的 general orphan/quarantine reconciliation 使用 non-mutating logical quarantine：
purpose-exact read-only root scan 只能观察和分类 unknown、DB-unmatched 或 forged workspace，不得 rename、move、
remove 或借用任意 Recovery job cleanup lease；远端对象保持原位。每次发现只形成有界、脱敏且可稳定重发现的
finding/fingerprint，并进入 audit/alert；不得持久化或输出 remote locator、认证 artifact、原始依赖错误或其他私密
绑定。Task 8 负责 managed cadence/runtime composition；Task 7 只交付可显式调用、可重入的 reconciliation
产品及其关闭语义。只要某一 finding 仍可被当前扫描确认，downgrade-readiness 必须把它视为 blocker；后续 pass
在对象消失、获得合法归属或已能由正常 Recovery 清理合同解释后，才可不再报告该 finding。

该批准拒绝在 Task 7 内扩张 physical quarantine 所需的 node/root-scoped mutation lease、durable orphan registry
和 paired-schema 合同；也不把 read-only scan 误记为远端对象已经隔离或清理。该批准只冻结产品方向，不等于完整
A3 架构、错误合同、测试矩阵、书面实施计划或产品实现已经获批。

### Full A3 Owned Workspace Removal Planning Facts (2026-08-05)

- `RemoveOwnedJobDirRequest` 当前只绑定 exact workspace object 与 marker binding；现有 SFTP facade 没有 `ReadDir`
  或递归删除能力，`Remove` 对非空目录会失败。因此 concrete `RemoveOwnedJobDir` 不能靠一次 path remove 完成。
- exact authenticated `0700` job-directory marker 证明该 workspace namespace 由对应 Recovery job 创建和拥有，但不
  逐项冻结所有 descendant identity。成功、失败或崩溃后的 workspace 都可能包含 nested directories、payload、
  marker 以及协议产生的临时/恢复证据。
- 若只准删除数据库逐项声明的 exact paths，任何未持久化 temp/residue 或额外 descendant 都会阻断目录删除并可能
  让受限 plaintext 无限期滞留；若把 authenticated workspace namespace 整体视为 cleanup authority，则可在先
  no-overwrite capture 整个 exact job directory 后，对 captured tree 做可重入、bounded、no-follow、same-filesystem
  清理，但必须把 symlink 当作叶节点，并对 mount/filesystem/canonical drift 保留证据、fail closed。
- known owned-workspace cleanup 与 general orphan quarantine 不同：前者已有 exact job、marker、cleanup/node fence
  和永久 use-latch authority；后者没有这些绑定，仍按已批准的 read-only logical quarantine 处理。

本轮提交用户决策的问题是 authenticated owned workspace 的授权粒度：cleanup authority 是否覆盖该 exact
job-directory namespace 内的全部 non-mount descendants（包括未在 DB 逐项列出的 residue），还是只覆盖 durable
manifest 能逐项证明的路径并让任一额外 descendant 阻断、进入 logical quarantine。

### Full A3 Owned Workspace Removal Direction Approval (2026-08-05)

用户批准方案 A。对已通过 exact job、authenticated marker、cleanup/node fences 与 permanent use latch 重验的
owned workspace，`remove_owned_job_dir` authority 覆盖该 exact job-directory namespace 内的全部 non-mount
descendants，包括未在数据库逐项列出的协议 temp/residue；不要求先建立 per-entry durable manifest。实现方向为先
使用 cleanup-specific、同父目录、authenticated、standard no-overwrite capture 取得 mutation-instant 整个 job
directory，再对 captured namespace 做 bounded、可重入、depth-first cleanup。symlink 只能作为叶节点 unlink；任何
filesystem/mount boundary、canonical/marker drift、ambiguous capture 或 external final winner 都保留对象/证据并
fail closed，不得递归越界。

该 namespace authority 只属于已证明 ownership 的 exact workspace，不授权删除 `jobs` shared directory、registered
root、兄弟 workspace 或 general orphan。unknown、DB-unmatched、forged 或 marker-invalid workspace 继续使用已批准
的 non-mutating logical quarantine。该批准只冻结授权粒度和总体删除方向，不等于 bounded-pass 状态、崩溃接管、
终态投影、错误合同、测试矩阵、书面实施计划或产品实现已经获批。

### Full A3 Delivery Structure Approval (2026-08-05)

用户批准方案 A，以独立可验证的 vertical slices 顺序完成完整 A3：

1. A3b concrete exact-mirror `Delete`：delete-specific authenticated artifact/state machine、worker/executor
   crash re-entry、durable operation checkpoint/finalization 与 focused stop；
2. A3c concrete `RemoveOwnedJobDir` + lifecycle terminalization：owned-directory capture、bounded resumable tree
   cleanup、`delete_started -> deleted -> tombstoned`、`cleaned|workspace_cleaned` 与成功 node-lease release；
3. A3d general logical orphan/quarantine reconciliation：purpose-exact read-only scan、bounded sanitized finding/
   fingerprint、audit/alert 与 downgrade-readiness blocker；managed cadence/runtime composition 保持 Task 8-owned；
4. V14 whole Task 7 review：cross-engine、race/crash、privacy/static、Trellis/scope 与完整规格一致性，只在所有前置
   切片完成后评价 Task 7 closure。

每个切片都必须有自己的 RED/GREEN、正常/race/required-real-PostgreSQL（适用时）验证、evidence ledger 和明确停止
点；前一切片完成后可基于证据继续细分后一切片，但不得把剩余 A3、Task 8 或 Git delivery 完成度提前计入。拒绝
把完整 A3 合并为一个不可控大批次，也拒绝先做跨协议通用 artifact/scan/delete 框架；只允许在不混淆 purpose、
binding 或 authority 的前提下复用低层 canonical/framing/resource helpers。该批准是架构设计输入，不等于任何
切片的书面技术设计或产品实现已经获批。

### Full A3 Architecture Boundary Approval (2026-08-05)

用户批准完整 A3 的架构与责任边界：A3b 的 execution/target vertical slice、A3c 的 target/lifecycle terminal
cleanup、A3d 的 read-only reconciliation product 和最终 V14 review 保持分离；`TargetPort` 继续作为远端
capability boundary，`ResultLifecycleService` 继续独占 cleanup phase/fence/lease 的数据库投影。A3c 的
`tombstoned`、`cleaned|workspace_cleaned` 与成功 cleanup node-lease release 必须同事务；A3d 不取得任何 mutation
authority。Task 8/9/10 与 Git delivery 边界保持不变。该批准允许继续逐节评审 data flow、error/takeover 和 test
design，但尚未批准写入最终 technical design 或开始产品实现。

### Full A3b Exact-Mirror Delete Data-Flow Approval (2026-08-05)

用户批准 A3b data flow。Delete 使用 delete-specific、同父目录、稳定跨 attempt 的 authenticated `intent`、
`captured` 与 `verified` artifacts；artifact binding 绑定 immutable plan/job/item/operation、consumed delete-authority
checkpoint、root/object、expected prior 与 historical key version，fresh live permit 另行绑定 current attempt/node/
source fences。standard no-overwrite capture 后，regular 完整复验 bytes/digest/metadata，symlink 只 `Lstat+ReadLink`，
directory 必须 exact empty，special 只处理 captured entry；只有 exact captured 才创建 `verified` 并允许删除。

Worker 顺序固定为 `delete_authority_consumed -> Target.Delete artifact reconciliation -> Verify exact absence ->
operation checkpoint/item/target-chain projection`；durable consumed 后的 unfinished delete 不再仅凭 final absence 跳过
target tuple。same-invocation mismatch 仅在 final exact absent 时 no-overwrite restore；re-entry mismatch、forged/unknown
artifact、external final winner 和 ambiguous dependency result 全部保留证据并 fail closed。成功删除在 exact
final/captured absence 下先移除 `intent`、最后移除 `verified`；clean tuple 可由 durable consumed authority 幂等采用，
因此 A3b 不新增 `FinalizeDelete`。该批准完成 A3b 的设计分节输入，不等于书面 design/plan 或实现已经获批。

### Full A3c Owned Workspace Cleanup Data-Flow Approval (2026-08-05)

用户批准 A3c data flow。`ResultLifecycleService` 先在短事务中将 durable cleanup phase 从 `validated` 推进到
`delete_started`，续持 exact cleanup/node lease 与 permanent use latch，再签发只供 concrete target 使用的
destructive cleanup permit。permit 除 immutable job/root/workspace/marker binding 外，必须携带私有 live-validation
callback；concrete target 在每次 rename/remove mutation 前都要重新验证 cleanup fence、node fence、lease owner、
lease expiry、use latch 和 phase 仍允许本次清理，任何失配都在下一次 mutation 前停止。

远端协议先将 exact `jobs/<job>` workspace 使用 standard no-overwrite rename capture 到同一 `jobs` parent 下的
deterministic cleanup-specific sibling；禁止 replace rename、跨目录 capture 或 caller-supplied artifact path。capture
后必须在 captured tree 内重新认证原 owner marker，随后在 captured directory 之外创建 historical cleanup-key 签名的
`verified` marker。该外部 marker 绑定 exact job/workspace/captured tuple、owner marker facts 和 key version；只有
authenticated exact state 才能重入。ambiguous capture、external final winner、owner/canonical drift 或 forged/
unknown artifact 必须保留对象和证据并 fail closed。

captured namespace 使用 bounded、可重入、depth-first、no-follow、same-filesystem 清理。symlink、regular 与 special
entry 都作为叶节点处理，directory 仅在 children 已移除且自身为空后删除；进入和处理每个 directory 时都必须确认
`StatVFS.Fsid` 与 validated workspace/captured root 一致，任何 mount/filesystem boundary 立即停止。captured tree 内的
owner marker 可以作为 owned leaf 删除，但外部 `verified` marker 必须保留到 captured directory 已证明 exact absent。
每次 `RemoveOwnedJobDir` target 调用最多执行 256 次 remove mutation，不使用后台 heartbeat，也不依赖固定墙钟窗口。

target 返回私有 `OwnedJobDirRemoval{Complete, RemovedEntries, ProgressDigest}`。`Complete=false` 是正常的 bounded
progress，不是 failure；closing transaction 只续租 exact owner 并保持 `delete_started`，调用方可立即进入下一 pass。
若调用方停止，则 lease expiry 后由 fresh fence takeover 依据 remote authenticated state 重入。`ProgressDigest` 只证明
本次 closed progress shape，不作为跳过 remote reconciliation 的 durable cursor，也不得暴露路径、名称或 marker。

target error 或 cancellation 后，若 lifecycle closing transaction 仍确认当前 owner，则必须释放本次 cleanup ownership：
published result 投影为 `cleanup_failed`，unpublished result 投影为 ownerless `cleanup_due`，两者 cleanup phase 都保持
`delete_started`，以允许后续 fresh-fence retry/takeover；context identity 必须保留，依赖错误保持 sanitized。target 返回
`Complete=true` 后，lifecycle 先持久化 `delete_started -> deleted`，但暂不释放 cleanup/node lease；随后从 durable
`deleted` 再执行一次 clean-tuple reconciliation，要求 final workspace、captured sibling 和 external `verified` marker
全部 exact absent，不能仅凭前一 target return 推断成功。

只有 durable `deleted` 的 clean-tuple reconciliation 成功后，最终事务才可原子完成 `deleted -> tombstoned`、
`cleaned|workspace_cleaned` 投影、清除 cleanup owner/node fields，并释放 exact active cleanup node lease。该事务继续
服从 published/workspace tombstone shape 和 terminal immutability；任何 fence/lease/phase 变化都不得部分终态化。
该批准完成 A3c 的 data-flow 分节输入，不等于统一 error/takeover 设计、书面 design/plan、A3d、V14 或产品实现已经
获批。

### Full A3d Logical Reconciliation Data-Flow Approval (2026-08-06)

用户批准 A3d data flow。A3d 使用独立的 read-only reconciliation service/port、node/root-scoped
`TargetReconciliationPermit` 和 purpose-exact `recovery_reconcile` SSH session；不得复用 job-scoped cleanup permit、
借用 cleanup node lease、取得 use-latch mutation authority 或暴露任何 rename/remove capability。permit 必须绑定当前
node/credential/root revisions、registered root、expected-set digest、scan bounds、opaque cursor、expiry 与 exact
read-only operation；caller 不得提供 absolute root、remote path 或未注册 namespace。

service 从 DB 构造有界的当前 expected workspace/artifact set，只包含尚未 tombstone 的 isolated workspace 与其当前
A3c 合法 remote state。expected remote components 以 private keyed token 交给 target 比较；未知 remote name 不得越过
target boundary。只有 remote component 与某一 DB expected token 精确匹配后，finding 才可携带该已知安全 job ID；
其他情况只能返回 keyed fingerprint。DB expected set 本身超限或无法完整建立时不得开始或采纳 clean scan，必须返回
`scan_incomplete` blocker。

target 只扫描 canonical `<registered-root>/jobs` 的直接 children；不得跟随 symlink、递归进入未知 directory 或读取除
固定 Recovery marker/artifact document 外的用户内容。closed classification 固定为：DB expected component 与
marker/artifact/phase 一致的 `known_healthy`；DB matched 但 kind、marker、phase 或 artifact state 漂移的
`known_drift`；可由 historical Recovery key 认证但 DB 无当前 expected owner 的 `db_unmatched`；无法认证或不属于
closed Recovery component grammar 的 `forged_or_unknown`；依赖失败、目录漂移、超限或未证明 EOF 的
`scan_incomplete`。只有 `known_healthy` 不是 finding；任何其他类别都保持 remote object 原位且成为 blocker。

scan continuation cursor 只携带 authenticated ordinal/prefix digest 与 bound generation，不含 remote name/path。
resume 必须从 directory 起点重放已处理 prefix 并验证 exact digest 后才能继续；顺序、内容或 root revision 漂移会使
cursor fail closed 为 `scan_incomplete`，不得跳过 prefix 或将不稳定分页误判为 clean。每次调用、整个 chained scan、
expected set、finding list 与 aggregate counts 都有硬上限；超过任一上限本身就是 blocker，不通过扩大内存或无界重试
规避。

finding 只允许 closed category、audit-key-versioned HMAC fingerprint、closed entry kind、可选 DB-confirmed job ID 与
有界计数。raw name/path、marker bytes、artifact token、HMAC input、credential/root locator、dependency status/error
不得进入 result、cursor、JSON、audit、alert 或 log。每个 pass 只写一条 bounded aggregate `recovery_reconcile` audit，
并调用 required finding sink 发送可去重的 sanitized alert；Task 7 交付显式调用合同、audit/alert 行为与测试，Task 8
才拥有真实 runtime adapter、managed cadence、settings 和 startup/main composition。

reconciliation result 只有在 DB expected set 完整、remote scan 在同一 validated prefix chain 上达到可信 EOF、finding
为零且 aggregate audit 成功时才可返回 `clear`；其余状态一律 `blocked`。downgrade-readiness 必须在其 sticky
admission generation 下对全部 registered roots 获得 fresh `clear`，不得复用旧 scan cache；DB cleanup backlog 与
permanent use latch 的既有 blockers 仍独立生效。该批准完成 A3d 的 data-flow 分节输入，不等于统一
error/takeover/privacy 设计、testing design、书面 design/plan、V14 或产品实现已经获批。

### Full A3 Error, Takeover And Privacy Approval (2026-08-06)

用户批准方案 A。完整 A3 的 closed error precedence 固定为：caller cancellation/deadline 原始 context identity 优先；
其次是 invalid/stale authority；再其次是已经观察并证明的 contradictory remote state；resolver、key、SSH/SFTP、I/O、
close、audit/alert 或其他 dependency ambiguity 最后统一为 sanitized unavailable。target、service 与 worker 不得 wrap、
format 或记录 raw dependency error；并发出现 cancellation 与 transport close error 时仍只返回原 context identity。

A3b 对现有 post-arm failure rule 作 later-controlling 收窄。durable delete authority consumed 后，只有
`ErrRecoveryTargetChanged`、forged/unknown authenticated tuple、external winner 或其他已经证明不能被 exact state
machine 安全采用的矛盾，才允许通过 bounded `context.WithoutCancel` closing transaction 投影现有
`operation_unresolved`、item `failed/remote_outcome_unresolved`、job `needs_attention/remote_outcome_unresolved`、failure
evidence、attempt closure 与 source/node lease release。该 transaction 必须重新证明 exact current owner/fences；若已
takeover，则旧 owner 零更新。

A3b 的 resolver/key/SSH/SFTP/read/stat/rename/remove/close ambiguity、temporary unavailable、caller timeout 或
cancellation 不立即 terminalize，也不写 operation checkpoint、推进 target chain、删除证据或盲目推断 remote outcome。
当前 invocation 必须停止且不得启动 polling、heartbeat 或无界 same-turn retry；后续显式 invocation 在 claim 仍 exact
current 时可重入，或在 lease expiry 后由 fresh attempt/node/source fences takeover。stable artifact binding 继续绑定
historical consumed authority，fresh permit 只绑定当前 mutation authority；takeover 在任何后续 item 前必须先重放
`Target.Delete` tuple reconciliation。invalid/stale permit 不产生 remote-unresolved evidence，只按 safe worker
fence/conflict/unavailable contract 停止。

A3c 中 `Complete=false` 保持正常 progress。target error 或 cancellation 后使用无取消、短 deadline 的 closing
transaction；只有仍为 current cleanup owner 才按已批准 data flow 释放 cleanup node lease并投影 published
`cleanup_failed` 或 unpublished ownerless `cleanup_due`，cleanup phase 保持 `delete_started`。lost-owner CAS 必须零
更新且不得释放 fresh owner's lease；closing transaction 失败不得声称已释放或已投影，只返回 sanitized lifecycle
unavailable/conflict。进程崩溃不运行 detached heartbeat，由 lease expiry takeover。durable `deleted` takeover 禁止重新
capture/递归删除，只能重放 clean-tuple reconciliation，再决定是否原子 tombstone/release。

A3d finding 与 `scan_incomplete` 是正常 `blocked` products，不转换为系统错误；resolver/SSH/key、audit 或 alert sink
失败才返回稳定 sanitized reconciliation-unavailable，且任何 caller/downgrade-readiness 必须将 error 视为 blocker。
A3d 没有 durable owner 或 orphan row，restart 只能从 fresh DB/root snapshot 和 authenticated cursor/prefix 重放；旧
result/cache 不具有 clear authority。

全部 A3 permit、proof、artifact binding、cursor、request 与 private result 必须隐藏或排除 private fields，并提供
redacted `String`/`GoString`；target 零 direct logging。verification 必须捕获 error、`%v/%+v/%#v`、JSON、audit、alert、
metric labels 与 structured logs，使用 recognizable canaries 证明 host/user/credential/root/path/name/token/marker/
content/digest input/SFTP status/raw error 零泄漏。audit/alert 只允许 closed category、bounded counts、opaque IDs 与
sanitized digest；A3d finding fingerprint 只保证同一显式 audit-key version 内稳定，不宣称跨 key rotation 相等。该
批准完成统一 error/takeover/privacy 设计输入，不等于 testing/V14、书面 design/plan 或产品实现已经获批。

### Full A3 Testing And V14 Approval (2026-08-06)

用户批准完整 A3 的 testing/V14 验收设计。A3b 必须覆盖 regular、symlink、exact empty directory、special object，
以及 delete-specific `intent/captured/verified` tuple 的所有正常、冲突、崩溃、重入和 takeover 状态；capture、captured
verification、delete、artifact cleanup、absence verification、operation checkpoint/final projection 的每个边界都要有
故障注入。验证必须证明 consumed authority 后先做 remote tuple reconciliation；temporary unavailable、caller
cancellation/deadline 和 dependency ambiguity 不得错误终态化，只有已证明 contradictory tuple 才能进入既有
`needs_attention/remote_outcome_unresolved` 投影。

A3c 必须以 published/unpublished parity 覆盖 cleanup lifecycle，逐次证明每个 rename/remove mutation 前都重新执行
live fence validation；单次 target 调用最多 256 次 remove，`Complete=false` 是正常进度，并覆盖多 pass、显式重入和
lease-expiry takeover。filesystem 矩阵必须覆盖 depth-first、no-follow、symlink-as-leaf、same-filesystem、mount/Fsid
漂移、canonical/marker 漂移和兄弟 namespace 隔离。crash matrix 必须覆盖 `validated -> delete_started`、capture、owner
marker reauthentication、external verified marker、partial cleanup、`deleted`、clean-tuple reconciliation 及最终事务；
SQLite 和 required-real-PostgreSQL 都必须证明 tombstone、`cleaned|workspace_cleaned`、cleanup owner/node fields 清除与
exact cleanup node-lease release 原子完成，lost owner 零更新。

A3d 必须证明独立 `recovery_reconcile` purpose/read-only permit 在所有路径零 rename/remove mutation，完整覆盖
`known_healthy`、`known_drift`、`db_unmatched`、`forged_or_unknown`、`scan_incomplete` 五类结果、expected-set/scan/
finding/aggregate hard bounds、可信 EOF 和 authenticated cursor prefix replay。顺序、内容、cursor generation 或 root
revision 漂移必须 fail closed；canary privacy 矩阵必须证明 remote name/path、private token input、marker、credential、
raw dependency error 等不会进入 result、cursor、JSON、audit、alert、metric label 或 log。aggregate audit、required
finding sink、fresh-clear downgrade blocker、旧 result/cache 无 clear authority 均为强制验收。

每个 A3 vertical slice 都先记录 genuine RED，再交付 minimal GREEN、focused normal/race、只在涉及 DB/CAS/lease/
transaction 的路径运行 required-real-PostgreSQL no-skip 验证、更新 implementation evidence ledger 并在明确 stop point
停止。纯 target filesystem 行为不得用无关 PostgreSQL gate 制造虚假完成度；前一切片不得提前计入后一切片、Task 8、
Git delivery 或父任务进度。

V14 必须以新鲜证据重新追踪全部 Task 7 acceptance，运行 whole Recovery normal/race、required-real-PostgreSQL
normal/race no-skip、vet/backend lint、format/diff、privacy/static、paired migration/schema、Trellis/JSON/JSONL、exact
145-path manifest、protected hashes 与 branch/HEAD/staged/outside-scope Git-state gates。现有 PostgreSQL fixture 不重启、
不替换、不删除，也不输出密码或 DSN。只有全部 A3 acceptance 都有新鲜证据且 whole review 为零 Critical/Important，
才可评价 Task 7/Child 13 closure；V14 不计入 Task 8 runtime/main、commit、push、PR、CI、merge 或父任务整体交付。

该批准闭合完整 A3 的交互式 design input，允许把全部已批准内容整理进现有 `design.md` 并进行书面规格自检；它仍不
批准更新 `implement.md` 或开始任何产品实现。

## Task 8 Production Authority Focused Amendment (2026-08-14)

Task 8 的 runtime/lifecycle shell 已达到 fail-closed checkpoint，但生产 enablement 仍缺少 current preflight
external evidence、complete live authority revalidation 和 reconciliation root revision。用户批准方案 A：在
不新增 `000070`、不伪造 clean/false/zero evidence、也不把 locator digest 当 root authority revision 的前提下，
扩展现有 encrypted target-root registry，并由 Recovery-owned production authority 组合当前 source、security、
target 和 policy evidence。该批准取代“保留 unavailable adapter 即可完成 Task 8”的任何旧推断，但不追认产品
实现、测试或交付已经完成。

### Requirements

- encrypted target-root registry 继续存放在现有 internal settings namespace，不新增 table、column、migration 或
  `000070`。每个 `(node_id, root_id)` record 必须同时拥有独立、durable、opaque authority revision、当前已证明的
  root observation binding、reserve bytes/inodes policy 和 closed overlap/policy binding；locator digest 只绑定
  canonical private locator，不能替代 authority revision。任一 locator、root observation、reserve 或 policy
  rotation 都必须产生新 authority revision；safe-label-only 修改不得静默改变安全 authority。
- target-root register/rotate/delete/list 必须由 Recovery-owned Admin operation 管理。registration/rotation 在持久化前
  取得 fresh purpose-exact read-only target probe，并只保存经过验证的 encrypted locator 与 closed authority
  product；generic settings BatchUpdate/Delete/config import 不得直接构造、覆盖或导出 registry record。Task 9 的 API
  scope 必须补齐这些管理 routes、Auth/RBAC/step-up/idempotency/audit/privacy/Swagger contracts；Task 8 只拥有
  production service/facade 与 runtime composition。
- `RecoveryPreflightExternalEvidenceAuthority` 必须从当前 durable plan 重新取得 exact Repository source authority、
  Provider capability、Processing malware/security evidence、registered target-root authority 和 fresh target probe，
  生成一个 Recovery-owned closed eligibility product。managed Rsync 使用现有 Repository pinned-tree source resolver；
  没有 exact production source authority 的 Provider 必须返回 closed unavailable，不能降级到 locator、旧 Catalog、
  request echo 或 generic read success。
- security evidence 必须由当前 canonical Processing malware artifacts 与当前 Recovery policy 聚合为 exact
  finding-set digest、policy revision、closed disposition 和 overridable categories。单个 `safe` bool、固定 clean
  值、旧 preflight security decision 或 plan-carried digest 都不能签发新 authority。
- overlap 与 reserve evidence 必须属于上述 current eligibility product。它必须由 current source namespace evidence、
  registered root policy 和 fresh target observation 得出；caller-supplied flags、path-prefix guess、zero reserve 或
  registration-time assertion 单独都不能批准 preflight。任一 evidence unavailable 必须阻止 production-enabled graph
  publication，或在已发布 graph 的 effect boundary fail closed，绝不能产生 synthetic clear。
- `RecoveryAuthorityRevalidator` 必须复用同一个 eligibility owner，在 caller-owned transaction 内重验 node/
  credential、registered root authority revision、source/capability、policy/finding/disposition 和 durable preflight
  binding；需要 external observation 的部分在 transaction 外取得并以 private sealed snapshot 带入，commit 前再锁定
  对应 durable revisions。partial node/source success 不构成完整 effect authority。
- `RecoveryReconciliationRevisionSource` 必须从同一 target-root authority record 取得独立 current authority revision，
  并与 current node/credential revisions 在同一 transaction 内解析。reconciliation 继续使用 read-only remote scan；
  authority unavailable、revision drift、scan incomplete、finding 或 sink/audit failure 全部是 blocker，从不投影 clear。
- production Recovery provider coverage 以 exact registered restore port 为准。当前 managed Rsync exact source seam 可
  enable；没有同等 source-access/revalidation product 的 Restic/Rclone plan 必须显式 unavailable，不得 fallback 到
  Rsync、generic Provider contract 或 legacy restore path。

### Settings Disposition

- 保留并接入 `PreflightTTL`：由 server-owned plan/preflight policy 生成并验证 expiry，API/caller 不得自由选择 TTL。
- 保留并接入 `MaxSelectionItems` 与 `MaxLogicalBytes`：PlanService/selection materialization 在持久化前后都按 dynamic
  limit 与 immutable hard cap 的较小值拒绝超限，handler-only validation 不算 domain enforcement。
- 保留并接入 `LeaseRenewMargin`：managed worker 必须在 active claim 中按 margin 调用 durable heartbeat，传播 renewed
  claim/fence deadline，并在 renew failure 后取消当前 effect、阻止旧 lease 继续 mutation。
- 保留并接入 `ExecutionTimeout`：execute effect 创建 source lease/job 时冻结 absolute deadline，managed execution
  context 不得超过它；timeout 后只走现有 post-arm/unresolved 或 safe takeover contract，不推断远端零写入。
- 将 `OrphanQuarantineLimit` 更名为 logical `ReconciliationFindingLimit` 并接入 A3d finding/aggregate bound；它不表示
  physical move/delete/quarantine，也不能扩大 immutable chain/expected-set hard caps。
- 删除 `DefaultRootID`。root identity 是 node-scoped `(node_id, root_id)`，一个 global root ID 无法无歧义选择 authority；
  Task 10 可在已选 node 只有一个可用 registered root 时做显式 UI 预选，但 request 仍必须提交 exact pair。
- 删除 `VerificationTimeout`。当前 Recovery verification 是 execution 内逐 operation 的 fenced evidence，不存在可独立
  计时、持久化或 takeover 的 verification phase；`ExecutionTimeout` 与每个 target/provider bounded context 继续
  约束它。未来若新增独立 durable verification phase，必须另行规划 setting 和 state contract。

### Acceptance

- [ ] encrypted registry authority revision 与 locator digest 独立；register/rotate/no-op/delete、tamper、key rotation、
  stale probe 和 concurrent update 在 SQLite 与 required real PostgreSQL 上有 closed tests，且没有 `000070`。
- [ ] 三个 production authority 由同一 current eligibility owner 提供，并覆盖 source/capability/policy/finding/root/
  overlap/reserve 的 drift、unavailable、substitution 和 privacy matrix；known-unavailable adapters 不能发布 enabled graph。
- [ ] managed Rsync exact source access 通过 pinned resolver 正常完成；无 exact production source authority 的 Provider
  稳定 fail closed 且零 target mutation。
- [ ] 五个保留/更名 settings 在 owning domain seam 被消费，两个删除 settings 从 registry/snapshot/config fixtures 中
  移除；BatchUpdate/reset/import 仍通过 validate -> drain -> persist -> install/rollback。
- [ ] disabled graph 继续 cleanup、logical reconciliation 和 receipt reaping；fresh/transition downgrade readiness 在
  authority unavailable 时保持 blocked，use latch 仍永久支配 `forward_fix_only`。
- [ ] Task 9 route map 明确包含 target-root management 与 downgrade-readiness Admin operations；Task 8 不冒领其 handler/
  audit/Swagger 完成度，Tasks 9--10 在本 amendment 完成前仍为 `not_executed`。

### Planning State

方案 A 的产品方向、settings disposition、architecture、data flow、error/privacy/compatibility 与 testing/rollback
均已由用户批准并写入 `design.md` section 48。focused execution plan 与 convergence review 已完成，blocking open
questions 为空；final design summary 获得后续明确批准前，不得修改产品代码、运行 `task.py start`、stage、commit、
push 或创建 PR。

## Task 10 live recovery read model and delete handoff focused amendment (2026-08-16)

Task 10 的 approved wizard contract 依赖 current backend 未发布的两类产品：owner-scoped job/checkpoint/result
读模型，以及把 exact-mirror delete authorization request 中的 ephemeral grant secret 交给当前 paused fenced
execution 的瞬时 handoff。当前 `GET /recovery-jobs/:id` 不返回 attempt/checkpoint/result-set product，现有
result download route 又要求 caller 已知 `resultId`；delete authorization durable mint 后只 wake 普通 claim scan，
而普通 worker 已在 `delete_authority_required` checkpoint 返回，raw secret 没有进入 resume execution。前端若猜测
这些字段、轮询不存在的 endpoint，或只展示一个无法继续的按钮，都不满足原 Task 10 acceptance。因此本
amendment 是 Task 10 的最小 live-contract closure，不是 Task 11、legacy restore GA 或新的 Recovery product line。

### Requirements

- Recovery API 必须提供 owner-scoped、schema-versioned、closed-enum 的 job detail、bounded job-item page 和
  published-result page。job detail 可包含 aggregate progress、sanitized failure category、current exact-mirror
  delete checkpoint product 与 ResultSet lifecycle summary；item/result pages 只包含 opaque IDs、ordinal/kind/
  outcome、non-negative counts/bytes 和 UTC timestamps。它们不得投影 locator、locator/internal digest、ciphertext、
  credential/revision evidence、workspace phase、fence 或 raw internal error。
- current delete checkpoint product 只在 exact current `delete_authority_required` binding 可证明时存在，并包含
  public handoff 所需的 opaque `checkpoint_id`、`attempt_id`、decimal `expected_plan_revision`、closed status 和 UTC
  expiry。unknown phase、stale attempt、contradictory chain、expired authority window 或 foreign owner 必须把整个
  product fail closed；不得拼出 partial checkpoint。
- item/result list 必须使用 real server pagination 和 hard maximum page size。ResultSet lifecycle 与 Job outcome
  分离；只有 isolated、published、owner-matched result rows可列出并取得既有 download ticket。in-place、partial、
  revoking/cleaned、foreign 或 private workspace rows不得因 list API 变成可下载。
- existing exact-mirror delete authorization endpoint 同时拥有 ephemeral handoff：durable authorization 成功或
  same-intent replay 后，必须把 request 中 exact raw secret 交给同一 job/plan/checkpoint/attempt/fence 的 paused
  execution，并在 handoff 未被 current managed graph 接受时返回 closed conflict/unavailable，不能报告可继续。
  durable grant/receipt transaction 本身必须在 commit 前复用完整 checkpoint-history/current lease/operation 与
  pending-grant authority validation；不得先按 phase-only check 持久化 grant，再依赖 facade 事后拒绝或补偿。
  raw secret 只存在于 request scope 与 bounded in-memory handoff；不得写入 DB、queue payload、URL、response、
  audit、log、metric、error 或 formatting。shutdown/context cancellation、definitive consumption、replacement 或
  stale fence 必须清除它。
- network/5xx ambiguity 必须允许 caller 使用同 endpoint、同 idempotency key、同 secret 重放 durable receipt 和
  transient handoff；different intent/secret、expired grant、stale checkpoint/attempt、duplicate concurrent consumer
  和 process-restart gap 全部 fail closed。runtime 继续负责 heartbeat、absolute deadline、source/target revalidation、
  grant consumption 与 exactly-once fenced mutation；handler 或 API projection不得直接执行 remote delete。
- mixed version 必须 closed：新 frontend 遇到缺少这些产品的旧 backend 显示 unavailable 且无 fallback；新 backend
  仍保持 Recovery default-disabled、Auth/RBAC/ownership/step-up/idempotency/audit 与 legacy route gating。

### Acceptance

- [ ] backend genuine RED -> GREEN 覆盖 owner hiding、closed schema/enums/count/time validation、checkpoint chain
  contradiction、real pagination bounds、ResultSet/Job separation、download eligibility 与全隐私 canary。
- [ ] delete handoff genuine RED -> GREEN 覆盖 current pause resume、heartbeat/deadline、same-key same-secret lost-response
  replay、concurrent duplicate、stale attempt/fence、expired grant、shutdown/cancel/process-memory loss 和零 secret sink。
- [ ] router/RBAC/Swagger 只增加 Task 10 所需的 two read routes；已有 delete authorization route 保持唯一 public
  secret handoff mutation，不新增 migration、settings、public delete route 或 legacy fallback。
- [ ] 原 Task 10 frontend RED/GREEN、a11y/i18n/responsive 与 frozen gates 在 live contract GREEN 后完整执行；在
  backend closure 与 frontend closure 都通过前，Task 10 保持 `not_executed`/`in_progress` 而不得宣称 complete。
