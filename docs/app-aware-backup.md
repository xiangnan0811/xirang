# 应用感知备份 (App-Aware Backup)

## 概述

应用感知备份让息壤能识别业务应用类型，在备份前自动执行数据库 dump 操作，
确保备份数据的一致性。支持 MySQL / PostgreSQL / MongoDB / Redis 以及
对应的 Docker 容器化部署。

## 快速开始

### 1. 创建凭据

在「凭据管理」页面创建应用凭据，填写数据库连接信息：
- 主机部署：填写 host、port、user、password
- Docker 部署：填写 container_name、user、password

### 2. 创建备份策略

在策略编辑页面选择「应用类型」和对应的凭据，息壤会自动注入正确的
dump 脚本到 pre-hook / post-hook。

### 3. 验证

触发一次备份任务，检查备份日志确认 dump 成功执行。

## 支持的 Profile

| Profile | 类型 | Pre-hook | Post-hook |
|---------|------|----------|-----------|
| mysql | 主机 | mysqldump --all-databases --single-transaction | 清理临时文件 |
| postgres | 主机 | pg_dumpall | 清理临时文件 |
| mongodb | 主机 | mongodump | 清理临时文件 |
| redis | 主机 | BGSAVE + cp RDB | 清理临时文件 |
| docker-mysql | 容器 | docker inspect + docker exec mysqldump | 清理临时文件 |
| docker-postgres | 容器 | docker inspect + docker exec pg_dumpall | 清理临时文件 |
| docker-mongodb | 容器 | docker inspect + docker exec mongodump | 清理临时文件 |
| docker-redis | 容器 | docker inspect + BGSAVE + docker cp | 清理临时文件 |

## 安全性

- 数据库密码加密存储在息壤数据库中
- API 响应不会返回明文密码
- 渲染后的 hook 脚本对拥有 RBAC 权限的用户可见

## 向后兼容

- 未选择应用类型的策略保持原有行为不变
- 用户可以手动编辑自动生成的 hook 脚本
- 旧版 `GET /api/v1/hook-templates` 端点仍然可用，但已标记为废弃，建议迁移到 `GET /api/v1/app-credentials/profiles`
