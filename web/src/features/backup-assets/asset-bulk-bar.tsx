import { Eye, X } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

export interface AssetBulkBarProps {
  count: number;
  onClear: () => void;
  onInspect: () => void;
}

export function AssetBulkBar({ count, onClear, onInspect }: AssetBulkBarProps) {
  const { t } = useTranslation();
  if (count === 0) return null;
  return (
    <div
      role="status"
      aria-live="polite"
      className="flex h-11 shrink-0 items-center gap-2 border-t border-border bg-secondary/35 px-3"
    >
      <span className="min-w-0 flex-1 truncate text-xs font-medium">
        {t("backupAssets.selection.count", { count })}
      </span>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        disabled={count !== 1}
        aria-label={t("backupAssets.actions.inspectSelected")}
        onClick={onInspect}
      >
        <Eye className="size-4" aria-hidden />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-label={t("backupAssets.actions.clearSelection")}
        onClick={onClear}
      >
        <X className="size-4" aria-hidden />
      </Button>
    </div>
  );
}
