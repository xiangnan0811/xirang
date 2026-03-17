import React from "react";
import { useTranslation } from "react-i18next";
import { History, Loader2, Pencil, Play, RotateCcw, Square, Terminal, Trash2, Plus } from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import { canCancel, canTrigger } from "@/pages/tasks-page.utils";

import type { TasksViewProps } from "@/pages/tasks-page.utils";

export const TasksGrid = React.memo(function TasksGrid({
  loading,
  filteredTasks,
  pendingAction,
  resetFilters,
  setCreateDialogOpen,
  handleRetry,
  handleCancel,
  handleDelete,
  handleTrigger,
  onEdit,
  onViewHistory,
}: TasksViewProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();

  return (
    <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
      {filteredTasks.map((task) => {
        const status = getTaskStatusMeta(task.status);
        const isPendingAny = pendingAction?.id === task.id;
        const isPendingRetry = pendingAction?.id === task.id && pendingAction.action === "retry";
        const isPendingCancel = pendingAction?.id === task.id && pendingAction.action === "cancel";
        const isPendingDelete = pendingAction?.id === task.id && pendingAction.action === "delete";
        const isPendingTrigger = pendingAction?.id === task.id && pendingAction.action === "trigger";

        return (
          <div
            key={task.id}
            className={cn(
              "interactive-surface flex h-full flex-col gap-2 p-4",
              task.status === "failed" && "border-destructive/35 bg-destructive/10",
              task.status === "running" && "border-info/30 bg-info/5",
              task.status === "warning" && "border-warning/35 bg-warning/10"
            )}
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <p className="font-medium">{task.name || task.policyName}</p>
                <p className="text-xs text-muted-foreground">{t('tasks.taskIdLabel', { id: task.id })}</p>
              </div>
              <div className="flex flex-wrap items-center gap-1.5">
                {!task.cronSpec && (
                  <Badge variant="outline" className="text-[10px] px-1.5 py-0">{t('tasks.typeManual')}</Badge>
                )}
                {task.cronSpec && (
                  <Badge variant="secondary" className="text-[10px] px-1.5 py-0">{t('tasks.typeCron')}</Badge>
                )}
                <Badge variant={status.variant}>{status.label}</Badge>
                {task.verifyStatus && task.verifyStatus !== "none" && (
                  <Badge
                    variant={task.verifyStatus === "passed" ? "success" : "warning"}
                    className="text-[10px] px-1.5 py-0"
                  >
                    {task.verifyStatus === "passed" ? t('tasks.verifyPassed') : task.verifyStatus === "warning" ? t('tasks.verifyWarning') : t('tasks.verifyFailed')}
                  </Badge>
                )}
              </div>
            </div>

            <div className="space-y-0.5 text-sm text-muted-foreground">
              <p>{t('tasks.nodeLabel', { name: task.nodeName })}</p>
              <p>{t('tasks.startedAtLabel', { time: task.startedAt })}</p>
              {task.nextRunAt ? <p>{t('tasks.nextRunAtLabel', { time: task.nextRunAt })}</p> : null}
              {task.lastError ? (
                <p className="break-all text-destructive">{t('tasks.errorLabel', { error: task.lastError })}</p>
              ) : null}
            </div>

            <div className="space-y-1">
              <div className="flex items-center justify-between text-xs text-muted-foreground">
                <span>{t('tasks.columnProgress')}</span>
                <span>{task.progress}%</span>
              </div>
              <div className="h-2 rounded-full bg-muted">
                <div
                  className={cn(
                    "h-2 rounded-full transition-all",
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

            <div className="mt-auto flex flex-wrap-reverse items-center justify-between gap-2 border-t border-border/40 pt-3">
              <div className="flex flex-wrap items-center gap-1">
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
              </div>
              <Button
                size="sm"
                disabled={!canTrigger(task.status) || !!task.dependsOnTaskId || isPendingAny}
                onClick={() => void handleTrigger(task.id)}
              >
                {isPendingTrigger ? <Loader2 className="mr-1 size-4 animate-spin" /> : <Play className="mr-1 size-4" />}
                {t('tasks.trigger')}
              </Button>
            </div>
          </div>
        );
      })}

      {!loading && !filteredTasks.length ? (
        <FilteredEmptyState
          className="md:col-span-2 2xl:col-span-3"
          title={t('tasks.emptyTitle')}
          description={t('tasks.emptyDesc')}
          onReset={resetFilters}
          onCreate={() => setCreateDialogOpen(true)}
          createLabel={t('tasks.emptyCreateLabel')}
          createIcon={Plus}
        />
      ) : null}
    </div>
  );
});
