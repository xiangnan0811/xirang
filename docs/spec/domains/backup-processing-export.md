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

### 工具链身份与构建输入

Core 与 Worker 共用 `capabilities` 中的生产工具链 inventory，schema 与摘要域均为 `xirang.asset.toolchain.v3`。除精确包版本、组件、profile 和策略外，inventory 必须绑定完整 APK 输入集的内容摘要。`deploy/build-inputs/lock.json` 是构建输入锁；生成的 `backend/internal/backupasset/processing/capabilities/production_apk_inputs.json` 仅嵌入 Worker 所需投影，不能把整个根 lock 编入二进制。

投影 schema 为 1，顶层仅有 `schema_version` 和 `inputs`；每项仅含 `role`、`platform`、`manifest_sha256`。固定包含以下四项，按 role/platform 排序：

| 输入角色 | 平台 | 约束范围 |
| --- | --- | --- |
| `cgo-builder` | `linux/amd64`、`linux/arm64` | Core/Worker 共用的 CGO 构建依赖 |
| `worker-runtime` | `linux/amd64`、`linux/arm64` | Worker 运行依赖 |

两种架构的 Core 和 Worker 均使用完整四项计算同一个 toolchain fingerprint，不能按运行平台只取其中一半。摘要必须是非零、64 位小写十六进制 SHA-256；缺项、重复项、未知 role/platform/字段、非规范 JSON 或不支持的 schema 均失败关闭，不能以空指纹继续开放能力。投影必须由真实归档 manifest 生成并与根 lock 校验一致，不能手填摘要或只校验字符串格式。

输入 manifest 的稳定内容身份覆盖相应 base 平台摘要、完整 APK 字节及安装语义；存储地址、tag、采集时间和 workflow provenance 不进入该内容身份。`core-runtime` 和 `supercronic-builder` 不进入 Worker 投影；只改这两组或归档位置元数据，不得改变 Worker 投影字节或 toolchain fingerprint。共用 `cgo-builder` 或 `worker-runtime` 任一架构的输入内容变化，则推进 toolchain 及依赖它的 pipeline 指纹。

inventory 的 `BuilderBase`、`RuntimeBase` 保留原始上游的版本与 digest 身份，对应根 lock 的 `bases.go.source`、`bases.alpine.source`；Docker `FROM` 使用各自 `ref` 中按 digest 固定的保留镜像。来源身份与实际取得位置必须校验对应，不能因移动归档地址改变工具链语义，也不能用地址相同替代内容相同的验证。输入归档按精确 root pins 保存完整安装事务，构建时离线验证并安装；这使已归档输入不再依赖上游保留旧 APK，但不补回归档机制引入前缺失的历史输入。归档、更新、安全扫描与保留流程见[发布手册](../../maintainers/release.md)。

