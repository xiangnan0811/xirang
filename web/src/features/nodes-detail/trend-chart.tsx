import {
  CartesianGrid,
  ComposedChart,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import type { MetricSeries } from "@/lib/api/node-metrics-api";

export type Range = "1h" | "6h" | "24h" | "7d" | "30d";
const RANGES: Range[] = ["1h", "6h", "24h", "7d", "30d"];

type TrendChartProps = {
  series: MetricSeries[];
  range: Range;
  onRangeChange: (r: Range) => void;
  fields?: string[];
  height?: number;
};

// Union-all timestamps from the input series to build a single frame whose
// keys are metric names. Missing values become null — Recharts skips the gap
// rather than plotting a phantom zero.
function buildFrames(series: MetricSeries[]): Array<Record<string, number | string | null>> {
  const timeMap = new Map<string, Record<string, number | string | null>>();
  for (const s of series) {
    for (const p of s.points) {
      const key = p.t;
      if (!timeMap.has(key)) {
        timeMap.set(key, { t: key });
      }
      const row = timeMap.get(key)!;
      row[s.metric] = p.avg ?? p.v ?? null;
      if (p.max !== undefined) row[`${s.metric}_max`] = p.max;
    }
  }
  return Array.from(timeMap.values()).sort((a, b) =>
    String(a.t).localeCompare(String(b.t))
  );
}

const COLORS = ["#60a5fa", "#34d399", "#f472b6", "#fbbf24", "#a78bfa", "#f87171"];

export default function TrendChart({
  series,
  range,
  onRangeChange,
  fields,
  height = 240,
}: TrendChartProps) {
  const visible =
    fields && fields.length > 0
      ? series.filter((s) => fields.includes(s.metric))
      : series;

  const data = buildFrames(visible);
  const hasData = data.length > 0 && visible.some((s) => s.points.length > 0);

  return (
    <div
      data-testid="trend-chart"
      className="flex flex-col gap-3 rounded-md border border-border bg-card p-4"
    >
      <div className="flex items-center gap-2">
        {RANGES.map((r) => {
          const active = r === range;
          return (
            <button
              key={r}
              type="button"
              data-testid={`range-${r}`}
              data-state={active ? "active" : "inactive"}
              aria-pressed={active}
              onClick={() => onRangeChange(r)}
              className={
                "rounded-full px-3 py-1 text-xs font-medium transition-colors " +
                (active
                  ? "bg-primary text-primary-foreground"
                  : "bg-muted text-muted-foreground hover:text-foreground")
              }
            >
              {r}
            </button>
          );
        })}
      </div>

      {hasData ? (
        <ResponsiveContainer width="100%" height={height}>
          <ComposedChart data={data} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
            <CartesianGrid strokeDasharray="3 3" opacity={0.3} />
            <XAxis dataKey="t" tick={{ fontSize: 11 }} minTickGap={24} />
            <YAxis tick={{ fontSize: 11 }} />
            <Tooltip labelFormatter={(v) => new Date(String(v)).toLocaleString()} />
            {visible.map((s, i) => (
              <Line
                key={s.metric}
                type="monotone"
                dataKey={s.metric}
                stroke={COLORS[i % COLORS.length]}
                strokeWidth={1.5}
                dot={false}
                isAnimationActive={false}
                connectNulls
                name={`${s.metric} (${s.unit})`}
              />
            ))}
          </ComposedChart>
        </ResponsiveContainer>
      ) : (
        <div
          style={{ height }}
          className="flex items-center justify-center text-sm text-muted-foreground"
        >
          暂无数据
        </div>
      )}
    </div>
  );
}
