# 备份资产 Worker 协议

## 0. 文档状态与授权边界

- Trellis task：`.trellis/tasks/07-19-backup-assets-worker-protocol`，状态必须保持
  `planning`，parent 为 `.trellis/tasks/07-12-backup-data-explorer-design`。
- Child 10 从合并后的 `main` 独立规划；基线为
  `2ce71339b7f10fe759c0009ff01a100e589a700c`，分支为
  `codex/backup-assets-worker-protocol`。
- 真实 program 已交付进度仍为 9/15；Child 10 只是已实例化并进入 focused
  planning，不能计作已交付。父任务继续是 `planning` program tracker，不是本次
  产品实现目标。
- 本轮授权只覆盖 Phase 1 研究及 `prd.md`、`design.md`、`implement.md`、research
  evidence。用户已于 2026-07-19 整体批准该 focused planning package；该批准不包含
  `task.py start` 或产品实施授权。
- 规划审阅通过与 `task.py start`/产品实施授权是两个独立门禁。本轮不得执行
  `task.py start`、产品代码、migration、测试实现、stage、commit、push、PR、CI、
  merge 或 post-merge/release/deploy mutation。
- 用户已于 2026-07-19 进一步明确授权“执行 `task.py start` 并实施”；但当前会话的
  上层 workflow state 仍强制 `planning / stay in planning`。因此该授权已记录但尚未
  执行，必须等待 workflow state 切换到 Phase 2，不能以用户授权覆盖上层门禁。
- `backup_assets.enabled` 继续默认 `false`；Command Provider 继续返回 typed
  `task_artifact_contract_missing`。Release Please PR #386 不在本 Child 范围且不得
  合并。

Current-main 事实与定位证据见
`research/current-main-evidence.md`。本 PRD 与 `design.md`、`implement.md` 已作为
一个完整规划包获批；该规划批准仍不打开独立的实施门禁。

## 1. Goal

在不向 Worker 暴露数据库、Provider locator、仓库/SSH/Restic/Rclone credential、
宿主路径或写 Catalog 权限的前提下，为备份资产建立一个可选、持久、拉取式、
lease/fence 驱动的处理控制面和独立加密的 Derived Store。该基础只证明协议、
调度、输入/输出 grant、原子发布、撤销与恢复语义；真实 OCR、缩略图、文本、
Office/PDF、媒体、恶意软件和归档能力仍由 Child 11 实现。

Core 的 Catalog、Search、Content Broker、workspace 和原件下载必须在没有任何
Worker 时完全可用。Worker 缺席或未配置是 informational capability 状态，不得
生成重复失败作业、噪声告警，也不得改变 RecoveryPoint 的 committed/verification/
drill/retention 真相。

## 2. 成功定义

1. Core 以数据库持久队列保存工作、interest、attempt、transition revision、
   worker pull lease 与 RecoveryPoint `processing_job` lease；崩溃接管总是产生新
   fence，旧 owner/attempt 永远不能发布。
2. `work_key` 对相同的、严格 schema 校验并规范编码的输出请求进行合并；任何
   source fingerprint、capability/schema、pipeline fingerprint、output profile、
   security-policy revision、尺寸、codec、页范围、quality、输入/输出限制或其他
   影响输出的参数变化都生成不同 key。
3. Interactive 与 background 使用保留槽；同 `work_key` 的多个 interest 共用一个
   底层作业，只有最后一个 active interest 消失才撤销底层作业。
4. Worker 只通过同机 authenticated local transport 或管理员显式启用的 mTLS
   trust domain 拉取工作。Remote trust 默认关闭；不可信、版本不兼容或发生合约/
   安全错误的 Worker 被隔离且不盲重试。
5. Input 与 Sink activation secret 都是随机、单次使用、短时且绑定
   job/attempt/worker/fence。Input 激活后经 Child 8 Content Broker 的窄内部端口做
   有界 sequential/多次 Range；Sink 只接受一个有界原子多产物集和一次 manifest
   commit。
6. Core 对 MIME、产物数量、单项/总大小、digest、completeness、security-policy
   revision、source fingerprint 和当前 fence 做闭合校验。旧 attempt、迟到输出、
   被取消/过期/源已变化的输出被拒绝并销毁。
7. Derived Store 使用独立 `derived_store` key domain；每个物理 blob 有随机 DEK，
   以 Derived KEK 包装，并使用认证分块 ciphertext。它支持跨 RecoveryPoint 引用、
   最后引用销毁、KEK rewrap、tamper 检测和 orphan reconciliation，不复用 Search、
   Export 或 preview cache key。
