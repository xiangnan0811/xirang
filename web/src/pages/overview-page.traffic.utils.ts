import type { OverviewTrafficSeries } from "@/types/domain";

/** Plotted fields are null for buckets the server could not fully count (row cap hit). */
export type TrafficChartData = {
  label: string;
  throughput: number | null;
  activity: number | null;
  failed: number | null;
  partial?: boolean;
  incomplete?: boolean;
};

export type TrafficChartMetrics = {
  chartData: TrafficChartData[];
  /** Throughput was measured; when truncated, only plottable buckets with samples count. */
  hasRealSamples: boolean;
  /** Server hit sample/task-run row cap; counts may under-represent the window. */
  truncated: boolean;
  /** At least one bucket carries complete counts that the chart can plot. */
  hasPlottableBuckets: boolean;
  hasPartialBucket: boolean;
  /** Peak and totals cover complete buckets only. */
  peakThroughput: number;
  totalStartedCount: number;
  totalFailedCount: number;
};

export type VisibleLayers = {
  throughput: boolean;
  activity: boolean;
  failures: boolean;
};

export function buildTrafficChartMetrics(trafficData: OverviewTrafficSeries | null): TrafficChartMetrics {
  const points = trafficData?.points ?? [];
  const truncated = Boolean(trafficData?.truncated);
  // Legacy servers report truncation without per-bucket flags, so no bucket can be trusted.
  const legacyTruncated = truncated && !points.some((point) => point.incomplete === true);
  let peakThroughput = 0;
  let totalStartedCount = 0;
  let totalFailedCount = 0;
  let hasPlottableBuckets = false;
  let hasMeasuredThroughput = false;

  const chartData = points.map((point): TrafficChartData => {
    const flags = {
      ...(point.partial ? { partial: true } : {}),
      ...(point.incomplete ? { incomplete: true } : {}),
    };
    if (legacyTruncated || point.incomplete === true) {
      return { label: point.label, throughput: null, activity: null, failed: null, ...flags };
    }
    hasPlottableBuckets = true;
    if (point.sampleCount > 0) hasMeasuredThroughput = true;
    peakThroughput = Math.max(peakThroughput, point.throughputMbps);
    totalStartedCount += point.startedCount;
    totalFailedCount += point.failedCount;
    return {
      label: point.label,
      throughput: point.throughputMbps,
      activity: Math.max(point.startedCount, point.activeTaskCount),
      failed: point.failedCount,
      ...flags,
    };
  });

  return {
    chartData,
    // Capped responses: the window-wide flag may only reflect discarded buckets.
    hasRealSamples: truncated ? hasMeasuredThroughput : Boolean(trafficData?.hasRealSamples),
    truncated,
    hasPlottableBuckets,
    hasPartialBucket: points.some((point) => point.partial === true),
    peakThroughput,
    totalStartedCount,
    totalFailedCount,
  };
}

/** Raw axis maxima over visible layers; null (incomplete) buckets never stretch the scale. */
export function computeTrafficAxisMax(
  chartData: readonly TrafficChartData[],
  visibleLayers: VisibleLayers,
): { yMaxLeft: number; yMaxRight: number } {
  let maxThroughput = 0;
  let maxCount = 0;
  for (const point of chartData) {
    if (visibleLayers.throughput && point.throughput !== null) maxThroughput = Math.max(maxThroughput, point.throughput);
    if (visibleLayers.activity && point.activity !== null) maxCount = Math.max(maxCount, point.activity);
    if (visibleLayers.failures && point.failed !== null) maxCount = Math.max(maxCount, point.failed);
  }
  return { yMaxLeft: computeThroughputMax(maxThroughput), yMaxRight: maxCount };
}

export function hasTrafficOrActivityData(metrics: TrafficChartMetrics): boolean {
  return metrics.hasRealSamples || metrics.totalStartedCount > 0 || metrics.totalFailedCount > 0;
}

export function computeThroughputMax(maxThroughput: number): number {
  const safeMax = Number.isFinite(maxThroughput) && maxThroughput > 0 ? maxThroughput : 0;
  if (safeMax <= 0) {
    return 0;
  }
  if (safeMax <= 40) {
    return Math.max(10, Math.ceil((safeMax * 1.1) / 10) * 10);
  }
  return Math.ceil((safeMax * 1.1) / 50) * 50;
}

export function computeIntegerAxisTicks(maxCount: number): {
  domain: [number, number];
  ticks: number[];
} {
  const safeMax = Number.isFinite(maxCount) && maxCount > 0 ? maxCount : 0;
  const targetMax = safeMax > 0 ? Math.max(4, Math.ceil(safeMax * 1.2)) : 4;
  const step = Math.max(1, Math.ceil(targetMax / 4));
  const top = step * 4;
  return {
    domain: [0, top],
    ticks: [0, step, step * 2, step * 3, top],
  };
}

export function hasPartialFlag(value: unknown): boolean {
  return typeof value === "object" && value !== null && "partial" in value && value.partial === true;
}

export function formatTrafficTooltipLabel(
  label: unknown,
  payload: ReadonlyArray<unknown> | undefined,
  partialSuffix: string,
): string {
  const first = Array.isArray(payload) && payload.length > 0 ? payload[0] : undefined;
  const point = first && typeof first === "object" && "payload" in first ? first.payload : undefined;
  if (hasPartialFlag(point)) {
    return `${String(label)} (${partialSuffix})`;
  }
  return String(label);
}

export function formatTrafficTooltipValue(
  value: unknown,
  name: unknown,
  throughputName: string,
  countUnit: string,
): [string, string] {
  const seriesName = String(name ?? "");
  if (seriesName === throughputName) {
    return [`${String(value ?? 0)} Mbps`, seriesName];
  }
  return [`${String(value ?? 0)} ${countUnit}`, seriesName];
}
