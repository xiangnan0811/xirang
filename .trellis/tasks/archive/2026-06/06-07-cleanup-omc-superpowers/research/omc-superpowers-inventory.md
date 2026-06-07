# Research: OMC and Superpowers installation/configuration inventory

- **Query**: Identify all likely oh-my-claudecode / OMC / superpowers related installation traces and configuration files in this environment, both project-level (`/home/murray/code/xirang`) and user-level (`/home/murray/.claude`, plus obvious CLI/package locations if discoverable without destructive actions). Include recommended action: remove, edit, keep, or needs user decision.
- **Scope**: internal / local environment read-only inventory
- **Date**: 2026-06-07

## Findings

### Summary

OMC is installed/configured at multiple user-level and project-level locations. The strongest install traces are the Claude plugin marketplace/cache entries under `/home/murray/.claude/plugins/marketplaces/omc` and `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5`, plus always-loaded instruction blocks in `/home/murray/.claude/CLAUDE.md`, global OMC settings in `/home/murray/.claude/settings.json`, and project runtime state under `/home/murray/code/xirang/.omc`.

Superpowers appears as a registered marketplace clone/cache under `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace`, but no installed `superpowers@superpowers-marketplace` plugin entry was found in `/home/murray/.claude/plugins/installed_plugins.json`. There are also no obvious global `omc` executable or npm package traces from the read-only command checks.

### User-Level Files Found

