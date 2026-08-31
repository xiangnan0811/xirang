# Xirang Gateway Template (Nginx)

该目录提供 Nginx 模板，供生产 All-in-One 镜像使用（静态前端 + 反向代理）。

## 当前生产镜像

- 推荐使用一体化镜像：`deploy/allinone/Dockerfile`
- 该镜像内包含：
  - 后端二进制（容器内部监听 `:3000`）
  - 前端静态资源
  - Nginx 反向代理（容器内部固定监听 `10761`）
  - `supercronic` 数据库备份任务（每日 02:00 备份，02:30 清理 30 天前备份）

## 路由说明

- `/api/v1/asset-content/<opaque-id>`：仅转发精确 32 位小写十六进制内容交付 ID；关闭代理缓冲、请求缓冲、缓存、临时文件与 gzip，并保留单 Range/If-Range 请求头
- `/api/v1/*`：转发到后端 API（含 WebSocket 升级）
- `/healthz`：转发到后端进程存活检查（不访问数据库）
- `/readyz`：转发到后端数据库就绪检查（数据库 Ping 失败返回 503）
- 其它路径：前端 SPA 静态资源与 history 回退

内容交付 ID 不具备独立授权能力；后端仅接受精确路径 Cookie，并在每次请求重新校验会话、权限、资源与预算。该专用路由不改变普通 API 的超时或 WebSocket 合同。

`/api/v1/asset-content` 形状但不匹配精确 32 位 ID 的请求只进入脱敏日志与安全拒绝回退；该回退不继承精确内容路由的 streaming、Range、buffering 或专用 timeout 策略。

## HTTPS

容器只提供 HTTP 单入口。HTTPS/TLS 由用户在外部反向代理层处理，例如 Caddy、Nginx Proxy Manager、Nginx 或云厂商负载均衡。

## 日志

Nginx 访问日志和错误日志写入 `/logs/nginx-access.log` 与 `/logs/nginx-error.log`，生产 Compose 默认将容器内 `/logs` 映射到宿主机 `./logs`；访问日志请求行仅记录路径，不记录查询字符串。

内容交付请求使用 `/logs/nginx-asset-content.log`，只记录请求 ID、状态、响应字节数与时延，不记录 URI、参数、Cookie、来源页或 User-Agent。专用 location 禁止继承可能包含完整 URI 的 Nginx error log；故障诊断使用安全访问日志、后端指标与聚合审计。

认证分块缓存根目录为 `/var/cache/xirang/asset-content`。它不属于 `/data`、`/backup`、`/logs`，也不声明为持久卷；进程重启后旧分块因进程密钥失效而由后端对账删除，不能将该路径映射到备份源或持久数据目录。
