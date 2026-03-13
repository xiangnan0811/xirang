import { useCallback, useEffect, useRef, useState } from "react";
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Circle,
  Loader2,
  XCircle,
} from "lucide-react";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { apiClient } from "@/lib/api/client";
import type { BatchStatus } from "@/lib/api/batch-api";
import type { LogEvent } from "@/types/domain";

type BatchResultDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  batchId: string | null;
  retain: boolean;
  token: string;
};

const statusIcon: Record<string, React.ReactNode> = {
  success: <CheckCircle2 className="size-4 shrink-0 text-emerald-500" />,
  failed: <XCircle className="size-4 shrink-0 text-destructive" />,
  running: <Loader2 className="size-4 shrink-0 animate-spin text-amber-500" />,
  pending: <Circle className="size-4 shrink-0 text-muted-foreground" />,
};

const statusLabel: Record<string, string> = {
  success: "成功",
  failed: "失败",
  running: "执行中",
  pending: "等待中",
  canceled: "已取消",
};

export function BatchResultDialog({
  open,
  onOpenChange,
  batchId,
  retain,
  token,
}: BatchResultDialogProps) {
  const [status, setStatus] = useState<BatchStatus | null>(null);
  const [error, setError] = useState("");
  const [expandedTaskId, setExpandedTaskId] = useState<number | null>(null);
  const [taskLogs, setTaskLogs] = useState<Record<number, LogEvent[]>>({});
  const [loadingLogs, setLoadingLogs] = useState<number | null>(null);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const taskLogsRef = useRef<Record<number, LogEvent[]>>({});

  const stopPolling = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  const fetchStatus = useCallback(async () => {
    if (!batchId) return;
    try {
      const result = await apiClient.getBatchStatus(token, batchId);
      setStatus(result);
      setError("");

      const hasActive = result.tasks.some(
        (t) => t.status === "running" || t.status === "pending"
      );
      if (!hasActive) {
        stopPolling();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "获取批次状态失败");
    }
  }, [batchId, token, stopPolling]);

  // 打开时开始轮询
  useEffect(() => {
    if (!open || !batchId) {
      setStatus(null);
      setError("");
      setExpandedTaskId(null);
      setTaskLogs({});
      taskLogsRef.current = {};
      stopPolling();
      return;
    }

    void fetchStatus();
    intervalRef.current = setInterval(() => {
      void fetchStatus();
    }, 3_000);

    return stopPolling;
  }, [open, batchId, fetchStatus, stopPolling]);

  // 加载单个任务的日志输出
  const loadTaskLogs = useCallback(
    async (taskId: number) => {
      if (taskLogsRef.current[taskId]) return; // 已加载
      setLoadingLogs(taskId);
      try {
        const logs = await apiClient.getTaskLogs(token, taskId, { limit: 50 });
        taskLogsRef.current[taskId] = logs;
        setTaskLogs((prev) => ({ ...prev, [taskId]: logs }));
      } catch {
        const fallback: LogEvent[] = [{ id: "0", level: "error", message: "加载日志失败", timestamp: "" }];
        taskLogsRef.current[taskId] = fallback;
        setTaskLogs((prev) => ({ ...prev, [taskId]: fallback }));
      } finally {
        setLoadingLogs(null);
      }
    },
    [token]
  );

  const handleToggleExpand = useCallback(
    (taskId: number, taskStatus: string) => {
      if (expandedTaskId === taskId) {
        setExpandedTaskId(null);
        return;
      }
      setExpandedTaskId(taskId);
      // 仅在任务已完成时加载日志
      if (taskStatus !== "running" && taskStatus !== "pending") {
        void loadTaskLogs(taskId);
      }
    },
    [expandedTaskId, loadTaskLogs]
  );

  // 关闭时清理
  const handleClose = useCallback(
    (nextOpen: boolean) => {
      if (!nextOpen && batchId && !retain) {
        // 后台清理，不阻塞关闭
        void apiClient.deleteBatch(token, batchId).catch(() => {});
      }
      onOpenChange(nextOpen);
    },
    [batchId, retain, token, onOpenChange]
  );

  const allDone = status
    ? status.tasks.every((t) => t.status !== "running" && t.status !== "pending")
    : false;
  const successCount = status?.statusCounts["success"] ?? 0;
  const failedCount = status?.statusCounts["failed"] ?? 0;

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>批量执行结果</DialogTitle>
          <DialogDescription>
            批次 {batchId}
            {status && (
              <span className="ml-2">
                — {allDone ? "已完成" : "执行中"}
                {status.total > 0 && (
                  <span className="ml-1 text-xs">
                    ({successCount} 成功
                    {failedCount > 0 && (
                      <span className="text-destructive">，{failedCount} 失败</span>
                    )}
                    ，共 {status.total})
                  </span>
                )}
              </span>
            )}
            {!retain && (
              <span className="ml-2 text-xs text-muted-foreground">
                · 关闭后自动清理任务记录
              </span>
            )}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>
        <DialogBody>
          {error && (
            <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {error}
            </div>
          )}

          {!status && !error && (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="size-5 animate-spin text-muted-foreground" />
              <span className="ml-2 text-sm text-muted-foreground">加载中...</span>
            </div>
          )}

          {status && (
            <div className="max-h-96 space-y-1.5 overflow-y-auto">
              {status.tasks.map((task) => {
                const isExpanded = expandedTaskId === task.id;
                const isDone = task.status !== "running" && task.status !== "pending";
                const logs = taskLogs[task.id];

                return (
                  <div key={task.id} className="rounded-md border border-border">
                    <button
                      type="button"
                      className="flex w-full items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 transition-colors"
                      onClick={() => handleToggleExpand(task.id, task.status)}
                      disabled={!isDone}
                    >
                      {statusIcon[task.status] ?? (
                        <Circle className="size-4 shrink-0 text-muted-foreground" />
                      )}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium truncate">
                            {task.nodeName}
                          </span>
                          <span className="shrink-0 text-xs text-muted-foreground">
                            {statusLabel[task.status] ?? task.status}
                          </span>
                        </div>
                        {task.lastError && !isExpanded && (
                          <p
                            className="mt-0.5 text-xs text-destructive truncate"
                            title={task.lastError}
                          >
                            {task.lastError}
                          </p>
                        )}
                      </div>
                      {isDone && (
                        <span className="shrink-0 text-muted-foreground">
                          {isExpanded ? (
                            <ChevronDown className="size-4" />
                          ) : (
                            <ChevronRight className="size-4" />
                          )}
                        </span>
                      )}
                    </button>

                    {isExpanded && isDone && (
                      <div className="border-t border-border bg-muted/30 px-3 py-2">
                        {loadingLogs === task.id && (
                          <div className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
                            <Loader2 className="size-3 animate-spin" />
                            加载日志...
                          </div>
                        )}
                        {logs && logs.length === 0 && (
                          <p className="text-xs text-muted-foreground">无日志输出</p>
                        )}
                        {logs && logs.length > 0 && (
                          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">
                            {logs.map((log) => log.message).join("\n")}
                          </pre>
                        )}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  );
}
