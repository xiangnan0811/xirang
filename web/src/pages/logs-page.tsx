import { useEffect, useMemo, useRef, useState } from "react";
import { Download, Maximize2, Minimize2, RefreshCw, Search, TriangleAlert } from "lucide-react";
import { useOutletContext, useSearchParams } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { useLiveLogs } from "@/hooks/use-live-logs";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import type { LogEvent } from "@/types/domain";

const splitByErrorCodeRegex = /(XR-[A-Z]+-\d+)/g;
const singleErrorCodeRegex = /^XR-[A-Z]+-\d+$/;

function highlightErrorCode(message: string) {
  const parts = message.split(splitByErrorCodeRegex);
  return parts.map((part, idx) =>
    singleErrorCodeRegex.test(part) ? (
      <span key={`${part}-${idx}`} className="rounded bg-red-600/25 px-1 text-red-300">
        {part}
      </span>
    ) : (
      <span key={`${part}-${idx}`}>{part}</span>
    )
  );
}

function getLevelClass(level: "info" | "warn" | "error") {
  if (level === "error") {
    return "text-red-400";
  }
  if (level === "warn") {
    return "text-amber-300";
  }
  return "text-emerald-300";
}

function distanceBetweenTouches(touches: React.TouchList) {
  const first = touches.item(0);
  const second = touches.item(1);
  if (!first || !second) {
    return 0;
  }
  return Math.hypot(first.clientX - second.clientX, first.clientY - second.clientY);
}

