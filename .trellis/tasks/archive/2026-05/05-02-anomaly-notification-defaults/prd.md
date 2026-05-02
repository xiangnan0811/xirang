# 调整异常通知默认策略

## Goal

降低默认异常告警对用户的打扰。当前 EWMA 基线异常检测会在默认配置下直接升级为告警并触发通知；对低负载、小波动或样本窗口较短的服务器，`cpu_pct` / `load_1m` 的低绝对值变化也可能因为基线标准差很小而产生 `XR-ANOMALY-*` warning/critical 通知。目标是在保留可观测性的同时，让默认行为更稳健、低噪声，并允许需要强告警的用户显式启用。

## What I Already Know

* 用户反馈的通知来自 `XR-ANOMALY-CPU-*` 和 `XR-ANOMALY-LOAD-*`，示例中 CPU 当前值仅 `2.00`、负载当前值仅 `0.24`，但因相对基线偏离达到 `3.00σ` / `6.95σ` 被发送为 warning/critical。
* 当前后端默认值位于 `backend/internal/settings/service.go`：
  * `anomaly.enabled=true`
  * `anomaly.ewma_sigma=3.0`
  * `anomaly.ewma_window_hours=1`
  * `anomaly.ewma_min_samples=8`
* 当前 EWMA 检测位于 `backend/internal/anomaly/ewma.go`，每 5 分钟扫描 CPU、内存、1 分钟负载，并在偏离超过 `k * stddev` 时生成 finding。
* 当前严重度规则：超过阈值为 `warning`，超过 `2 * threshold` 为 `critical`。
* 当前 `backend/internal/anomaly/raise.go` 对每个 finding 都会先调用 alert raiser，再写入 `anomaly_events`；因此“检测到异常事件”和“升级为告警通知”目前不可独立配置。
* `anomaly_events` 支持 `RaisedAlert=false`，适合保留事件但默认不触发外部通知。
* 系统设置页会按 settings registry 自动展示 anomaly 类设置，新增后端 settings definition 会自然进入系统设置页；如需更友好的文案，需同步更新前端 i18n 或 UI。

## Assumptions

* 用户主要想降低默认外部通知噪声，而不是完全删除异常检测能力。
* 异常事件历史仍有诊断价值，默认可以保留在节点异常 tab / 全局 anomaly events 中。
* 对已经显式配置过 DB/env setting 的部署，应尊重其已有覆盖值；本任务只调整 code default 和新增可配置项。

## Requirements

* 默认情况下，低价值 EWMA anomaly 不应直接触发外部告警通知。
* 仍应允许用户显式开启 anomaly finding 到 alert pipeline 的升级。
* 默认 EWMA 参数应更保守，降低低负载服务器的小幅波动误报。
* 变更应保持现有 anomaly events API 和页面可用。
* 新增或调整的设置需能通过环境变量和系统设置接口配置。
* 测试需覆盖默认不升级告警、显式开启可升级，以及新默认阈值行为。

## Candidate Approaches

### Approach A: 默认保留事件、关闭告警升级（推荐）

新增 `anomaly.alerts_enabled`，默认 `false`。EWMA/DiskForecast 仍可写入 `anomaly_events`，但默认不调用 alerting pipeline，不发送 Telegram/邮件/Webhook 等通知。用户需要 anomaly 通知时显式开启。

优点：最符合“这些也不必要提醒”；不丢诊断事件；风险低；与现有 `RaisedAlert=false` 模型匹配。

缺点：默认不会把 anomaly 作为未处理告警出现在 alert center；需要在设置文案中说明“事件”和“通知”的区别。

### Approach B: 只调高默认阈值

例如将 `ewma_sigma` 从 `3.0` 调到 `5.0`，将窗口和最少样本调大。

优点：变更最小；仍默认通知真正离谱的异常。

缺点：低负载且标准差极小的场景仍可能误报；不能解决用户“不必要提醒”的核心问题。

### Approach C: 通知升级 + 绝对变化门槛

在 Approach A 基础上，对 CPU/内存/负载增加最小绝对变化门槛，避免 `0.03 -> 0.24 load` 或 `0.32 -> 2.00 CPU` 这类低绝对值变化进入 anomaly finding。

优点：事件本身也更少噪声；显式开启通知后仍更稳健。

缺点：需要设计每个 metric 的默认绝对门槛和配置项，范围更大。

