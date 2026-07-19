# 备份资产内容平面

## 1. 文档状态

- Child：`backup-assets-content-plane`，父任务第 8/15 个实施 Child；当前真实
  program 进度基线为 7/15，父任务继续是 `planning` tracker，不得归档。
- 当前阶段：focused 规划、两次 implementation amendment、`task.py start` 与
  inline TDD 产品实施均已完成；Phase 2.2 fresh focused/race/SQLite/真实
  PostgreSQL 18/frontend/Nginx/Swagger/Docker/full/security 验证已通过，正在执行
  exact staging 与交付流程。历史 Amendment A/B 门禁和修订理由保留在下文及
  research evidence 中。
- Trellis task：`.trellis/tasks/07-18-backup-assets-content-plane`，状态
  `in_progress`。
- Git：`codex/backup-assets-content-plane`，PR base `main`，工作树
  `/home/murray/code/xirang`，基线
  `a3c309a922d9a4f48cb82031031c0975c251f5f4`。
- 依赖：Child 2、6、7 均已从该 merged main 满足；不依赖未合并 sibling。
- 授权记录：用户于 2026-07-18 明确“批准”完整
  `prd.md + design.md + implement.md`，并随后明确“启动并实施 Child 8”，已打开
  `task.py start` 与产品实施门禁。后续 commit/archive/journal/push/PR/CI/merge/
  post-merge 仍须按 `implement.md` 的验证与交付顺序执行。
- Amendment A 不是原批准的默示延伸；用户已于 2026-07-18 再次明确“批准”，
  新增 Provider/Repository/source-contract 产品文件现可按 TDD 实施。
- Amendment B 同样不是原批准的默示延伸。其设计提案与完整书面
  `prd.md + design.md + implement.md + research evidence` 均已获用户明确批准。
  它只新增 Content lease 两个文件，并恢复四个 manifest 外的并发代理改动；
  详见 `research/implementation-amendment-b-evidence.md`。
- Child 1-14 的 `backup_assets.enabled` 继续默认 `false`；Command Provider
  继续 typed unsupported。

详细 current-main 证据见 `research/current-main-evidence.md`。本 PRD 与
`design.md`、`implement.md` 必须整体批准，单独批准其中一份不打开实施门禁。

## 2. Goal

在不把 JWT/秘密放入 URL、不暴露 Provider 凭据、不伪造 Range、也不落地明文
缓存的前提下，为 Child 6 已授权的复合 `AssetRef` 提供短时、可撤销、有预算、
可审计的内容预览与原件下载通道。Core 必须在无 Worker、无 Derived Store、缓存
禁用、Provider 无 Range 或仓库暂时离线时给出诚实降级，而不是扩大权限或无限
读取。

## 3. 用户价值与成功定义

- 已具备 `backup_assets:preview` 且拥有精确 RecoveryPoint 的 Admin/Operator
  可以预览已证明为非敏感的安全文本、raster、PDF、音视频或 metadata/hex
  fallback，无需重复 step-up。
- `secret|unknown` 内容只有精确 `asset.secret_reveal` proof 才能揭示；原件下载
  每次签票只有精确 `asset.download` proof 才能进行。两个 purpose 及其他所有
  purpose 互不替代。
- 浏览器原生图片/音视频/PDF 使用两段式交付：正常 Authorization JSON API
  签票，随后仅以非授权 `delivery_id` URL + 精确 path-scoped HttpOnly cookie
  获取内容。URL、日志、审计和浏览器存储中没有 JWT/cookie secret。
- Range、并发、字节、请求数、idle/absolute TTL、RecoveryPoint lease 和可变源
  指纹都被服务端原子约束；取消、退出登录、权限/所有权/分类/源变化和 shutdown
  能终止或拒绝旧会话。
- Provider 无 Range 时，Core 只在预算内使用 sequential preview 或完成认证
  分块物化；否则明确返回“仅顺序预览/仅下载/仅恢复”等 capability reason。
- Content Broker 是所有内容消费者的唯一入口。API、未来 Worker 和前端不能
  绕过它拿 Provider locator、凭据、SSH/runner 或任意路径。

## 4. Confirmed current-main facts

1. Child 2 已提供 bounded `SequentialReader` 与可选 `RangeReader`；Rsync/Rclone
   有已证明 Range，Restic 仅 sequential。reader `Close` 承担进程 join 和源后验
   校验。
