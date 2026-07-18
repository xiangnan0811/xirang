# 备份资产 Catalog

## 1. 文档状态

- Child：`backup-assets-catalog`（父任务第 6/15 个 Child；总进度基线为 5/15）。
- 父任务：`.trellis/tasks/07-12-backup-data-explorer-design`，继续作为 `planning` program tracker，不是本 Child 的实现目标。
- 当前阶段：Phase 2 implementation 与本地验证已完成；task 仍为 `in_progress`，正在进入精确 staging、提交、归档/journal、PR/CI/merge/post-merge 交付流程。
- 分支：`codex/backup-assets-catalog`；基线 `2edd795581f9368dbaacb27ad2d9f389848060fe`。
- 功能门禁：`backup_assets.enabled` 在 Child 1–14 继续默认 `false`；Child 6 不提前公开完整 UI。
- 决策记录：用户于 2026-07-17 先选择 schema 方案 A，明确接受“无 Child 6 migration、单 runtime 内 per-repository lane + durable per-point fence”，以及它不提供跨进程同 Repository 严格串行化的父合同偏差；随后明确同意三份规划文档、范围/验证合同、Provider seam 和 implementation/start。审批门禁已经满足，Phase 2 已合法启动。

## 2. Goal

在不修改 Provider 字节的前提下，为 Restic、Rsync、Rclone 的精确 `RecoveryPoint` 构建可重建、原子发布、可授权过滤的 Catalog。Catalog 提供仓库、恢复点、目录项、可信证据和精确两点 diff 的稳定 API 边界；它是浏览索引，不是内容恢复源，Provider 离线时仍允许读取已经提交的 immutable Catalog，但必须把内容可用性独立报告为 `false`。

## 3. 用户价值与成功定义

- Admin 能按仓库和恢复点浏览已提交资产，并区分 Catalog 完整度、陈旧度、Provider 内容可用性和恢复可信证据。
- Operator 只能看到其拥有的 producing lineage；共享仓库中的其他 lineage 在名称、计数、证据和分页之前就被过滤。
- Viewer 在任何 Catalog 数据访问前由 RBAC 返回 403。
- 对 immutable point，Catalog 构建失败、进程重启、Provider 暂时离线或 lease takeover 都不能破坏上一个 active generation。
- 对 mutable head，只有前后 source fingerprint 一致且 activation transaction 再验证通过的 generation 才能发布；retired head 不保留 active Catalog projection。
- 后续 Child 7–15 能只依赖 opaque `{ recoveryPointId, entryId }`、coverage/staleness/availability 和 evidence/diff 合同，不依赖 Provider 路径或命令细节。

## 4. In Scope

### 4.1 Catalog generation

- `building | complete | partial | failed | superseded` generation 状态机。
- generation state、对外 coverage、staleness、content availability 四个正交维度。
- deterministic composite identity：每个资产引用必须同时携带 `recoveryPointId + entryId`；`entryId` 由 installation-stable Entry Identity key 与 canonical entry identity 派生。
- 分批写入 staging generation、零条目完整 generation、完整性 proof、单事务原子激活、旧 generation supersede、失败 generation 对账。
- immutable point 的 active manifest identity/count/digest/completeness 与 Provider-specific Catalog proof 对齐；mutable head 的 source fingerprint race 防护。
- retired mutable head 删除 active projection，但不删除 Provider 字节；失败与回滚只清理可重建 Catalog 数据。

### 4.2 Query and API boundary

- repository list/detail 的 producing-lineage ownership 过滤收敛。
- recovery-point list/detail、entry list/detail、recovery-point evidence、exact two-point diff API。
- opaque、签名、过期、用户/角色/查询/generation/sort scope 绑定的 cursor。
- 稳定排序、safe enum projection、稳定错误码、capability reason、server-evaluated permissions。
- Swagger 注释、tracked `docs.go` 和 backend route documentation。

### 4.3 Evidence and diff

- 只聚合精确 RecoveryPoint 的 TaskRun、active manifest、publication verification/commit evidence、`RestoreDrillEvidence`；不同来源分层展示，不提升或合并信任结论。
- diff 只接受两个明确 RecoveryPoint scope 和可选的两侧 opaque subtree entry IDs；不接受 Provider/native path。
- Catalog metadata diff 与 Provider-native evidence diff 分层，稳定分页。

