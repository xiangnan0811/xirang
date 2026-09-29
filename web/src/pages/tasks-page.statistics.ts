import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { apiClient } from "@/lib/api/client";
import {
  TaskStatisticPayloadError,
  type TaskStatisticAggregation,
  type TaskStatisticMetric,
  type TaskStatisticResult,
  type TaskStatisticSeries,
} from "@/lib/api/tasks-api";
import { getErrorMessage } from "@/lib/utils";
import { isAbortError } from "@/hooks/inventory-request-state";

export const TASK_STATS_WINDOWS = ["24h", "7d", "30d"] as const;
export type TaskStatsWindow = (typeof TASK_STATS_WINDOWS)[number];
export const DURATION_QUANTILES = ["p50", "p95", "p99"] as const;
export type DurationQuantile = (typeof DURATION_QUANTILES)[number];
export type TaskStatsScope = "all" | "specified";

const WINDOW_MS: Record<TaskStatsWindow, number> = {
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
  "30d": 30 * 24 * 60 * 60 * 1000,
};

export type StatisticChartId = "success" | "throughput" | "duration";

export type TaskStatisticSlot = {
  loading: boolean;
  error: string | null;
  result: TaskStatisticResult | null;
};

const loadingSlot = (): TaskStatisticSlot => ({ loading: true, error: null, result: null });
const idleSlot = (): TaskStatisticSlot => ({ loading: false, error: null, result: null });

function freshSlots(loading: boolean): Record<StatisticChartId, TaskStatisticSlot> {
  return {
    success: loading ? loadingSlot() : idleSlot(),
    throughput: loading ? loadingSlot() : idleSlot(),
    duration: loading ? loadingSlot() : idleSlot(),
  };
}

export function taskStatisticsRange(window: TaskStatsWindow, now = Date.now()): { start: string; end: string } {
  const end = now;
  return {
    start: new Date(end - WINDOW_MS[window]).toISOString(),
    end: new Date(end).toISOString(),
  };
}

export function statisticSeriesLabel(name: string, allLabel: string): string {
  if (name === "" || name === "all" || name === "全部任务") {
    return allLabel;
  }
  return name;
}

function trimNumber(value: number): string {
  const rounded = Math.round(value * 10) / 10;
  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
}

export function formatSuccessRate(value: number): string {
  return `${trimNumber(value * 100)}%`;
}

export function formatThroughputMbps(value: number): string {
  return `${trimNumber(value)} Mbps`;
}

export function formatDurationMs(value: number): string {
  if (value < 1000) {
    return `${trimNumber(value)} ms`;
  }
  if (value < 60_000) {
    return `${trimNumber(value / 1000)} s`;
  }
  return `${trimNumber(value / 60_000)} min`;
}

export function formatStatisticValue(metric: TaskStatisticMetric, value: number): string {
  if (metric === "task.success_rate") return formatSuccessRate(value);
  if (metric === "task.throughput") return formatThroughputMbps(value);
  return formatDurationMs(value);
}

export function hasStatisticPoints(result: TaskStatisticResult | null): boolean {
  return Boolean(result?.series.some((series) => series.points.length > 0));
}

export function latestPointValue(series: TaskStatisticSeries): number | null {
  let latest: { ts: number; value: number } | null = null;
  for (const point of series.points) {
    const ts = Date.parse(point.ts);
    if (!Number.isFinite(ts)) continue;
    if (!latest || ts >= latest.ts) {
      latest = { ts, value: point.value };
    }
  }
  return latest?.value ?? null;
}

export type StatisticFrame = {
  ts: string;
  gap: boolean;
  values: Array<number | null>;
};

