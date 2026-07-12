# PRD: P2 安全加固 ownership/secrets

## 问题范围

原审计覆盖 Overview ownership、演练字段权限、二次验证码、production metrics/Swagger 和授权 fail-closed。二次审查又发现 task 空过滤、共享策略演练源节点、沙箱节点、演练证据读取、历史明文脚本、代理 IP 和查询错误处理等同类缺口。

## 实际落地

| 领域 | 已实现契约 |
|---|---|
| Overview / metrics | overview、storage、traffic、backup health、node metrics 均按角色/owned 节点收敛；查询错误不再静默当空数据 |
| Task / policy | task 空过滤也按 owned 节点；create/update 目标节点必须 owned；共享 policy 的节点关联响应过滤 |
| Restore drill | 触发前同时拥有沙箱节点；源备份任务只从 owned 节点选择；latest evidence / confidence 读取同时满足源节点与沙箱节点 ownership |
| CAPTCHA | primary 与 second 开关独立；challenge 分别生成并一次性校验；缺 store 时 fail-closed；自由文本 legacy 字段不再提供假安全 |
| Secrets | `drill_pre_verify` / `drill_verify` / `drill_post_verify` 经 model hook 加密；启动时幂等迁移历史明文；非 admin 响应隐藏脚本 |
| Runtime | production metrics token 强制、Swagger 默认关闭、trusted proxy 显式 allowlist、CAPTCHA 端点限流 |
| Query quality | task-run traffic 查询增加 SQLite/PostgreSQL 对等索引；handler/provider 查询错误向上返回 |

## 验收矩阵

| 场景 | 预期 |
|---|---|
| operator 无 owned 节点 | 所有相关列表/聚合为空，不回退全局数据 |
| operator 仅拥有演练源或沙箱之一 | 不允许触发，也不暴露 drill evidence/task_run_id |
| shared policy 含 owned + unowned source | 仅从 owned source 选择备份任务 |
| 两个 CAPTCHA 开关四种组合 | 仅生成/显示/提交/校验启用通道 |
| CAPTCHA store 缺失或 challenge 重放 | 登录失败 |
| 非 admin 读取/更新 policy | 不见脚本明文，也不能覆盖或注入脚本 |
| 历史明文 drill script | 启动迁移为 `enc:v2:`；失败时拒绝继续启动 |
| production 弱 runtime 配置 | 配置加载或启动 fail-closed |

## 验证

- handler、model、task manager、bootstrap、router 与 config 回归测试覆盖关键拒绝路径。
- paired migration `000061_task_runs_traffic_indexes` 同时存在于 SQLite/PostgreSQL。
- `go test ./...`、`golangci-lint` 与 backend build 已通过；Trellis/spec 更新后须重跑。

## 状态

实现完成，位于 `fix/07-11-audit-p1-security`；工作提交后归档。
