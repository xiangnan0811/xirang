# 息壤 (Xirang)

[![CI](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml/badge.svg)](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

轻量、易部署的服务器运维管理平台。通过 Web 界面集中管理多台服务器的备份、任务调度和状态监控。

> 名字寓意来自《山海经》中的"息壤"：自适应增长、永不耗减。

---

## 亮点

- **无需 Agent** — 通过 SSH 管理目标服务器，无需在被管理节点安装任何软件
- **单容器部署** — 前端 + 后端 + Nginx 打包为一个 Docker 镜像，5 分钟完成部署
- **SQLite 开箱即用** — 默认零依赖，也可切换 PostgreSQL
- **多备份引擎** — 支持 Rsync / Restic / Rclone，按策略自动调度
- **全平台通知** — 邮件 / Webhook / 飞书 / 钉钉 / 企业微信 / Slack / Telegram

## 功能

### 节点管理

SSH 接入多台服务器，按探测间隔采集 CPU、内存、磁盘、负载等指标并生成状态与告警。
支持维护窗口设置、节点到期提醒、紧急备份触发，以及按分组批量管理。

### 备份策略

灵活的 cron 调度，支持 Rsync / Restic / Rclone 三种备份引擎。
提供策略模板与批量应用、前后置钩子脚本、带宽调度、保留策略、自动完整性校验。

### 任务编排

远程命令执行与批量操作，支持任务依赖链、失败自动重试与指数退避。
任务可随时暂停、跳过下次执行，运行历史与日志独立追溯。

### 文件浏览

基于 SFTP 的远程文件浏览器，支持快照版本对比与一键备份恢复。
同时提供本地备份目录的浏览与管理。

### Web 终端

浏览器内直接 SSH 连接服务器，基于 xterm.js，支持 30 分钟会话超时与完整审计记录。

### 监控与告警

全平台通知渠道：邮件、Webhook、飞书、钉钉、企业微信、Slack、Telegram。
告警自动去重与分级、投递状态追踪、失败批量重发，确保关键事件不遗漏。

### 安全

RBAC 权限控制，TOTP 两步验证（兼容 Google Authenticator 等应用）。
登录锁定与数学验证码防暴力破解，敏感字段（密码、私钥）全程加密存储。
操作审计日志支持哈希链防篡改，所有变更有据可查。

### 运维工具

SLA 报告按配置自动生成并推送，配置导入导出方便环境迁移。
内置数据库自助备份与恢复脚本、版本更新检查、新手引导向导，降低上手门槛。

---

## 部署

### 官方发布标准

- GitHub Release 是唯一权威公开版本源和变更说明源。
- Docker Hub 是唯一官方镜像源：`docker.io/xirang/xirang`
- `latest` 仅代表最新稳定版；生产环境建议显式固定到 `vX.Y.Z`

升级提示：
`SSH_AUTO_ACCEPT_NEW_HOSTS` 默认值为 `true`，首次连接的新主机密钥会被自动接受并写入 known_hosts，但已知主机密钥变更仍会被拒绝。如需禁用自动接受，请显式设置 `SSH_AUTO_ACCEPT_NEW_HOSTS=false`。

### Docker Compose（推荐）

```bash
# 1. 克隆本仓库或下载发布附带的部署文件
git clone https://github.com/xiangnan0811/xirang.git
cd xirang

# 2. 准备配置
cp .env.deploy .env
# 必填：
#   ADMIN_INITIAL_PASSWORD
#   JWT_SECRET
#   DATA_ENCRYPTION_KEY

# 生产环境建议固定稳定版标签
echo 'IMAGE_TAG=vX.Y.Z' >> .env

# 可选：启用版本检查
echo 'VERSION_CHECK_URL=https://api.github.com/repos/xiangnan0811/xirang/releases/latest' >> .env

# 3. 可选：启用 HTTPS
mkdir -p certs
cp /path/to/fullchain.pem certs/
cp /path/to/privkey.pem certs/
# 然后在 docker-compose.prod.yml 中取消注释 ./certs:/etc/nginx/certs:ro

# 4. 拉取并启动官方镜像
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
```

启用证书挂载后访问 `https://your-domain`，使用 `admin` 和设置的初始密码登录。未挂载证书时容器会自动使用 HTTP 模式，可先通过 `http://your-domain` 试用。

> `docker-compose.prod.yml` 默认读取仓库根目录 `.env`。默认使用 SQLite，无需额外配置数据库。如需 PostgreSQL，在 `.env` 中修改 `DB_TYPE=postgres` 并设置 `DB_DSN`。完整配置项见 [`.env.deploy`](.env.deploy)、[docs/env-vars.md](docs/env-vars.md) 和 [docs/deployment.md](docs/deployment.md)。
>
> `VERSION_CHECK_URL` 会让后台将当前构建版本与 GitHub latest release 比较；如果二进制或镜像构建时没有注入版本信息，当前版本会显示为 `dev`，检查结果只能作为开发提示。

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

### 从源码运行

需要 Go 1.26.2 或更新的兼容版本，以及 Node.js 20+：

```bash
# 终端 1 — 后端（:8080）
cd backend
ADMIN_INITIAL_PASSWORD='your-strong-password' APP_ENV=development \
go run ./cmd/server

# 终端 2 — 前端（:5173）
cd web && npm install && npm run dev
```

后端不会自动读取 `.env` 文件；如果你希望使用 `backend/.env.example`，请复制后在 shell 中显式加载，例如 `set -a; . ./.env; set +a`。

---

## 更新与回滚

```bash
# 升级到指定稳定版
# 编辑 .env，将 IMAGE_TAG 设置为目标稳定版，例如：
# IMAGE_TAG=vX.Y.Z
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d

# 或者临时指定版本回滚
IMAGE_TAG=vX.Y.Z docker compose -f docker-compose.prod.yml pull
IMAGE_TAG=vX.Y.Z docker compose -f docker-compose.prod.yml up -d
```

快速试用可以继续使用 `latest`，但生产环境建议固定到显式稳定版 tag。

---

## 数据与备份

容器使用 `/data`（数据库）和 `/backup`（备份）两个持久化卷。

内置定时任务每日 02:00 自动备份数据库，保留最近 30 天。也可手动操作：

```bash
# 备份（Docker Compose 默认 bind mount: ./data -> /data）
DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/backup-db.sh ./backups

# 恢复（自动生成回滚文件）
DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/restore-db.sh ./backups/xirang-sqlite-20250301-020000.db
```

---

## 常见问题

**Q: 支持哪些操作系统？**

被管理的节点只需支持 SSH 连接即可（Linux / macOS / BSD 等）。Xirang 服务端以 Docker 容器运行，支持 `linux/amd64` 和 `linux/arm64`。

**Q: 忘记管理员密码怎么办？**

SQLite 用户可以直接操作数据库重置；PostgreSQL 用户通过 SQL 更新 `users` 表。首次启动时密码由 `ADMIN_INITIAL_PASSWORD` 环境变量决定。

**Q: 如何从 SQLite 迁移到 PostgreSQL？**

导出 SQLite 数据，修改 `DB_TYPE=postgres` 和 `DB_DSN`，重启后导入。后端启动时会自动执行数据库迁移。

**Q: 如何启用两步验证？**

登录后进入设置 > 个人，开启 TOTP 两步验证，支持 Google Authenticator 等应用扫码绑定。

**Q: 支持哪些通知渠道？**

邮件、Webhook、Slack、Telegram、飞书、钉钉、企业微信，可在设置 > 通知渠道中配置。

---

## 相关链接

- [贡献指南](CONTRIBUTING.md)
- [安全政策](SECURITY.md)
- [部署指南](docs/deployment.md)
- [环境变量参考](docs/env-vars.md)
- [维护者发布手册](docs/release-maintainers.md)

## 许可证

[MIT](LICENSE)
