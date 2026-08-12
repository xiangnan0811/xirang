# 依赖更新治理与 CI 去重

## Goal

将普通依赖更新从高频、逐依赖 PR 改为低频、按生态分组的可控维护流程，同时保留及时的安全告警与安全修复通道，消除依赖 PR 上重复执行的 CI，并固化本项目 Codex 默认使用子代理的执行偏好。

## Background (Governance Baseline)

- `.github/dependabot.yml` 当前每周一检查 Go modules、npm 和 GitHub Actions，开放 PR 上限分别为 5、5、3。
- 2026-08-11 的治理基线快照显示远程共有 14 个开放 PR，其中 13 个来自 Dependabot，恰好占满上述三个生态的上限；另一个是 Release Please PR #386。
- 历史 Dependabot PR 中有 24 个已合并、28 个关闭未合并、13 个仍开放；这些旧状态仅用于定义迁移 allowlist，不代表 post-merge 结果。
- 治理合并后的证据显示，配置合并会自动启动每个 ecosystem/directory 恰好一个 version-update job；没有发生 `Check for updates` 点击，也不得在这些精确 job 已存在时重复触发。
- GitHub API 的 post-merge 证据确认 vulnerability alerts GET 为 HTTP 204，automated security fixes 为 `enabled: true`、`paused: false`。
- `.github/workflows/ci.yml` 的基线同时监听所有分支的 `push` 与 `pull_request`，导致 PR 分支同时触发两套 CI。

## Requirements

### Routine version updates

- R1. 普通版本更新改为每月检查一次，并使用 `Asia/Shanghai` 时区的固定低峰时段。
- R2. Go modules 的 minor/patch 更新合并为一个组；npm 生产依赖与开发依赖分别成组；GitHub Actions 的 minor/patch 更新合并为一个组。
- R3. 普通 major 更新不由月度维护流自动创建 PR，必须另建升级任务完成兼容性评估、验证与发布说明审查。
- R4. 开放普通版本更新 PR 的上限与分组数量匹配，避免单依赖 PR 再次占满 13 个槽位。
- R5. 不启用 Dependabot 自动合并；所有依赖变更继续经过现有 PR 审查与 CI。

### Security updates

- R6. 启用 GitHub Dependabot vulnerability alerts 与 automated security fixes，安全告警不受月度普通版本更新时间窗延迟。
- R7. 若安全修复只能通过 major 升级完成，必须保留可见告警并创建人工高优先级升级任务，不能因普通 major 更新策略静默遗漏。Task 6 必须将审查结果解析为机器可读的 `r7-follow-up-task-paths.txt`：无需 follow-up 时内容精确为 `NONE`，否则每行一个实际创建且已写入内容的 `.trellis/tasks/<exact-task-name>` 直接子任务目录；拒绝 `PENDING`、重复项、当前任务、`archive`、嵌套路径、缺失目录和无新建内容的目录。同一精确结果必须记入 `post-merge-evidence.md`。

### CI behavior

- R8. `pull_request` 继续运行完整 PR CI；`push` 仅对 `main` 运行，避免同一 PR 提交同时触发 push 与 pull-request 两套 CI。
- R9. 不在本任务中引入路径过滤或削减现有必需检查，避免扩大 CI 行为变更范围。

### Migration and cleanup

