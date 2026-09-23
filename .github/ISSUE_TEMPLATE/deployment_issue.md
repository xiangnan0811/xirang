---
name: 部署 / 升级问题
about: 反馈 Docker、源码运行、升级回滚或反向代理问题
title: '[Deploy] '
labels: deploy
assignees: ''
---

## 问题描述

请简要说明部署、升级或访问失败的现象。填写前可参考[部署指南](../../docs/deployment.md)与[环境变量](../../docs/env-vars.md)。

## 部署环境

- 部署方式：Docker Compose / Docker run / 源码运行
- Xirang 版本或提交 ID：
- 操作系统与架构：
- 数据库：SQLite / PostgreSQL
- 访问入口：默认 10761 / 外部反向代理 / 其他

## 已检查配置

- [ ] 首次启动且数据库尚无管理员时，已设置 `ADMIN_INITIAL_PASSWORD`
- [ ] `JWT_SECRET`、`DATA_ENCRYPTION_KEY` 符合环境变量文档的强度要求
- [ ] 生产环境已设置非占位符的强 `METRICS_TOKEN`
- [ ] 如使用 HTTPS，TLS 终止在外部反向代理

## 相关日志

请粘贴已脱敏的容器日志、后端日志或浏览器控制台错误。不要提交完整 `.env`、密钥、密码、令牌或 Cookie；安全漏洞按[安全政策](../../SECURITY.md)私下报告。

## 期望行为

描述你期望的部署或升级结果。
