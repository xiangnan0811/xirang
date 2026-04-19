import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { NodeStatus } from "@/lib/api/node-metrics-api";

export type { NodeStatus };

interface UseNodeStatusResult {
  data: NodeStatus | null;
  isLoading: boolean;
  error: unknown;
  refetch: () => void;
}

export function useNodeStatus(nodeId: number): UseNodeStatusResult {
  const [data, setData] = useState<NodeStatus | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const abortRef = useRef<AbortController | null>(null);

  const token = (() => {
    try {
      return sessionStorage.getItem("xirang-auth-token");
    } catch {
      return null;
    }
  })();

  const fetchStatus = useCallback(async () => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setIsLoading(true);
    setError(null);
    try {
      const result = await apiClient.getNodeStatus(token, nodeId);
      if (!controller.signal.aborted) {
        setData(result);
      }
    } catch (err) {
      if (!controller.signal.aborted) {
        setError(err);
      }
    } finally {
      if (!controller.signal.aborted) {
        setIsLoading(false);
      }
    }
  }, [token, nodeId]);

  useEffect(() => {
    void fetchStatus();
    const id = setInterval(() => void fetchStatus(), 30_000);
    return () => {
      clearInterval(id);
      abortRef.current?.abort();
    };
  }, [fetchStatus]);

  return { data, isLoading, error, refetch: fetchStatus };
}
