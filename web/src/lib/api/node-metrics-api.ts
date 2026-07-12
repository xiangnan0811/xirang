import { request } from "./core";
import { finiteNumber } from "./number-utils";

/** Wire shape from GET /nodes/:id/status — not exported (API boundary only). */
type RawNodeStatus = {
  probed_at?: unknown;
  online?: unknown;
  current?: unknown;
  trend_1h?: unknown;
  trend_24h?: unknown;
  open_alerts?: unknown;
  running_tasks?: unknown;
};

/** Latest sample metrics (backend keys: cpu_pct, mem_pct, disk_pct, load1, latency_ms). */
export interface NodeMetricCurrent {
  cpuPct: number;
  memPct: number;
  diskPct: number;
  load1: number;
  latencyMs: number | null;
}

/** Hourly-weighted trend averages (backend keys: *_avg, probe_ok_ratio). */
export interface NodeMetricTrend {
  cpuPctAvg: number;
  memPctAvg: number;
  diskPctAvg: number;
  load1Avg: number;
  latencyMsAvg: number | null;
  probeOkRatio: number | null;
}

export interface NodeStatus {
  probedAt: string | null;
  online: boolean;
  current: NodeMetricCurrent;
  trend1h: NodeMetricTrend;
  trend24h: NodeMetricTrend;
  openAlerts: number;
  runningTasks: number;
}

function asNumberMap(raw: unknown): Record<string, number> {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out: Record<string, number> = {};
  for (const [k, v] of Object.entries(raw as Record<string, unknown>)) {
    const n = finiteNumber(v, Number.NaN);
    if (Number.isFinite(n)) out[k] = n;
  }
  return out;
}

function mapNodeMetricCurrent(raw: unknown): NodeMetricCurrent {
  const m = asNumberMap(raw);
  return {
    cpuPct: finiteNumber(m.cpu_pct),
    memPct: finiteNumber(m.mem_pct),
    diskPct: finiteNumber(m.disk_pct),
    load1: finiteNumber(m.load1),
    latencyMs: m.latency_ms == null ? null : finiteNumber(m.latency_ms),
  };
}

function mapNodeMetricTrend(raw: unknown): NodeMetricTrend {
  const m = asNumberMap(raw);
  return {
    cpuPctAvg: finiteNumber(m.cpu_pct_avg),
    memPctAvg: finiteNumber(m.mem_pct_avg),
    diskPctAvg: finiteNumber(m.disk_pct_avg),
    load1Avg: finiteNumber(m.load1_avg),
    latencyMsAvg: m.latency_ms_avg == null ? null : finiteNumber(m.latency_ms_avg),
    probeOkRatio: m.probe_ok_ratio == null ? null : finiteNumber(m.probe_ok_ratio),
  };
}

export function mapNodeStatus(raw: RawNodeStatus | null | undefined): NodeStatus {
  const row = raw ?? {};
  const probed = row.probed_at;
  return {
    probedAt: typeof probed === "string" && probed.length > 0 ? probed : null,
    online: Boolean(row.online),
    current: mapNodeMetricCurrent(row.current),
    trend1h: mapNodeMetricTrend(row.trend_1h),
    trend24h: mapNodeMetricTrend(row.trend_24h),
    openAlerts: finiteNumber(row.open_alerts),
    runningTasks: finiteNumber(row.running_tasks),
  };
}

export interface MetricPoint {
  t: string;
  avg?: number;
  max?: number;
  v?: number;
}
export interface MetricSeries {
  metric: string;
  unit: string;
  points: MetricPoint[];
}

/** Wire shape from GET /nodes/:id/metric-series — not exported (API boundary only). */
type RawMetricSeriesResponse = {
  granularity?: unknown;
  bucket_seconds?: unknown;
  series?: unknown;
};

export interface MetricSeriesResponse {
  granularity: "raw" | "hourly" | "daily";
  bucketSeconds: number;
  series: MetricSeries[];
}

function mapMetricPoint(raw: unknown): MetricPoint | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const o = raw as Record<string, unknown>;
  const t = typeof o.t === "string" ? o.t : o.t == null ? "" : String(o.t);
  if (!t) return null;
  const point: MetricPoint = { t };
  if (o.avg != null) point.avg = finiteNumber(o.avg);
  if (o.max != null) point.max = finiteNumber(o.max);
  if (o.v != null) point.v = finiteNumber(o.v);
  return point;
}

function mapMetricSeries(raw: unknown): MetricSeries | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const o = raw as Record<string, unknown>;
  const metric = typeof o.metric === "string" ? o.metric : "";
  if (!metric) return null;
  const pointsRaw = Array.isArray(o.points) ? o.points : [];
  return {
    metric,
    unit: typeof o.unit === "string" ? o.unit : "",
    points: pointsRaw.map(mapMetricPoint).filter((p): p is MetricPoint => p != null),
  };
}

export function mapMetricSeriesResponse(raw: RawMetricSeriesResponse | null | undefined): MetricSeriesResponse {
  const row = raw ?? {};
  const g = row.granularity;
  const granularity: MetricSeriesResponse["granularity"] =
    g === "raw" || g === "hourly" || g === "daily" ? g : "raw";
  const seriesRaw = Array.isArray(row.series) ? row.series : [];
  return {
    granularity,
    bucketSeconds: finiteNumber(row.bucket_seconds),
    series: seriesRaw.map(mapMetricSeries).filter((s): s is MetricSeries => s != null),
  };
}

