import { useTranslation } from "react-i18next";
import { Play, Terminal, X } from "lucide-react";
import { Button } from "@/components/ui/button";

export type TasksBulkBarProps = {
  selectedCount: number;
  onBatchExecute: () => void;
  onBatchTrigger: () => void;
  onClearSelection: () => void;
};

export function TasksBulkBar({
  selectedCount,
  onBatchExecute,
  onBatchTrigger,
  onClearSelection,
}: TasksBulkBarProps) {
  const { t } = useTranslation();

  if (selectedCount === 0) return null;

  return (
    <div className="flex items-center gap-2 rounded-lg border border-primary/30 bg-primary/5 px-4 py-2.5 animate-fade-in">
      <span className="text-sm font-medium text-primary">
        {t("tasks.selectedCount", { count: selectedCount })}
      </span>
      <div className="ml-2 flex items-center gap-1.5">
        <Button
          size="sm"
          variant="outline"
          onClick={onBatchExecute}
        >
          <Terminal className="mr-1 size-3.5" aria-hidden="true" />
          {t("tasks.batchExecuteCount", { count: selectedCount })}
        </Button>
        <Button
          size="sm"
          variant="outline"
          onClick={onBatchTrigger}
        >
          <Play className="mr-1 size-3.5" aria-hidden="true" />
          {t("tasks.triggerCount", { count: selectedCount })}
        </Button>
      </div>
      <Button
        size="sm"
        variant="ghost"
        className="ml-auto text-muted-foreground"
        onClick={onClearSelection}
        aria-label={t("tasks.clearSelection")}
      >
        <X className="mr-1 size-3.5" aria-hidden="true" />
        {t("tasks.clearSelection")}
      </Button>
    </div>
  );
}
