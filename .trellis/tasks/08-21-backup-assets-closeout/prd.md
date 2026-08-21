# 备份资产审查收尾

## Goal

Child 16 是 v0.50.0 之后备份资产浏览器的**收尾载体**：先修当前终审已点名的缺口，再承接后续外部复审发现的问题。父任务继续 `planning`。审查过程文档只留在本 Child，不写进父任务。

这不是「可默认开启」或「关闭父任务」的验收。CodeDefault 保持 `"false"`，不公开发 Worker。本 Child 在 Alan 根据复审明确要求归档之前保持未归档。

## User value

- 管理员用设置界面或环境变量请求启用时，准入、Catalog、Search、Content 看到同一扇门。
- 启用后，秘密文件按已有 `asset.secret_reveal` 合同可揭示/预览；版本检查器和全历史搜索能看见并打开其它保留恢复点。
- 不依赖公开发 Worker 的体验缺口被关掉或说清楚。
- 复审再发现问题，在本 Child 里改规划再修，不新开 Child 17（除非 Alan 明确要求拆分）。

## Background

基线是 `main` / v0.50.0 / `1750a238`。Children 1–15 已归档。2026-08-21 终审认定：默认关闭的 Core-only 架构和安全基线成立；不能认定功能已完善，也不能归档父任务。

已锁定决策：

1. 完成线按建议 R1–R6（Important 做成可用，不是只改文案）。
2. 本 Child 范围不限于这一轮问题；后续复审问题继续在这里修。
3. 审查结论不放父任务。
4. CodeDefault 保持 false；不公开发 Worker。
5. 遗留 snapshot HTTP 保持兼容，不作为主 UX，本波不删除。
6. 生产机走查和父任务归档不在本 Child 验收里。

## Requirements

### R1 — 启用门必须是一扇门

`FeatureEnabled()` 继续表示**请求值**（DB > env > CodeDefault）。Catalog / Search / Overlay / Content 的公开读路径必须跟 admission 一样，只在就绪门禁已通过时放行。

env `BACKUP_ASSETS_ENABLED=true` 但现有安装未 ack：Core 能启动；admission 保持 disabled；Catalog/Search/content 保持关闭。设置界面启用未就绪的现有安装仍 409。

锚点：`runtime.go` 约 2218–2226（`StartupPass` 阻塞后 `InitializeDisabled` 并跳过 content）；`service.go` 约 316–325（`FeatureEnabled` 只读设置）；`catalog/service.go` 约 825–837（`ensureFeatureEnabled` 跟后者）。Search / Overlay 使用同一注入回调。

### R2 — 秘密揭示接到现有后端

Admin 对 secret/unknown 走已有 `asset.secret_reveal` step-up（`STEP_UP_ACTIONS.assetSecretReveal`）。成功后预览票据和**每一次**搜索请求（含翻页、已保存搜索刷新）带同一 session proof。无 proof 继续 fail-closed。

Operator/Viewer **不能**揭示：前端不得为其弹出 `asset.secret_reveal`；后端 ticket / search 对非 Admin 的该 proof 必须拒绝。`POST /auth/step-up` 本身没有角色白名单，不能当门。

Wave 1 只在首次 `executeSearch` 带 proof，且 `secret_reveal_required` 时只要有 `ensureStepUpProof` 就重试——页面把该回调给了所有角色，等于拉开 Operator 口子。见 `research/re-review-1.md` F1/F2。

### R3 — 版本与全历史搜索不再撒谎

同一谱系+路径若有多条保留命中：全历史搜索保留「最新一条」主命中，但必须带**用户可见**的保留版本计数（结果行，不只是 API 字段或版本检查器）；版本检查器列出其它保留恢复点，点击可打开（不透明深链接）。禁止只改「展开不可用」文案。

Wave 1 后端已写 `retained_version_count`，mapper 已收，`commitSearchProjection` 丢掉了。见 `research/re-review-1.md` F3。

锚点：`search/service.go` `groupAllRetainedHits` 约 1313–1324；`asset-versions.tsx` 约 33–35。分组键已是 `lineageToken + pathGroupToken`（HMAC 令牌，不是原始路径）。Catalog 用 `catalog_entries.normalized_path` + 同一 lineage 的活跃 generation 做有界历史列表；响应只有不透明 ID、时间、大小、类型。

