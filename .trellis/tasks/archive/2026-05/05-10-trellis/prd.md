# 更新 Trellis 本地项目文件

## Goal

查询当前项目的 Trellis 本地文件是否落后于已安装 CLI 版本；如果存在可用更新，使用官方 `trellis update` 命令更新项目内 Trellis 生成/管理文件，保持业务代码不变。

## Requirements

* 在非 `main` 分支执行所有会写入仓库的操作。
* 使用 Trellis CLI 的官方更新入口查询和执行更新。
* 更新范围限于 Trellis 相关文件（`.trellis/`、平台集成目录、共享技能层等），不改业务代码。
* 更新后检查变更范围并运行可用的 Trellis 校验/上下文命令确认基础可用。

## Acceptance Criteria

* [ ] 能明确说明是否存在 Trellis 更新。
* [ ] 如果有更新，已运行 `trellis update`。
* [ ] 更新后 `git diff` 仅包含 Trellis/平台集成相关文件。
* [ ] Trellis 基础命令仍可运行。

## Definition of Done

* 记录更新前后的版本信息。
* 实际命令输出支持“已更新/无需更新”的结论。
* 未提交代码，除非用户另行要求。

## Technical Approach

先用 `trellis --version` / `trellis --help` 确认 CLI 与更新提示，再用 `trellis update` 更新本地项目文件。更新完成后通过 `git status`、`git diff --stat` 和 Trellis 上下文/任务校验命令验证结果。

## Decision (ADR-lite)

**Context**: Trellis 本地项目文件由 CLI 管理，直接编辑上游 npm 包或手工覆盖模板容易丢失本地定制。  
**Decision**: 使用 `trellis update` 作为唯一更新入口，并保留生成后的 diff 供检查。  
**Consequences**: 更新会触碰 Trellis 管理文件；若本地文件有用户改动，需根据 CLI 输出和 diff 处理冲突或保留本地定制。

## Out of Scope

* 不修改后端或前端业务代码。
* 不提交、推送或创建 PR。
* 不卸载或重新初始化 Trellis。

## Technical Notes

* 已读取 `.claude/skills/trellis-meta/references/local-architecture/overview.md`。
* 已读取 `.trellis/config.yaml` 和 `.trellis/workflow.md` 的任务/工作流入口。
* `trellis --version` 输出 CLI 版本 `0.5.7`，并提示项目可从 `0.5.0-rc.1` 更新到 `0.5.7`。
