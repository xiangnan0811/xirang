# Verified Restore Drill Evidence

## Goal

把现有恢复演练能力从“可触发执行”提升为可解释、可追溯、可被 Backup Confidence Center 消费的恢复可信证据。

## Requirements

* 记录或聚合每次 drill 的关键证据：policy/task/snapshot、目标节点、沙箱路径、restore 起止时间、校验结果、清理结果、失败步骤。
* Drill 结果必须能被后续 Backup Confidence Center 作为 evidence 消费。
* Drill 历史必须能在用户界面中查看，并能解释最近一次成功或失败的原因。
* Drill 清理与恢复路径必须限制在明确 sandbox 安全边界内。
* 不做生产路径覆盖恢复，不做自动修复。

## Acceptance Criteria

* [ ] 用户能看到 policy 最近一次 restore drill 的状态、时间、耗时和失败步骤。
* [ ] 后端能提供结构化 drill evidence，包含恢复、校验、清理阶段结果。
* [ ] 失败 drill 不会被误判为 backup confidence 的正向证据。
* [ ] 清理逻辑无法删除 sandbox 外路径。
* [ ] 相关后端测试覆盖成功、失败和 cleanup 边界。

## Definition of Done

* Backend tests added/updated.
* Frontend UI/tests added or updated where applicable.
* Code-spec updated for drill evidence/API contracts if new contracts are introduced.
* `go -C backend test ./...`, `npm --prefix web run check`, and `git diff --check` pass before completion.

## Out of Scope

* Full disaster recovery workflow.
* Production restore overwrite.
* Auto-remediation.
* Backup Confidence scoring implementation, except for exposing evidence needed by that task.

## Technical Notes

* Existing foundations include policy drill fields/migrations, `backend/internal/task/drill.go`, `backend/internal/task/manager.go`, snapshot handlers, verifier/integrity checker, policy editor, task run detail/history UI.