### 4.4 Worker and runtime

- startup + periodic indexing，per-repository scheduling，bounded global concurrency，指数 backoff + jitter，cancel/shutdown。
- `catalog_build` RecoveryPoint lease、心跳、takeover、fencing；稳定 point-scoped owner ID 防止 generation-ID 绕过唯一槽。
- Runtime 是唯一 composition root；handler 不创建第二套 service/provider graph。
- Provider offline 是 retryable availability，不是 Catalog corruption。

### 4.5 Frontend boundary only

- `backup-repositories-api`、`recovery-points-api`、`backup-assets-api` raw DTO、mapper、domain type 与 API factory wiring。
- raw snake_case、日期/nullable/closed enums/capability fallback 只存在于 mapper 边界。
- mapper 与 API request-shape tests；不新增页面、路由、组件、i18n 文案或完整 workspace UI。

## 5. Out of Scope

- Child 7 的 search、saved searches、favorites、tags、recent access。
- Child 8 的内容 ticket、Range delivery、preview/download。
- Child 9 的 `/app/backups` workspace UI。
- Child 10–15 的 Worker 派生内容、export、recovery、retention/purge、GA enablement。
- 任何 Provider bytes 的修改、移动、重命名、版本化、删除或用 Catalog 作为恢复源。
- handler 直接执行 Restic/Rsync/Rclone/SSH/系统命令或接收 Provider locator/path。
- Command Provider：无显式 artifact contract 时继续返回 typed unsupported。
- 预占或实现 `000065…000071` 中任何 migration。

## 6. Functional Requirements

### FR-1 Atomic generation

1. 构建先创建不可见的 `building` generation；Catalog entry 只写该 generation。
2. 只有 proof、count、digest、source revision、active manifest、point state 和 fence 全部通过，才能在一个 DB transaction 中激活新 generation 并 supersede 旧 active generation。
3. 任一失败必须保留旧 active generation；新 generation 标为 `partial` 或 `failed`，不得通过孤立 entry 推断 complete。
4. `expected_entry_count=0 && written_entry_count=0` 在 proof 完整时是合法 complete。
5. 重启后超时 `building` generation 必须被可重复地 reconcile 为 failed/清理候选，绝不自动激活。

### FR-2 Immutable and mutable semantics

1. immutable `committed | degraded` point 可索引；Catalog 对 point 的已提交事实只读。
2. immutable generation 绑定 exact point、active manifest ID/revision/digest/count/completeness 和 Provider source revision。
3. mutable `observed` head 在枚举前、枚举后和 activation transaction 内三次验证 source fingerprint；任何变化均拒绝发布。
4. mutable head 仍使用同一 RecoveryPoint ID，但 refresh 生成新 Catalog generation。
5. `retired` mutable head 无 active generation、无 entry projection、不可读；只保留父合同允许的 opaque tombstone/audit/rollback locator。

### FR-3 Identity and navigation

1. API 不接受路径作为资产身份，所有 entry response 都包含 `{ recovery_point_id, entry_id }`。
2. deterministic `entry_id` 绑定 recovery point 与 canonical normalized Provider-relative path keyed digest；entry type 只是 metadata/proof，不参与 ID，因此同一 mutable point 的同路径类型变化保持 ID、由 diff 报 `type_changed`，不同 recovery point 的同路径仍不会混淆。
3. parent navigation 使用 `parent_entry_id`；root 使用空 parent，而不是特殊路径字符串。
4. entry ID 单独出现不能 resolve；repository/recovery-point/entry scope 不匹配返回不泄漏存在性的 safe not-found。

### FR-4 Authorization and ownership

