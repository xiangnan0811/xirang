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

## Rsync 版本化恢复点

Rsync 任务默认继续使用传统的可变目标。只有管理员可以从任务操作区为一个现有 Rsync 任务启动版本化迁移；系统不会把旧目录、历史 TaskRun 或目录时间戳自动认定为恢复点。

版本化发布会为每次可证明成功的运行创建新的受管目录树。管理员先选择一种模式并运行本地预检：

- **版本化硬链接树**：在同一受管仓库内复用上一个已提交点的未变更文件。预检必须验证挂载、硬链接、原子提交、容量、inode 和路径安全条件；失败时必须由管理员显式改选完整副本，系统不会自动降级。
- **版本化完整副本树**：每个点都使用独立文件树，不与前一个点共享 inode。

预检成功后，管理员必须明确选择以下其中一种迁移路径：

- **创建新的完整基线**：立即从旧目标创建一个新的、完整的受管副本并发布为第一个恢复点。它不会原地标记、移动或硬链接旧目录；旧目标仍只作为回退定位器保留。
- **从下一次成功运行开始**：保持旧目标不变，也不追认任何旧历史。下一次成功运行才会创建第一个受管恢复点；若长期模式为硬链接，首个点仍是完整副本种子。

激活后任务保持暂停，直到受管发布流程可安全接管。任务的传输结果、Provider 目录提交和数据库恢复点发布是独立事实：一次传输成功不表示恢复点已经可用。

### 安全与回退边界

- Xirang 管理的目录树不等于存储 WORM。拥有底层目录写权限的外部主体仍可能修改文件；保护边界是受控命名空间、权限和 admission，而不是物理不可变介质。
- Rsync 也不是源端的时间点快照。需要应用一致性时，应在源端静默应用或使用底层卷快照。
- 版本化点的恢复/浏览、通用 retention/purge 和物理删除不属于当前功能。系统不会为了回退、对账或迁移而删除已提交点。
- “准备回退”会停止新的受管准入、排空相关工作，并恢复保留的 legacy locator 后让任务保持暂停；它不会删除已提交恢复点，也不会自动恢复旧的可变执行路径。
- 一旦存在受管 Rsync 历史，managed-history latch 会阻止不安全的 mutable fallback，即使备份资产功能后来被关闭。执行 schema down 前也会因受管 history、versioned link 或活动 lease 而失败关闭；保留 `000064_backup_asset_rsync_publication_contract`。

## Rclone 版本化恢复点

Rclone 任务默认仍是 `legacy_mutable`：任务把数据同步到一个可变 Remote 目标，不会把旧 TaskRun、对象时间或 Remote 当前内容追认为历史恢复点。`backup_assets.enabled` 仍默认是 `false`；只有管理员显式开启该功能后，才能从现有 Rclone 任务的操作区配置、预检并激活版本化发布。传输完成、Provider 端提交完成和数据库恢复点发布是三个独立事实，只有三者的精确证据收敛后，恢复点才会进入可用状态。

管理员必须为受管任务选择以下一种模式：

| 模式 | 适用范围 | 可信边界与成本 |
|---|---|---|
| Portable 独立前缀（`versioned_prefix`） | 默认推荐；通过经过校验的 Rclone v1.74.4 bound config 访问 Remote | 每次运行写入新的受管前缀，生成规范化清单，并最后写入 commit marker。Remote 只提供弱哈希或没有哈希时，Xirang 会逐字节读取并校验源、目标数据；这会增加节点出口流量、Remote API 请求、读取费用和运行时间，超过配置的字节或时限上限时失败关闭。 |
| AWS 原生对象版本（`native_object_versions`） | 仅 AWS 官方区域端点上的通用型 S3 bucket | 用 S3 `VersionId`、delete marker、完整 mutation ledger 和精确版本读取证明一个恢复点。当前实现范围不覆盖 directory bucket、access point/Outposts、自定义端点、任意 S3-compatible 存储、Azure Blob 或 Google Cloud Storage 的原生版本能力；这些目标应使用 Portable 或保持 legacy。 |

Portable 仍是默认推荐。当前版本保留官方 AWS 的 opt-in live conformance suite，但项目维护者没有为本版本配置专用 AWS fixture，因此不提供 release-level AWS live certification 声明。AWS Native 的每个实际目标仍必须通过自身 bucket、Role、versioning、lifecycle、加密和 KMS 状态的完整运行时预检，缺少或漂移任一证据都会失败关闭。

### 管理员配置与预检

