# 备份资产搜索与用户覆盖

## 1. 文档状态

- Child：`backup-assets-search-overlays`，父任务第 7/15 个实施 Child；现行
  项目真实交付进度基线为 6/15。
- 父任务：`.trellis/tasks/07-12-backup-data-explorer-design`，继续保持
  `planning` program tracker；本 Child 绝不归档父任务。
- 当前阶段：Phase 3 pre-commit verification；Tasks 1-11 已在本地完成，Task
  仍为 `in_progress`，尚未 stage/commit/archive/push。用户于 2026-07-18 先明确批准
  `prd.md + design.md + implement.md`，随后另行明确授权
  `task.py start` 与 implementation，Trellis task 已切换为 `in_progress`。
- 分支：`codex/backup-assets-search-overlays`；PR base：`main`；工作树：
  `/home/murray/code/xirang`。
- 基线：`8cd6e5184e7dd05f702c3a5762b013c67901a399`，与刷新后的
  `main`/`origin/main` 一致。
- 依赖：Child 6 PR #390 已合并，依赖满足；本 Child 不依赖任何未合并
  sibling。
- 功能门禁：Child 1-14 的 `backup_assets.enabled` 继续默认 `false`；
  Command Provider 继续 typed unsupported。
- 当前授权覆盖本 Child 已批准计划内的 inline 实现、测试与既定交付流程；不得
  扩张到 sibling、父任务归档、Release Please #386 或计划外 Provider/Content/UI。

## 2. Goal

在不读取或修改 Provider 字节的前提下，为 Child 6 已原子发布的 Catalog
建立 SQLite/PostgreSQL 语义一致、权限优先、覆盖度诚实的便携搜索投影，并交付
保存搜索、收藏、用户标签和最近访问等服务端用户覆盖。搜索与覆盖都使用复合
`AssetRef{recovery_point_id, entry_id}`，不把旧 Restic
`SnapshotFileIndex`、Provider path 或最近一次 TaskRun 当作资产真相。

## 3. 用户价值与成功定义

- Admin 和 Operator 能在其服务端授权范围内，以相同 Unicode、排名、稳定
  排序和 cursor 语义搜索名称、路径、扩展名、类型、修改时间和未来可选的
  正文/OCR 字段。
- `current` 永远指向每个已授权 producing lineage 的最新 committed point
  或稳定 `mutable_head`；最新点未索引时明确 partial/building，绝不回退到旧
  索引并宣称“没有结果”。
- Viewer、无权 Operator、缺少精确 step-up 的 secret/unknown 内容都不能
  通过结果、总数、建议、snippet、分组、cursor 或错误差异推断资产存在。
- 保存搜索、收藏、标签和最近访问属于用户控制面元数据，不写回 Provider、
  不改变 Catalog/manifest/可信证据，也不形成 retention hold。
- 后续 Child 10/11 能通过稳定 `ContentIndexIngest` 端口发布正文/OCR postings、
  field coverage 和 opaque encrypted-excerpt reference；Child 7 自身不创建
  excerpt ciphertext、正文、OCR 或派生物。

## 4. Confirmed current-main facts

详细证据见 `research/current-main-evidence.md`。当前主线已确认：

1. Child 6 已提供 active Catalog generation、composite AssetRef、producing
   lineage ownership、coverage/staleness/content availability、签名 cursor、
   runtime Catalog worker 和前端 mapper 边界。
2. paired migration 最高为 `000064`，`000065` 在两引擎均空闲；CI 已有真实
   PostgreSQL 18 service，但 regex/behavior job 尚未包含 Child 7。
3. `wrapped_domain_keys` 没有 `search_token`，`recovery_point_leases` 没有
   `search_index`；生产启动也尚未调用 `RewrapAll`。
4. `asset.secret_reveal` 五分钟 exact-purpose proof、资产权限和大部分 audit
   action 已存在，可复用；强制 step-up middleware 不适合“无 proof 仍允许
   metadata-only 搜索”的可选验证语义。
