# 备份资产 Worker 能力与增强预览

## 0. 文档状态与授权边界

- Trellis task：`.trellis/tasks/07-20-backup-assets-worker-capabilities`。
- task status：`in_progress`；parent：`07-12-backup-data-explorer-design`，parent 继续是
  `planning` program tracker，不是本 Child 的实现目标。
- 分支：`codex/backup-assets-worker-capabilities`；base/HEAD/main/origin/main 均为
  `be6eebbe50dfd78e071c6d73e9c81493487fb4d5`，即 Child 10 / PR #394 merged main。
- 真实 program 交付进度为 10/15；Trellis 已实例化 11 个 Child 不等于 11 个已交付，
  不能据此归档父任务。
- 用户已在本 Child 明确授权 Phase 2；总控已独立完成 `prd.md`、`design.md`、
  `implement.md` 与 current-main evidence 的技术审阅并批准规划包。当前 planning
  package 为 `approved`，implementation 为 `authorized`，无需再次审批产品方案。
- 总控已完成 workflow transition 与 `task.py start`，Child status 为 `in_progress`；
  产品代码/测试正在实施，migration 仍禁止，stage、commit、archive/journal、push、PR、
  CI、merge 与 post-merge/release/deploy mutation 尚未执行。

Current-main 证据见 `research/current-main-evidence.md`。本 PRD 定义需求；技术合同与
exact future manifest 分别见 `design.md`、`implement.md`。

## 1. Goal

在 Child 10 已落地的持久队列、Worker 协议、Content Attempt Broker、Derived Store、
Search projection port 和 `000067_backup_asset_processing` schema 上，交付一组真实但
隔离的备份内容处理能力，并让用户在现有备份资产工作台中安全地看到增强预览、处理
覆盖、安全 finding 与明确降级状态。

能力范围固定为：图片缩略图、有限文本/OCR、PDF/Office 文档渲染、恶意软件扫描
finding、媒体探测/转码、归档索引/单 member 提取，以及可选的有限秘密分类。Worker
只处理 Core 经一次性 Input grant 提供的原件副本，产物只经一次性 Sink grant 返回；
它不修改 Provider bytes，也不获得数据库、Provider locator、SSH/Restic/Rclone/
Command credential、宿主路径或任意网络访问。

无 Worker、Worker 未配置、能力不匹配、bundle 未激活或增强处理失败时，Catalog、
现有元数据 Search、Content Broker、workspace、原生预览、下载和 recovery 必须继续
可用。系统显示稳定、诚实的 capability reason，不把“未部署”变成 failed job、备份
失败或噪声告警。

## 2. 用户价值与成功定义

1. 操作员可以在不下载到个人设备、不执行活动内容的前提下，检查常见图片、文本、
   PDF/Office、音视频和归档内容；超限或不支持时仍能回到原生预览、下载或恢复。
2. 管理员可以看到按 Provider/Task/capability 聚合的 eligible、complete、partial、
   queued、failed、unsupported 覆盖，控制回填 pause/quota，并查看经过脱敏的 updater
   版本、签名身份、bundle fingerprint 与最近成功/失败。
3. 恶意软件 finding 是一次成功扫描的安全结果，不篡改 RecoveryPoint 信任状态；
   malware/secret gate 由服务端强制，前端状态不能放宽权限。
4. bundle fingerprint、capability schema、pipeline、profile 或 security policy revision
   变化时，旧派生物进入明确 stale 状态并按配额回填；旧 attempt 永远不能写 Derived
   或 Search。
5. 同一目标输出只在强 source/entry fingerprint、capability/schema、pipeline、output
   profile、policy revision 和所有输出参数完全相等时复用。弱 fingerprint 不跨
   RecoveryPoint 复用。
6. content/OCR/classification 的 Derived 引用、HMAC postings、excerpt reference 与
   field coverage 在同一数据库事务和同一 processing fence 下发布；不存在一侧成功、
   另一侧失败的可见中间状态。
7. Worker 镜像在 amd64/arm64 构建和扫描，但不发布；官方 all-in-one core image、
   公开端口和 Docker Hub 发布合同不变化。

