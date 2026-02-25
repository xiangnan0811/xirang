import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";

type FilteredEmptyStateProps = {
  title?: string;
  description?: string;
  className?: string;
  icon?: LucideIcon;
  onReset?: () => void;
  resetLabel?: string;
  onCreate?: () => void;
  createLabel?: string;
  createIcon?: LucideIcon;
  action?: ReactNode;
};

export function FilteredEmptyState({
  title = "当前筛选条件下暂无结果",
  description = "可调整筛选条件后重试。",
  className,
  icon,
  onReset,
  resetLabel = "重置筛选",
  onCreate,
  createLabel,
  createIcon: CreateIcon,
  action,
}: FilteredEmptyStateProps) {
  const builtInAction =
    action ??
    (onReset || onCreate ? (
      <div className="flex flex-wrap items-center justify-center gap-2">
        {onReset ? (
          <Button size="sm" variant="outline" onClick={onReset}>
            {resetLabel}
          </Button>
        ) : null}
        {onCreate ? (
          <Button size="sm" onClick={onCreate}>
            {CreateIcon ? <CreateIcon className="mr-1 size-4" /> : null}
            {createLabel ?? "新建"}
          </Button>
        ) : null}
      </div>
    ) : undefined);

  return (
    <EmptyState
      icon={icon}
      className={className}
      title={title}
      description={description}
      action={builtInAction}
    />
  );
}

export type { FilteredEmptyStateProps };
