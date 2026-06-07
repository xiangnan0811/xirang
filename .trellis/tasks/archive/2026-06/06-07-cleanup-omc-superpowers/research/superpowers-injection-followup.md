# Research: Superpowers injection follow-up

- **Query**: Locate every local file under `/home/murray/.claude` and `/home/murray/code/xirang` that contains or configures `using-superpowers`, `superpowers:`, `superpowers-marketplace`, `obra/superpowers`, or skill/plugin manifests that could load available skills from Superpowers.
- **Scope**: internal
- **Date**: 2026-06-07

## Findings

### Executive Summary

The injected `superpowers:using-superpowers` text is explained by a cached Superpowers plugin hook at `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/hooks/session-start`. That hook reads `skills/using-superpowers/SKILL.md` and emits `hookSpecificOutput.additionalContext` containing the phrase `Below is the full content of your 'superpowers:using-superpowers' skill` when `CLAUDE_PLUGIN_ROOT` is set.

There is still no installed Superpowers entry in `/home/murray/.claude/plugins/installed_plugins.json`; it only lists `frontend-design@claude-plugins-official`. However, `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/` contains a complete cached Superpowers plugin, including hook, skills, commands, and `.in_use/3697733` marker.

The currently available un-namespaced skills (`browser`, `codeagent`, `codex`, `dev`, `do`, `gemini`, `omo`, `skill-install`, etc.) are installed as user skills under `/home/murray/.claude/skills/` and project skills under `/home/murray/code/xirang/.claude/skills/`; they are not from the Superpowers cache. The Superpowers cache has its own skill set (`using-superpowers`, `brainstorming`, `writing-plans`, etc.), but those are not copied into `/home/murray/.claude/skills/`.

### Files Found

