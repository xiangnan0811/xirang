# 前端审查整改

## Goal

将前端审查中已由源码、测试输出和浏览器采样确认的问题收敛为一批小范围整改，提升 xirang 控制台的设计系统治理、可访问性、国际化、语义化状态颜色、PWA 元信息和图表稳定性。整改必须保持现有产品结构，不做整体重设计，不引入后端变更。

## Requirements

- 补齐前端设计系统契约，基于现有 `web/src/index.css`、`web/tailwind.config.ts`、Radix/shadcn UI primitive 和页面组合模式形成 `DESIGN.md`，不得借机改视觉方向。
- 修复 `/app/more` 和移动端 More tab 显示原始 `nav.more` i18n key 的问题，补齐 zh/en 翻译。
- 修复浏览器关于 `apple-mobile-web-app-capable` 的弃用提示，保留 Apple meta，同时增加标准 `mobile-web-app-capable` meta。
- 将状态页和服务监控页中已确认的 `emerald-500`/`red-500` 固定 Tailwind 调色板用法替换为语义化 `success`/`destructive` token。
- 让概览页主机状态矩阵 tooltip 同时支持键盘焦点，不再只依赖 hover。
- 修复登录页 DOM 中存在两个 `h1` 的问题，保留单一主标题；修复审计页缺少页面级 `h1`/`PageHero` 的问题。
- 修复 SLO 表单对话框缺少描述导致的 Radix `DialogContent` 警告。
- 定位并修复 dashboard panel 图表在测试/浏览器中出现的 Recharts `width(-1)`/`height(-1)` 容器尺寸警告，不通过 suppress/console filter 掩盖问题。
- 对每类行为改动先补充可失败的聚焦测试，再做最小实现。
- 仅在确认生产构建不受影响的前提下，增加 dev-only React render observability；当前仅允许已验证安装方式的 `react-scan`，`react-grab`/`react-doctor` 先记录为后续建议。

## Acceptance Criteria

- [ ] `DESIGN.md` 存在并记录当前前端 tokens、字体、spacing/radius/shadow/motion、组件 primitive、页面组合和使用禁忌。
- [ ] `nav.more` 在中文和英文资源中都有翻译；`/app/more` 与移动端 More tab 不再显示原始 key。
- [ ] `web/index.html` 同时包含 `mobile-web-app-capable` 和 `apple-mobile-web-app-capable`。
- [ ] 已确认的状态颜色不再使用固定 `emerald-500`/`red-500` 类，改用语义 token，且有测试锁定。
- [ ] 概览页状态矩阵链接在键盘焦点下能关联并显示 tooltip 信息。
- [ ] 登录页只有一个 level-1 heading；审计页存在页面级 Audit heading。
- [ ] SLO 新建/编辑对话框包含可访问描述，相关测试不再产生 Radix missing description 警告。
- [ ] dashboard panel chart 测试不再产生 Recharts 0/-1 尺寸警告。
- [ ] 聚焦测试、`npm run typecheck`、`npm run lint`、`npm run test`、`npm run build`、`npm run check` 通过。
- [ ] 浏览器抽样至少覆盖 `/login`、`/status`、`/app/overview`、`/app/audit`、`/app/more`、`/app/service-monitors`，并确认本次修复项无回归。

## Out of Scope

- 不改后端接口、数据库、认证或 API contract。
- 不做整站视觉重设计，不重构节点表格密集操作区，不替换图表库。
- 不解决 demo 模式缺少后端导致的 `/api/v1/*` 500；仅关注前端展示和已确认的前端问题。
- 不添加未经验证安装/接入方式的 `react-grab` 或 `react-doctor`。

## Notes

- 该任务源自已完成的只读前端审查；本阶段已得到用户许可创建 Trellis 任务并执行整改。
- 当前工作分支：`fix/frontend-audit-remediation`，基础分支：`main`。
- 标准前端质量门禁：`cd web && npm run check`。
