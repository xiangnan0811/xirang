# Design — 备份资产启用事务死锁修复

## Boundaries

改动集中在 `settings` 的 Foundation mutation coordinator、`backupasset.FoundationService`
的 prospective parsers、`backupasset/runtime` 的 Content/Search feature transition，以及 settings
和 config-import handlers 的共享调用边界。API 路由、前端按钮、schema 72、Repository 数据模型、
Provider adapter 和生产配置不变。

## Failure model

当前第一处确定性自锁：

```
PUT settings
  -> WithBackupAssetMutation
     -> lock backupAssetMutationMu
     -> TransitionBackupAssetSettings(effective)
        -> TransitionFeature(true)
           -> contentManager.PrepareEnable
              -> foundation.ContentConfig
                 -> BackupAssetSettingsSnapshot
                    -> lock backupAssetMutationMu forever
```

只绕过 Content 后仍有第二处：

```
TransitionFeature(true)
  -> startSearchAfterEnable
     -> startupSearch
        -> searchWorker.StartupPass
           -> injected Config closure
              -> foundation.SearchConfig
                 -> SearchOverlayConfig
                    -> BackupAssetSettingsSnapshot
                       -> same lock forever
```

`sync.Mutex.Lock` 不监听 context，所以浏览器取消、Gin request cancellation、Nginx timeout 都不能
解除这个 goroutine。499 只是客户端/代理放弃等待，不代表服务端转换结束。

## Proposed architecture

### 1. Context-aware exclusive gate

把 `settings.Service` 内私有的 Foundation mutation mutex 收敛为一个容量 1、初始含 token 的
gate，并只暴露私有 acquire/release：

```
mutation acquire(ctx) -> select token or ctx.Done
snapshot acquire()     -> blocking token acquisition
release                -> return exactly one token
```

- mutation waiter 能响应 caller cancel/deadline；
- snapshot reader 仍与 mutation 互斥，保持旧的全有或全无可见性；
- acquisition 成功后必须 defer release；panic-safe、不可重复 release；
- 第二个 mutation 的取消不取消 owner，也不改变 token 数；
- 该 gate 不是可重入锁。正确性仍依赖 mutation-inner path 不再次读取 settings。

### 2. Prospective transition bundle

handler 已拥有 `current`、`overlay` 与完整 `effective`。在进入运行时转换前构建一个包内
不可变 bundle（名称可在实现时按现有 style 调整）：

```go
type foundationTransitionConfig struct {
    Enabled  bool
    Content  backupasset.ContentConfig
    Search   backupasset.SearchConfig
    Overlay  backupasset.OverlayConfig
    Export   backupasset.ExportConfig
    Recovery backupasset.RecoveryConfig
}
```

同时从 `current` 构建 prior bundle，仅供失败恢复。构造器要求完整 key set、调用一次共享
Foundation validation，并通过纯 parser 生成各 typed config。

`ContentConfigFromValues`、`SearchOverlayConfigFromValues` 与既有
`ExportConfigFromValues`、`RecoveryConfigFromValues` 采用同一模式。公共
`FoundationService.ContentConfig/SearchOverlayConfig` 先读 atomic snapshot，再调用同一 parser，
从而保证 current 与 prospective 两条路径不会漂移。

### 3. Config-aware Content transition

内部 Content transition port 改为显式配置：

```
PrepareEnable(ctx, prospective.Content)
PrepareDisable(ctx)
RestoreEnable(ctx, prior.Content)
```

真实 `managedContentRuntime` 用传入配置完成 reconcile/cache/broker resume，不调用 Foundation。
普通 startup/background reconcile 仍可在 mutation 之外动态读取 Foundation；source guard 只禁止
mutation-inner config-aware path 的重读。

### 4. Config-aware Search startup

Search worker 增加显式 `StartupPassWithConfig(ctx, SearchWorkerConfig)` 或等价私有方法。
后台 `Run` 继续使用动态 Config closure；feature enable path 则把 prospective Search config 映射为
worker config 并直接执行，不调用 closure。

启用 path 的 requested/live 判断来自 prospective `Enabled`、已经通过的 readiness，以及 admission
managed 状态；不能通过 `FeatureLive()` 再读取 current settings。Search key rewrap/ensure、pass 和 ready
发布全部使用同一个 bounded operation context。

### 5. Transition state machines

启用：

```
validate bundles
  -> authorize readiness
  -> Content prepare(prospective)
  -> Admission transition + persist enabled
  -> Search prepare/pass(prospective)
  -> commit exact success stamp
  -> publish Search ready + Content ready
```

成功 stamp 的物理写入可以与 settings persist 同事务，或用可证明的 exact prior-state compensation；
可观察合同是：只有最后成功状态保留 stamp，任一失败精确恢复 prior stamp。不要保留“先 stamp、后
persist/search”造成假成功的窗口。

失败补偿按逆序执行并 join errors：Search not-ready → Content not-ready/drain → Admission false/restore →
settings/stamp exact prior state。补偿失败时保持 product planes fail-closed，并返回 joined typed error。

禁用：

```
validate current/prospective
  -> publish Content/Search not-ready
  -> drain/join Content/Search
  -> Admission transition + persist false
  -> success
```

若 persist/Admission 失败，使用 prior bundle 恢复 Content/Search 和 ready 状态；不读取 Foundation。

