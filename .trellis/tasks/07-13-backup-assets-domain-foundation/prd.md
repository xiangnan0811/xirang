# 备份资产领域基础

## Goal

在不启用任何公开备份资产功能、也不读写 Provider 数据的前提下，建立完整终态共用的备份资产领域基础：独立 `BackupRepository` / `RecoveryPoint` 身份与状态、复合 `AssetRef`、能力与错误契约、双数据库基础 schema、分域密钥基础设施、通用恢复点租约、purpose-bound step-up、专用 RBAC、typed 资产审计注册表，以及默认关闭的 feature gate。后续 14 个子任务只能依赖这些稳定契约扩展，不能另建平行身份、安全或生命周期模型。

## Parent Contract

- 父任务：`07-12-backup-data-explorer-design`。
- 规范来源：父任务 `prd.md`、`design.md`，以及 `implement.md` 的 Execution Contract、Child 1、Requirement Coverage Matrix、High-Risk Review Gates 与 Program Rollback Strategy。
- 本任务无前置子任务；必须从已经包含父规划包的最新 `main` 开始。
- 本任务属于复杂跨层变更。进入实施前必须补齐并复核 focused `design.md` 与 `implement.md`，然后由用户明确授权执行 `task.py start`。

## Confirmed Code Evidence

- 当前分支为 `codex/backup-assets-domain-foundation`，seed commit 为 `743aa94`，其直接基线 `4ea4c2b` 是已通过 PR #371 合并到 `main` 的父规划包；开始 focused planning 时工作区干净。
- SQLite 与 PostgreSQL 当前最高 migration 都是 `000061_task_runs_traffic_indexes`，`000062` 在两端均未被占用。
- 当前没有 `backend/internal/backupasset` 包、备份资产基础表、RecoveryPoint 租约或 PostgreSQL migration apply/down CI harness。
- 当前 step-up JWT 只有通用 `purpose=step_up`；签发请求只提交 TOTP code，验证器不接收 expected action，终端 WebSocket 也接受任意有效通用 proof。前端只在 sessionStorage 保存一个全局 proof。
- 当前九个高风险动作边界为 SSH Key 导出、终端打开、配置导入、敏感配置导出、快照恢复、任务恢复、任务手工触发、任务批量触发和批量命令创建；它们必须与八个新资产动作一起迁移为 action-bound proof。
- 当前 `secure` 包的 `enc:v2` 字符串加密适合字段加密，但没有带 domain/version AAD 的小型数据密钥包装 envelope，也不能用上一把 v2 KEK 对 domain key 执行 rewrap；Child 1 必须增加独立 wrapping primitive，不能复用大内容加密。
- 当前 HTTP `AuditLogger` 跳过 GET/HEAD，且现有单链不能安全裁剪历史；备份资产 list/preview/read 因此必须使用独立 typed action registry、专用 sanitizer 和 segment/checkpoint chain。
- 当前 settings registry 有 34 项且支持 DB > env > default、Sensitive 和 RequiresRestart；Child 1 新增设置必须沿用该 registry，并同步环境变量参考。
- 当前 `Task` 没有归档时间，`TaskRun`/旧 `SnapshotFileIndex` 也没有可信恢复点身份；本任务只增加可空 `tasks.archived_at` 和独立 lineage 基础，不改变现有 Task 删除行为。

## Requirements

### Domain And Identity

- 建立独立于 Task 的 `BackupRepository`、`RepositoryAccessBinding`、`TaskRepositoryLink`、`RecoveryPoint`、Manifest/Catalog generation/entry 基础实体，不把 TaskRun 或旧 `SnapshotFileIndex` 冒充恢复点或可信清单。
- 冻结 Repository status、RecoveryPoint state、Task publication mode、Repository version mode、RecoveryPoint version semantics、immutability、physical availability、hold state 和 capability reason 等 typed enums，并通过纯状态机拒绝非法转换。
- `mutable_head` 使用每仓库稳定单例 RecoveryPoint ID；`observed`/非破坏性 `retired`、显式物理 purge 的 `expiring`/`expired` 语义必须与不可变点分离。不得把 observed 行改写成 committed/imported baseline，也不得复活 retired AssetRef。
- 对外资产身份固定为 `AssetRef{recovery_point_id, entry_id}`；entry ID 单独不能解析。Command Task 在没有显式产物契约时返回 typed capability unavailable，不生成虚假资产。
- Provider locator、Repository access binding、凭据和底层错误不得进入公开 DTO、日志或审计；模型必须使用显式 sanitized DTO，而不是返回 raw GORM model。

