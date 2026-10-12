import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Circle,
  Copy,
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
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast-sonner";
import { getAuthIdentitySnapshot, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import type { BatchStatus } from "@/lib/api/batch-api";
import type { LogEvent } from "@/types/domain";

type BatchResultDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  batchId: string | null;
  retain: boolean;
  token: string;
};

type BatchResultLifetime = {
  generation: number;
  sessionGeneration: number;
  token: string;
  role: AuthRole | null;
  open: boolean;
};

const BATCH_LOG_LIMIT = 50;
type TaskLogState =
  | { status: "loading" }
  | { status: "error" }
  | { status: "loaded"; logs: LogEvent[] };

const statusIcon: Record<string, React.ReactNode> = {
  success: <CheckCircle2 className="size-4 shrink-0 text-success" />,
  failed: <XCircle className="size-4 shrink-0 text-destructive" />,
  running: <Loader2 className="size-4 shrink-0 animate-spin text-warning" />,
  pending: <Circle className="size-4 shrink-0 text-muted-foreground" />,
};

const activeBatchTaskStatuses = new Set(["pending", "running", "retrying"]);

function isBatchTaskActive(task: BatchStatus["tasks"][number]) {
  if (task.dispatchStatus === "pending" || task.dispatchStatus === "dispatching") return true;
  return activeBatchTaskStatuses.has(task.status) && !(task.status === "pending" && task.dispatchStatus === "failed");
}

export function BatchResultDialog(props: BatchResultDialogProps) {
  useAuth();
  return <BatchResultSession key={JSON.stringify([props.open, props.batchId, props.token, getAuthSessionGeneration()])} {...props} />;
}

