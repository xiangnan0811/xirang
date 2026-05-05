# Wave 4 — 前端 a11y 全审（axe-core CI + WCAG AA）

## Goal

把 a11y（无障碍）作为持续质量维度纳入开发流程：接 axe-core 到 vitest CI 自动检测违规，接 eslint-plugin-jsx-a11y 静态层兜底，修一批已知的真违规（i18n lang 同步 / 装饰 icon aria-hidden / 颜色对比度），沉淀 a11y guideline spec 防回归。

## What I already know

经子代理实读 25 文件得出（详见 research/a11y-design.md）：
- **当前 a11y 覆盖率中上**：aria-* 文件覆盖 47%（112/236）、20 个 Dialog 全含 DialogTitle、70 个 icon 按钮抽样均有 aria-label、`<th scope="col">` 合规、app-shell 有 skip-link + `<main>` landmark
- **真违规集中三类**：
  - **P0 `<html lang>` 写死 `zh-CN`**（i18n 切换未同步，违反 WCAG 3.1.1/3.1.2）
  - **P1 装饰 lucide icon ~33 处缺 `aria-hidden`**（SR 重复朗读）
  - **P2 颜色对比度未量化**（待 axe 自动跑后定位）
- 无 playwright，无 axe 任意衍生；vitest@4 + jsdom + testing-library 已就绪
- 历史 a11y 改动：Wave 0 dialog 小屏（无 a11y 影响）、Wave 2 PR-D 加 sr-only DialogTitle + i18n 漏洞修复（与本 wave P0 同根因）

## Research References

- [`research/a11y-design.md`](research/a11y-design.md) — 25 文件实读 + 工具选型对比 + 4 PR 拆分（含具体文件清单 + 风险）

## Decision (ADR-lite)

**Context**：a11y 是 4 wave 累积下来唯一未做的质量维度。前端虽已有较强 a11y 意识，但缺自动化检测 → 历史债务难收敛、未来回归无网。

**Decision**：
- **CI 工具**：vitest-axe（与现有 vitest+jsdom 零摩擦）
- **Lint 兜底**：eslint-plugin-jsx-a11y（flat config）
- **不引入** playwright（重，与项目轻测试栈不符）
- **不引入** @axe-core/react（dev-only，价值低）
- **分阶段开启 jsx-a11y 规则**：PR-A warn → PR-D error（避免 CI 立刻红）
- **颜色对比度局部 patch**，不全局改 token（避免视觉回归）
- **react-grid-layout 拖拽 a11y 不根治**，仅加键盘替代按钮兜底（社区已知难题）

**Consequences**：
- 4 PR ≈ 11-13h，30-35 文件改动（绝大多数 attribute 增补）
- 引入 3 个新 devDeps：vitest-axe / axe-core / eslint-plugin-jsx-a11y
- bundle 不受影响（这些都是 devDeps）
- 后续新组件必须遵循新 spec，不然 CI 卡

## Requirements

### PR-A：脚手架（S, ~2h）
- [PR-A1] 新 devDeps：`vitest-axe` + `axe-core` + `eslint-plugin-jsx-a11y`
- [PR-A2] `web/eslint.config.js` 接入 jsx-a11y 推荐规则集（全部初期 `warn` 而非 `error`，先看清债务）
- [PR-A3] `web/src/test/setup.ts` 加 `expect.extend(toHaveNoViolations)`（vitest-axe matcher）
- [PR-A4] 新增 1 个 smoke 测试 `web/src/components/ui/__tests__/dialog.a11y.test.tsx`：render Dialog → 跑 axe → 应 0 violations
- [PR-A5] 新增 spec `.trellis/spec/frontend/a11y-guidelines.md`（8 条最小规范），并在 `.trellis/spec/frontend/index.md` 链入
- 验收：`npm run lint` 跑出 jsx-a11y warn（不强制 0），`npm run test` 含 1 个 axe smoke 通过

