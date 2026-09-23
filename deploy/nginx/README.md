# Nginx 网关模板

[文档入口](../../docs/README.md) · [部署指南](../../docs/deployment.md)

本目录的 [default.conf.template](templates/default.conf.template) 供[一体化镜像](../allinone/Dockerfile)使用，负责前端静态资源与后端反向代理。网关固定监听容器内 `10761`，上游为 `127.0.0.1:3000`；后端镜像默认 `SERVER_ADDR=:3000`，不要仅修改后端端口而不调整网关。容器入口先等候后端 `/readyz` 成功，再启动 Nginx。

## 路由

| 路径 | 行为 |
|---|---|
| `/api/v1/asset-content/<opaque-id>` | 精确匹配 32 位小写十六进制 ID；关闭代理响应和请求缓冲、缓存、临时文件与 gzip，转发 `Range` / `If-Range`；读、写和发送超时为 75 秒 |
| `/api/v1/asset-content` 形状但不满足精确 ID 的路径 | 使用专用脱敏日志并交由后端安全拒绝，不继承精确内容路由的流式传输、缓冲或超时配置 |
| `/api/v1/*` | 普通 API 与 WebSocket 升级，响应读取超时为 3600 秒 |
| `/healthz` | 后端进程存活检查，不访问数据库 |
| `/readyz` | 后端数据库连接检查，数据库缺失或 Ping 失败返回 503；不表示所有后台子系统就绪 |
| 其他路径 | 静态资源与 SPA history 回退 |

内容交付 ID 的授权、Cookie、单 Range 和缓存安全要求由[内容交付合同](../../docs/spec/domains/backup-content-delivery.md)维护。精确路由不改变普通 API 的超时与 WebSocket 行为。

## 日志与安全响应头

普通访问日志写入 `/logs/nginx-access.log`，错误日志写入 `/logs/nginx-error.log`。普通访问日志请求行仅记录路径，但仍包含来源页和 User-Agent 等字段，不能将其视为完全脱敏的日志。

内容交付路径使用 `/logs/nginx-asset-content.log`，仅记录请求 ID、状态、响应字节数与时延，不记录 URI、参数、Cookie、来源页或 User-Agent。相关路由把 Nginx 错误日志设为 `/dev/null crit`，避免完整 URI 泄露；故障诊断使用安全访问日志、后端指标与聚合审计。

普通页面由模板设置 CSP 等安全响应头；外部 WebSocket 构建需要配套 `CSP_CONNECT_SRC_EXTRA`。内容交付路由拥有独立响应头配置，使后端经过审查的渲染器 CSP 与框架策略透传。更改时运行[内容网关检查](../../scripts/check-asset-content-nginx.sh)及其[自测](../../scripts/check-asset-content-nginx.test.sh)。

TLS 终止、日志轮转、持久化卷、数据库备份和容器启动参数统一见[部署指南](../../docs/deployment.md)与[环境变量](../../docs/env-vars.md)。认证分块缓存目录 `/var/cache/xirang/asset-content` 不得映射到备份源或持久数据目录，详细生命周期见内容交付合同。
