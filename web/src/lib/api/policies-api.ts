import type { NewPolicyInput, PolicyRecord } from "@/types/domain";
import { request, type Envelope, unwrapData } from "./core";

type PolicyResponse = {
  id: number;
  name: string;
  source_path: string;
  target_path: string;
  cron_spec: string;
  enabled: boolean;
  node_ids?: number[];
  verify_enabled?: boolean;
  verify_sample_rate?: number;
  is_template?: boolean;
  pre_hook?: string;
  post_hook?: string;
  hook_timeout_seconds?: number;
  max_retries?: number;
  retry_base_seconds?: number;
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
    criticalThreshold: 2,
    nodeIds: row.node_ids ?? [],
    verifyEnabled: row.verify_enabled ?? false,
    verifySampleRate: row.verify_sample_rate ?? 0,
    isTemplate: row.is_template ?? false,
    preHook: row.pre_hook ?? undefined,
    postHook: row.post_hook ?? undefined,
    hookTimeoutSeconds: row.hook_timeout_seconds ?? undefined,
    maxRetries: row.max_retries ?? undefined,
    retryBaseSeconds: row.retry_base_seconds ?? undefined,
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
          enabled: input.enabled,
          node_ids: input.nodeIds,
          verify_enabled: input.verifyEnabled,
          verify_sample_rate: input.verifySampleRate,
          pre_hook: input.preHook ?? undefined,
          post_hook: input.postHook ?? undefined,
          hook_timeout_seconds: input.hookTimeoutSeconds ?? undefined,
          max_retries: input.maxRetries ?? undefined,
          retry_base_seconds: input.retryBaseSeconds ?? undefined,
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
          enabled: input.enabled,
          node_ids: input.nodeIds,
          verify_enabled: input.verifyEnabled,
          verify_sample_rate: input.verifySampleRate,
          pre_hook: input.preHook ?? undefined,
          post_hook: input.postHook ?? undefined,
          hook_timeout_seconds: input.hookTimeoutSeconds ?? undefined,
          max_retries: input.maxRetries ?? undefined,
          retry_base_seconds: input.retryBaseSeconds ?? undefined,
        }
      });
      return mapPolicy(unwrapData(payload));
    },

    async deletePolicy(token: string, policyId: number): Promise<void> {
      await request(`/policies/${policyId}`, {
        method: "DELETE",
        token
      });
    },

    async batchTogglePolicies(token: string, policyIds: number[], enabled: boolean): Promise<void> {
      await request("/policies/batch-toggle", {
        method: "POST",
        token,
        body: { policy_ids: policyIds, enabled }
      });
    },

    async clonePolicyFromTemplate(token: string, templateId: number): Promise<PolicyRecord> {
      const payload = await request<Envelope<PolicyResponse>>(`/policies/from-template/${templateId}`, {
        method: "POST",
        token
      });
      return mapPolicy(unwrapData(payload));
    }
  };
}
