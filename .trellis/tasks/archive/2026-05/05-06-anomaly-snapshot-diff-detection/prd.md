# 快照异常变更检测 (Anomaly Snapshot Diff Detection)

## Goal

备份完成后自动分析快照差异，通过统计基线检测异常文件变更（勒索加密、误删、异常大范围改动），触发告警。在好的备份被覆盖之前发现问题。

## Requirements

### 数据模型
- 新增 `snapshot_diff_history` 表（迁移 000053）：
  - `id`, `policy_id` (index), `task_id` (index), `task_run_id` (index)
  - `added_count`, `removed_count`, `changed_count` — 文件变更统计
  - `total_size_bytes` — 变更总大小
  - `ransom_suffix_hits` — 匹配勒索后缀的文件数
  - `created_at`
- Anomaly 告警通过现有 `AnomalyEvent` + `Alert` 表，不新增告警模型

### 触发时点
- Task runner 中，备份 TaskRun 成功完成后：
  1. 调 `restic diff latest-1 latest` 拿到 DiffResult
  2. 解析统计 + 后缀匹配
  3. 写入 `snapshot_diff_history`（无论是否异常——作为基线种子）
  4. 若已有 ≥N 条历史记录 → 计算基线 → 判断是否异常
  5. 若异常 → 写入 `AnomalyEvent` + 提升为 `Alert`
- 首次备份（无历史基线）：仅记录，不检测

### 异常检测算法
- **维度 1 — 变更数量异常**：
  - 查最近 N 条（默认 10）diff history 的 `added_count + removed_count + changed_count`
  - 计算移动平均 μ 和标准差 σ
  - 当前值 > μ + k·σ（默认 k=3）→ 异常
- **维度 2 — 勒索后缀检测**：
  - 在 diff 的 Changed/Added 文件路径中匹配已知勒索后缀
  - 预置后缀列表：`.encrypted`, `.locked`, `.crypt`, `.ransom`, `.xxx`, `.zzz`, `.enc`, `.lock`, `.crypto`, `.wannacry`, `.pay`, `.decrypt`
  - 任何文件匹配 → 触发 `ransomware_pattern` 类型告警（不管数量阈值）

### 告警
- 新增 AnomalyEvent metric 值：`snapshot_churn` / `ransomware_pattern`
- Alert error_code 格式：`XR-SNAPSHOT-CHURN-{policyID}` / `XR-SNAPSHOT-RANSOM-{policyID}`
- Severity：`snapshot_churn` = warning，`ransomware_pattern` = critical
- 复用现有 `anomaly.alerts_enabled` 系统设置控制是否提升为 Alert

### 异步执行
- 检测逻辑在独立 goroutine 中运行，不阻塞 task runner
- 超时控制：SSH 连接 + restic diff 执行 ≤ 30s

### API
- 无需新增端点（复用现有 AnomalyEvent 列表 / Alert 列表）
- 前端 AnomalyEvent 列表已支持按 Detector/Metric 过滤

### 兼容性
- 非 restic 类型 Task：跳过检测
- 无历史基线：静默跳过，仅记录 history
- EWMADetector / DiskForecastDetector：不受影响

## Acceptance Criteria

- [ ] 备份成功 → diff history 写入 → 在基线范围内 → 不产生 AnomalyEvent
- [ ] 模拟异常：手动创建大量文件 → 下次备份 → change_count > μ+3σ → AnomalyEvent(snapshot_churn) + Alert(warning)
- [ ] 模拟勒索：创建 `.encrypted` 文件 → 下次备份 → AnomalyEvent(ransomware_pattern) + Alert(critical)
- [ ] 首次备份 → diff history 写入但无基线 → 不触发检测
- [ ] 非 restic task → 跳过
- [ ] 现有 EWMADetector / DiskForecastDetector / task runner 测试全部 GREEN

## Definition of Done

