# RPO+RTO 目标 + GFS 保留策略

## Goal

Policy 声明 RPO/RTO 目标，Report 计算实际达成率并对比。Retention 支持 GFS（祖父-父-子）多级保留，替代单一 `retention_days`。

## Requirements

### 数据模型（迁移 000055）
- **Policy** 加：
  - `rpo_minutes` (int, nullable, 0=未设置)
  - `rto_minutes` (int, nullable, 0=未设置)
  - `retention_mode` (string, size:16, default:"simple")
  - `keep_daily` (int, default:0) — GFS mode only
  - `keep_weekly` (int, default:0)
  - `keep_monthly` (int, default:0)
  - `keep_yearly` (int, default:0)
- **Report** 加：
  - `actual_rpo_minutes` (int, nullable) — 实际 RPO（最近两次成功备份间隔）
  - `actual_rto_minutes` (int, nullable) — 实际 RTO（最近恢复的 duration_ms/60000）
  - `rpo_compliant` (bool) — 实际 ≤ 目标
  - `rto_compliant` (bool)

### Retention 执行
- **simple 模式**：继续用 `retention_days`，restic `--keep-within Nd`
- **GFS 模式**：忽略 `retention_days`，restic `--keep-daily N --keep-weekly N --keep-monthly N --keep-yearly N --prune`
- rsync/rclone 保留 simple 模式（GFS 对文件系统无意义）

### RPO/RTO 计算（报告生成时）
- RPO 实际值：该 Policy 关联 Task 的最近 N 次成功 TaskRun 中，相邻两次 `started_at` 的最大间隔（分钟）
- RTO 实际值：最近一次 restore TaskRun 的 `duration_ms / 60000`
- 对比目标：actual ≤ target → compliant=true

### API
- Policy CRUD 透传新字段
- Report 生成时计算 RPO/RTO actual + compliance

### 前端
- Policy 编辑页：RPO/RTO 输入 + retention_mode 切换 + GFS 字段
- 报告页：RPO/RTO 目标 vs 实际对比

### 兼容性
- 现有 Policy：`retention_mode="simple"`，行为完全不变

## Acceptance Criteria

- [ ] Policy 设 retention_mode=gfs + keep_daily=7 → restic forget --keep-daily 7 --prune
- [ ] Policy 设 rpo_minutes=60，报告显示实际 RPO（分钟）及是否达标
- [ ] retention_mode=simple → 行为与当前完全一致
- [ ] 现有所有测试 GREEN

## Out of Scope

- rsync/rclone GFS（不支持，restic 专属）
- 实时 RPO/RTO dashboard（仅在报告中计算）
- 自动调整 keep 参数（用户手工配置）

## Decision

### D1: retention 兼容 — Mode 选择 (C)
**Decision**: `retention_mode` 枚举 "simple"/"gfs"。simple 默认，完全向后兼容。
### D2: 范围 — GFS + RPO/RTO 一起做 (A)
**Decision**: 一次迁移 000055 包含所有字段。

## Implementation Plan

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | 模型+迁移+handler+RPO/RTO计算+restic GFS执行 | 3 天 |
| **PR2** | 前端 Policy 编辑页 + Report 页改动 + i18n + 文档 | 2 天 |

总计：**5 天**
