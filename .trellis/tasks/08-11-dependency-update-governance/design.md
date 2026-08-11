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

Subagent-Driven 执行使用仓库内 `.worktrees/` 作为项目默认隔离目录。`.gitignore` 已包含 `.worktrees/`，创建前必须用 `git check-ignore -v .worktrees/` 复核。具体任务目录采用 `.worktrees/<task-slug>`；本任务使用 `.worktrees/dependency-update-governance`。该约定写入项目分支工作流规范，后续任务无需再次选择全局或项目内位置。

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

若某个漏洞只能通过 major 升级修复，`allow.update-types` 不会阻止安全更新；如果 GitHub 仍无法自动生成修复 PR，告警必须保持可见，并另建高优先级 Trellis 升级任务。

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
5. 显式切换命令执行位置到主 worktree `/home/murray/code/xirang`，先确认它干净且已检出 `main`，再执行 `git -C /home/murray/code/xirang pull --ff-only origin main` 并确认 `HEAD` 等于 `origin/main`。GitHub CLI 的 PR 查询可从任一 worktree 执行，但后续 Git 同步不得在治理 worktree 中尝试 `git switch main`。
6. 确认合并提交进入 `main`，并观察 Release Please 更新；不合并发布 PR。
7. 逐个关闭步骤 1 快照中的 13 个旧普通版本 PR，并附上由分组策略取代的说明。
8. 对每个已关闭的捕获 PR 查询完整 `headRefOid` 并校验 CLOSED 状态、`app/dependabot` author 和精确 head name。远程分支存在时，其当前 OID 必须仍等于该 `headRefOid`，随后使用 expected-OID `--force-with-lease` 原子条件删除；OID 不匹配、非完整 OID 或传输错误都必须在删除前中止。
9. 使用执行时加载的 `browser` skill 和已认证 GitHub Web UI 打开 `https://github.com/xiangnan0811/xirang/network/updates`，在 Recent update jobs 中依次对 `gomod /backend`、`npm /web`、`github-actions /` 各点击一次 `Check for updates`，总计恰好三次。每次点击前保存该 ecosystem/directory 的 baseline job IDs；npm 虽有两个 groups，仍只触发一个 ecosystem/directory 任务。
10. 对每次点击记录 ecosystem/directory、baseline job IDs、点击时间、job ID、任务 timestamp/type/status 与 logs URL，并异步监控。点击后若超时或页面重载导致结果不明确，不得再次点击；只能重载 Recent update jobs 并与 baseline 比较，直到识别出恰好一个新 job，无法唯一识别时记录阻塞。
11. 三个任务必须全部达到终态 `success`。`queued` 只表示 pending；任何 `failure`（即使已调查并记录为外部阻塞）都阻止 Task 6，并使 R10/AC1/AC2/AC6 保持未完成；不得提交第四次触发。成功但无可用更新是有效结果。
12. 从三个成功任务的页面和日志捕获关联的新普通版本 PR，确认它们仅使用 `go-minor-patch`、`npm-production-minor-patch`、`npm-development-minor-patch`、`actions-minor-patch` 四个批准身份，每个身份最多对应一个 PR（身份唯一、不得重复），且总数不超过 4；不得关闭这些新 PR。
13. 只有步骤 9-12 的三个任务全部成功且分组证据完整后，才启用 vulnerability alerts 和 automated security fixes；只读 API 必须确认 alerts 请求成功，且 security fixes 精确为 `enabled: true`、`paused: false`。安全 PR 独立保留。
14. 完成 Trellis 任务归档和所有合并后的 follow-up 后，只在主 worktree `/home/murray/code/xirang` 完成最终同步：先确认该 worktree 干净且当前分支恰好为 `main`，再运行 `git -C /home/murray/code/xirang pull --ff-only origin main`，并确认其 `HEAD` 等于 `origin/main`。随后确认治理 worktree 存在、干净、检出精确治理分支且 HEAD 等于已合并 PR 的完整 `headRefOid`；移除治理 worktree 后，使用该 OID 条件删除本地 ref，并仅在远程 ref 仍等于同一 OID 时通过 expected-OID lease 删除远程 ref。条件删除可阻止验证后 ref 被并发移动时误删新提交。

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

### Operational validation

- GitHub vulnerability alerts 只读 API 请求成功；automated security fixes 精确返回 `enabled: true`、`paused: false`，查询失败或 false 值均不通过。
- 旧 13 个 PR 全部关闭；每个远程 head 只在 OID 等于对应 PR 的完整 `headRefOid` 时以 expected-OID lease 条件删除，或为外部阻塞留下精确记录。
- Release Please PR #386 在手动触发前及 post-merge 检查后均精确为 `OPEN`、head `release-please--branches--main`，并记录 URL。
- 在 `https://github.com/xiangnan0811/xirang/network/updates` 使用受支持的 `Check for updates` Web UI 动作恰好触发三个任务：`gomod /backend`、`npm /web`、`github-actions /`。对每个任务保存 baseline job IDs、点击时间、job ID、任务 timestamp/type/status 和 logs URL；结果不明确时只重载并对比 baseline，不得重试点击。
- 三个任务必须全部 `success`；成功且无可用更新有效，queued 仍待处理，任何 failure 都阻止 Task 6 并使 R10/AC1/AC2/AC6 未完成。
- 从三个成功任务捕获所有关联 version-update PR，验证它们仅使用上述四个批准 group identities、每个 identity 最多一个 PR（identity unique、不得重复）、总数不超过 4，并保持这些 PR 开放；成功任务没有可用更新时 0 个 PR 也有效。完成该证据后才启用安全设置。

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
- 子代理派发出现平台阻塞：可在独立 PR 中临时改回 `auto` 或 `inline`；不得通过删除 agent/hook 文件绕过配置入口。

## 11. Deferred Work

- Docker 基础镜像自动更新。
- major 依赖季度维护流程。
- Dependabot 自动合并。
- CI paths filters 与按变更范围缩减 jobs。
