export function dialogOpenerFromTarget(target: EventTarget | null | undefined): HTMLElement | null {
  return target instanceof HTMLElement ? target : null;
}

export function restoreConnectedDialogOpener(
  event: { preventDefault(): void },
  opener: HTMLElement | null,
): void {
  if (!opener?.isConnected) return;
  event.preventDefault();
  opener.focus();
}

/**
 * 回焦目标此刻能否真正接住焦点：仍在文档中、未禁用、不在 inert 子树内，且自身与祖先
 * 未被 display:none / visibility:hidden 隐藏（响应式布局跨断点后旧布局的按钮仍在 DOM 中）。
 * 不支持 checkVisibility 的环境沿用“已连接即可”的语义。
 */
export function canRestoreFocusTo(element: HTMLElement | null | undefined): element is HTMLElement {
  if (!element?.isConnected) return false;
  if (element.matches(":disabled") || element.closest("[inert]")) return false;
  return typeof element.checkVisibility !== "function"
    || element.checkVisibility({ visibilityProperty: true });
}

/**
 * 在 root 内找与 opener 同一 attribute 值的首个可聚焦元素，用于响应式布局里同一实体
 * 在另一布局中的对应按钮。
 */
export function focusablePeerOf(
  opener: HTMLElement | null,
  attribute: string,
  root: ParentNode | null | undefined,
): HTMLElement | null {
  const value = opener?.getAttribute(attribute);
  if (value == null || !root) return null;
  for (const candidate of root.querySelectorAll<HTMLElement>(`[${attribute}]`)) {
    if (candidate.getAttribute(attribute) === value && canRestoreFocusTo(candidate)) {
      return candidate;
    }
  }
  return null;
}

/**
 * opt-in 版本：opener 无法接住焦点（隐藏、禁用或已卸载）时改为聚焦 resolveFallback 给出的
 * 目标；两者都不可用时保留 Radix 默认回焦。
 */
export function restoreDialogOpenerOrFallback(
  event: { preventDefault(): void },
  opener: HTMLElement | null,
  resolveFallback: (opener: HTMLElement | null) => HTMLElement | null,
): void {
  const target = canRestoreFocusTo(opener) ? opener : resolveFallback(opener);
  if (!canRestoreFocusTo(target)) return;
  event.preventDefault();
  target.focus();
}