function BatchResultSession({
  open,
  onOpenChange,
  batchId,
  retain,
  token,
}: BatchResultDialogProps) {
  const copyHintId = useId();
  const { t } = useTranslation();
  const { role } = useAuth();
  const lifetimeRef = useRef<BatchResultLifetime>({
    generation: 0,
    sessionGeneration: getAuthSessionGeneration(),
    token,
    role,
    open,
  });
  const statusTokenRef = useRef<string | null>(null);
  const statusGenerationRef = useRef<number | null>(null);
  const [status, setStatus] = useState<BatchStatus | null>(null);
  const [statusRole, setStatusRole] = useState<AuthRole | null>(null);
  const [sessionGeneration] = useState(getAuthSessionGeneration);
  const [error, setError] = useState("");
  const [expandedTaskId, setExpandedTaskId] = useState<number | null>(null);
  const [taskLogStates, setTaskLogStates] = useState<Record<number, TaskLogState>>({});
  const taskLogStatesRef = useRef<Record<number, TaskLogState>>({});
  const updateTaskLogStates = useCallback((states: Record<number, TaskLogState>) => {
    taskLogStatesRef.current = states;
    setTaskLogStates(states);
  }, []);
  const [copyPending, setCopyPending] = useState(false);
  const copyPendingRef = useRef(false);
  const [appliedRole, setAppliedRole] = useState(role);
  if (appliedRole !== role) {
    setAppliedRole(role);
    setStatusRole(null);
    setExpandedTaskId(null);
    setCopyPending(false);
  }
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  useLayoutEffect(() => {
    if (lifetimeRef.current.role !== role) {
      updateTaskLogStates({});
      statusGenerationRef.current = null;
      copyPendingRef.current = false;
    }
    lifetimeRef.current = {
      generation: lifetimeRef.current.generation + 1,
      sessionGeneration: getAuthSessionGeneration(),
      token,
      role,
      open,
    };
    return () => {
      lifetimeRef.current = {
        ...lifetimeRef.current,
        generation: lifetimeRef.current.generation + 1,
      };
    };
  }, [open, role, token, updateTaskLogStates]);

  const stopPolling = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
    }
  }, []);

  const fetchStatus = useCallback(() => {
    if (!batchId) return;
    const generation = lifetimeRef.current.generation;
    const sessionGeneration = lifetimeRef.current.sessionGeneration;
    const requestToken = lifetimeRef.current.token;
    if (!requestToken || sessionGeneration !== getAuthSessionGeneration()) return;
    return apiClient.getBatchStatus(requestToken, batchId).then((result) => {
      if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== sessionGeneration) return;
      statusGenerationRef.current = generation;
      statusTokenRef.current = requestToken;
      setStatus(result);
      setStatusRole(lifetimeRef.current.role);
      setError("");

      const hasActive = result.tasks.some(isBatchTaskActive);
      if (!hasActive) {
        stopPolling();
      }
    }).catch((err: unknown) => {
      if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== sessionGeneration) return;
      setError(err instanceof Error ? err.message : t("batch.fetchStatusFailed"));
    });
  }, [batchId, stopPolling, t]);

  // 打开时开始轮询
  useEffect(() => {
    if (!open || !batchId || !token) {
      stopPolling();
      return;
    }

    void fetchStatus();
    intervalRef.current = setInterval(() => {
      void fetchStatus();
    }, 3_000);

    return stopPolling;
  }, [open, batchId, token, role, fetchStatus, stopPolling]);

  // 加载单个任务的日志输出
  const loadTaskLogs = useCallback(
    async (taskId: number) => {
      if (taskLogStatesRef.current[taskId]) return;
      const generation = lifetimeRef.current.generation;
      const sessionGeneration = lifetimeRef.current.sessionGeneration;
      const requestToken = lifetimeRef.current.token;
      if (!requestToken || sessionGeneration !== getAuthSessionGeneration()) return;
      updateTaskLogStates({ ...taskLogStatesRef.current, [taskId]: { status: "loading" } });
      try {
        const logs = await apiClient.getTaskLogs(requestToken, taskId, { limit: BATCH_LOG_LIMIT });
        if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== sessionGeneration) return;
        updateTaskLogStates({ ...taskLogStatesRef.current, [taskId]: { status: "loaded", logs } });
      } catch {
        if (lifetimeRef.current.generation !== generation || getAuthSessionGeneration() !== sessionGeneration) return;
        updateTaskLogStates({ ...taskLogStatesRef.current, [taskId]: { status: "error" } });
      }
    },
    [updateTaskLogStates]
  );

  const handleToggleExpand = useCallback(
    (task: BatchStatus["tasks"][number]) => {
      if (expandedTaskId === task.id) {
        setExpandedTaskId(null);
        return;
      }
      setExpandedTaskId(task.id);
      if (
        task.dispatchStatus !== "pending"
        && task.dispatchStatus !== "dispatching"
        && task.dispatchStatus !== "failed"
        && !activeBatchTaskStatuses.has(task.status)
      ) {
        void loadTaskLogs(task.id);
      }
    },
    [expandedTaskId, loadTaskLogs]
  );

  // 关闭时清理
  const handleClose = useCallback(
    (nextOpen: boolean) => {
      try {
        if (!nextOpen) {
          const current = lifetimeRef.current;
          const settled = status?.tasks.every((task) => !isBatchTaskActive(task)) ?? false;
          if (
            batchId
            && !retain
            && settled
            && current.token.length > 0
            && statusTokenRef.current === current.token
            && (current.role === "admin" || current.role === "operator")
          ) {
            void apiClient.deleteBatch(current.token, batchId).catch(() => {});
          }
        }
      } finally {
        if (!nextOpen) {
          lifetimeRef.current = { ...lifetimeRef.current, open: false, generation: lifetimeRef.current.generation + 1 };
        }
        onOpenChange(nextOpen);
      }
    },
    [batchId, retain, onOpenChange, status]
  );

  function ownsCopyLifetime(captured: BatchResultLifetime): boolean {
    const current = lifetimeRef.current;
    const identity = getAuthIdentitySnapshot();
    return current.open
      && captured.generation === current.generation
      && captured.sessionGeneration === getAuthSessionGeneration()
      && !isAuthTransitionActive()
      && identity.token === captured.token
      && identity.role === captured.role;
  }

  const identity = getAuthIdentitySnapshot();
  const copyAvailable = Boolean(open && batchId && token && status && !error
    && statusRole === role
    && sessionGeneration === getAuthSessionGeneration()
    && !isAuthTransitionActive()
    && identity.token === token && identity.role === role
    && (role === "admin" || role === "operator" || role === "viewer"));

  function canCopyResults(): boolean {
    const current = lifetimeRef.current;
    return Boolean(copyAvailable
      && statusGenerationRef.current === current.generation
      && current.role === role && current.token === token
      && (role === "admin" || role === "operator" || role === "viewer")
      && ownsCopyLifetime(current));
  }

  const copyHint = t("batch.copyLoadedHint", { limit: BATCH_LOG_LIMIT });

  async function handleCopyResults(): Promise<void> {
    if (copyPendingRef.current || !canCopyResults() || !status) return;
    const captured = { ...lifetimeRef.current };
    copyPendingRef.current = true;
    setCopyPending(true);
    try {
      const sections = [
        [
          t("batch.resultTitle"),
          t("batch.batchId", { id: batchId }),
          copyHint,
          t("batch.loadedResults", { loaded: status.tasks.length, total: status.total }),
        ].join("\n"),
        ...status.tasks.map((task) => {
          const taskStatus = task.dispatchStatus === "failed"
            ? t("batch.dispatchFailed")
            : task.dispatchStatus === "pending" || task.dispatchStatus === "dispatching"
              ? t("batch.dispatchPending")
              : t(`status.batch.${task.status}`, task.status);
          const lines = [
            t("batch.copyNode", { name: JSON.stringify(task.nodeName), id: task.nodeId }),
            t("batch.copyTask", { id: task.id }),
            t("batch.copyStatus", { status: taskStatus }),
          ];
          if (task.lastError) lines.push(t("batch.copyError", { error: task.lastError }));
          const logs = taskLogStates[task.id];
          if (!logs) lines.push(t("batch.logsNotLoaded"));
          else if (logs.status === "loading") lines.push(t("batch.loadingLogs"));
          else if (logs.status === "error") lines.push(t("batch.logLoadFailed"));
          else {
            lines.push(t("batch.loadedLogCount", { count: logs.logs.length, limit: BATCH_LOG_LIMIT }));
            lines.push(...logs.logs.map((log) => log.message));
          }
          return lines.join("\n");
        }),
      ];
      await navigator.clipboard.writeText(sections.join("\n\n"));
      if (ownsCopyLifetime(captured)) toast.success(t("common.copiedToClipboard"));
    } catch {
      if (ownsCopyLifetime(captured)) toast.error(t("common.copyFailed"));
    } finally {
      if (ownsCopyLifetime(captured)) {
        copyPendingRef.current = false;
        setCopyPending(false);
      }
    }
  }

  const allDone = status
    ? status.tasks.every((task) => !isBatchTaskActive(task))
    : false;
  const successCount = status?.statusCounts["success"] ?? 0;
  const failedCount = status?.tasks.filter((task) => task.status === "failed" || task.dispatchStatus === "failed").length ?? 0;

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("batch.resultTitle")}</DialogTitle>
          <DialogDescription>
            {t("batch.batchId", { id: batchId })}
            {status && (
              <span className="ml-2">
                — {allDone ? t("batch.done") : t("batch.running")}
                {status.total > 0 && (
                  <span className="ml-1 text-xs">
                    ({successCount} {t("batch.successCount")}
                    {failedCount > 0 && (
                      <span className="text-destructive">，{failedCount} {t("batch.failedCount")}</span>
                    )}
                    ，{t("batch.total", { count: status.total })})
                  </span>
                )}
              </span>
            )}
            {!retain && (
              <span className="ml-2 text-xs text-muted-foreground">
                · {t("batch.autoCleanHint")}
              </span>
            )}
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>
        <DialogBody>
          <div className="min-w-0 space-y-2">
            <Button
              type="button"
              variant="outline"
              className="h-auto max-w-full whitespace-normal text-left"
              aria-describedby={copyHintId}
              disabled={copyPending || !copyAvailable}
              onClick={() => void handleCopyResults()}
            >
              <Copy className="size-4 shrink-0" aria-hidden />
              {t("batch.copyLoadedResults")}
            </Button>
            <p id={copyHintId} className="text-xs text-muted-foreground break-words">{copyHint}</p>
          </div>
          {error && (
            <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {error}
            </div>
          )}

          {!status && !error && (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="size-5 animate-spin text-muted-foreground" />
              <span className="ml-2 text-sm text-muted-foreground">{t("common.loading")}</span>
            </div>
          )}

          {status && (
            <div className="max-h-96 space-y-1.5 overflow-y-auto">
              {status.tasks.map((task) => {
                const isExpanded = expandedTaskId === task.id;
                const isDone = !isBatchTaskActive(task);
                const dispatchFailed = task.dispatchStatus === "failed";
                const dispatchUnconfirmed = task.dispatchStatus === "pending" || task.dispatchStatus === "dispatching";
                const logState = taskLogStates[task.id];

                return (
                  <div key={task.id} className="rounded-md border border-border">
                    <button
                      type="button"
                      className="flex w-full items-center gap-3 px-3 py-2 text-left hover:bg-muted/50 transition-colors"
                      onClick={() => handleToggleExpand(task)}
                      disabled={!isDone}
                    >
                      {statusIcon[dispatchFailed ? "failed" : dispatchUnconfirmed ? "pending" : task.status] ?? (
                        <Circle className="size-4 shrink-0 text-muted-foreground" />
                      )}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium truncate">
                            {task.nodeName}
                          </span>
                          <span className="shrink-0 text-xs text-muted-foreground">
                            {dispatchFailed ? t("batch.dispatchFailed") : dispatchUnconfirmed ? t("batch.dispatchPending") : t(`status.batch.${task.status}`, task.status)}
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
                      <div className="space-y-2 border-t border-border bg-muted/30 px-3 py-2">
                        {task.lastError && (
                          <p className="text-xs text-destructive whitespace-pre-wrap break-all">
                            {task.lastError}
                          </p>
                        )}
                        {logState?.status === "loading" && (
                          <div className="flex items-center gap-2 py-2 text-xs text-muted-foreground">
                            <Loader2 className="size-3 animate-spin" />
                            {t("batch.loadingLogs")}
                          </div>
                        )}
                        {logState?.status === "error" && (
                          <p className="text-xs text-destructive">{t("batch.logLoadFailed")}</p>
                        )}
                        {logState?.status === "loaded" && logState.logs.length === 0 && (
                          <p className="text-xs text-muted-foreground">{t("batch.noLogOutput")}</p>
                        )}
                        {logState?.status === "loaded" && logState.logs.length > 0 && (
                          <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">
                            {logState.logs.map((log) => log.message).join("\n")}
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