| File Path | Description | Evidence | Recommended cleanup action |
|---|---|---|---|
| `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/hooks/session-start` | Most likely source of the injected text. | Lines 17-18 read `skills/using-superpowers/SKILL.md`; line 35 constructs `You have superpowers` and `full content of your 'superpowers:using-superpowers' skill`; lines 49-51 emit Claude Code `hookSpecificOutput.additionalContext` when `CLAUDE_PLUGIN_ROOT` is present. | Remove/disable this cached plugin hook or remove the cached Superpowers plugin directory if not intended to load. Also investigate why Claude Code still sets `CLAUDE_PLUGIN_ROOT` for this cache despite no installed entry. |
| `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/skills/using-superpowers/SKILL.md` | Skill content injected by the session-start hook. | Lines 1-4 define `name: using-superpowers`; lines 10-16 enforce Superpowers skill usage; lines 44-46 require relevant skills before any response/action. | Remove only as part of removing the cached Superpowers plugin; deleting this file alone may leave a broken hook. |
| `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/.claude-plugin/plugin.json` | Cached plugin manifest for Superpowers. | Lines 2-4: `name: superpowers`, version `5.0.7`; lines 9-10 reference `https://github.com/obra/superpowers`. | Remove cached plugin if no Superpowers load is desired. |
| `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/.claude-plugin/marketplace.json` | Cached marketplace metadata inside Superpowers plugin clone. | Lines 2-12 describe `superpowers-dev` and plugin `superpowers` version `5.0.7`. | Remove with the cached plugin directory if cleanup is chosen. |
| `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/.in_use/3697733` | Runtime/cache in-use marker. | File exists with `{"pid":3697733,"procStart":"1063619163"}`. | Check whether PID `3697733` is still live before deleting cache; stale marker could indicate cache cleanup was skipped. |
| `/home/murray/.claude/plugins/known_marketplaces.json` | Registers Superpowers marketplace. | Lines 26-34 register `superpowers-marketplace` from GitHub repo `obra/superpowers-marketplace`, install location `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace`, `autoUpdate: true`. | Remove `superpowers-marketplace` entry if the marketplace itself should no longer be available/autoupdated. |
| `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/.claude-plugin/marketplace.json` | Local marketplace clone manifest. | Line 2 `name: superpowers-marketplace`; lines 13-20 list plugin `superpowers` from `https://github.com/obra/superpowers.git`; additional Superpowers-related plugins at lines 23-30, 53-60, 63-80. | Delete marketplace clone together with registry entry if Superpowers marketplace should be removed. |
| `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/README.md` | Marketplace README with install instructions. | Line 10 `/plugin marketplace add obra/superpowers-marketplace`; line 23 `/plugin install superpowers@superpowers-marketplace`; line 32 repository `https://github.com/obra/superpowers`. | Remove with marketplace clone if cleanup is chosen. |
| `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/.claude/settings.local.json` | Settings local to the marketplace clone. | Lines 3-9 allow Python, episodic-memory MCP, and git commands. | Remove with marketplace clone; not a load source for `using-superpowers`. |
| `/home/murray/.claude/plugins/installed_plugins.json` | Installed plugin registry. | Lines 3-13 list only `frontend-design@claude-plugins-official`; no `superpowers@superpowers-marketplace`. | No Superpowers removal needed here; this file is evidence of absence. |
| `/home/murray/.claude/plugins/plugin-catalog-cache.json` | Large plugin catalog cache. | Contains `using-superpowers` and `obra/superpowers` on line 1 because the entire catalog is minified to one line. | Optional cache clear if trying to purge search noise; not the direct injection source. |
| `/home/murray/.claude/settings.json` | User-level Claude settings. | Lines 35-37 enable only `frontend-design@claude-plugins-official`; no Superpowers enabled plugin. | No Superpowers cleanup needed here. |
| `/home/murray/code/xirang/.claude/settings.json` | Project-level Claude settings. | Lines 5-72 configure hooks and `enabledPlugins: {}` at line 72; no Superpowers enabled plugin. | No Superpowers cleanup needed here. |
| `/home/murray/code/xirang/.claude/settings.local.json` | Project local settings. | Lines 3-6 allow `Skill(update-config)` and `Bash(python3 *)`; no Superpowers enabled plugin. | No Superpowers cleanup needed here. |
| `/home/murray/.claude/history.jsonl` | Historical command usage. | Lines 218-219 record `/plugin marketplace add obra/superpowers-marketplace` and `/plugin install superpowers@superpowers-marketplace`; lines 231, 240, 241, 245 record `/superpowers:brainstorm` prompts. | Historical only; optional privacy/history cleanup, not necessary to stop current injection. |
| `/home/murray/.claude.json` | Claude global state and usage counters. | Lines 1077-1190 `skillUsage` includes `superpowers:brainstorm`, `superpowers:brainstorming`, `superpowers:writing-plans`, and `superpowers:subagent-driven-development`. Lines 820-919 are the Xirang project state. | Historical/usage state only; editing usage counters is not necessary to stop injection. |
| `/home/murray/.claude/backups/.claude.json.backup.1780842819242` | Backup of Claude global state. | Lines 1104, 1108, 1164, 1168 contain Superpowers skill usage keys. | Backup/history only; optional cleanup if purging references. |
| `/home/murray/.claude/backups/.claude.json.backup.1780843065389` | Backup of Claude global state. | Lines 1106, 1110, 1166, 1170 contain Superpowers skill usage keys. | Backup/history only. |
| `/home/murray/.claude/backups/.claude.json.backup.1780843341402` | Backup of Claude global state. | Lines 1106, 1110, 1166, 1170 contain Superpowers skill usage keys. | Backup/history only. |
| `/home/murray/.claude/backups/.claude.json.backup.1780843452993` | Backup of Claude global state. | Lines 1106, 1110, 1166, 1170 contain Superpowers skill usage keys. | Backup/history only. |
| `/home/murray/.claude/backups/.claude.json.backup.1780843543789` | Backup of Claude global state. | Lines 1107, 1111, 1167, 1171 contain Superpowers skill usage keys. | Backup/history only. |
| `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/prd.md` | Current task PRD records prior inventory. | Line 18 states Superpowers is registered/cloned as a marketplace but absent from installed plugins. | Keep; task documentation. |
| `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/research/omc-superpowers-inventory.md` | Prior research output. | Lines 13, 45, 63-65 mention Superpowers marketplace and installed-plugin absence. | Keep; prior research. |

### Skill Directories and Plugin Manifests

#### User skills currently available outside Superpowers

These are loaded from `/home/murray/.claude/skills/` and match the available un-namespaced skills in the session. They are not under the Superpowers cache:

