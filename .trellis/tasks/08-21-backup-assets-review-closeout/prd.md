# 备份资产审查全量收口

## Goal

把 2026-08-21 对 `main` / v0.50.1 的独立终审里**全部**发现问题修完，使备份资产浏览器可以在默认关闭、Core-only、Admin 可控的前提下进入受控试运行准备态。父任务继续 `planning`。审查过程、风险台账、验收协议只留在本 Child。

这不是「可默认开启」或「关闭父任务」。CodeDefault 保持 `"false"`，不公开发 Worker。本功能尚未完全上线，因此不接受「只修 P0 / 只做 MVP / 其余记债」的缩范围。

## User value

- Viewer / Operator / Admin 都不能再走旧 snapshot HTTP 旁路读备份树或搜路径。
- 管理员热启用后，设置、Catalog、Search、Content 看到同一扇门；不出现「设置已开、搜索 503」的半开状态。
- 秘密搜索没有审计落盘时，结果不会先返回。
- 预览续期不再无故再要一次 TOTP。
- CI 对备份资产核心包和关键漏洞不再软绿。
- 支持矩阵、风险台账、生产验收协议与代码现状一致。

## Background

基线是 `main` / v0.50.1 / `dd2cb4a7`。Children 1–16 已归档。Child 16（PR #444）关掉了 FeatureLive / Admin-only reveal / 版本计数 / 部分 UX，但明确留下遗留 snapshot HTTP 作兼容，并留下 F4/F5/F7。

2026-08-21 独立复审认定：默认关闭的 Core-only 安全基线仍成立；**不能**认定功能已完善；**不能**归档父任务；受控试运行被旧 snapshot 读 API 挡住。完整条目见 `research/review-2026-08-21.md`。

Alan 于 2026-08-21 锁定：创建 Child 17，发现的问题全部修复，不只做 MVP。功能尚未完全上线。

已锁定决策：

1. 本 Child 覆盖该复审的 P0、全部 P1、全部 P2，以及父任务版本事实漂移。
2. 遗留 snapshot **读**路由改为 410 Gone，不再做 Admin-only 兼容保留。
3. 审查结论不放父任务正文。
4. CodeDefault 保持 false；不公开发 Worker。
5. 父任务归档和生产最终验收仍要独立 Go；本 Child 必须交出可执行的验收协议和权威风险台账。
6. Native AWS Rclone 在 live suite 跑通之前不得进入支持矩阵。

## Requirements

### R1 — 关闭遗留 snapshot 读旁路（P0）

- `GET /api/v1/tasks/:id/snapshots`
- `GET /api/v1/tasks/:id/snapshots/:sid/files`
- `GET /api/v1/tasks/:id/snapshots/search`
- `GET /api/v1/tasks/:id/snapshots/diff`

对已认证的 Admin / Operator / Viewer 一律返回 **410 Gone**，不得再列出 snapshot、文件树、路径搜索或 diff。不得再用 `tasks:read` + `OwnershipTaskCheck` 作为这些读接口的有效授权。浏览、搜索、对比只走 Catalog / Search / Overlay。

证据锚点：`backend/internal/api/router.go:765-769`，`backend/internal/api/handlers/snapshot_search_handler.go:51`。文档 `docs/admin/backup-recovery.md` 中的旧 search URL 必须改写或标明已退役。

### R2 — 旧恢复面与受控恢复对齐（P0/P1）

`POST /api/v1/tasks/:id/snapshots/:sid/restore` 不得继续作为未受 FeatureLive 约束的第二恢复面。必须满足：仅 Admin、既有 step-up + credential grant、且 `Runtime.FeatureLive()` 为真；否则 410 或 403。Viewer / Operator 直接打 restore 必须失败。受控恢复仍是产品主路径。

### R3 — 启用必须原子（P1 / Child 16 F4）

