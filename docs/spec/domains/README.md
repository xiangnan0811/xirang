# 领域合同

领域主文同时规定持久化、服务与 API、前端表现、兼容边界和回归证据。修改一条业务规则时更新其主文，通用层仅链接，不复制规则。

| 按任务阅读 | 主文 |
|---|---|
| 身份、SSH 范围、step-up、临时授权和凭据审计 | [凭据与访问](credentials-access.md) |
| 调度、执行证据、恢复、演练及 RPO/RTO 定义 | [任务执行与恢复](task-execution-recovery.md) |
| 投递、健康和诊断（RPO 引用任务合同） | [告警与健康](alerting-health.md) |
| 节点日志采集、游标和关闭 | [节点日志](node-log-collection.md) |
| Provider、访问绑定、版本化和恢复点发布 | [备份仓库与发布](backup-repository.md) |
| 文件来源、目录、可变源刷新和代次回收 | [Catalog](backup-catalog.md) |
| 索引、内容匹配、收藏、标签和保存搜索 | [搜索与用户状态](backup-search.md) |
| 安全预览、下载、票据、传输、网关和日志 | [内容交付](backup-content-delivery.md) |
| Worker、派生内容、归档和导出 | [处理与导出](backup-processing-export.md) |
| 归档、冻结、retention、purge 和灾难恢复 | [生命周期](backup-lifecycle.md) |
| 开关、库存确认、运行时切换和补偿 | [启用条件](backup-enablement.md) |

通用约定见[后端](../backend/README.md)、[前端](../frontend/README.md)；协作、测试与文档维护见[指南](../guides/README.md)。
