# 后端目录与依赖

后端 Go 模块位于 `backend/`，HTTP 进程入口为 `backend/cmd/server/main.go`。大部分代码放在 `internal/`，不作为外部库公开。优先沿用现有包边界，再决定是否拆分新包。

## 代码归属

| 路径（相对 backend） | 职责 |
| --- | --- |
| `cmd/server/` | 进程启动、依赖装配与后台工作器生命周期 |
| `internal/api/router.go` | 路由注册及认证、权限、所有权、限流、请求体上限 |
| `internal/api/handlers/` | 按资源组织的参数绑定、服务调用和响应映射 |
| `internal/model/` | GORM 模型、模型 hooks 与脱敏方法 |
| `internal/database/` | 数据库连接、日志适配、成对迁移与启动检查 |
| `internal/settings/` | 动态设置注册、校验、缓存及解析 |
| `internal/middleware/` | 认证、RBAC、请求审计、访问日志与指标 |
| `internal/auth/`、`internal/secure/` | 身份验证与敏感字段加密基础设施 |
| `internal/sshutil/`、`internal/fileaccess/` | 共享 SSH 机制与受约束文件访问 |
| `internal/task/`、`internal/task/executor/` | 调度、任务状态及执行器 |
| `internal/alerting/`、`internal/dashboards/`、`internal/metrics/` | 告警、看板与指标服务 |
| `internal/backupasset/`、`internal/nodelogs/` | 备份资产、节点日志领域 |
| `internal/credentialaudit/` | 凭据使用领域审计；与 HTTP 请求审计分开 |
| `internal/ws/`、`internal/util/` | WebSocket Hub 与小型通用辅助函数 |

## 分层规则

- Handler 保持薄层：解析参数、绑定 JSON、调用服务或简单查询，再通过响应辅助函数返回。复杂工作流归领域服务，不能持续堆进 Handler。
- 服务返回领域错误，不依赖 Gin 响应。数据库连接和迁移归 `database`；模型敏感字段通过模型或服务边界加解密，Handler 不重复实现。
- SSH 认证、连接和命令生命周期复用 `sshutil`；任务执行差异放执行器，避免各资源包复制认证解析逻辑。
- 在进程组合根注入共享服务与后台工作器，不在 Router 或各 Handler 私建第二套运行时。备份资产各子包的禁用依赖及唯一 executor→Provider 映射见[仓库与发布合同](../domains/backup-repository.md)。

## 路由注册与认证边界

新增普通 `/api/v1` 业务路由默认注册到 `secured`，继承 `AuthMiddleware`、请求审计、API 限流及请求体上限，再按资源合同配置适用的 RBAC/角色限制和所有权检查。不能为方便测试或调用省略认证、权限或对象授权；资源 ID 本身不是访问许可。账户自身操作等不需要独立资源权限的既定路由仍须通过其账户/会话授权边界。

以下是当前 `router.go` 中位于 `secured` 外的明确例外，不是新增无保护 API 的通用许可：

| 路径（相对 `/api/v1`） | 独立边界 |
| --- | --- |
| `GET /auth/captcha`、`POST /auth/login`、`POST /auth/2fa/login` | 登录前入口，使用登录限流；登录挑战和二步登录自行校验其证明 |
| `GET /version` | 公开版本信息；其他版本管理操作不因此公开 |
| `/asset-content/:deliveryId` 及尾斜杠/不支持方法的拒绝路由 | 内容网关使用自身 cookie/grant/session 授权和安全 recovery；不把 opaque ID 或 Bearer header 当内容权限，错误形状也必须走安全拒绝链。详见[内容交付](../domains/backup-content-delivery.md) |
| `GET /status-page` | 明确公开的服务状态页，只返回该端点允许的公开视图 |
| `GET /ws/logs` | WebSocket 首条协议消息验证主 token、当前会话权限及 `tasks:read`，并限制 operator 对象可见性；不是匿名日志流 |
| `GET /ws/terminal` | WebSocket 协议内验证当前 admin 主 token、step-up 与匹配的临时凭据授权，再进入节点/凭据/SSH 边界；不是匿名终端。详见[凭据与访问](../domains/credentials-access.md) |

`/healthz`、`/readyz` 位于 `/api/v1` 之外，存活/数据库就绪语义见[部署运行时](deployment-runtime.md)。`/metrics` 有自身 token/限流边界，Swagger 有生产启用条件，不能以这些独立协议端点为由放松普通业务 API。

路由回归经过实际 Router，中间件拒绝必须阻止 Handler 副作用；覆盖缺失/失效会话、错误角色和未拥有资源。例外保留其真实认证/拒绝方式，尤其验证内容 gateway 不绕过安全链和 WebSocket 会话吊销失败关闭。现有证据入口为 `api/router_test.go`、资源 RBAC 测试、`handlers/realtime_auth_test.go` 与 `handlers/terminal_handler_test.go`。

## 命名和导航

包名小写，通常使用一个词；沿用现有复合词包名。Handler 文件按资源命名，如 `node_handler.go`。测试与实现同目录，后缀为 `*_test.go`；尽量使用包内测试，谨慎增加仅供测试的导出 API。

Go 导出字段采用 PascalCase；JSON 和数据库字段采用 snake_case。数据库命名、迁移命名及历史兼容由[数据库合同](database-guidelines.md)定义。

可参考 `dashboard_handler.go` 的薄 Handler、`dashboards/service.go` 的验证与事务、`settings/service.go` 的注册表和缓存、`model/node.go` 的 GORM tags、`Node.Sanitized()` 与 `BeforeSave`/`AfterFind` hooks；`model/models.go` 仅为模型分领域文件索引。示例是导航，不替代对应领域的授权和状态合同。
