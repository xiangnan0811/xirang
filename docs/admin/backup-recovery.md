# 备份、恢复与快照

本文档说明 Xirang 的备份策略、应用感知备份、保留策略、恢复演练、快照浏览/搜索和快照异常检测。

## 备份引擎

Xirang 支持三类备份执行器：

| 执行器 | 适用场景 | 说明 |
|---|---|---|
| Rsync | 文件/目录同步 | 适合传统目录级备份和快速增量同步。 |
| Restic | 快照备份 | 支持快照浏览、快照对比、文件搜索、GFS 保留、完整性检查和快照异常检测。 |
| Rclone | 对接对象存储/远端存储 | 适合把备份同步到云存储或其他 rclone 支持的后端。 |

备份策略可配置 cron 调度、源/目标路径、排除规则、带宽限制、前后置 hook、重试策略和保留策略。

## Restic 精确血缘与安全回退

`backup_assets.enabled` 默认是 `false`。在从未产生过 managed 恢复点的安装上，关闭该开关保持既有 Restic 兼容行为。启用或关闭开关、以及通过设置导入/删除覆盖值时，服务会先停止新的 Restic admission，并排空已经开始的 backup、list、files、search、diff、snapshot restore、anomaly、retention、publication 与 reconciliation 命令；数据库设置只会在该排空完成后写入。

启用后，一次 Restic backup 只有在 exit-zero summary、完整原生 snapshot ID、精确 Task/TaskRun 标记、Manifest 和最低验证全部一致时，才会异步发布为可信恢复点。传输成功与发布成功是独立事实：Manifest 或血缘失败不会改写已经发生的 TaskRun 传输结果。

一旦安装产生 native managed 恢复点或保留 tombstone，持久的 managed-history latch 会生效。此后即使关闭 feature，也进入 rollback-safe 模式：继续执行精确 Task lineage guard，禁止无 tag 的 legacy backup fallback、`restore latest`、仓库级最新快照异常比较和无 tag 的 Restic retention。该保护不会执行 `forget`、`prune`、`delete`，也不会删除原生 snapshot。

已经使用 managed publication 的安装必须保留 migration `000063_backup_asset_publication_contract`，并在 feature 关闭时继续使用兼容 Child 3 的二进制。回退到不理解该合同的应用版本或执行 schema down 前，必须走独占 preflight；只要存在 active publication lease、managed history 或 tombstone，preflight 会拒绝继续。

## 应用感知备份

应用感知备份会根据业务应用类型，在备份前自动执行数据库 dump，降低直接备份数据目录造成不一致的风险。

支持的 profile：

| Profile | 部署类型 | Pre-hook 行为 | Post-hook 行为 |
|---|---|---|---|
| `mysql` | 主机 | `mysqldump --all-databases --single-transaction` | 清理临时文件 |
| `postgres` | 主机 | `pg_dumpall` | 清理临时文件 |
| `mongodb` | 主机 | `mongodump` | 清理临时文件 |
| `redis` | 主机 | `BGSAVE` + 复制 RDB | 清理临时文件 |
| `docker-mysql` | 容器 | `docker inspect` + `docker exec mysqldump` | 清理临时文件 |
| `docker-postgres` | 容器 | `docker inspect` + `docker exec pg_dumpall` | 清理临时文件 |
| `docker-mongodb` | 容器 | `docker inspect` + `docker exec mongodump` | 清理临时文件 |
| `docker-redis` | 容器 | `docker inspect` + `BGSAVE` + `docker cp` | 清理临时文件 |

使用步骤：

1. 在「凭据管理」创建应用凭据。
2. 在策略编辑页选择应用类型和对应凭据。
3. Xirang 会注入相应 pre-hook / post-hook。
4. 触发一次备份任务，在任务日志中确认 dump 成功。

安全注意：

- 应用凭据密码加密存储。
- API 响应不会返回明文密码。
- 渲染后的 hook 脚本对有权限的用户可见，请按 RBAC 控制管理权限。

旧版 hook 模板端点已移除；应用感知备份的受支持路径是 `GET /api/v1/app-credentials/profiles`。

## 保留策略与 RPO/RTO

### Simple 模式

默认保留模式。按 `retention_days` 清理过期快照或备份目录：

- Restic：`restic forget --keep-within <N>d`
- Rsync/Rclone：按 `retention_days` 清理旧备份目录

### GFS 模式

GFS（Grandfather-Father-Son）按日/周/月/年多级保留快照。

| 参数 | 说明 | 默认值 | 范围 |
|---|---|---:|---|
| `keep_daily` | 保留最近 N 个日快照 | 0 | 0-365 |
| `keep_weekly` | 保留最近 N 个周快照 | 0 | 0-104 |
| `keep_monthly` | 保留最近 N 个月快照 | 0 | 0-120 |
| `keep_yearly` | 保留最近 N 个年快照 | 0 | 0-30 |

