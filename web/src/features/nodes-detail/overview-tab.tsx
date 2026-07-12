import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import StatCard from "./stat-card";
import TrendChart, { type Range } from "./trend-chart";
import DiskForecastCard from "./disk-forecast-card";
import { useNodeMetrics } from "./use-node-metrics";
import type { OverviewTabProps } from "./types";

const HOURS_BY_RANGE: Record<Range, number> = { "1h": 1, "6h": 6, "24h": 24, "7d": 168, "30d": 720 };

export default function OverviewTab({ nodeId, token, status, statusError }: OverviewTabProps) {
  const { t } = useTranslation();
  const [range, setRange] = useState<Range>("24h");

  const { from, to } = useMemo(() => {
    const now = new Date();
    const fromDt = new Date(now.getTime() - HOURS_BY_RANGE[range] * 3600_000);
    return { from: fromDt.toISOString(), to: now.toISOString() };
  }, [range]);

  const { data: metrics, isLoading: metricsLoading, error: metricsError } = useNodeMetrics({
    nodeId,
    token,
    from,
    to,
    fields: ["cpu_pct", "mem_pct", "disk_pct", "load1"],
  });

  // Missing/failed status, or node never probed, must show unknown (—) —
  // never paint literal zeros as if they were live metrics.
  const statusReady = !statusError && status != null && status.probedAt != null;
  const cpu = statusReady ? status.current.cpuPct : Number.NaN;
  const mem = statusReady ? status.current.memPct : Number.NaN;
  const disk = statusReady ? status.current.diskPct : Number.NaN;
  const load = statusReady ? status.current.load1 : Number.NaN;

  return (
    <div className="flex flex-col gap-6" data-testid="overview-tab">
      {(statusError || metricsError) ? (
        <p className="text-sm text-destructive" role="alert">
          {t("nodes.nodeDetail.loadFailed")}
        </p>
      ) : null}
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard label="CPU" value={cpu} unit="%" warnAt={statusReady ? 80 : undefined} />
        <StatCard label="MEM" value={mem} unit="%" warnAt={statusReady ? 85 : undefined} />
        <StatCard label="DISK" value={disk} unit="%" warnAt={statusReady ? 85 : undefined} />
        <StatCard label="LOAD" value={load} />
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <div className="md:col-span-2">
          <TrendChart
            series={metrics?.series ?? []}
            range={range}
            onRangeChange={setRange}
            loading={metricsLoading}
          />
        </div>
        <aside className="flex flex-col gap-4">
          <section className="rounded-md border border-border bg-card p-4">
            <header className="flex items-center justify-between">
              <h3 className="text-sm font-medium">{t("nodes.nodeDetail.openAlertsTitle")}</h3>
              <span className="text-xs text-muted-foreground">
                {statusReady ? status.openAlerts : "—"}
              </span>
            </header>
            {!statusReady ? (
              <p className="mt-2 text-sm text-muted-foreground">{t("nodes.nodeDetail.statusUnknown")}</p>
            ) : status.openAlerts === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">{t("nodes.nodeDetail.openAlertsEmpty")}</p>
            ) : (
              <p className="mt-2 text-sm text-muted-foreground">{t("nodes.nodeDetail.openAlertsHint")}</p>
            )}
          </section>
          <section className="rounded-md border border-border bg-card p-4">
            <header className="flex items-center justify-between">
              <h3 className="text-sm font-medium">{t("nodes.nodeDetail.runningTasksTitle")}</h3>
              <span className="text-xs text-muted-foreground">
                {statusReady ? status.runningTasks : "—"}
              </span>
            </header>
            {!statusReady ? (
              <p className="mt-2 text-sm text-muted-foreground">{t("nodes.nodeDetail.statusUnknown")}</p>
            ) : status.runningTasks === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">{t("nodes.nodeDetail.runningTasksEmpty")}</p>
            ) : (
              <p className="mt-2 text-sm text-muted-foreground">{t("nodes.nodeDetail.runningTasksHint")}</p>
            )}
          </section>
        </aside>
      </div>

      <DiskForecastCard nodeId={nodeId} token={token} />
    </div>
  );
}
