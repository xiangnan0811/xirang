# 备份资产 Provider 读取适配

## Goal

在 Child 1 已建立的 Repository/RecoveryPoint/AssetRef、安全与审计基础上，提供 Rsync、Restic、Rclone 的统一只读 Provider 访问边界：识别并验证仓库身份，安全保存/撤销访问绑定，探测真实能力，以稳定 opaque locator 分页列举恢复点和目录项，并提供受限的 stat、顺序读取与可选 Range 读取。该边界必须让后续 Catalog、Content、恢复与生命周期子任务依赖窄接口而不是 executor 字符串，同时不得在本任务中发布、迁移或删除任何 Provider 数据。

## Parent Contract

- 父任务：`07-12-backup-data-explorer-design`。
- 依赖：已归档 Child 1 `07-13-backup-assets-domain-foundation`，其功能提交为 `9d8215f8d18d70e522cb8965ccb584d0a0f5162a`，归档提交为 `b1b8beae13b5df49a59fa5d2a9fcf08e35405318`。
- 规范来源：父任务 `prd.md`、`design.md`，以及 `implement.md` 的 Execution Contract、Child 2、Requirement Coverage Matrix、High-Risk Review Gates 与 Program Rollback Strategy。
- 本任务从正式发布 `v0.45.0` 的 `main@8317fe6e04ea9ec1480460074ce9fe99ae8a7626` 开始。
- 本任务属于复杂跨层变更。进入实施前必须完成并复核 focused `design.md` 与 `implement.md`，然后由用户明确批准 `task.py start`。

## Requirements

### Provider Ports And Registry

- 建立按消费者能力拆分的窄接口：Repository probe、point/entry read、可选 Range read、未来 publication/manifest/reconcile/delete 契约；消费者不得按 Task executor 字符串分支，也不得要求一个巨型 Provider 接口。
- Registry 对未知、未注册或能力不满足的 Provider 返回 Child 1 typed sentinel/capability reason；不得 panic、静默降级或暴露底层工具原始错误。
- 本任务可以冻结 mutation 接口的类型契约，但不得调用 publication、manifest build 或 delete，也不得向现有仓库写入 marker、metadata 或测试文件。
- Command Task 在没有显式产物契约时继续返回稳定 unsupported capability，不推测目录或创建虚假 Repository/RecoveryPoint。

### Repository Identity And Access Binding

- Probe 必须从 Provider 原生事实或 Xirang 管理标记生成稳定、非秘密 `repository_identity`，并返回 capability revision、availability 与 typed unavailable reasons。
- 新接入必须先 probe 再持久化；相同 identity 可以幂等重试或显式重连，不同 identity 不得覆盖旧 Repository。共享仓库身份不得自动扩大 Task lineage ownership。
- Access binding 只保存加密 credential/config/locator 引用和必要的非秘密连接元数据；密码、SSH key、Rclone 配置、Restic secret、原始 remote/path 不得进入公开 DTO、日志、错误或资产审计。
- Disconnect 只撤销当前访问并标记 Repository 不可在线读取；不得删除 Repository、RecoveryPoint、Catalog、Provider bytes 或历史身份。重新接入的完整 import/rebuild/purge 流程仍属于 Child 14。

### Safe Read Semantics

- 所有 API 和内部任务使用 opaque Repository/RecoveryPoint/Entry locator；HTTP 请求不得接收任意 Provider path、remote 或 shell fragment 作为资源身份。
- 从现有 file handler 抽取共享的 safe-path 下层能力：root allowlist、规范化、realpath/containment、NUL/绝对路径/`..` 拒绝、symlink 逃逸与特殊文件处理必须只有一个权威实现；Provider package 不得导入 handler 或复制路径验证。
- 本地/Rsync tree 读取只能在验证后的 root 内执行只读 list/stat/open；任何符号链接跟随策略都必须显式、可测试并禁止越界。
- Restic 只使用精确 repository identity 与 full snapshot ID 进行 snapshots/ls/dump 等只读操作；禁止 `latest`、forget/prune/restore 或含糊 snapshot prefix。
- Rclone 只使用经过验证的 remote/config binding 和只读命令；list/stat/cat/Range 必须以参数数组传递并限制输出、条目、字节、时间和并发。只有 capability probe 证明 offset/count 语义稳定时才报告 Range。
- 每次 open/stat/list 前后必须验证 locator/capability revision 所需的不变量；Provider 离线、内容变化、Range 不可用、超限和取消要返回稳定 typed error，不返回部分数据后伪装成功。

