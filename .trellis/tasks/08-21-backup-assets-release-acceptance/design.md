# Design — 备份资产发布验收

## Shape

本 Child 的交付物是**绑定 v0.50.2 的验收记录**，不是新功能。执行分两层：

1. **仓库内（agent 可做）**：本 Child 协议模板、父任务 notes 口径、核对 Native AWS 仍不在支持矩阵、走查完成后据实填协议。
2. **生产机（仅 Alan）**：升级/核对镜像、inventory、ack、启用、浏览器与 API 走查、回滚。Agent 不登录该生产机，不代填未报告的结果。

Child 17 `research/acceptance-protocol.md` 保持历史 `not_executed`。权威协议改到本 Child `research/acceptance-protocol.md`。

## Binding

| Field | Authority |
|---|---|
| Git SHA | `9f059b0b3283825b41462c76ea42259a2d9ab9dc` |
| Release | https://github.com/xiangnan0811/xirang/releases/tag/v0.50.2 |
| Image tag | `docker.io/linnea7171/xirang:v0.50.2` |
| Image digest | walkthrough only |
| Provider mode | `core-only` |
| Acceptor | Alan |
| Recorder | weibo |

`main` 在 tag 之后的 `024d69cf` 只含 Child 17 台账，不含产品代码。走查制品是 **v0.50.2**，不是 bookkeeping commit。

## Evidence rules

- 勾选必须能追溯到现场：时间、操作者、观察到的 HTTP/UI 行为。
- GitHub Actions、Playwright fixture、独立复审不得勾 must-pass。
- 缺 digest 则 AC1 失败。
- Core-only 以外的 Provider 一律 `not_executed` 或 `excluded_this_ga`，不得写成通过。

## Native AWS

公开文档已声明不在支持矩阵（`docs/admin/backup-recovery.md`、`docs/admin/backup-assets-load.md`）。UI 仍有 Native 绑定对话框；那是 opt-in + 预检失败关闭，不是本次承诺。本 Child 只要求：

- 协议排除声明
- 文档未改口
- 走查不打开 Native 绑定

若走查中发现文档或 UI 把 Native 标成「已支持」，才允许最小文档修正 PR。不拆除 UI。

## SLO

`alerting.Dispatcher.BackupAssetSLORules()` 只在进程启动时提供 PromQL，不会写入 Prometheus / Alertmanager / Grafana。走查前 Alan 在该实例的告警栈粘贴 `docs/admin/backup-assets-slo.md` 规则，或在协议里记 `slo_rules_installed: false` 及豁免 owner。规则命名与窗口偏差（P2）不挡本次。

## Rollback

走查结束必须 disable `backup_assets.enabled`（该实例覆盖，不是改仓库 CodeDefault）。确认工作区关闭、旧读仍 410、未拉取官方 Worker 镜像。

## Parent archive

本 Child 通过 ≠ 自动归档父任务。归档是另一次、仅 Alan 可发的指令。

## Non-goals in design

不改 `publish-images.yml`、不改 CodeDefault、不跑 Native suite、不新增 Provider 验收场、不修 P2 代码。
