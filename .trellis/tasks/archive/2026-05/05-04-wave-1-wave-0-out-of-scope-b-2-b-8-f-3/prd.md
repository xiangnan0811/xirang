# Wave 1 — 清理 Wave 0 Out-of-Scope 三项 (B-2 / B-8 / F-3)

## Goal

收尾 Wave 0 PRD 中明确推迟的 3 项加固，全部已经经过实读复核确认范围真实，本任务**只剩设计与实施**，不再做新一轮 finding 核验。

## What I already know

### B-2 file_handler 远程路径符号链接校验
- 文件：`backend/internal/api/handlers/file_handler.go:305-340` `validateNodePath()`
- 现状：用 `filepath.Clean + HasPrefix(root+"/")` 校验白名单，**不防御远程 symlink 逃逸**——节点上的 `/data/normal/link → /etc` 可让 `/data/normal/link/passwd` 通过校验
- Wave 0 子代理给的修复方向（本地 EvalSymlinks）技术上不可行（远程路径不能用本地 syscall）
- 仓库已用 `github.com/pkg/sftp@v1.13.10`，完整支持 `Lstat / ReadLink / RealPath`（research 已确认）

### B-8 时区混用
- 实测当前分布：140 处 `time.Now()` vs 30 处 `time.Now().UTC()`（共 170 处，~82% 用 Local）
- 热点文件（前 5）：`runner.go(29)` / `dispatcher.go(19)` / `ws/hub.go(8)` / `auth/jwt.go(6)` / `node_handler.go(6)`
- `database/database.go:64` 的 `gorm.Config` **没有设 NowFunc**——所以 GORM `CreatedAt/UpdatedAt` 自动填的是 Local 时区
- Wave 0 复核结论：无功能 bug（同绝对时刻，比较逻辑无错），仅一致性/可读性问题
- 若做：要决定改造范围（全量 vs 边界层）

### F-3 长列表虚拟化
- 当前现状：
  - `pages/logs/logs-viewer.tsx` — 已硬 cap 400+200 条
  - `pages/tasks-page.table.tsx` — 已 useClientPagination 分页
  - `pages/audit-page*.tsx` — 已分页
  - `pages/nodes-page.table.tsx` — 已分页 + 卡片视图切换
- Wave 0 复核：现有机制已缓解卡顿，无明显 hot 点
- 用户希望补虚拟化作为长期能力（不是为了修 bug，是为了未来扩展）

## Research References

- [`research/sftp-symlink-validation.md`](research/sftp-symlink-validation.md) — pkg/sftp RealPath/Lstat 用法 + 3 个方案 A/B/C
- [`research/react-virtualization-libs.md`](research/react-virtualization-libs.md) — tanstack/react-virtual (5.4 KiB) + react-virtuoso TableVirtuoso (18.9 KiB) 混用方案

## Open Questions

（已全部解决）

## Requirements

- **B-2（方案 A，工作量 M）**：`file_handler.go validateNodePath()` 改为：先用 `sftp.RealPath` 解析用户传入的 rawPath 拿到节点真实绝对路径；roots 也各自 RealPath 一遍（避免用户 BasePath 本身是 symlink 时永远拒绝）；然后对 RealPath 后的字符串做 `HasPrefix(root+"/") || ==root` 比对。补单元测试覆盖 symlink 逃逸场景。

- **B-8（全量 UTC 迁移，工作量 L）** —— 必须一次性闭环：
  1. `gorm.Config` 加 `NowFunc: func() time.Time { return time.Now().UTC() }`
  2. SQLite 连接串加 `_loc=UTC`；PostgreSQL DSN 加 `timezone=UTC`
  3. 新建迁移 000050（双轨）把所有 timestamp 列 `-8h`（按当前部署时区偏移；如果非 UTC+8 部署需要人工调整）
  4. 写一份**部署 runbook**（docs/migration-utc-cutover.md）覆盖：备份、停服、迁移、回滚、验证步骤
  5. 单元测试断言 NowFunc 与 loc=UTC 端到端写入读出一致；回归测试 LoginFailure.LockedUntil / Task.NextRunAt 等关键时间字段
  6. **本 wave 不在生产环境执行迁移** — 仅交付代码、迁移脚本、runbook，由用户人工选窗口在 staging→生产逐级演练

- **F-3（仅 logs-viewer，工作量 S）**：引入 `@tanstack/react-virtual` v3，把 `pages/logs/logs-viewer.tsx` 列表换成虚拟化容器；保持 autoscroll-to-newest 行为（**注意：filteredLogs 由 logs-page.tsx 按 logId 降序排列，新日志位于数组首位即视觉顶部，所以"吸附最新"= 滚到 top；用 `scrollToIndex(0, { align: 'start' })`**）；更新 `web/scripts/check-bundle-budget.mjs` 预算（实际 vite tree-shake 后 +0.13 KiB，预算上限提到 546 KiB 留余量）。新增 1-2 个组件级测试 + 必要的 jsdom 补丁（ResizeObserver stub + 容器 dimensions 模拟）。