## 3. 已确认的 Current-Main 事实

1. paired SQLite/PostgreSQL `000067_backup_asset_processing` 已合并；它已包含 processing
   job/interest/attempt/grant/upload、Worker identity/capability、加密 Derived blob/set/
   artifact/reference 与 generic updater metadata。`000068...000071` 不存在，继续保留
   给 Children 12-15。
2. `WorkDescriptorV1` 已把 source/entry fingerprint、capability/schema、pipeline
   fingerprint、output profile、security policy revision 与输出参数纳入 canonical
   work identity。现有队列只有 `interactive/background` 两类，但持久
   `effective_priority` 足以在不改 schema 的前提下表达 latest/recent/history 次序。
3. `/internal/v1/asset-worker/...` 已有 authenticated transport identity、pull/lease/
   heartbeat、Input/Sink activation、body/rate limit 和 sanitized error mapping；
   `GET /api/v1/admin/backup-asset-processing` 已是 Admin-only、feature-gated 的聚合摘要。
4. `NewProductionCapabilityRegistry()` 与 `NewProductionWorkerCapabilitySet()` 当前都为空；
   `asset-worker` 是 protocol-only。`RequiresMaterialization=true` 当前稳定失败为
   `materialization_disabled`。
5. 现有 artifact role 只允许 `noop/content/ocr/thumbnail/metadata`，coverage validator
   只接受 `{"schema_version":1,"kind":"all"}`；processing 与 updater stable failure
   code 也由 000067 CHECK 冻结。Child 11 必须在这些闭合集合内实现，不新增 migration。
6. `runtimeDerivedProjectionPort` 已接入 Child 7 `ContentIndexIngest` 实例但生产
   `Publish/Revoke` 仍 fail closed；当前 manifest 流程先写 Derived，再在事务外请求
   Search publish，因此不能直接作为 Child 11 的 atomic publication 合同。
7. `backup_assets.enabled=false`，local/remote Worker transport 也默认 false；静态 socket、
   trust material 和 Derived root 均标记 RequiresRestart。Command Provider 仍 typed
   `task_artifact_contract_missing`。
8. 前端已有 escaped text、safe raster、same-origin PDF、native audio/video、metadata/
   hex 与 attachment fallback；API 使用 central `request()`、私有 raw DTO、camelCase
   mapper。Backups 路由已 lazy，但 main bundle 仅余约 JS 1.91 KiB / CSS 0.79 KiB。

## 4. In-Scope Requirements

### 4.1 Closed capability 与 tool runner

- 注册且只注册这些 capability：`image.thumbnail`、`text.extract`、`image.ocr`、
  `document.convert`、`malware.scan`、`media.probe`、`media.transcode`、
  `archive.inspect`、`archive.extract_entry`；`secret.classify` 为显式可选且默认关闭。
- 每个 capability 只有版本化、allowlisted profile。请求不能提交任意 executable、
  argv、codec、字体、语言、路径、环境变量、URL 或 tool configuration。
- runner 不经 shell，binary path/argv/env/cwd 均由 typed profile 构造；进程组、wall
  time、CPU/memory/PID、stdin/stdout/stderr、output files 和 tmpfs 都有硬上限。取消或
  lease/fence 丢失必须终止完整进程树并销毁 workspace。
- 输入先做 media sniff 与 declared MIME 一致性检查；输出由 Core 重新校验 MIME、
  数量、大小、digest、canonical metadata/coverage 和安全策略。stdout/stderr 只保留
  有界、脱敏的 category/code，不持久化 raw tool output。
- Worker 不修改 Input bytes；所有 path materialization 只写 job-exclusive tmpfs，
  `noexec,nosuid,nodev`，无普通磁盘 fallback。

### 4.2 文件类型与安全边界

- Image：只输出静态 raster thumbnail/安全 metadata；限制输入、解码像素、帧数、
  尺寸和输出；本 Child 不提供 active SVG/HTML raster profile，它们不得进入图片/
  文档工具，也不得在主 origin inline，只保留 escaped text/download/recovery fallback。
