# Catalog 生命周期 race 稳定性

## Goal

消除 Catalog 续租丢失与统一 builder teardown 测试在 race/高负载下对 5ms 数据库时序的隐含依赖，恢复发布后主干门禁全绿，同时保持生产 Catalog 心跳、租约和 Provider 生命周期语义不变。

## Requirements

- 将 GitHub Actions run `32811380329` 中 `TestCatalogIndexerRenewalLossCancelsBuild` 和 run `32814904444` 中 `TestRecoveryPointSourceLifecycleCatalogWaitsForUnifiedBuilderTeardown` 作为已观测 RED；不得用重跑成功掩盖两次独立出现的同类时序失败。
- 失败夹具必须在 Provider 枚举已确定进入后才注入续租丢失，使测试真正验证“运行中的枚举收到续租失败后被取消并完成统一 teardown”。
- 同步必须使用 test-owned channel/context，不得依赖 sleep、更大的 wall-clock timeout、数据库执行速度或 race detector 调度。
- 最小修复限于测试夹具和测试装配；不得移动生产 heartbeat 启动点，不得改变 lease Acquire/Renew/Release、generation、Provider session、active-build registry 或 recovery-point source lifecycle 行为。
- 若确定性 RED 或审查证明生产实现存在独立缺陷，停止测试-only 假设，先更新 PRD/design 再修改生产代码。
- 不改变 schema、settings、API、Swagger、frontend、状态值、错误码、日志或隐私边界。
- 生产升级继续暂停；节点日志 collectors 继续关闭，直到修复版本全链路发布且父任务真实数据预览验收完成。

## Acceptance Criteria

- [x] 根因文档记录两次 CI RED、生产调用顺序、旧夹具竞态窗口和选定同步边界。
- [x] 两个测试都显式证明 Provider 已进入枚举后才允许 lease renewer 返回 `ErrLeaseFenceLost`。
- [x] 续租丢失仍会取消 Provider context、完成 builder teardown、释放/清理 build/lease/generation 资源，并保留原有错误分类。
- [x] 精确 selector 在普通模式高重复和 `-race` 高重复下稳定通过，无 data race、goroutine/channel 泄漏或超时兜底依赖。
- [ ] Catalog 包测试、backup-asset CI race selector、backend lint/test/build 和 diff/format 检查通过。
- [x] Trellis implement 自检与独立 Trellis check 均无未解决 finding。
- [ ] PR 全部 required checks 绿色后合并；合并后主干 CI 绿色，后续稳定 release 与 Docker 双架构镜像发布并核验标签。
- [ ] 固定 release 发布前不向生产执行新命令；发布后生产命令不包含 shell `test` 或 `cd`，具备只读预检、备份、精确回滚与健康验收。

## Notes

- 本任务是父任务 `08-21-backup-assets-release-acceptance` 的 P0 发布阻断子任务，不替代父任务真实数据 Catalog/Search/UI 验收。
- 任务文档不得记录生产标识、路径、locator、凭据、内容或原始敏感错误。