8. revoke、expiry、key-loss 和 rollback 总是先通过 Child 7
   `ContentIndexIngest.RevokeContentProjection` 原子移除 postings、excerpt reference
   并把 field coverage 标为 unavailable/推进 revision，再销毁 wrapped DEK/引用/
   ciphertext；任何崩溃重试都保持这个单向顺序，不产生 ghost projection。
9. `asset-worker` 只是协议客户端，内置 fake/no-op 测试能力只用于证明
   handshake、pull lease、heartbeat、cancel、upload/manifest 和 graceful shutdown；
   Core server binary 不链接重型解析工具。
10. 仅提供 dedicated internal Worker transport 和一个 admin-only、feature-gated、
    rate/body-limited、sanitized 的健康摘要。Child 10 不修改前端，也不提前扩大
    Child 9 workspace。

## 3. 已确认的 current-main 事实

1. SQLite/PostgreSQL migration 均止于成对
   `000066_backup_asset_content`；`000067...000071` 尚不存在。Child 10 独占成对
   `000067_backup_asset_processing`，`000068...000071` 完全保留。
2. Migration 由 `backend/internal/database/migrator.go` 的 embedded glob 自动发现，
   无手工 version registry；现有
   `backup_asset_migrations_integration_test.go` 已覆盖 062-066，并有可强制失败的真实
   PostgreSQL fixture。Focused 实施计划必须把父 Section 11 的历史
   `BackupAssetMigration066` typo 更正为 `BackupAssetMigration067`。
3. `LeaseHolderProcessingJob` 已在 Foundation closed set 中；`LeaseService` 已提供
   acquire、renew、release、takeover、absolute deadline、attempt/fence 和
   `ValidateFenceTx`。本 Child 不改 holder closed set。
4. Child 7 的 `search.ContentIndexIngest` 已要求 exact `AssetRef`、Catalog/Search
   generation、source fingerprint、`processing_job` lease/attempt/fence、classification
   CAS 与单调 coverage/pipeline/index revision。其 revoke 在一个 DB 事务内删除
   postings、清空 excerpt ref、标 unavailable 并推进 projection revision。
5. Child 8 的 `content.SourceResolver`/Broker 已持有唯一合法的 bounded Provider
   read seam、Range、Provider-byte accounting、源前后复核、配额和 shutdown；但当前
   public Broker 形状绑定用户 delivery ticket/cookie，不能直接给 Worker 复用。
6. `backupasset/runtime.Runtime` 是唯一 backup-asset composition root，已经共享
   keyring、LeaseService、Search ingest、Repository service 与 Content Broker。
   Processing 不能在 `cmd/server` 另建第二套 DB/lease/keyring/Provider graph。
7. 当前 keyring 只有 entry identity、cursor signing、audit fingerprint、recovery
   cleanup ownership 与 search token；没有 Derived Store domain。`000067` 与
   `keyring.go` 必须成对扩展并有安全 down/loss/rewrap 测试。
8. Settings 通过 `settings.Service` 的原子 backup-asset snapshot 读取；所有动态
   processing/queue/quota/TTL 设置必须进入 registry。local socket、mTLS material
   path 与 Derived Store root 是 startup-only 值，必须 `RequiresRestart`；秘密/信任
   路径按敏感字段处理。
9. Child 9 已合并且主 bundle 只剩 JS 1.91 KiB、CSS 0.79 KiB 余量。Child 10 默认
   不新增任何 frontend 文件；现有 UI 继续诚实显示 enhanced capability unavailable/
   not deployed。
10. 当前 CI 的 PostgreSQL 18 job 只列 062-066 和 Catalog/Search/Overlay/Content
    behavior parity。Child 10 实施时必须显式加入 067 与 Processing behavior parity；
    未配置/未执行的真实 PostgreSQL gate 不得写成 pass。

## 4. In Scope Requirements

### 4.1 持久工作身份、合并与状态

- 定义 closed `ProcessingState`：
  `queued, leased, fetching, materializing, processing, uploading, validating,
  retry_wait, cancel_requested, canceled, succeeded, failed, superseded, expired`。
- `fetching` 与 `materializing` 是两个独立状态。Streaming/Range 路径可
  `fetching → processing`；需要路径的未来 capability 才可
  `fetching → materializing → processing`。Child 10 不实现真实 tmpfs/tool runner。
- 状态、transition revision、stable error code、attempt、Worker pull lease、
  RecoveryPoint lease/fence、retry schedule、cancel/supersede/expiry reason 分列保存，
  不用一个自由字符串隐含多个维度。
