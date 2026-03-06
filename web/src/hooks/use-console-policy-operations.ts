import { useCallback, type Dispatch, type SetStateAction } from "react";
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
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

export function usePolicyOperations({
  token,
  policies,
  setPolicies,
  setTasks,
  setAlerts,
  ensureDemoWriteAllowed,
  handleWriteApiError
}: UsePolicyOperationsParams) {
  const exec = useApiAction({ token, ensureDemoWriteAllowed, handleWriteApiError });

  const createPolicy = useCallback(async (input: NewPolicyInput) => {
    const result = await exec("创建策略", (t) => apiClient.createPolicy(t, input));
    if (result) {
      if (result.ok) {
        setPolicies((prev) => [{
          ...result.data,
          criticalThreshold: input.criticalThreshold,
          naturalLanguage: describeCron(result.data.cron)
        }, ...prev]);
      }
      return;
    }
    setPolicies((prev) => [buildDemoPolicy(input, policies), ...prev]);
  }, [exec, policies, setPolicies]);

  const updatePolicy = useCallback(async (policyID: number, input: NewPolicyInput) => {
    const result = await exec("更新策略", (t) => apiClient.updatePolicy(t, policyID, input));
    if (result) {
      if (result.ok) {
        setPolicies((prev) => prev.map((policy) => (policy.id === policyID ? {
          ...result.data,
          criticalThreshold: input.criticalThreshold,
          naturalLanguage: describeCron(result.data.cron)
        } : policy)));
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
              targetPath: input.targetPath,
              cron: input.cron,
              naturalLanguage: describeCron(input.cron),
              enabled: input.enabled,
              criticalThreshold: Math.max(1, input.criticalThreshold)
            }
          : policy
      )
    );
  }, [exec, setPolicies]);

  const deletePolicy = useCallback(async (policyID: number) => {
    await exec("删除策略", (t) => apiClient.deletePolicy(t, policyID));
    const policyName = policies.find((policy) => policy.id === policyID)?.name;
    setPolicies((prev) => prev.filter((policy) => policy.id !== policyID));
    setTasks((prev) => prev.filter((task) => task.policyId !== policyID));
    if (policyName) {
      setAlerts((prev) => prev.filter((alert) => alert.policyName !== policyName));
    }
  }, [exec, policies, setAlerts, setPolicies, setTasks]);

  const togglePolicy = useCallback(async (policyID: number) => {
    const current = policies.find((policy) => policy.id === policyID);
    if (!current) {
      return;
    }
    await updatePolicy(policyID, {
      name: current.name,
      sourcePath: current.sourcePath,
      targetPath: current.targetPath,
      cron: current.cron,
      criticalThreshold: current.criticalThreshold,
      enabled: !current.enabled
    });
  }, [policies, updatePolicy]);

  const updatePolicySchedule = useCallback(async (policyID: number, cron: string, naturalLanguage: string) => {
    const current = policies.find((policy) => policy.id === policyID);
    if (!current) {
      return;
    }
    await updatePolicy(policyID, {
      name: current.name,
      sourcePath: current.sourcePath,
      targetPath: current.targetPath,
      criticalThreshold: current.criticalThreshold,
      enabled: current.enabled,
      cron
    });
    setPolicies((prev) =>
      prev.map((policy) =>
        policy.id === policyID
          ? { ...policy, naturalLanguage }
          : policy
      )
    );
  }, [policies, setPolicies, updatePolicy]);

  return {
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    updatePolicySchedule
  };
}
