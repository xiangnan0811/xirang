# Rsync 版本化恢复点：Focused 技术设计

- 状态：用户于 2026-07-15 明确批准；本文仍不是 task.py start 或实施授权。
- 日期：2026-07-15
- 范围：Child 4，Rsync 版本化目录树的受控发布、兼容接管和对应的安全契约。
- 已批准的产品决定：
  1. 采用 paired 000064_backup_asset_rsync_publication_contract（方案 A）。
  2. legacy 接管仅可选 imported_baseline（新的 full-copy 发布）或 first_new_point（保留旧树、下一次成功才开始历史）。
- 证据：Rsync/POSIX/Linux 事实、实验和一手资料见 research/rsync-posix-publication-semantics.md；Child 1--3 的领域、read adapter 和 Restic publication 合同是本设计的既有依赖。

## 1. 目标、边界与术语

目标是在不伪造快照能力的前提下，让一个 Rsync producing TaskRun 把一次成功传输发布为一个新的、完整且由 Xirang 管理的 RecoveryPoint。版本化点不是 TaskRun 的别名，也不是对旧 mutable target 的重新命名。

所谓“不可变”仅指 Xirang 受控命名空间中已经 committed 的目录不被本功能修改、覆盖或删除。它不是存储 WORM：拥有底层目录写权限的外部主体仍可能修改硬链接 inode。Rsync 也不是源端时间点快照；应用一致性仍需要源端静默、LVM/ZFS 快照或其他上层机制。

本 Child 的边界如下：

| 负责 | 不负责 |
| --- | --- |
| Rsync managed tree 的 preflight、staging、commit marker、manifest、发布和恢复 | Rclone 版本化、通用 retention/purge、物理删除 Provider bytes |
| 基于精确 point ID 的内部 Rsync point read capability | Catalog/search/content API、文件浏览工作区、公开下载或受控恢复 |
| legacy Rsync 到 managed tree 的显式迁移/回退准备 | 由 mtime、目录名或旧 TaskRun 推测历史点 |
| 通用 publication coordinator 的严格 provider 扩展 | 第二套 publication 状态机、任意 JSON 的 provider payload |

本文使用以下术语：

- legacy target：现有 Task.RsyncTarget 指向的可变目录；它始终作为 encrypted legacy rollback locator 保留。
- managed root：与 legacy target 分离、只能由 encrypted binding v2 指向的 Xirang 控制目录。
- point：一个稳定 opaque RecoveryPoint ID 对应的最终目录。
- attempt：同一 point 的一次唯一发布尝试；任意重试有新的 attempt ID 和新的 staging。
- provider commit：managed root 内 final point 的原子可见性事实。
- DB publication：RecoveryPoint/manifest 行经 fence 验证后进入 verifying 或 committed 的数据库事实。
- transfer result：Rsync 子进程本身的结果；它不因 publication 成败被改写。

## 2. 既有合同与整体结构

Child 1 已提供 BackupRepository、TaskRepositoryLink、RecoveryPoint、RecoveryPointLease、加密 locator 和 UTC 模型。Child 2 已提供 Rsync mutable-head reader、encrypted binding v1 和 strict fileaccess 边界。Child 3 已提供 evidence executor、publication coordinator、point_publication lease、worker、reconciler、admission/generation token 及 Restic lineage guard。

Child 4 不在旧 RsyncExecutor.Run 的直接写最终目录路径上附加参数。该路径固定使用 -avz 并直接消费 Task.RsyncTarget，仍只服务 legacy_mutable。managed Rsync 走新的 provider strategy，旧 executor 永远不能取得 managed root。

~~~mermaid
flowchart LR
  T["Task manager / transfer result"] --> A["Publication admission + coordinator"]
  A --> P["RsyncTreeStrategy"]
  P --> S["unique staging tree"]
  S --> V["manifest + fidelity validation"]
  V --> R["renameat2 NOREPLACE provider commit"]
  R --> D["fenced DB publication: verifying"]
  D --> W["shared publication worker"]
  W --> C["committed RecoveryPoint"]

  L["legacy target"] -. "rollback locator only" .-> T
  M["encrypted binding v2"] --> P
  P --> M
~~~

### 2.1 责任分界

