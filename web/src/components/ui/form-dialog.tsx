import { useLayoutEffect, useRef, type ComponentProps, type FormEvent, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

export type DialogCloseAutoFocus = NonNullable<ComponentProps<typeof DialogContent>["onCloseAutoFocus"]>;

type FormDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description: ReactNode;
  icon?: ReactNode;
  size?: "sm" | "md" | "lg";
  saving: boolean;
  /** 保存进行中禁止关闭（关闭按钮、Esc、外部点击、取消），避免过期的提交结果落到下一次打开。 */
  lockWhileSaving?: boolean;
  submitDisabled?: boolean;
  onSubmit: () => void | Promise<void>;
  submitLabel: ReactNode;
  savingLabel?: ReactNode;
  extraFooter?: ReactNode;
  onCloseAutoFocus?: DialogCloseAutoFocus;
  children: ReactNode;
};

function canHoldFocus(element: Element | null, container: HTMLElement): element is HTMLElement {
  return element instanceof HTMLElement && container.contains(element) && !element.matches(":disabled");
}

export function FormDialog({
  open,
  onOpenChange,
  title,
  description,
  icon,
  size = "md",
  saving,
  lockWhileSaving = false,
  submitDisabled = false,
  onSubmit,
  submitLabel,
  savingLabel,
  extraFooter,
  onCloseAutoFocus,
  children,
}: FormDialogProps) {
  const { t } = useTranslation();
  const resolvedSavingLabel = savingLabel ?? t("common.saving");
  const dismissLocked = lockWhileSaving && saving;
  const contentRef = useRef<HTMLDivElement>(null);
  const submitFocusRef = useRef<HTMLElement | null>(null);
  const previousStateRef = useRef({ open, saving });

  // 保存期间字段与按钮被禁用，焦点会掉到 body；把它留在对话框内，结束后还给提交前的控件。
  // 只处理打开期间 saving 的切换：打开瞬间交给 Radix 的初始聚焦与关闭后的焦点归还。
  useLayoutEffect(() => {
    const previousState = previousStateRef.current;
    previousStateRef.current = { open, saving };
    const content = contentRef.current;
    if (!open || !previousState.open || previousState.saving === saving || !content) return;
    const active = document.activeElement;
    if (saving) {
      if (!canHoldFocus(active, content)) {
        content.focus({ preventScroll: true });
      }
      return;
    }
    const previous = submitFocusRef.current;
    submitFocusRef.current = null;
    if (canHoldFocus(previous, content) && (active === content || !canHoldFocus(active, content))) {
      previous.focus({ preventScroll: true });
    }
  }, [open, saving]);

  const handleOpenChange = (next: boolean) => {
    if (!next && dismissLocked) return;
    onOpenChange(next);
  };

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (saving || submitDisabled) return;
    const active = document.activeElement;
    submitFocusRef.current = active instanceof HTMLElement ? active : null;
    void onSubmit();
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {/* 内容区保留 overflow-y-auto 作为兜底：头部有上限、正文有最小高度，极矮视口下整体滚动而不裁掉页脚。 */}
      <DialogContent
        ref={contentRef}
        size={size}
        className="flex flex-col"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <DialogHeader className="max-h-[30dvh] shrink-0 overflow-y-auto thin-scrollbar pr-10">
          {icon ? (
            <div className="flex min-w-0 items-center gap-2">
              <span className="shrink-0">{icon}</span>
              <DialogTitle className="min-w-0 [overflow-wrap:anywhere]">{title}</DialogTitle>
            </div>
          ) : (
            <DialogTitle className="[overflow-wrap:anywhere]">{title}</DialogTitle>
          )}
          <DialogDescription className="[overflow-wrap:anywhere]">{description}</DialogDescription>
          <DialogCloseButton disabled={dismissLocked} />
        </DialogHeader>

        <form noValidate onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogBody className="min-h-24 flex-1 overflow-y-auto thin-scrollbar space-y-3">
            {children}
          </DialogBody>

          <DialogFooter className="shrink-0 flex-wrap">
            <Button type="button" variant="outline" disabled={dismissLocked} onClick={() => handleOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            {extraFooter}
            <Button type="submit" disabled={saving || submitDisabled}>
              {saving ? resolvedSavingLabel : submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
