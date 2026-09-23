# 备份增强处理与导出

处理队列、Worker、Derived Store、归档成员和 Export 是备份资产的独立增强面。它们不替代 Provider、RecoveryPoint、Catalog、Content Broker 或恢复授权。公共交付遵守[内容交付](backup-content-delivery.md)，源撤销遵守[生命周期](backup-lifecycle.md)。

## 可选 Worker 与安全隔离

- Worker 当前非 GA、没有稳定公共镜像；全局 feature、local/remote transport、updater 和有限秘密分类默认关闭。缺 Worker 不阻止 Core GA，不把 `not_deployed` 当文件不存在、备份失败或告警。
- Compose `asset-worker` profile 仅本地 build/验证。Parser 与 updater UDS 分属 `asset-worker-worker-runtime` 和 `asset-worker-updater-runtime`，双方只读各自 socket volume，parser 不加入 updater GID。bundle store updater 写/parser 读；inbox/trust updater-only。
- Worker 仅凭一次性 attempt-bound Input grant 读 bytes，再通过一次性 Sink grant 交回 Core。不得访问 Provider locator、DB、SSH/Restic/Rclone/command 凭据、宿主 source 或网络，不得改 Provider bytes。
- 能力为闭合 profile/limit：静态缩略图、有界 text/OCR、静态文档页、malware finding、媒体探测/预览、有界归档索引。未支持/超限保留原生预览、download/recovery 路径，不把增强失败提升为 RecoveryPoint 不可信。

### Parser 沙箱与工具调用

Parser 固定 non-root UID/GID `10000:10000`，read-only rootfs、drop-all capabilities、`no-new-privileges`、经审查的 seccomp、PID/CPU/memory 上限及 `noexec,nosuid,nodev` job tmpfs。Compose 中 parser/updater 的 `memswap_limit` 必须等于 `mem_limit`，禁止容器 swap；其他运行方式仍须关闭宿主 swap 或使用经审计的全盘加密 swap，不能让敏感 tmpfs 内容进入明文交换空间。

Parser 使用 `network_mode: none` 且不配置 DNS。它只读挂载 worker runtime 到 `/run/xirang/worker`，只连接 mode `0600`、owner `10000:10000` 的 Core UDS，并只读挂载 active bundle；不挂载 updater runtime、`/data`、`/backup`、`/logs`、Docker socket、updater inbox/credential 或 Provider source。

所有 parser/tool 调用来自服务端闭合 capability/profile，不经过 shell，禁止调用方提供 executable、argv、环境变量、codec、字体、模型、路径、URL 或工具配置。源输入只来自一次性 attempt-bound grant；输出必须在 Core 重新校验 MIME、数量、大小、digest、coverage 和安全策略后才可发布。取消、超时或 fence 丢失必须终止整个进程组并清理私有 workspace，不能只杀父进程。缺少可验证 tmpfs、Landlock 或 seccomp 合同时，不得 advertise 对应 capability。

### Updater 身份、签名与原子激活

Updater 使用独立进程、UID/GID `10002:10002`、PID namespace、socket 和可写 bundle volume。它只读挂载独立 updater runtime 到 `/run/xirang`，不挂 worker runtime，parser 也不可反向观察 updater socket。Core 在 setgid mode `2770`、owner/group `10000:10002` 的 updater runtime 中创建 `/run/xirang/asset-worker-updater.sock`，socket mode `0660`、owner/group `10000:10002`，并在解码 receipt 前验证 socket 和 Linux peer credential。跨 PID namespace 的 `SO_PEERCRED` PID 可以为 0；PID 仅供诊断，不是认证主体，认证由受保护 UDS、精确 UID/GID 和 socket owner/mode 共同完成。

content-addressed bundle volume 是 parser/updater 唯一共享数据 mount：根目录 owner/group `10002:10000`、setgid mode `2750`，updater 可写、parser 强制只读。inbox 和 Ed25519 trust secret 仅 updater 可见；socket volumes、bundle volume、secret mount 不得替代或合并。

