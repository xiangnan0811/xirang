import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context.hooks";
import { useNodesContext } from "@/context/nodes-context.hooks";
import { useTasksContext } from "@/context/tasks-context.hooks";
import { useLiveLogs } from "@/hooks/use-live-logs";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { Button } from "@/components/ui/button";
import { InlineAlert } from "@/components/ui/inline-alert";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
  DataSurfaceToolbar,
} from "@/components/ui/data-surface";
import { PageHero } from "@/components/ui/page-hero";
import { Badge } from "@/components/ui/badge";
import { apiClient } from "@/lib/api/client";
import {
  ApiError,
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthSessionGeneration,
  subscribeAuthTransition,
} from "@/lib/api/core";
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
  parseLogsRunScope,
} from "../logs-page.utils";
import { LogsFullscreenDialog } from "../logs-page.fullscreen-dialog";
import { LogsFilterBar } from "./logs-filter-bar";
import { LogsViewer } from "./logs-viewer";
import { LogsHistory } from "./logs-history";

const RSYNC_PROGRESS_RE = /^\s*[\d,]+\s+(\d+)%\s+[\d.]+[KMGT]?i?B\/s/i;
const RUN_INITIAL_LIMIT = 200;
const OLDER_PAGE_LIMIT = 120;

type HistoryFailure = {
  phase: "initial" | "older";
  kind: "forbidden" | "unavailable" | "mismatch" | "failed";
};

function classifyHistoryFailure(error: unknown): HistoryFailure["kind"] {
  if (error instanceof ApiError) {
    const status = error.httpStatus ?? error.status;
    if (status === 403) return "forbidden";
    if (status === 404) return "unavailable";
  }
  return "failed";
}

function authIdentityMatches(token: string | null, role: unknown): boolean {
  const snapshot = getAuthIdentitySnapshot();
  return snapshot.token === token && snapshot.role === role;
}

export function LogsPage() {
  const { token, role } = useAuth();
  // Keep external-store subscriptions outside the session's render-phase scope resets:
  // React 18 must commit each barrier snapshot, including false → true → false.
  const authGeneration = useSyncExternalStore(
    subscribeAuthSessionGeneration,
    getAuthSessionGeneration,
    getAuthSessionGeneration,
  );
  const authTransitioning = useSyncExternalStore(
    subscribeAuthTransition,
    isAuthTransitionActive,
    () => false,
  );
  return (
    <LogsPageSession
      key={JSON.stringify([token, role])}
      authGeneration={authGeneration}
      authTransitioning={authTransitioning}
    />
  );
}

