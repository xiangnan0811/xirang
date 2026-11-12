# 测试约定

## 按风险建立证据

回归应验证对外行为、数据保护和失败边界，避免只重复实现内部步骤。优先覆盖实际修改的职责层及相关合同；命令、hooks 和完整门禁统一见[贡献指南](../../../CONTRIBUTING.md)，领域场景见[领域索引](../domains/README.md)。

- 纯函数或映射用聚焦单元测试；空值、非法输入、时间边界和未知枚举须有明确结果。
- 持久化变更同时验证 SQLite 与 PostgreSQL；缺少数据库服务造成的 skip 不是双引擎通过。
- 并发、租约、取消、事务补偿和重启恢复应有可重复的竞争或失败注入测试；必要时运行竞态检测。
- API 和 UI 联动同时覆盖请求、错误语义、授权及用户可见结果；浏览器模拟不能代替真实后端验收。

时间相关用例使用受控时钟或相对稳定的测试时间，不依赖逐渐过期的固定日期。异步断言等待业务条件，不用盲目延长超时隐藏回归。测试替身只替代必要的外部边界，跨层投影使用实际生产者生成的值。

## 真实浏览器与 SSH 生命周期

`web/` 下的 `npm run e2e` 使用模拟 API，不选择 `real-backend-smoke.spec.ts`、
`lifecycle-p1.spec.ts`、`config-name-mapping.spec.ts`、`ssh-key-rotation.spec.ts`
或 `account-recovery.spec.ts`。真实验收使用：

```bash
env -u NODE_ENV npm run e2e:evidence -- --config=playwright.real-backend.config.ts
```

该入口先完成密码登录 smoke，再执行生命周期及配置名称关联场景，自动启动隔离
后端和 loopback OpenSSH。名称关联使用独立账户、真实 TOTP/proof/grant、Maintenance
实际下载文件原样上传及 API/数据库读回；同实例重建 ID 不替代双引擎跨库回归。
轮换场景使用隔离 SSH 候选和 SQLite 实际持久化证据，证明任一节点失败时不替换、
修复并重新检查全部库存后才保存，以及普通编辑仍支持离线保存。轮换后端竞争回归
另以 `TestSSHKeyRotationPostgres` 在独占 PostgreSQL URL DSN 上运行 race，包含
验证期间库存 phantom、最终事务阻止节点插入及锁忙；未提供 DSN 的 skip 不算通过。
本机需有 Go、Chromium、`sshd`、`ssh`、`ssh-keygen`、`sqlite3` 和
`python3`；CI 安装同样依赖，不以缺 SSH 配置跳过终端场景。生成的密钥、私有
known_hosts、SQLite 与 cron 产物均位于本次唯一 `.tmp/agent/lifecycle-p1-e2e/runtime.*`
目录，严格主机校验保持启用；退出时只清理该次夹具，不触碰真实 SSH 配置或数据库。
无需手工设置 SSH/数据库路径，已有占用目录必须拒绝而不是清空复用。

可以用 `E2E_BACKEND_PORT`、`E2E_VITE_PORT` 调整本地端口；更改端口不允许复用其他
运行实例。真实 SSH 的父级关闭场景必须先重新准入并证明 shell 仍连接，再关闭弹窗，
不能用已退出或已断开的 socket 代替活动连接清理证据。

该持续入口执行配置中全部已声明项目及依赖：chromium-smoke、Chromium 生命周期及
Firefox/WebKit 下载项目。默认 mock 三浏览器同样通过 `npm run e2e:evidence --
--config=playwright.config.ts` 执行。原生 list 与执行事件绑定相同候选、配置和选择，
分别记录 collected、selected、not-selected、首次 pass/fail/timeout/skip/interrupted/missing、
retry 和最终结果。缺事件、setup 失败、首次失败后重试成功均不能作为通过。
证据模式仅使用安全 reporter，记录稳定 test/project 身份、retry、status/expectedStatus
和完成信息；不输出原始错误或 annotations。每次独占 `.tmp/agent/e2e-*`，失败也保留 summary。
list 是库存，不能代替执行。

### 离线账户恢复

账户恢复使用独立入口，不复用运行中的后端：

