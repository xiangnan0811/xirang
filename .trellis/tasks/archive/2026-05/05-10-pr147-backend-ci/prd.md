# 修复 PR 后端 CI 失败

## Goal

修复 PR #147 的 Backend Test & Build 失败，使 Trellis 更新 PR 可以通过 required checks 并安全合并。

## Requirements

* 定位 `TestTriggerDrillSuccessReturnsRunID` 在 CI 中初始状态从 `pending` 变为 `failed` 的原因。
* 在当前 PR 分支上做最小修复，不扩大 Trellis 更新范围以外的业务行为。
* 确认 push-run 上的 govulncheck 失败是否与本 PR 相关；若是本 PR 引入则修复，若是基线/事件差异则记录依据。
* 修复后运行相关后端测试，并推送新提交触发 CI。

## Acceptance Criteria

* [ ] `go test` 相关包/测试在本地通过。
* [ ] PR #147 的 required CI checks 通过。
* [ ] 未引入无关业务改动。

## Definition of Done

* 修复提交已推送到 PR 分支。
* CI 已重新监控到通过或明确记录外部阻塞。

## Technical Notes

* PR 后端 pull_request run 失败：`backend/internal/task` 的 `TestTriggerDrillSuccessReturnsRunID`，`drill_test.go:524` 期望初始 status=pending，实际 failed。
* push run 另有 govulncheck 报告，需要确认是否 required 或与本 PR 有关。
