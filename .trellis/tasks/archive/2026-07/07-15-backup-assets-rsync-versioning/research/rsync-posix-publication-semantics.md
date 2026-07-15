# Rsync / POSIX 版本化发布语义研究

日期：2026-07-15
范围：Child 4 的 Rsync 版本化目录树、`--link-dest`、文件系统提交与崩溃恢复。本文记录的是设计证据，不是对任意 OS、文件系统或外部写入者的保真/WORM 承诺。

## 一手资料

- [rsync 3.4.4 manual](https://download.samba.org/pub/rsync/rsync.1)：[`--archive`](https://download.samba.org/pub/rsync/rsync.1#opt--archive)、[`--link-dest`](https://download.samba.org/pub/rsync/rsync.1#opt--link-dest)、[`--checksum`](https://download.samba.org/pub/rsync/rsync.1#opt--checksum)、[`--inplace`](https://download.samba.org/pub/rsync/rsync.1#opt--inplace)、[`--append`](https://download.samba.org/pub/rsync/rsync.1#opt--append)、[`--delete`](https://download.samba.org/pub/rsync/rsync.1#opt--delete)、[`--ignore-existing`](https://download.samba.org/pub/rsync/rsync.1#opt--ignore-existing)、[`--fsync`](https://download.samba.org/pub/rsync/rsync.1#opt--fsync) 和 [exit values](https://download.samba.org/pub/rsync/rsync.1#EXIT_VALUES)。
- [How Rsync Works](https://rsync.samba.org/how-rsync-works.html)：quick-check、临时文件与完成后的 rename 的算法说明。
- Rsync 3.4.4 源码：[generator.c `hard_link_one()`](https://github.com/RsyncProject/rsync/blob/v3.4.4/generator.c#L948-L1044) 在硬链接失败时落入 copy 路径；[util1.c](https://github.com/RsyncProject/rsync/blob/v3.4.4/util1.c#L543-L580) 说明 rename 遇到 `EXDEV` 时可退化为复制再删除。
- POSIX：[rename](https://pubs.opengroup.org/onlinepubs/9799919799/functions/rename.html)、[link](https://pubs.opengroup.org/onlinepubs/9799919799/functions/link.html)、[fsync](https://pubs.opengroup.org/onlinepubs/9799919799/functions/fsync.html)。
- Linux man-pages：[rename/renameat2](https://man7.org/linux/man-pages/man2/rename.2.html)、[link](https://man7.org/linux/man-pages/man2/link.2.html)、[openat2](https://man7.org/linux/man-pages/man2/openat2.2.html)、[fsync](https://man7.org/linux/man-pages/man2/fsync.2.html)、[statx mount ID](https://man7.org/linux/man-pages/man2/statx.2.html)、[inode](https://man7.org/linux/man-pages/man7/inode.7.html)、[xattr](https://man7.org/linux/man-pages/man7/xattr.7.html)、[ACL](https://man7.org/linux/man-pages/man5/acl.5.html)、[statvfs](https://man7.org/linux/man-pages/man3/statvfs.3.html)。

## 本机实验

实验使用 Rsync 3.4.4 与 Linux 7.1.2；`/tmp` 和 `/dev/shm` 是不同 tmpfs mount。实验环境支持 ACL、xattr 与 hard link。脚本和 C probe 在当时保存在 `/tmp/xirang-rsync-semantics.sh`、`/tmp/xirang-fs-probe.sh`、`/tmp/xirang-fs-probe.c`、`/tmp/xirang-same-mount-link-failure.sh`；以下可重复观察必须以引用的一手资料为准，不把临时脚本作为持久工件。

| 观察 | 结果 | 设计含义 |
| --- | --- | --- |
| 空 staging + `--link-dest` | 未变化文件与 parent 有相同 `dev:inode`；变化文件不同；已从 source 删除的文件不会进入新树，parent 保持原样。 | 每次 attempt 只能使用实际为空的新 staging；不需要为这个语义给空 staging 加 `--delete`。 |
| 同 size + mtime 的内容变化 | 默认 quick-check 没有传输，继续链接旧 inode，结果仍是旧内容；加 `--checksum` 后传输为新 inode。 | hardlink 模式必须强制 `--checksum`；不能把默认 size/mtime 判断当作恢复点内容证明。 |
| `--inplace` 与 `--append*` | staging/parent 的共享 inode 被直接改写，parent 内容也变化。 | 必须从 typed command allowlist 中排除所有 inplace/append/resume 写法。 |
| 非空 staging | 即便没有 inplace，Rsync 可直接 `chmod` 已共享 inode，parent 权限随之改变。 | 不允许复用、清洗或续传 staging；重试必须生成新的独占路径。 |
| `--ignore-existing --delete` | extraneous 文件可被删除，但预放入的 `POISON` 文件会保留，且 rc=0。 | 禁止 `--ignore-existing`；实际为空必须由 provider preflight 与 postcondition 证明，不能由 Rsync“修复”。 |
| hard link 创建失败 | 跨 mount 及同 mount 的 `protected_hardlinks` `EPERM` 均可静默 full copy 并 rc=0；单次 `-i` / `--stats` 不能可靠区分。重复 `-ii` 可在该版本显示 `hf`（link）或 `cf`（copy），但仍只是诊断。 | hardlink mode 不能只依赖 preflight、rc=0 或 itemized/stats；commit 前必须全量比较 eligible parent/new 文件的 mount、device 与 inode。 |
| partial exit | exit 23 与 24 都会留下部分 staging。 | 所有非零 exit 均不得发布；可保留受限 staging 供 reconciliation/安全清理，但不能命名为 RecoveryPoint。 |
| source 尾斜杠 | `src -> dest/` 产生 `dest/src/file`；`src/ -> dest/` 产生 `dest/file`。 | command 必须固定 `source/` 和已存在的 `staging/`；禁止用户自带 source-format 参数。 |
| `-a` 与 `-aHAX` | `-a` 不包含 `-H/-A/-X`，源内 hardlink、xattr、命名 ACL 可丢失；`-aHAX` 在本机保留三者。 | source 内 hardlink 拓扑与跨点 `--link-dest` 是两项独立 fidelity evidence；ACL/xattr/H 必须按实际 capabilities、flags 和 probe 记录，不能泛化承诺。 |
| symlink 与路径重叠 | `-a` 可原样保存 `/etc/passwd`、`../../outside` 等 target；`src/ -> src/dest/` 可递归生成 `dest/dest/...` 且 rc=0。 | 在受信 repository dirfd 下用 `openat2(BENEATH|NO_SYMLINKS|NO_XDEV)` / `*at` 操作；拒绝 source、repository、staging、final、parent 的物理祖先/后代重叠。 |
| `renameat2(RENAME_NOREPLACE)` | 空目标成功；碰撞返回 `EEXIST`；跨 mount 返回 `EXDEV`。 | provider commit 使用相对受信 dirfd 的 no-replace rename；不允许跨文件系统 copy fallback 或覆盖已有 final。 |
| file / directory fsync | 本机两个 syscall 均成功。 | 这只能证明接口可用，不能模拟断电；设计仍需 fsync 文件、目录、marker 和 final parent，任何不支持/失败均失败关闭。 |
| link count 与空间 | 8 MiB 文件建立 200 个 hard link 后数据 blocks 不变、`nlink=201`；full copy 增加 inode 与数据 blocks。`getconf LINK_MAX=127` 却仍能创建 201 links。 | 用 `statvfs`、配额与实际 `st_nlink` 作为估计/运行时证据，不把 `LINK_MAX` 当精确剩余额度；`EMLINK`、`ENOSPC`、`EDQUOT` 仍为失败关闭。 |

## 从资料和实验导出的硬约束

### Rsync command 边界

- 命令必须由 provider 的 typed allowlist 生成、argv 直 exec，并用 `--` 终结 option parsing；不接受用户自由追加的 Rsync 参数。
- hardlink mode 至少固定 archive、checksum、受控 `--link-dest`、受控 itemize/诊断和 `--fsync`。Rsync 协商 checksum 不取代 Xirang canonical manifest 的独立强摘要。
- 禁止 `--inplace`、全部 `--append*`、partial/resume、`--ignore-existing`、`--ignore-missing-args`、外部 `--temp-dir`、用户提供的 `--link-dest`/`--copy-dest`/`--compare-dest`、remove-source、dry-run/list-only、copy-links/dirlinks、ignore-errors 与 checksum-none 类参数。
- 远端 Rsync 必须使用其受控 argument-protection 模式、清理 `RSYNC_OLD_ARGS` 等环境，并明确说明该模式只减少 shell argument 风险，不构成 repository path containment。

### Publication 与 crash linearization

1. 产生每 attempt 独有、实际空的 staging；Rsync 成功并 join 后，生成 canonical manifest 和 fidelity evidence。
2. 对 hardlink mode 全量验证 eligible 文件的共享 inode；对 full-copy mode 全量验证每个 inode 的 `st_nlink` 与树内出现次数相等，排除跨点/外部共享。
3. fsync 新文件和目录树，写入并 fsync attempt/manifest/commit marker；在同一 mount 内用 `renameat2(RENAME_NOREPLACE)` 把 staging 改名为 exact final point，再 fsync repository parent。
4. 只有这个 no-replace rename 是 Provider 可见性线性化点。随后才进入 Child 3 的 DB publication；DB 成功不是 TaskRun transfer 成功的替代事实。
5. `EEXIST`、`EXDEV`、未确认的 I/O 结果和网络文件系统的模糊失败均不能猜测成功；reconciliation 仅依据 exact marker、repository/link identity、commit digest、point deadline 和 fence。无 marker 的旧 staging 绝不提升为 point。

### 保真与不可承诺项

- Xirang 管理的目录树不是存储 WORM。外部主体若拥有写权限，仍可修改共享 inode；安全边界是受控 namespace、权限和 admission，而非物理不可变媒介。
- Rsync 不是 source 的时间点快照。即便 `--checksum`，source 在扫描期间变化仍可能形成跨文件时刻的树；应用一致性需要 source quiesce 或底层 snapshot，这不属于本 Child。
- 原子 rename 只给出同一文件系统的可见性原子性；持久化还依赖文件与目录 fsync。某些网络文件系统上，返回失败也可能已完成服务端操作，因此必须按 marker 调和。
- full-copy 只能保证不跨点共享 inode；稀疏文件、压缩、reflink 或底层 dedupe 使“物理占用等于 logical size”不可承诺。
- ACL、xattr、owner/group、特殊文件和源内 hardlink 的完整保真受 OS、Rsync build、账户权限、namespace 和 filesystem 支持影响。证据应报告实际达成和已知缺口。

## 对 Child 4 设计的直接结论

- hardlink 与 full-copy 必须是 preflight 冻结的不同 publication mode；hardlink 失败不能在同一个 attempt 静默退化为 full copy。
- legacy target 保持 `Task.RsyncTarget` 的 rollback locator；managed root 只由 encrypted binding v2 持有，且位于独立的 `.xirang-rsync-v1` 控制 namespace。
- read adapter 只接受 opaque exact point ID，并将读取根限定在 `points/<id>/tree`；不得把 legacy target 或任意子目录暴露成已提交恢复点。
- 清理只可删除带有效 attempt marker 的 exact owned staging；不得删除 final point、legacy target 或任何未知目录。
