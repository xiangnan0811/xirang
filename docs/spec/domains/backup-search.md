# 备份资产搜索与用户状态

本合同覆盖 `backupasset/search`、`backupasset/overlay`、Search worker、API mapper 与工作区搜索。上游真相见 [Catalog](backup-catalog.md)，秘密证明生命周期见[凭据与访问](credentials-access.md)，跨运行时配置见[启用条件](backup-enablement.md)。

## 持久化与搜索语义

- 配对 `000065_backup_asset_search` 保持 SQLite/PostgreSQL 表、FK、CHECK、unique、index、model 与 UTC 等价。有 durable Search/publication 事实后 used-down 原子拒绝，不能丢掉历史或 Provider 事实。
- 搜索语义在 Go 计算：Unicode NFKC、完整 case fold、slash/path 分段、汉字 bigram、Latin token、extension/date token、整数 ranking、stable sort/group/cursor。数据库 FTS/collation 不是产品定义。
- SQL/posting 和 owner-tag 候选必须在私有 Catalog hydration 前有界。`TagResolver.CandidateRefs` 只收到已经授权选定的 point IDs。`any` 合并可用 positive 字段；无选择性的 negative branch 仅扫到 hard ceiling，超限返回 typed resource limit，不能截断后声称完整。
- Search Token HMAC 绑定 field/kind/normalizer/key version；使用独立 `search_token` 域，cursor 使用 Cursor Signing 域。密钥丢失/版本不符使投影 unavailable，不静默生成密钥或继续旧 postings；KEK rewrap 不改变 token。
- 最新授权点未索引时报告 building/failed/unavailable 的真实 coverage，不回退旧点。complete/partial、exact/lower-bound/unavailable total、authoritative empty 必须一致。
- `ContentIndexIngest.PublishContentProjection/RevokeContentProjection` 绑定 source、fence、classification CAS。classification 变化删除 content/OCR 两族 postings，同时推进两字段 classification revision，清 sibling excerpt ref，直至重新发布前 sibling 不可用。replace/revoke 失败整体回滚。
- 没有精确、未过期 `asset.secret_reveal` 时 secret/unknown content/OCR 采用三值逻辑，不泄露 hit/count/suggestion/snippet。HMAC posting 必须由 excerpt resolver 真实匹配确认后才成为内容命中；resolver 失败不能猜测。metadata suggestions 不推断内容。
- cursor 绑定 user/role/scope/query/key/point/generation/projection/classification/owner-tag revision/proof；payload 无 query/token/path/name/tag/label/snippet 明文。tag 定义/assignment 变化使 owner-tag digest 变，旧 cursor stale。

## 生产者与消费者语义

Catalog `security_state=sealed` 证明 locator 已认证加密，不是非秘密分类。Search 将精确大小写的 `sealed` 和 legacy 空字符串保守映射为 `unknown`；`non_secret|secret|unknown` 原样，其他非空（含大小写/空白变体）失败为 `search_invalid_security_state`。不 trim/fold/wildcard，不改 Catalog 持久值，不做 SQL 修复或 Provider 枚举。

候选本地 Build 失败保留 durable failed generation/metrics，但不使整个 startup 失败；必须 bounded join 全部工作。caller cancel、配置/密钥、abandoned reconciliation、overlay、candidate listing 等 pass 基础设施失败仍传播并保持 Search unready。旧失败代次不可重写，下一个 sequence 单调收敛并原子激活。回归必须消费真实 Catalog producer 输出，不能手写 `non_secret` 伪造通过。

## API、审计与 overlay