## Acceptance Criteria

- [ ] **B-2**: 在 SFTP root 下创建 `link → /etc` 后访问 `link/passwd`，handler 拒绝并返回明确错误；同等条件下访问 root 下的合法符号链接（指向同 root 内）应通过
- [ ] **B-8**:
  - [ ] 单元测试断言 `time.Now()`（GORM NowFunc）返回 UTC
  - [ ] 单元测试模拟"老 Local 数据 + 迁移 -8h + 新 UTC 数据"场景，断言读出后所有 time.Time 对应同一绝对时刻
  - [ ] `docs/migration-utc-cutover.md` 包含 backup / 停服 / migrate / verify / rollback 五段
  - [ ] 迁移脚本本地 dry-run 通过（用临时 SQLite + 含 30 天历史数据的种子）
- [ ] **F-3**: logs-viewer 在 1000 条日志输入下 DOM 节点 ≤ 显示窗口数（10x），autoscroll-to-bottom 仍有效，无滚动跳动
- [ ] `cd backend && go test ./...` 全绿
- [ ] `cd web && npm run check` 全绿（含 bundle budget 调整后通过）

## Definition of Done

- 三项各自独立 commit / 可单独回滚
- B-2 的 SFTP 多 RTT 性能成本在文档中明确说明
- F-3 引入新 npm 依赖时同步更新 `web/scripts/check-bundle-budget.mjs` 预算（如需）
- B-8 改动如涉及 GORM 时间存储，需说明对历史数据的兼容性

## Out of Scope (explicit)

- 在生产环境执行 B-8 数据迁移（本 wave 仅交付脚本与 runbook，迁移由用户人工择窗口操作）
- F-3 给 tasks/audit/nodes 表格也加虚拟化（复核确认 cap+pagination 已缓解，无明显卡顿前不做）
- B-8 改造业务层 140 处 `time.Now()` 为 `time.Now().UTC()` 的代码美化（GORM 层 + DB loc 切换后已端到端 UTC，业务层显式 UTC 不再必要）
- 把 DB 列改为 `timestamp with timezone`（PG 专属功能，破坏双轨迁移对称；当前 timezone-naive + loc=UTC 已足够）

## Decision (ADR-lite)

**Context**：Wave 0 三项 Out-of-Scope 整改。B-8 在 brainstorm 中暴露了 "GORM NowFunc 切 UTC 会导致新旧数据混杂" 的隐藏复杂度；F-3 复核确认是 "为未来准备" 而非修 bug。

**Decision**：
- B-2 用 RealPath 方案 A（一次 RTT，最简单），不走 Lstat 逐级方案
- B-8 接受全量改造（NowFunc + loc + 历史数据迁移 + runbook），代码与脚本本 wave 交付，**迁移本身由用户操作**（不在 CI/PR 自动执行）
- F-3 仅给 logs-viewer 加虚拟化，其他 3 个列表保持现状

**Consequences**：
- B-8 是本 wave 最大的工程，工作量 L，风险来自迁移阶段，已通过 runbook 控制
- F-3 包体积 +5.4 KiB，需调整 bundle budget
- B-2 引入 +1 RTT 性能开销（LAN ~10ms，跨网 ~100ms），file_handler 吞吐略降，但仅在用户主动浏览文件时触发，可接受

## Implementation Plan (small PRs)

| PR | 内容 | 风险 | 测试 |
|---|---|---|---|
| PR-A | F-3 logs-viewer 虚拟化（含 bundle budget 调整 + 组件测试） | 低 | npm run check + 手动 1000 条日志压测 |
| PR-B | B-2 RealPath 路径校验（含 SFTP fixture 单测） | 中 | go test + 手动 symlink 逃逸尝试 |
| PR-C | B-8 NowFunc + loc=UTC + 迁移 000050 + runbook | **高** | go test + dry-run 迁移在临时 SQLite + 文档 review |

PR-A → PR-B → PR-C 串行（PR-C 前两个完成后再做，避免被 B-8 复杂度拖慢前两个简单项）。

## Technical Notes

- 当前分支：`wave1-out-of-scope-cleanup`（已基于 origin/main 创建）
- 任务目录：`.trellis/tasks/05-04-wave-1-wave-0-out-of-scope-b-2-b-8-f-3/`
- 验证命令：`cd backend && go test ./...` ; `cd web && npm run check`
