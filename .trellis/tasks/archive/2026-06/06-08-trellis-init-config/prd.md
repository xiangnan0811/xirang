# 初始化并对齐 Trellis 配置

## Goal

确认本项目的 Trellis 本地初始化可用，并将项目内 Trellis 管理文件对齐到本机已安装的当前 beta 版。

## Requirements

- 不创建额外功能代码，仅处理 Trellis 本地配置、模板版本和可用性验证。
- 保留现有 `.trellis/tasks/`、`.trellis/spec/`、`.trellis/workspace/` 等用户数据。
- 确认 Claude Code 集成文件存在并可输出 SessionStart / UserPromptSubmit 上下文。
- 确认 Codex 集成文件存在，并修复当前 beta 更新中已知的 Codex agent prelude 重复问题。
- 确认 Trellis CLI、本地脚本、任务状态查询和 spec 索引读取可用。

## Acceptance Criteria

- [x] `trellis --version` 返回 `0.6.0-beta.22`。
- [x] `.trellis/.version` 记录为 `0.6.0-beta.22`。
- [x] `trellis update --dry-run` 显示 `Already up to date!`。
- [x] `.claude/settings.json` 注册 Trellis SessionStart、UserPromptSubmit 和 sub-agent context hooks。
- [x] `.claude/agents/trellis-research.md`、`.claude/agents/trellis-implement.md`、`.claude/agents/trellis-check.md` 存在。
- [x] `.codex/agents/trellis-implement.toml` 和 `.codex/agents/trellis-check.toml` 不再包含重复的 context-loading prelude。
- [x] `python3 ./.trellis/scripts/get_context.py --mode packages` 可读取 backend/frontend spec layers。
- [x] `python3 ./.trellis/scripts/task.py current --source` 可正常报告当前任务来源。

## Notes

本任务为轻量配置任务，PRD-only 足够；不需要额外 `design.md` 或 `implement.md`。