1. 所有公开 route 使用 AuthMiddleware + `backup_assets:list` RBAC；Viewer 在进入 handler/service 数据查询前返回 403。
2. Admin 可见所有合格 point，包括 imported baseline 和无法归属的历史 point。
3. Operator 只可见仍可验证的 current producing Task → Task.NodeID → `node_owners` 授权链；RecoveryPoint 的 task/node snapshot 只用于授权后的展示，不能代替当前 ownership 控制面事实，也不能因共享 Repository 的另一 active Task 而获得旁路权限。
4. `producing_task_id IS NULL`、Task 已删除/归档、无法验证的 lineage、`imported_baseline` 均为 Admin-only；TaskRepositoryLink 是否仍 active 不能扩大 point visibility。
5. 过滤必须先于 repository/point/entry 名称、lineage、count、evidence、coverage、cursor 和 `has_more` 计算。

### FR-5 Browse availability

1. committed immutable active Catalog 在 Repository offline/disconnected 时仍可列出。
2. `content_availability.available=false` 与 stable reason 明确表示 Provider 不能提供内容；不得把 offline 映射为 Catalog unavailable 或 backup corruption。
3. 若没有 active Catalog，coverage 可为 `building | partial | failed | unavailable`；只有 `complete` 可支持“零结果”结论。
4. generation state 不作为 coverage 输出；staleness 不从 availability 推断。

### FR-6 Evidence

1. evidence DTO 固定区分 lineage、publication manifest、verification、restore drill 四层来源与各自状态/时间/opaque IDs。
2. raw command、stderr、Provider locator/config、credential、path/name/content 不进入证据 DTO、审计或错误。
3. 缺少某类 evidence 用 typed `unavailable/not_recorded`，不把其他层级成功升级成该层成功。

### FR-7 Exact two-point diff

1. 请求明确提供同一授权 repository 内的 `base_recovery_point_id` 与 `compare_recovery_point_id`；两者相同、越权或无 active Catalog 均安全失败。
2. 可选 `base_parent_entry_id` 与 `compare_parent_entry_id` 各自绑定对应 point；不接受 path。
3. 输出 `added | removed | modified | type_changed` 的 Catalog metadata layer；Child 6 固定不返回 unchanged 项，并把 Provider-native evidence 支持度单独投影。
4. cursor 绑定两点 active generation、两侧 subtree、sort、role/user 和方向；任一 generation 改变返回 stale cursor。

### FR-8 Provider contracts

1. Restic、Rsync、Rclone 都必须实现 Repository-owned exact Catalog read session；Catalog package 不持有 credentials/native locator。
2. 三套 contract suite 均证明 exact point、canonical traversal、bounded pagination、manifest/count/digest/source proof、cancel/close、无 mutation。
3. 任一 Provider suite 未通过时该 Provider 不得宣称 complete Catalog；三套均通过才满足 Child 6 “完整 Catalog”暴露前提。

### FR-9 Worker safety

1. startup scan 异步执行，不无限阻塞 HTTP server readiness。
2. 同一 Repository 在单 runtime 内串行枚举，全局并发有硬上限；同一 point 由 durable `catalog_build` lease/fence 防重复发布。
3. lease owner 使用稳定 `catalog:<recoveryPointId>`，attempt/generation 另存，不把 generation ID 当 owner。
4. shutdown 停止新调度、cancel 当前 Provider call、等待 bounded grace、释放 lease；超时退出时旧 fence 永远不能发布。

## 7. Non-functional Requirements

- SQLite 与 PostgreSQL 对 generation transaction、locking/fence、stable ordering、cursor 和 ownership filtering 行为一致；真实 PostgreSQL 未执行不得记 pass。
- 所有时间为 UTC；所有动态设置走 `settings.Service`（DB > env > default）。
- 日志使用 `logger.Module`、opaque IDs、低基数 metrics；不记录 raw path/name/query/command/credential。
- request body/query/cursor 有明确 byte/record/page limit；Provider 操作沿用动态 timeout/resource limits。
- `errors.Is` + typed sentinel/capability errors；500 不返回 raw `err.Error()`。
- 前端严格 TypeScript、无 `any`、无 `unknown as T`、无组件 direct fetch。
- 前端所有验证命令使用 `env -u NODE_ENV`。

## 8. Schema / Migration Decision

现行 paired `000062` 已包含 `catalog_generations`、`catalog_entries`、active-generation unique index、count/digest/source/error 字段；`000063` 已让 `recovery_point_leases.holder_type` 支持 `catalog_build`。这些足以实现 generation、entries 和 durable per-point fence。

