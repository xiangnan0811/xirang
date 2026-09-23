# 备份文件 Catalog

Catalog 是可浏览事实的数据库投影，Provider 观察由有界后台协调器拥有。接口、索引器、worker 与前端工作区均遵守本合同；内容读取另见[内容交付](backup-content-delivery.md)。

## 来源与所有权

- Files 来源依据 durable RecoveryPoint producing-task/node snapshot、严格 lineage JSON、不可变 Task–Repository link snapshot、已审查 import/publication、Repository 可用性/能力和完整 active generation。可变 Task 状态或 JSON 单独不能证明来源；mutable 点的 durable producing Task ID 必须与严格 lineage 一致。
- Admin 可见事实一致的 retained lineage，即使生产 Task 已中断、停用、归档或删除。Operator 还须当前未归档 Task、当前存活 Node 及当前 ownership，并核对历史 link/point attribution；合法 Task move 不重写历史 snapshot，也不把权限留给原节点 owner。缺失/冲突/歧义失败关闭。
- `browse_state` 仅 `browsable|indexing|unavailable`，`retained_version_count` 为非负整数。public point + active complete Catalog 才 browsable；精确 retained provenance 无完整 Catalog 为 indexing；offline/无 sequential 为 unavailable，原因仅允许已知 code 与空 params。只有配置链接、无 retained 点或未审查 import 不出现在 Files selector。
- HTTP source listing 只做有界固定形态 DB 查询，无 Provider/SSH/命令/locator 访问，也不在线修复。cursor 绑定 user/role/ownership/identity/state/count/reason/availability/generation 的可见安全事实，漂移拒绝 replay；DTO/cursor/audit/UI 不含 locator、路径、token 或 proof。

## 协调、公平性与唤醒

- batch 范围 `1..1000`，按持久化 `updated_at,id` 排序，候选选择至多两个 DB 查询且不随 batch 增长；每个 claimed point 至多一次 reconciliation。后端各自的身份/来源观察集固定、有界，不能 N+1、无限列表或重试。
- lease、attempt、backoff 是跨重启/实例共享 DB 事实。低 ID backoff、持续 wake、重启及多实例不能饿死后续 due 候选；周期 timer 仅在周期 pass 后 reset，不被 wake 延期。同一调度边界 timer 到期与 scan 完成时，优先周期 pass。
- `TryWake` 为 capacity-one 合并信号，send 必须 `select/default`；scan 期间 wake 留给后续 pass，Run 前 wake 由 initial pass 吸收。shutdown cancel/join 后拒绝 wake。active-build gauge 使用独立串行发布，所有 build join 后准确为 0。
- connect 是管理员显式动作：`POST /api/v1/backup-repositories/connect` 的 Task 入口仅 `{task_id}`，不接受 locator、credential、replacement flag、display name 或 proof。服务从 Task/Node/credential 推导访问，事务外 probe，再锁定重验证后提交；不启用 versioning、不改 Provider bytes。
- 仅 commit 成功且存在有效 observed、未 retired mutable 点时，锁外请求一次 best-effort wake。nil/typed-nil/重复 wiring 拒绝并保留原 requester；生产只注入已有生命周期 CatalogWorker。失败、rollback、nil/retired 点不 wake；满队列/停止不能逆转 connect 成功。

## 可变源刷新与首次预览