`TransitionFeature(true)` 必须在同一次成功路径里完成：准入授权、content prepare、search rekey / search ready。任一步失败则回滚到未启用，设置不得显示已开。Worker 仍可选；Worker 未就绪不得把搜索或目录标成失败，也不得把 processing 标成已就绪。

证据锚点：`backend/internal/backupasset/runtime/runtime.go:1940-1969`。

### R4 — Handler 只认 FeatureLive（P1 / Child 16 F5）

Catalog / Search / Overlay / Content / Retention / 旧 restore 的 handler `Enabled` 必须来自 `Runtime.FeatureLive()`，不得只读 `SearchOverlayConfig().Enabled`（requested）。环境变量单独为 true、ack 未过、search 未就绪时，HTTP 面必须关闭。

证据锚点：`backend/internal/api/router.go:176-199`。

### R5 — 设置 UI 不得假装已上线（P1）

管理员保存 `backup_assets.enabled=true` 后，若 runtime 尚未 `FeatureLive`，设置页必须明确「未上线 / 需完成启用条件」，不得出现可点进工作区且搜索 503 的半开态。热启用成功则工作区与搜索同时可用，无需为搜索单独重启。若进程级动作（例如可选 Worker）仍需重启，文案只说 Worker，不说整个功能。

### R6 — 搜索审计失败则不返回结果（P1）

`BackupAssetSearchHandler.writeAudit` 失败时不得只打日志后继续 200。至少对 `asset.secret_reveal`、分类为 secret 的命中、以及一切成功搜索：审计写入失败则请求失败（5xx），结果不返回。仓库没有现成 audit outbox，本 Child 不新造队列；先 fail-closed。

证据锚点：`backend/internal/api/handlers/backup_asset_search_handler.go:267-273`。

### R7 — 预览续期复用会话内 reveal proof（P2 / Child 16 F7）

`renewPreview` 不得用 `revealOnce: true` 丢掉已成功的会话内 proof。只在内存复用：同一 session、同一 user、同一 asset、同一 action、proof 未过期。角色、权限、资产或 session 变化时清除。过期或缺失时再走 Admin TOTP。

证据锚点：`web/src/features/backup-assets/use-backup-assets-state.ts:1285-1306`。

### R8 — 真实浏览器可完成主路径（P2）

预览面板高度必须可实际阅读（不得再用 18rem 把预览挤没）。列表/网格行必须有稳定 React key。Restic Range：有自动化证据，或对 Restic 明确禁用 Range 并在 UI 说明。本 Child 增加 Playwright 主路径（Chromium / Firefox / WebKit）：登录后工作区在功能关闭时不可用；Admin 在 FeatureLive 后可浏览/搜索/预览（用 fixture，不打生产）。

### R9 — CI 对备份资产不再软绿（P1）

- 备份资产核心包（`backend/internal/backupasset/...`、相关 handlers）覆盖率设可执行下限，低于下限失败。
- 上述包以及 `backend/internal/api/handlers` 中备份资产/旧 snapshot 测试跑 `go test -race`。
- `govulncheck`、`npm audit` 对高危及以上改为阻断；允许的例外必须写进 allowlist 文件并在 PR 说明。
- Vitest 全局：MSW `onUnhandledRequest: "error"`；未在 allowlist 的 `console.error` / `console.warn` 失败。

不要求一次把全仓库 coverage / 全包 `-race` 都变成硬门。范围是备份资产核心加本 Child 改到的 CI 合同。

### R10 — 规模、故障、云厂商诚实化（P1）

- `scripts/test-backup-asset-load.sh` 的 million-catalog / zip-bomb / process-restart 不得再是「打印拒绝后成功退出」的占位。CI 跑有上限的可执行档（至少 10k catalog + zip-bomb 拒绝 + 受控 SIGKILL 恢复）；百万级作为显式 operator 档，文档写清如何跑、跑失败算未认证。
- Native AWS Rclone 保持 `not_executed`，从产品支持矩阵和用户文档中移出「已支持」。Portable Rclone 可单独保留。
- 现有 backup-asset 指标补上文档化的 SLO 与至少一条可配置告警规则（搜索 5xx、审计写入失败、FeatureLive 抖动）。没有 Grafana 仪表盘仓库也不新造；以代码 + `docs/` 为准。

