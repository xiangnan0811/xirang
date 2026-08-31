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
    escalationPolicyId: row.escalation_policy_id ?? null,
    appProfile: row.app_profile ?? "",
    appCredentialId: row.app_credential_id ?? null,
    retentionDays: row.retention_days ?? 7,
    retentionMode: row.retention_mode ?? "simple",
    keepDaily: row.keep_daily ?? 0,
    keepWeekly: row.keep_weekly ?? 0,
    keepMonthly: row.keep_monthly ?? 0,
    keepYearly: row.keep_yearly ?? 0,
    rpoMinutes: row.rpo_minutes ?? 0,
    rtoMinutes: row.rto_minutes ?? 0,
    drillEnabled: row.drill_enabled ?? false,
    drillCron: row.drill_cron ?? "",
    drillTargetNodeId: row.drill_target_node_id ?? null,
    drillRestorePath: row.drill_restore_path ?? "/tmp/xirang-drill",
    drillPreVerify: row.drill_pre_verify ?? "",
    drillVerify: row.drill_verify ?? "",
    drillPostVerify: row.drill_post_verify ?? "",
    drillAutoCleanup: row.drill_auto_cleanup ?? true,
    latestDrill: mapLatestDrillSummary(row.latest_drill),
  };
}

function toPolicyRequestBody(input: NewPolicyInput) {
  return {
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
    escalation_policy_id: input.escalationPolicyId ?? undefined,
    app_profile: input.appProfile ?? undefined,
    app_credential_id: input.appCredentialId ?? undefined,
    retention_days: input.retentionDays ?? undefined,
    retention_mode: input.retentionMode ?? undefined,
    keep_daily: input.keepDaily ?? undefined,
    keep_weekly: input.keepWeekly ?? undefined,
    keep_monthly: input.keepMonthly ?? undefined,
    keep_yearly: input.keepYearly ?? undefined,
    rpo_minutes: input.rpoMinutes ?? undefined,
    rto_minutes: input.rtoMinutes ?? undefined,
    drill_enabled: input.drillEnabled ?? undefined,
    drill_cron: input.drillCron ?? undefined,
    drill_target_node_id: input.drillTargetNodeId ?? undefined,
    drill_restore_path: input.drillRestorePath ?? undefined,
    drill_pre_verify: input.drillPreVerify ?? undefined,
    drill_verify: input.drillVerify ?? undefined,
    drill_post_verify: input.drillPostVerify ?? undefined,
    drill_auto_cleanup: input.drillAutoCleanup ?? undefined,
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
        body: toPolicyRequestBody(input),
      });
      return mapPolicy(row);
    },

    async updatePolicy(token: string, policyId: number, input: NewPolicyInput): Promise<PolicyRecord> {
      const row = await request<PolicyResponse>(`/policies/${policyId}`, {
        method: "PUT",
        token,
        body: toPolicyRequestBody(input),
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

    async triggerDrill(token: string, policyId: number): Promise<{ taskRunId: number; message: string }> {
      const row = await request<{ task_run_id?: unknown; message?: unknown }>(`/policies/${policyId}/drill-trigger`, {
        method: "POST",
        token
      });
      return {
        taskRunId: finiteNumber(row?.task_run_id),
        message: String(row?.message ?? ""),
      };
    }
  };
}