- legacy Rsync 成功写入及 post-hook 后，runner 在报告 success/verification warning 前调用 `ObserveBackupSourceCompletion`。只刷新精确已链接 mutable 来源，即使原位子文件修改未改变根 fingerprint 也失效 Catalog；不等待预览失败或时间阈值。真实观察错误不把成功 copy 改成 failed，但 parent cancel 仍使 TaskRun canceled。
- completion 按现有 Task/link/repository/point 锁序及 point write-admission 重验证，supersede active 和被完成事件淘汰的 building/failed/partial。保留失败证据并补缺失 finished time，不释放别人的 builder lease。completion replacement 立即 eligible；普通失败保留 backoff。关闭、未连接、managed/foreign、断连来源不自动连接。
- mutable Build 在 Catalog lease 和有界 context 下先观察精确 repository/point/producing Task/active binding/current link，再 reload point 冻结当前 fingerprint/capability revision。不能按节点或 selector 顺序选 Task，也不从浏览请求 reconnect。immutable/disconnected 不参与此刷新；build 内不重复 wake。
- 时钟过期不是 source drift：active complete generation 的 fingerprint 与 point 相同，不能仅因 `observed_at` 老于 `2 × ReconcileInterval` 重建、标 stale 或改写 observation 时间。`StalenessStale + CapabilityMutableSourceChanged` 需要 fingerprint 差异或等价 durable source evidence。
- refresh 后只有无匹配 active complete、最新 failed/partial 已到 retry 时间、或 fingerprint 改变才建立新 generation。无需重建时非失败结束，不插 generation/building、不制造失败、不 wake。completion/真实 preview drift 先 supersede，因此仍能强制替换；last-good 不能遮蔽更新的 failed/in-progress。
- safe preview 先 `POST /recovery-points/:id/entries/:entryId/preview-source`，body 仅 `schema_version:1`，要求 list + preview 路由权限及正常 transport/session。返回已有 Catalog status，无 content capability/locator。
- 仅 stat 已授权、已连接、精确 mutable Rsync tuple；旧 metadata/obsolete evidence 用 exact-generation CAS 失效及合并 wake，不 reconnect、不更新 observation 时间、不运行完整 build。immutable 不变。
- 没有 active 且 superseded/building 是 pending，不是文件不存在；只有当前 complete generation 证明缺失才 404。仅 durable lease 真正 live 才 join building；丢失/过期 lease 按 point-before-lease 锁序精确 rearm，不能偷活 lease。旧 pending 观察不能收养并失效并发产生的新 active。
- ready 后客户端仅做一次有界物理 preparation 验证，以同一 token/ref/AbortSignal reload 精确 entry 后发一次 ticket。持续 drift 保持可重试，无 ticket；manual retry 走同路，不 Task-derived connect 或猜 replacement。目录不进入 preparation。
- pending/timeout/失败验证期间保留同一授权 token/ref 文件 inspector 与 Retry，清空旧票据内容；token/selection 变更清旧状态。回归必须从真实物理变化开始，不能预先 Build 后宣称首次预览修复通过。

## 索引 owner 与代次回收

- Catalog/Search 可在生命周期准入和 deadline 验证后，仅回收精确 point/holder/owner 的已过期 active slot：`lease_expires_at<=now` 或 `absolute_deadline<=now`，expire + acquire 同事务。新 identity/attempt/fence 不复用；旧 fence 永远不能续约/写/释放替代者。live slot 拒绝，rollback 不留半回收。
- 保留其他 holder takeover、显式 publication deadline 和全局 absolute sweeper。不能缩短 absolute deadline 掩盖心跳恢复缺失。abandoned-generation 查询锁实际 lease rows，不用 PostgreSQL 禁止的 `COUNT(*) ... FOR UPDATE`。
- 每点非保护 generation 必须有界。保护：active、building、该点有 live `catalog_build|search_index` lease、generation 最大行、最近两次 failed/partial、被 delivery grant/processing job/derived artifact set/blob reference/recovery plan item 的真实 RESTRICT FK 引用。
- 其余 inactive complete/superseded/failed/partial 可回收。查询排除 RESTRICT 引用，旧引用不能阻塞新可回收代次。按 bounded `document_id IN (?)` 删 Search postings，再 Search generations，再分批 Catalog entries，最后 generation；不靠整代 CASCADE 或 OR 链。预算耗尽/取消保留 generation 供续扫，只剩引用时计数并报告，不以 FK 错误终止。
- 回收在 worker 的 feature-disabled 短路前执行，关闭时仍清理/采集指标但不排 build；它不属于 recovery-point retention purge。指标仅 closed state/outcome：代次数、单点最大代次数、entry/posting 行数、SQLite 大小和回收结果，不把 point/path/document/locator 作标签。
- storage gauges 在完成的 scan 上采集，负数归零，generation state 仅闭合枚举。只有 RESTRICT 引用候选的 pass 必须零删除且计数解释跳过原因；全被保护不是扫描错误，也不能阻止其他可回收代次继续。
- 控制面备份脚本清理 owner PID 不存在或超过一天的 `*.tmp.*` 及 SQLite journal/wal/shm sidecar；目标空间不足以容纳源与安全余量时拒绝开始。

## 目录 API 与前端

