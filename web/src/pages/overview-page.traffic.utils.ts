import type { OverviewTrafficPoint, OverviewTrafficSeries } from "@/types/domain";

/**
 * Plotted fields are null when the server could not fully count their source rows in that bucket (row cap hit):
 * throughput needs complete samples, activity (max of starts and active tasks) needs complete samples and starts,
 * and failures need complete failed runs. Throughput is also null for complete buckets without samples
 * (`sampleCount === 0`), so the line breaks there instead of drawing a fake 0 Mbps; a measured 0 stays numeric.
 */
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
  /** Throughput was measured in at least one bucket with complete samples (`sampleCount > 0`). */
  hasRealSamples: boolean;
  /** At least one bucket has complete samples, so missing throughput means "no samples", not "unavailable". */
  hasSampleCoverage: boolean;
  /** Server hit a sample/task-run row cap; affected metrics are blank and totals may under-represent the window. */
  truncated: boolean;
  /** At least one bucket has at least one complete source the chart or totals can use. */
  hasPlottableBuckets: boolean;
  hasPartialBucket: boolean;
  /** Peak covers measured buckets with complete samples only. */
  peakThroughput: number;
  /** Sum over buckets with complete starts; null when no bucket has complete starts. */
  totalStartedCount: number | null;
  /** Sum over buckets with complete failures; null when no bucket has complete failures. */
  totalFailedCount: number | null;
};

export type VisibleLayers = {
  throughput: boolean;
  activity: boolean;
  failures: boolean;
};

type SourceCoverage = { samples: boolean; started: boolean; failed: boolean; flagged: boolean };

/** Per-source completeness of one bucket as reported by the server, before the window-wide legacy fallback. */
function sourceCoverage(point: OverviewTrafficPoint): SourceCoverage {
  const samples = point.samplesIncomplete !== true;
  const started = point.startedIncomplete !== true;
  const failed = point.failedIncomplete !== true;
  if (!samples || !started || !failed) return { samples, started, failed, flagged: true };
  // Legacy servers mark a capped bucket without saying which source was cut off.
  const legacyIncomplete = point.incomplete === true;
  return { samples: !legacyIncomplete, started: !legacyIncomplete, failed: !legacyIncomplete, flagged: legacyIncomplete };
}

export function buildTrafficChartMetrics(trafficData: OverviewTrafficSeries | null): TrafficChartMetrics {
  const points = trafficData?.points ?? [];
  const coverages = points.map(sourceCoverage);
  const anyFlagged = coverages.some((coverage) => coverage.flagged);
  const truncated = Boolean(trafficData?.truncated) || anyFlagged;
  // Older servers report truncation without any bucket flags, so no bucket can be trusted.
  const globalFallback = truncated && !anyFlagged;
  let peakThroughput = 0;
  let totalStartedCount: number | null = null;
  let totalFailedCount: number | null = null;
  let hasPlottableBuckets = false;
  let hasSampleCoverage = false;
  let hasMeasuredThroughput = false;

  const chartData = points.map((point, index): TrafficChartData => {
    const reported = coverages[index];
    const flags = {
      ...(point.partial ? { partial: true } : {}),
      ...(reported.flagged ? { incomplete: true } : {}),
    };
    const coverage = globalFallback ? { samples: false, started: false, failed: false } : reported;
    if (coverage.samples || coverage.started || coverage.failed) hasPlottableBuckets = true;

    let throughput: number | null = null;
    if (coverage.samples) {
      hasSampleCoverage = true;
      if (point.sampleCount > 0) {
        hasMeasuredThroughput = true;
        peakThroughput = Math.max(peakThroughput, point.throughputMbps);
        throughput = point.throughputMbps;
      }
    }
    if (coverage.started) totalStartedCount = (totalStartedCount ?? 0) + point.startedCount;
    if (coverage.failed) totalFailedCount = (totalFailedCount ?? 0) + point.failedCount;
    return {
      label: point.label,
      throughput,
      // Active-task counts come from samples, so activity needs both sources to avoid relabeling starts alone.
      activity: coverage.samples && coverage.started ? Math.max(point.startedCount, point.activeTaskCount) : null,
      failed: coverage.failed ? point.failedCount : null,
      ...flags,
    };
  });

  return {
    chartData,
    // Derived per bucket: capped responses' window-wide flag may only reflect discarded buckets.
    hasRealSamples: hasMeasuredThroughput,
    hasSampleCoverage,
    truncated,
    hasPlottableBuckets,
    hasPartialBucket: points.some((point) => point.partial === true),
    peakThroughput,
    totalStartedCount,
    totalFailedCount,
  };
}

/** Raw axis maxima over visible layers; null (incomplete or unmeasured) values never stretch the scale. */
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
  return metrics.hasRealSamples || (metrics.totalStartedCount ?? 0) > 0 || (metrics.totalFailedCount ?? 0) > 0;
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
