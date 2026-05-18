# Health Incident Timeline MVP

## Goal

把 alerts、node metrics、probe、TaskRun/logs 和 backup health 聚合成只读 incident-like timeline，帮助用户理解发生了什么、影响什么、下一步该做什么。

## Requirements

* 第一轮做 read-only aggregation，不引入完整 incident lifecycle。
* 聚合维度应覆盖 node、policy、task 等主要资源。
* Timeline 包含指标异常、probe 失败、任务失败、告警触发、通知失败、backup confidence 降级等事件。
* 每个事件组提供 severity、impacted resource、last seen、likely cause 和 next action links。
* 能链接到日志、Doctor、Drill、Policy 或 Backup Confidence 等相关页面。

## Acceptance Criteria

* [ ] 用户能看到按资源聚合的近期健康事件时间线。
* [ ] 任务失败、节点不可达、告警、备份健康降级能出现在同一上下文中。
* [ ] 每个事件组至少提供一个相关下一步入口。
* [ ] 第一轮不自动修复、不自动处置生产资源。
* [ ] 后端测试覆盖聚合排序、severity 和资源关联。

## Definition of Done

* Backend tests added/updated for incident aggregation.
* Frontend tests/checks added or updated for timeline UI.
* Code-spec updated for incident/timeline read model contracts if introduced.
* `go -C backend test ./...`, `npm --prefix web run check`, and `git diff --check` pass before completion.

## Out of Scope

* Ack/snooze/escalation lifecycle.
* Alert routing redesign.
* Automatic remediation.
* Persistent incident model unless selected during child-task planning.

## Technical Notes

* Existing foundations include Alert/AlertDelivery, alert grouping, node metrics handler, probe results, TaskRun/logs, backup health, notification center, and Overview data.
* Should consume Doctor and Confidence outputs where available, but can begin with existing signals.
