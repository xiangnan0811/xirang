import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import i18n from "@/i18n";
import { LogsSocketClient } from "@/lib/ws/logs-socket";
import type { LogEvent } from "@/types/domain";

type UseLiveLogsOptions = {
  taskId?: number;
};

/** 待处理队列上限，防止后台标签页内存无限增长 */
const MAX_PENDING = 500;

export function useLiveLogs(token: string | null, options?: UseLiveLogsOptions) {
  const [logs, setLogs] = useState<LogEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const [connectionWarning, setConnectionWarning] = useState<string | null>(null);
  const [cursorLogId, setCursorLogId] = useState<number>(0);

  const cursorRef = useRef<number>(0);
  const tokenRef = useRef(token);
  tokenRef.current = token;

  const pendingRef = useRef<LogEvent[]>([]);
  const rafRef = useRef<number | null>(null);

  const client = useMemo(() => new LogsSocketClient(), []);

  const flushPending = useCallback(() => {
    rafRef.current = null;
    const batch = pendingRef.current;
    if (batch.length === 0) return;
    pendingRef.current = [];

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
  }, [client]);

  useEffect(() => {
    if (!token) {
      setLogs([]);
      cursorRef.current = 0;
      setCursorLogId(0);
      setConnected(false);
      setConnectionWarning(i18n.t("logs.connectionWarning.notLoggedIn"));
      return;
    }

    const unsubscribeMessage = client.subscribe((event) => {
      pendingRef.current.push(event);
      // 限制队列长度，防止后台标签页无限增长；保留最新的
      if (pendingRef.current.length > MAX_PENDING) {
        pendingRef.current = pendingRef.current.slice(-MAX_PENDING);
      }
      if (rafRef.current === null) {
        rafRef.current = requestAnimationFrame(flushPending);
      }
    });

    const unsubscribeStatus = client.onStatusChange((status) => {
      setConnected(status);
      if (status) {
        setConnectionWarning(null);
      } else if (client.isGivingUp()) {
        setConnectionWarning(i18n.t("logs.connectionWarning.maxRetries"));
      } else {
        setConnectionWarning(i18n.t("logs.connectionWarning.reconnecting"));
      }
    });

    client.connect(token, {
      taskId: options?.taskId,
      sinceId: cursorRef.current > 0 ? cursorRef.current : undefined,
      tokenGetter: () => tokenRef.current,
    });

    return () => {
      unsubscribeMessage();
      unsubscribeStatus();
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
      pendingRef.current = [];
      client.disconnect();
    };
  }, [client, flushPending, options?.taskId, token]);

  return {
    logs,
    connected,
    connectionWarning,
    cursorLogId
  };
}
