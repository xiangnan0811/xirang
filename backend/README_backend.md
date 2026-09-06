# Xirang 后端

## 概述

基于 Go + Gin + GORM 的后端服务，提供完整的服务器运维管理 API。

主要能力：
- 多引擎备份（Rsync / Restic / Rclone）+ 命令执行
- 节点管理与健康探测（SSH 连接、资源采样）
- 任务调度与依赖编排（cron、链式执行、暂停/跳过）
- 多渠道通知（邮件 / Webhook / Slack / Telegram / 飞书 / 钉钉 / 企业微信）
- RBAC 权限控制 + TOTP 两步验证 + 审计日志
- SLA 报告、配置导入导出、系统自助备份

## 备份资产运行时

`backend/internal/backupasset/runtime` 是备份资产的唯一组合根：它创建一套 Provider transport、admission barrier、Repository service、Restic/Rsync/Rclone publication strategy、manifest/reconciliation worker、Rclone health worker 和 lineage guard。`cmd/server` 在 Task Manager 与 executor factory 之前创建该运行时；同一套 admission 与精确血缘合同同时保护 legacy 读取、受管发布和后台调和。Router 只接收已注入的 runtime/窄端口，不会自行创建第二个 Provider 图。

`backup_assets.enabled` 默认仍为 `false`。请求启用必须先通过 GA 就绪门禁（库存盘点、导出根、密钥域；已有安装还须确认当前库存摘要）。启用后，Catalog API 只公开经过 producing-lineage 授权的已提交元数据；每个资产引用必须同时携带恢复点 ID 与 entry ID，不接受 Provider 路径。已经原子提交的 immutable Catalog 在 Provider 离线时仍可浏览，但内容可用性会独立返回 `false`；Catalog 不提供内容字节，也不是恢复源。Rclone 版本化 setup、binding、preflight、activation 与 rollback 继续通过 Task 子资源提供，全部要求认证、Admin、`backup_repositories:manage` 和 Task ownership。

增强处理复用同一 runtime、Child 10 持久队列/Input-Sink grant、Content Broker 与 Derived Store。全局 feature、本机/远程 Worker transport、独立 updater 和可选秘密分类均默认关闭；生产 capability 只接受编译期闭合的 capability/profile/limit，不接受调用方 executable、argv、路径、URL 或工具配置。公开响应只返回 exact AssetRef、用户作用域的 processing-interest handle、闭合状态/原因、覆盖和 fallback，不返回共享 coordinator job、attempt/fence/grant/Worker/blob 身份、Provider locator、bundle 路径、凭据或原始工具输出。无 Worker 或无匹配 capability 时返回 `not_deployed`/`unsupported`，不会把 Catalog、原生预览、下载或 recovery 变成失败。

非 GA 的本地 `asset-worker` Compose profile 使用两个独立 socket volume：Core 同时挂载 `asset-worker-updater-runtime` 与嵌套的 `asset-worker-worker-runtime`，parser 只读挂载后者且不加入 updater GID，updater 只读挂载前者；双方都看不到对方的 socket 或 secret。Worker 没有稳定公共镜像，也不会由本功能发布到 Docker Hub/GitHub Release；普通 Core-only Compose 与 `10761` 端口不变。

## 快速运行

```bash
cd backend
go mod tidy
# 未声明 APP_ENV 时按生产硬化：缺 JWT_SECRET / DATA_ENCRYPTION_KEY / METRICS_TOKEN 会拒绝启动。
# 本地开发请显式设置 development，并在首次启动（库中尚无 admin）时提供初始密码。
# 密码须同时含大写/小写字母、数字与特殊字符（见 auth.ValidatePasswordStrength）：
ADMIN_INITIAL_PASSWORD='LocalDev#2026' APP_ENV=development \
  go run ./cmd/server
```

默认监听：`127.0.0.1:8080`

## API 接口

所有接口前缀 `/api/v1`，需 JWT 认证的接口标注 🔒。

