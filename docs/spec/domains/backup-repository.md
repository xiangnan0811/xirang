# 备份仓库与恢复点发布

本合同覆盖 `backend/internal/backupasset/{provider,repository,publication,runtime}`、`fileaccess`、任务执行器以及版本化 API。保留与物理删除由[生命周期](backup-lifecycle.md)拥有；有效开关由[启用条件](backup-enablement.md)拥有。

## 包依赖与执行器映射

| 包 | 唯一职责及依赖边界 |
|---|---|
| `backupasset` 根包 | 只拥有领域类型，不承担运行时组装。 |
| `backupasset/provider` | 可依赖根领域类型；不得 import Gin handler、`task/executor`、runtime、Repository、Task 或 API 包。 |
| `backupasset/repository` | 编排领域服务和 Provider registry，消费类型化访问绑定。 |
| `backupasset/publication` | 定义与具体 Provider 无关的端口。 |
| `backupasset/runtime` | `cmd/server` 之下唯一的备份资产组合根。 |
| `fileaccess` | 独立于 Gin、GORM、model 以及 backup/task 包；不得查询数据库或读取 `FILE_BROWSER_ALLOW_ALL`。 |
| `sshutil` | 承载 Provider 与 task executor 共用的 SSH auth/dial/runner 机制，不在两侧重复实现。 |

Task executor 到 Provider 的映射只能在 `backupasset/repository/binding.go` 中发生。其他 Repository service 文件必须使用映射后的类型化 binding，不得根据 `Task.ExecutorType` 分支。扩展 Provider 时保持窄端口与此依赖方向，不能把 Task 配置解释散布到 Reader、reconcile 或 lifecycle 消费者。

包依赖和映射位置要有编译/源码边界回归：拒绝上述禁止 import，枚举 Repository 中 `Task.ExecutorType` 的分支并证明只在 `binding.go`，检查 `fileaccess` 无数据库和 `FILE_BROWSER_ALLOW_ALL` 依赖。仅接口行为测试不能替代这类边界检查。

## 身份、密钥与读取边界

- `entry_identity`、`recovery_cleanup_ownership`、`rclone_preflight_identity`、`search_token` 是安装稳定密钥域，`Keyring.Rotate` 必须拒绝。KEK 轮换只能重新包裹，不能改变密钥材料。丢失时不得静默生成替代；Entry ID 支撑深链接与用户引用，更换需要明确兼容方案。Rclone 预检身份独立于可重建的 Search 域，用于持久 binding/历史 attempt 的私有身份摘要，不允许 rebuildable 替换或 lost 转换。Runtime 无论 Search 是否启用，均先重新包裹再初始化该域；缺表、lost、历史无 active 或解包失败拒绝启动。完全删除某域全部历史无法与首次安装区分，恢复仍要求匹配数据库与 KEK。Search、Entry、Cursor、Audit、Derived、Export 和预检身份域不复用。
- Provider locator 和 access binding 只在进程内部使用并保持 `json:"-"`。HTTP 只接受不透明 Xirang ID，不接受路径、remote、仓库字符串、shell 片段或凭据。
- Restic 使用完整原生仓库/快照身份。可变 Rsync/Rclone 使用 Task 范围、带盐且域隔离的 HMAC 身份，不能跨 Task 自动合并。共享 Restic 仓库也不合并所有权。
- 保留的 active binding 必须仍指向未归档 Task，当前 Provider/Node 血缘须匹配加密绑定。漂移在 probe 前拒绝，保留原 Repository/link/binding；替换必须指定精确 Repository 并显式 `replace_access=true`。
- 窄端口为 `RepositoryProber`、`PointLister`、`EntryLister`、`EntryStatter`、`SequentialReader`、可选 `RangeReader`。每次读取携带绑定 Repository、capability revision、source revision 和私有访问的 `ReadSnapshot`。分页有界并签名；顺序读取要求 `MaxBytes>0`，Range 要求非负 offset 和正 length。
- 本地严格读取使用 `fileaccess.Tree.List/Lstat/OpenRegular/OpenRange` 的句柄相对约束，不跟随 symlink，不得先验路径后自行打开。SFTP 采用严格相对 locator，在打开前、打开时、结束后验证 containment/type/source；已验证 SSH/SFTP 服务端仍是基础设施信任边界。
- 绝对路径、NUL、dot-dot、歧义 locator 在 I/O 前拒绝；特殊文件只可按类型列出，不能读取。源/根/对象在读取期间改变返回 `mutable_source_changed`，不能把部分内容作为成功。
- Provider 命令仅使用服务端固定工具/操作枚举、独立引用的参数和有界输出/记录/条目/时间/并发。Restic 密码使用一次性秘密 stdin；Rclone bound config 使用下述命令独占临时文件。流持有许可直到进程 join、配置清理及 close 完成，并传播 wait/cancel/limit/invariant/cleanup 错误。取消必须关闭自有句柄/会话并 join goroutine；本地 join 不等于远端已退出。
- Restic 密码只通过一次写入 `/dev/stdin` 传递，不能进入 argv、环境变量或临时文件。Restic snapshot `latest` 或前缀在命令执行前拒绝；Rclone Range 语义未证明时保持 `OpenRange=false` 并返回 `range_unavailable`，不能由通用 reader 猜测支持。
- `provider_operation_timeout`、`provider_max_concurrency`、`provider_metadata_limit_bytes` 从 `backup_assets` 动态配置读取；`RESTIC_BINARY`、`RCLONE_BINARY` 是重启生效的可执行文件选择。SSH purpose 为 `repository_probe|repository_list|repository_read`。
- 注册读适配器不赋予写权限。发布与 `PointDeleter` 必须单独注册并经过各自准入；远端 Rsync 目标没有独立目标凭据合同时保持 unsupported，不能假设可复用源 Node 凭据。
- 读适配器不能触达 Restic mutation 命令或 Rclone/Rsync copy、sync、delete、restore、publication 路径。关闭功能的仓库数据面在 Task/DB/keyring/audit/dial/command 副作用前拒绝；始终在线的归档与明确维护例外由各自合同授权。
- 可变 Rsync Repository 维护一个稳定的 observed head 并原位更新；disconnect 保留 observed/offline 事实，reconnect 推进 capability revision，不能把重连误作新的历史快照。