- 成功页（含 root、empty、cursor）必须有自身 `directory.current/parent/breadcrumb`。root 严格 `null/null/[]`；非 root 来自精确 active point generation 的目录项，1..256 个唯一祖先，末项为 current，前一项给 parent。
- cursor 绑定 user/role/direction/sort/repository/point/generation/requested parent 和完整 canonical directory digest。tamper 400，合法 token scope/context 漂移 409；持久 ancestry/DTO 违约为通用 500，禁止原始 path/locator/cursor。Swagger 必填 directory 与 breadcrumb 的 `recovery_point_id/entry_id/name`。
- 前端原子验证页面：缺成员、空/坏 ref、跨点、环、>256、parent/name/crumb 矛盾、跨目录 item 均拒整页。cursor append 仅完整 context 完全相等；否则清 rows/selection/context 并 failed。额外 raw path/locator/proof/ticket 字段丢弃。
- `/app/backups` 到 `/app/backups/data`；overview/recovery 为显式同级，repositories 是 data 内视图。page 负责组合，共用 browser/source-selector/preview/split-pane 在 `features/backup-assets`。
- list/grid 使用复合 `assetRefKey`。非 browsable lineage 保持可见但 disabled，精确 version browsable 前清 descendants。目录定位用有标签 nav，root crumb 常驻，祖先原生按钮，只有当前 `aria-current=page`。仅一个本地化 Up，root disabled，root child 依据 nullable opaque parent 回 root，不猜路径。
- list/grid 指针/Enter/Space 一致，Up/crumb 在空目录仍可用且最小 44×44。导航同步 abort/detach content，再清 selected/bulk descendants 和 patch route；generation 阻 late response/StrictMode。恢复态仅内存 opaque ref/index/scroll；commit 后恢复有效 origin，否则焦点 current/root crumb 或 results，禁止 late request 偷焦点。
- Task preview 仅 admin 且 canonical Rsync `legacy_mutable/legacy/legacy`；missing 历史 executor 可用 Rsync 默认，未知非空 executor 或 blocked summary 不可。暂停/停用可用，running/retrying/active run 禁用。connect response 只允许一致 Go `Repository/MutablePoint` 或规范 `repository/mutable_point`，重复/混 casing/坏 present point 拒绝；absent/null 可用于 reconcile/disconnect。mutable snapshot 不是完整 Catalog DTO。
- 客户端只 poll 返回的精确 point，ready=complete generation + complete coverage + available content + list permission，不要求 preview 或 entry_count>0。两分钟 wall-clock 上限也必须 abort 永不返回的请求；close/Task/token/unmount abort 静默并清 timer。失败显示闭合本地化指导，不 disconnect，不显示原始异常。

## 回归证据

覆盖所有 retained/ownership/移动/孤儿/导入矩阵与 DB-only 静态边界；cursor 全绑定/tamper；候选查询数、claim 一次、公平性/多实例/backoff/取消 join；真实 Restic tags、Rclone manifest/commit、Rsync marker 的精确证据拒绝；真实 local Rsync runtime→worker→source resolver→Broker 首次 preview，无人工 Connect/Build/retry，含同节点第二 Task 隔离、post-hook warning/cancel、unchanged root child drift。

覆盖 aged matching fingerprint 不 rebuild、不改 observed_at，而 mismatch/superseded/due failed 能恢复；所有回收保护及实际迁移 RESTRICT、posting 归零/分批/cancel 续跑；index heartbeat recovery 的双引擎真实 Catalog/Search rebuild。前端覆盖 root/deep/empty/late/键盘、390px/桌面/200% zoom/reduced motion 的焦点和滚动、axe、连接 empty-ready/hung timeout/tooltip 边界、严格 DTO；运行相关 repeat/race 和完整门禁。

独立断言 mutable refresh 后、activation 前的源漂移返回可重试 `catalog_source_changed` 并保留 backoff；completion 不改根 fingerprint 时仍构建新 active complete；无辜 aged matching Catalog 的 generation 和 observed_at 都不变。paused/disabled Task preview 与 running/retrying 禁用状态在表格/网格一致，zh/en 下首末行 tooltip 在水平 scrollport 内，键盘焦点/hover 均可读。目录 loading/stale/failed/empty 使用本地化 live status，空目录 Up/crumb 不消失，列表与网格的激活、选择和布局控件保持不同原生键盘目标。

源码入口：[Catalog](../../../backend/internal/backupasset/catalog/service.go)、[工作区](../../../web/src/features/backup-assets/backup-assets-workspace.tsx)。