### 6. Bounded operation ownership

Runtime 在 transition entry 建立 `opCtx=min(caller deadline, featureTransitionTimeout)`。内部 ceiling
集中定义并严格小于 server 30 秒写超时，测试使用注入 clock/deadline 或同步 channel，不依赖脆弱
sleep。

任何内部 goroutine 必须由调用者拥有 completion channel/WaitGroup。timeout/cancel 顺序固定为：
cancel → stop accepting → close/drain owned resource → wait/join → return context error。若底层组件无法
在界限内证明 join，保持 runtime not-ready 并返回明确 failure，不在后台继续 persistence。

## Entry-point matrix

| Entry | Overlay source | Persistence | Required result |
|---|---|---|---|
| Settings PUT | request values | one DB transaction | prospective config, standard 200/409/500 |
| Settings DELETE | env/code fallback for key | delete override transaction | same transition before delete |
| Config import | bounded imported settings | import transaction | same transition, no partial settings |
| Runtime startup | persisted effective snapshot | no request mutation | existing startup contract, no gate re-entry |
| Disable rollback | exact prior bundle | exact prior setting/stamp | no Foundation reread |

## Error and observability contract

- Readiness blocked/ack required remains typed 409 `就绪检查未完成`。
- Canceled/deadline errors preserve `errors.Is`; client-facing body stays generic and standard.
- Unexpected internal failures remain 500; no raw DB/config/path/provider error reaches the response.
- Structured events use module `backup_asset_ga` or existing owner and closed fields such as
  `stage=gate|content|admission|search|persist|rollback`, `outcome`, `reason`, `latency_ms`。
- Never log settings values, cache/export/derived roots, credentials, locators, proofs, output or config maps.

## Test design

### Genuine RED

- Use a helper subprocess or isolated test process that runs real `settings.Service.WithBackupAssetMutation`
  and the production-equivalent runtime path whose Content manager reads `Foundation.ContentConfig`.
- On current code the helper fails its deadline because the same goroutine re-locks; the parent test kills/joins
  the subprocess and records RED without poisoning the main test process.
- A second RED drives Search `StartupPass` through its real config closure while the mutation is owned, proving
  Content-only repair is insufficient.

### Prospective config

- Current getter and `FromValues` parser equality for Content/Search/Overlay.
- Complete prospective overlay differs from persisted DB and runtime observes the prospective value.
- Missing key/invalid cross-setting combination fails before runtime or persistence.
- Source/AST guard rejects Foundation snapshot/current getter calls in config-aware transition functions.

### Concurrency and atomicity

- External snapshot blocks until mutation commit then sees every new key.
- Mutation failure releases gate and reader sees every old key.
- Second mutation canceled while waiting returns context error with no callback/persist; subsequent mutation works.
- Panic test, if the API permits callback panic, proves defer release without masking panic semantics.

### Failure matrix

- Content prepare, Admission, settings persist, stamp, Search key and Search pass failures.
- Search failure after persisted true restores setting false/prior and prior stamp exactly.
- Disable persist failure restores prior Content config and ready state without Foundation calls.
- Timeout/cancel at each blocking seam proves cancel/close/join and no later write/readiness change.
- Handler tests cover PUT, DELETE fallback and config import with production-equivalent runtime, not silent managers.

### Gates

- Focused selectors repeated `-count=50` and `-race`.
- Full settings, backupasset, runtime and handler/config-import owned package tests.
- Full backend test/build/lint/vet, source/privacy scans, task validation, `git diff --check`.
- Independent review of lock order, goroutine ownership, persistence/stamp compensation and all error exits.

## Alternatives rejected

| Alternative | Rejection reason |
|---|---|
| Remove the outer mutation lock | Breaks existing all-or-nothing snapshot and allows concurrent runtime/settings races |
| Move runtime work after unlocking | Exposes persisted/runtime half-transition and makes rollback race with another mutation |
| Make the mutex reentrant/owner-aware | Go mutex has no safe owner model; hides future lock-order violations |
| Add an unsafe snapshot bypass | Lets a future caller silently read stale or half-transition values |
| Only patch Content | Search StartupPass contains a second proven snapshot re-entry |
| Only lower Nginx/Gin timeout | Client returns but deadlocked goroutine and held lock remain |
| Keep silent handler fakes | They cannot prove the production dependency graph or config-read path |
| Fire Search in background | HTTP success would precede known runtime readiness and rollback authority |

## Production rollout

1. Keep production v0.50.3 healthy with feature disabled; do not repeat enable.
2. Merge only after required CI, then observe post-merge Release Please and image publication.
3. Record the new stable version, GitHub release commit, Docker manifest digest and amd64 image ID.
4. Before upgrade, create/verify a logical SQLite backup and record current health/schema/feature state.
5. Upgrade Core only; verify healthy, schema clean, setting still disabled and no critical logs.
6. If inventory digest changed, run inventory and acknowledge once.
7. Start a timed observation, click enable exactly once, and verify HTTP 200 well below the deadline.
8. Verify DB setting true, success stamp nonempty, Content/Search ready, data page loads, no 499/critical error,
   container remains healthy with restart count 0.
9. Candidate count may remain 0 while current tasks have capability gaps/no Repository; that does not invalidate
   the enable transition itself.
10. Any failure: do not click again; preserve logs/DB/image evidence and use the documented restart/rollback path.
