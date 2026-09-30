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

当前迁移版本：`000091_service_monitor_retirement`。

该迁移的 SQLite/PostgreSQL 配对文件为：

- `internal/database/migrations/sqlite/000091_service_monitor_retirement.up.sql`
- `internal/database/migrations/sqlite/000091_service_monitor_retirement.down.sql`
- `internal/database/migrations/postgres/000091_service_monitor_retirement.up.sql`
- `internal/database/migrations/postgres/000091_service_monitor_retirement.down.sql`

`000091_service_monitor_retirement.up.sql` 是不可逆的服务监控退役迁移：在同一事务封存精确匹配 `^XR-SERVICE-DOWN-[0-9]+$` 的历史告警与未发送投递，再删除 `service_uptime_samples` 和 `service_monitors`。告警行、升级历史、已发送投递及 attempt 事实保留；备份资产及 Provider 元数据、任务与运行历史、任务日志、恢复证据、审计、异常和任务 SLO 不变。已发布的历史迁移（含职责收敛迁移及其保护器）保持不变。

`000091_service_monitor_retirement.down.sql` 明确失败，不是回滚方案。独立 `schema_migrations` 保护器在迁移驱动写入旧版本/dirty 之前拒绝低于该版本，即使空库也不允许降级；失败后仍为 clean，允许后续向前迁移及未来版本退至该保护版本。启动精确验证保护器的事件、条件、函数体及启用状态，损坏时返回 `ErrMigrationSchemaDrift`。不可逆升级只能通过升级前已验证的整库备份，配套旧版二进制、配置、`DATA_ENCRYPTION_KEY` 及适用历史密钥恢复，不能手改迁移元数据、删除保护器或重建空表伪装恢复。

该声明与 `internal/database/migrations/{sqlite,postgres}` 中最新的成对 up/down 文件同步，并由迁移检查脚本校验。升级前提、旧写入者排空、双引擎恢复和降级保护要求见[数据库合同](../docs/spec/backend/database-guidelines.md)、[部署指南](../docs/deployment.md#备份职责收敛升级)及[备份、恢复与快照](../docs/admin/backup-recovery.md#升级与灾难恢复)；不要通过强制修改版本或删除安全证据绕过保护。