| Path | What it is | Related? | Evidence | Recommended action |
|---|---|---:|---|---|
| `/home/murray/.claude/CLAUDE.md` | Global Claude instruction file with two OMC instruction blocks. | OMC | Lines 1-65: `<!-- OMC:START -->`, `<!-- OMC:VERSION:4.13.5 -->`, `# oh-my-claudecode`; lines 68-150: escaped older OMC block `<!-- OMC:VERSION:4.7.8 -->`; line 50: `DISABLE_OMC`, `OMC_SKIP_HOOKS`; line 58: `.omc/...` paths. | **Edit** if removing OMC behavior from always-loaded global context. Keep only non-OMC custom instructions. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-02-24_185225` | Backup of global instructions. | OMC trace | Lines 1-2 contain escaped `OMC:START` / `OMC:END`. | **Remove** if cleanup should delete historical OMC backup traces; **keep** if backups are intentionally retained. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-02-26_165414` | Backup of global instructions. | OMC trace | Lines 1-2: OMC v4.4.4 block; lines include `oh-my-claudecode`, `/omc-teams`, MCP tools. | **Needs user decision**; backup/history only, not active instructions. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-03-06_000249` | Backup of global instructions. | OMC trace | Lines 1-2: OMC v4.5.1; many `oh-my-claudecode`/`omc` references. | **Needs user decision**; backup/history only. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-03-06_140131` | Backup of global instructions. | OMC trace | Lines 1-2: OMC v4.6.0; line 71 `omc team`; line 72 `omc ask`. | **Needs user decision**; backup/history only. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-03-08_173244` | Backup of global instructions. | OMC trace | Same OMC v4.6.0 style block as 2026-03-06 backup. | **Needs user decision**; backup/history only. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-03-20_220009` | Backup of global instructions. | OMC trace | Lines 1-2: OMC v4.7.8; includes `omc team`, `omc-reference`, `.omc` paths. | **Needs user decision**; backup/history only. |
| `/home/murray/.claude/CLAUDE.md.backup.2026-04-29_135301` | Backup of global instructions. | OMC trace | Lines 1-2: OMC v4.9.0; includes OMC agent/skill references. | **Needs user decision**; backup/history only. |
| `/home/murray/.claude/settings.json` | Global Claude settings. | OMC | Lines 41-99 define `omcHud`; line 51 `omcLabel`; lines 52-63 include OMC HUD elements such as `rateLimits`, `ralph`, `autopilot`, `activeSkills`, `agents`, `lastSkill`; line 13 sets `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`. | **Edit** to remove OMC HUD config if OMC is being removed. Keep non-OMC settings separately. Security caveat: this file also contains an auth token at line 4; handle carefully. |
| `/home/murray/.claude/settings.json.bak` | Backup settings file. | Not OMC | Contains only `primaryApiKey`. | **Keep** or user decision; not OMC/superpowers-related. |
| `/home/murray/.claude/config.json` | Claude config/statusline backup-ish config. | Not OMC | Statusline command points to `~/.claude/ccline/ccline`; no OMC/superpowers evidence. | **Keep** unless separately cleaning statusline tooling. |
| `/home/murray/.claude/.omc-config.json` | OMC user config. | OMC | Lines 2-11: `defaultExecutionMode: ultrawork`, team defaults, `setupVersion: v4.5.1`. | **Remove** if uninstalling OMC user config. |
| `/home/murray/.claude/.omc/update-check.json` | OMC update-check state. | OMC | Name/location plus content: `latestVersion: 4.14.5`, `currentVersion: 4.13.5`, `updateAvailable: true`. | **Remove** if deleting OMC user state/cache. |
| `/home/murray/.claude/.omc/update-state.json` | OMC update notification state. | OMC | Content references `plugin:4.11.2-npm:null-claude:4.9.0`. | **Remove** if deleting OMC user state/cache. |
| `/home/murray/.claude/rules/agents.md` | Global private rule file instructing agent orchestration through OMC. | OMC | Line 3: `主力插件：oh-my-claudecode (OMC)`; lines 9-16 list `oh-my-claudecode:*` agents. | **Edit** if removing OMC usage from global behavior. |
| `/home/murray/.claude/rules/codex-preference.md` | Global private rule file mentioning OMC team routing. | OMC | Line 5: `omc-teams`; line 9: `/omc-teams`; line 11: `OMC agents`; line 12: `/omc-teams N:codex`. | **Edit** if removing OMC team references; keep Codex preference content if desired. |
| `/home/murray/.claude/rules/coding-style.md` | Global coding style rules. | No direct OMC/superpowers evidence | Read via context; no OMC/superpowers matches found. | **Keep**. |
| `/home/murray/.claude/rules/git-workflow.md` | Global git workflow rules. | No direct OMC/superpowers evidence | No OMC/superpowers matches found. | **Keep**. |
| `/home/murray/.claude/rules/hooks.md` | Global hook rules. | No direct OMC/superpowers evidence | No OMC/superpowers matches found. | **Keep**. |
| `/home/murray/.claude/rules/performance.md` | Global performance rules. | No direct OMC/superpowers evidence | No OMC/superpowers matches found. | **Keep**. |
| `/home/murray/.claude/rules/security.md` | Global security rules. | No direct OMC/superpowers evidence | No OMC/superpowers matches found. | **Keep**. |
| `/home/murray/.claude/rules/testing.md` | Global testing rules. | No direct OMC/superpowers evidence | No OMC/superpowers matches found. | **Keep**. |
| `/home/murray/.claude/skills/omc-reference/SKILL.md` | User-level native skill containing OMC reference. | OMC | Line 2: `name: omc-reference`; line 3: OMC catalog description; line 13 `oh-my-claudecode:`; lines 43-64 list OMC tools; lines 68-87 list `/oh-my-claudecode:*` workflows/utilities. | **Remove** if removing OMC skills from user skill set. |
| `/home/murray/.claude/skills/omo/SKILL.md` | User-level OmO orchestration skill. | Possibly related orchestration, not explicitly OMC | No `omc`/superpowers term in manifest except `omo` name; uses `codeagent-wrapper` and agents `explore`, `oracle`, `develop`. | **Needs user decision**; not direct OMC/superpowers but may be adjacent orchestration tooling. |
| `/home/murray/.claude/skills/browser`, `codeagent`, `codex`, `dev`, `do`, `gemini`, `skill-install`, `test-cases`, `ui-ux-pro-max` | User-level skills. | Not OMC/superpowers by path; some are orchestration/dev tooling | Directory listing found no direct OMC/superpowers path names except `omc-reference`. | **Keep** unless user wants broader skill cleanup. |
| `/home/murray/.claude/agents` | User-level agents directory. | Not OMC based on listing | Directory exists but was empty in top-level listing. | **Keep**. |
| `/home/murray/.claude/plugins/known_marketplaces.json` | Claude plugin marketplaces registry. | OMC and Superpowers | Lines 26-33 register `superpowers-marketplace` from `obra/superpowers-marketplace`; lines 35-42 register `omc` from `https://github.com/Yeachan-Heo/oh-my-claudecode.git`. | **Edit/remove entries** if deregistering marketplaces. |
| `/home/murray/.claude/plugins/installed_plugins.json` | Installed Claude plugins registry. | No OMC/superpowers installed entry found | Only `frontend-design@claude-plugins-official` appears at lines 4-12. | **Keep**; no OMC/superpowers plugin listed as installed here. |
| `/home/murray/.claude/plugins/config.json` | Plugin config repositories object. | Not OMC/superpowers | Lines 1-3 show empty `repositories`. | **Keep**. |
| `/home/murray/.claude/plugins/blocklist.json` | Plugin blocklist cache. | Not active OMC/superpowers config | Lines 1-17 contain unrelated blocklist entries. | **Keep**. |
| `/home/murray/.claude/plugins/oh-my-claudecode/.usage-cache.json` | OMC usage cache. | OMC | Path name `oh-my-claudecode`; listed as 115 bytes. | **Remove** if deleting OMC plugin cache/state. |
| `/home/murray/.claude/plugins/oh-my-claudecode/.usage-cache-anthropic.json` | OMC usage cache. | OMC | Path name `oh-my-claudecode`; listed as 152 bytes. | **Remove** if deleting OMC plugin cache/state. |
| `/home/murray/.claude/plugins/oh-my-claudecode/.usage-cache-anthropic.json.lock` | OMC usage cache lock file. | OMC | Path name `oh-my-claudecode`; listed as 41 bytes. | **Remove** if deleting OMC plugin cache/state. |
| `/home/murray/.claude/plugins/marketplaces/omc/.claude-plugin/marketplace.json` | Local marketplace clone metadata for OMC. | OMC | Line 3: `name: omc`; line 4: OMC description; line 11: `oh-my-claudecode`; line 13: version `4.13.5`; line 20 homepage. | **Remove** directory or deregister marketplace if removing OMC marketplace clone. |
| `/home/murray/.claude/plugins/marketplaces/omc/.claude-plugin/plugin.json` | OMC plugin manifest in marketplace clone. | OMC | Line 2: `name: oh-my-claudecode`; line 3 version `4.13.5`; line 4 description; lines 8-9 repository/homepage; lines 18-19 skills/MCP server paths. | **Remove** with OMC marketplace clone/cache if uninstalling. |
| `/home/murray/.claude/plugins/marketplaces/omc/` | Full local clone of OMC repository/marketplace. | OMC | Top-level includes `AGENTS.md`, `CLAUDE.md`, `README*`, `agents/`, `skills/`, `hooks/`, `scripts/`, `src/`, `dist/`, `package.json`, `.mcp.json`. | **Remove** if user wants no local OMC marketplace clone. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/.claude-plugin/marketplace.json` | Cached OMC marketplace metadata. | OMC | Same evidence as marketplace clone: `name: omc`, plugin `oh-my-claudecode`, version `4.13.5`. | **Remove** if clearing OMC plugin cache. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/.claude-plugin/plugin.json` | Cached OMC plugin manifest. | OMC | Line 2 `oh-my-claudecode`, version `4.13.5`, repository/homepage lines 8-9, `skills`, `mcpServers`. | **Remove** if clearing OMC plugin cache. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/.mcp.json` | OMC plugin MCP server definition. | OMC | Listed in OMC plugin cache; plugin manifest line 19 points to `./.mcp.json`. | **Remove** with cache. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/agents/` | Cached OMC agents. | OMC | Directory contains `analyst.md`, `architect.md`, `code-reviewer.md`, `executor.md`, `planner.md`, etc. | **Remove** with cache. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/skills/` | Cached OMC skills. | OMC | Directory contains `autopilot`, `ralph`, `ultrawork`, `omc-reference`, `omc-setup`, `omc-teams`, `team`, etc. | **Remove** with cache. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/hooks/` and `scripts/` | Cached OMC hooks/scripts. | OMC | Directory contains `hooks.json`; scripts include `session-start.mjs`, `status.mjs`, `uninstall.sh`, `setup-claude-md.sh`, etc. | **Remove** with cache. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/.in_use` | Cache marker. | OMC | Top-level marker in OMC cache. | **Remove** with cache only if not active. |
| `/home/murray/.claude/plugins/cache/omc/oh-my-claudecode/4.13.5/.orphaned_at` | Cache marker. | OMC | Top-level marker in OMC cache. | **Remove** with cache if cleaning orphaned OMC cache. |
| `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/.claude-plugin/marketplace.json` | Local marketplace clone metadata for Superpowers marketplace. | Superpowers | Line 2 `name: superpowers-marketplace`; line 13 `name: superpowers`; line 16 source `obra/superpowers.git`; lines 23-29 `superpowers-chrome`; lines 53-59 `superpowers-lab`; lines 63-69 `superpowers-developing-for-claude-code`. | **Needs user decision**: remove/deregister marketplace if user wants no Superpowers marketplace; not necessarily installed plugin. |
| `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/README.md` | Superpowers marketplace README. | Superpowers | Lines 1-11 marketplace install instructions; lines 15-32 core Superpowers install details; line 23 `/plugin install superpowers@superpowers-marketplace`; lines 57-74 developing plugin. | **Remove** with marketplace clone if cleanup includes marketplace traces. |
| `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/.claude/settings.local.json` | Settings local to the marketplace clone repository. | Superpowers-adjacent | Lines 4-8 allow Python, episodic-memory MCP, git add/commit/push for that repo. | **Remove** with marketplace clone; otherwise leave local to cloned marketplace repo. |
| `/home/murray/.claude/plugins/plugin-catalog-cache.json` | Claude plugin catalog cache. | OMC/Superpowers cache likely | Compact JSON cache had hits for OMC/superpowers terms; first line contains the entire cache and generated metadata. | **Remove/cache-clear** if clearing plugin marketplace cache; otherwise harmless cache. |
| `/home/murray/.claude/.session-stats.json` | Claude session/tool stats. | OMC usage trace | Multiple lines reference `mcp__plugin_oh-my-claudecode_t__...`, e.g. lines 26-27, 79-82, 111-112. | **Needs user decision**; historical usage telemetry/stats, not active config. |
| `/home/murray/.claude/installed_modules.json` | Installer record for non-OMC modules. | Not OMC/superpowers | Modules listed: `essentials`, `omo`, `do`; no OMC or superpowers. | **Keep** unless cleaning unrelated old installer modules. |
| `/home/murray/.claude/.claude-plugin/plugin.json` | User-level plugin metadata for `essentials`. | Not OMC/superpowers | Lines 2-4: `name: essentials`, development commands. | **Keep**. |
| `/home/murray/.claude/cache/` | Generic Claude cache. | No direct OMC/superpowers evidence from focused listing | Contains `changelog.md`, `my-closed-issues.json`; no OMC/superpowers path names. | **Keep** unless doing broad cache cleanup. |

### Project-Level Files Found

| Path | What it is | Related? | Evidence | Recommended action |
|---|---|---:|---|---|
| `/home/murray/code/xirang/.claude/settings.json` | Project Claude settings. | Not directly OMC/superpowers | Lines 1-73 define Trellis session hooks and `enabledPlugins: {}`; no OMC/superpowers matches. | **Keep**. |
| `/home/murray/code/xirang/.claude/settings.local.json` | Project Claude local settings. | Not directly OMC/superpowers | Lines 3-6 allow `Skill(update-config)` and `Bash(python3 *)`; no OMC/superpowers matches. | **Keep**. |
| `/home/murray/code/xirang/.claude/skills/` | Project Trellis skills. | Not OMC/superpowers | Directory contains Trellis skills such as `trellis-before-dev`, `trellis-brainstorm`, `trellis-check`, `trellis-update-spec`; no direct OMC/superpowers matches in project search. | **Keep**. |
| `/home/murray/code/xirang/.claude/agents/` | Project Trellis agents. | Not OMC/superpowers | Contains `trellis-check.md`, `trellis-implement.md`, `trellis-research.md`; no OMC/superpowers matches in project search. | **Keep**. |
| `/home/murray/code/xirang/.claude/hooks/` | Project Trellis hook scripts. | Not OMC/superpowers | Contains `inject-subagent-context.py`, `inject-workflow-state.py`, `session-start.py`; no OMC/superpowers matches in project search. | **Keep**. |
| `/home/murray/code/xirang/.claude/commands/trellis/` | Project Trellis commands. | Not OMC/superpowers | Contains `continue.md`, `finish-work.md`; no OMC/superpowers matches in project search. | **Keep**. |
| `/home/murray/code/xirang/.omc/project-memory.json` | Project OMC memory/cache file. | OMC by path/state purpose | Lines 1-4 show project memory metadata; line 4 `projectRoot: /home/murray/code/xirang`; line 21 build command; line 22 test command. No literal `omc` inside file, but path is `.omc`. | **Remove** if deleting project-level OMC state; **needs user decision** if memory may contain useful project notes. |
| `/home/murray/code/xirang/.omc/state/hud-stdin-cache.json` | Project OMC HUD/session cache. | OMC | Path `.omc/state`; line 1 has session id, transcript path, cwd, model/effort display info. | **Remove** if deleting project-level OMC runtime state. |
| `/home/murray/code/xirang/.omc/state/sessions/bba3515b-49d1-4722-ba5c-14a1b0df9e4b/hud-state.json` | OMC session HUD state. | OMC | Lines 2-5 timestamp/session info. | **Remove** if deleting project-level OMC runtime state. |
| `/home/murray/code/xirang/.omc/state/sessions/bba3515b-49d1-4722-ba5c-14a1b0df9e4b/session-started.json` | OMC session-start marker. | OMC | Lines 2-6 session id, started_at, cwd, pid, boot_id. | **Remove** if deleting project-level OMC runtime state. |
| `/home/murray/code/xirang/AGENTS.md` | Project agent instructions. | Not OMC/superpowers | Lines 1-21 Trellis block; no OMC/superpowers matches found. | **Keep**. |
| `/home/murray/code/xirang/CLAUDE.md` | Project instruction file. | Missing | `exists=False` from read-only check. | No action. |
| `/home/murray/code/xirang/package.json` | Root package file. | Missing | `exists=False` from read-only check. | No action. |
| `/home/murray/code/xirang/web/package.json` | Frontend package file. | Not OMC/superpowers | Exists size 2296; focused project search found no OMC/superpowers terms. | **Keep**. |
| `/home/murray/code/xirang/backend/go.mod` | Backend Go module file. | Not OMC/superpowers | Exists size 3369; focused project search found no OMC/superpowers terms. | **Keep**. |
| `/home/murray/code/xirang/Makefile` | Project Makefile. | Not OMC/superpowers | Exists size 3597; focused project search found no OMC/superpowers terms. | **Keep**. |
| `/home/murray/code/xirang/.trellis/.runtime/sessions/claude_bba3515b-49d1-4722-ba5c-14a1b0df9e4b.json` | Trellis active session pointer. | Only task-name-related | Line 4 current task `.trellis/tasks/06-07-cleanup-omc-superpowers`. | **Keep**; this is Trellis runtime for the current task, not an OMC install trace. |
| `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/prd.md` | Current Trellis task PRD. | Task-related only | Line 1 title `清理 OMC 和 Superpowers 配置`. | **Keep**. |
| `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/task.json` | Current Trellis task metadata. | Task-related only | Lines 2-4 `cleanup-omc-superpowers` and title. | **Keep**. |
| `/home/murray/code/xirang/.trellis/tasks/archive/2026-05/05-04-wave-0-p0/research/docs-recheck.md` | Archived research note. | Incidental `.omc` mention | Line 22 mentions `.omc/prd.json` as possibly stale in old context. | **Keep** unless doing broad archive cleanup; not active OMC config. |

### Global Command / Package Traces

| Check | Result | Related? | Recommended action |
|---|---|---:|---|
| `shutil.which('omc')` | `None` | No global `omc` executable found in PATH. | No action. |
| `shutil.which('claude')` | `/home/murray/.nvm/versions/node/v22.18.0/bin/claude` | Claude Code executable; not OMC itself. | Keep. |
| `shutil.which('codex')` | `/home/murray/.nvm/versions/node/v22.18.0/bin/codex` | Not OMC/superpowers. | Keep unless unrelated cleanup. |
| `shutil.which('gemini')` | `/home/murray/.nvm/versions/node/v22.18.0/bin/gemini` | Not OMC/superpowers. | Keep unless unrelated cleanup. |
| `npm root -g` | `/home/murray/.nvm/versions/node/v22.18.0/lib/node_modules` | Environment info. | No action. |
| `npm list -g --depth=0 --json` | `@anthropic-ai/claude-code: 2.1.168`, `@google/gemini-cli: 0.31.0`, `@openai/codex: 0.137.0`; no OMC/superpowers package shown. | No OMC/superpowers npm global package found. | No OMC/superpowers package action. |
| Local bin candidates | No OMC/superpowers candidate found under `/home/murray/.local/bin`, `/home/murray/.npm-global/bin` missing, `/home/murray/.bun/bin`, `/usr/local/bin`, `/home/murray/.cargo/bin` missing. | No executable trace found. | No action. |

### Code Patterns / Behavioral Configuration

- `/home/murray/.claude/CLAUDE.md:1-65` injects active OMC guidance into every Claude session via a marker block. It includes skill triggers, OMC kill switches, and `.omc` worktree paths.
- `/home/murray/.claude/CLAUDE.md:68-150` contains an escaped older OMC block after `<!-- User customizations -->`; despite escaped markers, its text still appears in loaded instructions.
- `/home/murray/.claude/settings.json:41-99` configures an `omcHud` object, including `omcLabel`, `ralph`, `autopilot`, `activeSkills`, `agents`, `lastSkill`, and context/session HUD settings.
- `/home/murray/.claude/plugins/known_marketplaces.json:26-42` registers both Superpowers and OMC marketplaces.
- `/home/murray/.claude/plugins/installed_plugins.json:3-13` does **not** list OMC or Superpowers as installed plugins; only `frontend-design@claude-plugins-official` is listed.
- `/home/murray/code/xirang/.omc/` is purely project-level OMC state/memory, not application source code.

### External References

No external web lookup was needed; the task is a local environment inventory.

### Related Specs / Task Docs

- `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/prd.md` — current task PRD; title indicates cleanup of OMC and Superpowers configuration.
- `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/task.json` — current task metadata.

## Caveats / Not Found

- No destructive actions were performed and no files were modified outside `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/research/`.
- No `omc` command was found on `PATH` via read-only command lookup.
- No global npm package with an obvious OMC/superpowers name was found in `npm list -g --depth=0 --json`.
- Superpowers marketplace is present, but no installed Superpowers plugin entry was found in `installed_plugins.json`.
- Session transcripts/history/todos may contain many historical OMC mentions; these were not exhaustively inventoried as configuration/install traces because they are transient logs and can be very large.
- `/home/murray/.claude/settings.json` contains an auth token at line 4. Treat any edits/removal carefully and avoid exposing the token in downstream summaries.
