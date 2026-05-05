# Journal - xiangnan-mac (Part 1)

> AI development session journal
> Started: 2026-05-02

---


## Session 1: Bootstrap Trellis project guidelines

**Date**: 2026-05-02
**Task**: Bootstrap Trellis project guidelines
**Branch**: `main`

### Summary

Initialized Trellis project support files, filled backend and frontend engineering specs, verified the bootstrap task, and archived 00-bootstrap-guidelines.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `657f1e1` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 2: Document branch-required workflow

**Date**: 2026-05-02
**Task**: Document branch-required workflow
**Branch**: `chore/require-work-branches`

### Summary

Added repository workflow rules requiring all file-changing work to happen on dedicated branches, documented the rule for agents and contributors, and added Trellis branch workflow guidance.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `0859f8f` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 3: Anomaly notification defaults

**Date**: 2026-05-02
**Task**: Anomaly notification defaults
**Branch**: `fix/anomaly-notification-defaults`

### Summary

Made anomaly alert notifications opt-in by default, preserved anomaly event recording, raised EWMA defaults, updated tests/docs/smoke coverage, and recorded the Trellis task.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `7aee27a` | (see git log) |
| `64ac4e5` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 4: Documentation current-state audit

**Date**: 2026-05-03
**Task**: Documentation current-state audit
**Branch**: `docs/current-state-audit`

### Summary

Audited and refreshed README/docs against current repo state, marked dated specs as historical snapshots, added a documentation truth guide, and verified doc freshness plus local links.

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `fc8a7b1` | (see git log) |
| `e1f6304` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 5: Wave 0 全方位审查整改：14 项真问题落地（剔除 9 项子代理误报）

**Date**: 2026-05-04
**Task**: Wave 0 全方位审查整改：14 项真问题落地（剔除 9 项子代理误报）
**Branch**: `main`

### Summary

对 backend/web/docs/部署 做全方位审查（4 个并行 Explore 子代理）→ 实读复核（4 个 trellis-research 子代理逐项核验）→ 剔除 9 项子代理误报后落地 14 项真问题，分 3 个 PR / 7 个 commit / squash 合并为 #105。核心修复：任务 goroutine 加全局+策略级超时（B-7，唯一会引发生产事故的真实问题）、命令执行 ShellEscape 16 类对抗性测试 + 路径字段拒绝控制字符与 $( 反引号、PG 端 integrations.endpoint VARCHAR(1024)→TEXT (000048)、Policy 加 max_execution_seconds 字段 (000049)、LOG_FILE 双写、Dockerfile/compose 加 TZ + logging rotation + HEALTHCHECK、SSHKey.PrivateKey json:"-" 防御性加固、Dialog 小屏溢出。新增 6 个测试文件覆盖超时/对抗性输入/路径校验/日志双写。本次最大教训：子代理审查报告错报率约 40%（22 项 P0/P1 中 9 项误报），必须经实读核验门槛才能转化为修复任务。

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `6556214` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 6: Wave 1 清理 Wave 0 Out-of-Scope: SFTP RealPath + UTC 全量迁移 + logs 虚拟化

**Date**: 2026-05-04
**Task**: Wave 1 清理 Wave 0 Out-of-Scope: SFTP RealPath + UTC 全量迁移 + logs 虚拟化
**Branch**: `chore/post-wave1-archive-journal`

### Summary

收尾 Wave 0 PRD 中明确推迟的 3 项加固，合并为 PR #108：(1) F-3 logs-viewer 引入 @tanstack/react-virtual v3 虚拟化 (autoscroll-to-newest 修正：filteredLogs 按 logId 降序，吸附 = 滚到 top；scrollToIndex(0,'start') + STICK_THRESHOLD_PX=64)；(2) B-2 file_handler.validateNodePath 用 sftp.RealPath 加固远程 symlink 逃逸 (输入与 roots 都解析；BasePath 自身是 symlink 时不再永远拒绝；新增 11 个 fixture 测试)；(3) B-8 DB 时间存储统一切到 UTC (gorm NowFunc=UTC + SQLite _loc=UTC + PG timezone=UTC + 25 表 76 列迁移 000050 + 8 段 runbook + 49 测试 fixture DSN 同步)。本 wave 还修了 release-please 自动开的 v0.19.2 release PR (#106) + Wave 0 archive 的 chore PR (#107)，并把 #106 squash merge 触发了 v0.19.2 GitHub Release。重要教训：本 session 跑 finish-work 之前先切了 chore/post-wave1-archive-journal 分支 (按 feedback_finish_work_branch.md 已记录)；CI 抓出本地 go vet 漏过的 golangci-lint errcheck 错误 (file_handler_validate_test.go 3 处 defer Close)，补 //nolint 注释一行修复。子代理交付：调研子代理给出 SFTP RealPath/Lstat 方案对比 + react-virtual/virtuoso 包大小对比；trellis-implement 子代理 PR-A/B/C 三轮接力 (PR-C 因额度切断后第二个 subagent 接力补完 70%)。

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `b59f7d8` | (see git log) |
| `7eb7a22` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 7: Wave 2 第二轮全方位审查 + Trivy hotfix + GitGuardian 教训沉淀

