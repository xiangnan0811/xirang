# 依赖更新治理与 CI 去重：技术设计

## 1. Design Goals

本设计将普通依赖版本更新、安全依赖更新和发布自动化拆成三个独立通道：

1. 普通版本更新低频、分组、人工审查。
2. 安全告警与安全修复及时触发，不等待普通更新窗口。
3. Release Please 继续独立维护发布 PR，不参与依赖 PR 清理。

同时收敛 CI 事件模型，保证 PR 提交只运行一套 pull-request CI，合并到 `main` 后再运行一套 push CI。

## 2. Current-State Boundaries

- `.github/dependabot.yml` 管理 `/backend` 的 Go modules、`/web` 的 npm 依赖和根目录的 GitHub Actions。
- 普通版本更新当前每周运行，开放 PR 上限合计 13，且没有分组或 major 更新边界。
- `.github/workflows/ci.yml` 的无条件 `push` 与 `pull_request` 事件会对同一 PR 提交产生重复运行。
- GitHub 仓库设置中的 vulnerability alerts 与 automated security fixes 当前均为 disabled。
- Release Please PR #386 与 `release-please--branches--main` 分支必须保持独立。

## 3. Persistent Sub-Agent Dispatch Contract

`.trellis/config.yaml` 是本项目 Codex 派发模式的持久化入口。当前 `dispatch_mode: auto` 已会派发 `trellis-implement`、`trellis-check` 与 `trellis-research`；本任务将其改为等价但显式表达维护者偏好的：

```yaml
codex:
  dispatch_mode: sub-agent
```

`sub-agent` 是 `auto` 的向后兼容别名，运行时仍使用 Codex 原生子代理与 `SubagentStart` 上下文注入。项目规范同时记录：未来 Trellis 实施、研究和检查默认使用子代理，只有用户明确要求 inline 时才临时覆盖。

本任务不修改 `.codex/agents/trellis-*.toml`、`.codex/hooks.json` 或用户级 `~/.codex/config.toml`。这样既保留现有角色边界和递归保护，也避免把项目偏好错误扩散到其他仓库。

Subagent-Driven 执行使用仓库内 `.worktrees/` 作为项目默认隔离目录。`.gitignore` 已包含 `.worktrees/`，创建前必须用 `git check-ignore -v .worktrees/` 复核。具体任务目录采用 `.worktrees/<task-slug>`；本任务使用 `.worktrees/dependency-update-governance`。治理 PR 合并后的短期 evidence follow-up 是明确例外：primary worktree 已同步且不再编辑 `main` 后，可直接在该 worktree 创建专用 evidence branch，无需为只包含任务证据/归档/journal 的小型 follow-up 再建第二个 implementation worktree；专用分支和 PR 规则仍然强制。该约定写入项目分支工作流规范，后续任务无需再次选择全局或项目内位置。

## 4. Dependabot Configuration Contract

每个 ecosystem 使用月度调度，并明确：

```yaml
schedule:
  interval: "monthly"
  time: "03:00"
  timezone: "Asia/Shanghai"
```

`allow.update-types` 只允许普通版本更新中的 minor 和 patch：

```yaml
allow:
  - dependency-name: "*"
    update-types:
      - "version-update:semver-minor"
      - "version-update:semver-patch"
```

GitHub 官方配置契约明确 `allow.update-types` 仅影响 version updates，不影响 security updates。因此本设计不用会同时影响安全更新的 `ignore` 规则来排除 major。

分组仅应用于普通版本更新：

- Go modules：一个 `go-minor-patch` 组，匹配全部依赖。
- npm：一个 production minor/patch 组和一个 development minor/patch 组。
- GitHub Actions：一个 `actions-minor-patch` 组，匹配全部 actions。
- 每个组显式使用 `applies-to: version-updates` 和 `update-types: [minor, patch]`。

普通版本更新 PR 上限设置为：

- Go modules：1。
- npm：2。
- GitHub Actions：1。

`open-pull-requests-limit` 只限制普通版本更新；安全更新不计入该上限。现有 labels 保持不变，不增加自动合并配置。

