import type { AutomationRule, AutomationRuleInput } from "@/types/domain";
import { request } from "./core";

type RawAutomationRule = {
  id: number;
  name?: string;
  description?: string;
  event_type?: string;
  event_filter?: unknown;
  action_type?: string;
  action_config?: unknown;
  enabled?: boolean;
  created_at?: string;
  updated_at?: string;
};

function asStringRecord(value: unknown): Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return {};
  }
  const out: Record<string, string> = {};
  for (const [key, entry] of Object.entries(value as Record<string, unknown>)) {
    if (typeof entry === "string") {
      out[key] = entry;
    } else if (typeof entry === "number" || typeof entry === "boolean") {
      out[key] = String(entry);
    }
  }
  return out;
}

function parseJsonRecord(raw: unknown): Record<string, string> {
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (!trimmed) {
      return {};
    }
    try {
      return asStringRecord(JSON.parse(trimmed) as unknown);
    } catch {
      return {};
    }
  }
  return asStringRecord(raw);
}

function mapRule(row: RawAutomationRule): AutomationRule {
  return {
    id: row.id,
    name: String(row.name ?? ""),
    description: String(row.description ?? ""),
    eventType: String(row.event_type ?? ""),
    eventFilter: parseJsonRecord(row.event_filter),
    actionType: String(row.action_type ?? ""),
    actionConfig: parseJsonRecord(row.action_config),
    enabled: Boolean(row.enabled),
    createdAt: String(row.created_at ?? ""),
    updatedAt: String(row.updated_at ?? ""),
  };
}

function toRuleWire(input: AutomationRuleInput) {
  return {
    name: input.name,
    description: input.description,
    event_type: input.eventType,
    event_filter: JSON.stringify(input.eventFilter ?? {}),
    action_type: input.actionType,
    action_config: JSON.stringify(input.actionConfig ?? {}),
    enabled: input.enabled,
  };
}

export function createAutomationRulesApi() {
  return {
    async list(token: string, options?: { signal?: AbortSignal }): Promise<AutomationRule[]> {
      const rows = (await request<RawAutomationRule[]>("/automation-rules", { token, signal: options?.signal })) ?? [];
      return rows.map(mapRule);
    },

    async create(token: string, input: AutomationRuleInput): Promise<AutomationRule> {
      const row = await request<RawAutomationRule>("/automation-rules", {
        method: "POST",
        body: toRuleWire(input),
        token,
      });
      return mapRule(row);
    },

    async update(token: string, id: number, input: AutomationRuleInput): Promise<AutomationRule> {
      const row = await request<RawAutomationRule>(`/automation-rules/${id}`, {
        method: "PUT",
        body: toRuleWire(input),
        token,
      });
      return mapRule(row);
    },

    async delete(token: string, id: number): Promise<void> {
      await request<void>(`/automation-rules/${id}`, { method: "DELETE", token });
    },
  };
}
