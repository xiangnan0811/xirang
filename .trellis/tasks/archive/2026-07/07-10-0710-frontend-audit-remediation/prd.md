# PRD: 前端深度审查修复（渲染隔离 / 域映射 / 轮询门控 / 状态拆分）

> 来源：2026-07-10 对 `web/src` 全量深度审查（context / hooks / lib / pages / components + 部署层 nginx）。
> 本任务为 **parent（规划 + 集成验收）**，不直接写业务代码；具体改动由 5 个子任务各自完成、各自 PR、各自归档。
> 工作必须在工作分支上进行（`fix/*` 或 `refactor/*`），不允许在 `main` 直接改动源码。

---

## 0. 问题真实性核验（已逐项确认，无臆测）

| ID | 问题 | 证据（真实存在） | 结论 |
|----|------|----------------|------|
| A2 | `useConsoleData.refresh` 每次渲染新建箭头函数，破坏 `sharedContextValue` 的 `useMemo`，导致 SharedContext 消费者随外壳每次渲染重渲染 | `web/src/hooks/use-console-data.ts:474`（内联 `refresh: () => {...}`）；`web/src/components/layout/app-shell.tsx:89,94`（作为 `useMemo` 依赖项） | **真实** |
| A3 | `service-monitors` 域绕过 snake→camel 映射约定，raw 字段泄漏进组件 | `web/src/types/domain.ts:1039+`（`ServiceMonitor` 含 `interval_seconds/http_method/http_headers/uptime_pct/last_checked_at`）；`web/src/pages/service-monitors-page.tsx`、`status-page.tsx` 直接消费 `editingMonitor.interval_seconds` 等并含 `as "http"\|"tcp"` 断言 | **真实** |
| P1 | REST 轮询无可见性门控，后台标签页仍 30s 发请求 | `web/src/hooks/use-alert-bell.ts:37`、`status-page.tsx:56`、`use-console-data.ts:317-325` | **真实** |
| P2 | 自动刷新间隔在挂载时快照，改设置后不重建定时器 | `use-console-data.ts:319` 取一次 `getRefreshIntervalMs()` | **真实** |
| A1 | `useConsoleData` 为集中于巨型 god hook（~55 成员 / 529 行），单一状态变更放大整壳重渲染 | `use-console-data.ts` 全量；`app-shell.tsx:67` 在其内调用 | **真实（结构性）** |

### 已确认无问题的维度（避免误报）
- **XSS**：生产代码零 `dangerouslySetInnerHTML` / `innerHTML` / `eval`；测试断言 `not.toContain("<script>")`。
- **CSRF**：认证走 `Authorization: Bearer`（非 Cookie 会话）→ 天然免疫。
- **敏感数据落盘**：token 仅 `sessionStorage`；step-up proof / grant 明确不落盘；401 自动清理。
- **响应头**：`deploy/nginx/templates/default.conf.template` 已配 CSP `script-src 'self'` / `X-Frame-Options: DENY` / `nosniff` / `Referrer-Policy` / `Permissions-Policy`。
- **资源清理**：`ReconnectingSocket`（退避 + 心跳 + 可见性恢复 + stale-open 防护）、`useLiveLogs`（rAF 批处理 + 去重 + `MAX_PENDING=500` + 400 条滑窗）均已规范处理；异步普遍用 `AbortController`。

---

## 1. 子任务映射与优先级

