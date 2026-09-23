# 前端质量与回归

控制台优先保证正确状态、可访问操作和可重复工作流。通用组件、状态与 API 规则分别归[组件](component-guidelines.md)、[状态管理](state-management.md)、[类型边界](type-safety.md)；不要在本篇复制业务状态机。

## 质量门禁与测试范围

完整前端门禁是 `npm run check`（typecheck、lint、带覆盖率的测试、build），执行位置和交付流程见[贡献指南](../../../CONTRIBUTING.md)。行为变更在合并前通过完整门禁；跨模块类型或时间夹具变更也适用。依赖 audit、Node 版本、lockfile 结构比较与 jsdom selector override 见[仓库自动化](../../maintainers/automation.md)。

- 页面测试就近放置；API mapper、工具函数和 Hook 测试放在对应模块旁；UI 原语可使用 `components/ui/__tests__/`。
- 按变化的行为更新测试，包含加载、成功、空、失败以及用户可见的过期/刷新状态。
- 可访问性敏感界面验证角色、标签、禁用状态、对话框、键盘和空/错误变体。新顶层页面或复杂对话框添加 `*.a11y.test.tsx`，使用[共用 axe 方式](a11y-guidelines.md#工具与回归)。
- 现有 axe smoke 必须通过，lint 不得出现 `jsx-a11y` error。当前债务 warn 的准确范围见[可访问性](a11y-guidelines.md#工具与回归)，不得据此豁免新增障碍。
- 桌面和移动布局都保持可用。禁止负值或按视口缩放的文字技巧造成控制文字溢出；布局/格式化复用共用状态、日期、图表及主题工具。
- API/领域变更同步后端合同、前端领域类型与对应回归。备份专用回归见[领域入口](../domains/README.md)。

## 语言资源

用户可见文案使用现有 i18n。显式切换语言只能走 `setLanguage()`，启动等待 `i18nReady`，详见[可访问性语言规则](a11y-guidelines.md#语言与语义)。

`i18n/index.ts` 在首次渲染前加载检测语言，另一语言按需懒加载；不要从启动模块静态导入 `i18n/locales/*`，也不要把演示数据或只在管理界面使用的 API 重新塞入启动 bundle。

## 时间稳定夹具

涉及 `expiresAt`、TTL、lease、setup token、preflight 或 grant 的测试，必须以测试时钟派生时间或冻结时钟。有效夹具的余量要明显长于测试耗时；过期夹具使用同一时钟之前的时间。仅作展示、无有效期判断的固定历史日期可以保留。

```ts
const futureExpiry = () => new Date(Date.now() + 60 * 60 * 1000).toISOString();
const pastExpiry = () => new Date(Date.now() - 60 * 1000).toISOString();
```

不要用某个日历日期永久代表“未来”。若假时钟影响 `userEvent`，优先使用相对实时时间，或配置其推进时钟方法并在测试后恢复。回归断言有效状态动作先变为可用再点击；过期状态动作仍禁用且没有 API 写入。改动后运行相关测试及完整门禁。

## 演示模式

`VITE_ENABLE_DEMO_MODE=true` 才开启无令牌的前端演示路径。有认证用户仍使用真实 API 路径；默认无令牌访问 `/app/*` 必须返回登录页。

- 演示分支通过 `loadMocks() = import("@/data/mock")` 懒加载数据。无令牌读写使用本地 mock 或安全空状态，不调用写 API、不连接真实服务器、不使用真实 SSH key 或备份存储。
- 登录页、应用外壳和演示入口明确说明仅使用模拟数据，未连接真实基础设施。文档不得宣称未经当前证据支持的托管演示基础设施、遥测采集、生产成熟度或用户规模。
- 模拟故事至少含成功路径及可解释的失败路径；涉及的备份可信度、恢复演练、Doctor、健康时间线和任务/日志都提供相应证据与下一步。
- 路由测试覆盖默认拒绝及显式开关放行；登录/外壳测试验证模拟标识与进入演示不提交登录凭据；Hook/页面测试覆盖成功和失败故事。
- 演示代码或说明变更同时通过文档新鲜度、前端门禁和 diff 检查。环境变量说明见[环境变量](../../env-vars.md)。

返回[前端入口](README.md)。
