import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { NodeRecord } from "@/types/domain";
import type { NodeDetailAuthToken } from "./types";

interface UseNodeRecordResult {
  data: NodeRecord | null;
  isLoading: boolean;
  error: unknown;
}

export function useNodeRecord(nodeId: number, token: NodeDetailAuthToken): UseNodeRecordResult {
  const [data, setData] = useState<NodeRecord | null>(null);
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

  const fetchNode = useCallback(() => {
    if (!token || nodeId <= 0) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    return apiClient.getNode(token, nodeId, { signal: controller.signal }).then((result) => {
      if (!controller.signal.aborted) {
        setData(result);
        setError(null);
      }
    }).catch((err: unknown) => {
      if (!controller.signal.aborted) {
        setData(null);
        setError(err);
      }
    }).finally(() => {
      if (!controller.signal.aborted) setIsLoading(false);
    });
  }, [token, nodeId]);

  useEffect(() => {
    void fetchNode();
    return () => {
      abortRef.current?.abort();
    };
  }, [fetchNode]);

  return { data, isLoading, error };
}