| 子任务 | slug | 维度 | 风险 | 成本 | 验收门槛 |
|--------|------|------|------|------|----------|
| A2 稳定 refresh | `0710-a2-refresh-memoization` | 性能/架构 | 中 | 极低 | 分支 `fix/a2-refresh-memoization`；`refresh` 用 `useCallback`；`SharedContext` 消费者在仅外壳其它状态变化时不再无谓重渲染；现有 `app-shell.test.tsx` 通过；`npm run check` 通过 |
| A3 域映射器 | `0710-a3-servicemonitor-mapping` | 架构/类型 | 中 | 中 | 分支 `fix/a3-servicemonitor-mapping`；新增 `mapServiceMonitor`/`RawServiceMonitor`；`ServiceMonitor` 改为 camelCase；`service-monitors.ts` 在边界映射；`service-monitors-page.tsx`、`status-page.tsx` 改消费 camelCase；新增映射器 vitest；两页面测试通过 |
| P1 轮询门控 | `0710-p1-poll-visibility` | 性能 | 中低 | 低 | 分支 `fix/p1-poll-visibility`；`use-alert-bell`、`status-page`、控制台自动刷新在 `document.hidden` 时跳过并监听 `visibilitychange` 恢复即拉一次；`npm run check` 通过 |
| P2 响应式间隔 | `0710-p2-refresh-interval-reactive` | 性能 | 低 | 低 | 分支 `fix/p2-refresh-interval-reactive`；自动刷新间隔来自响应式偏好，设置变更后定时器重建；无回归 |
| A1 状态拆分 | `0710-a1-split-console-data` | 架构 | 中 | 高 | 分支 `refactor/a1-split-console-data`；按域把 `useConsoleData` 的状态/轮询/操作下沉到各自 `*-context-provider`；`AppShellInner` 仅保留跨域 `SharedContext`；全量 `npm run check` 通过、行为无回归 |

---

## 2. 执行顺序（依赖与铺路关系）

1. **A2 先行**：一行级改动、零行为风险，立即恢复渲染隔离基线，并为后续拆分铺路。
2. **A3 与 P1 并行**：彼此独立，均为局部改动，配套补测试。
3. **P2 紧随**：依赖 A3 之后的控制台刷新逻辑稳定；与 P1 同属轮询治理，可一并。
4. **A1 最后**：大型重构，需在所有局部修复稳定后进行；逐域迁移（nodes → tasks → policies → alerts → integrations → sshKeys），每域独立可验。

> 子任务之间**非强依赖**（树位置不隐含依赖）。任何顺序或并行均可，只要各自 `npm run check` 通过。A1 建议在 A2 之后，因 A2 验证的渲染隔离是 A1 的目标基线。

---

## 3. 跨子任务集成验收（parent 关门条件）

- 所有 5 个子任务已 `archive`（状态 completed 并移入 `archive/`）。
- 5 个分支均已 squash merge 至 `main`，CI 全绿（`make check`：golangci-lint + eslint + 后端/前端 test + build + bundle-budget）。
- 合并后从 `origin/main` 同步本地 `main`，确认无遗漏自动化（Release Please / Docker 发布本任务不预期触发，需显式记录）。
- 前端 `npm run check` 在最终态仍通过；无新增 `any`、无新增 `as` 不安全断言、无新增 raw snake_case 进组件。

---

## 4. 范围与明确排除

- **范围**：上表 5 项；配套单测/集成测试；必要的 spec 回写（如 A3 需把"API 边界必须映射"规则写清到 `frontend/index` 或 `type-safety` spec）。
- **排除**：
  - 不改变既有后端契约（A3 仅前端映射，若后端字段重命名属独立后端任务）。
  - 不引入新状态库（Zustand/React-Query）——规范明确"无显式架构决策不加依赖"。
  - 不处理已在历史安全加固中关闭的项（CSRF/XSS/响应头）。
  - 不处理本次审查的"低/美化"项（Q1 `t as any` 窄化、Q2 dev 可观测、Q3 Provider compose）除非对应子任务顺带——可作为 A3 的附带清理。
- **非目标**：不重写 UI 视觉、不改动业务语义。

---

## 5. 风险与回滚

- 每个子任务独立分支 + 独立 PR → 单点失败可单独 revert，不影响其余。
- A1 重构最大风险点：`SharedContext` 的 `overview/fetchOverviewTraffic/fetchHealthIncidentTimeline` 为跨域派生物，必须保留在父壳；迁移其余域时不得误迁跨域数据。回滚点：每域迁移后跑 `npm run check` + 页面冒烟。
- 所有改动须过 `npm run check`（`web/` 全量门禁）方可合并。
