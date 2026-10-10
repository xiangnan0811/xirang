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

审阅 Release PR 时，将已交付的 `Unreleased` 条目纳入目标版本，补齐迁移、备份保全、旧进程排空和降级限制，不能只保留自动生成的 PR 标题。对运行逻辑修复，还须说明新逻辑何时生效、旧进程中已开始操作的处置边界；没有 schema migration 时也明确写明，并说明回退是否会重现缺陷。required checks 全部通过后合并，确认 GitHub Release 创建。

发布说明的归属以目标版本标题为边界：将完整的“行为与升级说明”移到目标版本节内，并清空已交付的 `Unreleased` 条目。仅在 CHANGELOG 顶部保留警告不够；合并前和发布后都须核对目标版本及 GitHub Release 正文实际包含这些警告。

Release Please 自动插入版本标题时可能将原有升级说明留在 `Unreleased`，生成的 PR 正文也可能只有提交摘要。此时原样移动 CHANGELOG 中已审阅的说明，并同步 PR 正文的目标版本节；两处都保留完整风险边界，再提交补充变更。
SSH 轮换预验证与同批安全补丁的说明作为一个完整版本节归档，不拆散业务升级边界和工具链回退风险。
若本版本同时补齐历史 Release 正文，须分别保留历史说明修订与本次新版本发布的边界；历史正文修改不重发历史镜像，也不能替代本次版本的构建、扫描、标签提升和证明验收。

同步后重新读取远端 PR 正文，将其版本说明与 CHANGELOG 的目标版本节逐条比对；不能仅凭编辑命令成功或本地文件正确就视为远端风险说明完整。版本发布后再对 GitHub Release 正文做同样核对，分别确认文件归属、PR 生成输入和最终公开说明。

镜像软件包锁定补丁的目标版本说明还须保留新锁定版本、同源 Core/Worker 重建、架构匹配的闭包及签名运行包更新要求；不能将包可安装或本地单架构构建成功等同于正式双架构发布通过。

人工补齐 Release PR 时，先核对 manifest 的目标版本与 CHANGELOG 版本标题一致，再原样迁移已审阅的升级警告；不要为了移动章节改写上一发行版的保留/删除边界。编辑 PR 正文必须保留 Release Please 生成的首尾标识及分隔符，只更新中间版本说明，否则合并后可能无法识别发布。该补充提交仍走提交钩子与 PR required checks，不直接修改正式 tag。已合并 PR 的正文编辑也可能触发 CI 并取消同 ref 的运行；如需恢复，应重跑原 `push` CI 并核对同 SHA 的完整结果，不能用 PR CI 代替发布准入。

[Publish Docker Images](../../.github/workflows/publish-images.yml)监听 `release.published`，按以下顺序发布：

1. 从发布工作流自身的不可变提交加载验证策略，一次解析并冻结源码 SHA。
2. 等待同仓库 `main` 的同 SHA、`push` 事件、完整 `ci.yml` 成功，默认最多 60 分钟，为 PostgreSQL 等串行合同测试留出时间；验证任务超时为 65 分钟，预留 checkout 和 API 调用开销。缺失、未完成、失败、取消或超时均拒绝发布，新的失败运行不能被旧成功掩盖。
3. 在原生 amd64/arm64 runner 分别构建并按 digest 推送，再用显式 `TRIVY_PLATFORM` 扫描各自 digest。
4. 提升 multi-arch manifest/tag 前再次验证同 SHA CI；只有全部平台任务成功才发布正式标签。
5. 发布后生成 provenance attestation 和摘要，记录源码 SHA、CI run、平台 digest 与扫描结论。

当前 Trivy 设置为 `severity: HIGH,CRITICAL`、`exit-code: 1`、`ignore-unfixed: true`：扫描识别且已有修复版本的高危/严重漏洞阻断正式标签，未修复漏洞被过滤。不能将通过结果解释为不存在任何高危漏洞。基础镜像或包漏洞阻断时，更新来源并重新走 PR/release；不得降低 severity、添加临时 ignore 或绕过扫描。Actions pin 和依赖维护见[仓库自动化](automation.md)。

安全包版本锁定补丁的发行说明须指出替换的旧版本、固定的新版本及镜像重建要求；回退旧镜像会同时恢复旧依赖及其已知漏洞，不能仅按应用行为判断回退风险。

