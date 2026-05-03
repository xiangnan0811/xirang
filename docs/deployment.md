# 构建、发布与部署指南

本文档说明 Xirang 的官方发布约定、生产部署方式、版本回滚和维护者发布入口。

## 官方交付标准

- GitHub Release 是唯一权威公开版本源和变更说明源。
- Docker Hub 是唯一官方镜像源，默认镜像地址为 `docker.io/xirang/xirang`。
- 当前仅支持稳定版 semver：`vX.Y.Z`。
- `latest` 仅代表最新稳定版；生产环境建议固定到显式版本标签。
- 公开 release 不自动触发私有部署；维护者部署使用手动 workflow。

## 架构概览

生产环境使用 All-in-One 单容器架构。容器启动时会检测 `/etc/nginx/certs/fullchain.pem` 和 `/etc/nginx/certs/privkey.pem`：存在证书时启用 HTTPS 并将 HTTP 重定向到 HTTPS；未挂载证书时自动使用 HTTP 模式。

```text
                    ┌────────────────────────────────┐
                    │        Docker Container        │
   :80 (HTTP)  ───> │  Nginx                         │
   :443 (HTTPS) ──> │  Nginx（证书存在时启用）          │
                    │    ├── /api/v1/*  ──> Backend  │
                    │    ├── /healthz   ──> Backend  │
                    │    └── /*         ──> 静态文件  │
                    │                                │
                    │  Backend (:3000)               │
                    │    └── SQLite(/data) 或 PG     │
                    │                                │
                    │  Cron (每日 02:00 自动备份)      │
                    ├────────────────────────────────┤
                    │  /data    → 数据库文件           │
                    │  /backup  → 备份文件            │
                    └────────────────────────────────┘
```

## 生产部署

### Docker Compose（推荐）

`docker-compose.prod.yml` 已默认指向官方 Docker Hub 镜像。

```bash
# 1. 获取部署文件
git clone https://github.com/xiangnan0811/xirang.git
cd xirang

# 2. 准备环境变量
cp .env.deploy .env

# 必填项
# ADMIN_INITIAL_PASSWORD=<强密码>
# JWT_SECRET=<强随机字符串>
# DATA_ENCRYPTION_KEY=<加密密钥>

# 生产环境建议固定稳定版镜像
echo 'IMAGE_TAG=vX.Y.Z' >> .env

# 可选：开启版本检查
echo 'VERSION_CHECK_URL=https://api.github.com/repos/xiangnan0811/xirang/releases/latest' >> .env

# 3. 可选：启用 HTTPS
mkdir -p certs
cp /path/to/fullchain.pem certs/
cp /path/to/privkey.pem certs/
# 然后在 docker-compose.prod.yml 中取消注释 ./certs:/etc/nginx/certs:ro

# 4. 拉取并启动
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

默认情况下：

- 镜像地址：`docker.io/xirang/xirang`
- 数据目录：`./data`
- 备份目录：`./backups`
- HTTP 端口：`80 -> 8080`
- HTTPS 端口：`443 -> 8443`
- HTTPS 证书目录：`./certs`（需要取消注释 Compose 里的证书挂载；如未挂载证书，容器使用 HTTP 模式）

如需 PostgreSQL，在 `.env` 中改为：

```env
DB_TYPE=postgres
DB_DSN=postgresql://user:pass@host:5432/xirang?sslmode=require
```

### Docker Run

```bash
cp .env.deploy .env

docker run -d \
  --name xirang \
  --restart unless-stopped \
  -p 80:8080 -p 443:8443 \
  -v xirang-data:/data \
  -v xirang-backup:/backup \
  -v "$(pwd)/certs:/etc/nginx/certs:ro" \
  --env-file .env \
  docker.io/xirang/xirang:vX.Y.Z
```

### 环境变量要点

必填变量：

- `ADMIN_INITIAL_PASSWORD`
- `JWT_SECRET`
- `DATA_ENCRYPTION_KEY`

常用部署变量：

- `IMAGE_TAG`
- `DB_TYPE`
- `DB_DSN`
- `SQLITE_PATH`
- `HTTP_PORT`
- `HTTPS_PORT`
- `VERSION_CHECK_URL`

完整列表见 [docs/env-vars.md](env-vars.md)。

## 更新与回滚

### 升级到新稳定版

推荐方式是修改 `.env` 中的 `IMAGE_TAG`：

```env
IMAGE_TAG=vX.Y.Z
```

然后执行：

```bash
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

### 临时指定版本

