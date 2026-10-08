# 测试约定

## 按风险建立证据

回归应验证对外行为、数据保护和失败边界，避免只重复实现内部步骤。优先覆盖实际修改的职责层及相关合同；命令、hooks 和完整门禁统一见[贡献指南](../../../CONTRIBUTING.md)，领域场景见[领域索引](../domains/README.md)。

- 纯函数或映射用聚焦单元测试；空值、非法输入、时间边界和未知枚举须有明确结果。
- 持久化变更同时验证 SQLite 与 PostgreSQL；缺少数据库服务造成的 skip 不是双引擎通过。
- 并发、租约、取消、事务补偿和重启恢复应有可重复的竞争或失败注入测试；必要时运行竞态检测。
- API 和 UI 联动同时覆盖请求、错误语义、授权及用户可见结果；浏览器模拟不能代替真实后端验收。

时间相关用例使用受控时钟或相对稳定的测试时间，不依赖逐渐过期的固定日期。异步断言等待业务条件，不用盲目延长超时隐藏回归。测试替身只替代必要的外部边界，跨层投影使用实际生产者生成的值。

## 真实浏览器与 SSH 生命周期

`web/` 下的 `npm run e2e` 使用模拟 API，不选择 `real-backend-smoke.spec.ts`、
`lifecycle-p1.spec.ts` 或 `config-name-mapping.spec.ts`。真实验收使用：

```bash
env -u NODE_ENV npx playwright test --config=playwright.real-backend.config.ts --project=chromium
```

该入口先完成密码登录 smoke，再执行生命周期及配置名称关联场景，自动启动隔离
后端和 loopback OpenSSH。名称关联使用独立账户、真实 TOTP/proof/grant、Maintenance
实际下载文件原样上传及 API/数据库读回；同实例重建 ID 不替代双引擎跨库回归。
本机需有 Go、Chromium、`sshd`、`ssh`、`ssh-keygen`、`sqlite3` 和
`python3`；CI 安装同样依赖，不以缺 SSH 配置跳过终端场景。生成的密钥、私有
known_hosts、SQLite 与 cron 产物均位于本次唯一 `.tmp/agent/lifecycle-p1-e2e/runtime.*`
目录，严格主机校验保持启用；退出时只清理该次夹具，不触碰真实 SSH 配置或数据库。
无需手工设置 SSH/数据库路径，已有占用目录必须拒绝而不是清空复用。

可以用 `E2E_BACKEND_PORT`、`E2E_VITE_PORT` 调整本地端口；更改端口不允许复用其他
运行实例。真实 SSH 的父级关闭场景必须先重新准入并证明 shell 仍连接，再关闭弹窗，
不能用已退出或已断开的 socket 代替活动连接清理证据。

## 候选和环境

记录检查针对的提交及工作区内容、命令、工具版本、必要服务、结果和跳过原因。相同 HEAD 不代表暂存、未暂存、未跟踪文件或外部依赖未变。修复后重跑失败项和受影响检查；只有候选与条件未变化时才复用已有证据。

临时 Git 仓库测试须控制宿主 CI 的基线环境变量，并在夹具内建立所需引用。文档门禁自测同时覆盖本地回退与 PR 远端基线，不能依赖调用者恰好没有设置 `GITHUB_BASE_REF`。

本地测试、独立审查、PR CI、合并后自动化、原生代理加载和生产验收分别报告。required checks 未通过、缺失或尚未完成时不得宣布交付门禁通过。具体审查及原生加载要求见[代理协作](agent-collaboration.md)，专项负载和恢复验证见[维护者验证](../../maintainers/verification.md)。
