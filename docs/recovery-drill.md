# 自动恢复演练 (Recovery Drill)

## 概述

自动恢复演练定期将最新备份快照恢复到隔离沙箱节点，执行用户自定义校验脚本，
验证"备份真的能用"。记录真实 RTO（Recovery Time Objective），失败时触发告警。

## 为什么需要恢复演练？

> "You don't have a backup until you've tested the restore."

备份文件的存在不等于备份可用。恢复演练通过定期自动化验证，确保：
- 备份数据完整可恢复
- 恢复流程符合预期 RTO
- 恢复后的数据能通过业务校验

## 快速开始

### 1. 准备沙箱节点

在息壤中注册一台空闲或测试服务器作为沙箱节点，确保：
- SSH 连接正常
- 磁盘空间足够容纳备份数据
- 沙箱节点不等于备份源节点（防止覆盖生产数据）

### 2. 启用恢复演练

编辑备份策略，展开"恢复演练"配置区：
1. 打开"启用自动恢复演练"
2. 设置演练调度 cron（如 `0 3 * * 0` 每周日凌晨 3 点）
3. 选择沙箱节点
4. 设置恢复路径（默认 `/tmp/xirang-drill`）
5. 填写校验脚本

### 3. 编写校验脚本

三段式脚本，通过 SSH 在沙箱节点执行：

| 阶段 | 说明 | 示例 |
|------|------|------|
| Pre-verify | 环境准备，如启动恢复后的数据库 | `systemctl start mysql` |
| Verify | 实际校验，exit 0=成功 | `mysql -e "SELECT 1"` |
| Post-verify | 清理，无论成败都执行 | `systemctl stop mysql` |

### 4. 验证

点击"手动触发演练"立即执行一次，检查 TaskRun 结果：
- 状态为 `completed` = 成功
- RTO 记录在 `duration_ms` 中
- 失败时检查告警通知

## 告警

| error_code | 严重度 | 说明 |
|---|---|---|
| drill_sandbox_unreachable | warning | 沙箱节点离线 |
| drill_verify_failed | critical | 校验脚本失败 |
| drill_restore_failed | critical | 恢复过程失败 |

## 安全措施

- 沙箱节点不能是备份源节点（保存时校验）
- 恢复路径必须是绝对路径，禁止系统目录
- 演练后自动清理恢复文件（可关闭）

## 与完整性检查的区别

| | 完整性检查 | 恢复演练 |
|---|---|---|
| 验证内容 | 备份仓库结构 | 备份数据可恢复 + 可被业务使用 |
| 执行方式 | restic/rclone check | 实际恢复 + 自定义校验脚本 |
| 执行位置 | 备份源节点 | 沙箱节点 |
| RTO 记录 | 否 | 是 |

## API 接口

### 手动触发演练

```
POST /api/v1/policies/:id/drill-trigger
```

立即对指定策略执行一次恢复演练，不等 cron 调度。返回 `task_run_id` 和确认消息。

请求需要 JWT 认证，且当前用户需有该策略的 ownership 权限。

## 配置字段

| 字段 | 类型 | 说明 |
|------|------|------|
| drill_enabled | bool | 是否启用自动恢复演练（默认 false） |
| drill_cron | string | cron 表达式调度（如 `0 3 * * 0`） |
| drill_target_node_id | uint | 沙箱节点 ID（不能是备份源节点） |
| drill_restore_path | string | 沙箱上的恢复目标路径（默认 `/tmp/xirang-drill`） |
| drill_pre_verify | text | 环境准备脚本 |
| drill_verify | text | 校验脚本（exit 0 = 成功） |
| drill_post_verify | text | 清理脚本（无论成败都执行） |
| drill_auto_cleanup | bool | 是否自动清理恢复文件（默认 true） |