```bash
IMAGE_TAG=vX.Y.Z docker compose -f docker-compose.prod.yml pull
IMAGE_TAG=vX.Y.Z docker compose -f docker-compose.prod.yml up -d
```

### 关于 `latest`

- `latest` 仅表示当前最新稳定版
- 适合快速试用
- 不建议作为生产环境长期固定标签

### 版本检查说明

`VERSION_CHECK_URL` 会让 `/api/v1/version/check` 请求 GitHub latest release API，并将返回的 `tag_name` 与服务端当前构建版本比较。当前构建版本来自编译时注入；如果二进制或镜像构建时没有注入版本信息，`/api/v1/version` 会返回 `dev`，版本检查结果只能作为开发提示。

## 镜像构建（仅维护者或高级用户）

如果你不是在维护发布链路，通常不需要本地构建镜像。

### All-in-One 单镜像构建

```bash
docker build -f deploy/allinone/Dockerfile -t xirang/xirang:latest .
```

### 多架构构建

```bash
docker buildx create --use

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f deploy/allinone/Dockerfile \
  -t xirang/xirang:latest \
  --push .
```

说明：

- SQLite 默认要求 `CGO_ENABLED=1`
- 本仓库的官方镜像发布由 GitHub Actions 完成
- 用户默认路径应始终优先使用预构建镜像，而不是手工 build

## 数据与备份

### 数据卷

生产容器使用两个持久化目录：

| 路径 | 用途 |
|------|------|
| `/data` | SQLite 数据库及应用数据 |
| `/backup` | 自动/手动备份文件 |

### 自动备份

容器内置 cron：

| 时间 | 操作 |
|------|------|
| 每日 02:00 | 执行 `backup-db.sh`，备份数据库到 `/backup/db/` |
| 每日 02:30 | 清理 30 天前的旧备份文件 |

### 手动备份与恢复

```bash
# SQLite 备份（Docker Compose 默认 bind mount: ./data -> /data）
DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/backup-db.sh ./backups

# SQLite 恢复
DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/restore-db.sh ./backups/xirang-sqlite-20260301-020000.db

# PostgreSQL 备份
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/backup-db.sh ./backups

# PostgreSQL 恢复
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/restore-db.sh ./backups/xirang-postgres-20260301-020000.dump
```

## 健康检查与运维

### 健康检查

```bash
# 容器内部
curl -fsS http://127.0.0.1:8080/healthz

# 通过 HTTP（外部，无证书模式）
curl -fsS http://127.0.0.1/healthz

# 通过 HTTPS（外部，证书模式）
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

# 查看 SQLite 任务数量
docker exec -it xirang sh -lc \
  "sqlite3 /data/xirang.db 'SELECT count(*) FROM tasks;'"
```

## CI/CD 发布链路

### 持续集成

- 工作流：`.github/workflows/ci.yml`
- 触发：`push` / `pull_request`
- 检查项：
  - PR 标题 Conventional Commits 校验
  - 后端 `golangci-lint`、`go test -coverprofile=coverage.out ./...`、`go build ./...`、`govulncheck ./...`
  - 前端 `npm audit --audit-level=moderate`、`npm run check`
  - bundle budget
  - 文档新鲜度提醒

### Release Please

- 工作流：`.github/workflows/release-please.yml`
- 触发：`main` 分支 push
- 作用：自动维护 Release PR、更新 `CHANGELOG.md`、生成 GitHub Release

### Docker 镜像发布

- 工作流：`.github/workflows/publish-images.yml`
- 正式入口：`release.published`
- 手动入口：仅用于维护者重发
- 产物标签：
  - `vX.Y.Z`
  - `X.Y.Z`
  - `latest`（仅正式稳定版更新）

### 私有部署

- 工作流：`.github/workflows/deploy.yml`
- 触发：仅 `workflow_dispatch`
- 用途：维护者私有环境部署
- 不属于公开开源发布主线

## 维护者说明

维护者需要额外关注：

- Release Please manifest 与 `CHANGELOG.md`
- GitHub branch protection / squash merge 设置
- Docker Hub secrets / variables
- 镜像重发与私有部署

详见 [docs/release-maintainers.md](release-maintainers.md)。

## 快速参考

```bash
# 生产部署
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d

# 健康检查
curl -fsS http://127.0.0.1/healthz
curl -kfsS https://127.0.0.1/healthz

# 查看日志
docker compose -f docker-compose.prod.yml logs -f xirang

# 版本回滚
IMAGE_TAG=vX.Y.Z docker compose -f docker-compose.prod.yml pull
IMAGE_TAG=vX.Y.Z docker compose -f docker-compose.prod.yml up -d
```
