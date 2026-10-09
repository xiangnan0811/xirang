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

当前迁移版本：`000092_cron_backup_health`。

该迁移的 SQLite/PostgreSQL 配对文件为：

- `internal/database/migrations/sqlite/000092_cron_backup_health.up.sql`
- `internal/database/migrations/sqlite/000092_cron_backup_health.down.sql`
- `internal/database/migrations/postgres/000092_cron_backup_health.up.sql`
- `internal/database/migrations/postgres/000092_cron_backup_health.down.sql`

`000092_cron_backup_health.up.sql` 新增 cron 数据库备份故障游标及固定单行使用标记。游标约束来源密钥、来源身份、revision、故障与告警关联；首次 enrollment 与业务行同事务写入使用标记。为保存 `XR-CRON-DB-BACKUP-` 加 64 位 source key 的完整告警身份，PostgreSQL 将 `alerts.error_code` 扩展为 `VARCHAR(128)`；SQLite 保留基线 `VARCHAR(64)` 声明（SQLite 不执行声明宽度），两引擎都接受完整 82 字符值。SQLite/PostgreSQL 均在 `schema_migrations` 的 INSERT/UPDATE 降级准入点拒绝已使用 schema 或已存在超过 64 字符告警编码的降级，即使游标业务行后来被清理，使用标记仍保留。

`000092_cron_backup_health.down.sql` 仅允许未写入使用标记且不存在超过 64 字符告警编码的 schema 降级；PostgreSQL 仅在该准入检查通过后将 `error_code` 安全收窄回 `VARCHAR(64)`，SQLite 不重建 alerts 表。down 文件保留独立 body guard；已使用状态拒绝后版本保持 clean，迁移表、约束、外键和保护器不被删除。启动会验证两引擎真实列、默认值、NOT NULL、主键、CHECK、FK、告警编码容量以及保护器触发器/函数体；漂移返回 `ErrMigrationSchemaDrift`，不能以同名空操作对象放行。

该声明与 `internal/database/migrations/{sqlite,postgres}` 中最新的成对 up/down 文件同步，并由迁移检查脚本校验。升级前提、旧写入者排空、双引擎恢复和降级保护要求见[数据库合同](../docs/spec/backend/database-guidelines.md)、[部署指南](../docs/deployment.md#备份职责收敛升级)及[备份、恢复与快照](../docs/admin/backup-recovery.md#升级与灾难恢复)；不要通过强制修改版本或删除安全证据绕过保护。
