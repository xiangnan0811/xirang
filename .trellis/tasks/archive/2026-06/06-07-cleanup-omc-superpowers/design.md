# 清理 OMC 和 Superpowers 配置 — 技术设计

## Boundaries

### Clean up

用户级活跃配置、缓存、插件市场、历史备份/统计中与 OMC/oh-my-claudecode/Superpowers 直接相关的内容：

- `/home/murray/.claude/CLAUDE.md` 中的 OMC 指令块和旧 OMC 文本。
- `/home/murray/.claude/settings.json` 中的 OMC 专用配置：`omcHud`，以及 OMC 安装引入的 `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`。
- `/home/murray/.claude/.omc-config.json`、`/home/murray/.claude/.omc/`。
- `/home/murray/.claude/skills/omc-reference/`。
- `/home/murray/.claude/plugins/known_marketplaces.json` 中的 `omc` 与 `superpowers-marketplace` 注册项。
- `/home/murray/.claude/plugins/marketplaces/omc/`、`/home/murray/.claude/plugins/cache/omc/`、`/home/murray/.claude/plugins/oh-my-claudecode/`。
- `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/`、`/home/murray/.claude/plugins/cache/superpowers-marketplace/`。
- `/home/murray/.claude/plugins/plugin-catalog-cache.json` 中的插件目录缓存。
- `/home/murray/code/xirang/.omc/` 项目级 OMC 状态。
- 用户已确认删除的历史备份/统计类痕迹：`/home/murray/.claude/CLAUDE.md.backup.*`、`/home/murray/.claude/.session-stats.json`、与 Superpowers/OMC 使用记录相关的 `.claude` 备份/历史条目。

### Preserve

- Claude Code 本体、认证、代理地址、模型环境变量、statusLine、主题、verbose、非目标 permissions。
- `frontend-design@claude-plugins-official` 已安装插件和官方 marketplace。
- Codex/Gemini CLI 与相关用户技能，除非文件本身只是在配置 OMC 路由。
- 项目 `.claude/` 中 Trellis hooks/skills/agents/commands。
- `.trellis/` 当前任务和研究产物。
- 项目源代码、Go/Node 依赖配置、Makefile。
- 普通安全、测试、编码风格、Git 工作流规则。

## Data / Configuration Flow

- Claude Code 会从用户级 `/home/murray/.claude/CLAUDE.md` 加载全局指令；因此必须删除其中 OMC 块，但保留 `# 全局开发配置` 之后的非 OMC 内容。
- Claude Code 会读取用户级 `settings.json`；因此必须精确移除 OMC 键，不得整文件覆盖或泄露认证令牌。
- Claude Code 插件系统会读取 `known_marketplaces.json` 和 `plugins/cache/...`；Superpowers 的 SessionStart 注入来自缓存目录内的 hook，因此只移除 marketplace 注册不足以阻止注入，必须删除 Superpowers cache。
- 项目 `.omc/` 是 OMC runtime/project-memory 状态；删除不会影响项目源代码。

## Compatibility and Safety

- 当前 Claude 会话已经加载了旧指令和 hook 注入；清理文件后，本会话上下文不会完全“失忆”。最终验证以磁盘文件为准，并提示用户重启 Claude Code 或开启新会话确认。
- 删除操作使用显式白名单路径，不使用通配符删除父目录。
- 对 JSON 文件先读取、精确编辑、再用 JSON parser 校验。
- 如果 Superpowers cache 的 `.in_use/<pid>` 指向仍存活的进程，先报告；默认仍可移除 registry 和 marketplace，但 cache 删除需谨慎处理，避免破坏正在运行的插件代码路径。
- 不清理所有 transcripts，因为当前清理过程本身会持续产生 OMC/Superpowers 字符串；把 transcript 当作历史日志，不作为 active config。

## Rollback Shape

- Git 仓库内的 `.omc/` 删除可通过工作区 diff 审查；用户级文件不一定受 Git 管理。
- 不创建新的用户级备份，因为用户明确要求环境干净；Trellis 研究和计划记录保留清理前的路径清单，但不会保存敏感令牌值。
- 如果用户希望可逆清理，可在执行前改为“移动到隔离备份目录”，但当前范围按“删除备份统计”执行。

## Trade-offs

- 删除 `CLAUDE.md.backup.*` 和使用统计能减少历史痕迹，但不可从这些备份恢复旧全局指令。
- 保留非 OMC 技能（如 `codex`、`gemini`、`browser`）能避免误删用户仍可能需要的工具，但 `~/.claude/skills/` 不会变成最小安装状态。
- 不清理 transcripts 能避免删除大量会话历史和当前任务记录，但关键词全盘搜索仍可能在 transcript 中命中历史文本。
