# 备份资产发布验收

## Goal

在已发布的 **v0.50.2**（`9f059b0b` / `docker.io/linnea7171/xirang:v0.50.2`）上，由 Alan 在**现网 Core-only 生产机**完成真实走查并填完验收协议。通过后，父任务才具备归档条件。本 Child 不是代码修复任务。

## User value

运维能在默认关闭、Admin 可控的前提下，按**本次实际走查过的形态**试点备份资产浏览器。上线承诺不超出 Core-only 走查；Rclone Native AWS 不进入承诺；未走查的 Restic / Rsync / Rclone Portable 保持「代码支持、本次未验收」。

## Background

Children 1–17 已归档。Child 17（PR #446 / #448）关掉代码级 P0/P1，并把 Child 17 协议留在 `not_executed`。正式公开发布是 v0.50.2。

2026-08-21 独立 GitHub 复审（`research/github-review-2026-08-21.md`）结论：代码 P0/P1 已关；默认关闭受控试点 Go；Native AWS 排除后其他已声明 Provider 为有条件 Go；父任务归档等真实走查。该复审不是生产走查。

Alan 现网：一台 Admin、生产、Core-only、已运行 3+ 个月。启用需要 dry-run inventory + Admin ack + 进程重启（若产品仍要求）。

仓库已有事实：官方 `.env.deploy` 与 CodeDefault 均为 `BACKUP_ASSETS_ENABLED=false`。`docs/admin/backup-recovery.md` 已写 AWS Native 不在本版本支持矩阵。UI 仍提供 Native 绑定入口，运行时预检失败关闭；本 Child 不借验收之机改掉该入口。

## Locked decisions

1. 制品：GitHub Release [v0.50.2](https://github.com/xiangnan0811/xirang/releases/tag/v0.50.2)，commit `9f059b0b3283825b41462c76ea42259a2d9ab9dc`，镜像 `docker.io/linnea7171/xirang:v0.50.2`。digest 只能从已发布镜像或走查现场抄入。
2. **走查环境 = Alan 现网 Core-only 生产机。** 不另搭 Restic / Rsync / Rclone Portable 验收场。
3. Restic / Rsync / Rclone Portable：代码支持矩阵可保留；本 Child 协议记 `not_executed`，不挡本 Child 通过，也不把它们写成「已在 v0.50.2 生产验收」。
4. Rclone Native AWS：**本次 GA 正式排除**。协议写排除声明。不得用 mock S3 替代。
5. CodeDefault 与官方 `.env.deploy` 保持 `false`。不公开发 Worker，不改 `publish-images.yml`。
6. 父任务保持 `planning`，直到本 Child 协议通过且 Alan 另发归档指令。
7. 不把 CI、fixture、静态阅读或 GitHub 复审勾成生产走查通过。
8. 复审三条 P2 为非阻断维护项，不进本 Child 通过门。
9. 审查与验收记录只留在本 Child。
10. 启用前应在部署环境安装 `docs/admin/backup-assets-slo.md` 中的 PromQL（应用不会自动写入 Alertmanager）。未安装必须记入豁免，不得假装已告警。
11. 「至少一个完整备份周期」是受控试点观察项：能记则记；未到周期记 `not_elapsed`，不单独阻断本 Child 的启用/回滚演练通过。

## Requirements

### R1 — 绑定发布制品

本 Child `research/acceptance-protocol.md` 必须写清 git SHA、GitHub Release、镜像 tag、镜像 digest、DB 引擎、Provider 模式、浏览器、验收人、记录人。SHA / Release / tag 已由 v0.50.2 确定；digest 与环境字段只来自现场。

### R2 — Core-only 真实走查

必须在指定制品上，由 Alan 完成：启用前默认关闭、dry-run inventory、Admin ack、进程重启（若仍需要）、FeatureLive 后 Catalog / Search / Content 一致、旧 snapshot 读 410、旧 restore 在未 live 或非 Admin 时失败、Admin-only secret reveal、预览续期不重复 TOTP、就地恢复确认、disable 回滚、Worker 未作为官方镜像拉取。Agent 不得代替 SSH 进生产机，也不得代填未报告的勾选。

### R3 — Native AWS 排除声明

验收协议必须含本次范围排除声明。核对 `docs/admin/backup-recovery.md` 与 `docs/admin/backup-assets-load.md` 仍写不在支持矩阵。发现文档改口成「已支持」才允许改文档；不把 Native UI 入口拆除列入本 Child。

### R4 — 未走查 Provider 诚实

协议明确：本走查 Provider 模式 = `core-only`。Restic / Rsync / Rclone Portable = `declared_supported_not_executed`。Native AWS = `excluded_this_ga`。

### R5 — 失败与豁免诚实

失败项写入协议，带 owner。未执行不得标通过。

### R6 — 父任务归档门

仅当本协议 `production_walkthrough` 不再是 `not_executed`，且 R3/R4 声明已落盘，才允许**请求**父任务归档。真正归档仍要 Alan 明确指令。

### R7 — 默认关闭合同不变

走查全程不得把 CodeDefault 或官方 `.env.deploy` 改成 true。试点启用只发生在该生产实例的设置/环境覆盖，并在回滚演练中关掉。

## Acceptance Criteria

- [ ] AC1：协议绑定 v0.50.2 SHA、Release、镜像 tag 和现场抄录的 digest。
- [ ] AC2：must-pass 与回滚按 Alan 现场勾选；未跑保持未勾选并说明。
- [ ] AC3：Native AWS 在协议中为 `excluded_this_ga`；公开文档仍不把它写成已支持。
- [ ] AC4：Provider 模式为 `core-only`；Restic / Rsync / Portable 为 `declared_supported_not_executed`。
- [ ] AC5：CodeDefault 与 `.env.deploy` 仍为 false；Worker 仍未公开发。
- [ ] AC6：父任务在本 Child 通过前保持 `planning`。
- [ ] AC7：SLO PromQL 已在该实例安装，或豁免写明未安装及 owner。

## Out of scope

- 翻转 CodeDefault 或官方 deploy env。
- 公开发 Worker。
- 把 Native AWS 纳入本次 GA，或为它跑 mock/live suite。
- 为 Restic / Rsync / Rclone Portable 另建验收环境。
- 代码修复（P0/P1 已在 Child 17 关闭）。
- 把三条 P2 当成通过门。
- 拆除 Native 绑定 UI。
- 百万级目录 soak / 多小时压测。
- 伪造生产证据。
- 在本 Child 通过前，或未经 Alan 指令，归档父任务。
