# 备份内容交付

本合同拥有 preview/download ticket、Broker、数据库账本、Cookie/Range、HTTP transport、内容网关/缓存/日志和前端安全预览。派生、归档成员、Export、Recovery result 的交付也服从共同传输与隐私边界；各资源自身授权不能被通用交付规则替代。

## 资源、票据与闭合表示

- JSON 路由为 `POST /recovery-points/:id/entries/:entryId/delivery-tickets`，权限 `backup_assets:preview`；原始 content URL 仅同源 `/api/v1/asset-content/[0-9a-f]{32}`，无 query/fragment。URL 不解析重建 ID，不拼 token/proof，不存 router/history/browser storage。
- 输入严格判别联合：普通 `schema_version:1,action:preview,preview_intent:safe_preview_v1` 不带 renderer/profile；兼容/处理 exact preview 为一个非 attachment closed renderer/profile；download 仅 `attachment/original_v1` 与精确 `asset.download` proof。intent 与 product 不能同时发送。
- 服务端有界读取分类并持久化一个 exact product，grant/descriptor/Serve/success audit 不存未解析 intent。泛用可读文本为 `plain_text/text_v2`；仅真实 binary 退到 bounded hex。classifier 只用内存 prefix、严格 UTF-8/批准且格式正确 UTF-16，active markup 保持惰性；native media 仅闭合 signature/capability，MIME 不能升级歧义 bytes。
- preview 不可 attachment。non-secret 无 proof；secret/unknown 必须精确 `asset.secret_reveal` 且 Admin。服务端最终验证 RBAC、step-up、session、origin、purpose、source、lease 和预算；UI eligibility 不构成授权。
- only text/hex 可 truncated，且 Range none；safe intent 解析出的 native raster/PDF/audio/video 要 single Range，缺能力返回 typed denial，不伪装 text/hex。legacy exact native/download 保留已定 `none|single` 兼容。
- ticket mapper 原子验证 schema/opaque AssetRef/URL/action/renderer/profile/MIME/Range/classification/proof/ETag/非负 safe integer length/truncation/UTC absolute+idle expiry/capability reason/fallback。exact request 必须 exact match，safe intent 允许一个服务端解析 product；任一 contradiction/未来 enum/坏时间均 blocked。available 不带 reason/fallback。proof 仅通过 central request header，API mapper 不解释/存储 proof、不 fetch bytes/建 Blob/media。
- Issue typed 503 仅 feature/offline/disconnected/unavailable/timeout/resource-limit；501 仅 artifact/identity/protocol/committed-point/mutable-source/Catalog/sequential/Range/download capability。`params` 必须精确 `{}`，合法但越界 code/参数化 reason 都变通用无细节 503。source-stage error 仅 closed open/read/changed/timeout/capability + empty params/correlation；禁止 Provider detail。

## 有界 prefix 与关闭

Issue 分类阶段恰一次源 open/read，Serve 自己恰一次授权 bounded/exact read；分类、选择、grant prepare、audit 不得重开。Restic/Rclone 各 wrapper 将可选 `ClosePrefix()` 传到底层 command stream，Rsync 无此能力用严格普通 close。

`ClosePrefix` 只忽略成功取得 prefix 后人为终止仍运行命令诱发的 wait 错误；已自然结束的失败以及 read/cancel/timeout/byte-limit/background/invariant/ordinary-close 错误必须保留。失败 prefix 不进入 intentional-success cleanup。真正 Core source 失败给本地化源读取指导，Worker 文案仅用于真实 ZIP/Office/OCR 增强状态。

## 持久化账本与预算

