export type TrafficChartData = {
  label: string;
  throughput: number;
  activity: number;
  failed: number;
  partial?: boolean;
};

export type TrafficChartMetrics = {
  chartData: TrafficChartData[];
  hasRealSamples: boolean;
  /** Server hit sample/task-run row cap; counts may under-represent the window. */
  truncated: boolean;
  hasPartialBucket: boolean;
  peakThroughput: number;
  totalStartedCount: number;
  totalFailedCount: number;
};

export type VisibleLayers = {
  throughput: boolean;
  activity: boolean;
  failures: boolean;
};

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

export function formatTrafficTooltipLabel(
  label: unknown,
  payload: ReadonlyArray<unknown> | undefined,
  partialSuffix: string,
): string {
  const first = Array.isArray(payload) && payload.length > 0 ? payload[0] : undefined;
  const point =
    first && typeof first === "object" && "payload" in first
      ? (first.payload as Partial<TrafficChartData> | undefined)
      : undefined;
  if (point?.partial) {
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