| Skill | File Path | Evidence | Recommended cleanup action |
|---|---|---|---|
| `browser` | `/home/murray/.claude/skills/browser/SKILL.md` | Lines 1-4 frontmatter: `name: browser`, CDP browser automation description. | No Superpowers cleanup; separate user skill. |
| `codeagent` | `/home/murray/.claude/skills/codeagent/SKILL.md` | Lines 1-4 frontmatter: `name: codeagent`. | No Superpowers cleanup; separate user skill. |
| `codex` | `/home/murray/.claude/skills/codex/SKILL.md` | Lines 1-4 frontmatter: `name: codex`. | No Superpowers cleanup; separate user skill. |
| `dev` | `/home/murray/.claude/skills/dev/SKILL.md` | Lines 1-4 frontmatter: `name: dev`. | No Superpowers cleanup; separate user skill. |
| `do` | `/home/murray/.claude/skills/do/SKILL.md` | Frontmatter exists with `name: do` from directory inventory. | No Superpowers cleanup; separate user skill. |
| `gemini` | `/home/murray/.claude/skills/gemini/SKILL.md` | Frontmatter exists with `name: gemini` from directory inventory. | No Superpowers cleanup; separate user skill. |
| `omo` | `/home/murray/.claude/skills/omo/SKILL.md` | Lines 1-4 frontmatter: `name: omo`. | No Superpowers cleanup; separate user skill. |
| `skill-install` | `/home/murray/.claude/skills/skill-install/SKILL.md` | Lines 1-4 frontmatter: `name: skill-install`. | No Superpowers cleanup; separate user skill. |
| `test-cases` | `/home/murray/.claude/skills/test-cases/SKILL.md` | Frontmatter exists with `name: test-cases` from directory inventory. | No Superpowers cleanup; separate user skill. |
| `ui-ux-pro-max` | `/home/murray/.claude/skills/ui-ux-pro-max/SKILL.md` | Frontmatter exists with `name: ui-ux-pro-max` from directory inventory. | No Superpowers cleanup; separate user skill. |
| `omc-reference` | `/home/murray/.claude/skills/omc-reference/SKILL.md` | Frontmatter exists with `name: omc-reference` from directory inventory. | No Superpowers cleanup; separate user skill. |

#### Project skills currently available outside Superpowers

These are loaded from `/home/murray/code/xirang/.claude/skills/` and are Trellis project skills, not Superpowers:

| Skill | File Path | Evidence | Recommended cleanup action |
|---|---|---|---|
| `trellis-before-dev` | `/home/murray/code/xirang/.claude/skills/trellis-before-dev/SKILL.md` | Frontmatter `name: trellis-before-dev`. | No Superpowers cleanup. |
| `trellis-brainstorm` | `/home/murray/code/xirang/.claude/skills/trellis-brainstorm/SKILL.md` | Frontmatter `name: trellis-brainstorm`. | No Superpowers cleanup. |
| `trellis-break-loop` | `/home/murray/code/xirang/.claude/skills/trellis-break-loop/SKILL.md` | Frontmatter `name: trellis-break-loop`. | No Superpowers cleanup. |
| `trellis-check` | `/home/murray/code/xirang/.claude/skills/trellis-check/SKILL.md` | Frontmatter `name: trellis-check`. | No Superpowers cleanup. |
| `trellis-meta` | `/home/murray/code/xirang/.claude/skills/trellis-meta/SKILL.md` | Frontmatter `name: trellis-meta`. | No Superpowers cleanup. |
| `trellis-spec-bootstarp` | `/home/murray/code/xirang/.claude/skills/trellis-spec-bootstarp/SKILL.md` | Frontmatter `name: trellis-spec-bootstarp`. | No Superpowers cleanup. |
| `trellis-update-spec` | `/home/murray/code/xirang/.claude/skills/trellis-update-spec/SKILL.md` | Frontmatter `name: trellis-update-spec`. | No Superpowers cleanup. |

#### Superpowers cached skills

`/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/skills/` contains the following Superpowers skills:

- `brainstorming`
- `dispatching-parallel-agents`
- `executing-plans`
- `finishing-a-development-branch`
- `receiving-code-review`
- `requesting-code-review`
- `subagent-driven-development`
- `systematic-debugging`
- `test-driven-development`
- `using-git-worktrees`
- `using-superpowers`
- `verification-before-completion`
- `writing-plans`
- `writing-skills`

Important examples:

| Skill | File Path | Evidence | Recommended cleanup action |
|---|---|---|---|
| `using-superpowers` | `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/skills/using-superpowers/SKILL.md` | Lines 1-4 define the skill injected by the hook. | Remove with cached plugin if unwanted. |
| `brainstorming` | `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/skills/brainstorming/SKILL.md` | Lines 1-4 define `name: brainstorming`. | Remove with cached plugin if unwanted. |
| `writing-plans` | `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/skills/writing-plans/SKILL.md` | Lines 1-4 define `name: writing-plans`; line 52 references `superpowers:subagent-driven-development`. | Remove with cached plugin if unwanted. |
| `subagent-driven-development` | `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/skills/subagent-driven-development/SKILL.md` | Lines 1-4 define `name: subagent-driven-development`; lines 268-277 reference related `superpowers:` skills. | Remove with cached plugin if unwanted. |

