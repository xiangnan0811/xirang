# 不可变备份 (Immutable Backup)

## Goal

让 restic 备份仓库支持 append-only（只追加不可修改/删除），防止勒索病毒或恶意操作篡改删除历史快照。是防勒索的最后一公里。

## Requirements

### 数据层
- **ResticConfig** 加 `append_only bool` 字段（存储在 Task.ExecutorConfig JSON）
- **无迁移**：ExecutorConfig 是 GORM text 字段，JSON 序列化自动扩展
- Task 创建/编辑 API 透传 `append_only`，默认 false

### 执行层
- **init 时**：若 `append_only=true` → `restic init --repository-version 2 -r <repo>`
- **备份时**：`restic snapshots` 后检查返回 JSON 中的 repository 版本 → 若要求 append-only 但仓库 version < 2 → emit log warning（不中断备份）
- **无需 restic 版本检测**：直接尝试 init，restic 自己会报错（旧版本不认识 `--repository-version` flag）

### 前端
- Task 编辑页：restic 类型专属的"不可变备份 (append-only)"开关
- 不需要独立的状态展示页面（仓库版本信息可通过 TaskRun 日志查看）

### API
- Task Create/Update handler 透传 `append_only` 字段
- Policy Clone 时保留 `append_only` 配置

## Acceptance Criteria

- [ ] 新建 Task + append_only=true → init 带 `--repository-version 2` → 仓库版本为 2
- [ ] 已有旧仓库 + 编辑 Task 加 append_only=true → 备份继续 + 日志 warn
- [ ] append_only=false → init 不加 `--repository-version 2` → 仓库版本为 1（默认）
- [ ] rclone/command/rsync 类型 Task 不显示 append-only 选项
- [ ] 现有 restic executor 测试全部 GREEN

## Definition of Done

- 单测覆盖：init cmd 构建 / 仓库版本解析 / 版本不匹配 warn
- `go test ./...` + `npm run check` 全绿
- 前端 i18n (zh/en) 更新

## Out of Scope

- S3 Object Lock / rclone 不可变后端（预留，另立 PR）
- 已有仓库迁移工具（restic 不支持，必须重建）
- locked_until Snapshot 字段
- 不可变状态仪表盘

## Decision (ADR-lite)

### D1: 配置位置 — Approach A (ResticConfig 字段)
**Decision**: ResticConfig 加 `append_only bool`，Task.ExecutorConfig JSON 中存储。
### D2: 已有仓库 — Approach B (告警但继续)
**Decision**: 版本不匹配时 warn log 不中断备份。

## Technical Notes

- ResticConfig: `restic_executor.go:18-21`
- init 流程: `restic_executor.go:68-81`
- 前端 Task 编辑: `web/src/components/task-*-dialog.tsx`
