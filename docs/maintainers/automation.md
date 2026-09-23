# 仓库自动化维护

新增外部依赖必须有明确需求并经过评审；优先复用项目已有能力，不以版本更新自动化替代必要性与兼容性判断。

## 依赖更新

[Dependabot 配置](../../.github/dependabot.yml)按月更新 Go、npm 与 GitHub Actions，按生态分组；npm 生产与开发依赖分别成组，常规更新限定 minor/patch。不要未经维护者决策改回每周、每依赖一条 PR。major 升级单独评估兼容性、上游发行说明和完整验证。

安全告警与安全修复不受常规版本更新排期约束。替换旧机器人 PR 前先记录精确 PR 编号与 head 分支，只关闭该名单；不得用动态“全部关闭”查询误伤新安全 PR。

## 前端依赖审计

更改锁文件、升级依赖或排查本地与 CI/Docker 差异时，使用与 [CI](../../.github/workflows/ci.yml)及 [Docker web-builder](../../deploy/allinone/Dockerfile)一致的 Node 主版本，当前两者均为 Node 20。不要把 Actions 自身的 JavaScript runtime 与项目 Node 版本混为一谈。

审计必须包括 `devDependencies`：Vite、Vitest、ESLint 和构建工具也是攻击面。开发 shell 的 `NODE_ENV=production` 会让 npm 隐藏开发依赖，不能采用该结果作为通过证据。专项复现从干净安装开始：

```bash
env -u NODE_ENV npm --prefix web ci
env -u NODE_ENV npm --prefix web audit --audit-level=moderate
```

现有合同仍要求 moderate 级完整审计。当前 CI 调用 [check-npm-audit.sh](../../scripts/check-npm-audit.sh)，实际只阻断未列入 GHSA allowlist 的 high/critical，不能单独证明满足上述要求。脚本还忽略 npm 本身的退出码，未验证 JSON 是否为有效审计结果；排障时必须确认获取到完整漏洞报告，不能把错误响应或缺失结果当作通过。这是实现与有效合同的差异，不能通过降低文档要求消除。

优先在现有 semver 范围内修复锁文件，不用 `--force` 或未经审查的 major 升级压制审计。锁文件变更后执行贡献指南的前端完整门禁，并完成以下核对：

- 结构化比较 `packages` 记录，确认只改了预期依赖。npm writer 差异可能删除未变平台包的 `cpu`、`os`、`libc`、`optional` 元数据；拒绝或恢复无关变化。仅锁文件修复应保持 `package.json` 不变。
- 分别记录实际漏洞摘要、退出码和去重后的 GHSA 集合。一个公告可能经多个父依赖传播，不能用包计数下降或 GHSA 减少单独宣称通过；结合 `via`、`nodes`、`npm ls`、`npm explain` 核对依赖路径。
- 本地通过而 CI 失败时，先核对 Node/npm、环境变量和干净锁文件安装，不能绕过审计。

### jsdom 选择器兼容性

[package.json](../../web/package.json)中的 jsdom 专属 override 将 `nwsapi` 固定为已验证的 2.2.25。既有回归记录表明 jsdom 26 下 2.2.26/2.2.27 的 `:modal` 匹配会递归进入原生匹配适配器，导致下拉交互超时。调整 override 前，在 CI Node 版本下同时验证独立选择器匹配、节点和通知下拉确认测试，保留完整审计及前端门禁；不得通过增加超时或修改产品交互掩盖问题。

## Actions 与 CI

[CI](../../.github/workflows/ci.yml)使用 `pull_request` 检查 PR，push 仅监听 `main`，避免同一 PR 提交重复运行。required checks 的设置与交付步骤见贡献指南；发布链路与凭据归[发布手册](release.md)。

更新工作流 action 时：

- GitHub 官方和第三方 action 均固定完整 commit SHA，并保留版本 tag 旁注。
- 修改前用 `git ls-remote` 解析目标 tag；附注 tag 应解析到实际提交。
- 核对目标版本的 `action.yml` 或 `action.yaml`，确认 `runs.using` 兼容当前 GitHub Actions runtime。JavaScript action 的 Node 24 迁移要求不得用临时 Node 20 opt-out 长期绕过。
- 已归档或废弃的 action 应迁到受维护上游，再更新版本。Release Please 使用 `googleapis/release-please-action`。
- 修改发布工作流时分别验证 amd64/arm64 构建和扫描的实际平台选择；multi-arch manifest 成功不能证明每个平台扫描正确。

仓库不依赖 Codecov 账户或上传令牌。覆盖率由 CI 自行阻断：后端总覆盖率和备份资产专项阈值、前端非空 LCOV；竞态、漏洞、PostgreSQL、浏览器和容器验收各自保留。

## 镜像构建依赖

基础镜像以 digest 固定，显式安装的 Alpine 包使用精确版本。软件源可能移除旧包：安装失败时在锁定基础镜像中复现，分别核对该 Alpine 分支的 amd64 与 arm64 软件源，再更新必要锁定。`apk` 显示的已安装版本不代表软件源仍可安装。

保留完整 Compose smoke、Worker 对应检查和漏洞扫描，不能用单一架构成功替代另一个架构。维护性依赖更新也应按发布手册检查合并后自动化，不手动提升版本或重发镜像。
