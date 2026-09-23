# 可访问性合同

控制台以 WCAG 2.1 AA 为可访问性目标；这表示开发要求，不表示已经取得全站符合性验证。键盘、读屏、对比度与移动界面都需要对应证据，不能用静态检查或 jsdom smoke 替代真实浏览器验证。

## 语言与语义

- 每页恰有一个主 `h1`；`PageHero` 提供主标题时不再另加。标题层级表达内容结构。
- 装饰性 lucide 图标显式 `aria-hidden`。已有可访问名称的纯图标按钮，其内部图标同样属于装饰；不要把“按钮无可见文字”等同于“SVG 必须暴露”。
- 纯图标按钮通过 `aria-label`、`aria-labelledby` 或 `sr-only` 文本取得名称，不能只靠 tooltip。独立承载语义的图形应提供相应可访问名称；不要臆测图标库会自动提供正确读屏名称。
- 每个输入、文本框和选择控件有可关联标签（`label htmlFor`、`aria-label` 或 `aria-labelledby`）；嵌套标签要确实关联其原生输入。
- Radix Dialog 必须有有意义的 `DialogTitle`，可视觉隐藏；通常同时提供 `DialogDescription`。确实不需要描述时显式按组件 API 取消描述关联，不能留下指向不存在元素的引用。`FormDialog` 必须有有意义的 description，提交按钮 `type="submit"`，取消按钮 `type="button"`。
- 页面语言随 i18n 更新：中文为 `zh-CN`，英文为 `en`。启动等待 `i18nReady`，显式切换调用 `setLanguage()`，不从组件直接调用 `i18n.changeLanguage()`。这是页面语言规则；混合语言片段需要自身恰当的语言语义，不能仅靠根属性宣称全部覆盖。
- 错误、警告与状态使用现有 alert/inline alert 或适当 live status；关键信息同时提供文字，不只靠颜色。

## 键盘焦点与页签

所有交互保持可见的 `focus-visible` 指示，不在业务代码中覆盖去除。对话框、表格、分页、移动导航和命令面板保留键盘工作流。点击操作优先用原生 button；若无法使用，必须补齐角色、Tab 可达性和等价键盘处理。

手写页签必须一起提供 `tablist`、`tab`、`tabpanel`、`aria-controls`、`aria-selected` 和正确 `tabIndex`，支持方向键导航。每个 `aria-controls` 指向已挂载面板；若只挂载活动内容，非活动面板仍保留隐藏空壳作为关联目标。回归覆盖键盘、选择状态和目标存在性。

Catalog 的 44 像素上一级按钮、面包屑、空目录导航、移动端/缩放和精确焦点恢复见[Catalog 合同](../domains/backup-catalog.md)。

## 颜色与真实布局

普通文字对比度至少 4.5:1，大字（18pt 或 14pt 粗体）至少 3:1。新增 token 或弱化文字样式需在真实浏览器检查；jsdom 的 axe 不提供可靠对比度证据。响应式布局与减少动效同时遵守[设计系统](../guides/design-system.md)。

## 工具与回归

| 工具 | 当前作用和限制 |
|---|---|
| `eslint-plugin-jsx-a11y` | `web/eslint.config.js` 对源码使用 recommended；`aria-role`、`no-redundant-roles`、`anchor-is-valid`、`no-autofocus` 为 error |
| 历史债务 warn | 仅 `label-has-associated-control`、`no-noninteractive-tabindex`、`click-events-have-key-events`、`no-static-element-interactions` 四项；不表示对应障碍可以新增 |
| `vitest-axe` 和 `axe-core` | 都列在开发依赖；`web/vitest.setup.ts` 提供 `toHaveNoViolations()` |
| `runAxe` | `web/src/test/a11y-helpers.ts` 共用包装，因 jsdom canvas 限制禁用 `color-contrast` |

```tsx
import { render } from "@testing-library/react";
import { expect, it } from "vitest";
import { runAxe } from "@/test/a11y-helpers";

it("默认界面没有 axe 违规", async () => {
  const { container } = render(<MyComponent />);
  expect(await runAxe(container)).toHaveNoViolations();
});
```

Radix Portal 位于 `document.body` 时扫描 body，不能只扫描 render container。依赖 Context 的页面 smoke 可 mock Context 并提供最小领域数据，避免真实 API/WebSocket；仍需单独覆盖交互、错误和焦点状态，首次渲染无违规不等于完整验收。

## 已知限制与回退

- 仪表盘拖拽有独立键盘回退：`pages/dashboards/panel-card.tsx` 在编辑模式显示“上移/下移”按钮。保留并测试它们；不能把鼠标拖拽当作完整键盘支持。
- `components/web-terminal.tsx` 当前为终端外层提供名称，但初始化未启用 `screenReaderMode`。终端缓冲区读屏可用性尚无本合同认可的验收证据；不能因使用 xterm/canvas 就声称天然豁免 WCAG。
- jsdom 不检查对比度；应以真实浏览器检查补充。新增豁免必须说明具体限制、影响和回退，不能通过删要求掩盖实现缺口。
- 当前 smoke 不构成全站 E2E、移动触控或读屏兼容性证明；触及这些行为时按受影响场景补充实际验证。

返回[前端入口](README.md)。
