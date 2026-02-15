import { useEffect, useMemo, useRef, useState } from "react";
import { LogsSocketClient } from "@/lib/ws/logs-socket";
import type { LogEvent } from "@/types/domain";

type UseLiveLogsOptions = {
  taskId?: number;
};

function deriveCursor(logs: LogEvent[]) {
  return logs.reduce((max, item) => {
    if (!item.logId) {
      return max;
    }
    return Math.max(max, item.logId);
  }, 0);
}

export function useLiveLogs(token: string | null, options?: UseLiveLogsOptions) {
  const [logs, setLogs] = useState<LogEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const [connectionWarning, setConnectionWarning] = useState<string | null>(null);
  const [cursorLogId, setCursorLogId] = useState<number>(0);

  const cursorRef = useRef<number>(0);
  const client = useMemo(() => new LogsSocketClient(), []);

  useEffect(() => {
    if (!token) {
      setLogs([]);
      cursorRef.current = 0;
      setCursorLogId(0);
      setConnected(false);
      setConnectionWarning("未登录，实时日志通道未建立。");
      return;
    }

    const unsubscribeMessage = client.subscribe((event) => {
      setLogs((prev) => {
        const merged = [event, ...prev];
        const dedup = new Map<string, LogEvent>();
        for (const row of merged) {
          const key = row.logId ? `log-${row.logId}` : row.id;
          if (!dedup.has(key)) {
            dedup.set(key, row);
          }
        }
        const nextLogs = [...dedup.values()].slice(0, 400);
        cursorRef.current = deriveCursor(nextLogs);
        setCursorLogId(cursorRef.current);
        client.updateSinceId(cursorRef.current > 0 ? cursorRef.current : undefined);
        return nextLogs;
      });
    });

    const unsubscribeStatus = client.onStatusChange((status) => {
      setConnected(status);
      setConnectionWarning(status ? null : "日志通道断开，正在尝试自动重连。");
    });

    client.connect(token, {
      taskId: options?.taskId,
      sinceId: cursorRef.current > 0 ? cursorRef.current : undefined
    });

    return () => {
      unsubscribeMessage();
      unsubscribeStatus();
      client.disconnect();
    };
  }, [client, options?.taskId, token]);

  return {
    logs,
    connected,
    connectionWarning,
    cursorLogId
  };
}
