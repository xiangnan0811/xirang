import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { DiskForecast } from "@/lib/api/node-metrics-api";

export function useDiskForecast(nodeId: number) {
  const [data, setData] = useState<DiskForecast | null>(null);
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

  const fetchOnce = useCallback(async () => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setIsLoading(true);
    setError(null);
    try {
      const resp = await apiClient.getDiskForecast(token, nodeId);
      if (!controller.signal.aborted) {
        setData(resp);
      }
    } catch (e) {
      if (!controller.signal.aborted) {
        setError(e);
      }
    } finally {
      if (!controller.signal.aborted) {
        setIsLoading(false);
      }
    }
  }, [token, nodeId]);

  useEffect(() => {
    void fetchOnce();
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchOnce]);

  return { data, isLoading, error };
}