| 层 | 唯一职责 | 不能做的事 |
| --- | --- | --- |
| Task manager | 创建 TaskRun，执行受控 transfer，记录真实 exit/cancel/timeout | 不能把 managed root 写进 Task.RsyncTarget，不能用 legacy verifier 验证 managed attempt |
| Publication coordinator | point 预留、事务 CAS、fence、deadline、lease、状态推进和审计事件 | 不解析 Rsync 输出，不推测目录成功，不持有 provider-specific string map |
| RsyncTreeStrategy | preflight、argv 构造、目录操作、manifest/marker、provider commit 与精确调和 | 不决定通用 DB 状态，不暴露路径或命令文本 |
| Worker/reconciler | 按 point ID 和 provider strategy 继续 verifying/recovery | 不按“最新目录”、mtime 或 TaskRun 完成时间猜测结果 |
| Repository/read adapter | 仅在已 committed 的精确 point/tree 内受限读取 | 不把 mutable head 或任意子目录宣称为 immutable point |
| API/UI | 传递严格 mode、safe state 和稳定 reason code | 不接收 flags、root、locator、marker、文件名、命令输出或秘密 |

### 2.2 严格 provider 契约

现有 Restic 专有 PublicationAttempt、ProviderCommitEvidence、ManifestEvidence、ResticOperation 和 ResticPublisher 不能承载 Rsync staging/final/parent/fence 语义。实现时将把共享层改为 provider-tagged union，而不是添加 any、map[string]any 或自由 JSON。

共享层新增一个以 backupasset.ProviderKind 注册的 PublicationStrategy 边界，操作固定为 Prepare、Run/ObserveProviderCommit、ValidateCommit、EncodePointLocator、BuildOrVerifyManifest 和 ReconcileAttempt。每个操作都接收/返回共享的 opaque point/attempt/fence/deadline 记录以及一个封闭的 provider-tagged payload；它不能接收 map、interface 任意值或 JSON bytes。现有同名 Restic struct 会在这次重构中统一改为带版本后缀的内部 payload，而不是和新接口并存造成 Go 名称冲突。

具体 payload 只有 ResticAttemptV1 / ResticCommitV1 和 RsyncTreeAttemptV1 / RsyncTreeCommitV1。它们由显式 type switch 注册到 PublicationStrategy；未知 provider、schema version、mode 或字段立即失败关闭。序列化采用每种类型自己的严格 decoder，拒绝未知字段和重复/空 identity。公共层只保存必要的 opaque ID、摘要和 safe code；路径、locator、parent root、SSH 参数和原始输出只留在加密 binding 或进程内。

RsyncTreeAttemptV1 至少冻结：

- repository ID、TaskRepositoryLink ID、point ID、attempt ID 和 immutable child deadline；
- publication mode（versioned_hardlink 或 versioned_full_copy）；
- expected task revision、repository marker digest、managed-root identity digest；
- hardlink mode 的 exact parent point ID、parent commit digest、parent manifest digest；
- staging/final 相对 component 名称，不含绝对路径；
- expected command-profile version 和 preflight ID/digest。

RsyncTreeCommitV1 至少冻结：

- layout version、point/attempt/repository identity、mode、parent identity；
- canonical manifest digest、manifest algorithm、entry count、logical bytes；
- fidelity digest、managed-tree source fingerprint、provider-commit timestamp（UTC）；
- commit marker digest、child-fence digest 与 deadline；
- rename/dir-fsync verified 的布尔证据和 stable failure code（失败时没有 success evidence）。

fence 本身不会出现在公共 DTO 或日志中；marker 和数据库保存可比较的 keyed digest。验证需要的原值留在受控 lease service/加密证据中。

## 3. Managed root、身份与安全命名

managed root 与 legacy target 必须是两个物理上不重叠的树。显示形式如下，实际 root 只保存在 encrypted binding v2，绝不从 API 回显或在日志中打印：

~~~
<legacy-target>.xirang-rsync-v1/
  repository.json
  staging/
    <point-id>.<attempt-id>/
      attempt.json
      tree/
      manifest.jsonl
      commit.json
  points/
    <point-id>/
      attempt.json
      tree/
      manifest.jsonl
      commit.json
~~~

point ID 和 attempt ID 均由现有 opaque-ID 生成器生成，并在所有 decoder 中使用同一严格格式检查。它们不是用户输入，也不接受路径分隔符、点段或 URL 编码变体。staging 的目录名由二者以固定单个点连接；final 只能是 points/<point-id>，不存在可覆盖的“latest”别名。

repository.json 是 layout-versioned、canonical 的所有权 marker，包含不泄露路径的 repository ID、managed-root identity digest、layout version、创建时间和服务端 keyed authentication tag。attempt.json 描述准备中的 immutable identity；manifest.jsonl 是 canonical、字节排序、受长度/条数限制的目录清单；commit.json 是将 manifest、attempt、mode、parent、fence digest 和 deadline 绑定在一起的 canonical marker。所有 JSON decoder 拒绝未知字段，所有 marker 都经过 schema/version、canonical bytes 和 authentication tag 校验。