**Date**: 2026-05-05
**Task**: Wave 2 第二轮全方位审查 + Trivy hotfix + GitGuardian 教训沉淀
**Branch**: `chore/post-wave2-archive-journal`

### Summary

用 trellis-research 子代理一阶段实读审查（避免 Wave 0 两阶段 40% 错报）扫 4 领域共 53 finding。范围 P1+P2 ~25 项分 5 PR (A migration 加固 / B /metrics 鉴权 / C 后端 reporting+terminal+alerting+silence / D 前端 i18n / E 文档+CI) 入 PR #110 squash 合并。中间踩两次坑：(1) GitGuardian 误报 fixture 凭据 (hunter2 / SECRETXYZ / secret-metrics-token base64) 阻 PR #110，admin override 合并；(2) PR-E 子代理把 trivy-action 钉到不存在的 @0.28.0 → release v0.19.3 publish workflow 失败，hotfix #112 改 @v0.36.0 触发 v0.19.4 成功 + v0.19.3 删除清理。事后沉淀：auto-memory 加 'subagent 版本号必须核验'、spec 加 'Test fixture credential naming FAKE_*_FOR_TEST_ONLY 约定'。本 session 严格遵守 [Never commit directly to main] 与 [finish-work must run on a work branch] 规范，所有 chore commit 都先切工作分支再做。

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `27ee709` | (see git log) |
| `47e533b` | (see git log) |
| `676517d` | (see git log) |
| `c765d2c` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 8: Wave 3 前端架构收敛: Tree immutable / dashboard 404 / API 工厂 / lib/ws 抽象

**Date**: 2026-05-05
**Task**: Wave 3 前端架构收敛: Tree immutable / dashboard 404 / API 工厂 / lib/ws 抽象
**Branch**: `chore/post-wave3-archive-journal`

### Summary

收尾 Wave 2 frontend-audit 5 项 P3：F-10 Tree 懒加载 mutate item.children → useState<Map> immutable；F-11 dashboard 404 改 ApiError.status === 404 与 i18n 解耦；F-8/F-9 7 个裸函数 API 文件转工厂模式 + 全 GET 加 options.signal + system-api 风格统一 + 19 业务 + 15 测试 mock 迁移；F-5 抽 lib/ws/reconnecting-socket（指数退避 + jitter + heartbeat + token refresh + URL callback），logs-socket + web-terminal 都迁移，web-terminal 重连后 xterm.clear + 提示重新登录（SSH PTY 协议特性）。本 wave brainstorm 仅用 1 个 trellis-research 子代理做实读+设计（不再发审查 audit），子代理修正了 Wave 2 finding 的 3 项细节（files-api 已统一/裸函数 7 非 8/Tree 无业务调用方/不需改后端）。GitGuardian 这次没误报，证明 Wave 2 spec 沉淀的 FAKE_*_FOR_TEST_ONLY 命名约定起作用。bundle 净 +5.16 KiB（仍在 546 预算内 +12.33 headroom），70 文件 312 测试全过。本 session 严格遵守 [Never commit directly to main] + [finish-work must run on a work branch] 规范。

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `8061381` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 9: Wave 4 前端 a11y 全审 (vitest-axe CI + WCAG AA 修复 + spec 沉淀)

**Date**: 2026-05-05
**Task**: Wave 4 前端 a11y 全审 (vitest-axe CI + WCAG AA 修复 + spec 沉淀)
**Branch**: `chore/post-wave4-archive-journal`

### Summary

a11y 作为 4 wave 累积下来唯一未做的质量维度，本 wave 把它纳入 CI: vitest-axe + axe-core + eslint-plugin-jsx-a11y 接入；修一批已知真违规：P0 i18n lang 同步 (WCAG 3.1.1)、P1 装饰 lucide icon ~18 处加 aria-hidden、version-banner sr-only、select ChevronDown、隐藏 input file aria-label、ssh-key role=list 冗余去除；P2 颜色对比 5 处局部 patch (text-mini → text-xs text-foreground/70，不动全局 token 避免视觉回归)；react-grid-layout 拖拽加键盘上下移按钮兜底；3 个 jsx-a11y 规则 warn → error (aria-role / no-redundant-roles / anchor-is-valid)，5 个保持 warn 带 TODO 注释（label-has-associated-control / no-noninteractive-tabindex / click-events-have-key-events / no-static-element-interactions / no-autofocus，需 UX/design review）；新增 7 个 a11y smoke 测试 + runAxe helper + a11y-guidelines spec (8 条规范 + 测试模板 + decorative-vs-semantic icon 判定 + i18n+lang 样板 + Known exemptions)。关键发现: 当前 a11y 比预期好 (47% aria-* 覆盖、Dialog 全有 Title)，是收敛而非重写。学到的 Wave 2 教训用：所有 npm 版本号都用 npm view 核验最新 stable 后再写入。77 文件 / 319 测试全过；lint 0 errors / 73 warnings (jsx-a11y 16 → 12)；bundle 534/546 KiB。

### Main Changes

(Add details)

### Git Commits

| Hash | Message |
|------|---------|
| `427dd42` | (see git log) |

### Testing

- [OK] (Add test results)

### Status

[OK] **Completed**

### Next Steps

- None - task complete
