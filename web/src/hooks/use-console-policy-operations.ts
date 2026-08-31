import { useCallback, type Dispatch, type SetStateAction } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { describeCron } from "@/hooks/use-console-data.utils";
import { useApiAction } from "@/hooks/use-api-action";
import { buildDemoPolicy } from "@/hooks/use-console-data.demo";
import type {
  AlertRecord,
  NewPolicyInput,
  PolicyRecord,
  TaskRecord
} from "@/types/domain";

type UsePolicyOperationsParams = {
  token: string | null;
  policies: PolicyRecord[];
  setPolicies: Dispatch<SetStateAction<PolicyRecord[]>>;
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

export function usePolicyOperations({
  token,
  policies,
  setPolicies,
  setTasks,
  setAlerts,
  markTasksMutated,
  ensureDemoWriteAllowed,
  handleWriteApiError
}: UsePolicyOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });

  const refreshTasks = useCallback(async () => {
    if (!token) return;
    try {
      const tasks = await apiClient.getTasks(token);
      markTasksMutated();
      setTasks(tasks);
    } catch {
      // 静默失败，下次全量刷新会补上
    }
  }, [token, markTasksMutated, setTasks]);

  const createPolicy = useCallback(async (input: NewPolicyInput) => {
    const result = await exec(i18n.t("policies.actions.createPolicy"), (t) => apiClient.createPolicy(t, input));
    if (result) {
      if (result.ok) {
        setPolicies((prev) => [{
          ...result.data,
          criticalThreshold: input.criticalThreshold,
          naturalLanguage: describeCron(result.data.cron)
        }, ...prev]);
        // 后端 SyncPolicyTasks 会自动创建关联任务，需刷新任务列表
        if (input.nodeIds.length > 0) {
          void refreshTasks();
        }
      }
      return;
    }
    setPolicies((prev) => [buildDemoPolicy(input, policies), ...prev]);
  }, [exec, policies, refreshTasks, setPolicies]);

  const updatePolicy = useCallback(async (policyID: number, input: NewPolicyInput) => {
    const result = await exec(i18n.t("policies.actions.updatePolicy"), (t) => apiClient.updatePolicy(t, policyID, input));
    if (result) {
      if (result.ok) {
        setPolicies((prev) => prev.map((policy) => (policy.id === policyID ? {
          ...result.data,
          criticalThreshold: input.criticalThreshold,
          naturalLanguage: describeCron(result.data.cron)
        } : policy)));
        // 后端 SyncPolicyTasks 会同步更新关联任务，需刷新任务列表
        void refreshTasks();
      }
      return;
    }
    setPolicies((prev) =>
      prev.map((policy) =>
        policy.id === policyID
          ? {
              ...policy,
              name: input.name,
              sourcePath: input.sourcePath,
              targetPath: input.targetPath || "/backup",
              cron: input.cron,
              naturalLanguage: describeCron(input.cron),
              enabled: input.enabled,
              criticalThreshold: Math.max(1, input.criticalThreshold),
              nodeIds: input.nodeIds,
              verifyEnabled: input.verifyEnabled,
              verifySampleRate: input.verifySampleRate,
            }
          : policy
      )
    );
  }, [exec, refreshTasks, setPolicies]);

  const buildPolicyUpdateInput = useCallback((policy: PolicyRecord, overrides: Partial<NewPolicyInput> = {}): NewPolicyInput => ({
    name: policy.name,
    sourcePath: policy.sourcePath,
    targetPath: policy.targetPath,
    cron: policy.cron,
    criticalThreshold: policy.criticalThreshold,
    enabled: policy.enabled,
    nodeIds: policy.nodeIds ?? [],
    verifyEnabled: policy.verifyEnabled ?? false,
    verifySampleRate: policy.verifySampleRate ?? 0,
    preHook: policy.preHook,
    postHook: policy.postHook,
    hookTimeoutSeconds: policy.hookTimeoutSeconds,
    maxRetries: policy.maxRetries,
    retryBaseSeconds: policy.retryBaseSeconds,
    bandwidthSchedule: policy.bandwidthSchedule,
    escalationPolicyId: policy.escalationPolicyId ?? null,
    appProfile: policy.appProfile,
    appCredentialId: policy.appCredentialId ?? null,
    retentionDays: policy.retentionDays,
    retentionMode: policy.retentionMode,
    keepDaily: policy.keepDaily,
    keepWeekly: policy.keepWeekly,
    keepMonthly: policy.keepMonthly,
    keepYearly: policy.keepYearly,
    rpoMinutes: policy.rpoMinutes,
    rtoMinutes: policy.rtoMinutes,
    drillEnabled: policy.drillEnabled,
    drillCron: policy.drillCron,
    drillTargetNodeId: policy.drillTargetNodeId ?? null,
    drillRestorePath: policy.drillRestorePath,
    drillPreVerify: policy.drillPreVerify,
    drillVerify: policy.drillVerify,
    drillPostVerify: policy.drillPostVerify,
    drillAutoCleanup: policy.drillAutoCleanup,
    ...overrides,
  }), []);

  const deletePolicy = useCallback(async (policyID: number) => {
    await exec(i18n.t("policies.actions.deletePolicy"), (t) => apiClient.deletePolicy(t, policyID));
    const policyName = policies.find((policy) => policy.id === policyID)?.name;
    setPolicies((prev) => prev.filter((policy) => policy.id !== policyID));
    markTasksMutated();
    setTasks((prev) => prev.filter((task) => task.policyId !== policyID));
    if (policyName) {
      setAlerts((prev) => prev.filter((alert) => alert.policyName !== policyName));
    }
  }, [exec, markTasksMutated, policies, setAlerts, setPolicies, setTasks]);

  const togglePolicy = useCallback(async (policyID: number) => {
    const current = policies.find((policy) => policy.id === policyID);
    if (!current) {
      return;
    }
    await updatePolicy(policyID, buildPolicyUpdateInput(current, { enabled: !current.enabled }));
  }, [buildPolicyUpdateInput, policies, updatePolicy]);

  const updatePolicySchedule = useCallback(async (policyID: number, cron: string, naturalLanguage: string) => {
    const current = policies.find((policy) => policy.id === policyID);
    if (!current) {
      return;
    }
    await updatePolicy(policyID, buildPolicyUpdateInput(current, { cron }));
    setPolicies((prev) =>
      prev.map((policy) =>
        policy.id === policyID
          ? { ...policy, naturalLanguage }
          : policy
      )
    );
  }, [buildPolicyUpdateInput, policies, setPolicies, updatePolicy]);

  return {
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    updatePolicySchedule
  };
}
