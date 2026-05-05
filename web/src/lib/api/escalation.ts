import { request } from "./core";
import type {
  EscalationEvent,
  EscalationLevel,
  EscalationPolicy,
} from "@/types/domain";

export type EscalationPolicyInput = {
  name: string;
  description?: string;
  min_severity: "info" | "warning" | "critical";
  enabled: boolean;
  levels: EscalationLevel[];
};

// Backend stores levels as JSON string; transform both directions.
type PolicyWire = Omit<EscalationPolicy, "levels"> & { levels: string };

function decodePolicy(p: PolicyWire): EscalationPolicy {
  let levels: EscalationLevel[] = [];
  try {
    levels = JSON.parse(p.levels) as EscalationLevel[];
  } catch {
    levels = [];
  }
  return { ...p, levels };
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
      return list.map(decodePolicy);
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
        body: input,
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
        body: input,
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
      return request<EscalationEvent[]>(`/alerts/${alertId}/escalation-events`, {
        token,
        signal: options?.signal,
      });
    },
  };
}