### Schema And Cross-Database Contract

- 以 `000061` 为当前基线，在 SQLite/PostgreSQL 同时提供成对的 `000062_backup_asset_foundation` up/down migrations。
- schema 覆盖仓库/access/link/recovery point/manifest/catalog generation/catalog entry、versioned wrapped-domain-key、RecoveryPointLease、分段资产审计事件/checkpoint，并为 Task 增加安全的可空 archival 字段。
- 可空 Task/TaskRun lineage 使用 `ON DELETE SET NULL` 与不可变摘要保留历史语义；不得让删除 Task 级联删除仓库、恢复点或资产证据。
- 建立真实的双数据库 migration apply/down 集成 harness 和 PostgreSQL CI service；缺少 PostgreSQL DSN 时相应 CI 必须失败而不是跳过。

### Key Domains And Leases

- 提供通用 versioned keyring：随机域密钥生成、KEK 包装、版本、rewrap、可用性和丢失状态；不同领域不得共享数据密钥。
- Child 1 至少建立 Entry Identity、Cursor Signing、Audit Fingerprint 与 Recovery Cleanup Ownership 的独立领域契约。Entry Identity/Recovery Cleanup Ownership 为安装级稳定；普通 KEK 轮换只能 rewrap。
- 提供统一 `RecoveryPointLease` acquire/renew/release/takeover/fencing 服务，holder 至少支持 `rsync_parent | catalog_build | content_session | processing_job | export_job | recovery_job`，并同时限制短 lease 与绝对 deadline。
- takeover 后旧 fence 不得发布、读取或推进状态；服务必须支持启动 reconciliation，而不是依赖进程内锁或文件 age。

### Purpose-Bound Step-Up And RBAC

- 保留 JWT `purpose=step_up` 作为 token class，新增必填、服务端 allowlist 的 typed `step_up_action` claim；请求 DTO、签发 API、验证 API 和所有既有调用方都必须显式传递 expected purpose。
- 注册全部现有高风险 purpose（包括 `terminal.open`）以及 `asset.secret_reveal`、`asset.download`、`asset.export_create`、`asset.export_download`、`asset.recover`、`recovery.result_download`、`recovery.result_retain`、`repository.purge`。
- 缺失、未知、旧通用 proof 和任何错误 purpose 必须失败；后端运行完整 pairwise cross-purpose rejection 与 caller/router coverage 测试。终端 WebSocket 只接受 `terminal.open`。
- 前端 TOTP API、Auth context、hook 与 storage 以 purpose 为键；默认不复用，只有同 purpose 且未过期的 proof 才能按策略复用。
- 新权限固定为 `backup_assets:list|preview|download|export|recover` 与 `backup_repositories:manage|purge`。Admin 拥有全部，Operator 仅 list/preview，Viewer 无资产权限；repository purge 不由 manage 隐式获得。
- 恢复结果交付由 `backup_assets:recover` + exact RecoveryJob ownership + dedicated purpose 管理；`backup_assets:download` 单独不足，也不是额外叠加条件。

### Audit, Settings And Feature Boundary

- 建立唯一 typed 资产 action registry、sanitizer、分段 hash-chain/checkpoint/anchor 基础；新路由不得使用自由字符串 action。
- 审计不得保存 raw path/name/query/snippet/content/ticket/cookie/JWT/credential/Provider locator；低熵关联使用独立 keyed Audit Fingerprint，不使用裸 hash。
- 注册安全默认 settings：`backup_assets.enabled=false`、Catalog batch/build timeout、repository reconcile interval、审计 segment/retention、lease duration/heartbeat/absolute deadline。路径/密钥设置标记 RequiresRestart，秘密值标记 Sensitive。
- 本任务不增加公开 backup asset route，不执行 Provider publication/read/delete，不迁移现有 Provider 数据，也不启用 UI。feature gate 在 Child 1 合并后仍默认关闭。
- 新 settings 的环境变量、默认值和 KEK 轮换语义必须同步到 `docs/env-vars.md` 及后端环境示例；不得把规划文档发布为用户功能说明。