### SSH, Process And Resource Safety

- 新增并复用 repository probe/list/read 的 SSH credential purposes，通过现有 `DialSSHForNodePurpose`/credential provider 路径获取凭据；不得建立裸 SSH auth、绕过 scope 或复用 terminal/task-write purpose。
- 所有外部命令使用 context cancellation、固定 argv、受限环境与输出、可配置 timeout/concurrency；禁止 shell 拼接和把 credential/locator 写入 process/error logs。
- 客户端取消必须终止远程命令/reader 并关闭 SSH session；超时、截断、解析失败和工具版本不兼容必须可区分并进入聚合指标/安全审计。
- 凭据实际使用继续写既有 credential audit，并以 correlation ID 关联新的 typed asset audit；两者均不得复制秘密。

### API, Authorization And Audit

- 提供 feature-gated 的 Repository connect/probe、list、detail、reconcile 和 disconnect 后端 API；handler 只负责 parse/bind/call/respond，领域服务拥有 identity conflict、access binding、状态转换和幂等性。
- 所有 `/api/v1` 路由必须经过现有 Auth、Child 1 专用 RBAC 和服务端 ownership。Admin 可管理接入；Operator 只能在授权 lineage 范围内读取允许的元数据；Viewer 在名称、计数、identity 或存在性泄漏前被拒绝。
- 共享 Repository 的恢复点、计数、能力和证据必须按 producing lineage 过滤；未归属/imported 数据默认仅 Admin 可见，不得因为拥有同仓库的一个 Task 而横向看到其他 Task。
- 公开响应只使用显式 sanitized DTO、稳定 code 与 localized params；500 不返回 `err.Error()`，未知 Provider code 映射为安全 fallback 并保留 correlation ID。
- connect/list/detail/reconcile/disconnect 均使用 Child 1 typed action registry 和 sanitizer；不得记录 raw path、entry name、query、credential、Provider locator、命令参数或工具输出。

### Mutable-Head Observation

- 对 legacy mutable Repository，首次成功 probe/reconcile 只能创建一个稳定 `state=observed`、`version_semantics=mutable_head` 的单例 RecoveryPoint；后续成功观察复用同一 ID，不制造历史点。
- 成功观察原子推进 `observed_at`、source fingerprint、capability revision 和后续 Catalog 所需 observation metadata；失败只更新 availability/staleness/安全错误码，不把 observed 改成 committed/degraded，也不丢弃上一有效 observation。
- Disconnect 保留 observed 身份和未来离线 Catalog 语义；retired/purge/imported baseline 等生命周期仍由后续任务处理，本任务不得把 mutable view 转换成不可变恢复点。

### Compatibility And Feature Boundary

- `backup_assets.enabled=false` 时新路由返回稳定 not-enabled 响应，不执行 probe、凭据解析、SSH、Provider 命令或数据库 mutation；现有备份执行、旧 snapshot/file 浏览和终端行为保持不变。
- 本任务不增加公开前端入口，不启用新功能，不改变 Task 执行命令，不创建新的 schema migration，也不写、移动、重命名或删除 Provider bytes。
- Swagger/generated API 文档只反映实际实现的 feature-gated routes；snake_case DTO 与公开错误语义保持项目规范。

## Constraints

- 遵循现有 response helpers、Auth/RBAC/ownership、structured logging、sentinel errors、settings registry、sanitized DTO、SSH purpose 和 audit 规范。
- 复用 Child 1 已落地的 model、state machine、keyring、lease、feature gate、permissions 和 audit registry，不创建第二套 Repository/RecoveryPoint/secret/audit 抽象。
- 任何路径或命令安全规则修改必须同时迁移现有 file handler 使用方并保持行为/测试兼容，不能只让新 Provider 安全。
- Provider 原生语义和能力必须通过 fixture/可执行 fake 验证；不能仅凭工具文档字符串或版本号宣称支持。
- 如果最新代码证据要求改变父任务领域、安全、权限或生命周期边界，必须返回父规划审阅，不能在 Child 2 内静默扩张。

