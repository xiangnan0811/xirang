# 息壤 (Xirang)

[![CI](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml/badge.svg)](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

轻量、可自托管的服务器运维管理平台。Xirang 通过 SSH 集中管理多台服务器，将备份执行、恢复点与文件管理、节点诊断、监控告警、Web 终端和审计日志整合到一个控制台。

[部署指南](docs/deployment.md) · [使用文档](docs/README.md) · [版本发布](https://github.com/xiangnan0811/xirang/releases) · [更新日志](CHANGELOG.md)

> 名字寓意来自《山海经》中的“息壤”：自适应增长、永不耗减。

## 核心能力

- **SSH 节点管理**：无需常驻 Agent，支持 SSH 密钥管理、连接诊断（Fleet Doctor）、Web 终端、批量命令和节点日志查看。备份节点仍需具备所选引擎的工具及权限。
- **备份策略与任务**：支持 Rsync、Restic、Rclone，以及数据库和 Docker 数据库的应用感知备份；提供 cron 调度、任务依赖、重试、保留策略和执行记录。
- **备份数据工作区**：通过「备份」下的概览、数据、恢复入口查看备份证据，按仓库和恢复点浏览、搜索文件，并在权限和 Provider 能力允许时预览、下载、导出或发起受控恢复。
- **监控与告警**：节点资源采样、HTTP/TCP 服务监控、自定义仪表盘、公开状态页、异常检测；支持邮件、Webhook、Slack、Telegram、飞书、钉钉和企业微信通知。
- **自动化与报表**：事件触发动作编排、备份健康与 RPO/RTO 报告，区分传输成功、恢复点发布和已验证备份证据。
- **安全与审计**：角色与节点访问控制、JWT 会话、TOTP 两步验证、登录防护、敏感字段加密、凭据访问授权及操作审计。
- **单容器部署**：前端、Go 后端和 Nginx 打包为 All-in-One 镜像，支持 Linux amd64 / arm64；默认 SQLite，也可连接 PostgreSQL。

### 备份与恢复的可用范围

备份资产功能默认关闭。启用前需完成库存盘点、导出根和密钥域就绪检查；已有安装还需管理员确认当前库存摘要。仅设置 `BACKUP_ASSETS_ENABLED=true` 不代表功能已生效。各引擎的版本化、浏览和恢复能力不同，具体条件见 [备份、恢复与快照](docs/admin/backup-recovery.md)。

生产环境的**恢复演练当前不可用**，历史演练结果仍可查看。可选的增强处理 Worker 尚非 GA，没有稳定公共镜像；核心工作区的原生预览、下载和恢复不依赖 Worker。

## 快速部署

使用 Docker Engine 和 Docker Compose 插件运行官方镜像 `docker.io/linnea7171/xirang`。以下为首次部署步骤；已有实例请先阅读 [升级与回滚说明](docs/deployment.md#升级与回滚)。

1. 获取部署文件：

```bash
git clone https://github.com/xiangnan0811/xirang.git
cd xirang

cp .env.deploy .env
```

2. 编辑 `.env`，填写以下配置。每个密钥应独立随机生成，例如使用 `openssl rand -hex 32`：

```dotenv
ADMIN_INITIAL_PASSWORD=<首次 admin 强密码>
JWT_SECRET=<至少 32 字符的强随机字符串>
DATA_ENCRYPTION_KEY=<至少 16 字符的强随机加密密钥>
METRICS_TOKEN=<至少 16 字符的强随机 token>
```

`.env.deploy` 默认使用生产模式，缺少必填密钥会拒绝启动。`ADMIN_INITIAL_PASSWORD` 仅首次启动且库中尚无 `admin` 时需要，不会重置已有密码。生产环境请从 [GitHub Releases](https://github.com/xiangnan0811/xirang/releases) 选择稳定版本，并在 `.env` 中设置对应的 `IMAGE_TAG=vX.Y.Z`；未设置时使用 `latest`。

3. 启动并检查服务：

```bash
docker compose pull
docker compose up -d
docker compose ps
curl -fsS http://127.0.0.1:10761/readyz
```

访问 `http://<server>:10761`，使用 `admin` 和配置的初始密码登录。生产访问请通过外部 Caddy、Nginx Proxy Manager 或 Nginx 终止 HTTPS；备份内容默认仅允许 HTTPS 交付。受控内网的临时 HTTP 例外见 [部署指南](docs/deployment.md#6-可选私网-http-备份内容)。

默认将应用数据、数据库备份和日志分别挂载到 `./data`、`./backups`、`./logs`。请保留这些持久化目录，并妥善备份数据库和 `DATA_ENCRYPTION_KEY`；完整持久化与升级要求见 [部署指南](docs/deployment.md)。

## 开始使用

1. 添加 SSH 密钥和节点，测试连接并检查主机密钥信任及执行权限。
2. 创建备份策略或任务，选择引擎、源和目标，先手动运行并查看执行记录。
3. 在备份概览检查就绪状态；需要数据工作区时，完成备份资产启用流程，再查看仓库、恢复点和文件。
4. 按需配置服务监控、通知渠道与自动化规则，并通过审计日志追踪操作。

## 从源码运行

后端使用 Go / Gin / GORM，前端使用 React / TypeScript / Vite / Tailwind CSS。开发环境需要：

- Go 1.26.6 或更新的兼容版本，以及 SQLite 驱动所需的 C 编译工具链（CGO）。
- Node.js 20.19+（20.x）、22.13+（22.x）或 24+，以及 npm；版本要求以当前锁文件为准。

以下两个终端均从仓库根目录开始：

```bash
# 终端 1：后端 (:8080)
cd backend
ADMIN_INITIAL_PASSWORD='LocalDev#2026' APP_ENV=development \
  go run ./cmd/server

# 终端 2：前端 (:5173)
cd web
npm ci
npm run dev
```

后端不会自动读取 `.env` 文件；源码运行时请通过 shell、systemd 或 `docker run --env-file` 注入环境变量。

### 本地 Demo

仅查看界面时，可在 `web/` 安装依赖后运行：

```bash
VITE_ENABLE_DEMO_MODE=true npm run dev
```

Demo 使用本地 mock 数据，不连接真实服务器或备份存储，仅用于开发演示。生产构建禁止启用 Demo；它不代表真实备份或恢复验收。

### 开发检查

从仓库根目录运行 `make check` 执行项目 lint、测试和构建；前端完整检查为 `(cd web && npm run check)`。协作流程、Git hooks 和 CI 要求见 [贡献指南](CONTRIBUTING.md)。

## 文档

- [文档索引](docs/README.md)
- [部署、升级与运维](docs/deployment.md)
- [环境变量参考](docs/env-vars.md)
- [备份、恢复与快照](docs/admin/backup-recovery.md)
- [监控、告警与状态页](docs/admin/monitoring-alerting.md)
- [自动化规则](docs/admin/automation.md)
- [安全加固](docs/admin/security.md)
- [贡献指南](CONTRIBUTING.md)
- [安全政策](SECURITY.md)

## 贡献与安全

欢迎通过 Issue 或 Pull Request 参与项目。提交前请阅读 [贡献指南](CONTRIBUTING.md)。

如果发现安全漏洞，请不要公开提交 Issue，按 [安全政策](SECURITY.md) 通过 GitHub Security Advisories 私下报告。

## 许可证

[MIT](LICENSE)
