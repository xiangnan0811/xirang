# 后端开发合同

本目录只承载跨领域的 Go、Gin、GORM 与部署运行时约定。业务持久化、API、前端表现、兼容和回归场景统一见[领域合同](../domains/README.md)。

| 文档 | 适用变更 |
| --- | --- |
| [目录与依赖](directory-structure.md) | 包组织、组合根、命名与新增模块 |
| [数据库](database-guidelines.md) | 查询、事务、迁移、降级保护与双引擎时间语义 |
| [错误处理](error-handling.md) | 响应封装、状态映射、限流及中间件拒绝 |
| [代码质量](quality-guidelines.md) | 安全边界、动态设置、路由退役与确定性测试 |
| [日志](logging-guidelines.md) | 结构化字段、脱敏、日志级别与队列过载 |
| [部署运行时](deployment-runtime.md) | 官方镜像、健康检查、持久化及进程生命周期 |

开发环境、命令、hooks 与 PR 操作见[贡献指南](../../../CONTRIBUTING.md)，后端源码导航及迁移版本见[后端说明](../../../backend/README.md)。