默认更新路径是 signed offline import。固定 updater-only inbox 中候选目录要求 `10002:10002`、mode `0555`；Ed25519 trust 文件要求 `10002:10002`、mode `0440`。Updater no-follow 扫描，验证 canonical manifest、Ed25519 signature、精确 tar/file SHA-256，以及大小、时间、路径和文件类型限制；先 fsync content-addressed store，再通过 journal 和原子 pointer rename 激活，不能在校验或耐久化未完成时切换 active bundle。

浏览器和 Core HTTP API 不接收 bundle bytes、multipart、URL、服务器路径、inbox 文件名或原始 manifest。Admin API 仅接受脱敏 candidate ID 和 expected fingerprint 的小型 JSON 控制请求。Compose updater 同样 `network_mode: none`，只支持 offline-only。Online updater 默认关闭；任何单独的 online 部署必须同时具备 exact HTTPS origin allowlist、独立 allowlist proxy/firewall、隔离网络及 updater-only credential secret。应用 allowlist 不能代替 egress firewall，parser 永远不继承 updater 网络或凭据。

处理 API、日志、指标、审计与管理聚合仅记录闭合 capability/profile/state/error category、不透明资源引用和有界计数。禁止记录 Provider locator、宿主/tmp/bundle/inbox 路径、Worker UID/PID/证书、credential、grant/session/attempt/fence/activation secret、原始 argv/stdout/stderr/tool diagnostic、manifest/body 或源内容；底层 peer PID 可作私有诊断输入，不因此变成公开日志字段。

## 队列、租约与原子发布

- 配对 `000067_backup_asset_processing` 用相同 closed `ProcessingState`，独立 transition revision、stable error、retry/cancel/supersede/expiry 产品。partial unique 保证每 canonical work_key 一个 current job、每 job 一个 current attempt、每 owner tuple 一个 active interest；并发请求由 DB 合并，不以进程 mutex 作为唯一性契约。
- Worker pull 同时拥有短 Worker lease 和 `processing_job` RecoveryPoint lease。heartbeat 在同事务续 attempt、RP lease、live grants。takeover 建新 attempt/fence，旧 heartbeat/grant/upload/projection/manifest 永久失效。
- activation secret 仅 hash 持久化且一次性；Input/Sink request count、bytes、in-flight reservation 双引擎事务约束。最后一个 interest 删除先 revoke 两 grants 再 cancel_requested；仍有其他 interest 保持共享 job。
- Derived 使用独立 `derived_store` key、每 blob 随机 DEK、authenticated chunks、opaque locator 和 explicit references。manifest 成员/digest/MIME/count/size/completeness/source/policy 任一不符拒整集并清理或调和不可见 staging；late/tampered output 失败关闭。
- `ArtifactSink.CommitManifest` 将 upload/blob/artifact 与 Search content/OCR/classification/ref/excerpt/coverage 在一个 fence 和事务内发布。`PreparedDerivedProjection.PublishTx` 只用 supplied tx、以 artifact-set ID 幂等，transient rollback 可复用 prepared publication。
- 两个边界均有相同 bounded context-aware transient conflict retry：`prepareProjectionEvidence` 的事务前读取/解密，以及提交 upload/blob/artifact/Search 的 caller transaction。SQLite BUSY/LOCKED、PostgreSQL serialization/deadlock 可重试；semantic validation/source/policy/fence/payload 不重试，caller cancel 停止。
- Search revoke 必须先成功，再毁 Derived ref/wrapped DEK/ciphertext，不能留 ghost projection 或先毁 source。失败保留证据和可重试状态。
- `000067` pristine down 必须无 Processing/Worker/Derived/updater rows 且无 active processing_job lease；used-down 在删任何 table/index 前拒绝，保留并 forward repair。

## 安全结果与前端表现