- 配对 `000066_backup_asset_content` 的该账本仅 `resource_kind=backup_asset`；point/generation/entry 非空，RecoveryResult 字段 null，不混资源。Catalog FK 为 `(catalog_generation_id,entry_id,recovery_point_id)` RESTRICT，lease FK 指一个 `content_session` RESTRICT。
- grant/request/usage 分别落在 `backup_asset_delivery_grants`、`backup_asset_delivery_requests`、`backup_asset_delivery_usage`。四种内容审计 action（`preview_ticket`、`preview_read`、`asset_download_ticket`、`asset_download`）以 `idx_backup_asset_audit_events_content_grant_action(grant_id,action)` 保持唯一性。RecoveryResult 不得借该迁移增加资源种类或外键；时间字段保持 SQLite `DATETIME`、PostgreSQL `TIMESTAMPTZ` 的 UTC 兼容。
- 仅持久化 Cookie secret SHA-256 hash，不存 secret。public delivery ID、internal grant ID、session JTI、proof、action、renderer/profile、source fingerprint、lease fence hash、absolute/idle expiry、budgets 是独立绑定，私有字段 `json:"-"`。
- SQL CHECK 验证完整 action/renderer/profile/range/classification/proof 产品。`000073` 只增加 safe-preview 的 `plain_text/text_v2/range_policy=none`，不放宽其他耦合、truncation/step-up/audit/budget。SQLite rebuild 保留所有行/FK/index/trigger/无关约束，PostgreSQL 只事务替换四个命名产品约束；出现新增值即拒 used-down/metadata downgrade。
- request reservation 与 global/provider/user usage 在 source I/O 前同事务提交，所有 request/累计 bytes/count/in-flight 上限一致且非负。不能读后才扣费。
- `BudgetService.RecordBlocked` 插 request 同事务计 blocked audit；`Finalize` terminal transition、释放 reservation、success/failure safe audit counters 同事务。Broker 不能另写第二次 counter；duplicate finalize 不重复计数。
- final read/download audit 仅 grant `revoked|expired|closed`、in-flight 0 且 retry due 后发出。`byte_count` 是 persisted `response_bytes` 总和；Range count/bytes 仅 effective/blocked Range 子集，request rows count 必须等于 audit count，不能写不完整 append-only event。
- audit SQL state 闭合：none counters 为 0 且无 retry/failure；pending/emitted/retry_wait/failed 有非零且 outcome 平衡 summary，不超 grant request count；Range count≤audit count、0 count→0 bytes、Range bytes≤charged delivered bytes；仅 retry/failed 有 failure code、正 attempt、next-at。retry CAS 绑定已读 version/state，旧失败不能重开 emitted 或覆盖更新 retry。
- crash reconcile 先 revoke old grants。短 live content fence 可 defer，不让 startup 整体失败；terminal active lease takeover/release 失败保留并下轮重试或 absolute-deadline fencing。feature disabled/not-ready 仍维护 terminal state/lease/audit；shutdown/schema-drain fence 必须 cancel/join loop，未 join 成功不得 down。
- 任何 grant/request/usage/content_session lease 都阻 `000066` down，先于任何 index/table 删除。仅已证明 runtime drain 后清 terminal ephemeral state 才允许 pristine→000065；否则 forward repair。真实 PostgreSQL migration/behavior 破坏性 fixture 共 DSN 时串行，不多包并行。

## TLS、私网 HTTP 与代理证据

`backup_assets.content_allow_insecure_private_network` / `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK` 默认 false，Foundation DB > env > default；PUT/reset/import/失败/补偿遵守[启用条件](backup-enablement.md) exact snapshot。审计仅 action/key/change fact/actor，成功 PUT source=db，不记录值。独立 loopback 兼容键仅允许直接 loopback→localhost Host 且无 forwarding header，不能被私网开关扩大。

- 直接 TLS 使用 Secure Cookie。显式私网键 true 时 HTTP effective client 仅 RFC1918/ULA/loopback（先 unmap IPv4-mapped）；CGNAT、link-local、public、unspecified、multicast、zoned、invalid 拒绝。
- 一律拒 `Forwarded`。XFP 必须来自 trusted immediate peer，恰一个 raw 值、≤16 bytes、大小写精确 http/https。携带 XFP 的 HTTP hop 还须恰一个 XFF、≤1024 bytes/16 hops。逐个 exact IP 解析，append immediate peer，从右剥 trusted hops，最近 untrusted 为 client；duplicate/compound/empty/malformed/超限/all-trusted 都拒绝。
- Issue 和每次 GET/HEAD 重读 Content config 并执行同策略；关闭键立即阻旧 non-Secure ticket HTTP 读取。HTTP Cookie 保持 HttpOnly、SameSite=Strict、exact Path。
- transport deny 在 JSON ticket 为 503 `reason={code:secure_transport_required,params:{}}`，raw content GET/HEAD 仅 status。normal preview/download、Export、archive member、Recovery result 一致，deny 前不调用服务。
- 前端 setting mapper 只收 exact key/env/bool/category=backup_assets/default=false 和闭合 value/source db/env/default；坏形状 throw。authenticated Admin 的 Overview `#backup-assets-content-transport` 才挂载，启用要风险确认，save single-flight，失败保留最后 server-confirmed 值；保存 live region/focus 可访问。
- UI 仅 content_ticket context + exact 503 closed reason（只 code/空 params）识别非 retryable/action none transport error。Admin 指向精确 hash，Operator 提示 HTTPS/联系 Admin 无 settings action，Viewer/未知仅 generic；其他 503 不冒充 transport。指导无 asset path/name/content/token/proof/ticket/delivery URL。

## 网关、缓存与隐私

