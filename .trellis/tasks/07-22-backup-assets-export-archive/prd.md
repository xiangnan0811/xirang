# 备份资产导出与归档

## 0. 文档状态与授权边界

- Trellis task：`.trellis/tasks/07-22-backup-assets-export-archive`；approved `task.py start`
  已在 2026-07-22 执行一次，Child status 为 `in_progress`；parent 是
  `07-12-backup-data-explorer-design`，且继续保持 `planning`。
- 专用分支：`codex/backup-assets-export-archive`；规划基线、`HEAD`、`main`、
  `origin/main` 与 merge-base 均为 Child 11 / PR #398 的 squash merge
  `9ad2893c714c82781461f452030c25e0766eedd4`。
- 真实 program 交付进度是 11/15；12 个已实例化 child 不表示父任务完成，父任务不得由
  Child 12 归档。
- 2026-07-22 总控会话中用户回复“批准”，
  将本 PRD、`design.md`、`implement.md`、`research/current-main-evidence.md`、当时的 56 create +
  46 modify exact manifest 与十三项 corrections/deviations 记为 `complete_approved`。后续聚焦
  修订把当前 manifest 调整为 56 create + 67 modify：2026-07-23 增加三个无老化日历 fixture，
  2026-07-24 增加四个 Settings/Config handler 及测试路径，用于落实已经批准的 Export 动态设置
  原子切换合同；2026-07-25 增加 PostgreSQL migration-dirty search-path guard 与其回归测试，
  使隔离 schema 不会互相制造 false dirty/error，同时不放宽真实 dirty 的 fail-closed；同日增加
  SQLite DSN query-replacement guard 与回归，阻止调用方的 `_txlock`/`_busy_timeout` 覆盖 Export
  所需 immediate writer serialization。最终范围审计移除无关的 `settings_handler_test.go`；live
  settings 的专门覆盖仍在 `settings_transition_test.go` 与 `config_handler_test.go`；同一动态设置
  合同还需要既有 `overlay/idempotency.go` 在 service lock 下读取 live TTL/key。2026-07-26
  总控依照用户 standing authorization 将两个既有 `processing/derived_manifest` 路径纳入 MODIFY，
  使 `archive.extract_entry/archive_member_v1` 输出不能在 request ready 前发布 generic
  Search/preview text/OCR projection；这是 Child 12 既有 attachment-only one-hop 合同内的
  security/no-leak containment，不是新产品能力、历史 projection 的广泛清理承诺，也不表示代码或
  测试已经实施。source-reader drain 审查还把既有 `content/attempt_broker.go` 纳入 MODIFY：取消必须
  在另一个 goroutine 阻塞 `Read` 时关闭底层 reader 并确认 drain，才可进入 key/source-lease cleanup；
  这只修正共享内部锁域，不改变 Content public contract、Broker、BudgetService、Auth、ledger/model/migration
  或 Provider。后续 root-cause 审查确认共享的 Provider `boundedReadHandle` 在持有内部 mutex 时
  调用底层 `Read`，而 `Close` 需要先取得该 mutex，故真正阻塞的 Provider read 仍能阻止取消抵达
  底层 reader。最小修订将既有 `provider/restic.go` 和 `provider/runner_test.go` 纳入 MODIFY，
  仅收窄内部锁域并证明 `Close` 可抵达底层 reader；不改变 Provider public contract、bytes、locator/
  credential、Content contract、ledger、migration 或产品范围。2026-07-28 的 focused accessibility
  amendment 只增加既有 tracked `web/src/index.css` 与 `web/src/index-css.test.ts`，以覆盖已批准的
  global reduced-motion/power-save 行为；导出列表 inset focus ring 仍由已列入清单的 export panel
  文件负责。随后把既有 tracked `web/src/lib/api/core.ts` 纳入 MODIFY，使移除 decoded
  `exportJobId` 时逐字节保留无关 query、重复项、bare flag、空分隔符与 hash。这些修订不改变产品
  范围、migration、Provider 行为或十三项 corrections。2026-07-28 的 final full-gate rerun 又在
  既有 `processing/capabilities` streaming runner 中捕获真实低概率竞态：`exec.Cmd.Wait` 可在
  consumer 观察 EOF 前关闭 `StdoutPipe`，产生 `read |0: file already closed`。聚焦修订只把既有
  `runner.go` 与 `runner_test.go` 纳入 MODIFY，以确定性 delayed-consumer RED 和 parent-owned pipe
  GREEN 修复 ownership，同时保留并发 `Wait` 与 process-group cleanup；不增加依赖、API、profile、
  migration、deploy、release 或产品 correction。用户随后
  澄清该同一次批准也已批准 planning workflow transition、`task.py start` 与 exact manifest
  内的 Phase 2 实施；这些授权均为 `complete_approved`。
- 同一澄清冻结 standing interpretation：总控一次提出多个 approval requests 时，若用户没有
  明确排除某项，其批准覆盖全部所列请求。该解释不授权真正的新 scope、不可逆高风险动作，
  或 approved manifest 之外的 deviation。
- 当前 `task.py start`、workflow status transition、产品代码、测试与 paired 000068
  migration 实施已在 exact manifest 内 `executed`。runner delayed-consumer 回归先稳定 RED 为
  `read |0: file already closed`，parent-owned `os.Pipe` GREEN 后 focused、capabilities package、race、
  20 次重复与 `go vet` 均通过。fresh exact union 是
  `8 Phase-1 + 56 create + 67 modify = 131`，即 `68 tracked + 63 untracked`，zero missing/extra/
  overlap/duplicate/staged；corrected short-TMP `env -u NODE_ENV make check` exit 0，frontend
  `168 files / 1388 tests`，bundle JS `498.48/500 KiB`、CSS `104.94/105 KiB`，真实 PostgreSQL 18
  migration/Export/Processing required selectors 均通过。Make binary 已删除并确认不存在。因此
  Step 10 的所有 runnable gates 为 `passed_current_runner_amended`，当前只因下述 unchanged
  dependency audit 保持 `blocked_external`。产品范围与十三项 corrections 未改变。
- Step 11 的 narrow workflow-only permission 已恢复为 `authorized_limited_pending`：只允许
  exact-131 stage/coherent commit、push 与 draft PR 作为 CI-validation channel；dependency-risk
  closure 前不得 ready、merge 或支撑 completion claim。
