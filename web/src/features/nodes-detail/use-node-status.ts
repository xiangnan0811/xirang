import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import type { NodeStatus } from "@/lib/api/node-metrics-api";
import type { NodeDetailAuthToken } from "./types";

export type { NodeStatus };

interface UseNodeStatusResult {
  data: NodeStatus | null;
  isLoading: boolean;
  error: unknown;
  refetch: () => void;
}

export function useNodeStatus(nodeId: number, token: NodeDetailAuthToken): UseNodeStatusResult {
  const [data, setData] = useState<NodeStatus | null>(null);
  // First paint must show loading (not offline/unknown) when a fetch is expected.
  const [isLoading, setIsLoading] = useState(() => Boolean(token && nodeId > 0));
  const [error, setError] = useState<unknown>(null);
  const abortRef = useRef<AbortController | null>(null);
  const [scope, setScope] = useState({ nodeId, token });
  if (scope.nodeId !== nodeId || scope.token !== token) {
    setScope({ nodeId, token });
    setData(null);
    setError(null);
    setIsLoading(Boolean(token && nodeId > 0));
  }

  const fetchStatus = useCallback(() => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    // Keep prior error until success so a background refresh does not flash
    // "ok" before the response; clear only on success path below.
    return apiClient.getNodeStatus(token, nodeId, { signal: controller.signal }).then((result) => {
      if (!controller.signal.aborted) {
        setData(result);
        setError(null);
      }
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) {
        // Drop stale status so UI never paints a previous poll as current.
        setData(null);
        setError(err);
      }
    }).finally(() => {
      if (!controller.signal.aborted) {
        setIsLoading(false);
      }
    });
  }, [token, nodeId]);

  // Immediate fetch whenever node/token changes so we never show the previous
  // node's status until the next 30s poll tick.
  useEffect(() => {
    void fetchStatus();
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchStatus, token, nodeId]);

  const refetch = useCallback(() => {
    if (!token || nodeId <= 0) return;
    setIsLoading(true);
    void fetchStatus();
  }, [fetchStatus, nodeId, token]);

  // Interval + visibility recovery only (immediate handled above).
  useVisibilityPolling(
    () => {
      refetch();
    },
    30_000,
    { enabled: Boolean(token) && nodeId > 0, immediate: false },
  );

  return { data, isLoading, error, refetch };
}