- 永久输入/策略错误、瞬时资源/基础设施错误、合约/安全错误采用父合同给出的 closed
  codes；只有瞬时错误进入有界 `retry_wait`。合约/安全错误隔离 Worker，永久错误
  终止，取消/源变化/源过期分别进入自己的终态而不是 `failed`。
- `work_key` 由严格版本化 typed descriptor 的规范编码计算。未知字段、重复 JSON
  member、非规范数字、越界值和 schema 不匹配整体拒绝，不能对任意 map 排序后猜测。
- 工作请求先查有效 Derived set，再查当前 coalescing job；旧的 terminal/不可复用
  job 保留历史但不阻止新 job。数据库唯一约束保证并发创建只能产生一个 current
  job。

### 4.2 调度、interest、lease 与 crash recovery

- Worker 主动 pull，不由 Core 回调任意 Worker URL。调度只匹配可信 identity、
  compatible protocol、capability/schema/pipeline/profile/limits 与 ready/degraded
  health；draining Worker 不取得新作业。
- background 不能占用 interactive reserve；interactive 可借用空闲 background
  capacity。队列在每类内稳定、公平且有界，不能以 unbounded goroutine/内存队列
  作为真相。
- 每个 interest 有独立 owner/type/priority/lifecycle。取消一个 interest 只移除该
  引用并重新计算优先级；最后一个引用消失后先 revoke Input/Sink grant，再进入
  `cancel_requested`。
- 成功 pull 时取得覆盖整个作业的 `processing_job` RecoveryPoint lease，并把其
  lease ID、attempt ID、fence 和 absolute deadline 绑定到 attempt。heartbeat 同时
  续 Worker attempt lease 与 RecoveryPoint lease，任何一方失败都使当前 attempt
  丧失发布权。
- crash/timeout takeover 创建新 attempt/fence；旧 grant 立即撤销，旧 staged output
  清理。旧 fence 不能因 Worker 重连或同一 job ID 而复活。

### 4.3 Worker trust 与协议

- 默认 local transport 使用受权限保护的 Unix socket 和 OS peer identity；只信任
  与 Core 相同的有效 UID。身份还绑定协议实例 ID，但客户端自报字段本身不构成信任。
- Remote transport 只有显式 `worker_remote_enabled=true` 且完整 TLS 1.3 server
  certificate/key、client CA 和 trust-domain 配置才监听；必须
  `RequireAndVerifyClientCert` 并从经验证 URI SAN 派生 Worker identity。默认不监听、
  不接受 bearer/API key 替代 mTLS。
- Worker protocol 不携带 Provider kind/locator、repository config、SSH/Restic/
  Rclone credential、host path、database DSN、user query、原始文件名或 updater
  credential。安全日志也不得包含 activation secret、fence、原始 tool output 或
  certificate subject。
- Child 10 的协议客户端只连接配置的 Core Unix socket/mTLS endpoint。完整
  non-root/rootfs/cgroup/seccomp/AppArmor/tmpfs/swap/network/DNS sandbox 由 Child 11
  落地；在该门禁前不注册真实解析 capability，也不声称生产 sandbox 已完成。

### 4.4 Input 与 Sink sessions

- Coordinator 生成两个独立 256-bit activation secret，DB 只保存 hash。Input 与
  Sink 分别只能从 `issued` 原子变为一次 `active`；错误 worker/attempt/fence、重复
  激活、过期或已 revoke 一律失败关闭。
- Content package 增加 Worker-internal attempt broker facet；processing 只传
  `AssetRef`、exact Catalog generation、source/entry fingerprints、允许的 read mode
  和预算。Repository/Provider locator、registry 和 source handle 保持封装在 Child 8
  boundary 内。
- Input session 支持 bounded stat、sequential 与多次单 Range read；request count、
  cumulative Provider bytes、in-flight、per-read size 和 absolute TTL 在打开 source
  前原子 reserve，取消/错误后保守 finalize。每次 read 前后及发布前复核 mutable
  source。
- Sink session 流式接收 plaintext 并立即写入 Derived Store 的加密 staging，不得
  使用普通 temp/plaintext disk。每产物和总集合都有 count/size/MIME/role/coverage
  限制；Core 自己重算 digest。
- Manifest 恰好提交一次。先完成所有产物和完整性校验、最终源/政策复核及 current
  fence 校验，再以单一事务发布整个 artifact set；DB publication 之前 ciphertext
  已 durability-safe，DB 之外的 crash orphan 只能不可见并由 reconciliation 删除。

