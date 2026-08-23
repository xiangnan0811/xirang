# 节点日志采集超时与队列卡死修复

## Goal

修复节点日志采集在 SSH 已连接后的读/等待阶段不响应超时，导致 worker 永久占用、
同一节点重复入队和队列持续溢出的故障。修复后，每次采集都有覆盖完整生命周期的
硬截止时间，每个节点最多一个 queued/in-flight 任务，超时/超限后资源被关闭并可在
后续轮次恢复，Scheduler 的 Shutdown 能真正取消并等待所有 worker。

这是独立 P1 可靠性任务，不属于 Child 18 迁移失败的因果链，也不阻塞迁移 P0 的
实现顺序。生产节点日志采集在本任务发布并完成受控验证前保持临时关闭。

## Background

- 生产有多个仅启用 journalctl 的节点，游标长期没有推进。
- 全部 worker 被占用后，30 秒 scheduler 仍为相同节点重复创建任务，50 深度队列
  很快填满，并持续输出 queue-full 警告。
- 观察窗口内有大量 queue-full，但没有 fetch/cursor/insert/save failure；这与
  worker 卡在 `Runner.Run`、尚未返回错误一致。
- 当前 SSH runner 的 context 只覆盖 TCP dial/SSH handshake。会话创建后使用阻塞
  `io.ReadAll(io.LimitReader(...))` 和 `session.Wait()`，context 到期不会主动关闭
  session/client。
- `LimitReader(maxBytes)` 只停止本地读取，不证明远端停止写；远端可能因 stdout
  未继续排空而阻塞，随后 `Wait()` 永远等待。
- Scheduler 没有 per-node queued/in-flight 状态，也没有等待 worker 的 WaitGroup；
  `done` 只证明调度循环返回，不证明采集 goroutine 已退出。

详细去标识化证据见 `research/root-cause.md`。

## Locked decisions

1. 超时覆盖 auth、dial、handshake、session start、stdout read、remote wait 和资源 join；
   不能只约束连接阶段。
2. 输出上限是硬失败，不返回/解析/保存截断数据，也不推进游标。
3. context 取消或输出超限必须主动关闭 session 和 client，并等待内部 goroutine 退出。
4. 每个节点在 scheduler 中最多一个 queued 或 in-flight job；下一 tick 不复制同一工作。
5. 任务完成（成功或失败）后释放节点状态；永久失败不能永久占住去重键。
6. 生产节点名称、host、命令、日志正文和凭据不得写入新错误、指标或审计 metadata。
7. 不把“临时关闭采集”当产品修复；它只是发布前的生产缓解措施。

## Requirements

### R1 — SSH 全生命周期 deadline

- `Runner.Run(ctx, node, cmd, timeout, maxBytes)` 必须以调用者 ctx 与 timeout 中更早的
  截止为权威。
- deadline 到期后主动关闭 session/client，使阻塞的 stdout 和 Wait 返回。
- Run 在资源关闭后才返回；不得留下一个仍可能读/写的后台 goroutine。
- 返回值使用 `context.DeadlineExceeded`/`context.Canceled` 可识别包装，使 Fetcher
  仍能记录闭合 reason=`timeout|canceled|ssh_error|output_limit`。
- SSH 凭据审计只记录 safe stage、outcome、node ID 和界限；不记录 raw error/output。

### R2 — 输出上限无死锁

- 读取最多 `maxBytes+1` 用于判定超限；第 `maxBytes+1` 字节出现即为 typed
  output-limit failure。
- 超限后关闭远端会话/连接并 join；不得停读后无界等待 `session.Wait()`。
- 超限或不完整输出不得进入 parser、node_logs 或 cursor storage。
- `maxBytes<=0` 被视为无效调用并在 dial 前拒绝。

### R3 — per-node 单飞去重

- Scheduler 维护并发安全的 per-node queued/in-flight 集合。
- enqueue 前原子认领；已 queued/in-flight 的节点计入 deduplicated 指标但不再写队列。
- channel 满或 context 已取消时撤销未成功入队的认领。
- Worker 以 `defer` 释放认领，覆盖 cursor load、fetch、insert、cursor save 的所有退出。
- 成功/失败完成后的下一轮允许该节点再次采集；禁用源或删除节点不会留下永久键。

