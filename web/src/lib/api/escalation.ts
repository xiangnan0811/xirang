import { request } from "./core";
import type {
  EscalationEvent,
  EscalationLevel,
  EscalationPolicy,
} from "@/types/domain";

export type EscalationPolicyInput = {
  name: string;
  description?: string;
  minSeverity: "info" | "warning" | "critical";
  enabled: boolean;
  levels: EscalationLevel[];
};

type RawEscalationLevel = {
  delay_seconds?: number;
  integration_ids?: number[];
  severity_override?: string;
  tags?: string[];
};

type PolicyWire = {
  id: number;
  name?: string;
  description?: string;
  min_severity?: string;
  enabled?: boolean;
  levels?: string | RawEscalationLevel[];
  created_at?: string;
  updated_at?: string;
};

type RawEscalationEvent = {
  id: number;
  alert_id?: number;
  escalation_policy_id?: number | null;
  level_index?: number;
  integration_ids?: unknown;
  severity_before?: string;
  severity_after?: string;
  tags_added?: unknown;
  fired_at?: string;
};

function decodeArray<T>(raw: unknown, isItem: (value: unknown) => value is T): T[] {
  let parsed = raw;
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (!trimmed) {
      return [];
    }
    try {
      parsed = JSON.parse(trimmed) as unknown;
    } catch {
      return [];
    }
  }
  if (!Array.isArray(parsed) || !parsed.every(isItem)) {
    return [];
  }
  return parsed;
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

function isString(value: unknown): value is string {
  return typeof value === "string";
}

function mapSeverity(raw?: string): "info" | "warning" | "critical" {
  if (raw === "info" || raw === "warning" || raw === "critical") {
    return raw;
  }
  return "warning";
}

function mapSeverityOverride(raw?: string): EscalationLevel["severityOverride"] {
  if (raw === "info" || raw === "warning" || raw === "critical" || raw === "") {
    return raw;
  }
  return "";
}

function mapLevel(raw: RawEscalationLevel): EscalationLevel {
  return {
    delaySeconds: Number(raw.delay_seconds) || 0,
    integrationIds: Array.isArray(raw.integration_ids) ? raw.integration_ids : [],
    severityOverride: mapSeverityOverride(raw.severity_override),
    tags: Array.isArray(raw.tags) ? raw.tags : [],
  };
}

function isRawLevel(value: unknown): value is RawEscalationLevel {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function decodeLevels(raw: PolicyWire["levels"]): EscalationLevel[] {
  let parsed: unknown = raw;
  if (typeof raw === "string") {
    const trimmed = raw.trim();
    if (!trimmed) {
      return [];
    }
    try {
      parsed = JSON.parse(trimmed) as unknown;
    } catch {
      return [];
    }
  }
  if (!Array.isArray(parsed)) {
    return [];
  }
  return parsed.filter(isRawLevel).map(mapLevel);
}

function decodePolicy(p: PolicyWire): EscalationPolicy {
  return {
    id: p.id,
    name: String(p.name ?? ""),
    description: String(p.description ?? ""),
    minSeverity: mapSeverity(p.min_severity),
    enabled: Boolean(p.enabled),
    levels: decodeLevels(p.levels),
    createdAt: String(p.created_at ?? ""),
    updatedAt: String(p.updated_at ?? ""),
  };
}

function toLevelWire(level: EscalationLevel): RawEscalationLevel {
  return {
    delay_seconds: level.delaySeconds,
    integration_ids: level.integrationIds,
    severity_override: level.severityOverride,
    tags: level.tags,
  };
}

function toPolicyWireBody(input: EscalationPolicyInput) {
  return {
    name: input.name,
    description: input.description,
    min_severity: input.minSeverity,
    enabled: input.enabled,
    levels: input.levels.map(toLevelWire),
  };
}

function mapEvent(row: RawEscalationEvent): EscalationEvent {
  return {
    id: row.id,
    alertId: Number(row.alert_id) || 0,
    escalationPolicyId: row.escalation_policy_id ?? null,
    levelIndex: Number(row.level_index) || 0,
    integrationIds: decodeArray(row.integration_ids, isPositiveInteger),
    severityBefore: mapSeverity(row.severity_before),
    severityAfter: mapSeverity(row.severity_after),
    tagsAdded: decodeArray(row.tags_added, isString),
    firedAt: String(row.fired_at ?? ""),
  };
}

export function createEscalationApi() {
  return {
    async listEscalationPolicies(
      token: string,
      options?: { signal?: AbortSignal },
    ): Promise<EscalationPolicy[]> {
      const list = await request<PolicyWire[]>("/escalation-policies", {
        token,
        signal: options?.signal,
      });
      return (list ?? []).map(decodePolicy);
    },

    async getEscalationPolicy(
      token: string,
      id: number,
      options?: { signal?: AbortSignal },
    ): Promise<EscalationPolicy> {
      const p = await request<PolicyWire>(`/escalation-policies/${id}`, {
        token,
        signal: options?.signal,
      });
      return decodePolicy(p);
    },

    async createEscalationPolicy(
      token: string,
      input: EscalationPolicyInput,
    ): Promise<EscalationPolicy> {
      const p = await request<PolicyWire>("/escalation-policies", {
        token,
        method: "POST",
        body: toPolicyWireBody(input),
      });
      return decodePolicy(p);
    },

    async updateEscalationPolicy(
      token: string,
      id: number,
      input: EscalationPolicyInput,
    ): Promise<EscalationPolicy> {
      const p = await request<PolicyWire>(`/escalation-policies/${id}`, {
        token,
        method: "PATCH",
        body: toPolicyWireBody(input),
      });
      return decodePolicy(p);
    },

    async deleteEscalationPolicy(
      token: string,
      id: number,
    ): Promise<{ deleted: boolean }> {
      return request<{ deleted: boolean }>(`/escalation-policies/${id}`, {
        token,
        method: "DELETE",
      });
    },

    async listAlertEscalationEvents(
      token: string,
      alertId: number,
      options?: { signal?: AbortSignal },
    ): Promise<EscalationEvent[]> {
      const rows = await request<RawEscalationEvent[]>(`/alerts/${alertId}/escalation-events`, {
        token,
        signal: options?.signal,
      });
      return (rows ?? []).map(mapEvent);
    },
  };
}