Provider locator 仍然加密保存。对于 managed point，它只编码 binding v2 identity、layout version、point ID 和 expected commit digest；读取根只能由该 locator 推导为 points/<point-id>/tree。它不接受直接的 path、任意 point prefix 或 legacy target。

### 3.1 受信路径操作

managed root 的所有控制面操作使用 root 的 trusted dirfd 和 Linux openat2-style policy：BENEATH、NO_SYMLINKS、NO_XDEV。创建、stat、link probe、fsync、rename 和 cleanup 均通过相对 *at 调用完成；不以字符串拼接后再信任路径。

preflight 与每次 attempt 都重新检查：

1. legacy target、managed root、staging、final、parent tree 与 source 的解析后物理路径不能互为祖先或后代；
2. managed root 及其控制目录没有 symlink、bind/跨 mount 意外或 root replacement；
3. staging 和 points 在同一可提交 mount；hardlink parent 与新 tree 位于同一实际 linkable mount；
4. 目录权限、owner 和 credential 能创建/读取要求的文件，且 final 名称不存在；
5. repository.json 与 binding v2 的 repository/root identity 精确相符。

source 中的 symlink 可以作为 symlink 对象复制，但 never follow 的 reader policy、未来 restore policy 和 manifest fidelity 都必须把它视为数据，而不是可解析路径。任何 copy-links、copy-dirlinks 或让 source path 逃出固定 source root 的请求都被拒绝。

### 3.2 durability 顺序

每个 provider commit 按以下顺序执行，并且任一步失败均不进入 DB publication：

1. 在 staging 下创建唯一 attempt root，验证它是服务刚创建的空目录；不会清洗、续传或复用旧 staging。
2. transfer 已退出为 0 且所有 stdout/stderr goroutine 已 join 后，生成 canonical manifest 和 fidelity evidence。
3. 对 tree 全量执行 mode-specific inode/metadata 验证；fsync 新文件和目录树。
4. 写 attempt.json、manifest.jsonl、commit.json，fsync 每个文件及 staging directory。
5. 在 trusted points dirfd 中执行 renameat2(RENAME_NOREPLACE)，把整个 staging root 改名为 exact final point；EEXIST 与 EXDEV 都不能被替代成覆盖或 copy fallback。
6. fsync points directory 及 managed-root parent；第 5 步成功的 rename 是 provider 可见性线性化点。
7. 仅在仍持有同一有效 child fence 时，写入 provider-commit evidence 并将数据库推进到 verifying。

rename 的可见性原子性不是掉电持久性保证，因此 file/directory fsync 是硬要求。任何 fsync、rename、mount 或 marker verification 不支持/失败，结果都是不发布而非“尽力成功”。

## 4. Preflight、mode 与 Rsync 命令边界

### 4.1 mode 和冻结规则

Task publication mode 是严格 literal union：

- legacy_mutable：现有行为，唯一允许旧 RsyncExecutor.Run 使用 Task.RsyncTarget。
- versioned_hardlink：每个新 point 使用上一个同 repository、同 active link lineage 的 committed tree 作为 link-dest。
- versioned_full_copy：每个新 point 是独立 inode tree。

mode 由用户在受控表单中选择，且 versioned mode 必须绑定未过期、同 Task revision 的成功 preflight。hardlink preflight 失败时，UI 可以建议 versioned_full_copy，但不会在同一 preflight、attempt 或 retry 中静默降级。用户确认 full-copy 后产生新 preflight 和新的 immutable attempt mode。

全新 versioned repository 的第一个 point 必然是 full-copy seed。若用户的长期模式是 versioned_hardlink，wizard 明确显示 seed_full_copy_required：它可以由 imported_baseline 完成，或由 first_new_point 的下一次成功 transfer 完成。该 seed attempt/point 明确记录 full_copy_tree 和 imported_baseline 或 xirang_manifest 语义；seed 成功后才允许随后的 attempt 使用 versioned_hardlink 和该 point 作为 parent。它不是 hardlink mode 的静默降级、空 parent 的伪成功或隐式 legacy import。长期模式为 versioned_full_copy 时每个 point 都是 full-copy。

### 4.2 preflight 内容和产物

preflight 只探测和创建/删除受控 probe 文件；它不传输用户数据、修改 legacy target 或创建 RecoveryPoint。成功时返回短期 opaque preflight ID，服务端保存其 digest、Task revision、mode、binding/root marker digest、capability revision、probe evidence、容量估算和 UTC expiry。activation/migration 仅接受这个 opaque ID 和 expected revision，二者任一漂移都会失效。

