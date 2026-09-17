# Backup 生命周期 provider effect fencing 与 settled audit 幂等

## Goal

让 Backup 生命周期的 provider deletion 在多 worker、并发 `Advance`、进程崩溃与接管下只执行受持久 claim 保护的远端删除，并让 settled purge audit 在并发 tick、重启和明细保留清理后仍只发射一次同一逻辑事件。

这是持久并发正确性，不是新的产品功能。运维看到的是：同一 lifecycle attempt 不会因并发或 crash window 被无 claim 地再次删除，审计时间线不会在明细过期后重放同一条 settled 事件。

## Background and current evidence

2026-09-02 的冻结 target、repository/point/attempt/lease 复核、late hold、provider receipt 和多提供方身份闭包是本任务的基线，不是本任务的交付物。当前代码仍是：

```text
Coordinator.Advance
  -> deleteAndTransition
     -> lookupProviderDeleteReceipt / prepareExternalEffect
     -> RegistryPointDeletion.DeleteRecoveryPoint
        -> Tx1 freeze + revalidate
        -> PointDeleter.DeletePoint (transaction 外)
        -> Tx2 revalidate
     -> persistProviderDeleteReceipt
     -> settled audit
```

当前 `RegistryPointDeletion.DeleteRecoveryPoint` 在 provider 调用前后有事务，但没有绑定 executor、execution、fence、revision 和 exact target 的持久独占 claim。`LeaseOwnerID` 仍是所有 retention worker 共用的 `retention-worker`，不能作为 effect executor。crash-after-effect/before-receipt 会让重启看起来像从未执行。

当前 settled audit 在生产 adapter 上是 scan-then-`Write`：它可能扫描会被 `AuditRetention.PurgeEligibleDetails` 删除的 `backup_asset_audit_events` 明细，且成功路径没有经过同一去重边界。详见 `research/current-necessity-review.md`；该文件和 `research/session-handoff.md` 是代码取证/历史背景，不是已完成证明。

## Scope and requirements

### R1 — Coordinator-owned durable provider-delete claim

1. 只覆盖 `retention_expire` 和 `explicit_purge` 的 `provider_delete`。Coordinator 必须拥有专用 provider-delete transaction 和 claim 生命周期；不得再由旧的一次性 `PointDeletion.DeleteRecoveryPoint` 隐式包住 claim。
2. `RegistryPointDeletion` 只提供进程内的三段式 adapter boundary：
   - `Prepare(..., profile=observer|execution)`：observer 只校验 repository/point/locator/provider authority，不要求 active lease、future old deadline 或 caller fence；execution 才校验 fresh/current lease。两者只解析/冻结，不 network/provider；native client lazy。
   - `Execute(ctx, prepared)`：唯一 provider 调用点，事务外，返回 providerCalled/stage。
   - `Verify(...)`：Tx2 重新解析 stable semantic authority 与结果先决条件，不调用 provider。
   旧 one-shot bypass 移除；`prepareExternalEffect` 仅 Revoke/Cleanup。
3. Tx1 全锁后 proof/live winner 优先。仅无 claim 的 renewable pre-claim 路径可先 `ensureLifecycleFenceTx`。任何 takeover-eligible stale/uncertain claim（short live、short-expired/absolute-live、absolute-expired）都必须先 hold/reference 分类与 observer Prepare/digest；匹配后 lease/fence takeover、execution Prepare、claim CAS 才可同一事务全成/全 rollback。provider 仅 commit 后调用。
4. claim 绑定 lifecycle attempt（通过 attempt FK 取得 operation/phase/recovery point authority）、transition revision、lease fence snapshot、不可变 exact target identity、process-unique `EffectExecutorID` 和本次 acquisition 的 `execution_id`。每次 acquire/takeover 都生成新的 32-hex `execution_id`；同一 Coordinator 的并发调用也必须互相区分。
5. `CoordinatorDependencies.NewID` 只用于既有 attempt ID 生成。effect executor、execution、claim row 和 audit slot ID 必须独立使用安全随机生成；不得借用 `NewID`。
6. 无 claim row 对既有 v76 任意合法 phase/reason 都合法，包括 valid-tombstone tombstoning/complete；provider_delete 无 tombstone 在 000077 cutover 前被拒。claimed matrix：`in_flight+provider_delete`；`uncertain+provider_delete` 或三个 observer block；`proven+valid tombstone` 可在 provider_delete、late-hold blocked/active_hold、tombstoning/complete。proven 无 tombstone 是 corruption。observer resume 只回 provider_delete；live loser 零 mutation。

