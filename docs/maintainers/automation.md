# 仓库自动化维护

新增外部依赖必须有明确需求并经过评审；优先复用项目已有能力，不以版本更新自动化替代必要性与兼容性判断。

## 依赖更新

[Dependabot 配置](../../.github/dependabot.yml)按月更新 Go、npm 与 GitHub Actions，按生态分组；npm 生产与开发依赖分别成组，常规更新限定 minor/patch。不要未经维护者决策改回每周、每依赖一条 PR。major 升级单独评估兼容性、上游发行说明和完整验证。

安全告警与安全修复不受常规版本更新排期约束。替换旧机器人 PR 前先记录精确 PR 编号与 head 分支，只关闭该名单；不得用动态“全部关闭”查询误伤新安全 PR。

### Go 工具链升级

项目使用 Go 1.27.1；CI 从 `backend/go.mod` 读取版本。Core、Worker 和
supercronic 的构建镜像同步固定到 Go 1.27.1 / Alpine 3.24 的多架构摘要，
Worker 的 `BuilderBase` 运行环境指纹和镜像检查脚本须随之更新。
保留 SQLite 所需的 CGO，以及与运行镜像兼容的 musl；升级 Go 不自动升级 Alpine 主线。

Go 升级不要求所有模块跟随更新。先核对上游兼容说明、构建标签、CGO、
运行时内部接口和漏洞结果，仅升级不兼容或需要安全修复的依赖；不要用批量
`go get -u` 扩大变更。`go.mod` 的版本指令也会影响运行时兼容默认值，必须在
修改指令后重新验证，不能仅复用新编译器搭配旧指令的结果。

SSH 轮换预验证候选将 `golang.org/x/net` 从 v0.59.0 定向更新到 v0.60.0，
修复 HTTP/2 安全公告；未升级其他模块。验收继续使用固定版本 govulncheck
及完整本地 CI 门禁，不以依赖升级代替业务验证。

验收包括模块完整性、完整后端测试与构建、lint、漏洞扫描、并发敏感路径的 race、
SQLite / PostgreSQL 行为，以及目标镜像的启动和 `/readyz`。
amd64 验证不能替代 arm64 原生 Worker 沙箱验收；本地通过也不代表线上已部署。

Go minor/patch 依赖更新也须沿生产者与消费者验证兼容性：STS 的可选 token
指标按[仓库准入合同](../spec/domains/backup-repository.md)解释，不能沿用已取消的
packed-policy-size 阈值；pgx 的 libpq URI 解析将 `+` 视为字面量，隔离测试通过
直接 `search_path` 参数选择 schema，不把 `options` 中的空格编码成 `+`。
Unicode 规范化输出变化须推进持久化 Search normalizer 版本，并验证旧代失效、
调度重建和查询恢复；升级及降级边界见[搜索合同](../spec/domains/backup-search.md)。

## 前端依赖审计

更改锁文件、升级依赖或排查本地与 CI/Docker 差异时，使用与 [CI](../../.github/workflows/ci.yml)及 [Docker web-builder](../../deploy/allinone/Dockerfile)一致的 Node 主版本，当前两者均为 Node 20。不要把 Actions 自身的 JavaScript runtime 与项目 Node 版本混为一谈。

审计必须包括 `devDependencies`：Vite、Vitest、ESLint 和构建工具也是攻击面。开发 shell 的 `NODE_ENV=production` 会让 npm 隐藏开发依赖，不能采用该结果作为通过证据。专项复现从干净安装开始：

```bash
env -u NODE_ENV npm --prefix web ci
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
```

现有合同仍要求 moderate 级完整审计。当前 CI 调用 [check-npm-audit.sh](../../scripts/check-npm-audit.sh)，实际只阻断未列入 GHSA allowlist 的 high/critical，不能单独证明满足上述要求。脚本还忽略 npm 本身的退出码，未验证 JSON 是否为有效审计结果；排障时必须确认获取到完整漏洞报告，不能把错误响应或缺失结果当作通过。这是实现与有效合同的差异，不能通过降低文档要求消除。

优先在现有 semver 范围内修复锁文件，不用 `--force` 或未经审查的 major 升级压制审计。锁文件变更后执行贡献指南的前端完整门禁，并完成以下核对：

- 结构化比较 `packages` 记录，确认只改了预期依赖。npm writer 差异可能删除未变平台包的 `cpu`、`os`、`libc`、`optional` 元数据；拒绝或恢复无关变化。仅锁文件修复应保持 `package.json` 不变。
- 分别记录实际漏洞摘要、退出码和去重后的 GHSA 集合。一个公告可能经多个父依赖传播，不能用包计数下降或 GHSA 减少单独宣称通过；结合 `via`、`nodes`、`npm ls`、`npm explain` 核对依赖路径。
- 本地通过而 CI 失败时，先核对 Node/npm、环境变量和干净锁文件安装，不能绕过审计。

构建链中的 `brace-expansion` 必须同时核对 ESLint/minimatch 的旧主版本分支与 TypeScript ESLint 的新分支，不能只更新顶层解析结果。针对括号展开拒绝服务公告，分别在父依赖允许的 semver 范围内更新锁定补丁版本，保持 `package.json`、平台选择元数据和审计阈值不变；使用 CI Node 主版本干净安装后执行完整审计与前端门禁。

