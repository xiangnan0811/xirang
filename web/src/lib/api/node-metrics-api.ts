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
  };
}
