# Design — v0.50.4 真实备份数据验收

## Selected approach

采用现有 Task 派生的仓库接入 API，而不是生产 SQL 或新造测试数据：

```text
已完成真实备份的本地 Rsync Task
  -> POST /backup-repositories/connect（只读探测先行）
  -> backup repository + active task link + mutable_head recovery point（单事务）
  -> Catalog worker（最长一个 15m reconcile 周期）
  -> Search worker（最长一个 1m reconcile 周期）
  -> exact-point type=file search
  -> /app/backups/data 资产元数据与内容预览
```

该方案复用线上已经发布的领域边界，最小化生产写入，并能用仓库详情中的安全 lineage 证明活动关联。零仓库 UI 目前没有初次 Connect 入口，但 API 已完整存在；为本次验收新增 UI 不是必要条件。SQL 伪造会绕过探测、加密绑定、审计和事务，因此明确拒绝。

若正式 API 因当前产品确实缺少必要能力而失败，才进入新 Trellis 子任务实现；本 Child 不先验性扩写产品代码。

## Authority and data boundaries

- 认证：同源已登录浏览器的 `sessionStorage` 令牌只在闭包内使用，不输出。
- Task authority：`GET /tasks/:id`、`GET /tasks/:id/runs` 与 Admin `GET /tasks/:id/backup-files?path=/`。
- Repository authority：`POST /backup-repositories/connect`、`GET /backup-repositories/:id`。
- Recovery point authority：`GET /backup-repositories/:id/recovery-points` 与 `GET /recovery-points/:id/catalog-status`。
- Search authority：`POST /asset-search`，schema version 1，root `type=file`，scope `exact_points`。
- UI authority：`/app/backups/data` 的 route state 使用 repository/recovery-point/entry opaque IDs；内容通过正式 delivery-ticket 机制加载。

输出证据只包含必要的状态、计数、时间和 opaque ID。Task 的原始 API DTO 含路径等字段，因此脚本永远构造新的脱敏对象，不直接 `console.log` response data。

## State machine and stop conditions

1. `preflight_pending`：纯只读。任一 HTTP 非 200、非 Admin、仓库非预期、非本地 Rsync、存在活动运行、无成功历史或根目录空，均停止。
2. `connect_authorized`：锁定一个数字 `task_id`；写入前立即重跑关键预检，避免时序漂移。
3. `connected`：Connect 必须单次 HTTP 200，并解析 32 位小写十六进制 repository/recovery-point ID。
4. `lineage_verified`：仓库 `access_active=true`，且目标 Task 有 active task-link lineage；否则停止并保留回退选项。
5. `catalog_wait`：条件轮询；complete 且 indexed entries 大于 0、content available、Catalog list permitted 才继续。Catalog 状态不承载内容 preview 授权，后者在 delivery-ticket/UI 阶段独立证明。failed build 立即停止。
6. `search_wait`：精确 point 的 file 搜索 HTTP 200 且至少一项。只保存第一项 opaque AssetRef 和非敏感元数据。
7. `ui_acceptance`：打开该 AssetRef，验证 metadata 与 preview；需要 step-up 时只在 UI 内完成。
8. `accepted`：复核健康、restart count、关键错误和 collector=0，证据落盘。
9. `rolled_back`：仅在失败/中止时正式 disconnect；不删除备份文件。

## Error handling and rollback

- Connect 的 Provider probe 位于事务前；非 200 不重试、不写 SQL、不把部分状态视为成功。
- Connect 200 但后置 lineage/point 证据异常时，不自动连续写操作；先记录 request ID 与脱敏结果，再由明确的 disconnect 命令回退。
- Disconnect 是本次唯一生产回退写入：撤销 access binding 和 active link，保留底层备份数据。
- Catalog/search 超时不重启容器；记录最后状态和稳定错误码，进入诊断或子任务。
- 任何敏感资产 classification 触发 UI step-up 时，认证输入留在用户浏览器；agent 只记录成功/失败状态。

## Verification

- 本地：相关 repository/catalog/search/content/handler 包测试作为现有链路基线；若需代码变更，先写 RED，再由 `trellis-implement` 实现、`trellis-check` 独立复核。
- 线上 API：release/health、Task 预检、repo/link/point/catalog/search 的结构化脱敏输出。
- 线上 UI：真实资产 metadata 和 renderer 预览截图/人工观察，避免包含敏感文件名或内容。
- 运行态：容器健康、restart count 不变、关键错误为 0、node-log collectors 仍为 0。

## Sequencing

本 Child 完成前不启动节点日志 P1。通过后另建/恢复 P1，保持专用分支/工作树、TDD、Trellis implement/check、PR/CI、绿后直接合并与合并后自动化监控。
