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
