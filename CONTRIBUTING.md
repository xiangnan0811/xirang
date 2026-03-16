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
2. 基于 `master` 创建功能分支：`git checkout -b feat/your-feature`
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

## 开发环境搭建

参考 [README.md](README.md) 中的快速开始章节。

## 发布流程

本项目使用 [Release Please](https://github.com/googleapis/release-please) 自动管理版本和 CHANGELOG。合并到 `master` 的 PR 会自动触发版本发布流程。