2. `repository.Service` 已是唯一凭据/Provider resolution boundary，但尚无
   content read adapter；Content 不得直接依赖 Provider registry。
3. Child 6 的 active complete Catalog、复合 Catalog FK 和
   `catalog.Ownership.AuthorizedPointIDs` 是资产/所有权真相。
4. Child 7 的 `ContentIndexIngest` 只接受 exact ProcessingJob lease/fence；Child
   8 没有合法 caller，不得写 Search postings/classification/excerpt。
5. `asset.secret_reveal`、`asset.download` 及 content audit action 已存在；本
   Child 复用，不扩 action registry 或 credential grants。
6. JWT 有 JTI、expiry、role、`token_version` 和持久撤销；但 cookie-only route
   尚无 safe session binding/logout cancellation seam。
7. `content_session` lease holder 已存在；`000066` 不需要修改 lease closed set。
   父合同只要求 multi-stage publication 的新 fence 继承同一显式 point deadline；
   Catalog/Search/Content 等独立 producer 使用现行 zero-deadline acquisition
   得到各自有界 holder deadline。Content grant 的 absolute expiry 取
   session/proof/profile/返回 lease deadline 的最小值，并通常远早于 lease deadline。
8. backend 全局 `WriteTimeout=30s`；Nginx 仅有 generic `/api/v1/` route，且应用
   StructuredLogger 会记录 raw path。
9. paired migration 当前最高 `000065`；`000066` 在两引擎均空闲并专属 Child 8，
   `000067-000071` 保留。
10. Rclone portable 内容可复用 registry reader；native object-version locator
    必须在 Repository 内重建 exact native request，并使用现有
    `RcloneNativeExactReader`/`RcloneNativeExactRangeReader`，不能把它误当成 generic
    Rclone registry read。
11. 现有 append-only audit writer 对 unique violation 重试后返回错误。Child 8 的
    adapter 必须在失败后读取并比对 exact existing `(grant_id, action)` event；仅完全
    匹配时视为幂等成功，任何碰撞/不一致继续失败关闭。
12. Provider 的 `boundedReadHandle` 在恰好读满 limit 时会额外消费一个不可见的
    overflow probe byte；当前 `ReadHandle` 与 Repository wrapper 不暴露该计量，
    无法满足冻结的 actual Provider-byte accounting。
13. 现行 `000066` grant 只有 `source_size`，不能冻结 escaped text/hex 的真实
    representation Content-Length 与 truncation 状态。
14. 签票 POST 受全局 `WriteTimeout=30s` 约束；Provider scan 必须有严格小于
    30s 的 hard deadline，且大型全对象 cache materialization 不得同步发生在签票内。
15. 当前全局 CORS 会在 route middleware 前处理 OPTIONS；Gin 默认 trailing-slash
    redirect 也可能绕过 content-local recovery/origin/log policy。
16. Nginx generic proxy 的 `Host $host` 会丢失 `:10761`，且 inner `$scheme` 会覆盖
    外层 TLS proto；asset-content exact route 需要闭合 effective origin evidence。
17. usage ledger 的 scope request counter 尚无 user/Provider/global window maxima；
    cache settings 也缺少完整的 object/user/Provider file 与 memory scope limits。
18. 上述实现期证据和最小修订见
    `research/implementation-amendment-a-evidence.md`；它扩大原 manifest 的精确
    Provider/Repository 文件，必须 focused approval 后才能实施。
19. Task 3 并发代理曾把 Content lease 错误绑定到历史 publication deadline；旧的
    committed RecoveryPoint 可能早已超过该 deadline，因此会被永久拒绝。Focused
    Amendment B 恢复 root LeaseService 现行语义，冻结独立 content-session lease、
    admission-before-decryption、单 token transfer 和有界 close-time revalidation。

## 5. In scope

### 5.1 Typed delivery grants

- 服务器生成内部 `grant_id`、公开非授权 `delivery_id` 和随机 cookie secret；
  DB 只保存 secret 的 SHA-256 hash。
- 每个 grant 恰好绑定一个 typed resource。Child 8 只接受
  `backup_asset AssetRef{recovery_point_id, entry_id}`。
- Schema 预留 nullable `RecoveryResultRef` 列，但 `resource_kind` SQL/service
  closed set 在 `000066` 仅允许 `backup_asset`；无 Child 13 FK、route 或 adapter。