对 pristine legacy Restic 任务，保留策略的命令形态为：

```bash
restic forget --keep-daily <N> --keep-weekly <N> --keep-monthly <N> --keep-yearly <N> --prune
```

GFS 模式仅适用于 Restic 执行器。Rsync 和 Rclone 任务按 Simple 模式处理。出现 managed-history latch 后，上述无 tag `forget --prune` 路径会被安全阻止，直到受控生命周期功能接管；不会以删除 snapshot 作为回退或对账手段。

### RPO/RTO 目标

策略编辑器的“保留策略与 SLA 目标”中可设置：

- RPO 目标（分钟）：预期恢复点目标，0 表示不设目标。
- RTO 目标（分钟）：预期恢复时间目标，0 表示不设目标。

SLA 报告会计算：

- 实际 RPO：该策略关联任务最近成功执行记录中，相邻两次 `started_at` 的最大间隔。
- 实际 RTO：最近一次恢复任务（`trigger_type=restore`）的 `duration_ms / 60000`。
- 达标判断：`actual <= target`。

## 恢复演练

恢复演练会定期将最新备份恢复到隔离沙箱节点，并执行自定义校验脚本，验证备份是否真的可用。

### 使用步骤

1. 注册一台空闲或测试服务器作为沙箱节点。
2. 编辑备份策略，打开“启用自动恢复演练”。
3. 设置演练 cron，例如 `0 3 * * 0`。
4. 选择沙箱节点，设置恢复路径，例如 `/tmp/xirang-drill`。
5. 填写校验脚本。
6. 点击“手动触发演练”验证一次。

校验脚本分三段：

| 阶段 | 说明 | 示例 |
|---|---|---|
| Pre-verify | 环境准备 | `systemctl start mysql` |
| Verify | 实际校验，退出码 0 表示成功 | `mysql -e "SELECT 1"` |
| Post-verify | 清理脚本，无论成败都执行 | `systemctl stop mysql` |

手动触发 API：

```text
POST /api/v1/policies/:id/drill-trigger
```

安全措施：

- 沙箱节点不能是备份源节点。
- 恢复路径必须是绝对路径，禁止系统目录。
- 默认演练后自动清理恢复文件。

恢复演练与完整性检查的区别：

| 项目 | 完整性检查 | 恢复演练 |
|---|---|---|
| 验证内容 | 备份仓库结构 | 备份数据可恢复且能通过业务校验 |
| 执行方式 | `restic check` / `rclone check` | 实际恢复 + 自定义脚本 |
| 执行位置 | 备份源节点 | 沙箱节点 |
| RTO 记录 | 否 | 是 |

## 快照文件搜索

Restic 任务支持跨快照按文件路径搜索，方便定位某个文件出现在哪些历史快照中。

使用方式：

1. 打开任务列表。
2. 点击 Restic 任务的“执行历史”。
3. 点击“搜索文件”。
4. 输入关键词，例如 `nginx.conf` 或 `data/backup`。
5. 搜索结果展示匹配路径、快照和文件大小。
6. 点击结果可跳转到快照文件浏览页面查看或恢复。

索引机制：

- 首次搜索某个任务时，后台运行 `restic ls --json` 构建文件索引。
- 后续搜索只对新增快照做增量索引。
- 构建中 API 会返回 `{status: "indexing"}`，前端提示稍后重试。

限制：

- 仅支持 Restic 任务。
- 搜索范围是文件路径，不搜索文件内容。
- 单次最多返回 200 条结果。
- 不支持正则表达式，使用 SQL `LIKE` 匹配。

API：

```text
GET /api/v1/tasks/:id/snapshots/search?q=<keyword>
```

## 快照异常检测

Restic 备份完成后，Xirang 会分析最新两个快照之间的文件变更，检测异常行为。

| 维度 | 检测方法 | 严重度 | error_code |
|---|---|---|---|
| 变更量异常 | 当前变更文件数 > 历史基线 + 3σ | warning | `XR-SNAPSHOT-CHURN-{policyID}` |
| 勒索后缀 | 变更文件中出现已知勒索后缀 | critical | `XR-SNAPSHOT-RANSOM-{policyID}` |

基线建立规则：

- 每次备份后的 diff 统计写入历史记录。
- 首次 2 次备份仅收集基线，不触发检测。
- 第 3 次起，与最近 10 次历史的移动平均和标准差比对。

已知勒索后缀包括：

```text
.encrypted .locked .crypt .ransom .xxx .zzz
.enc .lock .crypto .wannacry .pay .decrypt
```

异常事件默认只记录，不升级为告警通知。需要通知时，在系统设置中打开 `anomaly.alerts_enabled`，或设置环境变量 `ANOMALY_ALERTS_ENABLED=true`。

限制：

- 仅支持 Restic 任务。
- 检测在备份完成后异步执行，不影响备份任务本身。
- 首次部署后需要至少 3 次备份才能建立有效基线。
