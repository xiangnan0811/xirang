import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useOutletContext } from "react-router-dom";
import { TrendingUp, Clock, AlertTriangle, CheckCircle2, Maximize2 } from "lucide-react";
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
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { LoadingState } from "@/components/ui/loading-state";
import { NodeMetricsPanel } from "@/components/node-metrics-panel";
import { InlineAlert } from "@/components/ui/inline-alert";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { useAuth } from "@/context/auth-context";
import { getErrorMessage } from "@/lib/utils";
import type { OverviewTrafficSeries, OverviewTrafficWindow } from "@/types/domain";

const MATRIX_PREVIEW_LIMIT = 80;


function parseDateValue(value?: string) {
  if (!value) {
    return 0;
  }

  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

export function OverviewPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { token } = useAuth();
  const { overview, nodes, tasks, loading, refreshVersion, fetchOverviewTraffic, refreshNodes, refreshTasks } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshNodes();
    void refreshTasks();
  }, [refreshNodes, refreshTasks]);

  const healthRate = overview.totalNodes > 0
    ? Math.round((overview.healthyNodes / overview.totalNodes) * 100)
    : 0;

  const [matrixFullscreen, setMatrixFullscreen] = useState(false);
  const [trafficWindow, setTrafficWindow] = useState<OverviewTrafficWindow>("1h");
  const [trafficData, setTrafficData] = useState<OverviewTrafficSeries | null>(null);
  const [trafficLoading, setTrafficLoading] = useState(true);
  const [trafficError, setTrafficError] = useState<string | null>(null);
  const [visibleLayers, setVisibleLayers] = useState({ throughput: true, activity: true, failures: true });
  const trafficRequestRef = useRef(0);
  const previewNodes = useMemo(() => nodes.slice(0, MATRIX_PREVIEW_LIMIT), [nodes]);
  const hiddenNodeCount = Math.max(0, nodes.length - previewNodes.length);
  useEffect(() => {
    const controller = new AbortController();
    const requestId = trafficRequestRef.current + 1;
    trafficRequestRef.current = requestId;
    setTrafficLoading(true);
    setTrafficError(null);

    void fetchOverviewTraffic(trafficWindow, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted && trafficRequestRef.current === requestId) {
          setTrafficData(result);
        }
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        if (trafficRequestRef.current === requestId) {
          setTrafficError(getErrorMessage(error, t("overview.trafficLoadFailed")));
          setTrafficData(null);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted && trafficRequestRef.current === requestId) {
          setTrafficLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps -- t is stable from react-i18next
  }, [fetchOverviewTraffic, refreshVersion, trafficWindow]);

  const recentTasks = useMemo(
    () => [...tasks]
      .sort((first, second) => {
        const timeGap = parseDateValue(second.createdAt) - parseDateValue(first.createdAt);
        if (timeGap !== 0) {
          return timeGap;
        }
        return second.id - first.id;
      })
      .slice(0, 5),
    [tasks]
  );

  const chartMetrics = useMemo(() => {
    const points = trafficData?.points ?? [];
    const totalStartedCount = points.reduce((sum, point) => sum + point.startedCount, 0);
    const totalFailedCount = points.reduce((sum, point) => sum + point.failedCount, 0);
    const peakThroughput = points.reduce((max, point) => Math.max(max, point.throughputMbps), 0);

    const chartData = points.map((point) => ({
      label: point.label,
      throughput: point.throughputMbps,
      activity: Math.max(point.startedCount, point.activeTaskCount),
      failed: point.failedCount,
    }));

    return {
      chartData,
      hasRealSamples: Boolean(trafficData?.hasRealSamples),
      peakThroughput,
      totalStartedCount,
      totalFailedCount,
    };
  }, [trafficData]);

  const chartTheme = useMemo(() => getChartTheme(), []);

  const { yMaxLeft, yMaxRight } = useMemo(() => {
    const points = chartMetrics.chartData;
    let maxThroughput = 0;
    let maxCount = 0;
    for (const point of points) {
      if (visibleLayers.throughput) maxThroughput = Math.max(maxThroughput, point.throughput);
      if (visibleLayers.activity) maxCount = Math.max(maxCount, point.activity);
      if (visibleLayers.failures) maxCount = Math.max(maxCount, point.failed);
    }
    return {
      yMaxLeft: maxThroughput > 0 ? Math.ceil((maxThroughput * 1.1) / 50) * 50 : 100,
      yMaxRight: maxCount > 0 ? Math.max(1, Math.ceil(maxCount * 1.2)) : 5,
    };
  }, [chartMetrics.chartData, visibleLayers]);

  return (
    <div className="animate-fade-in space-y-5">
      <StatCardsSection
        className="grid-cols-4 animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: t("overview.healthRateTitle"),
            value: healthRate,
            unit: "%",
            icon: healthRate >= 90 ? (
              <TrendingUp className="size-4 sm:size-5 text-success hidden sm:block" />
            ) : undefined,
            description: t("overview.healthRateDesc", { healthy: overview.healthyNodes, total: overview.totalNodes }),
            tone: "success",
          },
          {
            title: t("overview.taskSuccessRate"),
            value: overview.overallSuccessRate,
            unit: "%",
            description: t("overview.taskSuccessRateDesc", { count: overview.failedTasks24h }),
            tone: "info",
          },
          {
            title: t("overview.currentThroughput"),
            value: overview.avgSyncMbps,
            unit: "Mbps",
            description: t("overview.currentThroughputDesc", { count: overview.runningTasks }),
            tone: "warning",
          },
          {
            title: t("overview.policyCoverage"),
            value: overview.activePolicies,
            description: t("overview.policyCoverageDesc", { count: tasks.length }),
            tone: "primary",
          },
        ]}
      />

      <section className="grid gap-4 grid-cols-1 lg:grid-cols-2 animate-slide-up [animation-delay:200ms]">
        <div className="flex flex-col gap-2 w-full min-w-0">
          <Card className="rounded-lg border border-border bg-card flex-1 flex flex-col min-h-0">
            <CardHeader className="pb-2">
              <div className="flex items-center justify-between gap-2">
                <CardTitle className="text-base">{t("overview.matrixTitle")}</CardTitle>
                {nodes.length > 0 ? (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-7 text-muted-foreground hover:text-foreground"
                    onClick={() => setMatrixFullscreen(true)}
                    aria-label={t("overview.fullscreenAriaLabel")}
                    title={t("overview.fullscreenTitle")}
                  >
                    <Maximize2 className="size-3.5" />
                  </Button>
                ) : null}
              </div>
            </CardHeader>
            <CardContent className="flex-1 flex flex-col min-h-0">
              {loading ? (
                <LoadingState
                  className="mb-3"
                  title={t("overview.matrixLoading")}
                  description={t("overview.matrixLoadingDesc")}
                  rows={2}
                />
              ) : null}
              {!loading && nodes.length === 0 ? (
                <p className="rounded-lg border border-border bg-card px-3 py-4 text-sm text-muted-foreground">
                  {t("overview.matrixEmpty")}
                </p>
              ) : (
                <div className="flex flex-col flex-1 min-h-0 h-full">
                  <div
                    role="group"
                    aria-label={t("overview.matrixPreviewAriaLabel", { shown: previewNodes.length, total: nodes.length })}
                    className="flex flex-wrap gap-2 overflow-y-auto pb-4"
                  >
                    {previewNodes.map((node) => {
                      let dotColor = "bg-muted-foreground/30";
                      if (node.status === "online") dotColor = "bg-success";
                      if (node.status === "warning") dotColor = "bg-warning";
                      return (
                        <button
                          key={node.id}
                          type="button"
                          className={`relative size-3 rounded-sm ${dotColor} hover:ring-2 hover:ring-primary/50 hover:ring-offset-1 hover:ring-offset-background transition-shadow focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-1 group`}
                          onClick={() => navigate(`/app/nodes?keyword=${encodeURIComponent(node.name)}`)}
                          aria-label={t("overview.nodeStatusAriaLabel", { name: node.name, status: node.status === "online" ? t("overview.legendOnline") : node.status === "warning" ? t("overview.legendWarning") : t("overview.legendOffline") })}
                        >
                          {/* Tooltip on hover */}
                          <span className="pointer-events-none absolute bottom-full left-1/2 mb-2 w-max -translate-x-1/2 opacity-0 transition-opacity group-hover:opacity-100 z-10 rounded-md border border-border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md">
                            <span className="font-medium">{node.name}</span>
                            <span className="ml-2 text-muted-foreground">{node.lastProbeAt || node.lastSeenAt || t("common.unknown")}</span>
                          </span>
                        </button>
                      );
                    })}
                  </div>

                  {hiddenNodeCount > 0 ? (
                    <p className="pb-3 text-[11px] text-muted-foreground">
                      {t("overview.matrixPreviewHint", { shown: previewNodes.length, total: nodes.length })}
                    </p>
                  ) : null}

                  {nodes.length > 0 && (
                    <div className="mt-auto shrink-0 flex items-center gap-4 text-[11px] text-muted-foreground pt-3 border-t border-border">
                      <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-success"></span>{t("overview.legendOnline")}</span>
                      <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-warning"></span>{t("overview.legendWarning")}</span>
                      <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-muted-foreground/30"></span>{t("overview.legendOffline")}</span>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </div>

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
<CartesianGrid strokeDasharray="none" stroke={chartTheme.grid} vertical={false} />
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
                          }}
                          labelStyle={{ color: chartTheme.axis }}
                          formatter={(value, name) => {
                            if (name === t("overview.chartThroughput")) return [`${value} Mbps`, name];
                            return [value, name];
                          }}
                        />
                        {visibleLayers.activity && (
                          <Bar dataKey="activity" yAxisId="right" name={t("overview.chartActivity")}
                            fill={chartTheme.series[1]} opacity={0.22}
                            maxBarSize={8} radius={[2, 2, 0, 0]} isAnimationActive={false} />
                        )}
                        {visibleLayers.failures && (
                          <Bar dataKey="failed" yAxisId="right" name={t("overview.chartFailures")}
                            fill={chartTheme.error} opacity={0.7}
                            maxBarSize={8} radius={[2, 2, 0, 0]} isAnimationActive={false} />
                        )}
                        {visibleLayers.throughput && (
                          <Area
                            type="monotone"
                            dataKey="throughput"
                            yAxisId="left"
                            name={t("overview.chartThroughput")}
                            stroke={chartTheme.series[0]}
                            strokeWidth={1.5}
                            fill={chartTheme.series[0]}
                            fillOpacity={0.06}
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
      </section>

      {/* 节点资源概览 */}
      {nodes.length > 0 && nodes.some(n => n.status === "online") && token && (
        <section className="animate-slide-up [animation-delay:250ms]">
          <Card className="rounded-lg border border-border bg-card">
            <CardHeader className="pb-3">
              <CardTitle className="text-base">{t("overview.nodeResources")}</CardTitle>
            </CardHeader>
            <CardContent>
              <NodeMetricsPanel nodes={nodes} token={token} />
              {nodes.filter(n => n.status === "online").length > 8 && (
                <p className="mt-3 text-xs text-muted-foreground">
                  {t("overview.nodeResourcesHint")}
                </p>
              )}
            </CardContent>
          </Card>
        </section>
      )}

      <Dialog open={matrixFullscreen} onOpenChange={setMatrixFullscreen}>
        <DialogContent size="lg" className="max-h-[90vh] flex flex-col">
          <DialogHeader>
            <DialogTitle>{t("overview.matrixFullTitle", { count: nodes.length })}</DialogTitle>
            <DialogDescription>{t("overview.matrixFullDesc")}</DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <div className="flex-1 overflow-y-auto px-6 pb-6">
            <div role="group" aria-label={t("overview.matrixFullAriaLabel", { count: nodes.length })} className="flex flex-wrap gap-2">
              {nodes.map((node) => {
                let dotColor = "bg-muted-foreground/30";
                if (node.status === "online") dotColor = "bg-success";
                if (node.status === "warning") dotColor = "bg-warning";
                return (
                  <button
                    key={node.id}
                    type="button"
                    className={`relative size-3.5 rounded-sm ${dotColor} hover:ring-2 hover:ring-primary/50 hover:ring-offset-1 hover:ring-offset-background transition-shadow focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-1 group`}
                    onClick={() => {
                      setMatrixFullscreen(false);
                      navigate(`/app/nodes?keyword=${encodeURIComponent(node.name)}`);
                    }}
                    aria-label={t("overview.nodeStatusAriaLabel", { name: node.name, status: node.status === "online" ? t("overview.legendOnline") : node.status === "warning" ? t("overview.legendWarning") : t("overview.legendOffline") })}
                  >
                    <span className="pointer-events-none absolute bottom-full left-1/2 mb-2 w-max -translate-x-1/2 opacity-0 transition-opacity group-hover:opacity-100 z-10 rounded-md border border-border bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md">
                      <span className="font-medium">{node.name}</span>
                      <span className="ml-2 text-muted-foreground">{node.ip}</span>
                      <span className="ml-2 text-muted-foreground">{node.lastProbeAt || node.lastSeenAt || t("common.unknown")}</span>
                    </span>
                  </button>
                );
              })}
            </div>
            <div className="mt-4 flex items-center gap-4 text-[11px] text-muted-foreground pt-3 border-t border-border">
              <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-success" />{t("overview.legendOnline")}</span>
              <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-warning" />{t("overview.legendWarning")}</span>
              <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-muted-foreground/30" />{t("overview.legendOffline")}</span>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* 最近同步任务框 */}
      <section className="animate-slide-up [animation-delay:250ms]">
        <Card className="rounded-lg border border-border bg-card">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-base flex items-center gap-2">
              <Clock className="size-4 text-primary" />
              {t("overview.recentTasks")}
            </CardTitle>
            <Link to="/app/tasks" className="inline-flex items-center text-xs px-2 py-1 rounded-md text-muted-foreground hover:text-foreground transition-colors">
              {t("overview.viewMore")} &rarr;
            </Link>
          </CardHeader>
          <CardContent>
            {loading ? (
              <LoadingState className="py-6" rows={3} title={t("overview.recentTasksLoading")} />
            ) : tasks.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted-foreground">{t("overview.noTaskData")}</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <thead className="border-b border-border text-xs text-muted-foreground uppercase bg-secondary">
                    <tr>
                      <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableNodeName")}</th>
                      <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableTaskName")}</th>
                      <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableSyncStatus")}</th>
                      <th scope="col" className="px-4 py-2 font-medium">{t("overview.tableTransfer")}</th>
                      <th scope="col" className="px-4 py-2 font-medium text-right">{t("overview.tableCompletedAt")}</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {recentTasks.map((task) => {
                      // Estimate transfer size if speed exists and it's not pending/retrying
                      let transferData = "-";
                      if (task.speedMbps > 0) {
                        // Rough estimation for display purposes during active transfer
                        transferData = `≈ ${(task.speedMbps / 8).toFixed(1)} MB/s`;
                      }

                      let StatusIcon = Clock;
                      let statusColor = "text-muted-foreground";
                      let statusLabel = t("overview.taskStatusQueued");

                      switch (task.status) {
                        case "success":
                          StatusIcon = CheckCircle2;
                          statusColor = "text-success";
                          statusLabel = t("overview.taskStatusSuccess");
                          break;
                        case "failed":
                          StatusIcon = AlertTriangle;
                          statusColor = "text-destructive";
                          statusLabel = t("overview.taskStatusFailed");
                          break;
                        case "running":
                          StatusIcon = TrendingUp;
                          statusColor = "text-info";
                          statusLabel = t("overview.taskStatusRunning");
                          break;
                        case "retrying":
                          StatusIcon = AlertTriangle;
                          statusColor = "text-warning";
                          statusLabel = t("overview.taskStatusRetrying");
                          break;
                      }

                      return (
                        <tr key={task.id} className="hover:bg-accent transition-colors">
                          <td className="px-4 py-2.5 font-medium">{task.nodeName}</td>
                          <td className="px-4 py-2.5 text-muted-foreground">{task.name || task.policyName}</td>
                          <td className="px-4 py-2.5">
                            <span className={`inline-flex items-center gap-1.5 ${statusColor}`}>
                              <StatusIcon className="size-3.5" />
                              {statusLabel}
                            </span>
                          </td>
                          <td className="px-4 py-2.5 font-mono text-xs">{transferData}</td>
                          <td className="px-4 py-2.5 text-right text-muted-foreground text-xs">{task.updatedAt || "-"}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </CardContent>
        </Card>
      </section>
    </div>
  );
}
