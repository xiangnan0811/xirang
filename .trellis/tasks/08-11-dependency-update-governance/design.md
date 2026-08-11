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

配置 PR 合并且旧版本 PR 清理后，通过 GitHub API 按顺序启用：

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
4. 确认合并提交进入 `main`，并观察 Release Please 更新；不合并发布 PR。
5. 逐个关闭步骤 1 快照中的 13 个旧普通版本 PR，并附上由分组策略取代的说明。
6. 验证对应 Dependabot branches 已删除；如 GitHub 未自动删除，仅记录并按精确分支名清理。
7. 启用 vulnerability alerts 和 automated security fixes，并通过 API 验证。
8. 观察 Dependabot 因配置变更产生的更新任务；普通版本更新应遵循最多 4 个分组 PR，安全 PR 独立保留。
9. 同步本地 `main`，完成 Trellis 任务归档。

先清理旧 PR、后启用安全更新，可以避免新生成的安全 PR 与旧普通版本 PR 混淆或被误关。

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
- 合并后确认 `main` push CI 只产生一套运行。

### Operational validation

- GitHub API 返回 vulnerability alerts enabled。
- GitHub API 返回 automated security fixes `enabled: true`。
- 旧 13 个 PR 全部关闭，Release Please PR #386 仍开放。
- 远程不再保留旧 Dependabot heads，或为每个外部阻塞留下精确记录。

## 9. Risks And Mitigations

### Grouped update failure isolation

一个分组中任一依赖失败会阻塞整个组。npm production/development 分组分离，Go 与 Actions 分生态分离，并保留完整 CI，控制排障范围。

### Immediate security PR burst

首次启用安全功能可能一次产生多个 PR。先完成旧版本 PR 清理，再启用安全功能；安全 PR 不按普通版本噪音处理，也不自动关闭。

### Major-version debt

排除普通 major PR 会降低噪音，但可能积累兼容性债。major 升级由独立季度维护或安全告警驱动任务处理，不在本任务中自动化。

### Dependabot scheduling observability

Dependabot 的服务端调度不能由本地测试完整验证。PR 合并后结合 GitHub update job、实际 PR 形状和下一个月度窗口验证；服务端未立即运行不视为配置失败，但必须记录等待项。

### Generated Trellis file drift

`.trellis/config.yaml` 是 Trellis 管理文件且当前已经包含项目级修改。本任务只改 `dispatch_mode` 单行并保留全部其他内容；不编辑生成的 agent/hook 文件。未来 `trellis update` 如报告配置冲突，应保留项目选择的 `sub-agent` 值。

## 10. Rollback

- 配置或 CI 行为异常：通过新 PR revert 本任务的仓库提交，不直接改 `main`。
- 安全更新 PR 数量异常：保留 vulnerability alerts，必要时临时关闭 automated security fixes；不得为降噪关闭告警。
- 已关闭的旧 PR 可按精确编号重新打开，但优先让 Dependabot 按新配置重建，避免恢复旧分散策略。
- 子代理派发出现平台阻塞：可在独立 PR 中临时改回 `auto` 或 `inline`；不得通过删除 agent/hook 文件绕过配置入口。

## 11. Deferred Work

- Docker 基础镜像自动更新。
- major 依赖季度维护流程。
- Dependabot 自动合并。
- CI paths filters 与按变更范围缩减 jobs。