## API 能力错误

能力原因必须通过 `ValidateCapabilityReason`，禁止把任意 Provider 文本放入参数。未支持 Provider/端口或缺少 Task artifact 映射 501；关闭、离线、断连、超时、资源上限映射 503，并附安全 correlation ID。输入错误为 400，Operator 无权与不存在的仓库同为 404，身份/绑定/状态冲突为 409，其他 DB/crypto/SSH/protocol 错误为通用 500。

`respondBackupCapabilityError` 只接受 501/503。内容 ticket 的更窄原因白名单见[内容交付](backup-content-delivery.md)。失败 probe 仅更新安全 offline/reason，保留最后成功身份、mutable observation、fingerprint 和时间。超预算不能返回部分成功；cursor 签名、范围、revision 或列表指纹漂移不得猜测续页。

## Restic 精确发布

- `publication.Coordinator.Prepare` 先产生已准入 execution；只有 `RecordProviderCommit` 可把 `preparing` 变为 `verifying`，`Defer/Reject/Fail` 不得代替提交证据。
- 自动恢复点只来自最终成功 summary 的完整 snapshot ID 和精确 Task/TaskRun 两个 tag。禁止 `latest`、前缀、时间窗口、仓库 diff、legacy index 推断。检查 `original`、原生身份、时间和 capability 漂移；已经记录的 locator 不被替换。
- TaskRun 传输成功、Provider 提交和数据库发布是独立事实。已知 exit zero 但 summary 缺失/重复/非最终/坏格式时保留传输成功，按稳定原因延迟发布；仅可从唯一有效存储 summary 恢复。
- 有界异步 reconciler 校验完整 Manifest 和最低验证后才进入 `committed`。Manifest 激活与所有状态写入在同一事务检查当前 lease fence；点的绝对 deadline 不可延长。部分/不可用清单仅为 inactive 诊断，不可浏览，也不投影 count/digest。
- publication/lease 交界按数据库实际表示继承绝对期限：PostgreSQL 将原 UTC 期限向下截到微秒再作精确比较，SQLite 保留纳秒。原签名 lineage/attempt/marker 不改写，既有 stored deadline 不延长，接管只旋转 fence；原 point 到期判断仍使用原签名期限。真实跨微秒漂移（SQLite 任一纳秒漂移）拒绝，不使用容差。
- publication 的显式期限历史只比较同一恢复点的 publication holder，跨发布阶段仍不可改变；其他显式消费者按恢复点、holder 与 owner 比较自身历史，已释放的其他作业不限制新 Recovery job 的独立短期限。同一 scope 的历史期限、takeover 围栏与到期拒绝保持不变，零期限请求继续采用既有服务配置预算。
- 共享 Restic 仓库保留 Repository 范围绑定，但执行访问必须来自当前链接 Task 的 Node/config；不得借另一个 Task 的 node、secret、locator、ID 或审计上下文。
- 每条 Restic 命令在凭据/SSH/stream/handle 前取得 generation admission token，直到 join/close 和响应或状态投影结束才释放。切换开关须排空后持久化。
- managed history 是永久保护：安装级或任一当前 Task 活跃 Repository link 的 tombstone 均阻止关闭模式下 `legacy_backup`。禁止无 tag fallback、`restore latest`、仓库级 anomaly selector 和无 tag `forget --prune`。回退和调和不删除 Provider snapshot。
- 配对迁移 `000063` 保护 native snapshot、publication、producing-run/native-source 唯一性；有 durable history、tombstone 或 active publication lease 时不能破坏性降级。

## Rsync 版本化

legacy mutable 不自动升级；管理员暂停任务后显式选择 `versioned_hardlink` 或 `versioned_full_copy` 并运行预检。首次合法预检创建或重验 `<target>.xirang-rsync-v1` 受管根（`repository.json`、`staging`、`points`），并验证挂载、硬链接、原子提交、容量、inode 和路径安全；硬链接不满足时不得自动降级。激活重验只读打开既有受管根与控制目录，受管根或控制目录缺失、移走或标记漂移均返回 409 且不得重建目录或修改持久状态。

硬链接模式先传输保持源硬链接分组的完整 staging 树，再按内容、元数据和 inode 分组兼容性复用上一 committed 点，不能用 `--link-dest` 把旧链接关系带入新点。新建/断开硬链接关系必须准确保留；不兼容组用独立文件，前一点不能被修改。容量和流量按完整 staging 计算。

迁移显式选择新完整 baseline 或从下次成功运行开始。baseline 复制旧目标到新受管树，不原地标记、移动或硬链接旧数据；后者不追认历史，首个硬链接点仍为完整种子。激活后保持暂停，传输、目录提交、发布分别取证。受管目录不是 WORM，也不是源端时间点快照。

完整 commit/manifest/tree 认证成功后，Rsync 发布事务同时将点置为 `committed` 和 `online`；preparing/verifying、缺失或漂移的树不得据此发布 online，不为不可变点伪造 mutable `observed_at`。

committed 本地 strict tree 的范围读取具有真实非零 offset 支持；每次 operation/binding/path/range 及关闭时的 tree 验证保持。PostgreSQL 的持久 `captured_at` 与 Catalog mtime 只在数据库表示边界按微秒核对，原 commit 摘要和文件 manifest 仍认证完整纳秒时间。内部文件 open/close 重验保留原物理 mtime，向内容 grant 返回持久 Catalog 时间；同微秒内的物理漂移及持久时间变更仍拒绝。SQLite 继续精确纳秒比较，不推广到 mutable 或其他 Provider。

`task_revision` 与 `expected_task_revision` 必须是非零规范无符号十进制字符串，不能转换为 JavaScript number。preflight、activate、rollback-preparations 均使用 CAS。激活/回退准备提交后重新加载并返回持久化 summary；同一对话框后续请求使用最近响应 token，reset effect 依赖初始 prop revision，不能抹掉新 token。坏 token 或未知 mode/state/reason 禁止操作；不从 executor config、日期或旧 prop 猜测。安全 summary 不含根路径、locator、marker/manifest/fence digest、命令、输出或凭据。