- Grant 绑定 user、JWT session JTI/version/role/expiry、action、resource、exact
  Catalog generation、method/range policy、renderer/profile、classification、
  source fingerprint、lease、TTL 和 budgets。
- malformed、dual、empty、unknown resource；非法 action/renderer/profile/range
  closed product；或任何 cross-purpose proof 整体失败关闭。

### 5.2 Cookie and browser delivery

- Issuance 在 secured Authorization route；content route 仅接受 URL 中的
  `delivery_id` 和恰好一个同名 cookie，不接受 Authorization/JWT/query secret。
- Cookie：HttpOnly、SameSite=Strict、host-only（不设置 Domain）、精确
  `/api/v1/asset-content/<delivery_id>` Path；生产必须 Secure。
- HTTP 开发仅在显式设置且 RemoteAddr/Host/scheme 全部证明是受控 loopback 时
  允许 insecure cookie。否则客户端只能使用后续 bounded Blob fallback；本
  Child 不实现该 UI fallback，更不能把 secret/JWT 放入 URL。
- 重名 cookie、非法编码、过长值、delivery/cookie 不匹配、cookie fixation 和
  client-chosen ID 全部拒绝。

### 5.3 Authorization, step-up, revocation

- Viewer 在读取 Provider/分类/创建 grant/设置 cookie 之前返回 403。
- 每次 issuance、GET/HEAD 和长流 heartbeat 都重新检查 feature gate、当前用户、
  JWT revocation/token version/role、RBAC、producing-lineage ownership、active
  Catalog generation、RecoveryPoint state、classification/source binding 和
  lease fence。
- 普通 non-secret preview 不要求 proof。
- secret/unknown preview 只接受未过期 exact `asset.secret_reveal`。
- original download 每次创建 grant 只接受未过期 exact `asset.download`；不能
  叠加或替换为 secret reveal/recover/export/result proof。
- logout 主动 revoke/cancel 同 JTI grant；即使回调失败，JWT 撤销检查仍立即
  拒绝下一次 request/heartbeat。
- permission/ownership/classification revision、point expiring/retired/purge、
  expiry/idle、feature disable、source change、lease loss、budget exhaustion、
  process restart 和 shutdown 都产生 typed revoke/fail-closed 状态。

### 5.4 Lease and Broker source boundary

- 签票尝试先以 zero explicit deadline 取得独立 `content_session` RecoveryPoint
  short lease；现行 LeaseService 给出有界 holder deadline，grant absolute deadline
  不超过返回的 lease deadline，但不复用历史 publication deadline。
- 活跃请求定期 heartbeat；renew/release/validate 使用完整 attempt/fence。旧进程
  fence 在 takeover/restart 后永不复用。
- `content.Broker` 只接受复合 opaque AssetRef。`repository.Service` 的窄
  `SourceResolver` 在服务端解析 encrypted Provider locator/access 和精确 point
  locator，返回 closeable source session，不返回秘密或 native path。
- SourceResolver 在任何 Catalog/access model hook 解密或 Provider port 前取得恰好
  一个 `content_read` admission token；managed Rsync/native Rclone 只转移该 token，
  不嵌套 acquire。close-time revalidation 使用 cancellation-detached 但同时受原
  request deadline 与 5s cleanup ceiling 约束的 context。
- 仅允许 immutable `committed|degraded` 或
  `PointObserved + SemanticsMutableHead`。
- Mutable source 在 ticket classification/open、每个 Range request 前后和关闭
  reader 时重验 exact fingerprint；变化即撤销 grant、清 cache generation 并
  返回 stable `source_changed`。
- 新增 admission ledger 值 `content_read`，复用现有 Provider commands。不得新增
  Provider mutation/command 或让 Content 持有 runner/SSH/process dependency。

### 5.5 HTTP GET/HEAD/Range and budgets

- 支持 HEAD、full GET，以及恰好一个 normal/open-ended/suffix byte Range；
  multipart/malformed/out-of-bounds 返回冻结的 416 语义。
- 冻结 200/206/416、`Content-Range`、`Content-Length`、`If-Range`、ETag、
  `Last-Modified`、`Accept-Ranges` 和 HEAD 无 body 行为。
- Range 只在真实 Provider Range 或完整验证后的 authenticated cache 上声明；
  sequential source 不伪造 seek。
