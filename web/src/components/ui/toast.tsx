import { Toaster as SonnerToaster } from "sonner";

export function Toaster() {
  return (
    <div aria-live="polite" aria-atomic="false" role="status">
      <SonnerToaster
        position="bottom-right"
        offset={16}
        expand
        richColors
        closeButton
        visibleToasts={5}
        theme="system"
        toastOptions={{
          duration: 4200,
          classNames: {
            toast:
              "group rounded-lg border border-l-4 border-border shadow-lg",
            title: "text-sm font-semibold tracking-wide",
            description: "text-xs text-muted-foreground",
            actionButton: "bg-primary text-primary-foreground",
            cancelButton: "bg-muted text-muted-foreground",
            default: "border-l-border",
            error: "border-l-destructive",
            success: "border-l-success",
            warning: "border-l-warning",
            info: "border-l-info",
          },
        }}
      />
    </div>
  );
}