准备回退停止准入并排空，恢复保存的 legacy locator 后仍暂停，保留所有已提交点与保护记录；受管历史不能因此重新获得 mutable fallback。配对 `000064` 的 history、versioned link、active lease 阻止 used-down。

## Rclone 版本化与加密

- legacy 默认 `legacy_mutable`。Portable `versioned_prefix` 为独立 namespace，每次运行规范化清单并最后写 commit marker；弱/无哈希须逐字节核对源与目标，超过字节/时限失败关闭。
- 受管 Portable 使用加密持久化的同一份 bound config bytes/revision；当前运行时必须精确通过 `rclone v1.74.4` 校验，旧版、新版及 prerelease 不能自动视为兼容。`node_default` 只属于 legacy；不自动导入节点配置。拒绝动态凭据、未知选项、未认证 backend/wrapper 和不闭合依赖。变更版本须同步路径 codec 和实际适配器验证，不从命令名称推断支持。
- Native 仅限官方 AWS 区域端点的通用 S3 bucket；directory bucket、access point/Outposts、自定义/S3-compatible、Azure/GCS 不可冒充已认证 Native。实际 live 认证状态与支持矩阵必须分开报告，存在 opt-in 测试不等于通过。
- Native 使用同账户专用 IAM Role、信任策略强制 external ID 和覆盖单次操作时限的 STS 会话，不能用节点静态身份替代绑定。external ID 只在短期 setup 显示一次。两次 versioning/lifecycle/身份/capability 稳定观察至少间隔 15 分钟，并通过精确版本 canary。
- Native STS `AssumeRole` 的 `SessionTokenSize` 与 `SessionTokenUtilization` 都是可选指标；存在时前者必须为正且精确等于会话 token 的字节长度，后者必须落在含端点的 `[0,100]`。任一指标缺失不阻止准入；异常指标按 `credential_invalid` 拒绝，并在 external ID 否定探针与 bootstrap deny probe 之前检查。不得由 size 推算 utilization、另加固定 4096 字节限制或恢复已移除的 packed-policy 准入条件；会话 token 仍遵守既有有效性边界。
- 预检绑定 Task/binding/credential/capability/lifecycle/encryption revisions 和有效期。任一漂移必须重做，不静默降级 Portable/mutable。
- 候选 generation、预检票据和 activation reservation 分别管理：同候选并发预检直接冲突，新候选可替换仍在观察的旧候选，但迟到结果不可安装。reservation 在候选到期后仍保留到 owner 退出；consume 的票据失败也不复活。provider I/O 不持协调锁或数据库锁，提交按 Repository→Task→TaskRun→link→binding→Node→SSHKey 重验持久快照及事务内 publication 配置。
- Native 稳定窗口属于同候选和完整身份/revisions，首次及漂移重置时间取外部观察完成之后。未满15分钟仅投影 `capability_settling`，不产生可激活证明；重新打开摘要保留当前临时进度，进程重启后票据/窗口全部失效。候选 TTL 不延长，operation deadline受候选expiry限制，ready票据还受STS有效期减2分钟限制；初次剩余时间不足完整窗口与收尾余量直接拒绝。
- Native 仅支持 `sse_s3` 或同账户 customer-managed `sse_kms_cmk`。后者有一个 active write key 和有界 decrypt-only read key ring；先保留旧读 key 再切写 key。任何 committed VersionId 仍引用旧 key 时必须保留可解密性。校验账户、区域、状态、用途、来源和权限，DTO/日志/审计不公开 ARN。Xirang 不创建、修改或删除 KMS key。
- 与 namespace 重叠的 current/noncurrent expiration、delete-marker cleanup、未知 lifecycle 动作或未认证离线转换都阻止准入；不自动修改 bucket lifecycle。Native `backend_versioned` 不等于 Object Lock/WORM。
- 预检命令的本地/SSH staging 与远端 proof 是不同的所有权。取消会关闭并 join 自有 SSH/SFTP 会话，包括握手、写入和 Remove 停答；清理使用脱离 caller cancel、至多 command join timeout 的独立期限，主错误与清理错误均保留，清理失败不得返回成功证据。Stage 未返回 ref 时，只能在新有界会话重验 home/root/owner 后删除本次 O_EXCL 确认创建的精确 regular payload；碰撞、symlink、身份不明和同 attempt 其他文件保留。Portable proof 与 Native canary/version/delete marker仍按独占ID保留，不借 staging 清理广泛删除远端历史。
- `first_new_point` 不改变旧目标，在任何 managed reservation/history/point/lease 之前可 clean rollback；`imported_baseline` 完整复制稳定旧 current head，并立即建立 durable reservation，不伪造历史，因此没有 clean rollback 窗口。窗口关闭后仅准备回退，保留 committed/failed/orphan/manifest/audit/latch，不删除远端字节、不自动恢复旧执行。

### 发布结果与未知 writer 保护

Rclone publisher 以专用类型化错误向 strategy、executor 和持久化 owner 传递发布事实，
错误文本固定且安全，原 cause 可通过 `Unwrap` 保留。事实区分 publication mutation
是否尝试、commit mutation 是否尝试，以及全部已尝试 writer 是否停止且不会再启动后续变更。
未标记、缺失或矛盾事实按 unknown 处理；只读预检失败可以明确属于未尝试发布变更。
不能从任意 error、本地 join、EOF、TERM 成功、lease 到期或认证 commit 推导 writer 已停止。
SSH 的 RemoteExited 独立于真实正数 exit-status；Start 报错可能已发请求。
Native SDK 在实际 PutObject 调用边界区分本地拒绝，成功且有效 VersionID 才证明该次写入完成。

