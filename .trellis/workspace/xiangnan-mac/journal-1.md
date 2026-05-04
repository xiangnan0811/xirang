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