## 5. Security Update Contract

配置 PR 合并、旧版本 PR 精确清理且三次普通版本 update job 均取得终态 `success` 与分组证据后，通过 GitHub API 按顺序启用：

1. Dependabot vulnerability alerts。
2. Dependabot automated security fixes。

启用后立即通过只读 API 验证两个状态。安全更新保持 Dependabot 默认的独立、即时 PR 行为，不加入月度 version-update groups。

若某个漏洞只能通过 major 升级修复，`allow.update-types` 不会阻止安全更新；如果 GitHub 仍无法自动生成修复 PR，告警必须保持可见，并另建高优先级 Trellis 升级任务。Evidence branch 首先创建 `r7-follow-up-task-paths.txt` 并写入 `PENDING`；Task 6 审查完成后必须将它替换为单行 `NONE` 或一个以上精确 `.trellis/tasks/<exact-task-name>` 直接子任务路径，并从同一规范化列表写入 post-merge evidence。该 manifest 同时是 Phase 3.4 提交和 archive 副作用的唯一 follow-up 路径 allowlist。

## 6. CI Event Contract

CI 工作流事件调整为：

```yaml
on:
  push:
    branches:
      - main
  pull_request:
```

由此得到：

- PR 分支 push：只触发 `pull_request` CI。
- 合并到 `main`：只触发 `push` CI。
- 直接针对 `main` 的仓库行为不丢失现有检查。

本任务不增加 paths filters，也不修改 jobs、权限、并发组或检查内容。

## 7. Migration Sequence

迁移必须使用精确快照，不能用“关闭所有 Dependabot PR”的宽泛过滤：