- R10. 新配置通过 PR 合并并完成必需 CI 后，仅按预先快照验证并记录 13 个旧分散 Dependabot PR 的自动 supersede/close 结果及 exact captured `headRefOid`，确认其机器人分支已缺失；不得声称执行了手动 close/delete。配置激活可以由治理合并自动发生，也可以在未观察到合并激活时使用 GitHub 支持的 Web UI 对 `gomod /backend`、`npm /web` 和 `github-actions /` 各显式触发一次作为 fallback；无论触发方式如何，必须观察到每个 configured ecosystem/directory 恰好一个 job，严禁重复触发。job 关联的 PR 编号必须从 job/browser 证据独立提取，完整 live `app/dependabot` 普通版本集合必须分别校验 numeric、unique、sorted，且两组完全相等；每个 live PR 必须为 `OPEN`、作者为 `app/dependabot`、head 属于四个批准分组且 group identity 唯一，总数为 0-4。Go job 的 Recent Jobs capacity error 仅在 approved grouped PR #420 已创建、`mark_as_processed` 精确返回 HTTP 204、Actions wrapper/run 成功、该 UI error 仅由 `open-pull-requests-limit: 1` 造成且完整 live version set exact/reconciled 时可接受；npm/actions 正常完成。任何真实 repository/job failure、queued、missing grouped PR、failed wrapper 或 `mark_as_processed`、unexpected error、duplicate trigger、错误 state/author/head、遗漏/多余 live PR 或集合不等都会使 R10 保持未完成并阻止安全设置启用。
- R11. 保留 Release Please PR #386 及其分支；它不属于依赖清理范围。
- R12. 不在本任务中直接升级 `package.json`、`package-lock.json`、`go.mod`、`go.sum` 或现有 GitHub Actions 版本。

### Persistent execution mode

- R13. 将本项目 Codex 的 Trellis 派发偏好显式设为 `sub-agent`；后续实现、研究与检查默认使用子代理，只有用户明确要求 inline 时才覆盖该偏好。
- R14. 本项目需要隔离工作区时默认使用仓库内 `.worktrees/`；该目录必须保持在 `.gitignore` 中，未来任务沿用此位置而不重复询问。
- R15. 治理 PR 合并后，所有 live 结果、任务验收、R7 follow-up 任务内容、归档与开发者日志写入必须在主 worktree 从已同步 `main` 新建的精确分支 `codex/chore-dependency-governance-evidence` 上完成，并通过独立 follow-up PR、完整 required CI 和 squash merge 进入 `main`。创建分支时必须将已验证的同步 main OID 持久化为单行 `evidence-branch-base-oid.txt`，closeout 时以该值证明 work commit 的唯一 parent 正是分支 base。分支相对该 base 必须精确且仅有三个提交：先在 Phase 3.4 将 audited evidence/验收/R7 follow-up 工作提交为 `docs(task): record dependency governance evidence`，再由 `task.py archive` 自动提交 `chore(task): archive 08-11-dependency-update-governance`，最后由 `add_session.py` 自动提交 `chore: record journal`；日志 `--commit` 只接收 Phase 3.4 work commit 的完整 hash，不得包含 archive commit。work commit 完成后必须基于实际 committed tree 再次执行相同 active-task/R7-manifest allowlist 审计并重验 evidence、manifest 和 base-OID 文件，防止 hooks 或并发 index 变化夹带路径；push 前必须重验从 base 到 HEAD 的精确三提交数量、顺序、subjects 和 parent 链，拒绝任何额外 clean commit。远程写入必须使用 `${journal_commit}:refs/heads/codex/chore-dependency-governance-evidence` 精确 refspec 和“远程 ref 必须不存在”的 atomic force-with-lease，不得从可移动本地分支名推送；PR 在 CI 监控前及 merge 紧邻边界都必须返回 `headRefOid == journal_commit`，merge 请求也必须携带同一 expected head OID。该 PR 合并后的最终 main CI、Release Please 与 no-publish 观察只记录在最终任务/用户交接中，不再创建 tracked evidence，避免递归 evidence PR。

## Acceptance Criteria