- Text/OCR：限制输入、编码、行/字符、语言、页数、像素和输出；invalid UTF-8、
  truncation 与 partial coverage 必须显式。OCR 不隐式下载语言模型。
- PDF/Office：禁宏、脚本、外链、模板、字体/图片远程加载和写回；限制页数、像素、
  转换时长与输出。失败回到 metadata/download/recovery，不执行文档。
- Malware：扫描结果是 `not_scanned/no_finding/finding/stale` 中的 typed 状态；positive
  finding 是成功结果，server-enforced gate 决定预览/下载是否警告或阻断。
- Media：probe/transcode 只接受本地 file/pipe 协议和 closed codec/container；限制
  streams、duration、resolution、bitrate、frames 和 output bytes，malformed media
  失败关闭。
- Archive：限制格式、输入、depth、entry count、expanded bytes、compression ratio 和
  单 member bytes；绝对/穿越路径、symlink/hardlink/device/FIFO、嵌套炸弹与 encrypted
  archive 均拒绝。member 使用 opaque token/ordinal，不接受客户端路径。
- Secret：Core 的已有 path/name/MIME/有界内容分类保持第一道 gate；可选 Worker 只对
  已授权的有限 text/OCR 做增强。`unknown` 与 `secret` 均保持 fail closed。

### 4.3 Updater 与 bundle

- updater 是与 parser Worker 分离的身份和进程；只实现 generic signed metadata/bundle
  contract，不实现某一家供应商的账号、市场或插件系统。
- 默认支持管理员审核的 signed offline import，但 bundle bytes 不经过浏览器或 Core
  HTTP API。运维侧只可把 bundle 放入 updater-only 固定只读 inbox；updater 以 no-follow、
  大小/时间上限扫描、验签并复制到 content-addressed store，再由 Core 分配 000067
  现有 metadata row ID 作为 opaque one-purpose candidate handle。Admin UI/API 只用
  小型 JSON 请求触发扫描、查看 sanitized candidate 和
  确认激活；不接受上传体、multipart、URL 或服务器路径。
- 可选 egress 只能访问 exact allowlisted HTTPS origins，拒绝 scheme/host/port 漂移与
  越界 redirect。egress credential 只存在 updater secret file/memory，不进入 Core
  settings、DB、API、日志或 Worker envelope。
- manifest 和每个文件使用 canonical schema、Ed25519 signature 与 SHA-256 digest；
  验证后写入 content-addressed bundle store，fsync 后以原子 pointer rename 激活。
  Worker 只读挂载 active bundle，不能写 store，也不能继承 updater network namespace。
- Core 仅持久 000067 已允许的 source kind/opaque ID、version、manifest digest、signing
  key fingerprint、bundle fingerprint、state、stable failure code 和 UTC 时间；不存
  URL、credential、raw manifest/body、bundle path、raw diagnostic 或 secret。

### 4.4 Invalidation、回填与 atomic publication

- 激活新 bundle 前计算受影响 capability/pipeline；一个有界事务切换 updater metadata
  并递增受影响的 internal content/OCR pipeline revision，使旧 Search/Derived 立即逻辑
  stale。之后由可恢复的 bounded batch 原子撤销旧 Derived/Search、supersede 旧 work
  并登记新 descriptor/interest；旧 attempt 发布时独立重验 active fingerprint。任何
  激活失败保持旧 bundle active，不执行无界全表事务。
- 回填/重处理顺序固定为：每 lineage 最新 retained point；当前交互请求；近期 retained
  history；更老 history。映射到现有 `interactive/background + effective_priority`，不新增 priority
  schema。管理员可 pause，并可收紧 batch、每小时 job、Provider/capability 并发与字节
  quota；回填不能占用 interactive reserve，也不能挤占 backup/recovery 资源。
- 跨 RecoveryPoint 派生复用只允许 strong source/entry fingerprint 与全部输出身份一致；
  新 RecoveryPoint 仍创建独立 authorization reference 与 fenced Search publication。
  weak/none fingerprint 只能在精确 AssetRef 内复用。
