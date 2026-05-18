# Trust Ops 全链路审查

## Goal

对五个已完成的 Trust Ops 方向做一次独立、发布后的全链路审查，确认“可验证恢复 → 备份信心 → 节点诊断 → 事件解释 → demo/反馈”的产品链闭环真实可用，合同、安全边界、文档叙事和验证证据一致，再决定是否归档 umbrella roadmap。

## What I already know

* Trust Ops umbrella `.trellis/tasks/05-17-trust-ops-roadmap` 的五个子任务已完成并发布到 `v0.38.0`。
* 已完成方向包括 Verified Restore Drill Evidence、Backup Confidence Center、SSH Fleet Doctor、Health Incident Timeline、Trust Demo and Feedback Funnel。
* 每个子任务都经过各自 Trellis check、PR、CI、Release Please 和 Docker 发布验证。
* 尚未做过一次独立的全 Trust Ops 产品链审查。
* 当前审查不应重复实现五个功能，而应找出跨功能断层、合同漂移、安全/文档风险和需要后续任务化的问题。

## Requirements

* 审查五个方向是否形成闭环：drill evidence 能支持 backup confidence；confidence/doctor/task/log 信号能支撑 incident timeline；demo/docs 能真实展示这些能力。
* 审查前后端 API、domain type、mapper、UI props 和 mock/demo 数据是否保持 camelCase/snake_case 边界和状态 union 一致。
* 审查安全边界：restore drill sandbox、Doctor read-only allowlist、incident read-only aggregation、demo no-token mock-only、敏感字段不外泄。
* 审查用户可见叙事：README/docs/env/issue templates 不夸大用户规模、托管 demo、生产成熟度或真实基础设施连接。
* 审查验证证据：每个方向的测试、typecheck、docs freshness、release/Docker 状态是否足以支撑“已完成”结论。
* 输出明确结论：通过、需要小修、需要新任务，或阻塞 umbrella archive。

## Acceptance Criteria

* [ ] 审查报告覆盖五个 Trust Ops 方向及其跨功能依赖。
* [ ] 至少验证代码层合同一致性：backend response、frontend API mapper、domain types、UI/demo 使用路径。
* [ ] 至少验证安全边界：无任意 Doctor 命令、无 demo real API write path、无明显敏感字段展示、incident 不自动处置。
* [ ] 至少验证文档真实性：README、docs/env、issue templates 与当前能力一致。
* [ ] 若发现缺陷，按最小改动修复；若不是本轮可修，创建/记录明确后续 Trellis 任务建议。
* [ ] 完成后给出是否可以归档 `05-17-trust-ops-roadmap` 的建议。

## Definition of Done

* 审查过程读取相关 PRD/spec/code/tests/docs，而不是只依赖记忆。
* 必要修复完成并通过对应检查。
* 至少运行 `git diff --check`；若触碰 frontend，运行 `TMPDIR=$PWD/.tmp npm --prefix web run check`；若触碰 backend，运行 `go -C backend test ./...`。
* 若只产出审查/任务元数据，也需保持工作树 clean 后再 finish-work。
* Trellis task 归档并记录 journal；若建议归档 umbrella roadmap，按单独 finish/cleanup 流程处理。

## Out of Scope

* 不重新设计五个 Trust Ops 功能。
* 不新增大功能或进行产品方向扩展。
* 不自动归档 umbrella roadmap，除非审查结论明确允许且流程进入对应 finish 步骤。
* 不发布新的外部营销材料或托管 demo。

## Technical Notes

* Umbrella PRD: `.trellis/tasks/05-17-trust-ops-roadmap/prd.md`。
* 已归档子任务 PRD：
  * `.trellis/tasks/archive/2026-05/05-17-verified-restore-drill/prd.md`
  * `.trellis/tasks/archive/2026-05/05-17-backup-confidence-center/prd.md`
  * `.trellis/tasks/archive/2026-05/05-17-ssh-fleet-doctor/prd.md`
  * `.trellis/tasks/archive/2026-05/05-17-health-incident-timeline/prd.md`
  * `.trellis/tasks/archive/2026-05/05-17-trust-demo-feedback/prd.md`
* Relevant specs include `.trellis/spec/frontend/type-safety.md`, frontend a11y/component specs, backend API/security/testing specs, and documentation truth guide.
* Review should prefer reading current code and tests over relying on release memory.
