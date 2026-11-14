# 发布手册

## 公开发布合同

GitHub Release 是公开版本和变更说明的权威来源，Docker Hub 的 `docker.io/linnea7171/xirang` 是唯一官方公开镜像源。仅发布稳定 semver `vX.Y.Z`；正式镜像包含 `vX.Y.Z`、`X.Y.Z` 和 `latest`，后者表示最近正式发布的稳定版。手动重发不得移动 `latest`。

同仓库的 `inputs-*` 和 `base-*` 标签用于保留构建输入，包含内容标识，不属于正式
产品标签。其采集流程只生成待审查的依赖 lock，不创建 GitHub Release、不移动
`latest`，也不替代当前补丁发版。保存范围、签名检查和安全更新见
[构建输入保留与更新](automation.md#构建输入保留与更新)。

采用归档 lock 的版本才具备对应 APK 与基础镜像的留存证据。发布前核对所引用
两架构输入仍可读取、产品构建使用禁网 APK 安装，并继续执行当前漏洞扫描与
同 SHA CI 准入。首次迁移使 Worker 工具链从 v2 推进到 v3；须配套更新 Core、
Worker、两架构最终闭包、签名 bundle 和 proof，排空旧 Worker 在途处理后切换。
闭包和 bundle 格式仍为 v1；回退时整套恢复匹配版本，不能混用 v2 与 v3 身份。
无新增运行时配置或 schema migration。旧 tag 未采集过的输入不因此次迁移自动
恢复，保留原有不可重建与漏洞回退警告；已有 #649、#651、#653 的修复状态不变。

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

Core PostgreSQL client／`libpq` 与 Worker 传递 `libpq` 的 `18.6-r0` → `18.7-r0`
属于实际运行依赖安全修复，按 `fix` 正常发行，不适用纯 CI 免发行判断，也不手动
指定版本或移动旧 tag。输入来源为配方 `02151ca90884f2a185bb9d5e1d507c7c58a0d827`
的[原生双架构采集](https://github.com/xiangnan0811/xirang/actions/runs/43454390891)；
八事务只有上述包变化，其他包、固定 base、安装器与可信根保持。保留输入的内容寻址
发布及 lock attestation 不等于正式产品镜像发布或生产部署。

本修复无新增配置或 schema migration，Worker 仍非 GA、默认关闭。发行说明须保留
数据库、匹配密钥、配置与备份保全、暂停准入并排空旧 Core／Worker owner、同源
重建替换、更新 compiled Worker toolchain／pipeline 身份及架构匹配闭包、签名
bundle／proof 的要求。旧进程不会自动获得补丁，旧 receipt 不得改标为新身份；
回退必须恢复匹配整套镜像与制品，会恢复旧 PostgreSQL 依赖漏洞，既有迁移降级限制
继续适用。各次成功 PR、main 及自然 Release PR 仍须独立取得完整源码与签名证明。

同批取证工具修复仅纠正本地采用检查对无关 ignored 恢复 checkout 的误报，仍拒绝
实际构建与嵌入输入的本地变化；不改变生产输入或新增配置、迁移。旧恢复材料保留，
发行准入仍须确认当前 `live_eligibility=eligible_at_observation`；正常 status 退出
或历史 verification=passed 不能替代该门槛，操作见[可恢复 CI 取证](automation.md#可恢复-ci-取证)。

已 squash 合并的 PR 若包含需要发布、但未被提交语义识别的修复，可按 [Release Please 官方说明](https://github.com/googleapis/release-please#how-can-i-fix-release-notes)，在原 PR 正文追加带日期的发布决定和 `BEGIN_COMMIT_OVERRIDE` / `END_COMMIT_OVERRIDE` 区块；保留原始变更与验证事实，在块内补齐真实的 `fix` 条目并按需添加 `Release-As:`。重新运行 Release Please 后核对实际生成的 Release PR，仍须完成下述说明审阅、检查与镜像门禁。该方式不改写 Git 提交或旧 tag；仅改已合并 PR 标题不会更新提交语义，普通 merge 不适用此 override 路径。

审阅 Release PR 时，将已交付的 `Unreleased` 条目纳入目标版本，补齐迁移、备份保全、旧进程排空和降级限制，不能只保留自动生成的 PR 标题。对运行逻辑修复，还须说明新逻辑何时生效、旧进程中已开始操作的处置边界；没有 schema migration 时也明确写明，并说明回退是否会重现缺陷。required checks 全部通过后合并，确认 GitHub Release 创建。

PR 标题／正文编辑的轻量分类及并发隔离见[仓库自动化](automation.md#actions-与-ci)。
正文仍是已合并 PR 的 Release Please override 输入，也是 Release PR 发行说明审阅的载体；
编辑后应核对当前标题和有效 required checks。metadata 的三个固定同名检查产生新的
check ID，保留原源码检查结果；只有对应来源 job、check 与整个来源 workflow 均成功，
验证者才通过，pending 等待，失败或证据不可读则阻断。metadata 验证不运行源码测试，
也不能替代发布要求的同 SHA `main` push 完整 CI；合并后正文编辑同样须核对原 push
运行的身份与结果。

Go 1.27.4 与 libxslt 运行依赖安全修复属于实际产品输入更新，按 `fix` 正常发行，
不适用纯 CI 免发行判断。Release PR 和正式 Release 说明须保留
[GO-2026-6859](https://vuln.go.dev/ID/GO-2026-6859.json)、
[CVE-2026-87636](https://security.alpinelinux.org/vuln/CVE-2026-87636) 与
[CVE-2026-9947](https://security.alpinelinux.org/vuln/CVE-2026-9947)，以及 Core 直接
`libxslt=1.1.43-r4` 和 Worker 实际传递 r3 → r4 的区别。Q2 的八事务还包含新 Go 基础镜像
带来的 Alpine 3.24 补丁、证书、APK 工具及加密库变化；supercronic 构建阶段 Git 从
不可安装的 `2.54.0-r0` 改为 `2.54.1-r0`，其上游修复见
[Git 安全公告](https://github.blog/open-source/git/git-security-vulnerabilities-announced-7/)。
这些输入及其来源见[构建输入](automation.md#构建输入保留与更新)，Git 不属于 Worker inventory。

升级前保全数据库、配置、匹配密钥及备份副本，暂停 backfill 并排空旧 Core／Worker
在途操作，同源重建和替换 Core、Worker 与 supercronic 二进制，配套更新共享 toolchain／
pipeline 指纹、架构匹配的最终 closure、授权签名 bundle 和 proof。回退须恢复匹配的整套
二进制、镜像及制品，会恢复旧 Go／libxslt 已知漏洞和旧构建工具风险；既有迁移降级
限制继续适用。本次不新增 API、配置、schema 或迁移；Worker 仍非 GA、默认关闭。

每次完整成功的 PR 源码、合并后 main、Release PR 源码及发行 main CI，都须使用各自
amd64 profile smoke 上传的七份签名 fixture 原件／receipt，并按[测试约定](../spec/guides/testing.md#worker-profile-签名证据)
核验双架构 closure、签名与重启后指纹。私有导出只接受独占绝对目录及匹配 checkout 的
合法 CI 身份；环境变量不认证生产者，临时测试公钥和 receipt 不是生产 proof。输入采集、
CI 及正式镜像发布各自取证，任何一项不能替代其他项或证明生产部署。

完整源码取证使用受维护的 `scripts/ci-evidence.py source`，显式传入本次 request、
case 和在项目 scratch 构建的 `verify-ci-profile`；中断后仅对相同 context 使用
`--resume`，离线复验使用 `verify`。四个命令、current 和退出语义见
[仓库自动化](automation.md#可恢复-ci-取证)。发布或合并准入还须重新观察实际候选
及保护状态；历史 case 的 complete 或离线验签不替代当前远端资格。

本次 Go／libxslt 补丁的 Release PR 只补充版本材料和本手册，并须逐项核对执行消费者的
blob 与已审阅的合并源码 `429b92aa1144d6d11e41717cfb7d17cc2f190b6f`（S）一致。
[R2 源码运行](https://github.com/xiangnan0811/xirang/actions/runs/43013360818)与
[S 的 main 验证运行](https://github.com/xiangnan0811/xirang/actions/runs/43026849570)是证据入口；
不得仅凭链接预先计为成功。最终 head 的 PR 源码、合并后 main、Release PR 源码和
发行源码的 main 运行各自须取得当前 attempt 的 PostgreSQL、浏览器、原生双架构及七文件
签名 fixture／receipt 证据，核对 producer API、ZIP 摘要和产生时间窗口，并按既有
`VerifyPackage` 重放验证及同 SHA 发行来源选择器检查。metadata、旧 head 或其他运行的证据
均不能替代；具体取证步骤以[测试约定](../spec/guides/testing.md#worker-profile-签名证据)为准。

纯 `ci`／`test`／`docs` 变更没有需发行的运行行为或依赖修复时，说明保留在
`Unreleased`，不为此单独制造正式版本或镜像；仍核对正常 hooks、required CI、
合并后同 SHA 的完整 `main` push CI 与 Release Please 的实际结果。

双语 key 对齐检查接入前端统一门禁属于 `ci`／测试／文档变更：只验证 `zh.ts` 与 `en.ts`
的叶子 key 集合，沿正常 pre-push、本地 parity 与前端 CI 传播失败，不代表代码引用、
共同缺失或未用 key 已检查。没有应用运行行为、依赖、API、配置或 schema 变化时，说明留在
`Unreleased`，核对正常 hooks、完整 required CI、合并后同 SHA CI 与 Release Please，
不为接线单独制造正式版本或镜像。
该 npm 门禁的失败传播夹具按现有锁文件和依赖补丁元数据复制真实所需输入，避免整个
`node_modules` 的复制／清理使 teardown 超时；真实依赖完整性校验、npm key 不一致失败
传播和私有目录身份保护保持。该修复仅限测试，仍留在 `Unreleased`，不单独发行版本。

退役 snapshot 消费者与旧 indexer 的清理说明须区分代码删除和数据处置：兼容读取仍返回 410，live restore 门控、历史表及已发布迁移保留，旧 `SNAPSHOT_INDEX_MAX_SECONDS` 不再有消费者；不得把清理写成新增恢复能力、历史数据销毁或撤销既有迁移限制。纯 `refactor` 按现行提交语义处理，没有正式 release 时将说明留在 `Unreleased`，待实际发行再归入目标版本。

保留策略与删除安全的入口回归迁移按 `test` 语义处理：真实 Coordinator、注册删除 adapter 与分页选择验收替换旧 helper 覆盖，不代表新增保留规则、删除权限或远端 Provider 能力。无 API、配置或 schema migration；保留既有版本的密钥、持久格式及降级限制。未生成正式 release 时，说明保留在 `Unreleased`，并核对同 SHA CI 与 Release Please；不要求为测试迁移单独生成镜像，也不将受控本地烟测记为 AWS live 或生产部署。

旧 Updater metadata 和 Provider 解码便利包装的断言迁移／死码清理同样按 `test`／`refactor` 语义处理：保留当前签名包、receipt、类型化发布证据和持久格式，不把内部源码 API 清理写成新增 wire 或更新流程。没有运行行为变化时不强制发版／镜像，说明留在 `Unreleased`；仍须完成正常 hooks、同候选独审、required CI 及合并后的同 SHA CI／Release Please 核对。

孤立 Handler 与旧 snapshot 列表执行器接口清理同样不强制制造正式版本或镜像：四路遗留读取仍为 410，节点远程执行路由仍未挂载，活 Restore、恢复错误处理和任务归档合同保持。没有公开 API、wire、配置或 schema migration 变化，不处置历史数据；说明留在 `Unreleased`，仍按同候选独审、正常 hooks、required CI 以及合并后同 SHA CI／Release Please 完成交付。

`ArchiveService.TryHardDelete` 的零消费者清理与归档断言迁移按 `test`／`refactor` 处理：真实 `Archive` 和任务删除路由承担任务／历史保全，HTTP 身份归属与数据库引用保护分别验收；保留策略审计、依赖拒绝、事务和调度边界，不新增硬删除、自动回收或 Provider 删除能力。历史模型、表和已发布迁移保留；无实际运行行为变化时说明留 `Unreleased`，仍须正常 hooks、同候选独审、完整 required PostgreSQL／CI、合并后同 SHA CI 与 Release Please，不额外制造版本或正式镜像。

孤立状态 validator、Task 能力便利投影与 mutable-head 内存投影的清理按 `test`／`refactor` 交付：现行服务转换、双引擎约束、Provider 准入／能力快照、真实观测持久化与公开 DTO 承担对应合同，不为旧测试新增 Command artifact 能力、Range 限制或观测流程。没有运行行为、公开 API、wire、配置或 schema migration 变化时不强制发版／镜像，说明留在 `Unreleased`；仍须完成同候选独审、正常 hooks、required CI 以及合并后同 SHA CI／Release Please 核对，不改写历史迁移或数据。

Content grant 与 Export item 的孤立转换／reset helper 清理同样按 `test`／`refactor` 处理：有效断言迁入真实 Broker／Reconciler 条件更新和 Coordinator Claim／Checkpoint 事务，保留活状态、fence／lease／字节／配额及 item-attempt 历史，不新增转换入口或 reset API。受控 spool 数据库夹具只证明事务与持久投影，真实服务执行另行记录；两者都不能外推完整远端读取／归档端到端。没有实际运行行为或依赖修复时说明留 `Unreleased`，核对完整 required CI、合并后同 SHA CI 与 Release Please，不额外制造版本或正式镜像；已发布迁移和历史数据保持。

孤立 `ScoreBackfill`／`CanReuseDerived` 及专属身份类型的清理按 `test`／`refactor` 处理：有效断言由真实 Runtime 收集／准入、Coordinator work key／interest／pull／slot、精确已完成 publication 查询和 Derived 上传／来源独立引用承担。既有输出 blob 物理去重继续保留，不把同 key 共用或相同输出字节等同 strong fingerprint 跨点跳过处理；现行 BackfillPolicy、RecentWindow／HistoryAgingStep 配置及有效性校验保留。usage 的单次收集统计不代表跨请求小时／Provider 活跃并发保证，澄清文档不等于已修复该差异。无运行行为、依赖、API、配置或 schema migration 变化时说明留 `Unreleased`，正常 hooks、完整 required CI、合并后同 SHA CI／Release Please 仍须完成，不额外制造正式版本或镜像；历史数据及已发布迁移保持。

第二十五轮调查在 SQLite／PostgreSQL 真实 Sink 第二次上传相同输出时复现 `upload.staging_id` 唯一约束冲突；该轮清理只闭合具名死码与有效断言迁移，未修复配额描述差异和重复输出问题。后续持久化修复须独立完成数据保全、双引擎迁移、真实 Sink 验收与正常正式发行，不能将存储层 blob 去重或预置跨点引用测试写为实际跨点双发布通过，也不能修改已发布迁移或删除历史数据凑验收。

共享输出修复的发行说明须包含 schema98 的 N:1 物理 blob 与独立上传／产物／来源授权、流式加密回放、新密钥代次与旧 inode 保全、最终事务授权及 Search 锁序。升级前暂停准入、停止并排空旧 Core／Worker 和全部发布／撤销／清理／对账 owner，保全数据库、密文、配置及匹配密钥，禁止混跑；共享后重复 staging ID／manifest digest 阻止98回退，SQLite 未知依赖也须拒绝。保留历史 upload／artifact／reference 和已发布迁移，回退旧运行代码会恢复缺陷；真实 Sink／Search／Unix HTTP、双引擎事务与受控 source／parser／Provider 的证据边界分别说明。正式 Release PR、versioned CHANGELOG 与公开 Release 正文须同时保留这些边界。

持久后台配额修复的正式 Release PR、目标版本 CHANGELOG 与公开 Release 正文还须包含 schema99 的数据／密钥保全、全部旧 Core／Worker 停止排空与禁止混跑、精确来源元数据回填及无历史补收费。新额度仅从升级后新后台 job 创建开始计，预计输入字节不等于网络字节；同 job 重试不重收、取消不退费、当窗设置更新不清空累计。charged 历史（含零字节）或账本使用会阻止99回退，98共享后的降级限制仍独立保留；不得删除历史对象或已发布迁移绕过。回退旧运行代码恢复缺陷，需配套一致备份、配置和密钥。本次属于实际运行修复，按正常正式发行，不套用第二十五轮纯清理无需发行的判断；本地真实组合证据仍不外推 parser、所有 Provider、AWS live 或生产部署。

上述共享输出、持久后台配额及同批测试／清理的完整行为与升级说明归档于 CHANGELOG `0.73.17` 版本节。该发行 PR 与公开 Release 正文须保留完整版本块，包含 schema98／99 的独立降级限制、数据与密钥保全、旧 owner 排空和证据边界；第二十五轮当时未修复的观察作为历史保留，不与本版修复混淆，也不将纯清理另称为运行能力变更。

Core 的 `rsync 3.5.0-r0` → `3.5.2-r1` 是实际运行依赖安全更新，修复 Trivy 报告的 `CVE-2026-119741`（修复门槛 `3.5.2-r0`，官方两架构当前后续修订为 `r1`），须按 `fix` 完成正常发行，不能套用纯清理无需版本的判断。发布说明保留原生双架构输入采集／验签／禁网重放及最终镜像扫描边界；更新须重新构建并替换 Core，先保全数据库、匹配密钥、配置和备份副本并排空旧 Core 在途任务。输入锁变更不修复已启动旧进程或外部节点，回退旧镜像恢复已知漏洞；无新增 API、配置或 schema migration，既有降级限制不变。同次完整采集实际带来 Worker 传递 `glib 2.86.3-r0` → `2.86.5-r0`，必须同步生成的完整 APK 投影并推进 toolchain／pipeline 指纹，Core／Worker 同源重建、重新取得双架构 runtime closure、attestations、签名 bundle 和 proof，先排空旧 Worker 在途处理再配套切换及回退。该传递变化不等同另一个已证实的漏洞修复；既有显式 root inventory 不新增 glib，完整输入身份由真实 manifest 投影承担。

后端 lint 的 CI 安装渠道改为固定 action 的 `install-mode: goinstall`，使用当前 Go 工具链安装同一 `v2.14.0`，不依赖 GitHub Release 二进制资产；Go 模块获取渠道仍可能不可用。固定版本、配置 schema 校验、全范围 lint、缓存、注解和失败门禁保持，仍须取得新候选的完整 required CI 与合并后同 SHA CI；新候选 CI 实际执行前不宣称此前安装失败已解决。此项仅涉及 CI，无新增 API、运行时配置或 schema migration。

上述 Core Rsync 安全更新、Worker 输入同步与 CI 安装调整，以及孤立状态／能力／观测投影、Handler／旧 snapshot 列表接口、测试迁移／未用解码包装三组清理的完整四部分审阅说明，已归档于 CHANGELOG `0.73.16` 版本节；发行 PR 与正式 GitHub Release 正文须完整保留全部四部分及其升级、排空、回退和历史数据保护边界，纯清理不单独制造版本。

Core Compose 自测的 APK 检查完整消费输入、避免 `pipefail`／SIGPIPE 假阴性的修复仅属测试，说明留在 `Unreleased`，不因此单独制造运行版本。已发布 tag 保持不可变；后续测试修复提交与原发行源码的 CI／镜像证据分别记录，不能用新主线 CI 替代原发行 SHA 的完整准入，也不能将一次恢复成功写成此前从未失败。

Recent 内容访问修复的发布验收须覆盖同 grant 首个持久成功 GET、并发 Range 去重、失败不补录、owner 隔离及 clear/replay，并实际运行 required-PostgreSQL 库存中对应的三个 content 子路径。发布说明保留有限预览/Range 不代表整文件下载完成、有界尽力漏记和必需内容审计不降级的边界；新逻辑仅在新版 Core 后续成功请求生效，不回填历史，已有成功 GET 的 grant 不重新记录。无新增 API、配置或 schema migration，保全数据库、匹配密钥与配置，正常换版前排空旧 Core 在途内容请求；回退会恢复缺触发的问题，不删除已有 Recent 或解除既有迁移/恢复限制。真实本地 HTTP、双引擎与浏览器验收不代表 AWS live、生产升级或远端恢复。

上述 Recent 修复与此前保留/删除安全回归、退役快照及未用代码清理的完整说明归档于 CHANGELOG `0.73.8` 版本节。该发行 PR 与正式 GitHub Release 正文须保留三部分全部行为、生效和升级边界，不将原 `Unreleased` 的已交付条目留在版本节之外。

真实恢复修复的发行说明须同时覆盖目录 capture 后非空子项保全、枚举／关闭失败、有界目录检查、取消独立中断 transport 和晚到资源 join，以及 SSH usage 不改变策略 revision。PostgreSQL 期限与 committed Rsync 元数据只在数据库表示边界投影，原签名时间、manifest 与旧租约不改写、不延长；发布完整认证后才置 online。旧 Rsync none/weak Catalog 代次由后台按原退避重建，失败保留旧 active，旧引用不改写。

同版必须配套切换 Core／Worker 的 activation v2 与 trailer 终态合同，排空旧进程；malware stat/range profile 推进 pipeline 指纹，旧证据需重新处理，并重新取得匹配 Worker 二进制的两架构 closure／签名 bundle。canonical malware 格式不变；这些 Worker 子功能无新增配置或 schema migration，不改写已发布迁移。同版本的 Catalog 扫描状态迁移须另行说明。保全数据库、密钥、配置和备份副本，回退整套匹配版本会恢复旧缺陷；换版不能撤销远端副作用或解除未知写入保护。SQLite/PostgreSQL 真恢复、真实 HTTP/SSH/持久状态、sandboxed scanner、required CI 与正式镜像证明分别闭合；受控 Processing 输入不冒称实际 scanner、其他 Provider、AWS live 或生产部署认证。

该恢复补充还须说明持久工作流期限与短时观察新鲜度的区别、每次新许可／成功投影的重新观察与事务重验，以及已改变的 in-place anchor 如何依完整 checkpoint 历史继续。isolated 不再探测无关同名路径；旧 path-derived 计划／快照不重写、不续期，新尝试创建新计划。认证覆盖／删除残留矛盾保持 unresolved，限制性失败记录不要求目标仍满足成功条件；不将此修复写成新增自动 crash adoption、远端锁或副作用撤销保证。

同版还须覆盖 isolated sealed 结果的后台补发与到期清理、unknown 分类及精确 step-up 下载，明确 workspace 原 24 小时期限不延长，disabled 只维护清理，不发布结果。exact-mirror 先写后删但不重写 canonical ordinal/digest；请求构建按当前锁定历史认证，明确 pre-dispatch 且未 armed 的失败显示 `pre_write_failed`，不能用于已执行、可能写入或未知远端结果。新增公开分类要求 Core/web 同版切换，不新增 schema、配置或自动 crash replay。完整到期验收使用真实期限及独立进程；运行中或仅完成 capture 的证据不能记为删除完成。

本轮 Catalog 公平性修复新增独立扫描状态及创建时间／ID 索引迁移，迁移 head 只在后端 README 维护。发行说明须解释有限高水位窗口、跨重启／实例推进及晚到点下轮访问，提交后未派发可在后续 sweep 重访，不是持久投递队列。启动缺表／种子或结构／状态漂移拒绝且不修复。升级按正常停止、排空流程配套切换 Core／Worker／web；使用后回退此迁移仅丢失可重扫进度，不删除 Catalog 代次、备份或授权证据，再次升级重建空闲种子，旧 selector 会恢复饥饿缺陷。既有备份、Recovery 与稳定身份迁移的降级保护继续适用，不得把局部无 schema 变更扩大为本版本无迁移。

上述真实恢复、Catalog 扫描、Worker 输入完整性及同批 lint 工具链修复的完整说明归档于 CHANGELOG `0.73.9` 版本节；该发行 PR 与正式 GitHub Release 正文须同时保留这两组行为和升级边界，不能只保留自动生成的提交摘要或将已交付条目留在 `Unreleased`。

Export 真实归档修复的发行说明须包含非文件原始空 fingerprint 与可选 MIME、创建／下载 proof 的登录 session 绑定、续租读取及过期调和所有权、ready 取消容量结算和测试专用包装退役。明确新逻辑在新版 Core 后续操作生效，换版前排空旧 Export／下载操作，并重新取得这两个 action 的 session-bound proof；不得将正常换版描述为解除旧进程的阻塞读取或延长原任务期限。

该修复新增双引擎 Export fingerprint 约束迁移；SQLite 额外索引、触发器或外部外键在升级写 dirty 前拒绝，降级也受结构保护。保留行出现旧约束不接受的空 fingerprint 时，迁移元数据及 down 均拒绝；旧兼容行且结构允许时可回退本迁移，其他历史迁移限制仍独立适用。发行 PR 和正式 Release 必须保留这些边界、数据库／密钥／备份保全及回退恢复缺陷的说明；本地真实 Rsync／认证 HTTP／双引擎期限验收不等于其他 Provider、AWS live 或生产部署。

同批 Expat 安全更新须保留 CVE-2026-117610／CVE-2026-118370 与 `2.10.0-r0` 的修复关系、Core／Worker 显式锁定及 supercronic-builder 传递包更新。实际重新采集仅改变 Expat 包，基础镜像、其余安装包、安装器和可信密钥不变；原生双架构输入验签、断网重放与安装后扫描不能替代最终镜像验收。Worker inventory 与 APK 投影变化会推进工具链及 pipeline 指纹，须排空旧处理并配套更新 Core／Worker、最终闭包和签名运行包。此依赖修复本身无配置或 schema migration，不能覆盖同版 Export 的迁移边界；回退旧镜像恢复已知漏洞。

同版全局／Policy 任务期限回归测试使用标准虚拟时间，保留真实定时器、执行器准入及持久终态断言；该变化仅限测试，不改变生产预算、优先级或原截止期限。
同版 CI race 的每包测试进程预算显式为 20 分钟，保留完整并发敏感包列表和竞态检查；该预算不改变产品期限。仍须取得最终提交的完整远端 CI 成功，才能继续合并与发布。
Export 下载取消回归在释放 writer 前等待读取 context 确实取消；持久 `draining` 只证明撤销已提交，不证明取消信号已送达。生产撤销顺序和错误分类不变，原有字节、终态与 drain 断言保留。

上述 Export 真实归档、Expat 安全更新和任务期限与交付取消回归验证的完整说明归档于 CHANGELOG `0.73.10` 版本节；发行 PR 与正式 GitHub Release 正文须保留三部分全部升级及回退边界。

备份资产启用与切换恢复围栏修复的发行说明须包含内层持久化补偿去重、内层 `restore` 失败向 `ErrFeatureTransitionCompensation` 与 `featureTransitionFenced` 的传播、补偿失败先置 `contentReady`／`searchReady` 未就绪、受围栏态拒绝 GA 就绪／盘点／确认及降级就绪／准备，以及 `ga.EnablementGate` 与 `settingsFeatureTransitionOnly` 测试包装退役。明确直接设置、删除覆盖值恢复环境默认、配置导入三入口执行同一门禁，区分补偿成功在线恢复与补偿失败保持关闭且须重启恢复；保留 `EvaluateEnablement`、`DatabaseReadiness` 和 `forbiddenGAMutations` 保护。该修复无新增公开 API、运行时配置或 schema migration，不覆盖既有迁移降级限制；新逻辑在新版 Core 后续切换生效，回退会恢复重复回滚与围栏遗漏缺陷。

上述备份资产启用门禁、运行时恢复围栏与测试包装收敛的完整说明归档于 CHANGELOG `0.73.11` 版本节；发行 PR 与正式 GitHub Release 正文须完整保留该节全部行为、生效与回退边界，不能只保留自动生成的提交摘要或将已交付条目留在 `Unreleased`。

Rsync 版本化激活只读根重验与历史准备死码收敛的发行说明归档于 CHANGELOG `0.73.12` 版本节：须包含首次合法预检保留创建／重验专属 `<target>.xirang-rsync-v1` 受管根、激活重验改用 `RevalidateRsyncManagedRoot` 与 `openRsyncManagedTreeReadOnly` 实现移走受管根或缺失控制目录时零磁盘新建副作用 `409` 拒绝，以及退役 `prepareManagedRsyncActivation` 死码并保留 `managedRsyncBindingDocumentV2`。无新增 API、配置或 schema migration，换版前排空在途预检／激活（重启后重新预检），回退会恢复缺失路径重建目录缺陷。

Rsync 版本化首次预检前置路径分离校验的发行说明归档于 CHANGELOG `0.73.13` 版本节：须包含在 `BootstrapRsyncManagedRoot` 前调用 `ValidateRsyncManagedRootBootstrapSeparation` 对 `<target>.xirang-rsync-v1` 与 `legacyTarget`、`sourcePath` 执行词法与只读 VFS 分离校验（`ENOENT` 时不创建目录），确保重叠来源被移走后首次预检返回 `409`、不签发票据、不在来源原路径重建受管根或写入控制文件且旧目标／绑定／恢复点不变，同时保留正常独立来源首次预检创建专属 `<target>.xirang-rsync-v1` 受管根的正向行为与既有 `ValidateRsyncManagedRootSeparation` 重验。无新增公开 API、配置或 schema migration，换版前排空在途预检／激活（重启后重新预检），回退会恢复首次预检先建目录再校验分离的缺陷。

Catalog 状态与证据投影及死码收敛的发行说明归档于 CHANGELOG `0.73.14` 版本节：须包含新增 `authorizedPublicPoint` 使缺少来源观测（`observed_at IS NULL`）的可变恢复点在 `catalog-status` 正常投影 `staleness.status = "unknown"`（HTTP `200 OK`）并在 `evidence` 正常返回四节证据视图（HTTP `200 OK`），同时保留 `GetRecoveryPoint`／`ListRecoveryPoints` 的 `ToRecoveryPointDTO` 校验，以及移除 `ParseCoverageStatus`、`ParseStalenessStatus`、`ParseEvidenceLayerStatus` 无生产调用解析函数并保留 `ParseFingerprintStrength` 与四类真实入口回归边界（`catalog-status` 对异常代次状态／错误码以 `ErrUnknownInternalState` 闭合拒绝、`evidence` 四节对未知枚举／字段值收敛为 `status = "invalid"` 且不泄露内部值）。无新增公开 API、配置或 schema migration，换版前排空在途请求，回退会恢复缺少来源观测的可变恢复点查询返回 `400` 的缺陷。

以下节点预览、迁移目录比较与安全依赖更新的完整说明归档于 CHANGELOG `0.73.15` 版本节，发行 PR 与公开 Release 正文须保持该节的全部升级和回退边界。

节点文件预览打开句柄恢复重验、节点迁移目录比较死码收敛、`golang.org/x/net` 与 Alpine 双架构构建输入安全更新的发行说明须包含：在 `(*FileHandler).GetNodeFileContent`（`GET /api/v1/nodes/:id/files/content`）完成 SFTP 读取与 `handle.Close()` 后，新增 `verifyNodeFilePreviewOverSSH` 复用同一 `*ssh.Client` 连接在远端从 `/` 经 `accessRoot.Path` 起以 `(O_PATH | O_DIRECTORY | O_NOFOLLOW)` + `dir_fd` 逐级遍历打开目标普通文件（`O_RDONLY | O_NOFOLLOW`），核验目录链与文件 `fstat` 身份稳定性、文件大小、读取字节数与 SHA-256 摘要，阻断“打开时将目标文件或中间目录替换为根外同尺寸／同秒级 `mtime` 软链接、打开后立即恢复原根内路径”导致的根外内容泄露（闭合返回 HTTP `502` `"文件在读取期间发生变化"` 并记录 `stage = "post_check"`、`outcome = "failure"` 审计），同时保留根内合法路径（含 `..` 前缀合法文件名与 `0111` 仅遍历权限目录）、根内软链接、节点归属检查与开发环境开关边界；移除 `node_migrate_handler.go` 中零生产调用的 `directoryTreesEqual` 包装，保留 `snapshotDirectoryTree` 与 `directoryTreeSnapshotsEqual` 并沿真实 `POST /api/v1/nodes/:id/migrate` 覆盖等长字节篡改、元数据漂移与复制期间源目录变更的事务前拒绝；将 `golang.org/x/net` 从 `v0.60.0` 更新至 `v0.62.0`（修复 CVE-2026-78661，同步 `unix.LandlockRulesetAttr.Handled_access_fs`）；以及通过原生双架构采集更新 `deploy/build-inputs/lock.json`、八份 manifest 与 Worker 投影（Alpine 3.24 构建阶段锁定 `musl=1.2.6-r3`、`musl-utils=1.2.6-r3`、`zlib=1.4-r0` 修复 CVE-2026-107891／CVE-2026-85091，Alpine 3.23 运行阶段锁定 `musl=1.2.5-r24`、`musl-utils=1.2.5-r24`、`zlib=1.4-r0`、Core `openssh-client-default=10.2_p1-r1` 修复 CVE-2026-107891／CVE-2026-119709／CVE-2026-22184／CVE-2026-27171／CVE-2026-85091，并同步已移除旧包的 Core `tzdata=2026f-r0` 与 Worker `font-noto=2026.11.01-r0`）。明确节点文件预览要求远端节点支持在同一 SSH 连接上执行 `python3`，Worker 工具链及 pipeline 指纹随新 inventory 与 APK 投影推进，须排空旧 Core／Worker 在途请求与任务后同源重建并配套切换 Core、Worker、两架构闭包与签名制品，无新增公开 API、配置或 schema migration，回退会恢复打开句柄在路径恢复后返回根外文件内容的缺陷及上述旧依赖漏洞风险。

发布说明的归属以目标版本标题为边界：将完整的“行为与升级说明”移到目标版本节内，并清空已交付的 `Unreleased` 条目。仅在 CHANGELOG 顶部保留警告不够；合并前和发布后都须核对目标版本及 GitHub Release 正文实际包含这些警告。

Release Please 自动插入版本标题时可能将原有升级说明留在 `Unreleased`，生成的 PR 正文也可能只有提交摘要。此时原样移动 CHANGELOG 中已审阅的说明，并同步 PR 正文的目标版本节；两处都保留完整风险边界，再提交补充变更。
SSH 轮换预验证与同批安全补丁的说明作为一个完整版本节归档，不拆散业务升级边界和工具链回退风险。
若本版本同时补齐历史 Release 正文，须分别保留历史说明修订与本次新版本发布的边界；历史正文修改不重发历史镜像，也不能替代本次版本的构建、扫描、标签提升和证明验收。

同步后重新读取远端 PR 正文，将其版本说明与 CHANGELOG 的目标版本节逐条比对；不能仅凭编辑命令成功或本地文件正确就视为远端风险说明完整。版本发布后再对 GitHub Release 正文做同样核对，分别确认文件归属、PR 生成输入和最终公开说明。

镜像软件包锁定补丁的目标版本说明还须保留新锁定版本、同源 Core/Worker 重建、架构匹配的闭包及签名运行包更新要求；不能将包可安装或本地单架构构建成功等同于正式双架构发布通过。
Core 专属依赖修复应明确其范围，不能套用共享依赖的 Worker 指纹更新要求。c-ares `1.34.8-r0` → `1.34.9-r0` 须保留旧锁定不可安装、Core 重建替换后生效、回退恢复旧依赖风险、无新增配置或迁移的说明；它不属于 v0.73.2 的说明勘误，不回填到旧 tag，也不因此修改 Worker inventory。
Core curl `8.22.0-r0` → `8.23.0-r0` 同样保留不可安装旧锁定、同版本 libcurl 更新、整镜像重建替换和回退边界；不修改 Worker inventory，无新增 API、配置或迁移。若同时交付 zod `4.6.5` → `4.6.6`，另行保留开发环境重新安装、后续 lint 生效及回退工具链风险，不将开发依赖公告解释为生产应用可利用路径。
同批 Core Brotli 修复须保留 `brotli-libs` 从基础镜像的 `1.2.0-r0` 显式升级至 `1.2.1-r0`、CVE-2026-111497、整镜像替换后生效与回退恢复旧库风险；不得将 curl 更新成功等同于所有传递依赖均已修复。
共享 CGO 构建包 `musl-dev` 的 `1.2.6-r2` → `1.2.6-r3` 修复则须同步 Core/Worker 与生产 inventory，并按新指纹重新生成两架构闭包、签名运行包和 proof。发布说明保留旧锁定不可安装、重建替换后生效、切换前排空旧 Worker 在途处理及配套回退要求；无新增配置或 schema migration，不将 builder 补丁描述为运行镜像 libc 升级。
同一版本同时包含 Core 专属包、共享构建包及前端测试工具补丁时，按各条目分别保留影响范围和生效条件，再完整归入目标版本；不得将某一项“不改变 Worker inventory”的结论扩大到整个版本。
0.73.6 的 Worker Python 补丁须保留 `3.12.15-r0` → `3.12.16-r0`、CVE-2026-115557，以及四个 Python 子包的双架构完整输入更新。显式 root 与输入投影改变 Worker 工具链指纹，必须同源重建 Core/Worker，更新架构匹配闭包、签名 bundle 与 proof，排空在途处理后切换；CI 临时签名 fixture 不等于生产签名制品。本依赖补丁无新增配置或迁移，配套回退会恢复旧漏洞风险，且不能覆盖同版本业务迁移的降级限制。

人工补齐 Release PR 时，先核对 manifest 的目标版本与 CHANGELOG 版本标题一致，再原样迁移已审阅的升级警告；不要为了移动章节改写上一发行版的保留/删除边界。编辑 PR 正文必须保留 Release Please 生成的首尾标识及分隔符，只更新中间版本说明，否则合并后可能无法识别发布。该补充提交仍走提交钩子与 PR required checks，不直接修改正式 tag。已合并 PR 的正文编辑仍可能触发 CI；按[事件分类和并发规则](automation.md#actions-与-ci)核对运行及原 `push` CI 状态，不能用 PR CI 或 metadata 检查代替同 SHA 发布准入。历史运行重跑仍遵循其原工作流定义。

已发布版本若遗漏上述说明，通过文档 PR 将已审阅条目归入对应历史版本，并同步现有 GitHub Release 正文后重新读取核对；这是发布说明勘误，不修改版本号、移动或重打 tag，也不重新发布镜像。

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

告警升级只读预演的发行候选还须通过 PostgreSQL parity 中必选的
`TestEscalationPreviewPostgres`，核对两种模式的只读一致性快照与连接取消清理；
该门禁与 SQLite、真实后端 Chromium 验收互补，不能用无 DSN 的跳过结果代替。
完整行为与升级说明归档于 CHANGELOG `0.70.0` 版本节；Release PR 与最终
GitHub Release 正文须保留手动快照、非发送证明、同版本 Core／页面切换、
无新增迁移及回退边界，不将只读诊断解释为生产投递或生产部署验收。

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
- 恢复空态导流的发行说明须区分概览优先回退与真实恢复能力，保留 viewer/未知角色/身份切换的数据入口边界。新版页面加载后生效，无新增 schema migration、不自动创建恢复计划或任务；换版与回退都不撤销服务端已提交操作，回退旧页面会恢复空态及受限导航缺陷。
- 应用凭据引用反查的发行说明须保留 admin-only 安全 DTO、引用实时变化及删除独立重查的边界，不把列表解释为删除许可、持续快照、数据库外键或全局并发围栏。新版 Core 的计数失败/事务回滚与新版页面的身份隔离分别从换版后生效；前后端同版本切换，完成旧页面交互并排空旧 Core 在途凭据更新/删除请求，不追溯撤销已提交操作。无新增 schema migration，回退会恢复旧计数和定位缺陷，不回滚已有凭据/策略修改。
- 恢复码登录的发行说明须保留 ASCII 大小写及首尾空白容忍、无 Unicode 相似字替换、历史存储兼容和一次性消费边界。新版 Core 与页面同版本切换，升级前完成或退出旧登录交互并排空旧 Core 在途认证请求，不追溯撤销已提交消费。无新增 schema migration，回退恢复旧输入及交互限制，不恢复已消费码；不将 R-10 扩展为恢复码管理或管理员账户恢复。
- Search 规范化协议代次推进时，版本说明必须明确 Core/Indexer 同步升级、旧代 metadata postings 的异步 bounded rebuild、content/OCR 不因 metadata rebuild 自动完成，以及旧版回退不得复用新版 postings；不能把这一派生索引协议升级写成数据库 schema 迁移或仅替换镜像即可安全回滚。
- 纯前端视觉修复须明确新版资源加载后的生效时点、是否影响 API、schema 与在途服务端操作，以及回退是否恢复原有视觉缺陷；没有这些行为变化时直接说明，不附加无关的服务端排空要求。视觉修复的说明同样归入目标版本，不能因自动摘要已有修复标题而将生效及回退边界留在 `Unreleased`。
- 前端开发／构建／测试工具安全补丁须保留旧版与修复版、重新按锁文件安装和重启相关开发／测试进程的生效条件，以及回退旧锁文件的漏洞风险；区分开发工具攻击面与正式静态资源，不把工具链审计通过写成生产已升级或曾受攻击。Vite 内嵌 launch-editor 与 Vitest mocker 修复及验证要求见[前端依赖审计](automation.md#前端依赖审计)；Vitest 与 coverage 配套更新不扩大为生产运行逻辑或数据库变更。
- Go 标准库安全补丁须同步 module、Core／Worker／supercronic 构建镜像及 Worker 工具链指纹，重新生成架构匹配的闭包、签名 bundle 和 proof；说明二进制替换后的生效条件与回退旧标准库的风险。Go 1.27.3 的证书拒绝修复及验证要求见[Go 工具链升级](automation.md#go-工具链升级)，不得通过关闭证书校验规避。

0.73.6 的 Rclone 预检、续期与 Portable 保真修复发行说明须保留稳定身份密钥迁移的使用后降级保护、
Portable V2 写入后的旧运行时不兼容，以及数据库、匹配密钥和远端数据的配套恢复要求。
升级前暂停相关任务并排空旧 Core、scheduler、executor 及资产处理/清理在途操作，
按同一发行版本切换 Core、页面和启用的 Worker；新版行为不撤销旧版已提交的远端副作用。
bound config 改为执行节点私有可写配置后，普通 bound 读取也要求 SFTP 与可验证权限的 home；
远端退出未知或所有权无法重验时失败并保留必要残留，不能以全目录扫描删除代替人工确认。
Release PR 与最终 Release 正文须完整保留这些边界，真实本地 ticket/Serve 和受控 Native 验收
不等同于 AWS live 认证或生产部署；内容 ticket 仍使用原默认时限。

0.73.7 的 Rclone 发布结果保护发行说明须完整保留[未知发布结果与升级保护](../admin/backup-recovery.md#未知发布结果与升级保护)：
历史 failed 受管点缺少正面停止事实可阻止启动，未知 writer 到期后仍持续保护且没有自动解锁；
可选 JSON 字段虽无 SQL 列迁移，写入后旧 binary 严格解码不兼容。升级前停止旧 owners，
禁止新旧 writer 混跑；不得把删除事实或恢复 DB 快照写成 writer 已停止。Release PR 与公开
Release 均须保留此可用性代价，不能只发布“修复重试”标题。

历史版本的具体迁移编号和交付事件保留在 CHANGELOG/发行说明；当前迁移版本唯一声明在[后端入口](../../backend/README.md)。公开发布成功不代表生产已经升级或真实恢复已经验收。

告警快捷静默的完整行为与升级说明已归档于 CHANGELOG `0.61.0` 版本节；发行 PR 与最终 GitHub Release 正文均须保留该节全部边界，不能只发布自动生成的功能标题。

恢复空态导流的完整行为与升级说明归档于 CHANGELOG `0.61.1` 版本节；发行 PR 与最终 Release 正文须完整保留概览回退、导航角色、旧页面在途操作及无新增迁移的边界。
应用凭据引用反查的完整行为与升级说明归档于 CHANGELOG `0.62.0` 版本节；发行 PR
与最终 Release 正文须保留管理员权限、安全投影、实时引用与独立删除重查、旧操作
排空、无新增迁移及回退限制，不将公开发布解释为生产部署。
恢复码登录的完整行为与升级说明归档于 CHANGELOG `0.62.1` 版本节；发行 PR 与最终
Release 正文须保留输入归一化、一次性消费、旧认证请求排空、无新增迁移及回退不恢复
已消费码的边界，不增加 R-19 管理或管理员账户恢复能力。
本人恢复码管理的发行说明须保留当前 TOTP 校验、整批替换、旧码立即失效、明文仅展示
一次及结果未知不得自动重试的边界。新版 Core 与页面同版本切换，排空旧认证请求；
无新增 schema migration，回退不恢复旧码或已消费码，也不提供管理员账户恢复。
该批完整行为与升级说明归档于 CHANGELOG `0.63.0` 版本节，发行 PR 和最终 GitHub
Release 须同步保留，不以剩余数量查询或本地验收替代重新生成批次及生产部署证据。

批量命令显式重试的发行说明须保留来源可验证的失败子集、专用二次验证和逐节点授权、
永久运行证据、同失败运行唯一消费及 unknown 不自动重放的边界。迁移 93 启用前须保全
数据库、适用密钥与配置，停止并排空旧 Core、scheduler、executor 及相关 worker/清理
操作，按同一发行版本切换前后端与执行组件；禁止混用缺少 Evidence writer 的旧二进制。
存在 v1 批次或证据后拒绝降级，不能删记录或仅换旧镜像绕过；隔离恢复旧数据库也不能
撤销已发生的远端命令副作用。R-13 结果复制仅覆盖已加载内容，不代表完整日志或生产验收。
该批完整行为与升级说明归档于 CHANGELOG `0.64.0` 版本节，Release PR 与最终 GitHub
Release 正文须保留全部七项边界，不以自动生成的功能标题替代迁移、排空与降级警告。

轻量配置导入预览的发行说明须保留“当前快照预计值、非执行计划”、秘密 presence 与
有效凭据状态分离、pending 不计确定成功、skip 仍写设置及资产未预测的边界。
新版 Core 与页面同版本切换，升级前保全数据库、适用密钥与配置，完成或退出旧导入
交互并排空旧 Core 在途导入及设置运行时转换；关闭页面不撤销已提交写入。
本次无新增 schema migration，回退移除预览与对应界面保护但不撤销已导入配置，
既有迁移降级保护和配套恢复要求仍适用。Release PR 与最终 Release 正文须完整保留
CHANGELOG `0.65.0` 版本节的七项行为与升级说明，不将 R-15 第一阶段写成完整计划绑定或生产部署。

自动化历史安全标识与任务日志导航的发行说明须保留已有安全 DTO、成对目标、任务级而非
Run 定位、原生剪贴板无法撤回及当前权限独立判定的边界。新版页面加载后生效，完成或退出
旧页面交互并刷新客户端；本次不改后端读取/调度/执行，无需因只读界面改动额外排空任务，
也不撤销已提交操作。无新增 schema migration，回退旧页面恢复旧筛选与错误展示缺陷，
既有迁移降级和配套恢复要求仍适用。Release PR 与最终 Release 正文须完整保留
CHANGELOG `0.66.0` 版本节的七项行为与升级说明；
不得写成精确 Run 导航、新增历史元数据或生产部署。

临时授权只读状态展示修复的发行说明须区分存储状态、浏览器到期提示和实际服务端授权，
保留历史/未知状态、创建时间筛选及当前页/服务端总数的语义。新版页面加载后生效，
旧客户端须刷新；不改后端授权 gate、状态机或在途操作，无新增 schema migration，
无需因只读展示修复额外排空任务。回退恢复旧展示与请求状态缺陷，既有迁移降级及
配套恢复要求不变。Release PR 与最终 GitHub Release 正文须完整保留 CHANGELOG
`0.66.1` 版本节的七项行为与升级说明，不得表述为新增审批流、全量实时有效查询或生产部署。
该版同时修正 SFTP 取消回归的跨 goroutine 全序断言；保留精确取消结果及返回前资源恰好关闭一次，
保留独立的传输优先关闭回归，不改变生产恢复或关闭逻辑，不以重跑掩盖测试缺陷。

Toast 与有界启动体积修复的发行说明须保留唯一应用播报通道、同文通知/未发布更新语义、
可见浏览与键盘路径，以及按需凭据读取的取消、身份代次、认证屏障和不自动重试边界。
新版页面加载后生效，旧客户端须完成或退出交互并刷新；不改后端调度/执行，
无需额外排空任务，也不撤销已提交操作。无新增 schema migration，升级前保全数据库、
适用密钥与配置，回退会恢复旧主题、播报及迟到选项缺陷，既有迁移降级与恢复要求不变。
Release PR 与最终 GitHub Release 正文须完整保留 CHANGELOG `0.66.2` 版本节的五项行为与升级说明，
不将 Orca/AT-SPI/Speech Dispatcher/eSpeak 的 libao null-sink 自动化验收写成人工耳听、
其他读屏器或全站 WCAG 合规，不扩展为 Cron/Doctor 交付或生产部署。
该版同时修正 SSH 轮换/普通编辑 E2E 的通知定位范围；保留真实请求与存储结果断言，
不改变产品播报路径，也不以重跑失败场景代替选择器修复。

剩余控制台 UX 的发行说明须保留一次性权限通知、健康原因与来源诊断分离、只读安全导航、
Cron 的 UTC/表达式时区及服务端 nextRunAt 权威、Doctor 代次/剪贴板/只聚焦不执行、
标准公钥指纹与旧私钥摘要兼容的边界。CSV 新字段仅追加到尾部；有限样本预览不代表
服务端当前可导出集合。新版 Core 与页面同版本切换，完成或退出旧交互并刷新客户端，
按正常换版流程排空旧 Core 在途请求，不额外排空执行中的备份任务，也不撤销已提交操作。
无新增 schema migration，升级前保全数据库、适用密钥与配置；回退恢复旧 UX 缺陷，
既有迁移降级保护不变。完整七项行为与升级说明归档于 CHANGELOG `0.66.3` 版本节，
Release PR 和最终 Release 须原样保留，不将 null-sink 读屏证据写成人工耳听，不把 generator-only
nextRunPreview、生产部署或灾备恢复纳入本轮完成声明。

CronGenerator 运行预估修复的发行说明须保留任务/策略有限 UTC 预估与服务端 nextRunAt
权威、报表管理员只读接口与既有简化 matcher/日周 AND、分钟窗口非执行或投递保证、
显式请求及身份/过期失效边界。新版 Core 与页面同版本切换，完成或退出旧编辑交互并
刷新客户端，按正常换版流程排空旧 Core 在途请求；不改真实调度，不额外排空执行中的
备份任务，也不撤销已开始或已提交操作。无新增 schema migration，升级前保全数据库、
适用密钥与配置；回退恢复旧预估缺陷或移除报表预览，既有迁移降级与恢复要求不变。
Release PR 与最终 GitHub Release 正文须完整保留 CHANGELOG `0.66.4` 版本节的五项行为与升级说明；
不得将 v0.66.3 未重写 generator-only 预估的历史范围改写为本次修复已在旧版生效。

离线原账户恢复的完整说明归档于 CHANGELOG `0.67.0` 版本节；Release PR 与最终
GitHub Release 正文须完整保留该节七项行为与升级边界：
三种显式重置范围、原身份保留、控制终端与停机前提、受限绑定不建立会话、
未知提交不得重放、迁移 94 使用后降级保护及旧库配套恢复限制。
Core、页面与执行组件同版本切换，禁止混跑不识别受限标志的旧 Core；
公开发布不授权生产恢复，也不证明远端实例已经停机或已完成真实账户恢复。

精确历史 Run 导航的完整说明归档于 CHANGELOG `0.68.0` 版本节；Release PR 与最终
GitHub Release 正文须保留该节七项行为与升级边界：成对标识及先核对后读取、手动快照
而非实时跟踪、当前权限及失败清缓存、仅已加载日志的搜索与导出、身份和请求隔离、
新版页面生效与旧客户端刷新、无新增迁移及回退限制。不因只读页面改动额外排空任务，
也不撤销已提交操作；升级前保全数据库、适用密钥与配置，既有迁移降级保护仍适用。
不得改写 `0.66.0` 仅任务级导航的历史范围，不将精确 Run 日志解释为完整执行证据、
新增历史元数据、生产部署或灾备恢复验收；两条 lint warning 修复保持原通知与诊断行为。

自动化历史第二阶段安全详情与关联追踪的发行说明须完整保留 CHANGELOG `0.71.0` 的七项
行为与升级边界：阶段不是最终成功或通知投递证明，来源与动作目标分别使用历史 ID，
durable 事务失败不补写外部失败日志，legacy 调用失败可能部分生效且不自动重试，
新版元数据不回填，身份切换无法撤回已发出的系统剪贴板写入。Core 与页面同版本切换，
保全数据库、匹配密钥与配置，按正常换版流程排空旧 Core 在途请求并刷新客户端；
不因历史展示额外停止备份任务。无新增 schema migration，回退不撤销已有动作与历史，
既有降级保护仍适用。完整说明已归入该发行版本，Release PR 和最终 Release 须同步保留，
不把双引擎生产者验收或合成导航日志写成真实备份成功、生产部署或灾备恢复验收。

通知配置上下文闭环的发行说明须保留管理员入口、保存留页与人工返回、同一安全告警
目标及真实失败状态、当前文档／身份来源和迟到回执失效的边界。新版页面加载后生效，
完成或退出旧编辑交互并刷新客户端，按同一发行版本交付 Core 与页面；不改变后端
调度、投递或任务执行，不因本次前端改动额外排空备份任务，也不撤销已提交配置。
无新增 schema migration，升级前保全数据库、匹配密钥与配置；回退旧页面会恢复旧
导航与迟到响应缺陷，但不删除配置、静音或既有投递。Release PR 与最终 GitHub
Release 正文须完整保留 CHANGELOG `0.72.0` 版本节的五项行为与升级说明，不把配置返回
或本地浏览器验收解释为通知送达、生产部署或灾备恢复证明。

系统数据库下载的完整说明归档于 CHANGELOG `0.69.0` 版本节；
Release PR 与最终 GitHub Release 正文须完整保留其功能与升级说明：管理员专用 fresh proof 和临时授权、Web SQLite 与 cron
SQLite/PostgreSQL 双来源、两成员有界 USTAR，以及目录保存拒绝同名目标和同源锁的边界。
目录须由操作者独占使用；锁不保护其他 origin/profile、旧页面或外部程序，
取消、close 失败及文档丢失不保证撤销，不能将服务端发送完成解释为客户端落盘或恢复成功。
兼容路径只允许最多 64 MiB payload，不宣称与原生路径具有相同峰值内存。
按同一版本切换 Core、页面、备份脚本与 Nginx，结束旧下载和页面交互、排空旧 Core
在途请求后刷新页面；升级前保全数据库、匹配密钥与配置。tar 不包含密钥、
外部配置、known_hosts 或远端数据，仍须独立校验并在隔离目标执行既有离线恢复。
无新增 schema migration、不改写历史记录；回退移除新下载及保存保护，
不撤销已交付文件或外部效果，不将公开发布写成生产部署或生产灾备验收。
下载验收用例归属真实后端 Playwright 配置，不由普通 mock 矩阵收集；
脚本回归须在 Alpine/BusyBox 环境验证校验文件可消费及迁移目录后的真实恢复，
不能仅用已额外安装 GNU coreutils 的本地容器替代默认 CI 镜像证据。
v0.69.0 的发行源码因 Alpine 3.23 移除 `libexpat=2.8.5-r0` 而未通过镜像准入；
v0.69.1 补丁将 Core 固定为 `2.9.0-r0`，不得移动旧 tag 或将旧 GitHub Release
解释为镜像发布成功。Release PR 与最终 GitHub Release 正文须完整保留 CHANGELOG `0.69.1` 版本节：旧包不可重建、回退恢复旧库、
同版本切换与旧操作排空、无新增迁移，以及 v0.69.0 全部下载/恢复边界。
按新补丁源码重新取得主干 CI、双架构镜像扫描、正式标签与 provenance。


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

真实私钥替换 step-up 的发行说明须保留条件材料比较、主会话绑定及 300 秒可复用
proof、普通编辑离线能力、轮换完整库存验证和配置导入独立授权链。Core 与页面同版本
切换，完成或退出旧密钥交互、排空旧 Core 在途请求并刷新客户端；新门禁不追溯撤销
旧版已提交操作。无新增 schema migration，升级前保全数据库、匹配密钥、配置及主机
信任文件；回退旧版会移除门禁与身份保护、恢复旧并发回写风险，但不还原已替换私钥。
Release PR 与最终 GitHub Release 须完整保留 CHANGELOG `0.73.0` 的七项行为与升级说明，
不把结果未知写成确定未保存，也不宣称一次性 proof、远端公钥部署或生产恢复验收。

## Docker Hub 描述同步

[Sync Docker Hub Description](../../.github/workflows/dockerhub-description.yml)在 `README.md` 或该工作流文件变更并 push 到 `main` 时运行，也支持手动触发。短描述取 GitHub 仓库 description（为空时使用工作流默认文本），长描述取根 README。

缺少元数据凭据时任务会报告跳过而不是失败；绿色 workflow 不等于描述已更新。涉及 README/发布文档的交付需检查这项自动化的实际结果。

## 手动重发与私有部署

`publish-images.yml` 的 `workflow_dispatch` 仅用于已有稳定版本的推送故障恢复、重建或补发证明。填写 `version` 和 `source_ref`，人工确认二者对应同一正式代码；脚本验证来源 SHA 的主干 CI，但不会替代版本与源码对应关系的审阅。手动重发仅写版本标签，不移动 `latest`，也不替代 GitHub Release。

当前工作流兼容完全未采用构建输入归档合同的旧源码，只跳过该新增输入锁检查。
源码中只要出现归档脚本、输入目录、Worker 投影或 Dockerfile 归档消费痕迹，就必须
通过完整校验，缺少部分文件不能退回旧模式。两种来源均继续使用当前工作流的
同 SHA 主干 CI 准入、双架构构建及漏洞门槛；旧源码未归档的 APK 仍可能已下架，
保留这个入口不保证旧版本能成功重建，也不自动触发任何已有版本的补发。

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
