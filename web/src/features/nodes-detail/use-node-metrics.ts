import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { MetricSeriesResponse } from "@/lib/api/node-metrics-api";

type Params = {
  nodeId: number;
  from: string;
  to: string;
  fields?: string[];
  granularity?: "auto" | "raw" | "hourly" | "daily";
  refetchMs?: number;
};

export function useNodeMetrics({ nodeId, from, to, fields, granularity = "auto", refetchMs }: Params) {
  const [data, setData] = useState<MetricSeriesResponse | null>(null);
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

  const fieldsKey = fields?.join(",");

  const fetchOnce = useCallback(async () => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setIsLoading(true);
    setError(null);
    try {
      const resp = await apiClient.getMetricSeries(token, nodeId, {
        from,
        to,
        fields: fields,
        granularity,
      });
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, nodeId, from, to, fieldsKey, granularity]);

  useEffect(() => {
    void fetchOnce();
    if (refetchMs && refetchMs > 0) {
      const id = setInterval(() => void fetchOnce(), refetchMs);
      return () => {
        clearInterval(id);
        abortRef.current?.abort();
      };
    }
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchOnce, refetchMs]);

  return { data, isLoading, error };
}
