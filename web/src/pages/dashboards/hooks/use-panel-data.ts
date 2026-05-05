import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
import type { Panel, PanelQueryResult } from "@/types/domain";

export type UsePanelDataReturn = {
  data: PanelQueryResult | null;
  loading: boolean;
  error: string | null;
  retry: () => void;
};

export function usePanelData(
  panel: Panel,
  start: string,
  end: string,
  token: string,
  refreshNonce: number
): UsePanelDataReturn {
  const [data, setData] = useState<PanelQueryResult | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [localNonce, setLocalNonce] = useState(0);

  const retry = useCallback(() => {
    setLocalNonce((n) => n + 1);
  }, []);

  // 保存最新的 abort controller
  const controllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    // 取消上一个请求
    if (controllerRef.current) {
      controllerRef.current.abort();
    }
    const controller = new AbortController();
    controllerRef.current = controller;

    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setError(null);

    apiClient.queryPanel(
      token,
      {
        metric: panel.metric,
        filters: panel.filters,
        aggregation: panel.aggregation,
        start,
        end,
      },
      { signal: controller.signal }
    )
      .then((result) => {
        if (controller.signal.aborted) return;
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setData(result);
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        const msg = err instanceof Error ? err.message : "查询失败";
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setError(msg);
      })
      .finally(() => {
        if (controller.signal.aborted) return;
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setLoading(false);
      });

    return () => {
      controller.abort();
    };
    // panel 字段作为基础依赖，用 panel.id + metric + aggregation 而非整个对象避免引用变化
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [panel.id, panel.metric, panel.aggregation, start, end, token, refreshNonce, localNonce]);

  return { data, loading, error, retry };
}
