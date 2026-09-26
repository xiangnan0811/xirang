# 任务执行与恢复

本文统一任务配置、调度、持久化执行身份、旧版备份恢复及演练合同。受管恢复点的 Provider 身份、发布与恢复权威见[仓库与发布](backup-repository.md)，授权与秘密见[凭据与访问](credentials-access.md)，面向运维的步骤见[备份与恢复](../../admin/backup-recovery.md)。

## 配置更新与目标归属

- Task 更新是部分更新，缺字段保留原值；必须提交当前字符串 `revision` 对应的 `expected_revision`。非法/缺失修订返回 400，并发变更 409，前端保留草稿并重新加载，不能自动重试覆盖。
- 显式空 `cron_spec` 表示手动任务，关联 policy 也不补回 Cron。任务持久化调度继承来源：显式 Cron（包括清空）建立 override，策略重命名、Cron 变化及停用/恢复都不抹掉 override。停用策略停止关联任务并清活动游标；恢复时手动仍手动，自定义用自己的 Cron，继承任务用当前策略 Cron。
- 配对迁移 `000088_task_cron_override` 仅把与当时策略有效计划不同的历史 Cron 标记为 override；相同值不猜测曾经覆盖。配置导入导出保留来源；使用后禁止降级抹除。
- Cron 在写入前验证；历史坏 Cron 只隔离该任务并留诊断，不阻断全部启动。配置已保存而即时同步调度失败时返回明确“已保存”的 503，不用旧全行回滚；周期调和恢复调度。
- API 只公开白名单 `executor_settings`、`executor_secrets_configured`，不输出原始 `executor_config`。Restic 排除规则 `[]` 显式清空；密码空值保留，非空替换保留原始字节（包括空白）。仅重命名等更新保留 Rclone 带宽/并发配置。
- policy、模板克隆、导入和 service monitor 创建均保留显式 `enabled=false`；policy 还保留 `verify_enabled=false`、`max_retries=0`。模板克隆始终禁用；省略字段才使用默认，零重试不自动重跑；持久化仍执行加密 hook。
- 新 policy Rsync 目标为 `<backup-root>/.xirang/policies/<policy-id>/nodes/<node-id>`。持久化 ID 隔离 writer，不能靠 source basename 或可改节点名称。同步/改名不改已有 target；路径变化不证明历史数据已经迁移。Core 本地准入拒绝规范路径冲突、symlink alias 和祖先/后代归属重叠。
- 节点迁移要求相关任务禁用、无调度及无活动 durable run；只把独立归属的源复制到全新隔离目标，验证后在锁下重核归属/配置再切换 DB。共享源及已有目标拒绝合并，保留原源。导入补偿先在全局/policy/task 锁下捕获 before-image，再核对当前 claim 与导入后行；并发变化或旧目标的新 owner 均拒绝补偿覆盖，不强制恢复旧 locator。
- Restic `repository_version` 仅选择新仓库格式（默认/1/2），不是 append-only 或删除保护。旧 `append_only=true/false` 在加密配置边界分别幂等转为 2/默认，保留其他字段，冲突或非法值隔离诊断；旧字段只允许显式导入转换，不再作为运行时配置。删除保护须在存储后端独立验证。
- Rsync allowlist 依赖 Linux Landlock ABI 3、匹配本地/远端 helper 及私有 user/mount namespace；运行环境不满足时失败关闭，不能移除 allowlist 或自动授予 privileged。受管 hardlink staging 要预留完整树容量和传输空间。可选 Worker 的精确系统包版本影响工具链 fingerprint，Core/Worker 必须由同一发布源码重建并匹配；细节见[处理与导出](backup-processing-export.md)。

## 持久化执行与调度

普通 TaskRun 创建冻结正数 `node_id_snapshot`，必须等于当时 Task 节点；`task_id` 与 snapshot 不可变。执行、恢复、演练、发布与准入同时核对任务 ID、节点快照及预期状态，不能只查询任意成功历史。snapshot 0 仅是迁移保留的 terminal orphan `legacy_unknown`：允许 `success|failed|canceled|warning|skipped`，状态不可变，所有可执行消费方拒绝它。活动/未知状态 orphan 或非正/不匹配 live Task 使迁移原子失败。