### 认证与用户

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /auth/captcha | 获取登录验证码 |
| POST | /auth/login | 用户登录 |
| POST | /auth/2fa/login | TOTP 二次验证登录 |
| GET | /me | 🔒 当前用户信息 |
| POST | /me/onboarded | 🔒 完成新手引导 |
| POST | /auth/logout | 🔒 注销 |
| POST | /auth/change-password | 🔒 修改密码 |
| POST | /auth/2fa/setup | 🔒 配置 TOTP |
| POST | /auth/2fa/verify | 🔒 验证 TOTP |
| POST | /auth/2fa/disable | 🔒 关闭 TOTP |
| POST | /auth/step-up | 🔒 高风险操作二次验证 |
| GET | /users | 🔒 用户列表 |
| POST | /users | 🔒 创建用户 |
| PUT | /users/:id | 🔒 更新用户 |
| DELETE | /users/:id | 🔒 删除用户 |

> 登录验证码（`/auth/captcha`）启停由系统设置 `login.captcha_enabled` / `login.second_captcha_enabled` 控制，可通过 `/settings` 接口实时调整，无需重启进程。环境变量 `LOGIN_CAPTCHA_ENABLED` / `LOGIN_SECOND_CAPTCHA_ENABLED` 仅作为首次启动时的回退默认值。

### 概览与监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /overview | 🔒 仪表盘概览（`tasks:read`；operator 仅统计自己拥有的节点） |
| GET | /overview/traffic | 🔒 任务流量趋势 |
| GET | /overview/backup-health | 🔒 备份健康状态 |
| GET | /overview/backup-confidence | 🔒 备份可信度 |
| GET | /overview/health-incident-timeline | 🔒 健康事件时间线 |
| GET | /overview/storage-usage | 🔒 存储使用统计 |

### 节点管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /nodes | 🔒 节点列表 |
| GET | /nodes/:id | 🔒 节点详情 |
| POST | /nodes | 🔒 创建节点 |
| POST | /nodes/batch-delete | 🔒 批量删除 |
| PUT | /nodes/:id | 🔒 更新节点 |
| DELETE | /nodes/:id | 🔒 删除节点 |
| POST | /nodes/:id/test-connection | 🔒 测试连接 |
| POST | /nodes/:id/doctor | 🔒 SSH Fleet Doctor 诊断 |
| GET | /nodes/:id/metrics | 🔒 资源指标 |
| GET | /nodes/:id/status | 🔒 节点状态快照（最新采样 + 1h/24h 聚合 + 告警/任务计数） |
| GET | /nodes/:id/metric-series | 🔒 按时间窗返回多指标序列（granularity=auto/raw/hourly/daily）|
| GET | /nodes/:id/disk-forecast | 🔒 磁盘用量线性回归预测（days_to_full + confidence 分层） |
| GET | /nodes/:id/files | 🔒 远程文件列表 |
| GET | /nodes/:id/files/content | 🔒 文件内容 |
| GET | /nodes/:id/docker-volumes | 🔒 Docker 卷列表 |
| GET | /nodes/:id/owners | 🔒 节点 owner 列表 |
| POST | /nodes/:id/owners | 🔒 添加 owner |
| DELETE | /nodes/:id/owners/:user_id | 🔒 移除 owner |
| POST | /nodes/:id/emergency-backup | 🔒 紧急备份 |
| POST | /nodes/:id/migrate | 🔒 节点迁移 |
| POST | /nodes/:id/migrate/preflight | 🔒 迁移预检 |
| GET | /nodes/:id/log-config | 🔒 获取节点日志采集配置 |
| PATCH | /nodes/:id/log-config | 🔒 更新节点日志采集配置 |

### SSH 密钥

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /ssh-keys | 🔒 密钥列表（含派生公钥） |
| POST | /ssh-keys | 🔒 创建密钥 |
| POST | /ssh-keys/batch | 🔒 批量创建（最多 50 条） |
| POST | /ssh-keys/batch-delete | 🔒 批量删除（跳过使用中） |
| GET | /ssh-keys/export | 🔒 导出（authorized_keys/json/csv，需二次验证） |
| GET | /ssh-keys/:id | 🔒 密钥详情 |
| PUT | /ssh-keys/:id | 🔒 更新密钥 |
| DELETE | /ssh-keys/:id | 🔒 删除密钥 |
| POST | /ssh-keys/:id/test-connection | 🔒 测试密钥连接节点 |

