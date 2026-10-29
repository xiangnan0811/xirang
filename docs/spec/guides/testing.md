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
DSN。每次使用随机独占 Docker network、PG18 容器及保留的 Go1.27.3 Alpine 工具镜像；
PG 数据为专用 tmpfs，不发布数据库宿主端口、不挂持久业务卷或 Docker socket。
客户端与服务端主版本核对后运行原完整 SQLite/PG cron roundtrip，恢复已知内容并核对
私有状态、回执与失败后的成功回执保留；源码只读，构建 scratch 私有可执行。
成功、失败和中断均只清理本次资源，清理失败必须非零。直接调用 runner 不证明
supercronic 时钟调度、All-in-One 非 root 启动或告警投递。

原生 amd64/arm64 Docker CI 在静态 checker 和 mutation 自测外实际执行
`scripts/check-asset-content-nginx-runtime.sh`，使用 retained lock。合成 upstream 只证明
gateway 行为，不证明 Broker 授权或生产部署；清理失败不能输出成功结论。

## 候选和环境

记录检查针对的提交及工作区内容、命令、工具版本、必要服务、结果和跳过原因。相同 HEAD 不代表暂存、未暂存、未跟踪文件或外部依赖未变。修复后重跑失败项和受影响检查；只有候选与条件未变化时才复用已有证据。

临时 Git 仓库测试须控制宿主 CI 的基线环境变量，并在夹具内建立所需引用。文档门禁自测同时覆盖本地回退与 PR 远端基线，不能依赖调用者恰好没有设置 `GITHUB_BASE_REF`。

本地测试、独立审查、PR CI、合并后自动化、原生代理加载和生产验收分别报告。required checks 未通过、缺失或尚未完成时不得宣布交付门禁通过。具体审查及原生加载要求见[代理协作](agent-collaboration.md)，专项负载和恢复验证见[维护者验证](../../maintainers/verification.md)。
