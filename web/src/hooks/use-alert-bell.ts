import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { apiClient } from "@/lib/api/client";
import { getAuthSessionGeneration, isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";
import { useVisibilityPolling } from "@/hooks/use-visibility-polling";
import type { AlertRecord } from "@/types/domain";

interface AlertBellState {
  unreadCount: { total: number; critical: number; warning: number };
  recentAlerts: AlertRecord[];
  loading: boolean;
  fetchRecent: () => Promise<void>;
  refresh: () => void;
}

export function useAlertBell(token: string | null): AlertBellState {
  const [unreadCount, setUnreadCount] = useState({ total: 0, critical: 0, warning: 0 });
  const [recentAlerts, setRecentAlerts] = useState<AlertRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const pollAbortRef = useRef<AbortController | null>(null);
  const authTransitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, () => false);

  const poll = useCallback(async () => {
    if (!token || isAuthTransitionActive()) return;
    const controller = new AbortController();
    const requestGeneration = getAuthSessionGeneration();
    pollAbortRef.current = controller;
    try {
      const data = await apiClient.getAlertUnreadCount(token);
      if (!controller.signal.aborted && !isAuthTransitionActive() && getAuthSessionGeneration() === requestGeneration) {
        setUnreadCount(data);
      }
    } catch {
      // 轮询异常时静默忽略，避免干扰用户体验
    }
  }, [token]);

  useEffect(() => {
    if (!authTransitioning) return;
    pollAbortRef.current?.abort();
    abortRef.current?.abort();
  }, [authTransitioning]);

  // 每 30s 轮询未读告警；后台标签页不轮询，切回前台立即补拉一次。启用会话换发期间暂停。
  useVisibilityPolling(() => { void poll(); }, 30_000, { enabled: Boolean(token) && !authTransitioning });

  const fetchRecent = useCallback(async () => {
    if (!token || isAuthTransitionActive()) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    const requestGeneration = getAuthSessionGeneration();
    abortRef.current = controller;
    setLoading(true);
    try {
      const alerts = await apiClient.getRecentAlerts(token, { limit: 10 });
      if (!controller.signal.aborted && !isAuthTransitionActive() && getAuthSessionGeneration() === requestGeneration) {
        setRecentAlerts(alerts);
      }
    } catch {
      // 静默忽略
    } finally {
      if (!controller.signal.aborted && !isAuthTransitionActive()) {
        setLoading(false);
      }
    }
  }, [token]);

  const refresh = useCallback(() => {
    void poll();
  }, [poll]);

  return {
    unreadCount,
    recentAlerts,
    loading,
    fetchRecent,
    refresh,
  };
}
