# 部署、升级与运维

本文档面向自托管 Xirang 的用户，说明生产部署、外部反向代理、升级回滚、数据备份、健康检查和常见故障处理。维护者发布链路请看 [维护者发布手册](maintainers/release.md)。

## 官方交付方式

- GitHub Release 是权威公开版本源和变更说明源。
- 官方公开镜像为 `docker.io/linnea7171/xirang`。
- `latest` 表示最新稳定版；生产环境建议固定到显式 `vX.Y.Z` 标签。
- 当前公开发布仅使用稳定版 semver，不提供 nightly/prerelease 镜像通道。

## 运行架构

生产部署默认使用 All-in-One 单容器镜像：

```text
                  ┌────────────────────────────────┐
HTTP :10761  ───> │ Nginx                          │
                  │   ├── /api/v1/*  ──> Backend   │
                  │   ├── /healthz   ──> Backend   │
                  │   └── /*         ──> Web UI    │
                  │                                │
                  │ Backend (:3000)                │
                  │ SQLite(/data) 或 PostgreSQL    │
                  │ Cron 每日备份数据库              │
                  └────────────────────────────────┘
```

容器内入口端口固定为 `10761`。项目不在容器内处理 HTTPS；如需公网 HTTPS，请在外部使用 Caddy、Nginx Proxy Manager、Nginx 或云厂商负载均衡终止 TLS，再反代到 `http://127.0.0.1:10761`。

## Docker Compose 部署（推荐）

### 1. 获取部署文件

```bash
git clone https://github.com/xiangnan0811/xirang.git
cd xirang
```

如果你不需要完整源码，也可以只复制发布包或仓库中的以下文件到服务器同一目录：

- `docker-compose.yml`
- `.env.deploy`

### 2. 准备 `.env`

```bash
cp .env.deploy .env
```

至少填写这三个必填项：

```env
ADMIN_INITIAL_PASSWORD=<首次登录 admin 的强密码>
JWT_SECRET=<至少 16 字符的强随机字符串>
DATA_ENCRYPTION_KEY=<强随机加密密钥>
```

生产环境建议同时固定镜像版本：

```env
IMAGE_TAG=vX.Y.Z
```

常用部署项：

```env
APP_ENV=production
TZ=Asia/Shanghai
DB_TYPE=sqlite
SQLITE_PATH=/data/xirang.db
```

完整变量说明见 [环境变量参考](env-vars.md)。

### 3. 启动服务

```bash
docker compose pull
docker compose up -d
```

检查状态：

```bash
docker compose ps
docker compose logs --tail=200 xirang
curl -fsS http://127.0.0.1:10761/healthz
```

首次登录：

- 地址：`http://<server>:10761`
- 用户名：`admin`
- 密码：`.env` 中的 `ADMIN_INITIAL_PASSWORD`

### 4. 可选：外部反向代理与 HTTPS

Xirang 容器只提供 HTTP 单入口。生产公网访问建议在同机或前置网关部署外部反向代理：

```text
https://xirang.example.com ──> 外部反向代理 ──> http://127.0.0.1:10761
```

反代需要保留 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto`，并支持 WebSocket Upgrade。使用外部域名时，在 `.env` 中设置：

```env
CORS_ALLOWED_ORIGINS=https://xirang.example.com
```

## Docker Run 部署

```bash
cp .env.deploy .env
mkdir -p data backups logs

docker run -d \
  --name xirang \
  --restart unless-stopped \
  -p 10761:10761 \
  -v "$(pwd)/data:/data" \
  -v "$(pwd)/backups:/backup" \
  -v "$(pwd)/logs:/logs" \
  --env-file .env \
  docker.io/linnea7171/xirang:vX.Y.Z
```

## PostgreSQL 部署

默认使用 SQLite，适合小规模单机部署。如需 PostgreSQL，在 `.env` 中设置：

```env
DB_TYPE=postgres
DB_DSN=postgresql://user:pass@host:5432/xirang?sslmode=require
```

后端会在 PostgreSQL DSN 未显式设置 `timezone` / `TimeZone` 时追加 `timezone=UTC`，确保时间戳按 UTC 读写。

## 升级与回滚

### 升级到稳定版

1. 阅读目标版本的 GitHub Release 和 `CHANGELOG.md`。
2. 备份数据库和 `.env`。
3. 修改 `.env`：

```env
IMAGE_TAG=vX.Y.Z
```

4. 拉取并重启：

```bash
docker compose pull
docker compose up -d
```

5. 检查健康状态和日志：

```bash
curl -fsS http://127.0.0.1:10761/healthz
docker compose logs --tail=200 xirang
```

### 回滚到旧版本

回滚镜像版本：

```bash
IMAGE_TAG=vX.Y.Z docker compose pull
IMAGE_TAG=vX.Y.Z docker compose up -d
```

如果新版本已经执行数据库迁移，回滚前请确认旧版本是否兼容当前 schema。无法确认时，优先恢复升级前数据库备份。

## 数据目录与备份

Docker Compose 默认持久化目录：

| 宿主机路径 | 容器路径 | 用途 |
|---|---|---|
| `./data` | `/data` | SQLite 数据库及应用数据 |
| `./backups` | `/backup` | 自动/手动备份文件 |
| `./logs` | `/logs` | 应用日志与 Nginx 访问/错误日志 |

容器内置 cron：

| 时间 | 操作 |
|---|---|
| 每日 02:00 | 执行 `backup-db.sh` 备份数据库到 `/backup/db/` |
| 每日 02:30 | 清理 30 天前的旧备份文件 |

### 手动备份与恢复

SQLite：

```bash
DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/backup-db.sh ./backups

DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/restore-db.sh ./backups/xirang-sqlite-20260301-020000.db
```

PostgreSQL：

```bash
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/backup-db.sh ./backups

DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/restore-db.sh ./backups/xirang-postgres-20260301-020000.dump
```

## 健康检查与日志

```bash
# 容器状态
docker compose ps

# 实时 stdout 日志
docker compose logs -f xirang

# 最近 200 行 stdout 日志
docker compose logs --tail=200 xirang

# 持久化日志文件
ls -lah ./logs
tail -f ./logs/xirang.log

# 宿主机健康检查
curl -fsS http://127.0.0.1:10761/healthz
```

进入容器排查：

```bash
docker exec -it xirang bash
```

查看 SQLite 表数据示例：

```bash
docker exec -it xirang sh -lc \
  "sqlite3 /data/xirang.db 'SELECT count(*) FROM tasks;'"
```

## Prometheus `/metrics`

`/metrics` 是后端进程提供的 Prometheus 指标端点。生产环境建议配置随机 `METRICS_TOKEN`，否则端点保持公开兼容旧部署，但会暴露路由标签和流量画像。

All-in-One 镜像内置 Nginx 默认只代理 `/api/v1/*`、`/healthz` 和前端静态资源，不会暴露 `/metrics`。如果需要抓取指标，请在可信网络中抓取可直达的后端地址，或自行在外层反向代理中将 `/metrics` 转发到后端，并务必启用 token。

```bash
# 后端直连部署（例如源码运行 SERVER_ADDR=:8080）
curl -fsS http://127.0.0.1:8080/metrics | head

# 启用 token
curl -fsS -H "Authorization: Bearer ${METRICS_TOKEN}" http://127.0.0.1:8080/metrics | head
```

Prometheus 示例：

```yaml
scrape_configs:
  - job_name: xirang
    metrics_path: /metrics
    bearer_token_file: /etc/prometheus/secrets/xirang-metrics-token
    static_configs:
      - targets: ['backend-host:8080']  # 替换为 Prometheus 可访问的后端地址
```

详见 [监控、告警与状态页](admin/monitoring-alerting.md)。

## 迁移 dirty 状态排障

后端使用 golang-migrate 维护 `schema_migrations`。如果上一次迁移异常中断，启动日志可能出现：

```text
schema_migrations.dirty=1
```

默认情况下服务会拒绝启动，避免基于半完成 schema 继续写入数据。

处理步骤：

1. **先备份当前数据库文件或 PostgreSQL dump**，不要直接删除 `schema_migrations`。
2. 查看 dirty 版本：
   ```bash
   # SQLite
   sqlite3 ./data/xirang.db "SELECT version, dirty FROM schema_migrations;"

   # PostgreSQL
   psql "$DB_DSN" -c "SELECT version, dirty FROM schema_migrations;"
   ```
3. 根据失败点选择恢复方式：
   - 如果迁移明显只执行了一部分：恢复最近一次迁移前备份，再重新升级。
   - 如果确认迁移已完整执行，只是 clean 标记未写入：使用 golang-migrate CLI `force <version>` 标记 clean。
   - 如果需要短暂启动服务做人工修复，可临时设置 `ALLOW_DIRTY_STARTUP=true`；修复完成后必须移除该变量并重启。

`ALLOW_DIRTY_STARTUP=true` 只适合 rescue，不应作为长期配置。

## UTC 时间戳约定

当前后端使用 UTC 写入时间：

- GORM `NowFunc` 返回 UTC。
- SQLite DSN 包含 `_loc=UTC`。
- PostgreSQL DSN 默认追加 `timezone=UTC`。

新增 migration 不应使用 SQL `DEFAULT CURRENT_TIMESTAMP`、`datetime('now')`、`localtime` 或显式时区转换。涉及迁移文件时运行：

```bash
bash scripts/check-migration-utc-safety.sh
```

## 本地构建镜像（高级用户）

普通部署应优先使用官方预构建镜像。需要自行构建时：

```bash
docker build -f deploy/allinone/Dockerfile \
  -t docker.io/linnea7171/xirang:vX.Y.Z-local .
```

多架构构建：

```bash
docker buildx create --use

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f deploy/allinone/Dockerfile \
  -t docker.io/linnea7171/xirang:vX.Y.Z-local \
  --push .
```

本地构建默认不要推送或覆盖 `latest`；`latest` 只应由正式 GitHub Release 触发的发布 workflow 更新。
