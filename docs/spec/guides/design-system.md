# 控制台设计系统

[返回指南入口](README.md) · [前端合同](../frontend/README.md)

本文负责视觉语义、设计变量、动效和页面组合。变量实现位于 [index.css](../../../web/src/index.css)，Tailwind 映射位于 [tailwind.config.ts](../../../web/tailwind.config.ts)。修改现有系统时同步本文，不为单个页面创建平行设计系统。组件 API、类型安全和可访问性分别由对应前端合同维护。

## 视觉方向

息壤是信息密集的运维控制台，优先保证清晰、稳定和可信。浅色与深色主题都必须可用；通过 `bg-background`、`text-foreground`、`border-border` 等语义类适配主题，不在页面中手工切换固定颜色。

阴影保持克制和中性，浅色使用灰蓝色调、深色使用黑色调；终端范围以外不引入彩色或发光阴影。页面使用 `background` / `card` 表面，装饰背景仅限现有 `app-shell-bg::before` 点阵和 `bg-login-ambient` 登录背景。

`primary` 是品牌强调色，`accent-brand` 提供相同品牌色值。`success`、`warning`、`destructive`、`info` 表达状态，不得新增装饰性用途。当前登录渐变仍使用 `--success`，这是现存实现与该要求的差异，不构成新用法的依据。

## 设计变量

### 颜色

颜色变量以 HSL 通道值定义，通过 `hsl(var(--token))` 消费；阴影、尺寸和时长变量使用各自 CSS 值。浅色定义在 `:root`，深色在 `.dark`。下表记录实现对应的语义颜色合同；修改颜色时同步源码与本表，并重新验证对比度。[设计变量回归](../../../web/src/index-css.test.ts)检查深色语义值与文档一致。

| 变量 | 浅色值 | 深色值 | 职责 |
|---|---|---|---|
| `--background` / `--foreground` | `220 24% 97%` / `222 34% 13%` | `224 40% 5%` / `210 30% 94%` | 页面背景和正文 |
| `--card` / `--card-foreground` | `0 0% 100%` / `222 34% 13%` | `223 32% 8%` / `210 30% 94%` | 卡片和内容表面 |
| `--popover` / `--popover-foreground` | `0 0% 100%` / `222 34% 13%` | `223 32% 8%` / `210 30% 94%` | 菜单、提示和浮层 |
| `--primary` / `--primary-foreground` | `217 84% 43%` / `210 40% 98%` | `217 100% 62%` / `224 60% 6%` | 主要操作、链接 |
| `--secondary` / `--secondary-foreground` | `220 17% 93%` / `222 34% 13%` | `222 22% 14%` / `210 30% 94%` | 次级表面 |
| `--muted` / `--muted-foreground` | `220 17% 93%` / `220 10% 42%` | `222 22% 14%` / `215 18% 72%` | 弱化背景、说明文字 |
| `--accent` / `--accent-foreground` | `213 32% 91%` / `222 34% 13%` | `217 28% 16%` / `210 30% 94%` | 悬停与选中背景 |
| `--accent-brand` | `217 84% 43%` | `217 100% 62%` | 品牌标识与登录背景 |
| `--destructive` / `--destructive-foreground` | `0 72% 43%` / `210 40% 98%` | `0 80% 64%` / `224 60% 6%` | 错误、离线、删除 |
| `--success` / `--success-foreground` | `156 66% 32%` / `150 45% 96%` | `158 70% 48%` / `224 60% 6%` | 成功、健康、在线 |
| `--warning` / `--warning-foreground` | `36 88% 42%` / `42 45% 97%` | `38 95% 60%` / `224 60% 6%` | 警告与风险 |
| `--info` / `--info-foreground` | `199 87% 39%` / `200 52% 97%` | `199 95% 58%` / `224 60% 6%` | 信息提示 |
| `--border` / `--input` | `220 16% 86%` | `220 20% 18%` | 边框与输入框 |
| `--ring` | `217 84% 43%` | `217 100% 62%` | 焦点轮廓 |

`--chart-1/2/3`、`--chart-ingress`、`--chart-egress` 用于图表序列，不代表状态文字。导航与面板使用 `--nav-active`、`--nav-active-foreground`、`--shadow-panel`、`--shadow-panel-hover`、`--shadow-mobile-sheet`；这些变量独立定义，不能假定它们在所有主题中与 `accent`、`foreground` 数值相同。

### 字体与字号

无衬线字体依次为 `Inter Variable`、`Inter`、`PingFang SC`、`Microsoft YaHei`、系统字体；Inter 由 [main.tsx](../../../web/src/main.tsx) 导入。等宽字体配置为 `JetBrains Mono`、`monospace`。

优先使用 Tailwind 内置字号及以下项目字号；确有一次性需要时可使用 `text-[Npx]`，不要为单一场景新增不匹配的变量名称。不能通过负字号或随视口缩小文字回避布局问题。

| 类名 | 字号 / 行高 | 用途 |
|---|---|---|
| `text-micro` | 10px / 14px | 微型标签、来源标记 |
| `text-mini` | 11px / 16px | 统计说明、表头、脚注 |
| `text-nav` | 13px / 18px | 侧栏导航 |
| `text-stat` | 28px / 32px | 概览统计数字 |

### 间距、圆角与阴影

