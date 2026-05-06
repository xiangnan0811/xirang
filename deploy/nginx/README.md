# Xirang Gateway Template (Nginx)

该目录提供 Nginx 模板，供生产镜像使用（静态前端 + 反向代理）。

## 当前生产镜像

- 推荐使用一体化镜像：`deploy/allinone/Dockerfile`
- 该镜像内包含：
  - 后端二进制（容器内部监听 `:3000`）
  - 前端静态资源
  - Nginx 反向代理（容器内部监听 `8080/8443`，由 Compose 映射到宿主机 `80/443`）
  - `supercronic` 数据库备份任务（每日 02:00 备份，02:30 清理 30 天前备份）

## Nginx 环境变量

- `BACKEND_UPSTREAM`：后端服务地址，默认 `http://127.0.0.1:3000`

## TLS 证书

容器启动时自动检测以下文件：

- `/etc/nginx/certs/fullchain.pem`
- `/etc/nginx/certs/privkey.pem`

两者都存在时启用 HTTPS 模式：容器 `8080` 仅保留 `/healthz`，其他 HTTP
请求会重定向到 `8443`。未挂载证书时入口脚本会切换到 HTTP 模式，只监听
容器 `8080`。

## 路由说明

- `/api/v1/*`：转发到后端 API（含 WebSocket 升级）
- `/healthz`：转发到后端健康检查
- 其它路径：前端 SPA 静态资源与 history 回退
