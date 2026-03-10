import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Download,
  Maximize2,
  TriangleAlert,
} from "lucide-react";
import { useOutletContext, useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { useLiveLogs } from "@/hooks/use-live-logs";
import { usePersistentState } from "@/hooks/use-persistent-state";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { AppSelect } from "@/components/ui/app-select";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { LoadingState } from "@/components/ui/loading-state";
import { SearchInput } from "@/components/ui/search-input";
import { cn } from "@/lib/utils";
import type { LogEvent, TaskStatus } from "@/types/domain";

const selectedNodeStorageKey = "xirang.logs.selected-node";
const selectedTaskStorageKey = "xirang.logs.selected-task";
const keywordStorageKey = "xirang.logs.keyword";

const splitByErrorCodeRegex = /(XR-[A-Z]+-\d+)/g;
const singleErrorCodeRegex = /^XR-[A-Z]+-\d+$/;

function isTerminalTaskStatus(status?: TaskStatus) {
  return status === "success" || status === "failed" || status === "canceled";
}

function isActiveTaskStatus(status?: TaskStatus) {
  return status === "running" || status === "retrying";
}

function highlightErrorCode(message: string) {
  const parts = message.split(splitByErrorCodeRegex);
  return parts.map((part, idx) =>
    singleErrorCodeRegex.test(part) ? (
      <span
        key={`${part}-${idx}`}
        className="rounded border border-destructive/35 bg-destructive/20 px-1 text-destructive"
      >
        {part}
      </span>
    ) : (
      <span key={`${part}-${idx}`}>{part}</span>
    )
  );
}

function getLevelClass(level: "info" | "warn" | "error") {
  if (level === "error") {
    return "text-destructive";
  }
  if (level === "warn") {
    return "text-warning";
  }
  return "text-success";
}

function parseToMillis(log: LogEvent) {
  if (Number.isFinite(log.timestampMs)) return log.timestampMs as number;
  const timestamp = log.timestamp;
  const parsed = Date.parse(timestamp);
  if (Number.isNaN(parsed)) {
    return 0;
  }
  return parsed;
}

function formatLogTime(ts: string) {
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return ts;
  const pad = (n: number) => n.toString().padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

function minLogId(logs: LogEvent[]) {
  let min = Number.MAX_SAFE_INTEGER;
  for (const log of logs) {
    if (log.logId && log.logId < min) {
      min = log.logId;
    }
  }
  return min === Number.MAX_SAFE_INTEGER ? null : min;
}

export function LogsPage() {
  const { token } = useAuth();
  const { tasks, nodes, fetchTaskLogs, refreshTask } = useOutletContext<ConsoleOutletContext>();
  const [searchParams, setSearchParams] = useSearchParams();

  const initialTask = searchParams.get("task") ?? "all";
  const initialNode = searchParams.get("node") ?? "all";
  const initialKeyword = searchParams.get("q") ?? "";

  const [selectedNode, setSelectedNode] =
    usePersistentState<string>(selectedNodeStorageKey, initialNode);
  const [selectedTask, setSelectedTask] =
    usePersistentState<string>(selectedTaskStorageKey, initialTask);
  const [keyword, setKeyword] =
    usePersistentState<string>(keywordStorageKey, initialKeyword);
  const [historyLogs, setHistoryLogs] = useState<LogEvent[]>([]);
  const [historyCursor, setHistoryCursor] = useState<number | null>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyPaging, setHistoryPaging] = useState(false);
  const [fullScreen, setFullScreen] = useState(false);

  const focusedTaskID = selectedTask !== "all" ? Number(selectedTask) : undefined;
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
    }
    if (nextNode !== null) {
      setSelectedNode(nextNode);
    }
    if (nextKeyword !== null) {
      setKeyword(nextKeyword);
    }
  }, [searchParams, setKeyword, setSelectedNode, setSelectedTask]);

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
          log.nodeName ?? (log.taskId ? taskNodeMap.get(log.taskId) : "系统") ?? "系统",
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
  }, [historyLogs, logs, tasks]);

  const filteredLogs = useMemo(() => {
    const keywordValue = keyword.trim().toLowerCase();
    return mergedLogs.filter((log) => {
      if (selectedNode !== "all" && log.nodeName !== selectedNode) {
        return false;
      }
      if (selectedTask !== "all" && String(log.taskId ?? "") !== selectedTask) {
        return false;
      }
      if (!keywordValue) {
        return true;
      }
      const text = `${log.nodeName ?? "系统"} ${log.taskId ?? "-"} ${log.level} ${log.message} ${log.errorCode ?? ""
        }`
        .toLowerCase()
        .trim();
      return text.includes(keywordValue);
    });
  }, [keyword, mergedLogs, selectedNode, selectedTask]);

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
      (log) => log.taskId === focusedTaskNumber && isTerminalTaskStatus(log.status)
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
      (log) => log.taskId === focusedTaskNumber && isTerminalTaskStatus(log.status)
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

  const runningTasks = tasks.filter(
    (task) => task.status === "running" || task.status === "retrying"
  );
  const progressValue = focusedTask
    ? focusedTask.progress
    : Math.round(
      runningTasks.reduce((sum, task) => sum + task.progress, 0) /
      Math.max(1, runningTasks.length)
    );
  const normalizedProgress = Math.min(100, Math.max(0, progressValue || 0));

  const syncSearchParams = (patch: { task?: string; node?: string; q?: string }) => {
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
        const taskText = log.taskId ? `任务#${log.taskId}` : "全局";
        return `[${formatLogTime(log.timestamp)}] [${log.level.toUpperCase()}] [${log.nodeName ?? "系统"
          }] [${taskText}] ${log.message}`;
      })
      .join("\n");

    const blob = new Blob([content || "当前无日志可导出"], {
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
      <Card className="border-border/75">
        <CardContent className="space-y-4 pt-6">
          <FilterPanel sticky={false} className="flex flex-wrap items-center gap-3">
            <AppSelect
              containerClassName="w-[140px]"
              aria-label="日志节点筛选"
              value={selectedNode}
              onChange={(event) => {
                const value = event.target.value;
                setSelectedNode(value);
                syncSearchParams({ node: value });
              }}
            >
              <option value="all">全部节点</option>
              {nodes.map((node) => (
                <option key={node.id} value={node.name}>
                  {node.name}
                </option>
              ))}
            </AppSelect>

            <AppSelect
              containerClassName="w-[160px]"
              aria-label="日志任务筛选"
              value={selectedTask}
              onChange={(event) => {
                const nextTask = event.target.value;
                setSelectedTask(nextTask);
                syncSearchParams({ task: nextTask });
              }}
            >
              <option value="all">全部任务</option>
              {tasks.map((task) => (
                <option key={task.id} value={String(task.id)}>
                  #{task.id} {task.policyName}
                </option>
              ))}
            </AppSelect>

            <div className="flex items-center gap-3 pr-2 border-r border-border/50 hidden sm:flex">
              <div
                className={cn(
                  "size-2.5 rounded-full shadow-sm",
                  connected ? "bg-success" : "bg-muted"
                )}
                title={connected ? "已连接" : "未连接"}
              />

              {(focusedTask || runningTasks.length > 0) ? (
                <div className="flex items-center gap-2" title="任务执行进度">
                  <span className="text-[11px] font-medium text-foreground/80">
                    进度 {Number.isFinite(progressValue) ? progressValue : 0}%
                  </span>
                  <div
                    className="w-16 h-1.5 rounded-full bg-muted overflow-hidden"
                    role="progressbar"
                    aria-label="日志任务进度"
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
                            : "bg-success"
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
                aria-label="日志关键词筛选"
                value={keyword}
                placeholder="搜索"
                onChange={(event) => {
                  const value = event.target.value;
                  setKeyword(value);
                  syncSearchParams({ q: value });
                }}
              />

              <Button size="sm" variant="outline" className="w-[80px]" onClick={resetFilters}>
                重置
              </Button>

              <Button variant="outline" size="sm" className="w-[80px]" onClick={exportAsText}>
                <Download className="mr-1 size-3.5" />
                导出
              </Button>

              <Button
                variant="outline"
                size="icon"
                className="size-8"
                aria-label="全屏查看日志"
                title="全屏"
                onClick={() => setFullScreen(true)}
              >
                <Maximize2 className="size-3.5" />
              </Button>
            </div>
          </FilterPanel>

          <FilterSummary filtered={filteredLogs.length} total={mergedLogs.length} unit="条日志" />

          {connectionWarning ? (
            <p className="rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning">
              {connectionWarning}
            </p>
          ) : null}

          {focusedTask?.errorCode ? (
            <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
              <TriangleAlert className="mr-1 inline size-4" />
              当前任务错误码：{focusedTask.errorCode}
            </div>
          ) : null}

          <div className="space-y-3">
            {historyLoading && !filteredLogs.length ? (
              <LoadingState
                title="日志加载中"
                description="正在拉取历史日志与实时流式增量..."
                rows={4}
              />
            ) : (
              <div
                ref={terminalRef}
                className={cn(
                  "terminal-surface thin-scrollbar overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-[13px]",
                  "h-[62vh]"
                )}
                role="log"
                aria-live="polite"
                aria-relevant="additions text"
                aria-label={`日志终端，共 ${filteredLogs.length} 条日志`}
              >
                {filteredLogs.length > 0 ? (
                  <div className="px-1">
                    {filteredLogs.map((log) => (
                      <div
                        key={log.logId ? `line-${log.logId}` : log.id}
                        className="mb-0.5 flex flex-col md:flex-row md:items-start gap-1.5 border-b terminal-group-row hover:bg-white/5 py-1.5 transition-colors"
                      >
                        <div className="flex items-center gap-3 shrink-0 md:w-[260px]">
                          <span className="terminal-time opacity-60 text-[11px] md:text-[12px]">{formatLogTime(log.timestamp)}</span>
                          <span className={cn("font-medium w-12 text-[11px] md:text-[12px]", getLevelClass(log.level))}>
                            {log.level.toUpperCase()}
                          </span>
                        </div>
                        <div className="break-all flex-1 leading-relaxed">
                          <span className="terminal-node-chip mr-2 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] md:text-[11px]">
                            {log.nodeName ?? "系统"}
                            {log.taskId ? <span className="ml-1 opacity-70">| #{log.taskId}</span> : null}
                          </span>
                          <span>{highlightErrorCode(log.message)}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="px-2 py-10">
                    <EmptyState
                      className="terminal-empty"
                      title="当前筛选条件下暂无日志输出"
                      description="可尝试切换节点/任务或重置筛选条件。"
                      action={(
                        <Button size="sm" variant="outline" onClick={resetFilters}>
                          重置筛选
                        </Button>
                      )}
                    />
                  </div>
                )}
              </div>
            )}

            {focusedTaskNumber ? (
              <div className="flex items-center justify-between gap-2 rounded-xl border border-border/75 bg-background/60 px-3 py-2">
                <p className="text-xs text-muted-foreground">
                  历史回溯：{historyLogs.length} 条
                  {historyCursor ? `，最早游标 #${historyCursor}` : "，已到最早"}
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!historyCursor || historyPaging}
                  onClick={() => void loadMoreHistory()}
                >
                  {historyPaging ? "加载中..." : "加载更早日志"}
                </Button>
              </div>
            ) : null}
          </div>

        </CardContent>
      </Card>

      <Dialog open={fullScreen} onOpenChange={setFullScreen}>
        <DialogContent size="lg" className="max-h-[90vh] md:max-w-[calc(100vw-64px)] flex flex-col">
          <DialogHeader>
            <DialogTitle>日志终端（共 {filteredLogs.length} 条）</DialogTitle>
            <DialogCloseButton />
          </DialogHeader>
          <div className="flex-1 overflow-y-auto px-6 pb-6">
            <div
              className="terminal-surface thin-scrollbar overflow-auto rounded-xl p-3 font-mono text-[12px] md:text-[13px] h-[calc(90vh-140px)]"
              role="log"
              aria-live="polite"
              aria-relevant="additions text"
              aria-label={`全屏日志终端，共 ${filteredLogs.length} 条日志`}
            >
              {filteredLogs.length > 0 ? (
                <div className="px-1">
                  {filteredLogs.map((log) => (
                    <div
                      key={log.logId ? `fs-${log.logId}` : `fs-${log.id}`}
                      className="mb-0.5 flex flex-col md:flex-row md:items-start gap-1.5 border-b terminal-group-row hover:bg-white/5 py-1.5 transition-colors"
                    >
                      <div className="flex items-center gap-3 shrink-0 md:w-[260px]">
                        <span className="terminal-time opacity-60 text-[11px] md:text-[12px]">{formatLogTime(log.timestamp)}</span>
                        <span className={cn("font-medium w-12 text-[11px] md:text-[12px]", getLevelClass(log.level))}>
                          {log.level.toUpperCase()}
                        </span>
                      </div>
                      <div className="break-all flex-1 leading-relaxed">
                        <span className="terminal-node-chip mr-2 inline-flex items-center rounded px-1.5 py-0.5 text-[10px] md:text-[11px]">
                          {log.nodeName ?? "系统"}
                          {log.taskId ? <span className="ml-1 opacity-70">| #{log.taskId}</span> : null}
                        </span>
                        <span>{highlightErrorCode(log.message)}</span>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="px-2 py-10">
                  <EmptyState
                    className="terminal-empty"
                    title="当前筛选条件下暂无日志输出"
                    description="可尝试切换节点/任务或重置筛选条件。"
                  />
                </div>
              )}
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