安全更新若将传递包提升为显式 root，输入规格、Dockerfile 与生产 inventory 必须同步。完整事务中同源子包的修复须由各架构的真实 APK、安装清单及最终镜像扫描证明；仅更新 root 版本不足以证明全部子包修复。Worker Python 更新与对应四个包的验收要求见[自动化维护](../../maintainers/automation.md#worker-python-传递依赖安全更新)。

APK 输入锁负责重新取得构建依赖；runtime closure 负责验证最终安装结果。后者仍须在 APK 安装、用户与文件权限规范化、Worker 二进制及入口文件复制完成后生成，且继续覆盖 `/etc/apk/world`、`/etc/apk/repositories` 和 `/lib/apk/db/installed`。不得以输入摘要替代安装后闭包，也不得把最终镜像或闭包摘要写回输入 manifest 形成循环。

runtime closure、双架构 attestations 与签名 bundle manifest 的 schema 保持 1。v3 输入绑定会改变 Worker 二进制；必须从两种架构的新最终镜像重新取得 closure，再生成同时绑定两者的 `toolchain/attestations.v1.json` 并重新签名 bundle。旧 bundle 中的闭包证据不能沿用到新二进制；Core/Worker 也必须共享相同的工具链合同与输入投影，不能混用 v2/v3 身份。输入留存不改变 Worker 的非 GA 状态、默认关闭和既有签名、运行时校验及漏洞扫描门槛。

### Updater 身份、签名与原子激活

Updater 使用独立进程、UID/GID `10002:10002`、PID namespace、socket 和可写 bundle volume。它只读挂载独立 updater runtime 到 `/run/xirang`，不挂 worker runtime，parser 也不可反向观察 updater socket。Core 在 setgid mode `2770`、owner/group `10000:10002` 的 updater runtime 中创建 `/run/xirang/asset-worker-updater.sock`，socket mode `0660`、owner/group `10000:10002`，并在解码 receipt 前验证 socket 和 Linux peer credential。跨 PID namespace 的 `SO_PEERCRED` PID 可以为 0；PID 仅供诊断，不是认证主体，认证由受保护 UDS、精确 UID/GID 和 socket owner/mode 共同完成。

content-addressed bundle volume 是 parser/updater 唯一共享数据 mount：根目录 owner/group `10002:10000`、setgid mode `2750`，updater 可写、parser 强制只读。inbox 和 Ed25519 trust secret 仅 updater 可见；socket volumes、bundle volume、secret mount 不得替代或合并。

默认更新路径是 signed offline import。固定 updater-only inbox 中候选目录要求 `10002:10002`、mode `0555`；Ed25519 trust 文件要求 `10002:10002`、mode `0440`。Updater no-follow 扫描，验证 canonical manifest、Ed25519 signature、精确 tar/file SHA-256，以及大小、时间、路径和文件类型限制；先 fsync content-addressed store，再通过 journal 和原子 pointer rename 激活，不能在校验或耐久化未完成时切换 active bundle。

浏览器和 Core HTTP API 不接收 bundle bytes、multipart、URL、服务器路径、inbox 文件名或原始 manifest。Admin API 仅接受脱敏 candidate ID 和 expected fingerprint 的小型 JSON 控制请求。Compose updater 同样 `network_mode: none`，只支持 offline-only。Online updater 默认关闭；任何单独的 online 部署必须同时具备 exact HTTPS origin allowlist、独立 allowlist proxy/firewall、隔离网络及 updater-only credential secret。应用 allowlist 不能代替 egress firewall，parser 永远不继承 updater 网络或凭据。

Updater 的注册 wire 是签名离线包经过 inbox 验证后生成的 `InboxReceipt`，由 `DecodeRegisterCandidateRequest` 严格解码；顶层与嵌套未知／重复字段、尾随 JSON、不支持的 schema 和无效身份／版本／摘要／验证时间均拒绝。注册消息不携带 URL、凭据、原始输出或 bundle 内容。metadata 的 verified／active 等生命周期由 Core 的关系表及注册／激活事务拥有，不是独立 metadata JSON 的读写入口；不得以测试便利格式替代真实签名包、receipt 或持久化合同。

处理 API、日志、指标、审计与管理聚合仅记录闭合 capability/profile/state/error category、不透明资源引用和有界计数。禁止记录 Provider locator、宿主/tmp/bundle/inbox 路径、Worker UID/PID/证书、credential、grant/session/attempt/fence/activation secret、原始 argv/stdout/stderr/tool diagnostic、manifest/body 或源内容；底层 peer PID 可作私有诊断输入，不因此变成公开日志字段。

## 队列、租约与原子发布

- 配对 `000067_backup_asset_processing` 用相同 closed `ProcessingState`，独立 transition revision、stable error、retry/cancel/supersede/expiry 产品。partial unique 保证每 canonical work_key 一个 current job、每 job 一个 current attempt、每 owner tuple 一个 active interest；并发请求由 DB 合并，不以进程 mutex 作为唯一性契约。
- canonical work key 绑定精确 point／entry／Catalog generation、来源与条目 fingerprint、Provider revision、capability/schema、pipeline、output profile、security policy 和完整输出参数。同 key 的并发请求共用作业、各自保留 interest；已完成产物另按精确来源及当前 profile／pipeline／policy 查询，不因 strong fingerprint 相同就跨恢复点跳过处理。不同作业或 attempt 仍独立执行；相同输出可按 digest、size、cipher format 和 chunk size 共用 Derived blob，多个 upload 的 `staging_id` 可以指向同一 blob，多个 ArtifactSet 也可有相同 manifest digest。
  每次 upload、ArtifactSet、Artifact 和来源 BlobReference 保留独立身份；同一 grant 的 ordinal 和每个 job 的 ArtifactSet 仍分别唯一。来源引用绑定各自 point／Catalog generation／entry 和 source fingerprint；授权读取须验证活跃引用、匹配的 artifact 及可读且已完成必要 Search 投影的 ArtifactSet，不能跨来源串用授权或撤销。
  命中已有 blob 仍须完整流式验证实际大小、EOF 和 SHA-256；复用不重复计入物理容量，Sink grant 仍按本次明文字节结算。并发相同输出通过有界重试收敛，不能绕过内容校验。
- Worker pull 同时拥有短 Worker lease 和 `processing_job` RecoveryPoint lease。heartbeat 在同事务续 attempt、RP lease、live grants。takeover 建新 attempt/fence，旧 heartbeat/grant/upload/projection/manifest 永久失效。
- activation secret 仅 hash 持久化且一次性；Input/Sink request count、bytes、in-flight reservation 双引擎事务约束。最后一个 interest 删除先 revoke 两 grants 再 cancel_requested；仍有其他 interest 保持共享 job。
- Input activation 响应使用 schema v2，携带服务端实际准入的 `max_bytes_per_request`；Worker 要求正值并取其与本地上限的较小者。其余协议 DTO 保持 v1。严格解码拒绝新旧 activation 混用，Core/Worker 必须同次配套切换，没有旧协议回退。
- 每次 HTTP 输入读取在 200 前声明 `X-Xirang-Input-Status` trailer，流式复制与句柄关闭都成功才为唯一的 `ok`；失败为 `failed`，不在 200 后追加 JSON。Worker 有界读取至 EOF 并关闭响应体，拒绝 transport/read/close 错误、超长、缺失或重复终态，不交付部分 bytes。
- Worker 以绝对 offset 按 `min(服务端每请求预算, 本地上限, 16 MiB)` 预取单块，消费方小 buffer 不增加请求次数；响应必须恰好匹配所请求范围。仅顺序源只允许从零一次读取整文件，超过单次预算在工具调用前拒绝；空文件不发送零长度请求，prefix 探测也受相同预算约束。累积 bytes、请求数和在途预算不扩额、不重试拼接前缀。
- malware 生产 profile 要求 stat/range，改变 canonical pipeline 指纹。strong 输入的工作描述符 fingerprint 必须是 64 位小写 SHA-256；materialization 流式核对实际字节数与原始 SHA-256，只有精确 EOF 和摘要匹配才可启动 scanner。短流、超量、同大小内容漂移或终态错误不得生成成功产物；none/weak 不冒称通过强摘要校验。canonical `malware.scan.v1` 与 `signature_scan_v1` 格式不变，旧 pipeline 结果不能满足新 Recovery 证据查找，须重新处理。
- Derived 使用独立 `derived_store` key、每 blob 随机 DEK、authenticated chunks、opaque locator 和 explicit references。输出及上传校验的暂存不得在 Derived Store 持久介质上留下明文；临时文件须保持加密，且失败、取消和崩溃后的残留必须可安全收束。一个上传重试在已经安装过候选后再次重激活，即使逻辑 blob ID 相同，也须以新密钥和 nonce 将已认证字节流式重加密至新 inode；取消或失败保留旧候选及已发布硬链接的字节和元数据，不能原位改写。manifest 成员/digest/MIME/count/size/completeness/source/policy 任一不符拒整集并清理或调和不可见 staging；late/tampered output 失败关闭。
- 暂存和已发布引用共同保活共享 blob：`ref_count` 是活跃已发布 BlobReference 数，不计 `staged` upload；判断清理时还须检查有效的 `staged` upload，`committed` 已由发布事务生成引用，不重复计数。有效性须证明当前 job／attempt／grant／RP lease 关联、全部期限和 token 对应的 fence hash。Store discard、产物集撤销与来源销毁须先按序锁齐所有目标 blob 的 staged holder job，在同事务内、blob 锁前按观测身份收束失权 upload；授权锁保留到销毁提交，使已开始且尚未提交的合法续租先完成。blob 锁内只读计数并修正实际引用数；发现未覆盖的新 holder 时整事务回滚重建锁集合，不能反向取 job/upload。PG 清理使用 READ COMMITTED，锁后新语句及新时间判定；heartbeat 同样在授权锁后重验期限，不能用等待前时间恢复已到期 holder。仅当活跃引用和有效暂存均为零时才清除 wrapped DEK、置 blob 不可用并删除对应密文，撤销一个来源不得破坏其他来源的发布或暂存。
- 最后引用清理后再次上传相同字节时，保留逻辑 blob ID 和历史外键，使用新的随机 DEK 和独立 locator 重激活；旧代次的延迟删除、失败状态回写和 DEK rewrap 都须比较观测 locator，rewrap 还比较旧 envelope，不能把旧密钥材料写入新代次。staged 恢复在 blob 锁内比较观测代次后才检查、rename 和同步文件；旧快照不得先操作文件再以条件更新弥补围栏。不可读后的来源撤销须退出 blob 锁后按来源锁序进行。旧 ArtifactSet／BlobReference 不因 blob 重激活而重新获得授权。
- `ArtifactSink.CommitManifest` 将 upload/blob/artifact 与 Search content/OCR/classification/ref/excerpt/coverage 在一个 fence 和事务内发布；发布、撤销与退休按 point→job/set→attempt→grant/upload→升序 blob 加锁。准备完成或等待锁后仍须重验当前授权和绝对期限，pending 恢复在 Search 发布前及最终完成前都复核。Worker 短租约过期仅允许现行 RP lease 心跳晚于 attempt 最后心跳的既有受限恢复路线，不能延长 job／attempt 绝对期限或绕过普通 Sink grant。`PreparedDerivedProjection.PublishTx` 只用 supplied tx、以 artifact-set ID 幂等，transient rollback 可复用 prepared publication。
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

- Runtime 收集按 Catalog `entry_id` keyset 遍历，每次最多 100 个 descriptor（另受配置批次及准入约束），以 system-owned background interest、数值 priority `100` 经真实 `RequestWork` 入队。共享 job 的 active interest 先选择 interactive class，再取该 class 的最大 priority；不同 job 的 pull 先按数值 priority 降序，数值相同时 interactive 优先，再按排队时间及 ID。background 只用后台槽位，interactive 先用保留交互槽位，再借后台槽位；槽位占用由该 Worker 的持久 current active attempts 计算。
- 每小时任务／字节预算由持久单行账本跨请求、进程重启和共享数据库的 Runtime 实例累计。只有 `RequestWork` 实际创建新的 current background job 时，在同一事务收费 `1` 任务及 Catalog 逻辑大小；零字节来源仍收费一次任务。Provider kind 和预计字节只由精确恢复点／Repository／Catalog 关联取得，交互新 job 也保存这些元数据，调用方不能提供大小或分组来绕过限制。预计输入不等于实际网络流量，attempt 的读取预算仍由 Grant 账本独立约束。
- 同 work key 复用作业和附加独立 interest、同 job 的失败重试均不重复收费；取消／终态不退款，后续新建替代 job 再收费。交互新建和升级前历史作业不补收后台创建费，即使后来成为 background；所有后台执行仍经过真实并发准入。current job 的复用不因暂停而拒绝，被拒或事务回滚的新建不留 job／interest／费用；QueueMax 和 Worker 槽位限制继续保留。
- 账本采用 UTC 整点小时窗，只在实际收费时向更新整点滚动；新整点的收集快照可返回零用量而不改写账本。未来窗口遇时钟回拨时拒绝新增后台扣费，保留原累计。任务／字节为受检非负 `int64`，按剩余额度比较，不允许整数溢出获得准入。修改暂停、额度或并发配置不清空当窗用量；生产准入在账本锁后通过调用方事务读取最新完整设置，其他 Runtime 的下一事务看到已提交值，本机缓存只服务显示／调度。
- Provider 桶按规范化 `provider_kind`（`restic/rsync/rclone/command`）、能力桶按 capability，在全数据库统计当前 background job 的有效 current active attempt。要求 job、attempt、短租约和绝对期限仍有效，且现行 processing RP lease 的身份、状态和 fence hash 匹配；`cancel_requested` 不占后台桶。完成、失败、取消、supersede、停机及到期解除占用；计数释放不保证已经失权的外部进程瞬时停止，既有取消／期限和输出围栏继续生效。
- Pull 先锁 Worker，再锁来源／job 并重读优先级及槽位；后台候选在 metadata→ledger 锁内读取最新策略与占用，并将锁保留到 attempt／lease／grant 提交。暂停或桶满的候选通过 savepoint 回滚释放该候选全部锁，再扫描其他桶或交互作业。来源／生命周期准入的语义冲突也回滚候选 savepoint 并继续；数据库、context 和回滚错误直接传播，若没有候选成功则保留来源冲突错误。交互不受后台暂停、小时配额和后台桶限制；运行中提权退出后台桶，但保留已分配的 attempt slot，仍活跃时移除交互 interest 不使它中途重入后台桶，queued／retry_wait 可按剩余 interest 恢复 background。
- 后台 Heartbeat 按 Worker→current job→metadata 共享锁→ledger→attempt／RP lease／grants 的顺序持锁至提交，所有必要锁取得后重新采样时间并重验身份、fence 和期限；合法续租即使在旧期限之后提交仍有效。此串行边界不读取策略、不重新准入或收费、不重置窗口；交互 Heartbeat 绕过 ledger，active trust 的 draining Worker 仍可续租。所有 Transition 先锁 Worker 作为身份互斥，再锁 job／attempt 和后续资源，不额外要求 active trust 才能取得互斥锁。
- RequestWork、Pull、Invalidation 和快照遵循来源／job→metadata 共享锁→ledger 的方向，其他来源写入者继续遵循 point→job 锁序；PG 使用 READ COMMITTED 并在等锁后发新语句、重新采样时间。多来源失效先升序锁齐全部 point 和实际 current jobs，再接触 ledger；可选 replacement 被普通配额拒绝只回滚本次 replacement，保留旧失效和此前成功项。PG 快照先持 jobs 表 ACCESS SHARE，再取 metadata／ledger，避免与 direct-down 的 jobs 排他锁方向成环。缺端口、来源、种子、schema 或设置读取失败均保留真正错误，不能当零用量或普通额度拒绝。
- 收集以持久当前小时用量、queued／retry_wait 与有效 active 后台 job 的分组占用作提示，再累计批内选择；它不预留额度，最终由 RequestWork／Pull 事务裁决。Queue 遇暂停／任务／字节／未来窗口普通拒绝停止本轮并返回此前成功数；malware continuation 保持未排入／未证明，secret continuation 返回此前排数并停止，不制造安全证据或新增自动重试策略。`RecentWindow`／`HistoryAgingStep` 保留读取与有效性校验，当前未使用 latest／recent／history 评分或 aging 调度。
- `ExpectedDescriptors` 不应用配额筛选，只有 leftover walk 真实走到 Catalog end 的 empty 才算 proven。InspectedLimit 默认 200/max1000，按 generation+point 内存 keyset cursor；本页预算耗尽且 0 descriptor 仍返回 unproven marker，Queue 不 enqueue marker。配额或并发竞争拒绝不能越过未入队项推进 cursor 或标记 complete。
- 未证明单位为 `(entry_id,capability)` 而非整行。walk fingerprint 包含排序 capability 名和 SecretClassify；failed/canceled revision（count、latest updated_at、id）变化清 complete。completeness 绑定 walk-start revision，不能漏掉 cursor 后方并发 cancel。
- 管理面 scan/rebuild 每次点击最多 8 页，保存 scanCursorByRepository/rebuildCursorByRepository，以 Continue 继续；不无限 drain cursor。API/面板保持 lazy，不能进入 startup chunk 破坏既有预算。

## Export 与归档成员交付

Export 管理自己的持久任务、attempt/fence、加密产物、quota 和交付账本，不混入 backup_asset content grant 的资源 tuple。签发必须冻结 exact ready artifact、recorded KEK/job DEK envelope、attempt fence digest、session/cookie/action/path/subject；坏 metadata/伪造 fence 在账本副作用前拒绝。归档成员冻结 exact Derived tuple，独立 ledger 且 Range none，不能夹带 Export tuple，反之亦然。

创建 Export 和签发下载票据分别要求 `asset.export_create` 和 `asset.export_download` proof，均须绑定当前请求的有效主登录会话并检查该会话未撤销；下载票据使用同一会话绑定。归档成员的 `asset.download` 合同不变。

冻结选择保留 Catalog 的原始 entry fingerprint：仅 `none` 强度的 directory、symlink、hardlink、special、unknown 可为空；普通文件及 strong/weak 必须非空。原值参与 canonical digest 和后续重验，空值变为非空同样属于漂移。非文件继续按现有归档策略处理，不打开 Provider 内容读取。

源的 MIME 元数据允许为空，AttemptBroker 保留原值，不据此判定 source drift；Export 仍核对冻结 MIME、大小及 source/entry fingerprint。空文件只做 stat 与重验，不进行顺序内容读取，也不把零字节 stat reservation 记作内容读取。

Export 输入 session 以冻结的任务绝对期限为上限；每次读取仍在持久预算事务中核对当前 attempt、item 和未过期租约。续租后的活读取不会仅因原 reader reservation 的旧到期时间被回收；其真实关闭照常结算，失权或到期后按保守账本回收。过期 Worker 配额扫描只选择 running/sealing 任务；选取后按配额桶、任务、attempt 和配对 reservation 的锁顺序重验，合法取消及已完成或正在进行的生命周期收束交给生命周期，完整一致且已续租至扫描截止时间之后的配对不被回收，损坏或无法证明归属的配对仍报围栏错误。ready 发布已释放 Worker 配额，后续取消或清理须核对同一 attempt 的已释放配对记录，不能再次释放。

- item 转换由实际 Worker、Checkpoint 与持久快照重验共同校验条目类型、spool 和字节证据；非文件可直接由 pending 进入 packed，普通文件 packed 必须具备同 attempt 的有效 read spool，read 后失败须有已确认的 pre-header 恢复。当前 attempt 的终态不能经普通 checkpoint 回退；合法 Claim 必须事务性重验任务、source authority、deadline、旧 attempt 租约及重试资格，创建新 attempt/fence 并重置当前 item／汇总投影，保留旧 item-attempt 历史。未到期 owner 的竞争 Claim 和接管后的旧 token 写入必须拒绝且不改写当前投影；调用方布尔值和 SQL 值域 CHECK 都不构成接管证明。

HEAD 在 metadata commit 前也重验 live proof/artifact/member binding，GET 在 reserve 后及每 chunk 前重验；漂移零字节交付并保守计 reservation。单 Range/If-Range、非法 multi/overflow range、bytes/count/in-flight 与并发 finalize CAS 保持精确。authenticated ciphertext tamper 失败关闭，不把损坏 bytes 交出。

撤销必须先关 grant 并 drain 活 read，再按生命周期毁 key 和 release source lease；key destruction 失败不能释放 source。unlink/purge 失败保留 store charge 并准确重试，不能把 released quota + 未删 ciphertext 当已清理。丢失密钥按 exact key version 撤销受影响任务并保留 lost 事实；可验证旧 KEK 的 ready artifact 在合法轮换后仍读取。

audit failure 使已发 grant 撤销后才返回 ticket 错误，read summary 有持久 retry；重启补账、保守 charge reserved request、拒绝 request replay。maintenance 只 flush terminal audit，不撤销仍活 delivery。runtime stop 先 terminalize active jobs，公平地处理有界 cleanup；单个失败不饿死后续任务，未证明清理不释放 quota。

## 存储运行时

Derived named volume `asset-worker-derived-store`、Export `asset-worker-export-store` 为独立持久加密存储。initializer 设置 `0700:10000:10000`，仅 Core 与 initializer 挂载，parser/updater 不能看到。Export root 为 `/var/lib/xirang-asset-runtime/export`，配置 `backup_assets.export.root` / `BACKUP_ASSETS_EXPORT_ROOT`；不得放在 /data、/backup、/logs、Content cache、Derived 或 Provider source。readiness 验实际配置/目录，不只是路径非空。

官方 All-in-One 镜像/端口不因 Worker 改变；不得通过改 Worker Dockerfile/seccomp/publish workflow 创建公共发布承诺。新增安装例子保持 feature false，说明 env true 仍受 GA 门禁；不重写已有环境文件。

## 回归证据

沙箱回归逐项破坏 rootfs、capabilities、NNP、seccomp、tmpfs flags、Landlock、进程/资源/网络边界，证明缺能力时不 advertise、不执行；拒绝 caller executable/argv/env/codec/font/model/path/URL/config，验证源只能通过 grant。使用确实有子进程的 fixture 验证 cancel/timeout/fence-loss 终止整个进程组且清 workspace，不能仅断言父进程退出。

构建输入回归逐项改变 `cgo-builder`、`worker-runtime` 的 amd64/arm64 manifest 摘要，证明 toolchain/pipeline 指纹均推进；只改 Core runtime、supercronic 或存储/provenance 元数据时，证明 Worker 投影字节和指纹保持不变。覆盖投影与真实根 lock 漂移、缺失架构、重复/未知字段、非法摘要、v2 schema 以及无效输入在工具探测前失败关闭；保留安装后 APK 数据库和 world 文件的闭包覆盖、错误架构/旧 attestations/文件内容或权限漂移的拒绝证据。APK 离线取得与安装验证的成功，不能替代最终 Worker 的双架构运行时和签名闭包验证。

Updater 回归覆盖双向 socket/mount 隔离、精确 UID/GID/owner/mode、跨 PID namespace PID=0 的合法情况和错误身份在 receipt 解码前拒绝；覆盖 inbox/trust owner/mode、symlink/no-follow、非 canonical manifest、错误 Ed25519 签名、tar/file digest/大小/时间/路径/类型越界、stale expected fingerprint，以及 fsync/journal/pointer rename 各边界的中断与恢复。任何失败都不得激活未验证 bundle，parser 只读挂载不能改 store。API 测试拒绝 bundle/multipart/URL/path/raw manifest，privacy canary 覆盖全部禁止字段；Compose static/runtime gate 验 offline-only、无 DNS/egress、无 swap、各 volume 权限，不能以配置文本检查代替进程和文件系统行为证据。

双引擎 `TestProcessingBehavior*`、`TestArchiveMemberBehaviorPostgres`、迁移 000067、used-down/CHECK/FK/UTC；真实 DSN required，不 skip。fixture bool 用 Go bool，不用跨引擎 0/1；SQLite memory DSN 包含每次 open 序号，支持 `-count`。

`TestConcurrentAtomicProjectionRetriesCommitExactlyOnce` 在 pre-tx blob read 与 in-tx upload update 各注入一次 SQLite lock，race/repeat 证明仅一个 durable winner，无二次 publication。覆盖 work_key/quota/interest races、stale fence、takeover、manifest 全字段否定、Search revoke failure 保留 key/blob、secret/malware states、closed UI fallback。

Export/成员门禁覆盖实际 Worker artifact、full canonical binding drift、HEAD/GET/chunk revalidation、cross-tuple contamination、forged fence、hash/cookie/session/proof、Range/budget/CAS/tamper、审计失败/幂等/restart、真实 SIGKILL recovery、revoke/drain、key loss/rotation、purge failure 与 store charge、stop sweep 公平性。执行 `TestExportBehaviorPostgres` 时设置 `REQUIRE_POSTGRES_EXPORT_TEST=1`，与其他破坏性 DB fixture 串行。

backfill 覆盖 media-inapplicable 首页、sibling capability、admission denied 仍 expected、SecretClassify/failed/canceled/中途 cancel 重开；前端第八页仍 cursor 必停并可 Continue。volume 变更执行 Compose checker/self-test、`ASSET_WORKER_STATIC_ONLY=1 scripts/test-asset-worker.sh`，Compose 行为变更加 `scripts/test-core-compose.sh`。

后台队列回归须连到真实 RequestWork／interest／PullAttempt／Worker slot，覆盖跨调用及重启的持久小时预算、两个实际 Runtime 与独立 settings 实例的策略即时生效、同 key 一次收费、重试／取消替代、UTC／回拨、事务回滚、最后一个 Provider／capability 槽竞争、跨桶调度和生命周期释放。普通拒绝后 ExpectedDescriptors 保持 unproven。双引擎迁移覆盖精确来源回填、历史不补费、结构／种子／索引／guard 漂移失败关闭及实际 metadata／direct-down 竞争；升级／回退的数据和密钥边界见[运维恢复](../../admin/backup-recovery.md)。复用回归分别验证同 key 并发共用、精确已完成 publication 查询、真实 Sink 的跨点相同输出独立发布与来源授权、暂存／发布并存和最后引用清理、重激活及并发上传；两点共用输出 blob 不代表跨点省略处理。Round 25 的真实第二次上传唯一冲突保留为历史失败，后续真实 Sink 双发布验收承担修复证明，预置 source-local reference 不能替代。受控 source／projection 回调不外推远端 Provider 或 parser 验收。
