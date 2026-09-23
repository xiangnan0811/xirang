# 组件约定

使用函数组件、TypeScript 属性和 `components/ui/` 共用原语。新增按钮、卡片、对话框、输入、开关、徽章、骨架、分页和空状态前，先查现有实现。视觉变量、布局风格、图表与动效归[设计系统](../guides/design-system.md)；可访问性归[可访问性合同](a11y-guidelines.md)。

## 组件与导出边界

组件保持单一职责；大页面的组织遵守[目录结构](directory-structure.md)。本地辅助类型就近声明，API 数据规范化归[类型与 API 边界](type-safety.md)。

导出 React 组件的 `.tsx` 模块应保持组件导出边界；普通常量、variant 函数、第三方 API 和消费 Hook 放在同级 `.ts` 文件。例：`button.tsx` 与 `button.variants.ts`，`toast.tsx` 与 `toast-sonner.ts`。路由构造与懒加载组件也分开。

修改导出位置时更新使用方与 mock，运行 lint 验证 Fast Refresh 警告消失，不能只禁用规则。ESLint 当前允许常量导出的配置不改变共享模块的职责划分。

## Radix 单元素组合

使用 Radix `Slot` 或 `asChild` 必须只传入一个 React 元素。共用原语把插槽分支单独处理，不能把条件 loader/icon（即使分支为 `null`）和调用方元素作为多个兄弟子项传给 Slot。

```tsx
if (asChild) return <Slot>{children}</Slot>;

return (
  <button disabled={loading}>
    {loading ? <Loader2 aria-hidden /> : null}
    {children}
  </button>
);
```

修改支持 `asChild` 的原语时，至少测试一个链接子元素，捕获 `React.Children.only` 崩溃。

## 属性与共享导航

- 属性使用具名字段；多组布尔开关可用清晰的 variant 或小型联合类型替代。
- 持久化/API 数据使用共享领域类型，raw DTO 不进入组件。
- 事件按动作命名，如 `onSave`、`onClose`、`onConfirm`、`onRefresh`、`onSelectionChange`。
- 承载异步数据的组件显式处理加载、空、错误和权限状态。
- 侧栏、移动抽屉、命令面板等导航入口统一通过 `getVisibleNavItems(role)` 筛选，不直接遍历 `navItems`。后端 RBAC 仍是最终权限边界。
- 导航项权限变更时更新 registry 测试，并测试至少一个替代入口（如命令面板），验证未授权角色不可见。

## 工作台页面外壳

顶层控制台页面用 `PageHero` 承载标题、描述、紧凑元数据和主操作。列表与运维工具区域使用 `DataSurface`、`DataSurfaceHeader`、`DataSurfaceContent`。卡片用于重复条目、对话框、紧凑部件及确需边框的内容，避免整块页面再套卡片形成嵌套外壳。

页面外壳回归验证路由标题、主操作、影响扫描理解的元数据及存在的 DataSurface 标题。URL 控件遵守[参数保留规则](state-management.md#服务器与-url-状态)，并验证不丢失无关参数；手写页签遵守[键盘与页签合同](a11y-guidelines.md#键盘焦点与页签)。

SSH 密钥最小权限字段和风险徽章统一见[凭据与访问合同](../domains/credentials-access.md)。

## 实现边界

- 不为现有 Radix、lucide、Recharts 或本地组件已支持的功能引入替代依赖。
- 用户界面使用有意义的图标和文本，不能只依赖颜色；图标用 `lucide-react`，不重复手写现有图标。
- 条件类组合使用 `cn()`；具体样式、紧凑工作台风格、图表主题和响应式约束见[设计系统](../guides/design-system.md)。
- 不向产品文案加入实现细节、快捷编程说明或设计自述，除非帮助用户完成真实操作。

返回[前端入口](README.md)。
