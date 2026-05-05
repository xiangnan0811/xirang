import { request } from "./core"
import type { Silence, SilenceInput } from "@/types/domain"

export type { Silence, SilenceInput } from "@/types/domain"

export function parseSilenceTags(s: Pick<Silence, "match_tags">): string[] {
  if (!s.match_tags || (typeof s.match_tags === "string" && s.match_tags.trim() === "")) return []
  if (Array.isArray(s.match_tags)) return s.match_tags
  try {
    const parsed = JSON.parse(s.match_tags)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

export function createSilencesApi() {
  return {
    async listSilences(token: string, activeOnly = false, options?: { signal?: AbortSignal }): Promise<Silence[]> {
      return request<Silence[]>(`/silences${activeOnly ? "?active=true" : ""}`, { token, signal: options?.signal })
    },

    async createSilence(token: string, s: SilenceInput): Promise<Silence> {
      return request<Silence>("/silences", { method: "POST", token, body: s })
    },

    async deleteSilence(token: string, id: number): Promise<void> {
      return request<void>(`/silences/${id}`, { method: "DELETE", token })
    },
  }
}
