# Wave 1 — Native AWS public-docs exclusion

Recorded 2026-08-21 on branch `chore/backup-assets-release-acceptance`. This is a repository read, not a production walkthrough. `production_walkthrough` stays `not_executed`.

## Verdict

**`docs_exclusion: verified`.** Public docs still keep Rclone Native AWS out of this version's support matrix. No docs-only correction PR. Native binding UI was not removed.

## `docs/admin/backup-recovery.md`

Mode table still describes Native as an opt-in implementation shape, not a certified GA promise:

```60:62:docs/admin/backup-recovery.md
| AWS 原生对象版本（`native_object_versions`） | 仅 AWS 官方区域端点上的通用型 S3 bucket | 用 S3 `VersionId`、delete marker、完整 mutation ledger 和精确版本读取证明一个恢复点。当前实现范围不覆盖 directory bucket、access point/Outposts、自定义端点、任意 S3-compatible 存储、Azure Blob 或 Google Cloud Storage 的原生版本能力；这些目标应使用 Portable 或保持 legacy。 |

Portable 仍是默认推荐，也是本版本唯一进入支持矩阵的 Rclone 模式。AWS Native live suite 仍为 `not_executed`，因此 **AWS Native 不在本版本支持矩阵内**，不得按已认证能力对外承诺。当前版本保留官方 AWS 的 opt-in live conformance suite；在 live suite 跑通并写入验收记录之前，不要把 Native AWS 写成已支持。若运维人员仍打开 Native 绑定，每个实际目标必须通过自身 bucket、Role、versioning、lifecycle、加密和 KMS 状态的完整运行时预检，缺少或漂移任一证据都会失败关闭。
```

Quoted claim that closes the lie check: **「AWS Native 不在本版本支持矩阵内」** and **「不要把 Native AWS 写成已支持」**.

The later admin-config / encryption / WORM sections describe what happens if an operator still opens Native binding. That is fail-closed opt-in wording, not a support-matrix claim.

## `docs/admin/backup-assets-load.md`

```23:23:docs/admin/backup-assets-load.md
AWS Native stays out of the support matrix until a live suite exists.
```

Quoted claim: **「AWS Native stays out of the support matrix until a live suite exists.」**

## Not a docs lie

| Check | Result |
|---|---|
| Either file says Native AWS is already supported / certified / in-matrix? | No |
| Portable called the only Rclone mode in this version's support matrix? | Yes (`backup-recovery.md:62`) |
| Live suite still `not_executed`? | Yes |
| Public docs edited in this wave? | No |

## Protocol update

Child 18 Binding `docs_exclusion` set to `verified` with the two file:line anchors above. Child 17 archive protocol was not edited.
