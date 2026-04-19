import { useMemo, useState } from "react";
import StatCard from "./stat-card";
import TrendChart, { type Range } from "./trend-chart";
import DiskForecastCard from "./disk-forecast-card";
import { useNodeStatus } from "./use-node-status";
import { useNodeMetrics } from "./use-node-metrics";

const HOURS_BY_RANGE: Record<Range, number> = { "1h": 1, "6h": 6, "24h": 24, "7d": 168, "30d": 720 };

export default function OverviewTab({ nodeId }: { nodeId: number }) {
  const [range, setRange] = useState<Range>("24h");
  const { data: status } = useNodeStatus(nodeId);

  const { from, to } = useMemo(() => {
    const now = new Date();
    const fromDt = new Date(now.getTime() - HOURS_BY_RANGE[range] * 3600_000);
    return { from: fromDt.toISOString(), to: now.toISOString() };
  }, [range]);

  const { data: metrics } = useNodeMetrics({
    nodeId,
    from,
    to,
    fields: ["cpu_pct", "mem_pct", "disk_pct", "load1"],
  });

  const cpu = status?.current?.cpu_pct ?? 0;
  const mem = status?.current?.mem_pct ?? 0;
  const disk = status?.current?.disk_pct ?? 0;
  const load = status?.current?.load1 ?? 0;

  return (
    <div className="flex flex-col gap-6" data-testid="overview-tab">
      <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
        <StatCard label="CPU" value={cpu} unit="%" warnAt={80} />
        <StatCard label="MEM" value={mem} unit="%" warnAt={85} />
        <StatCard label="DISK" value={disk} unit="%" warnAt={85} />
        <StatCard label="LOAD" value={load} />
      </div>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <div className="md:col-span-2">
          <TrendChart
            series={metrics?.series ?? []}
            range={range}
            onRangeChange={setRange}
          />
        </div>
        <aside className="flex flex-col gap-4">
          <section className="rounded-md border border-border bg-card p-4">
            <header className="flex items-center justify-between">
              <h3 className="text-sm font-medium">⚠ 未处理告警</h3>
              <span className="text-xs text-muted-foreground">{status?.open_alerts ?? 0}</span>
            </header>
            {(status?.open_alerts ?? 0) === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">暂无未处理告警</p>
            ) : (
              <p className="mt-2 text-sm text-muted-foreground">前往「告警」tab 查看详情</p>
            )}
          </section>
          <section className="rounded-md border border-border bg-card p-4">
            <header className="flex items-center justify-between">
              <h3 className="text-sm font-medium">🔄 正在运行任务</h3>
              <span className="text-xs text-muted-foreground">{status?.running_tasks ?? 0}</span>
            </header>
            {(status?.running_tasks ?? 0) === 0 ? (
              <p className="mt-2 text-sm text-muted-foreground">暂无运行中任务</p>
            ) : (
              <p className="mt-2 text-sm text-muted-foreground">前往「任务」tab 查看详情</p>
            )}
          </section>
        </aside>
      </div>

      <DiskForecastCard nodeId={nodeId} />
    </div>
  );
}