### 备份策略

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /policies | 🔒 策略列表 |
| GET | /policies/:id | 🔒 策略详情 |
| POST | /policies | 🔒 创建策略 |
| POST | /policies/batch-toggle | 🔒 批量启停 |
| POST | /policies/from-template/:id | 🔒 从模板创建 |
| PUT | /policies/:id | 🔒 更新策略 |
| DELETE | /policies/:id | 🔒 删除策略 |
| POST | /policies/:id/drill-trigger | 🔒 手动触发恢复演练 |

### 备份 Repository（默认关闭）

以下只读接入能力受 `backup_assets.enabled` 控制，默认关闭；接入与管理仅限 Admin，Operator 仅可读取其当前 Task/Node 谱系可见的数据，Viewer 无权限。

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | /backup-repositories/connect | 🔒 从现有 Task 派生并探测 Repository（`backup_repositories:manage`） |
| GET | /backup-repositories | 🔒 列出谱系可见 Repository（`backup_assets:list`） |
| GET | /backup-repositories/:id | 🔒 查看脱敏 Repository 详情（`backup_assets:list`） |
| GET | /backup-repositories/:id/recovery-points | 🔒 列出谱系授权后的恢复点与 Catalog 状态（`backup_assets:list`） |
| POST | /backup-repositories/:id/reconcile | 🔒 执行有界只读重新探测（`backup_repositories:manage`） |
| POST | /backup-repositories/:id/disconnect | 🔒 撤销访问但保留 Repository、恢复点及 Provider 数据（`backup_repositories:manage`） |
| GET | /backup-file-sources/nodes | 🔒 列出当前谱系授权的脱敏文件源节点（`backup_assets:list`） |
| GET | /backup-file-sources/nodes/:nodeId/sets | 🔒 列出节点下由服务端投影的脱敏 Backup Set（`backup_assets:list`） |
| GET | /backup-file-sources/sets/:backupSetId/versions | 🔒 列出 Backup Set 的脱敏版本摘要（`backup_assets:list`） |
| GET | /backup-file-sources/recovery-points/:recoveryPointId/source | 🔒 将已授权恢复点精确解析为脱敏节点、Backup Set、仓库和任务坐标（`backup_assets:list`，不访问 Provider） |
| GET | /recovery-points/:id | 🔒 查看脱敏恢复点详情（`backup_assets:list`） |
| GET | /recovery-points/:id/catalog-status | 🔒 独立查看 generation、coverage、staleness 与内容可用性（`backup_assets:list`） |
| GET | /recovery-points/:id/evidence | 🔒 查看分层且不提升信任结论的精确证据（`backup_assets:list`） |
| GET | /recovery-points/:id/entries | 🔒 使用 opaque parent/cursor 浏览 active Catalog（`backup_assets:list`） |
| GET | /recovery-points/:id/entries/:entryId | 🔒 使用恢复点与 entry 复合身份查看条目（`backup_assets:list`） |
| POST | /recovery-points/:id/entries/:entryId/delivery-tickets | 🔒 为 exact AssetRef 签发原生/派生 Content Broker ticket（`backup_assets:preview`） |
| POST | /recovery-points/:id/entries/:entryId/preview-jobs | 🔒 创建闭合 representation 的增强预览 interest（`backup_assets:preview`；queued 为 `202 + Location`） |
| GET | /recovery-points/:id/entries/:entryId/preview-jobs/:jobId | 🔒 按当前用户与 exact AssetRef 查询一次性处理结果（`backup_assets:preview`） |
| POST | /recovery-points/:id/entries/:entryId/preview-jobs/:jobId/cancel | 🔒 只取消当前用户的 interest，不越权取消共享 work（`backup_assets:preview`） |
| GET | /recovery-points/:id/entries/:entryId/processing | 🔒 查看闭合增强处理状态，不创建任务（`backup_assets:preview`） |
| POST | /recovery-point-diffs | 🔒 对两个明确恢复点执行精确 metadata diff（`backup_assets:list`） |

### 备份资产处理管理（默认关闭）

