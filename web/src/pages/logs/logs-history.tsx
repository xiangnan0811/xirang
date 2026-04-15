import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";

export interface LogsHistoryProps {
  historyCount: number;
  historyCursor: number | null;
  historyPaging: boolean;
  onLoadMore: () => void;
}

export function LogsHistory({
  historyCount,
  historyCursor,
  historyPaging,
  onLoadMore,
}: LogsHistoryProps) {
  const { t } = useTranslation();

  return (
    <div className="flex items-center justify-between gap-2 rounded-xl border border-border bg-background px-3 py-2">
      <p className="text-xs text-muted-foreground">
        {t("logs.historyCount", { count: historyCount })}
        {historyCursor
          ? t("logs.earliestCursor", { cursor: historyCursor })
          : t("logs.reachedEarliest")}
      </p>
      <Button
        variant="outline"
        size="sm"
        disabled={!historyCursor || historyPaging}
        onClick={onLoadMore}
      >
        {historyPaging ? t("logs.loadingOlder") : t("logs.loadOlderLogs")}
      </Button>
    </div>
  );
}