- 2026-07-28 fresh CI-equivalent dependency audit 新增一个外部 Step 10 blocker。当前 HEAD
  `web/package.json`/`web/package-lock.json` 未修改；Node 20/npm 10 与本机 npm 11 均复现
  `1 moderate + 3 high` audit failure。兼容 lockfile probe 能升级到 `react-router(-dom) 7.18.1`、
  `postcss 8.5.23`、`nanoid 3.3.16`、`brace-expansion 1.1.16/5.0.8`，但随后仍被新公告要求
  尚无兼容发布的 `brace-expansion` 修复与 `react-router 8.3.0`；后者要求 Node `>=22.22`
  和 React `>=19.2.7`，与仓库 Node 20/React 18 合同不兼容。因此不执行 `--force`、不做
  主版本路由/React 迁移，也不把 `web/package-lock.json` 纳入 Child 12；独立的 `core.ts`
  route-cleanup amendment 当时使 scope 为 `8 + 56 + 65 = 129`；独立 runner amendment 现使
  approved target 为 `8 + 56 + 67 = 131`。该 blocker 需要 upstream
  compatible fix、独立的 `brace-expansion` lint-tool migration，或另行批准的 Node `22.22+` /
  React `19.2.7+` / Router 8 migration；在此之前 Step 10 保持 `blocked_external`。

本 PRD 冻结产品需求和验收边界；技术合同、schema/API/安全取舍见 `design.md`，未来
TDD 顺序、exact file manifest、验证与 rollback 见 `implement.md`。这些文档均不
自我授权实施。

## 1. Goal

在已合并的 Catalog/Search/Overlay、Content Broker、Processing/Derived Store、
`archive.inspect`/`archive.extract_entry` 和统一 Runtime 基础上，交付一个持久、可恢复、
默认 Admin-only 的备份资产批量导出闭环：用户把当前选择或保存搜索一次性冻结为精确
资产集合，后台以有界流式方式生成 ZIP/TAR，服务器只持久化独立 Export key 域保护的
ciphertext，并在 fresh download step-up 后通过现有 cookie/Range 内容路由下载。

同时把 Child 11 已有的安全归档索引和单 member 提取能力接入现有备份工作区。归档能力
不得演变为挂载、执行、无界解压或第二套 parser；Export/Catalog/Derived 都不是备份或
恢复事实源，Provider bytes 永远只读。

## 2. 用户价值与成功定义

1. Admin 可以检查显式请求的恢复点/条目根、格式、客户端估算和上限；目录/保存搜索展开后
   的权威 item 数、逻辑字节和 selection digest 只由成功的 create response/status 给出。
   创建后搜索结果、新恢复点或 `mutable_head` 变化不会静默改变集合。
2. 大导出可以跨 Core 重启继续被可靠观察并由新 attempt 安全重试；只有当前
   RecoveryPoint lease/fence 可以 seal/publish，旧 attempt 不能覆盖结果。
3. 用户能区分 complete 与 partial，看到每项 `pending/read/packed/skipped/failed` 和稳定
   失败类别；缺失项绝不被包装成完全成功。
4. ready 产物默认最多存在绝对 24 小时，并受最早返回 lease deadline 与
   RecoveryPoint `RetentionUntil`（若有）限制；取消、失败、
   到期或 key loss 都先撤销访问并完成密码学删除，再幂等清理 ciphertext。
5. ZIP/TAR 内部名称确定、安全且可审计；symlink 不被跟随，特殊文件不会进入归档，路径
   穿越、炸弹、加密/异常 archive 和恶意 Worker 输出均 fail closed。
6. 前端只在现有 `/app/backups/data` 工作区按需加载导出/归档 UI；不新增一级导航，不把
   job/ticket/selection 变成公共分享，不宣称 GA。

## 3. Current-Main 基线

完整证据见 `research/current-main-evidence.md`。以下事实约束本 Child：

1. paired migration 当前止于 `000067_backup_asset_processing`；`000068...000071` 均不
   存在。Child 12 独占 paired `000068_backup_asset_export`；Recovery/Lifecycle/GA 继续
   保留 `000069/000070/000071`。
2. `backup_assets.enabled` 默认 `false`；Command Provider 继续返回 typed
   `task_artifact_contract_missing`。本 Child 不改变任何 Provider 能力或字节。
3. 当前没有 export package/model/handler/API/panel，也没有 Export keyring domain、
   ciphertext root、worker 或 GC。
4. 已有 typed `backup_assets:export`、`LeaseHolderExportJob`、
   `asset.export_create`、`asset.export_download` 和 export/archive audit actions；handler
   不得新增自由字符串或 generic step-up/grant 旁路。
5. Content plane 已提供 composite `AssetRef`、source/entry fingerprint 重验、有界
   sequential/Range、Provider byte 计量、cookie ticket、Range planner、session/revocation、
   TLS 与日志脱敏。`000066` delivery CHECK 仅允许 `resource_kind='backup_asset'`、
   `action in ('preview','download')`，因此不能把 export 行硬塞进旧表。
6. Child 11 已提供 closed `archive.inspect` 与 `archive.extract_entry` profiles、恶意 archive
   限制、Worker grant/Sink/Derived Store。当前 public processing API 只有 archive index；
   member retrieval orchestration 和 UI 不存在。
7. 当前 archive extractor 通过 opaque member ID 找到一个 regular member；
   `DerivedAttemptSourceResolver` 只把 exact text/OCR Derived artifact 作为下一次 Worker
   输入。现行 main 没有安全的递归 member-to-member pipeline。
8. 前端 selection 是内存 `Map<assetRefKey, AssetRef>`；context/result generation 变化会
   清空，但 toggle/clear 不推进 `selectionGeneration`。创建前必须独立 clone 精确 refs，
   不能把该 generation 当作完整冻结证明。
9. 当前 `content.IssueRequest` 只能按一个外层 `AssetRef + renderer/profile` 选择普通资产，
   download 不选择 Derived；它无法唯一绑定同一 archive 的多个 member 结果。当前 Content
   TLS helper 还会接受未核对 proxy peer 的单个 `X-Forwarded-Proto`，不能原样复用于 Export。
10. 当前 Provider-backed Content source 只打开 regular file。Provider `CatalogRecord`、
    Catalog model/DTO 与 Content `SourceStat` 都不携带 link target。目录、symlink、hardlink
    和 special entry 只能经 exact Catalog/source metadata revalidation，不能伪装成可读文件，
    也不能从 path/name/locator/fingerprint 猜 link target。
11. current-main capability diagnostic 可以产生 `ReasonArchiveRatioLimit`，但 Worker 将所有
    `capabilities.ErrInputLimit` 持久化为 generic `ProcessingErrorInputTooLarge`，Processing
    state/model 不保留 diagnostic reason。archive product mapper 必须以真实持久化状态为输入。
    `make backend-build` 还会生成未被 ignore 的 `backend/xirang-server`，所以 Phase 2 交付卫生
    必须显式覆盖该产物。
12. current-main PostgreSQL migration/Processing helpers 只有在对应 `REQUIRE_*_TEST=1` 时才会
    对缺失 DSN fail closed；Export helper 尚不存在。Phase 2 必须新增 required Export helper/env，
    并以清除宿主 `NODE_ENV` 的 full gate 运行前端依赖与审计测试。现行 database connector 已
    同时为 PostgreSQL `timestamp`/`timestamptz` codec 注册 `ScanLocation`，068 只需 required
    parity regression，不新增 connector 路径。