| 状态 | 必须表达的事实 |
|---|---|
| native | 使用原生安全 renderer |
| derived | exact source/profile/policy 的派生结果可用 |
| partial | 有界页/字/时长/成员覆盖，显示精确 coverage |
| queued | 当前用户 interest 排队/运行，按 `poll_after_seconds` poll |
| unsupported | 能力/安全策略不支持此增强 |
| not_deployed | Worker/sandbox/verified bundle 未就绪，不制造噪声 job |
| failed | 有界重试后的安全失败，不改源/恢复点可信度 |

上述各状态均保留本身仍获准的原生路径。malware 严格 `not_scanned|no_finding|finding|stale`：finding 是成功扫描结果，不是 crash，不能通过 retry 变 no_finding；未扫描/过期不展示安全。preview job、derived ticket、实际 read、Search hit/snippet 释放都重验 malware+sensitivity。`secret.classify` 默认 off，只能加强 Core classification，unknown/secret 继续失败关闭。派生内容通过 Broker ticket/cookie/Range/audit，不提供 blob URL。

没有 Worker/禁 profile/bundle activation 失败时，Catalog、metadata Search、workspace 和获准的原生预览/download 保持可用。受控 Recovery 另须满足其自身 malware evidence 要求，不能以通用“可回退”绕过[恢复合同](task-execution-recovery.md)。回退暂停 backfill、关 Worker/updater、停可选 profile，保留 Provider/point/Catalog/source；Derived 可交调和或重建。

## 有界 backfill 与管理 UI

- `ExpectedDescriptors` 不应用 `AdmitBackfill`，只有 leftover walk 真实走到 Catalog end 的 empty 才算 proven。InspectedLimit 默认 200/max1000，按 generation+point 内存 keyset cursor；本页预算耗尽且 0 descriptor 仍返回 unproven marker，Queue 不 enqueue marker。
- 未证明单位为 `(entry_id,capability)` 而非整行。walk fingerprint 包含排序 capability 名和 SecretClassify；failed/canceled revision（count、latest updated_at、id）变化清 complete。completeness 绑定 walk-start revision，不能漏掉 cursor 后方并发 cancel。
- 管理面 scan/rebuild 每次点击最多 8 页，保存 scanCursorByRepository/rebuildCursorByRepository，以 Continue 继续；不无限 drain cursor。API/面板保持 lazy，不能进入 startup chunk 破坏既有预算。

## Export 与归档成员交付

Export 管理自己的持久任务、attempt/fence、加密产物、quota 和交付账本，不混入 backup_asset content grant 的资源 tuple。签发必须冻结 exact ready artifact、recorded KEK/job DEK envelope、attempt fence digest、session/cookie/action/path/subject；坏 metadata/伪造 fence 在账本副作用前拒绝。归档成员冻结 exact Derived tuple，独立 ledger 且 Range none，不能夹带 Export tuple，反之亦然。

HEAD 在 metadata commit 前也重验 live proof/artifact/member binding，GET 在 reserve 后及每 chunk 前重验；漂移零字节交付并保守计 reservation。单 Range/If-Range、非法 multi/overflow range、bytes/count/in-flight 与并发 finalize CAS 保持精确。authenticated ciphertext tamper 失败关闭，不把损坏 bytes 交出。

撤销必须先关 grant 并 drain 活 read，再按生命周期毁 key 和 release source lease；key destruction 失败不能释放 source。unlink/purge 失败保留 store charge 并准确重试，不能把 released quota + 未删 ciphertext 当已清理。丢失密钥按 exact key version 撤销受影响任务并保留 lost 事实；可验证旧 KEK 的 ready artifact 在合法轮换后仍读取。

audit failure 使已发 grant 撤销后才返回 ticket 错误，read summary 有持久 retry；重启补账、保守 charge reserved request、拒绝 request replay。maintenance 只 flush terminal audit，不撤销仍活 delivery。runtime stop 先 terminalize active jobs，公平地处理有界 cleanup；单个失败不饿死后续任务，未证明清理不释放 quota。