function LogsPageSession({
  authGeneration,
  authTransitioning,
}: {
  authGeneration: number;
  authTransitioning: boolean;
}) {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const { nodes, refreshNodes } = useNodesContext();
  const {
    tasks,
    fetchTaskLogs,
    refreshTask,
    refreshTasks,
    tasksLoading,
    tasksError,
    tasksLoaded,
  } = useTasksContext();

  useEffect(() => {
    void refreshNodes();
    void refreshTasks();
  }, [refreshNodes, refreshTasks]);

  const [searchParams, setSearchParams] = useSearchParams();
  const runScope = parseLogsRunScope(searchParams);
  const scopeKind = runScope.kind;
  const runTaskId = runScope.kind === "run" ? runScope.taskId : 0;
  const runRunId = runScope.kind === "run" ? runScope.runId : 0;
  const isTaskMode = scopeKind === "task";
  const isRunMode = scopeKind === "run";
  const isInvalidRun = scopeKind === "invalid-run";

  // rememberAuthIdentity does not notify; the auth provider's state update re-renders us after install.
  const authIdentityReady = authIdentityMatches(token, role);

  const [storedSelectedNode, setSelectedNode] = usePersistentState<string>(selectedNodeStorageKey, "all");
  const [storedSelectedTask, setSelectedTask] = usePersistentState<string>(selectedTaskStorageKey, "all");
  const [storedKeyword, setKeyword] = usePersistentState<string>(keywordStorageKey, "");
  // Run/invalid-run identity comes only from the URL; persisted task-log preferences never apply.
  const selectedTask = isTaskMode ? searchParams.get("task") ?? storedSelectedTask : "all";
  const selectedNode = isTaskMode
    ? searchParams.get("node") ?? (searchParams.has("task") ? "all" : storedSelectedNode)
    : "all";
  const keyword = isTaskMode
    ? searchParams.get("q") ?? (searchParams.has("task") ? "" : storedKeyword)
    : searchParams.get("q") ?? "";
  const [historyLogs, setHistoryLogs] = useState<LogEvent[]>([]);
  const [historyCursor, setHistoryCursor] = useState<number | null>(null);
  const [historyLoading, setHistoryLoading] = useState(
    isRunMode
      || (selectedTask !== "all" && Number.isFinite(Number(selectedTask)) && Number(selectedTask) > 0),
  );
  const [historyPaging, setHistoryPaging] = useState(false);
  const [historyLoaded, setHistoryLoaded] = useState(false);
  const [historyFailure, setHistoryFailure] = useState<HistoryFailure | null>(null);
  const [historyRevision, setHistoryRevision] = useState(0);
  const [historyBlocked, setHistoryBlocked] = useState(false);
  // Run rows/actions are shown only after a paired detail and the first exact log page both succeed.
  const [runVerified, setRunVerified] = useState(false);
  const [fullScreen, setFullScreen] = useState(false);
  const [wsProgress, setWsProgress] = useState<Record<number, number>>({});
  const [progressInput, setProgressInput] = useState<{ logs: string; tasks: string; watermark: number; runs: Record<number, number>; running: Set<number> }>({ logs: "", tasks: "", watermark: 0, runs: {}, running: new Set() });

  const focusedTaskID = selectedTask !== "all" ? Number(selectedTask) : undefined;
  const focusedTaskNumber = focusedTaskID && Number.isFinite(focusedTaskID) && focusedTaskID > 0
    ? focusedTaskID
    : undefined;

  const { connected, logs, connectionWarning } = useLiveLogs(token, {
    taskId: focusedTaskNumber,
    enabled: isTaskMode,
  });

  const historyRequestIdRef = useRef(0);
  const olderFlightRef = useRef(0);
  const olderFlightSeqRef = useRef(0);
  const olderAbortRef = useRef<AbortController | null>(null);
  const runLoadInFlightRef = useRef(false);
  const lastHandledTerminalLogKeyRef = useRef<string | null>(null);
  const initialAlignmentTaskIdRef = useRef<number | null>(null);
  const refreshInFlightTaskIdRef = useRef<number | null>(null);
  const fullscreenOpenerRef = useRef<HTMLElement | null>(null);

  // Task mode keeps its original scope; run/invalid-run also bind the auth generation and barrier.
  const historyScope = JSON.stringify(
    isTaskMode
      ? [token, role, "task", selectedTask, 0, false]
      : [token, role, scopeKind, runTaskId, runRunId, authGeneration, authTransitioning],
  );
  const [previousHistoryScope, setPreviousHistoryScope] = useState(historyScope);
  const scopeChanged = previousHistoryScope !== historyScope;
  if (scopeChanged) {
    setPreviousHistoryScope(historyScope);
    setHistoryLogs([]);
    setHistoryCursor(null);
    setHistoryLoading(isRunMode || Boolean(focusedTaskNumber));
    setHistoryPaging(false);
    setHistoryLoaded(false);
    setHistoryFailure(null);
    setHistoryBlocked(false);
    setRunVerified(false);
    setFullScreen(false);
  }
  useLayoutEffect(() => () => {
    historyRequestIdRef.current += 1;
    olderFlightRef.current = 0;
    olderAbortRef.current?.abort();
    olderAbortRef.current = null;
    // A new scope must not send focus back to an opener captured for the previous view.
    fullscreenOpenerRef.current = null;
    runLoadInFlightRef.current = false;
  }, [historyScope]);

  useEffect(() => {
    if (!isTaskMode) return;
    setSelectedTask(selectedTask);
    setSelectedNode(selectedNode);
    setKeyword(keyword);
  }, [isTaskMode, keyword, selectedNode, selectedTask, setKeyword, setSelectedNode, setSelectedTask]);

  useEffect(() => {
    // Run/invalid-run scopes own (or deliberately skip) history reads; never touch task endpoints.
    if (scopeKind !== "task") return;
    const taskID = Number(selectedTask);
    if (selectedTask === "all" || !Number.isFinite(taskID) || taskID <= 0) {
      historyRequestIdRef.current += 1;
      return;
    }

    const requestId = ++historyRequestIdRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    olderFlightRef.current = 0;
    const isCurrent = () => requestId === historyRequestIdRef.current
      && sessionGeneration === getAuthSessionGeneration();

    void fetchTaskLogs(taskID, { limit: 200 })
      .then((rows) => {
        if (!isCurrent()) return;
        setHistoryBlocked(false);
        setHistoryLogs(rows);
        setHistoryCursor(minLogId(rows));
        setHistoryLoaded(true);
      })
      .catch((error: unknown) => {
        if (!isCurrent()) return;
        const kind = classifyHistoryFailure(error);
        setHistoryFailure({ phase: "initial", kind });
        if (kind !== "failed") {
          setHistoryLogs([]);
          setHistoryCursor(null);
          setHistoryLoaded(false);
          setHistoryBlocked(true);
        }
      })
      .finally(() => {
        if (isCurrent()) setHistoryLoading(false);
      });
    return () => {
      historyRequestIdRef.current += 1;
    };
  }, [fetchTaskLogs, historyRevision, scopeKind, selectedTask, token]);

  // Exact run first page: paired detail check, then the run-scoped log endpoint only.
  useEffect(() => {
    if (scopeKind !== "run") return;
    if (!token || authTransitioning || !authIdentityReady) return;

    const requestId = ++historyRequestIdRef.current;
    const controller = new AbortController();
    olderFlightRef.current = 0;
    runLoadInFlightRef.current = true;
    const requestToken = token;
    const isCurrent = () => !controller.signal.aborted
      && requestId === historyRequestIdRef.current
      && authGeneration === getAuthSessionGeneration()
      && !isAuthTransitionActive()
      && authIdentityMatches(requestToken, role);
    const settle = () => {
      runLoadInFlightRef.current = false;
      setHistoryLoading(false);
    };
    const block = (kind: HistoryFailure["kind"]) => {
      setHistoryLogs([]);
      setHistoryCursor(null);
      setHistoryLoaded(false);
      setRunVerified(false);
      setFullScreen(false);
      setHistoryFailure({ phase: "initial", kind });
      setHistoryBlocked(kind !== "failed");
      settle();
    };

    void (async () => {
      try {
        const detail = await apiClient.getTaskRun(requestToken, runRunId, { signal: controller.signal });
        if (!isCurrent()) return;
        if (detail.id !== runRunId || detail.taskId !== runTaskId) {
          block("mismatch");
          return;
        }
        const rows = await apiClient.getTaskRunLogs(requestToken, runRunId, {
          limit: RUN_INITIAL_LIMIT,
          signal: controller.signal,
        });
        if (!isCurrent()) return;
        setHistoryBlocked(false);
        setHistoryFailure(null);
        setHistoryLogs(rows);
        setHistoryCursor(minLogId(rows));
        setHistoryLoaded(true);
        setRunVerified(true);
        settle();
      } catch (error: unknown) {
        if (!isCurrent()) return;
        block(classifyHistoryFailure(error));
      }
    })();
    return () => {
      historyRequestIdRef.current += 1;
      runLoadInFlightRef.current = false;
      controller.abort();
    };
  }, [
    authGeneration,
    authIdentityReady,
    authTransitioning,
    historyRevision,
    role,
    runRunId,
    runTaskId,
    scopeKind,
    token,
  ]);

  const mergedLogs = useMemo(() => {
    if (!isTaskMode) {
      // Exact run rows come solely from the run endpoint: no live merge, no current-task enrichment.
      const dedup = new Map<string, LogEvent>();
      for (const log of historyLogs) {
        const key = log.logId ? `log-${log.logId}` : log.id;
        if (!dedup.has(key)) dedup.set(key, log);
      }
      return [...dedup.values()].sort((first, second) => {
        const idGap = (second.logId ?? 0) - (first.logId ?? 0);
        if (idGap !== 0) return idGap;
        return parseToMillis(second) - parseToMillis(first);
      });
    }

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
  }, [historyLogs, isTaskMode, logs, t, tasks]);

  const filteredLogs = useMemo(() => {
    const keywordValue = keyword.trim().toLowerCase();
    return mergedLogs.filter((log) => {
      if (isTaskMode) {
        if (selectedNode !== "all" && log.nodeName !== selectedNode) {
          return false;
        }
        if (
          selectedTask !== "all" &&
          String(log.taskId ?? "") !== selectedTask
        ) {
          return false;
        }
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
  }, [isTaskMode, keyword, mergedLogs, selectedNode, selectedTask, t]);
  const runDataVisible = isRunMode && runVerified && !historyBlocked;
  const displayLogs = isTaskMode
    ? historyBlocked ? [] : filteredLogs
    : runDataVisible ? filteredLogs : [];

  const focusedTask = selectedTask === "all"
    ? null
    : tasks.find((task) => String(task.id) === selectedTask) ?? null;

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

  const progressLogsKey = JSON.stringify(logs.map((log) => [log.logId, log.taskId, log.taskRunId, log.status, log.message]));
  const progressTasksKey = JSON.stringify(tasks.map((task) => [task.id, task.status]));
  // Reduce each new websocket batch once; retain progress across bounded log windows.
  if (progressInput.logs !== progressLogsKey || progressInput.tasks !== progressTasksKey) {
    const next = { ...wsProgress };
    const runs = { ...progressInput.runs };
    let watermark = logs.length ? progressInput.watermark : 0;
    if (progressInput.logs !== progressLogsKey) {
      for (let i = logs.length - 1; i >= 0; i--) {
        const log = logs[i];
        if (!log.logId || log.logId <= progressInput.watermark || !log.taskId) continue;
        if (log.taskRunId && runs[log.taskId] !== log.taskRunId) {
          runs[log.taskId] = log.taskRunId;
          delete next[log.taskId];
        }
        if (log.status && isTerminalTaskStatus(log.status)) {
          delete next[log.taskId];
          continue;
        }
        const match = RSYNC_PROGRESS_RE.exec(log.message ?? "");
        const pct = match ? Number(match[1]) : 0;
        if (pct > 0 && pct <= 100) next[log.taskId] = Math.max(next[log.taskId] ?? 0, pct);
      }
      watermark = logs.reduce((max, log) => Math.max(max, log.logId ?? 0), watermark);
    }
    const running = new Set(tasks.filter((task) => task.status === "running" || task.status === "retrying").map((task) => task.id));
    if (progressInput.tasks !== progressTasksKey) {
      for (const id of running) if (!progressInput.running.has(id)) delete next[id];
      for (const id of progressInput.running) if (!running.has(id)) delete next[id];
    }
    setProgressInput({ logs: progressLogsKey, tasks: progressTasksKey, watermark, runs, running });
    setWsProgress(next);
  }

  const runningTasks = tasks.filter(
    (task) => task.status === "running" || task.status === "retrying",
  );
  const failedTasks = tasks.filter((task) => task.status === "failed").length;
  const focusedWsProgress = focusedTask ? wsProgress[focusedTask.id] : undefined;
  const progressValue = focusedTask
    ? (focusedWsProgress ?? focusedTask.progress)
    : selectedTask === "all" ? Math.round(
        runningTasks.reduce(
          (sum, task) => sum + (wsProgress[task.id] ?? task.progress),
          0,
        ) / Math.max(1, runningTasks.length),
      ) : 0;
  const normalizedProgress = Math.min(100, Math.max(0, progressValue || 0));
  const showAggregateProgress = selectedTask === "all" && runningTasks.length > 0;

  const taskListReady = tasksLoaded && !tasksLoading && !tasksError;
  const selectedTaskInList = tasks.some((task) => String(task.id) === selectedTask);

  const syncSearchParams = (patch: {
    task?: string;
    node?: string;
    q?: string;
    dropRun?: boolean;
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
      if (patch.q.length === 0) {
        next.delete("q");
      } else {
        next.set("q", patch.q);
      }
    }
    if (patch.dropRun) {
      next.delete("run");
    }
    // The URL controls the input; commit it before another keystroke can read the old value.
    setSearchParams(next, { replace: true, flushSync: true });
  };

  const resetFilters = () => {
    if (!isTaskMode) {
      // Run reset stays on the same run and never consults saved task/node preferences.
      syncSearchParams({ q: "" });
      return;
    }
    setSelectedNode("all");
    setSelectedTask("all");
    setKeyword("");
    syncSearchParams({ node: "all", task: "all", q: "", dropRun: true });
  };

  const openTaskLogs = () => {
    if (runScope.kind !== "run") return;
    const next = new URLSearchParams(searchParams);
    next.set("task", String(runScope.taskId));
    next.delete("run");
    next.delete("node");
    next.delete("q");
    // Push so browser Back returns to the exact run.
    setSearchParams(next);
  };

  const leaveInvalidRun = () => {
    const next = new URLSearchParams(searchParams);
    next.delete("task");
    next.delete("run");
    next.delete("node");
    next.delete("q");
    setSearchParams(next);
  };

  const showViewer = isTaskMode
    ? !historyBlocked && (!historyFailure || displayLogs.length > 0)
    : isRunMode && (runDataVisible || (historyLoading && !historyFailure));
  const logActionsDisabled = isTaskMode
    ? !showViewer || (historyLoading && displayLogs.length === 0)
    : !runDataVisible || historyLoading;

  const runIdentityLabel = isRunMode
    ? t("logs.runIdentity", { taskId: runTaskId, runId: runRunId })
    : undefined;

  const exportAsText = () => {
    if (logActionsDisabled) return;
    const lines = displayLogs
      .map((log) => {
        const taskText = log.taskId
          ? t("logs.taskIdLabel", { id: log.taskId })
          : t("logs.global");
        return `[${formatLogTime(log.timestamp)}] [${log.level.toUpperCase()}] [${log.nodeName ?? t("logs.system")}] [${taskText}] ${log.message}`;
      })
      .join("\n");
    const body = lines || t("logs.noLogsExport");
    const content = isRunMode
      ? `${runIdentityLabel}\n${t("logs.runSnapshot")}\n${body}`
      : body;

    const blob = new Blob([content], {
      type: "text/plain;charset=utf-8",
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    const suffix = new Date().toISOString().replace(/[:.]/g, "-");
    anchor.href = url;
    anchor.download = isRunMode
      ? `xirang-task-${runTaskId}-run-${runRunId}-${suffix}.txt`
      : `xirang-logs-${suffix}.txt`;
    document.body.appendChild(anchor);
    try {
      anchor.click();
    } finally {
      document.body.removeChild(anchor);
      URL.revokeObjectURL(url);
    }
  };

  const loadMoreHistory = () => {
    if (!focusedTaskNumber || historyCursor === null || historyBlocked || historyLoading || olderFlightRef.current !== 0) {
      return;
    }
    const requestId = historyRequestIdRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    const flight = ++olderFlightSeqRef.current;
    olderFlightRef.current = flight;
    const taskId = focusedTaskNumber;
    const cursor = historyCursor;
    setHistoryPaging(true);
    setHistoryFailure(null);
    const ownsFlight = () => olderFlightRef.current === flight
      && requestId === historyRequestIdRef.current
      && sessionGeneration === getAuthSessionGeneration();

    void fetchTaskLogs(taskId, { beforeId: cursor, limit: OLDER_PAGE_LIMIT })
      .then((rows) => {
        if (!ownsFlight()) return;
        if (rows.length > 0) {
          setHistoryLogs((prev) => [...prev, ...rows]);
          setHistoryCursor(minLogId(rows));
        } else {
          setHistoryCursor(null);
        }
        setHistoryFailure(null);
      })
      .catch((error: unknown) => {
        if (!ownsFlight()) return;
        const kind = classifyHistoryFailure(error);
        setHistoryFailure({ phase: "older", kind });
        if (kind !== "failed") {
          setHistoryLogs([]);
          setHistoryCursor(null);
          setHistoryLoaded(false);
          setHistoryBlocked(true);
        }
      })
      .finally(() => {
        if (!ownsFlight()) return;
        olderFlightRef.current = 0;
        setHistoryPaging(false);
      });
  };

  const loadMoreRunHistory = () => {
    if (
      runScope.kind !== "run"
      || !runVerified
      || historyCursor === null
      || historyBlocked
      || historyLoading
      || runLoadInFlightRef.current
      || olderFlightRef.current !== 0
      || !token
      || authTransitioning
      || !authIdentityMatches(token, role)
    ) {
      return;
    }
    const requestId = historyRequestIdRef.current;
    const generation = authGeneration;
    const requestToken = token;
    const runId = runScope.runId;
    const flight = ++olderFlightSeqRef.current;
    const controller = new AbortController();
    olderFlightRef.current = flight;
    olderAbortRef.current = controller;
    const cursor = historyCursor;
    setHistoryPaging(true);
    setHistoryFailure(null);
    const ownsFlight = () => !controller.signal.aborted
      && olderFlightRef.current === flight
      && requestId === historyRequestIdRef.current
      && generation === getAuthSessionGeneration()
      && !isAuthTransitionActive()
      && authIdentityMatches(requestToken, role);
    const release = () => {
      olderFlightRef.current = 0;
      if (olderAbortRef.current === controller) olderAbortRef.current = null;
      setHistoryPaging(false);
    };

    void (async () => {
      try {
        // The run/task pairing was verified on the first page and is immutable; each page is re-authorized server-side.
        const rows = await apiClient.getTaskRunLogs(requestToken, runId, {
          beforeId: cursor,
          limit: OLDER_PAGE_LIMIT,
          signal: controller.signal,
        });
        if (!ownsFlight()) return;
        if (rows.length > 0) {
          setHistoryLogs((prev) => [...prev, ...rows]);
          setHistoryCursor(minLogId(rows));
        } else {
          setHistoryCursor(null);
        }
        setHistoryFailure(null);
        release();
      } catch (error: unknown) {
        if (!ownsFlight()) return;
        const kind = classifyHistoryFailure(error);
        setHistoryFailure({ phase: "older", kind });
        if (kind !== "failed") {
          setHistoryLogs([]);
          setHistoryCursor(null);
          setHistoryLoaded(false);
          setRunVerified(false);
          setHistoryBlocked(true);
          setFullScreen(false);
        }
        release();
      }
    })();
  };

  const retryInitialHistory = () => {
    if (!focusedTaskNumber || historyLoading) return;
    setFullScreen(false);
    setHistoryLoading(true);
    setHistoryLoaded(false);
    setHistoryFailure(null);
    setHistoryPaging(false);
    setHistoryRevision((revision) => revision + 1);
  };

  // Explicit run refresh and initial-failure retry share one single-flight first-page reload.
  const reloadRun = () => {
    if (
      runScope.kind !== "run"
      || runLoadInFlightRef.current
      || !token
      || authTransitioning
      || !authIdentityMatches(token, role)
    ) {
      return;
    }
    runLoadInFlightRef.current = true;
    historyRequestIdRef.current += 1;
    olderFlightRef.current = 0;
    olderAbortRef.current?.abort();
    olderAbortRef.current = null;
    setFullScreen(false);
    setHistoryLogs([]);
    setHistoryCursor(null);
    setRunVerified(false);
    setHistoryLoaded(false);
    setHistoryBlocked(false);
    setHistoryFailure(null);
    setHistoryPaging(false);
    setHistoryLoading(true);
    setHistoryRevision((revision) => revision + 1);
  };

  const failureMessage = isTaskMode
    ? historyFailure?.kind === "forbidden" ? "logs.historyForbidden"
      : historyFailure?.kind === "unavailable" ? "logs.historyUnavailable"
        : historyFailure?.phase === "older" ? "logs.historyOlderFailed" : "logs.historyLoadFailed"
    : historyFailure?.kind === "forbidden" ? "logs.runForbidden"
      : historyFailure?.kind === "unavailable" ? "logs.runUnavailable"
        : historyFailure?.kind === "mismatch" ? "logs.runMismatch"
          : historyFailure?.phase === "older" ? "logs.historyOlderFailed" : "logs.runLoadFailed";

  if (isInvalidRun) {
    return (
      <div className="animate-fade-in space-y-5">
        <PageHero title={t("logs.runTitle")} />
        <DataSurface>
          <DataSurfaceContent className="space-y-3">
            <InlineAlert tone="critical">{t("logs.runInvalidLink")}</InlineAlert>
            <Button variant="outline" size="sm" onClick={leaveInvalidRun}>
              {t("logs.backToTaskLogs")}
            </Button>
          </DataSurfaceContent>
        </DataSurface>
      </div>
    );
  }

  return (
    <div className="animate-fade-in space-y-5">
      {isRunMode ? (
        <PageHero
          title={t("logs.runTitle")}
          subtitle={t("logs.runSnapshot")}
          meta={<span className="min-w-0 break-all font-mono text-foreground/80">{runIdentityLabel}</span>}
          actions={
            <>
              <Button variant="outline" size="sm" disabled={historyLoading} onClick={reloadRun}>
                {t("common.refresh")}
              </Button>
              <Button variant="outline" size="sm" onClick={openTaskLogs}>
                {t("logs.viewTaskLogs")}
              </Button>
            </>
          }
        />
      ) : (
        <PageHero
          title={t("logs.pageTitle")}
          subtitle={t("logs.pageDesc")}
          meta={
            <>
              <Badge tone={connected ? "success" : "neutral"}>
                {connected ? t("logs.connected") : t("logs.disconnected")}
              </Badge>
              <Badge tone={runningTasks.length > 0 ? "info" : "neutral"}>
                {t("logs.runningTasksMeta", { count: runningTasks.length })}
              </Badge>
              <Badge tone={failedTasks > 0 ? "destructive" : "neutral"}>
                {t("logs.failedTasksMeta", { count: failedTasks })}
              </Badge>
            </>
          }
        />
      )}

      <DataSurface>
        <DataSurfaceHeader
          title={isRunMode ? <span className="break-all">{runIdentityLabel}</span> : t("logs.taskSurfaceTitle")}
          description={isRunMode ? undefined : t("logs.taskSurfaceDesc")}
        />
        <DataSurfaceToolbar className="space-y-4">
          <LogsFilterBar
            mode={isRunMode ? "run" : "task"}
            nodes={nodes}
            tasks={tasks}
            selectedNode={selectedNode}
            selectedTask={selectedTask}
            keyword={keyword}
            connected={isTaskMode && connected}
            connectionWarning={isTaskMode ? connectionWarning : null}
            progressValue={progressValue}
            normalizedProgress={normalizedProgress}
            showProgress={isTaskMode && (focusedTask !== null || showAggregateProgress)}
            filteredCount={displayLogs.length}
            totalCount={isTaskMode ? (historyBlocked ? 0 : mergedLogs.length) : (runDataVisible ? mergedLogs.length : 0)}
            errorCode={isTaskMode ? focusedTask?.errorCode : undefined}
            taskListReady={taskListReady}
            logActionsDisabled={logActionsDisabled}
            onNodeChange={(value) => {
              if (!isTaskMode) return;
              setSelectedNode(value);
              syncSearchParams({ node: value });
            }}
            onTaskChange={(value) => {
              if (!isTaskMode) return;
              setSelectedTask(value);
              syncSearchParams({ task: value });
            }}
            onKeywordChange={(value) => {
              if (isTaskMode) setKeyword(value);
              syncSearchParams({ q: value });
            }}
            onReset={resetFilters}
            onExport={exportAsText}
            onFullscreen={() => {
              if (logActionsDisabled) return;
              const opener = document.activeElement;
              fullscreenOpenerRef.current = opener instanceof HTMLElement && opener !== document.body ? opener : null;
              setFullScreen(true);
            }}
          />
          {isTaskMode && selectedTask !== "all" && tasksError ? (
            <InlineAlert tone="warning">{t("logs.taskListUnavailable")}</InlineAlert>
          ) : isTaskMode && selectedTask !== "all" && taskListReady && !selectedTaskInList ? (
            <p className="text-sm text-muted-foreground">{t("logs.historicalTaskHint")}</p>
          ) : null}
        </DataSurfaceToolbar>

        <DataSurfaceContent className="space-y-3">
          <div className="space-y-3">
            {isRunMode ? (
              historyFailure ? (
                <div className="flex flex-wrap items-center gap-3">
                  <InlineAlert tone="critical" className="min-w-0 flex-1">
                    {t(failureMessage)}
                  </InlineAlert>
                  {historyFailure.phase === "initial" || historyBlocked ? (
                    <Button variant="outline" size="sm" disabled={historyLoading} onClick={reloadRun}>
                      {t("common.retry")}
                    </Button>
                  ) : null}
                </div>
              ) : null
            ) : historyFailure || (historyBlocked && historyLoading) ? (
              <div className="flex flex-wrap items-center gap-3">
                {historyFailure && (
                  <InlineAlert tone="critical" className="min-w-0 flex-1">
                    {t(failureMessage)}
                  </InlineAlert>
                )}
                {(historyBlocked || historyFailure?.phase === "initial") && (
                  <Button variant="outline" size="sm" disabled={historyLoading} onClick={retryInitialHistory}>
                    {t("common.retry")}
                  </Button>
                )}
                {historyLoading && <p role="status">{t("common.loading")}</p>}
              </div>
            ) : null}

            {showViewer ? (
              <LogsViewer
                mode={isRunMode ? "run" : "task"}
                filteredLogs={displayLogs}
                historyLoading={historyLoading}
                onReset={resetFilters}
              />
            ) : null}

            {isRunMode
              ? runDataVisible && historyLoaded && !historyLoading ? (
                <LogsHistory
                  historyCount={historyLogs.length}
                  historyCursor={historyCursor}
                  historyPaging={historyPaging}
                  onLoadMore={loadMoreRunHistory}
                />
              ) : null
              : focusedTaskNumber && historyLoaded && !historyBlocked ? (
                <LogsHistory
                  historyCount={historyLogs.length}
                  historyCursor={historyCursor}
                  historyPaging={historyPaging}
                  onLoadMore={loadMoreHistory}
                />
              ) : null}
          </div>
        </DataSurfaceContent>
      </DataSurface>

      <LogsFullscreenDialog
        open={fullScreen && !logActionsDisabled}
        onOpenChange={setFullScreen}
        filteredLogs={displayLogs}
        mode={isRunMode ? "run" : "task"}
        scopeLabel={runIdentityLabel}
        onCloseAutoFocus={(event) => {
          const opener = fullscreenOpenerRef.current;
          fullscreenOpenerRef.current = null;
          // Removed or disabled openers (scope change, denial, reload) keep the default behavior.
          if (
            !opener
            || !opener.isConnected
            || opener.matches(":disabled")
            || opener.getAttribute("aria-disabled") === "true"
          ) {
            return;
          }
          event.preventDefault();
          opener.focus();
        }}
      />
    </div>
  );
}