## Constraints

- 遵循现有 response helpers、Auth/RBAC/ownership、structured logging、sentinel error、settings registry、sanitized model 和双数据库 migration 规范。
- 不降低现有终端、配置导入/导出、任务触发/恢复等高风险流程的安全性；purpose migration 必须原子覆盖全部调用方，不保留通用 proof 兼容旁路。
- schema 和 API 契约必须为后续 Provider、Catalog、Content、Worker、Export、Recovery 与 Retention 子任务提供稳定扩展点，但本任务不得提前实现这些上层能力。
- 任何设计偏差必须先回写父 `design.md` 与本任务 focused 设计/计划，不能只在代码注释中改变契约。

## Acceptance Criteria

- [x] focused `design.md` 与 `implement.md` 已基于最新 `main`、父规划和相关 Trellis specs 完成并经用户复核。
- [x] Repository/RecoveryPoint/AssetRef/capability/state machine 的全部合法与非法组合有单元测试，包含 mutable-head stable ID、retirement/purge 边界和 Command unsupported。
- [x] SQLite/PostgreSQL `000062` migrations 均通过真实 apply/down、FK/index/UTC 检查，且 CI 不允许缺失 PostgreSQL 验证。
- [x] model、公开 DTO、日志和审计测试证明 access binding、credential、locator 与 Provider 原始错误不会泄露。
- [x] versioned keyring 与四个基础 key domains 通过生成、rewrap、轮换重叠、丢失影响和分域测试。
- [x] RecoveryPointLease 通过 acquire/renew/release/takeover、absolute deadline、旧 fence 拒绝和重启 reconciliation 测试。
- [x] purpose-bound step-up 覆盖现有后端/前端全部 caller，缺失/未知/旧通用 proof 被拒绝，完整 cross-purpose matrix 通过，终端仅接受 `terminal.open`。
- [x] 新权限在所有 role maps 一致，Viewer 无存在性泄漏，purge 不由 manage 推导，恢复结果权限规则被固定测试。
- [x] typed asset audit registry、sanitizer、segment/checkpoint continuity 和 keyed fingerprint 基础测试通过。
- [x] 所有新增 settings 通过 registry/default/sensitivity/restart 测试，`backup_assets.enabled` 保持 false。
- [x] focused backend、frontend、双数据库 migration 与全仓质量门禁通过；提交不包含公开资产路由、Provider mutation 或 feature enablement。
- [x] 新增 settings/env 参考与 `DATA_ENCRYPTION_LEGACY_KEY` 的 domain-key rewrap 说明和实际代码一致。
- [ ] 独立分支 PR 的 required CI 全绿，合并后自动化已检查，本地 `main` 已同步后才允许创建 Child 2。

## Out Of Scope

- Rsync/Restic/Rclone 读取或发布实现。
- Catalog 构建、搜索、浏览 API 和前端工作区。
- Content ticket、Range、预览缓存与下载。
- Worker、派生物、导出、归档浏览、受控恢复、保留/purge 执行和 GA 启用。

## Open Decisions

- 无产品范围决策。focused 设计阶段只允许根据最新代码补充实现细节、测试夹具和精确文件清单；如果发现会改变父任务安全或领域边界的事实，必须返回父规划审阅。

## Notes

- 用户于 2026-07-13 同意采用 planning parent + 顺序创建子任务的交付方式，并同意在新会话中推进 Child 1。
- 创建 Child 1 不等于启动实施；本任务在 focused planning package 通过 review gate 前保持了 `planning`。
- 用户于 2026-07-13 明确批准 focused package；随后仅启动 Child 1，任务状态转为 `in_progress`，父任务继续保持 `planning`。