13. `FoundationService.atomicFoundationValues` 要求
    `BackupAssetFoundationSettingKeys` 全量存在，而 Repository 测试的
    `BackupAssetSettingsSnapshot` 只复制 fixture 显式 map。新增 Export/Archive foundation keys
    必须同步 Repository frozen defaults fixture，不能弱化 snapshot-complete 合同。
14. current Overlay `UseSavedSearch` 没有 caller transaction 或 expected-version 参数；只在最后
    一页后重验 frozen items 仍会留下 saved search 在 create commit 前变化的竞态。
15. current `content.BudgetService` 具体读写 000066 `BackupAssetDeliveryUsage/Grant/Request`，不能
    直接用于 000068。current Auth handler 也只接一个 session revoker，main 传入 Content Broker；
    Export delivery 必须在已计划的 content/export mux 内自有预算并组合撤销两个 ledger。

## 4. In-Scope Requirements

### 4.1 精确选择冻结与幂等创建

- Create request 只能选择一种来源：显式 `AssetRef[]`，或当前用户拥有的一个
  `saved_search_id + expected_version`。禁止 path、Provider locator、任意 query string
  或客户端声称的 fingerprint。
- 服务端在创建时把目录后代和保存搜索分页结果一次性解析为有上限、去重、稳定排序的
  精确 `(recovery_point_id, entry_id)` 集合，并冻结 Catalog generation、source/entry
  fingerprint、type、size、RecoveryPoint `RetentionUntil`（若有）与生成归档所需的加密
  路径元数据。
- canonical selection digest 覆盖版本号、排序后的完整冻结记录和数量；显式选择顺序、
  重复项和分页边界不改变 digest。任何 API/job/read 都以 composite identity 查询，禁止
  entry-only lookup。
- 保存搜索只接受 `coverage=complete` 的完整分页；stale cursor、broken/changed saved
  search、结果超过硬上限或任何 scope 未授权都让创建整体失败。持久 job 创建后永远不
  重跑保存搜索，也不自动纳入新 RecoveryPoint。
- 保存搜索解析结果还携带 typed commit binding：saved-search ID、owner、expected version、
  active state、canonical query digest 与本次冻结的 Search generation binding。在同一个 durable
  create transaction 中、任何 `AcquireTx`/000068 insert 之前，Export 通过 Overlay
  service 的窄 Tx validator 锁定并复核 owner/state/version/query binding，并由 resolver 复核
  Search generation。任一变化原子失败且不留下 job/lease/key/reservation；Export 不读取
  Overlay raw model。显式选择没有该 binding，不受 saved-search 更新影响。
- `Idempotency-Key` 必填且只是 bounded opaque replay token，不是授权秘密。服务按现有
  Overlay 模式存 domain-separated SHA-256 digest，并先按 requester + endpoint 查 replay，
  再解析保存搜索；同 key/同 request intent 返回原 job，同 key/不同 intent 返回稳定
  conflict。digest 不依赖 Export KEK，因此轮换/loss/cryptographic deletion 不改变 replay。
  首次并发解析只能有一个 durable winner，loser 回读 winner，不产生两个作业。
- durable create transaction 在返回 `202` 前完成 saved-search commit binding 与精确来源重验，
  再同时写 receipt/job/job-key/items/source refs/quota reservation，并通过既有
  `LeaseService.AcquireTx` 原子取得每个 distinct RecoveryPoint 的 `export_job` lease；每次
  request 的 `AbsoluteDeadline` 必须为 zero，owner 为 export job ID，并把每个返回
  `Lease.AbsoluteDeadline` 原样持久化。任一 selection/key/lease/quota/FK-like identity 失败就
  整体回滚，不允许 queued job 没有 key 或 source protection。
- `mutable_head` 或其他 source fingerprint 在读前/读后漂移时，只把对应 item 标为
  `failed/source_changed`；不替换为新 entry。若没有任何可发布项，job 为 failed，而不是
  ready partial。

### 4.2 Durable job、attempt、lease 与状态

- Job 执行状态闭合为 `queued/running/retry_wait/sealing/ready/expiring/expired/
  failed/cancel_requested/canceled/source_expired`；另有正交 cleanup state
  `none/revoking/purging/purged/purge_failed`。执行结果不得被物理清理失败覆盖，未知执行/
  cleanup 状态都禁止映射为成功。
- 每项状态闭合为 `pending/read/packed/skipped/failed`。ready job 另带
  `result_kind=complete|partial`；只有所有应打包项成功才是 complete。partial 至少包含
  一个 packed item 和成功写入归档的 manifest report。
- 持久化每次 attempt、attempt number、checkpoint、lease owner/expiry、随机 256-bit
  fencing token、绝对 deadline、重试次数和稳定 error category；token 只在内部 DB/CAS
  使用且永不序列化或记录。另存 immutable per-attempt item observation，job-level item
  只是当前投影。绝对 deadline 永不因重启、renew 或 takeover 延长。
- create 以所有返回 lease 的 exact `AbsoluteDeadline`、所有非空 RecoveryPoint
  `RetentionUntil` 与冻结配置分别计算语义边界：job execution deadline 不晚于
  `created_at + frozen max_duration`，ready access/artifact expiry 不晚于
  `ready_at + frozen ready_ttl`，且两者都不晚于任一适用 source cap。任一返回 deadline
  已到或剩余时间不足 frozen lease safety margin/安全执行时，create/attempt fail closed。
  renew/takeover 只接受并保留 Foundation 返回的原 deadline，绝不显式重写或通过 reacquire
  延长。
- 每个来源 RecoveryPoint 从 create commit 起持有一个 `export_job` lease，贯穿 queued、
  running、ready，直到 cancel/fail/expiry 的访问撤销边界。active job 只有当前 attempt +
  source fence 可续租；ready 只有当前 reconciliation lease-owner fence 可续租；release 只在
  fenced 访问撤销事务中发生，迟到 attempt/旧 owner 均无权 renew/release。worker 在每项
  read 前后及 seal/publish transaction 内重验 RP lease/fence、job attempt、source
  fingerprint、cancel/deadline 和 feature gate。
- 只有 `running/retry_wait/sealing` 的 active job-attempt takeover 才递增 job fence、
  supersede 旧 attempt 并删除其 staging；新 attempt 从安全边界
  从 byte zero 重试，并在新 fence 下把所有 job-level item projection、owning attempt 与
  aggregate/checkpoint counter（包括旧 packed/skipped/failed）重置为 pending/zero。只有
  immutable item-attempt history 保留旧观察；旧 checkpoint 仅是诊断/进度证据，不能授权
  继续写，迟到 seal/publish 必须原子拒绝并清理。ready 仅允许接管 source-lease heartbeat
  ownership 后验证/保留或撤销 sealed artifact，绝不新建 attempt、回到 running 或重置投影。
- ready ciphertext 和终态摘要可跨重启恢复。启动 reconciliation 处理 running/sealing、
  orphan staging、missing artifact/key、到期 delivery、quota reservation 和 cleanup failure。

### 4.3 有界 archive writer 与 partial contract

