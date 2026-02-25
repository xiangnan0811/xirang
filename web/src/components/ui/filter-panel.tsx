import type { PropsWithChildren } from "react";
import { cn } from "@/lib/utils";

type FilterPanelProps = PropsWithChildren<{
  className?: string;
  sticky?: boolean;
}>;

export function FilterPanel({ className, sticky = true, children }: FilterPanelProps) {
  return (
    <div className={cn("filter-panel", sticky && "sticky-filter", className)}>
      {children}
    </div>
  );
}

type FilterSummaryProps = {
  filtered: number;
  total: number;
  unit?: string;
  className?: string;
};

export function FilterSummary({
  filtered,
  total,
  unit = "项",
  className,
}: FilterSummaryProps) {
  return (
    <p className={cn("text-xs text-muted-foreground", className)}>
      当前筛选 {filtered} / {total} {unit}
    </p>
  );
}