preflight 必须检查：

| 类别 | hardlink | full-copy |
| --- | --- | --- |
| Rsync/peer capability | archive/checksum/fsync/protected-args、版本和受控 transport | 同左，不要求 link-dest |
| path/root | trusted containment、root marker、无重叠、空 staging、不可访问/非空失败 | 同左 |
| commit primitive | exact parent directory 的 NOREPLACE rename probe、file/dir fsync | 同左 |
| mount/link | statx mount ID、实际 hardlink probe、unlink cleanup、parent tree 可读且同 lineage | 不允许跨 point/external inode sharing |
| fidelity | ACL/xattr/source-hardlink/uid-gid/symlink/unsupported-type 的双端 capability + round-trip probe | 同左 |
| capacity | logical/allocated estimate、free blocks、free inodes、quota signal、parent nlink 和安全余量 | logical/allocated estimate、free blocks、free inodes、quota signal |

getconf LINK_MAX 不能被当作可用链接精确上限；probe 和估算只能作为预检证据。运行中的 EMLINK、ENOSPC、EDQUOT、EPERM、EXDEV 仍然失败关闭。硬链接 probe 不能只比较 st_dev：它在 exact parent/target dirs 上验证真实 link 创建、inode/nlink、unlink、NOREPLACE rename 和 fsync。

ACL/xattr/source-hardlink 是互相独立的 fidelity 项。无法获得或验证时，activation 要么因需要的 fidelity profile 而阻断，要么产生明确的 not_captured/not_verified 状态；它绝不能被显示为已验证完整保真。无法证明完整树策略（例如检测到禁止的特殊节点）时本次 transfer 失败，不发布一个悄悄缺项的 point。

### 4.3 typed command allowlist

managed Rsync command 只能由 RsyncTreeStrategy 的 typed builder 生成，使用 argv exec，没有 shell。用户、Task.ExecutorConfig、API 与导入文件都不能附加 flags。固定 source 语义是已验证 source root 加尾随 slash；destination 是已存在、空、受信的 staging/tree。

允许的 argv 元素只有：

~~~text
rsync
--archive
--checksum
--hard-links
--numeric-ids
--fsync
--protect-args
--info=progress2
--acls                 (仅当 preflight 批准)
--xattrs               (仅当 preflight 批准)
--no-devices
--no-specials
--bwlimit=<digits>k    (仅服务端已批准的带宽策略)
-e <internally-built-ssh-transport>
--rsync-path <internally-built-sudo-rsync>  (仅节点契约需要时)
--link-dest=<internally-derived-parent-tree> (仅 hardlink mode)
-- <internally-constructed-source/> <fresh-staging-tree/>
~~~

--hard-links 是复制 source 内 hardlink topology；--link-dest 是跨 point 去重，两者均需要独立验证。--archive 不自动证明 ACL/xattr/hardlink fidelity，因此 --acls/--xattrs 的选择和实际结果都进入 evidence。

明确拒绝短/长选项等价形式的：verbose/itemize path 输出、inplace、全部 append、partial/partial-dir、append-verify、ignore-existing、ignore-missing-args、temp-dir、delete、remove-source-files、backup/backup-dir、compare-dest/copy-dest、用户 link-dest、files-from/include-from/exclude-from、copy-links/dirlinks、ignore-errors、checksum-none、dry-run/list-only、out-format/log-file/batch、所有用户 -e/--rsh、用户 --rsync-path、ownership remapping、device/special-file 选项和未知 flags。

进程环境会清除 RSYNC_OLD_ARGS、RSYNC_PROTECT_ARGS、RSYNC_RSH、TMPDIR 及其他可改变 argv/temporary-path 语义的变量；transport 仅复用经过现有 credential/host-key/audit 边界构造的 SSH 配置。stdout/stderr 受字节上限并仅用于进程控制/安全 code；原始行、文件名和路径不进入 TaskRun、日志、audit、metrics 或 API。

## 5. Publication 状态、leases 与线性化

### 5.1 三个独立事实

| 事实 | 成功条件 | 存放位置 | 失败后含义 |
| --- | --- | --- | --- |
| transfer | Rsync exit=0，进程和 stream 已 join | TaskRun | 本次字节传输成功；不等同于 RecoveryPoint 已可用 |
| provider commit | exact final path 的 authenticated commit.json 已经随 NOREPLACE rename 可见且父目录 fsync 成功 | managed root | Provider 有一个候选树；尚未向资产平面发布 |
| DB publication | current fence 验证、marker/manifest 精确匹配、RecoveryPoint 进入 verifying/committed | database | 该 point 可以被 worker/repository adapter 按规则使用 |

