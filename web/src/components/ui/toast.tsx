import { Toaster as SonnerToaster, toast } from "sonner";

function Toaster() {
  return (
    <SonnerToaster
      position="top-right"
      offset={16}
      expand
      richColors
      closeButton
      visibleToasts={5}
      toastOptions={{
        duration: 4200,
        classNames: {
          toast:
            "group rounded-xl border border-border/75 bg-background/85 text-foreground shadow-panel backdrop-blur-xl",
          title: "text-sm font-semibold tracking-wide",
          description: "text-xs text-muted-foreground",
          actionButton: "bg-primary text-primary-foreground",
          cancelButton: "bg-muted text-muted-foreground",
          error: "border-destructive/45 text-destructive",
          success: "border-success/45 text-success",
          warning: "border-warning/45 text-warning",
          info: "border-info/45 text-info",
        },
      }}
    />
  );
}

export { Toaster, toast };
