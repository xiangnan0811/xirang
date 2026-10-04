# Xirang 项目架构导航

本文是实现与合同的导航，不是另一套开发规范，也不代表功能已经完成端到端验收。审计证据、限制与后续修复范围见 [体验审计报告](UX_AUDIT_REPORT.md)。

## 系统边界

```text
React 控制台
  ├─ HTTP API ──────── Go / Gin Core
  └─ WebSocket ─────── 日志、终端等实时路径
                         ├─ GORM / SQLite、PostgreSQL
                         ├─ 调度、任务执行、自动化派发
                         ├─ 凭据、授权、审计
                         └─ 节点与备份存储适配
```

这是职责示意，不是集群部署、高可用、安全隔离或恢复能力的验收证明。具体准入、持久化与失败语义以领域合同为准。

## 实现入口

| 范围 | 入口 | 说明 |
| --- | --- | --- |
| 前端路由 | [router.tsx](web/src/router.tsx) | 页面与嵌套备份路由；访问目标不等于全部页面状态 |
| 前端技术依赖 | [package.json](web/package.json)、[锁文件](web/package-lock.json) | React、Vite、TypeScript、Tailwind、Radix、React Router、Vitest、Playwright；不在本页复制易过时的版本表 |
| 后端工具链 | [go.mod](backend/go.mod)、[贡献指南](CONTRIBUTING.md) | Go 版本、开发命令与验证入口 |
| HTTP 路由 | [router.go](backend/internal/api/router.go) | API 注册与权限中间件 |
| 自动化执行 | [dispatcher.go](backend/internal/automation/dispatcher.go) | 已有逐规则持久日志和 durable effect 路径；查询展示缺口见审计报告 |
| SLA 报表 | [generator.go](backend/internal/reporting/generator.go)、[前端 API](web/src/lib/api/reports-api.ts) | 已计算及映射 RPO/RTO；不得把前端未展示误写成后端没有聚合 |
| 实时重连 | [reconnecting-socket.ts](web/src/lib/ws/reconnecting-socket.ts) | 日志及终端复用指数退避、jitter 与重试上限 |

## 页面拓扑

以路由源码为准；页签是否可用还取决于页面、角色和功能条件。

```text
/login
/app
  ├─ overview
  ├─ nodes
  ├─ nodes/:id
  ├─ ssh-keys
  ├─ policies
  ├─ backups
  │    ├─ overview
  │    ├─ data
  │    └─ recovery
  ├─ tasks
  ├─ logs
  ├─ notifications
  ├─ automation-rules
  ├─ audit
  ├─ credential-audit
  ├─ credential-access-grants
  ├─ credentials
  ├─ reports
  ├─ settings
  └─ more
```

路由树另有重定向与未找到页面分支。审计的 32 个路由/页签目标是特定采样清单，不应表述为全部路由、权限组合及业务生命周期的覆盖证明。

## 合同导航

| 任务 | 权威主文 |
| --- | --- |
| 环境、命令、分支和 PR | [贡献指南](CONTRIBUTING.md) |
| 组件、Hook、状态、类型 | [前端合同](docs/spec/frontend/README.md) |
| 可访问性与验证边界 | [可访问性合同](docs/spec/frontend/a11y-guidelines.md)，当前目标 WCAG 2.1 AA；额外扫描 2.2 标签不等于升级合同 |
| 颜色、动效与页面组合 | [设计系统](docs/spec/guides/design-system.md) |
| 后端、数据库、迁移 | [后端合同](docs/spec/backend/README.md)、[后端说明](backend/README.md) |
| 凭据、step-up、临时授权和审计 | [凭据与访问](docs/spec/domains/credentials-access.md)；管理员授权列表为只读入口 |
| 任务、恢复、演练、RPO/RTO | [任务执行与恢复](docs/spec/domains/task-execution-recovery.md) |
| 告警与健康 | [告警与健康](docs/spec/domains/alerting-health.md) |
| 备份仓库、Catalog、内容交付及生命周期 | [领域合同索引](docs/spec/domains/README.md) |
| 验证与独立审查 | [代理协作与验证](docs/spec/guides/agent-collaboration.md) |

私有代理目录不作为架构组成、合同来源或干净 checkout 的运行依赖。审计原始文件的获取与指纹核对见报告；长期规范不在此记录代理阶段、成员名称或临时任务状态。

返回 [文档总入口](docs/README.md)。