managed Rsync 绕过 legacy verifier.Verify。该 verifier 读取 mutable Task.RsyncTarget 和活源，无法识别当前 attempt 的 final tree，且可能错误把 transfer result 改成 warning。point 的 manifest/fidelity/minimum verification 是版本树的验证事实。

TaskRun.status 只表达真实 transfer/cancel/timeout/exit 结果。UI 通过安全 publication summary 另外显示 publication_preparing、publication_verifying、publication_committed、publication_failed 或 publication_blocked，并仅暴露稳定 code。不能为使旧任务列表方便而把 transfer success 改成失败，也不能因为 point committed 抹去真实 transfer failure。

### 5.2 child/parent leases 和 deadline

在 provider work 前，coordinator 以 producing TaskRun 创建或精确重取一个 preparing RecoveryPoint，并取得 child point_publication lease。该 point 的 absolute_deadline 一经创建即不可延长；全部 attempt、worker 和 reconciliation 对同一点使用同一 deadline。新的 attempt 只能更换 staging/attempt ID，不能延长 point 的时限。

hardlink mode 还必须在每个 attempt 取得 parent 的 rsync_parent lease。它从开始依次持有到 transfer、inode validation、marker、rename 和 staging cleanup 完成，确保 retention/lifecycle 不能在 link-dest 使用期间改变或删除 parent。parent 必须是相同 repository、相同 active link lineage、committed state、expected marker/manifest digest 的 point。

rsync_parent lease 有自己的 service-issued absolute deadline；它不复制或延长 child deadline。每个 provider context 的 effective deadline 为 child/parent 两者更早者。任意 child/parent renewal 失败、fence 变化、deadline 到达或 admission revoke 都立即取消 provider work，等待所有 stream/command goroutine join，并拒绝后续 marker/rename/DB promotion。

所有 mutating provider operation 在开始前和 rename 前都检查 current fence。若在 fence/renew 丢失附近存在任何 outcome 不确定性，自动 reconciliation 不会把该 final tree 变成 committed；它只标记 quarantine/unknown，并保留证据给受控运维流程。这个保守规则避免旧 attempt 在新 fence 后迟到发布。

### 5.3 正常状态流

~~~mermaid
stateDiagram-v2
  [*] --> preparing: "fenced point reservation"
  preparing --> transfer_failed: "non-zero / cancel / deadline"
  preparing --> provider_committed: "manifest + marker + NOREPLACE rename"
  provider_committed --> verifying: "fenced DB commit evidence"
  verifying --> committed: "exact worker verification"
  preparing --> failed: "preflight / admission / marker failure"
  provider_committed --> quarantine: "fence or outcome unknown"
  verifying --> failed: "exact verification mismatch"
  transfer_failed --> failed
  quarantine --> [*]: "no automatic promotion"
~~~

图中的 provider_committed 是 provider 事实，不是公开的 RecoveryPoint state。数据库继续使用 preparing、verifying、committed 和 failed，从而保持 Child 3 的生命周期契约。

minimum verification before rename includes canonical manifest completion, marker authentication, mode-specific inode validation, approved fidelity checks and post-copy source-deletion semantics. Worker verification after DB verifying reopens only the exact final tree through trusted dirfd, rechecks marker/manifest digest/fence/deadline identity and performs bounded full verification before CAS to committed.

### 5.4 mode-specific validation

Hardlink mode:

1. Parent and candidate manifests are compared by canonical relative name and relevant metadata/content identity.
2. For every eligible unchanged regular file, candidate and parent must share the expected mount/device/inode; nlink must be consistent with the manifest topology.
3. Every changed file must have a distinct inode and checksum/metadata evidence. Every source-deleted file must be absent from candidate, while it remains in parent.
4. Any silent copy fallback, unexpected sharing, inaccessible metadata, parent drift or incomplete eligible set rejects this attempt. Sampling, rsync stats and a single itemize line are insufficient.

Full-copy mode:

1. Canonical tree enumeration records every regular-file inode and its count within candidate tree.
2. For each file, st_nlink must equal its count within that tree; no inode can be shared with parent, another committed point or an external probe file.
3. Reflink/compression/dedup may change physical allocation but does not weaken the no-shared-inode guarantee.

Both modes retain parent immutability by construction: fresh staging plus prohibited inplace/append/resume avoids mutation through shared inode; final points are never used as future staging.

## 6. Crash, retry, reconciliation and shutdown

Retries use the same stable RecoveryPoint ID only while its immutable deadline remains valid, but always allocate a new attempt/staging root. A retry never reuses, cleans and continues a previous staging tree.

