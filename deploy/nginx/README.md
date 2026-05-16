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

- `/api/v1/*`：转发到后端 API（含 WebSocket 升级）
- `/healthz`：转发到后端健康检查
- 其它路径：前端 SPA 静态资源与 history 回退

## HTTPS

容器只提供 HTTP 单入口。HTTPS/TLS 由用户在外部反向代理层处理，例如 Caddy、Nginx Proxy Manager、Nginx 或云厂商负载均衡。

## 日志

Nginx 访问日志和错误日志写入 `/logs/nginx-access.log` 与 `/logs/nginx-error.log`，生产 Compose 默认将容器内 `/logs` 映射到宿主机 `./logs`。