配对迁移 000069/000072 使旧升级和已有 69–71 安装汇合到此合同；含 legacy-zero 的 used-down 在修改 migration version 前拒绝。启动不 Force dirty；clean >=69 先后核对最小 Recovery schema，>=72 核对最终 trigger/PostgreSQL constraint，假同名空操作对象仍是 drift。通用迁移流程见[数据库规范](../backend/database-guidelines.md)。

执行租约、原子结束和 TaskRunEffect 持久化副作用保证重启可调和；批次幂等和派发回执避免重复边。历史重复 `(task_id, upstream_task_run_id)` 在标记 dirty 前拒绝迁移，须离线核实真实历史，不能猜测删除。使用过的 effect/批次回执禁止擦除降级。

- `000082` 保存私有不可变 cron occurrence 与执行备份配置 fingerprint，历史 NULL/空值保持未知。普通备份恢复要求匹配节点和 policy 执行输入；预约与执行入口使用同一锁定策略快照。
- `000086` 在本地、节点、资源、策略准入前先持久化唯一 `(task_id, scheduled_at)` occurrence；不同到期时间不合并。遇满额/busy 保留 queued intent，重启仍通过相同安全准入排空；在线调和和重启保留已知过期 `next_run_at`，不猜整个系统停机历史。
- 仅有持久身份、仍安全 pending 且旧 lease 已过期的 occurrence 可重新认领。running 或未知远端结果不能盲目重放。skip-next 只在 cron 执行入口事务消费，包括入队前/排队中设置；手动不消费，各 policy task 独立。相同 occurrence 重放不能在消费 skip 后再创建一次执行。
- 新 retry effect 的投递时刻用 `TaskRunEffect.NextAttemptAt`，普通 Cron 游标保持 `Task.NextRunAt`；旧未标记 effect 保持兼容解释。retry 拒绝也执行完整次数递增/耗尽逻辑，配置变化和取消不覆盖标记 retry 的普通游标。
- 优雅 shutdown 先封本地准入再等待，释放尚未启动 durable run 供重启；显式用户取消与停机释放分开。认领拒绝/启动前错误只释放当前 local owner。执行前拒绝、取消和普通终结均 Task→TaskRun 加锁并重新验证身份。
- 下游任务忙时链式 effect 可重试，不确认不存在的 child；禁用/归档下游保留 skipped child，重试耗尽仍是 failed effect。重放不复制同一上下游边。
- policy `max_concurrent` 在 DB policy lock 下跨节点/Core 统计普通 pending/running reservation，并在执行入口重核；全局 semaphore 独立。手动忙请求不生成延期 occurrence，Cron intent 不占配额但等待可用 slot；完成释放容量，禁用 policy 取消 pending 普通预约并记录 missed occurrence。旧非正限制保守按 1，API 拒绝负值；restore/drill 单独准入。

## 旧版 Rsync 恢复

Legacy Rsync/Rclone 是可变当前树，不是历史恢复点；retention 拒绝破坏性的按年龄删除并记录安全理由。受管恢复点与 Restic snapshot 保留各自边界。升级或补证前先保全数据库、密钥、TaskRun 及独立备份副本；共享过的树可能已无法由任一历史 manifest 描述，不删除 dirty/失败证据或覆盖唯一可救副本来恢复资格。

Rsync 恢复必须找到当前配置 fingerprint、node snapshot 相符的成功普通备份及 verified capture generation。`000083` 记录 directory-self、directory-content、single-file 布局、由源端选择的 manifest 和恢复 source-run 绑定；不能统一补斜杠或换根重套排除模式，不能把节点同名路径当 Core 来源。缺源、枚举/哈希失败不是空成功备份。

恢复先在 Core 私有临时目录复制被选来源，验证暂存的内容、文件类型和 link target，完成前不创建/修改节点目标；传输仅用 verified staging，不重读可变来源。使用内容比较修复同大小/mtime 的损坏，不因此删除节点文件；恢复后仍核对 Core 与节点证据。空间不足或校验失败在目标写入前结束。

v2 manifest 对路径、逻辑根、link target 用 Base64 保留字节，哈希和路径分离、链接名和目标分离；DB root sidecar 按版本编码，不把非 UTF-8 文件名写入 PostgreSQL text。中文、换行、非 UTF-8、反斜杠、字面转义、链接箭头及目标末尾换行不改变身份。v1 manifest/raw root 继续按 v1 读取，不重写/补猜历史；旧 writer 不理解 v2，升级前排空。

