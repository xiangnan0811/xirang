# Xirang 体验审计：证据校正与修复范围

校正日期：2026-10-04。本报告记录已有浏览器扫描证据的复核结果，不是全站符合性认证、真实后端验收或完整生命周期验收。产品行为以 [docs/spec](docs/spec/README.md) 为准，架构导航见 [PROJECT.md](PROJECT.md)。

## 1. 结论与证据边界

agy 的 Mock 驱动浏览器扫描提供了有用的问题线索，标签与宽表焦点修复方向合理。但原报告的「444 项评估、384 路由状态、45 边缘状态」「完美适配」「容错优秀」「全生命周期已验证」不能由当前可获取的证据支持，撤回这些验收表述。

历史证据校正只重算已有 JSON、核对走查源码与当前产品合同；下述历史统计不包含本轮产品修复的浏览器验证。历史数据没有运行批次 ID、浏览器项目字段、主题字段或脏工作区指纹，不能事后确定每条记录属于修复前还是修复后。文件摘要只能绑定这份证据，不能反向证明浏览器当时运行的代码版本。

### 原始证据身份

本地原始文件位于 `.agents/teamwork/teamwork_preview_worker_walkthrough_1/walkthrough_evidence.json`，该私有目录不需要提交 Git；校正工具不修改它。

| 字段 | 核验值 |
| --- | --- |
| SHA-256 | `96430fb0ae10420e8da40fbd81c5622dd5a706d0779a48e6e1ddfb8a668a0131` |
| 字节数 | 777878 |
| 原文件生成时间 | `2026-10-04T07:27:38.376Z` |
| 记录时间范围 | `2026-10-04T07:15:12.132Z` 至 `2026-10-04T07:27:33.018Z` |
| 原文件 runner 声明 | Playwright 1.63.0 / Chromium Headless (1243)；旧汇编器硬编码，不能单独作为环境证明 |

干净 checkout 不包含该私有证据。复核历史数字需要取得 SHA-256 匹配的原文件；仅有源码不能复原历史浏览器状态。不将截图、Trace、真实后端闭环或读屏人工验收假定为已提供。

## 2. 校正后的统计

### 2.1 观察记录与独立覆盖分开计数

| 分类 | 观察记录数 | 按已记录字段区分的键数 | 同键额外观察数 | 限制 |
| --- | ---: | ---: | ---: | --- |
| 路由/页签 | 256 | 128 | 128 | 键为路径、视口、语言 |
| 弹窗 | 24 | 12 | 12 | 有视口，无语言字段；不推断完整语言覆盖 |
| 边缘状态 | 30 | 15 | 15 | 有路径及异常类型，无视口/语言字段 |
| 登录 | 2 | 1 | 1 | 无视口/语言字段 |
| 合计 | 312 | 156 | 156 | 同键不等于同一运行或完全相同页面状态 |

旧汇编器扫描固定临时目录的所有 chunk 并追加汇总，不隔离批次，因此重复观察被混入「覆盖数」。当前统计只读一个显式指定的快照，不扫描环境中的 chunk、不选择最近一次结果，也不丢弃重复观察中的违规。

### 2.2 路由矩阵

| 视口 | 语言 | 观察记录 | 独立路径/页签 |
| --- | --- | ---: | ---: |
| 1440×900 | zh | 64 | 32 |
| 1440×900 | en | 64 | 32 |
| 1024×768 | zh | 64 | 32 |
| 390×844 | zh | 64 | 32 |
| 1024×768 | en | 0 | 0 |
| 390×844 | en | 0 | 0 |

现有独立路由组合为 128；若要完成本次定义的 32 个目标 × 3 个视口 × 2 种语言，应有 192 个独立组合。此矩阵不包括主题、角色、表单状态或业务结果维度，也不覆盖所有重定向、404、查询参数变体。

走查源码中的 32 个目标如下；这是访问目标清单，不表示每个页面的全部操作已完成：