1. 记录配置变更前 13 个开放 Dependabot PR 的编号、标题和 head branch。
2. 在独立分支修改配置，运行本地验证，提交并创建 PR。
3. 监控全部 required CI，通过后 squash merge。
4. 使用 `gh pr merge --squash` 合并但不让 `gh` 删除仍在 linked worktree 中检出的治理分支；本地和远程治理分支清理推迟到治理 worktree 被安全移除之后。
5. 显式切换命令执行位置到 primary worktree `/home/murray/code/xirang`，先确认它干净且已检出 `main`，再 `pull --ff-only origin main` 并确认 `HEAD == origin/main`。GitHub CLI 查询可从任一 worktree 执行，但不得在治理 worktree 尝试 `git switch main`。
6. 在任何 tracked post-merge evidence 写入前，分别检查本地和远程 `codex/chore-dependency-governance-evidence` 均不存在；本地存在、远程存在或远程查询错误都 fail closed。随后从已同步 main OID 在 primary worktree 创建并切换到该精确分支，复核新分支 HEAD 等于该 OID，并将它持久化为单行 `.trellis/tasks/08-11-dependency-update-governance/research/evidence-branch-base-oid.txt`；同时定义唯一 live evidence 文件 `post-merge-evidence.md`。Task 5-7 的结果、验收、归档和 journal 写入只允许发生在此 branch/worktree，旧治理 worktree 与 `main` 都不可写。
7. 逐个关闭步骤 1 快照中的 13 个旧普通版本 PR，并附上由分组策略取代的说明；对每个已关闭 PR 校验 CLOSED、`app/dependabot`、精确 head 和完整 `headRefOid`，只在远程 ref 仍等于该 OID 时以 expected-OID lease 删除。
8. 在手动触发前确认 Release Please PR #386 仍为 OPEN 且 head 精确匹配。使用执行时加载的 `browser` skill 和已认证 GitHub Web UI 打开 Dependabot Recent update jobs，对 `gomod /backend`、`npm /web`、`github-actions /` 各点击一次 `Check for updates`，总计恰好三次；每次点击前保存 baseline job IDs。
9. 对每次点击记录 ecosystem/directory、baseline job IDs、点击时间、job ID、timestamp/type/status 与 logs URL，并异步监控。结果不明确时只能重载并与 baseline 比较，不得再次点击；三个任务必须全部终态 `success`，任何 queued/failure 都阻止后续步骤且不得第四次触发。
10. 从三份成功 job 日志独立提取关联 PR 编号并拒绝非数字或重复；同时使用 guarded `gh pr list --state open --author app/dependabot --limit 200` 枚举完整 live 集合。逐项校验 OPEN、`app/dependabot`、批准 head/group、group 唯一、URL 非空和总数 0-4，拒绝重复 live 编号。两组编号分别数字化、去重并排序后必须完全相等；live 非空而手填 job 数组为空、多余 live PR 或 job-only PR 都 fail。此 gate 执行时安全设置尚未启用且旧 13 PR 已关闭，因此任何 live Dependabot PR 都必须是这批普通分组 PR；违背该假设即阻止 Task 6。
11. 只有步骤 8-10 全部通过，才启用 vulnerability alerts 和 automated security fixes；只读 API 必须确认 alerts 请求成功且 fixes 精确为 `enabled: true`、`paused: false`。安全 PR 独立保留。
12. 对治理 merge SHA 断言 main CI 与 Release Please 均有唯一 completed/success push run，并断言无关联 image publish/description run；所有输出继续写入 evidence 文件。
13. 在 evidence branch 上完成验收证据、解析 R7 manifest 和任何 follow-up 任务内容。Phase 3.4 提交前显式断言 primary 路径/evidence branch 和 HEAD 等于持久化 base OID，仅允许当前 active task 根和 manifest 中精确列出且确实有新建内容的 R7 直接子任务目录，拒绝 workspace、archive 和任意 `.trellis/tasks/*`。将 audited 工作提交为 `docs(task): record dependency governance evidence` 后，使用 `git diff-tree --no-renames` 审计实际 committed path set，复跑相同 allowlist 并要求 evidence、R7 manifest、base-OID 文件和每个 manifest follow-up 均存在于 commit；同时断言 work parent 等于 base OID、分支干净，防止 hooks 或并发 index 变化夹带内容。
14. 运行 `get_context.py --mode record`，再运行 `task.py archive 08-11-dependency-update-governance`。审计自动 archive commit 的 subject 精确为 `chore(task): archive 08-11-dependency-update-governance`、parent 精确为 `work_commit`，diff 仅允许当前 active/archive 精确路径以及由归档父子关系处理实际修改的 manifest follow-up 目录。
15. 运行 `add_session.py`，显式传入 evidence branch，且 `--commit` 仅传入 `work_commit`。审计自动 journal commit 的 subject 精确为 `chore: record journal`、parent 精确为 archive commit，diff 仅允许当前开发者 workspace 中精确 `index.md` 和 `journal-*.md`。从持久化 base OID 到 journal HEAD 必须精确枚举三个提交，顺序和 parent 链均为 base → work → archive → journal，且分支干净。
16. Push 前再次从归档后的 base-OID 文件解析 base，重验 `base..HEAD` 精确三提交、三个 subjects 和完整 parent 链，拒绝在上一次审计后新增的 clean commit。只以 audited `journal_commit` 到远程 evidence ref 的显式 refspec 推送，并用 expected-empty force-with-lease 原子要求远程 ref 不存在；回读远程 ref 必须等于该 commit。创建 target `main` 的 follow-up PR 后，在 CI 监控前断言其完整 `headRefOid == journal_commit`；CI/diff 通过后紧邻 merge 再查询同一断言，并以 `--match-head-commit journal_commit` squash merge，且不使用隐式 branch cleanup。
17. 对 evidence PR 的 merge SHA 监控 main CI、Release Please 和 no-publish/no-description 状态并重验 PR #386。为了避免 evidence-PR 无限递归，这一最后观察只写入最终任务/用户交接，不再写 tracked 文件或创建第三个 PR。
18. Evidence PR 合并且步骤 17 完成后，在 primary worktree 显式切回 `main`、`pull --ff-only` 并确认 `HEAD == origin/main`；再以 evidence PR 的完整 `headRefOid` 校验本地/远程 evidence ref，拒绝 symbolic ref，使用 exact-ref `update-ref --no-deref -d <ref> <OID>` 和 remote expected-OID lease 条件清理，并只删除精确 branch config section。
19. 只有 primary `main` 已同步且 evidence branch cleanup 完成后，才确认治理 worktree 存在、干净、检出精确治理分支且 HEAD 等于治理 PR 的完整 `headRefOid`；移除治理 worktree后，以相同 no-symref、no-deref expected-OID 和 remote lease 规则清理治理分支。两组条件删除均防止验证后 ref 被并发移动时误删新提交。

