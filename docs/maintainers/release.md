# 发布手册

## 公开发布合同

GitHub Release 是公开版本和变更说明的权威来源，Docker Hub 的 `docker.io/linnea7171/xirang` 是唯一官方公开镜像源。仅发布稳定 semver `vX.Y.Z`；正式镜像包含 `vX.Y.Z`、`X.Y.Z` 和 `latest`，后者表示最近正式发布的稳定版。手动重发不得移动 `latest`。

版本基线见 [.release-please-manifest.json](../../.release-please-manifest.json)，版本历史见 [CHANGELOG](../../CHANGELOG.md)。不要在多个维护文档中复制当前版本。需要调整下一版本时使用 Release Please 的 `Release-As:`，或经明确的基线重置修改 manifest；不要手动打正式 tag 绕过发布链路。

仅在重建发布链路、迁移仓库或重置首发版本时重新检查 bootstrap：manifest 起始版本、CHANGELOG 接管、README/部署/环境变量文档默认使用 Docker Hub 预构建镜像，以及 GitHub 仓库和 Docker Hub 命名空间已确定后再公布 `VERSION_CHECK_URL` 示例。

## 仓库设置与凭据

GitHub 设置应保护 `main`、禁止直接 push、要求 CI 通过、使用 squash merge 并自动删除合并分支；这些设置无法仅靠仓库文件保证。分支及 PR 操作以[贡献指南](../../CONTRIBUTING.md)为准。

| 范围 | 凭据或变量 | 用途 |
| --- | --- | --- |
| 仓库 Secret | `RELEASE_PLEASE_TOKEN` | 创建 Release PR 并允许后续 CI 触发；经典 PAT 需要 `repo`、`workflow` 权限 |
| 仓库 Secret | `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN` | 镜像发布及部署前登录 |
| 仓库 Secret | `DOCKERHUB_DESCRIPTION_PASSWORD` | 修改 Docker Hub 仓库元数据 |
| 仓库 Secret | `DOCKERHUB_DESCRIPTION_USERNAME` | 可选；回退到 `DOCKERHUB_USERNAME` |
| Deploy Environment Secret | `DEPLOY_HOST`、`DEPLOY_USER`、`DEPLOY_SSH_KEY`、`DEPLOY_PATH` | 手动部署目标 |
| Deploy Environment Variable 或 Secret | `DEPLOY_SSH_PORT` | variable 优先，其次 secret，默认 22 |
| Deploy Environment Variable | `IMAGE_WAIT_MAX_ATTEMPTS`、`IMAGE_WAIT_INTERVAL_SECONDS` | 等待镜像，默认分别为 90 次、10 秒 |

镜像发布和描述同步工作流固定官方仓库名，不读取命名空间变量。镜像推送 token 不一定拥有元数据编辑权限，两者使用独立凭据。

## Release PR 与镜像发布

普通 PR 的提交语义和合并前门禁由贡献指南规定。合并后检查 [Release Please](../../.github/workflows/release-please.yml) 是否成功创建或更新 Release PR；没有生成正式 release 时，交付说明明确记录没有预期的 GitHub Release 或 Docker Hub 发布，仍需处理自动化失败。

审阅 Release PR 时，将已交付的 `Unreleased` 条目纳入目标版本，补齐迁移、备份保全、旧进程排空和降级限制，不能只保留自动生成的 PR 标题。required checks 全部通过后合并，确认 GitHub Release 创建。

[Publish Docker Images](../../.github/workflows/publish-images.yml)监听 `release.published`，按以下顺序发布：

1. 从发布工作流自身的不可变提交加载验证策略，一次解析并冻结源码 SHA。
2. 等待同仓库 `main` 的同 SHA、`push` 事件、完整 `ci.yml` 成功，默认最多 60 分钟，为 PostgreSQL 等串行合同测试留出时间；验证任务超时为 65 分钟，预留 checkout 和 API 调用开销。缺失、未完成、失败、取消或超时均拒绝发布，新的失败运行不能被旧成功掩盖。
3. 在原生 amd64/arm64 runner 分别构建并按 digest 推送，再用显式 `TRIVY_PLATFORM` 扫描各自 digest。
4. 提升 multi-arch manifest/tag 前再次验证同 SHA CI；只有全部平台任务成功才发布正式标签。
5. 发布后生成 provenance attestation 和摘要，记录源码 SHA、CI run、平台 digest 与扫描结论。

当前 Trivy 设置为 `severity: HIGH,CRITICAL`、`exit-code: 1`、`ignore-unfixed: true`：扫描识别且已有修复版本的高危/严重漏洞阻断正式标签，未修复漏洞被过滤。不能将通过结果解释为不存在任何高危漏洞。基础镜像或包漏洞阻断时，更新来源并重新走 PR/release；不得降低 severity、添加临时 ignore 或绕过扫描。Actions pin 和依赖维护见[仓库自动化](automation.md)。

持续监控发布直至结束。标签推送发生在 attestation 之前，因此后置证明失败时可能已有公开镜像；核对失败步骤及 digest，不能把 workflow 失败等同于完全未发布。交付声明区分 GitHub Release、镜像、证明与实际部署结果。

## 升级说明必须覆盖的风险

