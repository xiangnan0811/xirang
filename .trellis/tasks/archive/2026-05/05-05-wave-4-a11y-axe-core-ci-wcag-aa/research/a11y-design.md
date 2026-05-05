# Wave 4 a11y 全审 - 实施设计

- **Date**: 2026-05-05
- **Scope**: 内部代码扫描 + 工具选型 + 4-PR 拆分
- **Read Budget**: 25 文件已用满

---

## 1. 当前现状

### 1.1 a11y 覆盖率扫描（实读）

| 指标 | 数值 | 备注 |
|---|---|---|
| 主页面 (`pages/*.tsx`) | 85 | 含子模块 (logs/, dashboards/, …) |
| 组件 (`components/*.tsx`) | 117 | 含 ui/ 33 个、layout/ 4 个 |
| `aria-*` 出现的文件数 | 112 / 236 (~47%) | 文件覆盖率 |
| `aria-*` 出现总次数 | 373 | 平均每文件 3 处 |
| `role=` 出现总次数 | 64 | 含 role="tab/tablist/group/alert/toolbar" |
| `sr-only` / `VisuallyHidden` 文件数 | 9 | dialog 关闭按钮 + skip-link 等 |
| `<Dialog ` 使用文件 | 20 | 全部含 `DialogTitle` ✅ 0 处违规 |
| `size="icon"` 按钮总数 | 70 | 抽样验证：68 个均有 aria-label（grep 跨行漏报） |
| Radix UI 一致性 | 高 | `@radix-ui/react-{alert-dialog,dialog,checkbox,dropdown-menu,select,switch,label,slot,separator}` 全部接入 ui/ 层 |
| `<table>` + `<th scope="col">` 结构 | 21 处 table，nodes-page 等抽样均合规 ✅ |

**整体定性：覆盖率不错（中上）**。开发者已有较强 a11y 意识，主要违规集中在「i18n 语言同步」「细节 sr-only 缺失」「色彩对比未量化」三类。

### 1.2 组件 ui/ 层质量

实读结果：
- `components/ui/dialog.tsx`：Close 按钮带 `<X aria-hidden />` + `<span className="sr-only">Close</span>` ✅
- `components/ui/button.tsx`：focus-visible ring 完备，loader 图标带 `aria-hidden` ✅
- `components/ui/input.tsx`：支持 `aria-invalid`，但**未提供 `aria-describedby` slot**（业务侧自行拼接 errorId）
- `components/ui/select.tsx`：原生 `<select>`，可访问性天然 OK；`ChevronDown` 装饰**未加 `aria-hidden`**

### 1.3 抽样违规清单（按优先级）

| 优先级 | 类型 | 位置 (文件:行) | 修复方向 |
|---|---|---|---|
| **P0** | `<html lang>` 不随 i18n 切换 | `web/index.html:2` 写死 zh-CN；`web/src/i18n/index.ts` 无 `document.documentElement.lang = ...` | 在 i18n init 与 `setLanguage()` 中同步 lang 属性（WCAG 3.1.1/3.1.2） |
| **P1** | `Input` 装饰图标缺 `aria-hidden` | `web/src/components/ui/select.tsx:27` ChevronDown | 加 `aria-hidden` |
| **P1** | 装饰性 lucide icon 大量未声明 `aria-hidden` | `components/ssh-key-actions-menu.tsx:71/100`、`tag-chips.tsx:50` 等 ~33 处 | 装饰图标统一加 `aria-hidden`，避免读屏重复朗读 |
| **P1** | `version-banner.tsx:85` 关闭按钮 `<X />` 无文案 | `components/version-banner.tsx:80-90` | 加 `aria-label={t('common.close')}` + sr-only |
| **P2** | `pages/dashboards/panel-editor-dialog.tsx` 拖拽 grid 无键盘替代 | `react-grid-layout` 默认依赖鼠标 | 提供「上移/下移」按钮兜底 |
| **P2** | 颜色 token 对比度未量化 | `index.css` 中 `--muted-foreground` `--terminal-muted` 等多档 alpha | 用 axe-core color-contrast 自动跑 |
| **P2** | 部分 `text-mini` 12px 灰文本 | `pages/overview-page.tsx:253/259` 等 25 处 | axe 跑后按 fail 项修；不要全局改 |
| **P3** | landmark `<main>` 已有 (`#main-content`)；skip-link 已有 (`app-shell.tsx:84`) | — | 无需新增，PR-D 仅做收尾审查 |

> 已知历史：Wave 2 PR-D F-1 已为 `command-palette` 加 sr-only DialogTitle；F-2/F-3/F-4 是 zh→en 切换 i18n 漏洞，与本任务 P0「html lang 同步」属同一根因，可一起修。

