# Wave 0 — 全方位审查后的修复整改

## Goal

基于 4 个并行子代理的全方位审查 + 4 份逐项实读复核报告，整改一批**经核实真实存在**的安全/稳定性/文档/UX 问题。原始审查报告错报率 ~40%，本任务范围已剔除全部误报。

## What I already know

- 主代理直接核验：审查报告 P0-1（SSHKey 私钥暴露）、B-1（Integration.Secret 暴露）、B-3（SSRF）均为误报；B-2（file_handler EvalSymlinks）修复方向不可行（远程 SFTP 路径不能用本地 EvalSymlinks）。
- 4 份复核报告（见 research/）覆盖后端安全、前端 UX、文档治理、迁移与部署，对剩余 P0/P1 finding 逐项核验。
- 总计 22 项原始 P0/P1 中，真实 7、部分真实 6、误报 9。
- 仅 **B-7 任务 goroutine 无超时** 是会引发生产事故的真实安全/稳定性问题；其余多为质量/治理/防御性改进。

## Research References

- [`research/backend-security-recheck.md`](research/backend-security-recheck.md) — B-4/B-5/B-6/B-7/B-8 复核：B-5/B-6 误报；B-7 真实，B-4/B-8 部分真实
- [`research/frontend-ux-recheck.md`](research/frontend-ux-recheck.md) — F-1~F-5 复核：仅 F-1 部分真实，其余皆误报（confirm-dialog 已普遍使用、WS 清理已完整、列表已 cap+pagination）
- [`research/docs-recheck.md`](research/docs-recheck.md) — 文档 7 项复核：CLAUDE.md/MEMORY.md migration 版本过时真实；README_backend.md / web/.env.example 都已存在（误报）
- [`research/migration-deploy-recheck.md`](research/migration-deploy-recheck.md) — 部署 5 项复核：日志无落盘、缺 TZ 真实；dev compose 误报（`./:/workspace` 已持久化）

## Requirements（经核实真实需做项，按 PR 分组）

### PR-A：文档与配置校准（低风险、纯文本/yaml）
- [A1] CLAUDE.md:50 migration 版本 `000033_node_metric_samples_extend` → `000047_alert_deliveries_drop_error`
- [A2] CLAUDE.md handler 计数 "30+" → "40+"（实测 ~42 个 handler 文件）
- [A3] auto-memory MEMORY.md 的 migration 版本 `000030` → `000047`
- [A4] 新增 `CODE_OF_CONDUCT.md`（参考 Contributor Covenant 2.1）
- [A5] `docs/env-vars.md` 增加"敏感字段加密策略"章节（说明 GORM hooks 自动加解密、哪些字段属于敏感、统一处理路径）
- [A6] `docker-compose.prod.yml` 给 backend service 加 `logging.options.max-size / max-file`（避免 docker logs 无限增长）
- [A7] `deploy/allinone/Dockerfile` 安装 tzdata + `ENV TZ=Asia/Shanghai`；`docker-compose.prod.yml` + `.env.example` 增加 `TZ` 注释/示例
- [A8] `deploy/allinone/Dockerfile` 末尾补 `HEALTHCHECK CMD curl -f http://127.0.0.1:8080/healthz || exit 1`（compose 已配，补镜像层兜底）

### PR-B：前端小屏与防御性加固（前端 + model 层防御）
- [B1] `web/src/components/ui/dialog.tsx`：基类加 `mx-4 max-h-[calc(100dvh-4rem)] overflow-y-auto`，使长内容/小屏不溢出
- [B2] **防御性深度防御**：`backend/internal/model/models.go` SSHKey.PrivateKey 的 json tag `json:"private_key"` → `json:"-"`。当前 handler 已通过 `toSSHKeyResponse()` 脱敏，此改动是为防止未来有人误写 `c.JSON(model.SSHKey{})`；同时检查 grep 确认无任何位置依赖该字段被序列化

