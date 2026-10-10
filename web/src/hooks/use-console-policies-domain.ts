import { useCallback, useLayoutEffect, useRef, useState, type Dispatch, type SetStateAction } from "react";
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

// 已提交身份代不会取 -1。layout 清理用它作废在途回调，避免 A→B→A 复用旧闭包。
const invalidatedIdentityEpoch = -1;

// 策略域：自持 refreshPolicies 与 abort 控制，接线 usePolicyOperations。
// 成功就绪只属于产生该次请求的身份代。token 变化时在渲染期把 loaded/error 收成未就绪；
// 同一次身份的刷新仍保留已成功库存。提交后的 layout 同步中止并作废旧请求。
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
  const [observedToken, setObservedToken] = useState(token);
  const [identityEpoch, setIdentityEpoch] = useState(0);
  const ownershipRef = useRef({ token, epoch: 0 });

  if (observedToken !== token) {
    setObservedToken(token);
    setIdentityEpoch((epoch) => epoch + 1);
    setPoliciesRequest(token ? idleInventoryRequestState : { loading: false, error: null, loaded: true });
  }

  useLayoutEffect(() => {
    ownershipRef.current = { token, epoch: identityEpoch };
    policiesRequestGenRef.current += 1;
    refreshPoliciesAbortRef.current?.abort();
    refreshPoliciesAbortRef.current = null;
    return () => {
      ownershipRef.current = { token, epoch: invalidatedIdentityEpoch };
      policiesRequestGenRef.current += 1;
      refreshPoliciesAbortRef.current?.abort();
      refreshPoliciesAbortRef.current = null;
    };
  }, [identityEpoch, token]);

  const refreshPolicies = useCallback(async () => {
    const epoch = identityEpoch;
    const ownsRequest = () => ownershipRef.current.token === token && ownershipRef.current.epoch === epoch;
    if (!ownsRequest()) return;
    if (!token) {
      setPoliciesRequest({ loading: false, error: null, loaded: true });
      return;
    }
    refreshPoliciesAbortRef.current?.abort();
    const controller = new AbortController();
    refreshPoliciesAbortRef.current = controller;
    const requestGen = ++policiesRequestGenRef.current;
    const stillCurrent = () =>
      ownsRequest() && policiesRequestGenRef.current === requestGen && !controller.signal.aborted;
    setPoliciesRequest((prev) => ({ ...prev, loading: true }));
    try {
      const result = await apiClient.getPolicies(token, { signal: controller.signal });
      if (!stillCurrent()) return;
      setPolicies(result);
      setPoliciesRequest({ loading: false, error: null, loaded: true });
    } catch (error) {
      if (!stillCurrent() || isAbortError(error)) return;
      setPoliciesRequest((prev) => ({
        loading: false,
        error: getErrorMessage(error, i18n.t("policies.loadFailed")),
        loaded: prev.loaded,
      }));
    }
  }, [identityEpoch, setPolicies, token]);

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