现表没有 repository-scoped durable lease。父计划 §7 要求 “per-repository scheduler lease”，focused 研究给出两个方案：

- **A — 已选择（2026-07-17）**：官方单 runtime/all-in-one 部署内使用 process-local per-repository lane，跨进程同 point 正确性由 durable per-point lease/fence 保证。它不承诺多个 runtime 对同一 Repository 的不同 points 严格串行，用户已明确接受该父合同强度修订。
- **B — 未选择**：新增 paired SQLite/PostgreSQL durable repository lease schema 与真实 PostgreSQL apply/down/behavior 证据；只有用户以后重新打开此决策时，才由父计划整体重分配连续 migration reservation。

Child 6 因此不新增 migration，也不占用 `000065…000071`。Schema gate、三份规划文档整体审阅与显式 implementation/start 审批均已完成；总控已在获批后执行 Phase 1.4 `task.py start`，Phase 2 按批准计划进行。

## 9. Acceptance Criteria

以下为 Phase 2 实现验收项；没有新鲜测试证据的条目继续保持未勾选，历史 Phase 1 规划验证不计产品 pass。

- [x] AC-1：SQLite 与真实 PostgreSQL 测试证明 generation staging、zero-entry complete、atomic activation、old-active preservation、supersede、abandoned-build reconciliation 和 stale-fence rejection。
- [x] AC-2：immutable manifest ID/revision/count/digest/completeness 与 Provider Catalog proof 完全一致；错配只能 partial/failed，不能 complete。
- [x] AC-3：mutable head 三段 fingerprint race 测试阻止发布；retired head 无 active generation/entries。
- [x] AC-4：Restic/Rsync/Rclone 三套 Catalog contract suites 通过；Command Provider 返回 stable unsupported。
- [x] AC-5：repository/recovery-point/entry list/detail、evidence、exact two-point diff API 与 Swagger 一致，cursor 具有稳定排序和完整 scope/generation binding。
- [x] AC-6：每个 entry DTO 都包含 `recovery_point_id + entry_id`，所有 path/native locator 输入被拒绝或根本不存在于 schema。
- [x] AC-7：Admin/Operator/Viewer + shared repository + typed lineage mismatch + archived/unlinked producer + imported/unattributed matrix 证明授权发生在名称、计数、证据、pagination 之前。
- [x] AC-8：Provider offline 时 immutable committed Catalog 可读且 `content_availability.available=false`；Catalog 从不提供内容字节。
- [x] AC-9：safe enum projection 对未知 DB/provider 值失败关闭，不把 raw 值暴露给 API/frontend。
- [x] AC-10：audit tests 覆盖 repository/point/asset list、evidence、diff 的 success/blocked/failure 且无 path/name/command/secret；handler command-boundary test 通过。
- [x] AC-11：startup/periodic scheduling、bounded concurrency、backoff、lease heartbeat/takeover、cancel、bounded shutdown 测试通过。
- [x] AC-12：frontend raw DTO mappers/domain types/API factories 与 tests 通过；无页面/UI/feature enablement。
- [x] AC-13：`make swag-init` 后 tracked docs、backend route docs、focused tests、`env -u NODE_ENV npm run check`、`make check` 全部通过。
- [ ] AC-14：单一工作提交、独立 archive/journal 提交、单一 PR、required CI、merge/post-merge/main-sync/branch hygiene 按批准流程完成；规划阶段不得把这些记作 pass。

## 10. Approval Record

用户于 2026-07-17 明确同意以下全部门禁：

1. 本 `prd.md`、`design.md`、`implement.md` 的范围与 API/DTO 合同；
2. Child 6 对三类 Provider 增加内部 Catalog proof/read-session seam，但不增加 Provider mutation；
3. `backup_assets.enabled=false`、frontend boundary-only、Command unsupported 和无完整 UI 的范围边界；
4. implementation/start 授权。

Phase 1 审批已经完成，总控已在用户明确批准后执行 Phase 1.4 `task.py start`。Task 当前为 `in_progress`；Phase 2 先执行计划约定的基线、spec 和 pre-development 检查，再修改产品代码。
