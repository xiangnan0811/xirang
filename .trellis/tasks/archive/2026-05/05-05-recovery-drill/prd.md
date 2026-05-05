# 自动恢复演练 (Recovery Drill)

## Goal

定期自动将最新备份快照恢复到隔离沙箱节点，执行用户自定义校验脚本，验证"备份真的能用"。记录真实 RTO，失败时高优先级告警。把 Xirang 从"会备份"升级到"敢恢复"。

## Requirements

### 数据模型
- **Policy** 加字段：
  - `drill_enabled` (bool, default=false)
  - `drill_cron` (string, size:128, nullable) — cron 表达式
  - `drill_target_node_id` (uint, nullable, FK→Node) — 沙箱节点
  - `drill_restore_path` (string, size:512) — 沙箱上的恢复目标路径（默认 `/tmp/xirang-drill`）
  - `drill_pre_verify` (text) — 环境准备脚本
  - `drill_verify` (text) — 校验脚本（exit 0 = 成功）
  - `drill_post_verify` (text) — 清理脚本（无论成败都跑）
  - `drill_auto_cleanup` (bool, default=true) — 是否自动清理恢复文件
- **TaskRun.TriggerType** 新增值：`"drill"`
- 数据库迁移：`000052_drill_config`（SQLite + Postgres 双轨）

### 调度器
- `scheduler.go` 新增 `drillLoop()`：60s tick，扫描 `drill_enabled=true` 的 Policy，匹配 `drill_cron` → 触发 drill
- Drill 执行流程（`manager.go`）：
  1. 校验沙箱节点在线 + `drill_target_node_id != 备份源节点`
  2. `restoreBackup()` → 恢复最新快照到沙箱 `drill_restore_path`
  3. 恢复成功 → SSH 执行 `drill_pre_verify` → `drill_verify` → `drill_post_verify`
  4. 若 `drill_auto_cleanup=true` → `rm -rf drill_restore_path`
  5. 全程创建 TaskRun（trigger_type="drill"），记录 `started_at`/`finished_at`/`duration_ms` = RTO
- **pre_verify 失败**：跳过 verify，仍然执行 post_verify + cleanup，TaskRun 标记 failed
- **verify 失败**（exit≠0）：执行 post_verify + cleanup，TaskRun 标记 failed，触发 drill_failed 告警
- **post_verify 失败**：不影响 drill 总体结果（post 是 best-effort）

### 告警
- 新增 alert error_code：
  - `drill_sandbox_unreachable` — 沙箱节点离线（severity=warning）
  - `drill_verify_failed` — 校验脚本失败（severity=critical）
  - `drill_restore_failed` — 恢复本身失败（severity=critical）
- 复用现有 `alerting.RaiseIntegrityCheckFailure()` 模式

### 安全校验
- `drill_target_node_id` 不能等于 Policy 关联的任一备份源节点
- `drill_restore_path` 必须是绝对路径，不含 `..`
- 禁止恢复到系统目录（`/`, `/etc`, `/usr`, `/bin`, `/sbin`, `/boot`）

### API
- Policy CRUD（Create/Update/Get/List）增加 drill 字段
- `POST /api/v1/policies/:id/drill-trigger` — 手动触发一次演练（不等 cron）

### 前端
- Policy 编辑页新增"恢复演练"折叠区：
  - 启用开关
  - Cron 表达式输入（复用 CronGenerator 组件）
  - 沙箱节点下拉（过滤掉 Policy 已关联的备份源节点）
  - 恢复路径输入
  - pre/verify/post 三段脚本输入（Textarea）
  - 自动清理开关
- Policy 详情页展示最近一次 drill 结果（TaskRun 摘要）
- 手动触发按钮

### 兼容性
- 现有 Policy 全部 `drill_enabled=false`，行为不变
- 现有 restore + integrity check 不受影响

## Acceptance Criteria

- [ ] 创建 Policy → 启用 drill + 配 cron + 选沙箱 + 填 verify=`echo ok` → cron 触发 → TaskRun 显示 trigger_type=drill, status=completed, duration_ms>0
- [ ] verify 脚本 exit 1 → Alert 触发（error_code=drill_verify_failed, severity=critical）
- [ ] 沙箱节点离线 → Alert 触发（error_code=drill_sandbox_unreachable, severity=warning）
- [ ] drill_auto_cleanup=true → 演练后沙箱上的恢复路径被删除
- [ ] drill_target_node_id 选到备份源节点 → 保存 Policy 时返回 400
- [ ] POST /policies/:id/drill-trigger → 不等 cron，立即执行一次演练
- [ ] 现有所有 Policy（drill_enabled=false）行为不变
- [ ] 迁移 000052 up + down 双向成功
- [ ] `go test ./...` + `npm run check` 全绿

