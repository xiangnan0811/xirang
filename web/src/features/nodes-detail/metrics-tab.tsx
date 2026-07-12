import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import TrendChart from "./trend-chart";
import type { Range as TrendChartRange } from "./trend-chart";
import { useNodeMetrics } from "./use-node-metrics";
import type { NodeDetailTabProps } from "./types";

type Range = "24h" | "7d" | "30d" | "90d";
const HOURS_BY_RANGE: Record<Range, number> = { "24h": 24, "7d": 168, "30d": 720, "90d": 2160 };
const RANGES: Range[] = ["24h", "7d", "30d", "90d"];

type Granularity = "auto" | "raw" | "hourly" | "daily";
const GRANULARITIES: Granularity[] = ["auto", "raw", "hourly", "daily"];

type SeriesPoint = { t: string; avg?: number; max?: number; v?: number };
type SeriesItem = { metric: string; unit: string; points: SeriesPoint[] };

function toCSV(
  granularity: string,
  bucketSeconds: number,
  series: SeriesItem[],
): string {
  const timestamps = new Set<string>();
  for (const s of series) for (const p of s.points) timestamps.add(p.t);
  const header = ["t"];
  for (const s of series) {
    header.push(s.metric);
    if (s.points.some((p) => p.max !== undefined)) header.push(`${s.metric}_max`);
  }
  const lines = [
    `# granularity=${granularity} bucket_seconds=${bucketSeconds}`,
    header.join(","),
  ];
  const sortedTs = Array.from(timestamps).sort();
  for (const t of sortedTs) {
    const row: (string | number)[] = [t];
    for (const s of series) {
      const p = s.points.find((q) => q.t === t);
      const hasMaxCol = s.points.some((q) => q.max !== undefined);
      if (p === undefined) {
        row.push("");
        if (hasMaxCol) row.push("");
      } else {
        row.push(p.avg ?? p.v ?? "");
        if (hasMaxCol) row.push(p.max ?? "");
      }
    }
    lines.push(row.join(","));
  }
  return lines.join("\n");
}

function triggerDownload(csv: string, filename: string) {
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function toChartRange(r: Range): TrendChartRange {
  if (r === "90d") return "30d";
  return r as TrendChartRange;
}

export default function MetricsTab({ nodeId, token }: NodeDetailTabProps) {
  const { t } = useTranslation();
  const [range, setRange] = useState<Range>("24h");
  const [granularity, setGranularity] = useState<Granularity>("auto");

  const METRICS: Array<{ field: string; label: string; unit: string }> = [
    { field: "cpu_pct", label: "CPU", unit: "%" },
    { field: "mem_pct", label: t("nodes.nodeDetail.metricMem"), unit: "%" },
    { field: "disk_pct", label: t("nodes.nodeDetail.metricDisk"), unit: "%" },
    { field: "load1", label: t("nodes.nodeDetail.metricLoad"), unit: "" },
    { field: "latency_ms", label: t("nodes.nodeDetail.metricLatency"), unit: "ms" },
    { field: "probe_ok_ratio", label: t("nodes.nodeDetail.metricUptime"), unit: "" },
  ];
  const ALL_FIELDS = METRICS.map((m) => m.field);

  const { from, to } = useMemo(() => {
    const now = new Date();
    const fromDt = new Date(now.getTime() - HOURS_BY_RANGE[range] * 3600_000);
    return { from: fromDt.toISOString(), to: now.toISOString() };
  }, [range]);

  const { data, isLoading, error } = useNodeMetrics({
    nodeId,
    token,
    from,
    to,
    fields: ALL_FIELDS,
    granularity,
  });

  const handleExport = () => {
    if (!data) return;
    const csv = toCSV(data.granularity, data.bucketSeconds, data.series);
    const ts = new Date().toISOString().replace(/[:.]/g, "-");
    triggerDownload(csv, `node-${nodeId}-metrics-${ts}.csv`);
  };

  const seriesByMetric = new Map(
    (data?.series ?? []).map((s) => [s.metric, s]),
  );

  return (
    <div className="flex flex-col gap-6" data-testid="metrics-tab">
      {error ? (
        <p className="text-sm text-destructive" role="alert">
          {t("nodes.nodeDetail.metricsError")}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-3" role="toolbar">
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">{t("nodes.nodeDetail.metricsTimeRange")}</span>
          <select
            data-testid="range-select"
            value={range}
            onChange={(e) => setRange(e.target.value as Range)}
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
          >
            {RANGES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">{t("nodes.nodeDetail.metricsGranularity")}</span>
          <select
            data-testid="granularity-select"
            value={granularity}
            onChange={(e) => setGranularity(e.target.value as Granularity)}
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
          >
            {GRANULARITIES.map((g) => (
              <option key={g} value={g}>
                {g}
              </option>
            ))}
          </select>
        </label>
        {data && (
          <span className="text-xs text-muted-foreground">
            {t("nodes.nodeDetail.metricsCurrent", {
              granularity: data.granularity,
              bucketSeconds: data.bucketSeconds,
            })}
          </span>
        )}
        <button
          type="button"
          data-testid="export-csv"
          onClick={handleExport}
          disabled={!data}
          className="ml-auto rounded-md border border-border bg-card px-3 py-1 text-sm hover:bg-muted disabled:opacity-50"
        >
          {t("nodes.nodeDetail.metricsExportCsv")}
        </button>
      </div>

      <div className="grid grid-cols-1 gap-6">
        {METRICS.map((m) => {
          const s = seriesByMetric.get(m.field);
          const chartSeries = s ? [s] : [];
          return (
            <section
              key={m.field}
              className="flex flex-col gap-2"
              data-testid={`metric-section-${m.field}`}
            >
              <header className="flex items-center justify-between">
                <h3 className="text-sm font-medium">
                  {m.label}
                  {m.unit && (
                    <span className="ml-1 text-muted-foreground">({m.unit})</span>
                  )}
                </h3>
              </header>
              <TrendChart
                series={chartSeries}
                range={toChartRange(range)}
                onRangeChange={(r) => {
                  if (r === "1h" || r === "6h") {
                    setRange("24h");
                  } else {
                    setRange(r as Range);
                  }
                }}
                height={200}
                loading={isLoading}
              />
            </section>
          );
        })}
      </div>
    </div>
  );
}