### R4 — 可证明的 Scheduler shutdown

- Scheduler 拥有内部 cancel 和 worker WaitGroup。
- `Shutdown(ctx)` 主动停止新 enqueue、取消 in-flight Runner、关闭/停止队列消费并等待
  调度循环和所有 worker；`done` 只能在全部 join 后关闭。
- Shutdown 幂等；Run 未开始/已经结束的行为有确定测试。
- caller deadline 到期返回 context error，不声称已完成；后续 close 仍能最终释放资源。

### R5 — 数据与游标一致性保持

- fetch 失败、超时、取消、超限时保持现有游标不变且不插入日志。
- 成功路径继续 sanitization 后批量写入，再保存新游标。
- 本任务不改变现有日志内容脱敏、路径白名单、shell quote 或 retention 语义。
- 日志插入与 cursor upsert 的跨事务原子化不是本次卡死修复范围；若实现审查发现它
  影响本故障安全性，另列任务而不是暗中扩大范围。

### R6 — 可观测性和降噪

- 保留 fetch duration/error/ingested/queue depth，并增加低基数结果：至少
  deduplicated、queue rejected、in-flight 和 shutdown timeout。
- queue-full 日志每次 enqueue pass 聚合为一条，报告数量/容量，不逐节点刷屏。
- timeout/output-limit 使用稳定 reason，不把 raw SSH error 作为 Prometheus label。
- 不新增 Node 名、host、用户名、路径、cursor、journal 内容或命令标签。

### R7 — 生产缓解与恢复边界

- 当前生产所有节点日志采集保持关闭，直到修复镜像发布、Core 健康和受控单节点验证
  完成。
- 发布后先只启用一个低风险节点，验证两个以上采集周期、游标推进、无 queue-full、
  无 worker 泄漏，再分批启用其余节点。
- 该操作必须由用户在生产 UI/配置中完成；代码任务不得远程修改生产节点。

## Out of scope

- 修改节点日志保留期、页面查询或日志内容展示。
- 重新设计日志存储 schema 或把日志采集改成 Agent/push 模式。
- 修复所有 lifecycle.Worker 的全局 shutdown 排序；只保证 nodelogs 自己可取消/join。
- 自动重新开启生产采集。
- 修复迁移 69；由独立 P0 任务负责。

## Acceptance Criteria

- [ ] AC1：fake SSH session 在 stdout 永不结束、remote Wait 永不结束、或调用者取消时，
  Run 在确定性时限内返回对应 context error，并关闭 session/client。
- [ ] AC2：输出恰好等于上限成功；超过一个字节返回 output-limit，关闭/join，返回无
  payload，parser/DB/cursor 均无变化。
- [ ] AC3：重复调用 enqueue 多轮时，同一节点最多一个 queued/in-flight job；去重不
  占额外队列槽。
- [ ] AC4：job 的成功、fetch 失败、cursor load 失败、insert 失败、cursor save 失败
  都释放 per-node 状态，后续轮次可再次入队。
- [ ] AC5：一个/全部 worker 遇到阻塞 runner 并超时后，后续正常 job 能执行；队列不
  永久饱和。
- [ ] AC6：Shutdown 主动取消 blocking runner，等待全部 worker 后才返回 nil；caller
  deadline 先到则明确返回 error。
- [ ] AC7：fetch timeout/output-limit 不插入日志、不推进已有 cursor；正常采集仍写入
  sanitized rows 并推进 cursor。
- [ ] AC8：queue-full 每轮最多一条聚合日志；指标 reason 集合闭合且测试证明无敏感/
  高基数字段。
- [ ] AC9：focused tests 在 `-count=50` 和 `-race` 下稳定；nodelogs、sshutil 相关测试、
  全 backend tests、lint、vet、build、`git diff --check` 全绿。
- [ ] AC10：v0.44.8 与当前 main 的问题代码等价证据保留；修复不错误归因于 v0.50.2。
- [ ] AC11：新镜像发布前生产采集保持关闭；发布后的单节点/分批恢复步骤写入验收记录，
  未由用户执行时任务不得宣称生产采集恢复。

## User action after implementation

用户后续只需要执行生产重新启用与观察，且必须等修复发布后。本规划不会要求现在
开启任何节点日志采集。
