# Backup asset load gates

`scripts/test-backup-asset-load.sh` is the executable load/security gate.

| Mode | What it runs | Who |
|---|---|---|
| `ci-bounded` (default) | 10k catalog pagination + zip-bomb reject + SIGKILL-then-reconcile + existing range/lease/audit owners | CI |
| `million-catalog` | The same 10k catalog owner. A literal million-row catalog is not generated in-tree. | Operator |
| `archive-bomb` | Zip / ratio bomb rejection owners | CI / operator |
| `process-restart` | Export/search restart owners plus the SIGKILL reconcile owner | CI / operator |
| `million-catalog-full` | Refuses unless `BACKUP_ASSET_LOAD_ALLOW_MILLION=1`. There is no checked-in million generator. | Operator host only |

```bash
BACKUP_ASSET_LOAD_LOCAL=ci-bounded bash scripts/test-backup-asset-load.sh
BACKUP_ASSET_LOAD_LOCAL=million-catalog bash scripts/test-backup-asset-load.sh
BACKUP_ASSET_LOAD_LOCAL=process-restart bash scripts/test-backup-asset-load.sh
```

SIGKILL: `TestControlledProcessSIGKILLThenRestartReconciles` starts a helper
process, sends SIGKILL, then runs delivery reconcile. It does not claim a
production Worker restart.

AWS Native stays out of the support matrix until a live suite exists.
