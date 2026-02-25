# AGENTS（Codex）

Xirang（息壤）是一个服务器运维管理平台，前后端分离架构。

## 项目级 vs 用户级

- 项目级 skill：仓库内 `.codex/skills/`，仅对当前项目生效。
- 用户级 skill：用户目录中的通用能力（跨项目复用）。
- 冲突处理：**项目级优先于用户级**。

## 技术栈

- 后端：Go 1.24 + Gin + GORM + SQLite（支持 PostgreSQL）+ JWT 认证 + WebSocket
- 前端：React 18 + TypeScript + Vite 7 + Tailwind CSS 3 + Radix UI + React Router 6
- 测试：后端 `go test`，前端 Vitest + Testing Library
- 部署：Docker Compose，Makefile 快捷命令

## 核心领域模型

User, Node, SSHKey, Policy, Task, Alert, Integration, AuditLog, TaskLog
- Node：服务器节点（SSH 连接信息、状态、磁盘指标）
- Task：运维任务（rsync 同步 / 命令执行，支持 cron 调度）
- Policy：备份策略（源/目标路径、cron、排除规则、带宽限制）
- Alert/Integration：告警 + 通知渠道（webhook 等）
- 敏感字段（密码、私钥）通过 GORM hooks 自动加解密，不要手动处理

## 后端结构 (`backend/internal/`)

- `api/` — 路由 + handlers（RESTful，按资源拆分）
- `auth/` — JWT、密码哈希、登录锁定、认证服务
- `middleware/` — 审计、鉴权、限流、RBAC
- `task/` — 任务管理器 + executor + scheduler + 状态机
- `alerting/` — 告警分发
- `model/` — GORM 模型定义
- `config/`, `database/`, `secure/`, `sshutil/`, `util/`, `ws/`, `bootstrap/`

## 前端结构 (`web/src/`)

- `pages/` — 9 个页面：Overview, Nodes, SSHKeys, Policies, Logs, Notifications, Tasks, Audit, Users
- `components/` — 编辑器对话框 + layout（AppShell/Sidebar/MobileNav）+ UI 基础组件
- `hooks/` — 控制台数据、节点操作、集成告警操作等自定义 hooks
- `lib/api/` — 按资源拆分的 API 客户端（client.ts + core.ts + 各资源 api）
- `lib/ws/` — WebSocket 日志推送
- `context/` — AuthContext + ThemeContext
- 路由：`/login` + `/app/*`（lazy loading），ProtectedRoute 守卫

## 代码与变更约定

1. 默认使用简体中文沟通与注释（必要时保留英文术语）
2. 优先复用已有组件与工具，不引入无必要依赖
3. 修改应保持最小影响面，避免顺手重构无关代码
4. 前端改动需同步关注可访问性（`aria-*`、键盘可操作）
5. 任何“已完成/已修复”结论必须基于实际命令输出
6. API 路径前缀：`/api/v1`，前端通过 Vite proxy 转发
7. 新增页面需在 `router.tsx` 注册，使用 lazy loading
8. UI 组件优先复用 `components/ui/` 下已有组件

## 按需加载（Skill）

- 开发流程与校验：`.codex/skills/xirang-dev-workflow/SKILL.md`
- 环境变量与配置：`.codex/skills/xirang-env-config/SKILL.md`
- 高风险操作管控：`.codex/skills/xirang-risk-control/SKILL.md`
