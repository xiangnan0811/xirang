import type { ElementType, ReactNode } from "react";
import { cn } from "@/lib/utils";

type DataSurfaceProps = {
  children: ReactNode;
  className?: string;
  variant?: "default" | "flat";
  "aria-label"?: string;
  "aria-labelledby"?: string;
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
  /** 控制标题的 heading 级别，默认为 h2 */
  headingLevel?: "h1" | "h2" | "h3" | "h4" | "h5" | "h6";
};

export function DataSurface({ children, className, variant = "default", ...aria }: DataSurfaceProps) {
  return (
    <section
      className={cn(
        "overflow-hidden rounded-lg border border-border bg-card",
        variant === "default" ? "shadow-sm" : "shadow-none",
        className
      )}
      data-variant={variant}
      {...aria}
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
  headingLevel = "h2",
}: DataSurfaceHeaderProps) {
  if (!title && !description && !actions) {
    return null;
  }

  const HeadingTag = headingLevel as ElementType;

  return (
    <div
      className={cn(
        "flex flex-col gap-3 border-b border-border bg-secondary/30 px-4 py-3 md:flex-row md:items-center md:justify-between",
        className
      )}
    >
      <div className="min-w-0">
        {title ? (
          <HeadingTag className="text-sm font-semibold leading-tight text-foreground">
            {title}
          </HeadingTag>
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