## Definition of Done

- 单测覆盖：drill scheduler 匹配 / 沙箱校验 / RTO 计算 / 失败告警 / auto_cleanup
- 集成测试：至少 1 个 e2e（小文件 rsync → 沙箱 → verify 通过 + verify 失败两种路径）
- 数据库迁移 SQLite + Postgres 双轨
- Swagger 注解完整
- 用户文档 `docs/recovery-drill.md`
- `go test ./...` + `npm run check` 全绿

## Out of Scope

- 与 app-aware backup 联动（自动注入 DB import 校验脚本）→ 独立后续 PR
- 多沙箱（一个 Policy 恢复到多个沙箱）
- 演练结果历史趋势图（前端图表）
- 演练自动回滚（沙箱清理即覆盖回滚需求）
- 自定义重试（失败后等下次 cron 即可）

## Decision (ADR-lite)

### D1: 调度架构 — Approach A (独立 drill cron 轮询)
**Context**: 独立 cron / 影子 Task / 挂在备份 hook。
**Decision**: **Approach A**。Policy 加 `drill_cron`，scheduler 新增 `drillLoop`（60s tick）。
**Consequences**: 最小改动；drill 独立生命周期；scheduler 多一轮轮询。

### D2: 沙箱目标 — Approach A (选择现有 Node)
**Context**: 选 Node / 自由 IP / 原节点隔离路径。
**Decision**: **Approach A**。Policy 加 `drill_target_node_id`，复用 Node SSH 体系。
**Consequences**: 无需额外凭据管理；需预先注册沙箱节点；需防"沙箱=源节点"。

### D3: 校验脚本 — Approach B (多步骤 pre + verify + post)
**Context**: 单命令 / 多步骤三段式。
**Decision**: **Approach B**。`drill_pre_verify` / `drill_verify` / `drill_post_verify` 三段。
**Consequences**: 与现有 hook 风格一致；错误定位清晰；Policy 加 3 个 text 字段。

### D4: 自动清理 — MVP 纳入
**Context**: 演练文件占磁盘；手动清理易遗漏。
**Decision**: `drill_auto_cleanup` bool 字段 (default=true)。演练结束后 `rm -rf drill_restore_path`。
**Consequences**: 0.2 天成本，消除磁盘撑爆风险。

## Technical Approach

### 实现概要
1. Policy 加 8 个 drill 字段 + 迁移 000052
2. Scheduler 加 `drillLoop()` → 60s 扫描 → cron 匹配 → 调用 `manager.triggerDrill()`
3. `manager.triggerDrill()` → 校验沙箱 → `restoreBackup()` → SSH 执行 verify 三段 → cleanup → 记录 TaskRun + RTO
4. 告警：`alerting.RaiseDrillFailure()` 注册 drill 专用 error_code
5. Policy handler + 前端 Policy 编辑页加 drill 配置区
6. 手动触发端点 `POST /policies/:id/drill-trigger`

### PR 拆分

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | Policy 模型加字段 + 迁移 000052 + Policy handler 适配 + 单测 | 2 天 |
| **PR2** | Scheduler drillLoop + manager.triggerDrill + 沙箱校验 + verify 执行 + auto_cleanup + RTO 记录 + 单测 | 2-3 天 |
| **PR3** | 告警集成 (drill error_codes) + 手动触发端点 + 集成测试 | 1-2 天 |
| **PR4** | 前端 Policy 编辑页 drill 配置区 + 手动触发按钮 + i18n | 1-2 天 |
| **PR5** | 文档 + Swagger + changelog | 0.5-1 天 |

总计：**6.5–10 天**

### 风险与回滚
- 迁移失败：新字段全部 nullable + default，回滚 = drop 新列
- drillLoop panic：独立 goroutine + recover，不影响主 scheduler
- 沙箱节点生产数据被覆盖：`drill_target_node_id != 源节点` 校验 + `drill_restore_path` 强制默认 `/tmp/xirang-drill`

## Technical Notes

### 受影响的关键文件
- `backend/internal/model/models.go` — Policy + drill 字段；TaskRun.TriggerType
- `backend/internal/task/scheduler/scheduler.go` — drillLoop
- `backend/internal/task/manager.go` — triggerDrill()
- `backend/internal/task/hook.go` — 复用于 verify 脚本的 SSH 执行
- `backend/internal/alerting/` — drill error_code + RaiseDrillFailure
- `backend/internal/api/handlers/policy_handler.go` — drill 字段 + 手动触发
- `backend/internal/database/migrations/{sqlite,postgres}/000052_*.sql`
- `web/src/components/policy-editor-dialog.tsx` — drill 配置区
- `web/src/i18n/locales/{zh,en}.ts` — drill 翻译 key
