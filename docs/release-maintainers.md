# 维护者发布手册

本文档面向 Xirang 维护者，定义公开发布、镜像发布和私有部署的标准流程。

## 发布标准

- GitHub Release 是唯一权威公开版本源和变更说明源。
- Docker Hub 是唯一官方公开镜像源。
- 当前仅支持稳定版 semver：`vX.Y.Z`。
- `latest` 仅表示最新稳定版；手动重发和恢复构建不得移动 `latest`。
- 私有部署不绑定公开 release 事件；部署仅通过手动 workflow 触发。

## 首次公开发布 bootstrap

当前仓库已启用 Release Please，manifest 基线见 `.release-please-manifest.json`。

首次公开发布前请完成以下检查：

1. 确认 `.release-please-manifest.json` 中的起始版本号就是你希望公开的首个稳定版。
2. 确认 `CHANGELOG.md` 已纳入仓库，并由 Release Please 接管。
3. 确认 `README.md`、`docs/deployment.md`、`docs/env-vars.md` 中的默认安装路径是 Docker Hub 预构建镜像，而不是本地 `docker build`。
4. 确认 Docker Hub 命名空间和 GitHub 仓库名已经最终确定，再向外公开 `VERSION_CHECK_URL` 示例。

若需要调整首版号，不要手动打 tag；先修改 `.release-please-manifest.json`，再等待/触发新的 Release PR。

## GitHub 仓库设置

以下设置无法通过仓库文件强制，需要在 GitHub 仓库设置中手动启用：

- `main` 开启 branch protection。
- 禁止直接 push 到 `main`。
- 要求 CI 通过后才能合并。
- 默认使用 `Squash and merge`，关闭普通 merge commit。
- 合并后自动删除分支。

## 必要 Secrets / Variables

### 仓库级

- `RELEASE_PLEASE_TOKEN`（PAT，至少需要 `repo` 和 `workflow`；用于让 release-please 创建的分支正常触发 CI）
- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`
- `DOCKERHUB_NAMESPACE`（可用 variable；不设时回退到用户名）

### Deploy Environment 级

- `DEPLOY_HOST`
- `DEPLOY_USER`
- `DEPLOY_SSH_KEY`
- `DEPLOY_PATH`
- `DEPLOY_SSH_PORT`（可用 variable；不设时默认 22）

## 标准发布流程

1. 功能 PR 标题使用 Conventional Commits，合并到 `main` 时保持语义不变。
2. `release-please.yml` 使用 `RELEASE_PLEASE_TOKEN` 自动更新或创建 Release PR，确保 release 分支会触发 CI。
3. 审阅并合并 Release PR。
4. GitHub 创建对应 `vX.Y.Z` Release。
5. `publish-images.yml` 监听 `release.published`，向 Docker Hub 发布：
   - `vX.Y.Z`
   - `X.Y.Z`
   - `latest`
6. 如需私有环境部署，由维护者手动运行 `deploy.yml`。

## 手动重发镜像

仅在以下情况使用 `publish-images.yml` 的 `workflow_dispatch`：

- Docker Hub 短暂故障导致推送失败
- 需要基于已有 tag 或 commit 重新推送稳定版镜像
- 需要补发 provenance / digest 记录

注意：

- 手动重发不会更新 `latest`
- 手动重发不替代正式 GitHub Release
- 手动重发前必须确认 `version` 与 `source_ref` 对应的是同一份正式代码

## 手动部署

`deploy.yml` 是维护者私有运维入口，不属于公开发布主链。

使用原则：

- 手动选择 `environment`
- 显式填写 `image_tag`
- 默认优先部署具体稳定版 tag；`latest` 仅适合临时试用环境

## 变更同步要求

只要改动以下任一入口，就必须同步检查和更新文档、模板与规范：

- `.github/workflows/release-please.yml`
- `.github/workflows/publish-images.yml`
- `.github/workflows/deploy.yml`
- `docker-compose.prod.yml`
- `.env.deploy`
- `backend/.env.production.example`
- `backend/internal/api/handlers/version_handler.go`
- `README.md`
- `docs/deployment.md`
- `docs/env-vars.md`
- `AGENTS.md`

## 故障恢复

### Release Please 没有生成 Release PR

- 检查最近合并到 `main` 的 squash commit 是否仍符合 Conventional Commits。
- 检查 `release-please.yml` 是否有失败记录。
- 如需强制指定下个版本，优先通过 release-please 支持的 `Release-As:` 机制处理，不要手工打正式 tag。

### GitHub Release 已创建，但 Docker 镜像缺失

- 先检查 `publish-images.yml` 失败原因。
- 若只是推送瞬时失败，使用 `workflow_dispatch` 按原版本号和原 tag 重发。
- 若 digest 已发布但 `latest` 未更新，不要用手动重发覆盖；应修复正式 release 流程后重新发稳定版。

### 版本检查提示异常

- 检查 `VERSION_CHECK_URL` 是否仍指向 GitHub latest release API。
- 检查返回 JSON 是否包含 `tag_name` 和 `html_url`。
- 检查 Release tag 是否保持稳定版 `vX.Y.Z` 格式。
