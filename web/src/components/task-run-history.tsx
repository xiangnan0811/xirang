import { useCallback, useEffect, useState } from "react";
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

function getTriggerLabel(type: TaskRunRecord["triggerType"]) {
  switch (type) {
    case "cron":
      return "定时";
    case "retry":
      return "重试";
    case "restore":
      return "恢复";
    default:
      return "手动";
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
  const [runs, setRuns] = useState<TaskRunRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchRuns = useCallback(async (currentOffset: number) => {
    setLoading(true);
    setError(null);
    try {
      const result = await apiClient.getTaskRuns(token, taskId, {
        limit: PAGE_SIZE,
        offset: currentOffset,
      });
      setRuns(result.items);
      setTotal(result.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : "加载执行历史失败");
    } finally {
      setLoading(false);
    }
  }, [token, taskId]);

  useEffect(() => {
    void fetchRuns(offset);
  }, [fetchRuns, offset]);

  const totalPages = Math.ceil(total / PAGE_SIZE);
  const currentPage = Math.floor(offset / PAGE_SIZE) + 1;

  if (loading && runs.length === 0) {
    return <LoadingState title="加载执行历史" description="正在获取执行记录..." rows={3} />;
  }

  if (error) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        <p>{error}</p>
        <Button variant="outline" size="sm" className="mt-2" onClick={() => void fetchRuns(offset)}>
          重试
        </Button>
      </div>
    );
  }

  if (!runs.length) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        暂无执行记录
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
                    {getTriggerLabel(run.triggerType)}
                  </span>
                  <Badge variant={statusMeta.variant} className="text-[10px] px-1.5 py-0">
                    {statusMeta.label}
                  </Badge>
                  {run.verifyStatus !== "none" && (
                    <Badge
                      variant={run.verifyStatus === "passed" ? "success" : "warning"}
                      className="text-[10px] px-1.5 py-0"
                    >
                      {run.verifyStatus === "passed" ? "校验通过" : run.verifyStatus === "warning" ? "校验异常" : "校验失败"}
                    </Badge>
                  )}
                </div>
                <span className="text-xs text-muted-foreground">#{run.id}</span>
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                <span>开始：{run.startedAt}</span>
                {run.durationMs > 0 && <span>耗时：{formatDuration(run.durationMs)}</span>}
                {run.throughputMbps > 0 && <span>速率：{run.throughputMbps.toFixed(1)} MB/s</span>}
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
            共 {total} 条，第 {currentPage}/{totalPages} 页
          </span>
          <div className="flex items-center gap-1">
            <Button
              variant="outline"
              size="icon"
              className="size-7"
              disabled={offset === 0 || loading}
              onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              aria-label="上一页"
            >
              <ChevronLeft className="size-3.5" />
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="size-7"
              disabled={offset + PAGE_SIZE >= total || loading}
              onClick={() => setOffset(offset + PAGE_SIZE)}
              aria-label="下一页"
            >
              <ChevronRight className="size-3.5" />
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