## Acceptance Criteria

- [x] focused `design.md` 与 `implement.md` 已基于 `main@8317fe6`、Child 1 实现、父规划和相关 Trellis specs 完成并经用户复核。
- [x] Registry 与窄 Provider ports 有 compile-time/behavior tests；未知 Provider、缺失能力和 Command unsupported 返回稳定 typed result。
- [x] Repository probe/identity/access-binding 服务通过幂等接入、identity conflict、secret sanitization、disconnect/reconnect 与共享仓库 ownership 测试。
- [x] safe-path 权威实现同时服务现有 file handler 和新 Provider，覆盖绝对路径、`..`、NUL、symlink race/escape、root overlap、特殊文件及平台边界。
- [x] Rsync/local、Restic、Rclone 只读 adapters 通过 exact locator、分页、stat、顺序读取、取消、超时、输出上限、离线与 capability fixture 测试；没有写/delete 命令。
- [x] Range capability 仅在真实 probe 证明后暴露；无 Range、非法/变化 source 和超限均明确降级或失败。
- [x] repository probe/list/read SSH purposes 通过 scope、ownership、credential audit 与 wrong-purpose rejection 测试；没有裸 SSH credential path。
- [x] connect/list/detail/reconcile/disconnect routes 通过 Auth/RBAC/ownership、feature-disabled no-side-effect、共享 Repository 逐 lineage 过滤、Viewer no-existence-leak 与 typed asset audit 测试。
- [x] mutable-head 首次观察、stable singleton ID、成功刷新、失败保留上一 generation metadata、disconnect offline/stale 和 no-fake-history 测试通过。
- [x] Swagger 生成结果与 tracked docs 一致，公开 DTO/错误/log/audit secret scan 通过。
- [x] focused backend tests、全仓质量门禁和 `git diff --check` 通过；变更不包含 schema migration、Provider mutation、公开 UI 或 feature enablement。
- [ ] 按纠正后的流程在同一分支完成实现/验证、Phase 3.4 工作提交、`trellis-finish-work` 归档+journal 自动提交后，才允许 push、创建 PR、监控 CI/合并与 post-merge；不得再次拆成功能 PR 和归档 PR。

## Out Of Scope

- Restic run-to-snapshot 精确发布、Rsync/Rclone 版本化写入与 commit marker。
- Catalog generation、全局搜索、Content ticket/Range HTTP delivery、预览、Worker、导出、恢复、保留、purge 与 GA 启用。
- 任意 Provider 数据写入、迁移、修复、删除或自动 import/rebuild。
- 新前端工作区、Repository 管理 UI 或公共分享。

## Open Decisions

- 已解决。用户于 2026-07-13 选择方案 A：采用分层能力端口、受限 runner 与操作级 fileaccess；Restic 使用 Provider 原生强身份，Rsync/Rclone 使用任务谱系作用域的、经过 probe 验证的 endpoint identity，不因物理目标相似自动合并 mutable lineage。
- 不提供接收任意 remote/path 的独立 probe API。connect 必须先 probe 再持久化，reconcile 复用相同 probe；两者分别使用既有 `repository_connect` / `repository_reconcile` typed audit action，并以安全 stage 区分。
- 纯 SFTP 无法对恶意远端服务端提供本地 `openat2` 等价的原子 containment。设计冻结的信任边界是：SSH 主机身份和远端服务端属于受信基础设施，路径、条目类型和并发内容变化不受信并执行 pre/open/post 校验；若未来要求抵御恶意服务端，必须另立引入 remote helper/agent 的父级架构任务。

## Notes

- 用户于 2026-07-13 明确授权创建并推进 Child 2。
- 用户于 2026-07-13 明确批准 focused 架构方案 A。
- 创建 Child 2 不等于启动实施；本任务在 focused planning package 通过用户 review gate 前保持了 `planning`，用户明确批准后才转为 `in_progress`。
