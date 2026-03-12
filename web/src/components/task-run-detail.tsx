import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, Clock, Play, RotateCcw, Timer } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { apiClient } from "@/lib/api/client";
import { getTaskStatusMeta } from "@/lib/status";
import type { LogEvent, TaskRunRecord } from "@/types/domain";

function getTriggerLabel(type: TaskRunRecord["triggerType"]) {
  switch (type) {
    case "cron":
      return "定时触发";
    case "retry":
      return "自动重试";
    case "restore":
      return "备份恢复";
    default:
      return "手动触发";
  }
}

function getTriggerIcon(type: TaskRunRecord["triggerType"]) {
  switch (type) {
    case "cron":
      return <Clock className="size-4" />;
    case "retry":
      return <RotateCcw className="size-4" />;
    case "restore":
      return <Timer className="size-4" />;
    default:
      return <Play className="size-4" />;
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

function logLevelClass(level: LogEvent["level"]) {
  switch (level) {
    case "error":
      return "text-destructive";
    case "warn":
      return "text-warning";
    default:
      return "text-muted-foreground";
  }
}

type Props = {
  run: TaskRunRecord;
  token: string;
  onBack: () => void;
};

export function TaskRunDetail({ run, token, onBack }: Props) {
  const [logs, setLogs] = useState<LogEvent[]>([]);
  const [logsLoading, setLogsLoading] = useState(true);
  const [logsError, setLogsError] = useState<string | null>(null);

  const fetchLogs = useCallback(async () => {
    setLogsLoading(true);
    setLogsError(null);
    try {
      const result = await apiClient.getTaskRunLogs(token, run.id, { limit: 500 });
      setLogs(result);
    } catch (err) {
      setLogsError(err instanceof Error ? err.message : "加载日志失败");
    } finally {
      setLogsLoading(false);
    }
  }, [token, run.id]);

  useEffect(() => {
    void fetchLogs();
  }, [fetchLogs]);

  const statusMeta = getTaskStatusMeta(run.status);

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Button variant="ghost" size="icon" className="size-7" onClick={onBack} aria-label="返回执行历史">
          <ArrowLeft className="size-4" />
        </Button>
        <span className="text-sm font-medium">执行记录 #{run.id}</span>
      </div>

      <div className="rounded-lg border border-border/60 bg-card/50 p-4 space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex items-center gap-1.5 text-sm">
            {getTriggerIcon(run.triggerType)}
            {getTriggerLabel(run.triggerType)}
          </span>
          <Badge variant={statusMeta.variant}>{statusMeta.label}</Badge>
          {run.verifyStatus !== "none" && (
            <Badge variant={run.verifyStatus === "passed" ? "success" : "warning"}>
              {run.verifyStatus === "passed" ? "校验通过" : run.verifyStatus === "warning" ? "校验异常" : "校验失败"}
            </Badge>
          )}
        </div>

        <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
          <div>
            <span className="text-muted-foreground">创建时间</span>
            <p>{run.createdAt}</p>
          </div>
          <div>
            <span className="text-muted-foreground">开始时间</span>
            <p>{run.startedAt}</p>
          </div>
          <div>
            <span className="text-muted-foreground">结束时间</span>
            <p>{run.finishedAt}</p>
          </div>
          <div>
            <span className="text-muted-foreground">执行耗时</span>
            <p>{run.durationMs > 0 ? formatDuration(run.durationMs) : "-"}</p>
          </div>
          {run.throughputMbps > 0 && (
            <div>
              <span className="text-muted-foreground">平均速率</span>
              <p>{run.throughputMbps.toFixed(2)} MB/s</p>
            </div>
          )}
        </div>

        {run.lastError && (
          <div className="rounded border border-destructive/30 bg-destructive/5 p-2">
            <p className="text-xs font-medium text-destructive">错误信息</p>
            <p className="mt-1 text-sm text-destructive/90 break-all">{run.lastError}</p>
          </div>
        )}
      </div>

      <div>
        <h4 className="mb-2 text-sm font-medium">执行日志</h4>
        {logsLoading ? (
          <LoadingState title="加载日志" description="正在获取执行日志..." rows={3} />
        ) : logsError ? (
          <div className="py-4 text-center text-sm text-muted-foreground">
            <p>{logsError}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={() => void fetchLogs()}>
              重试
            </Button>
          </div>
        ) : logs.length === 0 ? (
          <div className="py-4 text-center text-sm text-muted-foreground">暂无日志</div>
        ) : (
          <div className="max-h-80 overflow-y-auto rounded border border-border/60 bg-card/30 p-2 thin-scrollbar">
            {logs.map((log) => (
              <div key={log.id} className="flex gap-2 py-0.5 text-xs font-mono">
                <span className="shrink-0 text-muted-foreground/60">{log.timestamp}</span>
                <span className={logLevelClass(log.level)}>{log.message}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
