import type { NewPolicyInput, PolicyRecord } from "@/types/domain";
import { request, type Envelope, unwrapData } from "./core";

type PolicyResponse = {
  id: number;
  name: string;
  source_path: string;
  target_path: string;
  cron_spec: string;
  enabled: boolean;
};

function mapPolicy(row: PolicyResponse): PolicyRecord {
  return {
    id: row.id,
    name: row.name,
    sourcePath: row.source_path,
    targetPath: row.target_path,
    cron: row.cron_spec,
    naturalLanguage: `按照 ${row.cron_spec} 调度`,
    enabled: row.enabled,
    criticalThreshold: 2
  };
}

export function createPoliciesApi() {
  return {
    async getPolicies(token: string, options?: { signal?: AbortSignal }): Promise<PolicyRecord[]> {
      const payload = await request<Envelope<PolicyResponse[]>>("/policies", { token, signal: options?.signal });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapPolicy(row));
    },

    async createPolicy(token: string, input: NewPolicyInput): Promise<PolicyRecord> {
      const payload = await request<Envelope<PolicyResponse>>("/policies", {
        method: "POST",
        token,
        body: {
          name: input.name,
          source_path: input.sourcePath,
          target_path: input.targetPath,
          cron_spec: input.cron,
          enabled: input.enabled
        }
      });
      return mapPolicy(unwrapData(payload));
    },

    async updatePolicy(token: string, policyId: number, input: NewPolicyInput): Promise<PolicyRecord> {
      const payload = await request<Envelope<PolicyResponse>>(`/policies/${policyId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          source_path: input.sourcePath,
          target_path: input.targetPath,
          cron_spec: input.cron,
          enabled: input.enabled
        }
      });
      return mapPolicy(unwrapData(payload));
    },

    async deletePolicy(token: string, policyId: number): Promise<void> {
      await request(`/policies/${policyId}`, {
        method: "DELETE",
        token
      });
    }
  };
}