- 每 request 在打开 source 前原子 reserve request count、worst-case bytes 和
  grant/user/provider/global in-flight slot。完成/取消后按 reader bytes 与成功
  response bytes 的较大者计费；崩溃/未知结果保守收取全部 reservation。
- Provider handle 必须报告包括内部 overflow probe 在内的实际读取字节；Broker
  同时计 visible reads 并取两者最大值。reservation 必须预留可能的 probe overhead，
  不能观测最终计量时收取全部 reservation。
- Grant request/cumulative bytes、request count、max in-flight 及 user/provider/
  global concurrency/window byte+request budgets 均不可超卖；SQLite/PostgreSQL
  并发结果一致。
- idle refresh 只在已认证并成功 reserve 的 activity 上发生，且永不超过
  absolute TTL。HEAD/失败 Range 也消耗 request count，不能被用于无限续租。
- 客户端取消、writer error、deadline、lease/source/authorization heartbeat
  失败必须传播 cancel，close/join source 并原子释放 in-flight reservation。

### 5.6 Authenticated cache and bounded materialization

- small/sequential preview 优先 bounded memory；large random access 仅使用
  per-process random key 的 AEAD authenticated chunk cache。
- 每 chunk 随机 nonce；AAD 绑定 format/process generation、RecoveryPoint、
  entry、source/content fingerprint、chunk index 和 plaintext length。
- tamper、wrong generation/resource/chunk/key、truncation 和 duplicate nonce
  detection 均失败关闭。
- Cache root 必须是 dedicated absolute root，不能位于或解析到 `/data`、
  `/backup`、`/logs`、任何 backup source 或其父/子路径；拒绝 symlink/bind/
  traversal/special-file escape，并通过 `os.Root` containment 操作。
- disk cache disabled/full/unsafe/unsupported 时明确降级；无 plaintext disk、
  generic temp dir 或整值 `secure.EncryptString` fallback。
- per-object/user/Provider/global bytes/files、memory、idle/absolute TTL 和 active
  cache lease 配额必须在写前 reserve。LRU 只是无 lease entry 的候选策略。
- Core cache 按 owner partition；不同 user 不共享物化对象。owner 加入 opaque
  identity/AAD，同 user 也只有 exact resource/source/representation generation
  才可复用。
- startup 删除旧 process generations；periodic reconciliation 删除 orphan/
  partial/expired ciphertext；shutdown 清 key 并 best-effort 删除本 generation。
- Worker 专属 tmpfs/path materialization 属于 Child 10-11，不在本 Child。

### 5.7 Classification and core renderer policy

- 在任何 text reveal、native content preview、hex projection 或未来 content
  projection 前，以 versioned path/name/MIME/config rules + bounded content
  sniff/scan 生成 closed `secret|non_secret|unknown`；error/inconclusive 为
  unknown，unknown 按 secret 授权。
- Child 7 active classification 只能在 exact generation/source/revision 下提高
  风险，不能替代 bounded scan、降低 secret/unknown 或被 Child 8 写回。
- Core renderer closed set：bounded safe text/config/log、safe raster、reviewed
  same-origin PDF、native audio/video、metadata/hex fallback、attachment download。
- HTML/XML/SVG/active image/scriptable content 永不以同 origin active-inline MIME
  交付；可以 escaped source/hex/attachment。
- MIME 以 magic/sniff + renderer policy 为准；扩展名与 Provider metadata 只是
  hint。raster 必须通过 bounded header/pixel checks。
- 固定 `nosniff`、same-origin resource、no-referrer、严格 CSP/sandbox/frame/
  object policy；Content-Disposition filename 去 path/CRLF/control/bidi，使用
  safe ASCII fallback + RFC 5987 encoding。
- Parser/malformed PDF/media/image 风险以 closed MIME、size/budget、browser
  isolation和 fallback 限制；Child 8 不调用外部 parser/codec/office/archive 工具。

### 5.8 Handler, audit, logs, Nginx and frontend boundary

- Handler 只做 strict bind/session/proof adapter、调用 Broker、映射 sentinel、
  设置 cookie/HTTP headers/stream；无 Provider/runner/SSH/process/SQL business
  logic。
- Issuance 使用 response helpers；raw content route 是明确的 binary HTTP 例外，
  不发 JSON envelope 或 secret-bearing error body。