- Nginx exact `^/api/v1/asset-content/[0-9a-f]{32}$` 才转发 Range/If-Range，禁 proxy/request buffering、cache/temp/gzip，proxy/send 75 秒上限；应用 grant/lease/write deadline 更短且最终。
- shaped fallback `^/api/v1/asset-content(?:/|$)` 仅脱敏拒绝，不继承 streaming/Range/timeout 特例。二者位于 generic API 前，专用 `xirang_asset_content` access log 与 `error_log /dev/null crit`。
- access log 只 request ID/status/body bytes/request+upstream timing；URI/args/cookies/referrer/user-agent/client identity/request line 禁止。两路 XFP 都 overwrite 实际 `$scheme`；exact 保留 `$http_host` 显式端口、append `$proxy_add_x_forwarded_for`、显式清 X-Real-IP；fallback 显式清 XFF/X-Real-IP，省略 directive 不等于清除。
- cache root `/var/cache/xirang/asset-content` 为 runtime-owned 非持久化目录，不作 volume，不在 /data、/backup、/logs 或 source。containment 失败禁 disk cache，不落普通临时盘替代。验证 `Lstat→OpenRoot→Root.Stat(.)→Lstat` 同一非 symlink identity，确认后才 lock/清 orphan；rename/replacement 不能删替代目标，句柄内操作仍受 os.Root 约束。
- 应用 exact/坏形状 content path 都用常量 `/api/v1/asset-content/:deliveryId`，仅 method/status/latency/可选 request ID。无 raw delivery/internal ID、RequestURI/query/Cookie/Auth/session/secret/Catalog name/path/locator/content，也无 client_ip/user_id/XFF/X-Real-IP。
- `ContentSafeRecovery` 在外层 Gin recovery 前拦 panic，日志仅 module/fixed category/request ID，不含 panic 值/请求。诊断可用进程密钥 delivery fingerprint 和闭合 action/outcome/reason/renderer/provider；任意 MIME/ID 不作 label。audit 用 internal grant ID/keyed asset fingerprint，不用 public delivery ID，Range 汇总。
- 未知或高基数 metric 输入必须降为闭合 `unknown` 类，不能原样作标签；常规 API 路由的身份日志行为保持其自身合同，不因内容路径脱敏而一并改变。
- cancel 先到 Provider reader 并 join；cache revoke、reservation finalize、aggregate audit 共用 `WithoutCancel(requestCtx)` 的单个 5 秒 deadline。无法证明最终态则 conservative reconcile 按 full reservation 计费并补 audit。ticket 获取 provisional lease 后失败也用有界 detached 5 秒 release，失败加入安全 unavailable 并保留 fence，不能 Background 无限等。

## 前端激活、proof 与布局

authenticated Admin/Operator + list permission + available content + 精确 selected point/file + preview tab + openSequential 才 advisory eligible。上游 Catalog list-only，不能要求/伪造 `permissions.preview=true`。选文件自动恰一次 safe intent，StrictMode/无关 rerender 不重复；不先要求 Load Preview，不按 MIME/扩展名选 renderer。

file/node/version/directory/auth/tab 变化 abort/detach 旧 ticket/content，latest selection wins。Admin secret-required 最多一次 prompt/同 intent retry；cached proof 被拒清除后最多一次新 prompt/retry；Operator 不调用 ensureStepUpProof。proof 中央生命周期见[凭据与访问](credentials-access.md)，ticket 永远只内存。renewal 使用当前 exact resolved product/ref，不重猜 MIME；download/export/recover/archive/显式处理独立。

401、403、capability 和 typed secret-reveal 错误保持各自安全 mapper 行为；手动 retry 仅作用于当前 selection 的可重试失败。Search 拒绝已附 proof 时清除中央对应 action 并失败关闭，不循环重新发票，也不重试已被新 selection 取代的请求。

frame 产品（plain/compat text、hex、同源 PDF）必须填满 viewport content box，Flex 链用 dedicated `min-height:0` stretch item，不能仅靠未定高度链的 100% iframe。图片/音视频/loading/empty/error 保留 centered/native，不能套 frame-only wrapper。viewport 保留 `min-h-[24rem]` flex-fill，不锁 18rem。

## 回归证据

覆盖实际 Restic/Rsync/Rclone adapter→Issue→persisted grant→Serve，每阶段恰一次 open/read、intent/exact 产品、prefix termination 与自然失败、所有 close/error/cancel/limit；至少一条真实 Repository+handler vertical slice，fake Broker 不够。

双引擎 SQL 有效/无效 closed product、跨点 FK、hash-only secret、并发 budget 的精确 totals/非负、finalize 幂等与 crash、audit request/counter 原子、active 不 flush、retry deadline/CAS、Range subset、短 lease defer/release retry、disabled maintenance/shutdown join、pristine/used/drain down；PostgreSQL required DSN 不可 skip。

transport 表覆盖全部 IP 类型、direct/proxy/TLS/HTTP、spoof/duplicate/limits/all-trusted/XFP-XFF、Cookie/path、deny 零 service、动态关停旧 ticket。所有交付面与角色一致。网关 checker + mutation self-test + official-template live probe 验实际 XFP/XFF/X-Real-IP/Host/GET/HEAD/Range/bytes/logs；cache root pre-open replacement sentinel 不受损，post-open rename 独立验证。

日志/panic/audit/metrics canary 保证无泄密；cleanup context 检测共享、非 canceled、≤5 秒。UI 覆盖 producer-realistic list-only fixture、Admin/Operator/Viewer/未知/无 token、快速切换无 stale、proof/renewal/denial、mapper 全 contradiction、transport setting confirmation/single-flight/hash；真实浏览器普通桌面与 focused reading 几何 poll 断言 iframe 与 content box 高度差≤1 CSS px，class 断言不能替代。相关 normal/repeat/race 与完整前端门禁必须通过。