| 目标组 | 路径或页签 | 数量 |
| --- | --- | ---: |
| 认证 | `/login` | 1 |
| 概览和节点列表 | `/app/overview`、`/app/nodes` | 2 |
| 节点详情 | `/app/nodes/1?tab=`：overview、tasks、alerts、profile、anomaly | 5 |
| 密钥与策略 | `/app/ssh-keys`、`/app/policies` | 2 |
| 备份 | `/app/backups/overview`、`/app/backups/data`、`/app/backups/recovery` | 3 |
| 任务、日志、通知、自动化 | `/app/tasks`、`/app/logs`、`/app/notifications`、`/app/automation-rules` | 4 |
| 审计与凭据 | `/app/audit`、`/app/credential-audit`、`/app/credential-access-grants`、`/app/credentials` | 4 |
| 报表 | `/app/reports?tab=`：sla、slo | 2 |
| 设置 | `/app/settings?tab=`：personal、account、users、channels、silences、escalation、system、maintenance | 8 |
| 更多导航 | `/app/more` | 1 |

### 2.3 弹窗和边缘状态

有观察记录的 12 种弹窗：NodeEditorDialog、SSHKeyEditorDialog、SSHKeyRotationWizard、CredentialEditorDialog、PolicyEditorDialog、TaskCreateDialog、NasMountWizard、IntegrationCreateDialog、EscalationPolicyEditor、AutomationRulesFormDialog、ReportConfigDialog、SLODialog。每种两条记录，均记为 1440×900；语言未记录。

源码还尝试 NodeDoctorDialog、WebTerminal、BatchCommandDialog、RecoveryPlanWizard、SilenceEditor、UserCreateDialog、TOTPSetupDialog，但当前快照没有这些名称的成功扫描记录。不能标记为已覆盖，也不能仅凭没有记录断定产品功能失败。

15 个边缘状态键来自：8 个空状态、5 个 API 错误状态、2 个长字符串状态。它们是渲染采样，不是「错误出现 → 用户重试 → 数据恢复 → 确认无副作用」的闭环验证。

### 2.4 axe、布局和运行错误

| 指标 | 当前快照重算值 | 正确含义 |
| --- | ---: | --- |
| `color-contrast` 规则出现次数 | 258 | 258 条规则违规观察，含同场景重复采样 |
| 这些规则的 `nodesCount` 合计 | 1329 | 元素出现次数，含重复；不是独立缺陷数 |
| 其他 axe 违规规则 | 0 | 当前快照未记录，不证明历史问题均已修复 |
| 文档级横向溢出观察 | 0 | 采样时 `documentElement.scrollWidth <= innerWidth` |
| 控制台 error 条目 | 58 | 日志条目合计，不等于独立根因数 |
| `pageerror` 条目 | 0 | 未记录未捕获页面异常，不等于所有业务成功 |
| 请求失败事件条目 | 0 | `requestfailed` 不等于 HTTP 500/503 计数 |

旧报告的 10/15/381/3 次违规计数没有与本次原始快照一致的可追溯基线，保留为「旧报告主张，未核验」，不再作为验收数字。采集器每条规则只保留前三个节点样本，因此无法从现有文件精确还原全部独立违规元素。

现有对比度样本既包含 Badge，也包含说明文字、表头和状态文字。不能将全部违规归因于 Badge。部分样本已带新 Badge 类名，仍有不足 4.5:1 的记录；需在字体、数据和动效稳定后复验，不能据此宣称当前最终样式已通过或确定其失败根因。

## 3. 历史方法局限与新采集边界

