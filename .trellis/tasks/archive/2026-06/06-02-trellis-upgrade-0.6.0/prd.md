# 升级 Trellis 配置到 0.6.0-beta.21

## Goal

本机 Trellis CLI 已从 0.5.15 升级到 0.6.0-beta.21，项目中的 `.trellis/` 配置和平台文件已通过 `trellis update --migrate --force` 同步升级。

## Requirements

- 将 Trellis 项目文件从 0.5.15 升级到 0.6.0-beta.21
- 保留用户数据（workspace, tasks, spec, .developer）
- 不破坏现有工作流

## 变更概要

### 新增文件（10 个）
- `trellis-spec-bootstarp` 内置技能（`.claude/skills/` + `.agents/skills/`，各 5 个文件）

### 模板更新（38 个文件）
- 核心脚本：task.py, task_context.py, task_store.py, session_context.py, workflow_phase.py
- Hooks：session-start.py, inject-workflow-state.py, inject-subagent-context.py
- Agents：trellis-check.md, trellis-implement.md
- Skills：trellis-before-dev, trellis-brainstorm, trellis-check, trellis-continue, trellis-start, trellis-meta
- Config：config.yaml（新增 session_auto_commit + channel.worker_guard）
- Codex：agents, hooks, config.toml

### 用户修改文件覆盖（4 个）
- `.trellis/workflow.md`
- `AGENTS.md`
- `.agents/skills/trellis-meta/references/local-architecture/workspace-memory.md`
- `.codex/config.toml`

## Acceptance Criteria

- [x] `trellis update --migrate --force` 执行成功（0.5.15 → 0.6.0-beta.21）
- [x] `.trellis/.version` 已更新为 `0.6.0-beta.21`
- [x] 备份文件在 `.trellis/.backup-2026-06-02T15-32-49/`
- [ ] 提交到分支 `chore/trellis-upgrade-0.6.0`
- [ ] finish-work 归档任务

## Notes

- 轻量级任务，仅 PRD，无需 design.md / implement.md
- 备份目录 `.trellis/.backup-*` 已被 `.gitignore` 排除，不会被提交