### Code Patterns

#### Session-start injection pattern

The cache hook contains the exact injection path:

- `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/hooks/session-start:17-18` reads the skill file:

```bash
using_superpowers_content=$(cat "${PLUGIN_ROOT}/skills/using-superpowers/SKILL.md" 2>&1 || echo "Error reading using-superpowers skill")
```

- `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/hooks/session-start:35` creates the injected text:

```bash
session_context="<EXTREMELY_IMPORTANT>\nYou have superpowers.\n\n**Below is the full content of your 'superpowers:using-superpowers' skill - your introduction to using skills. For all other skills, use the 'Skill' tool:**\n\n${using_superpowers_escaped}\n\n${warning_escaped}\n</EXTREMELY_IMPORTANT>"
```

- `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/hooks/session-start:49-51` emits Claude Code hook output:

```bash
elif [ -n "${CLAUDE_PLUGIN_ROOT:-}" ] && [ -z "${COPILOT_CLI:-}" ]; then
  printf '{\n  "hookSpecificOutput": {\n    "hookEventName": "SessionStart",\n    "additionalContext": "%s"\n  }\n}\n' "$session_context"
```

This is the strongest evidence that cached Superpowers plugin code can inject `superpowers:using-superpowers` despite the installed plugin registry not listing Superpowers.

#### Marketplace/cache mismatch pattern

- `/home/murray/.claude/plugins/known_marketplaces.json:26-34` registers and auto-updates `superpowers-marketplace`.
- `/home/murray/.claude/plugins/installed_plugins.json:3-13` lists only `frontend-design@claude-plugins-official`.
- `/home/murray/.claude/plugins/cache/superpowers-marketplace/superpowers/5.0.7/` nevertheless exists and includes plugin manifest, hook, and skills.

This suggests cleanup should distinguish between:

1. marketplace registration (`known_marketplaces.json` + marketplace clone),
2. plugin installation registry (`installed_plugins.json`), and
3. cached plugin material (`plugins/cache/superpowers-marketplace/...`) that can still contain executable hooks.

#### Available skill source pattern

The available skills `browser`, `codeagent`, `codex`, `dev`, `do`, `gemini`, `omo`, `skill-install`, `test-cases`, `ui-ux-pro-max`, and `omc-reference` correspond to directories under `/home/murray/.claude/skills/`. Their frontmatter names match their directory names and do not contain Superpowers references in the scanned headers.

The available Trellis skills correspond to directories under `/home/murray/code/xirang/.claude/skills/`. They are project-specific and not Superpowers.

### Related Specs

- `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/prd.md` — task PRD; line 18 records prior finding that Superpowers marketplace is registered/cloned but not installed in `installed_plugins.json`.
- `/home/murray/code/xirang/.trellis/tasks/06-07-cleanup-omc-superpowers/research/omc-superpowers-inventory.md` — prior research inventory; includes marketplace and cache findings.

### External References

No external web search was needed; the task requested local filesystem research only. Local manifests reference:

- `https://github.com/obra/superpowers-marketplace` — Superpowers marketplace repository, present in `/home/murray/.claude/plugins/marketplaces/superpowers-marketplace/.git/config` and README.
- `https://github.com/obra/superpowers` — Superpowers plugin repository, present in cached plugin manifests and marketplace manifest.

## Caveats / Not Found

- No `superpowers@superpowers-marketplace` entry was found in `/home/murray/.claude/plugins/installed_plugins.json`.
- No Superpowers enabled plugin was found in `/home/murray/.claude/settings.json` or `/home/murray/code/xirang/.claude/settings.json`.
- No `using-superpowers` directory was found under `/home/murray/.claude/skills/` or `/home/murray/code/xirang/.claude/skills/`; it exists only under the Superpowers plugin cache.
- Many transcript files under `/home/murray/.claude/transcripts/` contain Superpowers strings; these are historical conversation records, not load configuration. They were not enumerated individually in the table because they are not plugin/skill loading sources.
- The current session tool-output files under `/home/murray/.claude/projects/-home-murray-code-xirang/.../tool-results/` contain the search terms because this research generated them; they are not original load configuration.
- The `.in_use/3697733` marker indicates the cached plugin may have been in use or may have stale state. Cleanup should verify the process before removing cache material.