### R4 — 不依赖公开发 Worker 的体验

- 原地恢复：切换到 `in_place` 或点「创建计划」之前，必须有单独确认。默认目标仍是 isolated。锚点：`recovery-plan-wizard.tsx` 约 266–321。
- 预览主面板必须能表达 capability 码（例如 `range_unavailable`），不能只在上下文面板。锚点：`asset-preview.tsx` 约 382–386。
- Worker 未部署时，ZIP/Office/OCR 明确是可选增强，不装成产品坏了。不把这些做成 Core 原生能力。

### R5 — 小 Residual

- Ack 审计记录真实 `conflicts` 计数，不写死 0。锚点：`ga/inventory.go` 约 219–223；同文件约 725 已有正确计数可复用。
- `AdmissionController.Initialize()` 单独调用不得把未授权的 env `true` 当成 `AdmissionManaged`。`InitializeDisabled` 与 `StartupPass` 授权之后的 `Initialize` 保持现语义。锚点：`runtime/controller.go` 约 152–166。
- Swagger / 注释与 `ListRetentionPolicies` 的 `NextCursor` 实际形状一致（未签名原始 ID，Admin-only）。不改成签名游标。

### R6 — 本 Child 收尾与复审循环

Wave 1 修好 R1–R5 后：**先复审工作分支**。复审无误才提交、开 PR、合入。不要把「先合入再复审」当成闸门。不翻转 CodeDefault，不改 `publish-images.yml`，不归档父任务。

Wave 1 合入 **不是** 本 Child 归档条件。复审若有问题：在本 Child `research/` 记录、修订本 Child 的 PRD/design/implement、开下一波实施，仍是先复审再提交。过程文档不进父任务。

## Acceptance criteria

### Wave 1（当前终审）

- [ ] AC1 env `BACKUP_ASSETS_ENABLED=true` 且现有安装未 ack：Core 启动成功；Catalog/Search/content/admission 全部保持关闭（自动化测试）。
- [ ] AC2 设置界面启用未就绪的现有安装仍 409。
- [ ] AC3 Admin 完成 `asset.secret_reveal` 后可以预览被标为 secret 的条目；无 proof 仍被拒；Operator 不能揭示（即使页面传入了 `ensureStepUpProof`，前端不弹 step-up、不重试票据；后端拒绝 Operator 的 secret-reveal proof）。Admin 揭示后的搜索翻页 / 已保存搜索刷新仍携带同一 proof。
- [ ] AC4 同一路径存在多个保留恢复点时，全历史搜索命中在结果行上可见保留版本计数；版本检查器能列出并打开其它点。
- [ ] AC5 切换原地恢复在创建计划前有确认；Worker 缺失和 capability 失败在主预览区可见，且不误报成功。
- [ ] AC6 Ack 审计日志的 `conflicts` 等于当次库存计数；`Initialize()` 在未授权的 requested-true 下不会进入 managed。
- [ ] AC7 CodeDefault 仍为 `"false"`；`publish-images.yml` 无 Worker；父任务仍为 `planning`。
- [ ] AC7b 工作分支复审无误之后，才提交 / 开 PR。复审未通过或未做，不得合入。

### 本 Child 关闭（后置，需 Alan 明示）

- [ ] AC8 至少完成一轮 Wave 1 之后的外部复审；新增问题已在本 Child 修完或被 Alan 明确推迟。
- [ ] AC9 Alan 明确要求归档本 Child。归档本 Child 仍不等于归档父任务。

## Out of scope

- 把 CodeDefault 改成 `true`，或宣称可默认开启。
- 公开发 Worker / 改 `publish-images.yml`。
- 让 ZIP 内浏览、Office 内联、OCR/正文搜索在没有 Worker 时变成 Core 原生能力。
- 删除遗留 snapshot HTTP。
- 百万条目 CI、AWS live-suite、Child 13 历史 RED 重建。
- 父任务最终验收/归档。
- 把本轮或后续复审过程文档写入父任务。
- 未修订本 Child 规划就实施复审新问题。

## Technical notes

公开文档（`docs/env-vars.md`、`docs/admin/backup-recovery.md`）必须写清：请求启用 ≠ 功能已开。不要把审查画布或本 Child 过程文档链进公开文档。