`source-map-js` 的锁定补丁从 1.2.1 更新为 1.2.2；按上述规则保留父依赖范围与
平台元数据，并使用包含开发依赖的完整审计及前端门禁验证。正式交付仍须重新构建镜像，
回退旧镜像会恢复旧依赖，不能仅凭应用回归结果判断安全性。

### jsdom 选择器兼容性

[package.json](../../web/package.json)中的 jsdom 专属 override 将 `nwsapi` 固定为已验证的 2.2.25。既有回归记录表明 jsdom 26 下 2.2.26/2.2.27 的 `:modal` 匹配会递归进入原生匹配适配器，导致下拉交互超时。调整 override 前，在 CI Node 版本下同时验证独立选择器匹配、节点和通知下拉确认测试，保留完整审计及前端门禁；不得通过增加超时或修改产品交互掩盖问题。

## Actions 与 CI

[CI](../../.github/workflows/ci.yml)使用 `pull_request` 检查 PR，push 仅监听 `main`，避免同一 PR 提交重复运行。required checks 的设置与交付步骤见贡献指南；发布链路与凭据归[发布手册](release.md)。

PR 打开、更新提交、重新打开及编辑时都会运行 CI；编辑事件用于重新校验修改后的
Conventional Commits 标题。旧运行的重跑仍使用原始事件中的标题，应以编辑后触发的
新运行结果为准。

后端 lint 固定 `golangci-lint v2.14.0`（支持 Go 1.27），CI action 与
`scripts/lint-backend.sh` 同步更新。本地通过带版本的 `go run` 隔离工具依赖，
以当前选中的 Go 工具链构建并运行，避免系统 Go 升级后继续加载由旧 Go 构建的 linter。
该方式不改项目模块或全局安装；升级 linter 后执行配置校验、全仓库 lint 和本地完整门禁。

浏览器 CI 将 mock 三浏览器矩阵与真实后端场景分开：默认配置排除
`real-backend-smoke.spec.ts` 和 `lifecycle-p1.spec.ts`；真实配置的 `chromium`
项目依赖 `chromium-smoke`，在同一隔离后端上串行执行。Ubuntu runner 安装
Playwright 系统依赖、OpenSSH server 与 sqlite3，由脚本生成独占 SSH/数据库
fixture 并清理；不接受共享手工 fixture 作为自动化通过证据。命令、严格主机密钥
校验与清理边界见[测试指南](../spec/guides/testing.md)。

更新工作流 action 时：

- GitHub 官方和第三方 action 均固定完整 commit SHA，并保留版本 tag 旁注。
- 修改前用 `git ls-remote` 解析目标 tag；附注 tag 应解析到实际提交。
- 核对目标版本的 `action.yml` 或 `action.yaml`，确认 `runs.using` 兼容当前 GitHub Actions runtime。JavaScript action 的 Node 24 迁移要求不得用临时 Node 20 opt-out 长期绕过。
- 已归档或废弃的 action 应迁到受维护上游，再更新版本。Release Please 使用 `googleapis/release-please-action`。
- 修改发布工作流时分别验证 amd64/arm64 构建和扫描的实际平台选择；multi-arch manifest 成功不能证明每个平台扫描正确。

### Core 双架构持续构建的证据边界

CI 的 `docker-build` 作业在 `ubuntu-24.04` 与 `ubuntu-24.04-arm` 上分别原生构建
`amd64` 和 `arm64` Core 镜像，不安装 QEMU。该作业使用固定 SHA 的 Buildx
Actions，以 `push: false`、`load: true` 将单平台镜像只加载到当前 runner；不登录
Docker Hub、不读取发布凭据，也不创建远端 digest、manifest 或 provenance
attestation。

每个矩阵任务都检查本地镜像的 `.Os`、`.Architecture` 与矩阵值匹配，并要求
`.Id` 与 Buildx `imageid` 输出相等且非空；Trivy 使用该本地 image ID，并通过
`TRIVY_PLATFORM`、`HIGH,CRITICAL`、`exit-code: 1` 和 `ignore-unfixed: true` 保持
严格扫描门槛。随后使用架构专属的 Compose tag/project 执行 Core readiness
smoke。因而该作业证明候选源码在两种原生平台上的构建、扫描和临时运行时检查，
但不能证明 Docker Hub 已收到镜像或已完成正式发布。

正式发布所需的按 digest 推送、远端 digest 扫描、multi-arch manifest/tag 提升、
凭据使用和 provenance attestation，仍只由[发布手册](release.md)规定的发布工作流
提供证据；不得用持续 CI 的绿色结果替代这些证据。

仓库不依赖 Codecov 账户或上传令牌。覆盖率由 CI 自行阻断：后端总覆盖率和备份资产专项阈值、前端非空 LCOV；竞态、漏洞、PostgreSQL、浏览器和容器验收各自保留。

