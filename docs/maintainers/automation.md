# 仓库自动化维护

新增外部依赖必须有明确需求并经过评审；优先复用项目已有能力，不以版本更新自动化替代必要性与兼容性判断。

## 依赖更新

[Dependabot 配置](../../.github/dependabot.yml)按月更新 Go、npm 与 GitHub Actions，按生态分组；npm 生产与开发依赖分别成组，常规更新限定 minor/patch。不要未经维护者决策改回每周、每依赖一条 PR。major 升级单独评估兼容性、上游发行说明和完整验证。

安全告警与安全修复不受常规版本更新排期约束。替换旧机器人 PR 前先记录精确 PR 编号与 head 分支，只关闭该名单；不得用动态“全部关闭”查询误伤新安全 PR。

离线账户恢复工具直接使用已锁定的 `golang.org/x/term v0.46.0` 读取控制终端，
因此在 `backend/go.mod` 声明为直接依赖；本次不升级该版本、Go 工具链或其他模块。
验证须包含真实 PTY 的无回显与重定向输入拒绝，不能以模块下载或编译成功替代。

### Go 工具链升级

项目使用 Go 1.27.3；CI 从 `backend/go.mod` 读取版本。Core、Worker 和
supercronic 的构建镜像同步固定到 Go 1.27.3 / Alpine 3.24 的多架构摘要，
Worker 的 `BuilderBase` 运行环境指纹和镜像检查脚本须随之更新。
保留 SQLite 所需的 CGO，以及与运行镜像兼容的 musl；升级 Go 不自动升级 Alpine 主线。

Go 升级不要求所有模块跟随更新。先核对上游兼容说明、构建标签、CGO、
运行时内部接口和漏洞结果，仅升级不兼容或需要安全修复的依赖；不要用批量
`go get -u` 扩大变更。`go.mod` 的版本指令也会影响运行时兼容默认值，必须在
修改指令后重新验证，不能仅复用新编译器搭配旧指令的结果。

SSH 轮换预验证候选将 `golang.org/x/net` 从 v0.59.0 定向更新到 v0.60.0，
修复 HTTP/2 安全公告；未升级其他模块。验收继续使用固定版本 govulncheck
及完整本地 CI 门禁，不以依赖升级代替业务验证。
远端标准库扫描要求 Go 1.27.3 安全补丁；本地验证须显式使用
`GOTOOLCHAIN=go1.27.3`，避免宿主工具链与 CI 不同导致错误复用证据。

验收包括模块完整性、完整后端测试与构建、lint、漏洞扫描、并发敏感路径的 race、
SQLite / PostgreSQL 行为，以及目标镜像的启动和 `/readyz`。
amd64 验证不能替代 arm64 原生 Worker 沙箱验收；本地通过也不代表线上已部署。

Go 1.27.2 → 1.27.3 修复 [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809)
（CVE-2026-111943）：`crypto/x509` 拒绝已不可信的中间证书。须重新构建并替换
Core、Worker 与 supercronic；只更新宿主 Go 或运行时环境变量不会修补已有二进制。
BuilderBase 变化会推进 Worker 工具链指纹，闭包、签名 bundle 与 proof 必须按
同一源码和目标架构重新生成，不能沿用旧制品。无新增 schema migration；回退旧镜像
会恢复旧标准库风险，不得通过关闭证书校验恢复连接。

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