先清理旧 PR、再主动验证新的普通版本分组、最后启用安全更新，可以避免新生成的安全 PR 与旧普通版本 PR 或新分组 version PR 混淆或被误关。

手动重新运行只使用 GitHub 官方文档支持的 Web UI 操作：[Re-running Dependabot jobs](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/manage-your-dependency-security/re-run-dependabot-jobs)。GitHub 没有为该动作公开记录 REST、GraphQL 或第一方 `gh` 触发接口；不得脚本调用不透明的内部 endpoint。

## 8. Validation Strategy

### Static validation

- YAML 语法解析。
- 使用 GitHub Actions 专用 lint 验证 workflow 事件结构。
- 对 Dependabot 配置做结构断言：三个 ecosystems、月度调度、固定时区、四个分组、minor/patch allow 规则、1/2/1 PR 上限。
- 断言 `.trellis/config.yaml` 的 `codex.dispatch_mode` 明确为 `sub-agent`，并检查 `.codex/agents/*` 与 hooks 未变化。
- 验证 `.worktrees/` 受 `.gitignore` 保护，隔离 worktree 从当前治理分支创建且基线检查通过。
- 检查依赖清单和 action pin 没有被意外修改。

### Repository validation

- 运行与配置变更匹配的轻量本地检查。
- PR 上监控仓库全部 required CI；不以本地静态检查代替远程事件验证。
- 解析唯一已合并治理 PR 的 `mergeCommit.oid`；对该 SHA 断言精确一个 CI `push` run，且 `headSha` 匹配、状态 `completed`、结论 `success`。
- 对同一 merge SHA 断言精确一个终态成功的 Release Please `push` run，并断言没有关联的 Publish Docker Images 或 Sync Docker Hub Description run。该结论对应实际触发器：前者只由 release published/手动触发，后者的 main push 仅匹配 README 或自身 workflow 路径。
- Evidence follow-up PR 必须验证 Conventional Commit 标题、完整 required CI 和预期 diff；其 merge SHA 也必须完成同样的 main CI、Release Please、no-publish/no-description 观察，但最后一次观察只进入非 tracked 最终交接，避免递归 evidence PR。

### Operational validation

- GitHub vulnerability alerts 只读 API 请求成功；automated security fixes 精确返回 `enabled: true`、`paused: false`，查询失败或 false 值均不通过。
- 旧 13 个 PR 全部关闭；每个远程 head 只在 OID 等于对应 PR 的完整 `headRefOid` 时以 expected-OID lease 条件删除，或为外部阻塞留下精确记录。
- Release Please PR #386 在手动触发前及 post-merge 检查后均精确为 `OPEN`、head `release-please--branches--main`，并记录 URL。
- 在 `https://github.com/xiangnan0811/xirang/network/updates` 使用受支持的 `Check for updates` Web UI 动作恰好触发三个任务：`gomod /backend`、`npm /web`、`github-actions /`。对每个任务保存 baseline job IDs、点击时间、job ID、任务 timestamp/type/status 和 logs URL；结果不明确时只重载并对比 baseline，不得重试点击。
- 三个任务必须全部 `success`；成功且无可用更新有效，queued 仍待处理，任何 failure 都阻止 Task 6 并使 R10/AC1/AC2/AC6 未完成。
- 从三个成功任务日志提取关联 version-update PR 编号；独立完整枚举当前开放 `app/dependabot` PR。两组均拒绝非数字和重复并按数字排序；live 集合逐项验证 OPEN、author、批准 head/group、group 唯一、URL 和 0-4 总数，随后要求两个集合完全相等并保持这些 PR 开放。此时安全设置仍关闭且旧快照已关闭，因此任何其他 live bot PR 都是阻塞；成功无更新仅在两组均为空时有效。

