# 息壤 (Xirang)

[![CI](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml/badge.svg)](https://github.com/xiangnan0811/xirang/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

轻量、易部署的服务器运维管理平台。Xirang 通过 SSH 集中管理多台服务器，提供备份、任务调度、监控告警、Web 终端和审计能力。

> 名字寓意来自《山海经》中的“息壤”：自适应增长、永不耗减。

## 核心能力

- **Agentless 管理**：通过 SSH 接入目标服务器，无需在被管理节点安装 Agent。
- **单容器部署**：前端、后端和 Nginx 打包为 All-in-One 镜像，默认 SQLite 开箱即用，也支持 PostgreSQL。
- **备份与恢复**：支持 Rsync、Restic、Rclone，多级保留策略、恢复演练、快照浏览与文件搜索。
- **任务与自动化**：支持 cron 调度、任务依赖链、批量命令、失败重试、事件触发动作编排。
- **监控与告警**：节点资源采样、HTTP/TCP uptime 监控、异常检测，以及邮件/Webhook/Slack/Telegram/飞书/钉钉/企业微信通知。
- **安全与审计**：JWT、RBAC、TOTP 两步验证、登录防护、敏感字段加密、操作审计日志。

## 快速部署

推荐使用 Docker Compose 运行官方镜像 `docker.io/linnea7171/xirang`：

```bash
git clone https://github.com/xiangnan0811/xirang.git
cd xirang

cp .env.deploy .env
# 编辑 .env，至少填写：
# ADMIN_INITIAL_PASSWORD=<强密码>
# JWT_SECRET=<强随机字符串>
# DATA_ENCRYPTION_KEY=<加密密钥>
# 生产环境建议固定稳定版：IMAGE_TAG=vX.Y.Z

docker compose pull
docker compose up -d
```

默认访问地址：`http://<server>:10761`。如需 HTTPS，请在外部使用 Caddy、Nginx Proxy Manager 或 Nginx 等反向代理终止 TLS。

首次登录用户名为 `admin`，密码为 `.env` 中的 `ADMIN_INITIAL_PASSWORD`。

完整部署、反向代理、升级回滚、备份恢复和排障步骤见 [部署指南](docs/deployment.md)。

## 从源码运行

需要 Go 1.26.3 或更新的兼容版本，以及 Node.js 20+。

```bash
# 终端 1：后端 (:8080)
cd backend
ADMIN_INITIAL_PASSWORD='your-strong-password' APP_ENV=development \
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