| 已观测到的精确事实 | reconciliation 动作 |
| --- | --- |
| 没有 owned attempt marker | 不做事；绝不从目录名推测 point |
| staging 有认证的 attempt marker、没有 final marker | 在 liveness/fence 检查后标记/保留该 attempt failed，仅移除这个 exact owned staging |
| transfer rc 非零且 staging 不完整 | 保持不可发布；只清理 marker-owned staging，绝不 rename |
| exact final commit marker 存在、DB 仍为 preparing | 验证 repository/link/point/attempt/marker/manifest/deadline/current fence；写 provider evidence 并入队 verifying |
| DB 为 verifying、exact final marker 缺失或不匹配 | failed/quarantine point；不编造 locator |
| rename 返回 EEXIST | 仅接受同 point/attempt/digest/fence 的 exact authenticated marker；否则 conflict/quarantine |
| lease loss、deadline expiry、timeout 或 root drift 后出现 marker | quarantine；不自动 DB publication，也不删除 final tree |
| DB 已 committed、provider marker/tree 不可用 | 经普通 provider reconciliation 标记 availability/degraded；绝不回退 legacy target |

The reconciliation candidate query is provider-neutral but filtered by persisted provider kind and strict attempt schema. It does not have the current Restic-only executor-type condition. The shared worker still consumes opaque point IDs; it dispatches to a registered strategy that reconstructs only its own typed attempt evidence. Interrupted TaskRun recovery likewise selects provider-tagged managed attempts, and it only changes stale run status according to the independent transfer/publication rules above.

Startup and graceful shutdown close admission before canceling work. They wait for active transfer processes, readers, manifest streams and lease renewers to join; no lease is released while a provider command might still mutate. Forced deadline/cancellation leaves staging for exact reconciliation/owned cleanup, not broad recursive deletion. Cleanup never touches final points, points/, repository.json or legacy target.

## 7. Legacy migration, admission and rollback safety

### 7.1 explicit migration paths

Existing Rsync Tasks remain legacy_mutable unless an authorized operator starts the migration workflow. No background job converts, moves, labels or deduplicates a legacy target.

The migration wizard first creates a preflight and presents two approved choices:

| Choice | Provider action | Result |
| --- | --- | --- |
| imported_baseline | fresh full-copy staging from legacy target, full manifest/validation, atomic final rename and normal DB publication | creates exactly one new RecoveryPoint with semantics imported_baseline and full_copy_tree mechanics |
| first_new_point | leaves legacy target byte-for-byte untouched and activates a new managed root | no historical point is claimed; next successful producing run creates the first xirang_manifest full-copy seed point |

imported_baseline does not hardlink with, rename, move, in-place mark or reuse the legacy tree. It is a normal fenced full-copy publication with a new point/attempt/marker/manifest. first_new_point never uses legacy mtime or directory structure as evidence. In both paths, TaskRepositoryLink.EncryptedLegacyLocator retains the old location; it is not replaced by managed root.

Activation performs a single expected-revision transaction that validates the unexpired preflight, writes binding v2, creates/updates the mode-specific TaskRepositoryLink and pauses the scheduler until managed admission is fully active. A partially configured versioned task is disabled/unscheduled; it can never execute once through the old mutable executor.

### 7.2 binding v2 and reader compatibility

bindingDocument v1 remains a strict legacy mutable document. v2 is an explicit versioned document with a managed repository identity class, layout version, encrypted managed-root locator, repository marker digest and mode/link identity. New code accepts v1 or v2 only through their exact decoder. An old/v1-only component confronted with v2 fails closed rather than treating it as Task.RsyncTarget.

The Child 2 mutable-head Rsync adapter remains unchanged for legacy links. A separate committed-tree adapter accepts only a committed point locator and produces points/<id>/tree using strict fileaccess policy. Child 4 does not expose a public browse/restore endpoint; legacy browse/restore paths fail closed for a managed Rsync link until a later Child owns the full user-facing capability contract.

### 7.3 latch and admission

The current Restic managed-history resolver becomes provider-neutral:

- a durable repository latch blocks mutable fallback for that repository;
- a durable installation latch blocks unsafe ambiguous/reconnected fallback;
- 任一状态的 xirang_manifest/imported_baseline/native_snapshot record、versioned link、active point_publication lease 或 active rsync_parent lease 都按保守路径处理；
- a future lifecycle tombstone remains another permanent managed-history signal.

Once a managed Rsync history fact exists, disabling the feature does not re-enable legacy Rsync execution, legacy reads/restores, unscoped anomaly/retention commands or cross-point access. Every such command acquires the same admission generation token before it begins and holds it until all handles/children are closed. Unknown binding/root identity fails closed.