- 默认 ZIP；可选 TAR；compression profile 是 closed server allowlist，客户端不能提交
  codec、level、argv 或可执行路径。归档写入顺序由 canonical selection 决定。
- regular file 读取只经 Child 8 `content.AttemptBroker`/`SourceResolver` seam；目录/link/
  special 只经 exact Catalog/source metadata revalidation port，绝不调用 file reader。逐项
  Provider bytes 先以 Export DEK 的 attempt/item domain-separated key 流式写入有界加密
  staging，close + read-after fingerprint/fence 重验成功后才允许向 ZIP/TAR 写 header/body。
  读中失败或漂移销毁该 spool 并只失败该 item；一旦 archive header 已写，任何 local
  decrypt/write/tamper error 都使整个 attempt abort/retry，绝不发布损坏 partial archive。
- 两段流都保持背压、取消和 fence-loss 传播；分别计量逻辑字节、Provider physical bytes、
  staging/final ciphertext bytes、条目数、reader/worker 并发和 wall time，并通过 durable
  quota bucket/reservation 在双 worker 与 crash 后保守结算。non-store reservation 可在
  密码学撤销后释放，但 store-byte reservation/used accounting 必须保留到对应 ciphertext
  已 unlink、parent fsync 且 locked-root inventory 证明物理占用消失；删除失败不得释放配额。
- archive path 从冻结选择根确定性生成：拒绝 NUL/control/绝对路径/盘符/`..`，执行
  Unicode NFKC collision 检查，处理 Windows reserved/trailing-dot/space 名；跨根冲突用
  稳定 RP/root 短标签，之后使用稳定 numeric suffix。所有变换写入加密 report。
- directory 可产生确定性空目录；regular file 经已验证的 encrypted spool 打包。由于
  current-main metadata contract 没有 link target，所有 symlink/hardlink 都不 dereference、
  不读取 Provider bytes、不生成任何 target-bearing member，并稳定记为
  `skipped/link_metadata_unavailable` 写入加密 report。不得从 path/name/locator/fingerprint
  猜 target。device/FIFO/socket/unknown 等特殊文件继续 skipped，并以稳定类别报告。
- report 至少包含 schema/version、selection digest、result kind、总计、每 item opaque ID、
  safe archive member、状态与 error category；它是已授权下载归档的一部分，不进入日志/
  审计。若 report 无法 seal，整个 artifact 不可发布。

### 4.4 独立 Export 加密、root、TTL 与 GC

- 每个 export 在 create transaction 生成独立随机 256-bit DEK，并原子持久化独立 job-key
  row 的 wrapped DEK/envelope nonce/KEK version；artifact 只引用该 key。metadata、item
  spool 和 final archive 使用 domain-separated subkeys。create commit 后首 attempt 前崩溃
  仍可恢复；不存在只有 ciphertext metadata、没有 durable key 的窗口。
- final archive 使用 streaming/chunked AEAD。每个 attempt 使用
  唯一 nonce prefix + 单调 chunk counter，关联数据绑定 domain/version、export ID、
  attempt fence、chunk index、selection digest、archive profile、plaintext length/final
  marker；最终 authenticated trailer 绑定 chunk count、logical size 和 archive digest，
  防止重排、截断或拼接。
- DEK 只由独立、versioned `export_store` KEK 包装；不得复用 Entry/Search/Derived/Audit/
  Content cache/Recovery key。新 export 使用 active KEK version；轮换保留旧 unwrap 或在
  fenced transaction 中 rewrap，直到引用为零。missing/wrong key 只能导致 unavailable、
  revoke 和清理，永远不能返回 plaintext。
- ciphertext root 默认 `/var/lib/xirang-asset-runtime/export`，与 preview cache、Derived
  root、`/data`、`/backup`、`/logs`、Task roots 和 Repository source/binding 全部无重叠；
  root 与既有 master wrapping secret 是 RequiresRestart 静态边界；versioned Export KEK
  由 typed keyring 管理而不暴露为 setting，quota/TTL/GC cadence 走 `settings.Service`。
  Child 12 只注册/验证/使用 root，不改 Compose durable volume；wiring 属 Child 15。
- ready absolute TTL 默认 24h；artifact/access expiry 取 `ready_at + frozen ready_ttl`、
  每个 create-time 返回 lease 的 exact `AbsoluteDeadline` 与每个非空 RecoveryPoint
  `RetentionUntil` 的最小值。TTL 不因下载、重启或重试滑动。摘要可按既定短期 retention
  保留，但加密 selection/path 元数据与 artifact 同时不可读/清理。
- 取消、失败或不可发布结果立即 fence/停止 attempt、revoke deliveries 并排空 stream，删除
  wrapped DEK 与加密 selection references 后才 release/expire source leases/non-store
  reservations，再幂等删除 ciphertext/staging；到期遵循相同 key-before-lease 顺序。物理删除
  失败使正交 cleanup/artifact state 进入 `purge_failed`，保留 store-byte
  charge 并重试，但原 canceled/failed/source_expired/expired outcome 不变，且密码学撤销已
  完成、不可逆。
- 000068 的 global quota-bucket singleton 同时是 durable use latch：第一次成功提交任一 Export
  或 archive-member 000068 write transaction 必须原子 ensure 该 row；一旦创建永不被 summary
  TTL、GC、purge 或 quota cleanup 删除。这样即使其他 12 表和 ciphertext 已清空，used schema
  仍永久拒绝 destructive down；只有从未写入过该 row 的 pristine schema 可 down。

### 4.5 下载 ticket、cookie、Range 与内容路由

- `POST /asset-exports/:id/download-ticket` 只为 ready、未过期、owner 匹配的完整/部分
  artifact 签发短时 ticket；签发和真正读取都重新验证 feature、RBAC、owner、state、
  key、session 和 revocation。
- 因 `000066` schema 闭合，`000068` 建 temporary-artifact delivery ledger，严格 tagged
  union 支持 `export_archive | archive_member`。export grant 冻结 exact job/artifact/
  attempt/fence/digest/size/chunk-format/key version；member grant 冻结 exact request、外层
  composite fingerprint 与 Processing set/artifact/blob/digest/size。现有
  `/api/v1/asset-content/:deliveryId` 通过 typed resolver/mux 分发普通 asset 或 000068 artifact，
  但复用同一 cookie material、canonical path、GET/HEAD method policy、single Range
  planner、deadline/scheme primitives、session revoke、TLS 和 access-log redaction。
  Export delivery 在 `export/delivery.go` 内对 000068 grant/request/bucket 拥有独立 transaction/
  CAS budgeting，按 parity tests 复现 reserve/finalize/replay/conservative-crash semantics；不得
  调用具体绑定 000066 model 的 Content `BudgetService`。不得放宽旧 content schema、扩展现有
  ambiguous `IssueRequest` 或复制一个公开 bearer URL route。