明确从未尝试发布变更时 Reject 并保留原错；commit 前全部 writer 停止且真实正数退出时
按 provider_nonzero_exit Fail。全部停止但仅有语义/验证错误时，Rclone 专用
provider_completion_unproven Fail，exit=-1，不伪造非零退出或放宽 Rsync。
transport/SDK 变更结果未知，或 commit mutation 已尝试但提交/回读未确认时 Defer，
SuppressRetry；停止事实独立保存，未知提交不能重放旧 attempt。普通任务和导入 baseline
共用此分类。允许的重试必须创建新 TaskRun、point、attempt 和 root，旧数据不变。

PublicationConsistencyV1 的可选 `rclone_writer_quiesced` 由 publication.Deferral 传递；
nil/false 均不授权解除 writer 保护。写该事实时同事务设置 provider=rclone，旧合法
provider 空行仍可解码。Reject/Fail 的终态与正面停止事实同事务持久化，Defer 幂等比较
包含该事实；提交与 reconcile 不能丢弃尚未证实的 writer 约束。

绝对 deadline 到达（含相等）时，未证实停止的 Rclone 点保持 preparing/verifying、locator、
signed attempt 和原期限，持久 outcome_unknown/publication_deadline_exceeded；不续租、
不调用 provider、不激活 manifest、不复活 attempt。重复处理无 I/O、revision/attempt 增量
或新 lease。SQL LIMIT 前排除精确过期 hold，避免后续候选饥饿。已证实停止才可沿原期限
规则终止；提前终止还需权威 incomplete 证据。此保护不使用具有独立语义的 Retention HoldState。

Runtime 在任何 admission/worker 和 disabled early return 前，以及运行中重新启用时，
有界 keyset 检查 repository.provider_kind=rclone、failed、semantics 为 xirang_manifest
或 imported_baseline 的全部历史点。没有版本化正面 writer 停止事实，或 DB/codec 错误，
均拒绝就绪；保持原 failed 和数据，不回填停止证据。失败码、TaskRun 终态、年龄、释放 lease
及后来成功均不是停止证明。这可能使有历史 failed 点的安装无法启动；未知非终态 writer
到期后持续阻写，也可能按全局 readiness 阻止重启。本轮没有自动恢复或解锁能力。

该可选 JSON 字段是持久格式兼容变化：升级前必须停止旧 owners，禁止新旧 writer 混跑；
写入后不能直接用旧 binary 读取。删除字段或恢复数据库快照不能证明远端 writer 已停止。
历史 committed 点不因上述 failed 库存而被宣称已恢复所有丢失的退出证据。

### Rclone 命令配置所有权

所有携带 bound config 的普通读取与受管命令，经执行 Node 的同一认证与审计上下文使用 SFTP，将不超过 64 KiB 的原始字节写入规范 home 下 `.xirang/rclone-command-config/<随机操作ID>/rclone.conf`。每命令独占目录和父目录为 0700，配置与随机 owner 标记为 0600；成功写入、关闭并重读校验大小与 SHA256 后才启动命令，不追加末尾换行。操作目录由原子创建取得，不复用 publication attempt 或长期节点配置。Rclone 可在该目录正常改写、备份及原子替换配置；数据库 bound bytes/revision 不回写，也不跨命令缓存探测结果。

执行 Node 必须提供 SFTP 和可写、可验证权限的私有 home；只有 exec 的节点不能执行 bound-config 操作，缺失前提时安全返回 provider-unavailable，不回退 stdin 或节点默认配置。`node_default`、不携带配置的普通 version 和 Restic 不新增这一前提。秘密不进入 argv、环境变量、shell 脚本或命令替换，命令路径仅由当前配置 owner 装配，调用者不能提供 `--config`。暂存、SSH dial 与命令执行共享入口的绝对期限，取 caller、operation timeout 与 invocation deadline 的最早者，不在暂存后重置预算。

携带配置的 Run/Open/OpenExecution 统一使用专有 SSH client 的 joined execution。取消先请求 TERM，等待既有 100ms grace，再按需关闭 transport 并 join 所有本地 owner。只有尚未尝试 Start，或 Wait 已实际返回正常 exit-status/exit-signal，才允许清理；Start 结果不确定、EOF、断线、发送 TERM 成功或本地 goroutine join 都不能作为远端停止证明。prefix 主动终止仅在已观察到相应远端退出且无其他权威错误时接受，普通取消、超时、限额和已有失败仍为失败。

收尾在命令和 transport join 后重建 SFTP，使用脱离 caller cancel、至多 10 秒的清理期限；许可与原 staged operand pin 保留到收尾结束。每次重验规范 home、真实目录、0700 及 owner 标记，至多读取 128 项并取得 EOF，全部验证通过才删除。仅允许 0600 普通非 symlink 的 `.owner-v1`、`rclone.conf`、`rclone.conf<十进制后缀>` 与 `rclone.conf.old<十进制后缀>`，后缀长 1..20；不比较已改写配置的原始摘要，不递归、不跟随 symlink。先删配置和临时备份，再删 marker 与空操作目录；删除中途失败保留错误，不能声称原子全清理。

