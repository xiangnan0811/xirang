# 前端审查整改第二阶段

## Goal

继续在 `fix/frontend-audit-remediation` 分支内推进同一次前端审查的剩余整改，但本阶段只处理已经确认、可测试、不会扩成全局重构的前端问题：桌面节点表格每行操作按钮过密。

本阶段目标是降低 `/app/nodes` 桌面表格操作列的认知负担，同时保持所有原有节点操作可达、可键盘操作、可回归测试。

## Requirements

- 保持同一工作分支 `fix/frontend-audit-remediation`，不推送远端，不改写既有提交历史。
- 复用现有 `web/src/components/ui/dropdown-menu.tsx` Radix primitive；不得创建新的菜单/弹层基础组件。
- 桌面节点表格每行只保留少量主操作内联展示：测试连接、查看日志、手动备份、更多操作触发器。
- 将次级/低频/危险操作移动到更多操作菜单：Fleet Doctor、Web Terminal、文件浏览、编辑、迁移、删除。
- 条件操作必须保留现有权限/能力控制：Web Terminal 仅 admin，文件浏览仅 `canBrowseNodeFiles`，迁移仅存在 `onMigrate` 时展示。
- 删除操作必须在菜单内保持 destructive 语义和视觉分隔。
- 保持查看日志为真实链接语义，不能退化为按钮导航。
- 保持移动端卡片视图和移动端工具栏“更多”菜单行为不变。
- 先写失败测试，再做实现；不得删除或弱化现有测试。
- 不引入新外部依赖，不处理后端/API，不做全局视觉重设计。

## Acceptance Criteria

- [ ] `web/src/pages/nodes-page.test.tsx` 新增桌面行操作聚合测试，能证明主操作仍内联、次级操作进入菜单。
- [ ] `web/src/pages/nodes-page.test.tsx` 覆盖菜单中 Fleet Doctor、Web Terminal、文件浏览、编辑、迁移、删除这些操作入口仍可达。
- [ ] `web/src/pages/nodes-page.table.tsx` 使用现有 `DropdownMenu` primitive 聚合次级操作。
- [ ] `/app/nodes` 桌面表格中每行不再横向铺满 8 个图标按钮加 1 个文字按钮。
- [ ] 删除菜单项与其他菜单项有视觉分隔并使用 destructive 样式。
- [ ] 现有日志链接、Fleet Doctor、移动端更多菜单、测试连接失败提示等节点页现有测试继续通过。
- [ ] `cd web && npm run test -- src/pages/nodes-page.test.tsx` 通过。
- [ ] `cd web && npm run typecheck`、`npm run lint`、`npm run check` 通过。
- [ ] 浏览器 QA 覆盖 `/app/nodes` 的 desktop/tablet/mobile 宽度，确认桌面更多菜单可打开、移动端行为未回归。

## Out of Scope

- React Scan / react-grab / react-doctor 安装。上一阶段 `react-scan@0.5.7` 尝试会将 `eslint-plugin-react-hooks` 升到 `7.1.1`，触发 67 个既有代码 lint 错误，本阶段不再尝试。
- 大范围性能重构、React context 拆分、`useConsoleData` 架构改造。
- 任意覆盖率百分比追逐；只补与本阶段行为变化直接相关的测试。
- 未能稳定复现的测试 warning 清理。已知 warning 后续单独处理，不混入本阶段节点表格改造。
- 全局视觉现代化或重新设计页面布局。
- 后端、API、权限模型、生产账号/真实后端联调。

## Notes

- 第一阶段已本地提交：设计系统文档、`nav.more`、PWA meta、状态 token、overview tooltip、login/audit heading、SLO dialog description、dashboard chart sizing。
- 本阶段是同一次审查整改的第二阶段，继续使用同一分支，但用新的 Trellis task 管理剩余范围。
