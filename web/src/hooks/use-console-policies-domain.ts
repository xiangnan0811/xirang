import { useCallback, useRef, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import { usePolicyOperations } from "@/hooks/use-console-policy-operations";
import type {
  AlertRecord,
  PolicyRecord,
  TaskRecord,
} from "@/types/domain";

export type UsePoliciesDomainParams = {
  token: string | null;
  policies: PolicyRecord[];
  setPolicies: Dispatch<SetStateAction<PolicyRecord[]>>;
  setTasks: Dispatch<SetStateAction<TaskRecord[]>>;
  setAlerts: Dispatch<SetStateAction<AlertRecord[]>>;
  markTasksMutated: () => void;
  ensureDemoWriteAllowed: (action: string) => void;
  handleWriteApiError: (action: string, error: unknown) => void;
};

// 策略域：自持 refreshPolicies 与 abort 控制，接线 usePolicyOperations。
export function usePoliciesDomain({
  token,
  policies,
  setPolicies,
  setTasks,
  setAlerts,
  markTasksMutated,
  ensureDemoWriteAllowed,
  handleWriteApiError,
}: UsePoliciesDomainParams) {
  const refreshPoliciesAbortRef = useRef<AbortController | null>(null);

  const refreshPolicies = useCallback(async () => {
    if (!token) return;
    refreshPoliciesAbortRef.current?.abort();
    const controller = new AbortController();
    refreshPoliciesAbortRef.current = controller;
    try {
      const result = await apiClient.getPolicies(token);
      setPolicies(result);
    } catch {
      // 按需刷新失败时静默处理
    }
  }, [token, setPolicies]);

  const {
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    updatePolicySchedule,
  } = usePolicyOperations({
    token,
    policies,
    setPolicies,
    setTasks,
    setAlerts,
    markTasksMutated,
    ensureDemoWriteAllowed,
    handleWriteApiError,
  });

  return {
    policies,
    refreshPolicies,
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    updatePolicySchedule,
  };
}
