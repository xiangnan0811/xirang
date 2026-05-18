---
name: 部署 / 升级问题
about: 反馈 Docker、源码运行、升级回滚或反向代理问题
title: '[Deploy] '
labels: deploy
assignees: ''
---

## 问题描述

请简要说明部署、升级或访问失败的现象。

## 部署环境

- 部署方式：Docker Compose / Docker run / 源码运行
- Xirang 版本或 Commit：
- 操作系统与架构：
- 数据库：SQLite / PostgreSQL
- 访问入口：默认 10761 / 外部反向代理 / 其他

## 已检查配置

- [ ] `ADMIN_INITIAL_PASSWORD` 已设置
- [ ] `JWT_SECRET` 已设置且不少于 16 字符
- [ ] `DATA_ENCRYPTION_KEY` 已设置
- [ ] 如使用 HTTPS，TLS 终止在外部反向代理

## 相关日志

请粘贴可脱敏的容器日志、后端日志或浏览器控制台错误。

## 期望行为

描述你期望的部署或升级结果。
