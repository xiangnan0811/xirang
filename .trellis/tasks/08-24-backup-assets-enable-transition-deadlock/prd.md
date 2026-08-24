# 备份资产启用事务死锁修复

## Goal

修复备份资产 Foundation 设置事务在启用、禁用或重配置运行时过程中重入自身设置锁，
导致请求永久挂起的问题。修复后，设置 PUT、DELETE-restore 与配置导入共享一条
可取消、有界、全有或全无的运行时转换路径；任何失败都不得留下已持有的锁、后台
goroutine、错误的 `backup_assets.enabled` 值或虚假的启用成功时间。

该任务是 v0.50.3 生产启用验收失败后的独立 P0。当前生产已通过受控重启恢复为
健康 v0.50.3，功能设置仍不存在（等效关闭）；修复镜像发布前不得再次点击启用。

## Background

- 生产升级到 v0.50.3 后，容器健康、schema `72|0`、迁移兼容和新 TaskRun authority
  验收均通过。
- 现有安装完成清单并确认后，单次“启用备份资产”请求挂起约 692 秒，Nginx 最终记录
  `PUT /api/v1/settings` 为 499。
- 请求后 `backup_assets.enabled` 仍不存在，`enablement_succeeded_at` 为空，资产记录为
  0；容器没有 critical 日志，但该请求占有的锁只有进程重启才释放。
- 受控重启后容器恢复 healthy、restart count 0、feature setting absent、active runs 0。
- 代码链证明 `WithBackupAssetMutation` 持有 `backupAssetMutationMu` 后，真实 Content
  `PrepareEnable` 调用 `FoundationService.ContentConfig()`，后者经
  `BackupAssetSettingsSnapshot()` 再次获取同一个非重入 mutex。
- 即使只修 Content，启用后的 Search `StartupPass` 仍会通过注入的 Config closure 调用
  `FoundationService.SearchConfig()`，形成第二次相同锁重入。
- 现有 handler 测试注入 `gaSilentContentManager`，因此只能证明 API 编排，不能证明真实
  production graph 不死锁。

详细链路、入口矩阵和测试缺口见 `research/root-cause-and-test-surface.md`。

## Locked decisions

1. 保留 Foundation 设置 mutation 的单写者和 snapshot 全有或全无可见性；不得通过删除
   外层锁、让 snapshot 绕锁或把运行时工作移出协调区来规避死锁。
2. 同一次 mutation 只构建一个完整、校验后的 prospective settings snapshot；Content、
   Search、Overlay、Export、Recovery 等转换只能消费从该 snapshot 解析出的配置。
3. mutation 内部禁止再次调用 Foundation 的当前值 getter 或 atomic snapshot getter。
4. settings mutation 等待锁必须响应 request context；运行时转换必须有小于 HTTP 写超时
   的明确上限，并在返回前完成 cancel、close、drain 与 join。
5. 启用、禁用、PUT、DELETE-restore、config import 和失败恢复共享相同合同，不为单一
   生产案例增加旁路。
6. `enablement_succeeded_at` 只代表整条启用转换成功；任一后续阶段失败时必须恢复转换前
   的 setting、stamp 与 runtime readiness，不允许假成功。
7. 生产保持 feature disabled；发布前不自动点击、不直接改生产 DB、不把重启当修复。

## Requirements

### R1 — 一次性 prospective 配置权威

- mutation owner 读取一个完整 `current` snapshot，叠加本次 overlay 得到 `effective`，
  完整校验一次后形成不可变 prospective transition bundle。
- 至少为 Content、Search/Overlay、Export 和 Recovery 提供“从完整 values 解析”的纯函数；
  公共 current-value getter 读取 atomic snapshot 后复用相同解析器，避免两套验证语义。
- Runtime settings transition 接收 prospective bundle 或等价的显式 typed configs；不得在
  mutation callback 内调用 `ContentConfig()`、`SearchConfig()`、`SearchOverlayConfig()`、
  `BackupAssetSettingsSnapshot()` 或其他会重获同一协调锁的 getter。
- disable 失败恢复使用从 `current` 解析的 prior config，不能在锁内重新读取当前设置。

### R2 — 独占与原子可见性

- 保持所有 Foundation settings mutation 串行化。
- 外部 `BackupAssetSettingsSnapshot` reader 在 mutation 完成前继续阻塞，完成后只看到全部
  旧值或全部新值；现有原子 snapshot 合同测试必须继续通过。
- 推荐把私有 `sync.Mutex` 协调点收敛为容量 1 的 context-aware gate：mutation acquisition
  可被 context 取消，普通 snapshot reader 保持阻塞式独占读取。
- 取消等待中的第二个 mutation 不得影响 owner，不得持久化任何值；owner 释放后 gate
  必须仍可复用。

### R3 — 有界且可证明结束的运行时转换

- operation context 使用 caller deadline 与内部 feature-transition ceiling 中更早者；内部
  ceiling 必须严格小于 30 秒 HTTP 写超时并集中定义。
- deadline/cancel 必须传播到 Content reconcile/cache、Admission drain/persist、Search key
  准备/StartupPass、Export 和 Recovery transition。
- 返回 timeout/cancel 前，所有由本次转换创建或控制的工作必须 close/drain/join；禁止用
  `time.After` 返回后留下 goroutine 继续写 DB 或改变 readiness。
- 同一错误可被 `errors.Is` 识别为 `context.Canceled` 或 `context.DeadlineExceeded`；HTTP
  仍使用标准 envelope 和安全、稳定的错误映射。

