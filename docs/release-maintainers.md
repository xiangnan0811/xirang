# 维护者发布手册

本文档面向 Xirang 维护者，定义公开发布、镜像发布和私有部署的标准流程。

## 发布标准

- GitHub Release 是唯一权威公开版本源和变更说明源。
- Docker Hub 是唯一官方公开镜像源。
- 当前仅支持稳定版 semver：`vX.Y.Z`。
- `latest` 仅表示最新稳定版；手动重发和恢复构建不得移动 `latest`。
- 私有部署不绑定公开 release 事件；部署仅通过手动 workflow 触发。

## Release Please 状态与基线

当前仓库已启用 Release Please，当前版本基线见 `.release-please-manifest.json`；
截至本次审查仓库内 manifest 为 `0.28.0`，`CHANGELOG.md` 已由 Release Please
维护。不要在文档中手写“当前版本”后长期依赖它；需要确认时以 manifest 和
GitHub Release 为准。

仅在重建发布链路、迁移仓库或重置首发版本时，才需要重新执行 bootstrap 检查：

1. 确认 `.release-please-manifest.json` 中的起始版本号就是你希望公开的首个稳定版。
2. 确认 `CHANGELOG.md` 已纳入仓库，并由 Release Please 接管。
3. 确认 `README.md`、`docs/deployment.md`、`docs/env-vars.md` 中的默认安装路径是 Docker Hub 预构建镜像，而不是本地 `docker build`。
4. 确认 Docker Hub 命名空间和 GitHub 仓库名已经最终确定，再向外公开 `VERSION_CHECK_URL` 示例。

若需要调整下一版号，不要手动打正式 tag；优先使用 Release Please 支持的 `Release-As:` 机制，或在明确重置基线时修改 `.release-please-manifest.json` 后等待/触发新的 Release PR。

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
2. 创建 PR 后，负责人必须监控 required CI jobs；失败时在同一工作分支修复、推送并重新监控。required checks 失败、pending 或缺失时不得合并。
3. PR 合并到 `main` 后，继续监控 `Release Please` workflow，确认它成功并按配置和提交语义创建或更新 Release PR。若 Release Please 只更新现有 Release PR 或未产生正式 release，需在交付记录中说明；不要把 post-merge 状态留空。
4. `release-please.yml` 使用 `RELEASE_PLEASE_TOKEN` 创建或更新 Release PR，确保 release 分支会触发 CI。
5. 审阅 Release PR，监控其 required checks，通过后合并。
6. GitHub 创建对应 `vX.Y.Z` Release。
7. `publish-images.yml` 监听 `release.published`，向 Docker Hub 发布：
   - `vX.Y.Z`
   - `X.Y.Z`
   - `latest`

   发布步骤为 **按平台原生构建 digest → Trivy 扫描每个平台 digest →
   合并并推送正式 multi-arch manifest/tag → attest**。扫描通过前不会创建
   正式 `vX.Y.Z` / `X.Y.Z` / `latest` 标签；当扫描到 HIGH/CRITICAL 漏洞时，
   workflow 会在 manifest/tag 发布前失败，不会污染 Docker Hub 的 `latest`
   标签。Trivy 当前固定到 v0.36.0 的解引用 commit
   `ed142fd0673e97e23eac54620cfb913e5ce36c25`，该 ref 已在 2026-05-06
   通过 `git ls-remote` 核验。维护者按需 bump（建议查看
   <https://github.com/aquasecurity/trivy-action/releases> 选择最新稳定 tag，并写入其
   解引用 commit 或可审计的 SHA pin）。
8. 监控 `Publish Docker Images` 直到成功；失败时优先修复自动链路，只有在符合“手动重发镜像”条件时才使用 `workflow_dispatch`。
9. 如需私有环境部署，由维护者手动运行 `deploy.yml`。

## PR 后监控要求

- PR 创建后，负责人必须监控 GitHub required checks，包括 `PR Title`、`Backend Test & Build`、`Frontend Test & Build`、`Doc Freshness Check`，以及当前 branch protection 要求的其他 jobs。
- CI 失败时，负责人应修复失败原因、推送到同一工作分支并重新监控；只有确认是真实外部阻塞时，才可把阻塞原因和下一步记录到 PR 或任务交付说明。
- 合并只能发生在 required checks 全部通过之后；不要在 checks 失败、pending 或缺失时合并。
- 普通 PR 合并后，负责人或 maintainer 必须检查 `Release Please` workflow 是否成功，并确认它是否创建或更新 Release PR。若本次合并不应触发正式 release，应明确记录“未触发正式 release”，并确认没有失败的 release automation 需要处理。
- Release PR 合并后，必须检查 GitHub Release 是否创建成功，并继续监控 `Publish Docker Images`。Docker Hub 仍是唯一官方公开镜像源；不要用其他 registry 或非稳定 tag 替代失败的正式发布链路。
- 公开 release tag 必须保持稳定 semver `vX.Y.Z`。不要发布 prerelease/nightly，不要手动创建绕过 GitHub Release 权威源的 Docker Hub tag。

## Docker Hub 仓库介绍同步

- 工作流：`.github/workflows/dockerhub-description.yml`
- 触发：
  - `README.md` 变更并合并到 `main`
  - 手动 `workflow_dispatch`
- 同步规则：
  - Docker Hub 短描述使用 GitHub 仓库 description
  - Docker Hub 长描述使用仓库 `README.md`
- 需要额外仓库 secret：
  - `DOCKERHUB_DESCRIPTION_PASSWORD`
  - `DOCKERHUB_DESCRIPTION_USERNAME`（可选；不设时回退到 `DOCKERHUB_USERNAME`）

说明：

- 当前用于镜像推送的 `DOCKERHUB_TOKEN` 可能没有 Docker Hub 仓库元数据编辑权限。
- 因此，仓库介绍同步与镜像推送使用分离凭据更稳妥。
- 如果缺少上述 metadata 凭据，workflow 会跳过同步而不是失败。

## 手动重发镜像

仅在以下情况使用 `publish-images.yml` 的 `workflow_dispatch`：

- Docker Hub 短暂故障导致推送失败
- 需要基于已有 tag 或 commit 重新推送稳定版镜像
- 需要补发 provenance / digest 记录

注意：

- 手动重发不会更新 `latest`；只会重发 `vX.Y.Z` 和 `X.Y.Z`
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
- 若平台 digest 已发布但正式 manifest/tag 未更新，优先修复发布 workflow 后按原版本号和原 tag 手动重发；手动重发不会覆盖 `latest`。
- 若正式 release run 仍在长时间运行但已明显超过近期成功发布耗时，不要同时发布新版本；先取消旧 run 或等待其结束，避免旧 release 延迟回写 `latest`。

### 版本检查提示异常

- 检查 `VERSION_CHECK_URL` 是否仍指向 GitHub latest release API。
- 检查返回 JSON 是否包含 `tag_name` 和 `html_url`。
- 检查 Release tag 是否保持稳定版 `vX.Y.Z` 格式。
- 检查构建产物是否注入了 `backend/internal/version` 中的当前版本；未注入时 `/api/v1/version` 会返回 `dev`，版本检查只能作为开发提示。
