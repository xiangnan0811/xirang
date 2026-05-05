# Wave 2 — 第二轮全方位审查（实读核验门槛）

## Goal

重新对仓库做一轮全方位审查，挑出经实读核验确认的真问题。Wave 0 用"扫描 → 复核"两阶段模式（4 Explore + 4 research = 8 子代理），错报率 ~40%；Wave 2 改为"实读审查一阶段"，每个 finding 必须含 文件:行 + 实读 ≥5 行上下文摘要，避免子代理基于片段直觉给虚报。

## What I already know

### Wave 0/1 已加固的，不再列入 finding（避免 echo）

| 项 | 已修复 commit |
|---|---|
| SSRF 校验（IsLoopback/Private/LinkLocal/Unspecified） | Wave 0 已在原代码 |
| 命令注入（ShellEscape 单引号 + 16 类对抗性测试 + validatePathChars 拒控制字符） | #105 / Wave 1 |
| WebSocket per-message ACL（hub.go 已做） | Wave 0 已在原代码 |
| JWT revoked map 竞态（pruneRevokedLocked） | Wave 0 已在原代码 |
| 任务 goroutine 全局 + 策略级超时（TASK_MAX_EXECUTION_SECONDS） | #105 |
| 文件浏览器远程 symlink 逃逸（sftp.RealPath） | #108 |
| DB 时间存储 UTC（NowFunc + DSN loc + migration 000050） | #108 |
| Dialog 小屏溢出（mx-4 + max-h overflow-y-auto） | #105 |
| logs-viewer 虚拟化（@tanstack/react-virtual） | #108 |
| Integration.endpoint VARCHAR→TEXT（migration 000048） | #105 |
| LOG_FILE 双写支持 | #105 |
| 部署：TZ + logging rotation + HEALTHCHECK + tzdata | #105 |
| docs/env-vars.md 加敏感字段加密策略章节 | #105 |
| CODE_OF_CONDUCT.md | #105 |
| SSHKey.PrivateKey 防御性 json:"-" | #105 |

### Wave 0/1 已确认的误报（不再争论）

P0-1 SSHKey 私钥暴露（toSSHKeyResponse 已脱敏）、B-3 SSRF（已覆盖）、B-5 WS ACL（已做）、B-6 JWT race（已加锁）、F-2 删除二次确认（confirm-dialog 已普遍使用）、F-4 WS 定时器清理（disconnect 完整）、F-5 batch dialog setInterval 泄漏、D-3 dev compose 无卷（实际 ./:/workspace 已持久化）、README_backend.md / web/.env.example 缺失（都存在）。

### 当前 main 状态

- HEAD: `8d95e86` (chore: archive wave-1 + journal)
- v0.19.2 已发布
- 30 个后端测试包全绿；前端 294 个测试全绿
- 47+ migrations（000050 是 Wave 1 加的）

## Audit Scope（4 个领域，每个领域分给一个 trellis-research 子代理）

### 1. 后端安全 + 代码质量（除已加固项之外）
- 重点：未覆盖领域（alerting/dispatcher、reporting/scheduler、metrics 远程推送、bandwidth/带宽调度、anomaly 检测、escalation、ws/terminal、retention worker）
- 不再扫：SSRF/命令注入/symlink/超时/UTC（已加固）
- 关注：错误处理、goroutine 泄漏、资源未关闭、context 传递、并发竞态、SQL 注入残留

### 2. 前端代码 + UX + a11y
- 重点：a11y（aria-*、键盘导航、Radix 组件正确使用）、bundle 体积上涨点、组件 props drilling、API client 错误处理一致性
- 不再扫：dialog 小屏（已加固）、删除确认（已 OK）、logs 虚拟化（已加固）、WS 清理（已 OK）

### 3. 文档 + CI/CD + 治理
- 重点：CHANGELOG/README/docs 与 main 实际状态是否同步（v0.19.2 后）、CI workflow（.github/workflows/）潜在问题、依赖版本是否过时、MIT LICENSE 之外的合规

### 4. 迁移 + 部署 + 运维
- 重点：000050 之后的 migration 设计原则审视、Docker prod 镜像 bundle、监控/可观测性（Prometheus metric 端点）、备份脚本健壮性
- 不再扫：TZ/HEALTHCHECK/logging rotation/endpoint TEXT（已加固）

## Audit Method（关键改进 vs Wave 0）

每个 trellis-research 子代理必须遵守：
1. **Read-first**：每个 finding 必须实读 ≥5 行上下文，并把片段贴到 research 报告里
2. **置信度分级**：每个 finding 标 ✅真实 / ⚠️部分真实 / ❓需进一步实读
3. **不要 echo Wave 0/1 finding**：上面的"已加固"清单不再列；如果发现确实有"加固后的回归"，特别标注 "REGRESSION"
4. **Read budget**：每个领域 read 文件 ≤ 30 个、报告字数 ≤ 3000 字
5. **保守报告**：宁可少不可多。错报一项扣 0.5 分，漏报一项扣 0.1 分（人为不对称鼓励严谨）
6. **可操作**：每个真 finding 必须附 "正确修复方向"（不是 Wave 0 那种"用 EvalSymlinks"这种技术上不可行的方向）

