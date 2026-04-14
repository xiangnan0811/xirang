import type { NewPolicyInput, PolicyRecord } from "@/types/domain";
import i18n from "@/i18n";
import { request } from "./core";

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
  bandwidth_schedule?: string;
};

function mapPolicy(row: PolicyResponse): PolicyRecord {
  return {
    id: row.id,
    name: row.name,
    sourcePath: row.source_path,
    targetPath: row.target_path,
    cron: row.cron_spec,
    naturalLanguage: i18n.t("policies.scheduledByCron", { cron: row.cron_spec }),
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
    bandwidthSchedule: row.bandwidth_schedule ?? undefined,
  };
}

export function createPoliciesApi() {
  return {
    async getPolicies(token: string, options?: { signal?: AbortSignal }): Promise<PolicyRecord[]> {
      const rows = (await request<PolicyResponse[]>("/policies", { token, signal: options?.signal })) ?? [];
      return rows.map((row) => mapPolicy(row));
    },

    async createPolicy(token: string, input: NewPolicyInput): Promise<PolicyRecord> {
      const row = await request<PolicyResponse>("/policies", {
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
          bandwidth_schedule: input.bandwidthSchedule ?? undefined,
        }
      });
      return mapPolicy(row);
    },

    async updatePolicy(token: string, policyId: number, input: NewPolicyInput): Promise<PolicyRecord> {
      const row = await request<PolicyResponse>(`/policies/${policyId}`, {
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
          bandwidth_schedule: input.bandwidthSchedule ?? undefined,
        }
      });
      return mapPolicy(row);
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
      const row = await request<PolicyResponse>(`/policies/from-template/${templateId}`, {
        method: "POST",
        token
      });
      return mapPolicy(row);
    }
  };
}