### PR-C：后端真问题修复（核心稳定性）
- [C1] **B-7 任务 goroutine 超时**（`backend/internal/task/runner.go:270, 530`）：注入全局执行超时 + 支持 Policy 级 `MaxExecutionSeconds` 字段，超时主动 cancel context；executor 端确保 context 被传递到 SSH session、cmd 执行；增加单元/集成测试
- [C2] **B-4 命令构造对抗性测试**：在 `backend/internal/task/executor/executor_test.go` 中补对抗性输入测试（`$(cmd)`、反引号、换行、`'; rm -rf /;'`、unicode shell 元字符），覆盖 RsyncSource/TargetPath/ExcludeRules/Command；同时在 `policy_handler` 创建/更新校验中默认拒绝路径含 `;` `|` `` ` `` `$(`  等可疑字符（可通过环境变量关闭以兼容历史数据）
- [C3] **D-1 Endpoint 字段 PG 容量对齐**：新建迁移 000048，PostgreSQL 端 `ALTER TABLE integrations ALTER COLUMN endpoint TYPE TEXT`；模型 size tag 同步调整。SQLite 端无需变更（VARCHAR 是软约束）但保持双轨迁移文件对称
- [C4] **D-2 应用日志可选落盘**：`backend/internal/util/logger.go` 增加 `LOG_FILE` 环境变量支持；不设置时保持 stdout-only 行为；文档同步

### Out of Scope（本任务不做）
- B-2 file_handler 远程路径符号链接校验：原 finding 修复方向不可行；正确方案需引入 SFTP Lstat 链路追踪，工作量大，暂列入后续单独任务
- B-8 时区统一 UTC：复核确认无功能 bug，仅一致性问题，工作量 M，单独任务
- F-3 列表虚拟化：现有 `cap + pagination` 已缓解，无明显卡顿前不动
- 所有已确认的误报（P0-1、B-1、B-3、B-5、B-6、F-2、F-4、F-5、D-3、D-4-pre-commit、D-6）
- 原计划 Wave 1/2/3：本任务先把"经核实清单"做完；后续若仍要扩范围，再起新任务并经过同样的"实读核验"门槛

## Acceptance Criteria

- [ ] PR-A 所有文件改动通过 lint/格式校验，CLAUDE.md 与 MEMORY.md 版本号、handler 计数与实际一致
- [ ] PR-B `web/src/components/ui/dialog.tsx` 在 320px / 768px / 1280px 三个断点视觉验证不溢出、长内容可滚动；`grep -r "model.SSHKey{" backend/` 确认无 `c.JSON(model.SSHKey{...})` 直接序列化
- [ ] PR-C C1：新增任务执行超时单元测试，模拟 executor 卡死，断言任务在配置秒数后被 cancel 且状态置为 timeout/failed
- [ ] PR-C C2：新增对抗性输入单元测试，至少覆盖 6 类 shell 元字符；handler 层校验有专门测试
- [ ] PR-C C3：迁移 000048 在 SQLite + PostgreSQL 双轨上 up/down 均成功
- [ ] PR-C C4：`LOG_FILE` 设置后日志同时写文件与 stdout；未设置时行为不变
- [ ] `cd backend && go test ./...` 全绿
- [ ] `cd web && npm run check` 全绿
- [ ] `bash scripts/smoke-e2e.sh` 通过（如本地有运行环境）

## Definition of Done

- 三个 PR 各自独立可 review、可回滚
- 每个 PR 的 commit message 遵循 `feat|fix|docs|chore(<scope>): ...` 规范
- 高风险变更（C1 任务超时机制、C3 迁移）在 PR 描述中明确风险与回滚方案
- CHANGELOG / release notes 同步（C 系列触发版本号 patch bump）

## Technical Approach

- 三个 PR 串行（A → B → C）但每个内部并行实施。A 风险最低、最快、能立即生效；B 是低风险加固；C 是核心修复需要最仔细 review。
- C1（任务超时）的实现需要在 PRD 实施阶段进一步设计：超时是否走 SIGTERM → SIGKILL 两段、是否需要在 TaskRun 表新增 timeout_seconds 字段（如否，则只用全局/策略级配置）。这部分细节留到 Phase 2 实施前再做小型 brainstorm。
- C2 默认字符黑名单可通过 `BACKUP_PATH_ALLOW_SHELL_META=true` 关闭，避免破坏既有数据。
- C3 迁移在 PostgreSQL 上需要确保表无锁冲突，但 ALTER COLUMN TYPE TEXT 在 PG 是 metadata-only（同 family）变更，不会重写表，安全。

## Decision (ADR-lite)

**Context**：原 4-wave 整改计划基于 4 份子代理审查报告，但实读复核发现错报率 ~40%（22 项 P0/P1 中 9 项为误报）。

**Decision**：放弃原 4-wave 闷头实施计划。本任务（Wave 0 重定义版）只做"经实读复核确认真实"的 14 项，分 3 个独立 PR 推进。后续若用户仍要做更大范围审查，必须重新走"实读核验门槛"再产出可执行清单。

**Consequences**：
- 工作量从原估计的 4-wave 下降到 3 PR
- 避免大量精力消耗在虚假问题上
- 留下 B-2 / B-8 / F-3 等"复核显示需独立设计"的项作为后续任务的候选
- 强化了"子代理审查报告必须经实读核验才能转化为修复任务"的工作流

## Technical Notes

- 当前分支：`audit-wave0-security-p0`（已基于 origin/main 创建）
- 任务目录：`.trellis/tasks/05-04-wave-0-p0/`
- 验证命令：
  - `cd backend && go test ./...`
  - `cd web && npm run check`
  - `bash scripts/smoke-e2e.sh`（如本地有运行环境）
- 关键引用文件：
  - 后端：`internal/task/runner.go`、`internal/task/executor/executor.go`、`internal/util/logger.go`、`internal/model/models.go`、`internal/database/migrations/{sqlite,postgres}/`
  - 前端：`web/src/components/ui/dialog.tsx`
  - 文档：`CLAUDE.md`、`docs/env-vars.md`、auto-memory `MEMORY.md`
  - 部署：`docker-compose.prod.yml`、`deploy/allinone/Dockerfile`、`backend/.env.example`