### R2 — Crash、deadline、fence 和 identity protocol

1. Claim 合法状态只有 `in_flight`、`uncertain`、`proven`。没有 claim row 表示尚未 acquire；不存在第四状态。
2. claim row 永不 DELETE。第一次成功 acquisition 持久化 `target_identity_digest` 后，该 digest 不可变；takeover/retry 必须使用同一 digest。`proven` 只能 prove-only 且不可回退/修改。
3. pre-claim 错误不产生 row；一旦 claim acquisition 已提交，任何错误、取消、超时、resolver/verify 失败或“看似没有效果”的失败都只能保留 `in_flight` 或变为 `uncertain`，不能删除 row 或伪造 `provider_delete_unproven`。
4. `EffectClaimTTL` 默认 2 分钟，边界保持 `<1s` 或 `>1h` 拒绝。claim `deadline_at` 是可续约期限，始终截断到 lifecycle lease 的不可续约 absolute deadline；expiry 必须在锁已串行化 claim row 后以同一注入的 `Now` 判断。live、fence/revision 仍匹配的 foreign 或 same-executor claim 返回 `ErrEffectClaimInFlight`：loser 只观察并返回，不写 `retry_at`，不 block，也不修改 attempt/lease/claim/tombstone；正常 worker 调度负责后续 tick。
5. Execute renewer 是有 claim 的 live execution 唯一续租方。Heartbeat 分三类：valid proof→成功且零 mutation；任意 provider-delete claim→只观察，不 ensure/adopt/Renew/block；无 claim（含 Revoke/Cleanup 与 pre-claim lifecycle）保留现有 ensure+RenewTx 行为。Coordinator、LeaseService、Registry、repository.Service、publication、Rclone-native lazy snapshot、AuditWriter 与 fixtures 共享同一 `Now`。
6. takeover-eligible claimed stale/uncertain 在任何 lease 时间组合都先 observer profile，禁止先 ensure/TakeoverTx/AcquireTx。active hold、native-version reference 或 digest mismatch 只 commit uncertain+对应 block，随后另开 slot Tx；native reference 不占 000076 reservation，清除后只 resume provider_delete。仅 observer digest 匹配才可在一个事务内处理 exact current lease（必要时 takeover/expire/Acquire）、CAS attempt、execution Prepare/recheck 和 uncertain→in_flight/new execution；后半段全成或全 rollback。
7. Tx2 校验 full binding/deadline/stable authority。proof settle 对 exact active lease覆盖三种时间：short `lease_expires_at` 与 absolute 都未来→ReleaseTx；short 已过但 absolute 未过→proof-specific exact active→expired CAS；absolute 已过→exact active→expired CAS；exact released/expired 幂等。所有 production proof 入口绕过 ensure/adopt/block/unproven/provider。
8. 无 claim 首次 acquisition 的 execution Prepare 用 current absolute deadline。任何 claimed takeover 的 observer Prepare 都用 `min(parent, now+EffectClaimTTL)`；digest match 且原子取得 current/fresh authority 后再用 fresh absolute deadline execution Prepare，并冻结 native lazy client。Execute child 受 authority deadline、parent 与 renew failure 共同约束；renewer Tx2 前 stop/join。
9. 只要 provider invocation 已开始，所有错误均按可能 partial effect 保留 in-flight/uncertain；Rclone 多版本 WORM 不是 definite no-effect。
10. stage 保留 `errors.Is`。claim-aware retryBlocked 不调用 pre-claim active_hold→revoking 或 blocked-phase lease adopter：active_hold/native-version-referenced/identity-conflict 分别 observer recheck，只 resume provider_delete。lifecycle fact 先 commit，slot 后开 repo-first Tx；fresh lease adoption 只能与同 digest takeover 原子提交。唯一非 effect retry 是 slot helper 的 retry_at-only CAS。