## 存储运行时

Derived named volume `asset-worker-derived-store`、Export `asset-worker-export-store` 为独立持久加密存储。initializer 设置 `0700:10000:10000`，仅 Core 与 initializer 挂载，parser/updater 不能看到。Export root 为 `/var/lib/xirang-asset-runtime/export`，配置 `backup_assets.export.root` / `BACKUP_ASSETS_EXPORT_ROOT`；不得放在 /data、/backup、/logs、Content cache、Derived 或 Provider source。readiness 验实际配置/目录，不只是路径非空。

官方 All-in-One 镜像/端口不因 Worker 改变；不得通过改 Worker Dockerfile/seccomp/publish workflow 创建公共发布承诺。新增安装例子保持 feature false，说明 env true 仍受 GA 门禁；不重写已有环境文件。

## 回归证据

沙箱回归逐项破坏 rootfs、capabilities、NNP、seccomp、tmpfs flags、Landlock、进程/资源/网络边界，证明缺能力时不 advertise、不执行；拒绝 caller executable/argv/env/codec/font/model/path/URL/config，验证源只能通过 grant。使用确实有子进程的 fixture 验证 cancel/timeout/fence-loss 终止整个进程组且清 workspace，不能仅断言父进程退出。

Updater 回归覆盖双向 socket/mount 隔离、精确 UID/GID/owner/mode、跨 PID namespace PID=0 的合法情况和错误身份在 receipt 解码前拒绝；覆盖 inbox/trust owner/mode、symlink/no-follow、非 canonical manifest、错误 Ed25519 签名、tar/file digest/大小/时间/路径/类型越界、stale expected fingerprint，以及 fsync/journal/pointer rename 各边界的中断与恢复。任何失败都不得激活未验证 bundle，parser 只读挂载不能改 store。API 测试拒绝 bundle/multipart/URL/path/raw manifest，privacy canary 覆盖全部禁止字段；Compose static/runtime gate 验 offline-only、无 DNS/egress、无 swap、各 volume 权限，不能以配置文本检查代替进程和文件系统行为证据。

双引擎 `TestProcessingBehavior*`、`TestArchiveMemberBehaviorPostgres`、迁移 000067、used-down/CHECK/FK/UTC；真实 DSN required，不 skip。fixture bool 用 Go bool，不用跨引擎 0/1；SQLite memory DSN 包含每次 open 序号，支持 `-count`。

`TestConcurrentAtomicProjectionRetriesCommitExactlyOnce` 在 pre-tx blob read 与 in-tx upload update 各注入一次 SQLite lock，race/repeat 证明仅一个 durable winner，无二次 publication。覆盖 work_key/quota/interest races、stale fence、takeover、manifest 全字段否定、Search revoke failure 保留 key/blob、secret/malware states、closed UI fallback。

Export/成员门禁覆盖实际 Worker artifact、full canonical binding drift、HEAD/GET/chunk revalidation、cross-tuple contamination、forged fence、hash/cookie/session/proof、Range/budget/CAS/tamper、审计失败/幂等/restart、真实 SIGKILL recovery、revoke/drain、key loss/rotation、purge failure 与 store charge、stop sweep 公平性。执行 `TestExportBehaviorPostgres` 时设置 `REQUIRE_POSTGRES_EXPORT_TEST=1`，与其他破坏性 DB fixture 串行。

backfill 覆盖 media-inapplicable 首页、sibling capability、admission denied 仍 expected、SecretClassify/failed/canceled/中途 cancel 重开；前端第八页仍 cursor 必停并可 Continue。volume 变更执行 Compose checker/self-test、`ASSET_WORKER_STATIC_ONLY=1 scripts/test-asset-worker.sh`，Compose 行为变更加 `scripts/test-core-compose.sh`。
