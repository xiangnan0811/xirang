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
}

export const listSLOs = (token: string) =>
  request<SLODefinition[]>("/slos", { token })

export const createSLO = (token: string, input: SLOInput) =>
  request<SLODefinition>("/slos", { method: "POST", token, body: input })

export const updateSLO = (token: string, id: number, input: SLOInput) =>
  request<SLODefinition>(`/slos/${id}`, { method: "PATCH", token, body: input })

export const deleteSLO = (token: string, id: number) =>
  request<void>(`/slos/${id}`, { method: "DELETE", token })

export const getSLOCompliance = (token: string, id: number) =>
  request<SLOComplianceResult>(`/slos/${id}/compliance`, { token })

export const getSLOSummary = (token: string) =>
  request<SLOSummary>("/slos/compliance-summary", { token })

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