以下路由全部要求认证、Admin、全局 feature gate、独立速率/请求体上限，并只返回有界脱敏 DTO。Offline bundle bytes 由运维人员放入 updater-only 固定只读 inbox；浏览器与 Core HTTP API 只发送 candidate JSON 控制请求，不接收 multipart、URL、服务器路径或 bundle bytes。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /admin/backup-asset-processing | 🔒 Worker/Derived 有界健康摘要 |
| GET | /admin/backup-asset-processing/capabilities | 🔒 闭合 capability/profile inventory |
| GET | /admin/backup-asset-processing/coverage | 🔒 eligible/complete/partial/queued/failed/unsupported/not-deployed/stale 聚合 |
| GET | /admin/backup-asset-processing/updater | 🔒 脱敏 updater 与 active bundle 状态 |
| GET | /admin/backup-asset-processing/updater/offline-candidates | 🔒 已验签 candidate 的脱敏列表 |
| PATCH | /admin/backup-asset-processing/backfill-policy | 🔒 使用 revision CAS 更新 pause/quota |
| POST | /admin/backup-asset-processing/updater/offline-candidates/scan | 🔒 请求扫描固定 inbox；请求体必须为空 |
| POST | /admin/backup-asset-processing/updater/offline-imports | 🔒 使用 `candidate_id` 与 expected fingerprint 确认原子激活 |

Updater receipt 只在独立 Unix socket `/run/xirang/asset-worker-updater.sock` 上提供 `/internal/v1/asset-worker-updater/*` 私有协议，并在解码请求前校验固定 socket 权限和 peer credential。Parser socket 位于独立的 `/run/xirang/worker/asset-worker.sock`；两个 socket 分属不同 named volume，互不挂载。Updater 协议不注册到公开 `/api/v1`、Nginx 或 parser Worker socket。

### 任务与执行

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /tasks | 🔒 任务列表 |
| GET | /tasks/:id | 🔒 任务详情 |
| GET | /tasks/:id/logs | 🔒 任务日志 |
| POST | /tasks | 🔒 创建任务 |
| PUT | /tasks/:id | 🔒 更新任务 |
| DELETE | /tasks/:id | 🔒 删除任务 |
| GET | /tasks/:id/runs | 🔒 执行历史 |
| POST | /tasks/batch-trigger | 🔒 批量触发（需二次验证 + task.batch_trigger/task_command/task_id 临时授权） |
| POST | /tasks/:id/trigger | 🔒 手动触发（需二次验证 + task.manual_trigger/task_command/task_id 临时授权） |
| POST | /tasks/:id/cancel | 🔒 取消执行 |
| POST | /tasks/:id/pause | 🔒 暂停调度 |
| POST | /tasks/:id/resume | 🔒 恢复调度 |
| POST | /tasks/:id/skip-next | 🔒 跳过下次 |
| POST | /tasks/:id/restore | 🔒 从备份恢复（需二次验证 + task.restore_trigger/task_restore/task_id 临时授权） |
| GET | /tasks/:id/backup-files | 🔒 备份文件列表 |
| POST | /tasks/:id/rclone-versioning/portable-binding-setups | 🔒 创建一次性 Portable binding setup（Admin + `backup_repositories:manage` + Task ownership） |
| PUT | /tasks/:id/rclone-versioning/portable-binding | 🔒 提交 write-only Portable bound config |
| POST | /tasks/:id/rclone-versioning/native-binding-setups | 🔒 创建一次性 AWS Native binding setup |
| PUT | /tasks/:id/rclone-versioning/native-binding | 🔒 提交 write-only STS/S3/KMS binding |
| POST | /tasks/:id/rclone-versioning/preflights | 🔒 运行有界 Rclone 版本化预检 |
| POST | /tasks/:id/rclone-versioning/activate | 🔒 激活 `first_new_point` 或 `imported_baseline` |
| POST | /tasks/:id/rclone-versioning/clean-rollbacks | 🔒 在首个 reservation 前执行 clean rollback |
| POST | /tasks/:id/rclone-versioning/rollback-preparations | 🔒 排空受管工作并准备保留证据的回退 |
| GET | /task-runs/:id | 🔒 执行详情 |
| GET | /task-runs/:id/logs | 🔒 执行日志 |

### 批量命令

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | /batch-commands | 🔒 创建批量命令（需二次验证 + batch_command.create/batch_command/node_id 临时授权） |
| GET | /batch-commands/:batch_id | 🔒 查询状态 |
| DELETE | /batch-commands/:batch_id | 🔒 取消/删除 |

