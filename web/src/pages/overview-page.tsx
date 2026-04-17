import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { Maximize2, TrendingUp } from "lucide-react";
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
import { useSharedContext } from "@/context/shared-context";
import { useNodesContext } from "@/context/nodes-context";
import { useTasksContext } from "@/context/tasks-context";
import { useAuth } from "@/context/auth-context";
import { getErrorMessage } from "@/lib/utils";
import type { OverviewTrafficSeries, OverviewTrafficWindow } from "@/types/domain";
import { OverviewTrafficChart } from "@/pages/overview-page.traffic";
import { OverviewRecentTasks } from "@/pages/overview-page.recent-tasks";
import { OverviewHero } from "@/pages/overview-page.hero";

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
  const { overview, loading, refreshVersion, fetchOverviewTraffic } = useSharedContext();
  const { nodes, refreshNodes } = useNodesContext();
  const { tasks, refreshTasks } = useTasksContext();

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
      <OverviewHero />
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
                    className="flex flex-wrap gap-1 overflow-y-auto pb-4"
                  >
                    {previewNodes.map((node) => {
                      let dotColor = "bg-muted-foreground/30";
                      if (node.status === "online") dotColor = "bg-success";
                      if (node.status === "warning") dotColor = "bg-warning";
                      return (
                        <button
                          key={node.id}
                          type="button"
                          className={`relative size-[18px] rounded-[4px] ${dotColor} hover:ring-2 hover:ring-primary/50 hover:ring-offset-1 hover:ring-offset-background transition-shadow focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-1 group`}
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

        <OverviewTrafficChart
          trafficWindow={trafficWindow}
          setTrafficWindow={setTrafficWindow}
          trafficLoading={trafficLoading}
          trafficError={trafficError}
          chartMetrics={chartMetrics}
          visibleLayers={visibleLayers}
          setVisibleLayers={setVisibleLayers}
          yMaxLeft={yMaxLeft}
          yMaxRight={yMaxRight}
        />
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
            <div role="group" aria-label={t("overview.matrixFullAriaLabel", { count: nodes.length })} className="flex flex-wrap gap-1">
              {nodes.map((node) => {
                let dotColor = "bg-muted-foreground/30";
                if (node.status === "online") dotColor = "bg-success";
                if (node.status === "warning") dotColor = "bg-warning";
                return (
                  <button
                    key={node.id}
                    type="button"
                    className={`relative size-[18px] rounded-[4px] ${dotColor} hover:ring-2 hover:ring-primary/50 hover:ring-offset-1 hover:ring-offset-background transition-shadow focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-1 group`}
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

      <OverviewRecentTasks tasks={tasks} recentTasks={recentTasks} loading={loading} />
    </div>
  );
}
