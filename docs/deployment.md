# 构建与部署指南

本文档详细说明 Xirang（息壤）的镜像构建、生产部署、数据管理及日常运维操作。

---

## 目录

- [架构概览](#架构概览)
- [镜像构建](#镜像构建)
  - [All-in-One 生产镜像（推荐）](#all-in-one-生产镜像推荐)
  - [纯后端镜像](#纯后端镜像)
  - [多架构构建](#多架构构建)
- [部署方式](#部署方式)
  - [生产部署](#生产部署)
  - [开发环境（Docker Compose）](#开发环境docker-compose)
  - [本地直接运行](#本地直接运行)
- [环境变量参考](#环境变量参考)
- [HTTPS 证书配置](#https-证书配置)
- [数据持久化与备份](#数据持久化与备份)
- [健康检查与运维](#健康检查与运维)
- [版本回滚](#版本回滚)
- [CI/CD 自动发布](#cicd-自动发布)

---

## 架构概览

生产环境使用 All-in-One 单容器架构：

```
                    ┌────────────────────────────────┐
                    │        Docker Container        │
   :80 (HTTP)  ───> │  Nginx ── 301 ──> HTTPS        │
   :443 (HTTPS) ──> │  Nginx                         │
                    │    ├── /api/v1/*  ──> Backend  │
                    │    ├── /healthz   ──> Backend  │
                    │    └── /*         ──> 静态文件  │
                    │                                │
                    │  Backend (:8080)               │
                    │    └── SQLite(/data) 或 PG     │
                    │                                │
                    │  Cron (每日 02:00 自动备份)      │
                    ├────────────────────────────────┤
                    │  /data    → 数据库文件           │
                    │  /backup  → 备份文件            │
                    └────────────────────────────────┘
```

---

## 镜像构建

### All-in-One 生产镜像（推荐）

三阶段构建，将前端、后端、Nginx 打包为单一镜像。

**Dockerfile**: `deploy/allinone/Dockerfile`

| 构建阶段 | 基础镜像 | 产物 |
|---------|---------|------|
| web-builder | `node:20-alpine` | 前端静态文件 (`web/dist`) |
| backend-builder | `golang:1.26` | Go 二进制 (`xirang`) |
| 运行时 | `nginx:1.27` | Nginx + 后端 + 前端 + cron |

在**项目根目录**执行：

```bash
docker build -f deploy/allinone/Dockerfile -t xirang/xirang:latest .
```

### 纯后端镜像

基于 `distroless` 的轻量后端镜像，适合前后端分离部署场景。

**Dockerfile**: `backend/Dockerfile`

```bash
cd backend
docker build -t xirang/backend:latest .
```

- 仅暴露 `:8080`，不含 Nginx 和前端静态文件
- 运行时镜像为 `distroless`（无 shell，体积小，安全性高）
- 使用 `CGO_ENABLED=0` 纯静态编译

### 多架构构建

支持同时构建 `linux/amd64` 和 `linux/arm64`：

```bash
# 需要先创建 buildx builder（仅首次）
docker buildx create --use

# 构建并推送到镜像仓库
docker buildx build --platform linux/amd64,linux/arm64 \
  -f deploy/allinone/Dockerfile \
  -t xirang/xirang:latest \
  --push .
```

---

## 部署方式

### 生产部署

使用 `docker-compose.prod.yml`，从镜像仓库拉取预构建镜像。

#### 1. 准备环境变量

```bash
cp .env.deploy .env
```

编辑根目录 `.env`，**必须配置**以下字段：

```env
ADMIN_INITIAL_PASSWORD=<强密码，首次启动创建 admin 账号>
JWT_SECRET=<随机字符串，建议 32 位以上>
DATA_ENCRYPTION_KEY=<加密密钥，用于敏感字段加解密>
DB_TYPE=sqlite
SQLITE_PATH=/data/xirang.db
```

> `docker-compose.prod.yml` 会读取同目录下的 `.env`。如使用 PostgreSQL，将 `DB_TYPE` 改为 `postgres` 并设置 `DB_DSN`。
>
> `SSH_AUTO_ACCEPT_NEW_HOSTS` 默认值为 `true`，首次连接的新主机密钥会被自动接受，已知主机密钥变更仍会被拒绝。如需禁用，请设置 `SSH_AUTO_ACCEPT_NEW_HOSTS=false`。

#### 2. 准备 HTTPS 证书

将证书文件放到 `deploy/certs/` 目录：

```
deploy/certs/fullchain.pem   # 证书链
deploy/certs/privkey.pem     # 私钥
```

#### 3. 启动服务

```bash
# 拉取最新镜像
make prod-pull

# 后台启动
make prod-up
```

或直接使用 docker compose：

```bash
docker compose -f docker-compose.prod.yml up -d
```

#### 4. 验证部署

```bash
# 健康检查
curl -kfsS https://127.0.0.1/healthz

# 查看容器状态
docker compose -f docker-compose.prod.yml ps

# 查看日志
docker compose -f docker-compose.prod.yml logs -f xirang
```

#### 5. 停止服务

```bash
make prod-down
```

#### 镜像参数（可选）

通过环境变量自定义镜像来源：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `IMAGE_REGISTRY` | `docker.io` | 镜像仓库地址 |
| `IMAGE_NAMESPACE` | `xirang` | 镜像命名空间 |
| `IMAGE_TAG` | `latest` | 镜像标签 |

示例：

```bash
IMAGE_TAG=v1.2.0 make prod-pull prod-up
```

### 开发环境（Docker Compose）

`docker-compose.yml` 直接挂载源码，支持热更新：

```bash
docker compose up
```

- 前端：`http://localhost:5173`（Vite HMR）
- 后端：`http://localhost:8080`（go run 热编译）

### 本地直接运行

不依赖 Docker，分两个终端启动：

```bash
# 终端 1：后端
make backend-run    # 等价于 cd backend && go run ./cmd/server

# 终端 2：前端
make web-dev        # 等价于 cd web && npm run dev
```

---

## 环境变量参考

### 必填变量（生产环境）

| 变量 | 说明 |
|------|------|
| `ADMIN_INITIAL_PASSWORD` | 初始 admin 密码（首次启动时创建） |
| `JWT_SECRET` | JWT 签名密钥（≥16 字符强随机字符串） |
| `DATA_ENCRYPTION_KEY` | 敏感字段加密密钥（推荐 32 字节 base64） |

### 前端变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `VITE_API_BASE_URL` | `/api/v1` | API 路径前缀 |
| `VITE_PROXY_TARGET` | `http://127.0.0.1:8080` | 开发代理目标地址 |
| `VITE_DEV_API_DIRECT_URL` | — | 开发模式直连后端地址 |
| `VITE_WS_URL` | 自动推导 | 自定义 WebSocket 地址 |
| `VITE_ENABLE_DEMO_MODE` | — | 设为 `true` 启用 mock 数据 |

完整环境变量参考见 [环境变量参考](env-vars.md)。

---

## HTTPS 证书配置

Nginx 模板（`deploy/nginx/templates/default.conf.template`）默认配置：

- 监听 80 端口，HTTP 自动 301 跳转 HTTPS
- 监听 443 端口，启用 HTTP/2
- TLS 协议：TLSv1.2 + TLSv1.3
- 安全头：HSTS、X-Content-Type-Options、X-Frame-Options、Referrer-Policy、Permissions-Policy
- 启用 gzip 压缩

证书获取建议使用 Let's Encrypt：

```bash
# 使用 certbot 申请证书（示例）
certbot certonly --standalone -d your-domain.com

# 将证书复制到部署目录
cp /etc/letsencrypt/live/your-domain.com/fullchain.pem deploy/certs/
cp /etc/letsencrypt/live/your-domain.com/privkey.pem deploy/certs/
```

---

## 数据持久化与备份

### 数据卷

生产容器使用两个 Docker volume：

| 卷名 | 容器路径 | 用途 |
|------|---------|------|
| `xirang-data` | `/data` | 数据库文件（SQLite） |
| `xirang-backup` | `/backup` | 自动/手动备份文件 |

### 自动备份

容器内置 cron 定时任务（`deploy/allinone/xirang-backup.cron`）：

| 时间 | 操作 |
|------|------|
| 每日 02:00 | 执行 `backup-db.sh`，备份数据库到 `/backup/db/` |
| 每日 02:30 | 清理 30 天前的旧备份文件 |

### 手动备份与恢复

```bash
# SQLite 备份
DB_TYPE=sqlite SQLITE_PATH=/data/xirang.db \
  bash scripts/backup-db.sh ./backups

# SQLite 恢复
DB_TYPE=sqlite SQLITE_PATH=/data/xirang.db \
  bash scripts/restore-db.sh ./backups/xirang-sqlite-20260301-020000.db

# PostgreSQL 备份
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/backup-db.sh ./backups

# PostgreSQL 恢复
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/restore-db.sh ./backups/xirang-postgres-20260301-020000.dump
```

> 恢复前脚本会自动生成 `.before-restore` 时间戳文件用于回滚。

---

## 健康检查与运维

### 健康检查

容器内置 healthcheck，每 30 秒探测后端 `/healthz`：

```bash
# 容器内部
curl -fsS http://127.0.0.1:8080/healthz

# 通过 HTTPS（外部）
curl -kfsS https://127.0.0.1/healthz
```

### 常用运维命令

```bash
# 查看容器状态
docker compose -f docker-compose.prod.yml ps

# 实时日志
docker compose -f docker-compose.prod.yml logs -f xirang

# 最近 200 行日志
docker compose -f docker-compose.prod.yml logs --tail=200 xirang

# 进入容器排查
docker exec -it xirang bash

# 容器内查看 SQLite 数据
docker exec -it xirang sh -lc \
  "sqlite3 /data/xirang.db 'SELECT count(*) FROM tasks;'"
```

---

## 版本回滚

指定镜像标签即可回滚到历史版本：

```bash
IMAGE_TAG=v1.0.0 make prod-pull prod-up
```

---

## CI/CD 自动发布

项目配置了完整的 CI/CD 流水线：

### 持续集成

- 工作流：`.github/workflows/ci.yml`
- 触发：push / pull_request
- 检查项：后端 `go test + go build`，前端 `typecheck + test + build`

### 版本发布（Release Please）

- 工作流：`.github/workflows/release-please.yml`
- 触发：master 分支 push
- 自动维护 Release PR，合并后打 Tag 并创建 GitHub Release

### 镜像发布

- 工作流：`.github/workflows/publish-images.yml`
- 触发：`release.published` 或手动
- 推送到 DockerHub，标签：`vX.Y.Z`、`X.Y.Z`、`latest`
- 多架构：`linux/amd64` + `linux/arm64`

### 自动部署

- 工作流：`.github/workflows/deploy.yml`
- 发布后自动部署 staging，可手动部署 production
- 远程执行 `docker compose pull && up -d`

### 所需 GitHub Secrets

| Scope | 变量 | 说明 |
|-------|------|------|
| 仓库级 | `DOCKERHUB_USERNAME` | DockerHub 用户名 |
| 仓库级 | `DOCKERHUB_TOKEN` | DockerHub 访问令牌 |
| Environment | `DEPLOY_HOST` | 部署目标主机 |
| Environment | `DEPLOY_USER` | SSH 用户名 |
| Environment | `DEPLOY_SSH_KEY` | SSH 私钥 |
| Environment | `DEPLOY_PATH` | 远端部署目录 |

---

## 快速参考

```bash
# 构建镜像
docker build -f deploy/allinone/Dockerfile -t xirang/xirang:latest .

# 生产部署
make prod-pull && make prod-up

# 停止服务
make prod-down

# 健康检查
curl -kfsS https://127.0.0.1/healthz

# 查看日志
docker compose -f docker-compose.prod.yml logs -f xirang

# 版本回滚
IMAGE_TAG=v1.0.0 make prod-pull prod-up
```