### R3 — Exact target identity without raw secrets

1. 不得整体 hash `DeletePointRequest` 或 `sameLifecycleDeleteRequestAuthority`，因为它们含 `Point.Native`、locator/config/secret、IdentitySalt 和 provider client。只存 64-hex digest，不存 locator、credential、command、raw fence token 或 provider client。
2. public framing 使用 `backupasset.NewCanonicalSHA256`；private fingerprint 用 32-byte IdentitySalt 对 exact point/locator/config/secret、provider authority 与 remote-command authority 做 length framing。RemoteCommandAccess 不是整体 strip：provider helper 必须对白名单 execution authority（Node endpoint、auth/credential/key lineage、base/backup path、sudo 与适用 key policy）做 keyed fingerprint；只排除 Audit、指针 identity、连接 client 及 health/telemetry/last-used 等非授权运行态。只持久化最终 64-hex digest，绝不原文 credential/command/salt/client。
3. 四种真实 resolver 必须传播 salt 与完整 authority；provider package 负责 private canonicalization/fingerprint。Rclone-prefix/Restic 的 command fingerprint 至少绑定 Node Host/Port/Username/AuthType/Password/PrivateKey/SSHKeyID、BasePath/BackupDir/UseSudo/Tags，以及已加载 SSHKey 的 ID/Username/KeyType/PrivateKey/Fingerprint/Disabled/ExpiresAt/AllowedPurposes/AllowedNodeIDs/AllowedNodeTags，并排除 volatile timestamps/health/audit context。未知/不完整 authority fail closed。
4. Tx2 不再使用当前 raw `reflect.DeepEqual`。provider-owned stable authority projector/comparator 与 digest 共享同一显式白名单/规范化，但 Tx2 仍重新解析并独立比较 frozen/current semantic authority，再分别核对 stored digest；endpoint/credential/key/policy/path 等变化 fail closed，SSH key `LastUsedAt/UpdatedAt`、Node health/status/timestamps、Audit 与 opaque client 变化不冲突。固定 vectors 及 receipt-path PostgreSQL test 必须覆盖 provider 自身更新 LastUsedAt 后一次调用即可成功落 proof。

### R4 — Retention-proof settled-audit emission slots

1. 建立 `recovery_point_lifecycle_audit_slots`，逻辑唯一键为 `(attempt_id, status)`，`emitted_at` NOT NULL。slot 是永久幂等证据，不能依赖可被 retention 删除的 `backup_asset_audit_events`。
2. `settledDeletionCandidate` 是 migration/runtime 唯一候选谓词：operation 为 retention_expire|explicit_purge，且（valid tombstone/receipt→terminal status，或 blocked 且 reason 为现有 `providerDeletionBlocked` 集合或 active_hold→observational status）。selected/revoking/draining/cleaning、lease_live、lease_drain_unproven、owner_cleanup_unproven、fence_lost、mutable_retire 等非候选一律 no-op。候选 writer 才自行开 repo-first Tx、推导 status、WriteTx、INSERT slot；缺 lease fail closed。
3. `retention.AssetAuditSink` 增加 `WriteTx`；nil sink 保持现有 skip 行为，非 nil sink 必须实现 `WriteTx`。不修改不同包的 `repository.AssetAuditSink`，不为没有 event ID 的 `WriteTx` 强行添加 `audit_event_id`。
4. `blocked` 与 `identity_conflict` 是 observational status：每个 status 最多一次，允许二者任意顺序出现；任一观察 status 后可追加恰好一个 terminal success status。`deleted` 与 `already_absent` 互斥且各自 terminal；terminal 后不得再出现任何 status。数据库 unique/partial unique/index 与 fail-closed transition checks 必须共同执行该状态机。
5. stale blocked caller 必须在 lock 下重新推导当前 receipt/claim/attempt truth；若已经有 terminal receipt/slot，写对应 terminal 或 no-op，不能再发 blocked、不能回退到 provider_delete_unproven、不能循环争抢 claim。
6. fact 先 commit，slot writer 后开 Tx；失败后全锁 `scheduleSettledAuditRetry` 只 CAS retry_at。Worker 先调用 attempt-ID-only flush，返回四态：non-candidate/existing-slot→继续；candidate missing 且 retry_at future→pending，跳过 Heartbeat/Advance；missing 且 retry_at nil/due→尝试 writer，失败跳过，成功继续。所有可写 slot 路径执行相同 locked due check，不能绕过 backoff。