1. 先暂停现有 Rclone 任务，并确认仍保留正确的 legacy locator。受管 namespace 必须与旧目标物理隔离；系统不会原地接管或重命名旧数据。
2. 在任务的“Rclone 版本化”对话框选择 Portable（默认）或 AWS Native，创建一次性 setup，并提交 write-only 绑定信息。
3. Portable 必须重新提交完整的 Rclone 配置和目标 Remote。受管命令始终使用加密保存的同一份 bound config bytes/revision；节点上的默认 Rclone 配置（`node_default`）只服务 legacy 任务，不会被导入、推断或自动升级为受管凭据。动态凭据来源、未知选项、未认证 backend/wrapper 或不闭合的 Remote 依赖会被拒绝。
4. AWS Native 必须使用同账户的专用 IAM Role，并配置由信任策略强制校验的 external ID。Xirang 只通过 STS `AssumeRole` 获得覆盖单次操作时限的临时会话，不接受把节点静态身份当作受管绑定。external ID 只在短期 setup 响应中显示一次，不会由普通任务查询接口回显。
5. 运行预检并查看 Remote 一致性、哈希强度、预计 API/存储/全字节校验成本、凭据有效期、外部 writer 风险、生命周期和加密状态。AWS Native 要求 versioning、lifecycle、身份和能力的两次稳定观察至少相隔 15 分钟，并通过精确版本 canary；在 settling 完成前不能激活。

预检是有时效和 revision 绑定的安全证据。任务、绑定、凭据、Remote 能力、生命周期或加密配置发生变化后，旧预检失效，必须重新运行；系统不会在执行中静默降级到 Portable 或可变同步。

### AWS 加密与生命周期

AWS Native 只接受两种闭合加密档位：

- **SSE-S3**：写入显式使用 S3 托管密钥加密，并核对每个精确对象版本的实际加密身份。
- **同账户 customer-managed SSE-KMS**：绑定一个 active write key，并可保留有限数量的 decrypt-only read keys。轮换时先把旧 active key 加入保留读取 key ring，再切换新 active key；只要任何已提交版本仍引用旧 key，就必须保持该 key 可用、可解密，不得禁用或安排删除。系统校验 key 的账户、区域、状态、用途、来源和权限，但普通 DTO、日志与审计不会暴露 key ARN。

Xirang 不会创建、修改或删除 KMS key，也不会自动修改 bucket lifecycle。任何与受管前缀重叠的 current/noncurrent expiration、expired delete-marker cleanup、未知动作或未认证的离线存储转换都会使预检/admission 失败关闭；运行期间检测到 versioning、lifecycle、身份、权限、KMS key 或加密档位漂移时同样阻止新发布。运维人员必须在 AWS 侧先修正策略，再重新预检。

### Baseline 与回退边界

预检成功后仍需明确选择迁移方式：

- **从下一次成功运行开始（`first_new_point`，默认）**：旧目标保持不变，下一次受管运行才创建第一个恢复点。在激活完成后、任何受管运行或 publication reservation 开始前，仍保留一次 clean rollback 窗口。
- **导入当前基线（`imported_baseline`）**：把稳定的 legacy current head 物理复制到新的受管 namespace，并用与正常恢复点相同的清单、fence、完整校验和发布合同建立第一个点。它不会根据 mtime、旧 TaskRun 或已有对象版本伪造历史。激活会立即建立 durable reservation，因此激活成功后不再存在 clean rollback 窗口。

Clean rollback 只适用于 `first_new_point` 激活后且从未出现任何受管 reservation、恢复点、history latch 或活动 lease 的短暂窗口；它会恢复原 legacy 配置，不删除 Remote 数据。窗口关闭后只能使用“准备回退”：系统停止新的受管准入、排空并调和未决工作、重新连接隔离保存的 legacy locator，同时让任务保持暂停。该操作保留所有 committed point、失败尝试、orphan、清单、审计证据和 managed-history latch，也不会自动启用旧的 mutable runtime。

### 明确不提供的保证

- Portable 前缀和 AWS 原生对象版本都不是 WORM。Native 的 `backend_versioned` 只说明 Xirang 能按精确 `VersionId` 证明和读取版本；拥有底层删除权限的外部主体仍可能破坏数据，也不代表启用了 Object Lock、合规保留或不可删除介质。
- 当前不实现 Provider deletion、精确版本清理、通用 retention/purge 或已提交恢复点删除；回退、调和和健康检查都不会删除已提交前缀、对象版本或 delete marker。
- Rclone 不是源端时间点快照。需要数据库或应用一致性时，仍应使用应用静默、dump 或底层卷快照。
- 一旦产生受管 reservation/history，持久 latch 会在 feature 后续关闭时继续阻止不安全的 legacy mutable fallback。关闭 `backup_assets.enabled` 不是降级或清除受管历史的方法。

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