### R4 — 三个设置入口一致

- `PUT /api/v1/settings`、`DELETE /api/v1/settings/{key}` 的 fallback 恢复和
  `POST /api/v1/config/import` 必须调用同一个 prospective transition contract。
- 改动非 Foundation setting 时保持现有直接持久化行为。
- readiness/ack gate 的现有 409 `就绪检查未完成` 合同保持不变；unexpected failure 仍为
  generic 500，不向客户端返回 raw error。
- CodeDefault 与 `.env.deploy` 的 `backup_assets.enabled=false` 保持不变。

### R5 — 启用、禁用与失败回滚

- 启用顺序保持：readiness authorize → Content prepare → admission/persistence → Search
  prepare/start → publish Content/Search ready。
- Content、Admission、settings persistence、enablement stamp 或 Search 任一阶段失败：最终
  effective enabled 保持/恢复 false，Content/Search not ready，Admission 不处于 managed
  假状态，stamp 精确恢复旧值。
- 禁用顺序保持先关闭 readiness、drain Content/Search，再持久化 false；若持久化失败，
  必须用 prior prospective config 恢复旧 runtime，且 setting/stamp 不变。
- rollback 自身失败必须返回 joined typed error 并保持 fail-closed；不得声称转换成功。

### R6 — 真实 production graph 回归测试

- 第一条 RED 必须命中真实 settings Service + Runtime settings transition 的锁重入，不得只
  使用 `gaSilentContentManager` 或不读取 Foundation 的 spy。
- RED 若会遗留死锁 goroutine，使用 helper subprocess 或等价隔离，保证测试进程可结束。
- handler 测试必须覆盖真实或 production-equivalent Content 与 Search config consumer，证明
  请求在 deadline 内返回并使用 prospective 值而不是 DB 中的旧值。
- 增加静态/source guard，禁止 config-aware mutation path 重新引入 Foundation snapshot
  getter；测试 fake 不能再次静默掩盖生产调用图。

### R7 — 观测、发布与生产复验

- 新日志只记录闭合 stage/outcome/reason、耗时和 request correlation；不得记录设置值、
  cache/export root、凭据、locator、Provider output 或完整 config。
- 修复通过 PR/CI、合并、release、Docker 多架构发布后，使用不可变 digest 验证新镜像。
- 生产只允许一次受控启用复验：HTTP 200 明显低于 timeout、setting true、success stamp
  非空、Content/Search ready、无 499/critical log、容器 healthy 且 restart count 0。
- 若复验失败，禁止反复点击；保留 DB/log/image 证据，并按 runbook 重启或回滚。

## Out of scope

- 为 capability-gap 任务自动创建 Repository 或改变清单分类；候选数 0 是独立产品数据
  状态，不是本次死锁修复目标。
- 发布可选 Worker、启用 Native AWS、改变 backup-assets 默认开关。
- 重构所有 settings 或所有 runtime lifecycle 锁；只修复 Foundation mutation 协调边界。
- 自动修改生产 compose、DB、设置或再次执行启用。
- 启动节点日志 P1；它继续等待本 P0 发布并完成生产验收。

## Acceptance Criteria

- [ ] AC1：真实 mutation regression 在修复前确定性超时/RED，修复后同一 selector 在明确
  deadline 内返回，不遗留锁或 goroutine。
- [ ] AC2：Content 与 Search enable path 均消费 prospective typed config，source guard
  证明 mutation-inner path 没有 Foundation snapshot/current getter。
- [ ] AC3：prospective parser 与普通 current getter 对同一完整 values 返回完全相同配置；
  缺 key、非法组合或错误类型 fail closed。
- [ ] AC4：PUT true、DELETE fallback true、config import true 均在 production-equivalent
  runtime 下完成，并持久化正确值；blocked readiness 仍为 409 且零副作用。
- [ ] AC5：并发第二个 mutation 在等待 owner 时取消，及时返回 context error、零持久化、
  owner 不受影响，gate 随后可复用。
- [ ] AC6：mutation 中的外部 snapshot reader 阻塞；成功后只见完整新值，失败后只见完整
  旧值；现有 atomic snapshot regression 保持绿色。
- [ ] AC7：Content prepare、Admission、persist、stamp、Search startup 各故障点都得到独立
  测试；setting/stamp/readiness/admission 全部恢复到精确 prior state。
- [ ] AC8：disable persistence failure 使用 prior config 恢复 Content/Search，不重读设置，
  不留下 half-disabled runtime。
- [ ] AC9：timeout/cancel 等待全部本次工作 join 后返回；focused tests 在 `-count=50` 与
  `-race` 下稳定，没有 goroutine/lock 泄漏。
- [ ] AC10：settings、backupasset/runtime、handler/config-import owned package tests、全
  backend tests/build/lint/vet、privacy/source scans 与 `git diff --check` 全绿。
- [ ] AC11：错误响应使用标准 envelope；日志/错误/指标不含 settings values、root paths、
  secrets、locators 或 Provider evidence。
- [ ] AC12：PR required CI 全绿，merge/post-merge/release/image publish 证据齐全并记录 immutable
  image digest。
- [ ] AC13：用户提供一次生产启用复验证据并满足 R7；在此之前 Child 18 发布验收保持失败，
  父任务不得宣称最终完成，节点日志 P1 不启动。

## User action after implementation

当前不需要用户执行任何生产操作，也不要再次点击“启用备份资产”。实现、发布和镜像摘要
验证完成后，用户只需按交付的单次受控验收步骤执行启用并回传结果。