### 通知集成

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /integrations | 🔒 渠道列表 |
| GET | /integrations/:id | 🔒 渠道详情 |
| POST | /integrations | 🔒 创建渠道 |
| PUT | /integrations/:id | 🔒 更新渠道 |
| PATCH | /integrations/:id | 🔒 部分更新 |
| POST | /integrations/:id/test | 🔒 测试发送 |
| DELETE | /integrations/:id | 🔒 删除渠道 |

### 应用凭据

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /app-credentials/profiles | 🔒 profile 列表（含配置 schema） |
| GET | /app-credentials | 🔒 凭据列表 |
| GET | /app-credentials/:id | 🔒 凭据详情 |
| POST | /app-credentials | 🔒 创建凭据 |
| PUT | /app-credentials/:id | 🔒 更新凭据 |
| DELETE | /app-credentials/:id | 🔒 删除凭据（有引用时阻止） |

### 自动化规则

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /automation-rules | 🔒 规则列表 |
| GET | /automation-rules/:id | 🔒 规则详情 |
| POST | /automation-rules | 🔒 创建规则 |
| PUT | /automation-rules/:id | 🔒 更新规则 |
| DELETE | /automation-rules/:id | 🔒 删除规则 |

### 服务监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /status-page | 公开状态页（无需认证） |
| GET | /service-monitors | 🔒 监控列表 |
| GET | /service-monitors/:id | 🔒 监控详情 |
| POST | /service-monitors | 🔒 创建监控 |
| PUT | /service-monitors/:id | 🔒 更新监控 |
| DELETE | /service-monitors/:id | 🔒 删除监控 |

### 告警

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /alerts | 🔒 告警列表 |
| GET | /alerts/unread-count | 🔒 未读数量 |
| GET | /alerts/:id | 🔒 告警详情 |
| GET | /alerts/:id/group-info | 🔒 内存分组计数（alerts:read） |
| GET | /alerts/delivery-stats | 🔒 投递统计 |
| GET | /alerts/:id/deliveries | 🔒 投递记录 |
| POST | /alerts/bulk-resolve | 🔒 批量解决未处理告警 |
| POST | /alerts/:id/ack | 🔒 确认告警 |
| POST | /alerts/:id/resolve | 🔒 解决告警 |
| POST | /alerts/:id/retry-delivery | 🔒 重试投递 |
| POST | /alerts/:id/retry-failed-deliveries | 🔒 批量重试 |
| POST | /alert-deliveries/:id/retry | 🔒 手动重试指定投递记录（admin-only；不存在返回 404） |
| GET | /alerts/:id/logs | 🔒 告警触发前后 ±5min 节点日志（alerts:read） |

### 静默规则

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /silences | 🔒 静默规则列表（?active=true 仅返回生效中，alerts:read） |
| GET | /silences/:id | 🔒 静默规则详情（alerts:read） |
| POST | /silences | 🔒 创建静默规则（admin-only） |
| PATCH | /silences/:id | 🔒 更新静默规则（admin-only） |
| DELETE | /silences/:id | 🔒 软删除静默规则（admin-only） |

### SLO 定义

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /slos | 🔒 SLO 定义列表（alerts:read） |
| GET | /slos/compliance-summary | 🔒 所有已启用 SLO 合规汇总（alerts:read） |
| GET | /slos/:id | 🔒 SLO 定义详情（alerts:read） |
| GET | /slos/:id/compliance | 🔒 单条 SLO 合规状态（alerts:read） |
| POST | /slos | 🔒 创建 SLO 定义（admin） |
| PATCH | /slos/:id | 🔒 更新 SLO 定义（admin） |
| DELETE | /slos/:id | 🔒 硬删除 SLO 定义（admin） |

### 节点日志

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /node-logs | 🔒 节点日志查询（logs:read；支持 node_ids/source/path/priority/q/time） |

