# GFS 保留策略与 RPO/RTO 目标

## 概述

息壤支持两种备份保留模式，并允许为每个备份策略设置 RPO/RTO 目标，
在 SLA 报告中对比目标与实际达成率。

## 保留模式

### 简单模式 (Simple)

默认模式，完全向后兼容。按 `retention_days`（保留天数）清理过期快照。

- restic: `restic forget --keep-within <N>d`
- rsync/rclone: 按 `retention_days` 清理旧备份目录

### GFS 模式 (Grandfather-Father-Son)

多级保留策略，按日/周/月/年四个层级保留快照。四个参数独立控制：

| 参数 | 说明 | 默认值 | 范围 |
|------|------|--------|------|
| keep_daily | 保留最近 N 个日快照 | 0 | 0-365 |
| keep_weekly | 保留最近 N 个周快照 | 0 | 0-104 |
| keep_monthly | 保留最近 N 个月快照 | 0 | 0-120 |
| keep_yearly | 保留最近 N 个年快照 | 0 | 0-30 |

restic 执行命令：
```
restic forget --keep-daily <N> --keep-weekly <N> --keep-monthly <N> --keep-yearly <N> --prune
```

**注意：GFS 模式仅适用于 restic 执行器。** rsync 和 rclone 任务将忽略 GFS
设置，按简单模式处理（文件系统级保留无法实现 GFS 语义）。

## RPO/RTO 目标

### 配置

在策略编辑器的"保留策略与 SLA 目标"折叠面板中设置：

- **RPO 目标 (分钟)**：预期恢复点目标。0 表示不设目标。
- **RTO 目标 (分钟)**：预期恢复时间目标。0 表示不设目标。

### 计算方式

报告生成时自动计算：

- **实际 RPO**：该策略关联任务的最近 N 次成功执行中，相邻两次 `started_at`
  的最大间隔（分钟）
- **实际 RTO**：最近一次恢复 (`trigger_type=restore`) 任务执行的
  `duration_ms / 60000`
- **达标判断**：`actual <= target` 则 `compliant = true`

### 查看结果

在 "SLA 报告" 页面查看历史报告，报告详情包含：

- `actual_rpo_minutes` / `actual_rto_minutes`：实际值
- `rpo_compliant` / `rto_compliant`：是否达标

## 兼容性

- 现有策略默认为 `retention_mode="simple"`，`retention_days=7`，行为完全不变
- 升级到包含 GFS 支持的版本后，无需手动迁移现有策略
- 切换到 GFS 模式后，`retention_days` 字段将被忽略，仅使用 GFS 参数
- 切换回简单模式后，GFS 参数被忽略，仅使用 `retention_days`