// Keep returned samples only. A missing bucket becomes a gap marker so the line
// breaks instead of being drawn as zero.
export function statisticFrames(series: TaskStatisticSeries[], stepSeconds: number): StatisticFrame[] {
  const stamps = new Set<string>();
  for (const item of series) {
    for (const point of item.points) stamps.add(point.ts);
  }
  const ordered = [...stamps].sort((left, right) => Date.parse(left) - Date.parse(right));
  const stepMs = stepSeconds > 0 ? stepSeconds * 1000 : 0;
  const keys: Array<{ ts: string; gap: boolean }> = [];
  for (let index = 0; index < ordered.length; index += 1) {
    const ts = ordered[index] ?? "";
    if (index > 0 && stepMs > 0) {
      const previous = Date.parse(ordered[index - 1] ?? "");
      const current = Date.parse(ts);
      if (Number.isFinite(previous) && Number.isFinite(current) && current - previous > stepMs * 1.5) {
        keys.push({ ts: "", gap: true });
      }
    }
    keys.push({ ts, gap: false });
  }
  return keys.map((key) => ({
    ts: key.ts,
    gap: key.gap,
    values: key.gap
      ? series.map(() => null)
      : series.map((item) => item.points.find((point) => point.ts === key.ts)?.value ?? null),
  }));
}

const CHARTS: Array<{
  id: StatisticChartId;
  metric: TaskStatisticMetric;
  aggregation: (quantile: DurationQuantile) => TaskStatisticAggregation;
}> = [
  { id: "success", metric: "task.success_rate", aggregation: () => "avg" },
  { id: "throughput", metric: "task.throughput", aggregation: () => "avg" },
  { id: "duration", metric: "task.duration", aggregation: (quantile) => quantile },
];

export function useTaskRunStatistics({
  token,
  window,
  durationAggregation,
  taskIds,
  refreshNonce,
}: {
  token: string | null;
  window: TaskStatsWindow;
  durationAggregation: DurationQuantile;
  taskIds: number[] | null;
  refreshNonce: number;
}): { slots: Record<StatisticChartId, TaskStatisticSlot>; needsSelection: boolean } {
  const { t, i18n } = useTranslation();
  const [completed, setCompleted] = useState<{
    key: string;
    slots: Record<StatisticChartId, TaskStatisticSlot>;
  } | null>(null);
  const taskKey = taskIds === null ? "*" : taskIds.join(",");
  const needsSelection = Boolean(token) && taskIds !== null && taskIds.length === 0;
  const canQuery = Boolean(token) && !needsSelection;
  const requestKey = JSON.stringify([token, window, durationAggregation, taskKey, refreshNonce, i18n.resolvedLanguage]);
  const slots = canQuery
    ? completed?.key === requestKey ? completed.slots : freshSlots(true)
    : freshSlots(false);

  useEffect(() => {
    if (!token || !canQuery) return;

    const controller = new AbortController();
    let active = true;
    const range = taskStatisticsRange(window);

    for (const chart of CHARTS) {
      void apiClient.queryTaskStatistics(
        token,
        {
          metric: chart.metric,
          aggregation: chart.aggregation(durationAggregation),
          start: range.start,
          end: range.end,
          ...(taskIds ? { taskIds } : {}),
        },
        { signal: controller.signal },
      ).then(
        (result) => {
          if (!active) return;
          setCompleted((current) => ({
            key: requestKey,
            slots: {
              ...(current?.key === requestKey ? current.slots : freshSlots(true)),
              [chart.id]: { loading: false, error: null, result },
            },
          }));
        },
        (error: unknown) => {
          if (!active || isAbortError(error)) return;
          const message = error instanceof TaskStatisticPayloadError
            ? t("tasks.statistics.invalidResponse")
            : getErrorMessage(error, t("tasks.statistics.loadFailed"));
          setCompleted((current) => ({
            key: requestKey,
            slots: {
              ...(current?.key === requestKey ? current.slots : freshSlots(true)),
              [chart.id]: { loading: false, error: message, result: null },
            },
          }));
        },
      );
    }

    return () => {
      active = false;
      controller.abort();
    };
  }, [canQuery, durationAggregation, requestKey, t, taskIds, token, window]);

  return { slots, needsSelection };
}
