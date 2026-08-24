# 备份资产 v0.50.4 真实数据发布验收

## Goal

在已发布且已启用的 **v0.50.4**（`214f5e18b47974d4e353227fa52782992cef70f7` / `docker.io/linnea7171/xirang:v0.50.4`）线上实例中，通过产品正式领域/API 链路接入一个确有真实文件的本地 Rsync 任务，使“备份 -> 数据”能够搜索、选择并预览真实资产；完成可复核的健康、权限、目录、搜索和内容端到端证据。

历史 v0.50.2 No-Go 保留在 `research/acceptance-protocol.md`，P0 启用死锁修复保留在归档任务 `08-24-backup-assets-enable-transition-deadlock`。两者不得重做。

## Current verified baseline

- v0.50.4 正式发布 revision 为 `214f5e18b47974d4e353227fa52782992cef70f7`。
- 线上容器运行且健康，restart count 为 0，`/healthz` 正常，SQLite schema 为 `72|0`。
- `backup_assets.enabled=true`，FeatureRequested 与 FeatureLive 指标均为 1。
- 仓库列表和两次资产搜索均返回 HTTP 200；当前空态来自 `backup_repositories=0`、活动 `task_repository_links=0`、`recovery_points=0`，不是预览故障。
- GA 清单为 candidates 0、conflicts 1、unsupported 0、capability gaps 12；12 个 Rsync 任务均由 GA readiness 归类为 `no_repository`。Task API 的安全发布摘要则正确保持 `legacy_mutable / legacy / legacy`，两者是不同契约，不可混作 Connect 前置条件。
- 节点日志收集器均保持关闭；真实数据预览验收前不得重新开启。

## Locked decisions

1. 首选并仅允许产品正式接入：`POST /api/v1/backup-repositories/connect`，请求只含确定的 `task_id`。服务从 Task 派生访问绑定，先做只读 Provider 探测，再在事务内建立仓库、活动任务关联与 Rsync `mutable_head` 恢复点。
2. 禁止手工写生产数据库、拼接 Provider locator、复制凭据、打印或回传令牌、Rsync 路径、文件名、命令或原始错误正文。
3. 初次零仓库接入由 Admin 在同源已登录页面的 DevTools 调正式 API；现有 UI 只覆盖已存在仓库的重连，不把 SQL 或 UI 假数据当替代方案。
4. 写入前必须完成只读预检：v0.50.4 健康、Admin 身份、仓库仍为 0、目标 Task 为启用的 Rsync、本地绝对目标、无活动运行、存在近期成功运行、备份根目录确有条目。
5. Connect 只执行一次且不自动重试。HTTP 非 200 立即停止；服务的探测先于事务，失败不得用 SQL 补状态。
6. 若接入后验收失败或明确中止，回退为正式 `POST /api/v1/backup-repositories/{id}/disconnect`：撤销访问绑定与活动关联，不删除或改写备份文件。成功验收不主动断开。
7. Catalog 默认最多等待一个 repository reconcile 周期（15 分钟），随后最多等待一个 search reconcile 周期（1 分钟）；用状态轮询，不用进程重启催化。
8. 如果没有满足预检的 Task，或正式 Connect 暴露产品缺失能力，则在 Trellis 下新建边界清晰的子任务，使用专用分支/工作树、TDD、`trellis-implement`、`trellis-check`、PR/CI 实现；不得绕过领域层。
9. 只有本任务真实数据 UI 预览全部通过后，才创建或恢复节点日志 P1 修复任务；此前日志收集器继续全部关闭。

## Requirements

### R1 — 精确、无敏感泄漏的预检

只输出 HTTP 状态、角色、GA 计数、仓库计数，以及候选 Task 的 ID、节点 ID、启用/运行/校验状态、最后运行时间、是否本地绝对目标和安全 publication 摘要。任何敏感值只留在浏览器会话闭包内。

### R2 — 正式仓库和活动任务关联

Connect HTTP 200 后，仓库详情必须显示在线且 `access_active=true`，并存在 `source=task_link`、`task_id` 等于目标 Task、`active=true` 的谱系。opaque 仓库、关联和恢复点 ID 可以记录；不得记录 locator 或凭据。

### R3 — 恢复点与目录

仓库至少存在一个由目标 Task 产生的 `mutable_head`、`observed` 恢复点。目录状态必须达到 complete，`indexed_entries>0`，内容可用且 Catalog list 权限成立；失败 build 必须记录稳定错误码并停止。Catalog 状态的权限投影是 list-only，内容预览授权由独立 delivery-ticket/UI 路径验收，不得错误要求 Catalog `permissions.preview=true`。

### R4 — 搜索与真实资产

对该恢复点执行精确范围、`type=file` 的 schema v1 搜索。HTTP 必须为 200，索引 coverage 为 complete/partial，返回至少一个真实文件。证据只记录总数、opaque AssetRef、类型、大小、MIME 和时间，不记录名称或 breadcrumb。

### R5 — 页面元数据与内容预览

在 `/app/backups/data` 打开绑定仓库、恢复点和 AssetRef，页面必须显示真实资产元数据，并按产品支持的 renderer 显示内容预览。若资产被分类为 secret/unknown，只能在 UI 内完成正常 step-up；不得把密码、TOTP 或 proof 粘贴给 agent。

### R6 — 健康与错误观测

验收后容器仍 running/healthy、restart count 不增加、`/healthz` 正常；验收窗口无 backup-assets 关键错误。节点日志 collector 数仍为 0，queue_full/fetch_failed/critical 不因本次操作复发。

### R7 — 证据与后续门禁

所有实际时间、HTTP、opaque ID、状态和 UI 结果写入 `research/v0504-real-data-acceptance.md`。未执行不得勾选。只有全部通过才允许启动节点日志 P1。

## Acceptance criteria

- [ ] AC1：v0.50.4 绑定、健康、schema 72|0、FeatureRequested/FeatureLive=1 已复核。
- [ ] AC2：一个确定 Rsync Task 通过只读预检，含成功历史且备份根目录有真实条目。
- [ ] AC3：仓库存在、访问活动、目标 Task 的活动 `task_link` 谱系存在。
- [ ] AC4：目标恢复点存在，Catalog complete、`indexed_entries>0`、Content available、Catalog list permitted。
- [ ] AC5：精确恢复点资产搜索 HTTP 200，至少返回一个真实 file AssetRef。
- [ ] AC6：数据页选择该资产后显示元数据和产品支持的内容预览。
- [ ] AC7：容器健康、restart count 不增加、无关键错误，节点日志 collectors 仍为 0。
- [ ] AC8：证据落盘；没有密钥、密码、令牌、proof、locator、路径或文件名泄漏。
- [ ] AC9：AC1–AC8 通过后才启动节点日志 P1。

## Out of scope

- 重做 v0.50.2 migration No-Go 或已归档的 P0 启用死锁修复。
- 修改 CodeDefault、官方 `.env.deploy` 或公开 Worker。
- 手工生产 SQL、伪造仓库/关联/恢复点/索引数据。
- 为验收而重启进程、提前重新开启节点日志收集器。
- 在真实数据预览通过前实现节点日志 P1。
- 打印或收集任何敏感认证、Provider locator、备份路径或资产名称。
