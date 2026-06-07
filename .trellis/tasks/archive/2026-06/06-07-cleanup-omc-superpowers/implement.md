# 清理 OMC 和 Superpowers 配置 — 执行计划

## Preconditions

- 用户已批准创建 Trellis 任务并进入规划。
- 用户已选择“删备份统计”的彻底清理范围。
- 计划获批后才运行 `task.py start` 并执行清理。

## Ordered Checklist

1. 激活 Trellis 任务
   - 运行 `python3 ./.trellis/scripts/task.py start .trellis/tasks/06-07-cleanup-omc-superpowers`。

2. 用户级 Markdown 指令清理
   - 编辑 `/home/murray/.claude/CLAUDE.md`：删除第一个 `<!-- OMC:START --> ... <!-- OMC:END -->` 块，以及 `<!-- User customizations -->` 后转义旧 OMC 块，保留 `# 全局开发配置` 及之后的非 OMC 内容。
   - 编辑 `/home/murray/.claude/rules/agents.md`：移除 OMC 主力插件和 `oh-my-claudecode:*` agent 表，改为不再推荐 OMC；保留通用“需要时使用合适 agent/审查”的非 OMC 原则。
   - 编辑 `/home/murray/.claude/rules/codex-preference.md`：删除 `/omc-teams`、OMC agents 等引用；保留“偏好 Codex 作为后端辅助”的非 OMC 内容（如仍有意义）。

3. 用户级 JSON 配置清理
   - 编辑 `/home/murray/.claude/settings.json`：
     - 删除顶层 `omcHud`。
     - 删除 `env.CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`。
     - 保留认证、模型、statusLine、permissions、enabledPlugins、theme、verbose 等非目标配置。
   - 编辑 `/home/murray/.claude/plugins/known_marketplaces.json`：删除 `omc` 与 `superpowers-marketplace` 两个 marketplace 项，保留官方和其他非目标 marketplace。
   - 不修改 `/home/murray/.claude/plugins/installed_plugins.json`，除非验证发现隐藏 OMC/Superpowers 项；当前只有 `frontend-design@claude-plugins-official`。

4. 删除 OMC/Superpowers 用户级文件和目录
   - 删除 OMC：
     - `/home/murray/.claude/.omc-config.json`
     - `/home/murray/.claude/.omc/`
     - `/home/murray/.claude/skills/omc-reference/`
     - `/home/murray/.claude/plugins/marketplaces/omc/`
     - `/home/murray/.claude/plugins/cache/omc/`
     - `/home/murray/.claude/plugins/oh-my-claudecode/`
   - 删除 Superpowers：
     - `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/`
     - `/home/murray/.claude/plugins/cache/superpowers-marketplace/`
   - 删除插件目录缓存：
     - `/home/murray/.claude/plugins/plugin-catalog-cache.json`

5. 删除用户确认范围内的历史备份/统计痕迹
   - 删除 `/home/murray/.claude/CLAUDE.md.backup.*`。
   - 删除 `/home/murray/.claude/.session-stats.json`。
   - 对 `/home/murray/.claude/history.jsonl` 和 `/home/murray/.claude/.claude.json`：仅删除或清理 OMC/Superpowers 相关历史记录/usage keys；如果精确编辑风险过高，则在执行时改为先报告并让用户选择整文件删除或保留。
   - 删除 `/home/murray/.claude/backups/.claude.json.backup.*` 中包含 Superpowers/OMC 使用记录的备份文件。

6. 删除项目级 OMC 状态
   - 删除 `/home/murray/code/xirang/.omc/`。
   - 保留项目 `.claude/` Trellis 配置、`.trellis/` 当前任务记录和项目源代码。

7. 验证
   - 校验 JSON：`python3 -m json.tool /home/murray/.claude/settings.json`、`python3 -m json.tool /home/murray/.claude/plugins/known_marketplaces.json`、`python3 -m json.tool /home/murray/.claude/plugins/installed_plugins.json`。
   - 验证关键路径不存在或已清理：OMC/Superpowers cache、marketplace、`.omc`、`omc-reference`。
   - 搜索活跃配置路径：`/home/murray/.claude/CLAUDE.md`、`/home/murray/.claude/settings.json`、`/home/murray/.claude/rules/`、`/home/murray/.claude/plugins/known_marketplaces.json`、项目 `.claude/`，确认不再含 `oh-my-claudecode`、`OMC`、`superpowers:using-superpowers`、`superpowers-marketplace`、`obra/superpowers`。
   - 运行 `git status --short`，说明项目内仅 `.omc/` 删除和 Trellis 任务文件变化（或实际结果）。

## Risky Files / Rollback Points

- `/home/murray/.claude/settings.json`：含认证令牌；只用精确编辑，不在总结中输出敏感值。
- `/home/murray/.claude/CLAUDE.md`：全局指令文件；删除 OMC 块后要确认非 OMC 全局开发配置仍在。
- `/home/murray/.claude/history.jsonl`、`/home/murray/.claude/.claude.json`：可能包含大量历史状态；若精确清理会破坏 JSON/JSONL 结构，则优先报告并采用更安全策略。
- `/home/murray/.claude/plugins/cache/superpowers-marketplace/.../.in_use/<pid>`：执行前检查 PID 是否存活；如仍存活，清理后需提示用户重启 Claude Code。

## Review Gate

执行前，用户需要批准本计划。批准后才可开始删除/编辑目标文件。

## Completion Notes

- 本会话仍可能带有已注入的旧上下文；最终建议用户重启 Claude Code 或开新会话，确认 SessionStart 不再注入 OMC/Superpowers 内容。
- 不提交 Git commit，除非用户另行要求。
