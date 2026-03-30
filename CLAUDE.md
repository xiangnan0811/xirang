# CLAUDE（Claude Code）

Xirang（息壤）是一个服务器运维管理平台，前后端分离架构。

## 技术栈

- 后端：Go 1.24 + Gin + GORM + SQLite（支持 PostgreSQL）+ JWT 认证 + WebSocket
- 前端：React 18 + TypeScript + Vite 7 + Tailwind CSS 3 + Radix UI + React Router 6
- 测试：后端 `go test`，前端 Vitest + Testing Library
- 部署：Docker Compose，Makefile 快捷命令

## 核心领域模型

User, SSHKey, Node, Policy, PolicyNode, Integration, Alert, AlertDelivery, Task, TaskRun, TaskLog, TaskTrafficSample, NodeMetricSample, NodeOwner, AuditLog, ReportConfig, Report, LoginFailure, SystemSetting
- Node：服务器节点（SSH 连接、状态、磁盘指标、维护窗口、到期日、sudo、备份目录、ownership）
- Task：运维任务（rsync/restic/rclone/command 四种执行器，cron 调度，依赖链，暂停/跳过）
- Policy：备份策略（源/目标、cron、排除规则、带宽限制/调度、模板、前后置钩子、重试配置）
- Alert/Integration：告警 + 通知渠道（邮件/Webhook/Slack/Telegram/飞书/钉钉/企业微信，代理支持）
- TaskRun：任务执行记录（触发类型、状态、依赖链标识、吞吐量、进度）
- ReportConfig/Report：SLA 报告配置与生成结果
- NodeMetricSample：节点资源采样（CPU/内存/磁盘/负载）
- LoginFailure：登录失败记录（锁定机制）
- SystemSetting：系统设置（key-value 存储，覆盖环境变量）
- 敏感字段（密码、私钥、TOTP、端点、代理地址）通过 GORM hooks 自动加解密，不要手动处理

## 后端结构 (`backend/internal/`)

- `api/` — 路由 + handlers（RESTful，按资源拆分，含 30+ handler）
- `auth/` — JWT、密码哈希、登录锁定、TOTP 2FA、认证服务
- `middleware/` — 审计、鉴权、限流、RBAC、ETag、ownership 校验
- `task/` — 任务管理器 + scheduler + 状态机 + executor factory（rsync/restic/rclone/command）+ retention worker
- `alerting/` — 告警分发（邮件/Webhook/Slack/Telegram/飞书/钉钉/企业微信）
- `reporting/` — SLA 报告生成与调度
- `settings/` — 系统设置服务（DB 覆盖 → 环境变量 → 代码默认值）
- `model/` — GORM 模型定义（19 个模型）
- `config/`, `database/`, `secure/`, `sshutil/`, `util/`, `ws/`, `bootstrap/`

## 前端结构 (`web/src/`)

- `pages/` — 12 个页面：Overview, Nodes, SSHKeys, Policies, Backups, Logs, Notifications, Tasks, Audit, Reports, Settings, Login
- `components/` — 50+ 组件：编辑器对话框、文件浏览器、快照浏览/对比、终端、TOTP 设置、批量命令、报告配置、NAS 挂载向导、setup wizard、layout 等
- `hooks/` — 控制台数据、节点操作、集成告警操作等自定义 hooks
- `lib/api/` — 按资源拆分的 API 客户端（29 个模块）
- `lib/ws/` — WebSocket 日志推送 + 终端
- `context/` — AuthContext + ThemeContext
- 路由：`/login` + `/app/*`（lazy loading），ProtectedRoute 守卫

## 数据库迁移

当前版本：`000030_task_run_progress`（SQLite + PostgreSQL 双轨迁移）

## 环境变量

关键必填项（生产环境）：
- `ADMIN_INITIAL_PASSWORD`：首次启动必须设置，初始化 admin 账号密码
- `JWT_SECRET`：JWT 签名密钥（≥16 字符）
- `DATA_ENCRYPTION_KEY`：敏感字段加密密钥

完整参考见 `docs/env-vars.md`，示例文件：`backend/.env.example`、`web/.env.example`

## 开发与校验命令

```bash
# 后端
cd backend && go run ./cmd/server        # 启动（:8080）
cd backend && go test ./...              # 测试

# 前端
cd web && npm run dev                    # 启动（:5173）
cd web && npm run check                  # typecheck + test + build（提交前必跑）
cd web && node scripts/check-bundle-budget.mjs  # 包体积检查

# 开发启动
make backend-run   # 终端 1
make web-dev       # 终端 2

# Docker
docker compose up                        # 开发环境
make prod-up                             # 生产环境

# 端到端冒烟
bash scripts/smoke-e2e.sh
```

## 代码与变更约定

1. 默认使用简体中文沟通与注释（必要时保留英文术语）
2. 优先复用已有组件与工具，不引入无必要依赖
3. 修改应保持最小影响面，避免顺手重构无关代码
4. 前端改动需同步关注可访问性（`aria-*`、键盘可操作）
5. 任何"已完成/已修复"结论必须基于实际命令输出
6. API 路径前缀：`/api/v1`，前端通过 Vite proxy 转发
7. 新增页面需在 `router.tsx` 注册，使用 lazy loading
8. UI 组件优先复用 `components/ui/` 下已有组件

## 提交流程

1. 本地完成实现后先运行校验命令（前端 `npm run check`，后端 `go test ./...`）
2. 提交信息格式：`feat(web): ...` / `fix(backend): ...` / `chore(ci): ...`
3. 高风险变更（删除数据、变更鉴权、生产配置调整）需先说明风险与回滚方案再执行