- text/OCR/classification artifact 先完成 bounded canonical decode/tokenization，再在一个
  outer GORM transaction 内重验 job/attempt/RP lease/fence/source/policy，创建 Derived
  set/artifact/reference、写 postings/classification/excerpt/coverage、标 projection
  published、完成 job。任一错误整体 rollback；stale attempt 两侧都不能写。
- 非 Search 产物也必须在现有 fenced manifest transaction 中有效一次发布。malware/
  secret gate 在发 ticket、读 Derived 和返回 Search match 三处均由服务端复核。

### 4.5 API、UI、审计与隐私

- Asset preview job API 使用 exact `AssetRef`、ownership 和
  `backup_assets:preview`；queued create 返回 public processing-interest
  `job_id`、bounded `poll_after_seconds` 与指向 exact poll route 的 `202 + Location`，
  或返回可复用的完成状态；poll/cancel 绑定请求者与 AssetRef。archive inspect/member
  使用已有 typed audit actions。
- Admin API 只向 Admin 暴露 bounded capability/coverage/updater aggregates 与动态
  pause/quota mutation；所有 route feature-gated、rate/body limited、strict decode，且
  使用 response helpers。internal Worker/updater API 只信任 transport identity。
- Admin updater 操作只列出/扫描固定 inbox 中已验签 candidate，并以 bounded JSON
  `candidate_id + expected_active_fingerprint` 确认激活；candidate ID 是 000067 已有的
  opaque metadata ID，terminal 后不可再次使用，不暴露文件名/路径，也不作为 bundle
  byte transport。
- API/日志/指标/UI 不返回原始路径、文件名以外的隐藏 locator、credential、Worker ID、
  UID/PID、certificate、grant/session/attempt/fence、activation secret、bundle path、
  updater secret、raw diagnostic 或 raw tool output。指标 labels 只使用 closed capability/
  profile/state/error category。
- Preview DTO 固定表达 `native/derived/partial/unsupported/not_deployed/queued/failed`、
  renderer/profile、coverage、freshness、scan/sensitivity、safe reason 和 fallback actions。
  不把 Worker missing 映射为内容不存在，也不把 not_scanned 映射为安全。
- 新 processing API 与 coverage panel 必须独立 lazy chunk；组件不 direct fetch，不看到
  snake_case，不用 `any`/`unknown as T`。中英文、键盘、ARIA、focus、live status、
  reduced motion 和 responsive inspector 均覆盖。main JS/CSS budget 必须继续通过。

### 4.6 Runtime、Compose 与 CI

- Worker 运行时固定 non-root、read-only rootfs、drop all capabilities、
  `no-new-privileges`、reviewed seccomp、tmpfs `noexec,nosuid,nodev`、CPU/memory/PID/
  file-size limits、disabled swap expectation、无 egress/DNS。它只通过共享 UDS 访问 Core。
- Updater 使用独立 UID/socket/volume 权限；只有 updater 可写 bundle store，Worker 只读。
  默认 offline-only；启用 egress 时只给 updater 网络，不给 parser Worker。
- `docker-compose.yml` 只增加显式 profile 和必要 runtime volumes/secrets；core-only 启动、
  `linnea7171/xirang:${IMAGE_TAG:-latest}` selector、10761 端口、healthcheck 与 official
  all-in-one Dockerfile 不变化。
- CI 以 matrix 构建并扫描 linux/amd64、linux/arm64 Worker OCI，执行 core-only 与
  profile smoke/sandbox tests；不得 login/push/tag/publish Worker 镜像。正式公开发布留给
  Child 15。

## 5. Acceptance Criteria

- [ ] 只复用已合并的 paired 000067；repository diff 中没有任何 migration 文件，
      `000068...000071` 仍不存在。
- [ ] global feature、local Worker、remote Worker 和 updater 均保持默认 false；Command
      Provider 仍稳定返回 `task_artifact_contract_missing`。
- [ ] 九个必需 capability 和可选 secret capability 使用 closed advertisement/profile/
      limit，真实工具由无 shell、有界 runner 调用，source bytes 不被修改。
- [ ] materialization 只发生于 job tmpfs，取消/超时/fence loss 会杀死进程树并清理；
      无 tmpfs/安全 mount 时返回现有 `materialization_disabled`。
