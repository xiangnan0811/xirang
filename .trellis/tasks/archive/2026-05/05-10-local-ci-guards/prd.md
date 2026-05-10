# 添加本地 CI 防护钩子

## Goal

在本地提交/推送前增加与 GitHub CI 尽量一致的防护，提前发现 PR CI 常见失败，减少远程 PR 反复修复。

## What I already know

* 用户同意采用两层策略：pre-commit 快检，pre-push 接近 CI 的重检。
* 当前分支：`chore/local-ci-guards-20260510`。
* 仓库已配置 `core.hooksPath=.githooks`。
* 现有 `.githooks/pre-commit` 已做文档新鲜度检查和迁移 UTC 快检。
* CI 定义在 `.github/workflows/ci.yml`：backend lint/download/test/build/govulncheck；frontend npm audit/check/bundle budget；doc freshness；migration UTC safety；PR title。
* Makefile 已有 `setup-hooks`、`backend-test`、`backend-build`、`lint-backend`、`web-test`、`web-build`、`check` 等目标，但没有完整 CI parity 的本地脚本。

## Assumptions (temporary)

* pre-commit 应保持快速，不跑完整 `npm run check` 或全量 backend test。
* pre-push 可以慢一些，作为提交到远端前的 CI parity gate。
* 本任务应优先复用脚本，避免把复杂命令直接写死在 hook 里。

## Open Questions

* None.

## Requirements (evolving)

* 保留现有 `.githooks/pre-commit` 的文档与 migration 检查。
* 增加 pre-commit 快检：至少 `git diff --check`，并对 staged Go 文件做 `gofmt` 检查。
* 增加 pre-push 严格重检：覆盖 `.github/workflows/ci.yml` 的本地可执行部分，失败默认阻止 push。
* 提供一个可手动运行的脚本，避免 hook 与手动验证逻辑漂移。
* 输出清晰失败信息；紧急跳过使用 Git 原生 `--no-verify`，不额外设计弱化开关。

## Acceptance Criteria (evolving)

* [ ] `.githooks/pre-commit` 仍保留现有检查，并新增快速失败检查。
* [ ] `.githooks/pre-push` 存在且可执行。
* [ ] 本地脚本能运行 CI parity 检查或清晰说明缺失依赖。
* [ ] hook 逻辑与 `.github/workflows/ci.yml` 的关键步骤对应。
* [ ] 新增/修改脚本有基础自检或至少可通过 shell 语法检查。

## Definition of Done

* 实现已提交到工作分支。
* 相关脚本通过本地验证。
* Trellis 任务归档并记录会话。

## Out of Scope (explicit)

* 不改 GitHub Actions 远程 CI 配置，除非本地 hook 必须复用现有脚本时发现明显问题。
* 不引入新的 hook 管理依赖（如 husky/pre-commit framework），除非后续明确选择。
* 不自动安装系统级依赖；缺失时给出清晰提示。

## Technical Approach

Use repository-managed `.githooks/` because `core.hooksPath` already points there. Keep hook files thin and move reusable checks into scripts under `scripts/` so developers can run the same gate manually. `pre-commit` stays fast and staged-file focused. `pre-push` runs a strict local CI parity script and exits non-zero on any failure.

## Decision (ADR-lite)

**Context**: PR CI failures often appear only after pushing, especially backend tests and `govulncheck`.
**Decision**: Use a strict `pre-push` gate that blocks failures, with Git's native `--no-verify` as the emergency escape hatch.
**Consequences**: Pushes become slower, but CI failures are caught earlier and the behavior is easy to reason about.

## Technical Notes

* Existing pre-commit: `.githooks/pre-commit`。
* CI source of truth: `.github/workflows/ci.yml`。
* Frontend package scripts: `web/package.json`，`npm run check` 已包含 typecheck/lint/test/build。
* Bundle budget: `web/scripts/check-bundle-budget.mjs`，CI 在 `npm run check` 后运行。
* Backend govulncheck 需可用 Go toolchain；可以用 `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` 避免预安装二进制。