function parseToMillis(timestamp: string) {
  const parsed = Date.parse(timestamp);
  if (Number.isNaN(parsed)) {
    return 0;
  }
  return parsed;
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
  const { tasks, nodes, fetchTaskLogs } = useOutletContext<ConsoleOutletContext>();
  const [searchParams, setSearchParams] = useSearchParams();

  const initialTask = searchParams.get("task") ?? "all";
  const initialNode = searchParams.get("node") ?? "all";
  const initialKeyword = searchParams.get("q") ?? "";

  const [selectedNode, setSelectedNode] = useState<string>(initialNode);
  const [selectedTask, setSelectedTask] = useState<string>(initialTask);
  const [keyword, setKeyword] = useState(initialKeyword);
  const [historyLogs, setHistoryLogs] = useState<LogEvent[]>([]);
  const [historyCursor, setHistoryCursor] = useState<number | null>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyPaging, setHistoryPaging] = useState(false);
  const [fontScale, setFontScale] = useState(1);
  const [fullScreen, setFullScreen] = useState(false);
  const [shortcutEcho, setShortcutEcho] = useState<string>("");

  const focusedTaskID = selectedTask !== "all" ? Number(selectedTask) : undefined;
  const focusedTaskNumber = focusedTaskID && Number.isFinite(focusedTaskID) && focusedTaskID > 0 ? focusedTaskID : undefined;

  const { connected, logs, connectionWarning, cursorLogId } = useLiveLogs(token, {
    taskId: focusedTaskNumber
  });

  const terminalRef = useRef<HTMLDivElement | null>(null);
  const pinchDistanceRef = useRef(0);
  const historyRequestIdRef = useRef(0);

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
  }, [fetchTaskLogs, selectedTask]);

  const mergedLogs = useMemo(() => {
    const taskNodeMap = new Map(tasks.map((task) => [task.id, task.nodeName]));
    const dedup = new Map<string, LogEvent>();

    for (const log of [...logs, ...historyLogs]) {
      const enriched: LogEvent = {
        ...log,
        nodeName: log.nodeName ?? (log.taskId ? taskNodeMap.get(log.taskId) : "系统") ?? "系统"
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
      return parseToMillis(second.timestamp) - parseToMillis(first.timestamp);
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
      const text = `${log.nodeName ?? "系统"} ${log.taskId ?? "-"} ${log.level} ${log.message} ${log.errorCode ?? ""}`
        .toLowerCase()
        .trim();
      return text.includes(keywordValue);
    });
  }, [keyword, mergedLogs, selectedNode, selectedTask]);

  const groupedLogs = useMemo(() => {
    const groups = new Map<string, { key: string; nodeName: string; taskId: number | null; logs: LogEvent[]; latestAt: number }>();

    for (const log of filteredLogs) {
      const nodeName = log.nodeName ?? "系统";
      const taskId = log.taskId ?? null;
      const key = `${nodeName}::${taskId ?? "global"}`;
      const existing = groups.get(key);
      const currentAt = parseToMillis(log.timestamp);
      if (!existing) {
        groups.set(key, {
          key,
          nodeName,
          taskId,
          logs: [log],
          latestAt: currentAt
        });
        continue;
      }
      existing.logs.push(log);
      existing.latestAt = Math.max(existing.latestAt, currentAt);
    }

    return [...groups.values()]
      .map((group) => ({
        ...group,
        logs: group.logs.sort((first, second) => {
          const idGap = (second.logId ?? 0) - (first.logId ?? 0);
          if (idGap !== 0) {
            return idGap;
          }
          return parseToMillis(second.timestamp) - parseToMillis(first.timestamp);
        })
      }))
      .sort((first, second) => second.latestAt - first.latestAt);
  }, [filteredLogs]);

  const focusedTask = selectedTask === "all" ? null : tasks.find((task) => String(task.id) === selectedTask);

  const runningTasks = tasks.filter((task) => task.status === "running" || task.status === "retrying");
  const progressValue = focusedTask
    ? focusedTask.progress
    : Math.round(runningTasks.reduce((sum, task) => sum + task.progress, 0) / Math.max(1, runningTasks.length));

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

  const exportAsText = () => {
    const content = filteredLogs
      .map((log) => {
        const taskText = log.taskId ? `任务#${log.taskId}` : "全局";
        return `[${log.timestamp}] [${log.level.toUpperCase()}] [${log.nodeName ?? "系统"}] [${taskText}] ${log.message}`;
      })
      .join("\n");

    const blob = new Blob([content || "当前无日志可导出"], { type: "text/plain;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    const suffix = new Date().toISOString().replace(/[:.]/g, "-");
    anchor.href = url;
    anchor.download = `xirang-logs-${suffix}.txt`;
    document.body.appendChild(anchor);
    anchor.click();
    document.body.removeChild(anchor);
    URL.revokeObjectURL(url);
  };

  const loadMoreHistory = async () => {
    if (!focusedTaskNumber || !historyCursor) {
      return;
    }
    setHistoryPaging(true);
    try {
      const rows = await fetchTaskLogs(focusedTaskNumber, {
        beforeId: historyCursor,
        limit: 120
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

  const handleTouchStart: React.TouchEventHandler<HTMLDivElement> = (event) => {
    if (event.touches.length !== 2) {
      return;
    }
    pinchDistanceRef.current = distanceBetweenTouches(event.touches);
  };

  const handleTouchMove: React.TouchEventHandler<HTMLDivElement> = (event) => {
    if (event.touches.length !== 2 || pinchDistanceRef.current === 0) {
      return;
    }
    const nextDistance = distanceBetweenTouches(event.touches);
    const ratio = nextDistance / pinchDistanceRef.current;
    setFontScale((prev) => Math.min(1.6, Math.max(0.75, Number((prev * ratio).toFixed(2)))));
    pinchDistanceRef.current = nextDistance;
  };

  return (
    <Card className={cn("animate-fade-in", fullScreen && "fixed inset-2 z-50 m-0")}> 
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle className="text-base">实时日志与监控终端</CardTitle>
          <div className="flex items-center gap-2">
            <Badge variant={connected ? "success" : "outline"}>{connected ? "已连接" : "未连接"}</Badge>
            <Button variant="outline" size="sm" onClick={exportAsText}>
              <Download className="mr-1 size-4" />
              导出 TXT
            </Button>
            <Button variant="outline" size="sm" onClick={() => setFullScreen((prev) => !prev)}>
              {fullScreen ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
            </Button>
          </div>
        </div>
      </CardHeader>

      <CardContent className="space-y-3">
        <div className="grid gap-2 md:grid-cols-4">
          <select
            className="h-10 rounded-md border bg-background px-3 text-sm"
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
          </select>

          <select
            className="h-10 rounded-md border bg-background px-3 text-sm"
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
          </select>

          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              value={keyword}
              placeholder="关键词过滤（错误码/内容）"
              onChange={(event) => {
                const value = event.target.value;
                setKeyword(value);
                syncSearchParams({ q: value });
              }}
            />
          </div>

          <div className="flex items-center gap-2 rounded-md border px-3 text-xs text-muted-foreground">
            <RefreshCw className={cn("size-3.5", (historyLoading || historyPaging) && "animate-spin")} />
            当前筛选 {filteredLogs.length} 条 · 游标#{cursorLogId || "-"}
          </div>
        </div>

        {connectionWarning ? (
          <p className="rounded-md bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
            {connectionWarning}
          </p>
        ) : null}

        {focusedTask?.errorCode ? (
          <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-400">
            <TriangleAlert className="mr-1 inline size-4" />
            当前任务错误码：{focusedTask.errorCode}
          </div>
        ) : null}

        <div className="rounded-lg border p-3">
          <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
            <span>高对比度执行进度</span>
            <span>{Number.isFinite(progressValue) ? progressValue : 0}%</span>
          </div>
          <div className="h-3 rounded-full bg-muted">
            <div
              className="h-3 rounded-full bg-gradient-to-r from-emerald-500 via-lime-400 to-cyan-400"
              style={{ width: `${Math.min(100, Math.max(0, progressValue || 0))}%` }}
            />
          </div>
        </div>

        <div
          ref={terminalRef}
          className="terminal-surface h-[58vh] overflow-auto rounded-lg p-3 font-mono text-[12px] text-slate-100"
          style={{ fontSize: `${12 * fontScale}px` }}
          onTouchStart={handleTouchStart}
          onTouchMove={handleTouchMove}
        >
          {groupedLogs.map((group) => (
            <section key={group.key} className="mb-3 rounded border border-slate-700/70 bg-slate-900/40">
              <div className="border-b border-slate-700/70 px-3 py-2 text-[11px] text-slate-300">
                节点：{group.nodeName} · {group.taskId ? `任务 #${group.taskId}` : "系统日志"}
              </div>
              <div className="px-2 py-1">
                {group.logs.map((log) => (
                  <div
                    key={log.logId ? `line-${log.logId}` : log.id}
                    className="mb-1 grid grid-cols-[92px_52px_1fr] gap-2 border-b border-slate-800/80 py-1 text-[11px] md:grid-cols-[130px_90px_1fr] md:text-[12px]"
                  >
                    <span className="text-slate-400">{log.timestamp}</span>
                    <span className={getLevelClass(log.level)}>{log.level.toUpperCase()}</span>
                    <span>
                      <span className="mr-2 rounded bg-slate-700/70 px-1 text-[10px] text-slate-300 md:text-[11px]">
                        {log.nodeName ?? "系统"}
                      </span>
                      {highlightErrorCode(log.message)}
                    </span>
                  </div>
                ))}
              </div>
            </section>
          ))}
          {!groupedLogs.length ? <p className="text-slate-400">当前筛选条件下暂无日志输出...</p> : null}
        </div>

        {focusedTaskNumber ? (
          <div className="flex items-center justify-between gap-2 rounded-lg border px-3 py-2">
            <p className="text-xs text-muted-foreground">
              历史回溯：{historyLogs.length} 条{historyCursor ? `，最早游标 #${historyCursor}` : "，已到最早"}
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

        <div className="md:hidden">
          <p className="mb-2 text-xs text-muted-foreground">虚拟快捷键（移动端终端增强）</p>
          <div className="grid grid-cols-4 gap-2">
            {["Ctrl", "C", "Esc", "全屏"].map((key) => (
              <Button
                key={key}
                size="sm"
                variant="outline"
                onClick={() => {
                  if (key === "全屏") {
                    setFullScreen((prev) => !prev);
                    return;
                  }
                  setShortcutEcho(`已发送快捷键：${key}`);
                }}
              >
                {key}
              </Button>
            ))}
          </div>
          {shortcutEcho ? <p className="mt-2 text-xs text-emerald-400">{shortcutEcho}</p> : null}
          <p className="mt-1 text-[11px] text-muted-foreground">支持双指缩放日志字体（Pinch-to-zoom）</p>
        </div>
      </CardContent>
    </Card>
  );
}
