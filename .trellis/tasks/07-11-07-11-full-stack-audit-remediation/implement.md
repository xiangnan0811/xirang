# 实施清单：全仓审计修复集成

> 代码修复已由审查团队与二次审查成员完成。本清单记录实际落地和本轮收尾步骤，不重新实现功能。

## A. 已落地代码域

- [x] A1 显式环境与 production secret/runtime 校验。
- [x] A2 store-backed 双 CAPTCHA、生成端点限流、前后端独立开关矩阵。
- [x] A3 panel-query、overview、task、policy、drill 的 ownership fail-closed。
- [x] A4 policy drill scripts 加密、历史明文迁移、非 admin 脱敏/写保护。
- [x] A5 task-run traffic paired indexes 与查询错误传播。
- [x] A6 credentials/node metrics/silences/auth 的 frontend API boundary mapping。
- [x] A7 node-detail i18n、共享 status poll、unknown/error/abort/stale-state 防护。
- [x] A8 SLO/Settings loading/error/confirm 与部分 P3 清理。
- [x] A9 operator/security/runtime/frontend regression tests 与公开文档同步。

## B. 本轮 Trellis 校准

- [x] 补齐父任务实际范围、决策、延期与完成语义。
- [x] 补齐七个子任务的实际落地、验收矩阵、验证与状态。
- [x] 为所有子任务记录当前 branch、scope、related files 与 notes。
- [x] 新增本 design/implement，描述跨层数据流和提交边界。
- [x] 更新缺失的可执行 spec：panel-query ownership、双 CAPTCHA、restore drill 双端 ownership、Wave1 DTO mapping。

## C. 最终质量门禁

- [x] `git diff --check`
- [x] Go formatting check
- [x] `cd backend && go test ./...`
- [x] backend `golangci-lint run ./...`（0 issues）
- [x] backend build
- [x] `cd web && env -u NODE_ENV npm run check`（127 files / 547 tests + build）
- [x] `bash scripts/check-doc-freshness.sh` 与 self-test
- [x] paired migration / stale deployment reference / task context validation
- [x] Compose config 与 Nginx 1.29 Alpine 配置语法验证

只有最新一轮全部通过，才可向用户展示提交与归档计划并请求确认。

## D. 拟议工作提交（需用户确认）

1. `fix(security): harden runtime configuration and authentication`
   - environment/config/bootstrap/auth/CAPTCHA/trusted proxies/Swagger/metrics/CSP/deployment docs。
2. `fix(backend): enforce ownership across operational queries`
   - panel/overview/task/policy/drill ownership、query correctness、paired indexes 与测试。
3. `fix(frontend): normalize API data and resilient UI states`
   - DTO mapping、node-detail i18n/polling/error、SLO/Settings 状态与测试。
4. `chore(trellis): align audit remediation tasks and specs`
   - 本任务树、design/implement 与可执行 specs。

实际文件清单在门禁后生成；若共享文件无法安全拆分，以“每个提交可构建/可审查”为优先调整批次。

## E. finish-work 顺序（确认后）

- [ ] 依次创建全部工作提交；不 amend。
- [ ] 记录 `get_context.py --mode record` 输出。
- [ ] 归档六个实现完成子任务：三个 backend 子任务与三个 frontend 子任务（不含 P3）。
- [ ] 保持 `07-11-p3-quality-debt` 和父任务 active。
- [ ] 用 `add_session.py` 写 developer journal，只记录工作提交 hash；让 archive/journal 各自生成独立自动提交。
- [ ] 最终检查 `git status`、`git log` 和 current task；不 push、不建 PR。