### 7.4 rollback preparation and application downgrade

“Rollback” here is an explicit safety preparation, not deletion of managed history and not a schema down migration:

1. pause scheduling and close new managed admission;
2. cancel/drain all provider commands, reads, workers and leases;
3. verify managed root is not reachable through the preserved legacy locator;
4. relink Task.RsyncTarget only to the encrypted legacy rollback locator and leave the Task paused;
5. retain every committed point, marker, binding evidence and latch.

The current runtime still refuses ordinary mutable fallback after managed history. A prepared older application sees only a paused Task and a legacy target that is physically separate from managed root; enabling it later is an explicit operator action outside the managed-history workflow, never an automatic fallback. Database down is separately blocked by 000064 while any durable managed-history evidence remains.

## 8. Paired migration 000064

Child 4 owns exactly these paired files:

~~~text
backend/internal/database/migrations/sqlite/000064_backup_asset_rsync_publication_contract.up.sql
backend/internal/database/migrations/sqlite/000064_backup_asset_rsync_publication_contract.down.sql
backend/internal/database/migrations/postgres/000064_backup_asset_rsync_publication_contract.up.sql
backend/internal/database/migrations/postgres/000064_backup_asset_rsync_publication_contract.down.sql
~~~

Search through GA reservations shift together to 000065 through 000071. Before implementation starts, both migration directories must be rechecked against current main; a newly consumed version requires renumbering the entire later reservation consistently, never a gap or engine divergence.

### 8.1 schema contract

The migration adds backup_asset_managed_history_latches with:

- scope: installation or repository;
- nullable repository_id only for installation scope, with a check tying scope to nullability;
- nonsecret repository identity digest for repository scope, first managed semantics/origin, first_seen_at UTC, created_at UTC and updated_at UTC;
- partial unique indexes enforcing one installation latch and one repository latch per repository ID, without a cascading repository FK so a later unlink/delete cannot erase the safety fact.

It also adds a partial unique index on recovery_points(repository_id, source_fingerprint) for nonempty managed-tree physical source fingerprints where semantics is xirang_manifest or imported_baseline. The fingerprint is a keyed digest over layout/repository marker/physical-root identity/final point identity; it is not a raw path, device number, inode, filename or public locator. It prevents an imported or committed physical tree from being registered twice under one repository.

The up migration backfills:

1. an installation latch if any existing native_snapshot point exists in any state;
2. one repository latch for each repository with existing native_snapshot history;
3. only UTC values from stored timestamps, using a deterministic existing timestamp fallback rather than local migration time where possible.

Normal Rsync DB publication inserts both latches in the same transaction that records the first exact provider commit; reconciliation does the same before promotion of an orphaned exact marker. A migration alone and a mutable_head observation do not set the latch.

### 8.2 down contract

Before any DDL/DML that could remove the new guard, down rejects if any of the following is true:

- an installation or repository latch exists;
- a RecoveryPoint has native_snapshot, xirang_manifest or imported_baseline semantics in any state;
- a TaskRepositoryLink in native_snapshot, versioned_hardlink or versioned_full_copy mode exists;
- an active point_publication or rsync_parent lease exists.

PostgreSQL raises an exception inside the transaction before dropping anything. SQLite performs an equivalent pre-DDL guard whose failing constraint aborts the migration before schema/data mutation. A blocked down leaves tables, indexes and data unchanged. This is intentionally fail closed: committed provider trees are never deleted to make a downgrade possible.

The managed-history resolver and admission checks are updated to consult latches first, then existing points/links/leases/tombstones. This closes the current native_snapshot-only query gap and prevents a future deletion/unlink from reopening mutable fallback.

## 9. Backend, API and frontend boundary

### 9.1 backend

Task validation gains a strict RsyncPublicationConfigV1 parser. legacy executor_config remains accepted only for legacy_mutable compatibility; a versioned Task rejects unknown fields, raw flags, mode mismatch and unsafe direct config mutation. Task create/update must not infer unknown executor/mode as rsync or legacy. Managed mode validation is also applied to config import; imported managed Tasks are paused/disconnected until a local preflight proves their binding/root identity.

The publication runtime receives a provider registry capability for RsyncTreeStrategy alongside Restic, and its coordinator/worker/interrupted-run scan dispatch on strict provider tag. Provider execution returns a managed result that tells the runner to skip legacy verification. No new global state machine, raw provider JSON column or duplicate worker is introduced.

### 9.2 safe API

Task DTOs add a typed rsync_publication summary:

~~~text
mode: legacy_mutable | versioned_hardlink | versioned_full_copy
state: legacy | preflight_required | ready | preparing | verifying |
       committed | failed | blocked | rollback_prepared