- Typed audit 覆盖 preview ticket/read 与 download ticket/download 的 success/
  blocked/failure。Range 按 grant 聚合；audit 写失败不阻塞已授权 content read，
  但进入 bounded retry/reconciliation metric。Ticket issuance audit 失败则不发
  cookie并撤销 grant。
- Audit/log 不记录 raw path/name/query/content/public delivery ID/cookie/JWT/
  locator。资产 path 只可在 AuditWriter 内存输入后变为 keyed fingerprint。
- StructuredLogger 将 content path 归一成固定 route label；Content 自有日志只
  使用 per-process keyed delivery fingerprint。
- Content route 使用内层 safe recovery，panic 时不触发 Gin 对 raw URI/header/
  cookie 的通用 request dump；Nginx content location 不继承无法自定义格式的
  error log。
- Nginx exact content route 关闭 buffering/cache/temp-file/gzip，使用 bounded
  timeout 和专用 redacted log format；generic API timeout、10761、TLS/image
  contract不变。
- Content-shaped path 在全局 CORS 前分流；OPTIONS、unsupported method 与 trailing
  slash 均走 content-safe rejection，不得由全局 preflight/redirect 短路。Nginx
  content route 保留外部 Host port 与 closed effective proto 供 same-origin 校验。
- Frontend 仅增加 raw DTO、closed mapper、domain unions 和 ticket API tests。
  URL 作为 opaque same-origin/query-free string；无 page/component/router/hook/
  storage/full preview UI。

## 6. Out of scope

- Child 9 `/app/backups` workspace、preview panel、media element、route/deep link、
  layout、a11y/i18n 和 Blob fallback UI。
- Child 10 Processing queue、Worker grant、Derived Store、persistent wrapped DEK、
  Worker tmpfs/path materialization。
- Child 11 OCR/thumbnail/Office/PDF rasterization/media transcoding/archive parser、
  malware engine、content postings/excerpt ciphertext/enhanced renderer UI。
- Child 12 export/archive member delivery，Child 13 RecoveryPlan/Job/result adapter，
  Child 14 retention/purge owner，Child 15 GA enablement/legacy removal。
- `RecoveryResult` ticket/read；在 Child 13 注册 exact job-owner +
  `recovery.result_download` adapter 前永远 stable unsupported。
- Provider byte write/move/delete/version/restore、new command、arbitrary path read，
  Command Provider support。
- public share link、cross-origin content、JWT/cookie secret in URL、persistent
  browser ticket storage。
- 占用、重排或修改 `000067-000071`。
- 自行合并 Release Please PR #386、创建 release 或发布 Docker image。

## 7. Functional requirements

### FR-1 Closed typed request and resource

1. Issuance payload schema version、action、renderer/profile 和可选 proof header
   是 closed contract；unknown field 由 strict JSON 拒绝。
2. URI 的 `rpId/entryId` 与内部 typed `AssetRef` 必须一致，不能提交 second
   resource、native path、repository credential 或 `RecoveryResultRef`。
3. Service-level constructor/validator仍测试 empty/dual/unknown resource，防止未来
   internal caller绕过 HTTP shape。
4. `delivery_id` 与 cookie secret 均由 CSPRNG 生成；客户端输入不能固定/复用。
5. Grant 冻结 `representation_source_bytes`、`representation_size` 和 closed
   `representation_truncated`；DTO/HEAD/GET 的 Content-Length 只取 representation
   size，不能用 source size 猜测转换后长度。

### FR-2 Ticket state and session binding

1. Grant state transition只允许
   `issued -> active -> draining -> closed` 或 active/issued 到
   `revoked|expired`；terminal 不可恢复。
2. Cookie 只在 ticket audit 成功且 grant 已 active 后写入响应。
3. Session expiry、proof expiry和grant TTL取最早边界；secret/download ticket不
   能在 proof 过期后继续揭示。
4. 新请求发现任何 binding drift 时先 revoke/cancel，再返回 generic typed failure。

### FR-3 Source identity and availability

1. SourceResolver 在一个调用内重载 exact active Catalog generation/entry/point
   和 encrypted locator；entry-only lookup、旧 generation 或 cross-point FK 均拒绝。
2. Immutable source必须有不可变 point contract；mutable head必须在 open 前后及
   Range boundary重验 root/object/source fingerprint。
3. Provider stat size/revision 与 grant binding 不一致时不能调整 grant 猜测继续，
   必须 supersede/revoke并重新签票。