### R5 — Paired schema, rollback and validation

1. migration `000077_lifecycle_effect_claim_audit_slot` SQLite/PostgreSQL paired；IDs/digests exact lowercase hex，slot status width 32（`identity_conflict` is 17 chars）。
2. normalized claim snapshots 无历史 lease FK；claim id/attempt/digest immutable，proven/no-delete triggers；slot attempt FK RESTRICT、unique attempt+status、partial terminal unique、append-only triggers。
3. 000077 metadata admission 与 down body 各自独立 guard。claim-only/slot-only 在 admission intact 的真实 `migrator.Steps(-1)` 必须拒绝且 version 77 clean；另有移除/绕过 admission 后执行实际 down body 的拒绝测试。两者都保留 version/rows/tables/indexes/全部旧新 triggers/functions。version>=77 startup schema contract 强制验证所有新对象/语义。
4. v76→v77 只允许 quiesced cutover。先拒绝 scoped provider_delete 无 valid tombstone。只对 settledDeletionCandidate 评估 backfill/ambiguity；普通 no-claim phase/reason 忽略。retained exact event 必须完整匹配当前 producer：action repository_purge，outcome 与 status 映射，attempt→point→repository IDs，ItemCount/fields.item_count 都为1，fields.stage=settled、source=attempt、合法 status；terminal 还须与 tombstone outcome 一致，并按事件时间验证 observational→terminal 状态机。任何 near miss 不生成 slot；候选因而仍 ambiguous 时整次 rollback。仅 tombstoning/complete+valid tombstone 可按 v76 ordering 推断 terminal。
5. broad DB first，加 exact DB/retention/runtime wrappers。UpgradeCutover 覆盖每类 exact-field near miss、dedupe/顺序、terminal inference、候选 ambiguity、非候选忽略和 active provider_delete rejection；Constraints 覆盖 clean-v77 drift；UsedDown 同时覆盖真实 migrator admission 与 direct body。PostgreSQL AC fixtures 用 real migrator。

## Acceptance Criteria

Checked items below record local behavioral acceptance for the repaired worktree, not release readiness. Exact commands, PostgreSQL results, the review ledger, and external delivery limits are in `implement.md` section 6. AC9 remains open for committed-snapshot required remote CI.

