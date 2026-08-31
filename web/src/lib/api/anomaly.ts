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
  nodeId?: number
  page?: number
  pageSize?: number
}

type RawAnomalyEvent = {
  id: number
  node_id?: number
  detector?: string
  metric?: string
  severity?: string
  observed_value?: number
  baseline_value?: number
  sigma?: number | null
  forecast_days?: number | null
  alert_id?: number | null
  raised_alert?: boolean
  details?: string
  fired_at?: string
}

type RawAnomalyListResult = {
  data?: RawAnomalyEvent[]
  total?: number
  has_more?: boolean
}

function mapDetector(raw?: string): AnomalyDetector {
  return raw === "disk_forecast" ? "disk_forecast" : "ewma"
}

function mapEvent(row: RawAnomalyEvent): AnomalyEvent {
  return {
    id: row.id,
    nodeId: Number(row.node_id) || 0,
    detector: mapDetector(row.detector),
    metric: String(row.metric ?? ""),
    severity: row.severity === "critical" ? "critical" : "warning",
    observedValue: Number(row.observed_value) || 0,
    baselineValue: Number(row.baseline_value) || 0,
    sigma: row.sigma ?? null,
    forecastDays: row.forecast_days ?? null,
    alertId: row.alert_id ?? null,
    raisedAlert: Boolean(row.raised_alert),
    details: row.details,
    firedAt: String(row.fired_at ?? ""),
  }
}

function buildQuery(q: AnomalyListQuery): string {
  const params = new URLSearchParams()
  if (q.detector) params.set("detector", q.detector)
  if (q.metric) params.set("metric", q.metric)
  if (q.severity) params.set("severity", q.severity)
  if (q.nodeId) params.set("node_id", String(q.nodeId))
  if (q.page) params.set("page", String(q.page))
  if (q.pageSize) params.set("page_size", String(q.pageSize))
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
      const row = await request<RawAnomalyListResult>(`/anomaly-events${buildQuery(q)}`, {
        token,
        signal: options?.signal,
      })
      return {
        data: Array.isArray(row.data) ? row.data.map(mapEvent) : [],
        total: Number(row.total) || 0,
        hasMore: Boolean(row.has_more),
      }
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
      const rows = await request<RawAnomalyEvent[]>(`/nodes/${nodeID}/anomaly-events${suffix}`, {
        token,
        signal: options?.signal,
      })
      return (rows ?? []).map(mapEvent)
    },
  }
}
