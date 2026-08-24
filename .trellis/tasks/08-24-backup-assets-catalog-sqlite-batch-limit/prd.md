# Catalog SQLite 默认批次变量上限修复

## Goal

让默认配置下的 Catalog 能把超过一个逻辑批次的真实目录安全写入 SQLite，避免有效、
在线且内容可读的 Rsync mutable point 反复快速落入 `catalog_build_failed`。修复发布后，
现有生产仓库和恢复点应由正常 worker 自动生成完整 Catalog，再继续 Search/UI 预览验收。

## Production evidence

- v0.50.4 正式 Connect 已生成在线、access-active 的 Rsync 仓库、活动 Task 关联和
  observed mutable-head point。
- Catalog generation 1 与 generation 4 均约 0.4 秒失败；中间自动重试亦未恢复。
- 四次终态均为 `catalog_build_failed`，无 active generation、indexed entries 为 0。
- 同期 repository/content/list 均可用，Task 成功且没有 active run，也没有新的 TaskRun。
- 未执行手工 Catalog retry、容器重启、生产 SQL 或 Disconnect。

## Locked requirements

### R1 — 真实 SQLite RED

- 首个测试必须走实际 SQLite 与 `Indexer.Build`/Catalog persistence 路径，使用注册默认
  logical batch size 2000 和足以形成满批次的 canonical records。
- 实现前必须观察到测试因 SQLite 单语句绑定变量上限失败；不能用人为返回的 fake error
  代替根因。
- RED 必须证明 generation 未激活且没有完整 Catalog，和生产签名一致。

### R2 — 逻辑批次与物理写入分离

- `backup_assets.catalog_batch_size` 继续控制 Provider 读取、内存界限和逻辑 flush。
- Catalog 持久化必须再分为数据库安全的物理 insert batch；调用者即使把逻辑批次配置到
  允许上限，也不能生成超过受支持数据库参数界限的单条 INSERT。
- 物理分块不得依赖生产人工降低 setting，也不得为当前数据写特例。

### R3 — Catalog 语义不变

- canonical 顺序、entry identity、parent graph、projection digest、written count、proof
  校验和 active generation compare-and-swap 保持不变。
- 只有整个 Build 完成并通过 proof/fence/source 检查后才能激活 generation。
- 失败 generation 的审计/清理语义不得因物理分块而被标记 complete。

### R4 — 跨数据库与加密边界

- SQLite 回归测试必须使用项目实际 driver/schema；现有 PostgreSQL 行为和测试保持通过。
- Provider locator 继续在 Repository 边界密封，并经过 model hook 保持加密；测试不得以
  明文路径、locator 或内容作为失败输出。
- 不新增 schema/migration，不把 raw SQL/database error 暴露给 API、日志或审计。

### R5 — 发布与生产恢复

- focused Catalog tests、backend quality gate、Trellis check、PR CI 全绿后直接合并。
- 监控 Release Please、正式 release 和 Docker publish；只使用稳定 semver 镜像。
- 升级生产前只读确认 exact container/health/schema/现有 repository/point；保留回退镜像和
  SQLite 逻辑备份。
- 升级后不手工创建 generation；观察正常 Catalog worker 为现有 point 生成 complete
  generation，然后恢复父任务的 Search/UI/health/privacy 验收。

## Out of scope

- 手工修改生产 SQLite、删除失败 generation、伪造 Catalog/Search records。
- 改变 Rsync Task target、备份内容或 repository identity。
- 扩展 Catalog API、搜索语法、内容 renderer 或前端设计。
- 启动节点日志 P1 或重新启用任何节点日志 collector。

## Acceptance criteria

- [ ] AC1：默认 batch size 2000 的真实 SQLite test 在修复前以变量上限失败，并保留 RED。
- [ ] AC2：同一测试修复后完成 Catalog activation，written/indexed count 与输入一致。
- [ ] AC3：物理 insert 分块在逻辑批次大于安全值时生效，并保持 parent/digest/proof 语义。
- [ ] AC4：密封 locator 在持久化后仍为加密值，测试输出无路径/名称/内容/凭据/raw error。
- [ ] AC5：Catalog package focused tests（含重复运行）及 backend 相关/全量 gates 通过。
- [ ] AC6：独立 Trellis check 无未解决 Important/Critical finding，PR CI 全绿并合并。
- [ ] AC7：正式稳定版本和 Docker 镜像发布；生产升级后现有 point 自动产生 complete Catalog。
- [ ] AC8：父任务继续完成真实 file Search、UI 元数据/内容预览、健康与关键错误验收。
