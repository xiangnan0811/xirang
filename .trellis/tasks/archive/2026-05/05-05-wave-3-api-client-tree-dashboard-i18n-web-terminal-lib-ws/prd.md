# Wave 3 — 前端架构收敛

## Goal

收尾 Wave 2 frontend-audit 5 项 P3 finding（F-5/F-8/F-9/F-10/F-11），让前端架构在 API 风格、组件不可变性、错误处理、WebSocket 抽象层四个维度统一收敛。本 wave 仅做架构整理，不引入新功能。

## What I already know

- 5 项 finding 均经 Wave 2 实读复核确认范围真实
- Wave 3 子代理已对 5 项做"实读 + 设计"研究（research/architecture-design.md），并修正了 3 项 Wave 2 finding 细节
- 当前分支：`wave3-frontend-architecture`（已基于 origin/main 创建）

## Research References

- [`research/architecture-design.md`](research/architecture-design.md) — 4 个 PR 实施设计（含当前代码片段、目标代码片段、文件清单、风险点）

## Decision (ADR-lite)

**Context**：Wave 2 frontend-audit 留下 5 项 P3 项未做，全部属于"现有代码能跑但架构不规范"。前端继续叠加新功能前先收敛风险。

**Decision**：
- F-10 Tree mutate 用 `useState<Set>` 外提（非 useReducer，最小改动）
- F-11 dashboard 404 判定走 `ApiError.status === 404`（前端单边修，**不需要改后端**——经实读 backend 已返 HTTP 404 + ApiError.status 已存在）
- F-8 + F-9 合并：7 个裸函数文件转工厂模式 + 全 GET endpoint 加 `options.signal`
- F-5 web-terminal 抽 `lib/ws/reconnecting-socket.ts`，logs-socket + web-terminal 都迁移；web-terminal 重连后 `terminal.clear()` + 显式提示用户重新登录（SSH PTY 无法跨重连复用）

**Consequences**：
- 4 个 PR 串行（顺序：A→B→C→D，从最小改动到最大改动）
- F-5 web-terminal 用户体验：重连后 PTY 失效 → 必须重新登录，比 logs 重连体验差，但 SSH 协议固有限制
- F-8 调用方迁移涉及 ~23 个文件，但单 PR 可控（codemod 风格 sed 即可）
- 暂不做 Wave 3 P2/P3 之外的扩展（如全量 React 19 升级 / framer-motion 替换 / 虚拟化扩到 tasks 表格）

## Requirements

### PR-A：F-10 Tree expand 状态外提（最小改动，最先做）
- [PR-A1] `web/src/components/ui/tree.tsx` 把 `handleToggle` 内的 mutate 改为 `useState<Set<string>>` + `setExpanded(new Set(prev).add/delete(id))`
- [PR-A2] `item.children` 字段不再被修改（保持纯数据）
- [PR-A3] 加单元测试：模拟 toggle expand → 断言 children 引用不变 + expanded set 正确
- 工作量 S（~30 行 + 1 测试，无业务调用方）

### PR-B：F-11 dashboard 404 判定改用 ApiError.status
- [PR-B1] `web/src/pages/dashboard-detail-page.tsx` 找出 `error.includes(t(...))` 行
- [PR-B2] 改为 `error instanceof ApiError && error.status === 404`
- [PR-B3] 与 i18n 解耦后切英文 UI 也能正确判 404
- 工作量 S（~10 行）

### PR-C：F-8 + F-9 API client 统一为工厂模式 + 全 GET 支持 AbortSignal
- [PR-C1] 7 个裸函数文件转工厂模式：`users-api.ts` / `system-api.ts` / 其他 5 个（详见 research/architecture-design.md 转换清单）
- [PR-C2] 所有 GET endpoint 签名统一为 `(token, options?: { signal?: AbortSignal })`，options.signal 转发给 `fetch`
- [PR-C3] 调用方迁移（~23 个文件）：grep 旧 `import { listUsers }` 等裸函数 → 改用 `useUsersApi()` 或 `createUsersApi()`
- [PR-C4] 测试 mock 适配：从 `vi.mock("@/lib/api/users")` 改为 `vi.spyOn(apiClient, "listUsers")`（写法差异较大，但调用方测试基本不动）
- [PR-C5] 不破坏现有公开行为，仅风格统一
- 工作量 M-L（7 + ~23 + 6 测试 = ~36 文件）