- 单测覆盖：基线计算（移动平均+标准差）/ 后缀匹配 / 阈值判断 / 首次跳过
- 集成测试：正常 diff + 异常 diff 两种路径
- 迁移 000053（SQLite + Postgres 双轨）
- `go test ./...` 全绿
- 用户文档 `docs/snapshot-anomaly-detection.md`

## Out of Scope

- 自动暂停 Policy（MVP 只告警不暂停）
- 用户自定义后缀列表（系统设置动态配置 → 后续 PR）
- 基于文件内容的深度检测
- 跨 Policy 关联分析
- 前端独立 UI（复用 anomaly-events 列表）

## Decision (ADR-lite)

### D1: 触发方式 — Approach A (内联到 TaskRun 完成回调)
**Context**: Detector 周期扫描 vs 内联到 runner。
**Decision**: **Approach A**。task runner 备份成功后异步调用检测函数。
**Consequences**: ✅ 实时；⚠️ runner 多一个调用点。

### D2: 基线存储 — Approach A (新表 snapshot_diff_history)
**Context**: 新表 vs 复用 AnomalyEvent.details。
**Decision**: **Approach A**。新增 `snapshot_diff_history` 表，持久化每次 diff 统计。
**Consequences**: ✅ 可审计可追溯；⚠️ 迁移 000053。

### D3: 基线算法
**Context**: 选统计方法。
**Decision**: 移动平均 μ + k·σ 阈值（k=3，样本 N=10）。首次无基线跳过。
**Consequences**: ✅ 简单可解释；⚠️ 对波动大的策略敏感度偏低。

### D4: 勒索后缀
**Context**: 硬编码 vs 可配置。
**Decision**: **MVP 硬编码 12 个常见后缀**。后续 PR 加系统设置。
**Consequences**: ✅ 零配置开箱即用；⚠️ 自定义需后续扩展。

## Technical Approach

### 实现概要
1. 新增 `snapshot_diff_history` 表 + 迁移 000053
2. 新增 `backend/internal/anomaly/snapshot_diff.go` — `AnalyzeSnapshotDiff()` 函数
3. 在 `backend/internal/task/runner.go` 的备份成功路径，异步调 `AnalyzeSnapshotDiff()`
4. `AnalyzeSnapshotDiff()`：调 restic diff → 解析 → 写 history → 算基线 → 判断异常 → 写 AnomalyEvent + Alert
5. 单测 + 集成测试
6. 文档

### PR 拆分

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | snapshot_diff_history 模型 + 迁移 000053 + 后缀匹配 + 基线算法 | 1.5 天 |
| **PR2** | runner 集成 + AnalyzeSnapshotDiff 主逻辑 + Alert 提升 | 2 天 |
| **PR3** | 单测 + 集成测试 + 文档 | 1 天 |

总计：**4.5 天**

### 风险与回滚
- 迁移失败：drop 新表，零数据损失
- SSH 超时：30s context timeout，超时 = 跳过本次检测
- runner 改动：新增调用点 ≤ 5 行，不影响现有流程

## Technical Notes

### 受影响的关键文件
- `backend/internal/anomaly/snapshot_diff.go` — NEW: 核心检测逻辑
- `backend/internal/anomaly/snapshot_diff_test.go` — NEW: 单测
- `backend/internal/model/models.go` — SnapshotDiffHistory 模型
- `backend/internal/task/runner.go` — 备份成功后调 AnalyzeSnapshotDiff
- `backend/internal/database/migrations/` — 迁移 000053
- `backend/cmd/server/main.go` — 可能需要迁移 SnapshotDiffHistory（AutoMigrate 或手动）

### 复用现有能力
- `parseDiffOutput()` from `snapshot_diff_handler.go` — 解析 restic diff
- `executor.DialSSHForNode()` + `RunSSHCommandOutput()` — SSH + 命令执行
- `anomaly.NewRaiseFn()` — Finding → AnomalyEvent + Alert
- `anomaly.Finding` — 检测结果载体
