import React from "react";
import { Loader2, Play, Plus, RotateCcw, Square, Terminal, Trash2 } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { TasksViewProps } from "@/pages/tasks-page.utils";
import { canCancel, canTrigger } from "@/pages/tasks-page.utils";

export const TasksTable = React.memo(function TasksTable({
  loading,
  filteredTasks,
  pendingAction,
  resetFilters,
  setCreateDialogOpen,
  handleRetry,
  handleCancel,
  handleDelete,
  handleTrigger,
}: TasksViewProps) {
  const navigate = useNavigate();

  return (
    <div className="glass-panel overflow-x-auto">
      <table className="min-w-[1100px] text-left text-sm">
        <thead>
          <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th className="px-3 py-2.5">任务</th>
            <th className="px-3 py-2.5">节点</th>
            <th className="px-3 py-2.5">状态</th>
            <th className="px-3 py-2.5">进度</th>
            <th className="px-3 py-2.5">调度</th>
            <th className="px-3 py-2.5">错误</th>
            <th className="px-3 py-2.5 text-right">操作</th>
          </tr>
        </thead>
        <tbody>
          {filteredTasks.length ? (
            filteredTasks.map((task) => {
              const status = getTaskStatusMeta(task.status);
              const isPendingAny = pendingAction?.id === task.id;
              const isPendingRetry = pendingAction?.id === task.id && pendingAction.action === "retry";
              const isPendingCancel = pendingAction?.id === task.id && pendingAction.action === "cancel";
              const isPendingDelete = pendingAction?.id === task.id && pendingAction.action === "delete";
              const isPendingTrigger = pendingAction?.id === task.id && pendingAction.action === "trigger";
              return (
                <tr key={task.id} className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-accent/35">
                  <td className="px-3 py-2.5">
                    <p className="font-medium">{task.policyName}</p>
                    <p className="text-xs text-muted-foreground">ID #{task.id}</p>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">{task.nodeName}</td>
                  <td className="px-3 py-2.5">
                    <Badge variant={status.variant}>{status.label}</Badge>
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="w-36 space-y-1">
                      <p className="text-xs text-muted-foreground">{task.progress}%</p>
                      <div className="h-2 rounded-full bg-muted">
                        <div
                          className={cn(
                            "h-2 rounded-full",
                            task.status === "success"
                              ? "bg-success"
                              : task.status === "failed"
                                ? "bg-destructive"
                                : task.status === "running" || task.status === "retrying"
                                  ? "bg-info"
                                  : "bg-muted-foreground"
                          )}
                          style={{ width: `${task.progress}%` }}
                        />
                      </div>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground">
                    <p>开始：{task.startedAt}</p>
                    <p>下次：{task.nextRunAt ?? "-"}</p>
                  </td>
                  <td className="px-3 py-2.5 text-xs">
                    <span
                      className={cn(
                        "line-clamp-2 break-all",
                        task.lastError ? "text-destructive" : "text-muted-foreground"
                      )}
                    >
                      {task.lastError || "-"}
                    </span>
                  </td>
                  <td className="px-3 py-2.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label="重试任务"
                        disabled={task.status !== "failed" || isPendingAny}
                        onClick={() => void handleRetry(task.id)}
                      >
                        {isPendingRetry ? <Loader2 className="size-4 animate-spin" /> : <RotateCcw className="size-4" />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={`查看任务 #${task.id} 日志`}
                        onClick={() => navigate(`/app/logs?task=${task.id}`)}
                      >
                        <Terminal className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label="取消任务"
                        disabled={!canCancel(task.status) || isPendingAny}
                        onClick={() => void handleCancel(task.id)}
                      >
                        {isPendingCancel ? <Loader2 className="size-4 animate-spin" /> : <Square className="size-4" />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                        aria-label="删除任务"
                        disabled={isPendingAny}
                        onClick={() => void handleDelete(task.id)}
                      >
                        {isPendingDelete ? <Loader2 className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
                      </Button>
                      <Button
                        size="sm"
                        className="ml-2"
                        disabled={!canTrigger(task.status) || isPendingAny}
                        onClick={() => void handleTrigger(task.id)}
                      >
                        {isPendingTrigger ? <Loader2 className="size-4 mr-1 animate-spin" /> : <Play className="size-4 mr-1" />}
                        触发
                      </Button>
                    </div>
                  </td>
                </tr>
              );
            })
          ) : !loading ? (
            <tr>
              <td colSpan={7} className="px-3 py-6">
                <FilteredEmptyState
                  className="py-8"
                  title="当前筛选条件下没有任务"
                  description="可重置筛选条件，或直接新建一个任务。"
                  onReset={resetFilters}
                  onCreate={() => setCreateDialogOpen(true)}
                  createLabel="新建任务"
                  createIcon={Plus}
                />
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
});
