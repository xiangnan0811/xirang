# 贡献指南

感谢你对 Xirang（息壤）的关注！欢迎提交 Issue、Pull Request 或参与讨论。

## 行为准则

请保持友善与尊重。我们致力于维护一个开放、包容的社区环境。

## 如何贡献

### 报告问题

- 使用 [GitHub Issues](../../issues) 提交 Bug 或功能建议
- 提交前请先搜索是否已有相同的 Issue
- Bug 报告请尽量包含：复现步骤、期望行为、实际行为、环境信息

### 提交代码

1. Fork 本仓库并 clone 到本地
2. 先同步 `main`，再基于 `main` 创建工作分支：`git checkout -b feat/your-feature`

> 不要直接在 `main` 分支提交。任何功能开发、问题修复、文档、配置、CI、Trellis 任务或流程规范变更，都应在独立工作分支完成，并通过 Pull Request 合入。

3. 完成开发后运行校验：

```bash
# 后端
cd backend && go test ./... && go build ./...

# 前端
cd web && npm run check   # typecheck + test + build
```

4. 提交代码并推送：

```bash
git add <files>
git commit -m "feat(web): 添加XX功能"
git push origin feat/your-feature
```

5. 在 GitHub 上发起 Pull Request

### Pull Request 标题与合并方式

- Pull Request 标题必须遵循 Conventional Commits；CI 会校验标题格式。
- Maintainer 合并到 `main` 时应优先使用 `Squash and merge`，并保留符合规范的 PR 标题，让最终进入发布分支的提交语义稳定可预测。
- Maintainer 也不要绕过 PR 直接在 `main` 提交功能或修复；如需维护仓库流程规范，也应走独立分支和 PR。
- 不要直接向 `main` 推送发布相关改动；正式版本通过 Release Please 生成的 Release PR 落地。

### Commit 规范

采用 [Conventional Commits](https://www.conventionalcommits.org/) 格式：

```
<type>(<scope>): <description>
```

- **type**: `feat` | `fix` | `docs` | `chore` | `refactor` | `test` | `ci`
- **scope**: `web` | `backend` | `ci` | `deploy` 等

示例：
- `feat(web): 添加节点批量操作功能`
- `fix(backend): 修复任务调度器内存泄漏`
- `docs: 更新部署文档`

### 代码规范

- 默认使用简体中文注释（必要时保留英文术语）
- 后端遵循 Go 标准代码风格
- 前端改动需关注可访问性（`aria-*`、键盘操作）
- 优先复用 `web/src/components/ui/` 下已有组件
- 不引入无必要的外部依赖

## 发布与镜像标准

- GitHub Release 是 Xirang 唯一权威公开版本源；`CHANGELOG.md` 由 Release Please 自动维护。
- Docker Hub 是唯一官方公开镜像源；`latest` 仅代表最新稳定版。
- 本仓库当前只支持稳定版 `vX.Y.Z` 发布，不支持 prerelease/nightly 通道。
- 公开发布流程不自动部署私有环境；`.github/workflows/deploy.yml` 仅用于维护者手动部署。
- 如修改 release/image/deploy/version-check 路径，必须同步更新 `README.md`、`docs/deployment.md`、`docs/env-vars.md` 以及维护者发布文档。

## 文档同步

PR 涉及以下变更时，请同步更新对应文档：

- 新增/修改数据库模型 → 更新 `CLAUDE.md` 核心领域模型
- 新增/修改 API 路由 → 更新 `backend/README_backend.md` 接口列表
- 新增/修改前端页面 → 更新 `CLAUDE.md` 前端结构
- 新增/修改环境变量 → 更新 `docs/env-vars.md` 和 `.env.example`
- 新增数据库迁移 → 更新 `CLAUDE.md` 迁移版本号

CI 中的 `doc-freshness` 检查会在关键文件变更但文档未同步时发出提醒。

## 开发环境搭建

参考 [README.md](README.md) 中的快速开始章节。

首次 clone 后请安装 Git hooks：

```bash
make setup-hooks
```

这会启用 pre-commit 文档新鲜度检查，在关键代码文件变更但文档未同步时阻止提交。

## 发布流程

本项目使用 [Release Please](https://github.com/googleapis/release-please) 自动管理版本和 `CHANGELOG.md`。合并到 `main` 的 PR 会自动更新或创建 Release PR；合并该 Release PR 后会创建 GitHub Release，并触发 Docker Hub 镜像发布。

维护者发布与仓库设置说明见 [docs/release-maintainers.md](docs/release-maintainers.md)。
