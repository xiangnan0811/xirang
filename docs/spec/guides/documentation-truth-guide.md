# 文档维护约定

## 入口与唯一归属

维护正文统一使用简体中文，保留代码、标识符和必要英文术语。每个实际文档目录以 `README.md` 导航；普通源码目录不批量补入口。各篇维护文档应能从[文档总入口](../../README.md)到达。

| 位置 | 职责 |
| --- | --- |
| 根 README | 产品介绍、最小部署示例及阅读导航 |
| 根 AGENTS 及平台入口 | 项目权威、适用范围和按任务加载规则 |
| 根 CONTRIBUTING | 开发环境、命令、hooks、分支及 PR 操作 |
| 部署、环境变量及 admin | 用户使用和运行维护 |
| maintainers | 发布、仓库自动化和专项验证 |
| docs/spec | 通用开发约定、领域合同和跨层指南 |

每条规则只有一个主文，其他文档链接引用。领域主文共同承载持久化、API、前端、兼容和回归要求；不要再在数据库、错误、类型、质量等文档中复制整套场景。模块 README 只保留本地职责和导航。

## 事实核验

文档中的命令、路径、默认值、环境变量、API 和工作流触发条件，必须逐项核对实现源码、配置、测试、工作流、manifest 和脚本。生成的版本历史以 CHANGELOG 为线索，公开版本以[发布合同](../../maintainers/release.md)为准。

区分文档过时与实现违反有效合同：前者修正文档；后者保留要求、记录实现缺口并补充回归，不能通过删规则宣称一致。测试名称、静态配置和历史验收也不能代替真实运行证据。

涉及外部当前状态时才作实时查询，注明范围和时间；尽量链接权威来源，避免硬编码当前版本、Actions SHA、镜像摘要和最新迁移号。迁移版本只由[后端入口](../../../backend/README.md)声明并受脚本校验。

## 整理与检查

删除模板填充状态、阶段编号、过期交付叙事和重复示例，但保留有效的数据保护、兼容及回归要求。历史版本事件通过 Git 历史、CHANGELOG 和 Release 追溯；不新建归档、占位页、审计日志或过程目录。

移动或删除文档须一次性更新链接、锚点、代理入口、隐藏配置、脚本、hooks、模板和构建排除项，不保留跳转空壳。验证目录入口、可达性、相对链接与锚点、旧路径和按主题要求的同步文档；检查命令和交付流程统一见[贡献指南](../../../CONTRIBUTING.md)。文档变更没有运行产品测试时，说明适用范围和理由，不能把跳过写成通过。

主题对应关系由 `scripts/doc-freshness-rules.sh` 维护，pre-commit 与 CI 使用同一函数。触发集合排除所有 `*_test.go`，文档同步只接受 ACMR，删除要求的主文不能算同步。

生产模型按有界主题集合要求同步（下表主文均位于 `docs/spec/domains/`）：

| 模型（相对 backend/internal/model） | 可满足同步的领域主文 |
| --- | --- |
| user.go、audit.go、token_revocation.go | credentials-access.md |
| node.go | credentials-access.md 或 node-log-collection.md 或 alerting-health.md |
| task.go、task_occurrence.go、task_resource.go、task_terminal_effect.go、backup_completion.go | task-execution-recovery.md |
| alert.go | alerting-health.md |
| monitor.go、integration.go | alerting-health.md 或 credentials-access.md |

OR 集合来自文件实际涉及的有限主题，并不证明具体改动语义；维护者仍须同步真正改变的领域。无关领域文档、领域索引或通用数据库文档不能代替表内主文。
其余模型（含 models.go 索引、policy.go、backup_asset 系列及未来文件）只输出非阻断导航，要求按[领域入口](../domains/README.md)核对实际合同，不伪称已自动覆盖。
`backend/internal/api/router.go` 同样只输出导航：按实际路由合同核对领域主文，只有路由组织或认证边界通用规则变化时才同步[后端目录合同](../backend/directory-structure.md)。导航使用 `[INFO]`，不增加阻断计数。

已知通用数据库实现 `backend/internal/database/database.go`、`migrator.go` 对应[数据库主文](../backend/database-guidelines.md)，不扩展到所有 database 文件。路径信号只是保守提示，不能证明时间、连接或迁移启动等通用约定确实改变；合同未变时应据实核验，不为消除提醒制造新规范。
前端路由对应公开导航或目录合同，配置对应环境变量，迁移对应后端 README，发布、部署、依赖与代理分别对应其主文。

门禁自测遵循[测试环境约定](testing.md#候选和环境)，核对 hook、本地和 PR 入口对同一主题的判断，确保宿主 CI 环境不会干扰临时仓库。

| 入口 | 主题规则 | 版本检查 | 结构检查 | UTC |
| --- | --- | --- | --- | --- |
| pre-commit | 缺必需同步阻断；导航不阻断 | 有暂存 SQL 时检查 index 快照并阻断 | 不执行 | 同一 SQL 条件、同一快照，失败阻断 |
| check-doc-freshness.sh --staged | 同上 | 不执行 | 不执行 | 不执行 |
| check-doc-freshness.sh 普通模式 | 提醒不阻断 | 工作区，失败阻断 | 工作区，失败阻断 | 不执行 |
| pre-push / local-ci-parity | 调用普通模式 | 同普通模式 | 同普通模式 | 单独运行并阻断 |
| CI 文档 job | 调用普通模式 | 工作区/干净 checkout，失败阻断 | 同左 | 独立 migration-utc-safety job，不由文档 job 执行 |

pre-commit 仅在暂存 SQL 变化（含删除）时，将 index 中后端 README 与两个引擎迁移目录导出到同一私有快照，复用普通版本和 UTC 检查器；工作区的未暂存修正或破坏不影响该候选。普通检查器仍检查工作区。主题门禁不证明语义同步，导航与 CI 提醒仍需人工核对。

结构门禁使用已跟踪及未忽略的未跟踪文件；docs 根入口始终必须存在。公开目录（包括未忽略的空目录）必须有 README。匹配 Git 忽略规则且没有可见文件后代的目录及空子目录不检查 README；忽略规则下仍被跟踪的文档及其祖先继续受约束。
结构检查验证维护文档的相对链接、Markdown 标题/显式锚点、从总入口的可达性，并检查点目录配置等当前消费者中的退役路径。Markdown 链接会校验 fragment；非 Markdown 消费者目前只扫描退役路径，不通用解析 fragment，因此移动标题必须人工核对 Go、SQL、shell 等源码引用。
历史 CHANGELOG、生成历史及专门负例不作为旧路径消费者；fenced code 中相对链接示例不作为真实链接。外部网站不做联网检查。
