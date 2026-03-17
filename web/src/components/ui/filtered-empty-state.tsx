import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
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
  title,
  description,
  className,
  icon,
  onReset,
  resetLabel,
  onCreate,
  createLabel,
  createIcon: CreateIcon,
  action,
}: FilteredEmptyStateProps) {
  const { t } = useTranslation();
  const resolvedTitle = title ?? t('filteredEmpty.defaultTitle');
  const resolvedDescription = description ?? t('filteredEmpty.defaultDescription');
  const resolvedResetLabel = resetLabel ?? t('filteredEmpty.resetFilter');

  const builtInAction =
    action ??
    (onReset || onCreate ? (
      <div className="flex flex-wrap items-center justify-center gap-2">
        {onReset ? (
          <Button size="sm" variant="outline" onClick={onReset}>
            {resolvedResetLabel}
          </Button>
        ) : null}
        {onCreate ? (
          <Button size="sm" onClick={onCreate}>
            {CreateIcon ? <CreateIcon className="mr-1 size-4" /> : null}
            {createLabel ?? t('common.create')}
          </Button>
        ) : null}
      </div>
    ) : undefined);

  return (
    <EmptyState
      icon={icon}
      className={className}
      title={resolvedTitle}
      description={resolvedDescription}
      action={builtInAction}
    />
  );
}

export type { FilteredEmptyStateProps };