- 现有 content handler/runtime/main 路径组装一个 typed Content+Export composite revoker；其
  `RevokeSession` 对 000066 Content 和 000068 Export ledger best-effort fan out，即使首个失败也
  必须尝试第二个，并只返回聚合后的安全错误。任一 ledger 在 logout 后都不能继续 serve；
  runtime reconciliation 收敛部分撤销失败。本 Child 不修改 Auth handler 签名或路径。
- cookie 为 HttpOnly/Secure/SameSite 且绑定 exact delivery ID/path/action/session/subject；
  create proof、download proof、cookie ticket 均不可互换。ticket 不进入 URL、响应 JSON、
  local/session storage、history 或日志。
- Range 在 plaintext 坐标规划，只解密覆盖 chunks；拒绝 artifact/key identity replacement、
  multi-range、越界、过期、撤销、
  replay 超预算和 key/tamper 失败。HEAD 不解密/流出正文。HTTP 与受信 proxy/TLS 行为和
  普通 Content route 使用同一修正后的 contract：direct TLS，或 remote peer 命中已验证
  `TRUSTED_PROXIES` 且只有一个 canonical `X-Forwarded-Proto:https`；伪造/多值/逗号/
  `Forwarded` 歧义均 fail closed。默认关闭的 direct loopback HTTP 仅供开发。

### 4.6 受限 archive inspect/member retrieval

- `archive.inspect` 继续只产生安全、有界、加密 Derived index：opaque member ID/parent、
  净化 display name、type/size/warning；不返回原始路径，不挂载、不执行、不展开到普通磁盘。
- 新 archive member API 创建独立 durable retrieval job，绑定外层 composite `AssetRef`、
  Catalog/source/entry fingerprint、`member_chain`、Child 11 capability/profile/pipeline
  fingerprint、security policy 和 source expiry。请求只能提交 inspect 返回的 opaque ID。
- 现行 main 只具有安全的单 hop input contract；本版把 `member_chain` 版本化为数组但
  **长度必须恰为 1**。长度 0 或 >1 返回 stable `archive_nested_unsupported`，不暗示递归
  已交付。未来多 hop 必须先增加 exact Derived-artifact input binding/fencing，再独立审批。
- Worker `archive.extract_entry` 仍是唯一 parser/extractor；Core 只负责 resolve opaque
  member -> frozen ordinal、提交现有 processing job、验证 Derived output 和继承外层
  owner/RBAC/sensitivity/expiry。encrypted/unsupported/bomb/traversal/link/special/malformed
  均 fail closed。
- member create 也要求 `Idempotency-Key`；request row 持久化 domain-separated key digest、
  request-intent digest 与 owner/endpoint unique slot。lookup 先于 index/Processing；same key/
  same intent 返回同一 request，different intent conflict；只有 durable winner 的 reconciler
  才创建 keyed Processing interest。
- member output 是短期 encrypted Derived artifact，不直接加入批量 export，不自动作为
  下一层 archive，也不成为 recovery/backup fact。inspect 与 member emit 已注册且脱敏的
  `archive_inspect/archive_member` audit action。
- `archive.extract_entry/archive_member_v1` 的 Derived manifest 必须是 non-projectable：即使
  member content 是 text/OCR，也只能服务绑定 request 的 attachment delivery，绝不进入 generic
  Search/preview projection。该约束在 request running 的 authority/source 终态变化前阻止错误
  发布；它不声称对历史 projection 做广泛、持久的清理。
- member delivery 不走现有 Content Broker 的外层 AssetRef fallback；000068 gateway 通过
  exact Derived resolver 只接受绑定 request 的 `archive.extract_entry/archive_member_v1`
  artifact。Child 12 冻结为 attachment-only、GET/HEAD、Range none，并要求 fresh 既有
  `asset.download` proof；不声称复用未暴露为端口的 preview renderer。
- 对 `encrypted/unsupported/limit` archive 状态（真实 ratio-bomb fixture 经
  `ErrInputLimit` 持久化为 `ProcessingErrorInputTooLarge`，backend mapper 将任何该 code 整体
  映射为同一个 closed `limit` fallback product），现有权限和
  Content availability 允许时，
  UI 提供“下载原件”动作，直接复用 Child 8 typed Content download
  (`download/attachment/original_v1`) 并每次取得 fresh exact `asset.download` proof/ticket；
  不把 member ticket 或 export proof 当作替代。无 download 权限、offline 或 Content
  unavailable 时只显示稳定 closed reason，不泄露资源是否存在。父合同中的受控恢复
  fallback 在 Child 13 前保持 capability-gated；本 Child 不创建 Recovery plan/job。

### 4.7 API、RBAC、step-up 与审计

- API 固定为：`POST /asset-exports`、`GET /asset-exports/:id`、
  `POST /asset-exports/:id/cancel`、`POST /asset-exports/:id/download-ticket`，以及 focused
  archive member create/status/cancel/delivery-ticket endpoints。所有 create 返回 `202 + Location`；
  mutation strict-decode、body/rate limited、支持 idempotency。
- Export create/status/cancel/download 默认仅 Admin 且要求 `backup_assets:export`；create
  必须带 fresh `asset.export_create` proof，download-ticket 必须带 fresh
  `asset.export_download` proof。两个 proof 两两交叉拒绝，不能用 `asset.download`、
  generic step-up 或 credential grant 替代。
- archive inspect/member 继承 `backup_assets:preview`、精确 ownership 和现有 sensitivity/
  malware gate；不因 export 权限而扩大。Admin/Operator 只按原 ownership matrix，Viewer
  无路由；403/404 采用统一 no-existence-leak contract。
- 复用 typed step-up、permission、audit registries；handler 不拼 action/purpose 字符串，
  export 不读取任何 credential，因此本 Child 不修改 credential-access-grant domain。
- 除共享审计 envelope 固有的 actor/action/time/correlation 与 typed opaque target reference
  外，Child 12 export detail payload 只记录 selection digest、count、logical/provider/artifact
  bytes 与 result/error category；不复制 RP/entry list 或 step-up/grant detail。禁止原始路径、
  文件名、member 名、selection body、ticket/cookie/JWT、Provider locator、wrapped key、
  nonce/fence。保留与 hash-chain/segment cleanup 继续使用已合并资产审计合同。

### 4.8 Frontend closed product

- 只扩展当前备份 workspace：bulk bar 的 Admin-only Export 命令、lazy export dialog/
  panel，以及 archive index 下的 lazy member panel；不新增一级导航或公共分享入口。
- Child 12 UI 只触发显式 bulk selection；saved-search create arm 是已测试的 typed API-only
  能力，本 Child 不修改 saved-search overlay/新增“整个搜索”按钮。点击 Export 时立即
  deep-clone 当前 `AssetRef` 集合并分配 local attempt ID；在 step-up/API await 期间 toggle、
  clear、result replace 或 route 变化不能改变已审阅 payload。双击和 stale response 由
  idempotency/revision/AbortController 拦截。