5. 当前唯一文件搜索是 legacy Restic `%LIKE% LIMIT 200` 路径；它继续保留到
   Child 15，但不得进入新搜索合同。
6. 现行主线没有 search/overlay schema、service、handler、runtime worker 或
   frontend DTO。

## 5. In Scope

### 5.1 Portable metadata search

- Go 层统一 Unicode NFKC、full case folding、slash/path segment、中文
  bigram、Latin token、扩展名和 UTC 日期规范化；数据库 collation/FTS 不是
  产品合同。
- 由独立 Search Token Key 生成 domain-separated HMAC token/gram postings，
  保存字段频率与便携确定性 sort data；数据库不保存 content/OCR/excerpt
  明文。
- Search projection 独立 staging/build/activate，绑定 exact active Catalog
  generation、RecoveryPoint source revision、normalizer version、Search Token
  key version 和 durable fence。
- SQLite/PostgreSQL 的 AST 求值、排名、分组、稳定分页、cursor stale、coverage
  和错误语义一致。

### 5.2 Versioned AST and scopes

- 初始 AST schema version 固定为 1；schema/op/field/value/time/scope 均为
  closed contract。
- 支持 `and | or | not | term | type | modified_time`；限制深度、节点、body
  bytes、每节点 values、单值字符/字节、候选数、执行时间和页大小。
- scope 支持 `current | all_retained | exact_points`，并可用 repository/task
  opaque filters 缩小；exact scope 必须非空且不能与动态 scope 字段混用。
- unknown schema/op/field、空 exact、非法组合、scope membership 漂移、active
  Catalog/Search generation 漂移、classification/coverage revision 漂移和过期
  cursor 全部失败关闭。

### 5.3 Authorization, classification, and coverage

- 固定 pipeline：authorization scope -> visible point/document candidates ->
  classification/exact step-up -> AST truth -> grouping/count/suggestions ->
  snippet/DTO。
- Viewer 在 handler/service 数据访问前被 `backup_assets:list` 拒绝。
- Operator 在共享 Repository 中仅能看到其 current producing lineage；一个
  owned Task 不授权 sibling lineage。
- 没有有效 `asset.secret_reveal` proof 时，`secret` 和 `unknown` 的
  content/OCR leaf 使用三值 `unknown`，不得贡献匹配、计数、建议、hit field
  或 snippet；download/recovery/purge/其他 step-up action 均不可替代。
- 名称/路径命中继续按 `backup_assets:list` 与 producing ownership 返回；敏感
  分类不能扩大权限。
- 每个响应返回 query/index generation、field hit、composite AssetRef、coverage、
  staleness、capability 和 server-evaluated permissions。coverage 不完整时
  `total` 是 `null` 或明确 lower-bound，`authoritative_empty=false`。

### 5.4 Future content ingest port

- 定义并测试 DB 原子 `ContentIndexIngest`：在一个事务中重验 active source、
  exact Catalog/Search generation、ProcessingJob/RecoveryPoint lease fence、
  source fingerprint 和 expected classification revision，再替换一个 AssetRef
  的 content/OCR postings、per-field coverage/revision 与 nullable opaque
  excerpt reference。
- Search Token Key 永不提供给 Worker；端口只在内存中规范化/HMAC，禁止把
  plaintext token、content、OCR 或 excerpt 写入 DB/log/audit/error。
- Child 7 runtime 没有 content producer；默认 ingest caller 不存在，未来
  Child 10/11 通过稳定端口接入。

### 5.5 User overlays

- Saved search：owner-scoped CRUD，仅保存加密的 versioned AST/scope，不保存
  结果、成员或 snippet；exact point 不可用时 durable broken，禁止扩大范围。
- Favorite：owner + composite AssetRef 唯一；可保存用户自写 label，不复制
  source path/name/MIME/hash。
- Tag：owner-scoped definition 与 assignment；tag name 使用同一 Unicode
  normalizer 生成 portable uniqueness key；assignment 绑定 composite AssetRef。
