# 快照异常变更检测

## 概述

每次 restic 备份完成后，息壤自动分析最新两个快照之间的文件变更，
通过统计基线和后缀模式匹配，检测异常行为（勒索软件加密、大范围误删等）。

## 工作原理

### 检测维度

| 维度 | 检测方法 | 严重度 | error_code |
|------|---------|--------|------------|
| 变更量异常 | 当前变更文件数 > 历史基线 + 3σ | warning | XR-SNAPSHOT-CHURN-{policyID} |
| 勒索后缀 | 变更文件中出现 .encrypted/.locked 等后缀 | critical | XR-SNAPSHOT-RANSOM-{policyID} |

### 基线建立

- 每次备份后的 diff 统计写入历史记录
- 首次 2 次备份仅收集基线数据，不触发检测
- 第 3 次起，与最近 10 次历史的移动平均 + 标准差比对

### 已知勒索后缀

`.encrypted` `.locked` `.crypt` `.ransom` `.xxx` `.zzz`
`.enc` `.lock` `.crypto` `.wannacry` `.pay` `.decrypt`

## 告警管理

### 启用/禁用告警

在「系统设置」中将 `anomaly.alerts_enabled` 设为 `true` 或 `false`。

### 查看检测结果

在「异常事件」页面可查看所有 snapshot_churn / ransomware_pattern 事件。

### 告警通知

异常事件产生后，若 `anomaly.alerts_enabled=true`，将自动通过已配置的通知渠道
（邮件/Webhook/Slack/Telegram/飞书/钉钉/企业微信）发送告警。

## 限制

- 仅支持 restic 类型备份任务
- 检测在备份完成后异步执行，不影响备份本身
- 首次部署后需 >= 3 次备份才能建立有效基线