Vite 的锁定补丁从 7.3.6 更新为 7.3.7，修复其内嵌 launch-editor 的
[Windows 转义](https://github.com/advisories/GHSA-698v-4r3x-3vcv)、
[编辑器参数解释](https://github.com/advisories/GHSA-r825-w8mm-5pxw)及
[Windows 路径检查](https://github.com/advisories/GHSA-587h-7wmj-h5pm)公告。
保持 `package.json` 的既有范围、其他锁定包和平台元数据不变；不要通过新增
顶层 launch-editor override 代替升级 Vite 内嵌代码。重新按锁文件安装并重启开发
服务后生效；正式构建仍验证入口资源与体积预算。该修复不改变 Core API 或数据库，
也不表示既有部署受到了攻击；回退锁文件会重新引入旧工具链漏洞。

Vitest 与 coverage-v8 配套锁定从 4.1.11 更新为 4.1.12，修复 `@vitest/mocker`
的[手动 mock 代码注入](https://github.com/advisories/GHSA-qxg6-89mg-rqv8)及
[Windows 路径检查绕过](https://github.com/advisories/GHSA-xp26-3454-mjgp)。
保留现有声明范围、其他依赖与平台元数据；按新锁文件重新安装并重启相关开发／测试
进程后生效。上游公告涉及可访问的 mocker 插件开发服务，不等于生产服务受攻击；
无新增配置或数据库迁移，回退旧锁文件会恢复旧工具链风险。

ESLint React Hooks 插件的开发依赖 `zod` 从 4.6.5 锁定升级至 4.6.6，
修复[集合校验资源耗尽](https://github.com/advisories/GHSA-mrhx-c7xc-m3w5)公告。
仅更新父依赖允许范围内的锁定补丁，保留声明范围、其他依赖与平台元数据；
按新锁文件重新安装后对后续 lint 进程生效。该包属于开发工具链，不据此声称
生产应用存在可利用路径；无 API 或数据库迁移，回退旧锁文件会恢复旧依赖风险。


### jsdom 选择器兼容性

[package.json](../../web/package.json)中的 jsdom 专属 override 将 `nwsapi` 固定为已验证的 2.2.25。既有回归记录表明 jsdom 26 下 2.2.26/2.2.27 的 `:modal` 匹配会递归进入原生匹配适配器，导致下拉交互超时。调整 override 前，在 CI Node 版本下同时验证独立选择器匹配、节点和通知下拉确认测试，保留完整审计及前端门禁；不得通过增加超时或修改产品交互掩盖问题。

## Actions 与 CI

[CI](../../.github/workflows/ci.yml)使用 `pull_request` 检查 PR，push 仅监听 `main`，避免同一 PR 提交重复运行。required checks 的设置与交付步骤见贡献指南；发布链路与凭据归[发布手册](release.md)。

PR 打开、更新提交、重新打开及编辑时都会运行 CI；编辑事件用于重新校验修改后的
Conventional Commits 标题。旧运行的重跑仍使用原始事件中的标题，应以编辑后触发的
新运行结果为准。

PostgreSQL job 只通过 `bash ../scripts/run-required-postgres-tests.sh --evidence-dir ...`
执行 `scripts/required-postgres-tests.json` 的统一库存，工作目录为backend；不再维护多个
宽regex、位置参数suite或直接Go测试旁路。独立backup/restore步骤保留。库存/原生声明/
AST候选校验、默认及覆盖timeout、14个required环境开关和逐测试事件失败规则见
[数据库合同](../spec/backend/database-guidelines.md#双引擎回归证据)。CI还运行隔离fake-process
协议自测，并在成功或失败时上传逐组JSON事件、脱敏stderr及summary；fake协议通过不代表真实PG通过。
证据文件写入、输出句柄关闭或读取句柄关闭失败也使 runner 失败；测试进程成功不能覆盖证据落盘或读取收尾错误。

库存保留批量重试的事务竞争、授权、执行证据和迁移约束，配置导入/导出的BothEngines、
混合依赖环及HTTP静态场景，以及SSH替换/轮换的真实节点库存、锁后材料重分类、撤销、
proof到期、并发导入与结果未知清理。每个必需PG子路径均须实际run/pass，不能以SQLite、
父测试通过或空选择器掩盖缺失分支；新增PG声明与场景必须同步库存并通过独立census。

升级策略只读预演的 `TestEscalationPreviewPostgres` 仍在库存中，使用随机隔离schema
验证真实路由权限、`REPEATABLE READ READ ONLY`、并发快照、零写入及取消清理。
Chromium真实后端单独选择 `escalation-preview.spec.ts`；普通mock配置排除它，
Firefox/WebKit下载项目不扩大。业务边界见
[升级预演合同](../spec/domains/alerting-health.md#升级策略只读预演)。

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

备份职责收敛后，CI race列表不再引用已删除的节点probe、metrics、系统日志或服务
uptime包，也不运行服务监控配置专属race；按需SSH、任务、备份资产及投递并发检查保留。
统一PG库存包含退役后的直接/升级投递围栏、退役schema与启动保护器漂移、策略创建及调度双引擎parity。
删除看板只移除其专用网格依赖，任务图表仍使用Recharts；锁文件和bundle budget门禁不变。

通知投递统计、自动化执行历史、失败任务摘要及生产者历史保全仍由各自领域测试证明；
它们现在由统一库存串行选择，而非相邻独立selector步骤。库存包含
`TestAlertDeliveryStatsPostgres`、`TestAutomationRuleLogsPostgres`、
`TestAutomationRuleLogsDispatcherPostgres`、`TestTaskFailureSummaryPostgres`、
`TestRecoveryPointProducerRetentionConsumersPostgres` 和
`TestRecoveryPointProducerRetentionPublicationPostgres`，保留原授权、真实Dispatcher、
迁移库Manager清理、搜索/生命周期解析及Prepare并发语义，不以optional skip代替PG。

Recovery授权收据库存保留
`TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres` 和
`TestRecoveryAuthorizationReceiptReaperHandlesAllEffectKindsPostgres`：
exact-mirror删除授权基于真实已暂停执行证据验证事务回滚；清理用例通过真实授权生成
短有效期收据并等待数据库时钟，不修改不可变收据或绕过数据库约束。

真实后端 Playwright smoke 在独立 CI 步骤先编译服务以准备 Go 构建缓存，避免冷缓存依赖下载和首次编译消耗浏览器 webServer 的就绪窗口。smoke 仍由隔离脚本构建并启动临时二进制与数据库，保留原有就绪超时及真实浏览器断言，不复用外部运行中的服务。

独立 Mock 可访问性走查使用 `web/` 下的 `npm run walkthrough`，不属于普通
`npm run e2e` 的收集范围，也不替代上述真实后端 smoke。该命令独占启动 Vite，
执行固定 464 场景，并输出候选指纹绑定的运行目录；在根目录显式执行
`node web/scripts/compile-walkthrough-evidence.mjs --run <目录>` 验证证据。
矩阵、严格 Mock、重试与缺失测量规则统一见[前端质量合同](../spec/frontend/quality-guidelines.md#独立-mock-浏览器走查)。

## 镜像构建依赖

根 `.dockerignore` 用通用的 `.*` 规则排除仓库根目录的隐藏配置、工具状态与本地
worktree，不按工具名称维护清单；Docker 自身所需的构建控制文件仍按客户端规则处理。
这些排除规则保护构建上下文传输，不仅是最终镜像内容。更换或退役工具时须保留这层
保护，并验证 Core/Worker 的 `COPY` 输入仍可用；模块内的必要隐藏构建文件不应被
根目录规则误排除。

基础镜像、完整 APK 事务和显式精确版本由输入 lock 绑定。Core/Worker 的 APK 安装
使用 `RUN --network=none`，只读取固定 digest carrier 的 `/inputs`；禁用软件源
解析与在线回退。软件源仍可能移除旧包，因此新输入的采集必须分别在该 Alpine
分支的 amd64 与 arm64 原生环境完成。`apk` 显示的已安装版本不代表软件源仍可安装。

### 构建输入保留与更新

精确版本更新仍负责安全维护；历史可重建性还需要保存原始输入。
本仓库采用完整 APK 输入归档，并保留它们安装所依赖的原始基础镜像：

| 路径 | 能保证的范围 | 维护与存储取舍 |
| --- | --- | --- |
| 持续更新精确版本 | 及时修复当前分支的兼容性与安全问题 | 仍需双架构验证；不保留已消失的旧版本，单独采用无法保证历史构建 |
| 完整 APK 集合＋基础镜像保留（采用） | 保存过的精确安装事务可验签、核对内容并断网重放 | 四种事务各两份清单；保存原始压缩 APK 和必要的基础层，复用共享 CGO 输入 |
| 另外保留已安装的工具链／运行依赖镜像 | 可直接复用安装结果，减少重复安装时间 | 仍需双架构构建、扫描、来源证明和保留策略；若还保留 APK，会增加安装结果层的存储与维护对象 |

已安装依赖镜像目前只用于采集验收与扫描，不另外长期发布。后续若安装时间成为
瓶颈，可把它们作为由现有归档重建的缓存；原始 APK 与基础镜像仍是重建输入。
不把普通构建缓存、短期 Actions artifact 或仅固定上游 digest 当作长期留存。

[输入规格](../../deploy/build-inputs/spec.json)按四种安装事务管理依赖：

| 输入角色 | 安装起点 | 使用者 |
| --- | --- | --- |
| `cgo-builder` | Go / Alpine | Core 与 Worker 共用 GCC、musl 开发工具链 |
| `supercronic-builder` | Go / Alpine | supercronic 构建阶段的 Git 依赖 |
| `core-runtime` | Nginx / Alpine | Core 运行时直接及传递依赖 |
| `worker-runtime` | Alpine | Worker 解析器、语言、字体及传递依赖 |

roots 固定具体包名和版本，不使用可由多个包满足的虚拟名称。Core 的 SSH 客户端
明确为 `openssh-client-default=10.2_p1-r0`，与迁移前实际选中的包及版本相同，
避免 `openssh-client` 在 default 与 krb5 provider 之间重新选择。

[Preserve Build Inputs](../../.github/workflows/build-inputs.yml)在 `build-inputs/**`
维护分支的相关文件 push 或手动执行时，分别使用原生 amd64 与 arm64 runner。
采集保留一次真实安装事务下载的全部原始 APK、包名/版本/架构/大小/SHA-256、
安装前后完整 inventory、原始可信密钥和已验证索引证据。基础镜像已提供且未改变的
包由该镜像承载；新增或升级的每个传递包必须有归档 APK。两个架构独立解析、验证、
断网重放和扫描，不能把 amd64 包清单复制成 arm64 输入。
零安装大小的元包可能只凭索引进入安装数据库而不产生下载缓存；采集器对照完整
安装差异，按精确版本补取缺失 APK，再统一验签，不能只复制缓存目录作为完整集合。
Alpine 索引可能将 `noarch` 改写成仓库架构；归档保留在线安装清单作为证据，
离线目标清单采用已验签 APK 自身的 `noarch` 声明，仍逐项精确比较包名、版本和
安装后架构，不能因此接受来自另一原生架构的包。

归档复用 `docker.io/linnea7171/xirang` 与已有镜像推送凭据。四种输入各保存两份
平台 carrier；Go、Node、Nginx、Alpine 的原始平台 manifest 和 blob 同时保留。
上游多架构 index 原始字节作为证据保存，保留仓库另建只含这两个平台的 index，
不宣称它与上游包含其他架构和证明的 index 具有相同 digest。
`inputs-*`、`base-*` 标签包含内容标识且不允许覆盖冲突；消费引用固定 digest。
这些标签只承载构建输入，不是可部署的产品版本，不触发正式镜像发布。

流程先在原始基础镜像中安装，并在禁网的新构建中重放，验证缺包、字节篡改、
无可信签名、错架构及额外包都会失败。APK 签名使用安装起点内的可信密钥；归档
附带密钥仅供审计，不能成为独立信任根。四种安装后的镜像均按现有 Trivy
`HIGH,CRITICAL`、`exit-code: 1`、`ignore-unfixed: true` 扫描，成功后才推送输入。
两平台全部成功才生成和 attest candidate lock；不能用存放压缩 APK 的 scratch
carrier 扫描替代安装后扫描。最终 Core/Worker 的漏洞与运行时检查仍独立执行。

candidate artifact 仅传递 lock、完整包 manifest 和 Worker 投影，Actions artifact
保留期不承担长期 APK 保存。维护者下载同一成功 run 的 `build-input-candidate`，
先核对来源 SHA 与 attestation，再验证和采用真实消费锁定：

```bash
gh attestation verify <目录>/lock.json \
  --repo xiangnan0811/xirang \
  --signer-workflow xiangnan0811/xirang/.github/workflows/build-inputs.yml \
  --source-digest <成功采集运行的完整提交SHA> \
  --deny-self-hosted-runners
python3 scripts/build-inputs.py verify --candidate --lock <目录>/lock.json
python3 scripts/build-inputs.py adopt --candidate <目录>
python3 scripts/build-inputs.py verify
```

包版本更新时同步 Dockerfile 的显式 roots；涉及 Worker 时同步生产 inventory。
新输入不会在采集后自动成为产品构建输入，lock 的变更仍需 PR 审查和完整 CI。
安装器或需求变更必须重新采集、验证和审查，不能手工伪造真实制品 digest。

安全更新以规格中的必要版本或基础镜像变更开始，在维护分支重新生成候选。
共享输入变更要验证两种使用者，Core 专属输入本身不进入 Worker 工具链投影。
当前采集一次重建四种事务；即使起因是 Core 修复，重新解析时若 Worker 或共享
CGO 的传递依赖也发生变化，仍须按实际内容差异处理 Worker 身份与制品更新。
禁止通过放宽版本、关闭签名、临时漏洞豁免或回退在线安装使候选通过。
保留旧输入只提供重建能力；旧版本仍须通过当时执行的漏洞门槛才能重新发布。

[Audit Preserved Build Inputs](../../.github/workflows/audit-build-inputs.yml)每周及手动
执行匿名冷读取、双原生架构断网重放和当前漏洞扫描。它不使用推送凭据、不更新
锁定、不删除输入；失败须检查归档可用性、数据完整性或新增漏洞，再通过维护 PR
处理。普通产品 CI 继续生成最终 Worker closure、验证签名 bundle、执行 Compose
smoke 和两架构镜像扫描；保留输入检查不能替代这些运行时证据。

存储按 registry 中实际引用 blob 的压缩大小、跨 manifest 去重后计量，不能用
容器内 installed size 估算账单。`python3 scripts/build-inputs.py report` 输出当前
lock 的引用与去重大小；`--lock <路径>/lock.json` 可指定待审查候选。不自动删除 `inputs-*`、
`base-*`；所有仍保留的 release、支持分支及回退窗口引用都须纳入保护集合。
定期盘点只报告，不删除；实际费用按仓库账单、流量与保留代数计算。
当前报告只统计给定 lock 可达的 blob，不代表整个仓库历史总量。多代留存成本为
全部受保护 lock 的 blob digest 并集；相同基础层或完全相同的 APK 层只计一次。
每个角色的 APK 单独组成一层，避免索引证据变化重复保存 APK；任一 APK 内容变化
会产生该角色该架构的新 APK 层，不能假设 registry 会在层内部按包去重。
因此每次更新须比较新增 blob 字节数，再结合保留窗口和实际下载流量评估增量费用。
每次完整采集包含八次在线安装、八次原生断网重放、十个拒绝边界，以及八个 carrier
各两次无缓存构建；每周审计包含八次断网安装。两种流程各扫描八个依赖镜像，
最终产品 CI 的构建、扫描与运行验证费用另计。

Worker 的 v3 工具链只嵌入共享 CGO 与 Worker runtime 的两架构内容摘要。
Core runtime、supercronic、存储位置、抓取时间和原始索引审计证据不进入该投影；
同一 APK 内容重新归档不应仅因位置变化重签 Worker。输入或生产二进制变化后，
仍须重新生成最终闭包并按[工具链身份合同](../spec/domains/backup-processing-export.md#工具链身份与构建输入)
提供新的签名制品。闭包是在安装完成后生成，不能代替原始 APK 与基础镜像留存。

该保证限定为保存过的 APK 事务和基础镜像可重新取得与安装，不承诺所有历史 tag
都可重建，也不宣称 npm、Go modules、supercronic 源码获取和最终产品已全流程离线。
没有采用归档 lock 的旧源码仍须保留原发布说明中的不可重建警告。

### 已合入的包版本修复

Core 的 curl 锁定从已不可安装的 `8.22.0-r0` 更新为 Alpine 3.23 的
`8.23.0-r0`，其包依赖要求 libcurl 同版本升级；保留 Nginx 基础镜像 digest。
同时显式锁定 `brotli-libs=1.2.1-r0` 修复 CVE-2026-111497：基础镜像中的
`1.2.0-r0` 仍满足 libcurl 的 SONAME 依赖，单独更新 curl 不会保证该库升级。
这是 Core 专属运行依赖更新，不改变 Worker inventory；须验证 Core readiness
和 Worker 组合启动，并按 CHANGELOG 的重建、替换及回退边界交付。

共享运行时加密库的补丁须同步 Core/Worker Dockerfile、生产工具链 inventory 和对应安装合同检查；指纹由 inventory 派生，不手工伪造摘要。按新源码重新生成两架构运行时闭包及证明，不能沿用旧包版本的已签名运行包。跨架构查询软件源时使用基础镜像内该架构的可信 Alpine 密钥，不能关闭签名验证，也不能以滞后的网页列表替代实际已验证索引。

共享 CGO 构建包同样属于工具链身份：Core/Worker 的 `musl-dev=1.2.6-r3`
替代已不可安装的 `1.2.6-r2` 时，须同步生产 inventory 并重新生成闭包及签名制品；
不能因为包仅在 builder 中安装就复用旧指纹。保留基础镜像 digest 和其他锁定，
并在实际运行镜像执行 CGO 二进制与 Core/Worker smoke，包安装成功不能替代 libc
兼容性验证。这不等于升级运行镜像的 libc，也不自动构成安全漏洞修复声明。

保留完整 Compose smoke、Worker 对应检查和漏洞扫描，不能用单一架构成功替代另一个架构。维护性依赖更新也应按发布手册检查合并后自动化，不手动提升版本或重发镜像。