### 4.5 Derived Store、Search projection 与密钥生命周期

- `derived_store` 是独立 versioned KeyDomain。每个新物理 blob 使用 CSPRNG 生成的
  32-byte DEK；AES-256-GCM 认证分块 nonce/AAD 绑定 format、blob ID、digest、plain
  size 和 chunk index。DEK envelope 绑定 blob ID 与 Derived KEK version。
- Derived root 必须是独立绝对路径，拒绝 symlink/traversal/special-file escape，
  且不能与 `/data`、`/backup`、`/logs` 或任何已登记 backup source 互为父子。Worker
  永远只见 Sink session，不见 root/relative blob locator。
- 一个物理 blob 可以被多个 source RecoveryPoint reference 引用，但每个 source 的
  authorization/lifecycle 独立。删除一个 source 只移除其 reference；最后一个 live
  reference 消失时先令 wrapped DEK 不可用，再删除 ciphertext。
- KEK rotation 只 rewrap 小型 DEK envelope，不重写大型 ciphertext；旧 KEK 在所有
  引用完成迁移前保持 verify-only。Master KEK rewrap 保持 Derived key version/plaintext
  不变。
- Derived states 固定为
  `active, stale, unavailable, superseded, revoked, purging, purge_failed`，与
  completeness/finding 正交。Pipeline 或 security-policy fingerprint 变化使旧产物
  stale/superseded 并创建新 work key；不在旧 job 上篡改身份。
- Search content/OCR projection 只通过 Child 7 port 发布/撤销。任何 revoke、expiry、
  active Derived key-loss、schema rollback 或 orphan repair 都执行
  `Search revoke committed → derived reference/key unavailable → ciphertext cleanup`，
  失败时停在更早、更安全的状态并重试。

### 4.6 Generic updater metadata 与 protocol-only Worker

- `000067` 可定义 generic signed updater metadata：source kind/opaque ID、version、
  manifest digest、signature key fingerprint、bundle fingerprint、verification/
  activation time、closed state 和 stable failure code。
- Child 10 不下载 bundle、不持久 updater credential/URL secret、不保存 raw output、
  不实现 updater binary/content-addressed bundle store，也不让 processing Worker
  借 updater identity 获得网络。
- `asset-worker` 只实现协议循环、heartbeat/cancel、bounded upload、draining 与
  graceful shutdown。Fake/no-op capability 只在测试注入路径可调度，不构成任何真实
  preview capability。

### 4.7 Internal/admin API、配置与降级

- Worker protocol 位于 dedicated internal listener，不注册到面向用户的
  `/api/v1` CORS surface；它只接受 transport-derived local/mTLS identity。每个 route
  有 fixed body limit、identity rate limit、strict decoder、fixed route log label 和
  sanitized error code。
- 面向现有 API 只增加 `GET /api/v1/admin/backup-asset-processing`，要求 JWT、Admin、
  `backup_assets.enabled`、既有 API rate limit 和无请求 body。响应只有 sanitized
  configured/trust/health/capacity/queue/state/error-category/derived quota summary；
  不返回 source/job/grant/fence/cert/path/parameter/raw error。
- 未配置兼容 Worker 时，内部 `RequestWork` 在持久化 job 前返回 informational
  `not_deployed` capability；已存在 Worker 暂时离线时作业可保持 queued/retry_wait，
  但不制造 backup failure 或重复 alert。
- 指标只使用低基数 labels：priority/state/error category、trusted/quarantined worker
  counts、slots、queue age、lease loss、job duration、sink bytes、derived quota/orphan/
  tamper/refcount。未配置 Worker 不触发告警；protocol quarantine 与长期 reconciliation
  failure 才是 bounded alert candidate。

## 5. Explicit Out Of Scope

- Child 11：真实 OCR/thumbnail/text/Office/PDF/media/malware/archive capability、tool
  runner、外部资源禁用实现、完整容器 sandbox、tmpfs materializer、updater binary/
  bundle、Worker image、Compose/profile、CI image publish、enhanced preview UI。
- Child 12 export、Child 13 recovery、Child 14 lifecycle/retention/purge scheduler、
  Child 15 GA/feature enablement/legacy removal。
- `000068...000071`、Provider byte mutation、新 Provider command、Worker direct
  Provider/repository access、Command Provider support。
- 前端 route、主 bundle、i18n、preview panel、workspace action 或 typed public
  preview-job API。Child 9 主 bundle 余量不得在本 Child 消耗。