- Recent access：owner + composite AssetRef merge，默认 30 天；提供 list/clear
  与内部 record port；source 退役/过期/purge 立即删除且不留墓碑。
- 收藏/标签在 source 失效后只保留 opaque target、用户自写 label/tag 和 typed
  tombstone；所有 server-derived source metadata 始终不持久化。
- 所有 mutation 有服务端 ownership、transactional quota、natural +
  `Idempotency-Key` replay protection、optimistic version、typed audit 和并发
  测试；达到上限返回稳定错误，不截断后谎报成功。
- 任一 overlay 不创建/延长 RecoveryPoint hold，不改变 retention deadline，
  不写回 Provider。

### 5.6 API and frontend boundary

- 新增 `POST /api/v1/asset-search`；query AST 只在 body，URL/access log 不含
  query/path/name。
- 新增 owner-scoped saved-search/favorite/tag/recent API；handler 使用 strict
  JSON、response helpers、RBAC、ownership、idempotency 和 typed audit。
- 前端只交付 raw DTO、camelCase domain types、API factories 与 mapper tests；
  不增加页面、路由、组件、完整 UI 或 i18n 文案。
- 浏览器不得持久化 query text、path、selection、result 或 saved AST；未来 URL
  只允许 opaque saved-search ID。
- 任一 unknown enum、非法 closed product、错误 composite ref、coverage/total
  矛盾或 scope/query schema 不匹配使整个 search/overlay projection blocked，
  不做字段级猜测修补。

## 6. Out of Scope

- Provider byte mutation、Provider command、SSH/Restic/Rsync/Rclone invocation、
  repository reconcile 或 publication change。
- Child 8 的 Content Broker、delivery ticket、Range、cache、renderer、preview、
  download 或内容分类实现。
- Child 9 的 `/app/backups` workspace、页面、路由、组件、深链接或 UI 状态。
- Child 10 的 Processing queue/Derived Store/derived encryption/Worker protocol。
- Child 11 的正文/OCR/thumbnail/document/media/archive producer、excerpt
  ciphertext 和实际 content postings publication。
- Child 12-15 的 export、recovery、retention/hold/purge/reconnect、GA enablement、
  legacy Snapshot search removal 和 release docs。
- Command artifact support；Command 继续返回 stable
  `task_artifact_contract_missing`。
- 占用、重排或修改 `000066...000071`。

## 7. Functional Requirements

### FR-1 Normalization and keyed postings

1. Normalizer 使用固定 version，按 NFKC -> full case fold -> slash/segment ->
   field tokenization 的顺序执行；identity normalization 不被修改。
2. 中文连续 Han run 产生 overlapping bigram（单字保留 unigram），Latin/数字
   run 产生完整 token，名称/路径产生 bounded gram，扩展名不含点，日期统一
   UTC `YYYY`/`YYYY-MM`/`YYYY-MM-DD`。
3. token HMAC 必须绑定 field、token kind、normalizer version 和 key version；
   不同字段/领域相同文本不能得到可复用 posting。
4. 排名与 sort tuple 只由 Go 计算；DB 仅做 equality/intersection 和 portable
   ASCII/hex/time/ID ordering。

### FR-2 Atomic projection and fencing

1. Search generation 先 `building` 且不可见；documents/postings/field rows 只写
   staging generation。
2. 只有 Catalog generation 仍 active/complete、point/source 未漂移、Search
   Token key version 可用且 `search_index` fence 有效时才能事务激活。
3. build failure 保留与同一 active Catalog generation 匹配的旧 complete
   projection；旧 projection 若绑定旧 Catalog generation，不得作为 fallback。
4. mutable head refresh、retirement、source fingerprint 变化、late worker 和 key
   replacement 都使旧 generation/cursor 失败关闭。
5. zero-entry complete Catalog 可生成 zero-document complete Search generation。

### FR-3 Query AST validation

1. `and/or` 至少两个 child，`not` 恰好一个 child；leaf 不允许 children。
2. `term` field 仅允许 `any|name|path|extension|tag|content|ocr` 且 text 非空；
   `type` 仅允许 closed Catalog entry types；`modified_time` 至少一个端点且
   `from <= to`，时间必须为 RFC3339 并转 UTC。
