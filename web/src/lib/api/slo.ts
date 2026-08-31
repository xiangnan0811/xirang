import { request } from "./core"
import type {
  SLOComplianceResult,
  SLODefinition,
  SLOMetricType,
  SLOStatus,
  SLOSummary,
} from "@/types/domain"

export type SLOInput = {
  name: string
  metricType: SLOMetricType
  matchTags: string[]
  threshold: number
  windowDays: number
  enabled: boolean
  escalationPolicyId?: number | null
}

type RawSLODefinition = {
  id: number
  name: string
  metric_type?: string
  match_tags?: string | string[] | null
  threshold?: number
  window_days?: number
  enabled?: boolean
  created_by?: number
  created_at?: string
  updated_at?: string
  escalation_policy_id?: number | null
}

type RawSLOComplianceResult = {
  slo_id?: number
  name?: string
  metric_type?: string
  window_start?: string
  window_end?: string
  threshold?: number
  observed?: number
  sample_count?: number
  error_budget_remaining_pct?: number
  burn_rate_1h?: number
  status?: string
}

function mapMetricType(raw?: string): SLOMetricType {
  return raw === "success_rate" ? "success_rate" : "availability"
}

function mapSLOStatus(raw?: string): SLOStatus {
  if (raw === "healthy" || raw === "warning" || raw === "breached" || raw === "insufficient_data") {
    return raw
  }
  return "insufficient_data"
}

function mapSLO(row: RawSLODefinition): SLODefinition {
  return {
    id: row.id,
    name: row.name,
    metricType: mapMetricType(row.metric_type),
    matchTags: row.match_tags ?? null,
    threshold: Number(row.threshold) || 0,
    windowDays: Number(row.window_days) || 0,
    enabled: Boolean(row.enabled),
    createdBy: Number(row.created_by) || 0,
    createdAt: String(row.created_at ?? ""),
    updatedAt: String(row.updated_at ?? ""),
    escalationPolicyId: row.escalation_policy_id ?? null,
  }
}

function mapCompliance(row: RawSLOComplianceResult): SLOComplianceResult {
  return {
    sloId: Number(row.slo_id) || 0,
    name: String(row.name ?? ""),
    metricType: mapMetricType(row.metric_type),
    windowStart: String(row.window_start ?? ""),
    windowEnd: String(row.window_end ?? ""),
    threshold: Number(row.threshold) || 0,
    observed: Number(row.observed) || 0,
    sampleCount: Number(row.sample_count) || 0,
    errorBudgetRemainingPct: Number(row.error_budget_remaining_pct) || 0,
    burnRate1h: Number(row.burn_rate_1h) || 0,
    status: mapSLOStatus(row.status),
  }
}

function toSLOWire(input: SLOInput) {
  return {
    name: input.name,
    metric_type: input.metricType,
    match_tags: input.matchTags,
    threshold: input.threshold,
    window_days: input.windowDays,
    enabled: input.enabled,
    escalation_policy_id: input.escalationPolicyId,
  }
}

export function parseSLOTags(s: Pick<SLODefinition, "matchTags">): string[] {
  if (!s.matchTags || (typeof s.matchTags === "string" && s.matchTags.trim() === "")) return []
  if (Array.isArray(s.matchTags)) return s.matchTags
  try {
    const parsed = JSON.parse(s.matchTags)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

export function createSLOApi() {
  return {
    async listSLOs(token: string, options?: { signal?: AbortSignal }): Promise<SLODefinition[]> {
      const rows = await request<RawSLODefinition[]>("/slos", { token, signal: options?.signal })
      return (rows ?? []).map(mapSLO)
    },

    async createSLO(token: string, input: SLOInput): Promise<SLODefinition> {
      const row = await request<RawSLODefinition>("/slos", { method: "POST", token, body: toSLOWire(input) })
      return mapSLO(row)
    },

    async updateSLO(token: string, id: number, input: SLOInput): Promise<SLODefinition> {
      const row = await request<RawSLODefinition>(`/slos/${id}`, { method: "PATCH", token, body: toSLOWire(input) })
      return mapSLO(row)
    },

    async deleteSLO(token: string, id: number): Promise<void> {
      return request<void>(`/slos/${id}`, { method: "DELETE", token })
    },

    async getSLOCompliance(token: string, id: number, options?: { signal?: AbortSignal }): Promise<SLOComplianceResult> {
      const row = await request<RawSLOComplianceResult>(`/slos/${id}/compliance`, { token, signal: options?.signal })
      return mapCompliance(row)
    },

    async getSLOSummary(token: string, options?: { signal?: AbortSignal }): Promise<SLOSummary> {
      return request<SLOSummary>("/slos/compliance-summary", { token, signal: options?.signal })
    },
  }
}
