// Wave 4 PR-D: a11y 测试公共辅助。
//
// 抽离 vitest-axe 调用与默认豁免规则，避免 6+ 个 a11y smoke 测试重复同一份
// `{ rules: { "color-contrast": { enabled: false } } }`。新增 a11y 测试时统一
// 通过 `runAxe(container)` 调用即可。
//
// color-contrast 关闭原因（jsdom 限制）：
//   axe-core 的颜色对比度检查依赖 `HTMLCanvasElement.prototype.getContext`，
//   jsdom 默认未实现该 API。开 color-contrast 会输出 stderr 噪音
//   "Not implemented: HTMLCanvasElement.prototype.getContext"，并且对比度计算
//   结果不可靠。我们在浏览器端通过 axe DevTools 手动验证对比度，避免在 CI
//   产生假阴性 / 假阳性。
import { axe } from "vitest-axe";

/**
 * 在 jsdom 测试环境下跑 axe-core 检查。
 *
 * 默认禁用 `color-contrast` 规则（详见模块顶部注释）；其余 axe 默认规则全开。
 * 调用方应该把整页 / 整个组件容器作为 `target` 传入：
 *
 * ```ts
 * const { container } = render(<MyComponent />);
 * const results = await runAxe(container);
 * expect(results).toHaveNoViolations();
 * ```
 *
 * 若 Radix Dialog/Tooltip 等通过 portal 渲染到 `document.body`，可改为
 * `runAxe(document.body)` 以扫描 portal 内容。
 *
 * @param target axe 扫描根（通常是 `render()` 返回的 `container`，或 `document.body`）
 */
export function runAxe(target: Element | string) {
  return axe(target, {
    rules: {
      "color-contrast": { enabled: false },
    },
  });
}