## Proposed MVP

采用 Approach A，并附带保守调整现有默认 EWMA 参数。用户已确认选择方案 1：

* 新增 `anomaly.alerts_enabled=false`，默认 anomaly events 不升级为 alert/notification。
* 将 `anomaly.ewma_sigma` 默认值从 `3.0` 调整到 `5.0`。
* 将 `anomaly.ewma_window_hours` 默认值从 `1` 调整到 `6`。
* 将 `anomaly.ewma_min_samples` 默认值从 `8` 调整到 `24`。
* 保持 `anomaly.enabled=true`，即默认仍记录异常事件以供诊断。

## Decision (ADR-lite)

**Context**: 当前 anomaly finding 与 alert/notification 升级耦合，默认会对低负载服务器的小绝对波动发送外部通知。单纯提高 sigma 不能可靠解决低标准差场景的误报通知。

**Decision**: 默认保留 anomaly event 记录，但新增 `anomaly.alerts_enabled=false` 关闭默认告警升级；用户可通过 `ANOMALY_ALERTS_ENABLED=true` 或系统设置显式恢复 anomaly 通知。同时提高 EWMA 默认窗口、样本数和 sigma 阈值，让显式开启通知后的默认检测更保守。

**Consequences**: 默认 alert center 不再出现新 anomaly alert，外部通知也不会发送；节点异常事件列表仍可用于诊断。已有 DB/env override 继续优先生效，不做迁移覆盖。需要更新测试和配置说明，避免用户误解 `anomaly.enabled` 等于“发送通知”。

## Acceptance Criteria

* [x] 默认配置下，EWMA finding 会写入 `anomaly_events`，但不会创建 alert，也不会触发通知投递。
* [x] 设置 `anomaly.alerts_enabled=true` 后，EWMA finding 会按现有 dedup/dispatch 流程创建 alert。
* [x] `anomaly.ewma_sigma` / `anomaly.ewma_window_hours` / `anomaly.ewma_min_samples` 的 code defaults 调整为更保守的值。
* [x] 现有 API smoke 中显式 `PUT /settings {"anomaly.enabled":"true"}` 后 anomaly events endpoint 仍返回 200。
* [x] 后端单元测试覆盖新增默认行为和显式开启行为。
* [x] 相关设置说明、环境变量说明或前端文案不再暗示 anomaly 默认会通知。

## Definition of Done

* Tests added/updated where behavior changes.
* Backend lint/typecheck/test pass for touched packages.
* Frontend typecheck/test only在触及前端类型或 UI 时运行。
* Docs/notes updated if public configuration behavior changes.
* Rollback path clear: users can set `ANOMALY_ALERTS_ENABLED=true` to recover previous notification behavior.

## Out of Scope

* 不在本任务内重做 anomaly 算法。
* 不在本任务内增加 per-node/per-metric anomaly 通知策略。
* 不在本任务内实现复杂静默、学习期或动态绝对门槛。
* 不迁移已有 DB override；已有用户设置继续优先生效。

## Spec Update Review

Phase 3.3 review completed. This task introduced concrete runtime configuration behavior (`anomaly.alerts_enabled` and more conservative EWMA defaults), but did not establish a new reusable coding convention beyond existing settings-registry, GORM persistence, and documentation-sync rules already covered by `.trellis/spec/backend/*`. No `.trellis/spec/` update is needed.

## Technical Notes

* Likely backend files:
  * `backend/internal/settings/service.go`
  * `backend/internal/anomaly/types.go`
  * `backend/internal/anomaly/raise.go`
  * `backend/internal/anomaly/raise_test.go`
  * `backend/internal/anomaly/ewma_test.go`
  * `docs/env-vars.md`
* Possibly frontend docs/i18n:
  * `web/src/i18n/locales/zh.ts`
  * `web/src/i18n/locales/en.ts`
* `anomaly.NewRaiseFn` currently has only DB + raiser. Prefer passing an explicit alert-upgrade policy/setting reader into anomaly sink rather than hiding this in `cmd/server/main.go`, so tests can cover default and enabled paths close to persistence semantics.
* `NewRaiseFn` can preserve existing event persistence by treating disabled alert upgrade as `(0, false, nil)` before writing `AnomalyEvent`.
* Existing `RaisedAlert=false` is the desired persisted state for default anomaly events.
