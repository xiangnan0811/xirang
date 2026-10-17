import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import i18n from "@/i18n";
import { LogsSocketClient } from "@/lib/ws/logs-socket";
import type { LogEvent } from "@/types/domain";
import { isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";

type UseLiveLogsOptions = {
  taskId?: number;
  /** 默认 true；为 false 时不订阅/连接，并返回空的断开状态。 */
  enabled?: boolean;
};

const EMPTY_LOGS: LogEvent[] = [];

/** 待处理队列上限，防止后台标签页内存无限增长 */
const MAX_PENDING = 500;
function readSessionToken(): string | null {
  try {
    return sessionStorage.getItem("xirang-auth-token");
  } catch {
    return null;
  }
}

export function useLiveLogs(token: string | null, options?: UseLiveLogsOptions) {
  const [logs, setLogs] = useState<LogEvent[]>([]);
  const [socketConnected, setSocketConnected] = useState(false);
  const [connectionWarning, setConnectionWarning] = useState<string | null>(null);
  const [cursorLogId, setCursorLogId] = useState<number>(0);
  const taskId = options?.taskId;
  const enabled = options?.enabled ?? true;
  const authTransitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, () => false);
  const [scope, setScope] = useState({ token, taskId, enabled });
  if (scope.token !== token || scope.taskId !== taskId || scope.enabled !== enabled) {
    setScope({ token, taskId, enabled });
    setLogs([]);
    setCursorLogId(0);
    setSocketConnected(false);
    setConnectionWarning(null);
  }

  const cursorRef = useRef<number>(0);

  const client = useMemo(() => new LogsSocketClient(), []);

  useEffect(() => subscribeAuthTransition(() => {
    if (isAuthTransitionActive()) {
      setSocketConnected(false);
    }
  }), []);

  useEffect(() => {
    cursorRef.current = 0;
    if (!enabled || !token || authTransitioning) {
      return;
    }

    // 每次 effect 独占的所有权：cleanup 先失效 owner，已排队的消息/状态回调与 RAF
    // 闭包只绑定创建它的 owner，失效后不得更新任何状态或游标。
    let active = true;
    let pending: LogEvent[] = [];
    let rafId: number | null = null;

    const flushPending = () => {
      rafId = null;
      if (!active) return;
      const batch = pending;
      if (batch.length === 0) return;
      pending = [];

      setLogs((prev) => {
        const merged = [...batch, ...prev];
        const dedup = new Map<string, LogEvent>();
        for (const row of merged) {
          const key = row.logId ? `log-${row.logId}` : row.id;
          if (!dedup.has(key)) {
            dedup.set(key, row);
          }
        }
        // 按 logId 降序排列，确保 slice 保留最新的 400 条而非最旧的
        const sorted = [...dedup.values()].sort(
          (a, b) => (b.logId ?? 0) - (a.logId ?? 0)
        );
        return sorted.slice(0, 400);
      });

      // cursor 更新移到 updater 外，避免嵌套 setState
      const batchMax = batch.reduce((m, e) => Math.max(m, e.logId ?? 0), cursorRef.current);
      if (batchMax > cursorRef.current) {
        cursorRef.current = batchMax;
        setCursorLogId(batchMax);
        client.updateSinceId(batchMax > 0 ? batchMax : undefined);
      }
    };

    const activeToken = readSessionToken() ?? token;
    const unsubscribeMessage = client.subscribe((event) => {
      if (!active) return;
      pending.push(event);
      // 限制队列长度，防止后台标签页无限增长；保留最新的
      if (pending.length > MAX_PENDING) {
        pending = pending.slice(-MAX_PENDING);
      }
      if (rafId === null) {
        rafId = requestAnimationFrame(flushPending);
      }
    });

    const unsubscribeStatus = client.onStatusChange((status) => {
      if (!active) return;
      setSocketConnected(status);
      if (status) {
        setConnectionWarning(null);
      } else if (client.isGivingUp()) {
        setConnectionWarning(i18n.t("logs.connectionWarning.maxRetries"));
      } else {
        setConnectionWarning(i18n.t("logs.connectionWarning.reconnecting"));
      }
    });

    client.connect(activeToken, {
      taskId,
      sinceId: cursorRef.current > 0 ? cursorRef.current : undefined,
      tokenGetter: () => {
        if (isAuthTransitionActive()) return null;
        return readSessionToken() ?? token;
      },
    });

    return () => {
      active = false;
      if (rafId !== null) {
        cancelAnimationFrame(rafId);
        rafId = null;
      }
      pending = [];
      unsubscribeMessage();
      unsubscribeStatus();
      client.disconnect();
    };
  }, [authTransitioning, client, enabled, taskId, token]);

  if (!enabled) {
    return { logs: EMPTY_LOGS, connected: false, connectionWarning: null, cursorLogId: 0 };
  }

  return {
    logs,
    connected: Boolean(token) && !authTransitioning && socketConnected,
    connectionWarning: token ? connectionWarning : i18n.t("logs.connectionWarning.notLoggedIn"),
    cursorLogId
  };
}