4. repository offline/disconnected、feature disabled、Command、no sequential、no
   Range/cache等返回 stable capability reason，不泄露 Provider error/raw stderr。

### FR-4 HTTP representation

1. Renderer/profile是representation identity的一部分；ETag绑定 resource、source、
   renderer/profile 和 classification policy revision。
2. 强 byte identity才发 strong ETag；弱/可变 identity发 weak ETag，弱 ETag不
   满足 If-Range strong comparison。
3. If-Range mismatch按HTTP语义尝试 full 200；若full不在ticket/budget合同内则
   返回 412，不静默发另一段 partial content。
4. 416必须包含 `Content-Range: bytes */<size>`；206必须包含精确 inclusive range
   和 length；HEAD返回同status/headers但0 body。
5. `Accept-Ranges: bytes`仅用于真实seek，其他为`none`；multipart永不物化/拼接。

### FR-5 Atomic accounting

1. 同一 DB transaction 以稳定顺序锁定/更新 global、Provider、user、grant，创建
   request reservation并增加request count/in-flight/reserved bytes。
2. 任一scope失败则整个reservation回滚；没有“source已打开但budget未占用”窗口。
3. Finish transaction只能匹配exact request/grant version和active fence，且幂等；
   duplicate finalize/cancel/replay不能释放两次或负计数。
4. Unknown crash reservation按worst case计费并由reconciler关闭；不能通过kill/
   disconnect回收未用quota后重放放大读取。
5. SQLite与PostgreSQL真实并发fixture必须证明总成功reservation不超过每个limit。
6. user/Provider/global scope 的 window request maxima 与 byte maxima 同 transaction
   生效；usage `request_count` 不得成为只记录不限制的指标。

### FR-6 Cache confidentiality/integrity

1. Process key、log fingerprint key和cookie secret是独立随机域；任何一个都不
   复用Search/Entry/Cursor/Audit/KEK。
2. Cache chunk文件名 opaque且不含user/RP/entry/path/name/fingerprint；文件内容
   只有versioned ciphertext header/nonce/tag。
3. 对象仅在全部预期chunk写入、source post-validation和manifest commit完成后
   对Range reader可见；partial generation永不可读。
4. Restart无法解密旧generation并必须删除；删除失败时disk cache保持disabled。
5. object/user/Provider/global 的 bytes/files 与 memory object/user/Provider/global
   配额在写前闭合；cross-user cache hit 永远不存在。

### FR-7 Classification and representation coupling

1. `non_secret` only在core bounded policy给出positive result且无explicit secret
   evidence时成立；scan读取不足、错误、未知binary/encoding均为unknown。
2. Search `secret`可提升core结果；Search `non_secret`不能覆盖core secret/unknown。
3. Preview action与renderer/profile/content type/disposition/range/step-up组合由单一
   closed policy validator验证；非法组合不能字段级fallback。
4. Download永远attachment；active content永远escaped/hex/attachment；PDF/media
   不支持/不安全时降级，不调用计划外parser。

### FR-8 Lifecycle and audit

1. Startup在开放route前revoke previous-process grants、拒绝旧fence、reconcile
   request reservations/audit pending/cache orphan和expired leases。
2. Shutdown先停止新ticket/request，revoke/cancel/close/join，flush/reconcile
   audit，release lease，删除cache generation，再让admission drain完成。
3. Read audit以internal grant ID幂等聚合 Range count/bytes/outcome；public
   delivery ID从不进入audit。
4. Audit backlog有上限、retry/backoff和metric；达到上限时停止新ticket而不是
   丢弃无限审计。已开始的合法read仍由budget/lease安全结束。

## 8. Non-functional requirements

- **Security:** fail closed on ambiguity; constant-time cookie hash comparison;
  no bearer URL, raw content metadata logs, path traversal/symlink escape or
  plaintext disk fallback.
- **Portability:** SQLite/PostgreSQL migration and behavior parity; HTTP
  semantics independent of Provider and database engine.
- **Boundedness:** every request, scan, buffer, cache, TTL, concurrency,
  reconciliation batch, retry queue and deadline has a validated upper bound.
- **Cancellation:** no detached reader/process/goroutine after client cancel,
  lease loss, source drift, shutdown or writer error.
- **Observability:** bounded safe counters/reason codes and keyed identifiers;
  no path/name/query/content/JWT/cookie/locator labels.
