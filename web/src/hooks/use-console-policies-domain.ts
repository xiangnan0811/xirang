import { useCallback, useRef, useState, type Dispatch, type SetStateAction } from "react";
import { apiClient } from "@/lib/api/client";
import i18n from "@/i18n";
import { getErrorMessage } from "@/lib/utils";
import { idleInventoryRequestState, isAbortError, type InventoryRequestState } from "@/hooks/inventory-request-state";
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
  const [policiesRequest, setPoliciesRequest] = useState<InventoryRequestState>(idleInventoryRequestState);
  const policiesRequestGenRef = useRef(0);

  const refreshPolicies = useCallback(async () => {
    if (!token) {
      setPoliciesRequest({ loading: false, error: null, loaded: true });
      return;
    }
    refreshPoliciesAbortRef.current?.abort();
    const controller = new AbortController();
    refreshPoliciesAbortRef.current = controller;
    const requestGen = ++policiesRequestGenRef.current;
    setPoliciesRequest((prev) => ({ ...prev, loading: true }));
    try {
      const result = await apiClient.getPolicies(token, { signal: controller.signal });
      if (requestGen !== policiesRequestGenRef.current || controller.signal.aborted) {
        return;
      }
      setPolicies(result);
      setPoliciesRequest({ loading: false, error: null, loaded: true });
    } catch (error) {
      if (requestGen !== policiesRequestGenRef.current || controller.signal.aborted || isAbortError(error)) {
        return;
      }
      setPoliciesRequest((prev) => ({
        loading: false,
        error: getErrorMessage(error, i18n.t("policies.loadFailed")),
        loaded: prev.loaded,
      }));
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
    policiesLoading: policiesRequest.loading,
    policiesError: policiesRequest.error,
    policiesLoaded: policiesRequest.loaded,
    refreshPolicies,
    createPolicy,
    updatePolicy,
    deletePolicy,
    togglePolicy,
    updatePolicySchedule,
  };
}
