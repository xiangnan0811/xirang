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

`docker-compose.yml` 通过 `env_file: .env` 注入配置；**缺少 `.env` 时 `docker compose config` / `up` 会失败**，不会静默回退到空密钥默认值。

生产环境启动前至少填写以下密钥（未声明 `APP_ENV=development` 时，缺 `JWT_SECRET` / `DATA_ENCRYPTION_KEY` / `METRICS_TOKEN` 会拒绝启动）：

```env
# 仅首次启动且库中尚无 admin 用户时需要；已有 admin 后可留空或删除
ADMIN_INITIAL_PASSWORD=<首次登录 admin 的强密码>
JWT_SECRET=<至少 32 字符的强随机字符串>
DATA_ENCRYPTION_KEY=<至少 16 字符的强随机加密密钥>
METRICS_TOKEN=<至少 16 字符的强随机 token，禁止文档占位符；可用 openssl rand -hex 32 生成>
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
# 若前端构建时使用了外部 VITE_WS_URL，需在 Nginx CSP 中追加允许的 connect-src：
# CSP_CONNECT_SRC_EXTRA=wss://ws.example.com
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

### 4. 可选：本地备份资产 Worker profile（非 GA）

仓库根 Compose 提供 `asset-worker` 可选 profile，用于在本机从源码构建和验证 parser Worker 与独立 updater。该能力当前**不是 GA**，没有稳定公共 Worker 镜像，也不会发布到 Docker Hub 或 GitHub Release。它使用本地镜像名 `xirang-asset-worker:${ASSET_WORKER_IMAGE_TAG:-local}`；官方 Core 仍是 `linnea7171/xirang:${IMAGE_TAG:-latest}`，公开端口仍只有 `10761`。

普通 `docker compose up -d` 不会启动 profile 服务，Catalog、元数据搜索、Content Broker、workspace、原生预览、下载和 recovery 继续工作。未部署 Worker、没有 active verified bundle 或 capability 不匹配时，增强处理显示 `not_deployed`/`unsupported`，不会制造备份失败或告警。

Profile 固定使用以下本地身份和权限合同：

| 对象 | 身份/模式 | 说明 |
|---|---|---|
| parser Worker | UID/GID `10000:10000` | non-root、read-only rootfs、无网络/DNS、只读 bundle mount |
| updater | UID/GID `10002:10002` | 独立 UDS 与 PID namespace；只有它可写 bundle store |
| parser socket volume | `asset-worker-worker-runtime` | parser 只读挂载到 `/run/xirang/worker`；不包含 updater socket |
| updater socket volume | `asset-worker-updater-runtime` | updater 只读挂载到 `/run/xirang`；不包含 parser socket volume |
| bundle root | `10002:10000`, mode `2750` | updater owner 可写；Worker group 可读，但 parser mount 强制只读 |
| Derived Store volume | `asset-worker-derived-store`, `10000:10000`, mode `0700` | 仅 Core 与 initializer 挂载到 `/var/lib/xirang-asset-runtime/derived`；parser/updater 不可见 |
| inbox 目录 | `10002:10002`, mode `0555` | updater-only、只读、不得是符号链接 |
| trust 文件 | `10002:10002`, mode `0440` | updater-only Ed25519 公钥集合，不得是符号链接 |

Core 同时挂载 updater runtime 和嵌套的 Worker runtime，以分别创建 mode `0660`（`10000:10002`）与 `0600`（`10000:10000`）的 UDS。`/run/xirang` 使用 setgid mode `2770`，让 Core 创建的 updater socket 继承 GID `10002`；parser 不加入该组，也完全不挂载 updater runtime。反向同样成立：updater 不挂载 `asset-worker-worker-runtime`，因此两个进程都不能观察或连接对方的 socket。

Core、parser 和 updater 保持各自独立的 PID namespace。Linux 跨 PID namespace 的 `SO_PEERCRED` 可能返回 peer PID `0`；PID 只作为诊断元数据，不参与授权。Core 的 updater listener 依赖受保护的 UDS，并在解码 receipt 前校验精确 UID/GID 与 socket owner/mode。

`asset-worker-init` 还会把独立 Derived Store volume 初始化为 `0700:10000:10000`。该 volume 持久保留 Core 加密产物并与 `/data`、`/backup`、`/logs` 以及所有 Provider 源隔离；parser 和 updater 都不挂载它。

先准备固定 inbox 和 trust 文件。Trust 文档只包含公钥与 UTC 生效/退役时间；不要把私钥或在线凭据放入该文件：

```json
{
  "schema_version": 1,
  "keys": [
    {
      "id": "operator-key-1",
      "public_key": "<base64-ed25519-public-key>",
      "active_from": "<RFC3339-UTC>",
      "retire_after": "<RFC3339-UTC>"
    }
  ]
}
```

```bash
mkdir -p asset-worker-inbox
sudo chown 10002:10002 asset-worker-inbox asset-worker-updater-trust.json
sudo chmod 0555 asset-worker-inbox
sudo chmod 0440 asset-worker-updater-trust.json
```

在 `.env` 中显式启用全局 feature、本机 UDS 和独立 updater，并让加密 Derived Store 使用 profile 提供的专用 volume 与默认路径。`/data`、`/backup` 和 `/logs` 及其子路径会被 private-runtime guard 拒绝，不能用作 Derived Store。Settings 数据库覆盖优先于环境变量；若已有同名 DB override，必须在设置界面同步更新或删除旧覆盖值：

```env
BACKUP_ASSETS_ENABLED=true
BACKUP_ASSETS_WORKER_LOCAL_ENABLED=true
BACKUP_ASSETS_WORKER_LOCAL_SOCKET=/run/xirang/worker/asset-worker.sock
BACKUP_ASSETS_WORKER_UPDATER_ENABLED=true
BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED=false
BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY=false
BACKUP_ASSETS_PROCESSING_BACKFILL_PAUSED=true
BACKUP_ASSETS_DERIVED_STORE_ROOT=/var/lib/xirang-asset-runtime/derived

