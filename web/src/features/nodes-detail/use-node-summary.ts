import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { apiClient } from "@/lib/api/client";
import { getAuthSessionGeneration, isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import type { NodeSummary } from "@/lib/api/nodes-api";
import type { NodeDetailAuthToken } from "./types";

interface UseNodeSummaryResult {
  data: NodeSummary | null;
  isLoading: boolean;
  error: unknown;
  refetch: () => void;
}

function isCurrentSummary(controller: AbortController, requestGeneration: number): boolean {
  return !controller.signal.aborted
    && !isAuthTransitionActive()
    && getAuthSessionGeneration() === requestGeneration;
}

export function useNodeSummary(nodeId: number, token: NodeDetailAuthToken): UseNodeSummaryResult {
  const authTransitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, () => false);
  const generation = getAuthSessionGeneration();
  const [seenGeneration, setSeenGeneration] = useState(generation);
  if (seenGeneration !== generation) {
    setSeenGeneration(generation);
  }
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
    if (!token || nodeId <= 0 || isAuthTransitionActive()) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const requestGeneration = getAuthSessionGeneration();
    return apiClient.getNodeSummary(token, nodeId, { signal: controller.signal }).then((result) => {
      if (!isCurrentSummary(controller, requestGeneration)) {
        return;
      }
      hasDataRef.current = true;
      setData(result);
      setError(null);
    }).catch((err: unknown) => {
      if (!isCurrentSummary(controller, requestGeneration) || hasDataRef.current) {
        return;
      }
      setData(null);
      setError(err);
    }).finally(() => {
      if (isCurrentSummary(controller, requestGeneration)) {
        setIsLoading(false);
      }
    });
  }, [token, nodeId]);

  useEffect(() => {
    if (authTransitioning) {
      abortRef.current?.abort();
      return;
    }
    hasDataRef.current = false;
    void fetchSummary();
    return () => {
      abortRef.current?.abort();
    };
  }, [authTransitioning, fetchSummary, seenGeneration]);

  const refetch = useCallback(() => {
    if (!token || nodeId <= 0 || isAuthTransitionActive()) return;
    void fetchSummary();
  }, [fetchSummary, nodeId, token]);

  useVisibilityPolling(
    () => {
      refetch();
    },
    30_000,
    { enabled: Boolean(token) && nodeId > 0 && !authTransitioning, immediate: false },
  );

  return { data, isLoading, error, refetch };
}
