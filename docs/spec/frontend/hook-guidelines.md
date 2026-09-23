# Hook 约定

Hook 用于复用界面状态、请求编排、操作封装、偏好、过滤、分页和实时流。服务器数据由 Context 与显式请求/刷新函数管理；状态库选择见[状态管理](state-management.md)。

## 职责与复用

- Hook 使用 `use*` 名称，按[目录结构](directory-structure.md)存放。
- 纯函数与 React 生命周期分离，例如 `use-console-data.utils.ts`。不要为了省略属性传递，把不相关领域塞入一个 Hook。
- 写操作提供稳定动作函数与一致 loading/error 状态，参考 `use-api-action.ts`。按操作域拆分，参考 `use-console-node-operations.ts`、`use-console-policy-operations.ts`、`use-console-task-operations.ts` 和 `use-console-integration-alert-operations.ts`。
- 偏好持久化复用 `usePersistentState`，用户偏好使用 `use-user-preferences.ts` 导出的 `useRefreshInterval`、`useDefaultPageSize`、`useDatetimeFormat`；行为适配时复用 `usePageFilters`。

## 请求与副作用

- Hook 和 Context 调用[类型化 API 边界](type-safety.md)，接收领域数据，不直接 `fetch` 或保存原始 DTO。
- API 支持时传递 `AbortSignal`，例如 overview API 的 `options?: { signal?: AbortSignal }`。取消或过期请求不能提交旧结果。
- 实时日志复用 `lib/ws/` 与 `use-live-logs.ts`，页面不另写 Socket 生命周期。业务规则见[节点日志采集](../domains/node-log-collection.md)。
- effect 清理订阅、定时器、可取消请求与 WebSocket 监听；存储遵守[认证所有权](state-management.md#浏览器存储与认证所有权)。

## 动画帧所有权

嵌套 `requestAnimationFrame` 的 effect 必须在本地变量或 ref 中持有两个 ID，并在 cleanup 取消尚未执行的回调。禁止给 React state setter 附加可变字段或用断言掩盖该行为。

RAF ID 是不透明数值，`0` 有效；使用 `number | null` 和显式 `!== null` 判断。保留有意设计的两帧布局等待。

面板编辑器回归覆盖首帧前清理、两帧之间清理（第二个 ID 为 `0`）、关闭重开、卸载，以及正常两帧后图表就绪。参考 `pages/dashboards/panel-editor-dialog.tsx` 与同级 `.raf.test.tsx`。

返回[前端入口](README.md)。
