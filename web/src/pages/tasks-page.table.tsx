import React from "react";
import { useTranslation } from "react-i18next";
import {
  ChevronDown,
  ChevronRight,
  GitFork,
  History,
  Loader2,
  Pause,
  Pencil,
  Play,
  Plus,
  RotateCcw,
  Square,
  Terminal,
  Trash2,
} from "lucide-react";
import { useNavigate } from "react-router-dom";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { TasksViewProps } from "@/pages/tasks-page.utils";
import { buildChainParentMap, canCancel, canTrigger } from "@/pages/tasks-page.utils";

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
  onEdit,
  onViewHistory,
  selectedTaskSet,
  allVisibleSelected,
  toggleTaskSelection,
  toggleSelectAllVisible,
  expandedChains,
  onToggleChain,
}: TasksViewProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();

  // Build chain parent map to know which tasks are parents
  const chainParentMap = buildChainParentMap(filteredTasks);

  // Determine which tasks to show (hide child rows whose parent is collapsed)
  const visibleTasks = filteredTasks.filter((task) => {
    if (!task.dependsOnTaskId) return true;
    // Check if any parent in the chain is expanded
    const parentId = task.dependsOnTaskId;
    const parent = filteredTasks.find((t) => t.id === parentId);
    if (!parent) return true;
    // Use dependsOnTaskId as the "chain key" for folding
    const chainKey = String(parentId);
    return expandedChains?.has(chainKey) ?? true;
  });

  return (
    <div className="rounded-lg border border-border bg-card overflow-x-auto">
      <table className="min-w-[1100px] text-left text-sm">
        <thead>
          <tr className="border-b border-border bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="w-10 px-3 py-2.5">
              <input
                type="checkbox"
                className="size-4 accent-primary rounded-sm"
                checked={allVisibleSelected}
                onChange={(e) => toggleSelectAllVisible(e.target.checked)}
                aria-label={t("tasks.selectAllVisible")}
              />
            </th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnTask")}</th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnNode")}</th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnStatus")}</th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnType")}</th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnProgress")}</th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnSchedule")}</th>
            <th scope="col" className="px-3 py-2.5">{t("tasks.columnError")}</th>
            <th scope="col" className="px-3 py-2.5 text-right">{t("tasks.columnActions")}</th>
          </tr>
        </thead>
        <tbody>
          {visibleTasks.length ? (
            visibleTasks.map((task) => {
              const status = getTaskStatusMeta(task.status);
              const isPendingAny = pendingAction?.id === task.id;
              const isPendingRetry = pendingAction?.id === task.id && pendingAction.action === "retry";
              const isPendingCancel = pendingAction?.id === task.id && pendingAction.action === "cancel";
              const isPendingDelete = pendingAction?.id === task.id && pendingAction.action === "delete";
              const isPendingTrigger = pendingAction?.id === task.id && pendingAction.action === "trigger";

              const isParent = chainParentMap.has(task.id);
              const isChild = Boolean(task.dependsOnTaskId);
              const chainKey = String(task.id);
              const isExpanded = expandedChains?.has(chainKey) ?? true;
              const isRunning = task.status === "running" || task.status === "retrying";

              return (
                <tr
                  key={task.id}
                  className={cn(
                    "group border-b border-border transition-colors duration-200 ease-out hover:bg-muted/40",
                    selectedTaskSet.has(task.id) && "bg-primary/5",
                    task.enabled === false && "opacity-50",
                    isChild && "bg-muted/20"
                  )}
                >
                  <td className="px-3 py-2.5">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary rounded-sm"
                      checked={selectedTaskSet.has(task.id)}
                      onChange={(e) => toggleTaskSelection(task.id, e.target.checked)}
                      aria-label={t("tasks.selectTaskAriaLabel", { name: task.name || task.policyName })}
                    />
                  </td>
                  <td className="px-3 py-2.5">
                    <div className={cn("flex items-center gap-1.5", isChild && "pl-6")}>
                      {/* Running pulse indicator */}
                      {isRunning && (
                        <span
                          className="pulse-online size-2 shrink-0 rounded-full"
                          aria-label={t("tasks.statusRunning")}
                        />
                      )}
                      {/* Chain expand/collapse toggle for parent rows */}
                      {isParent && onToggleChain ? (
                        <button
                          type="button"
                          className="shrink-0 text-muted-foreground hover:text-foreground"
                          onClick={() => onToggleChain(chainKey)}
                          aria-label={isExpanded ? t("tasks.collapseChain") : t("tasks.expandChain")}
                        >
                          {isExpanded ? (
                            <ChevronDown className="size-3.5" />
                          ) : (
                            <ChevronRight className="size-3.5" />
                          )}
                        </button>
                      ) : isParent ? (
                        <GitFork className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                      ) : null}
                      <div>
                        <p className="font-medium">{task.name || task.policyName}</p>
                        <p className="text-xs text-muted-foreground">ID #{task.id}</p>
                      </div>
                    </div>
                  </td>
                  <td className="px-3 py-2.5 text-muted-foreground">{task.nodeName}</td>
                  <td className="px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-1">
                      <Badge tone={status.variant}>{status.label}</Badge>
                      {task.verifyStatus && task.verifyStatus !== "none" && (
                        <Badge
                          tone={task.verifyStatus === "passed" ? "success" : "warning"}
                          className="text-[10px]"
                        >
                          {task.verifyStatus === "passed"
                            ? t("tasks.verifyPassed")
                            : task.verifyStatus === "warning"
                              ? t("tasks.verifyWarning")
                              : t("tasks.verifyFailed")}
                        </Badge>
                      )}
                      {task.enabled === false && (
                        <Badge tone="neutral" className="text-[10px]">
                          {t("tasks.paused")}
                        </Badge>
                      )}
                      {task.skipNext && (
                        <Badge tone="neutral" className="text-[10px]">
                          {t("tasks.skipNextBadge")}
                        </Badge>
                      )}
                    </div>
                  </td>
                  <td className="px-3 py-2.5">
                    <Badge tone="neutral" className="text-[10px]">
                      {task.cronSpec ? t("tasks.typeCron") : t("tasks.typeManual")}
                    </Badge>
                  </td>
                  <td className="px-3 py-2.5">
                    <div className="w-36 space-y-1">
                      <p className="text-xs text-muted-foreground">{task.progress}%</p>
                      <div
                        className="h-2 rounded-full bg-muted"
                        role="progressbar"
                        aria-valuenow={task.progress}
                        aria-valuemin={0}
                        aria-valuemax={100}
                        aria-label={`${task.progress}%`}
                      >
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
                    <p>{t("tasks.startedAtLabel", { time: task.startedAt })}</p>
                    <p>{t("tasks.nextRunAtLabel", { time: task.nextRunAt ?? "-" })}</p>
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
                      {/* Retry button: hidden until row is hovered (for failed rows) */}
                      <Button
                        variant="ghost"
                        size="icon"
                        className={cn(
                          "size-8 text-muted-foreground hover:bg-accent hover:text-foreground transition-opacity",
                          task.status === "failed"
                            ? "opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                            : undefined
                        )}
                        aria-label={t("tasks.retryAriaLabel")}
                        disabled={task.status !== "failed" || isPendingAny}
                        onClick={() => void handleRetry(task.id)}
                      >
                        {isPendingRetry ? (
                          <Loader2 className="size-4 animate-spin" />
                        ) : (
                          <RotateCcw className="size-4" />
                        )}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("tasks.viewLogsAriaLabel", { id: task.id })}
                        onClick={() => navigate(`/app/logs?task=${task.id}`)}
                      >
                        <Terminal className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("tasks.viewHistoryAriaLabel", { id: task.id })}
                        onClick={() => onViewHistory(task)}
                      >
                        <History className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("tasks.editAriaLabel")}
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
                          aria-label={t("tasks.pause")}
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
                          aria-label={t("tasks.resume")}
                          disabled={isPendingAny}
                          onClick={() => void handleResume(task.id)}
                        >
                          <Play className="size-4" />
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-muted-foreground hover:bg-accent hover:text-foreground"
                        aria-label={t("tasks.cancelAriaLabel")}
                        disabled={!canCancel(task.status) || isPendingAny}
                        onClick={() => void handleCancel(task.id)}
                      >
                        {isPendingCancel ? (
                          <Loader2 className="size-4 animate-spin" />
                        ) : (
                          <Square className="size-4" />
                        )}
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-8 text-destructive/80 hover:bg-destructive/10 hover:text-destructive"
                        aria-label={t("tasks.deleteAriaLabel")}
                        disabled={isPendingAny}
                        onClick={() => void handleDelete(task.id)}
                      >
                        {isPendingDelete ? (
                          <Loader2 className="size-4 animate-spin" />
                        ) : (
                          <Trash2 className="size-4" />
                        )}
                      </Button>
                      <Button
                        size="sm"
                        className="ml-2"
                        disabled={!canTrigger(task) || !!task.dependsOnTaskId || isPendingAny}
                        title={task.enabled === false ? t("tasks.pausedTooltip") : undefined}
                        onClick={() => void handleTrigger(task.id)}
                      >
                        {isPendingTrigger ? (
                          <Loader2 className="size-4 mr-1 animate-spin" />
                        ) : (
                          <Play className="size-4 mr-1" />
                        )}
                        {t("tasks.trigger")}
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
                  title={t("tasks.emptyTitle")}
                  description={t("tasks.emptyDesc")}
                  onReset={resetFilters}
                  onCreate={() => setCreateDialogOpen(true)}
                  createLabel={t("tasks.emptyCreateLabel")}
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
