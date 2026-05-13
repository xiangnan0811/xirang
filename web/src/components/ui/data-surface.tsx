import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

type DataSurfaceProps = {
  children: ReactNode;
  className?: string;
  variant?: "default" | "flat";
};

type DataSurfaceSectionProps = {
  children: ReactNode;
  className?: string;
};

type DataSurfaceHeaderProps = {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
};

export function DataSurface({ children, className, variant = "default" }: DataSurfaceProps) {
  return (
    <section
      className={cn(
        "overflow-hidden rounded-lg border border-border bg-card",
        variant === "default" ? "shadow-sm" : "shadow-none",
        className
      )}
      data-variant={variant}
    >
      {children}
    </section>
  );
}

export function DataSurfaceHeader({
  title,
  description,
  actions,
  className,
}: DataSurfaceHeaderProps) {
  if (!title && !description && !actions) {
    return null;
  }

  return (
    <div
      className={cn(
        "flex flex-col gap-3 border-b border-border bg-secondary/30 px-4 py-3 md:flex-row md:items-center md:justify-between",
        className
      )}
    >
      <div className="min-w-0">
        {title ? (
          <h2 className="text-sm font-semibold leading-tight text-foreground">
            {title}
          </h2>
        ) : null}
        {description ? (
          <p className="mt-1 text-xs text-muted-foreground">
            {description}
          </p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {actions}
        </div>
      ) : null}
    </div>
  );
}

export function DataSurfaceToolbar({ children, className }: DataSurfaceSectionProps) {
  return (
    <div className={cn("border-b border-border bg-background/55 px-4 py-3", className)}>
      {children}
    </div>
  );
}

export function DataSurfaceContent({ children, className }: DataSurfaceSectionProps) {
  return (
    <div className={cn("p-4", className)}>
      {children}
    </div>
  );
}

export function DataSurfaceFooter({ children, className }: DataSurfaceSectionProps) {
  return (
    <div className={cn("border-t border-border bg-background/55 px-4 py-3", className)}>
      {children}
    </div>
  );
}