```bash
env -u NODE_ENV -u DEBUG -u PWDEBUG npm run e2e:evidence -- --config=playwright.account-recovery.config.ts --project=chromium
```

夹具编译 server 与 recover-account，以随机 loopback 端口和独占
`.tmp/agent/r19-browser-*` 目录承载合成账户、密钥及 SQLite；每次重置前停服并回收
进程，通过真实控制终端无回显输入后重启同库。需 Go、Node、Chromium 和 Python 3，
不连接真实节点或使用生产数据。退出只清理其自有进程及目录。

覆盖三种重置范围、旧凭据及授权失效、身份与资源关联保留、受限响应不安装会话、
真实 TOTP 绑定与正常重新登录，以及错误 purpose/过期/替换、未知提交不重放、
复制失败、刷新和本地到期。中英文桌面与移动尺寸均验收；日志只保留安全阶段、
状态码和允许的源码位置，截图在捕获时遮盖秘密，禁用 trace、录像及自动 DOM 快照。
Playwright 输出另用独占 `.tmp/agent/r19-playwright-*/results`，worker 只采用所属
runner 的目录，不使用共享 `web/test-results`。成功退出清理自有输出；失败保留
失败用例的脱敏截图及安全错误摘要并打印安全路径；仅有 `.last-run.json` 时清理
运行目录。强制杀死进程可能留下本次目录，须确认无活动使用者后再按所有权清理。
事务、迁移及 race 回归另用独占 `TEST_POSTGRES_DSN` 验证真实 PostgreSQL，
该浏览器 SQLite 场景不替代双引擎证据。

CI 账户恢复使用独立无条件 job、Node20/Go/Python3/Chromium 和零重试。RecoveryReporter
仅增加结构化计数、允许的阶段、HTTP 状态及脱敏源码位置；不使用通用 JSON reporter、
原始错误/annotations、请求、DOM、trace 或附件。安全执行汇总位于 fixture 清理范围外，
成功或失败均保留；已有捕获时遮盖秘密的失败截图不混入通用执行事件。

## 隔离 cron 与真实网关

`bash scripts/cron-backup-isolated.integration.test.sh` 是本地和 CI 共用入口，不接受业务
DSN。每次使用随机独占 Docker network、PG18 容器及保留的 Go1.27.4 Alpine 工具镜像；
PG 数据为专用 tmpfs，不发布数据库宿主端口、不挂持久业务卷或 Docker socket。
客户端与服务端主版本核对后运行原完整 SQLite/PG cron roundtrip，恢复已知内容并核对
私有状态、回执与失败后的成功回执保留；源码只读，构建 scratch 私有可执行。
成功、失败和中断均只清理本次资源，清理失败必须非零。直接调用 runner 不证明
supercronic 时钟调度、All-in-One 非 root 启动或告警投递。

原生 amd64/arm64 Docker CI 在静态 checker 和 mutation 自测外实际执行
`scripts/check-asset-content-nginx-runtime.sh`，使用 retained lock。合成 upstream 只证明
gateway 行为，不证明 Broker 授权或生产部署；清理失败不能输出成功结论。

### Worker profile 签名证据

`ASSET_WORKER_PROFILE_EVIDENCE_DIR` 是 `scripts/test-asset-worker.sh` 的私有测试入口，
不是产品配置。未设置或为空时保持本地原有不导出行为；设置时必须同时启用
`ASSET_WORKER_PROFILE_SMOKE=1`，不得选择 static-only 或仅验证 closure 模式；使用不以 `/`
结尾的绝对路径，父目录须已存在，目标 leaf 不得存在（包括悬空 symlink）。脚本以
`umask 077` 创建本次独占目录，不清空或覆盖已有内容；
还须提供合法的 GitHub repository、正整数 run ID／attempt 和 40 位十六进制 SHA，
且 checkout HEAD 等于该 SHA。以上检查在首次 Docker 调用前完成；缺失、身份不符或
输出失败均使 smoke 失败。环境变量本身不是身份认证，下载时仍须独立核对实际 workflow
和 artifact 生产者。导出目录独立于 smoke 清理目录；失败时可能留下不含 receipt 的部分目录，
不得把它当作成功证据。