3. 未使用字段必须为空；duplicate/unsorted scope IDs 在 canonicalization 中
   去重排序，非法/超限值拒绝。
4. Search body 和 saved AST 使用相同 validator/canonicalizer；前端类型不能
   代替后端 runtime validation。

### FR-4 Scope truth

1. `current` 先解析所有 eligible authorized lineages，再为每个 lineage 选择
   newest committed/degraded point；legacy lineage 使用其稳定 observed mutable
   head。选择发生在检查 search coverage 之前。
2. 新est point building/failed/unavailable 时返回相应 coverage；禁止选择旧的
   complete Search generation。
3. `all_retained` 包含所有 authorized committed/degraded retained points，并为
   legacy lineage 包含当前 observed mutable head；按 validated producing lineage
   + canonical path 分组，不跨 Task/link/repository lineage 合并。
4. `exact_points` 要求每个 ID 都存在、eligible、authorized；任一 unavailable/
   expired/unowned 使整个 exact scope safe-fail。保存 exact scope durable broken。
5. Admin-only imported/unattributed point不进入 `current` producing-lineage
   expansion；Admin 可通过 `all_retained` 或 `exact_points` 查看，Operator 永远
   不可见。

### FR-5 Stable ranking and cursor

1. 默认 ranking 是固定 integer formula：positive-leaf coverage 优先，其次
	field weight/frequency 和 path-leaf proximity、current/freshness，再以 captured/observed time、
	portable path/name sort data 和 composite IDs 形成 total order。
2. Search cursor 使用独立 Cursor Signing key version，最多 15 分钟；只包含
   opaque group/document anchor、user、role、scope digest、query HMAC、selected
   point/search generation digest、projection revision、sort 和 exact-proof ID/
   expiry binding。
3. Cursor payload/signature/错误/log 均不含 query text、token、path、name、tag、
   snippet 或 source metadata。服务端通过 opaque anchor 重载 sort tuple。
4. user/role/ownership/scope membership/newest point/index/classification/owner-tag
	revision/proof 任一变化返回 typed stale cursor，绝不从猜测位置继续。

### FR-6 Coverage and authoritative empty

1. metadata fields 只有 exact Search generation complete 才 complete；
   content/OCR 由 per-document field state 聚合。
2. public status 仅为 `complete|partial|building|failed|unavailable`；内部
   `superseded/not_applicable` 不直接泄漏。
3. 只有 query 使用的所有字段在整个 selected scope complete，才允许 exact
   total 和 `authoritative_empty=true`。
4. 部分结果仍可返回 covered visible matches，但 total 为 null/lower-bound，
   suggestions 标注 covered scope，零 items 不能表述为零资产。

### FR-7 Secret-safe boolean evaluation

1. hidden secret/unknown content/OCR leaf 求值为 `unknown`；`NOT unknown` 仍为
   unknown，避免反向存在性泄漏。
2. `path:true OR hidden-content:unknown` 可因 path 分支返回，但 hit fields、
   count reason、suggestion 和 snippet 只能包含 path 事实。
3. 只有 exact `asset.secret_reveal` proof 可把 secret/unknown content leaf 变为
   eligible；proof 在每页请求重验并绑定 cursor。
4. content candidate 在返回前还需通过 future excerpt resolver 的真实命中
   验证；resolver 不可用时 content coverage 不 complete，HMAC gram candidate
   不得直接返回。

### FR-8 ContentIndexIngest

1. 只接受 server-resolved composite AssetRef、closed `content|ocr` field、bounded
   normalized terms/frequencies、source revision、expected classification
   revision、pipeline/index revision 和 optional 32-hex excerpt ref。
2. 同一 transaction 验证 point/catalog/search/document/lease fence/classification
	后删除旧 field postings、插入新 HMAC postings、更新 field coverage/excerpt
	ref、推进 generation projection revision。
