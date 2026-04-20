import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { useNodesContext } from "@/context/nodes-context";
import { useTasksContext } from "@/context/tasks-context";
import { useLiveLogs } from "@/hooks/use-live-logs";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
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
} from "../logs-page.utils";
import { LogsFullscreenDialog } from "../logs-page.fullscreen-dialog";
import { LogsFilterBar } from "./logs-filter-bar";
import { LogsViewer } from "./logs-viewer";
import { LogsHistory } from "./logs-history";
import { NodeLogsPanel } from "./logs-page.nodes";
import { AlertLogsPanel } from "./logs-page.alert";

const RSYNC_PROGRESS_RE = /^\s*[\d,]+\s+(\d+)%\s+[\d.]+[KMGT]?i?B\/s/i;

type LogTab = "task" | "node" | "alert";

export function LogsPage() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { nodes, refreshNodes } = useNodesContext();
  const { tasks, fetchTaskLogs, refreshTask, refreshTasks } = useTasksContext();

  useEffect(() => {
    void refreshNodes();
    void refreshTasks();
  }, [refreshNodes, refreshTasks]);

  const [searchParams, setSearchParams] = useSearchParams();

  const activeTab = (searchParams.get("tab") as LogTab | null) ?? "task";

  const setTab = (tab: LogTab) => {
    const next = new URLSearchParams(searchParams);
    next.set("tab", tab);
    setSearchParams(next, { replace: true });
  };

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

  const historyRequestIdRef = useRef(0);
  const lastHandledTerminalLogKeyRef = useRef<string | null>(null);
  const initialAlignmentTaskIdRef = useRef<number | null>(null);
  const refreshInFlightTaskIdRef = useRef<number | null>(null);

  // Sync filter state from URL search params (e.g. navigation from tasks page)
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

  // Fetch initial history when a specific task is selected
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

  // Reset alignment refs when focused task changes
  useEffect(() => {
    lastHandledTerminalLogKeyRef.current = null;
    initialAlignmentTaskIdRef.current = null;
    refreshInFlightTaskIdRef.current = null;
  }, [focusedTaskNumber]);

  // Initial status alignment for active focused task
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

  // Refresh task status when a terminal live log arrives
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

  // Parse rsync progress from incoming WS logs (watermark + runId isolation)
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

  // Track running task set changes: clear progress on new run start or terminal state
  useEffect(() => {
    const currentRunningIds = new Set(
      tasks
        .filter((t) => t.status === "running" || t.status === "retrying")
        .map((t) => t.id),
    );
    const toClear: number[] = [];
    for (const id of currentRunningIds) {
      if (!prevRunningIdsRef.current.has(id)) {
        toClear.push(id);
      }
    }
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
      {/* Tab bar */}
      <div className="flex gap-2" role="tablist" aria-label={t("nodeLogs.tab.task")}>
        {(["task", "node", "alert"] as LogTab[]).map((tab) => (
          <Button
            key={tab}
            role="tab"
            aria-selected={activeTab === tab}
            variant={activeTab === tab ? "default" : "outline"}
            size="sm"
            onClick={() => setTab(tab)}
          >
            {t(`nodeLogs.tab.${tab}`)}
          </Button>
        ))}
      </div>

      {activeTab === "task" && (
        <>
          <Card className="rounded-lg border border-border bg-card">
            <CardContent className="space-y-4 pt-6">
              <LogsFilterBar
                nodes={nodes}
                tasks={tasks}
                selectedNode={selectedNode}
                selectedTask={selectedTask}
                keyword={keyword}
                connected={connected}
                connectionWarning={connectionWarning}
                progressValue={progressValue}
                normalizedProgress={normalizedProgress}
                showProgress={focusedTask !== null || runningTasks.length > 0}
                filteredCount={filteredLogs.length}
                totalCount={mergedLogs.length}
                errorCode={focusedTask?.errorCode}
                onNodeChange={(value) => {
                  setSelectedNode(value);
                  syncSearchParams({ node: value });
                }}
                onTaskChange={(value) => {
                  setSelectedTask(value);
                  syncSearchParams({ task: value });
                }}
                onKeywordChange={(value) => {
                  setKeyword(value);
                  syncSearchParams({ q: value });
                }}
                onReset={resetFilters}
                onExport={exportAsText}
                onFullscreen={() => setFullScreen(true)}
              />

              <div className="space-y-3">
                <LogsViewer
                  filteredLogs={filteredLogs}
                  historyLoading={historyLoading}
                  onReset={resetFilters}
                />

                {focusedTaskNumber ? (
                  <LogsHistory
                    historyCount={historyLogs.length}
                    historyCursor={historyCursor}
                    historyPaging={historyPaging}
                    onLoadMore={() => void loadMoreHistory()}
                  />
                ) : null}
              </div>
            </CardContent>
          </Card>

          <LogsFullscreenDialog
            open={fullScreen}
            onOpenChange={setFullScreen}
            filteredLogs={filteredLogs}
          />
        </>
      )}

      {activeTab === "node" && <NodeLogsPanel />}

      {activeTab === "alert" && <AlertLogsPanel />}
    </div>
  );
}