- create/download 都调用 fresh、`persist:false/reuseCached:false` 的专用 proof。selection、
  reason、proof、cookie/ticket 永不持久化。允许把唯一的 opaque `exportJobId` 写入现有
  data-route query，以便浏览器/Core 重启后 GET-by-ID reconcile；首次打开 push 一条 history，
  dismiss/404/unauthorized/context reset 用 replace 清除，poll 不写 history。reload/direct URL
  没有 trigger 时，关闭后 focus 回到结果标题/workspace root。
- typed API 模块私有 raw snake_case DTO，`request<unknown>()` 后原子映射为 closed
  camelCase product。未知 execution/cleanup/artifact/item/error/result、非法时间/ID/bytes、
  矛盾 ready/expiry 或 query-bearing/non-same-origin download URL 让整个 product fail closed。
- create 前 panel 只显示 explicit roots/count 与明确标为 non-authoritative 的 client estimate；
  create response/status 才显示服务器冻结后的 count/bytes/digest。status items 使用 opaque
  cursor 分页（default 100、hard max 200）和有界 DOM，不一次返回/渲染最多 10,000 项。
- panel 显示状态、逐项失败、complete/partial、server poll cadence、离线/hidden resume、
  取消、自动 retry 状态（无手动 retry endpoint）、绝对 TTL 视觉秒级倒计时和 fresh
  download。`aria-live` 只在 state transition 与 1h/10m/1m/expired 阈值播报，不每秒发声。
  浏览器 reload 后只恢复 job，不恢复原 selection/reason/ticket。
- archive panel 对 encrypted/unsupported/limit（包括由 generic
  `ProcessingErrorInputTooLarge` 投影的 ratio bomb）统一映射 closed state；仅当 server-evaluated
  download permission、online 与 Content availability 同时成立时显示复用现有
  `onPrepareDownload` 的原件下载命令，否则显示不泄露存在性的稳定 reason。受控恢复入口
  在 Child 13 capability 交付前不渲染为可用动作。
- 覆盖 zh/en、键盘、focus return、dialog name、ARIA live/progress、reduced motion、axe 与
  1440/1200/390 viewports；Export 与 archive code 必须保持 lazy，并通过 main JS/CSS budget。

### 4.9 配置、指标与降级

- `settings.Service` 按 `design.md` §11.1 的 exact key/env/type/default/min/max 表注册 export
  enabled/root/profile/chunk、selection/source points、logical/provider/cipher bytes、user/global
  jobs/readers、duration/attempt/lease/retry、ready/ticket/summary TTL、store quota/GC，以及只可
  收紧 Child 11 hard caps 的 archive member limits。`backup_assets.enabled=false` 时全部 export/member
  route fail closed，GC/revoke reconciliation 仍可在安全模式完成。
- create 把 resolved profile/chunk 与所有 per-job item/source/byte/reader/duration/attempt/retry/
  lease/ready-TTL safety limits 以 versioned typed columns 冻结到 job；重启/接管使用该快照，
  后续 settings 提升不能扩张旧 job、降低不能抹掉已保守预留的 store bytes。global enable/
  concurrency/quota 仍控制即时 admission，ticket limits 在每次 grant 签发时另行冻结，archive
  member limits 绑定现有 versioned Processing descriptor/request。
- `BackupAssetFoundationSettingKeys` 与 atomic snapshot 必须保持全量 fail-closed；Repository
  `testutil_test.go` 只补齐本 Child 新增 keys 的 frozen defaults，并由既有 fixture-completeness
  test 与 focused Repository test 证明，禁止通过 fallback 或放宽校验掩盖缺 key。
- 指标只使用 closed state/format/result/error labels，覆盖 queue/duration/logical/provider/
  artifact bytes、lease loss/takeover、quota saturation、ticket reject、decrypt/tamper/key loss、
  GC/purge failure；绝不以 path、entry/member/export ID 作 label。
- 无 Worker 时批量 export 仍可使用 Core 的 Content source/archive writer；archive member
  返回 typed `not_deployed`，不影响 Catalog/Search/native preview/download。Export root/
  key 不可用时 export route unavailable，但已合并读平面继续可用。

## 5. Acceptance Criteria

- [ ] 保存搜索/显式/目录选择只解析一次，冻结 exact composite refs + fingerprints +
      canonical digest；saved search 最后一页后更新时，同一 create transaction 的 typed Overlay
      owner/state/version/query + Search-generation revalidation 原子拒绝且无 job/lease/key/reservation，
      显式选择不受影响；新 RP 不加入，mutable drift 逐项 fail closed，entry-only lookup 扫描为零。
- [ ] idempotency replay 在 saved-search resolution 前命中；并发 duplicate 只有一个 durable
      job；same-key/different-intent、Export key rotation/loss independence、member request
      replay 拒绝/命中正确。
- [ ] 所有 job execution/cleanup、item/attempt/artifact/delivery 状态和 transition 有 closed
      contract、DB CHECK 和 model parity；whole-attempt retry 重置当前投影但保留 immutable
      history，partial report 不伪装 complete。
- [ ] Export 对每个 source 调用 zero-`AbsoluteDeadline` `AcquireTx`；同一 RP 已有不同 holder
      或 released 历史 lease 时仍成功，持久化每个返回 deadline，并正确 cap job/artifact；
      acquire/renew/release/takeover、job/attempt fence 与迟到 seal/publish 在 SQLite/PostgreSQL
      和 race/fault tests 中成立；queued/ready 均持有 lease，`lease.go`/tests 不改。
- [ ] ZIP/TAR path、collision、special file、logical/provider/artifact byte、背压、encrypted
      per-item spool、header 后 attempt abort、cancel 和 limit fixtures 通过；所有 Provider 的
      symlink/hardlink 都为 `skipped/link_metadata_unavailable`，且无 target 猜测、byte read 或
      Provider mutation。
- [ ] Export DEK/KEK 分离、nonce/AAD/trailer、tamper/reorder/truncate、rotation/rewrap/loss、
      cryptographic deletion 和 restart reconciliation tests 通过。
- [ ] ready TTL 是 create-time frozen 的绝对 24h 默认且不晚于 earliest source expiry；
      fence/stop -> revoke/drain -> key destroy -> source lease/non-store release -> ciphertext
      unlink/fsync/inventory -> store release 顺序、每个 crash boundary 的原子/幂等恢复、
      pre/post-publication cleanup failure 和 orphan/purge_failed 重试可证；禁止 source lease 已
      释放而 artifact key 仍可读的窗口。所有 TTL/lease/expiry test 使用 injected/frozen clock
      或相对 test-start instant 的 future/past，禁止会随日历时间失效的固定日期 fixture。
- [ ] ticket/cookie/content mux 对 action/path/session/subject/Range/replay/revocation/TLS/log
      redaction、exact export/member artifact binding 与 trusted-proxy peer verification 成立，
      且不修改 000066 closed schema；000068 delivery 自有 transaction/CAS budget parity，logout
      对 000066+000068 best-effort 双 fan-out，one/both failure 与 restart reconciliation 后两个
      ledger 都不能继续 serve。
