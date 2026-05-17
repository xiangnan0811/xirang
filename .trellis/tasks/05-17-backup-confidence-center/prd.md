# Backup Confidence Center MVP

## Goal

把备份运行、恢复演练、完整性校验、RPO/RTO、告警等分散信号聚合成用户能理解的备份可信度视图。

## Requirements

* 按 policy 或 node 聚合 backup health、TaskRun、drill evidence、verifier/integrity、RPO/RTO、alert 等证据。
* 输出 confidence 状态、原因、证据列表和下一步建议。
* Confidence 状态必须可解释，不能只有绿/黄/红。
* Drill evidence 是关键输入；没有恢复验证时应明确标记为信心不足而不是健康。
* 前端提供清晰入口展示备份可信度。

## Acceptance Criteria

* [ ] 用户能看到每个关键备份对象的 confidence 状态和原因。
* [ ] 最近备份失败、RPO 超限、缺少 restore drill、drill 失败等情况能影响 confidence。
* [ ] 每个非健康状态都提供至少一个可执行下一步。
* [ ] Confidence API 不暴露敏感字段。
* [ ] 后端测试覆盖 scoring/reason 组合。

## Definition of Done

* Backend tests added/updated for confidence aggregation.
* Frontend checks/tests added or updated for confidence UI.
* Code-spec updated for confidence status/evidence contracts if introduced.
* `go -C backend test ./...`, `npm --prefix web run check`, and `git diff --check` pass before completion.

## Out of Scope

* Long-term confidence history and trend charts unless needed for MVP.
* Report export.
* Incident lifecycle.
* Implementing drill execution itself beyond consuming its evidence.

## Technical Notes

* Existing foundations include `overview_backup_health_handler.go`, task runner, drill/verifier/integrity checker, reporting generator, `backup-health-panel.tsx`, TaskRun and Policy model evidence.
* Depends on Verified Restore Drill evidence for strongest confidence signal, but can start with existing task/drill/read models.
