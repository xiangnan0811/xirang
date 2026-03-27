import * as React from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

const Dialog = DialogPrimitive.Root;
const DialogTrigger = DialogPrimitive.Trigger;
const DialogClose = DialogPrimitive.Close;
const DialogPortal = DialogPrimitive.Portal;

const DialogOverlay = React.forwardRef<
  React.ComponentRef<typeof DialogPrimitive.Overlay>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Overlay>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Overlay
    ref={ref}
    className={cn(
      "fixed inset-0 z-50 bg-background/80 backdrop-blur-sm data-[state=open]:animate-fade-in",
      className
    )}
    {...props}
  />
));
DialogOverlay.displayName = DialogPrimitive.Overlay.displayName;

const DialogContent = React.forwardRef<
  React.ComponentRef<typeof DialogPrimitive.Content>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Content> & {
    size?: "sm" | "md" | "lg";
  }
>(({ className, children, size = "md", ...props }, ref) => (
  <DialogPortal>
    <DialogOverlay />
    {/* 用 flexbox 居中，避免 translate 与 animation transform 冲突 */}
    <div className="fixed inset-0 z-50 flex items-end md:items-center md:justify-center">
      <DialogPrimitive.Content
        ref={ref}
        className={cn(
          // 移动端：底部抽屉，从底部滑入
          "relative w-full max-h-[92vh] rounded-t-2xl border-t border-border/60 max-md:bg-background/90 max-md:backdrop-blur-xl md:border md:border-border/60 md:bg-background/50 md:backdrop-blur-md shadow-mobile-sheet will-change-transform",
          "data-[state=open]:animate-slide-up data-[state=closed]:animate-none",
          // 桌面端：居中弹窗，缩放淡入
          "md:max-h-[85vh] md:rounded-xl md:shadow-panel md:data-[state=open]:animate-animate-in",
          size === "sm" && "md:max-w-[480px]",
          size === "md" && "md:max-w-[560px]",
          size === "lg" && "md:max-w-[640px]",
          className
        )}
        {...props}
      >
        {/* 移动端顶部拖拽指示条 */}
        <div className="flex justify-center pt-2 md:hidden">
          <div className="h-1 w-10 rounded-full bg-muted-foreground/30" />
        </div>
        {children}
      </DialogPrimitive.Content>
    </div>
  </DialogPortal>
));
DialogContent.displayName = DialogPrimitive.Content.displayName;

function DialogHeader({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("space-y-1.5 border-b border-border/40 px-6 pb-4 pt-4 relative z-10", className)}
      {...props}
    />
  );
}

function DialogBody({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "max-h-[calc(85vh-8rem)] overflow-y-auto px-6 py-4 thin-scrollbar relative z-10",
        className
      )}
      {...props}
    />
  );
}

function DialogFooter({
  className,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn(
        "flex justify-end gap-2 border-t px-6 pb-6 pt-4",
        className
      )}
      {...props}
    />
  );
}

const DialogTitle = React.forwardRef<
  React.ComponentRef<typeof DialogPrimitive.Title>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Title>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Title
    ref={ref}
    className={cn("text-lg font-semibold", className)}
    {...props}
  />
));
DialogTitle.displayName = DialogPrimitive.Title.displayName;

const DialogDescription = React.forwardRef<
  React.ComponentRef<typeof DialogPrimitive.Description>,
  React.ComponentPropsWithoutRef<typeof DialogPrimitive.Description>
>(({ className, ...props }, ref) => (
  <DialogPrimitive.Description
    ref={ref}
    className={cn("text-sm text-muted-foreground", className)}
    {...props}
  />
));
DialogDescription.displayName = DialogPrimitive.Description.displayName;

function DialogCloseButton({ className }: React.HTMLAttributes<HTMLButtonElement>) {
  const { t } = useTranslation();
  return (
    <DialogPrimitive.Close className={cn("absolute right-4 top-4 rounded-sm opacity-70 ring-offset-background transition-opacity hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none", className)}>
      <X className="size-4" />
      <span className="sr-only">{t('common.close')}</span>
    </DialogPrimitive.Close>
  );
}

export {
  Dialog,
  DialogPortal,
  DialogOverlay,
  DialogClose,
  DialogTrigger,
  DialogContent,
  DialogHeader,
  DialogBody,
  DialogFooter,
  DialogTitle,
  DialogDescription,
  DialogCloseButton,
};