取证独立于可选 policy sampling，`verify_enabled=false` 不授权无证据代次。取证上限/失败本身不阻断普通传输，但完成后只能 warning、不可自动恢复；只读 capture 期间取消且从未开始写入不弄脏前代。真实写入失败/中断保留不确定代次，不能借旧 success 背书。

Rsync 取证命令失败须保留失败阶段、底层原因、退出码（已启动进程时）及有界、脱敏的 stderr 诊断；任务历史和日志应能区分源文件消失、权限拒绝、空间不足、启动失败及取消/超时，不能统一抹成 `evidence copy failed`。错误链保留供调用者识别；stdout 文件清单不作为错误详情。输出超限须明确标注，凭据及完整或被截断的 PEM 私钥仍须脱敏。

## 旧版 Rclone 可变代次

Legacy Rclone 没有 immutable snapshot，也不使用 Rsync manifest。Prepare/前置条件完成后，在调用变更 executor 前才持久化 `writing`；明确失败成为 `dirty`，模糊完成、crash 或未知停止保留 `unknown` hold，完整成功才成为当前 `verified` head，仍不代表历史对象版本。

只有明确未启动证据能写 `no_start`：含 Prepare 失败/取消及可证明的 dial/lookup/start 前失败，也含同 owner 已 running 但未调用 Provider 的证明。不能根据 canceled 状态或错误文字回填；restore 只跳过可证 no_start，不跨越模糊历史 head。SSH 取消请求停止、必要时宽限后关闭所属 transport 并等待，但连接关闭/lease 到期不是远端 writer 已停止证明。

未决写按不可变 resource identity 划定冲突域；共享 Remote/namespace/node 证据的其他 Task 也阻断，独立资源可运行。缺可证 resource key 的历史保留节点/资源不确定 hold，不从后来配置或标签补猜；改配置或另建 Task 不能绕过。

管理员 `POST /tasks/:id/reconcile-legacy-rclone` 仅记录操作员确认，不负责远端探测/停止。必须有 task-write 权限、暂停任务、精确 `task_run_id`、`remote_stopped=true` 和 1–1024 字符安全 reason，实际确认原/外部 writer 停止并保留独立副本。事务拒绝 live/unbounded owner、active sibling、不匹配或非 eligible writing/unknown；放弃的 active writing 还要求明确 expired lease。按 Task→TaskRun 锁定，fence 旧 owner，必要时结为失败，改选中代次为 dirty 并原子保存 actor/confirmation 审计。审计失败回滚，原诊断保留；不自动恢复、重试、选旧 success 或标 verified。逐条清除 hold 后显式重新运行完整备份才可建立当前恢复来源。

历史 cleanup 在同一事务锁 Task 并重核谓词，保留每任务/节点最新非空代次（含 dirty）、所有未决 writing/unknown、restore source binding 和 active drill source；活动 successor 不能淘汰前一 final generation，避免 no-start 清除临时 dirty 后丢失前代。预约在同锁下重新解析来源；不能清除较新 dirty 令旧 success 复活。仅无引用且被替代历史按 retention 清理。

## 恢复演练准入与证据

**当前生产演练传输未启用。** `POST /policies/:id/drill-trigger` 返回 unavailable，不创建演练 TaskRun、不启动计划演练；UI 不提供手动触发/启用执行入口。以下是现有存储/准入/证据和未来启用仍须满足的合同，不代表生产执行已验收。

演练 reservation 对 source/sandbox ID 排序去重，同事务锁全部节点边界、检查两个 Recovery lease 并建立 TaskRun/Evidence。执行入口从 immutable TaskRun 获取 source，锁相同节点集、复查 lease，并原子 pending→running 与 evidence start。Recovery 准入拒绝任一端上的 active drill；分裂 pair 保持关闭直到持久调和。source=sandbox 只锁一次；无 admission 实现/节点边界即 unavailable，即使 transport 存在也不例外。

