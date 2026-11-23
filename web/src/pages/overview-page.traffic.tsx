import { useTranslation } from "react-i18next";
import { Activity, Loader2 } from "lucide-react";
import {
  Area,
  Bar,
  Cell,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ComposedChart,
} from "recharts";
import { Button } from "@/components/ui/button";
import { DataSurface, DataSurfaceContent, DataSurfaceHeader } from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { cn } from "@/lib/utils";
import type { OverviewTrafficWindow } from "@/types/domain";
import {
  computeIntegerAxisTicks,
  formatTrafficTooltipLabel,
  formatTrafficTooltipValue,
  hasTrafficOrActivityData,
  type TrafficChartData,
  type TrafficChartMetrics,
  type VisibleLayers,
} from "@/pages/overview-page.traffic.utils";

export type { TrafficChartData, TrafficChartMetrics, VisibleLayers };

export type OverviewTrafficChartProps = {
  trafficWindow: OverviewTrafficWindow;
  setTrafficWindow: (window: OverviewTrafficWindow) => void;
  trafficLoading: boolean;
  trafficError: string | null;
  hasLoadedData: boolean;
  onRetry?: () => void;
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
  hasLoadedData,
  onRetry,
  chartMetrics,
  visibleLayers,
  setVisibleLayers,
  yMaxLeft,
  yMaxRight,
}: OverviewTrafficChartProps) {
  const { t } = useTranslation();

  const isInitialLoading = trafficLoading && !hasLoadedData && !trafficError;
  const isRefreshing = trafficLoading && hasLoadedData;
  const hasData = hasTrafficOrActivityData(chartMetrics);
  const showLeftAxis = visibleLayers.throughput && chartMetrics.hasRealSamples && yMaxLeft > 0;
  const showRightAxis = visibleLayers.activity || visibleLayers.failures;
  const anyLayerVisible = visibleLayers.throughput || visibleLayers.activity || visibleLayers.failures;
  const rightAxisScale = computeIntegerAxisTicks(yMaxRight);

  const windowLabel = t(`overview.trafficWindow${trafficWindow}`);
  const chartAriaLabel = hasData
    ? t("overview.trafficAriaLabel", {
        window: windowLabel,
        peak: chartMetrics.peakThroughput,
        started: chartMetrics.totalStartedCount,
        failed: chartMetrics.totalFailedCount,
      })
    : t("overview.trafficAriaLabelEmpty", { window: windowLabel });

  const legendItems: Array<{ key: keyof VisibleLayers; label: string; dotClass: string }> = [
    { key: "throughput", label: t("overview.legendThroughput"), dotClass: "size-2 rounded-full bg-[hsl(var(--chart-ingress))]" },
    { key: "activity", label: t("overview.legendActivity"), dotClass: "size-2 rounded-sm bg-[hsl(var(--chart-egress))]" },
    { key: "failures", label: t("overview.legendFailures"), dotClass: "size-2 rounded-full bg-destructive" },
  ];

  return (
    <DataSurface className="flex flex-col w-full min-w-0">
      <DataSurfaceHeader
        title={
          <span className="inline-flex flex-wrap items-center gap-2">
            <Activity className="size-4 text-primary" aria-hidden />
            <span>{t("overview.trafficTitle", { window: windowLabel })}</span>
            {isRefreshing ? (
              <span
                className="inline-flex items-center gap-1 text-xs font-normal text-muted-foreground"
                role="status"
                aria-live="polite"
              >
                <Loader2 className="size-3 animate-spin" aria-hidden />
                {t("overview.refreshing")}
              </span>
            ) : null}
          </span>
        }
        actions={
          <div className="flex items-center gap-1.5" role="group" aria-label={t("overview.trafficWindowGroupAriaLabel")}>
            {(["1h", "24h", "7d"] as OverviewTrafficWindow[]).map((window) => (
              <Button
                key={window}
                type="button"
                size="sm"
                variant={trafficWindow === window ? "default" : "outline"}
                aria-pressed={trafficWindow === window}
                onClick={() => setTrafficWindow(window)}
              >
                {window}
              </Button>
            ))}
          </div>
        }
      />

      <DataSurfaceContent className="flex-1 flex flex-col min-h-0">
        {isInitialLoading ? (
          <LoadingState className="py-6" rows={3} title={t("overview.trafficLoading")} />
        ) : trafficError ? (
          <InlineAlert tone="warning" title={t("overview.trafficLoadFailed")}>
            <div className="space-y-2">
              <p>{trafficError}</p>
              {onRetry ? (
                <Button type="button" size="sm" variant="outline" onClick={onRetry}>
                  {t("common.retry")}
                </Button>
              ) : null}
            </div>
          </InlineAlert>
        ) : !hasData ? (
          <div
            role="img"
            aria-label={chartAriaLabel}
            className="flex min-h-[208px] flex-col items-center justify-center rounded-lg border border-dashed border-border bg-background/40 px-4 py-8 text-center"
          >
            <Activity className="mb-2 size-6 text-muted-foreground" aria-hidden />
            <p className="text-sm font-medium text-foreground">
              {t("overview.trafficEmptyTitle", { window: windowLabel })}
            </p>
            <p className="mt-1 max-w-md text-xs text-muted-foreground">
              {t("overview.trafficEmptyDesc")}
            </p>
          </div>
        ) : (
          <>
            {chartMetrics.truncated ? (
              <InlineAlert tone="warning" className="mb-3">
                {t("overview.trafficTruncated")}
              </InlineAlert>
            ) : null}

            {visibleLayers.throughput && !chartMetrics.hasRealSamples ? (
              <p className="mb-2 text-xs text-muted-foreground">
                {t("overview.trafficNoThroughputSamples")}
              </p>
            ) : null}

            {!anyLayerVisible ? (
              <div
                role="img"
                aria-label={chartAriaLabel}
                className="flex min-h-[208px] items-center justify-center rounded-lg border border-dashed border-border bg-background/40 px-4 text-center text-sm text-muted-foreground"
              >
                {t("overview.trafficAllLayersHidden")}
              </div>
            ) : (
              <div>
                <div className="mb-1 flex items-center justify-between text-[11px] text-muted-foreground">
                  <span>{showLeftAxis ? t("overview.axisUnitThroughput") : ""}</span>
                  <span>{showRightAxis ? t("overview.axisUnitCount") : ""}</span>
                </div>
                <div role="img" aria-label={chartAriaLabel}>
                  <ResponsiveContainer width="100%" height={208}>
                    <ComposedChart
                      data={chartMetrics.chartData}
                      margin={{ top: 6, right: 8, left: showLeftAxis ? -4 : 4, bottom: 4 }}
                    >
                      <defs>
                        <linearGradient id="throughputGradient" x1="0" y1="0" x2="0" y2="1">
                          <stop offset="0%" stopColor="hsl(var(--chart-ingress))" stopOpacity={0.08} />
                          <stop offset="100%" stopColor="hsl(var(--chart-ingress))" stopOpacity={0.01} />
                        </linearGradient>
                      </defs>
                      <CartesianGrid
                        strokeDasharray="none"
                        stroke="hsl(var(--border))"
                        strokeOpacity={0.5}
                        vertical={false}
                      />
                      <XAxis
                        dataKey="label"
                        tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))" }}
                        stroke="transparent"
                        interval="preserveStartEnd"
                        tickLine={false}
                      />
                      <YAxis
                        yAxisId="left"
                        domain={[0, yMaxLeft > 0 ? yMaxLeft : 1]}
                        tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))" }}
                        stroke="transparent"
                        tickLine={false}
                        axisLine={false}
                        hide={!showLeftAxis}
                      />
                      <YAxis
                        yAxisId="right"
                        orientation="right"
                        domain={rightAxisScale.domain}
                        ticks={rightAxisScale.ticks}
                        allowDecimals={false}
                        tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))" }}
                        stroke="transparent"
                        tickLine={false}
                        axisLine={false}
                        hide={!showRightAxis}
                      />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: "hsl(var(--card))",
                          color: "hsl(var(--card-foreground))",
                          border: "1px solid hsl(var(--border))",
                          fontSize: 11,
                          borderRadius: 6,
                          padding: "6px 10px",
                          boxShadow: "var(--shadow-sm)",
                        }}
                        labelStyle={{ color: "hsl(var(--muted-foreground))", marginBottom: 2, fontSize: 10 }}
                        itemStyle={{ padding: "1px 0" }}
                        labelFormatter={(label, payload) =>
                          formatTrafficTooltipLabel(label, payload, t("overview.trafficPartialBucket"))
                        }
                        formatter={(value, name) =>
                          formatTrafficTooltipValue(
                            value,
                            name,
                            t("overview.chartThroughput"),
                            t("overview.countUnit"),
                          )
                        }
                      />
                      {visibleLayers.activity && (
                        <Bar
                          dataKey="activity"
                          yAxisId="right"
                          name={t("overview.chartActivity")}
                          fill="hsl(var(--chart-egress))"
                          maxBarSize={8}
                          radius={[2, 2, 0, 0]}
                          isAnimationActive={false}
                        >
                          {chartMetrics.chartData.map((entry, idx) => (
                            <Cell
                              key={`activity-${entry.label}-${idx}`}
                              fill="hsl(var(--chart-egress))"
                              fillOpacity={entry.partial ? 0.14 : 0.26}
                              stroke={entry.partial ? "hsl(var(--chart-egress))" : undefined}
                              strokeDasharray={entry.partial ? "3 2" : undefined}
                            />
                          ))}
                        </Bar>
                      )}
                      {visibleLayers.failures && (
                        <Bar
                          dataKey="failed"
                          yAxisId="right"
                          name={t("overview.chartFailures")}
                          fill="hsl(var(--destructive))"
                          maxBarSize={8}
                          radius={[2, 2, 0, 0]}
                          isAnimationActive={false}
                        >
                          {chartMetrics.chartData.map((entry, idx) => (
                            <Cell
                              key={`failed-${entry.label}-${idx}`}
                              fill="hsl(var(--destructive))"
                              fillOpacity={entry.partial ? 0.42 : 0.72}
                              stroke={entry.partial ? "hsl(var(--destructive))" : undefined}
                              strokeDasharray={entry.partial ? "3 2" : undefined}
                            />
                          ))}
                        </Bar>
                      )}
                      {visibleLayers.throughput && chartMetrics.hasRealSamples && (
                        <Area
                          type="monotone"
                          dataKey="throughput"
                          yAxisId="left"
                          name={t("overview.chartThroughput")}
                          stroke="hsl(var(--chart-ingress))"
                          strokeWidth={1.5}
                          fill="url(#throughputGradient)"
                          fillOpacity={1}
                          dot={(dotProps) => {
                            const payload = dotProps.payload as Partial<TrafficChartData> | undefined;
                            if (payload?.partial && typeof dotProps.cx === "number" && typeof dotProps.cy === "number") {
                              return (
                                <circle
                                  key={`partial-dot-${dotProps.index}`}
                                  cx={dotProps.cx}
                                  cy={dotProps.cy}
                                  r={3}
                                  fill="hsl(var(--card))"
                                  stroke="hsl(var(--chart-ingress))"
                                  strokeWidth={1.5}
                                  strokeDasharray="2 1"
                                />
                              );
                            }
                            return <g key={`dot-${dotProps.index}`} />;
                          }}
                          activeDot={{ r: 3 }}
                          isAnimationActive={false}
                        />
                      )}
                    </ComposedChart>
                  </ResponsiveContainer>
                </div>
              </div>
            )}

            <div className="mt-auto shrink-0 flex flex-wrap items-center gap-2 text-xs text-foreground/80 pt-3 border-t border-border">
              {legendItems.map((item) => (
                <button
                  key={item.key}
                  type="button"
                  className={cn(
                    "inline-flex min-h-6 min-w-6 items-center gap-1.5 rounded px-2 py-1 transition-opacity focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2",
                    visibleLayers[item.key] ? "bg-secondary/40 hover:bg-secondary/70" : "opacity-45 hover:opacity-75",
                  )}
                  aria-pressed={visibleLayers[item.key]}
                  onClick={() =>
                    setVisibleLayers((current) => ({ ...current, [item.key]: !current[item.key] }))
                  }
                >
                  <span className={item.dotClass} aria-hidden />
                  <span>{item.label}</span>
                </button>
              ))}
              {chartMetrics.hasPartialBucket ? (
                <span className="ml-auto text-[11px] text-muted-foreground">
                  {t("overview.trafficPartialNote")}
                </span>
              ) : null}
            </div>
          </>
        )}
      </DataSurfaceContent>
    </DataSurface>
  );
}