3. classification revision 改变时同时删除 content/OCR 两个 sibling field 的
	postings，将未发布 sibling 置为同一新 revision 的 unavailable 并清除 excerpt
	ref；旧分类下的另一个 field 不得继续命中。
4. 任一失败回滚全部 field changes；metadata postings 不可由该端口覆盖。
5. Child 7 不创建/读取 excerpt ciphertext；opaque ref 无 Derived FK，直到 Child
	10/11 的 owner migration/port 接入。

### FR-9 Saved search lifecycle

1. owner-only list/detail/create/update/delete/use；missing 与 other-owner 返回同一
   safe not-found。
2. canonical AST/scope 使用 model hook 加密 at rest；响应只返回给 owner；
   audit/log/cursor/idempotency receipt 不保存 plaintext。
3. update 使用 expected version/CAS；create/update 支持 idempotency receipt。
4. exact source 进入 retired/expiring/expired/failed/purge_blocked 或消失时，
   lifecycle transaction 标记 broken 并审计；不得改写为 current/all。
5. unknown future AST schema 返回 blocked/broken，不自动猜测升级。

### FR-10 Favorite/tag/recent lifecycle

1. favorite/tag assignment 通过 owner+AssetRef natural unique 实现重复 add 成功
   同一结果；remove/unassign 对已不存在目标幂等成功。
2. favorite/tag source tombstone 只保留 opaque ref、user label/tag 和 typed state；
   不复制/缓存 Catalog metadata。
3. recent upsert 同一 AssetRef 合并计数/last access；每用户 120 writes/min，
   30-day expiry 和 10,000 quota；source 失效立即 delete。
4. cleanup 是可重入、分批、transactional，既能被后台 reconciliation 调用，
   也暴露 future lifecycle coordinator port。
5. 任何 overlay query/mutation 不读写 hold/retention/Provider 表外字段。

### FR-11 Quota and idempotency

1. per-user usage row在 SQLite immediate transaction/PostgreSQL row lock 下保留
   count；并发 create/bulk assignment 不能越过 quota。
2. bulk input 先 dedupe；全部 AssetRef/ownership 检查在同一个 mutation
	transaction 使用同一 `*gorm.DB` 完成，再计算新增长度。任一无权/非法目标使
	whole mutation rollback，禁止事务外授权后的 TOCTOU 写入。
3. `Idempotency-Key` 只接受 bounded opaque format；DB 保存 key hash 与加密 request
   fingerprint，unique `(owner, action, key_hash)`。同 key/同 request replay 相同
   结果；同 key/不同 request 返回 conflict。
4. idempotency receipt 有 TTL/cleanup，不存 query/path/name/label plaintext 或
   response body。

### FR-12 API, error, and audit

1. 所有 route 走 AuthMiddleware、`backup_assets:list`、owner checks 和 response
   helpers；Viewer 在 service 前 403。
2. request unknown fields/trailing bytes/oversize body/cursor/idempotency key 拒绝；
   raw internal/SQL/crypto/query/source errors不返回。
3. Search audit action 为 `asset_search`，query 仅进入 in-memory keyed fingerprint；
   valid proof 只保存 action + opaque proof ID。
4. Overlay CRUD、broken/tombstone/cleanup/clear 使用 typed registry actions 与
   safe operation/stage；不在 handler 拼自由字符串。

### FR-13 Frontend safe boundary

1. raw snake_case 仅在 API modules；domain/consumer 仅见 camelCase closed types。
2. mapper 对 unknown enum、非法 AST node、coverage/total/authoritative-empty
	矛盾、invalid AssetRef，或 content hit/snippet/suggestion 与 server content
	capability 矛盾时整体 blocked。`secret_reveal=false` 不能否定后端已授权的
	non-secret content hit；classification 仍由服务端判定。
3. search 始终 POST body；idempotency 使用 request option/header；step-up 仅用
   existing exact-purpose header。
4. 模块及测试不访问 localStorage/sessionStorage/history/router，不把 query/path/
   selection 加入 URL；saved-search ID validator 只接受 opaque ID。

