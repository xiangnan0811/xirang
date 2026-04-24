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

export const listSilences = (token: string, activeOnly = false) =>
  request<Silence[]>(`/silences${activeOnly ? "?active=true" : ""}`, { token })

export const createSilence = (token: string, s: SilenceInput) =>
  request<Silence>("/silences", { method: "POST", token, body: s })

export const deleteSilence = (token: string, id: number) =>
  request<void>(`/silences/${id}`, { method: "DELETE", token })
