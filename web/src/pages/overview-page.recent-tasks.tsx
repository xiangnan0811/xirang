import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import {
  AlertTriangle,
  ArrowRight,
  Ban,
  CheckCircle2,
  Clock,
  Loader2,
  RotateCw,
  SkipForward,
  TrendingUp,
  XCircle,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { InventoryRetryAlert } from "@/components/ui/inventory-retry-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { formatTime } from "@/lib/date-utils";
import type { TaskRecord, TaskStatus } from "@/types/domain";
import { formatTaskDuration } from "@/pages/overview-page.recent-tasks.utils";

export type OverviewRecentTasksProps = {
  tasks: TaskRecord[];
  recentTasks: TaskRecord[];
  loading: boolean;
  error?: string | null;
  onRetry?: () => void;
  /** Compact table columns when displayed side-by-side with Health Events. */
  compact?: boolean;
};

type TaskStatusVisual = {
  Icon: typeof Clock;
  iconClass: string;
  textClass: string;
  labelKey: string;
};

const TASK_STATUS_VISUALS: Record<TaskStatus, TaskStatusVisual> = {
  success: {
    Icon: CheckCircle2,
    iconClass: "text-success",
    textClass: "text-[hsl(var(--success-text))]",
    labelKey: "overview.taskStatusSuccess",
  },
  failed: {
    Icon: XCircle,
    iconClass: "text-destructive",
    textClass: "text-[hsl(var(--destructive-text))]",
    labelKey: "overview.taskStatusFailed",
  },
  running: {
    Icon: TrendingUp,
    iconClass: "text-info",
    textClass: "text-[hsl(var(--info-text))]",
    labelKey: "overview.taskStatusRunning",
  },
  retrying: {
    Icon: RotateCw,
    iconClass: "text-warning",
    textClass: "text-[hsl(var(--warning-text))]",
    labelKey: "overview.taskStatusRetrying",
  },
  pending: {
    Icon: Clock,
    iconClass: "text-muted-foreground",
    textClass: "text-muted-foreground",
    labelKey: "overview.taskStatusQueued",
  },
  canceled: {
    Icon: Ban,
    iconClass: "text-muted-foreground",
    textClass: "text-muted-foreground",
    labelKey: "overview.taskStatusCanceled",
  },
  warning: {
    Icon: AlertTriangle,
    iconClass: "text-warning",
    textClass: "text-[hsl(var(--warning-text))]",
    labelKey: "overview.taskStatusWarning",
  },
  skipped: {
    Icon: SkipForward,
    iconClass: "text-muted-foreground",
    textClass: "text-muted-foreground",
    labelKey: "overview.taskStatusSkipped",
  },
};

export function OverviewRecentTasks({
  tasks,
  recentTasks,
  loading,
  error,
  onRetry,
  compact = false,
}: OverviewRecentTasksProps) {
  const { t } = useTranslation();
  const isInitialLoading = loading && tasks.length === 0 && !error;
  const isRefreshing = loading && tasks.length > 0;

  return (
    <DataSurface className="flex h-full flex-col">
      <DataSurfaceHeader
        title={
          <span className="inline-flex flex-wrap items-center gap-2">
            <Clock className="size-4 text-primary" aria-hidden />
            <span>{t("overview.recentTasks")}</span>
            {isRefreshing ? (
              <span
                className="inline-flex items-center gap-1 text-xs font-normal text-muted-foreground"
                role="status"
                aria-live="polite"
              >
                <Loader2 className="size-3 animate-spin" aria-hidden />
                {t("overview.refreshing")}
              </span>
            ) : null}
          </span>
        }
        actions={
          <Button asChild variant="ghost" size="sm">
            <Link to="/app/tasks">
              {t("overview.viewMore")}
              <ArrowRight className="size-3.5" aria-hidden />
            </Link>
          </Button>
        }
      />

      <DataSurfaceContent className="flex-1 p-0">
        {error ? (
          <div className="p-4">
            <InventoryRetryAlert error={error} onRetry={onRetry ?? (() => undefined)} />
          </div>
        ) : null}

        {isInitialLoading ? (
          <div className="p-4">
            <LoadingState className="py-4" rows={3} title={t("overview.recentTasksLoading")} />
          </div>
        ) : !error && tasks.length === 0 ? (
          <p className="py-8 text-center text-xs text-muted-foreground">{t("overview.noTaskData")}</p>
        ) : tasks.length > 0 ? (
          <div
            role="region"
            // Keyboard focus enables native horizontal scrolling.
            // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
            tabIndex={0}
            aria-label={t("overview.recentTasks")}
            className="overflow-x-auto focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
          >
            <table className="w-full text-left text-sm">
              <thead className="border-b border-border bg-secondary/40 text-mini uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th
                    scope="col"
                    className={`${compact ? "px-2.5 first:pl-4" : "px-4"} py-2 font-medium whitespace-nowrap`}
                  >
                    {t("overview.tableNodeName")}
                  </th>
                  <th
                    scope="col"
                    className={`${compact ? "px-2.5" : "px-4"} py-2 font-medium whitespace-nowrap`}
                  >
                    {t("overview.tableTaskName")}
                  </th>
                  <th
                    scope="col"
                    className={`${compact ? "px-2.5" : "px-4"} py-2 font-medium whitespace-nowrap`}
                  >
                    {t("overview.tableSyncStatus")}
                  </th>
                  <th
                    scope="col"
                    className={`${compact ? "px-2.5" : "px-4"} py-2 font-medium whitespace-nowrap`}
                  >
                    {t("overview.tableDuration")}
                  </th>
                  <th
                    scope="col"
                    className={`${compact ? "px-2.5 last:pr-4 text-right" : "px-4"} py-2 font-medium whitespace-nowrap`}
                  >
                    {t("overview.tableCompletedAt")}
                  </th>
                  {!compact ? (
                    <th scope="col" className="px-4 py-2 font-medium text-right whitespace-nowrap">
                      {t("overview.tableNextRun")}
                    </th>
                  ) : null}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {recentTasks.map((task) => {
                  const visual = TASK_STATUS_VISUALS[task.status] ?? TASK_STATUS_VISUALS.pending;
                  const StatusIcon = visual.Icon;
                  const statusLabel = t(visual.labelKey);
                  const finishedDisplay =
                    task.lastFinishedAt && task.lastFinishedAt !== "-"
                      ? formatTime(task.lastFinishedAt)
                      : "—";
                  const nextRunDisplay =
                    task.nextRunAt && task.nextRunAt !== "-"
                      ? formatTime(task.nextRunAt)
                      : "—";
                  const durationDisplay = formatTaskDuration(task.lastDurationMs);
                  const taskTitle = task.name || task.policyName;
                  const cellPad = compact ? "px-2.5" : "px-4";

                  return (
                    <tr key={task.id} className="hover:bg-muted/30 transition-colors">
                      <td
                        className={`${cellPad} ${compact ? "first:pl-4 max-w-[116px]" : ""} py-2.5 font-medium text-foreground whitespace-nowrap`}
                        title={task.nodeName}
                      >
                        <span className={compact ? "block truncate" : ""}>{task.nodeName}</span>
                      </td>
                      <td className={`${cellPad} py-2.5 ${compact ? "max-w-[164px]" : "max-w-[240px]"}`}>
                        <Link
                          to={`/app/logs?task=${task.id}`}
                          title={taskTitle}
                          className="inline-flex max-w-full items-center gap-1 rounded-sm font-medium text-foreground hover:text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                        >
                          <span className="truncate">{taskTitle}</span>
                        </Link>
                      </td>
                      <td className={`${cellPad} py-2.5 whitespace-nowrap`}>
                        <span className={`inline-flex items-center gap-1.5 whitespace-nowrap ${visual.iconClass}`}>
                          <StatusIcon className="size-3.5 shrink-0" aria-hidden />
                          <span className={visual.textClass}>{statusLabel}</span>
                        </span>
                      </td>
                      <td className={`${cellPad} py-2.5 font-mono text-xs tabular-nums text-muted-foreground whitespace-nowrap`}>
                        {durationDisplay}
                      </td>
                      <td
                        className={`${cellPad} ${compact ? "last:pr-4 text-right" : ""} py-2.5 text-xs tabular-nums text-muted-foreground whitespace-nowrap`}
                      >
                        {finishedDisplay}
                      </td>
                      {!compact ? (
                        <td className="px-4 py-2.5 text-right text-xs tabular-nums text-muted-foreground whitespace-nowrap">
                          {nextRunDisplay}
                        </td>
                      ) : null}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        ) : null}
      </DataSurfaceContent>
    </DataSurface>
  );
}