### PR-B：高优先级修复 P0+P1（M, ~3-4h）
- [PR-B1] **P0 i18n lang 同步**：`web/src/i18n/index.ts` 在 init 与 `setLanguage()` / `i18n.on('languageChanged', ...)` 中同步 `document.documentElement.lang`（zh → `zh-CN`，en → `en`）
- [PR-B2] **P1 select.tsx ChevronDown 加 `aria-hidden`**
- [PR-B3] **P1 version-banner 关闭按钮加 `aria-label` + sr-only**
- [PR-B4] **P1 装饰 lucide icon 全量 `aria-hidden`**（~6 文件 × ~33 处：ssh-key-actions-menu / tag-chips / bandwidth-schedule-editor / escalation-level-row / docker-volumes-panel / self-backup-panel）
- [PR-B5] 新增 a11y 测试：`version-banner.a11y.test.tsx` 兜底
- 工作量：~12-15 文件 / ~30 行（attribute 增补，零行为变更）

### PR-C：中优先级修复 P2（M, ~4-5h）
- [PR-C1] 新增 5 个主页面 a11y smoke 测试：overview / nodes / tasks / logs / login
- [PR-C2] 跑 axe 拿真实 violations 列表（不再凭猜测）
- [PR-C3] 颜色对比度按 axe fail 项**局部 patch**（如 `text-mini` 灰文本改 `text-xs text-foreground/70`），不动全局 token
- [PR-C4] react-grid-layout 拖拽配套加上下移按钮兜底（panel-editor-dialog）
- 工作量：colors 1-2 处局部 patch + ~5 页面测试 + 1 处兜底按钮

### PR-D：低优先级 + 收尾（S, ~2h）
- [PR-D1] PR-A 阶段降级 `warn` 的 jsx-a11y 规则升回 `error`（已修完债务的）
- [PR-D2] `.trellis/spec/frontend/a11y-guidelines.md` 补充 axe 测试模板 + 装饰 vs 语义 icon 判定规则 + i18n+lang 样板 + 豁免清单（react-grid-layout）
- [PR-D3] `web/src/test/a11y-helpers.ts` 抽 `runAxe(container)` 辅助
- [PR-D4] `.trellis/spec/frontend/quality-guidelines.md` 加 "提交前需通过 a11y 测试" 一条
- [PR-D5] 复审 app-shell skip-link + `<main>` landmark + desktop-sidebar `<nav>` 已合规

## Acceptance Criteria

- [ ] PR-A：`npm run check` 含 vitest-axe smoke 1 通过；`npm run lint` 不退化（jsx-a11y 警告允许，不强制 0）
- [ ] PR-B：`document.documentElement.lang` 切英文 UI 后变 `en`；select / version-banner / 6 文件装饰 icon 全部 `aria-hidden`；axe 不再报这几类违规
- [ ] PR-C：5 主页面 a11y smoke 测试全过；axe `color-contrast` 0 violations
- [ ] PR-D：`npm run lint` jsx-a11y 0 error；spec 链接通
- [ ] 全 wave：`cd web && npm run typecheck && npx vitest run --pool=threads && npm run build && node scripts/check-bundle-budget.mjs` ✓
- [ ] 后端不受影响（不动后端代码）

## Definition of Done

- 4 PR 各自独立 review、可回滚
- 每个 PR commit message 遵循 conventional commits（feat/fix/chore/docs）
- jsx-a11y 规则升级为 error（PR-D 末态）后，未来新组件违规 CI 直接拒
- a11y guideline spec 入库供未来贡献者参考

## Out of Scope

- 引入 playwright + @axe-core/playwright 做 E2E a11y（重，超 wave 范围）
- 引入 @axe-core/react dev-time runtime（dev-only 价值低）
- 全局色 token 改造（视觉回归风险高）
- react-grid-layout 拖拽 a11y 根治（社区已知难题，仅提供键盘按钮兜底）
- 改非违规组件 / 重写已合规组件
- 移动端无障碍审计（与 desktop a11y 不同标准，留独立 wave）

## Technical Notes

- 当前分支：`wave4-a11y-audit`（已基于 origin/main 创建）
- 任务目录：`.trellis/tasks/05-05-wave-4-a11y-axe-core-ci-wcag-aa/`
- 验证命令：
  - `cd web && npm run lint`
  - `cd web && npx vitest run --pool=threads`（注意 fork pool macOS 环境 tmp 权限问题）
  - `cd web && npm run typecheck`
  - `cd web && npm run build`
- 子代理 read budget 已用满 25 文件，PR-C 的具体 violations 在跑 axe 前不能完全预测