备份职责收敛后，CI 的 race 列表不再引用已删除的节点 probe、metrics、系统日志或服务 uptime 包，也不运行服务监控配置的专属 race 步骤；按需 SSH、任务、备份资产及投递并发检查保留。创建默认值的双引擎 parity 仅保留策略创建与调度行为。PostgreSQL 的 alerting/escalation 必需选择器包含节点及服务来源退役迁移后的直接投递与升级投递围栏测试，数据库选择器同时覆盖退役 schema 和启动保护器漂移。删除看板只移除其专用网格布局依赖，任务图表仍使用 Recharts；锁文件和 bundle budget 继续按上述门禁验证。

通知投递统计的 SQLite／PostgreSQL 回归共用退役分类和权限夹具；PostgreSQL job 通过 `run-required-postgres-tests.sh` 必跑 `TestAlertDeliveryStatsPostgres`，防止缺少 DSN 或空选择器伪装通过。该入口与 alert delivery migration contract 相邻，但验证实际 handler 聚合，不替代迁移围栏测试；统计口径见[告警合同](../spec/domains/alerting-health.md#投递统计与窗口状态)。

自动化执行历史在 PostgreSQL job 中通过同一 required runner 分别执行 `TestAutomationRuleLogsPostgres`（过滤、分页、安全 DTO、数据库故障）和 `TestAutomationRuleLogsDispatcherPostgres`（真实 Dispatcher 的 legacy 错误、durable 提交/回滚到认证路由的读取）。两者使用隔离 schema，缺少 DSN 或空选择器不构成通过；不以 SQLite 结果代替 PostgreSQL，也不修改持久模型或执行引擎。行为合同见[自动化规则](../admin/automation.md#执行记录)。

通知页失败任务摘要的 PostgreSQL handler parity 通过同一 runner 必跑
`TestTaskFailureSummaryPostgres`，与 SQLite 共用时间窗口、任务去重、运行时节点快照
授权、历史删除及错误处理矩阵。缺少 DSN 或空选择器不得视为通过；该独立摘要不改变
旧 POST 任务统计的当前节点授权，具体合同见[任务执行与恢复](../spec/domains/task-execution-recovery.md#通知页失败任务摘要)。

RecoveryPoint 生产者历史保全由双引擎回归覆盖。PostgreSQL durable task runtime
选择器包含 TaskRun retention 回归；其后的 producer retention consumers 步骤通过
`run-required-postgres-tests.sh` 必跑
`TestRecoveryPointProducerRetentionConsumersPostgres` 和
`TestRecoveryPointProducerRetentionPublicationPostgres`，验证完整迁移库上的真实
Manager 清理、搜索及生命周期解析消费者和 Prepare 并发边界。缺少 DSN、空选择器或
跳过 PostgreSQL 不构成通过；保留规则归属[任务执行与恢复合同](../spec/domains/task-execution-recovery.md)。

Recovery 授权收据的 PostgreSQL 必需步骤使用同一 runner 执行
`TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres` 和
`TestRecoveryAuthorizationReceiptReaperHandlesAllEffectKindsPostgres`：
exact-mirror 删除授权必须基于真实已暂停的执行证据验证事务回滚；清理用例通过真实
授权生成短有效期收据，等待数据库时钟确认到期，不修改不可变收据或绕过数据库约束。

真实后端 Playwright smoke 在独立 CI 步骤先编译服务以准备 Go 构建缓存，避免冷缓存依赖下载和首次编译消耗浏览器 webServer 的就绪窗口。smoke 仍由隔离脚本构建并启动临时二进制与数据库，保留原有就绪超时及真实浏览器断言，不复用外部运行中的服务。

独立 Mock 可访问性走查使用 `web/` 下的 `npm run walkthrough`，不属于普通
`npm run e2e` 的收集范围，也不替代上述真实后端 smoke。该命令独占启动 Vite，
执行固定 464 场景，并输出候选指纹绑定的运行目录；在根目录显式执行
`node web/scripts/compile-walkthrough-evidence.mjs --run <目录>` 验证证据。
矩阵、严格 Mock、重试与缺失测量规则统一见[前端质量合同](../spec/frontend/quality-guidelines.md#独立-mock-浏览器走查)。

## 镜像构建依赖

基础镜像以 digest 固定，显式安装的 Alpine 包使用精确版本。软件源可能移除旧包：安装失败时在锁定基础镜像中复现，分别核对该 Alpine 分支的 amd64 与 arm64 软件源，再更新必要锁定。`apk` 显示的已安装版本不代表软件源仍可安装。

共享运行时加密库的补丁须同步 Core/Worker Dockerfile、生产工具链 inventory 和对应安装合同检查；指纹由 inventory 派生，不手工伪造摘要。按新源码重新生成两架构运行时闭包及证明，不能沿用旧包版本的已签名运行包。跨架构查询软件源时使用基础镜像内该架构的可信 Alpine 密钥，不能关闭签名验证，也不能以滞后的网页列表替代实际已验证索引。

保留完整 Compose smoke、Worker 对应检查和漏洞扫描，不能用单一架构成功替代另一个架构。维护性依赖更新也应按发布手册检查合并后自动化，不手动提升版本或重发镜像。