## 8. Non-functional Requirements

- SQLite 与 PostgreSQL 使用同一 Go normalizer/evaluator/ranker 和相同 fixture；
  真实 PostgreSQL apply/down + behavior job 是 required CI，不得 skip。
- 所有 persistence/runtime 时间为 UTC；测试使用注入 frozen clock 或相对
  future/past，不使用会过期的固定“未来”时间。
- DB/AST/response/token/page/candidate/transaction/workers 全部有 hard limit；
  超限返回 typed resource-limit，绝不 silent truncate。
- Search/overlay service 使用 context、transaction、`errors.Is`/sentinel、
  `logger.Module` 和低基数 metrics；不得记录 query/path/name/tag/content/token。
- `backup_assets.enabled=false` 时 Search worker/API 在接触 search key、DB
  mutation、Provider、audit side effect 前失败关闭；现有 Catalog/backup 主链
  不受影响。
- Frontend严格 TypeScript，无 `any`、无 `unknown as T`、无 direct fetch；所有
  Node/npm gate 以 `env -u NODE_ENV` 运行。

## 9. Schema / Migration Decision

Child 7 使用已保留的 paired
`000065_backup_asset_search.{up,down}.sql`，不改变 reservation。它必须：

- 扩展 `wrapped_domain_keys` closed domain 为 `search_token`；
- 扩展 `recovery_point_leases` holder 为 `search_index`；
- 创建 atomic search generation/document/posting/document-field schema；
- 创建 encrypted saved search/exact-scope、favorite、tag definition/assignment、
  recent access、overlay usage 和 idempotency schema；
- 增加所需 composite FKs、owner FKs、partial uniques、natural uniques、lookup/
  cleanup indexes、UTC timestamps和 closed CHECK；
- 对 legacy `000064` 数据 apply 后保持完全不变，不自动把旧 Snapshot index 或
  Catalog entry 宣称为 search-complete；
- pristine down 能恢复 000064；一旦存在 Search Token row、active search lease、
  projection 或任一用户 overlay，down 必须原子失败并保持 version/schema/data
  不变。部署回滚先 fence/disable，保留 additive schema，使用 forward repair。

`000066...000071` 继续专属 Content-GA。若实施前 main 现实占用了 `000065`，
必须返回父级整体重排决策，不得自行跳号。

## 10. Acceptance Criteria

以下验收中 AC-1 至 AC-13 已由当前未提交工作树的本地 fresh evidence 满足；
AC-14 仍等待交付链完成：

- [x] AC-1：NFKC/case fold/slash/segment/Chinese bigram/Latin token/extension/
  date property fixtures 在 SQLite/PostgreSQL 产生相同 token、ranking、order 和
  cursor pages。
- [x] AC-2：paired 000065 在两引擎通过 legacy apply、pristine down、used down
  fail-closed/atomic、FK/check/unique/index/UTC/model parity；CI 无 DSN 时 fail。
- [x] AC-3：Search Token key 随机独立、KEK rewrap 不改 token/version，普通
  rotate 禁止，explicit replace/loss 使 projection unavailable 并触发 reindex。
- [x] AC-4：Search generation staging/zero-doc/activation/old-active/source drift/
  lease takeover/late fence/restart reconciliation 在两引擎行为一致。
- [x] AC-5：AST/scope limits、unknown schema/op/field、empty exact、scope drift、
  newest-unindexed no-fallback 和 saved exact broken 全部失败关闭。
- [x] AC-6：Admin/Operator/Viewer/shared repository/imported/unattributed/ownership
  change矩阵证明权限先于 candidate/name/path/count/suggestion/snippet。
- [x] AC-7：secret/unknown content 三值求值、exact purpose、cross-purpose rejection、
  proof expiry/pagination 和 no-existence leak tests 通过。
- [x] AC-8：每个响应包含 composite AssetRef、hit fields、query/index generation、
  coverage/staleness/capability/permissions；partial zero 从不权威。
