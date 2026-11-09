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
| [internal/backupasset/processing/capabilities/apk_inputs.go](internal/backupasset/processing/capabilities/apk_inputs.go) | Worker 构建输入投影、双架构内容摘要与严格校验 |
| [internal/backupasset/processing/capabilities/sandbox.go](internal/backupasset/processing/capabilities/sandbox.go) | Core/Worker 共用工具链指纹、运行时闭包及能力就绪校验 |

API 以 Router 和生成的 OpenAPI 为准，本页不手工维护接口或模型全集。备份资产默认关闭，是否能够启用遵循领域就绪合同；可选 Worker 的缺席不等同于 Core 故障。

Worker 工具链 v3 的输入身份、保留镜像引用及 v1 签名闭包的兼容边界见[工具链身份与构建输入](../docs/spec/domains/backup-processing-export.md#工具链身份与构建输入)；归档与更新操作见[发布手册](../docs/maintainers/release.md)。

## 数据库迁移版本

当前迁移版本：`000099_processing_backfill_quota`。

该迁移的 SQLite/PostgreSQL 配对文件为：

- `internal/database/migrations/sqlite/000099_processing_backfill_quota.up.sql`
- `internal/database/migrations/sqlite/000099_processing_backfill_quota.down.sql`
- `internal/database/migrations/postgres/000099_processing_backfill_quota.up.sql`
- `internal/database/migrations/postgres/000099_processing_backfill_quota.down.sql`

该迁移为处理作业增加真实来源 Provider、预计输入字节和后台创建收费标记，并新增 UTC 整点小时单行账本。旧作业按精确恢复点／Repository／Catalog 关联回填元数据，不补造历史收费；无效关联、未知 Provider 或未版本化的部分结构在写 dirty 前拒绝。任何已收费作业（包括零字节）或账本正计数都会阻止降级，保留干净版本及历史；未使用时才允许受保护回退。SQLite 额外依赖或结构漂移会阻止回退，PostgreSQL 元数据准入与直接 down 均和收费事务保持锁序；启动验证实际列、约束、索引、种子与保护器，不自动修复漂移。

前一份 `000098_processing_shared_derived_blobs` 保留独立上传／产物集共享物理 blob 的关系、迁移与历史；出现重复 staging ID 或 manifest 摘要后，该版本的降级保护仍独立生效，不因配额迁移可回退而解除。

该声明与 `internal/database/migrations/{sqlite,postgres}` 中最新的成对 up/down 文件同步，并由迁移检查脚本校验。升级前提、旧写入者排空、双引擎恢复和降级保护要求见[数据库合同](../docs/spec/backend/database-guidelines.md)、[部署指南](../docs/deployment.md#备份职责收敛升级)及[备份、恢复与快照](../docs/admin/backup-recovery.md#升级与灾难恢复)；不要通过强制修改版本或删除安全证据绕过保护。