根据候选改动查阅[任务与恢复等领域合同](../spec/domains/README.md)及[运维恢复手册](../admin/backup-recovery.md)，将受影响条款写进该版本说明：

- 数据库和加密密钥、备份副本及必要日志/游标的保全，旧 Core/调度器/执行器是否须排空。
- 新迁移的不可逆数据和降级保护；迁移号相同不证明执行器可安全降级。
- 恢复准入、未知写入 hold、人工协调及配置导入补偿的边界；不得用降级、删记录或改配置绕过保护。
- Core 与可选 Worker 的源码和工具链指纹一致性、文件系统隔离能力及容量要求。
- 节点日志采集窗口、历史不补采的边界，以及逐节点恢复验证要求。

历史版本的具体迁移编号和交付事件保留在 CHANGELOG/发行说明；当前迁移版本唯一声明在[后端入口](../../backend/README.md)。公开发布成功不代表生产已经升级、采集已经开启或真实恢复已经验收。

## Docker Hub 描述同步

[Sync Docker Hub Description](../../.github/workflows/dockerhub-description.yml)在 `README.md` 或该工作流文件变更并 push 到 `main` 时运行，也支持手动触发。短描述取 GitHub 仓库 description（为空时使用工作流默认文本），长描述取根 README。

缺少元数据凭据时任务会报告跳过而不是失败；绿色 workflow 不等于描述已更新。涉及 README/发布文档的交付需检查这项自动化的实际结果。

## 手动重发与私有部署

`publish-images.yml` 的 `workflow_dispatch` 仅用于已有稳定版本的推送故障恢复、重建或补发证明。填写 `version` 和 `source_ref`，人工确认二者对应同一正式代码；脚本验证来源 SHA 的主干 CI，但不会替代版本与源码对应关系的审阅。手动重发仅写版本标签，不移动 `latest`，也不替代 GitHub Release。

[Manual Deploy](../../.github/workflows/deploy.yml)只由手动触发，选择 `staging` 或 `production` 并填写 `image_tag`；优先使用具体稳定 tag。`latest` 仅适合临时试用。工作流等待官方镜像、执行部署前数据库备份、通过 SSH 更新 Compose，并等待容器健康检查，最多约 90 秒。部署目标必须已有正确 Compose 和环境配置；操作与恢复以[部署指南](../deployment.md)为准。

### 部署前数据库备份门禁

工作流从本次 checkout 的 workflow ref 读取并通过 SSH 执行 [predeploy-backup.sh](../../scripts/predeploy-backup.sh)，不依赖远端预先安装脚本。`DEPLOY_PATH` 必须指向现有部署目录，并包含当前有效的 `docker-compose.yml` 和 `.env`；脚本进入该目录，固定使用 `./data` 判断本地持久数据、`./backups` 映射备份产物。Compose 必须保持相应的环境文件、网络和持久挂载。

只有 `xirang` 容器不存在、`./data` 没有任何持久数据且 `.env` 未配置 `DB_TYPE=postgres` 三项同时成立时，门禁才明确报告首次部署并跳过备份。Docker daemon 不可达、容器状态无法识别、数据路径异常或无法读取，都必须失败，不能解释为首次部署。

运行中的升级必须备份；容器已停止，或容器不存在但仍有本地数据或 PostgreSQL 配置时，也必须备份。脚本拉取本次 `IMAGE_TAG` 的官方镜像，通过 `docker compose -f docker-compose.yml run --rm --no-deps` 保留服务的环境、网络和挂载，在目标 All-in-One 镜像中执行 `/usr/local/bin/backup-db.sh /backup/db`。

备份命令必须返回位于 `/backup/` 内且不含越界路径片段的产物，备份文件和 `.sha256` 均须非空。门禁还确认备份目录权限为 `0700`、文件及校验文件权限为 `0600`，执行 `xirang:xirang` 所有权设置，并校验 SHA-256 一致。任何必需备份、产物、权限、所有权操作或校验失败都阻断后续部署；不能仅凭备份命令启动成功就继续更新容器。

## 故障定位

- Release PR 未产生：检查 squash commit 语义、Release Please 运行及 token；不要手动打正式 tag。
- Release 已有而镜像缺失：按发布步骤定位构建、扫描、CI 再核验、manifest 或 attestation 的失败点。仅瞬时推送故障时按原版号及来源重发。
- 等待 CI 超时：先确认同 SHA 的主干 CI 全部成功，再重跑原发布运行；重跑使用原工作流策略，后续合并的等待时间调整不会修改旧运行。原运行的构建、扫描与发布前复核仍须通过，并先确认没有更新版本已发布，避免旧版本回写 `latest`。
- 旧 release run 长时间未结束：先等待或取消旧运行，避免跨版本运行延迟回写 `latest`；当前 concurrency 按 ref 分组，不保证不同版本串行。
- 版本提示异常：核对 `VERSION_CHECK_URL`、响应中的 `tag_name`/`html_url`、稳定 tag 格式及后端构建版本；开发版本 `dev` 不构成正式发布证据。

修改 release/publish/deploy/描述同步工作流、安装模板或版本检查行为时，同步本篇及对应部署/配置主文。不要以关闭门禁处理外部故障；确实受外部条件阻塞时记录失败步骤和缺失证据。
