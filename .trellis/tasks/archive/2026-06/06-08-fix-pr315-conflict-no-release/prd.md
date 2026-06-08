# 修复 PR 315 冲突并避免版本发布

## Goal

修复 PR #315 的 merge conflict，并确保本次 Trellis 配置变更不会被当作产品版本发布工作处理。

## Requirements

- 仅处理 PR #315 的冲突与元数据/流程修正，不引入业务代码变更。
- 保留必要的 Trellis 配置更新：`.trellis/.version`、`.trellis/.template-hashes.json`、Codex Trellis agent 模板去重。
- 不再触发 `Publish Docker Images`、`Manual Deploy`、release 创建或任何会发布/部署镜像的工作流。
- 若 PR 描述或历史记录提到测试镜像，把它修正为“本次不需要新版本/新镜像发布”。
- 遵守 Trellis 流程：先规划，启动任务后再执行，完成后 `finish-work` 收口。

## Acceptance Criteria

- [x] PR #315 已关闭，不再尝试合并脏历史分支。
- [ ] 新 PR 描述明确：本次为 Trellis 配置维护，不触发产品版本发布，不需要新镜像。
- [ ] 最终提交只包含 Trellis 配置更新、Trellis 任务记录/归档，以及必要 PR 元数据修正；无业务代码变更。
- [ ] CI 通过或明确说明未触发/无需触发的检查。
- [ ] 未再次触发镜像发布/部署 workflow。

## Confirmed Facts

- PR #315 当前 `mergeable=CONFLICTING`，`mergeStateStatus=DIRTY`。
- `origin/main` 为 `0535fd9`，PR head 为 `1835912`，merge-base 仍为 `4fbfecd`。
- PR 分支复用了 #314 合并前的旧分支历史；#314 已把 `.trellis/workspace/weibo/index.md` 和 `.trellis/workspace/weibo/journal-1.md` 加入 `main`。
- `git merge-tree` 只读模拟显示真正冲突是 `.trellis/workspace/weibo/index.md` 和 `.trellis/workspace/weibo/journal-1.md` 的 add/add 内容冲突。
- Trellis 配置更新文件本身不会产生合并冲突。
- `Release Please` 只在 `main` push 上运行；`Publish Docker Images` 只在 GitHub Release 发布或手动 workflow_dispatch 时运行。本次 Trellis 配置维护不应再触发镜像发布或部署流程。

## Recommended Plan

1. 关闭 PR #315，停止在复用旧历史的脏分支上继续修补。
2. 从 `origin/main` 创建干净新分支，仅重新应用 Trellis 配置更新与本任务收口记录。
3. 先在本地完成 Trellis `finish-work`，确保任务归档和 journal 提交成为最终 head。
4. 再基于最终 head 创建新 PR，PR 描述明确本次只是 Trellis 配置维护，不触发产品版本发布，不需要新镜像。
5. 不再触发 `Publish Docker Images`、`Manual Deploy` 或 release 相关 workflow。
6. 验证新 PR 无 merge conflict，CI 通过或至少已启动并可追踪。

## Decision

- 用户已明确选择关闭 PR #315，并在本地处理完成后重新提交 PR。

## Notes

本任务是轻量流程/冲突修复任务，PRD-only 足够；不需要额外 `design.md` 或 `implement.md`。
