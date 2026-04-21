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

export const listEscalationPolicies = (token: string) =>
  request<PolicyWire[]>("/escalation-policies", { token }).then((list) =>
    list.map(decodePolicy),
  );

export const getEscalationPolicy = (token: string, id: number) =>
  request<PolicyWire>(`/escalation-policies/${id}`, { token }).then(decodePolicy);

export const createEscalationPolicy = (token: string, input: EscalationPolicyInput) =>
  request<PolicyWire>("/escalation-policies", {
    token,
    method: "POST",
    body: input,
  }).then(decodePolicy);

export const updateEscalationPolicy = (
  token: string,
  id: number,
  input: EscalationPolicyInput,
) =>
  request<PolicyWire>(`/escalation-policies/${id}`, {
    token,
    method: "PATCH",
    body: input,
  }).then(decodePolicy);

export const deleteEscalationPolicy = (token: string, id: number) =>
  request<{ deleted: boolean }>(`/escalation-policies/${id}`, {
    token,
    method: "DELETE",
  });

export const listAlertEscalationEvents = (token: string, alertId: number) =>
  request<EscalationEvent[]>(`/alerts/${alertId}/escalation-events`, { token });