- [x] AC1. Dependabot 配置为月度普通版本更新，明确时区、分组、major 策略和与分组匹配的 PR 上限；治理合并自动激活后，每个 configured ecosystem/directory 恰好观察到一个 job，且完整 live version set 与 job 证据精确相等。任何 queued/failure、无效编号或集合不等都保持阻塞。
- [x] AC2. 自动激活的 job/browser 证据与完整 live `app/dependabot` 集合均为 numeric/unique/sorted 的 `417 418 419 420`，每个 PR 为 `OPEN`、`app/dependabot` 且使用唯一批准 group identity；Go 的 capacity-only UI error 仅在 #420、wrapper、`mark_as_processed` 204 和 exact live reconciliation 成立时被接受。
- [x] AC3. GitHub API 确认 vulnerability alerts GET 成功（HTTP 204），且 automated security fixes 精确为 `enabled: true`、`paused: false`。
- [x] AC4. CI 配置在 PR 提交上只触发 `pull_request` 流；治理 PR 合并提交在 `main` 上精确对应一个 `push` CI run，且其状态为 `completed`、结论为 `success`。
- [x] AC5. 相关 YAML 可解析，仓库本地适用检查及治理合并 required CI 全部通过；真实 job failure、queued 或 unexpected error 仍保持阻塞。
- [x] AC6. 13 个旧 Dependabot PR 均由 `dependabot[bot]` 自动 supersede/close，精确 captured remote heads 均 absent，且没有手动 close/delete；自动激活每个 ecosystem/directory 恰好一个 job，npm/actions 正常完成，Go capacity-only split 满足完整例外条件并与 `417 418 419 420` 精确 reconciled。任何 queued/failure/missing PR/failed wrapper/mark_as_processed、重复触发或集合/shape 失败都保持阻塞。
- [x] AC7. Release Please PR #386 在自动激活前后及治理 post-merge 自动化检查后均保持 `OPEN`、head 为 `release-please--branches--main`，并记录精确 URL。
- [x] AC8. 治理 merge SHA 精确对应一个终态成功的 Release Please `push` run；没有与该 SHA 关联的 `Publish Docker Images` 或 `Sync Docker Hub Description` run。本次不直接发布版本或 Docker 镜像。
- [x] AC9. `.trellis/config.yaml` 明确包含 `codex.dispatch_mode: sub-agent`，现有 `.codex/agents/*` 与 Codex hooks 未被本任务修改。
- [x] AC10. `.gitignore` 继续忽略 `.worktrees/`，项目规范记录该目录为默认隔离 worktree 位置，本任务在 `.worktrees/dependency-update-governance` 中执行。
- [ ] AC11. 治理 merge 后先同步 primary `main`，确认同名 local/remote evidence branch 均不存在且查询无错误，再从该 main OID 创建 `codex/chore-dependency-governance-evidence` 并将精确 base OID 持久化；Task 5-7 所有 tracked live evidence、验收、R7 manifest/follow-up 内容、归档和 journal 变更仅在该分支完成。Phase 3.4 work commit 必须先经精确 active-task/R7-manifest allowlist 审计并保证拒绝任意 `.trellis/tasks/*`，提交后再用实际 tree diff 复跑同一 allowlist，重验 exact evidence/manifest/base-OID 文件，并断言其 parent 等于持久化 base；其后 archive 和 journal 由 Trellis 分别自动提交，两次均审计精确 subject、parent 和 diff allowlist，日志仅记录 work commit hash。push 前必须证明 `base..HEAD` 精确只有 work → archive → journal 三个提交，subjects 与 parent 链完全匹配，任何之前、中间或之后的额外 clean commit 都失败；仅以 audited `journal_commit` 精确 refspec 和 absent-ref lease 创建远程 evidence ref并回读同一 OID。PR 在 CI 前和 merge 前都必须断言完整 `headRefOid` 等于该 commit，merge 使用同一 expected-head guard。三个提交经独立 PR、required CI、diff 审查和 squash merge 进入 `main`。随后验证 evidence merge 的 main CI 与 Release Please/no-publish 状态，只在最终交接记录这次最后观察；同步 main、以 PR `headRefOid` 条件清理 evidence branch 后，才清理治理 worktree/branch。

AC11 的最终非递归 delivery lifecycle（evidence PR、required CI、squash merge、main 同步、OID-safe cleanup 和治理 worktree 清理）按设计保留为后续 closeout，当前不将其提前标记为完成。

## Out Of Scope

- 实际依赖版本升级或兼容性修复。
- Docker 基础镜像的自动版本更新策略。
- Dependabot 自动合并或无人工审查的补丁发布。
- 基于文件路径跳过后端、前端、Docker 或 Worker 检查。
- 合并或关闭 Release Please PR #386。
- 修改 Trellis 上游、全局 Codex 用户配置或其他项目的派发偏好。
