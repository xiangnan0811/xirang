# 状态管理

使用 React Context、组件状态、URL、浏览器存储及显式 API 包装。`web/package.json` 未引入 Redux、Zustand、React Query 或 SWR；新增状态库需要明确架构决策。

## 状态归属

| 状态 | 归属 |
|---|---|
| 认证会话 | auth Context，令牌保存于 `sessionStorage`，迁移并清除旧 `localStorage` 认证键 |
| 主题与偏好 | theme Context、`use-user-preferences.ts`、`use-persistent-state.ts` |
| 多页面共享领域集合 | nodes、tasks、policies、integrations 等 Context 及共享 console Provider |
| 筛选、分页、弹窗、草稿和视图模式 | 页面或 Hook 本地；适配时使用 `use-dialog-draft.ts` |
| 派生值 | 从唯一源状态计算，不重复存储 |
| 实时日志与终端流 | 专用 Hook、组件及 WebSocket 辅助 |

只有多路由/主要面板需要同一数据、跨页写操作需触发刷新，或认证、主题、命令面板等全局值，才提升到 Context。不要把每个筛选项和弹窗开关全局化，也不要在不同层重复共享搜索状态。

Context 按四文件拆分：`*-context.tsx` 入口重导出 Provider，`*-context-provider.tsx` 仅导出组件，`*-context.hooks.ts` 放消费 Hook，`*-context.shared.ts` 放 Context 对象与值类型。导出约束见[组件约定](component-guidelines.md#组件与导出边界)。

## 服务器与 URL 状态

- 服务器数据经过[API 映射](type-safety.md)再入状态树，不并存 raw DTO 与领域对象。
- Context 显式暴露刷新和写操作，不在展示组件内隐藏请求副作用。
- 分页使用 `unwrapPaginated` 暴露 `items`、`total`、`page`、`pageSize`。
- 备份、SSH、安全和告警操作采用保守更新与明确刷新，避免乐观显示尚未确认的成功。
- URL 页签、筛选或视图状态仅修改目标键，保留无关参数：

```ts
const next = new URLSearchParams(searchParams);
next.set("tab", tab);
setSearchParams(next, { replace: true });
```

领域禁止入 URL 的敏感查询、内容票据等不适用此模式；见[搜索](../domains/backup-search.md)与[内容交付](../domains/backup-content-delivery.md)。

## 浏览器存储与认证所有权

页面或认证顶层通过 `useAuth()` 取令牌，显式传给功能组件、Hook 与页面片段。功能模块不得自行读取 `xirang-auth-token`。auth Context 负责持久化、旧存储迁移、登出清理和存储不可用处理。

浏览器存储可能不可用，读写必须有空值检查与 `try/catch`，参考 `auth-context-provider.tsx` 的安全访问函数。禁止向 `localStorage` 写入敏感会话数据；允许的认证会话及按动作保存的 step-up 证明仍受[凭据与访问合同](../domains/credentials-access.md)限制，不构成任意业务数据落盘授权。

功能测试显式传入 `token="test-token"` 或 `token={null}`。重复调用 API 的功能应添加源码边界回归，禁止生产文件直接读取认证存储。

终端临时授权的草稿、授权状态、step-up、WebSocket 拒绝和重试统一见[凭据与访问合同](../domains/credentials-access.md)。

返回[前端入口](README.md)。
