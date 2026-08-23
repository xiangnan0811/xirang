# Design — 节点日志采集超时与队列卡死修复

## Boundaries

改动集中在 `backend/internal/nodelogs`，必要时复用/小幅扩展 `sshutil` 的连接关闭
能力，并更新后台 worker/日志规范。主产品页面、节点日志 schema 和生产配置不变。

## Failure model

当前数据流：

```
tick -> enqueue every eligible node -> buffered jobs -> 10 workers
                                             |
                                             v
                         DialSSH(ctx) -> NewSession -> ReadAll(limit) -> Wait
```

context 只在 DialSSH 阶段能关闭 socket。连接成功后，`ReadAll`/`Wait` 没有监听
context；而 `LimitReader` 到上限会停止读取但不关闭远端，远端可能卡在写 stdout。
每个 tick 又复制同一节点 job，最终所有 worker 和 queue 都被占满。

## Proposed component design

### 1. Testable session boundary

把 sshRunner 的连接/会话创建收窄成可注入私有接口，生产适配器仍使用
`*ssh.Client` / `*ssh.Session`。测试 fake 能分别控制：

- dial/start 成功或失败；
- stdout 正常 EOF、永不 EOF、精确上限、超限；
- Wait 正常、ExitError、永不返回；
- Close 是否被调用以及是否解除 read/wait。

接口只在 nodelogs 包内，不暴露凭据或通用远程命令 API。

### 2. Bounded command state machine

推荐实现顺序：

1. 校验 `maxBytes>0`，构造 `opCtx=min(parent deadline, now+timeout)`。
2. 构建 purpose-scoped auth、host-key callback并 dial。
3. 创建 session/stdout pipe，Start 固定 server-generated command。
4. 启动一个 bounded read owner，最多读 `maxBytes+1`；启动一个 wait owner。
5. 协调器 select read、wait、`opCtx.Done()`：
   - read+wait 都成功且字节数 `<=maxBytes` → 返回完整 output；
   - 多出一个字节 → 标记 output_limit，关闭 session/client，join owners；
   - context 到期/取消 → 关闭 session/client，join owners，返回 context cause；
   - transport/read/wait 异常 → 关闭并 join，返回 typed safe error。
6. 只有 join 完成才从 `Run` 返回。

若底层库需要一个 owner 同时管理 copy/wait，可调整 goroutine 数，但必须保持相同
可观察合同。不得通过 `time.After` 后直接返回并遗留 goroutine。

ExitError 的现有兼容语义可以保留：只有在 stdout 完整、未超限且 session 返回明确
远端 exit status 时，允许解析已有输出；transport/missing-status 仍失败。

### 3. Per-node state machine

Scheduler 内部维护：

```
absent --claim/enqueue--> queued --worker receive--> in_flight --finish--> absent
             | queue full / cancel
             +-----------------------------------------------> absent
```

实现可用 `map[uint]jobState` + mutex。认领和状态转换集中在 Scheduler 私有方法：

- `tryClaim(nodeID) bool`
- `markInFlight(nodeID)`
- `release(nodeID)`

Worker 获得 `onStart/onDone` callbacks 或一个窄 coordinator 接口；`release` 必须 defer。
不把 mutex 持有到 channel send、SSH 或 DB 操作。

节点在 queued 时被禁用/删除也只会执行最多一次旧快照 job；下一轮 DB 查询不再入队。
如需更严格的“receive 后重新读取节点配置”，另行评估；本任务不扩大到配置版本 fencing。

### 4. Scheduler lifecycle

Scheduler 新增私有 lifecycle 状态：run cancel、worker WaitGroup、run done、start/stop
同步。Run：

1. 建立可被 parent 或 Shutdown 取消的内部 context。
2. 启动 worker 并加入 WaitGroup。
3. tick enqueue。
4. context 结束后停止 tick、不再 enqueue、关闭 jobs（仅一个 owner）。
5. workers drain/observe cancellation并退出。
6. WaitGroup 完成后关闭 done。

Shutdown 先调用内部 cancel，再等待 done 或 caller context。它不依赖 main 在之后才
调用的 hub cancel，因此 nodelogs 自己符合 lifecycle.Worker 合同。

### 5. Metrics and logging

新增或规范化：

- `xirang_node_logs_jobs_deduplicated_total`
- `xirang_node_logs_queue_rejected_total{reason}`，reason 闭合为 `full|shutdown`
- `xirang_node_logs_in_flight`
- fetch error reason 扩展 `canceled|output_limit`
- 可选 `xirang_node_logs_shutdown_timeouts_total`

queue 满时统计本轮 rejected 数，循环后输出一条 structured Warn：`rejected`,
`queue_capacity`, `queue_depth`。不输出节点列表。

## Data behavior

- Runner error 返回 Fetcher 前，无 entries/new cursors。
- Worker fetch error 保持已有 cursor。
- 只在完整 output 后 parse。
- 原有 sanitization、batch insert、cursor save 顺序保持。
- output limit 不允许把前 `maxBytes` 当成功，因为末尾 delimiter/cursor 可能缺失。

## Test design

### Runner

- parent already canceled before dial；
- timeout during read；
- timeout during Wait；
- session close releases both owners；
- exact limit success / limit+1 failure；
- transport read error / missing exit status；
- ordinary ExitError compatibility；
- auth/host/session/start failures仍写 safe audit，不含 raw error/output。

用 channels 控制事件顺序，不使用脆弱的毫秒 sleep 断言。测试检查 close/join 事件和
goroutine completion channel。

### Scheduler/worker

- same node repeated enqueue → one queued job；
- queued → in-flight → repeated tick remains one；
- every worker exit path releases；
- queue full撤销 claim；
- blocking runners占满 pool后全部 timeout，随后 healthy job 被处理；
- Shutdown cancels blocking job and waits；
- Shutdown deadline error path；
- `-count=50` and `-race`。

### Data safety

- timeout/output_limit preserve seeded cursor and zero new rows；
- retry after failure fetches from same cursor；
- success remains sanitized and advances cursor。

## Alternatives rejected

| Alternative | Rejection reason |
|---|---|
| Increase worker/queue counts | Delays saturation but leaks more SSH sessions and memory |
| Only add scheduler dedup | One stuck job per node still exhausts all workers forever |
| Only add context.WithTimeout | Current session read/wait ignores it after dial |
| Return immediately on timeout | Leaks goroutines/session and makes shutdown claims false |
| Keep truncated output | Can advance cursor past logs never parsed and loses data |
| Run one goroutine per tick without queue | Removes backpressure and worsens resource exhaustion |
| Global lifecycle refactor | Larger unrelated risk; nodelogs can own correct cancellation locally |

## Production rollout

1. Merge/release with collection still disabled on production.
2. Upgrade Core only after the separate migration P0 release gate is satisfied.
3. User enables one low-risk node.
4. Observe at least two tick/fetch cycles: cursor advances, in-flight returns to zero, no queue
   rejection, no timeout/limit unless expected.
5. Exercise a controlled unreachable/slow target if safe; confirm timeout releases state.
6. Enable remaining nodes in small batches and observe after each batch.
7. Any recurrence: disable collection, preserve sanitized logs/metrics, keep cursor, rollback image
   if necessary. Do not delete cursor/log data.
