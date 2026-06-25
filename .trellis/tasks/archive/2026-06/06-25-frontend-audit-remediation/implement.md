# 前端审查整改实施计划

## Ordered Checklist

1. 启动 Trellis 任务：`python3 ./.trellis/scripts/task.py start 06-25-frontend-audit-remediation`。
2. 添加根级 `DESIGN.md`，只记录现有设计系统事实。
3. 增加 i18n 和 PWA meta 的 RED 测试，再补 `nav.more` 和 `mobile-web-app-capable`。
4. 增加状态颜色 token RED 测试，再替换 `status-page.tsx`、`service-monitors-page.tsx` 的固定 palette 类。
5. 增加 a11y RED 测试，再修复 overview tooltip、login 单 `h1`、audit `PageHero`、SLO dialog description。
6. 增加 Recharts warning RED 测试，再修复 panel chart 容器尺寸。
7. 最后处理 `react-scan` dev-only 接入；若验证失败，回滚并记录 deferred。
8. 运行聚焦测试、LSP diagnostics、完整 `npm run check` 和浏览器抽样。

## Targeted Test Commands

```bash
cd web && npm run test -- web/src/i18n/index.test.ts web/src/components/layout/mobile-navigation.test.tsx web/src/pages/more-page.test.tsx web/src/index-html.test.ts
cd web && npm run test -- web/src/pages/status-color-tokens.test.ts web/src/pages/service-monitors-page.test.tsx
cd web && npm run test -- web/src/pages/overview-page.test.tsx web/src/pages/login-page.test.tsx web/src/pages/__tests__/login-page.a11y.test.tsx web/src/pages/audit-page.test.tsx web/src/pages/reports-page.slo.test.tsx
cd web && npm run test -- web/src/pages/dashboards/panel-card.test.tsx
```

## Full Validation

```bash
cd web && npm run typecheck
cd web && npm run lint
cd web && npm run test
cd web && npm run build
cd web && npm run check
```

After edits, run `lsp_diagnostics` on changed TypeScript/TSX files. Browser QA must cover `/login`, `/status`, `/app/overview`, `/app/audit`, `/app/more`, `/app/service-monitors`, and a dashboard chart route if data is available.

## Rollback Points

- Documentation (`DESIGN.md`, Trellis files) is independent.
- i18n/meta fixes are independent.
- token drift fixes are independent.
- a11y/semantic fixes are independent but should keep tests paired with source edits.
- Recharts sizing fix is independent.
- `react-scan` dependency and bootstrap change must remain a final isolated batch.

## Completion Gate

- All acceptance criteria in `prd.md` checked.
- No source suppressions (`as any`, `@ts-ignore`, console warning filters) introduced.
- Git status reviewed; no commit or push without explicit user request.
