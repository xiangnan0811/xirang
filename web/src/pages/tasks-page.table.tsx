import React from "react";
import { useTranslation } from "react-i18next";
import { History, Loader2, Pause, Pencil, Play, Plus, RotateCcw, SkipForward, Square, Terminal, Trash2 } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { TasksViewProps } from "@/pages/tasks-page.utils";
import { canCancel, canSkipNext, canTrigger } from "@/pages/tasks-page.utils";

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
  handlePause,
  handleResume,
  handleSkipNext,
  onEdit,
  onViewHistory,
  selectedTaskSet,
  allVisibleSelected,
  toggleTaskSelection,
  toggleSelectAllVisible,
}: TasksViewProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();

  return (
    <div className="glass-panel overflow-x-auto">
      <table className="min-w-[1100px] text-left text-sm">
        <thead>
          <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="w-10 px-3 py-2.5">
              <input
                type="checkbox"
                className="size-4 accent-primary rounded-sm"
                checked={allVisibleSelected}
                onChange={(e) => toggleSelectAllVisible(e.target.checked)}
                aria-label={t('tasks.selectAllVisible')}
              />
            </th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnTask')}</th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnNode')}</th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnStatus')}</th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnType')}</th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnProgress')}</th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnSchedule')}</th>
            <th scope="col" className="px-3 py-2.5">{t('tasks.columnError')}</th>
            <th scope="col" className="px-3 py-2.5 text-right">{t('tasks.columnActions')}</th>
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
                <tr key={task.id} className={cn("border-b border-border/60 transition-colors duration-200 ease-out hover:bg-muted/40", selectedTaskSet.has(task.id) && "bg-primary/5", task.enabled === false && "opacity-50")}>
                  <td className="px-3 py-2.5">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary rounded-sm"
                      checked={selectedTaskSet.has(task.id)}
                      onChange={(e) => toggleTaskSelection(task.id, e.target.checked)}
                      aria-label={t('tasks.selectTaskAriaLabel', { name: task.name || task.policyName })}
                    />
                  </td>
                  <td className="px-3 py-2.5">
                    <p className="font-medium">{task.name || task.policyName}</p>
                    <p className="text-xs text-muted-foreground">ID #{task.id}</p>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">{task.nodeName}</td>
                  <td className="px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-1">
                      <Badge variant={status.variant}>{status.label}</Badge>
                      {task.verifyStatus && task.verifyStatus !== "none" && (
                        <Badge
                          variant={task.verifyStatus === "passed" ? "success" : "warning"}
                          className="text-[10px]"
                        >
                          {task.verifyStatus === "passed" ? t('tasks.verifyPassed') : task.verifyStatus === "warning" ? t('tasks.verifyWarning') : t('tasks.verifyFailed')}
                        </Badge>
                      )}
                      {task.enabled === false && (
                        <Badge variant="secondary" className="text-[10px]">
                          {t('tasks.paused')}
                        </Badge>
                      )}
                      {task.skipNext && (
                        <Badge variant="outline" className="text-[10px]">
                          {t('tasks.skipNextBadge')}
                        </Badge>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2.5">
                    <Badge variant={task.cronSpec ? "secondary" : "outline"} className="text-[10px]">
                      {task.cronSpec ? t('tasks.typeCron') : t('tasks.typeManual')}
                    </Badge>
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
                                  : task.status === "warning"
                                    ? "bg-warning"
                                    : "bg-muted-foreground"
                          )}
                          style={{ width: `${task.progress}%` }}
                        />
                      </div>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-xs text-muted-foreground">
                    <p>{t('tasks.startedAtLabel', { time: task.startedAt })}</p>
                    <p>{t('tasks.nextRunAtLabel', { time: task.nextRunAt ?? "-" })}</p>
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
                        aria-label={t('tasks.retryAriaLabel')}
                        disabled={task.status !== "failed" || isPendingAny}
                        onClick={() => void handleRetry(task.id)}
                      >
                        {isPendingRetry ? <Loader2 className="size-4 animate-spin" /> : <RotateCcw className="size-4" />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t('tasks.viewLogsAriaLabel', { id: task.id })}
                        onClick={() => navigate(`/app/logs?task=${task.id}`)}
                      >
                        <Terminal className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t('tasks.viewHistoryAriaLabel', { id: task.id })}
                        onClick={() => onViewHistory(task)}
                      >
                        <History className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t('tasks.editAriaLabel')}
                        disabled={isPendingAny}
                        onClick={() => onEdit(task)}
                      >
                        <Pencil className="size-4" />
                      </Button>
                      {task.enabled !== false ? (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                          aria-label={t('tasks.pause')}
                          disabled={isPendingAny}
                          onClick={() => void handlePause(task.id)}
                        >
                          <Pause className="size-4" />
                        </Button>
                      ) : (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8 text-success hover:bg-success/10"
                          aria-label={t('tasks.resume')}
                          disabled={isPendingAny}
                          onClick={() => void handleResume(task.id)}
                        >
                          <Play className="size-4" />
                        </Button>
                      )}
                      {canSkipNext(task) && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                          aria-label={t('tasks.skipNext')}
                          disabled={isPendingAny}
                          onClick={() => void handleSkipNext(task.id)}
                        >
                          <SkipForward className="size-4" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t('tasks.cancelAriaLabel')}
                        disabled={!canCancel(task.status) || isPendingAny}
                        onClick={() => void handleCancel(task.id)}
                      >
                        {isPendingCancel ? <Loader2 className="size-4 animate-spin" /> : <Square className="size-4" />}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                        aria-label={t('tasks.deleteAriaLabel')}
                        disabled={isPendingAny}
                        onClick={() => void handleDelete(task.id)}
                      >
                        {isPendingDelete ? <Loader2 className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
                      </Button>
                      <Button
                        size="sm"
                        className="ml-2"
                        disabled={!canTrigger(task) || !!task.dependsOnTaskId || isPendingAny}
                        title={task.enabled === false ? t('tasks.pausedTooltip') : undefined}
                        onClick={() => void handleTrigger(task.id)}
                      >
                        {isPendingTrigger ? <Loader2 className="size-4 mr-1 animate-spin" /> : <Play className="size-4 mr-1" />}
                        {t('tasks.trigger')}
                      </Button>
                    </div>
                  </td>
                </tr>
              );
            })
          ) : !loading ? (
            <tr>
              <td colSpan={9} className="px-3 py-6">
                <FilteredEmptyState
                  className="py-8"
                  title={t('tasks.emptyTitle')}
                  description={t('tasks.emptyDesc')}
                  onReset={resetFilters}
                  onCreate={() => setCreateDialogOpen(true)}
                  createLabel={t('tasks.emptyCreateLabel')}
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