### 自定义看板

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /dashboards | 🔒 看板列表（当前用户） |
| POST | /dashboards | 🔒 创建看板 |
| GET | /dashboards/:id | 🔒 看板详情（含 panels） |
| PATCH | /dashboards/:id | 🔒 更新看板设置 |
| DELETE | /dashboards/:id | 🔒 删除看板 |
| POST | /dashboards/:id/panels | 🔒 添加面板 |
| PATCH | /dashboards/:id/panels/:pid | 🔒 更新面板 |
| DELETE | /dashboards/:id/panels/:pid | 🔒 删除面板 |
| PUT | /dashboards/:id/panels/layout | 🔒 批量更新布局 |
| POST | /dashboards/panel-query | 🔒 执行面板查询（不绑定 panel） |
| GET | /dashboards/metrics | 🔒 获取可用 metric 清单 |

### 审计日志

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /audit-logs | 🔒 日志列表 |
| GET | /audit-logs/export | 🔒 导出 CSV |
| GET | /credential-audit-events | 🔒 管理员凭据使用审计事件列表 |
| GET | /credential-audit-events/export | 🔒 管理员导出凭据使用审计 CSV |
| GET | /credential-access-grants | 🔒 管理员只读查看短时凭据授权状态列表（安全元数据；支持分页、筛选、排序） |
| POST | /credential-access-grants/terminal | 🔒 申请并激活短时终端凭据使用授权（admin；需二次验证；绑定 node_id/action/purpose） |
| POST | /credential-access-grants/config-import | 🔒 申请并激活短时配置导入授权（admin；需二次验证；绑定 config.import/config_import） |
| POST | /credential-access-grants/config-export | 🔒 申请并激活短时敏感配置导出授权（admin；需二次验证；绑定 config.export/config_export） |
| POST | /credential-access-grants/snapshot-restore | 🔒 申请并激活短时快照恢复授权（admin；需二次验证；绑定 snapshot.restore/snapshot/task_id） |
| POST | /credential-access-grants/task-restore | 🔒 申请并激活短时任务恢复授权（admin；需二次验证；绑定 task.restore_trigger/task_restore/task_id） |
| POST | /credential-access-grants/task-manual-trigger | 🔒 申请并激活短时任务手动触发授权（admin/operator；需二次验证；绑定 task.manual_trigger/task_command/task_id） |
| POST | /credential-access-grants/task-batch-trigger | 🔒 申请并激活短时任务批量触发授权（admin/operator；需二次验证；逐 task_id 创建授权） |
| POST | /credential-access-grants/batch-command | 🔒 申请并激活短时批量命令授权（admin/operator；需二次验证；逐 node_id 创建授权） |

### SLA 报告

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /report-configs | 🔒 报告配置列表 |
| POST | /report-configs | 🔒 创建配置 |
| PUT | /report-configs/:id | 🔒 更新配置 |
| DELETE | /report-configs/:id | 🔒 删除配置 |
| POST | /report-configs/:id/generate | 🔒 立即生成 |
| GET | /report-configs/:id/reports | 🔒 报告列表 |
| GET | /reports/:id | 🔒 报告详情 |

### 快照与恢复

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /tasks/:id/snapshots | 🔒 快照列表 |
| GET | /tasks/:id/snapshots/:sid/files | 🔒 快照文件 |
| POST | /tasks/:id/restore | 🔒 从备份恢复（需二次验证 + task.restore_trigger/task_restore/task_id 临时授权） |
| POST | /tasks/:id/snapshots/:sid/restore | 🔒 从快照恢复（需二次验证 + snapshot.restore/snapshot/task_id 临时授权） |
| GET | /tasks/:id/snapshots/diff | 🔒 快照对比 |
| GET | /tasks/:id/snapshots/search | 🔒 搜索快照文件 |

