import { useTranslation } from "react-i18next";
import {
  Area,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ComposedChart,
} from "recharts";
import { getChartTheme } from "@/lib/chart-theme";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { OverviewTrafficWindow } from "@/types/domain";

export type TrafficChartData = {
  label: string;
  throughput: number;
  activity: number;
  failed: number;
};

export type TrafficChartMetrics = {
  chartData: TrafficChartData[];
  hasRealSamples: boolean;
  peakThroughput: number;
  totalStartedCount: number;
  totalFailedCount: number;
};

export type VisibleLayers = {
  throughput: boolean;
  activity: boolean;
  failures: boolean;
};

export type OverviewTrafficChartProps = {
  trafficWindow: OverviewTrafficWindow;
  setTrafficWindow: (window: OverviewTrafficWindow) => void;
  trafficLoading: boolean;
  trafficError: string | null;
  chartMetrics: TrafficChartMetrics;
  visibleLayers: VisibleLayers;
  setVisibleLayers: React.Dispatch<React.SetStateAction<VisibleLayers>>;
  yMaxLeft: number;
  yMaxRight: number;
};

export function OverviewTrafficChart({
  trafficWindow,
  setTrafficWindow,
  trafficLoading,
  trafficError,
  chartMetrics,
  visibleLayers,
  setVisibleLayers,
  yMaxLeft,
  yMaxRight,
}: OverviewTrafficChartProps) {
  const { t } = useTranslation();
  const chartTheme = getChartTheme();

  return (
    <div className="flex flex-col w-full min-w-0">
      <Card className="rounded-lg border border-border bg-card flex-1 flex flex-col min-h-0">
        <CardHeader className="pb-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">{t(`overview.trafficTitle`, { window: t(`overview.trafficWindow${trafficWindow}`) })}</CardTitle>
            <div className="flex items-center gap-2">
              {(["1h", "24h", "7d"] as OverviewTrafficWindow[]).map((window) => (
                <Button
                  key={window}
                  size="sm"
                  variant={trafficWindow === window ? "default" : "outline"}
                  aria-pressed={trafficWindow === window}
                  onClick={() => setTrafficWindow(window)}
                >
                  {window}
                </Button>
              ))}
            </div>
          </div>
        </CardHeader>
        <CardContent className="flex-1 flex flex-col min-h-0 pt-2">
          {trafficLoading ? (
            <LoadingState className="py-6" rows={3} title={t("overview.trafficLoading")} />
          ) : trafficError ? (
            <InlineAlert tone="warning" className="mb-2">
              {trafficError}
            </InlineAlert>
          ) : (
            <>
              <div
                role="img"
                aria-label={
                  chartMetrics.hasRealSamples
                    ? t("overview.trafficAriaLabel", { window: t(`overview.trafficWindow${trafficWindow}`), peak: chartMetrics.peakThroughput, started: chartMetrics.totalStartedCount, failed: chartMetrics.totalFailedCount })
                    : t("overview.trafficAriaLabelEmpty", { window: t(`overview.trafficWindow${trafficWindow}`) })
                }
              >
                <ResponsiveContainer width="100%" height={208}>
                  <ComposedChart
                    data={chartMetrics.chartData}
                    margin={{ top: 6, right: 8, left: -12, bottom: 4 }}
                  >
                    <defs>
                      <linearGradient id="throughputGradient" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="0%" stopColor="hsl(var(--chart-ingress))" stopOpacity={0.06} />
                        <stop offset="100%" stopColor="hsl(var(--chart-ingress))" stopOpacity={0.01} />
                      </linearGradient>
                    </defs>
                    <CartesianGrid strokeDasharray="none" stroke="hsl(var(--secondary))" vertical={false} />
                    <XAxis
                      dataKey="label"
                      tick={{ fontSize: 10, fill: chartTheme.axis }}
                      stroke="transparent"
                      interval="preserveStartEnd"
                      tickLine={false}
                    />
                    <YAxis
                      yAxisId="left"
                      domain={[0, yMaxLeft]}
                      tick={{ fontSize: 10, fill: chartTheme.axis }}
                      stroke="transparent"
                      tickLine={false}
                      axisLine={false}
                      hide={!visibleLayers.throughput}
                    />
                    <YAxis
                      yAxisId="right"
                      orientation="right"
                      domain={[0, yMaxRight]}
                      allowDecimals={false}
                      tick={{ fontSize: 10, fill: chartTheme.axis }}
                      stroke="transparent"
                      tickLine={false}
                      axisLine={false}
                      hide={!visibleLayers.activity && !visibleLayers.failures}
                    />
                    <Tooltip
                      contentStyle={{
                        backgroundColor: chartTheme.tooltip.bg,
                        color: chartTheme.tooltip.text,
                        border: chartTheme.tooltip.border,
                        fontSize: 11,
                        borderRadius: 6,
                        padding: "6px 10px",
                        boxShadow: "0 2px 6px rgba(0,0,0,0.06)",
                      }}
                      labelStyle={{ color: chartTheme.axis, marginBottom: 2, fontSize: 10 }}
                      itemStyle={{ padding: "1px 0" }}
                      formatter={(value, name) => {
                        if (name === t("overview.chartThroughput")) return [`${value} Mbps`, name];
                        return [value, name];
                      }}
                    />
                    {visibleLayers.activity && (
                      <Bar dataKey="activity" yAxisId="right" name={t("overview.chartActivity")}
                        fill="hsl(var(--chart-egress))" opacity={0.22}
                        maxBarSize={8} radius={[2, 2, 0, 0]} isAnimationActive={false} />
                    )}
                    {visibleLayers.failures && (
                      <Bar dataKey="failed" yAxisId="right" name={t("overview.chartFailures")}
                        fill="hsl(var(--destructive))" opacity={0.7}
                        maxBarSize={8} radius={[2, 2, 0, 0]} isAnimationActive={false} />
                    )}
                    {visibleLayers.throughput && (
                      <Area
                        type="monotone"
                        dataKey="throughput"
                        yAxisId="left"
                        name={t("overview.chartThroughput")}
                        stroke="hsl(var(--chart-ingress))"
                        strokeWidth={1.5}
                        fill="url(#throughputGradient)"
                        fillOpacity={1}
                        dot={false}
                        activeDot={{ r: 3 }}
                        isAnimationActive={false}
                      />
                    )}
                  </ComposedChart>
                </ResponsiveContainer>
              </div>

              {/* Legend — matching matrix style (border-t, inline dots) */}
              <div className="mt-auto shrink-0 flex items-center gap-4 text-[11px] text-muted-foreground pt-3 border-t border-border">
                {[
                  { key: "throughput", label: t("overview.legendThroughput"), dotClass: "size-2 rounded-full bg-[hsl(var(--chart-ingress))]" },
                  { key: "activity", label: t("overview.legendActivity"), dotClass: "size-1.5 rounded-sm bg-[hsl(var(--chart-egress))]" },
                  { key: "failures", label: t("overview.legendFailures"), dotClass: "size-1.5 rounded-full bg-destructive" },
                ].map((item) => (
                  <button
                    key={item.key}
                    type="button"
                    className={`inline-flex items-center gap-1.5 transition-opacity ${visibleLayers[item.key as keyof typeof visibleLayers] ? "" : "opacity-35"}`}
                    aria-pressed={visibleLayers[item.key as keyof typeof visibleLayers]}
                    onClick={() => setVisibleLayers((current) => ({ ...current, [item.key]: !current[item.key as keyof typeof current] }))}
                  >
                    <span className={item.dotClass} />{item.label}
                  </button>
                ))}
              </div>
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
