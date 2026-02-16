import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";

interface LoadingStateProps {
  title?: string;
  description?: string;
  rows?: number;
  className?: string;
}

function LoadingState({
  title = "数据加载中...",
  description,
  rows = 3,
  className,
}: LoadingStateProps) {
  const skeletonRows = Array.from({ length: Math.max(1, rows) }, (_, index) => ({
    id: index,
    width: `${Math.max(44, 92 - index * 13)}%`,
  }));

  return (
    <div
      className={cn(
        "rounded-2xl border border-border/70 bg-background/45 px-4 py-4 shadow-sm backdrop-blur-sm animate-fade-in",
        className
      )}
    >
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin text-primary" />
        <span className="font-medium text-foreground">{title}</span>
      </div>

      {description ? (
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
          {description}
        </p>
      ) : null}

      <div className="mt-3 space-y-2">
        {skeletonRows.map((row) => (
          <div
            key={row.id}
            className="h-2.5 rounded-full bg-muted/75 animate-pulse"
            style={{ width: row.width }}
          />
        ))}
      </div>
    </div>
  );
}

export { LoadingState };
export type { LoadingStateProps };