SSH 轮换预验证版本同时包含 `golang.org/x/net` v0.59.0 → v0.60.0、
Go 1.27.1 → 1.27.2 和 Core TIFF 4.7.1-r0 → 4.7.2-r0 安全更新。
发布须同源重建 Core/Worker/supercronic，重新生成匹配架构的闭包、签名运行包和证明，
并保留 CHANGELOG 中的升级与回退风险说明。

### 持续 CI 与正式发布证据边界

持续 CI 的 Core `docker-build` 仅在原生 `amd64`/`arm64` runner 上构建并加载本地
镜像：`push: false`，无 Docker Hub 登录或发布凭据。它会核对本地镜像的
OS/架构和 Buildx image ID，使用显式 `TRIVY_PLATFORM` 扫描本地 image ID，并运行
架构专属 Compose readiness smoke。该绿色结果是候选源码的双架构构建、漏洞门槛和
临时运行时证据，不是远端镜像已存在、已推送或已完成证明的证据。

只有本手册上文发布顺序中的正式工作流，才能证明按 digest 推送、远端 digest 扫描、
multi-arch manifest/tag 提升、发布凭据使用和 provenance attestation。交付记录必须
分别列出持续 CI 的 no-push 结果与正式发布的 digest、凭据使用范围及 attestation
结果；不得用 CI 的本地 tag 或绿色 Compose smoke 替代任一正式发布证据。

持续监控发布直至结束。标签推送发生在 attestation 之前，因此后置证明失败时可能已有公开镜像；核对失败步骤及 digest，不能把 workflow 失败等同于完全未发布。交付声明区分 GitHub Release、镜像、证明与实际部署结果。

## 升级说明必须覆盖的风险

根据候选改动查阅[任务与恢复等领域合同](../spec/domains/README.md)及[运维恢复手册](../admin/backup-recovery.md)，将受影响条款写进该版本说明：