/** Wire shape from GET /nodes/:id/disk-forecast — not exported (API boundary only). */
type RawDiskForecast = {
  disk_gb_total?: unknown;
  disk_gb_used_now?: unknown;
  daily_growth_gb?: unknown;
  forecast?: unknown;
};

export interface DiskForecast {
  diskGbTotal: number;
  diskGbUsedNow: number;
  dailyGrowthGb: number | null;
  forecast: {
    daysToFull: number | null;
    dateFull: string | null;
    confidence: "high" | "medium" | "low" | "insufficient";
  };
}

export function mapDiskForecast(raw: RawDiskForecast | null | undefined): DiskForecast {
  const row = raw ?? {};
  const fcRaw = row.forecast;
  const fc =
    fcRaw && typeof fcRaw === "object" && !Array.isArray(fcRaw)
      ? (fcRaw as Record<string, unknown>)
      : undefined;
  const conf = fc?.confidence;
  const confidence: DiskForecast["forecast"]["confidence"] =
    conf === "high" || conf === "medium" || conf === "low" || conf === "insufficient"
      ? conf
      : "insufficient";
  return {
    diskGbTotal: finiteNumber(row.disk_gb_total),
    diskGbUsedNow: finiteNumber(row.disk_gb_used_now),
    dailyGrowthGb: row.daily_growth_gb == null ? null : finiteNumber(row.daily_growth_gb),
    forecast: {
      daysToFull: fc?.days_to_full == null ? null : finiteNumber(fc.days_to_full),
      dateFull: typeof fc?.date_full === "string" ? fc.date_full : null,
      confidence,
    },
  };
}

export interface NodeMetricSample {
  id: number;
  nodeId: number;
  cpuPct: number;
  memPct: number;
  diskPct: number;
  load1m: number;
  sampledAt: string;
}

type RawNodeMetricSample = {
  id?: unknown;
  node_id?: unknown;
  cpu_pct?: unknown;
  mem_pct?: unknown;
  disk_pct?: unknown;
  load_1m?: unknown;
  sampled_at?: unknown;
};

function mapNodeMetricSample(raw: RawNodeMetricSample | null | undefined): NodeMetricSample {
  const row = raw ?? {};
  return {
    id: finiteNumber(row.id),
    nodeId: finiteNumber(row.node_id),
    cpuPct: finiteNumber(row.cpu_pct),
    memPct: finiteNumber(row.mem_pct),
    diskPct: finiteNumber(row.disk_pct),
    load1m: finiteNumber(row.load_1m),
    sampledAt: typeof row.sampled_at === "string" ? row.sampled_at : String(row.sampled_at ?? ""),
  };
}

export type NodeMetricsRequestOptions = {
  signal?: AbortSignal;
};

export function createNodeMetricsApi() {
  return {
    async getNodeMetrics(
      token: string,
      nodeId: number,
      params?: { limit?: number; since?: string },
      options?: NodeMetricsRequestOptions,
    ): Promise<{ items: NodeMetricSample[] }> {
      const query = new URLSearchParams();
      if (params?.limit) query.set("limit", String(params.limit));
      if (params?.since) query.set("since", params.since);
      const qs = query.toString();
      const raw = await request<{ items: RawNodeMetricSample[] }>(
        `/nodes/${nodeId}/metrics${qs ? `?${qs}` : ""}`,
        { token, signal: options?.signal },
      );
      const items = Array.isArray(raw?.items) ? raw.items : [];
      return { items: items.map(mapNodeMetricSample) };
    },
    async getNodeStatus(
      token: string,
      nodeId: number,
      options?: NodeMetricsRequestOptions,
    ): Promise<NodeStatus> {
      const raw = await request<RawNodeStatus>(`/nodes/${nodeId}/status`, {
        token,
        signal: options?.signal,
      });
      return mapNodeStatus(raw);
    },
    async getMetricSeries(
      token: string,
      nodeId: number,
      params: {
        from: string;
        to: string;
        fields?: string[];
        granularity?: "auto" | "raw" | "hourly" | "daily";
      },
      options?: NodeMetricsRequestOptions,
    ): Promise<MetricSeriesResponse> {
      const qs = new URLSearchParams({ from: params.from, to: params.to });
      if (params.fields && params.fields.length) qs.set("fields", params.fields.join(","));
      if (params.granularity) qs.set("granularity", params.granularity);
      const raw = await request<RawMetricSeriesResponse>(
        `/nodes/${nodeId}/metric-series?${qs.toString()}`,
        { token, signal: options?.signal },
      );
      return mapMetricSeriesResponse(raw);
    },
    async getDiskForecast(
      token: string,
      nodeId: number,
      options?: NodeMetricsRequestOptions,
    ): Promise<DiskForecast> {
      const raw = await request<RawDiskForecast>(`/nodes/${nodeId}/disk-forecast`, {
        token,
        signal: options?.signal,
      });
      return mapDiskForecast(raw);
    },
  };
}
