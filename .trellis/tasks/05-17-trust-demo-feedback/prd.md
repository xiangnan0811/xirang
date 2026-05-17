# Trust Demo and Feedback Funnel

## Goal

用 README、demo/mock 数据、引导和反馈入口承接“可信运维”主线，让新用户快速理解 Xirang 的备份可信度、恢复演练、节点诊断和事件解释能力，并能低摩擦反馈问题。

## Requirements

* README 首屏和关键文档突出真实的可信运维能力，不夸大用户规模或成熟度。
* Demo/mock 数据覆盖成功与失败故事：backup confidence、restore drill、SSH doctor、incident timeline。
* 反馈入口面向真实问题分类：部署、备份/恢复、SSH 诊断、功能建议。
* Demo 体验必须避免引导用户误以为连接了真实基础设施。
* 文档保持开源用户可执行、精简、当前真实。

## Acceptance Criteria

* [ ] README 能在短时间内说明 Xirang 的核心价值和可信运维主线。
* [ ] Demo/mock 数据能展示至少一个成功可信路径和一个失败可解释路径。
* [ ] 反馈模板或入口覆盖部署、备份恢复、SSH 诊断和功能建议。
* [ ] 文档不声称不存在的用户规模、托管服务或生产成熟度。
* [ ] 前端 demo 相关检查通过。

## Definition of Done

* Docs updated where relevant.
* Frontend mock/demo checks pass if demo code changes.
* `npm --prefix web run check`, docs freshness checks where applicable, and `git diff --check` pass before completion.

## Out of Scope

* Public hosted demo infrastructure.
* Telemetry collection.
* External analytics or third-party feedback services.
* Marketing claims not backed by current product behavior.

## Technical Notes

* Existing foundations include README/deployment/env docs, `VITE_ENABLE_DEMO_MODE`, `web/src/data/mock.ts`, setup wizard, onboarding fields, GitHub issue templates, PR template, and security policy.
