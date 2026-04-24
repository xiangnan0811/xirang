import {
  LineChart,
  Line,
  AreaChart,
  Area,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import type { Panel, PanelQueryResult, PanelQuerySeries } from "@/types/domain";

// 6 种系列配色
const SERIES_COLORS = [
  "#3b82f6", // blue
  "#10b981", // emerald
  "#f59e0b", // amber
  "#ef4444", // red
  "#8b5cf6", // violet
  "#06b6d4", // cyan
];

function colorAt(index: number): string {
  return SERIES_COLORS[index % SERIES_COLORS.length];
}

// ─── 时间格式化 ──────────────────────────────────────────────────

function formatTs(ts: string, shortWindow: boolean): string {
  const d = new Date(ts);
  const pad = (n: number) => n.toString().padStart(2, "0");
  if (shortWindow) {
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function isShortWindow(series: PanelQuerySeries[]): boolean {
  const allPoints = series.flatMap((s) => s.points);
  if (allPoints.length < 2) return true;
  const first = new Date(allPoints[0].ts).getTime();
  const last = new Date(allPoints[allPoints.length - 1].ts).getTime();
  const spanMs = last - first;
  return spanMs < 6 * 60 * 60 * 1000; // < 6h
}

// ─── 将 series 转为 recharts 需要的 data 格式 ────────────────────

type ChartRow = { ts: string; [key: string]: string | number };

function toChartData(series: PanelQuerySeries[], shortWindow: boolean): ChartRow[] {
  if (!series.length) return [];

  // 收集所有时间戳
  const tsSet = new Set<string>();
  for (const s of series) {
    for (const p of s.points) {
      tsSet.add(p.ts);
    }
  }
  const sortedTs = Array.from(tsSet).sort();

  return sortedTs.map((ts) => {
    const row: ChartRow = { ts: formatTs(ts, shortWindow) };
    for (const s of series) {
      const point = s.points.find((p) => p.ts === ts);
      row[s.name] = point?.value ?? 0;
    }
    return row;
  });
}

// ─── 图表组件 ────────────────────────────────────────────────────

type RendererProps = {
  panel: Panel;
  data: PanelQueryResult;
};

export function PanelRenderer({ panel, data }: RendererProps) {
  const { series } = data;
  const shortWindow = isShortWindow(series);
  const chartData = toChartData(series, shortWindow);

  const tooltipStyle = {
    backgroundColor: "hsl(var(--card))",
    border: "1px solid hsl(var(--border))",
    fontSize: 11,
    borderRadius: 6,
  };

  const axisStyle = {
    fontSize: 10,
    fill: "hsl(var(--muted-foreground))",
  };

  // Recharts passes raw numbers to the tooltip/axis by default, which yields
  // "1.4083333333333332"-style labels. Round to 2 decimals and strip trailing
  // zeros so "25.00" stays "25" but "5.508" becomes "5.51".
  const formatValue = (v: unknown): string => {
    if (typeof v !== "number" || !Number.isFinite(v)) return "—";
    // `Number(v.toFixed(2))` collapses 25.00 → 25 and 5.508333 → 5.51
    return String(Number(v.toFixed(2)));
  };
  const tooltipFormatter = (v: unknown): [string, undefined] => [formatValue(v), undefined];

  switch (panel.chart_type) {
    case "line": {
      return (
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={chartData} margin={{ top: 4, right: 8, left: -20, bottom: 0 }}>
            <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} vertical={false} />
            <XAxis dataKey="ts" tick={axisStyle} stroke="transparent" tickLine={false} interval="preserveStartEnd" />
            <YAxis tick={axisStyle} stroke="transparent" tickLine={false} axisLine={false} tickFormatter={formatValue} />
            <Tooltip contentStyle={tooltipStyle} formatter={tooltipFormatter} />
            {series.length > 1 && <Legend />}
            {series.map((s, i) => (
              <Line
                key={s.name}
                type="monotone"
                dataKey={s.name}
                stroke={colorAt(i)}
                strokeWidth={1.5}
                dot={false}
                connectNulls
                isAnimationActive={false}
              />
            ))}
          </LineChart>
        </ResponsiveContainer>
      );
    }

    case "area": {
      return (
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={chartData} margin={{ top: 4, right: 8, left: -20, bottom: 0 }}>
            <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} vertical={false} />
            <XAxis dataKey="ts" tick={axisStyle} stroke="transparent" tickLine={false} interval="preserveStartEnd" />
            <YAxis tick={axisStyle} stroke="transparent" tickLine={false} axisLine={false} tickFormatter={formatValue} />
            <Tooltip contentStyle={tooltipStyle} formatter={tooltipFormatter} />
            {series.length > 1 && <Legend />}
            {series.map((s, i) => (
              <Area
                key={s.name}
                type="monotone"
                dataKey={s.name}
                stroke={colorAt(i)}
                fill={colorAt(i)}
                fillOpacity={0.12}
                strokeWidth={1.5}
                dot={false}
                connectNulls
                isAnimationActive={false}
              />
            ))}
          </AreaChart>
        </ResponsiveContainer>
      );
    }

    case "bar": {
      return (
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={chartData} margin={{ top: 4, right: 8, left: -20, bottom: 0 }}>
            <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} vertical={false} />
            <XAxis dataKey="ts" tick={axisStyle} stroke="transparent" tickLine={false} interval="preserveStartEnd" />
            <YAxis tick={axisStyle} stroke="transparent" tickLine={false} axisLine={false} tickFormatter={formatValue} />
            <Tooltip contentStyle={tooltipStyle} formatter={tooltipFormatter} />
            {series.length > 1 && <Legend />}
            {series.map((s, i) => (
              <Bar
                key={s.name}
                dataKey={s.name}
                fill={colorAt(i)}
                fillOpacity={0.85}
                isAnimationActive={false}
              />
            ))}
          </BarChart>
        </ResponsiveContainer>
      );
    }

    case "number": {
      const firstSeries = series[0];
      const lastPoint = firstSeries?.points[firstSeries.points.length - 1];
      const value = lastPoint?.value;
      const display = Number.isFinite(value) ? (value as number).toFixed(2) : "—";
      return (
        <div className="flex h-full w-full flex-col items-center justify-center gap-1">
          <span className="text-4xl font-bold tabular-nums leading-none text-foreground">
            {display}
          </span>
          {firstSeries && (
            <span className="text-xs text-muted-foreground">{firstSeries.name}</span>
          )}
        </div>
      );
    }

    case "table": {
      if (!series.length) return null;
      // 收集所有时间戳（已排序）
      const tsSet = new Set<string>();
      for (const s of series) {
        for (const p of s.points) tsSet.add(p.ts);
      }
      const sortedTs = Array.from(tsSet).sort();

      return (
        <div className="h-full w-full overflow-auto">
          <table className="w-full text-xs border-collapse">
            <thead>
              <tr className="border-b border-border">
                <th className="px-2 py-1 text-left font-medium text-muted-foreground">时间</th>
                {series.map((s) => (
                  <th key={s.name} className="px-2 py-1 text-right font-medium text-muted-foreground">
                    {s.name}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {sortedTs.map((ts) => (
                <tr key={ts} className="border-b border-border/50 hover:bg-muted/30">
                  <td className="px-2 py-1 text-muted-foreground tabular-nums">
                    {formatTs(ts, shortWindow)}
                  </td>
                  {series.map((s) => {
                    const point = s.points.find((p) => p.ts === ts);
                    const v = point?.value;
                    return (
                      <td key={s.name} className="px-2 py-1 text-right tabular-nums">
                        {Number.isFinite(v) ? (v as number).toFixed(2) : "—"}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );
    }

    default:
      return (
        <div className="flex h-full w-full items-center justify-center text-sm text-muted-foreground">
          不支持的图表类型
        </div>
      );
  }
}