### 系统设置与配置

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /settings | 🔒 全部设置 |
| PUT | /settings | 🔒 批量更新 |
| DELETE | /settings/:key | 🔒 删除设置 |
| GET | /settings/security-risk-summary | 🔒 安全风险摘要（admin；只读计数与脱敏示例） |
| POST | /settings/backup-assets/ga/inventory | 🔒 运行备份资产 GA 干跑库存盘点（admin + `backup_repositories:manage`；只返回计数） |
| GET | /settings/backup-assets/ga/readiness | 🔒 读取备份资产 GA 安装类别与就绪状态（admin + `backup_repositories:manage`） |
| POST | /settings/backup-assets/ga/acknowledge | 🔒 确认当前库存摘要（admin + `backup_repositories:manage`；仅已有安装） |
| GET | /settings/logs | 🔒 节点日志保留默认天数（admin） |
| PATCH | /settings/logs | 🔒 更新节点日志保留默认天数（admin） |
| GET | /config/export | 🔒 导出配置（include_secrets=true 时需二次验证和 config.export/config_export 临时授权） |
| POST | /config/import | 🔒 导入配置 |
### 系统管理

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /version | 🔒 版本信息 |
| GET | /version/check | 🔒 检查更新 |
| POST | /system/backup-db | 🔒 备份数据库 |
| GET | /system/backups | 🔒 备份列表 |
| GET | /system/encryption-status | 🔒 加密迁移健康状态（admin）。返回 `v1_remaining_count`（enc:v1: 残留）、`plaintext_drill_script_field_count`（策略演练脚本明文残留字段数）；`healthy=true` 仅当两者均为 0。计数查询失败时返回 500，不报告 healthy。运维确认 V1/明文均已清理后可退役 V1 解密支持 |
| POST | /system/verify-mount | 🔒 验证挂载点 |

### WebSocket

| 路径 | 说明 |
|------|------|
| /ws/logs | 实时日志推送（协议内认证） |
| /ws/terminal | Web SSH 终端（协议内认证，需 admin 主 token、二次验证 proof、匹配且未过期的终端临时授权） |

