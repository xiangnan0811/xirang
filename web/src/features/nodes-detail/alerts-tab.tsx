import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { apiClient } from "@/lib/api/client";
import type { AlertRecord, AlertStatus } from "@/types/domain";
import { buildAlertJumpHref } from "./alert-jump";
import type { NodeDetailTabProps } from "./types";

type Filter = AlertStatus; // "open" | "acked" | "resolved"

export default function AlertsTab({ nodeId, token }: NodeDetailTabProps) {
  const { t } = useTranslation();
  const [alerts, setAlerts] = useState<AlertRecord[]>([]);
  // First paint must show loading (not empty) when a fetch is expected.
  const [loading, setLoading] = useState(() => Boolean(token && nodeId > 0));
  const [error, setError] = useState(false);
  const [filter, setFilter] = useState<Filter>("open");

  const filterLabels: Record<Filter, string> = {
    open: t("nodes.nodeDetail.alertsFilterOpen"),
    acked: t("nodes.nodeDetail.alertsFilterAcked"),
    resolved: t("nodes.nodeDetail.alertsFilterResolved"),
  };

  const fetchAlerts = useCallback(async (signal: AbortSignal) => {
    if (!token || nodeId <= 0) {
      setLoading(false);
      setAlerts([]);
      setError(false);
      return;
    }
    setLoading(true);
    setError(false);
    try {
      const rows = await apiClient.getAlerts(token, { signal });
      if (!signal.aborted) {
        setAlerts(rows ?? []);
      }
    } catch {
      if (!signal.aborted) {
        setAlerts([]);
        setError(true);
      }
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [nodeId, token]);

  useEffect(() => {
    setAlerts([]);
    setError(false);
    setLoading(Boolean(token && nodeId > 0));
    const controller = new AbortController();
    void fetchAlerts(controller.signal);
    return () => controller.abort();
  }, [fetchAlerts, token, nodeId]);

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
            {filterLabels[f]}
          </button>
        ))}
      </div>

      {loading && <p className="text-sm text-muted-foreground">{t("nodes.nodeDetail.loading")}</p>}
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {t("nodes.nodeDetail.alertsError")}
        </p>
      )}
      {!loading && !error && filtered.length === 0 && (
        <p className="text-sm text-muted-foreground">
          {t("nodes.nodeDetail.alertsEmpty", { status: filterLabels[filter] })}
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
                  {a.message || a.errorCode || t("nodes.nodeDetail.alertsUnnamed")}
                </p>
              </div>
              <Link
                to={buildAlertJumpHref(a)}
                data-testid={`alert-jump-${a.id}`}
                className="text-xs text-primary hover:underline whitespace-nowrap shrink-0"
              >
                {t("nodes.nodeDetail.alertsViewMetrics")}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