间距使用 Tailwind 默认比例及 `gap-*`、`p-*`、`m-*`，不要新增随意的像素间距。圆角默认 `--radius: 0.5rem`，`--radius-sm: 0.375rem`，`--radius-lg` / `--radius-xl: 0.5rem`；`rounded-xs` 是 `0.25rem`。阴影使用 `--shadow-sm/md/lg/xl`。

`html[data-density="compact"]` 将默认圆角设为 `0.3rem`，收紧 `.filter-panel` 内边距，把 `[data-density-gap="compact"]` 间距设为 `0.4rem`，并缩小表格单元格纵向内边距。不得通过改变字号代替密度控制。

## 动效与性能偏好

动效必须同时尊重操作系统减少动效偏好和应用 `powerMode=save`。[根入口](../../../web/src/main.tsx)中的 `MotionPreferenceBoundary` 必须位于 `ThemeProvider` 内；它用 `MotionConfig` 在省电时强制 `reducedMotion="always"`，其他模式使用 `"user"`。不得创建绕过该边界的独立动效区域。

时长变量为 `--duration-fast: 100ms`、`--duration-normal: 150ms`、`--duration-slow: 200ms`；缓动使用 `--ease-enter`、`--ease-exit`、`--ease-in-out`。已有工具类包括 `animate-fade-in`、`animate-slide-up`、`animate-slide-down`、`animate-in`、`animate-animate-in`、`animate-popover-in/out`、`animate-float`、`animate-spin`。

新增动效限制在 `transform`（含 `translate` / `scale`）、`opacity`、`filter`，禁止动画修改 `width`、`height`、`top`、`left`、`padding`、`margin` 等布局属性。不要把这种属性选择表述为浏览器必定使用 GPU 的保证。现有 `.card-lift` / `.filter-panel` 仍含颜色和阴影过渡，`shimmer` 关键帧使用 `background-position`，与上述限制存在差异；本次文档迁移不修改这些行为。

- `prefers-reduced-motion: reduce` 将动画和过渡时长降至 `0.01ms`，并关闭平滑滚动；加载反馈 `.animate-spin` 保留 1 秒循环。
- 当用户未要求减少动效时，`html[data-power="save"]` 将动画设为 `0.01ms` 且仅播放一次，过渡设为 `80ms`。省电模式关闭状态脉冲；背景点阵仍存在，基础规则将透明度设为 `0.35`，不是完全关闭背景。
- `.pulse-online` / `.pulse-warning` / `.pulse-offline` 使用成功、警告、错误色，执行 2.4 秒透明度与缩放呼吸动画；减少动效与省电模式均禁用它们。

## 状态与终端边界

| 状态 | 使用 | 禁止的固定调色板示例 |
|---|---|---|
| 在线、健康、成功 | `bg-success`、`text-success`、`border-success/30`、`bg-success/5` | `emerald-500` |
| 离线、错误、破坏性操作 | `bg-destructive`、`text-destructive`、`border-destructive/30`、`bg-destructive/5` | `red-500` |
| 警告 | `bg-warning`、`text-warning` | `amber-500` |
| 信息 | `bg-info`、`text-info` | `sky-500` |

禁止用固定十六进制、RGB 或 Tailwind 调色板值表达状态。未知、中性状态点可使用 `muted-foreground/30`。`panel-renderer.tsx` 的 `SERIES_COLORS` 固定颜色数组是现有图表序列例外，不得用于状态。

终端和日志流使用独立 `--terminal-*` 变量与 `.terminal-*` 工具类，允许终端自己的渐变、发光、标签和面板。这是限定范围的例外：不要将终端颜色替换为普通状态色，也不要把终端变量传播到普通页面。

## 页面组合

基础组件统一复用 `web/src/components/ui/`，具体规则见[组件合同](../frontend/component-guidelines.md)。常用组合为 `PageHero`、`StatCardsSection`、`DataSurface` 及其 Header / Content / Toolbar / Footer。卡片、徽章、按钮、输入、选择器、开关、分页与弹窗均复用现有组件，不在页面中另造基础组件。

```tsx
<div className="animate-fade-in space-y-5">
  <PageHero title={t("...")} subtitle={t("...")} meta={...} actions={...} />
  <StatCardsSection items={[...]} />
  <DataSurface>
    <DataSurfaceHeader title={...} description={...} actions={...} />
    <DataSurfaceContent>...</DataSurfaceContent>
  </DataSurface>
</div>
```

页面根可使用 `animate-fade-in`，主要区块可使用带延迟的 `animate-slide-up`，并尊重系统动效偏好。筛选区使用 `.filter-panel` / `.sticky-filter`，从 `md` 起吸顶。表格行使用 `border-b border-border` 和 `hover:bg-muted/30`，表头使用 `text-mini uppercase tracking-wide text-muted-foreground`。空、加载、错误状态分别复用 `EmptyState`、`LoadingState`、`InlineAlert`。

单页标题层级、`FormDialog` 的描述与原生提交语义、普通 `Dialog` 的可访问名称和描述以[可访问性合同](../frontend/a11y-guidelines.md)为准。`PageHero` 渲染 `h1`，`DataSurfaceHeader` 默认 `h2`；小页面可自行提供页标题。类型导入、API 映射、禁止不安全类型断言等规则见[类型安全](../frontend/type-safety.md)，语言切换见[可访问性语言规则](../frontend/a11y-guidelines.md#语言与语义)。不得通过过滤控制台或隐藏测试警告掩盖 Radix、Recharts 或浏览器问题；应修复缺失描述、图表尺寸等根因。
