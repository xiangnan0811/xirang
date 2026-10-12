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

当前迁移版本：`000093_batch_command_retry`。

该迁移的 SQLite/PostgreSQL 配对文件为：

- `internal/database/migrations/sqlite/000093_batch_command_retry.up.sql`
- `internal/database/migrations/sqlite/000093_batch_command_retry.down.sql`
- `internal/database/migrations/postgres/000093_batch_command_retry.up.sql`
- `internal/database/migrations/postgres/000093_batch_command_retry.down.sql`

该迁移为新批量命令保存不可变来源摘要、重试来源消费边和永久运行证据；不保存第二份命令正文，也不为历史批次补造来源。每个来源 Task/失败 Run 至多消费一次；`run_id=0` 仅代表可证未派发。根批次允许在预约前编辑，但运行记录当前输入快照，来源变化后不能获得显式重试资格；重试子批次预约和执行都须匹配自己的不可变来源。

SQLite 使用新增列和触发器，不重建 TaskRun 表。两引擎均校验来源关联、身份不可变、证据从 pending 单次进入真实终态及永久保留；已使用的 v1 批次或证据使降级在 dirty 元数据写入前拒绝，未使用时可退回并保留全部 v0 回执。启动检查真实列、索引、外键、约束及完整保护器定义；漂移返回 `ErrMigrationSchemaDrift`。升级必须停止并排空旧 Core/worker，不能与不写运行证据的旧二进制混用。

该声明与 `internal/database/migrations/{sqlite,postgres}` 中最新的成对 up/down 文件同步，并由迁移检查脚本校验。升级前提、旧写入者排空、双引擎恢复和降级保护要求见[数据库合同](../docs/spec/backend/database-guidelines.md)、[部署指南](../docs/deployment.md#备份职责收敛升级)及[备份、恢复与快照](../docs/admin/backup-recovery.md#升级与灾难恢复)；不要通过强制修改版本或删除安全证据绕过保护。
