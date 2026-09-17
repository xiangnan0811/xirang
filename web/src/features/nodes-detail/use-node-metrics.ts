import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import type { MetricSeriesResponse } from "@/lib/api/node-metrics-api";
import type { NodeDetailAuthToken } from "./types";

type Params = {
  nodeId: number;
  token: NodeDetailAuthToken;
  from: string;
  to: string;
  fields?: string[];
  granularity?: "auto" | "raw" | "hourly" | "daily";
  refetchMs?: number;
};

export function useNodeMetrics({
  nodeId,
  token,
  from,
  to,
  fields,
  granularity = "auto",
  refetchMs,
}: Params) {
  const [data, setData] = useState<MetricSeriesResponse | null>(null);
  // First paint must show loading (not empty) when a fetch is expected.
  const [isLoading, setIsLoading] = useState(() => Boolean(token && nodeId > 0));
  const [error, setError] = useState<unknown>(null);
  const abortRef = useRef<AbortController | null>(null);

  const fieldsKey = fields?.join(",");
  const [scope, setScope] = useState({ nodeId, token, from, to, fieldsKey, granularity });
  if (scope.nodeId !== nodeId || scope.token !== token || scope.from !== from ||
      scope.to !== to || scope.fieldsKey !== fieldsKey || scope.granularity !== granularity) {
    setScope({ nodeId, token, from, to, fieldsKey, granularity });
    setData(null);
    setError(null);
    setIsLoading(Boolean(token && nodeId > 0));
  }

  const fetchOnce = useCallback(() => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    return apiClient.getMetricSeries(
        token,
        nodeId,
        {
          from,
          to,
          fields,
          granularity,
        },
        { signal: controller.signal },
      ).then((resp) => {
      if (!controller.signal.aborted) {
        setData(resp);
      }
    }).catch((e: unknown) => {
      if (!controller.signal.aborted) {
        setData(null);
        setError(e);
      }
    }).finally(() => {
      if (!controller.signal.aborted) {
        setIsLoading(false);
      }
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, nodeId, from, to, fieldsKey, granularity]);

  // The render-time scope reset clears stale series before this request starts.
  useEffect(() => {
    void fetchOnce();
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchOnce, token, nodeId]);

  useVisibilityPolling(
    () => {
      setIsLoading(true);
      setError(null);
      void fetchOnce();
    },
    refetchMs && refetchMs > 0 ? refetchMs : 0,
    { enabled: Boolean(refetchMs && refetchMs > 0 && token && nodeId > 0), immediate: false },
  );

  return { data, isLoading, error };
}