- [ ] archive inspect/member 只复用 Child 11 Worker；one-hop chain 精确绑定 outer fingerprint；
      nested/encrypted/unsupported/bomb/traversal/link/special fixtures fail closed；真实 ratio-bomb
      capability fixture 证明 `ErrInputLimit -> persisted ProcessingErrorInputTooLarge`，backend mapper
      把任何 `ProcessingErrorInputTooLarge` 映射为 closed `limit` product，frontend
      regression fixture 证明该 ratio-bomb 状态与 encrypted/unsupported/其他 limit 一样在允许时
      提供 Child 8 原件下载 + fresh `asset.download`，否则显示同一 no-leak closed reason；受控
      恢复保持 Child 13 capability-gated。
- [ ] Admin/Operator/Viewer/no-existence-leak matrix、`backup_assets:export`、create/download fresh
      purpose 两两交叉拒绝和 typed audit/redaction/retention 均通过。
- [ ] frontend mapper、selection race、idempotent double click、poll/restart、partial/item errors、
      paged items、route push/replace/focus、quiet TTL/fresh download、archive member、a11y、
      zh/en、responsive、lazy bundle gate 通过；saved-search UI 明确不在本 Child。
- [ ] paired SQLite/PostgreSQL `000068_backup_asset_export` pristine apply/down、used-data blocked
      down、purge-to-empty 后 global quota-bucket use latch 仍永久阻止 down、forward-fix policy 和
      behavior parity 通过；required migration/export commands 分别
      设置 `REQUIRE_POSTGRES_MIGRATION_TEST=1`、新增 `REQUIRE_POSTGRES_EXPORT_TEST=1`，复用
      Processing PostgreSQL test 时同时设置 `REQUIRE_POSTGRES_PROCESSING_TEST=1`；缺失 DSN
      必须 fail 而非 skip，并回归证明现行 connector 对 `timestamp` 与 `timestamptz` 的
      `ScanLocation` 均生效；`000069...000071` 不存在且未改动。
- [ ] `make backend-build` 和任何后续 full build 产生的 `backend/xirang-server` 均被精确删除并
      断言 absent；full gate 固定为 `env -u NODE_ENV make check`；最终 exact manifest 比较联合
      tracked diff、staged diff 与
      `git ls-files --others --exclude-standard`，生成 binary 不进入 manifest/staging。
- [ ] `backup_assets.enabled` 仍默认 false，Command Provider 仍 typed unsupported；core-only、
      atomic foundation snapshot 与 Repository frozen-default fixture 对新增 keys 保持完整；
      all-in-one/public image/root README/release/publish contract 无变化。

## 6. Explicit Out Of Scope

- Child 13 recovery plan/job/result，Child 14 retention/hold/purge/lifecycle，Child 15 GA、
  Compose durable volume、docs/release/publication/default enable/legacy removal。
- `000069/000070/000071`、非配对 migration、修改 000062-000067、修改 Provider bytes、
  Command artifact reader、Provider locator/credential exposure。
- 公共分享链接、长期 ticket、URL bearer、用户提供 archive password、server-side user
  password storage、客户端加密归档密码。
- 递归 archive mount/browse、member chain >1、全 archive 解包到磁盘、执行 member、active
  HTML/SVG/macro/script、任意 codec/profile/argv/plugin。
- 把 Export/Catalog/Derived/report 作为备份成功、恢复验证或 Provider truth；自动把 archive
  member 加入 export/recovery。
- 超出 exact manifest 编辑产品/migration/test path，或在真实验证完成前
  stage/commit/archive/journal/push/PR/CI/merge/release。

## 7. Focused Scope Corrections 与审批状态

1. 父 `implement.md` Child 12 Step 11 的 `BackupAssetMigration067` 是历史 typo；真实 owner
   是 paired `BackupAssetMigration068`。focused plan 只使用 068 selector。
2. 父粗略 file list 要求改 credential-access-grant handler，但 export 不读取 credential，
   exact export proof 已在 typed step-up registry；本计划删除该修改，避免制造无意义 grant。
3. `000066` schema/Content IssueRequest 不能表达 export 或 exact archive-member delivery；
   计划增加 000068 temporary-artifact ledger + typed content mux，并冻结 exact artifact identity，
   而不是弱化旧 CHECK 或让 member attachment 回落到外层原文件。
4. 父设计写 `member chain`，但 merged Child 11/current-main 只有安全 single-hop input；本版
   冻结 versioned one-hop chain，>1 fail closed。这是诚实的 focused limitation，不是对父
   多 hop 的虚假完成声明。
5. 为满足浏览器重启恢复而不持久 selection/ticket，允许 route query 只携带一个 opaque
   export job ID；没有 list-all/export-history 或新导航。
6. 为满足父合同和 queued cleanup race，source lease 从 create commit 持有到访问终态，
   不在 ready 提前释放；父 implement Child 12 Step 9 的旧 release-before-key-delete 简写在本
   focused plan 中 refinement 为 fence/revoke/drain -> wrapped DEK/selection-reference destruction
   -> source lease/non-store release -> physical cleanup/store release，禁止 lease 先释放而 key
   仍可读；Export frozen rows 不通过对 RP/Catalog 的 FK 被动形成 retention hold。
7. 直接 archive streaming 无法在 post-read drift 后删除已写 member；本计划增加 encrypted
   per-item spool，并规定 archive header 后错误只能 abort 整个 attempt。
8. saved-search export 保留为 API-only；当前 UI 只做显式 bulk。create 前 estimate 不声称是
   目录/搜索展开后的权威 count/bytes，权威值来自 202/status。
9. 为安全复用 content route，本 Child 修正普通 Content 与 000068 branch 共用的 trusted-
   proxy scheme 判定；只接受 direct TLS 或来自 `TRUSTED_PROXIES` peer 的 canonical HTTPS。
10. 000068 增加 job-key、item-attempt history、quota bucket/reservation，并让 member request
    自身承担 durable HTTP idempotency；global quota-bucket singleton 还是第一次成功 000068
    write 后永不删除的 used-down latch。这是 restart/partial/quota/race/rollback 合同所需 schema，
    不增加第十四表或新业务范围。
11. current-main 显式 lease deadline 会复用同 RP 最近历史 deadline 且不隔离 holder/owner；
    Export 必须用 zero `AbsoluteDeadline` 取得 holder-isolated lease，持久化返回 deadline 并
    作为 execution/access/artifact cap。`lease.go`/tests 保持不变。
12. current-main Provider/Catalog/Content 合同没有 link target；本 Child 对全部 symlink/
    hardlink 稳定 `skipped/link_metadata_unavailable`，不读取 bytes 或猜 target。这是已纳入
    2026-07-22 planning approval 的 focused limitation/parent refinement，不新增 metadata
    seam/schema/path。
