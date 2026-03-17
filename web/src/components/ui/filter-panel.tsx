import type { PropsWithChildren } from "react";
import { useTranslation } from "react-i18next";
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
  unit,
  className,
}: FilterSummaryProps) {
  const { t } = useTranslation();
  return (
    <p className={cn("text-xs text-muted-foreground", className)}>
      {t('common.filterSummary', { filtered, total, unit: unit ?? t('common.unit') })}
    </p>
  );
}
