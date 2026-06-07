# 提交 Trellis 配置更新并创建 PR

## Goal

将本项目 Trellis 配置更新提交到当前工作分支，创建 PR，并确认后续真实测试环境可拉取构建镜像进行验证。

## Requirements

- 提交当前 Trellis 配置更新文件：`.trellis/.version`、`.trellis/.template-hashes.json`、`.codex/agents/trellis-check.toml`、`.codex/agents/trellis-implement.toml`。
- 遵循项目 Git 规范：提交信息使用 conventional commit，PR 前查看完整分支差异。
- 修改后按项目记忆调用 Codex 审查。
- 创建 PR，并包含清晰 summary 与 test plan。
- 检查 GitHub Actions / 镜像构建状态；若镜像尚未产出，给出可跟踪的 PR/CI 信息和后续测试环境拉取方式。
- 按 Trellis finish-work 流程完成任务归档与会话记录。

## Acceptance Criteria

- [x] Trellis 配置更新提交到当前分支。
- [x] 已按项目记忆尝试 Codex 审查；本机缺少 `codex-wrapper`，记录 `CODEX_FALLBACK` 后完成本地阻塞验证且未发现问题。
- [x] PR 已创建并包含本次变更说明与测试计划：<https://github.com/xiangnan0811/xirang/pull/315>。
- [x] 已检查 CI / 镜像构建状态，测试镜像 `docker.io/linnea7171/xirang:0.44.315` / `docker.io/linnea7171/xirang:v0.44.315` 可拉取。
- [ ] Trellis 任务已按 finish-work 流程收口。

## Notes

本任务为轻量流程任务，PRD-only 足够；不需要额外 `design.md` 或 `implement.md`。
