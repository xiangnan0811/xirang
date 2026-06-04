import * as React from "react";
import { useMemo } from "react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export interface EmptyStateProps
  extends Omit<React.HTMLAttributes<HTMLDivElement>, "title"> {
  icon?: React.ReactNode | LucideIcon;
  title: React.ReactNode;
  description?: React.ReactNode;
  action?: React.ReactNode;
  /** 标题使用的 HTML 元素，默认 h3 */
  as?: "h2" | "h3" | "h4";
}

export function EmptyState({
  className,
  icon,
  title,
  description,
  action,
  as: Heading = "h3",
  ...props
}: EmptyStateProps) {
  // Handle both LucideIcon (component type, incl. forwardRef) and ReactNode (element)
  const resolvedIcon = useMemo(() => {
    if (!icon) return null;
    if (React.isValidElement(icon)) return icon;
    // Treat anything else callable/forwardRef as a component type
    const Icon = icon as React.ComponentType<{ className?: string }>;
    return <Icon className="size-5" />;
  }, [icon]);

  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center rounded-lg bg-card px-6 py-10 text-center shadow-sm",
        "dark:border dark:border-border",
        className,
      )}
      {...props}
    >
      {resolvedIcon ? (
        <div className="mb-3 flex size-14 items-center justify-center rounded-xl bg-[hsl(var(--accent-brand)/0.18)] text-[hsl(var(--primary))]">
          {resolvedIcon}
        </div>
      ) : null}
      <Heading className="text-sm font-semibold text-foreground">{title}</Heading>
      {description ? (
        <div className="mx-auto mt-1 max-w-[260px] text-xs text-muted-foreground">{description}</div>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  );
}