- [x] **AC1 — PostgreSQL dual-Advance fence:** 两个并发 `Advance` 到同一个 `provider_delete` attempt，通过 `RegistryPointDeletion` 和真实 registered `PointDeleter` barrier；恰好一次 provider invocation、一个 claim row、一次 `in_flight → proven`、一个 terminal tombstone。另一调用只能观察 `ErrEffectClaimInFlight`，不得写 `retry_at`、`blocked/provider_delete_unproven` 或任何 attempt/claim mutation。确定性覆盖 late loser 与 winner receipt、以及 late loser 与 takeover 的交错，证明 loser 不能触碰 proven claim、新 acquisition 或已转换 attempt。
- [x] **AC2 — renewal, takeover and stale owner:** live loser/stale owner/receipt race；所有 stale/uncertain lease 组合先 observer。特别覆盖 short-expired/absolute-live 的 hold/native-reference/digest mismatch：零 lease/fence adoption、claim snapshot 不变；match 才原子 takeover。proof 三种 lease 时间及 Worker Heartbeat-first 均零 provider/ensure/adopt/block。合法 proof 先于当前 lease identity/authority 校验：历史 owner/holder/attempt/fence 重新绑定仍完成 audit、tombstoning、complete，且不变更或释放重新绑定的 lease；无 proof 或 proof 损坏仍 fail closed。
- [x] **AC3 — late hold and proof reuse:** late active hold 测试必须通过 `RegistryPointDeletion`（不是只用 `lifecycleDeletionFake`），证明 provider invocation 只有一次，receipt 与 proven claim 保持同一 fence/execution 关联；再次 tick 从 receipt 恢复并产出 provider outcome 对应的 settled status，不重删。
- [x] **AC4 — concurrent blocked emission:** 两个并发 blocked ticks 在真实 slot 表上对同一 `(attempt,status)` 只产生一个 slot 和一次 `WriteTx`；另一个调用 idempotent no-op，不靠 `HasSettledDeletion`、`RetryAt` 早退或 event scan 证明。
- [x] **AC5 — detail retention proof:** 在 `internal/backupasset/runtime` package 的真实 `retentionAssetAuditAdapter` 上构建 Coordinator/AuditWriter/retention，实际 purge closed non-latest details；越过 RetryAt 后不二写，slot 为唯一 proof，并由独立 exact runtime PostgreSQL selector 运行。
- [x] **AC6 — status state machine:** 表格/事务测试必须证明 `blocked` 和 `identity_conflict` 各最多一次且可任意顺序，二者之后最多追加一个 `deleted` 或 `already_absent`；`deleted`/`already_absent` 互斥且 terminal，terminal 后任何 status 都 fail closed。覆盖 stale blocked caller 在 success receipt 下重新推导 terminal/no-op。
- [x] **AC7 — shared writer, rollback and audit-only retry:** real Worker 中首次失败 schedule future retry；retry_at 前再次 tick 零 WriteTx/attempt/lease/claim/provider mutation；到期恰好一次 slot retry，成功/no-op 后才进 Heartbeat/Advance。非候选永远 no-op。
- [x] **AC8 — crash/partial-effect identity:** short-live、short-expired/absolute-live、absolute-expired 的 endpoint/auth/key drift都在 provider/lease mutation 前 observer block；Execute telemetry 不破坏 Tx2。claimed Rclone-native preparing sibling block 后可 commit，清除后 resume→Advance 且只删一次；post-call partial/WORM 永不 definite no-effect。
- [x] **AC9 — migration, upgrade parity and gates:** both engines 覆盖 exact-event near misses、候选/非候选、inference/ambiguity；claim/slot 各有 admission-intact migrator downgrade 与 bypassed direct-body down；clean-v77 drift。broad+exact 四组 CI/DSN/zero-match 通过。

## Out of scope

- Advisor #2 / #4 已完成的冻结 target、late-hold baseline、receipt reuse 和身份闭包。
- 通用 audit outbox、跨域消息队列或新的 delivery worker。
- 把 `RevokeRecoveryPoint` / `CleanupRecoveryPoint` 也改成 durable effect claim；另开 follow-up。
- 修改 `RetentionWorkerLeaseOwnerID`、publication reservation predicates 或 `provider_native_version_referenced` 的 000076 语义。
- 前端、HTTP/API 合同、设置项、GA 开关。

## Technical notes

权威实施细节在 `design.md`，有序执行清单在 `implement.md`。`research/current-necessity-review.md` 和 `research/session-handoff.md` 只记录取证与历史边界；未提交 worktree、旧测试通过或 planning review 不能当作本任务 AC 已完成证明。
