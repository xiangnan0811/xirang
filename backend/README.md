# Xirang 后端

基于 Go、Gin、GORM 的服务器运维服务，提供节点与 SSH 管理、备份和任务调度、恢复演练、监控告警、审计、WebSocket 终端及备份资产能力。依赖版本以 [go.mod](go.mod) 为准。

## 开发与合同

- [贡献指南](../CONTRIBUTING.md)：开发环境、启动、检查、hooks 与 PR 流程。
- [后端通用合同](../docs/spec/backend/README.md)：目录、数据库、错误、质量、日志与运行时。
- [领域合同](../docs/spec/domains/README.md)：凭据、任务恢复、健康和备份资产的持久化、API 和回归要求。
- [环境变量](../docs/env-vars.md)与[部署指南](../docs/deployment.md)：配置和运维。

## 源码导航

| 入口 | 职责 |
| --- | --- |
| [cmd/server/main.go](cmd/server/main.go) | 服务启动及依赖装配 |
| [internal/api/router.go](internal/api/router.go) | 真实路由与中间件注册 |
| [internal/api/handlers](internal/api/handlers/AGENTS.md) | 请求绑定、领域服务调用及响应 |
| [internal/model/models.go](internal/model/models.go) | 模型分领域文件索引 |
| [internal/model/node.go](internal/model/node.go) | Node 模型、GORM tags、脱敏方法与模型 hooks 示例 |
| [internal/database/database.go](internal/database/database.go) | SQLite/PostgreSQL 连接与时间语义 |
| [internal/database/migrator.go](internal/database/migrator.go) | 内嵌 SQL 迁移与启动准入 |
| [internal/settings/service.go](internal/settings/service.go) | 动态设置注册、覆盖和缓存 |
| [internal/task/manager.go](internal/task/manager.go) | 任务执行与生命周期 |
| [internal/taskstats/taskstats.go](internal/taskstats/taskstats.go) | 历史任务统计查询与聚合 |
| [internal/backupasset/runtime/runtime.go](internal/backupasset/runtime/runtime.go) | 备份资产组合根 |

API 以 Router 和生成的 OpenAPI 为准，本页不手工维护接口或模型全集。备份资产默认关闭，是否能够启用遵循领域就绪合同；可选 Worker 的缺席不等同于 Core 故障。

## 数据库迁移版本

当前迁移版本：`000090_backup_focus_retirement`。

该迁移的 SQLite/PostgreSQL 配对文件为：

- `internal/database/migrations/sqlite/000090_backup_focus_retirement.up.sql`
- `internal/database/migrations/sqlite/000090_backup_focus_retirement.down.sql`
- `internal/database/migrations/postgres/000090_backup_focus_retirement.up.sql`
- `internal/database/migrations/postgres/000090_backup_focus_retirement.down.sql`

`000090_backup_focus_retirement.up.sql` 是不可逆的职责收敛迁移：只删除已退役的节点资源采样、节点系统日志、可配置看板及其专用列/设置和磁盘预测字段；备份资产及其 Provider 元数据、任务与运行历史、任务日志、审计和告警投递事实不由该迁移删除。它在删除来源表/事件前先封存退役来源告警 ID；退役告警行、升级历史和已发送投递证据保留，未发送投递被标记为 `unknown`/`feature_retired` 语义并加围栏，不能重放。

`000090_backup_focus_retirement.down.sql` 明确失败，不是回滚方案。迁移驱动在执行 down 前写入旧版本号的行为也受 `schema_migrations` 保护器拦截；降级失败后版本号、dirty 状态和 schema 必须保持新版本的 clean 状态。不可逆升级只能通过升级前已验证的数据库备份，配套旧版二进制、`DATA_ENCRYPTION_KEY` 及适用的历史密钥恢复，不能手改迁移元数据或绕过保护器。

该声明与 `internal/database/migrations/{sqlite,postgres}` 中最新的成对 up/down 文件同步，并由迁移检查脚本校验。升级前提、旧写入者排空、双引擎恢复和降级保护要求见[数据库合同](../docs/spec/backend/database-guidelines.md)、[部署指南](../docs/deployment.md#备份职责收敛升级)及[备份、恢复与快照](../docs/admin/backup-recovery.md#升级与灾难恢复)；不要通过强制修改版本或删除安全证据绕过保护。