reason_code: stable optional code
capability_revision: integer
~~~

Unknown values map to blocked/unsupported, not a guessed legacy mode. The DTO intentionally omits roots, paths, mount IDs, device/inode values, locators, repository marker/manifest/fence digests, command argv/output and credentials.

New privileged endpoints are scoped to the existing Task ownership/RBAC checks plus Admin authorization:

~~~text
POST /api/v1/tasks/:id/rsync-versioning/preflights
POST /api/v1/tasks/:id/rsync-versioning/activate
POST /api/v1/tasks/:id/rsync-versioning/rollback-preparations
~~~

activate and rollback-preparations require expected Task revision and an unexpired opaque preflight ID where applicable. They are idempotent only for the same request identity; a changed mode, revision, binding, root marker or source topology gets a conflict/expired stable code. Audit records action, opaque task/repository/point IDs, mode, result and correlation ID only.

### 9.3 frontend

The task form uses literal TypeScript unions and typed API mappers. It presents three modes, with legacy as the compatibility default. Versioned choices show preflight state and a blocked safe reason; activation remains disabled until a matching preflight succeeds. A hardlink preflight failure offers an explicit full-copy choice rather than an automatic fallback.

Existing linked tasks use the migration wizard rather than normal PUT. The UI presents the approved imported_baseline and first_new_point semantics, capacity/inode estimate categories, warning that old target remains rollback-only, and rollback-prepared state. It does not show filesystem roots, raw command diagnostics or credentials. Chinese and English i18n keys are added together. No catalog/file browser/restore UI is added in this Child.

## 10. Test and verification matrix

| Area | Required coverage |
| --- | --- |
| strict contracts | v1/v2 binding decoding, tagged attempt/evidence decoder, unknown field/provider/mode rejection, no raw path/secret in public serialization |
| command builder | full argv snapshots for both modes, no shell, option-like source, environment scrub, every forbidden flag/extra config rejected, bounded output contains no filename/path in TaskRun/audit/log DTO |
| preflight | same mount, cross mount/EXDEV, actual hardlink, protected-hardlink EPERM, NOREPLACE collision, dir/file fsync failure, source/repository overlap, symlink escape, nonempty staging, root replacement, capacity/inode/quota/nlink failures, stale revision/preflight |
| hardlink publication | unchanged shared inode, changed independent inode, deleted source absent in new tree, parent content/mode unchanged on success/failure/cancel/crash, silent rsync copy fallback detected |
| full-copy publication | no cross-point/external inode sharing, source hardlink topology evidence, full manifest and fidelity behavior |
| fidelity | archive versus H/A/X capability cases, ACL/xattr round trip, unsupported metadata/permission is explicit or blocks, symlink never followed by reader |
| crash/reconcile | failure before marker, partial nonzero 23/24, marker before rename, rename before DB, DB verifying without marker, EEXIST exact/mismatch, stale fence, renewal loss, deadline, restart and shutdown join |
| admission/latch | pristine disabled legacy compatibility; first managed point permanently blocks fallback; feature disable; link/binding drift; Restic backfill; tombstone; active publication/parent lease |
| migration | SQLite and PostgreSQL apply/down parity, UTC checks, backfill idempotence, partial index behavior, every down blocker leaves schema/data untouched, reservation scan 000064--000071 |
| API/UI | RBAC/ownership/expected revision, safe DTO/no sensitive values, mappers for all modes/unknown state, stale preflight disablement, wizard semantics, zh/en key parity |
| integration | focused Go tests, race suites for lease/admission/reconcile, paired migration harness, make check, doc freshness, migration UTC safety, security/dependency scans and git diff --check |

The implementation plan must map these tests to concrete files before task.py start. This design intentionally does not create that plan and does not authorize any product or migration edit.

## 11. Design self-review

- 占位符扫描：没有未决占位、空章节或模糊的实现前提。
- Contract consistency: managed Rsync uses the Child 3 coordinator but not a Restic payload; provider commit, DB publication and transfer are separate throughout.
- Migration consistency: 000064 belongs to Child 4; all later parent reservations are 000065--000071.
- Cross-engine/UTC: schema and down conditions are specified for SQLite and PostgreSQL; persistent timestamps are UTC.
- Crash linearization: only NOREPLACE rename is provider visibility; uncertain fence/outcome is quarantined rather than guessed.
- Destructive behavior: no final point, legacy target or unknown directory is deleted; only exact marker-owned staging may be cleaned.
- Scope: no retention/purge, catalog, public Rsync browse/restore or Rclone work is pulled into this Child.