ASSET_WORKER_IMAGE_TAG=local
ASSET_WORKER_INBOX_DIR=./asset-worker-inbox
ASSET_WORKER_UPDATER_TRUST_FILE=./asset-worker-updater-trust.json
```

仓库 profile 固定为 offline-only，并给 Worker 与 updater 都设置 `network_mode: none`。不要在该 profile 中启用 online updater；在线模式需要独立的 allowlist proxy/firewall、隔离网络和凭据 secret 合同，当前仓库 Compose 不提供这条部署路径。

Worker 的 job workspace 是敏感 `tmpfs`。仓库 Compose 已把 parser/updater 的 `memswap_limit` 设为与各自 `mem_limit` 相同，使这两个容器不能使用 swap；部署前仍应确认宿主 swap 已关闭，或只使用经过审计的全盘加密 swap。若改用 `docker run` 或外部编排，必须保留同等的 no-swap、memory、PID、read-only、no-network、seccomp 与 `noexec,nosuid,nodev` tmpfs 限制。

本地构建并启动：

```bash
docker compose --profile asset-worker build asset-worker
docker compose --profile asset-worker up -d
docker compose --profile asset-worker ps
docker compose --profile asset-worker logs --tail=200 asset-worker asset-worker-updater
```

以下变更需要重启 Core/profile：本机/远程 Worker enablement 与 socket/certificate/trust、updater enablement/online origins、Derived Store root/chunk，以及 inbox、trust 文件或 bundle mount。Backfill pause/quota 与有限秘密分类是动态设置；默认分别为 paused 与 disabled。

回退时先暂停 backfill，再关闭本机 Worker 与 updater 设置并重启 Core，然后停止可选服务：

```bash
docker compose --profile asset-worker stop asset-worker asset-worker-updater
docker compose up -d xirang
```

不要使用 `down -v` 作为功能回退，也不要删除 Provider bytes、RecoveryPoint、Catalog 或源备份。加密 Derived 数据和已验签 bundle 可保留给受控调和或后续重新启用；移除 profile 不影响原生预览、下载与 recovery。

### 5. 可选：外部反向代理与 HTTPS

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

# 持久化日志文件（Nginx 访问日志请求行会省略查询字符串）
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

`/metrics` 是后端进程提供的 Prometheus 指标端点。**除显式 `APP_ENV=development` 外必须配置随机 `METRICS_TOKEN`**（含未声明 APP_ENV），否则进程拒绝启动。仅开发环境可留空 token 以兼容本地抓取，但会暴露路由标签和流量画像，并周期性打 warn 日志。

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
