import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import type { NodeSummary } from "@/lib/api/nodes-api";
import type { NodeDetailAuthToken } from "./types";

interface UseNodeSummaryResult {
  data: NodeSummary | null;
  isLoading: boolean;
  error: unknown;
  refetch: () => void;
}

export function useNodeSummary(nodeId: number, token: NodeDetailAuthToken): UseNodeSummaryResult {
  const [data, setData] = useState<NodeSummary | null>(null);
  const [isLoading, setIsLoading] = useState(() => Boolean(token && nodeId > 0));
  const [error, setError] = useState<unknown>(null);
  const abortRef = useRef<AbortController | null>(null);
  const hasDataRef = useRef(false);
  const [scope, setScope] = useState({ nodeId, token });
  if (scope.nodeId !== nodeId || scope.token !== token) {
    setScope({ nodeId, token });
    setData(null);
    setError(null);
    setIsLoading(Boolean(token && nodeId > 0));
  }

  const fetchSummary = useCallback(() => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    return apiClient.getNodeSummary(token, nodeId, { signal: controller.signal }).then((result) => {
      if (!controller.signal.aborted) {
        hasDataRef.current = true;
        setData(result);
        setError(null);
      }
    }).catch((err: unknown) => {
      if (!controller.signal.aborted && !hasDataRef.current) {
        setData(null);
        setError(err);
      }
    }).finally(() => {
      if (!controller.signal.aborted) setIsLoading(false);
    });
  }, [token, nodeId]);

  useEffect(() => {
    hasDataRef.current = false;
    void fetchSummary();
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchSummary]);

  const refetch = useCallback(() => {
    if (!token || nodeId <= 0) return;
    void fetchSummary();
  }, [fetchSummary, nodeId, token]);

  useVisibilityPolling(
    () => {
      refetch();
    },
    30_000,
    { enabled: Boolean(token) && nodeId > 0, immediate: false },
  );

  return { data, isLoading, error, refetch };
}
