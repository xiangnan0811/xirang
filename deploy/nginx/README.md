# Xirang Gateway Template (Nginx)

该目录提供 Nginx 模板，供生产镜像使用（静态前端 + 反向代理）。

## 当前生产镜像

- 推荐使用一体化镜像：`deploy/allinone/Dockerfile`
- 该镜像内包含：
  - 后端二进制（监听 `:8080`）
  - 前端静态资源
  - Nginx 反向代理（监听 `80/443`）

## Nginx 环境变量

- `BACKEND_UPSTREAM`：后端服务地址，默认 `http://127.0.0.1:8080`

## TLS 证书

容器默认监听 `443` 并启用 TLS，需挂载以下文件：

- `/etc/nginx/certs/fullchain.pem`
- `/etc/nginx/certs/privkey.pem`

## 路由说明

- `/api/v1/*`：转发到后端 API（含 WebSocket 升级）
- `/healthz`：转发到后端健康检查
- 其它路径：前端 SPA 静态资源与 history 回退
