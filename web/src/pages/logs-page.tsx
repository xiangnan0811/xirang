import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Download,
  Maximize2,
} from "lucide-react";
import { useOutletContext, useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { useLiveLogs } from "@/hooks/use-live-logs";
import { usePersistentState } from "@/hooks/use-persistent-state";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { LoadingState } from "@/components/ui/loading-state";
import { InlineAlert } from "@/components/ui/inline-alert";
import { SearchInput } from "@/components/ui/search-input";
import { toast } from "@/components/ui/toast";
import { cn, getErrorMessage } from "@/lib/utils";
import type { LogEvent } from "@/types/domain";
import {
  selectedNodeStorageKey,
  selectedTaskStorageKey,
  keywordStorageKey,
  isTerminalTaskStatus,
  isActiveTaskStatus,
  parseToMillis,
  formatLogTime,
  minLogId,
} from "./logs-page.utils";
import { LogEntry } from "./logs-page.log-entry";
import { LogsFullscreenDialog } from "./logs-page.fullscreen-dialog";

const RSYNC_PROGRESS_RE = /^\s*[\d,]+\s+(\d+)%\s+[\d.]+[KMGT]?i?B\/s/i;

export function LogsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { tasks, nodes, fetchTaskLogs, refreshTask, refreshNodes, refreshTasks } =
    useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshNodes();
    void refreshTasks();
  }, [refreshNodes, refreshTasks]);
  const [searchParams, setSearchParams] = useSearchParams();

  const initialTask = searchParams.get("task") ?? "all";
  const initialNode = searchParams.get("node") ?? "all";
  const initialKeyword = searchParams.get("q") ?? "";

  const [selectedNode, setSelectedNode] = usePersistentState<string>(
    selectedNodeStorageKey,
    initialNode,
  );
  const [selectedTask, setSelectedTask] = usePersistentState<string>(
    selectedTaskStorageKey,
    initialTask,
  );
  const [keyword, setKeyword] = usePersistentState<string>(
    keywordStorageKey,
    initialKeyword,
  );
  const [historyLogs, setHistoryLogs] = useState<LogEvent[]>([]);
  const [historyCursor, setHistoryCursor] = useState<number | null>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyPaging, setHistoryPaging] = useState(false);
  const [fullScreen, setFullScreen] = useState(false);
  const [wsProgress, setWsProgress] = useState<Record<number, number>>({});
  const prevRunningIdsRef = useRef<Set<number>>(new Set());
  const lastProcessedWsLogIdRef = useRef(0);
  const wsRunIdByTaskRef = useRef<Record<number, number>>({});

  const focusedTaskID =
    selectedTask !== "all" ? Number(selectedTask) : undefined;
  const focusedTaskNumber =
    focusedTaskID && Number.isFinite(focusedTaskID) && focusedTaskID > 0
      ? focusedTaskID
      : undefined;

  const { connected, logs, connectionWarning } = useLiveLogs(token, {
    taskId: focusedTaskNumber,
  });

  const terminalRef = useRef<HTMLDivElement | null>(null);
  const historyRequestIdRef = useRef(0);
  const lastHandledTerminalLogKeyRef = useRef<string | null>(null);
  const initialAlignmentTaskIdRef = useRef<number | null>(null);
  const refreshInFlightTaskIdRef = useRef<number | null>(null);

  useEffect(() => {
    const nextTask = searchParams.get("task");
    const nextNode = searchParams.get("node");
    const nextKeyword = searchParams.get("q");

    if (nextTask !== null) {
      setSelectedTask(nextTask);
      // 从任务页跳转时只带 task 不带 node，自动匹配任务所属节点
      if (nextNode === null) {
        const matchedTask = tasks.find((t) => String(t.id) === nextTask);
        setSelectedNode(matchedTask?.nodeName ?? "all");
      }
    }
    if (nextNode !== null) {
      setSelectedNode(nextNode);
    }
    if (nextKeyword !== null) {
      setKeyword(nextKeyword);
    }
  }, [searchParams, setKeyword, setSelectedNode, setSelectedTask, tasks]);

  useEffect(() => {
    const taskID = Number(selectedTask);
    if (selectedTask === "all" || !Number.isFinite(taskID) || taskID <= 0) {
      historyRequestIdRef.current += 1;
      setHistoryLogs([]);
      setHistoryCursor(null);
      return;
    }

    const requestId = ++historyRequestIdRef.current;
    setHistoryLoading(true);
    void fetchTaskLogs(taskID, { limit: 200 })
      .then((rows) => {
        if (requestId !== historyRequestIdRef.current) {
          return;
        }
        setHistoryLogs(rows);
        setHistoryCursor(minLogId(rows));
      })
      .catch((err) => {
        if (requestId === historyRequestIdRef.current) {
          toast.error(getErrorMessage(err));
        }
      })
      .finally(() => {
        if (requestId === historyRequestIdRef.current) {
          setHistoryLoading(false);
        }
      });
  }, [fetchTaskLogs, selectedTask, setHistoryCursor, setHistoryLoading, setHistoryLogs]);

  const mergedLogs = useMemo(() => {
    const taskNodeMap = new Map(tasks.map((task) => [task.id, task.nodeName]));
    const dedup = new Map<string, LogEvent>();

    for (const log of [...logs, ...historyLogs]) {
      const enriched: LogEvent = {
        ...log,
        nodeName:
          log.nodeName ??
          (log.taskId ? taskNodeMap.get(log.taskId) : t("logs.system")) ??
          t("logs.system"),
      };
      const key = enriched.logId ? `log-${enriched.logId}` : enriched.id;
      if (!dedup.has(key)) {
        dedup.set(key, enriched);
      }
    }

    return [...dedup.values()].sort((first, second) => {
      const idGap = (second.logId ?? 0) - (first.logId ?? 0);
      if (idGap !== 0) {
        return idGap;
      }
      return parseToMillis(second) - parseToMillis(first);
    });
  }, [historyLogs, logs, t, tasks]);

  const filteredLogs = useMemo(() => {
    const keywordValue = keyword.trim().toLowerCase();
    return mergedLogs.filter((log) => {
      if (selectedNode !== "all" && log.nodeName !== selectedNode) {
        return false;
      }
      if (
        selectedTask !== "all" &&
        String(log.taskId ?? "") !== selectedTask
      ) {
        return false;
      }
      if (!keywordValue) {
        return true;
      }
      const text =
        `${log.nodeName ?? t("logs.system")} ${log.taskId ?? "-"} ${log.level} ${log.message} ${log.errorCode ?? ""}`
          .toLowerCase()
          .trim();
      return text.includes(keywordValue);
    });
  }, [keyword, mergedLogs, selectedNode, selectedTask, t]);

  const focusedTask =
    selectedTask === "all"
      ? null
      : tasks.find((task) => String(task.id) === selectedTask);

  const refreshFocusedTaskStatus = useCallback(async () => {
    if (!focusedTaskNumber) {
      return false;
    }
    if (refreshInFlightTaskIdRef.current === focusedTaskNumber) {
      return false;
    }

    refreshInFlightTaskIdRef.current = focusedTaskNumber;
    try {
      await refreshTask(focusedTaskNumber);
      return true;
    } catch {
      return false;
    } finally {
      if (refreshInFlightTaskIdRef.current === focusedTaskNumber) {
        refreshInFlightTaskIdRef.current = null;
      }
    }
  }, [focusedTaskNumber, refreshTask]);

  useEffect(() => {
    lastHandledTerminalLogKeyRef.current = null;
    initialAlignmentTaskIdRef.current = null;
    refreshInFlightTaskIdRef.current = null;
  }, [focusedTaskNumber]);

  useEffect(() => {
    if (!focusedTaskNumber || !isActiveTaskStatus(focusedTask?.status)) {
      return;
    }

    const hasTerminalLiveLog = logs.some(
      (log) =>
        log.taskId === focusedTaskNumber && isTerminalTaskStatus(log.status),
    );
    if (hasTerminalLiveLog) {
      return;
    }
    if (initialAlignmentTaskIdRef.current === focusedTaskNumber) {
      return;
    }

    void (async () => {
      const refreshed = await refreshFocusedTaskStatus();
      if (refreshed) {
        initialAlignmentTaskIdRef.current = focusedTaskNumber;
      }
    })();
  }, [focusedTask?.status, focusedTaskNumber, logs, refreshFocusedTaskStatus]);

  useEffect(() => {
    if (!focusedTaskNumber) {
      return;
    }

    const terminalLog = logs.find(
      (log) =>
        log.taskId === focusedTaskNumber && isTerminalTaskStatus(log.status),
    );
    if (!terminalLog?.status || focusedTask?.status === terminalLog.status) {
      return;
    }

    const terminalLogKey = `${focusedTaskNumber}:${terminalLog.logId ? `log-${terminalLog.logId}` : terminalLog.id}`;
    if (lastHandledTerminalLogKeyRef.current === terminalLogKey) {
      return;
    }

    void (async () => {
      const refreshed = await refreshFocusedTaskStatus();
      if (refreshed) {
        lastHandledTerminalLogKeyRef.current = terminalLogKey;
      }
    })();
  }, [focusedTask?.status, focusedTaskNumber, logs, refreshFocusedTaskStatus]);

  // 从新到达的 WS 日志中解析 rsync 进度（logId 水位线防重放 + runId 隔离）
  useEffect(() => {
    if (logs.length === 0) {
      lastProcessedWsLogIdRef.current = 0;
      return;
    }

    const prevMaxLogId = lastProcessedWsLogIdRef.current;
    const updates: Record<number, number> = {};
    const runIdResets: number[] = [];

    // logs 按 logId 降序排列；倒序遍历以按 logId 升序处理，
    // 保证终态日志先于新 run 进度日志被处理，避免同批次回滚
    for (let i = logs.length - 1; i >= 0; i--) {
      const log = logs[i];
      if (!log.logId || log.logId <= prevMaxLogId) continue;
      if (!log.taskId) continue;

      // 检测 taskRunId 变化 → 新 run 开始，清除旧进度
      if (log.taskRunId && wsRunIdByTaskRef.current[log.taskId] !== log.taskRunId) {
        wsRunIdByTaskRef.current[log.taskId] = log.taskRunId;
        runIdResets.push(log.taskId);
        delete updates[log.taskId];
      }

      // 终态日志 → 清除该任务的进度缓存
      if (log.status && isTerminalTaskStatus(log.status)) {
        runIdResets.push(log.taskId);
        delete updates[log.taskId];
        continue;
      }

      if (!log.message) continue;
      const m = RSYNC_PROGRESS_RE.exec(log.message);
      if (m) {
        const pct = parseInt(m[1], 10);
        if (pct > 0 && pct <= 100) {
          updates[log.taskId] = Math.max(updates[log.taskId] ?? 0, pct);
        }
      }
    }

    // 更新水位线
    const maxLogId = logs.reduce((max, l) => Math.max(max, l.logId ?? 0), prevMaxLogId);
    lastProcessedWsLogIdRef.current = maxLogId;

    if (Object.keys(updates).length > 0 || runIdResets.length > 0) {
      setWsProgress((prev) => {
        let changed = false;
        const next = { ...prev };
        for (const tid of runIdResets) {
          if (next[tid] !== undefined) {
            delete next[tid];
            changed = true;
          }
        }
        for (const [tidStr, pct] of Object.entries(updates)) {
          const tid = Number(tidStr);
          if (pct > (next[tid] ?? 0)) {
            next[tid] = pct;
            changed = true;
          }
        }
        return changed ? next : prev;
      });
    }
  }, [logs]);

  // 跟踪 running 任务集合变化：新 run 启动时清除旧缓存，终态时也清除
  useEffect(() => {
    const currentRunningIds = new Set(
      tasks
        .filter((t) => t.status === "running" || t.status === "retrying")
        .map((t) => t.id),
    );
    const toClear: number[] = [];
    // 新进入 running 的任务（新 run 开始）→ 清除旧进度
    for (const id of currentRunningIds) {
      if (!prevRunningIdsRef.current.has(id)) {
        toClear.push(id);
      }
    }
    // 离开 running 的任务（进入终态）→ 清除缓存
    for (const id of prevRunningIdsRef.current) {
      if (!currentRunningIds.has(id)) {
        toClear.push(id);
      }
    }
    prevRunningIdsRef.current = currentRunningIds;
    if (toClear.length > 0) {
      setWsProgress((prev) => {
        const next = { ...prev };
        for (const id of toClear) {
          delete next[id];
        }
        return next;
      });
    }
  }, [tasks]);

  const runningTasks = tasks.filter(
    (task) => task.status === "running" || task.status === "retrying",
  );
  const focusedWsProgress = focusedTask ? wsProgress[focusedTask.id] : undefined;
  const progressValue = focusedTask
    ? (focusedWsProgress ?? focusedTask.progress)
    : Math.round(
        runningTasks.reduce(
          (sum, task) => sum + (wsProgress[task.id] ?? task.progress),
          0,
        ) / Math.max(1, runningTasks.length),
      );
  const normalizedProgress = Math.min(100, Math.max(0, progressValue || 0));

  const syncSearchParams = (patch: {
    task?: string;
    node?: string;
    q?: string;
  }) => {
    const next = new URLSearchParams(searchParams);
    if (patch.task !== undefined) {
      if (!patch.task || patch.task === "all") {
        next.delete("task");
      } else {
        next.set("task", patch.task);
      }
    }
    if (patch.node !== undefined) {
      if (!patch.node || patch.node === "all") {
        next.delete("node");
      } else {
        next.set("node", patch.node);
      }
    }
    if (patch.q !== undefined) {
      if (!patch.q.trim()) {
        next.delete("q");
      } else {
        next.set("q", patch.q.trim());
      }
    }
    setSearchParams(next, { replace: true });
  };

  const resetFilters = () => {
    setSelectedNode("all");
    setSelectedTask("all");
    setKeyword("");
    syncSearchParams({ node: "all", task: "all", q: "" });
  };

  const exportAsText = () => {
    const content = filteredLogs
      .map((log) => {
        const taskText = log.taskId
          ? t("logs.taskIdLabel", { id: log.taskId })
          : t("logs.global");
        return `[${formatLogTime(log.timestamp)}] [${log.level.toUpperCase()}] [${log.nodeName ?? t("logs.system")}] [${taskText}] ${log.message}`;
      })
      .join("\n");

    const blob = new Blob([content || t("logs.noLogsExport")], {
      type: "text/plain;charset=utf-8",
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    const suffix = new Date().toISOString().replace(/[:.]/g, "-");
    anchor.href = url;
    anchor.download = `xirang-logs-${suffix}.txt`;
    document.body.appendChild(anchor);
    try {
      anchor.click();
    } finally {
      document.body.removeChild(anchor);
      URL.revokeObjectURL(url);
    }
  };

  const loadMoreHistory = async () => {
    if (!focusedTaskNumber || !historyCursor) {
      return;
    }
    setHistoryPaging(true);
    try {
      const rows = await fetchTaskLogs(focusedTaskNumber, {
        beforeId: historyCursor,
        limit: 120,
      });
      if (rows.length > 0) {
        setHistoryLogs((prev) => [...prev, ...rows]);
        setHistoryCursor(minLogId(rows));
      } else {
        setHistoryCursor(null);
      }
    } finally {
      setHistoryPaging(false);
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <Card className="glass-panel border-border/70">
        <CardContent className="space-y-4 pt-6">
          <FilterPanel
            sticky={false}
            className="flex flex-wrap items-center gap-3"
          >
            <AppSelect
              containerClassName="w-[140px]"
              aria-label={t("logs.nodeFilter")}
              value={selectedNode}
              onChange={(event) => {
                const value = event.target.value;
                setSelectedNode(value);
                syncSearchParams({ node: value });
              }}
            >
              <option value="all">{t("logs.allNodes")}</option>
              {nodes.map((node) => (
                <option key={node.id} value={node.name}>
                  {node.name}
                </option>
              ))}
            </AppSelect>

            <AppSelect
              containerClassName="w-[160px]"
              aria-label={t("logs.taskFilter")}
              value={selectedTask}
              onChange={(event) => {
                const nextTask = event.target.value;
                setSelectedTask(nextTask);
                syncSearchParams({ task: nextTask });
              }}
            >
              <option value="all">{t("logs.allTasks")}</option>
              {tasks.map((task) => (
                <option key={task.id} value={String(task.id)}>
                  #{task.id} {task.policyName}
                </option>
              ))}
            </AppSelect>

            <div className="hidden items-center gap-3 border-r border-border/50 pr-2 sm:flex">
              <div
                className={cn(
                  "size-2.5 rounded-full shadow-sm",
                  connected ? "bg-success" : "bg-muted",
                )}
                title={connected ? t("logs.connected") : t("logs.disconnected")}
              />

              {focusedTask || runningTasks.length > 0 ? (
                <div
                  className="flex items-center gap-2"
                  title={t("logs.taskProgress")}
                >
                  <span className="text-[11px] font-medium text-foreground/80">
                    {t("logs.progress", {
                      value: Number.isFinite(progressValue) ? progressValue : 0,
                    })}
                  </span>
                  <div
                    className="h-1.5 w-16 overflow-hidden rounded-full bg-muted"
                    role="progressbar"
                    aria-label={t("logs.taskProgressAriaLabel")}
                    aria-valuemin={0}
                    aria-valuemax={100}
                    aria-valuenow={normalizedProgress}
                  >
                    <div
                      className={cn(
                        "h-full transition-all duration-500",
                        normalizedProgress < 40
                          ? "bg-destructive"
                          : normalizedProgress < 70
                            ? "bg-warning"
                            : "bg-success",
                      )}
                      style={{ width: `${normalizedProgress}%` }}
                    />
                  </div>
                </div>
              ) : null}
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <SearchInput
                className="w-[120px] sm:w-[140px]"
                aria-label={t("logs.keywordFilter")}
                value={keyword}
                placeholder={t("logs.searchPlaceholder")}
                onChange={(event) => {
                  const value = event.target.value;
                  setKeyword(value);
                  syncSearchParams({ q: value });
                }}
              />

              <Button
                size="sm"
                variant="outline"
                className="w-[80px]"
                onClick={resetFilters}
              >
                {t("logs.resetFilter")}
              </Button>

              <Button
                variant="outline"
                size="sm"
                className="w-[80px]"
                onClick={exportAsText}
              >
                <Download className="mr-1 size-3.5" />
                {t("common.export")}
              </Button>

              <Button
                variant="outline"
                size="icon"
                className="size-8"
                aria-label={t("logs.fullscreen")}
                title={t("logs.fullscreenShort")}
                onClick={() => setFullScreen(true)}
              >
                <Maximize2 className="size-3.5" />
              </Button>
            </div>
          </FilterPanel>

          <FilterSummary
            filtered={filteredLogs.length}
            total={mergedLogs.length}
            unit={t("logs.logUnit")}
          />

          <div
            className={cn(
              "overflow-hidden transition-all duration-300 ease-in-out",
              connectionWarning ? "max-h-24 opacity-100" : "max-h-0 opacity-0"
            )}
            aria-hidden={!connectionWarning}
          >
            <InlineAlert tone="warning">
              {connectionWarning ?? ""}
            </InlineAlert>
          </div>

          {focusedTask?.errorCode ? (
            <InlineAlert tone="critical">
              {t("logs.errorCodeLabel", { code: focusedTask.errorCode })}
            </InlineAlert>
          ) : null}

          <div className="space-y-3">
            {historyLoading && !filteredLogs.length ? (
              <LoadingState
                title={t("logs.loadingTitle")}
                description={t("logs.loadingDesc")}
                rows={4}
              />
            ) : (
              <div
                ref={terminalRef}
                className={cn(
                  "terminal-surface thin-scrollbar overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-[13px]",
                  "h-[62vh]",
                )}
                role="log"
                aria-live="polite"
                aria-relevant="additions text"
                aria-label={t("logs.terminalAriaLabel", {
                  count: filteredLogs.length,
                })}
              >
                {filteredLogs.length > 0 ? (
                  <div className="px-1">
                    {filteredLogs.map((log) => (
                      <LogEntry
                        key={log.logId ? `line-${log.logId}` : log.id}
                        log={log}
                        hoverClass="hover:bg-white/10"
                      />
                    ))}
                  </div>
                ) : (
                  <div className="px-2 py-10">
                    <EmptyState
                      className="terminal-empty"
                      title={t("logs.emptyTitle")}
                      description={t("logs.emptyDesc")}
                      action={
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={resetFilters}
                        >
                          {t("logs.resetFilter")}
                        </Button>
                      }
                    />
                  </div>
                )}
              </div>
            )}

            {focusedTaskNumber ? (
              <div className="flex items-center justify-between gap-2 rounded-xl border border-border/75 bg-background/60 px-3 py-2">
                <p className="text-xs text-muted-foreground">
                  {t("logs.historyCount", { count: historyLogs.length })}
                  {historyCursor
                    ? t("logs.earliestCursor", { cursor: historyCursor })
                    : t("logs.reachedEarliest")}
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!historyCursor || historyPaging}
                  onClick={() => void loadMoreHistory()}
                >
                  {historyPaging
                    ? t("logs.loadingOlder")
                    : t("logs.loadOlderLogs")}
                </Button>
              </div>
            ) : null}
          </div>
        </CardContent>
      </Card>

      <LogsFullscreenDialog
        open={fullScreen}
        onOpenChange={setFullScreen}
        filteredLogs={filteredLogs}
      />
    </div>
  );
}
