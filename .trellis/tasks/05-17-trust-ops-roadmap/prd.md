# 可信运维五方向并行推进

## Goal

把 Xirang 的下一阶段产品推进收敛到“可信运维”主线：让用户不仅能运行备份、任务、监控和告警，还能确认备份可恢复、节点问题可诊断、故障上下文可解释，并让开源新用户能快速理解、试用和反馈这些能力。

## What I already know

* 用户已同意按项目流程启动五个方向并行推进。
* 五个方向是 Verified Restore Drill、Backup Confidence Center、SSH Fleet Doctor、Health Incident View、README/Demo/Feedback Funnel。
* 前置代码调研显示五个方向都有现有基础，不是从零开始。
* 推荐产品主线是“可验证恢复 → 备份信心 → 问题诊断 → 事件解释 → 开源试用和反馈”。
* Verified Restore Drill 和 Backup Confidence Center 是 P0 架构中心。
* SSH Fleet Doctor 和 Health Incident View 是 P1，但可以并行推进。
* README/Demo/Feedback Funnel 是 P0.5，投入较小，应承接核心能力而不是单独做大项目。

## Assumptions (temporary)

* 这次任务的目标是形成可执行 PRD 与任务拆分，并按 Trellis 流程进入实施，不只是写方向分析。
* 第一轮应避免一次性做完五个方向的最终形态，而是定义可并行交付的 MVP。
* 安全边界优先：restore drill 不覆盖生产路径，SSH doctor 不执行用户输入的任意命令，incident view 先读聚合不自动处置。
* 开源文档保持真实克制，不夸大用户规模或生产成熟度。

## Decisions

* 交付形态采用 umbrella task + 5 个子任务并行。当前任务作为总控 PRD/架构约束，五个方向分别以独立 Trellis 子任务、独立 PRD、独立实施与独立 PR 推进。
* 子任务之间通过明确证据/API 合同协作，避免把五个方向堆进同一个巨大变更。

## Open Questions

* 当前无阻塞性开放问题；各子任务在自己的 PRD 中继续细化本方向的 MVP 边界。

## Requirements (evolving)

### Verified Restore Drill

* 将现有 drill 能力从“可触发执行”提升为“可解释、可追溯、可作为信心证据”。
* 记录或聚合 drill run 的关键证据：policy/task/snapshot、目标节点、沙箱路径、restore 时间、校验结果、清理结果、失败步骤。
* Drill 结果必须能被 Backup Confidence Center 消费。
* Drill 清理和恢复路径必须限制在明确安全边界内。

### Backup Confidence Center

* 聚合 backup health、TaskRun、drill、verifier/integrity、RPO/RTO、alert 等证据。
* 输出人能理解的 confidence 状态、原因和下一步建议。
* 避免只有“绿色/红色”的浅层面板；每个状态必须可解释。

### SSH Fleet Doctor

* 提供 allowlisted diagnostics，不支持任意 shell 输入。
* 诊断 SSH 连接、认证、known_hosts、sudo、备份目录、磁盘空间、基础工具和 probe 状态。
* 输出结果、证据和建议。
* 第一轮只诊断不自动修复。

### Health Incident View

* 将 alerts、node metrics、probe、TaskRun/logs、backup health 聚合为 incident-like timeline。
* 第一轮以 read-only 聚合为主，不引入复杂 incident lifecycle。
* 每个事件组应能链接到可执行下一步，如查看日志、运行 doctor、触发 drill、查看 policy。

### README/Demo/Feedback Funnel

* README 和 demo 叙事承接“可信运维”主线。
* Demo/mock 数据覆盖成功与失败场景，包括 backup confidence、restore drill、SSH doctor、incident timeline。
* 反馈入口面向真实用户问题：部署、备份恢复、SSH 诊断、功能建议。

## Acceptance Criteria (evolving)

