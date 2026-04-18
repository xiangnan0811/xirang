import { request } from "./core";

export interface NodeMetricSample {
  id: number;
  node_id: number;
  cpu_pct: number;
  mem_pct: number;
  disk_pct: number;
  load_1m: number;
  sampled_at: string;
}

export interface NodeStatus {
  probed_at: string | null;
  online: boolean;
  current: Record<string, number>;
  trend_1h: Record<string, number>;
  trend_24h: Record<string, number>;
  open_alerts: number;
  running_tasks: number;
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
export interface MetricSeriesResponse {
  granularity: "raw" | "hourly" | "daily";
  bucket_seconds: number;
  series: MetricSeries[];
}

export interface DiskForecast {
  disk_gb_total: number;
  disk_gb_used_now: number;
  daily_growth_gb: number | null;
  forecast: {
    days_to_full: number | null;
    date_full: string | null;
    confidence: "high" | "medium" | "low" | "insufficient";
  };
}

export function createNodeMetricsApi() {
  return {
    async getNodeMetrics(
      token: string,
      nodeId: number,
      params?: { limit?: number; since?: string }
    ): Promise<{ items: NodeMetricSample[] }> {
      const query = new URLSearchParams();
      if (params?.limit) query.set("limit", String(params.limit));
      if (params?.since) query.set("since", params.since);
      const qs = query.toString();
      return request<{ items: NodeMetricSample[] }>(
        `/nodes/${nodeId}/metrics${qs ? `?${qs}` : ""}`,
        { token }
      );
    },
    async getNodeStatus(token: string, nodeId: number): Promise<NodeStatus> {
      return request<NodeStatus>(`/nodes/${nodeId}/status`, { token });
    },
    async getMetricSeries(
      token: string,
      nodeId: number,
      params: { from: string; to: string; fields?: string[]; granularity?: "auto" | "raw" | "hourly" | "daily" }
    ): Promise<MetricSeriesResponse> {
      const qs = new URLSearchParams({ from: params.from, to: params.to });
      if (params.fields && params.fields.length) qs.set("fields", params.fields.join(","));
      if (params.granularity) qs.set("granularity", params.granularity);
      return request<MetricSeriesResponse>(`/nodes/${nodeId}/metric-series?${qs.toString()}`, { token });
    },
    async getDiskForecast(token: string, nodeId: number): Promise<DiskForecast> {
      return request<DiskForecast>(`/nodes/${nodeId}/disk-forecast`, { token });
    },
  };
}
