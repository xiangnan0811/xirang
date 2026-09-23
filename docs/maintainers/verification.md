# 专项验证

专项结果须说明候选、环境、实际数据量、测试入口和未覆盖范围。它们补充[测试约定](../spec/guides/testing.md)，不替代 required CI 或生产验收。

## 备份资产负载与安全

可执行入口是 [test-backup-asset-load.sh](../../scripts/test-backup-asset-load.sh)。所有可接受模式先运行共同测试：1 万条 Catalog 分页、Range、内容租约并发心跳、秘密预览证明、交付 Cookie、预算与请求重放、导出提交/恢复、受控 helper 的 SIGKILL 后 delivery reconcile、接管 fence 及审计脱敏。

| 模式 | 共同测试之外的行为 | 使用场景 |
| --- | --- | --- |
| `ci-bounded`（默认） | 压缩炸弹拒绝；再次运行共同测试中的 SIGKILL 后 reconcile，并运行搜索重启清理 | CI 与本地回归 |
| `million-catalog` | 再次运行同一个 1 万条分页测试，并明确输出未执行百万规模 | 手动验证现有分页回归 |
| `archive-bomb` | Zip 路径穿越、链接、设备、加密及大小/压缩比炸弹拒绝 | CI 或本地专项 |
| `process-restart` | 再次运行共同测试中的 SIGKILL 后 reconcile，并运行搜索重启清理 | CI 或本地专项 |
| `million-catalog-full` | 未设 `BACKUP_ASSET_LOAD_ALLOW_MILLION=1` 时立即拒绝；设置后先运行共同测试，再因没有百万数据生成器而失败 | 预留入口，不能出具通过证据 |

从仓库根目录执行：

```bash
BACKUP_ASSET_LOAD_LOCAL=ci-bounded bash scripts/test-backup-asset-load.sh
BACKUP_ASSET_LOAD_LOCAL=million-catalog bash scripts/test-backup-asset-load.sh
BACKUP_ASSET_LOAD_LOCAL=process-restart bash scripts/test-backup-asset-load.sh
```

脚本的规模声明约束为分页大小不超过 8、预览并发声明不超过 2、Catalog 记录数至少 10000，租约测试调用者不超过 16；这些声明不构成对生产全局并发或吞吐的测量。百万规模需额外准备专用环境与生成器，仓库未提供，不得把 `million-catalog` 模式名当作百万验证。

`TestControlledProcessSIGKILLThenRestartReconciles` 启动一个受控 helper 进程、发送 SIGKILL，再运行 delivery reconcile；它不重启真实生产 Worker，也不证明生产进程故障注入通过。导出/搜索重启测试仅证明其各自测试覆盖的持久化恢复边界。

AWS Native 在存在真实 live suite 并通过前，不进入支持矩阵。一般功能测试和模拟 Provider 不能代替这项证据。领域行为见[备份资产合同](../spec/domains/README.md)，运行监控见[运维入口](../admin/README.md)。
