import { request } from "./core"
import type {
  AnomalyDetector,
  AnomalyEvent,
  AnomalyListResult,
} from "@/types/domain"

export type AnomalyListQuery = {
  detector?: AnomalyDetector
  metric?: string
  severity?: "warning" | "critical"
  node_id?: number
  page?: number
  page_size?: number
}

function buildQuery(q: AnomalyListQuery): string {
  const params = new URLSearchParams()
  if (q.detector) params.set("detector", q.detector)
  if (q.metric) params.set("metric", q.metric)
  if (q.severity) params.set("severity", q.severity)
  if (q.node_id) params.set("node_id", String(q.node_id))
  if (q.page) params.set("page", String(q.page))
  if (q.page_size) params.set("page_size", String(q.page_size))
  const s = params.toString()
  return s ? `?${s}` : ""
}

export function createAnomalyApi() {
  return {
    async listAnomalyEvents(
      token: string,
      q: AnomalyListQuery = {},
      options?: { signal?: AbortSignal },
    ): Promise<AnomalyListResult> {
      return request<AnomalyListResult>(`/anomaly-events${buildQuery(q)}`, {
        token,
        signal: options?.signal,
      })
    },

    async listNodeAnomalyEvents(
      token: string,
      nodeID: number,
      opts: { limit?: number; detector?: AnomalyDetector } = {},
      options?: { signal?: AbortSignal },
    ): Promise<AnomalyEvent[]> {
      const params = new URLSearchParams()
      if (opts.limit) params.set("limit", String(opts.limit))
      if (opts.detector) params.set("detector", opts.detector)
      const s = params.toString()
      const suffix = s ? `?${s}` : ""
      return request<AnomalyEvent[]>(`/nodes/${nodeID}/anomaly-events${suffix}`, {
        token,
        signal: options?.signal,
      })
    },
  }
}
