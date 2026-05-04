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