## Research References

- [`research/backend-audit.md`](research/backend-audit.md) — 10 finding（5 真 / 4 部分真 / 1 待核），无 P0；reporting/terminal/alerting 是热点
- [`research/frontend-audit.md`](research/frontend-audit.md) — 13 finding（10 真 / 3 部分真），i18n 缺失 + DialogTitle 是即修项；framer-motion / API client 风格分裂留 P3
- [`research/docs-ci-audit.md`](research/docs-ci-audit.md) — 14 finding（11 真 / 1 部 / 2 待核），CHANGELOG/CLAUDE.md/.env.example 都需补；Trivy 用 @master 是真供应链风险
- [`research/deploy-ops-audit.md`](research/deploy-ops-audit.md) — 16 finding（9 真 / 1 部 / 6 观望），migration 000050 缺事务 + dirty 不拒启动是最严重 + /metrics 公开

## Decision (ADR-lite)

**Context**：Wave 2 用"实读审查一阶段"模式（4 trellis-research 子代理直接出可信 finding，避免 Wave 0 两阶段 40% 错报）。总计 53 finding，按用户选择整改 P1+P2 ~25 项，P3 留作后续 wave。

**Decision**：拆 5 PR 串行实施：
- PR-A 优先（migration 安全网）→ PR-B/D/E 轻量并行可穿插 → PR-C 后端核心慢工细活
- 顺序：A → B → D → E → C

**Consequences**：
- PR-A 是 Wave 1 migration 000050 的回头补救 + 防回归 lint，避免下次 cutover 又出问题
- PR-C 涉及 reporting/terminal/alerting 调度核心，工作量最大，最后做留时间
- P3 大重构（web-terminal reconnect、framer-motion 替换、API client 统一）留下次 wave

## Requirements

### PR-A：Migration 000050 加固 + 防回归（最高 P1，最先做）
- [PR-A1] migration `000050_utc_cutover.up.sql` / `.down.sql` 双轨用 BEGIN/COMMIT 显式事务包裹（避免部分失败后双时区污染）
- [PR-A2] `backend/internal/database/migrator.go`（或 migrate runner 入口）遇 `dirty=true` 拒绝启动（不只 log），需手动 `migrate force <ver>` + 修复后重启
- [PR-A3] 加 lint script `scripts/check-migration-utc-safety.sh`，扫所有 `migrations/{sqlite,postgres}/*.up.sql` 拒含 `localtime` / `CURRENT_TIMESTAMP` / `datetime('now', 'localtime')` 等回归 UTC 不变量的写法
- [PR-A4] `docs/migration-utc-cutover.md` 修：XXXX 占位补具体值；Migrate 步骤改为"启动 migrate-only 实例验证后停 → 再启 prod"

### PR-B：/metrics 鉴权 + 限速
- [PR-B1] `backend/internal/api/router.go` 给 `/metrics` 加 token 鉴权（`METRICS_TOKEN` 环境变量；未设置时回退现有公开行为 + log warning）
- [PR-B2] `/metrics` 接 `middleware.RateLimit`（独立桶，比 /api 宽松）
- [PR-B3] `docs/env-vars.md` §14 增加 `METRICS_TOKEN` 说明 + `.env.production.example` 示例

### PR-C：后端真实修复（reporting / terminal / alerting / silence）
- [PR-C1] `internal/reporting/scheduler.go` 把 `go sendReport(...)` 改为 `wg.Add+go func` 并在 Stop() Wait；`sendReport` 接受 ctx，shutdown 时 cancel
- [PR-C2] `internal/reporting/dispatcher` 中 `report.LastErr` 字段在送外部通知前调用现有 sanitize（与 alerting 同一个）
- [PR-C3] `internal/api/handlers/terminal_handler.go` session 上限 TOCTOU 改 `atomic.AddInt32 + cmpxchg` 或 mutex
- [PR-C4] `terminal_handler.go` SSH 拨号/认证失败路径写 audit_log（拒绝事件）
- [PR-C5] `internal/alerting/dispatcher.go` 慢通道隔离：每个 Integration 独立 worker pool + per-call timeout，不阻塞 task runner 主线
- [PR-C6] alerting 双 sanitize 函数（util.X vs 包内 sanitizeDeliveryError）合并为同一个，覆盖 webhook URL / Bearer Token
- [PR-C7] `silence_handler.go` Patch starts_at 修：要么真正写库，要么彻底拒绝该字段更新（按业务语义二选一）