- 查询使用 `POST /api/v1/asset-search`，AST/scope/cursor 在 body；不放 GET URL。未知 schema/op/field、坏 exact scope、任一限额超限拒绝整个查询。
- `backup_assets.enabled` 默认 false；所有请求使用 `FeatureLive`，关闭时在 Search key/projection/proof lookup、audit mutation 或 Provider access 前返回。metadata maintenance 的显式生命周期例外见各 owner 合同。
- ticket issue/search verify 的 `asset.secret_reveal` 只允许 Admin，Operator 有有效 proof 也拒绝。step-up 发行路由不是该角色边界的替代。
- 成功查询后的必要 Search audit 写失败必须使 HTTP 请求失败，不能返回 hits。503 也可由其他 unavailable 原因产生，不能仅凭状态码宣称审计故障。
- 收藏、批量收藏、标签 assignment、recent mutation 必须在同一事务中先 `AuthorizeAsset(ctx, tx, actor, ref)`，然后 idempotency replay/write；事务外授权不够。overlay 不创建 hold、不改 retention、不复制 source metadata、不写 Provider bytes。
- 所有 saved search/favorite/tag/recent 是 owner-scoped；quota race、幂等、tombstone/broken/recent cleanup 都保持用户隔离。原数据库丢失不能从 Provider 重建 overlay。

## 前端闭合映射与隐私

- Search response 作为完整产品原子校验：opaque 32-hex point/ID、64-hex entry、复合 AssetRef 一致、safe integer、UTC、schema/enums、hit field 不重复、generation/revision、coverage/total/authoritative-empty 耦合。坏字段不逐个默认修复，整投影 blocked。
- content/OCR hit、snippet、suggestion 必须 `capabilities.content=true`；`permissions.secret_reveal=false` 不否认服务端已授权的 non-secret 内容命中，浏览器不猜 classification。
- 出现 retained count 时必须整数 `>=1`，保留到 result-row 的 `retainedVersionCount`；0/负/小数/缺失成员阻整个响应。未知 overlay lifecycle/reason/version 同样 blocked。saved-search ID 不是 32-hex 则 request 前拒绝。
- 组件只收 camelCase，raw 类型私有；mutation 通过 central request 的 `idempotencyKey`/`stepUpProof` header，body 精确 snake_case。列表 key 为 `assetRefKey(ref)`。
- query/path/selection/results/AST 不进入 local/session storage、URL/history；以后可用 URL 仅不透明 saved-search ID。proof 由当前登录、action-keyed 中央存储拥有，Search mapper 自身不存储。每页（首/更多/saved reload）及精确 preview retry/renewal 转交相同有效 proof。
- 有效 secret-reveal proof 可跨 file/directory/version/set/node/search/retry/renewal 留存；仅 action+proof+固定 expiry 可在当前登录 sessionStorage。登录替换/退出、user/role/token-version/TOTP 变化、401、到期或 typed rejection 清除；相同 action 并发共享一个 pending dialog/result，late result 不跨 auth/selection owner。Operator 永不请求 Admin step-up。
- 禁止 proof/ticket 进入 localStorage、URL/history、analytics、console/log、raw error 或 ticket 产品。安全预览布局见[内容交付](backup-content-delivery.md)。

## 回归证据

双引擎真实 apply/pristine/used-down、FK/CHECK/index/UTC；normalization/property、HMAC 独立/丢失/rewrap、候选 hydration 前截断门禁、path proximity、stable ordering/grouping/拼页、coverage 与全部 cursor stale 绑定；三值逻辑、wrong-purpose/expired proof、resolver failure、metadata-only suggestion 与 query/log/audit/cursor 隐私扫描。

同时覆盖 ingest source/fence/classification CAS、sibling invalidation/rollback、无 plaintext/ciphertext ownership；owner/事务授权/quota race/幂等/tag revision/lifecycle cleanup 且不改 hold/retention/Provider。真实 Catalog→Search fixture 在 SQLite 与真实 PostgreSQL 上验证 sealed→unknown、坏变体拒绝、不改 prior active、候选失败隔离与所有基础设施失败、下一代收敛。

前端测试 full/partial 产品、复合 ref、全部 contradiction、合法 non-secret content、retained count、overlay mutation 精确 header/body、Admin proof复用/拒绝/并发/owner；source guard 针对 API 层禁止 direct fetch、storage/router/URL、`any`/`unknown as T`，中央 proof store 的明确例外不能误报为 Search 持久化授权。
