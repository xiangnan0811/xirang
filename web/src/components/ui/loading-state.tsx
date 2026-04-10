import * as React from "react";
import { Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";

interface LoadingStateProps {
  title?: string;
  description?: string;
  rows?: number;
  className?: string;
}

function LoadingState({
  title,
  description,
  rows = 3,
  className,
}: LoadingStateProps) {
  const { t } = useTranslation();
  const resolvedTitle = title ?? t("common.loading");
  const skeletonRows = Array.from({ length: Math.max(1, rows) }, (_, index) => ({
    id: index,
    width: `${Math.max(44, 92 - index * 13)}%`,
  }));

  return (
    <div
      role="status"
      aria-live="polite"
      className={cn("space-y-3 p-4", className)}
    >
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin text-primary" />
        <span className="font-medium text-foreground">{resolvedTitle}</span>
      </div>

      {description ? (
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          {description}
        </p>
      ) : null}

      <span className="sr-only">{resolvedTitle}</span>

      <div className="space-y-2">
        {skeletonRows.map((row) => (
          <div
            key={row.id}
            className="h-2.5 animate-pulse rounded-md bg-secondary"
            style={{ width: row.width }}
          />
        ))}
      </div>
    </div>
  );
}

export function Skeleton({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("animate-pulse rounded-md bg-secondary", className)} {...props} />;
}

export { LoadingState };
export type { LoadingStateProps };