- [ ] malicious fixtures 覆盖 malformed image/PDF/media、active HTML/SVG、Office macro/
      external link、archive traversal/symlink/device/bomb/encrypted 与 malware-positive；
      positive finding 被验证为成功 scan result。
- [ ] updater signature/digest、固定只读 inbox candidate scan/JSON activation、allowlisted
      egress、content-addressed write、crash-before/after-rename、atomic activation 与
      credential/raw-output redaction tests 通过；不存在浏览器/Core bundle-byte upload。
- [ ] bundle fingerprint 变化只使受影响产物 stale，按 latest/interactive/recent/history
      回填；pause/quota 生效，强 identity 去重，弱 fingerprint 不跨 RP 复用。
- [ ] fault-injection 证明 Derived + Search + classification/coverage 一事务提交；旧 fence、
      双提交、Search failure、Core crash 和 bundle race 都不留下半发布或 ghost posting。
- [ ] Admin/internal/asset APIs 完成 RBAC、ownership、strict DTO、rate/body、sanitization、
      Swagger 和 typed audit 覆盖；任何敏感字段扫描为零。
- [ ] 前端 mapper、state race、queued/partial/failed/not-deployed、malware/secret gate、
      archive member、fallback、a11y、zh/en 和三 viewport 验证通过；processing UI/API 为
      lazy chunk，main JS/CSS 预算不回归。
- [ ] Worker image 以 non-root/read-only/no-network/tmpfs/resource/seccomp 合同运行；
      amd64/arm64 build+scan 通过，CI 和本地命令均证明没有 publish。
- [ ] core-only Compose、Catalog/Search/Content/workspace/native preview/download/recovery 在
      Worker/updater 缺席时继续工作且没有 failed-job/alert 噪声。
- [ ] `make check`、focused Go/race/real PostgreSQL parity、frontend `npm run check`、bundle、
      Docker/system/security scripts、Trellis/exact-manifest/scope scans 都有 fresh evidence。

## 6. Explicit Out Of Scope

- 任何新 migration 或 000067 DDL/CHECK 修改；000068、000069、000070、000071 完全保留。
- Child 12 export，Child 13 recovery，Child 14 lifecycle/retention/hold/purge，Child 15 GA、
  feature 默认启用、legacy removal、release/version/publication。
- 修改 Provider、SSH、Restic、Rclone 或 Command bytes/locator/credential contract；新增
  Command artifact reader；Worker 直接访问 Provider/Repository/DB。
- 通用文件编辑、上传、同步、协作、网盘、远程挂载、执行宏/脚本、解包到宿主目录、
  批量 archive export 或任意插件执行。
- Redis/Kafka、Worker-to-Worker DAG、Worker 任意回调 URL、parser Worker Internet/DNS、
  plaintext persistent staging、普通磁盘 materialization fallback。
- 浏览器/Core bundle upload、multipart/form-data updater route、客户端 URL 导入、API
  传入服务器路径或任意 inbox path；离线 bundle 只能经固定 updater-only inbox。
- 修改 official all-in-one image 或公开端口，发布 Docker Hub/GitHub Release，新增公共
  stable Worker image contract；CI publish/login。
- migration、未通过完整验证前的 stage/commit，以及尚未进入的 archive/journal、
  push/PR/CI/merge/post-merge 交付动作。

## 7. 已冻结取舍、Scope Deviation 与审批状态

### 7.1 已冻结取舍

1. 不新增 artifact role：binary render/transcode 使用 000067 已有 `content` role，
   Search projection 由 media type + canonical projection payload 明确触发；scan/probe/
   archive index 使用 `metadata`，thumbnail 使用 `thumbnail`，OCR 使用 `ocr`。
2. 不新增 persisted failure code：tool/updater 结果映射到 000067 closed processing/updater
   codes；更细的安全原因只作为 bounded closed DTO reason，绝不存 raw diagnostic。
3. 不新增回填表/priority enum：使用现有 descriptor/job/interest/set/updater rows 与
   `interactive/background + effective_priority`；所有策略状态通过 settings registry。
