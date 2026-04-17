import * as React from "react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";

export interface EmptyStateProps
  extends Omit<React.HTMLAttributes<HTMLDivElement>, "title"> {
  icon?: React.ReactNode | LucideIcon;
  title: React.ReactNode;
  description?: React.ReactNode;
  action?: React.ReactNode;
}

export function EmptyState({
  className,
  icon,
  title,
  description,
  action,
  ...props
}: EmptyStateProps) {
  // Handle both LucideIcon (class) and ReactNode (element)
  const resolvedIcon = React.useMemo(() => {
    if (!icon) return null;

    // If icon is a Lucide icon component (function/class), render it with size
    if (typeof icon === "function") {
      const Icon = icon as LucideIcon;
      return <Icon className="size-5" />;
    }

    // Otherwise it's already a ReactNode element
    return icon;
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
      <div className="text-sm font-semibold text-foreground">{title}</div>
      {description ? (
        <div className="mx-auto mt-1 max-w-[260px] text-xs text-muted-foreground">{description}</div>
      ) : null}
      {action ? <div className="mt-4">{action}</div> : null}
    </div>
  );
}
