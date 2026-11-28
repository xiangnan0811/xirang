import { useEffect, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { NodeRecord } from "@/types/domain";
import type { NodeDetailAuthToken } from "./types";

interface UseNodeRecordResult {
  data: NodeRecord | null;
  isLoading: boolean;
  error: unknown;
}

/** Identity of one navigation; recreated on every input change, so A→B→A yields a fresh scope. */
interface NodeRecordScope {
  nodeId: number;
  token: NodeDetailAuthToken;
}

interface NodeRecordState extends UseNodeRecordResult {
  scope: NodeRecordScope;
}

function initialState(nodeId: number, token: NodeDetailAuthToken): NodeRecordState {
  return { scope: { nodeId, token }, data: null, isLoading: Boolean(token && nodeId > 0), error: null };
}

export function useNodeRecord(nodeId: number, token: NodeDetailAuthToken): UseNodeRecordResult {
  const [state, setState] = useState(() => initialState(nodeId, token));
  if (state.scope.nodeId !== nodeId || state.scope.token !== token) {
    setState(initialState(nodeId, token));
  }
  const { scope } = state;

  useEffect(() => {
    if (!scope.token || scope.nodeId <= 0) return;
    const controller = new AbortController();
    // A response settles only the scope that issued it, once. Checking scope identity in the
    // updater also rejects a response that lands after a newer scope committed but before this
    // cleanup aborted its signal.
    const settle = (data: NodeRecord | null, error: unknown) => {
      if (controller.signal.aborted) return;
      setState((prev) => (prev.scope === scope && prev.isLoading ? { scope, data, isLoading: false, error } : prev));
    };
    apiClient.getNode(scope.token, scope.nodeId, { signal: controller.signal }).then(
      (result) => settle(result, null),
      (err: unknown) => settle(null, err),
    );
    return () => {
      controller.abort();
    };
  }, [scope]);

  return { data: state.data, isLoading: state.isLoading, error: state.error };
}
