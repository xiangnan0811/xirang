import { request } from "./core";
import { finiteNumber } from "./number-utils";
import type { Silence, SilenceInput } from "@/types/domain";

export type { Silence, SilenceInput };

/** Wire shape from /silences — not exported (API boundary only). */
type RawSilence = {
  id?: unknown;
  name?: unknown;
  match_node_id?: unknown;
  match_category?: unknown;
  match_tags?: unknown;
  starts_at?: unknown;
  ends_at?: unknown;
  created_by?: unknown;
  note?: unknown;
  created_at?: unknown;
  updated_at?: unknown;
};

/** Wire body for create (backend expects snake_case). */
type WireSilenceInput = {
  name: string;
  match_node_id: number | null;
  match_category: string;
  match_tags: string[];
  starts_at: string;
  ends_at: string;
  note?: string;
};

function asString(value: unknown, fallback = ""): string {
  if (typeof value === "string") return value;
  if (value == null) return fallback;
  return String(value);
}

function asID(value: unknown): number {
  const n = finiteNumber(value, 0);
  return n >= 0 ? Math.trunc(n) : 0;
}

function normalizeTags(matchTags: unknown): string[] {
  if (matchTags == null) return [];
  if (typeof matchTags === "string") {
    if (matchTags.trim() === "") return [];
    try {
      const parsed: unknown = JSON.parse(matchTags);
      return normalizeTags(parsed);
    } catch {
      return [];
    }
  }
  if (!Array.isArray(matchTags)) return [];
  return matchTags
    .filter((t): t is string => typeof t === "string")
    .map((t) => t.trim())
    .filter((t) => t.length > 0);
}

export function mapSilence(raw: RawSilence | null | undefined): Silence {
  const row = raw ?? {};
  const nodeID = row.match_node_id;
  return {
    id: asID(row.id),
    name: asString(row.name),
    matchNodeId: nodeID == null || nodeID === "" ? null : asID(nodeID) || null,
    matchCategory: asString(row.match_category),
    matchTags: normalizeTags(row.match_tags),
    startsAt: asString(row.starts_at),
    endsAt: asString(row.ends_at),
    createdBy: asID(row.created_by),
    note: asString(row.note),
    createdAt: asString(row.created_at),
    updatedAt: asString(row.updated_at),
  };
}

/** Prefer Silence.matchTags; kept for call sites that still invoke the helper. */
export function parseSilenceTags(s: Pick<Silence, "matchTags">): string[] {
  return Array.isArray(s.matchTags) ? s.matchTags : [];
}

function toWireInput(input: SilenceInput): WireSilenceInput {
  return {
    name: input.name,
    match_node_id: input.matchNodeId,
    match_category: input.matchCategory,
    match_tags: input.matchTags,
    starts_at: input.startsAt,
    ends_at: input.endsAt,
    note: input.note,
  };
}

export function createSilencesApi() {
  return {
    async listSilences(token: string, activeOnly = false, options?: { signal?: AbortSignal }): Promise<Silence[]> {
      const rows = await request<RawSilence[]>(`/silences${activeOnly ? "?active=true" : ""}`, {
        token,
        signal: options?.signal,
      });
      return (Array.isArray(rows) ? rows : []).map(mapSilence);
    },

    async createSilence(token: string, s: SilenceInput): Promise<Silence> {
      const row = await request<RawSilence>("/silences", {
        method: "POST",
        token,
        body: toWireInput(s),
      });
      return mapSilence(row);
    },

    async deleteSilence(token: string, id: number): Promise<void> {
      return request<void>(`/silences/${id}`, { method: "DELETE", token });
    },
  };
}