---

## 2. 工具选型

### 2.1 对比表

| 工具 | 类型 | 集成成本 | 覆盖范围 | 维护负担 | 推荐 |
|---|---|---|---|---|---|
| **vitest-axe** | 单元/组件测试时跑 axe | 低（已有 vitest 4 + jsdom 26 + testing-library） | 单组件 + 局部页面（不含 portal CSS 计算受限） | 低 | ✅ CI 必装 |
| **eslint-plugin-jsx-a11y** | 静态规则 | 低（已有 eslint 9 flat config） | 静态可推断的 JSX 模式（无 alt、无 label、role 错配等） | 低 | ✅ Lint 兜底 |
| **@axe-core/playwright** | 真浏览器 E2E | **高**（需新增 playwright + headless chromium ~150MB） | 完整页面（含 portal、CSS contrast） | 高 | ❌ 不引入 |
| **@axe-core/react** | dev runtime warning | 低 | 浏览体验时观察 | 不算自动化测试 | ⚠️ 可选，PR-D 文档提一下 |

### 2.2 推荐组合

- **CI**：`vitest-axe` — 与现有 vitest + jsdom 零摩擦；用 `@axe-core/playwright` 会引入 playwright/chromium，与「Vitest + jsdom」一脉的轻测试栈不符。
- **Lint**：`eslint-plugin-jsx-a11y`（flat config 模式）— 装饰图标缺 aria-hidden、`<img>` 缺 alt、`<a>` href 等常规违规直接静态拦截。
- **不引入**：playwright（重，重复维护一套 E2E）、@axe-core/react（dev-only，不强制）。

### 2.3 已有依赖确认

`web/package.json` 现状：
- `vitest@4.0.18` + `@vitest/coverage-v8@4.1.4` ✅ 直接可挂 vitest-axe
- `@testing-library/react@16.3.0` + `@testing-library/user-event@14.6.1` ✅ render → axe(container)
- `eslint@9.39.4` flat config ✅ 直接 push plugin
- 无 playwright，无 axe-core 任意衍生

---

## 3. 4 PR 实施方案

### PR-A：脚手架 (S, ~2h)

**目标**：把 a11y 自动化纳入 `npm run check`，不改业务代码。

**文件清单**：
- `web/package.json` — 新增 devDeps：`vitest-axe`, `axe-core`, `eslint-plugin-jsx-a11y`
- `web/eslint.config.js` — 注册 `jsx-a11y` 推荐规则集；按需把高频但不易满足的规则降级 `warn`（如 `label-has-associated-control`，因部分 wrapping label 模式）
- `web/src/test/setup.ts` (若不存在则新建) — `expect.extend(toHaveNoViolations)`
- `web/src/components/ui/__tests__/dialog.a11y.test.tsx` (新增 1 个 smoke) — render Dialog + 跑 axe，确保流水线绿
- `.trellis/spec/frontend/a11y-guidelines.md` (新增) — 8 条最小规范：装饰 icon 加 aria-hidden、icon 按钮加 aria-label、表单 input 必有 label/aria-label、Dialog 必含 DialogTitle、html lang 跟随 i18n、对比度 ≥ 4.5、focus-visible ring 不可去除、tablist/tab/tabpanel 配套
- `.trellis/spec/frontend/index.md` — 链入新 guideline

**验收**：
- `npm run lint` 跑出 jsx-a11y 警告（不强制 0 警告，先看清债务）
- `npm run test` 包含 1 个 axe smoke 用例并通过
- `npm run check` 不退化

**关键风险**：jsx-a11y 推荐集若全开会暴露大量 warn，PR-A 阶段需要 **temporarily 把噪音规则降级为 warn**，PR-B/C 收敛后再升 error，避免 CI 直接红掉阻塞合并。

---

### PR-B：高优先级修复 (M, ~3-4h)

**目标**：修 P0 + P1 真违规，不动样式。

**文件清单（实读统计 ~12-15 文件）**：

P0：
- `web/src/i18n/index.ts` — `setLanguage()` 与 init 时同步 `document.documentElement.lang = lng === 'zh' ? 'zh-CN' : 'en'`
- `web/index.html` — 保持 zh-CN 兜底即可（i18n init 会覆盖）

