import { Toaster as SonnerToaster, toast } from "sonner";

function Toaster() {
  return (
    <SonnerToaster
      position="top-right"
      toastOptions={{
        duration: 4000,
        classNames: {
          toast:
            "group border-border bg-background text-foreground shadow-lg rounded-lg",
          title: "text-sm font-semibold",
          description: "text-sm text-muted-foreground",
          actionButton: "bg-primary text-primary-foreground",
          cancelButton: "bg-muted text-muted-foreground",
          error: "border-destructive/50 text-destructive",
          success: "border-success/50 text-success",
          warning: "border-warning/50 text-warning",
          info: "border-info/50 text-info",
        },
      }}
    />
  );
}

export { Toaster, toast };
