# 仓库自动化维护

新增外部依赖必须有明确需求并经过评审；优先复用项目已有能力，不以版本更新自动化替代必要性与兼容性判断。

## 依赖更新

[Dependabot 配置](../../.github/dependabot.yml)按月更新 Go、npm 与 GitHub Actions，按生态分组；npm 生产与开发依赖分别成组，常规更新限定 minor/patch。不要未经维护者决策改回每周、每依赖一条 PR。major 升级单独评估兼容性、上游发行说明和完整验证。

安全告警与安全修复不受常规版本更新排期约束。替换旧机器人 PR 前先记录精确 PR 编号与 head 分支，只关闭该名单；不得用动态“全部关闭”查询误伤新安全 PR。

离线账户恢复工具直接使用已锁定的 `golang.org/x/term v0.46.0` 读取控制终端，
因此在 `backend/go.mod` 声明为直接依赖；该账户恢复变更本身不升级此依赖、Go 工具链或其他模块。
验证须包含真实 PTY 的无回显与重定向输入拒绝，不能以模块下载或编译成功替代。

### Go 工具链升级

项目使用 Go 1.27.4；CI 从 `backend/go.mod` 读取版本。Core、Worker 和
supercronic 的构建镜像同步固定到 Go 1.27.4 / Alpine 3.24 的多架构摘要，
Worker 的 `BuilderBase` 运行环境指纹和镜像检查脚本须随之更新。
保留 SQLite 所需的 CGO，以及与运行镜像兼容的 musl；升级 Go 不自动升级 Alpine 主线。

Go 升级不要求所有模块跟随更新。先核对上游兼容说明、构建标签、CGO、
运行时内部接口和漏洞结果，仅升级不兼容或需要安全修复的依赖；不要用批量
`go get -u` 扩大变更。`go.mod` 的版本指令也会影响运行时兼容默认值，必须在
修改指令后重新验证，不能仅复用新编译器搭配旧指令的结果。