以下局限描述原始快照的旧采集方式，不是当前 `web/e2e/walkthrough/` 的实现。PR2 将采集与普通 E2E 分离，执行入口和证据规则见[前端质量合同](docs/spec/frontend/quality-guidelines.md#独立-mock-浏览器走查)；原始历史数据不因此获得新的验收效力。

- 路由循环在 `domcontentloaded` 后固定等待 100–120ms，没有断言目标页签、数据完成或关键内容可见；可能扫描过渡态。
- 路由套件只断言记录数，axe 违规、页面错误和横向溢出不直接导致失败。
- 打开弹窗失败会被捕获并忽略；19 种尝试只要求至少 5 种成功。打开、扫描、Escape 关闭不等于完成向导。
- 未匹配 API 默认返回成功空对象，不能证明真实响应契约；也没有真实存储、SSH、恢复执行的证据。
- 登录扫描在提交前完成，没有验证完整验证码、TOTP、锁定和成功登录链路。
- 没有完整明暗主题矩阵、键盘滚动、焦点恢复、实际读屏或移动触控验收。axe 无违规不等于 WCAG 符合性。
- [项目可访问性合同](docs/spec/frontend/a11y-guidelines.md)目标为 WCAG 2.1 AA；扫描包含 WCAG 2.2 AA 标签属于额外检查，不自动升级产品合同。

当前采集按运行隔离输出，记录候选内容指纹、run ID、实际浏览器版本、主题、视口、语言和角色；声明 384 个路由、48 个弹窗、30 个边缘状态及 2 个登录错误场景，共 464 个独立场景。初始化只写存储；导航后真正等待异步就绪结果、字体和有限过渡，不修改产品颜色来掩盖过渡态。未知 Mock 请求、运行错误、axe 违规及横向溢出使场景失败；重试单列且不能掩盖首次失败。未执行的审计保持 `null`，不计为零违规。

## 4. 经源码及合同校正的问题清单

axe impact 与项目缺陷优先级分开判断，不直接把 `critical` 映射为 P0。

| 编号 | 校正结论 | 依据与边界 |
| --- | --- | --- |
| A1 控件名称 | 标签修复方向认可，待浏览器验收 | [用户设置](web/src/pages/settings-page.users.tsx)缺口是创建用户的角色控件，不是旧报告所写的角色/状态筛选器；[报表配置](web/src/components/report-config-dialog.tsx)补充 scope/period 标签关联 |
| A2 宽表键盘访问 | region、名称和 `tabIndex` 方向认可，待实际滚动验证 | [操作审计](web/src/pages/audit-page.tsx)、[凭据审计](web/src/pages/credential-audit-page.tsx)、[临时授权](web/src/pages/credential-access-grants-page.tsx)；属性断言不证明方向键滚动和焦点可见 |
| A3 文字对比度 | 已改为独立状态文字 token，仍按实际表面验收，不宣称全站关闭 | [Badge 变体](web/src/components/ui/badge.variants.ts)保留语义背景、恢复原有字重并移除额外字距；侧栏分组移除额外透明度；普通小字仍需 4.5:1 |
| A4 Toast 点击区域 | 已收敛为 CSS 单一尺寸来源，真实 Toast 验收独立记录 | [index.css](web/src/index.css)提供 24px 关闭按钮尺寸；已删除未触发 Toast 的伪尺寸测试，不以 live region 属性作为热区证据 |
| B1 自动化运行可观测性 | 缺前端查询展示，不是没有持久日志 | [dispatcher](backend/internal/automation/dispatcher.go)已有逐规则日志及 durable effect 路径；[路由](backend/internal/api/router.go)和[前端 API](web/src/lib/api/automation-rules.ts)当前提供规则 CRUD。新增查询应复用执行事实，区分动作派发和最终任务结果，处理权限、脱敏、分页和保留语义 |
| B2 RPO/RTO 呈现 | 已聚合、已映射，报表未展示 | [generator](backend/internal/reporting/generator.go)、[Report 模型](backend/internal/model/report.go)、[前端映射](web/src/lib/api/reports-api.ts)、[报表行](web/src/pages/reports-page.tsx)。先展示已有值与未知状态，不新增重复聚合器 |
| B3 权限入口一致性 | 只保留逐页核对建议，不宣称普遍缺少拦截 | 凭据审计已有非 admin 不加载和重定向；自动化页情况不同。后端授权仍是安全边界，统一前端入口不能替代它 |
| C1 遗留用户页面 | 未挂载路由是线索，不代表引用清理已完成 | [router](web/src/router.tsx)没有挂载 [users-page](web/src/pages/users-page.tsx)；删除前核对所有引用和配套测试，不能以补标签代替清理 |
| C2 Cron 自然语言解释 | 暂缓，缺少定位到具体未覆盖控件的证据 | 旧报告的约 15 行实现量没有验证依据，不列入已确认缺陷 |
| C3 WebSocket 退避 | 撤回新增算法建议 | [重连客户端](web/src/lib/ws/reconnecting-socket.ts)已有指数退避、jitter、上限与最大次数；日志和终端均复用。没有重试风暴复现，不再重复建设 |

### 不应改变的业务事实

- [凭据合同](docs/spec/domains/credentials-access.md)明确授权列表是只读入口，不提供批准、拒绝或撤销按钮。撤回原报告对这些页面能力的宣称。
- [任务合同](docs/spec/domains/task-execution-recovery.md#完成事实rpo-与回归)规定 RPO 来自最近 verified completion 间隔，RTO 来自最近成功 restore 耗时；当前指标不按报告 period 筛选。「过去 30 天」和「最近演练 RTO」是另一个口径，需要独立需求决策，不能作为修复静默替换。
- 现有页面访问不能证明安全防篡改、零停机密钥轮换、完整灾难恢复或全键盘/辅助技术可用性。撤回原生命周期图中的验收暗示，实际承诺回到领域合同。

## 5. 修复范围与验收要求

本轮已授权第一批 A1–A4 与走查采集改造、第二批 B2 与 B1，按四个独立 PR 顺序实施和验收。批准范围不等于已交付；每个 PR 仍须完成浏览器或真实后端证明、独立双审和项目门禁。

| 范围 | 后续处置 | 关闭条件 |
| --- | --- | --- |
| A1、A2 | 首批保留并完善已有小修复 | 中英名称正确，label 关联；键盘可达、横向滚动、焦点可见，移动隐藏表格不增加焦点站点 |
| A3 | 首批重新整理颜色方案，不接受当前补丁直接关闭 | 基于实际违规样本定位 token/组件；明暗主题稳定渲染下验证对比度，覆盖弱化文字、状态文字及 Badge；不以类名测试代替 |
| A4 | 首批收敛到一处样式来源并实测 | 实际触发 Toast，检查按钮矩形、间距、点击关闭及明暗/移动布局 |
| 相关测试和 i18n | 与首批修复一起整理 | 去掉仅锁定类名或伪装尺寸验证的测试；保留行为回归，复用已有标题键，避免重复命名空间 |
| 走查工具 | 保留前先改造；否则不作为验收门禁 | 独立批次、明确矩阵、稳定等待、场景失败可见、Mock 边界严格，产出候选绑定证据 |
| B2 | 第二批展示已有 RPO/RTO | 真实 API 字段到界面，null/未知/不达标正确区分，明确现有统计口径，不改聚合定义 |
| B1 | 第二批提供安全日志查询与执行历史 | 仅公开白名单 DTO，不公开原始 Error、Details 或配置；保持 admin-only、分页和历史保留语义；不能用 Mock 闭环代替真实派发验收 |
| B3、C1 | 不纳入本轮全局重构或删除 | 自动化历史新增边界配套角色保护不扩展为全局权限统一；保留旧用户页面，不进行引用清理 |
| 新容灾大盘、Cron 优化、重连算法 | 暂不纳入修复 | 新大盘与 Cron 需要明确需求或具体缺陷；重连算法已有实现，除非新证据证明缺陷 |

## 6. 复核命令与交付状态

历史快照重算入口如下。从 checkout 根目录运行，参数可指向取得的原始快照；路径不是工具默认值，干净 checkout 缺少历史文件时不伪造替代数据：

```bash
node web/scripts/compile-walkthrough-evidence.mjs .agents/teamwork/teamwork_preview_worker_walkthrough_1/walkthrough_evidence.json
```

工具向标准输出打印源文件 SHA-256、观察数、按已记录字段区分的覆盖键数、路由矩阵、规则/节点出现次数和遥测条目数。不写入源文件，不聚合固定临时目录，不伪造缺失维度，不把旧 `meta.summary` 当作重算结果。

新采集在 `web/` 执行 `npm run walkthrough`，入口独占创建并打印 `.tmp/agent/walkthrough-<UUID>/`。完成后在根目录运行 `node web/scripts/compile-walkthrough-evidence.mjs --run <本次目录>`。只汇编该运行，不读取旧 chunk 或其他运行；候选改变、场景缺失、重试断档、重复或失败记录均不能通过。该矩阵是 Mock 界面证明，不是后端生命周期或全面 WCAG 合规证明。

当前状态：历史统计与产品验收分别记录。首个产品候选的定向验证已观察到：

- 真实 Chromium 的中英报表标签点击聚焦、选择值保持和用户创建角色键盘可访问名称。
- 平板三个宽表区域在中英、明暗主题下方向键滚动，滚至最右侧后末列可见；焦点环截图可见；移动隐藏区域不可聚焦，也未进入连续 Tab 顺序。
- 概览、操作审计与真实快照对比组件的明暗主题稳定态 axe 对比度检查无违规；普通状态文字、侧栏分组与四种 Badge 分开采样。深色 destructive Badge 首次检查仍不足 4.5:1，按每次增加 2 点亮度验证，在卡片、弱化及悬停表面收敛为 `--destructive-text: 0 80% 72%`；不改变原背景和图标色。
- 实际 success/error Toast 在桌面、平板、移动及明暗主题下可用 Enter 或点击关闭，热区没有裁切。计算样式为 24×24px；桌面和平板矩形为 24×24，移动矩形宽为 23.999998092651367、高为 24（浏览器浮点几何）。保留原有 live-region 结构；DOM 可观察到本次通知文字，但没有据此声称人工读屏播报验收完成。
- 显式设置 `prefers-reduced-motion` 为 `no-preference` 与 `reduce` 后，浏览器 `matchMedia` 分别返回 false 与 true；概览明暗主题四组对比度扫描均无违规，实际 Toast 用 Enter 关闭后剩余节点数均为 0。
- 修复后的定向回归 6 个文件、43 个测试通过；完整 `scripts/local-ci-parity.sh` 通过，包含前端 209 个文件、2057 个测试及构建、后端 lint/test/build、漏洞检查、bundle budget、文档与迁移门禁。

这些是 Mock 数据下的针对性产品证明，不是完整走查矩阵或后端证明。PR1 已在独立 GPT/Grok 双审、项目门禁和远程 CI 通过后合并（PR #588）。

PR2 的后续定向验证还观察到：

- 匿名/管理员 × 明暗主题四项初始化 smoke 通过；目标 URL 正确、无 pageerror，管理员通过 Enter 打开节点弹窗后 Escape 恢复原按钮焦点。
- 主题就绪修复后，凭据表格继承文字从过渡期浅色值收敛为深色前景，axe 对比度检查无违规；不改变原主题调色板。
- 节点详情、凭据、SSH Key 和备份概览相关的 116 个独立定向场景在禁用重试的检查中通过。
- 未使用 SSH Key 的表格与卡片不再用父级透明度弱化全部文字；存储用量文字使用语义文字 token，进度条背景色不变。真实 Chromium normal-motion 下，SSH Key 与备份概览的明暗、桌面/移动检查无 axe 违规或文档横向溢出；SSH 编辑弹窗 Escape 返回持久的下拉菜单触发按钮。
- 证据编译器的合成故障夹具验证重试缺口、缺失记录、跨运行记录和 scratch 越界会被拒绝；这些夹具不是浏览器验收。

完整 464 场景及最终候选指纹以本次运行 manifest、编译结果和对应 PR 的交付记录为准，不能由上述定向检查替代。日志连接旧回调污染新连接状态的问题已另获授权纳入 PR2 的有界修复，不引入新重连算法。第二批 B2/B1 仍按后续独立 PR 顺序交付。旧报告的全站合规、完整生命周期通过、零回归和架构重构工期结论均不保留。
