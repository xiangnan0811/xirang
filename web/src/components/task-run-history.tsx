import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Clock, Play, RotateCcw, Timer, ChevronLeft, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { TaskRunRecord } from "@/types/domain";

const PAGE_SIZE = 10;

function getTriggerIcon(type: TaskRunRecord["triggerType"]) {
  switch (type) {
    case "cron":
      return <Clock className="size-3.5" />;
    case "retry":
      return <RotateCcw className="size-3.5" />;
    case "restore":
      return <Timer className="size-3.5" />;
    default:
      return <Play className="size-3.5" />;
  }
}

function getTriggerLabelKey(type: TaskRunRecord["triggerType"]): string {
  switch (type) {
    case "cron":
    case "retry":
    case "restore":
      return `tasks.triggerType.${type}`;
    default:
      return "tasks.triggerType.manual";
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainSec = seconds % 60;
  if (minutes < 60) return `${minutes}m${remainSec}s`;
  const hours = Math.floor(minutes / 60);
  const remainMin = minutes % 60;
  return `${hours}h${remainMin}m`;
}

type Props = {
  taskId: number;
  token: string;
  onSelectRun?: (run: TaskRunRecord) => void;
};

export function TaskRunHistory({ taskId, token, onSelectRun }: Props) {
  const { t } = useTranslation();
  const [runs, setRuns] = useState<TaskRunRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchRuns = useCallback(async (currentPage: number) => {
    setLoading(true);
    setError(null);
    try {
      const result = await apiClient.getTaskRuns(token, taskId, {
        pageSize: PAGE_SIZE,
        page: currentPage,
      });
      setRuns(result.items);
      setTotal(result.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('taskRunHistory.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [token, taskId]);

  useEffect(() => {
    void fetchRuns(page);
  }, [fetchRuns, page]);

  const totalPages = Math.ceil(total / PAGE_SIZE);
  const currentPage = page;

  if (loading && runs.length === 0) {
    return <LoadingState title={t('taskRunHistory.loadingTitle')} description={t('taskRunHistory.loadingDesc')} rows={3} />;
  }

  if (error) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        <p>{error}</p>
        <Button variant="outline" size="sm" className="mt-2" onClick={() => void fetchRuns(page)}>
          {t('common.retry')}
        </Button>
      </div>
    );
  }

  if (!runs.length) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        {t('taskRunHistory.noRecords')}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="space-y-2">
        {runs.map((run) => {
          const statusMeta = getTaskStatusMeta(run.status);
          return (
            <button
              key={run.id}
              type="button"
              className={cn(
                "w-full rounded-lg border border-border/60 bg-card/50 p-3 text-left transition-colors hover:bg-accent/50",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              )}
              onClick={() => onSelectRun?.(run)}
            >
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2">
                  <span className="flex items-center gap-1 text-xs text-muted-foreground">
                    {getTriggerIcon(run.triggerType)}
                    {t(getTriggerLabelKey(run.triggerType))}
                  </span>
                  <Badge variant={statusMeta.variant} className="text-[10px] px-1.5 py-0">
                    {statusMeta.label}
                  </Badge>
                  {run.verifyStatus !== "none" && (
                    <Badge
                      variant={run.verifyStatus === "passed" ? "success" : "warning"}
                      className="text-[10px] px-1.5 py-0"
                    >
                      {run.verifyStatus === "passed" ? t('taskRunHistory.verifyPassed') : run.verifyStatus === "warning" ? t('taskRunHistory.verifyWarning') : t('taskRunHistory.verifyFailed')}
                    </Badge>
                  )}
                </div>
                <span className="text-xs text-muted-foreground">#{run.id}</span>
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                <span>{t('taskRunHistory.startedAt', { time: run.startedAt })}</span>
                {run.durationMs > 0 && <span>{t('taskRunHistory.duration', { value: formatDuration(run.durationMs) })}</span>}
                {run.throughputMbps > 0 && <span>{t('taskRunHistory.speed', { value: run.throughputMbps.toFixed(1) })}</span>}
              </div>
              {run.lastError && (
                <p className="mt-1 truncate text-xs text-destructive">{run.lastError}</p>
              )}
            </button>
          );
        })}
      </div>

      {totalPages > 1 && (
        <div className="flex items-center justify-between pt-1">
          <span className="text-xs text-muted-foreground">
            {t('taskRunHistory.pagination', { total, current: currentPage, pages: totalPages })}
          </span>
          <div className="flex items-center gap-1">
            <Button
              variant="outline"
              size="icon"
              className="size-7"
              disabled={page <= 1 || loading}
              onClick={() => setPage(Math.max(1, page - 1))}
              aria-label={t('taskRunHistory.prevPage')}
            >
              <ChevronLeft className="size-3.5" />
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="size-7"
              disabled={page >= totalPages || loading}
              onClick={() => setPage(page + 1)}
              aria-label={t('taskRunHistory.nextPage')}
            >
              <ChevronRight className="size-3.5" />
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