### Post-merge evidence recursion

治理 merge 后的 tracked evidence、R7 follow-up 任务、任务归档和 journal 必须通过专用 evidence branch/PR。该分支的已验证 base OID 必须持久化并贯穿 closeout；必须先提交并复审实际 committed work tree，再让 archive 和 journal 脚本各自产生自动提交，最终相对 base 精确只有这三个提交。不得将三类变更合并、将 archive hash 记入 journal，或容忍 hooks、并发 index 变化与额外 clean commits 绕过预提交路径审计。Evidence PR 合并后的自动化观察不能再修改 tracked evidence，否则会生成无穷 follow-up；该最后观察只进入最终任务/用户交接。若 evidence PR 或其 required CI 失败，保留分支并在同一 PR 修复，不得绕过 PR 直接写 `main`。

## 9. Risks And Mitigations

### Grouped update failure isolation

一个分组中任一依赖失败会阻塞整个组。npm production/development 分组分离，Go 与 Actions 分生态分离，并保留完整 CI，控制排障范围。

### Immediate security PR burst

首次启用安全功能可能一次产生多个 PR。先完成旧版本 PR 清理和三次普通版本 job/分组验证，再启用安全功能；安全 PR 不按普通版本噪音处理，也不自动关闭。

### Major-version debt

排除普通 major PR 会降低噪音，但可能积累兼容性债。major 升级由独立季度维护或安全告警驱动任务处理，不在本任务中自动化。

### Dependabot scheduling observability

Dependabot 的服务端调度不能由本地测试完整验证。PR 合并并清理旧 PR 后，必须通过官方 Web UI 恰好触发三个 `Check for updates` 任务，并异步等待每个任务进入 `success`；仅看到 queued 或等待下一个月度窗口不足以验收。成功但没有可用更新是有效结果；任何 failure 都必须调查或记录为真实外部阻塞，同时阻止 Task 6 并使 R10/AC1/AC2/AC6 保持未完成，不得触发第四次任务。点击结果不明确时使用 baseline job IDs 恢复观察，不能通过再次点击消除歧义。

### Generated Trellis file drift

`.trellis/config.yaml` 是 Trellis 管理文件且当前已经包含项目级修改。本任务只改 `dispatch_mode` 单行并保留全部其他内容；不编辑生成的 agent/hook 文件。未来 `trellis update` 如报告配置冲突，应保留项目选择的 `sub-agent` 值。

## 10. Rollback

- 配置或 CI 行为异常：通过新 PR revert 本任务的仓库提交，不直接改 `main`。
- 安全更新 PR 数量异常：保留 vulnerability alerts，必要时临时关闭 automated security fixes；不得为降噪关闭告警。
- 已关闭的旧 PR 可按精确编号重新打开；已触发的三个 update jobs 无法回滚，关联的新分组 PR 必须保留并单独评估，不得把它们当作旧快照关闭。优先通过新 PR 修正配置，避免恢复旧分散策略。
- squash merge 后不要立即删除仍在 linked worktree 中检出的治理分支。回滚、任务证据和 follow-up 全部完成后，先安全移除干净的治理 worktree，再以已验证 PR `headRefOid` 为旧值条件删除本地 ref，并用相同 OID lease 删除仍存在的远程 ref；不得按分支名无条件强删。
- Evidence branch 创建后若外部操作尚未开始，可切回 main 并按精确 OID 条件清理未推送分支；已有 live 证据时保留该分支继续同一 follow-up PR。Evidence PR 已 merge 时不得回写 tracked “最终观察”；先在交接中记录其 post-merge 状态，再同步 main、条件清理 evidence branch，最后清理治理 worktree/branch。
- 子代理派发出现平台阻塞：可在独立 PR 中临时改回 `auto` 或 `inline`；不得通过删除 agent/hook 文件绕过配置入口。

## 11. Deferred Work

- Docker 基础镜像自动更新。
- major 依赖季度维护流程。
- Dependabot 自动合并。
- CI paths filters 与按变更范围缩减 jobs。