沿用现有 fixture 的签名、验签、激活及重启重验流程，在清理前仅导出以下七份文件：
`manifest.json`、`bundle.tar`、`trust.json`、`runtime-closure-amd64.v1.json`、
`runtime-closure-arm64.v1.json`、`attestations.v1.json` 和 `receipt.json`。
`trust.json` 仅含临时测试公钥；attestations 取自已验证 bundle 中唯一对应文件，
不导出私钥、token、数据库或日志。receipt 仅在全部重启后指纹、能力及隔离断言通过后最后写入：
schema 1、kind `worker-profile-smoke`、repository、run ID／attempt、checkout SHA、
UTC RFC3339 观察时间、两架构 closure SHA-256，以及 bundle／pipeline 指纹和对应的
重启后实际观察指纹；两组指纹必须分别一致。receipt 是私有测试观察，不是产品签名 proof，
临时信任密钥和 CI 签名 fixture 也不是生产信任或授权签名制品。

CI 仅在 amd64 完整 Compose smoke 成功后上传 `asset-worker-profile-signing-evidence-amd64`；
失败不上传半成品，arm64 保留独立原生闭包及扫描，不把它说成完整 Compose smoke。
每个完整成功的 PR 源码运行、合并后 main 运行、Release PR 源码运行及发行 main 运行，
都须在 artifact 过期前分别取回本次唯一原件，核对 producer、run／attempt 和 source record
中的 checkout SHA，确认恰有上述七份文件。两份 closure 摘要须匹配本次双架构原生 artifact
及 attestations，独立 attestations 字节须与 bundle 一致；用现有 updater 按 receipt 的
观察时间验证 manifest、bundle 和临时公钥，重算 bundle 与 pipeline 指纹并核对重启后观察值。
缺失原件、receipt 或任一身份／摘要不一致均不算通过；测试签名证据不替代完整源码 CI、
最终镜像扫描、正式发布证明或生产部署验收。

### 取证恢复与故障边界

CI 取证命令及退出协议见[仓库自动化](../../maintainers/automation.md#可恢复-ci-取证)。
持续回归通过 `python3 -B scripts/ci-evidence.test.py --profile-verifier "$BIN"`
运行，使用隔离夹具和真实 Go 验签；CI 不依赖历史忽略目录或个人 harness。
历史 ZIP 派生回放另作只读验收，不能用裁决 JSON 替代新命令的实际退出状态。

恢复回归须在下载中、完整 ZIP 发布后、verifier 后、最终权威 GET 后及 current
替换前后中断真实 CLI 进程，验证续接只采用已认证完整原件，保留各次命令结果，
并复验 receipt 的输入、前置依赖、策略和实际 verifier 身份。两个 case 或路径别名
争用同一 checkout 锁时第二 writer 必须失败；父进程退出释放锁，helper 不继承锁。
kill／resume 只证明进程中断边界，不等同断电耐久性证明；fsync 或 rename 失败
仍须非零并保留可恢复原件。离线 verify 和默认 status 的只读性须由文件状态验证。

pending snapshot 仅证明观察窗口；终态须绑定其 predecessor 的相同 job／check ID，
并另取最终权威原件。旧 fixture 缺少必需 final-run 时只能作为不完整负例，不能
因历史特例成为 canonical complete。网络不可达、retention 过期、当前候选漂移
及 required／App 不匹配分别验证，不把离线历史真实性写成当前采用许可。


## 候选和环境

记录检查针对的提交及工作区内容、命令、工具版本、必要服务、结果和跳过原因。相同 HEAD 不代表暂存、未暂存、未跟踪文件或外部依赖未变。修复后重跑失败项和受影响检查；只有候选与条件未变化时才复用已有证据。

临时 Git 仓库测试须控制宿主 CI 的基线环境变量，并在夹具内建立所需引用。文档门禁自测同时覆盖本地回退与 PR 远端基线，不能依赖调用者恰好没有设置 `GITHUB_BASE_REF`。

本地测试、独立审查、PR CI、合并后自动化、原生代理加载和生产验收分别报告。required checks 未通过、缺失或尚未完成时不得宣布交付门禁通过。具体审查及原生加载要求见[代理协作](agent-collaboration.md)，专项负载和恢复验证见[维护者验证](../../maintainers/verification.md)。
