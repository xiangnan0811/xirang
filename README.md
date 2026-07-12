# 息壤 (Xirang)

[![CI](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml/badge.svg)](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

轻量、易部署的服务器运维管理平台。Xirang 通过 SSH 集中管理多台服务器，把备份可信度、恢复演练、节点诊断、监控告警、Web 终端和审计日志放在同一个可解释的运维闭环里。

> 名字寓意来自《山海经》中的“息壤”：自适应增长、永不耗减。

## 核心能力

- **Agentless 管理**：通过 SSH 接入目标服务器，无需在被管理节点安装 Agent。
- **单容器部署**：前端、后端和 Nginx 打包为 All-in-One 镜像，默认 SQLite 开箱即用，也支持 PostgreSQL。
- **可信备份与恢复**：支持 Rsync、Restic、Rclone，多级保留策略、恢复演练、快照浏览与文件搜索，并在控制台汇总备份可信度证据。
- **任务与自动化**：支持 cron 调度、任务依赖链、批量命令、失败重试、事件触发动作编排。
- **监控、告警与诊断**：节点资源采样、HTTP/TCP uptime 监控、异常检测、SSH Fleet Doctor，以及邮件/Webhook/Slack/Telegram/飞书/钉钉/企业微信通知。
- **安全与审计**：JWT、RBAC、TOTP 两步验证、登录防护、敏感字段加密、操作审计日志。

## 快速部署

推荐使用 Docker Compose 运行官方镜像 `docker.io/linnea7171/xirang`：

```bash
git clone https://github.com/xiangnan0811/xirang.git
cd xirang

cp .env.deploy .env
# 编辑 .env。生产/未声明 APP_ENV 时 JWT_SECRET、DATA_ENCRYPTION_KEY、METRICS_TOKEN 必填；
# ADMIN_INITIAL_PASSWORD 仅首次启动且库中尚无 admin 时需要：
# ADMIN_INITIAL_PASSWORD=<首次 admin 强密码>
# JWT_SECRET=<≥32 字符强随机字符串>
# DATA_ENCRYPTION_KEY=<≥16 字符强随机加密密钥>
# METRICS_TOKEN=<≥16 字符强随机 token，可用 openssl rand -hex 32>
# 生产环境建议固定稳定版：IMAGE_TAG=vX.Y.Z

docker compose pull
docker compose up -d
```

默认访问地址：`http://<server>:10761`。如需 HTTPS，请在外部使用 Caddy、Nginx Proxy Manager 或 Nginx 等反向代理终止 TLS。

首次登录用户名为 `admin`，密码为 `.env` 中的 `ADMIN_INITIAL_PASSWORD`。

完整部署、反向代理、升级回滚、备份恢复和排障步骤见 [部署指南](docs/deployment.md)。

## Demo 模式

前端可通过 `VITE_ENABLE_DEMO_MODE=true` 启用本地 mock 数据演示。Demo 仅用于了解可信备份、恢复演练、SSH 诊断和事件解释流程，不会连接真实服务器、SSH Key 或备份存储，也不代表托管演示服务。

## 从源码运行

需要 Go 1.26.3 或更新的兼容版本，以及 Node.js 20+。

```bash
# 终端 1：后端 (:8080)
cd backend
ADMIN_INITIAL_PASSWORD='LocalDev#2026' APP_ENV=development \
  go run ./cmd/server

# 终端 2：前端 (:5173)
cd web
npm install
npm run dev
```

后端不会自动读取 `.env` 文件；源码运行时请通过 shell、systemd 或 `docker run --env-file` 注入环境变量。

## 文档

- [文档索引](docs/README.md)
- [部署、升级与运维](docs/deployment.md)
- [环境变量参考](docs/env-vars.md)
- [备份、恢复与快照](docs/admin/backup-recovery.md)
- [监控、告警与状态页](docs/admin/monitoring-alerting.md)
- [自动化规则](docs/admin/automation.md)
- [贡献指南](CONTRIBUTING.md)
- [安全政策](SECURITY.md)

## 贡献与安全

欢迎通过 Issue 或 Pull Request 参与项目。提交前请阅读 [贡献指南](CONTRIBUTING.md)。

如果发现安全漏洞，请不要公开提交 Issue，按 [安全政策](SECURITY.md) 通过 GitHub Security Advisories 私下报告。

## 许可证

[MIT](LICENSE)