`restore_drill_evidences` 每 TaskRun 唯一，身份含 policy/task/run、可选 source run/snapshot ref、sandbox node/name/path。保存 top-level status、failed step、confidence eligible、start/finish/duration，以及 restore、verify、post-verify、cleanup 的独立状态、时间及脱敏错误。policy `latest_drill` 仅扫描摘要，TaskRun detail `drill_evidence` 返回结构化详情；非 drill 或历史无记录返回 null/省略，不能 500。

只有完整成功且 cleanup 成功或跳过可 `confidence_eligible=true`；failed/canceled/pending、post-verify/cleanup 失败及异常终止均 false。危险 restore path `/`、`/etc`、`/usr`、`/bin`、`/sbin`、`/boot`、`/dev`、`/proc`、`/sys`、`/run`、`/var/run` 及其子路径在 restore/cleanup 前拒绝；记录对应 `restore_path/cleanup_boundary`。restore、pre_verify/verify、post_verify、cleanup 失败保留独立 failed step，不伪装成功。

operator 触发和读证据须同时拥有 source task node 与 sandbox，共享 policy 不授权未拥有 source。`allowedSourceNodeIDs=nil` 仅表示内部/admin/cron 不限；非 nil 空数组表示无授权源且失败关闭。只拥有一端不得泄露另一端 TaskRun ID/证据。

证据写入和响应都脱敏，包括 legacy task/task-run log、detail、WebSocket backfill；命令 helper 对外包装错误前隐藏非空 stdout/stderr。前端映射 `latestDrill/drillEvidence`，保留 trigger type `drill`，详情必须加载 detail endpoint，不拿列表缺字段宣称无证据；缺字段安全回退，phase/i18n 同步。

配对 000074 保证同 task 最多一条 `pending|running|retrying` Drill；历史重复在 dirty 前原子拒绝，TaskRun/Evidence 成对核实修复后可重试，used-down 不可删证据。回归须覆盖两端权限、危险路径、全部 phase 失败、无 evidence、真实详情加载；双引擎 reservation/start 与 Recovery 两顺序竞态要求持锁的真实阻塞证据：SQLite BUSY/LOCKED，PostgreSQL Lock wait 且 `pg_blocking_pids` 含精确 holder。共享 start channel/预置 lease 不代替并发证据；预约后出现的 out-of-band lease 与正常 Recovery 准入分开测试。

## 完成事实、RPO 与回归

freshness 只来自可证 legacy transfer completion 或已提交、严格 Task/TaskRun/node lineage 和 Provider evidence 的 managed RecoveryPoint。普通 command/维护成功、pending/warning/失败、restore/drill、imported baseline 及不可证明历史不建立 verified backup completion。`000087` 清旧 `Node.last_backup_at` 投影后只从可证 committed point 重建，未知历史保留 unverified 引用，不猜测/丢弃；node health 按 `(completed_at,id)` 在 DB 选择每授权节点最新 verified completion，不删 immutable history。

SLA 报告的 RPO：每个设置正数目标的策略，合并其关联任务最近 20 条 verified completion，按 `completed_at` 排序，取相邻最大间隔的整数分钟；不足两条为未知，不可当零且该目标不达标。RTO：该策略最新成功 `trigger_type=restore` 的 `duration_ms/60000` 整数分钟。报告 actual 取有关策略最差值，全部有目标策略都 `actual<=target` 才达标，无任何目标返回 null。当前实现先用报告节点作用域选策略，再读该策略所有任务；这两项不按报告 period 起止时间筛选，也不是距现在的 freshness。这些事实来自 `reporting/generator.go`，不能再描述为 TaskRun started_at 的间隔。

升级前备份数据库、密钥和独立备份树，停止并排空旧 Core/executor/publication/reconciliation worker；同 migration version 也不能证明旧 writer 可理解新证据。used schema 禁止以删除事实、强制版本或旧 binary 降级绕过 hold。后端 focused 回归覆盖 CAS/override/显式零值/导入补偿、cron 去重/跳过/重启/配额/链式 effect、immutable snapshot、旧版 capture/byte path/staging、Rclone no-start/未知/跨任务 hold/原子 reconcile、cleanup 来源引用、完成事实与 RPO；涉及持久化竞争须双引擎与 race，前端覆盖配置冲突、安全 DTO 和证据 detail。实现验收、CI 与真实部署恢复各自举证。
