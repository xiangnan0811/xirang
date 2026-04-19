import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { apiClient } from "@/lib/api/client";
import type { AlertRecord, AlertStatus } from "@/types/domain";

// AlertStatus uses "acked" (not "acknowledged") per domain.ts
type Filter = AlertStatus; // "open" | "acked" | "resolved"

function windowHref(alert: AlertRecord): string {
  const triggered = new Date(alert.triggeredAt ?? Date.now());
  const from = new Date(triggered.getTime() - 15 * 60 * 1000).toISOString();
  const to = new Date(triggered.getTime() + 15 * 60 * 1000).toISOString();
  return `/app/nodes/${alert.nodeId}?tab=metrics&from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;
}

const FILTER_LABELS: Record<Filter, string> = {
  open: "未处理",
  acked: "已确认",
  resolved: "已解决",
};

export default function AlertsTab({ nodeId }: { nodeId: number }) {
  const [alerts, setAlerts] = useState<AlertRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState<Filter>("open");

  const fetchAlerts = useCallback(async (signal: AbortSignal) => {
    const token = sessionStorage.getItem("xirang-auth-token");
    if (!token || nodeId <= 0) return;
    setLoading(true);
    try {
      const rows = await apiClient.getAlerts(token, { signal });
      if (!signal.aborted) {
        setAlerts(rows ?? []);
      }
    } catch {
      // ignore aborts and network errors
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchAlerts(controller.signal);
    return () => controller.abort();
  }, [fetchAlerts]);

  const filtered = alerts.filter((a) => a.nodeId === nodeId && a.status === filter);

  return (
    <div className="flex flex-col gap-4" data-testid="alerts-tab">
      <div className="flex items-center gap-2">
        {(["open", "acked", "resolved"] as Filter[]).map((f) => (
          <button
            key={f}
            type="button"
            data-testid={`alerts-filter-${f}`}
            onClick={() => setFilter(f)}
            data-state={filter === f ? "active" : "inactive"}
            className={
              "rounded-full px-3 py-1 text-xs font-medium " +
              (filter === f
                ? "bg-primary text-primary-foreground"
                : "bg-muted text-muted-foreground")
            }
          >
            {FILTER_LABELS[f]}
          </button>
        ))}
      </div>

      {loading && <p className="text-sm text-muted-foreground">加载中…</p>}
      {!loading && filtered.length === 0 && (
        <p className="text-sm text-muted-foreground">
          该节点暂无{FILTER_LABELS[filter]}告警。
        </p>
      )}
      {filtered.length > 0 && (
        <ul className="flex flex-col gap-2">
          {filtered.map((a) => (
            <li
              key={a.id}
              className="rounded-md border border-border bg-card p-3 flex items-start justify-between gap-3"
            >
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{a.severity}</span>
                  <span className="text-xs text-muted-foreground">
                    {a.triggeredAt ? new Date(a.triggeredAt).toLocaleString() : "-"}
                  </span>
                </div>
                <p className="mt-1 text-sm truncate">
                  {a.message || a.errorCode || "未命名告警"}
                </p>
              </div>
              <Link
                to={windowHref(a)}
                data-testid={`alert-jump-${a.id}`}
                className="text-xs text-primary hover:underline whitespace-nowrap shrink-0"
              >
                查看关联指标 →
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
