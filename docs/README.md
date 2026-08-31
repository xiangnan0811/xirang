# Xirang 文档

本文档目录面向部署、使用和维护 Xirang 的用户与维护者。README.md 只保留快速入口，详细说明集中在本目录。

## 部署与配置

- [部署、升级与运维](deployment.md)：Docker Compose、Docker Run、HTTPS、升级回滚、备份恢复、健康检查和迁移排障。
- [环境变量参考](env-vars.md)：后端、前端、All-in-One 镜像、数据库、安全、通知、指标等全部环境变量。

## 管理员指南

- [备份、恢复与快照](admin/backup-recovery.md)：备份引擎、应用感知备份、GFS/RPO/RTO、快照文件搜索与异常检测。生产环境恢复演练当前不可用。
- [监控、告警与状态页](admin/monitoring-alerting.md)：节点指标、HTTP/TCP uptime 监控、公开状态页、异常事件、告警通知和 Prometheus 指标。
- [自动化规则](admin/automation.md)：事件触发动作编排、规则配置、模板变量和当前限制。
- [安全加固](admin/security.md)：生产环境密钥、HTTPS、SSH 主机校验、Webhook SSRF 防护、TOTP、审计与敏感字段保护。

## 维护者与贡献者

- [维护者发布手册](maintainers/release.md)：Release Please、GitHub Release、Docker Hub 镜像发布、发布后监控和故障恢复。
- [贡献指南](../CONTRIBUTING.md)：Issue、Pull Request、提交规范、质量检查和文档同步要求。
- [安全政策](../SECURITY.md)：漏洞报告方式和支持范围。

## 文档维护原则

- 公开文档必须描述当前代码、配置、脚本和发布流程的真实状态。
- 过程文档、历史设计稿、任务计划、归档材料不进入公开 docs 目录。
- README 保持精简；部署、配置、排障和功能使用说明放在 docs 下。
- 新增或修改环境变量、路由、迁移、部署流程、发布流程时必须同步更新相关文档。
