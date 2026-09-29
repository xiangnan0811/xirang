import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { RefreshCw } from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import { chartSeriesColors } from "@/lib/chart-colors";
import type { TaskStatisticMetric, TaskStatisticSeries } from "@/lib/api/tasks-api";
import type { TaskRecord } from "@/types/domain";
import { Button } from "@/components/ui/button";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { EmptyState } from "@/components/ui/empty-state";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { Select } from "@/components/ui/select";
import {
  DURATION_QUANTILES,
  TASK_STATS_WINDOWS,
  formatStatisticValue,
  latestPointValue,
  statisticFrames,
  statisticSeriesLabel,
  useTaskRunStatistics,
  type DurationQuantile,
  type StatisticChartId,
  type TaskStatisticSlot,
  type TaskStatsScope,
  type TaskStatsWindow,
} from "@/pages/tasks-page.statistics";

const CHART_META: Record<StatisticChartId, { metric: TaskStatisticMetric; titleKey: string; descriptionKey: string }> = {
  success: {
    metric: "task.success_rate",
    titleKey: "tasks.statistics.successTitle",
    descriptionKey: "tasks.statistics.successDescription",
  },
  throughput: {
    metric: "task.throughput",
    titleKey: "tasks.statistics.throughputTitle",
    descriptionKey: "tasks.statistics.throughputDescription",
  },
  duration: {
    metric: "task.duration",
    titleKey: "tasks.statistics.durationTitle",
    descriptionKey: "tasks.statistics.durationDescription",
  },
};

const COLORS = chartSeriesColors();

type TooltipRow = { name?: string; value?: unknown; color?: string; dataKey?: string | number };

function StatisticTooltip({
  active,
  payload,
  label,
  metric,
  gapLabel,
}: {
  active?: boolean;
  payload?: TooltipRow[];
  label?: string | number;
  metric: TaskStatisticMetric;
  gapLabel: string;
}) {
  if (!active || !payload || payload.length === 0 || String(label).startsWith("gap-")) return null;
  const parsed = new Date(String(label));
  const when = Number.isNaN(parsed.getTime()) ? gapLabel : parsed.toLocaleString();
  return (
    <div className="max-w-[240px] space-y-0.5 rounded-md border border-border bg-card px-2 py-1.5 text-xs shadow-sm">
      <div className="text-micro font-medium text-muted-foreground">{when}</div>
      {payload.map((item) => (
        <div key={String(item.dataKey ?? item.name)} className="flex items-baseline gap-2">
          <span className="size-1.5 shrink-0 rounded-full" style={{ backgroundColor: item.color ?? "currentColor" }} />
          <span className="truncate text-foreground/80">{item.name}</span>
          <span className="ml-auto font-medium tabular-nums">
            {typeof item.value === "number" ? formatStatisticValue(metric, item.value) : "—"}
          </span>
        </div>
      ))}
    </div>
  );
}

function StatisticChart({
  id,
  slot,
  window,
  onRetry,
}: {
  id: StatisticChartId;
  slot: TaskStatisticSlot;
  window: TaskStatsWindow;
  onRetry: () => void;
}) {
  const { t, i18n } = useTranslation();
  const meta = CHART_META[id];
  const title = t(meta.titleKey);
  const headingId = `task-stat-${id}-title`;
  const allLabel = t("tasks.statistics.allSeries");
  const series = slot.result?.series ?? [];
  const visible = series.filter((item) => item.points.length > 0);
  const frames = statisticFrames(visible, slot.result?.stepSeconds ?? 0);
  const plotted = frames.some((frame) => frame.values.some((value) => value !== null));
  const data = frames.map((frame, index) => {
    const row: Record<string, string | number | null> = {
      ts: frame.gap ? `gap-${index}` : frame.ts,
    };
    frame.values.forEach((value, seriesIndex) => {
      row[`s${seriesIndex}`] = value;
    });
    return row;
  });

  let body;
  if (slot.loading) {
    body = <LoadingState title={t("tasks.statistics.loading")} description={title} rows={2} />;
  } else if (slot.error) {
    body = (
      <div className="space-y-3">
        <InlineAlert tone="critical" title={t("tasks.statistics.loadFailed")}>{slot.error}</InlineAlert>
        <Button type="button" variant="outline" size="sm" onClick={onRetry} aria-label={t("tasks.statistics.retryChart", { chart: title })}>
          {t("tasks.statistics.retry")}
        </Button>
      </div>
    );
  } else if (!plotted) {
    body = <EmptyState as="h4" title={t("tasks.statistics.emptyTitle")} description={t("tasks.statistics.emptyDescription")} />;
  } else {
    body = (
      <div className="space-y-2">
        <LatestValues metric={meta.metric} series={visible} allLabel={allLabel} />
        <div className="h-56 min-w-0" aria-hidden="true">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" />
              <XAxis
                dataKey="ts"
                tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                minTickGap={28}
                tickFormatter={(value: string) => formatTick(value, window, i18n.language)}
              />
              <YAxis
                tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                width={56}
                domain={meta.metric === "task.success_rate" ? [0, 1] : ["auto", "auto"]}
                tickFormatter={(value: number) => formatStatisticValue(meta.metric, value)}
              />
              <Tooltip
                content={<StatisticTooltip metric={meta.metric} gapLabel={t("tasks.statistics.gap")} />}
                wrapperStyle={{ outline: "none" }}
              />
              {visible.map((item, index) => (
                <Line
                  key={`${item.name}-${index}`}
                  type="linear"
                  dataKey={`s${index}`}
                  name={statisticSeriesLabel(item.name, allLabel)}
                  stroke={COLORS[index % COLORS.length]}
                  strokeWidth={1.5}
                  dot={{ r: 3 }}
                  connectNulls={false}
                  isAnimationActive={false}
                />
              ))}
            </LineChart>
          </ResponsiveContainer>
        </div>
        <StatisticTable title={title} metric={meta.metric} series={visible} allLabel={allLabel} />
      </div>
    );
  }

  return (
    <article aria-labelledby={headingId} className="min-w-0 rounded-lg border border-border bg-background/40 p-3">
      <h3 id={headingId} className="text-sm font-semibold text-foreground">{title}</h3>
      <p className="mt-1 text-xs text-muted-foreground">{t(meta.descriptionKey)}</p>
      <div className="mt-3">{body}</div>
    </article>
  );
}

