import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { DiskForecast } from "@/lib/api/node-metrics-api";
import type { NodeDetailAuthToken } from "./types";

export function useDiskForecast(nodeId: number, token: NodeDetailAuthToken) {
  const [data, setData] = useState<DiskForecast | null>(null);
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

  const fetchOnce = useCallback(() => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    return apiClient.getDiskForecast(token, nodeId, {
        signal: controller.signal,
      }).then((resp) => {
      if (!controller.signal.aborted) {
        setData(resp);
      }
    }).catch((e: unknown) => {
      if (!controller.signal.aborted) {
        setError(e);
      }
    }).finally(() => {
      if (!controller.signal.aborted) {
        setIsLoading(false);
      }
    });
  }, [token, nodeId]);

  useEffect(() => {
    void fetchOnce();
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchOnce]);

  return { data, isLoading, error };
}
