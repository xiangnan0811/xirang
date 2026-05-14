# 检查并更新 Trellis 配置

## Goal

将本项目 Trellis 从 0.5.12 更新到 0.5.15，保留本地项目约束，审阅更新 diff，完成基础验证并提交到专用分支。

## Requirements

* 全局 Trellis CLI 使用 npm latest 0.5.15。
* 项目 `.trellis/.version` 更新为 0.5.15。
* 执行官方 `trellis update --migrate` 流程，自动更新未被本地修改的 Trellis 管理文件。
* 对上游生成的 `.new` 文件进行人工比对，不覆盖本地分支保护、研究落盘、子代理等项目约束。
* 审阅最终 diff，确认变更范围仅包含 Trellis 更新文件与本任务目录。
* 通过基础校验后提交。

## Acceptance Criteria

* [x] `trellis --version` 输出 0.5.15。
* [x] `trellis update --dry-run --migrate` 显示项目版本、CLI 版本、npm latest 均为 0.5.15。
* [x] Python Trellis 脚本可通过 `py_compile`。
* [x] `python3 ./.trellis/scripts/get_context.py --mode packages` 可正常运行。
* [x] `.new` 副本已审阅并移除，未采纳会删除本地项目约束的上游模板差异。
* [ ] 最终 diff 已审阅并提交到 `chore/trellis-update-20260514`。

## Definition of Done

* Trellis 更新命令完成且 dry-run 复验无待自动更新项。
* 本地约束未被上游模板回退。
* 变更通过基础脚本校验。
* 提交信息遵循仓库风格。

## Technical Approach

使用官方 `trellis update --migrate --create-new` 先应用安全模板更新，对本地修改文件生成 `.new` 后人工比对。保留本地 workflow/AGENTS/project skill 约束，删除未采纳 `.new` 文件，然后运行 dry-run、Python 编译和上下文脚本验证。

## Decision (ADR-lite)

**Context**: Trellis 0.5.15 提供 hook、session context、archive auto-commit 与 manifest pruning 修复，但本项目已有本地工作流约束。

**Decision**: 接受自动更新的 Trellis 运行时文件；不接受会删除本地约束的 `.new` 模板内容。

**Consequences**: 项目获得 0.5.15 修复，同时继续保留“不在 main 直接改动/提交”、研究输出落盘、子代理等待等本地流程规则。

## Out of Scope

* 不修改 Xirang 后端或前端业务代码。
* 不引入新的 Trellis 本地定制。
* 不覆盖已有本地流程约束。
* 不创建 PR 或推送远端，除非用户另行要求。

## Technical Notes

* 当前分支：`chore/trellis-update-20260514`。
* 更新命令：`npm install -g @mindfoldhq/trellis@latest`、`trellis update --migrate --create-new`。
* 已审阅 `.new` 对比：`.trellis/workflow.md.new`、`AGENTS.md.new`、`.agents/skills/trellis-meta/references/local-architecture/workspace-memory.md.new`。