function LatestValues({
  metric,
  series,
  allLabel,
}: {
  metric: TaskStatisticMetric;
  series: TaskStatisticSeries[];
  allLabel: string;
}) {
  const { t } = useTranslation();
  if (series.length === 1) {
    const value = latestPointValue(series[0] ?? { name: "", points: [] });
    return (
      <p className="text-2xl font-semibold tabular-nums tracking-tight text-foreground">
        <span className="sr-only">{t("tasks.statistics.latestPoint")} </span>
        {value === null ? "—" : formatStatisticValue(metric, value)}
      </p>
    );
  }
  return (
    <ul className="space-y-1">
      {series.map((item, index) => {
        const value = latestPointValue(item);
        return (
          <li key={`${item.name}-${index}`} className="flex items-baseline justify-between gap-3 text-sm">
            <span className="truncate text-muted-foreground">{statisticSeriesLabel(item.name, allLabel)}</span>
            <span className="font-medium tabular-nums">{value === null ? "—" : formatStatisticValue(metric, value)}</span>
          </li>
        );
      })}
    </ul>
  );
}

function StatisticTable({
  title,
  metric,
  series,
  allLabel,
}: {
  title: string;
  metric: TaskStatisticMetric;
  series: TaskStatisticSeries[];
  allLabel: string;
}) {
  const { t } = useTranslation();
  const rows = statisticFrames(series, 0).filter((frame) => !frame.gap);
  return (
    <table className="sr-only">
      <caption>{title}</caption>
      <thead>
        <tr>
          <th scope="col">{t("tasks.statistics.timeColumn")}</th>
          {series.map((item, index) => (
            <th key={`${item.name}-${index}`} scope="col">{statisticSeriesLabel(item.name, allLabel)}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {rows.map((frame) => (
          <tr key={frame.ts}>
            <th scope="row">{new Date(frame.ts).toLocaleString()}</th>
            {frame.values.map((value, index) => (
              <td key={`${frame.ts}-${index}`}>{value === null ? "—" : formatStatisticValue(metric, value)}</td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function formatTick(value: string, window: TaskStatsWindow, language: string): string {
  if (value.startsWith("gap-")) return "";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "";
  const locale = language.startsWith("zh") ? "zh-CN" : "en";
  if (window === "24h") {
    return parsed.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
  }
  return parsed.toLocaleDateString(locale, { month: "numeric", day: "numeric" });
}

function taskChoiceLabel(task: TaskRecord, fallback: string): string {
  const name = task.name?.trim() ?? "";
  return name || fallback;
}

export function TaskRunStatistics({ tasks }: { tasks: TaskRecord[] }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [window, setWindow] = useState<TaskStatsWindow>("24h");
  const [quantile, setQuantile] = useState<DurationQuantile>("p95");
  const [scope, setScope] = useState<TaskStatsScope>("all");
  const [selectedIds, setSelectedIds] = useState<number[]>([]);
  const [refreshNonce, setRefreshNonce] = useState(0);
  const choices = useMemo(
    () => [...tasks].sort((left, right) => (left.name ?? "").localeCompare(right.name ?? "", "zh") || left.id - right.id),
    [tasks],
  );
  const taskIds = scope === "all" ? null : selectedIds;
  const { slots, needsSelection } = useTaskRunStatistics({
    token,
    window,
    durationAggregation: quantile,
    taskIds,
    refreshNonce,
  });
  const truncated = Object.values(slots).some((slot) => slot.result?.truncated);
  const reload = () => setRefreshNonce((current) => current + 1);

  return (
    <DataSurface aria-labelledby="task-run-statistics-heading" className="animate-slide-up">
      <DataSurfaceHeader
        title={<span id="task-run-statistics-heading">{t("tasks.statistics.title")}</span>}
        description={t("tasks.statistics.subtitle")}
        actions={
          <Button type="button" variant="outline" size="sm" onClick={reload} aria-label={t("tasks.statistics.refresh")}>
            <RefreshCw className="size-3.5" aria-hidden="true" />
            {t("tasks.statistics.refresh")}
          </Button>
        }
      />
      <DataSurfaceContent className="space-y-3">
        <div className="flex flex-col gap-3 lg:flex-row lg:flex-wrap lg:items-end">
          <div className="space-y-1">
            <span id="task-stats-window-label" className="text-xs text-muted-foreground">{t("tasks.statistics.windowLabel")}</span>
            <div role="group" aria-labelledby="task-stats-window-label" className="flex flex-wrap gap-1">
              {TASK_STATS_WINDOWS.map((item) => {
                const active = item === window;
                return (
                  <button
                    key={item}
                    type="button"
                    aria-pressed={active}
                    onClick={() => setWindow(item)}
                    className={
                      "h-8 rounded-full px-3 text-xs font-medium focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/35 " +
                      (active
                        ? "bg-primary text-primary-foreground"
                        : "bg-muted text-muted-foreground hover:text-foreground")
                    }
                  >
                    {t(`tasks.statistics.window.${item}`)}
                  </button>
                );
              })}
            </div>
          </div>
          <div className="min-w-40 flex-1 space-y-1">
            <label htmlFor="task-stats-scope" className="text-xs text-muted-foreground">{t("tasks.statistics.scopeLabel")}</label>
            <Select
              id="task-stats-scope"
              value={scope}
              onChange={(event) => setScope(event.target.value === "specified" ? "specified" : "all")}
            >
              <option value="all">{t("tasks.statistics.scopeAll")}</option>
              <option value="specified">{t("tasks.statistics.scopeSpecified")}</option>
            </Select>
          </div>
          <div className="min-w-32 space-y-1">
            <label htmlFor="task-stats-quantile" className="text-xs text-muted-foreground">{t("tasks.statistics.quantileLabel")}</label>
            <Select
              id="task-stats-quantile"
              value={quantile}
              onChange={(event) => {
                const next = event.target.value;
                if (next === "p50" || next === "p95" || next === "p99") setQuantile(next);
              }}
            >
              {DURATION_QUANTILES.map((item) => (
                <option key={item} value={item}>{item}</option>
              ))}
            </Select>
          </div>
        </div>

        {scope === "specified" ? (
          <fieldset className="space-y-2 rounded-md border border-border p-3">
            <legend className="px-1 text-xs text-muted-foreground">{t("tasks.statistics.scopeTasks")}</legend>
            {choices.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("tasks.statistics.noTasks")}</p>
            ) : (
              <div className="grid max-h-40 grid-cols-1 gap-1 overflow-y-auto sm:grid-cols-2">
                {choices.map((task) => {
                  const label = taskChoiceLabel(task, t("tasks.taskFallbackName", { id: task.id }));
                  const checked = selectedIds.includes(task.id);
                  return (
                    <label key={task.id} className="flex min-w-0 items-center gap-2 rounded-md px-1 py-1 text-sm hover:bg-muted/60">
                      <input
                        type="checkbox"
                        className="size-4 shrink-0 accent-primary"
                        checked={checked}
                        onChange={() => {
                          setSelectedIds((current) => (
                            current.includes(task.id)
                              ? current.filter((id) => id !== task.id)
                              : [...current, task.id].sort((left, right) => left - right)
                          ));
                        }}
                      />
                      <span className="truncate">{t("tasks.statistics.includeTask", { name: label })}</span>
                    </label>
                  );
                })}
              </div>
            )}
          </fieldset>
        ) : null}

        {truncated ? (
          <InlineAlert tone="warning" title={t("tasks.statistics.truncatedTitle")}>
            {t("tasks.statistics.truncated")}
          </InlineAlert>
        ) : null}

        {needsSelection ? (
          <p className="rounded-md border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
            {t("tasks.statistics.needsSelection")}
          </p>
        ) : (
          <div className="grid grid-cols-1 gap-3 xl:grid-cols-3">
            {(Object.keys(CHART_META) as StatisticChartId[]).map((id) => (
              <StatisticChart
                key={id}
                id={id}
                slot={slots[id]}
                window={window}
                onRetry={reload}
              />
            ))}
          </div>
        )}
      </DataSurfaceContent>
    </DataSurface>
  );
}