### PR-D：F-5 web-terminal 抽 lib/ws 通用层
- [PR-D1] 新建 `web/src/lib/ws/reconnecting-socket.ts`（~220 行）：
  - exports: `ReconnectingSocket` class（OOP）或 `useReconnectingSocket` hook
  - 行为：base 2.5s / max 30s / 20 retries / jitter（与 logs-socket 现状对齐）
  - heartbeat：每 25s 发 ping，60s 无 pong 视为死连接 → reconnect
  - token refresh：onclose code 4401（自定义"token 过期"）触发 callback 刷 token 后重连
  - URL strategy：支持 `url: string | (() => string)` 回调（保留 logs-socket 的 candidate URL 切换能力）
- [PR-D2] `web/src/components/web-terminal.tsx` 重写连接逻辑用新抽象
  - reconnect 后：`xterm.clear()` + 显示提示 "会话已重连，请重新登录" + 重置 SSH session
- [PR-D3] `web/src/lib/ws/logs-socket.ts` 迁移到新抽象，保持现有公开行为（外部 import 不变）
- [PR-D4] 测试：reconnect on close、heartbeat trigger、token refresh、URL callback 切换
- 工作量 M（新增 1 个，修改 2 个 + N 测试）

## Acceptance Criteria

- [ ] **PR-A**：Tree 单元测试断言 `prev.children === next.children`（引用相等）；现有调用方（grep `<Tree`）使用 props 不变
- [ ] **PR-B**：dashboard 不存在时切英文 UI 仍正确显示 "Not found" 而非通用错误；不再 `error.includes(t())`
- [ ] **PR-C**：所有 `lib/api/*.ts` 都用 `createXxxApi()` / `useXxxApi()` 风格；grep `^export async function` in `lib/api/` 应无结果（除非内部 helper）；GET 调用都接 `options.signal`
- [ ] **PR-D**：手动测试：登录后打开 web-terminal → 关 wifi 30s → 开 wifi → 自动 reconnect 显示"会话已重连"提示；logs-viewer reconnect 行为不变
- [ ] 所有 PR：`cd web && npm run typecheck && npx vitest run && npm run build && node scripts/check-bundle-budget.mjs` ✓
- [ ] bundle 预算不显著上涨（PR-D 抽象层 ~220 行新代码，但 logs/web-terminal 各自代码减少，净影响可能 ≈ 0）

## Definition of Done

- 4 个 PR 各自独立 review、可回滚
- 每个 PR commit message 遵循 conventional commits（refactor / fix）
- PR-D 高风险变更（重连体验、xterm 状态）PR description 明确手动测试 checklist
- 不破坏 v0.19.5 已发布的任何用户可见功能

## Out of Scope

- React 19 / Router 7 升级（P3，breaking change 多）
- framer-motion 133KB 替换（P3，仅 3 处使用但需筛轻量库）
- i18n 4833 行 lazy load（P3，需重构 i18next 加载策略）
- 虚拟化扩到 tasks/audit/nodes 表格（已被 Wave 1 用 cap+pagination 缓解）
- web-terminal 真正的 SSH session 持久化（需要 backend 协议改造，超 Wave 3 范围）
- 全量 a11y 审查（建议作为 Wave 4 单独题目）

## Technical Notes

- 当前分支：`wave3-frontend-architecture`
- 任务目录：`.trellis/tasks/05-05-wave-3-.../`
- 验证命令：
  - `cd web && npm run typecheck`
  - `cd web && npx vitest run`
  - `cd web && npm run build`
  - `cd web && node scripts/check-bundle-budget.mjs`
- Wave 2 finding 编号修正记录：F-8 "files-api 内部不一致" 不成立、F-8 裸函数 7 个非 8 个、F-10 Tree 无业务调用方、F-11 不需后端配合