- README/public deployment docs、release notes、version bump、release/deploy mutation、
  PR #386 或任何 GitHub/Docker publish。
- Worker 主动回连任意 URL、Worker-to-Worker DAG、Redis/Kafka、共享可写 host
  directory、明文 Derived blob 或普通 temp fallback。

## 6. Acceptance Criteria（实施后才可勾选）

- [ ] SQLite 与真实 PostgreSQL 均完成 `000067` apply/down、closed CHECK/FK/index/
  UTC/model parity；down 在 processing/derived/key/lease state 未安全清空时失败关闭。
- [ ] 父 Section 11 的验证命令已明确使用 `BackupAssetMigration067`，没有遗留
  `Migration066` typo；`000068...000071` 不存在改动。
- [ ] 完整 ProcessingState/DerivedArtifactState transition matrix、revision CAS、
  terminal/retry/error-category invariants 在两数据库行为一致。
- [ ] work-key difference matrix 证明 size/codec/page/quality/limit/profile/schema/
  pipeline/policy/source 任一变化不合并，并发相同请求只产生一个 current job。
- [ ] interactive/background reserve、interest coalescing、priority recompute、final-
  interest cancel 和无 Worker informational behavior 有 race tests。
- [ ] pull lease、heartbeat、crash/takeover/new fence、old attempt、late output、lease
  loss、cancel、supersede、expiry、source revalidation 全部失败关闭。
- [ ] local peer identity 与 opt-in mTLS trust、remote-disabled default、protocol
  compatibility、draining/quarantine、rate/body/strict decode 和 payload secret scan 有
  测试。
- [ ] Input/Sink activation one-use/binding/TTL，多 Range atomic budget，bounded atomic
  multi-artifact manifest、MIME/count/size/digest/completeness/policy/fence 校验有测试。
- [ ] per-blob random DEK、independent Derived KEK、authenticated chunks、tamper/wrong
  chunk/AAD、cross-RP refcount、last-reference destruction、KEK rotation/rewrap、orphan
  reconciliation 和 quota tests 通过。
- [ ] revoke/expiry/key-loss/rollback 的 Search-first fault-injection tests 证明任何
  crash point都不留下可命中但不可验证/解密的 ghost projection。
- [ ] protocol-only `asset-worker` 用 fake/no-op fixture 证明 pull/heartbeat/cancel/
  upload/manifest/graceful shutdown；Core server binary 没有重工具依赖。
- [ ] admin handler 的 Admin/Operator/Viewer、feature-disabled、sanitization、rate/body
  和 Swagger coverage 通过；没有 frontend/product UI 文件变化。
- [ ] `go test -race` focused suites、backend full tests/build、asset-worker build、
  Swagger regeneration/diff、`git diff --check` 和 exact scope scan 通过。
- [ ] 任何未实际运行的 live/integration/CI gate 均记录 `not_executed`，不能写
  `pass`、`passed` 或等价成功表述。

## 7. 已批准的规划处置与实施前门禁

研究现行代码、父任务和已归档 Children 7-9 后，没有遗留需要用户选择的产品分叉。
用户于 2026-07-19 整体批准以下 focused design 处置：

1. Local transport 使用同 UID Unix socket peer identity；remote 使用显式 opt-in
   TLS 1.3 mTLS URI-SAN trust domain，默认不监听。
2. Worker Input 由 `content` package 新增 attempt-bound internal Broker facet，
   processing 永不持有 `repository.Service`/`SourceResolver`/Provider registry。
3. Child 10 只暴露 sanitized admin health GET；不新增 preview job/public mutation/UI。
4. Full container/network sandbox 和真实能力继续留给 Child 11；Child 10 只冻结协议
   不泄密，并以 fake/no-op 测试客户端证明控制面。

如果总控/用户要求改变上述任一边界，应先修订三份规划文档并重新校验；不得在 Phase 2
中以 implementation discovery 静默扩大 manifest。

## 8. 当前执行状态

| 项目 | 状态 |
|---|---|
| Baseline/fetch/branch/task creation | executed（证据见 research） |
| Phase 1 current-main research | executed |
| `prd.md` / `design.md` / `implement.md` | approved 2026-07-19; implementation gate unopened |
| implementation authorization | approved by user 2026-07-19; workflow transition pending |
| `task.py start` | not_executed / blocked by active planning workflow state |
| 产品代码、migration、tests | not_executed |
| stage / commit / push / PR | not_executed |
| CI / merge / post-merge / Release Please / deploy | not_executed |