4. Derived preview 复用现有 `/asset-content/:deliveryId` cookie/ticket 安全面，通过一个
   Derived representation resolver 选择并复核 artifact，不新增未持久授权的公开下载面。
5. 前端不继续膨胀 eager `backup-assets-api.ts`；新增独立 lazy processing API module。
   这是对父 implement §12 粗粒度文件建议的收紧，不改变产品/API范围。
6. Offline import 不扩展 central `request()` 的 JSON 合同：bundle 由运维侧放入固定
   updater inbox，浏览器只做 candidate scan/list/activate 的小型 JSON 控制请求。

### 7.2 Scope deviation

- 相比父 Section 12，focused manifest 会补充 materializer/shared runner、backfill、
  atomic Search transaction port、Derived delivery resolver、runtime/API router、server
  updater listener composition、lazy frontend controller/data-page auth handoff、config
  export internal-state filter、script tests 与 generated Swagger 等 current-main 必需路径。
- 相比父 Section 12，`backup-assets-api.ts` 不作为主要 processing DTO 容器；新模块
  `backup-asset-processing-api.ts` 保护极小的 main bundle 余量。
- 恢复父 implement Section 12 明确分配的 `docs/deployment.md`、`docs/env-vars.md`、
  `docs/admin/backup-recovery.md`、`docs/admin/security.md`，并因 future
  `backend/internal/api/router.go` 变更及 current-main 文档 freshness 规则加入
  `backend/README_backend.md`。这是 documentation-truth correction，不扩张产品范围；
  文档只能描述 default-off、可选本地 build/Compose profile、settings/security/rollback
  和无 Worker 降级，不得宣称 GA、稳定公共 Worker image 或 Docker Hub publication。
- 不修改任何 migration/model 文件。父建议若暗含新增 role/error/coverage schema，均以
  000067 现有 closed storage + versioned canonical payload 实现替代。
- 总控批准的 focused manifest amendment 仅加入
  `backend/internal/backupasset/processing/reconciler.go` 与
  `backend/internal/backupasset/processing/reconciler_test.go`，使缺失/不可读 Derived blob
  修复在 caller transaction 内先按 `processing_job` fence 撤销 Search 投影，再更新
  Derived state/reference/blob。该 **atomic Derived/Search reconciliation correction**
  将 exact future manifest 从 161 修正为 163 unique paths，不是产品范围扩张。
- 后续 full gate 暴露 repository package 的显式 Foundation settings fixture 未同步
  Child 11 新增的 12 个 processing/backfill/updater keys。总控批准只加入
  `backend/internal/backupasset/repository/testutil_test.go` 并补齐冻结默认值；该
  **repository foundation settings fixture synchronization** 将 exact future manifest
  从 163 修正为 164 unique paths，不改变生产 snapshot/fail-closed 合同，也不是产品
  范围扩张。
- 总控后续确认保留完整 closed advertisement，并在现有 164 paths 内完成
  **closed-profile advertisement/preflight/executable parity correction**：压缩 TAR 的
  gzip/xz/zstd 单流、尾随、截断、比例与展开上限由同一流式消费链验证；全部广告图片
  MIME 都有真实 libvips/Tesseract 执行路径；Worker 物理支持 optional
  `secret.classify`，而 Core admission/Search publication 仍由 default-off setting
  控制；OOXML/ODF 的 macro/script/external-link package 在 LibreOffice 前直接 fail
  closed。该修正不增加路径、依赖、migration/model、公开 API 或产品范围，exact
  future manifest 继续为 164 unique paths，并保留前两次 manifest amendment 历史。

### 7.3 审批状态与未来变更门禁

Current-main 研究已经给出满足冻结边界的单一推荐方案，没有未决产品问题；规划包、
161→163 atomic reconciliation amendment、163→164 repository fixture amendment 与
164-path 内的 closed-profile parity correction 均已由总控技术批准，用户也已授权
Phase 2，workflow transition 已完成。若未来要改变 capability 列表、工具链、公开
API、updater trust、atomic publication、Worker image/profile 或 164-path exact
manifest，仍须先形成新的 focused amendment 并审阅该 scope change。