清理失败使 Run/Close/Join 失败，不能产生成功内容请求或可激活预检证据。退出未知、owner/root 无法重验时保留目录并返回安全失败；不按年龄或 PID 自动清扫。正常执行期间节点有短期明文配置，崩溃、强杀或失联可能遗留，不承诺崩溃后零残留或磁盘安全擦除。人工处置须先停止并确认相关远端执行已无使用者，再核对精确目录；操作边界见[部署指南](../../deployment.md#rclone-bound-config-节点前提与异常处置)。


### Portable 清单与跨后端保真

Portable V2 清单将逻辑 path、物理 path、类型、大小及 symlink 目标保存一次，将源与目标的规范 UTC RFC3339Nano 时间、可用 SHA256 和实际 metadata 分别保存。源时间不截断为目标精度。两侧观察摘要分别由原 V1 canonical 投影及原 count/bytes framing 计算，可以不同；chunk/index 摘要仍认证实际持久字节。Native 源清单与已发布 Portable V1 的字段顺序、编码和摘要不变。

生成和读取均有界逐行处理，严格拒绝缺失、额外或重复路径、物理路径/类型/大小/链接目标差异，以及未知、重复、缺失、null、错版本或非规范记录。每侧前后观察必须稳定，跨侧内容证明不能替代单侧稳定性。两侧都有 SHA256 时不一致直接拒绝；symlink 的任一 SHA256 还必须匹配实际目标字节。任一内容项任一侧缺 SHA256 时，整点在预算内完成 `check --download --one-way`，之后重新观察两侧；路径集合精确匹配防止 one-way 漏掉额外目标对象。弱 hash 不作为强证明，部分验证、漂移、取消或超预算不得提交。

V2 内容证明绑定两个观察摘要，不把它们冒称两个独立计算的内容 hash。强 SHA256 为 complete 且 verified_bytes=0；download 为 complete 且 verified_bytes=logical_bytes；无 file/symlink 时为 not_evaluated、complete、0。保真分别声明两侧实际出现的 `gid,mode,mtime,uid`，保留集合仅包含全树存在性和值都相同的源字段；集合排序且空值为 `[]`。只有实际存在且证明通过的目录和链接才声明保留，空树不声明时间保留，硬链接拓扑不声明保留。V2 整条记录与 index 都占用原有预算，不扩大限额容纳新格式。

目标 `.rclonelink` 的特殊投影只用于 Portable DataRoot 的已认证 `--links` 观察：读取并验证目标字节后，允许缺 mode 或 regular mode 的目标编码投影为 symlink，随后仍须与源精确对应。源保留后缀碰撞、特殊文件拒绝及 Native 语义不放宽；目录负 Size 规范为0，非目录负 Size 仍拒绝。

版本准入是闭合组合：历史 Portable attempt 仅 runtime minimum/schema `1/1`，新 Portable 为 `2/2`；Native 仍仅 `1/1`。attempt 与 commit 的 mode 和 minimum 必须一致，未知未来版本失败关闭；这些检查不能替代原身份、HMAC、fence 和摘要验证。外层 commit、authenticated-control 与 layout 仍为 V1，持久 binding 不因新 manifest 升版。新 Portable manifest 的持久 generator version 为 `"2"`，历史和 Native 为 `"1"`。

发布、重启调和与 Catalog 复用同一严格认证的版本分派读取器，核对实际提交的 index/chunks；准备阶段的源 index 不能冒充发布后的双侧 index。源仍存在时核对源观察/count/bytes，重启后源消失时只依赖认证的 committed controls。旧 preparing attempt 不得重新写 V2 覆盖其 namespace；有效 V1 历史继续支持读取、调和和精确删除，不重写远端 controls 或数据库历史。

Rclone 的 Portable 与 Native 点仅在 authenticated commit 与完整 manifest 校验成功、激活 manifest 的同一事务中从 verifying 进入 committed，并将 physical availability 标为 online；准备中、验证中或验证失败不得提前变为 online。CapturedAt 保留 provider commit 时间，CommittedAt 为本地验证提交时间，immutable 点的 ObservedAt 仍为空。已提交点的重放不覆盖后续健康状态，不批量改写旧点的 availability；内容入口仍须检查当前可用性并执行实时对象验证。

写入 V2 后不支持直接降级旧运行时：旧二进制对 minimum 的检查不足以可靠拒绝新格式。回退必须恢复写 V2 前匹配的数据库、密钥和数据快照，或继续使用支持 V2 的版本读取；不得自动重编码历史或将新格式伪装成 V1。

### Portable 历史内容验证

Catalog 和内容准入复用同一已认证 V1/V2 清单。Catalog 仅投影源侧逻辑 metadata；内容读取逐项比对 sealed Catalog 的路径、类型、大小、时间、mode/owner 和 fingerprint，再使用已证明的目标物理时间执行 Stat/Open/close。只有 V2 可以区分逻辑与物理时间；返回调用者的时间仍是源时间，V1 继续精确相等规则，不全局忽略 mtime。

managed snapshot 的前后验使用 commit marker 身份及同一递归目标观察摘要，不与 mutable 的单层目录 fingerprint 混用；mutable 算法保持不变。精确对象比较和关闭后验不能省略。对象 locator 拼接保留根路径语义，区分 `remote:path`、`remote:/path` 和绝对本地路径，不剥离绝对路径的前导斜线。

历史只读操作不复用已过期的 publication deadline，也不改写 signed attempt。每次 Catalog/content 验证建立瞬态只读请求，期限取真实墙钟加 manifest timeout 与 caller deadline 的较早者；只允许读取认证 controls 和 DataRoot 观察，携带只读期限的写入、传输、字节核对和发布调和入口在副作用前拒绝。内容关闭时仍须重新加载数据库、认证清单并验证对象；managed Portable 使用已验证配置中的 manifest timeout，并受更早 caller deadline 约束，而非普通 cleanup 的五秒限额。取消后清理保持有界，admission token 在流 join 和验证完成前不释放；其他 provider 与 mutable 的五秒清理默认不变。

### 绑定 setup 与候选替换

Portable 和 Native setup 响应必须包含 `base_binding_revision`，表示生成 setup 时的持久绑定基准修订号。它是 `0..18446744073709551615` 的规范无符号十进制字符串，`"0"` 合法；缺失、空串、数字 JSON、符号、前导零和溢出均无效。前端不得转为 JavaScript number 或由候选摘要猜测基准；坏响应清空可用 setup，禁止继续发送配置或秘密。

setter 一次性消费 setup 后，要求请求基准、setup 保存的基准与重新加载的持久基准相等，并重验 Task CAS、完整快照和非 managed 状态。不匹配返回安全 409，既有 candidate/ready 不被替换，已消费 setup 不复活。候选摘要仍显示拟议 `base + 1`，不能作为下一次 setter 的基准。多个未消费 setup 可共存，创建 setup 不替换候选；成功 setter 在短协调锁内替换，清除旧票据与 Native 窗口，旧 I/O 不能回写，activation reservation 期间仍拒绝替换。

Native 保存 setup 时同时保存当时的 Task CAS；提交前验证 setup 有效期及当前 Task CAS，不能把旧 setup 套用到新修订号。Portable 连续 setup→setter 在首次等待后仍须验证当前任务和认证身份。关闭、身份变化、新 setup 和成功保存清除旧 setup；只有 setter 成功后才开始新候选预检。managed 绑定不通过 setup 轮换，同绑定续期入口不变。

激活响应中的持久 summary 和 Task CAS 优先于同任务、同身份下的滞后父级刷新；父级带来相同或更新修订号后可接管，但随后更旧响应不得退回已接受的 CAS。`first_new_point` 成功后仍可按最新摘要再次预检，并在真实 clean rollback 窗口内显示清理回退；`imported_baseline` 的 `preparation_only` 不显示该按钮。权限、任务、会话或关闭变化仍清空整个临时流程，不以强制显示按钮或放宽摘要解析补救坏响应。

CAS 高水位与父摘要快照身份分别判断：未变化的操作前父快照不能抹去成功响应，较低修订号不能回退；相同 CAS 下真正变化的 state、reason 或 rollback capability 仍须采纳，不能仅用 mode/Task revision/binding revision 判断摘要相同。同 CAS 的事实更新不应无故重挂表单或重发秘密。

### 同绑定显式续期

已激活 V3 任务继续受预检到期门控。管理员暂停任务且无 active TaskRun/rollback preparation 时，通过现有 preflights→activate 流程从持久绑定重新执行完整预检；不重新输入秘密、不换模式、仓库、配置或 KMS key，Native仍须新15分钟稳定窗口。响应必填闭合 `purpose=activation|renewal`；未知/缺失 purpose 在前端清空可用票据并阻断，不猜测激活意图。

续期只接受 `first_new_point`。事务从原V3文档复制，只延长 `PreflightExpiresAt` 并更新加密binding指纹/时间及 Task CAS；原proof、binding/config/credential/capability/KMS revisions、稳定能力观察时间、read-key ring、legacy policy和仓库身份不变。确定性能力漂移拒绝，随机canary不要求重复；expiry没有前进、票据过期/重放或提交失败均不得延长授权。旧point/attempt绝对deadline不延长，已过期attempt不复活，新publication受新expiry约束。

摘要先验证持久关联和rollback状态，再显示当前未过期且快照匹配的候选进度；临时ready只是待确认票据，不表示原授权已延长。没有有效候选时，持久expiry已到必须显示 `preflight_required/preflight_expired`，优先于已有committed点。UI续期隐藏迁移选项、固定提交策略并明确待确认；持久授权已到期但新票据有效时允许确认，settling/票据过期/未知purpose禁止确认，不自动轮询15分钟窗口。

rollback capability 独立于临时 publication state 和 expiry 投影，GET 摘要与续期预检票据均复用持久 clean-window 权威。已有 managed point、reservation、history 等关闭窗口的事实不能因过期、Native settling、renewal ready 或续期完成重新变成 `clean_available`；已关闭窗口保持 `preparation_only`，已准备回退保持 prepared。尚未产生上述事实的 pristine `first_new_point` 仍可清理回退。

### 安全摘要的闭合组合

| mode/profile | KMS 状态与数量 |
|---|---|
| `legacy_mutable` 或 `versioned_prefix` + `none` | `not_applicable`，0 |
| `native_object_versions` + `sse_s3` | `not_applicable`，0 |
| `native_object_versions` + `sse_kms_cmk` | `ready|degraded|at_risk|blocked`，非负安全整数 |

后端 `Validate/SafeRclonePublicationSummary` 和前端 mapper 都原子校验整个组合。坏值/缺失/未来值整体投影为 Native、blocked、unsupported_profile、SSE-KMS、blocked、count 0；不得逐字段修补。未知 rollback capability 单独保守为 `preparation_only`，不使其他合法字段丢失。私有 ARN、bucket、prefix、VersionId、config、digest 不上 DTO。

## 受控恢复权威与运行时发布

`RecoveryEligibilityAuthority.ObserveRecoveryAuthority` 与 `RecoveryAuthorityRevalidator.RevalidateRecoveryAuthorityTx` 分别拥有有界外部观察和 caller 事务内的耐久事实重验。流程为短事务 capture → 事务外 SSH/SFTP/Repository pinned-tree/Processing artifact 观察 → 短锁定事务 revalidation。外部 I/O 不得放 DB 事务内。

每次 issuance 最终事务及后续 effect 事务都重验在 owner 返回后仍可变的事实。opaque source namespace observation 保留私有 closed durable snapshot 和 transaction-only revalidator，覆盖 Task source、source Node、source credential；Repository source revision 不能替代它。sealed digest 还绑定 target/policy/finding/reserve/overlap 产品，format/JSON/error 不泄露 locator/credential/revision/proof/依赖原值。

Recovery 创建计划（含 caller-owned transaction 入口）在计算意图摘要前，将新签发的 preflight 期限按实际数据库表示固定：PostgreSQL 使用 UTC 微秒向下截断，SQLite 保留纳秒。合法重放使用原持久期限和完整身份，不改写历史计划或引入时间容差。preflight evaluator、生产 target 和持久服务共用注入时钟；目标及外部观察完成后重新取时，拒绝零值、回拨、未来观察和已到期 permit／证据。快照预算以开始时间冻结，提交与事务重验不能早于完成时间或越过原计划／快照期限。

持久工作流期限 D 保持为新计划固定的 `PreflightExpiresAt`，plan、snapshot、job、checkpoint 的期限等值保护不变。外部观察的 `ObservationExpiresAt` 是本次决定的新鲜度 F，不是工作流期限；不能把 F 扩写为 D，也不能将整个作业及 exact-mirror 确认窗口压成观察窗口。preflight 持久化携带原 sealed authority，经 adapter 同事务重验，而不是仅核 scalar；每次新许可和成功投影重新观察，并在 effect 事务中重验同一 seal、耐久绑定及当前 F/D。观察后、事务内及返回前重新取时，等于期限或回拨均回滚；原已签发 permit 继续受 grant、lease、fence、D 和既有操作前后身份检查约束。

job-bound 权威使用私有 current-target expectation，绑定当前 job、attempt/node/source fence、完整有序 checkpoint 历史及冻结 chain；source token 仅进入私有摘要。首次未 armed 的写入要求空历史与冻结目标；其后 in-place anchor 已完成时，以对应唯一 checkpoint 和 item 的精确摘要／长度验证当前文件；发布后未 checkpoint 的 anchor 仅在当前首个 pending create/overwrite 已通过真实 verification 后用于成功投影。原 preflight 身份不改写，chain 不充当 filesystem revision。内容验证必须有界完整读取并成功 Close，前后校验 canonical root、parent、安全 metadata 与文件稳定性；事务内重验不执行远端 I/O，也不保证观察之后远端从未变化。

isolated 的 preflight 和后续权威只观察真实 root 的 canonical 路径、祖先、权限、filesystem、容量及 overlap；保留 selected locator 的词法绑定，但不遍历根下无关同名文件或 parent。真正目的地仍是 `jobs/<jobID>`，owned workspace、authenticated marker、foreign collision、expected absence 和发布检查继续生效。旧 path-derived 计划／快照不重写或续期；新尝试创建新计划，既有明确授权的取消与 cleanup 仍遵守自身合同。

限制性 mismatch/unresolved/post-pause 记录只使用当前 durable fence、原结果分类及 CAS，不要求已经失败的目标满足成功权威，不签发新许可或推进成功 chain。完成 item 的补充 verification evidence 只能精确匹配既有 bytes、digest 和 checkpoint，不修改 terminal item。中断 adoption 在真实目标 verification 后重验当前权威及同一 durable handoff；覆盖还须核对 authenticated artifact tuple，删除须绑定原 consumed checkpoint/grant 并证明 final 与 captured 均已缺失。captured 未发布、标记矛盾、错误身份或观察失败保持 unresolved；此入口不盲重放写入、不替代清理、不新增 Runtime 自动 adoption。

in_place 的执行顺序从完整 canonical operation 集合派生：先按原相对次序执行非 delete，再按原相对次序执行 delete。canonical 排序、item ordinal、operation/delete-set digest、locator envelope 和 API item 顺序不变；checkpoint sequence 记录实际时间顺序。全体 item/operation 先核绑定，再按执行位置验证 completed prefix 与当前 pending anchor；required 必须位于首个 delete 前，consumed 紧随 required，所有写入先完成。旧 consumed authority 依不可变 checkpoint、原 grant/attempt 证明，不能把历史 node fence 强行与接管后的当前 node 行等同。

exact-mirror grant 的签发、pending handoff、消费及历史验证统一以 UTC RFC3339Nano 序列化同一期限。新 PostgreSQL grant 在生成绑定摘要及插入前向下截至数据库微秒精度；SQLite 保留纳秒。期限必须仍严格晚于事务当前时间且不超过原 checkpoint 上限，不重写历史期限、不引入时间容差或双摘要回退。

Repository restore request 由 WorkerCoordinator 在同一短事务内按 plan→job→preflight/grant→source→node→attempt→items/history 锁序构建，复用当前 canonical 来源和历史校验。未 armed 只接受 pristine/base chain，已 armed 按完整历史认证当前 chain；锁后及返回前检查时钟、lease 和原工作流期限。构建结果只表达解析意图，不授权远端写入；resolver、materialization 与 SSH 在事务外执行，first-write 和逐项 permit 仍独立重新观察、锁定重验。

仅在尚未调用 Repository 执行端口的构建失败，或已证明未 arm 的初始 source/snapshot/materialization 失败处，允许 `FailPristinePrewrite` 限制性关闭。事务须证明同一 running owner/fence、全 pending、零 checkpoint、workspace none、冻结起始 chain、无历史 armed 或接管者，并精确释放本 claim 的 source/node lease；同 owner 的过期 lease 只能关闭，不能续租或复活。先关闭 attempt，再将 job 标为 failed、分类 `pre_write_failed`；plan 保持 executed，items 保持 pending，原期限和已消费 grant 证据不变。source-drift supersession 保持原分类；已调用执行端口、可能 arm、已有历史或可能在途写入均不得走该快捷关闭。前端以“开始写入前失败”展示，未知分类继续拒绝。

公开 `pre_write_failed` 产品只能是 failed、零完成项和零写入字节；后端与前端 mapper 都拒绝其他状态或矛盾进度，不能把已经写入的产品展示为开始写入前失败。

enabled Runtime 从持久 isolated+succeeded/degraded+sealed 状态自动补发 ResultSet，重启不依赖内存通知。发布锁内完整重建成功 regular create/overwrite 集合与密封 locator；当前无秘密分类生产者，结果固定为 unknown/1/1，不代表扫描通过或非秘密。并发、提交确认丢失及重放须核唯一 set 和完整逐项一一对应，不重发 ID、不重置期限；缺失、多余、矛盾、active attempt 或无成功 regular item 不发布。取得锁后、CAS 前和事务返回前核原期限与回拨；不宣称 COMMIT 瞬间的墙钟保证，实际读取仍按当前交付期限拒绝。

workspace 的明文期限在首次写入时固定为 24 小时，与工作流期限和观察新鲜度分别管理；结果 retention 配置不缩短或重算该原期限。sealed 到期转换与发布互斥，且只在无 active attempt、无 ResultSet、认证 workspace 和中性 cleanup tuple 下原子变为 cleanup_due。enabled/disabled 均沿原 cleanup owner 的 claim→revoke→drain→验证→fenced 删除完成；disabled 不发布新结果。capture 或原路径缺失不等于删除成功，续行仍需有效 claim、认证残留与最终 absence 证明。两个批处理各持私有 high-water cursor 和独立批预算；LIMIT 只限制返回行及处理次数，不保证数据库扫描/排序工作量上界。graph 切换和关闭必须 cancel/join publisher。

workspace 与 ResultSet cleanup 的新 claim、接管及续租在计算 `now + cleanup_lease_ttl` 后按 PostgreSQL 微秒精度向下截断，同一新期限用于 node lease、job、ResultSet 和返回 claim；SQLite 保留纳秒。新 claim 的期限必须严格晚于未截断的当前时钟；续租仍须严格晚于原期限，若候选未推进则沿用原有增加一秒规则。普通阶段迁移与在途许可精确核对 claim 期限；显式 `delete_started`／`deleted` 续行沿既有规则，在 owner、cleanup fence／attempt、node identity／fence 和 phase 匹配且持久租约仍有效时，重载当前持久期限后校验与续租。marker 认证保持，不通过截断传入 claim、时间容差或改写原 workspace 24 小时期限放行。

作业查询中的删除确认是可选投影，只适用于in_place+exact_mirror。其他模式返回无删除检查点；合法running、尚未armed且没有任何检查点历史的镜像attempt也返回无检查点。仍须验证镜像attempt的live claim、source/node lease与fence；未armed却有历史、已有armed历史或tuple矛盾不可作为正常“尚无确认”隐藏。普通作业进度仍走完整item/state一致性校验，查询不签发或放宽删除权限。

enabled Recovery 仅当 Repository、target 和真正 ready 的 Processing malware evidence lifecycle 存在，且 metadata reconciliation 完成后才发布。Processing disabled/control-plane-only/stopped/无 installed reader 时不得发布 admission graph；非空对象指针不够。disabled Recovery 可不依赖 Processing 发布 cleanup/logical reconciliation/receipt reaping 等 maintenance，但不能 admission/effect。已发布后 reader 故障使 effect fail closed，maintenance 仍可用。

`StartupWithConfig`、`TransitionSettingsWithRestore`、`TransitionCurrentWithRestore` 必须保持 graph owner。current-config mutation 仅已启动并安装 config/graph 后可用，启动前在 validate/persist/build/publish 前拒绝，zero config 不能隐式成为 disabled installation。target-root facade 仅给安全 `RecoveryTargetRootSummary`，exact encrypted row/absence rollback 用 Recovery 私有 opaque token。

旧 graph shutdown/join 无法证明时 unpublish、清 graph、sticky fence，跳过 persist/build/install。root mutation 成功但 candidate install 失败时先恢复 exact encrypted row/absence 再重建旧 graph；任一恢复/join 无证明就保持 nil publication 与围栏。只有 persistence 和 graph 都精确恢复才可重新准入。

回归覆盖两处漂移窗口（namespace observation 后、sealed issuance 后）× Task source/Node/credential，断言每 owner 外部观察仅一次、事务仅 durable comparison、零私有 canary；覆盖 Processing disabled/control-plane/ready/reader-failure 的生产组合、pre-start/current/disabled mutation、validate/drain/persist/construct/reconcile/install 各失败、不可 join owner、exact row/absence restore 与 sticky failure。执行 SQLite、required no-skip PostgreSQL authority selectors、normal/race 及 privacy/static 边界检查。

受控恢复的精确删除在捕获目标并重验身份、marker与live permit后，通过生产SFTP的有界`ReadDir(1)`确认目录为空，删除后必须核对absence。非空、枚举错误或关闭错误不得投影删除成功；捕获已发生时保留captured目录及其子项，不将此失败描述为原路径从未改变。

SFTP v3 的合法 NAME 包可以仅含 `.` 与 `..`；有界读取器最多连续跨过两个这种无实际子项的包，再要求真实子项或协议 EOF。第三个无进展包、零条目的 NAME、坏包、overflow、传输或关闭失败继续拒绝；空包不能伪造 EOF，非空目录与前后身份校验不变。

取消先独立中断本次SSH transport，使目录通道打开、握手、读取或关闭不依赖同一资源锁才能退出；正常关闭仍先释放SFTP，再关闭SSH，并join在途目录初始化和tracked files。目录状态锁不跨网络初始化，同次初始化调用共享结果；关闭后拒绝新打开，晚到session/reader由取得它的调用关闭。文件目录读取与关闭状态串行，普通文件句柄可由关闭路径中断；重复关闭不重复释放，正常关闭完成前保持取消watcher有效。释放失败沿现有安全错误合同返回，不跳过后续资源清理，也不把本地join等同于远端未知写入已停止。

## 快照异常比较

Restic anomaly 在成功备份后的有界异步路径执行，不改传输结果。managed 模式只比较同 Task 精确血缘的相邻 committed 恢复点，不允许仓库 latest 推断；managed-history 关闭模式仍保持此边界。churn 使用排除当前 diff 的最近 10 条历史统计，至少已有 3 条历史 diff 后才比较均值加 3σ；勒索扩展名检查不等待该基线。事件默认记录，通知由 `anomaly.alerts_enabled` 明确控制。回归分别证明 insufficient baseline、当前样本排除、ransom 独立触发和共享仓库跨 Task 隔离。

## 回归证据

变更边界须覆盖：registry 未知/重复/缺端口；400/404/409/501/503/500 与隐私扫描；probe-first 无写入、绑定漂移、显式替换、断连/重连 revision、事务回滚与唯一性竞争；本地/SFTP traversal、symlink/race、特殊文件、root rename、Range、限额、取消和 close 错误；真实命令替身的严格 argv/schema/secret stdin 与无越权变更。

发布回归覆盖 Restic 最终 summary/tag/original、stored reconstruction、fence/deadline/backoff/worker shutdown、共享仓库隔离、Repository-only latch、所有 legacy 消费者；Rsync staged 硬链接关系和无旧点修改、预检后受管根移走/控制目录缺失的零磁盘副作用 409 拒绝、迁移 CAS 超安全整数及同窗 activation→rollback；Rclone 所有合法/非法摘要组合及 safe projection。双引擎迁移、必需真实 PostgreSQL、race/重复测试不得以静态文本或 SQLite 成功替代。

Provider 回归还须单独证明固定版本正例与旧版/新版/prerelease 否定、动态配置/并发限额变更、未知 Range 能力拒绝、可变 observed head 原位更新，以及所有 stream 的 close 错误和取消能到达最终 owner。

源码入口：[Provider](../../../backend/internal/backupasset/provider/registry.go)、[Repository](../../../backend/internal/backupasset/repository/service.go)、[发布协调器](../../../backend/internal/backupasset/publication/contracts.go)、[版本化前端边界](../../../web/src/lib/api/tasks-api.ts)。
