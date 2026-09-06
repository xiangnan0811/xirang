# Fresh-session planning handoff

> 2026-09-03: planning artifacts now exist. Treat `prd.md` + `design.md` + `implement.md` as current authority. This file is historical discovery context; function name `advanceProviderDelete` is stale.

## Handoff boundary

- 本任务只记录未完成工作；当前会话不实现、不迁移、不提交，也不把任务状态推进出 `planning`。
- `task.json` 中的 `implementation_authorized=false` 与 `next_gate=design_review` 保持有效。
- `prd.md` 的全部 Acceptance Criteria 仍为未完成；新的会话必须重新规划并通过设计评审后才能开始编码。
- 当前 worktree 含大量其他未提交修改。后续会话必须基于当时的实际 diff 和调用链重新取证，不得把本 handoff 当作代码已完成的证明，也不得 reset/checkout 覆盖现有工作。

## 已完成基线与本任务的边界

当前代码已经对 lifecycle deletion 的不可变 target、repository/point/attempt/lease 复核、late hold、provider receipt 和多提供方身份闭包做过加固。这些工作是后续设计的基线，但**不等于**本任务要求的 durable effect claim 或 settled-audit 持久幂等。

具体仍存在的边界：

1. `retention.RegistryPointDeletion.DeleteRecoveryPoint` 在事务中冻结并复核删除请求，但 `PointDeleter.DeletePoint` 是跨事务的外部效果；当前没有一个跨进程持久、绑定 executor/fence 的独占 effect claim 覆盖该窗口。
2. `Coordinator.advanceProviderDelete` 可以复用已持久化 receipt，但 crash-after-effect/before-receipt 仍需要明确的 uncertain-effect 状态与安全接管协议。
3. `Coordinator.flushSettledBlockedAudit` 先通过 `hasSettledDeletionAudit` 扫描 `backup_asset_audit_events` 明细，再单独调用 `writeSettledDeletionAudit`。该 scan-then-write 不是跨进程原子幂等边界，并且 detail retention 会删除它依赖的去重证据。
4. 最近的 backend 回归通过只能证明现有已实现范围；它没有覆盖本任务 PRD 中尚未实现的 claim、接管、crash window、outbox/emission slot 或 migration 行为。

## 未完成工作

### A. Durable provider-effect claim

- 重新设计 claim 状态机；至少明确未认领、执行中、已证明 settled、uncertain effect、可接管和终止状态，不能从本 handoff 直接假定最终枚举。
- 定义持久唯一身份：lifecycle attempt、operation、phase、transition revision、lease fence、immutable exact target、executor ID。
- 定义 claim deadline、续租、接管和旧 owner 晚到结果拒绝规则。
- 定义 provider receipt、late hold 与 terminal transition 的同一 claim/fence 原子关联。
- 明确 crash-after-effect/before-receipt 的恢复协议；不得通过生成新 operation/target identity 规避不确定性。
- 统一 publication reservation 与 lifecycle deletion 的 repository、point、attempt、lease/claim 锁顺序。

### B. Durable settled-audit idempotency

- 设计独立于 `backup_asset_audit_events` detail retention 的 emission slot 或等价持久状态。
- 定义至少包含 `(lifecycle attempt, settled status)` 的逻辑唯一键，以及不同 settled status 之间 fail-closed 的状态机。
- 将 emission claim 与 audit outbox/`WriteTx` 放入同一事务，替换 scan-then-`Write`。
- 明确 outbox 投递成功、失败、重试、进程崩溃和 detail 已清理后的行为。
- 保持 `deleted`、`already_absent`、`blocked`、`identity_conflict` 的真实语义，禁止错误合并或覆盖。

### C. Schema、迁移与验证

- 在设计评审中决定新增表还是扩展现有 lifecycle attempt/receipt 模型；本 handoff 不预先锁定方案。
- 成对实现 SQLite/PostgreSQL up/down migration、索引、约束和 used-down fail-closed guard。
- 增加 PostgreSQL 双 executor barrier、claim takeover、旧 owner late result、late hold receipt reuse、并发 settled audit、detail purge 后不重发等行为回归。
- 运行 targeted tests、race tests、migration tests 与完整 backend suite；保存失败复现和最终证据。

## 新会话建议起点

1. 重新读取 `prd.md`、本 handoff 和当前 worktree diff。
2. 逐段追踪：
   - `Coordinator.advanceProviderDelete`
   - `RegistryPointDeletion.DeleteRecoveryPoint`
   - `Coordinator.lookupProviderDeleteReceipt` / `persistProviderDeleteReceipt`
   - `Coordinator.flushSettledBlockedAudit`
   - `Coordinator.hasSettledDeletionAudit`
   - `Coordinator.writeSettledDeletionAudit`
   - runtime audit adapter 与 audit detail retention
3. 先提交 claim/crash-window/锁顺序和 audit emission/outbox/migration rollback 的设计评审材料。
4. 设计获批后再拆分 migration、model/store、coordinator、runtime wiring 和 PostgreSQL concurrency tests；不要在设计阶段顺手实现。

## Explicitly not done

- 没有为本任务修改生产代码、model、migration 或测试。
- 没有创建 effect claim 表、audit emission slot 或 outbox。
- 没有执行本任务 Acceptance Criteria。
- 没有 commit、push、PR、CI、merge 或发布动作。
