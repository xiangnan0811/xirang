# 依赖更新治理与 CI 去重

## Goal

将普通依赖更新从高频、逐依赖 PR 改为低频、按生态分组的可控维护流程，同时保留及时的安全告警与安全修复通道，消除依赖 PR 上重复执行的 CI，并固化本项目 Codex 默认使用子代理的执行偏好。

## Background

- `.github/dependabot.yml` 当前每周一检查 Go modules、npm 和 GitHub Actions，开放 PR 上限分别为 5、5、3。
- 2026-08-11 远程共有 14 个开放 PR，其中 13 个来自 Dependabot，恰好占满上述三个生态的上限；另一个是 Release Please PR #386。
- 历史 Dependabot PR 中有 24 个已合并、28 个关闭未合并、13 个仍开放，现有策略造成持续维护噪音。
- GitHub API 当前报告 Dependabot vulnerability alerts 与 automated security fixes 均未启用。
- `.github/workflows/ci.yml` 同时监听所有分支的 `push` 与 `pull_request`，导致 PR 分支同时触发两套 CI。

## Requirements

### Routine version updates

- R1. 普通版本更新改为每月检查一次，并使用 `Asia/Shanghai` 时区的固定低峰时段。
- R2. Go modules 的 minor/patch 更新合并为一个组；npm 生产依赖与开发依赖分别成组；GitHub Actions 的 minor/patch 更新合并为一个组。
- R3. 普通 major 更新不由月度维护流自动创建 PR，必须另建升级任务完成兼容性评估、验证与发布说明审查。
- R4. 开放普通版本更新 PR 的上限与分组数量匹配，避免单依赖 PR 再次占满 13 个槽位。
- R5. 不启用 Dependabot 自动合并；所有依赖变更继续经过现有 PR 审查与 CI。

### Security updates

- R6. 启用 GitHub Dependabot vulnerability alerts 与 automated security fixes，安全告警不受月度普通版本更新时间窗延迟。
- R7. 若安全修复只能通过 major 升级完成，必须保留可见告警并转为人工高优先级升级任务，不能因普通 major 更新策略静默遗漏。

### CI behavior

- R8. `pull_request` 继续运行完整 PR CI；`push` 仅对 `main` 运行，避免同一 PR 提交同时触发 push 与 pull-request 两套 CI。
- R9. 不在本任务中引入路径过滤或削减现有必需检查，避免扩大 CI 行为变更范围。

### Migration and cleanup

- R10. 新配置通过 PR 合并并完成必需 CI 后，仅按预先快照关闭现有 13 个分散 Dependabot PR，并以各 PR 的完整 `headRefOid` 校验和条件删除其机器人分支；随后在 GitHub 受支持的 Dependabot Web UI 中为 `gomod /backend`、`npm /web` 和 `github-actions /` 各手动触发一次（总计恰好三次）`Check for updates`。三个任务必须全部进入终态 `success` 才能记录关联分组 PR 并进入安全更新启用步骤；`queued` 仍为 pending，任何 `failure`（包括已记录的外部阻塞）都会使 R10 保持未完成且不得触发第四次检查。成功任务没有可用更新是有效结果。
- R11. 保留 Release Please PR #386 及其分支；它不属于依赖清理范围。
- R12. 不在本任务中直接升级 `package.json`、`package-lock.json`、`go.mod`、`go.sum` 或现有 GitHub Actions 版本。

### Persistent execution mode

- R13. 将本项目 Codex 的 Trellis 派发偏好显式设为 `sub-agent`；后续实现、研究与检查默认使用子代理，只有用户明确要求 inline 时才覆盖该偏好。
- R14. 本项目需要隔离工作区时默认使用仓库内 `.worktrees/`；该目录必须保持在 `.gitignore` 中，未来任务沿用此位置而不重复询问。

## Acceptance Criteria

- [ ] AC1. Dependabot 配置为月度普通版本更新，明确时区、分组、major 策略和与分组匹配的 PR 上限；Task 5 的三次手动检查全部 `success` 后，用实际 update job 与 PR 证据验证配置已生效。任何 queued/failure 都使 AC1 保持未完成。
- [ ] AC2. Task 5 的三次手动检查全部 `success` 后，实际普通版本 PR 仅使用 Go、npm production、npm development、GitHub Actions 四个批准的分组身份，每个身份最多对应一个 PR（身份必须唯一、不得重复），总数最多为 4；成功任务无可用更新时 0 个 PR 也是有效结果。任何 queued/failure 都使 AC2 保持未完成。
- [ ] AC3. GitHub API 确认 vulnerability alerts 请求成功，且 automated security fixes 精确为 `enabled: true`、`paused: false`。
- [ ] AC4. CI 配置在 PR 提交上只触发 `pull_request` 流；治理 PR 合并提交在 `main` 上精确对应一个 `push` CI run，且其状态为 `completed`、结论为 `success`。
- [ ] AC5. 相关 YAML 可解析，仓库本地适用检查及远程必需 CI 全部通过。
- [ ] AC6. 13 个旧 Dependabot PR 已按精确快照关闭，远程分支仅在当前 OID 等于该 PR 完整 `headRefOid` 时通过 expected-OID lease 条件删除，或记录外部阻塞；随后恰好三次受支持的 Web UI 检查均记录 ecosystem/directory、点击前 baseline job IDs、点击时间、job ID、任务 timestamp/type/status 和 logs URL，且三个任务全部 `success`。任何 queued/failure 都使 AC6 保持未完成并阻止 Task 6；不得触发第四次检查。
- [ ] AC7. Release Please PR #386 在手动版本检查前及 post-merge 自动化检查后均保持 `OPEN`、head 为 `release-please--branches--main`，并记录精确 URL。
- [ ] AC8. 治理 merge SHA 精确对应一个终态成功的 Release Please `push` run；没有与该 SHA 关联的 `Publish Docker Images` 或 `Sync Docker Hub Description` run。本次不直接发布版本或 Docker 镜像。
- [ ] AC9. `.trellis/config.yaml` 明确包含 `codex.dispatch_mode: sub-agent`，现有 `.codex/agents/*` 与 Codex hooks 未被本任务修改。
- [ ] AC10. `.gitignore` 继续忽略 `.worktrees/`，项目规范记录该目录为默认隔离 worktree 位置，本任务在 `.worktrees/dependency-update-governance` 中执行。

## Out Of Scope

- 实际依赖版本升级或兼容性修复。
- Docker 基础镜像的自动版本更新策略。
- Dependabot 自动合并或无人工审查的补丁发布。
- 基于文件路径跳过后端、前端、Docker 或 Worker 检查。
- 合并或关闭 Release Please PR #386。
- 修改 Trellis 上游、全局 Codex 用户配置或其他项目的派发偏好。
