# Xirang 项目入口

本文件是贡献者与编码代理的项目权威入口，适用于整个 checkout；子目录的
`AGENTS.md` 补充局部导航。开发合同统一位于 [docs/spec](docs/spec/README.md)。
个人 harness 可选，干净 checkout 本身即可找到全部项目要求。

## 按任务读取

先用 `git rev-parse --show-toplevel` 定位当前 checkout 根目录，再解析文档路径；
从 `backend/`、`web/` 或 worktree 进入时同样适用。多仓库任务分别确认各自权威。

| 任务 | 入口 |
| --- | --- |
| 开发环境、命令、hooks、分支与 PR | [贡献指南](CONTRIBUTING.md) |
| Go、API、数据库、迁移、运行时 | [后端合同](docs/spec/backend/README.md) |
| React、组件、Hook、状态、类型、可访问性 | [前端合同](docs/spec/frontend/README.md) |
| 凭据、任务、告警、节点日志、备份资产 | [领域合同](docs/spec/domains/README.md) |
| 架构、跨层设计、测试、文档维护 | [开发指南](docs/spec/guides/README.md) |
| 并行代理、独立审查、原生加载、候选证据 | [代理协作与验证](docs/spec/guides/agent-collaboration.md) |
| 部署、配置、使用、发布 | [文档总入口](docs/README.md) |

只加载受影响层和领域的相关合同。局部入口：
[API handlers](backend/internal/api/handlers/AGENTS.md)、[页面](web/src/pages/AGENTS.md)。

## 授权与适用范围

- 明确且已授权的请求直接执行；仅在事实与现有决策不能解决实质需求、风险或授权时询问。
- 只读请求始终只读，包括私有恢复状态与索引。不要为了分类创建任务、PRD、规划审批、
  例行日志或框架目录；Trellis 和 Superpowers 的旧生命周期已退役。
- 通用个人规则属于个人 harness；恢复记录和索引放在 Git 外，索引只保存指针。
  历史会话和旧记忆仅是线索，当前项目合同及当前证据优先，已完成或取消的工作不是活动任务。
- 合同变更、验证证据及交付遵循上表对应主文；本入口不复制技术细则或交付流程。
