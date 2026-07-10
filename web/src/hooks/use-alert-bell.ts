import { useCallback, useRef, useState } from "react";
import { apiClient } from "@/lib/api/client";
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

  const poll = useCallback(async () => {
    if (!token) return;
    const controller = new AbortController();
    pollAbortRef.current = controller;
    try {
      const data = await apiClient.getAlertUnreadCount(token);
      if (!controller.signal.aborted) {
        setUnreadCount(data);
      }
    } catch {
      // 轮询异常时静默忽略，避免干扰用户体验
    }
  }, [token]);

  // 每 30s 轮询未读告警；后台标签页不轮询，切回前台立即补拉一次。
  useVisibilityPolling(() => { void poll(); }, 30_000);

  const fetchRecent = useCallback(async () => {
    if (!token) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    try {
      const alerts = await apiClient.getRecentAlerts(token, { limit: 10 });
      if (!controller.signal.aborted) {
        setRecentAlerts(alerts);
      }
    } catch {
      // 静默忽略
    } finally {
      if (!controller.signal.aborted) {
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