- [x] AC-9：ContentIndexIngest 通过 source/fence/classification CAS、atomic replace、
  stale worker、no plaintext/no ciphertext ownership tests。
- [x] AC-10：saved/favorite/tag/recent ownership、quota race、bulk rollback、
  idempotency replay/conflict、expiry/tombstone/broken/cleanup/no-hold tests 通过。
- [x] AC-11：handler/router/audit/Swagger/source-boundary tests 证明 POST body、Viewer
  denial、query redaction、无 Provider command 和 stable error envelopes。
- [x] AC-12：frontend safe mappers/API factories 通过 closed-product、composite
  ref、unknown blocked、POST/idempotency/step-up和 browser-storage negative tests。
- [x] AC-13：focused tests、真实 PostgreSQL jobs、race/repetition、Swagger、
  backend/full frontend `env -u NODE_ENV` gate、`make check`、migration UTC safety、
  `git diff --check` 均有 fresh evidence。
- [ ] AC-14：exact staging、单一 work commit、Child-only finish/archive auto-commit、
  concrete journal commit、push、单一 PR、required CI、squash merge、post-merge
  automation、main sync 和 branch hygiene 完成；父任务保持 planning。

## 11. Planning completion criteria

- [x] 基线、依赖、migration reservation 和 worktree 已核验。
- [x] 现行 Catalog/schema/keyring/settings/bootstrap/step-up/audit/CI/frontend
  边界已有 source-backed research。
- [x] `prd.md`、`design.md`、`implement.md` 不含未决占位项，并明确 exact file、
  migration、validation、rollback 和 delivery contract。
- [x] 用户审阅并明确批准三份 focused artifacts。
- [x] 用户另行明确批准 `task.py start` 与实施。

## 12. Approval and execution ledger

| Item | Status | Meaning |
|---|---|---|
| Child task creation | complete | `.trellis/tasks/07-18-backup-assets-search-overlays` |
| dedicated branch/base | complete | `codex/backup-assets-search-overlays` -> `main`, base SHA `8cd6e51...` |
| focused research | complete | `research/current-main-evidence.md` |
| PRD/design/implementation plan | approved | 用户于 2026-07-18 明确批准 |
| paired 000065 reservation | implemented_verified_local | paired SQLite/PostgreSQL 000065 已创建并通过 real apply/down contract；000066-000071 未占用 |
| `task.py start` | complete | 用户独立授权后执行，task 为 in_progress |
| product implementation | complete_local | Tasks 1-10 与 review corrections 已在未提交工作树实现 |
| product tests/builds | pass_local | 2026-07-18 focused、双数据库、race、Swagger、backend/frontend/full gates fresh exit 0 |
| product work commit | not_executed | 等待 exact staging |
| finish/archive auto-commit | not_executed | Child 仍 active/in_progress；父任务保持 planning |
| concrete journal commit | not_executed | 无交付记录提交 |
| push/PR/required CI | not_executed | 无远端 mutation |
| merge/post-merge | not_executed | 尚无 PR/merge |
| main sync/branch cleanup | pending | 仅在未来 squash merge/post-merge 后执行 |

本地 fresh evidence 使用隔离 PostgreSQL 18 容器与
`xirang_child7` 数据库：paired 000065、Search behavior、Overlay behavior 均在
`REQUIRE_POSTGRES_*=1` 下退出 0；race 三轮退出 0；`make swag-init`、UTC migration
scan、`GOFLAGS=-count=1 make backend-test`、`env -u NODE_ENV npm run check`、
`env -u NODE_ENV make check` 和 `git diff --check` 均退出 0。required CI 仍必须在
PR 后独立运行，不能由这些本地结果替代。

## 13. Explicit approval gate

两个独立门禁均已满足。Phase 2 必须在同一 inline 线程按
`trellis-before-dev`、`superpowers:test-driven-development` 和
`superpowers:executing-plans` 执行；当前实现与本地验证已完成。任何范围或
migration reservation 变化仍需返回规划审阅，Tasks 12-13 只按既定交付流程
继续。