P1：
- `web/src/components/ui/select.tsx:27` — `ChevronDown` 加 `aria-hidden`
- `web/src/components/ui/button.tsx` — 复核 Loader2（已合规可不改）
- `web/src/components/version-banner.tsx:80-90` — 关闭按钮加 aria-label + sr-only
- `web/src/components/{ssh-key-actions-menu,tag-chips,bandwidth-schedule-editor,escalation-level-row,docker-volumes-panel,self-backup-panel}.tsx` — 全量 lucide icon 加 `aria-hidden`（icon 在含文案的 Button 内时也应加，避免 SR 重复读）
- `web/src/components/__tests__/version-banner.a11y.test.tsx` (新增) — 兜底测试

**工作量**：约 12-15 个文件、~30 行改动，全是 attribute 增补，零行为变更。

**关键风险**：
- icon `aria-hidden` 改动若误把"承担可访问名"的图标也藏掉会回退；逐个判断「按钮是否有可见文案 / aria-label」再决定。
- i18n lang 同步若没在 init 阶段执行，首次渲染期间 SR 仍按 `zh-CN` 读 en 内容，需在 i18n.use 链路里 `i18n.on('languageChanged', ...)` hook。

---

### PR-C：中优先级修复 (M, ~4-5h)

**目标**：跑完整 axe → 按 violations 列表修。

**预期文件清单（基于扫描预判，确切以 axe 报告为准）**：
- `web/src/index.css` — 若 axe 报 `color-contrast` fail：调整 `--muted-foreground`、`--terminal-muted` 的 hsl 值，或限定 fail 处用更高对比度类
- `web/src/pages/overview-page.tsx`、`pages/logs/logs-page.tsx`、`pages/nodes-page.tsx` 抽样 3-5 处 `text-mini` 灰文本：缩小使用面或改 `text-xs text-foreground/70`
- `web/src/pages/dashboards/panel-editor-dialog.tsx` — react-grid-layout 拖拽配套加上下移按钮（仅当 a11y 配置有 expectations）
- 新增 axe 测试：每个主页面（overview / nodes / tasks / logs / login）一个 smoke render → axe

**工作量**：colors 调整 1-2 处 token、~5 个页面 axe smoke 测试、~3 处对比度局部 patch。

**关键风险**：
- 调全局 color token 易引起视觉回归；优先用「局部更深一档」而非全局改。
- react-grid-layout 拖拽 a11y 是已知社区难题，本 PR 仅给「上移/下移按钮」兜底，不要尝试重写。

---

### PR-D：低优先级 + 收尾 (S, ~2h)

**目标**：把 a11y 纳入 CI 门槛 + 文档收口。

**文件清单**：
- `web/eslint.config.js` — 把 PR-A 阶段降级的 jsx-a11y 规则升回 `error`（已修完债务的）
- `.trellis/spec/frontend/a11y-guidelines.md` — 补充：
  - axe-core 测试编写模板
  - 「装饰 icon vs 语义 icon」判定规则
  - i18n + lang 同步样板
  - 已知豁免清单（如 react-grid-layout 拖拽）
- `web/src/test/a11y-helpers.ts` (新增) — 抽出 `runAxe(container)` 辅助
- `web/README.md` 或 `.trellis/spec/frontend/quality-guidelines.md` — 加入「提交前需通过 a11y 测试」一条
- 复审 `app-shell.tsx` 已有 `#main-content` skip-link、`<main>` landmark，确认 desktop-sidebar 含 `<nav>` ✅，无需新增

**验收**：
- `npm run lint` 0 jsx-a11y error
- 所有主页面 axe smoke 测试 pass
- spec 文件完整、链接通

**关键风险**：低。

---

## 4. 总结

- **总工作量**：S + M + M + S ≈ 11-13h（约 2 个工作日）
- **总 PR 数**：4
- **总改动文件量预估**：PR-A ~5 + PR-B ~12-15 + PR-C ~8-10 + PR-D ~5 = **30-35 文件**，绝大多数为 attribute 增补与 spec 文档
- **重点风险**：
  1. jsx-a11y 全量开启会暴露大量历史债务，必须分阶段升级 warn→error，否则 CI 立刻红
  2. i18n lang 同步若漏掉首次渲染窗口期，SR 短暂错读
  3. 颜色对比度调整易引起视觉回归，必须局部 patch 而非全局改 token
  4. react-grid-layout 拖拽 a11y 不要尝试根治，仅提供键盘替代按钮

## Caveats / Not Found

- 未实读 `pages/dashboards/panel-editor-dialog.tsx`（read budget 用满），P2 风险评估基于历史经验
- axe-core 实际 violations 列表只能在 PR-C 跑后确定；本设计 PR-C 文件清单为预判
- 未量化主题色对比度具体数字（需 axe 跑后给）
- iframe / WebSocket terminal (`xterm.js`) 的 a11y 不在 WCAG AA 强制范围内（终端模拟器 SR 体验在业界普遍豁免），本设计未涵盖
