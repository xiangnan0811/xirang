import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import { apiClient } from "@/lib/api/client";
import type { Dashboard, DashboardTimeRange } from "@/types/domain";
import type { DashboardInput } from "@/lib/api/dashboards";

function computeRange(
  timeRange: DashboardTimeRange,
  custom?: { start: string; end: string }
): { start: string; end: string } {
  if (timeRange === "custom" && custom) {
    return { start: custom.start, end: custom.end };
  }
  const now = new Date();
  const end = now.toISOString();
  const ms: Record<string, number> = {
    "1h": 60 * 60 * 1000,
    "6h": 6 * 60 * 60 * 1000,
    "24h": 24 * 60 * 60 * 1000,
    "7d": 7 * 24 * 60 * 60 * 1000,
  };
  const delta = ms[timeRange] ?? ms["1h"];
  const start = new Date(now.getTime() - delta).toISOString();
  return { start, end };
}

export type UseDashboardReturn = {
  dashboard: Dashboard | null;
  start: string;
  end: string;
  loading: boolean;
  error: Error | null;
  refreshNonce: number;
  refresh: () => void;
  setTimeRange: (tr: DashboardTimeRange, custom?: { start: string; end: string }) => void;
  updateAutoRefresh: (seconds: number) => void;
};

export function useDashboard(id: string | undefined, token: string): UseDashboardReturn {
  const { t } = useTranslation();
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);
  const [refreshNonce, setRefreshNonce] = useState(0);
  const [timeRange, setTimeRangeState] = useState<DashboardTimeRange>("1h");
  const [customRange, setCustomRange] = useState<{ start: string; end: string } | undefined>();

  const { start, end } = computeRange(timeRange, customRange);

  // 手动刷新
  const refresh = useCallback(() => {
    setRefreshNonce((n) => n + 1);
  }, []);

  // 切换时间范围
  const setTimeRange = useCallback(
    (tr: DashboardTimeRange, custom?: { start: string; end: string }) => {
      setTimeRangeState(tr);
      if (tr === "custom" && custom) {
        setCustomRange(custom);
      } else {
        setCustomRange(undefined);
      }
      setRefreshNonce((n) => n + 1);
    },
    []
  );

  // 更新自动刷新间隔（同步到后端）
  const updateAutoRefresh = useCallback(
    (seconds: number) => {
      if (!dashboard || !token) return;
      const prevSeconds = dashboard.autoRefreshSeconds;
      // optimistic update
      setDashboard((prev) =>
        prev ? { ...prev, autoRefreshSeconds: seconds } : prev
      );
      apiClient.updateDashboard(token, dashboard.id, {
        name: dashboard.name,
        description: dashboard.description,
        timeRange: dashboard.timeRange,
        customStart: dashboard.customStart,
        customEnd: dashboard.customEnd,
        autoRefreshSeconds: seconds,
      } as DashboardInput)
        .then((updated) => {

          setDashboard(updated);
        })
        .catch(() => {
          // revert on failure
          setDashboard((prev) =>
            prev ? { ...prev, autoRefreshSeconds: prevSeconds } : prev
          );
          toast.error(t("dashboards.errors.unknown"));
        });
    },
    [dashboard, token, t]
  );

  // 初始化时同步 dashboard.timeRange
  const initializedRef = useRef(false);

  // 拉取 dashboard
  useEffect(() => {
    if (!id || !token) return;
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);

    setError(null);

    apiClient.getDashboard(token, Number(id), { signal: controller.signal })
      .then((d) => {
        if (controller.signal.aborted) return;

        setDashboard(d);
        // 同步 timeRange
        if (!initializedRef.current) {
          initializedRef.current = true;

          setTimeRangeState(d.timeRange);
          if (d.timeRange === "custom" && d.customStart && d.customEnd) {

            setCustomRange({ start: d.customStart, end: d.customEnd });
          }
        }
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        const normalized =
          err instanceof Error ? err : new Error(String(err ?? "unknown error"));

        setError(normalized);
      })
      .finally(() => {
        if (controller.signal.aborted) return;

        setLoading(false);
      });

    return () => {
      controller.abort();
    };
  }, [id, token, refreshNonce]);

  // 自动刷新 interval（tab 隐藏时暂停）
  const autoRefreshSeconds = dashboard?.autoRefreshSeconds ?? 0;
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    if (autoRefreshSeconds <= 0) return;

    function startInterval() {
      intervalRef.current = setInterval(() => {
        if (!document.hidden) {
          setRefreshNonce((n) => n + 1);
        }
      }, autoRefreshSeconds * 1000);
    }

    function handleVisibilityChange() {
      if (document.hidden) {
        if (intervalRef.current !== null) {
          clearInterval(intervalRef.current);
          intervalRef.current = null;
        }
      } else {
        startInterval();
      }
    }

    startInterval();
    document.addEventListener("visibilitychange", handleVisibilityChange);

    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      if (intervalRef.current !== null) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [autoRefreshSeconds]);

  return {
    dashboard,
    start,
    end,
    loading,
    error,
    refreshNonce,
    refresh,
    setTimeRange,
    updateAutoRefresh,
  };
}