13. 父 design §13 的 archive fallback 在本 Child 只交付既有权限允许的 Child 8 原件下载；
    ratio bomb 不依赖未持久化的 diagnostic reason，而由真实
    `ErrInputLimit -> ProcessingErrorInputTooLarge` 链路进入 generic closed limit product。
    unsupported/limit/encrypted、无权限/offline/unavailable 都有 no-leak closed UI。受控恢复仍由
    Child 13 capability-gated，不在本 Child 实现。

以上十三项已由 2026-07-22 总控用户回复“批准”随完整 planning package 一并
`complete_approved`；同次批准经用户澄清也授权 workflow transition、`task.py start` 与
exact manifest 内 Phase 2 实施。fresh preflight 已通过且 start 已执行，后续严格按 TDD 推进。

闭环复核进一步澄清而不扩张产品范围：fresh attempt 必须重置全部 current item projection；
执行 outcome 与 cleanup state 分离；store-byte quota 保留到物理删除成功；job 冻结 typed
limit snapshot；archive-member request 只持 digest/ordinal、不持无 owner 的 ciphertext；
filesystem pristine 由 `ExportRuntime.PrepareSchemaDown` 锁 root 证明，SQL down 只判 DB/
lease/key pristine。这些合同也属于本次规划审批面。

本轮 current-main 审阅还把以下实现边界纳入同一个已批准 planning surface，而不新增 product
correction：saved-search commit 的 typed Overlay Tx seam；Repository foundation fixture 完整性；
global quota-bucket 永久 use latch；000068 自有 delivery budgeting；以及 Content+Export logout
双 ledger composite revoke。2026-07-23 focused amendments 又纳入两个既有 Rsync repository
test fixtures 和一个 Rsync Provider preflight test fixture，仅把会老化的固定日历时刻改为
test-start-relative time。2026-07-24 runtime publication 审查进一步证明，除 root 外动态可重载的
Export 设置若不经过 Settings/Config mutation boundary，就会继续使用启动快照，且
`backup_assets.export.enabled` 不能原子停止或重新发布 admission。总控依照用户的 standing
direction 纳入 `settings_handler.go`、`settings_transition_test.go` 与 `config_handler.go`/test 四个既有路径，只复用现有
设置/导入 endpoint，不新增 API。2026-07-25 的 guard amendment 只让已有 PostgreSQL
dirty-state 检查与实际未限定读使用同一 search path，并在 sibling-schema 与当前 schema
dirty 回归中保持 fail-closed；同日 SQLite DSN amendment 将调用方 query 中的受保护写入锁参数
替换为强制值。最终范围审计移除无关的 `settings_handler_test.go`，随后 2026-07-26
总控 technical direction 增加 archive-member output non-projectable containment 与其 focused test，
因此 exact future manifest 为 56 create + 67 modify；其中 `overlay/idempotency.go` 仅让同一
live settings transaction 的 idempotency TTL/key read 受 service lock 保护，不新增 endpoint 或
产品能力。最后的 shared Provider bounded-reader
cancel amendment 仅增加 `provider/restic.go` 和 `provider/runner_test.go`，不增加 000068 SQL、
create/product API、Provider public contract 或十三项 product corrections。
最终 runner stdout ownership amendment 仅增加既有
`processing/capabilities/runner.go` 与 `runner_test.go`，修正 `StdoutPipe`/`Wait` 生命周期竞态，
不改变 capability profile、Worker protocol、依赖、API、migration 或 correction count。

## 8. Phase 1 Status Ledger

| Gate/action | Status |
|---|---|
| fresh fetch / baseline / branch / child create | executed；证据见 research |
| parent/current-main/Children 7-11 research | executed |
| focused PRD review | `complete_approved`；2026-07-22 总控用户回复“批准” |
| focused design/security/data review | `complete_approved`；2026-07-22 总控用户回复“批准” |
| focused implementation plan / 56 create + 67 modify manifest / matrix review | `complete_approved`；既有 amendments 加 2026-07-28 runner stdout pipe ownership amendment；后者只增加 existing `processing/capabilities/runner.go` 与 `runner_test.go` |
| thirteen scope corrections/deviations approval | `complete_approved`；2026-07-22 总控用户回复“批准” |
| planning workflow transition approval | `complete_approved`；同次批准及 2026-07-22 用户澄清 |
| `task.py start` authorization | `complete_approved`；同次批准及 2026-07-22 用户澄清 |
| Phase 2 implementation authorization within exact manifest | `complete_approved`；同次批准及 2026-07-22 用户澄清 |
| `task.py start` | `executed`；2026-07-22，仅一次 |
| workflow status transition to in_progress | `executed`；Child `in_progress`，parent `planning` |
| red tests / product / migration implementation | `executed`；runner stdout ownership deterministic `file already closed` RED 与 parent-owned-pipe GREEN 已记录 |
| PostgreSQL lock-order/context/idempotency tranche | `closed`；controller fresh required `TestExportBehaviorPostgres` PASS `11.527s`；canonical `Q(global,user) -> J -> A -> I -> IA` order and required real-PostgreSQL selector evidence recorded |
| focused crypto/AAD review | `closed`；spec `✅`，quality `APPROVED`，zero findings |
| archive-profile sub-boundary | `closed`；independent current-code review 确认 allocation 前的完整 `AssetRef` + nonnegative root ordinal 校验，以及 scope prefixing/每次 retry 后的 collision allocator/limits final-member validation；focused selectors PASS；剩余 `P3` instrumentation coverage limitation 仅为验证覆盖限制，非产品失败 |
| Step 10 exact manifest/static/review | `passed_current_runner_amended`；exact `131 = 68 tracked + 63 untracked`，zero missing/extra/overlap/duplicate/staged；十三项 corrections 不变 |
| Step 10 dependency audit | `blocked_external`；unchanged package files 为 `1 moderate + 3 high`；无完整兼容 Node 20/React 18 修复，且未使用 `--force`/unsafe override |
| runner/package/race/full/PostgreSQL gates | `passed_current_runner_amended`；capabilities normal/race/20x、vet、full `make check`、bundle budget 与 real PostgreSQL required selectors fresh pass |
| backend/full `env -u NODE_ENV make check` gates | `passed_current_runner_amended`；corrected `/tmp/xc12` exit 0；generated binary removed/absent |
| Step 11 / delivery | `authorized_limited_pending`；exact-131 stage/commit/push + draft PR/CI only；dependency closure 前仍禁止 ready/merge/completion claim |
| exact staging / work commit | `not_executed`；现获 narrow permission，当前 `staged=0` |
| `trellis-finish-work` / Child archive / journal | `not_executed`；只可在 dependency closure、ready/merge/post-merge monitoring 之后执行；parent 保持 `planning` |
| archive/journal commit | `not_executed` |
| push / PR / required CI | `not_executed`；push + draft PR 已 narrow-authorized，PR 必须保持 draft 直至 risk closure |
| squash merge / post-merge automation | `not_executed`；dependency-risk closure 前禁止 ready/merge |
| local main sync / branch-worktree cleanup | `not_executed` |
| Release Please PR #386 / release / deploy | `not_applicable`；out of scope |
