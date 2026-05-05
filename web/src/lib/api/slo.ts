import { request } from "./core"
import type {
  SLODefinition,
  SLOComplianceResult,
  SLOSummary,
} from "@/types/domain"

export type SLOInput = {
  name: string
  metric_type: "availability" | "success_rate"
  match_tags: string[]
  threshold: number
  window_days: number
  enabled: boolean
  escalation_policy_id?: number | null
}

// Parse match_tags from server representation (JSON string, array, or null)
// to canonical string[] for UI use.
export function parseSLOTags(s: Pick<SLODefinition, "match_tags">): string[] {
  if (!s.match_tags || (typeof s.match_tags === "string" && s.match_tags.trim() === "")) return []
  if (Array.isArray(s.match_tags)) return s.match_tags
  try {
    const parsed = JSON.parse(s.match_tags)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

export function createSLOApi() {
  return {
    async listSLOs(token: string, options?: { signal?: AbortSignal }): Promise<SLODefinition[]> {
      return request<SLODefinition[]>("/slos", { token, signal: options?.signal })
    },

    async createSLO(token: string, input: SLOInput): Promise<SLODefinition> {
      return request<SLODefinition>("/slos", { method: "POST", token, body: input })
    },

    async updateSLO(token: string, id: number, input: SLOInput): Promise<SLODefinition> {
      return request<SLODefinition>(`/slos/${id}`, { method: "PATCH", token, body: input })
    },

    async deleteSLO(token: string, id: number): Promise<void> {
      return request<void>(`/slos/${id}`, { method: "DELETE", token })
    },

    async getSLOCompliance(token: string, id: number, options?: { signal?: AbortSignal }): Promise<SLOComplianceResult> {
      return request<SLOComplianceResult>(`/slos/${id}/compliance`, { token, signal: options?.signal })
    },

    async getSLOSummary(token: string, options?: { signal?: AbortSignal }): Promise<SLOSummary> {
      return request<SLOSummary>("/slos/compliance-summary", { token, signal: options?.signal })
    },
  }
}
