# 前端审查整改设计

## Architecture And Boundaries

本次整改只触及 `web/` 前端、根级 `DESIGN.md` 和 Trellis 任务文档。所有用户可见行为继续通过现有 React 18、Vite、Tailwind、Radix/shadcn、i18next 和页面级测试体系实现。

页面结构保持现状：路由仍由 `web/src/router.tsx` 和 `web/src/router-pages.tsx` 管理，页面仍在 `web/src/pages/`，共享 primitive 仍来自 `web/src/components/ui/`。新增测试优先放在已有 colocated test 或最邻近的 `*.test.ts(x)` 文件中。

## Implementation Decisions

1. 设计系统契约使用根级 `DESIGN.md`，因为审查用 `DESIGN.md`/`design-system.md`/`design-tokens.md` 全仓搜索验证缺失。文档只提炼现有实现，不改变 token。
2. `nav.more` 在 zh/en 的 `nav` namespace 中补齐，值分别为 `更多` 和 `More`。`common.more` 保持 `更多操作`/`More actions`，用于操作菜单，不复用为导航标签。
3. PWA meta 仅补标准 `mobile-web-app-capable`，保留现有 Apple meta，避免 iOS 行为回归。
4. 状态颜色只替换已确认的状态页和服务监控页固定 palette 类；图表 hex、terminal hex 和其他审查中已判定合理的特殊场景不纳入。
5. 概览页矩阵 tooltip 通过 `aria-describedby`、稳定 id、`role="tooltip"` 和 `group-focus-within:opacity-100` 支持键盘焦点，不改变 hover 视觉。
6. 登录页保留营销区 `h1` 作为页面主标题，移动卡片内品牌标题降级为非 heading 文本，避免 DOM 多个 `h1`。
7. 审计页使用已有 `PageHero` 补页面级 heading，现有筛选、表格、导出和分页结构不重排。
8. SLO dialog 使用显式 `description` prop 和新增 locale 文案，不修改 `FormDialog` 的可选描述契约。
9. Recharts 警告通过尺寸约束修复，优先在 `PanelRenderer` 的 `ResponsiveContainer` 使用 `minWidth`/`minHeight`，必要时给 `PanelBody` 增加最小高度。禁止 console suppress。
10. 性能观测只接入已验证 Vite 文档的 `react-scan`。若安装或生产构建验证失败，回滚该批次并记录为后续建议。

## Data Flow And Contracts

- i18n 修复仅补资源键，不改 `setLanguage()` 或 i18next 初始化。
- 状态颜色修复只改变 Tailwind class，语义仍由现有 `status`/`last_status` 字段决定。
- tooltip 修复不新增状态，只增强 DOM 关联与 CSS 可见性。
- SLO dialog 描述通过 i18n 资源传入 `FormDialog`，由现有 `DialogDescription` 渲染 Radix 描述。
- React scan 必须 dev-gated，不应改变生产运行时行为。

## Deferred Items

- `react-grab` 和 `react-doctor`：未取得足够可靠的项目接入资料，暂不引入。
- 节点表格密集操作区：属于 UX 重设计，不纳入本次小范围修复。
- 全站 token 扫描迁移：只修复审查中确认的状态页面漂移。
- 后端和 demo 500：非本任务范围。

## Risks And Rollback

- `react-scan` 是唯一新增依赖，若 `npm install`、typecheck、build 或 production preview 出现异常，单独回滚该批次。
- 图表尺寸警告可能来自多个 chart surface；若初始假设不成立，先扩展定位测试，不做盲改。
- AuditPage 加 `PageHero` 可能影响快照/布局类测试；只断言语义 heading 和现有交互不变。
- 文档和代码批次保持可独立回滚，不提交、不推送，除非用户后续明确要求。
