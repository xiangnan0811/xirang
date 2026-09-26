# 贡献指南

欢迎通过 [Issues](https://github.com/xiangnan0811/xirang/issues) 或 Pull Request 参与。
请选择部署、备份恢复、SSH 诊断、功能建议或通用问题模板，提供脱敏后的版本、
复现步骤和相关日志；安全漏洞按 [安全政策](SECURITY.md) 私下报告。
社区交流遵循 [行为准则](CODE_OF_CONDUCT.md)。

开发合同从 [docs/spec](docs/spec/README.md) 按任务读取；代理加载范围见
[项目入口](AGENTS.md)。无需安装个人 harness、创建任务目录、规划审批或例行日志。

## 开发环境

后端使用 Go / Gin / GORM，前端使用 React / TypeScript / Vite / Tailwind CSS。
版本以 `backend/go.mod`、`web/package.json` 及锁文件为准。需要 Go 1.27.1
或兼容版本、SQLite 驱动所需 C 编译工具链（CGO），以及 Node.js
20.19+（20.x）、22.13+（22.x）或 24+ 和 npm。文档结构检查使用 Python 3.9+
（仅标准库）。升级工具链后也须确认 linter 兼容。

本地 Go 1.27 可直接运行 `make lint-backend` 或推送前门禁。两者通过
`scripts/lint-backend.sh` 使用当前 Go 工具链构建并运行与 CI 对齐的固定 linter，
不依赖 PATH 中旧版 `golangci-lint`，也不强制降级 Go。首次执行需要下载工具依赖，
后续复用 Go 缓存；版本化 `go run` 不修改项目 `go.mod` / `go.sum`。

两个终端均从仓库根目录开始：

```bash
# 终端 1：后端 (:8080)
cd backend
ADMIN_INITIAL_PASSWORD='LocalDev#2026' APP_ENV=development go run ./cmd/server

# 终端 2：前端 (:5173)
cd web
npm ci
npm run dev
```

后端不自动读取 `.env`，通过 shell、systemd 或容器注入变量；默认值、优先级和
生效条件见 [环境变量](docs/env-vars.md)。不要将示例开发密码用于真实实例。

仅查看界面时，可在 `web/` 安装依赖后运行
`VITE_ENABLE_DEMO_MODE=true npm run dev`。Demo 使用本地 mock，不连接真实服务器
或备份存储，仅用于开发演示；生产构建禁止启用，不能作为真实备份或恢复证据。

首次 clone 后从根目录运行 `make setup-hooks`。它启用暂存内容快检、文档同步、
适用迁移检查和 pre-push 完整门禁。不要绕过 hooks 或 required CI。

## 常用命令

下表无特别说明时均从 checkout 根目录运行。

| 目的 | 命令 |
| --- | --- |
| 启动、构建后端 | `make backend-run`、`make backend-build` |
| 后端测试 | `make backend-test` |
| 更新 OpenAPI/Swagger 生成物 | `make swag-init` |
| 前端完整检查 | `(cd web && npm run check)` |
| 前端逐项检查 | 在 `web/` 执行 `npm run typecheck`、`npm run lint`、`npm run test`、`npm run build` |
| 项目 lint、测试和构建 | `make check` |
| 仅 lint、覆盖率 | `make lint`、`make coverage` |
| 单架构、多架构镜像 | `make docker-build`、`make docker-buildx` |
| 文档新鲜度及结构 | `bash scripts/check-doc-freshness.sh` |
| 文档检查回归 | `bash scripts/check-doc-freshness.test.sh`、`python3 scripts/check-doc-structure.test.py` |
| 迁移版本及回归 | `bash scripts/check-migration-version.sh`、`bash scripts/check-migration-version.test.sh` |
| 迁移 UTC 安全及回归 | `bash scripts/check-migration-utc-safety.sh`、`bash scripts/check-migration-utc-safety.test.sh` |
| 推送前完整门禁 | `bash scripts/local-ci-parity.sh` |

前端 `npm run check` 包括 typecheck、lint、测试与构建；pre-push 还执行后端 lint、
测试、构建、漏洞检查、前端依赖审计、bundle budget 和文档/迁移检查。
CI 另有 PostgreSQL parity、选定 race、浏览器验收、覆盖率和 Docker 运行时检查。
本地通过不能替代 CI 或真实环境验收。测试证据的选择见
[测试约定](docs/spec/guides/testing.md)。

## 分支与提交

`main` 是跟踪 `origin/main` 的集成分支。功能、修复、文档、配置、CI 和规范变更
都须在工作分支完成；`main` 仅允许只读检查、fetch、快进同步、创建分支与合并后同步。
开始新的文件修改前：

```bash
git fetch origin --prune
git switch main
git pull --ff-only
git switch -c <type>/<short-description>
```

若 `main` 有本地独有提交，先解决其应进入工作分支、PR 或经明确授权丢弃的归属，
不要继续在 `main` 上修改。延续已授权工作时核验当前分支和远端基线，保留已有改动。
隔离 worktree 使用仓库内已忽略的 `.worktrees/<task-slug>`，创建前重新确认忽略规则。

提交消息与 PR 标题遵循 Conventional Commits：`<type>(<scope>): <description>`。
常用 type 为 `feat`、`fix`、`docs`、`chore`、`refactor`、`test`、`ci`；
scope 可用 `web`、`backend`、`deploy` 等。完成相关检查及候选审查后：

```bash
git add <files>
git commit -m "docs: 整合维护文档"
git push origin <branch>
```

独立审查、暂存与未暂存内容的证据绑定及原生加载验收见
[代理协作与验证](docs/spec/guides/agent-collaboration.md)。

## PR 与合并

1. 向 `main` 创建 PR，说明具体问题、最终行为、受影响合同、验证和未完成验收。
2. 负责人持续监控全部 required checks；失败在同一工作分支修复、推送并继续监控，
   直到通过或记录真实外部阻塞。失败、pending 或缺失时不得合并。
3. 维护者优先使用 squash merge，保留符合规范的 PR 标题；不要直接向 `main` 推送变更。
4. 合并后按 [发布手册](docs/maintainers/release.md) 检查 Release Please、正式发布、
   Docker 镜像和相关 Docker Hub 描述同步。未触发正式发布时，明确说明未预期产生
   GitHub Release 或 Docker Hub 镜像。
5. 将本地 `main` 同步到 `origin/main` 后再开始下一分支。Squash 后不要从旧工作分支
   开始新任务。

## 文档同步

维护正文统一简体中文，标识符、代码和必要英文术语保持原样；历史 CHANGELOG 与生成物
不批量翻译。文档唯一归属、主题同步和结构门禁见
[文档维护](docs/spec/guides/documentation-truth-guide.md)。
模型、API、路由、配置、迁移、发布与部署的改动需同步其对应主题主文；
无关文档的改动不能替代必要同步。迁移号只在 [后端 README](backend/README.md) 维护。

发布版本和镜像标准统一见 [发布手册](docs/maintainers/release.md)，
依赖、漏洞与 Actions 维护统一见 [仓库自动化](docs/maintainers/automation.md)。
