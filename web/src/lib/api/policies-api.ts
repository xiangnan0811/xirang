import type { NewPolicyInput, PolicyLatestDrillSummary, PolicyRecord, RestoreDrillStatus } from "@/types/domain";
import i18n from "@/i18n";
import { request } from "./core";
import { finiteNumber } from "./number-utils";

type LatestDrillSummaryResponse = {
  task_run_id: number;
  status: string;
  failed_step?: string;
  confidence_eligible?: boolean;
  started_at?: string | null;
  finished_at?: string | null;
  duration_ms?: number;
};

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
  escalation_policy_id?: number | null;
  app_profile?: string;
  app_credential_id?: number | null;
  // Retention & SLA
  retention_days?: number;
  retention_mode?: string;
  keep_daily?: number;
  keep_weekly?: number;
  keep_monthly?: number;
  keep_yearly?: number;
  rpo_minutes?: number;
  rto_minutes?: number;
  // Recovery drill
  drill_enabled?: boolean;
  drill_cron?: string;
  drill_target_node_id?: number | null;
  drill_restore_path?: string;
  drill_pre_verify?: string;
  drill_verify?: string;
  drill_post_verify?: string;
  drill_auto_cleanup?: boolean;
  latest_drill?: LatestDrillSummaryResponse | null;
};

function mapDrillStatus(raw?: string): RestoreDrillStatus {
  switch (raw) {
    case "running":
    case "success":
    case "failed":
    case "skipped":
    case "canceled":
      return raw;
    default:
      return "pending";
  }
}

function mapLatestDrillSummary(row?: LatestDrillSummaryResponse | null): PolicyLatestDrillSummary | null {
  if (!row) return null;
  return {
    taskRunId: finiteNumber(row.task_run_id),
    status: mapDrillStatus(row.status),
    failedStep: row.failed_step || undefined,
    confidenceEligible: row.confidence_eligible ?? false,
    startedAt: row.started_at ?? undefined,
    finishedAt: row.finished_at ?? undefined,
    durationMs: finiteNumber(row.duration_ms),
  };
}

function mapPolicy(row: PolicyResponse): PolicyRecord {
  // 防御：后端任意返回路径若漏掉 cron_spec，避免污染下游 describeCron。
  const cron = typeof row.cron_spec === "string" ? row.cron_spec : "";
  return {
    id: row.id,
    name: row.name,
    sourcePath: row.source_path,
    targetPath: row.target_path,
    cron,
    naturalLanguage: i18n.t("policies.scheduledByCron", { cron }),
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
    escalation_policy_id: row.escalation_policy_id ?? null,
    app_profile: row.app_profile ?? "",
    app_credential_id: row.app_credential_id ?? null,
    retention_days: row.retention_days ?? 7,
    retention_mode: row.retention_mode ?? "simple",
    keep_daily: row.keep_daily ?? 0,
    keep_weekly: row.keep_weekly ?? 0,
    keep_monthly: row.keep_monthly ?? 0,
    keep_yearly: row.keep_yearly ?? 0,
    rpo_minutes: row.rpo_minutes ?? 0,
    rto_minutes: row.rto_minutes ?? 0,
    drill_enabled: row.drill_enabled ?? false,
    drill_cron: row.drill_cron ?? "",
    drill_target_node_id: row.drill_target_node_id ?? null,
    drill_restore_path: row.drill_restore_path ?? "/tmp/xirang-drill",
    drill_pre_verify: row.drill_pre_verify ?? "",
    drill_verify: row.drill_verify ?? "",
    drill_post_verify: row.drill_post_verify ?? "",
    drill_auto_cleanup: row.drill_auto_cleanup ?? true,
    latestDrill: mapLatestDrillSummary(row.latest_drill),
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
          escalation_policy_id: input.escalation_policy_id ?? undefined,
          app_profile: input.app_profile ?? undefined,
          app_credential_id: input.app_credential_id ?? undefined,
          retention_days: input.retention_days ?? undefined,
          retention_mode: input.retention_mode ?? undefined,
          keep_daily: input.keep_daily ?? undefined,
          keep_weekly: input.keep_weekly ?? undefined,
          keep_monthly: input.keep_monthly ?? undefined,
          keep_yearly: input.keep_yearly ?? undefined,
          rpo_minutes: input.rpo_minutes ?? undefined,
          rto_minutes: input.rto_minutes ?? undefined,
          drill_enabled: input.drill_enabled ?? undefined,
          drill_cron: input.drill_cron ?? undefined,
          drill_target_node_id: input.drill_target_node_id ?? undefined,
          drill_restore_path: input.drill_restore_path ?? undefined,
          drill_pre_verify: input.drill_pre_verify ?? undefined,
          drill_verify: input.drill_verify ?? undefined,
          drill_post_verify: input.drill_post_verify ?? undefined,
          drill_auto_cleanup: input.drill_auto_cleanup ?? undefined,
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
          escalation_policy_id: input.escalation_policy_id ?? undefined,
          app_profile: input.app_profile ?? undefined,
          app_credential_id: input.app_credential_id ?? undefined,
          retention_days: input.retention_days ?? undefined,
          retention_mode: input.retention_mode ?? undefined,
          keep_daily: input.keep_daily ?? undefined,
          keep_weekly: input.keep_weekly ?? undefined,
          keep_monthly: input.keep_monthly ?? undefined,
          keep_yearly: input.keep_yearly ?? undefined,
          rpo_minutes: input.rpo_minutes ?? undefined,
          rto_minutes: input.rto_minutes ?? undefined,
          drill_enabled: input.drill_enabled ?? undefined,
          drill_cron: input.drill_cron ?? undefined,
          drill_target_node_id: input.drill_target_node_id ?? undefined,
          drill_restore_path: input.drill_restore_path ?? undefined,
          drill_pre_verify: input.drill_pre_verify ?? undefined,
          drill_verify: input.drill_verify ?? undefined,
          drill_post_verify: input.drill_post_verify ?? undefined,
          drill_auto_cleanup: input.drill_auto_cleanup ?? undefined,
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
    },

    async triggerDrill(token: string, policyId: number): Promise<{task_run_id: number; message: string}> {
      return request<{task_run_id: number; message: string}>(`/policies/${policyId}/drill-trigger`, {
        method: "POST",
        token
      });
    }
  };
}