* [ ] PRD 明确五个方向的 MVP 范围、依赖关系、非目标和验收标准。
* [ ] 任务拆分支持五个方向并行推进，且不会互相阻塞在同一巨大变更里。
* [ ] Verified Restore Drill 的输出能够成为 Backup Confidence Center 的输入。
* [ ] SSH Fleet Doctor 的 MVP 不允许任意命令执行。
* [ ] Health Incident View 第一轮不自动修复、不自动处置生产资源。
* [ ] README/Demo/Feedback Funnel 不夸大项目用户规模或成熟度。
* [ ] 后续实施必须补齐后端测试、前端检查、文档同步和必要的 code-spec 更新。

## Definition of Done (team quality bar)

* Tests added/updated for new backend models/services/handlers and frontend behaviors.
* Backend `go test ./...` passes from `backend/` or with `go -C backend test ./...`.
* Frontend `npm run check` passes from `web/` or with `npm --prefix web run check`.
* `git diff --check` passes.
* Docs updated where user-facing behavior changes.
* Code-spec updated for new API/data contracts, diagnostic contracts, restore drill evidence, or incident aggregation contracts.
* Trellis task(s) archived and journaled after implementation, then PR/CI/merge/post-merge flow proceeds per project workflow.

## Out of Scope (explicit)

* Arbitrary remote command execution in SSH Fleet Doctor.
* Automatic remediation/fix actions for node or incident problems in the first MVP.
* Production-path destructive restore or overwrite behavior.
* Full incident lifecycle with ack/snooze/escalation in the first MVP unless explicitly chosen later.
* Public hosted demo infrastructure unless explicitly chosen later.
* Claims of large production adoption or user scale in docs.

## Subtasks

* `.trellis/tasks/05-17-verified-restore-drill` — Verified Restore Drill Evidence.
* `.trellis/tasks/05-17-backup-confidence-center` — Backup Confidence Center MVP.
* `.trellis/tasks/05-17-ssh-fleet-doctor` — SSH Fleet Doctor MVP.
* `.trellis/tasks/05-17-health-incident-timeline` — Health Incident Timeline MVP.
* `.trellis/tasks/05-17-trust-demo-feedback` — Trust Demo and Feedback Funnel.

## Technical Notes

* Backup Confidence foundations: `backend/internal/api/handlers/overview_backup_health_handler.go`, `backend/internal/task/runner.go`, `backend/internal/task/drill.go`, `backend/internal/task/verifier/verifier.go`, `backend/internal/task/integrity_checker.go`, `backend/internal/reporting/generator.go`, `web/src/components/backup-health-panel.tsx`.
* Verified Restore Drill foundations: policy drill fields in models/migrations, `backend/internal/task/drill.go`, `backend/internal/task/manager.go`, snapshot handlers, policy editor, task run detail/history UI.
* SSH Fleet Doctor foundations: Node/SSHKey models, `backend/internal/sshutil/`, `backend/internal/probe/`, task executors, sudo helper, node detail page.
* Health Incident foundations: Alert/AlertDelivery, alert grouping, node metrics handlers, probe results, TaskRun/logs, notification center.
* README/Demo/Feedback foundations: README/deployment docs, `VITE_ENABLE_DEMO_MODE`, mock data, setup wizard, onboarding fields, GitHub issue templates.

## Expansion Sweep

### Future evolution

* Confidence can evolve into historical trends, report exports, and release/readiness gates.
* Doctor can later support safe guided fixes, but only after diagnostic contracts and audit behavior are stable.

### Related scenarios

* Drill failures should surface in Confidence Center and Incident View.
* SSH diagnostics should be reusable from node detail, task failure detail, and incident next actions.

### Failure and edge cases

* Offline nodes, missing tools, sudo failure, known_hosts mismatch, missing backup paths, stale snapshots, and cleanup failure must be represented as explainable states.
* Security-sensitive values such as private keys, passwords, endpoints, and proxy URLs must not be exposed in diagnostics, logs, demo fixtures, or API responses.