### PR-D：前端 i18n + Dialog 加固
- [PR-D1] `web/src/components/command-palette.tsx` 加 `<DialogTitle>` 组件（Radix 强制要求；现在 dev 警告 + SR 无标题）
- [PR-D2] `web/src/components/cron-generator.tsx` 14 个 i18n key 全部补到 `web/src/lib/i18n/en.ts`
- [PR-D3] `web/src/pages/settings-page.tsx:71` `silences: "静默规则"` → `silences: t('settings.silences')`
- [PR-D4] `web/src/components/escalation/{escalation-policy-editor.tsx, escalation-level-row.tsx}` 三处硬编码中文按钮/提示 → i18n
- [PR-D5] 加一个组件级测试断言：每个 page 的 hardcoded 中文 lint（可选，留作后续 task）

### PR-E：文档 + 配置 + CI 治理
- [PR-E1] `CLAUDE.md` migration 版本 47 → 50（实际最新）
- [PR-E2] `CHANGELOG.md` v0.19.2 节增补 000050 不可幂等迁移警告 + 安全修复亮点
- [PR-E3] `backend/.env.example` 补 ~25 个文档化变量（ANOMALY_*、RETENTION_CHECK_INTERVAL、BACKUP_STORAGE_*、DB_BACKUP_*、SMTP_REQUIRE_TLS 等）
- [PR-E4] `release-please-config.json` 加 `changelog-sections`（让以后的 release notes 默认含分类，避免 #1 重演）
- [PR-E5] `.githooks/pre-commit` 与 `scripts/check-doc-freshness.sh` 检查规则对齐
- [PR-E6] `.github/workflows/publish-images.yml` Trivy `@master` → 钉版 `@v0.x.y`；scan 移到 push 前（fail-fast）

## Acceptance Criteria

- [ ] **PR-A**：本地用 dirty 状态 SQLite 启动 server，验证拒绝启动；migration 000050 dry-run 任意失败后状态干净；lint script 对故意含 `localtime` 的测试 sql 报错
- [ ] **PR-B**：未设 `METRICS_TOKEN` 时 `/metrics` 仍可访问（保兼容）+ 打 warn log；设了之后 `curl /metrics` 不带 token 返 401；rate limit 用 hey 验证 100 req/s 后被限
- [ ] **PR-C**：reporting Stop() 在 sendReport 在飞时 wait 完才退；terminal 并发 100 个连接 max=10 的限制不被绕过（go test stress）；alerting 模拟某 integration 卡 30s 不阻塞其他 integration 与 task 完成
- [ ] **PR-D**：`npm run dev` 启动后 command-palette 打开无 Radix 控制台警告；切英文 UI 后 cron 生成器、settings、escalation 不再回退到中文
- [ ] **PR-E**：`grep -r '^[A-Z_]*=' backend/.env.example` 数量 ≥ docs/env-vars.md 的总数；CHANGELOG v0.19.2 节包含 "000050"、"不可幂等"、"runbook" 等关键词
- [ ] 所有 PR：`cd backend && go vet ./... && go test ./...` 全绿；`cd web && npm run check` 全绿

## Definition of Done

- 5 个 PR 各自独立 review、可回滚
- 每个 PR commit message 遵循 conventional commits
- 高风险变更（PR-A migrator 拒启动、PR-C 调度核心）PR description 明确风险与回滚方案
- v0.19.3 release notes 由本 wave 完成后 release-please 自动生成（CHANGELOG 含本批 fix/feat 行）

## Out of Scope（明确）

- web-terminal 缺 reconnect/heartbeat/token refresh（P3，与 logs-socket 不一致严重，但需要重写 lib/ws 抽象，留 Wave 3）
- framer-motion 133KB 替换（P3，仅 3 处使用但替换需筛选轻量库 + 改组件）
- i18n 4833 行打包到主 bundle 拆分（P3，需 lazy load i18n resources）
- API client 17 工厂 vs 8 裸函数风格统一（P3，refactor 大）
- Tree.handleToggle mutate 修复（P3，需要重新设计 controlled state）
- dashboard error.includes(t()) 与 i18n 耦合（P3，需要错误码体系）
- CONTRIBUTING.md 加 trellis 工作流说明（低优先，可补在文档 wave）
- CI 加 docker smoke / e2e job（P3，需新 workflow + 长 CI 时间）
- React 18 / Router 6 升级（P3，breaking change 多）
- Bundle / cache 优化等纯性能项

## Acceptance Criteria (evolving)

- [ ] 4 份 audit 报告 persist 到 research/ 子目录
- [ ] 主代理基于 4 份报告产出"经核验真问题清单"（带置信度）
- [ ] 用户决策整改范围（可能是 0~N 项）
- [ ] 整改部分按 PR-A/B/... 实施

## Out of Scope（明确）

- echo Wave 0/1 已加固或已确认误报的项
- 重写或 refactor 没问题的代码
- 引入新功能（本 wave 仅做"审查 + 真问题修复"）

## Technical Notes

- 当前分支：`wave2-comprehensive-audit`（已基于 origin/main 创建）
- 任务目录：`.trellis/tasks/05-04-wave-2/`
- audit 子代理报告：`research/{backend-audit,frontend-audit,docs-ci-audit,deploy-ops-audit}.md`
- 审查时间锚：基于 commit `8d95e86` (HEAD)
