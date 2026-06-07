# 清理 OMC 和 Superpowers 配置

## Goal

清理当前机器上与 oh-my-claudecode / OMC / Superpowers 相关的 Claude Code 配置、插件市场缓存、用户级指令和项目级运行状态，避免后续 Claude 会话继续加载或触发这些工具，同时尽量保留与 OMC/Superpowers 无关的 Claude Code 配置、技能、插件和项目文件。

## Confirmed Facts

- 用户明确表示后续不再使用 `omc`、`superpowers`、`oh-my-claudecode` 等工具，并要求清理项目级与用户级配置。
- 用户已在 2026-06-07 批准 `design.md` / `implement.md` 计划，并明确确认项目生成的 `.omc/` 目录可以删除。
- 研究清单已写入 `research/omc-superpowers-inventory.md`。
- 用户级活跃 OMC 入口包括：
  - `/home/murray/.claude/CLAUDE.md` 中的 OMC 指令块和旧 OMC 文本。
  - `/home/murray/.claude/settings.json` 中的 `omcHud` 配置和 OMC 相关环境配置。
  - `/home/murray/.claude/.omc-config.json` 与 `/home/murray/.claude/.omc/` 状态文件。
  - `/home/murray/.claude/skills/omc-reference/`。
  - `/home/murray/.claude/plugins/known_marketplaces.json` 中的 OMC 与 Superpowers marketplace 注册项。
  - `/home/murray/.claude/plugins/marketplaces/omc/`、`/home/murray/.claude/plugins/cache/omc/`、`/home/murray/.claude/plugins/oh-my-claudecode/`。
- Superpowers 被注册并克隆为 marketplace：`/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/`，但未在 `/home/murray/.claude/plugins/installed_plugins.json` 中发现已安装的 Superpowers 插件条目。
- 当前会话中的 `superpowers:using-superpowers` 注入最可能来自 `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/hooks/session-start`，该 hook 读取缓存内 `skills/using-superpowers/SKILL.md` 并输出 `SessionStart` additionalContext。
- 未发现 `omc` 可执行文件在 `PATH` 中，也未发现明显的全局 npm OMC/Superpowers 包。
- 项目级 OMC 痕迹主要是 `/home/murray/code/xirang/.omc/` 运行状态/项目记忆；项目 `.claude/` 当前主要是 Trellis 配置，未发现 OMC/Superpowers 直接引用。
- `/home/murray/.claude/settings.json` 含认证令牌；后续总结不得泄露其值，编辑必须保留非目标配置。

## Requirements

- 移除或编辑会让后续 Claude Code 会话继续加载 OMC/oh-my-claudecode 指令、技能、插件市场、缓存、Hook/HUD 状态的用户级配置。
- 移除项目内 `.omc/` 运行状态和项目记忆，除非用户要求保留其中的历史项目笔记。
- 取消 Superpowers marketplace 注册并删除其本地 marketplace clone/cache，除非用户要求仅禁用活跃安装项。
- 保留非 OMC/Superpowers 配置，例如 Claude Code 本身、Codex/Gemini CLI、frontend-design 插件、Trellis 项目配置、项目源代码和普通开发规则。
- 修改现有 JSON/Markdown 配置时必须先读取、后精确编辑；不得整文件覆盖敏感配置。
- 删除操作前应列出目标，并在用户确认范围后执行。

## Acceptance Criteria

- [x] `/home/murray/.claude/CLAUDE.md` 不再包含 OMC/oh-my-claudecode 的活跃或旧指令块。
- [x] `/home/murray/.claude/settings.json` 不再包含 OMC HUD 或 OMC 专用环境配置，同时保留认证、状态栏和非目标设置。
- [x] OMC 用户配置/状态目录和技能目录已移除或按确认范围处理。
- [x] OMC 与 Superpowers marketplace 注册项已从 `known_marketplaces.json` 移除。
- [x] OMC/Superpowers marketplace clone、plugin cache、usage cache 已移除或按确认范围处理。
- [x] 项目级 `/home/murray/code/xirang/.omc/` 已移除或按确认范围处理。
- [x] 验证命令确认主要 OMC/Superpowers 关键词不再出现在活跃配置路径中；如仍有历史备份/日志命中，已明确说明。
- [x] 未删除与 OMC/Superpowers 无关的 Claude Code、Trellis、Codex、Gemini、frontend-design 或项目源代码配置。

## Verification Summary

- JSON 校验通过：`/home/murray/.claude/settings.json`、`/home/murray/.claude/plugins/known_marketplaces.json`、`/home/murray/.claude/plugins/installed_plugins.json`、`/home/murray/.claude.json`。
- JSONL 校验通过：`/home/murray/.claude/history.jsonl` 无无效记录。
- 已批准删除目标剩余数量为 0；`CLAUDE.md.backup.*` 剩余数量为 0。
- 活跃配置扫描通过：global CLAUDE.md、settings、rules、plugin registry、user skills、user state、project `.claude/` 均为 0 个目标关键词命中。
- 独立 `trellis-check` 代理验证通过：发现并修复项目记忆 `memory/codex-workflow.md` 中残留的 OMC 推荐内容；最终 16 项检查通过、0 项失败。
- 后端 `go test ./...` 由检查代理验证通过。
- 前端 `npm --prefix web run check` / lint 存在与本任务无关的既有工具链问题：`baseUrl` deprecated TypeScript 配置和 eslint executable not found；未修改相关项目代码。
- 未执行 Trellis archive / session journal，因为该步骤会自动产生 git commit，而用户未授权提交。

## Out of Scope

- 卸载 Claude Code 本体、Codex CLI、Gemini CLI 或非 OMC/Superpowers 插件。
- 清理所有会话 transcript、历史任务或通用 Claude 缓存中的偶发文本命中，除非用户明确要求。
- 修改系统级包管理器或执行 `push/pull/merge/rebase/reset` 等 Git 操作。

## Open Questions

- 无阻塞问题。用户已选择删除历史备份和统计类 OMC/Superpowers 痕迹。

## Scope Decision

- 清理范围采用“删备份统计”：除活跃配置、插件市场、缓存、项目 `.omc/` 外，也删除 `/home/murray/.claude/CLAUDE.md.backup.*` 中的 OMC 备份文件和 `/home/murray/.claude/.session-stats.json` 中的历史使用记录。

## Notes

- 这是复杂清理任务；执行前需要补充 `design.md` 和 `implement.md`，并在计划获批后再进入实现。