- **Compatibility:** no global timeout/port/TLS/image/release-source change;
  feature default false; old binary tolerates additive tables until an explicit
  down/drain decision.

## 9. Acceptance criteria

- [x] Paired SQLite/PostgreSQL `000066_backup_asset_content` applies from real
  `000065`, preserves prior rows/contracts, rejects invalid/dual/cross-point
  rows, passes UTC/model parity and pristine down to exactly `000065`.
- [x] Used down is atomic and unchanged for every grant/request/usage family and
  any `content_session` lease; explicit safe-drain tests prove the only allowed
  path to an empty down state. `000065` used-down defenses remain intact.
- [x] Only `backup_asset` resource rows can exist; RecoveryResult columns are
  null/no-FK/service-disabled and internal/HTTP recovery attempts return stable
  unsupported. `000067-000071` remain untouched.
- [x] Ticket tests prove random ID/secret, hash-only storage, exact one-resource
  binding, no URL bearer, cookie host/path/HttpOnly/Strict/Secure policy,
  duplicate-cookie rejection and controlled loopback behavior.
- [x] Grant/migration tests freeze representation source bytes/size/truncation
  as one closed product; text/hex Content-Length never aliases source size and
  raw renderers cannot claim truncation.
- [x] Auth tests prove Viewer pre-ticket 403; Admin/Operator ownership; nonsecret
  no-step-up preview; exact secret/download proofs; complete cross-purpose
  rejection; logout/token-version/role/ownership/classification/point/expiry
  revocation before the next byte boundary.
- [x] Broker/repository tests prove exact active composite lookup, no exposed
  locator/credential, allowed point states, Command unsupported, mutable
  before/after/boundary checks, cancel propagation and reader close/join.
- [x] Lease tests prove Content uses a fresh bounded zero-explicit-deadline
  holder lease rather than a historical publication deadline, keeps the grant
  expiry separately shorter, heartbeats/releases exact fences, and permits only
  expired cleanup takeover. Root LeaseService behavior remains unchanged.
- [x] Admission tests prove rejection happens before Catalog/access decryption
  or Provider use, every source path consumes exactly one token, and close-time
  source validation has a finite deadline no later than the request deadline.
- [x] Provider accounting tests prove visible reads plus hidden limit-probe bytes
  survive invariant/Repository wrappers; unknown/failure conservatively charges
  the reservation and no byte is read before atomic reserve.
- [x] HTTP tests freeze HEAD/full/single normal/open/suffix Range, multipart and
  malformed rejection, 200/206/416/412, Content-Range/Length, If-Range,
  strong/weak ETag, Last-Modified, Accept-Ranges and HEAD zero-body behavior.
- [x] SQLite and real PostgreSQL race/behavior tests prove atomic request/
  cumulative/in-flight/user/provider/global budgets under concurrent seek,
  replay, cancel, crash reconciliation and out-of-bounds requests.
- [x] No-Range fixtures prove sequential preview, bounded authenticated
  materialization, only-download/only-recovery reasons and zero fake seek or
  unbounded full reads.
- [x] Cache crypto tests cover tamper, wrong resource/generation/chunk/key/AAD,
  orphan/key-loss restart, quota/files/TTL/lease, full/disabled root and no
  plaintext/symlink/source-root escape.
- [x] Classification/renderer tests cover path/name/MIME/config signals,
  bounded scan/error/unknown, active HTML/XML/SVG, MIME confusion, raster pixel
  limits, malformed PDF/media and every coupled illegal product.
- [x] Handler/router tests prove issuance Authorization vs content cookie-only,
  strict JSON/no query, thin dependency boundary, generic errors, CORS/CSRF/
  Fetch-Metadata/CORP headers and route-specific response deadlines.
- [x] Ticket handler timeout is <=25s and strictly below the unchanged global
  30s WriteTimeout; timeout releases provisional state and no ticket POST performs
  large full-object disk materialization.
- [x] Audit/logger tests prove typed ticket/read/download outcomes, Range
  aggregation/retry, path keyed fingerprint only, and absence of raw
  path/name/query/content/public delivery ID/cookie/JWT/locator from audit,
  structured logs and failures.
- [x] Rendered Nginx tests prove exact content location, fully redacted log
  format, buffering/cache/temp/gzip off, bounded timeouts and unchanged generic
  route/10761/TLS/image contract, plus external Host port/effective-proto
  preservation; Docker build passes.