SSH 轮换预验证候选将 `golang.org/x/net` 从 v0.59.0 定向更新到 v0.60.0，
修复 HTTP/2 安全公告；未升级其他模块。验收继续使用固定版本 govulncheck
及完整本地 CI 门禁，不以依赖升级代替业务验证。
本地验证须显式使用 `GOTOOLCHAIN=go1.27.4`，避免宿主工具链与 CI 不同导致错误复用证据；
Go 1.27.4 修复 [GO-2026-6859](https://vuln.go.dev/ID/GO-2026-6859.json)。

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

针对 Trivy 镜像扫描识别的 `golang.org/x/net` CVE-2026-78661（`golang.org/x/net/html` 构造输入拒绝服务），将 `backend/go.mod` 中的 `golang.org/x/net` 从 `v0.60.0` 更新至 `v0.62.0`（及其最小版本选择要求的 `golang.org/x/crypto v0.58.0`、`golang.org/x/sys v0.49.0`、`golang.org/x/term v0.47.0`、`golang.org/x/text v0.43.0`、`golang.org/x/sync v0.24.0`、`golang.org/x/tools v0.51.0`），并同步 `rsyncconfinement` 与 `backupasset/processing/capabilities` 中 `unix.LandlockRulesetAttr` 的 `Handled_access_fs` 字段名及 ABI v1/v3 兼容结构体大小（`unsafe.Sizeof(attr.Handled_access_fs)`）。更新后须重新构建并替换 Core 与 Worker 镜像及二进制，回退旧镜像或旧模块锁定会恢复已知漏洞风险。

## 前端依赖审计

更改锁文件、升级依赖或排查本地与 CI/Docker 差异时，使用与 [CI](../../.github/workflows/ci.yml)及 [Docker web-builder](../../deploy/allinone/Dockerfile)一致的 Node 主版本，当前两者均为 Node 20。不要把 Actions 自身的 JavaScript runtime 与项目 Node 版本混为一谈。

审计必须包括 `devDependencies`：Vite、Vitest、ESLint 和构建工具也是攻击面。开发 shell 的 `NODE_ENV=production` 会让 npm 隐藏开发依赖，不能采用该结果作为通过证据。专项复现从干净安装开始：

```bash
env -u NODE_ENV npm --prefix web ci
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
```

CI frontend 和本地 parity 直接执行 `env -u NODE_ENV npm audit --audit-level=moderate`，包含开发依赖并保留 npm 的失败状态。历史 `check-npm-audit.sh` wrapper 仍只阻断未列入 allowlist 的 high/critical，不能作为完整 moderate 审计证据；本次不扩大修改该共享 wrapper。

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


### lint 依赖的本地可重复修补

针对 [GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249)，
五个固定父包通过根 `devDependencies` 的本地 tarball 声明使用官方 minimatch 11.0.1：
ESLint 9.39.4、config-array 0.21.2、eslintrc 3.3.7、jsx-a11y 6.10.2 和
typescript-estree 8.70.1。三个 scoped 父包的 override 引用同名根声明；保留真实
上游 name/version、许可证和其他包语义，不依赖安装后修改器或 matcher 兼容层。

可信输入及逐文件配方位于 [dependency-patches](../../web/dependency-patches/sources.json)
及同目录 `recipes.json`。Python 3.9+ 标准库生成器先核固定官方 SHA-512 SRI，拒绝
非规范路径、重复成员及链接，再执行精确替换。生成物包含五个 tarball、可读 patch、
完整文件哈希和工具链记录；同一 Python/zlib 工具链的独立构建必须逐字节相同。
可读 patch 的空白上下文行使用无空格前缀的空行表示，避免产物触发暂存区行尾空白检查；仍须能以零 fuzz 应用到已核上游，并逐文件还原相同包内容，不修改 tarball 或放宽提交门禁。
每个包保留原许可证并附 `XIRANG-MODIFICATIONS.txt`；eslintrc 仅修补独立 ESM 源，
原 CJS bundle 及准确描述该构建的 source map 保持原字节。

在 checkout 根运行生成器，`--work-dir` 必须为空且位于本 checkout 的 `.tmp/` 内，
`--out-dir` 必须位于同一 checkout，路径不能含 symlink。保留独占工作目录作为证据：

```bash
python3 scripts/build-lint-dependency-patches.py --work-dir "$PATCH_WORK/build" --out-dir web/dependency-patches
```

`PATCH_WORK` 指向预先独占创建的项目 scratch 目录；已有不同产物默认拒绝覆盖，
只有复核来源和配方后才使用 `--replace`。不运行 npm pack 或第三方生命周期代码。
父包或其固定 requester 版本改变时必须重新核来源、配方及真实消费者，不能自动沿用旧补丁。
config-array 的 matcher 类型采用 v11 公开接口；匹配语义也按 v11，不模拟完整 v3 行为。

`npm run check:dependency-patches` 只读核对来源、配方、产物、锁文件、已安装完整文件集、
全部 matcher 副本及固定 requester 的真实解析。普通 `check`、三个前端 CI 安装入口、
本地 parity 和 active Docker web-builder 都调用它；Docker 在 npm ci 前复制产物。
`node scripts/check-lint-dependency-patches.mjs --archives-only` 可在无 node_modules 时核验
必需清单、锁文件和归档，不是完整安装验收。完整门禁仍包含 Node20 干净安装、所有开发依赖
moderate 审计、原 lint 文件集合、ESM/CJS、类型声明、可访问性及 project service 消费者回归。
上游完整修复可用后，通过同样审计与消费者验收移除本地包和已无调用方的专用生成／校验逻辑。
本修复不改变运行时配置或数据库；回退旧依赖图会恢复工具链漏洞风险。

### jsdom 选择器兼容性

[package.json](../../web/package.json)中的 jsdom 专属 override 将 `nwsapi` 固定为已验证的 2.2.25。既有回归记录表明 jsdom 26 下 2.2.26/2.2.27 的 `:modal` 匹配会递归进入原生匹配适配器，导致下拉交互超时。调整 override 前，在 CI Node 版本下同时验证独立选择器匹配、节点和通知下拉确认测试，保留完整审计及前端门禁；不得通过增加超时或修改产品交互掩盖问题。

## Actions 与 CI

[CI](../../.github/workflows/ci.yml)使用 `pull_request` 检查 PR，push 仅监听 `main`，避免同一 PR 提交重复运行。required checks 的设置与交付步骤见贡献指南；发布链路与凭据归[发布手册](release.md)。

PR 打开、更新提交、重新打开及编辑均触发 CI 和独立的
[PR Metadata](../../.github/workflows/pr-metadata.yml) 工作流。`PR Title` 在每次运行时
通过 GitHub API 读取当前 PR 标题并复用标题校验脚本；API 失败或标题为空即失败。
旧工作流定义的历史运行重跑仍使用其原始行为，应核对当前标题对应的有效检查结果。

CI 的 `CI Event Classification` 记录事件、实际 checkout 和来源身份。只有 `edited`
事件的变化明确仅含标题或正文，且权威 PR 状态、候选源码和可用来源记录均匹配，
才归类为 metadata；未知变化、身份漂移或证据缺失／不可读取均保守运行完整源码检查。
打开、同步、重新打开及 `main` push 始终运行完整源码检查；分类作业失败、跳过或
输出不可用也运行完整源码检查，整个 workflow 被取消时不视为通过。

对打开的 PR，分类比较当前 head、base 和 merge 候选的提交及 tree；对已合并 PR，
比较固定 merge 提交与同仓库 `main` push 的源码记录，即使当前 main 已前进也不以
新 checkout 代替原合并来源。记录只证明验证输入，不证明测试成功。

三个受保护作业始终使用固定 required 名称 `Backend Test & Build`、
`Frontend Test & Build` 和 `Doc Freshness Check`；`PR Title` 仍是独立的 required
检查。metadata 运行会产生新的同名 check ID，原源码运行的 check ID 和结论保持不变，
作为不可变来源证据。每个 metadata 验证者必须确认固定来源的对应源码 job、对应 check
以及整个来源 workflow 均已完成且成功；单个 job 成功而 workflow 仍在运行时继续等待。
等待最多 350 分钟，每个受保护 job 的期限为 360 分钟；来源失败、取消、超时、跳过、
neutral 等非成功终态，或证据不可读取、身份不匹配、验证超时，均阻断通过。
验证前后重新核对候选与来源，来源 attempt 或候选变化不能沿用过期成功结果；合并前
还须核对当前来源及有效 required checks，不能只凭 workflow 总体成功或跳过结果判断。

三个受保护作业在 workflow 未取消时启动。只有分类成功且 mode 为 metadata 时，
才跳过全部源码步骤，并以稀疏 checkout 获取验证脚本，执行只读来源验证，不运行源码
测试；每次 metadata 运行最多占用三个轻量等待 runner。这些作业仅有 `contents`、
`actions`、`pull-requests`、`checks` 的 read 权限。分类完成后若来源证据失效，验证者
失败并阻断，不以只运行这三个局部源码作业冒充完整回退；下一事件仍由分类器决定是否
运行完整源码检查。其他情况执行原源码步骤，普通步骤保留前序失败后的默认跳过行为；
前端浏览器证据上传和覆盖率检查保留 `always()`，但同样受 metadata 排除条件约束。
其余源码作业仍在作业级排除 metadata。metadata 的实际步骤状态、来源及新 check 身份
和有效保护状态须在 GitHub 上逐项验证，不能仅凭配置推断通过。

使用新版 PR Overview 时，可从页首 `Merge status` 查看 required、等待及实际结论，
在 `Checks` 按提交、workflow 和运行查看步骤与日志；同时核对当前候选的有效合并状态。
metadata 验证成功不能代替合并后同 SHA `main` push 的完整 CI 或发布证明。

CI 将 push 与 PR 的并发组分开：同一 PR 的 opened／synchronize／reopened 源码运行
可替代较旧的同组源码运行，每次 edited 使用独立运行组，不取消其他源码运行或同 SHA
的 main push。PR Metadata 使用独立的 PR 并发组，只取消其自身较旧的标题运行。
合并后的正文仍可用于 Release Please override 和发行说明审阅，规则见[发布手册](release.md)。

### 可恢复 CI 取证

维护入口为 `python3 -B scripts/ci-evidence.py`，仅使用 Python 3.9+ 标准库和
POSIX 文件锁。先准备严格 JSON request，显式声明仓库、workflow blob、源码
run／attempt／checkout，以及 snapshot 所需的 PR、metadata、Title 和预期状态。
case 和实际 Go verifier 二进制必须位于当前 checkout 的 `.tmp/agent/` 内；
父目录须已存在，目标不得通过 symlink 越出工作区。七参数 profile verifier 在
`backend/` 使用 `go build -o "$BIN" ./cmd/verify-ci-profile` 构建。

```bash
python3 -B scripts/ci-evidence.py source --context "$REQUEST" --case "$CASE" --profile-verifier "$BIN"
python3 -B scripts/ci-evidence.py snapshot --context "$REQUEST" --case "$CASE"
python3 -B scripts/ci-evidence.py verify --case "$CASE" --profile-verifier "$BIN"
python3 -B scripts/ci-evidence.py status --case "$CASE"
```

`source` 只完成完整成功源码 CI 的证明；`snapshot` 完成 request 声明的单次
pending、success 或 failure 观察。新 case 必须不存在；`--resume` 必须指定
已有且 request、仓库与工作区身份完全一致的 case，不自动跟随新 attempt。
只有 source／snapshot 的 `--make-current` 才切换 `/.agent-state/` 中唯一指针；
未指定 case 的 status 读取该指针，不扫描目录或按时间选择结果。

case 按 capture、authenticate、verify、finalize 四阶段保存不可变 receipt。
resume 复验原件及依赖，复用完整身份一致的原 receipt 和观察时间；工具、输入、
依赖或策略变化只使相关链失效。完整认证 ZIP 可在后续失败后复用；部分下载不能采用。
采集、认证的缓存身份与下游验证身份分开；当前采用资格还核对工具的完整字节身份，
不能因采集 receipt 可复用就沿用旧资格。不可变发布要求不覆盖目标的原子 rename；
平台缺少该能力时返回 `E_INPUT`，不退化为覆盖发布。
各次调用保留独立命令退出记录，成功续接不改写旧失败。若中断发生在首份命令记录
发布前，恢复只追加未知结果的中断记录，不补造退出码、原命令或结束时间。源码
case 的离线 verify 仍须显式传入实际 Go verifier，不访问网络、不更新 summary 或 current。

stdout 始终为一个 JSON 对象，分别报告 case 的 `state`、`verification` 和
`live_eligibility`。complete 只表示该 context 的证明完成；failure snapshot 即使
验证通过仍为 blocked。默认 status 离线只读；无指针为 none 且不创建状态目录，
坏指针非零。`status --refresh` 只执行认证 GET，网络不可达为 unknown，不修改
case 或指针；历史成功不能代替当前候选、attempt 和保护状态的新核验。

当前采用资格还要求取证工具、生产 classifier、workflow、Go module 与 verifier 的
实际构建／嵌入输入保持洁净；相关 staged、unstaged、untracked 或 ignored 输入均
阻断。Go `_test.go` 叶文件沿用测试排除规则，相关嵌入目录即使同名也不能排除。
输入范围之外的 ignored 恢复 checkout 不影响资格，应保留其历史材料。
status 正常返回 `0` 仍可能报告 stale／blocked；采用前须实际确认
`live_eligibility=eligible_at_observation`，不能只看退出码或旧的 verification=passed。

退出码依次为：0 正常完成或 status-none，2 `E_INPUT`，3 `E_TRANSPORT`，
4 `E_INTEGRITY`，5 `E_VERIFY`，6 `E_STALE`，7 `E_BUSY`，8 `E_INCOMPLETE`，
9 `E_REMOTE_FAILURE`。stderr 仅保留错误码与安全摘要；原始 API 响应和认证下载
保存在私有 case，不能把 token、signed URL 或未过滤的响应复制进交付日志。

性能比较使用相同 checkout、Go、环境、runner 类别及缓存条件的配对样本，明确
记录实际 cache hit 和完整 job 路径；最多三对本地样本，不能用不同 tree 的历史
结果补样本。`ci-phase` 只表示 Go 命令和阶段耗时，不独立声称编译或迁移 body
耗时。候选必须先通过行为及库存等价验证，再按既定阈值决定是否采用；计时本身
不能证明优化收益。远端 baseline 与 candidate 顺序固定时应如实标明非随机实验。


前端 CI 和 `scripts/local-ci-parity.sh` 均通过 `npm run check` 继承中英文语言资源
key 对齐检查；正常 pre-push 调用本地 parity，不另设检查入口。该检查在依赖补丁校验后、
typecheck 前执行，也可在 `web/` 单独运行 `npm run check:i18n`。它只比较 `zh.ts` 与
`en.ts` 的叶子 key 集合：单侧缺失时报告缺失语言和排序后的完整路径并非零退出；语言文件
缺失或读取、语法、求值错误同样非零退出。它不校验翻译值、共同缺失的 key、代码引用、
动态 key、复数、间接引用或未使用的 key。

PostgreSQL job 只通过 `bash ../scripts/run-required-postgres-tests.sh --evidence-dir ...`
执行 `scripts/required-postgres-tests.json` 的统一库存，工作目录为backend；不再维护多个
宽regex、位置参数suite或直接Go测试旁路。独立backup/restore步骤保留。库存/原生声明/
AST候选校验、默认及覆盖timeout、14个required环境开关和逐测试事件失败规则见
[数据库合同](../spec/backend/database-guidelines.md#双引擎回归证据)。CI还运行隔离fake-process
协议自测，并在成功或失败时上传逐组JSON事件、脱敏stderr及summary；fake协议通过不代表真实PG通过。
证据文件写入、输出句柄关闭或读取句柄关闭失败也使 runner 失败；测试进程成功不能覆盖证据落盘或读取收尾错误。

逐组 JSONL 的 `Output` 最多保留首处可验证的源码位置，格式为
`basename.go:line: [redacted]\n`。位置必须来自本组当前 checkout 的普通 Go 源文件，
行号须在该文件范围内；symlink 文件、路径、未知文件、异常行号或无法建立源码允许表时
仍只写 `[redacted]`。正文、动态值及其余行不落盘，已知秘密在发布前继续替换。
该位置只表示测试输出引用的代码位置，不证明错误类别；没有合法位置时仍可能无法定位断言。
失败测试继续写失败 summary 并使命令非零退出，不因保留位置改变通过标准。

库存保留批量重试的事务竞争、授权、执行证据和迁移约束，配置导入/导出的BothEngines、
混合依赖环及HTTP静态场景，以及SSH替换/轮换的真实节点库存、锁后材料重分类、撤销、
proof到期、并发导入与结果未知清理。每个必需PG子路径均须实际run/pass，不能以SQLite、
父测试通过或空选择器掩盖缺失分支；新增PG声明与场景必须同步库存并通过独立census。

升级策略只读预演的 `TestEscalationPreviewPostgres` 仍在库存中，使用随机隔离schema
验证真实路由权限、`REPEATABLE READ READ ONLY`、并发快照、零写入及取消清理。
Chromium真实后端单独选择 `escalation-preview.spec.ts`；普通mock配置排除它，
Firefox/WebKit下载项目不扩大。业务边界见
[升级预演合同](../spec/domains/alerting-health.md#升级策略只读预演)。

后端 lint 固定 `golangci-lint v2.14.0`（支持 Go 1.27），CI 固定的 action 使用
`install-mode: goinstall`，以当前 Go 工具链从源码安装该版本；本地
`scripts/lint-backend.sh` 通过带版本的 `go run` 以当前选中的 Go 工具链构建并运行，
两者同步更新。本地方式隔离工具依赖，不改项目模块或全局安装，也避免系统 Go 升级后继续
加载由旧 Go 构建的 linter。CI 继续执行配置 schema 校验、后端全范围 lint 和失败门禁，
保留 action 的缓存与注解处理；源码安装不依赖 GitHub Release 二进制资产，但仍依赖 Go
模块获取渠道，不能保证 Go Proxy 始终可用。升级 linter 后执行配置校验、全仓库 lint 和本地完整门禁。

浏览器 CI 将 mock 三浏览器矩阵与真实后端场景分开：默认配置排除
`real-backend-smoke.spec.ts` 和 `lifecycle-p1.spec.ts`；真实配置的 `chromium`
项目依赖 `chromium-smoke`，在同一隔离后端上串行执行。Ubuntu runner 安装
Playwright 系统依赖、OpenSSH server 与 sqlite3，由脚本生成独占 SSH/数据库
fixture 并清理；不接受共享手工 fixture 作为自动化通过证据。命令、严格主机密钥
校验与清理边界见[测试指南](../spec/guides/testing.md)。

默认 mock 和真实后端均经安全事件 runner 执行，真实后端不再以 Chromium-only 选择
遗漏已声明的 Firefox/WebKit 项目。完整源码 CI 中账户恢复与完整 walkthrough 各有独立 job；
账户恢复保持安全 reporter 和零重试，walkthrough 独占4177，失败也汇编并上传证据。
原464场景不缩减，Docker/history新增状态由独立显式配置执行。正常 typecheck/check
包含严格 ES2023/DOM/Node 的全部 e2e 与 Playwright 配置，不把测试加入生产 bundle。
这些 job 均属于发布所要求的整轮 main CI；skip/list/重试后的成功不能替代首次实际通过。

cron job 使用本地同一隔离入口，PG18与工具容器每次独占，不复用 parity DSN。
Core双架构原生job保留静态及mutation检查，并运行locked Nginx live probe；资源清理失败
也使job失败。各入口与证据边界见[测试约定](../spec/guides/testing.md)。

Worker 完整 Compose profile smoke 的私有签名证据导出、七份文件和校验合同见
[测试约定](../spec/guides/testing.md#worker-profile-签名证据)。CI 只在 amd64 完整
profile smoke 成功后上传；arm64 原生闭包及扫描仍须分别验收。每个完整成功的 PR 源码、
合并后 main、Release PR 源码和发行 main 运行都须各用本次运行的原件验证，不能借用其他 run。

Core Compose 自测的 APK 绕过检查须完整消费经注释过滤的 Dockerfile：`pipefail` 下
`grep -q` 在命中后提前退出可能令上游 `sed` 收到 SIGPIPE，使已命中的检查误判为未命中。
使用输出丢弃的非 quiet `grep -E` 保留匹配规则；大文件 exec-form APK mutation 以
逐行输出的 `sed` 覆盖该场景。此项仅改变测试，无 API、配置、schema 或运行时变化，
不因此单独发行新版本。

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
CI race 命令显式设置每包 `-timeout=20m`，覆盖 Recovery 完整集成套件的累计执行时间；保留完整包列表、竞态检测和失败退出。该值仅是测试进程预算，不改变产品任务、租约或读取期限；达到上限仍须根据逐测试进度和堆栈调查，不自动重试或跳过测试。
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
明确选择 `openssh-client-default`，避免 `openssh-client` 在 default 与 krb5 provider
之间重新选择；当前精确版本以输入规格和 lock 为准。

已采用的输入来自提交 `f2eb82e7aff90b3b00da64655e09f2d5ef755bd4` 的
[原生采集 Q2](https://github.com/xiangnan0811/xirang/actions/runs/42972630089)，
同一次成功运行的双架构 lock、八份 manifest 和 Worker 投影经来源绑定的签名证明及内容核验后采用。
Go 来源为 `golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf`；
Core 保留 Nginx 基础镜像，并以 `libxslt=1.1.43-r4` 直接 root 将基础镜像内 r3 升级为 r4，
Worker 不新增该 root，双架构实际事务以传递依赖将 r3 升为 r4，修复
[CVE-2026-87636](https://security.alpinelinux.org/vuln/CVE-2026-87636) 和
[CVE-2026-9947](https://security.alpinelinux.org/vuln/CVE-2026-9947)。
先前采集 Q42969169392 因旧 `git=2.54.0-r0` 不可安装而失败；Q2 双架构实际采用
`git=2.54.1-r0` 及同版本 `git-init-template`，Git 仅用于 supercronic 构建阶段，
不加入 Worker inventory。[Git 上游安全公告](https://github.blog/open-source/git/git-security-vulnerabilities-announced-7/)
列出 2.54.1 的安全修复。

八份 manifest 的实际安装差异还包括新 Go 基础镜像带来的双架构共享 CGO 和
supercronic builder 变化：Alpine release 3.24.2 → 3.24.4、apk-tools／libapk
3.0.8 → 3.0.9、CA 证书及 bundle 20260909 → 20261020、libcrypto／libssl
3.5.8 → 3.5.9；Alpine 主线不变。两架构 Core 和 Worker runtime 的安装差异均为
libxslt r3 → r4。必须审阅完整八事务及其生成投影，不能只核对直接 root 或手工拼写摘要。
输入采集成功不代表最终产品镜像、正式发行或生产部署已完成。

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

### Worker Python 传递依赖安全更新

Worker 显式锁定 `python3=3.12.16-r0`，替换受 CVE-2026-115557 影响的
`3.12.15-r0`。按原生双架构采集流程重新生成完整输入事务；分别核对 `pyc`、
`python3`、`python3-pyc`、`python3-pycache-pyc0` 的已验签 APK 与安装清单，
并通过输入镜像及最终 Worker 镜像扫描，不能仅凭一个 root 的版本证明全部子包修复。
输入规格、Worker Dockerfile 与生产 inventory 的显式 pin 保持一致；新 lock、
八份 manifest 和 Worker 投影须来自同一成功采集 run，并验证其 source SHA 与 attestation。
重新解析引起的其他传递包变化也须纳入审查，不能手工拼接旧、新 manifest。

Worker 输入内容变化后，Core 与启用的 Worker 均须同源重建，并更新双架构最终闭包、
签名 bundle 和 proof。CI 临时密钥签出的 fixture 只用于验收验证路径，不可当作生产运行包；
实际切换仍须使用授权的签名与信任材料，先排空旧 Worker 在途处理，回退时恢复整套匹配制品。
本补丁不新增配置或数据库迁移，回退旧 Python 仍会恢复该漏洞风险。

### Alpine 构建与运行输入 musl、OpenSSH、zlib 安全更新

针对 Trivy 扫描识别的 Alpine 基础镜像与运行依赖漏洞（`musl`／`musl-utils` CVE-2026-107891、`openssh-client-default`／`openssh-client-common`／`openssh-keygen` CVE-2026-119709、`zlib` CVE-2026-22184／CVE-2026-27171／CVE-2026-85091），在保持四类基础镜像固定 digest 不变的前提下，将受影响的基础镜像预装包提升为显式 root pin 并通过原生双架构采集更新构建输入锁：Alpine 3.24 构建阶段（`cgo-builder`、`supercronic-builder`）显式锁定 `musl=1.2.6-r3`、`musl-utils=1.2.6-r3`、`zlib=1.4-r0`；Alpine 3.23 运行阶段（`core-runtime`、`worker-runtime`）显式锁定 `musl=1.2.5-r24`、`musl-utils=1.2.5-r24`、`zlib=1.4-r0`，Core 显式更新 `openssh-client-default=10.2_p1-r1` 与软件源已移除旧版本的 `tzdata=2026f-r0`，Worker 显式更新软件源已移除旧版本的 `font-noto=2026.11.01-r0`（同步 `fonts` 组件 revision 为 `noto-2026.11.01+noto-cjk-20220127`）。生产工具链 inventory 按 `Runtime` 角色区分 `cgo-builder`（Alpine 3.24）与 `worker-runtime`（Alpine 3.23）中的同名显式包（`musl`、`musl-utils`、`zlib`），拒绝同一角色内的重复声明。更新推进了 Worker 工具链与 pipeline 指纹，须验证 candidate lock 的 workflow provenance attestation，排空旧在途处理后同源重建 Core／Worker 并重新生成双架构最终闭包、签名 bundle 与 proof；无新增配置或数据库迁移，回退旧镜像及配套制品会恢复上述已知漏洞风险。

### Core Rsync 安全更新与 Worker 传递输入同步

Core `rsync` 显式 root 从 `3.5.0-r0` 更新为 Alpine 3.23 的 `3.5.2-r1`，修复 Trivy 报告的 `CVE-2026-119741`；扫描修复门槛 `3.5.2-r0` 与当前两架构可采集的后续修订 `r1` 分别记录。采用同一成功原生采集 run 的 lock、八份 manifest 与 Worker 投影，并核对 exact source SHA／指定 workflow 的 provenance，不手填 digest。

该次全事务重新解析使 Worker 两架构的传递 `glib` 从 `2.86.3-r0` 更新为 `2.86.5-r0`，其他包、基础镜像、安装器及可信密钥保持。显式 root 列表未变，glib 的完整 APK 字节和安装版本由生成的 `production_apk_inputs.json` 对应 manifest 身份覆盖；不为了复制传递版本而新增 root 或手工指纹。真实 Worker 输入变化会推进 toolchain／pipeline 指纹，须同源重建 Core／Worker、重新生成双架构最终闭包及签名配套制品，不能把 Core 起因概括成 Worker 不变。升级和回退边界见发布手册。
