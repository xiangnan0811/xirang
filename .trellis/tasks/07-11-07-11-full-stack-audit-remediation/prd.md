# PRD: 全仓前后端审计修复（parent）

> 来源：2026-07-11 对 backend / web / deploy 的全量审查，以及其后的多轮二次审查。
> 当前分支：`fix/07-11-audit-p1-security`。本任务负责范围校准与集成验收；不在本轮推送、建 PR 或合并。

## 1. 目标

- 修复审计确认的 P1/P2 安全、所有权、数据映射、国际化和异步状态问题。
- 接纳二次审查发现的同类缺陷，并把超出原范围的实际落地记录回 Trellis。
- 将大量未提交修改按依赖关系拆成可审查提交，再按 Trellis 顺序归档已完成子任务并记录 journal。
- 保留未完成的 P3 债务，避免为了“全部完成”而错误归档父任务。

## 2. 已确认的产品与安全决策

| 决策点 | 契约 |
|---|---|
| panel-query 非 owned `node_id` | 返回 403；空过滤也只能查询 owned 节点 |
| 双 CAPTCHA | 两个开关独立，均使用一次性 store-backed challenge；不接受自由文本占位 |
| Overview / task / drill | operator 按 owned 节点 fail-closed；演练同时校验源任务节点与沙箱节点 |
| 演练校验脚本 | 加密落库；仅 admin 可读写明文 |
| 生产 Swagger | 默认关闭，仅显式 `SWAGGER_ENABLED=true` 开启 |
| 生产 metrics | `METRICS_TOKEN` 必填且拒绝弱值/占位符 |
| 代理 IP | 默认不信任转发头，只信任显式 `TRUSTED_PROXIES` |
| 前端 wire DTO | snake_case 只存在于 API wrapper，组件消费 camelCase domain model |

## 3. 子任务与实际状态

| 子任务 | 实际覆盖 | 状态 |
|---|---|---|
| `b-p1-panel-query-ownership` | panel-query owned 过滤、空过滤、task metric 路径 | 实现完成，待提交/归档 |
| `b-p1-dev-env-hardening` | 显式开发环境、生产密钥/metrics/代理校验、bootstrap 日志 | 实现完成，待提交/归档 |
| `b-p2-ownership-and-secrets` | Overview/task/drill ownership、双 CAPTCHA、演练脚本加密与脱敏、Swagger/metrics | 实现完成，待提交/归档 |
| `f-p2-domain-mapping-wave1` | credentials/app credentials/node metrics/silences/auth DTO 映射 | 实现完成，待提交/归档 |
| `f-p2-i18n-nodes-detail` | 节点详情 i18n、未知/错误状态、共享 status poll | 实现完成，待提交/归档 |
| `f-p2-poll-and-error-states` | 可见性轮询、AbortSignal、SLO/Settings 错误与确认框 | 实现完成，待提交/归档 |
| `p3-quality-debt` | CAPTCHA 限流、SW 刷新、automation `any`、CSP 等 | 部分完成，保持 active |

## 4. 集成验收

- 后端：`go test ./...`、`golangci-lint`、build 全部通过。
- 前端：`env -u NODE_ENV npm run check` 通过（127 个测试文件、547 项测试）。
- 文档：`scripts/check-doc-freshness.sh` 与 `git diff --check` 通过。
- 安全回归覆盖：未授权/空 ownership、双 CAPTCHA 独立矩阵、生产弱配置、演练双端 ownership、敏感脚本落库与角色脱敏。
- 本次 Trellis/spec 变更后必须重新运行完整门禁；仅以最新输出作为 finish-work 证据。

## 5. 明确延期

- Middleware response envelope 全仓统一。
- panel editor 的 RAF/ref 优化。
- WebSocket 鉴权协议重构或 B-P2-8 的扩大实现。
- Argon2 per-deployment salt 迁移。
- 未被本次改动触及的历史 `log.Printf` 清理。
- PR、CI 监控、合并和 post-merge 发布监控（本轮不推送）。

## 6. 完成语义

- 工作提交完成后归档上表前六个子任务。
- `p3-quality-debt` 与父任务保持 active；父任务只有在剩余 P3 决策完成且未来 PR/merge 验收完成后才能归档。
