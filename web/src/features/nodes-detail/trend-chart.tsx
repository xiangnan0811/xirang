import { useState } from "react";
import {
  CartesianGrid,
  ComposedChart,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { Maximize2 } from "lucide-react";
import type { MetricSeries } from "@/lib/api/node-metrics-api";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogCloseButton,
} from "@/components/ui/dialog";

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

// Round to 2 decimals and strip trailing zeros so "25.00" → "25" and
// "5.50833" → "5.51". Non-numeric inputs pass through as "—".
function formatValue(v: unknown): string {
  if (typeof v !== "number" || !Number.isFinite(v)) return "—";
  return String(Number(v.toFixed(2)));
}

// Compact custom tooltip. Recharts' default renders every series inline with
// full precision and no size cap — on the overview page it covered the whole
// chart. This keeps it narrow (≤240px), rounds values, and truncates when
// labels are long.
type TooltipItem = { name?: string; value?: unknown; color?: string; dataKey?: string | number };
type TooltipProps = { active?: boolean; payload?: TooltipItem[]; label?: string | number };

function CompactTooltip({ active, payload, label }: TooltipProps) {
  if (!active || !payload || payload.length === 0) return null;
  return (
    <div
      className="rounded-md border border-border bg-card/95 px-2 py-1.5 text-xs shadow-sm backdrop-blur-sm max-w-[240px] space-y-0.5"
      style={{ pointerEvents: "none" }}
    >
      {label !== undefined && (
        <div className="text-[10px] font-medium text-muted-foreground">
          {new Date(String(label)).toLocaleString()}
        </div>
      )}
      {payload.map((item) => (
        <div key={String(item.dataKey ?? item.name)} className="flex items-baseline gap-2">
          <span
            className="h-1.5 w-1.5 rounded-full shrink-0"
            style={{ backgroundColor: item.color ?? "currentColor" }}
          />
          <span className="truncate text-foreground/80">{item.name}</span>
          <span className="ml-auto tabular-nums font-medium">{formatValue(item.value)}</span>
        </div>
      ))}
    </div>
  );
}

type ChartBodyProps = {
  data: Array<Record<string, number | string | null>>;
  visible: MetricSeries[];
  height: number;
};

function ChartBody({ data, visible, height }: ChartBodyProps) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <ComposedChart data={data} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
        <CartesianGrid strokeDasharray="3 3" opacity={0.3} />
        <XAxis dataKey="t" tick={{ fontSize: 11 }} minTickGap={24} />
        <YAxis tick={{ fontSize: 11 }} tickFormatter={formatValue} />
        <Tooltip
          content={<CompactTooltip />}
          cursor={{ strokeDasharray: "3 3", opacity: 0.5 }}
          wrapperStyle={{ outline: "none" }}
          // Pin tooltip off-cursor so it stops occluding the line it's about.
          position={{ y: 0 }}
          offset={12}
        />
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
  );
}

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

  const [zoomOpen, setZoomOpen] = useState(false);

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
        {hasData && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="ml-auto size-7"
            aria-label="放大图表"
            onClick={() => setZoomOpen(true)}
          >
            <Maximize2 className="size-3.5" />
          </Button>
        )}
      </div>

      {hasData ? (
        <ChartBody data={data} visible={visible} height={height} />
      ) : (
        <div
          style={{ height }}
          className="flex items-center justify-center text-sm text-muted-foreground"
        >
          暂无数据
        </div>
      )}

      <Dialog open={zoomOpen} onOpenChange={setZoomOpen}>
        <DialogContent className="max-w-[min(95vw,1100px)]">
          <DialogHeader>
            <DialogTitle>指标趋势</DialogTitle>
            <DialogDescription className="sr-only">
              放大视图：指标历史趋势，鼠标悬停查看具体数值。
            </DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <div className="px-2 pb-4">
            <ChartBody data={data} visible={visible} height={520} />
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
