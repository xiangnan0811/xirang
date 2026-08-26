# 备份资产内容预览授权门禁修复与线上验收

## Goal

修复“备份 -> 数据”工作台把 Catalog 的 list-only 权限投影误当作内容预览授权、从而隐藏正式“加载预览”入口的问题。在不修改 Catalog 数据、不放宽后端 RBAC、不伪造权限的前提下，让已认证且具备当前产品预览角色资格的用户通过正式 delivery-ticket 路径加载所选真实资产的产品支持预览，并完成 v0.50.9 后续正式发布与生产验收。

本任务是父任务 `08-21-backup-assets-release-acceptance` 的 P0 子任务。父任务的仓库、活动任务关联、恢复点、Catalog、Search、真实资产选择和元数据验收均已通过；只有内容预览尚未通过。节点日志 P1 与所有日志收集器继续保持门禁状态，直到本任务生产内容预览验收完成。

## Verified problem statement

- v0.50.9 生产页面已打开正式仓库、恢复点和一个真实 opaque AssetRef，并显示条目类型、大小和修改时间等元数据。
- 同一资产的预览页显示“内容操作不可用”；浏览器没有进入正式 delivery-ticket 内容流程。
- Catalog 合同按父任务锁定为 list-only：生产投影为 `permissions.list=true`、`preview=false`、`download=false`，内容可用且恢复点支持顺序读取。
- 工作台当前把 `selectedCatalog.permissions.preview` 作为显示“加载预览”的必要条件，因此 list-only 投影必然把入口隐藏。
- 后端正式 ticket 路由独立使用 `backup_assets:preview` RBAC；当前角色矩阵为 Admin/Operator 允许、Viewer/未知角色拒绝，服务器拒绝始终是最终授权结果。

## Locked requirements

1. 工作台的预览资格必须同时要求：有效 token、当前角色为 Admin 或 Operator、Catalog list 权限成立、内容可用、选中恢复点存在，并满足所选 renderer 对顺序读取或 Range 读取的能力要求。
2. 工作台不得再要求 Catalog `permissions.preview=true`；不得把 Catalog mapper 或生产数据中的 `preview` 人为改成 true。
3. 前端角色判断只决定是否显示/发起预览请求，不构成授权。正式 delivery-ticket 路由 RBAC 继续是最终授权者；HTTP 401/403/能力拒绝必须走现有安全错误映射并保持 fail-closed。
4. Viewer、未知角色、无 token、Catalog list=false、内容不可用或缺少所需读取能力时，不显示“加载预览”，也不发起 ticket 请求。
5. Admin 的 secret/unknown 内容仍只能走现有 UI step-up；Operator 遇到 `secret_reveal_required` 时不得升级到 Admin 能力或请求 step-up。
6. 本任务只修复 native preview 的授权门禁。不改变下载、导出、恢复、归档处理、Catalog/RBAC/API schema、renderer 选择或内容分类合同。
7. 实现必须使用专用分支/工作树、TDD、Trellis implement/check、PR 与 CI。CI 全绿后合并并持续监控 Release Please、正式发布和 Docker 镜像发布。
8. 生产验收不得泄漏 token、TOTP、proof、Provider locator、路径、文件名或内容；若需要 step-up，由用户仅在产品 UI 内完成。

## Acceptance criteria

- [x] AC1：新增生产等价 RED：Catalog `list=true/preview=false`、内容可用、顺序读取可用、Admin 或 Operator runtime 时，当前代码不显示“加载预览”。
- [x] AC2：修复后 Admin 与 Operator 均可看到并触发“加载预览”；ticket action 使用现有正式 API，Catalog 原始 list-only 投影保持不变。
- [x] AC3：Viewer、未知/空角色、无 token、list=false、content unavailable、缺少 renderer 所需读取能力的负向矩阵均不发起预览。
- [x] AC4：后端现有 Admin/Operator/Viewer delivery-ticket RBAC 回归通过，证明前端改动没有放宽服务器授权。
- [x] AC5：focused、重复、前端完整 `npm run check`、gofmt/diff/privacy 等适用门禁全绿；独立 Trellis check 无未解决 Critical/Important finding。
- [ ] AC6：PR CI 全绿并合并；正式稳定版与 Docker 镜像发布完成并经过发布后监控。
- [ ] AC7：生产升级后使用精确 AssetRef 打开数据页，真实元数据仍可见，“加载预览”可用；按产品 renderer 显示真实内容预览或在产品 UI 内完成必要 step-up 后显示。
- [ ] AC8：生产容器 running/healthy、restart count 不增加、healthz 200、无关键 backup-asset 错误；节点日志 collectors 仍为 0。
- [ ] AC9：父任务记录无敏感泄漏的生产证据并完成 AC1–AC8 后，才允许启动节点日志 P1。

## Out of scope

- 修改 Catalog 的 list-only DTO、数据库行、迁移或生产索引。
- 修改后端角色权限矩阵、delivery-ticket 服务或内容 delivery 数据面。
- 修复宽查询 10k 资源上限、清理逻辑上已失效的 lease 行、重建 Catalog/Search。
- 同时开放 download/export/recover 控件。
- 在内容预览正式验收前重启节点日志 collectors 或推进节点日志 P1。