### 健康检查与监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /healthz | 进程存活检查（无需认证） |
| GET | /readyz | 就绪检查（数据库 Ping 失败时返回 503，无需认证） |
| GET | /metrics | Prometheus 指标（生产必须 `METRICS_TOKEN` Bearer 鉴权 + 限速） |
| GET | /swagger/*any | Swagger UI（生产默认关闭；`SWAGGER_ENABLED=true` 可强制开启） |
| GET | /admin/metrics/rollup-status | 🔒 聚合器诊断（hourly/daily 最新桶 + 落后秒数），仅 admin |

## 执行器

| 类型 | 说明 |
|------|------|
| `rsync` | 基于 rsync 的文件同步，支持远端源/目标、SSH 密钥注入、带宽限制 |
| `restic` | 加密去重备份，支持仓库初始化、快照管理、进度解析 |
| `rclone` | 云存储同步（S3/MinIO 等），支持进度解析 |
| `command` | 远程 SSH 命令执行（批量命令场景） |

## 环境变量

完整参考见 [docs/env-vars.md](../docs/env-vars.md)。

关键必填项（生产环境）：
- `JWT_SECRET`：JWT 签名密钥（≥32 字符）
- `DATA_ENCRYPTION_KEY`：敏感字段加密密钥（≥16 字符）
- `METRICS_TOKEN`：`/metrics` Bearer token（≥16 字符，非文档占位符）
- `ADMIN_INITIAL_PASSWORD`：仅首次启动且库中尚无 admin 时需要（非每次启动必填）

## 数据库

支持 SQLite（默认）和 PostgreSQL。当前迁移版本：`000077_lifecycle_effect_claim_audit_slot`。该版本号由 `backend/internal/database/migrations/{sqlite,postgres}` 中成对的最新迁移文件维护，发布前必须通过迁移新鲜度检查。若升级时发现同一任务有多条 active drill，000074 会拒绝迁移；必须从已校验备份恢复，或先在单一事务中成对核对并终结 `TaskRun` 与 `RestoreDrillEvidence`，禁止只修改其中一侧。

000077 是从 v76 到 v77 的静默（quiesced）切换，只允许在完成 old-worker drain（旧 retention worker 已停止接收新任务并排空所有旧 worker）后执行。排空期间必须先处理 scoped `provider_delete`：`retention_expire` 或 `explicit_purge` 的 `provider_delete` 若没有匹配的有效 deletion receipt/tombstone，迁移会原子拒绝；普通的非候选 phase/reason 不会被误判为待迁移数据。迁移完成且所有旧进程退出后才能启动新 worker；这是 no mixed-version runtime 约束，v76 与 v77 retention worker 不得混跑。

settled audit backfill 与运行时共用唯一的 `settledDeletionCandidate` 谓词：operation 必须为 `retention_expire` 或 `explicit_purge`，且必须有有效 terminal tombstone/receipt（`terminal_state=expired`、`purged_at`、`deletion_receipt_digest` 非空，结果为 `provider_deleted` 或 `provider_already_absent`），或 phase=`blocked` 且 reason 属于 `{active_hold, provider_worm, provider_unavailable, provider_identity_conflict, provider_native_version_referenced, provider_delete_unproven, deletion_unavailable}`。`selected`、`revoking`、`draining`、`cleaning`、`lease_live`、`lease_drain_unproven`、`owner_cleanup_unproven`、`fence_lost`、`mutable_retire` 等均为 no-op，不产生 slot。

历史 `backup_asset_audit_events` 只有在完整匹配以下 producer signature 时才可 backfill slot：`action=repository_purge`；`repository_id`、`recovery_point_id` 与 attempt→point→repository 关系精确一致；顶层 `item_count=1` 且 `fields.item_count` 为整数 1；`fields.stage=settled`、`fields.source=<attempt_id>`，`fields.status` 为合法状态；`blocked`/`identity_conflict` 的 `outcome=blocked`，`deleted`/`already_absent` 的 `outcome=success`，并且 terminal 状态必须与 tombstone result 一致。相同 `(attempt_id,status)` 的精确重复只去重一次；`blocked` 与 `identity_conflict` 各最多一次、顺序任意，之后至多一个互斥的 terminal status。任何字段、关联、结果或顺序的 near miss 都不是匹配项；候选仍有歧义时整次迁移回滚且不生成 slot。

若 backfill 因 near miss 或缺失事件而 ambiguous，先在停机窗口内对照有效 tombstone、attempt/point/repository 关系和原始 event 逐项 reconcile，再重新运行迁移；不得用猜测事件填充 slot。000077 的 claim/slot 是永久幂等证据，不能依赖会被 retention 清理的 audit 明细。

000077 down 同时受 schema-migrations admission 与 down body 的独立 guard 保护；只有 effect-claim 与 audit-slot 两张表均为空才允许回退。已有 durable claim/slot rows 后禁止 destructive rollback 或删除历史，必须保留既有 rows/events 并通过 forward-only migration/repair 修复；只有尚未写入 durable rows 的未使用代码才可移除。

核心模型：User, SSHKey, Node, Policy, PolicyNode, Integration, Alert, AlertDelivery, Task, TaskRun, TaskLog, TaskTrafficSample, TokenRevocation, NodeMetricSample, NodeOwner, AuditLog, ReportConfig, Report, LoginFailure, SystemSetting, AppCredential, RestoreDrillEvidence, RecoveryPointLifecycleEffectClaim, RecoveryPointLifecycleAuditSlot, CredentialAuditEvent, CredentialAccessGrant, NodeMetricSampleHourly, NodeMetricSampleDaily, Silence, SLODefinition, NodeLog, NodeLogCursor, Dashboard, DashboardPanel, PanelFilters, EscalationPolicy, EscalationLevel, AlertEscalationEvent, AnomalyEvent, SnapshotDiffHistory, SnapshotFileIndex, AutomationRule, AutomationRuleLog, ServiceMonitor, ServiceUptimeSample（45 个模型）

敏感字段通过模型 hooks 加密保存；API 响应必须使用脱敏 DTO/辅助方法。`Node` 不返回密码/私钥，`SSHKey` 不返回私钥，`Task.ExecutorConfig` 不参与 JSON 序列化以避免泄露执行器密钥。

### 升级策略

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /escalation-policies | 🔒 策略列表 |
| POST | /escalation-policies | 🔒 创建策略（admin） |
| GET | /escalation-policies/:id | 🔒 策略详情 |
| PATCH | /escalation-policies/:id | 🔒 更新策略（admin） |
| DELETE | /escalation-policies/:id | 🔒 删除策略（admin，级联 SET NULL 到 task/policy/slo/node） |
| GET | /alerts/:id/escalation-events | 🔒 单告警升级历史（alerts:read） |

### 异常检测

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | /anomaly-events | 🔒 异常事件全局列表（detector/metric/severity/node_id 过滤 + 分页） |
| GET | /nodes/:id/anomaly-events | 🔒 单节点异常事件（OwnershipNodeCheck） |

## 测试

```bash
cd backend
go test ./...
```