### R11 — 权威风险台账与版本事实（P2）

本 Child `research/risk-ledger.md` 是父任务归档前的唯一权威台账：ID、severity、status、code evidence、test evidence、owner、blocker/waiver、target version。旧 research 标 superseded。父任务 `task.json` notes 改为：正式公开发布是 **v0.50.1**；parent-final-acceptance 仍未授权；Child 17 进行中。不把审查长文贴进父任务。

### R12 — 生产最终验收协议（P1）

本 Child 交付 `research/acceptance-protocol.md`，绑定：git SHA、镜像 digest、DB 引擎、Provider 模式、浏览器、验收人、失败/豁免、回滚演练。Alan 在生产机走查并勾选之前，不得把父任务标完成，也不得宣称 wide GA。本 Child 可以在代码/CI/台账完成后请求实现侧归档，但验收协议未填真实证据时必须保持「生产验收未执行」。

## Acceptance Criteria

- [ ] AC1：Admin / Operator / Viewer 对 R1 四条旧读路由均 410；Catalog / Search 不受此 410 影响。
- [ ] AC2：Viewer / Operator 不能经旧 restore 恢复；Admin 在 FeatureLive=false 时旧 restore 失败；FeatureLive=true 时仍要 step-up。
- [ ] AC3：热启用成功后，无需重启即可 Search 200（fixture）；启用失败时设置仍为关，FeatureLive=false。
- [ ] AC4：requested=true 但 FeatureLive=false 时，Catalog / Search / Overlay / Content HTTP 关闭。
- [ ] AC5：设置页在未 FeatureLive 时不进入可搜索工作区。
- [ ] AC6：搜索审计写入失败时，含 secret_reveal 的请求不返回命中。
- [ ] AC7：同会话同资产预览续期不再要求第二次 TOTP；换资产或登出后必须重新证明。
- [ ] AC8：Playwright 三条浏览器跑通关闭态拒绝 + 开启态浏览/搜索/预览 fixture。
- [ ] AC9：CI 对 R9 所列门失败即红；本 Child 的 race / coverage / audit 变更有证明。
- [ ] AC10：load script 的 CI 档可执行且失败会红；million 档文档化；Native AWS 不在支持矩阵。
- [ ] AC11：`research/risk-ledger.md` 覆盖本复审全部条目，状态与代码一致；父 notes 写 v0.50.1。
- [ ] AC12：`research/acceptance-protocol.md` 存在且字段完整；未填生产证据时明确 `not_executed`。
- [ ] AC13：CodeDefault 仍为 `"false"`；`publish-images.yml` 无 Worker 发布；父任务仍为 `planning`。

## Out of scope

- 翻转 `backup_assets.enabled` CodeDefault 或官方 `.env.deploy` 为 true。
- 公开发 Worker / 修改 `publish-images.yml`。
- 归档父任务，或把本 Child 的实现完成当成 parent-final-acceptance。
- Wide GA / 默认开启。
- 新造通用 audit outbox / 消息队列。
- 全仓库（非备份资产核心）coverage 硬门或全包 `-race`。
- 把 Native AWS 写成已支持。
- 在没有 Alan 生产机授权时伪造生产走查证据。

## Technical notes

- 410 使用现有 `response.go` 助手；若没有 410 helper 就加一个，禁止 handler 里 `c.JSON`。
- 旧 snapshot 测试改为断言 410，不要删路由注册（注册保留、handler 退役，避免客户端得到 404 误判为「任务不存在」）。
- OpenAPI / Swagger 必须 `make swag-init`。
- 规格更新写进 `.trellis/spec/backend/quality-guidelines.md` 与 frontend type-safety：遗留 snapshot 读路由必须 410；handler Enabled = FeatureLive；搜索审计 fail-closed。
