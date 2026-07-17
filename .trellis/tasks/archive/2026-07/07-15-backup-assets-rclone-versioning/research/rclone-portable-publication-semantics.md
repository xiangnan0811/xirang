# Rclone 可移植版本前缀发布语义研究

日期：2026-07-15
范围：Child 5 的 `versioned_prefix`、Rclone v1.74.4 命令/RC 能力、Remote 一致性与 hash 真实性、marker-last 提交、重试/调和、legacy 接管和当前 Xirang publication seam。本文记录设计证据，不是 `design.md`、实现授权或对任意 Rclone backend 的 WORM 承诺。

## 一手资料与固定版本

- [Rclone v1.74.4 release](https://github.com/rclone/rclone/releases/tag/v1.74.4)，发布于 2026-07-08；本次研究固定源码 commit `5bc93a2a7ab0ebd0a11352bc4968eabeffb18027`。
- Rclone 官方文档：[`backend`](https://rclone.org/commands/rclone_backend/)、[`lsjson`](https://rclone.org/commands/rclone_lsjson/)、[`check`](https://rclone.org/commands/rclone_check/)、[`rcat`](https://rclone.org/commands/rclone_rcat/)、[`--copy-dest`](https://rclone.org/docs/#copy-dest-stringarray)、[RC `operations/fsinfo` / `operations/hashsumfile`](https://rclone.org/rc/#operations-fsinfo)。
- Rclone v1.74.4 源码：[`fs/operations/copy.go`](https://github.com/rclone/rclone/blob/v1.74.4/fs/operations/copy.go)、[`fs/operations/check.go`](https://github.com/rclone/rclone/blob/v1.74.4/fs/operations/check.go)、[`fs/operations/lsjson.go`](https://github.com/rclone/rclone/blob/v1.74.4/fs/operations/lsjson.go)、[`fs/operations/rc.go`](https://github.com/rclone/rclone/blob/v1.74.4/fs/operations/rc.go)、[`fs/operations/operationsflags/operationsflags.md`](https://github.com/rclone/rclone/blob/v1.74.4/fs/operations/operationsflags/operationsflags.md)。
- 本地合同：父任务 `prd.md` / `design.md` / Child 5 计划；Child 2 的 `backend/internal/backupasset/provider/rclone.go` 与 `runner.go`；Child 3–4 的 tagged publication strategy、coordinator、worker、reconciler、lease/fence 与 managed-history latch。

## 已验证的命令级事实

| 事实 | Rclone v1.74.4 证据 | 对 Child 5 的含义 |
| --- | --- | --- |
| `operations/fsinfo` / `backend features` 只报告 optional features、hash 集合、precision 等 | `fs/operations/rc.go` 的 `operations/fsinfo` 输出包含 `Copy`、`Command`、`Hashes`，没有 list consistency、真实原生 version ID、delete marker 或 lifecycle 合同 | `Copy=true` 或 `Command=true` 只是提示，不能证明安全发布或原生版本认证 |
| 普通 copy 会先尝试 server-side copy；`fs.ErrorCantCopy` 时透明下载再上传 | `fs/operations/copy.go` 的 `serverSideCopy` → `manualCopy` 路径 | “server-side copy”只影响成本/传输路径；不能成为 commit 语义或 fidelity 证明 |
| `--copy-dest` 要求同一 Remote、目标 backend 暴露 server-side Copy，且 compare prefix 不能与 destination 重叠 | 官方 `--copy-dest` 文档；`fs/operations/operations.go` 的 `GetCopyDest` | 可作为前一 committed point 到新 attempt 的增量优化；Feature 不满足时在 attempt 准备阶段选择完整直传，不得改 point identity/mode |
| 即便 `Copy` feature 存在，单对象 Copy 仍可能透明 manual copy | `fs/operations/copy.go` | 运行证据必须记录实际 server-side/transfer bytes；验证合同不能因优化路径改变 |
| `--dest-after` 是逐条执行期间生成的“SHOULD”预测，不保证实际 “DID”；不支持 `--copy-dest`、high-level retries 等场景 | [`operationsflags.md`](https://github.com/rclone/rclone/blob/v1.74.4/fs/operations/operationsflags/operationsflags.md) | 禁止把 `--dest-after` 作为 canonical manifest 或 Remote 完整性证据 |
| 默认 `rclone check` 在无公共 hash/空 hash 等情况下不等价于全字节证明 | `fs/operations/check.go` 与 equality 路径 | weak/no-hash Remote 不能因 size/mtime 相等而提交 |
| `rclone check --download` 会同时完整读取 source 与 destination 并逐字节比较 | 官方 `rclone check` 文档；`CheckIdenticalDownload` / `CheckEqualReaders` | 可作为 weak/no-hash 的强读回证明，但会产生完整下载/API 成本，必须进入 preflight/UI/evidence |
| RC `operations/hashsumfile download=true` 会完整下载单对象后计算指定 hash | 官方 RC 文档与 `fs/operations/rc.go` | 可用于 exact-object SHA-256 readback；返回 JSON 不含路径，适合严格 typed parser，但逐对象调用成本高 |
| `lsjson --hash` 获取单对象 hash 失败时只记录日志并省略 hash | `fs/operations/lsjson.go` | “backend 宣称某 hash”不等于每个 entry 有可用、可信的 hash；缺失必须显式进入 fidelity |
| `rclone rcat` 从 stdin 写一个对象，目标已存在时覆盖，且普通整流不可重试 | 官方 `rcat` 文档 | marker 必须使用 attempt 独占 locator；不允许同 attempt 盲重放或覆盖不明 marker |

## 三种 publication 方案比较

| 方案 | 发布路径 | 优点 | 结构性问题 | 结论 |
| --- | --- | --- | --- | --- |
| 1. attempt-qualified 最终 prefix 直接写 | `points/<point>/attempts/<attempt>/data/`，同 attempt 的 `control/manifest-*` 后写、`control/commit.json` 最后写 | 无目录 rename 假设；一次数据写；每次 retry 天然隔离；exact attempt 可调和；可用前一 committed attempt 作 `--copy-dest` | provider 上可能留下不可发现 orphan；commit 前要完成强验证；locator 必须包含 attempt | **推荐** |
| 2. staging 后逐对象 copy 到 fixed final | 先传 staging，再把所有对象 copy 到 final，最后 marker | 可把 transfer 与 final namespace 分开 | Rclone 没有跨 backend 通用原子目录 promotion；final 仍会部分可见；多一次 API/存储窗口；Copy 可透明下载上传；产生两套 orphan。若 final 也含 attempt，它只是方案 1 加一轮 copy | 拒绝 |
| 3. fixed mutable head 后 clone | 先 sync legacy/fixed head，再复制为 point prefix | 对典型增量 sync 可能节省源端上传 | head 在 clone 遍历期间可变；外部 writer/旧 runtime 可覆盖；clone 逐对象且可能 manual fallback；重新引入 rollback/managed 隔离风险 | 拒绝作为 portable 模式 |

### 推荐布局的设计约束

以下只是 research 导出的硬约束，canonical 字段和限值仍应在经审阅的 `design.md` 冻结：

```text
<managed-root>/v1/
  points/<point-id>/attempts/<attempt-id>/
    control/attempt.json
    data/<source-relative-objects...>
    control/manifest-000000.jsonl
    control/manifest-index.json
    control/commit.json
```

- point/attempt 都是服务端 opaque ID；用户不能提供 component、prefix、Remote 或控制文件名。
- `data/` 与 `control/` 分离，源数据不能覆盖控制对象。attempt marker 先写并 readback，用于所有权与 orphan 识别；data 完成后写 manifest chunks/index；`commit.json` 是该 attempt 的最后一次 Remote mutation。
- Provider commit 是 exact `commit.json` bytes 成功读回、manifest/index/entry proof 精确匹配的事实；数据库 `committed` 才是 Catalog/API 可发现门。Remote 上存在 prefix 或 marker 不能自行成为 RecoveryPoint。
- marker 绑定 repository/link/point/attempt、publication mode、layout/schema/minimum runtime、source capture interval、entry count/logical bytes、canonical manifest digest、fidelity/capability revision、deadline、commit/fence keyed digest。Remote marker 不保存凭据；DB/公开 DTO 不回显 prefix、key、version ID 或命令输出。
- canonical manifest 必须以明确 normalization、排序和 record schema 生成。大清单不能依赖一次内存 JSON：使用有界 streaming parser、0600 spool/external sort、entry/byte/deadline quotas 与固定大小 chunks。任一超限为 typed `manifest_limit_exceeded`，不得提交 partial manifest。

## 完整性、hash 与 consistency 证明

### 不能只等待“两次 listing 相同”

任意有限次相同 listing 都不能从数学上证明一个未知 eventual-consistency backend 已经展示全部对象。因此最低可提交证明必须组合：

1. 新 attempt prefix 经 authenticated ownership probe 证明独占且 `data/` 初始为空；禁止复用、清洗或续传旧 attempt。
2. Rclone transfer 真实 exit=0，进程/stdout/stderr/stdin 全部 close/join；TaskRun 只记录这个 transfer 事实。
3. 在 absolute deadline 内对本地 source 与 exact attempt `data/` 做双向完整集合比较；weak/no-hash 使用 `rclone check --download` 全字节比较。source 在验证期间发生 size/mtime/inode/content drift 时本次 publication 失败关闭，不把跨时刻结果包装成稳定点。
4. 对 destination 进行至少两次完整 canonical listing，间隔使用 backend profile 的 settle interval；两次 digest、count、bytes 必须一致，并与 source/destination check 得到的集合一致。未知 consistency 只记录为 `observationally_stable`，不升级成 provider strong consistency。
5. 对 attempt/manifest/index/commit control objects 做 exact stat/full readback；对数据使用批准的强 checksum 或下载验证。只有全部证据在同一 point deadline 与当前 fence 下成立，才写最后 marker。

fresh + exclusive prefix 与 source 对照非常重要：它使“destination listing 暂时漏了一个已上传对象”表现为 missing-on-destination 并延迟提交；没有这个对照，两次相同但不完整的 listing 会被误判为完整。

### fidelity 分级

| 级别 | 允许的证明 | 提交规则 |
| --- | --- | --- |
| `provider_strong_checksum` | 每个对象都有经认证语义的 collision-resistant checksum（例如真实 SHA-256）、size 与 exact read-after-write；source/destination 同算法一致 | 可提交；仍记录 provider、算法与缺口，不能把泛化 `Hashes` 当值 |
| `download_verified_bytes` | backend hash 不可用、为空或只有弱/模糊值；`check --download` 对集合中每个对象完整读两端并逐字节相等，control bytes 另行 exact readback | 可提交；完整下载/API/egress 成本必须显示并记录 |
| `metadata_only` / `weak_unread` | 只有 size/mtime/ETag、缺少对象、无法完整读取、Range/readback 或 source drift | 不可 committed；保持 verifying/retry，超过 deadline 后 typed failure |

MD5、multipart ETag、CRC、mtime 或 backend 名称不能被包装成 collision-resistant 内容证明。未来可允许 provider-specific checksum profile，但必须以 fixture/官方合同逐项认证，而不是按品牌图标启用。

用户已选择全字节验证边界：weak/no-hash Remote 只有完成 source 与 exact attempt destination 的完整逐字节比较才可 committed；若 absolute deadline、资源或成本配额不允许完成，则 fail closed。`metadata_only` 只保留为诊断 fidelity，不得成为 Provider commit、RecoveryPoint 或 Catalog 可见事实。

### 空、超大和特殊 Remote

- 空 source 是合法的零 entry point：必须证明 `data/` 空、manifest count/bytes 为 0，再写 control marker；不能把“没有 prefix”与“已提交空点”混淆。
- 超大 listing 使用 streaming + spool/chunk；达到资源或 deadline ceiling 时安全失败，不截断成功。
- empty-directory、symlink/metadata、case sensitivity、unicode normalization、mtime precision 与特殊对象 fidelity 取决于实际 Rclone backend/source probe。不能把文件内容一致推导成完整 POSIX metadata 保真。
- Remote offline、限流和 consistency 尚未收敛属于 availability/retry；已读回对象/manifest 不匹配属于 integrity failure。两类 reason code、重试策略与 UI 文案必须分离。

## server-side copy 的安全优化边界

- 只允许从同 repository/link lineage 的前一 committed attempt `data/` 指向新 attempt `data/`，并在有效 parent-read lease/point-publication fence 下使用；不得从 mutable head 或“latest prefix”取 parent。
- preflight 的 `Features.Copy=true` 只允许构造 `--copy-dest` 候选。Feature=false 时直接选择完整 source upload，point ID、mode 与可信语义不变。
- 运行中 per-object Copy 可能 manual fallback；这不损害最终 bytes，只改变 API/egress/耗时。实际 server-side-copy count、download/upload bytes 与 fallback reason 进入私有 fidelity/cost evidence。
- `--dest-after` 与 `--copy-dest` 官方不兼容，manifest 必须由独立 post-transfer list/check/readback 生成。
- Copy/transfer 任一 partial/非零/取消都不得写 commit marker；重试使用新的 attempt prefix，不续传污染的 prefix。

## marker/manifest 写入与当前 transport 断点

当前 `RcloneAdapter` 对 bound config 固定使用 `--config /dev/stdin`，`CommandInvocation` / `sshutil.CommandSpec` 也只有一个、最大 64 KiB 的 `SecretStdin`。`rcat` 同样独占 stdin，因此不能在同一进程既传 Rclone config 又传 marker/manifest；把任一内容放 argv、base64 shell fragment、日志或环境变量都不安全。

focused design 应采用统一的 typed staged-payload 通道：

1. 通过受限 SSH/SFTP writer 把有界 canonical control payload 写入节点私有临时根，opaque filename、directory 0700、file 0600、exclusive create；不物化 Rclone secret config。
2. config 继续走 secret stdin；严格 allowlist 的 `rclone copyto <opaque-local-temp> <exact-private-remote-locator>` 上传 payload。
3. command 完成并 join 后 exact `cat`/hash readback，随后清除 exact owned temp；取消/崩溃清理由 ownership marker 和 deadline 限定，绝不扫描/删除任意节点文件。

node-default config 虽可直接给 `rcat` payload stdin，但统一 staged-payload 路径能避免两种配置源产生不同提交语义。当前 node-default fingerprint 也不能证明配置未漂移；managed preflight 需要实际 endpoint/ownership canary 与 capability revision，而不是常量 fingerprint。

## retry、crash 和 reconciliation

- coordinator 必须先持久化 exact point/attempt/prefix/fence/deadline，再开始 Remote mutation。重试保留 point ID/mode/deadline，分配新 attempt ID/prefix；绝不复用旧 prefix。
- 普通 transfer retry 只能发生在尚无 Provider commit 的 transfer 阶段。marker/DB 后置失败不得修改 TaskRun 成功，也不得回到 legacy executor 或普通 transfer retry。
- outcome unknown 时先调和同一 exact attempt：valid marker+matching digest 可幂等收敛；attempt/manifest 存在但无 commit 是不可发现 orphan；marker digest/schema/fence 不符进入 quarantine。
- marker 已写、DB 未记录：仅在 exact identity、current fence、未过 deadline、manifest/fidelity 全匹配时记录 Provider commit 并进入 verifying。stale fence 或 outcome ambiguity 不自动 promotion。
- DB preparing、marker 未见：deadline 内继续 bounded observation；未知 consistency 下 absence 不能立即证明失败。deadline 后标记 typed unavailable/failed，并保留诊断证据。
- 多 attempt 最终都出现有效 marker 时，只有 DB 选择的 exact attempt 可成为 locator；其他仍是 provider orphan，不能靠 lexicographic order、mtime、“最新 TaskRun”或唯一看似完整的目录认领。
- 本 Child 不删除 provider objects。未来清理只能删除带有效 ownership/attempt marker 的 exact uncommitted orphan，不能删除 committed prefix、legacy locator 或未知目录。

## legacy baseline、rollback 和 managed-history latch

- migration 只能在 Task 暂停、全部 Rclone command/admission 排空并取得 transition fence 后进行。legacy locator 继续加密保存在 link，managed root 与 legacy root 不得重叠。
- `imported_baseline` 必须把 legacy current remote **物理复制**到新 attempt-qualified prefix（server-side 或 download/upload 只是成本差异），完成与正常 point 相同的 stable list/full verification/manifest/marker/DB publication。它记录 capture interval 与较弱来源语义，不能原地给 legacy head 加 marker。
- `first_new_point` 不复制 legacy head；保留 legacy rollback locator，下一次成功 managed transfer 才产生首个 `xirang_manifest` point。
- 两种路径都必须检测迁移窗口外部 writer drift。无法证明稳定 observation 时阻断，而不是后台静默切换。
- downgrade 先阻止新 managed publication、排空全部相关 command/lease、恢复 exact legacy locator；不删除 committed prefixes。feature disable 或旧 runtime 不能把 managed root 当 mutable target。
- 000064 的 durable latch 已按 `native_snapshot | xirang_manifest | imported_baseline` 与 `point_publication | rsync_parent` 查询，可复用 Rclone point；Rclone commit/reconcile 必须在受 fence 事务中写 installation+repository latch。现有 resolver/down-guard 的 managed-link 覆盖缺口另见下节。

## schema 与现有 seam 结论

- 000062 已有 Rclone `versioned_prefix` / `native_object_versions`、`xirang_manifest`、encrypted locator/rollback、manifest/consistency/fidelity/capability 字段；000063 已有 producing TaskRun 唯一约束与 `point_publication` lease；000064 已有 managed-history latch 与 managed-tree source fingerprint 唯一约束。若每对象明细保存在 provider-side canonical manifest、DB 只保存 encrypted exact locator/evidence+digest，则 up schema 可以复用。
- `source_fingerprint` 必须绑定物理 point identity（exact unique prefix/attempt 或 exact native-version manifest locator），不能只用内容 digest；否则两个内容相同但合法的 point 会触发 `(repository_id, source_fingerprint)` 冲突。
- 当前 tagged `PublicationAttempt` / `ProviderCommit`、runtime strategy registry、task manager routing 与 reconciler 是 Restic/Rsync 闭集；Rclone 必须作为第三个显式分支扩展。不能把所有 `xirang_manifest` 当 Rsync，也不能继续让 Rclone绕过 coordinator。
- 单个 active `repository_access_bindings` 行可以保存 strict composite managed-Rclone binding，当前不因 portable 模式必然需要新列/表。
- 已发现 down-contract 缺口：000064 down 的 managed-link guard 只列 `native_snapshot | versioned_hardlink | versioned_full_copy`，遗漏 `versioned_prefix | native_object_versions`；installation resolver 也只查 latch/point/tombstone，不查已激活但未产点的 managed link。Git/release 证据表明 000064 尚未进入稳定 semver tag，归档 Child 4 design 又明确要求 down 覆盖所有 versioned links。因此 Child 5 修正现有 SQLite/PostgreSQL 000064 down guard并补双库测试，同时补 link query；不新增 schema version、不占用 000065，也不在 activation 预写尚未发生的 history latch。

## 对 focused design 的直接结论

1. 采用方案 1：attempt-qualified final prefix 直接写；`commit.json` 最后、DB 可见性最后。
2. 不依赖目录 rename、`--backup-dir`、`--dest-after`、mtime/latest 或一次 listing。
3. 用户已选择 weak/no-hash 的最低 committed fidelity 为 full-download byte verification；若无法承担或完成则失败关闭，不降成 size-only success。
4. `--copy-dest` 仅是前一 committed point 的成本优化，永远不改变 point/mode/manifest/commit 语义。
5. retry 使用新 attempt；exact marker/fence/deadline 调和；orphan 不可发现且本 Child 不物理删除。
6. imported baseline 必须物理复制，first-new-point 必须等下一次新发布；两者保留 legacy rollback locator。
7. 需要新增 typed staged-payload 能力解决 bound-config + marker 双 stdin，但不能放宽命令/秘密边界。