- [x] Frontend mapper tests use private raw DTOs, snake-to-camel mapping, closed
  enums/coupled validation and opaque safe content URL; no page/component/
  router/storage changes. `env -u NODE_ENV npm run check` passes.
- [x] Swagger is regenerated and focused/race/SQLite/PostgreSQL/Nginx/frontend/
  Docker/full gates in `implement.md` pass before staging.
- [ ] Exact staging contains only the approved Child 8 manifest and workflow
  Trellis files; work commit -> finish-work archive auto-commit -> concrete
  journal commit -> push -> one PR -> required CI green -> squash merge ->
  post-merge monitoring -> local main sync/branch hygiene completes.

## 10. Parent coverage and focused deviations

| Parent contract | Child 8 coverage | Focused decision |
|---|---|---|
| Implement Section 9 | Full | Keeps `000066`, Broker, ticket, Range, cache, renderer, handler/Nginx/frontend boundary |
| Design Section 6 | Full | Separates internal grant ID from public delivery ID and adds atomic request/usage ledgers |
| Design Sections 8-9 | Full | Reuses already-merged exact step-up/audit registries instead of modifying them again |
| Design Section 17 | Full | Uses specified issuance/content routes; no RecoveryResult route |
| Design Sections 18-21 | Full | Adds settings, metrics, dual-engine/race/security/Nginx/Docker gates |
| High-risk gates | Full | Ticket cookie, chunk cache, leases, composite identity and ordered migration get focused review |

Current-main-based deviations from the parent's provisional file list are
intentional and require approval with this package:

1. `step_up.go` and credential-grant files are unchanged because Child 7 already
   merged exact actions/verifiers; Child 8 adds only a content adapter/test.
2. `auth/jwt`, auth middleware and logout handler are added to the manifest
   because a cookie-only route otherwise cannot enforce current session
   revocation or cancel active grants.
3. `publication/contracts.go` gains only `content_read` admission, not a new
   Provider command.
4. Dockerfile, Nginx README and rendered-config scripts are included because a
   safe dedicated cache root/log policy cannot be proven by template text alone.
5. No Search ingest write, keyring domain, Worker/Derived/UI/export/recovery/
   lifecycle implementation is added.
6. Focused Amendment A adds only a read-only Provider byte meter and forwards it
   through existing wrappers; exact new mutable files are frozen in
   `implement.md` Section 2.5. It adds no Provider command or mutation.
7. Cache is owner-partitioned rather than cross-user shared, closing per-user
   quotas without introducing a durable shared-DEK/cache ownership model.
8. Focused Amendment B adds only `content/lease.go` and `content/lease_test.go`;
   Content gets an independent zero-explicit-deadline holder lease. It restores
   manifest-external root lease/Repository service/shared Rclone fake edits and
   keeps the exact native-read test local to Child 8 files.

No migration reservation change is needed. If `origin/main` advances or
`000066` becomes occupied before approved implementation, stop and return to
the parent migration gate; do not self-renumber.

## 11. Phase ledger

| Activity | Phase 1 status |
|---|---|
| focused task creation / branch / planning research | completed |
| `task.py start` | `executed`; status=`in_progress` |
| product implementation / migration DDL / tests | `completed`; Tasks 1-11 and seven final high-risk RED-GREEN fixes, including bounded invalid-lease rollback/close, are implemented within the approved manifest |
| focused Amendment A | `approved` 2026-07-18；evidence/artifact revision complete，新增 manifest 与剩余 GREEN 门禁已打开；三项 pre-discovery test edits 已披露 |
| focused Amendment B design + written package | `completed`；corrected implementation is in place and the four manifest-external edits match `origin/main` |
| focused validation commands from implementation plan | `completed`; fresh focused/race/SQLite/real PostgreSQL 18/frontend/Nginx/Swagger/Docker/full/security gates passed after final fixes |
| code-spec updates | `completed`; database/runtime/logging/frontend boundary contracts are executable and current |
| work commit / Trellis archive auto-commit / journal commit | `not_executed` |
| push / PR / CI / squash merge | `not_executed` |
| post-merge release/image/docs monitoring | `not_applicable` until merge |
| local main sync / branch cleanup | `pending` after merge |

This ledger is descriptive. No unchecked acceptance criterion is a pass.
Focused planning and implementation/start were explicitly approved on
2026-07-18; only completed commands may be recorded as passing.