- 数据库和加密密钥、备份副本及必要日志/游标的保全，旧 Core/调度器/执行器是否须排空。
- 新迁移的不可逆数据和降级保护；迁移号相同不证明执行器可安全降级。
- 不可逆的备份职责收敛还必须说明 STOP/排空全部旧 Core、scheduler、executor、collector、notification worker，使用旧版匹配二进制和全套旧密钥的隔离实际恢复演练，SQLite 离线与 PostgreSQL 手动 stop/drain，删除/保留的数据边界以及 Alert 投递 `unknown`/`feature_retired` 围栏；不得把替换镜像或 down SQL 写成回滚。
- 多次不可逆退役必须分别说明删除范围、来源告警集合和版本下限，不能将后续行为追溯到历史发行版。例如 `backup_focus_retirement` 的节点监控/日志/看板退役与后续 `service_monitor_retirement` 的两个服务监控表删除是独立边界；共用投递围栏不意味着来源集合相同。发布说明保留历史版本原义，并按引擎准确区分整库恢复、恢复副本启动和远端 Provider 文件恢复的实际验收证据。
- 恢复准入、未知写入 hold、人工协调及配置导入补偿的边界；不得用降级、删记录或改配置绕过保护。
- Core 与可选 Worker 的源码和工具链指纹一致性、文件系统隔离能力及容量要求。
- 涉及 Rsync 捕获流程时，说明 Core 暂存空间需求（含增量基准快照）、源文件变化的失败语义、带宽限制作用范围及本地隔离 helper 版本要求，并区分文件级证据一致性与活动数据库的事务一致性；不能把修复前者表述为已解决所有在线备份失败。
- 修复动态配置消费方时，说明先前已保存但未生效的数据库覆盖值会从何时生效；涉及历史清理时，升级前须核对保留窗口、保全数据库与密钥，明确当前轮 cutoff、下一轮设置生效和已删除数据不可恢复的边界。紧急备份或终端认证修复须说明新旧前后端切换、在途操作排空和重新验证要求，不能将换版描述为原子撤销已提交任务。
- RecoveryPoint 生产者历史保全修复须说明：任何存续 producer-run 引用优先于普通 TaskRun retention，可能导致执行历史长期保留；不回填 NULL producer、不恢复已删历史、不改变显式删除语义。新版保护只约束新版 Core 的后续自动清理轮，升级须排空旧清理操作并保全数据库与密钥；回退旧版会重新引入引用丢失风险，不能把无迁移或本地消费者 smoke 写成生产恢复已验收。
- 配置读取完整性和通知统计口径修复须区分配置何时参与校验、历史记录是否改写与 UI 请求状态何时生效；不将逐项有效值读取写成原子快照，也不将忽略迟到响应写成取消网络投递。按[启用合同](../spec/domains/backup-enablement.md#设置锁序前瞻配置与工作所有权)与[告警合同](../spec/domains/alerting-health.md#投递统计与窗口状态)核对升级说明。
- 通知页未解决告警统计的升级说明须区分未知状态与真实零值，明确刷新、告警操作、身份变化和手动重试均使旧请求代次失效；行为从新版页面加载后生效，不改变通知铃轮询、后端计数授权或在途服务端操作。测试取消后等待 worker 完成只能证明 fixture 清理顺序，不能据此关闭尚未定位的生产恢复错误；公开说明应保留该证据边界。
- 通知页失败任务摘要的升级说明须明确现存 TaskRun 的固定 24h 去重口径、运行时节点快照授权和历史清理可降低计数；新版 Core 请求与新版页面状态分别在换版后生效，前后端应同版本部署，旧后端 404 不回退当前状态计数。该修复无 schema migration、不改在途任务及保留策略，回退会恢复旧计数缺陷；同时保留既有迁移的恢复限制，不把遗留在 `Unreleased` 的上一版本说明归入新版本。
- 告警快捷静默的发行说明须保留管理员权限、当前节点与类别的固定匹配范围，以及平台/未知/退役类别拒绝创建的边界。新版页面加载后生效，关闭对话框或切换身份不撤销服务端已提交规则；无新增 schema migration，回退页面不会删除已创建规则，既有规则仍需在通知设置中撤销或等待到期。
- Search 规范化协议代次推进时，版本说明必须明确 Core/Indexer 同步升级、旧代 metadata postings 的异步 bounded rebuild、content/OCR 不因 metadata rebuild 自动完成，以及旧版回退不得复用新版 postings；不能把这一派生索引协议升级写成数据库 schema 迁移或仅替换镜像即可安全回滚。

历史版本的具体迁移编号和交付事件保留在 CHANGELOG/发行说明；当前迁移版本唯一声明在[后端入口](../../backend/README.md)。公开发布成功不代表生产已经升级或真实恢复已经验收。

All-in-One 同时设置产物观测目录 `CRON_DB_BACKUP_DIR=/backup/db` 和独立私有作业目录
`CRON_DB_BACKUP_STATE_DIR=/backup/.cron-db-state`，共用 26 小时默认窗口。发行验收须验证
相同非 root 身份的初始化、实际 supercronic 调度、脚本 receipt、状态持久性及后台告警；
仅启动容器或看到 fresh 产物不是该链路的证明。新的健康 cursor/永久使用标记通过
双引擎迁移维护，已使用后拒绝 down；数据库与私有状态文件必须配套保全身份和 revision。
发布说明须披露 Rename 后目录 Sync 失败的结果不确定窗口，不能由可见完成记录反推
原 runner 最终持久化确认成功。它不替代 Web SQLite 快照或 PostgreSQL 离线恢复验收。

`v0.60.0` 的完整行为与升级说明归档于 CHANGELOG 对应版本节；Release PR 与
GitHub Release 正文须保留迁移 92 的使用后降级保护、旧进程排空、配套状态保全
及上述 fsync 结果不确定窗口，不以生成的功能标题替代这些边界。

本次 Core 时区数据锁定从源中已移除的 `tzdata=2026d-r0` 更新至 `2026e-r0`，
发布须重新构建并扫描双架构镜像；不修改默认时区或迁移已保存的 UTC 时刻。
升级前复核受规则变化地区的本地调度时间，回退旧镜像也会回退时区数据。

`v0.58.2` 的生命周期行为、备份能力边界、旧操作排空、无新增 schema migration
及依赖回退风险集中于 CHANGELOG 对应版本的“行为与升级说明”；发布时将该节完整
同步至 Release PR 和 GitHub Release 正文，不将本地或 CI 验收写成生产部署完成。

SSH 候选连接预验证的发行说明须明确：新版 Core 与向导同版本切换，排空旧版轮换
操作，旧页面的先保存后测试不受新接口追溯保护；严格只读 known_hosts 不受全局
自动接受/关闭严格校验豁免，普通编辑仍可离线修复。保留无新增 schema migration、
关闭不撤销已提交替换、结果未知不重放及回退旧版恢复旧风险的边界；具体行为见
[凭据与访问](../spec/domains/credentials-access.md#受管-ssh-key-scope)。

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
