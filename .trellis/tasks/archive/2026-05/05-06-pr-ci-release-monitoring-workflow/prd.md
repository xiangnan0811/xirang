# PR CI Release Monitoring Workflow

## Goal

把本次审查分支的后续交付流程做完整：推送分支、创建 Pull Request、监控 PR CI，失败则修复并重跑，成功后合并；合并后继续监控 Release Please / auto release 相关 workflow。并把这套“开 PR 后必须监控到合并与发布链路稳定”的要求固化到仓库流程文档中，供以后 AI 与维护者遵循。

## What I Already Know

* 用户要求：提 PR，监控 PR 中 CI job 的运行情况；没问题就合并，有问题就修复；完成之后还需要监控 auto release 情况。
* 用户补充“这套流程以后都需要”，意味着不能只在本次聊天里执行，还要写入 repo-local 流程说明。
* 当前分支是 `audit/comprehensive-project-review`，工作树在本任务创建前是干净的，上一轮审查已有工作提交与 Trellis archive/journal 提交。
* 仓库流程已有基础规则：不直接提交到 `main`，通过 PR 合入，Release Please 管理发布，Docker Hub 是唯一官方公开镜像源。
* 已有相关文档入口：`AGENTS.md`、`CONTRIBUTING.md`、`.github/PULL_REQUEST_TEMPLATE.md`、`docs/release-maintainers.md`。
* 历史决策：GitHub Release 是版本权威，Docker Hub 是唯一公开镜像源，稳定 semver only，release/deploy 规则要写入 repo 文档和流程面。

## Assumptions

* 本任务只补齐流程规则和执行本次 PR，不再修改上一轮审查的业务代码。
* PR 标题应保持 Conventional Commits，便于 Release Please 正确识别。
* 合并方式优先使用 squash merge，并保留规范 PR 标题。
* 合并 PR 后，如果 Release Please 生成/更新 Release PR，应监控其 workflow 状态；如果没有生成正式 release，则仍需确认相关 workflow 没有失败。

## Requirements

* 在仓库流程文档中明确：PR 创建后必须监控 required CI jobs；CI 失败要修复并重新监控，不能把失败 PR 留给用户。
* 在仓库流程文档中明确：PR 合并后必须监控 post-merge automation，包括 Release Please PR/update、release workflow、Docker image publish 或明确说明本次没有触发正式 release。
* 推送当前分支到 GitHub 并创建 PR。
* 监控 PR required checks；如果失败，修复问题、提交、推送，并继续监控直到通过或遇到真实外部阻塞。
* 在 checks 通过后合并 PR。
* 合并后监控 auto release / Release Please / publish-images 相关状态；失败则修复或记录外部阻塞。
* 任务完成前归档 Trellis 任务并记录 journal。

## Acceptance Criteria

* [ ] 流程规则已写入 repo-local 文档/模板。
* [ ] 文档/流程变更已提交。
* [ ] 分支已推送并创建 PR。
* [ ] PR CI jobs 已监控并通过，或失败已修复后通过。
* [ ] PR 已合并到 `main`。
* [ ] 合并后的 Release Please / auto release 相关状态已检查并记录。
* [ ] 本地 `main` 已同步到 `origin/main`。

## Definition Of Done

* Worktree clean.
* PR merged.
* Post-merge automation status inspected.
* Trellis task archived and session recorded.

## Out Of Scope

* 改变 release/version 合约本身。
* 手动创建正式 GitHub Release 或 Docker tag，除非自动链路失败且需要明确修复。
* 重跑上一轮已通过的完整业务审查，除非 CI 失败要求修复。

## Technical Notes

* Task directory: `.trellis/tasks/05-06-pr-ci-release-monitoring-workflow`.
* Likely docs/process files: `AGENTS.md`, `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `docs/release-maintainers.md`.
* Relevant prior work commits on current branch:
  `68762b7`, `5002633`, `bcad0b5`, `4844a0e`, `3d6d987`, `18b2f02`.
