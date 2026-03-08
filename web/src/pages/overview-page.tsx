import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useOutletContext } from "react-router-dom";
import { TrendingUp, Clock, AlertTriangle, CheckCircle2, Maximize2 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { getErrorMessage } from "@/lib/utils";
import type { NodeStatus, OverviewTrafficSeries, OverviewTrafficWindow } from "@/types/domain";

const CHART_WIDTH = 360;
const CHART_HEIGHT = 160;
const PAD_TOP = 6;
const PAD_BOTTOM = 18;
const PLOT_H = CHART_HEIGHT - PAD_TOP - PAD_BOTTOM;
const BAR_MAX_RATIO = 0.45;
const MATRIX_PREVIEW_LIMIT = 80;
const TRAFFIC_WINDOW_LABELS: Record<OverviewTrafficWindow, string> = {
  "1h": "近 1 小时",
  "24h": "近 24 小时",
  "7d": "近 7 天"
};

function safeMax(values: number[]): number {
  let result = -Infinity;
  for (const v of values) if (v > result) result = v;
  return result;
}

function safeMin(values: number[]): number {
  let result = Infinity;
  for (const v of values) if (v < result) result = v;
  return result;
}

function buildLinePath(values: number[], width: number, height: number, yOffset = 0) {
  if (!values.length) {
    return "";
  }
  const max = safeMax(values);
  const min = safeMin(values);
  if (max === min) {
    const y = max <= 0 ? height : height * 0.35;
    return values
      .map((_, index) => {
        const x = (index / Math.max(1, values.length - 1)) * width;
        return `${index === 0 ? "M" : "L"}${x.toFixed(2)},${(yOffset + y).toFixed(2)}`;
      })
      .join(" ");
  }
  const delta = max - min;
  return values
    .map((value, index) => {
      const x = (index / Math.max(1, values.length - 1)) * width;
      const y = height - ((value - min) / delta) * height;
      return `${index === 0 ? "M" : "L"}${x.toFixed(2)},${(yOffset + y).toFixed(2)}`;
    })
    .join(" ");
}

function getNodeStatusLabel(status: NodeStatus) {
  if (status === "online") {
    return "在线";
  }
  if (status === "warning") {
    return "告警";
  }
  return "离线";
}


function getChartX(index: number, total: number, width: number) {
  return (index / Math.max(1, total - 1)) * width;
}

function parseDateValue(value?: string) {
  if (!value) {
    return 0;
  }

  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

export function OverviewPage() {
  const navigate = useNavigate();
  const { overview, nodes, tasks, loading, refreshVersion, fetchOverviewTraffic } = useOutletContext<ConsoleOutletContext>();

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
          setTrafficError(getErrorMessage(error, "概览流量趋势加载失败"));
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
    const values = points.map((point) => point.throughputMbps);
    const activityCounts = points.map((point) => Math.max(point.startedCount, point.activeTaskCount));
    const linePath = buildLinePath(values, CHART_WIDTH, PLOT_H, PAD_TOP);
    const maxActivity = activityCounts.length ? safeMax(activityCounts) : 0;
    const barMaxH = PLOT_H * BAR_MAX_RATIO;
    const barWidth = points.length > 0 ? Math.max(4, (CHART_WIDTH / points.length) * 0.5) : 4;

    const bars = points.map((point, index) => {
      const count = Math.max(point.startedCount, point.activeTaskCount);
      const h = maxActivity > 0 && count > 0 ? (count / maxActivity) * barMaxH : 0;
      const x = getChartX(index, points.length, CHART_WIDTH) - barWidth / 2;
      const y = PAD_TOP + PLOT_H - h;
      return { x, y, w: barWidth, h, failed: point.failedCount > 0 };
    });

    const labelStep = Math.max(1, Math.ceil(points.length / 6));
    const labels = points
      .map((point, index) => ({
        label: point.label,
        x: getChartX(index, points.length, CHART_WIDTH),
        show: index % labelStep === 0 || index === points.length - 1
      }))
      .filter((item) => item.show);

    return {
      points,
      values,
      activityCounts,
      linePath,
      areaPath: linePath
        ? `${linePath} L${CHART_WIDTH},${PAD_TOP + PLOT_H} L0,${PAD_TOP + PLOT_H} Z`
        : "",
      bars,
      labels,
      hasRealSamples: Boolean(trafficData?.hasRealSamples),
      peakThroughput: values.length ? safeMax(values) : 0,
      maxActivityCount: maxActivity,
      totalStartedCount: points.reduce((sum, point) => sum + point.startedCount, 0),
      totalFailedCount: points.reduce((sum, point) => sum + point.failedCount, 0),
    };
  }, [trafficData]);



  return (
    <div className="animate-fade-in space-y-5">
      <section className="grid gap-1.5 sm:gap-3 grid-cols-4 animate-slide-up [animation-delay:150ms]">
        <Card className="glass-panel border-success/30 bg-gradient-to-br from-success/10 via-transparent to-transparent">
          <CardHeader className="pb-1 sm:pb-2 px-3 sm:px-6">
            <CardTitle className="text-[10px] sm:text-sm font-medium text-muted-foreground truncate">节点健康率</CardTitle>
          </CardHeader>
          <CardContent className="px-3 sm:px-6">
            <div className="flex items-center gap-1 sm:gap-2">
              <p className="text-lg sm:text-3xl font-semibold">{healthRate}<span className="text-[10px] sm:text-sm font-normal text-muted-foreground ml-0.5 sm:ml-1">%</span></p>
              {healthRate >= 90 ? (
                <TrendingUp className="size-4 sm:size-5 text-success hidden sm:block" />
              ) : null}
            </div>
            <p className="mt-1 text-sm text-muted-foreground hidden sm:block">
              {overview.healthyNodes}/{overview.totalNodes} 节点在线
            </p>
          </CardContent>
        </Card>

        <Card className="glass-panel border-info/30 bg-gradient-to-br from-info/10 via-transparent to-transparent">
          <CardHeader className="pb-1 sm:pb-2 px-3 sm:px-6">
            <CardTitle className="text-[10px] sm:text-sm font-medium text-muted-foreground truncate">任务成功率</CardTitle>
          </CardHeader>
          <CardContent className="px-3 sm:px-6">
            <p className="text-lg sm:text-3xl font-semibold">{overview.overallSuccessRate}<span className="text-[10px] sm:text-sm font-normal text-muted-foreground ml-0.5 sm:ml-1">%</span></p>
            <p className="mt-1 text-sm text-muted-foreground hidden sm:block">过去 24h 失败 {overview.failedTasks24h} 次</p>
          </CardContent>
        </Card>

        <Card className="glass-panel border-warning/30 bg-gradient-to-br from-warning/10 via-transparent to-transparent">
          <CardHeader className="pb-1 sm:pb-2 px-3 sm:px-6">
            <CardTitle className="text-[10px] sm:text-sm font-medium text-muted-foreground truncate">当前吞吐</CardTitle>
          </CardHeader>
          <CardContent className="px-3 sm:px-6">
            <p className="text-lg sm:text-3xl font-semibold">{overview.avgSyncMbps}<span className="text-[10px] sm:text-sm font-normal text-muted-foreground ml-0.5 sm:ml-1">Mbps</span></p>
            <p className="mt-1 text-sm text-muted-foreground hidden sm:block">执行中任务 {overview.runningTasks} 个</p>
          </CardContent>
        </Card>

        <Card className="glass-panel border-primary/30 bg-gradient-to-br from-primary/10 via-transparent to-transparent">
          <CardHeader className="pb-1 sm:pb-2 px-3 sm:px-6">
            <CardTitle className="text-[10px] sm:text-sm font-medium text-muted-foreground truncate">策略覆盖</CardTitle>
          </CardHeader>
          <CardContent className="px-3 sm:px-6">
            <p className="text-lg sm:text-3xl font-semibold">{overview.activePolicies}</p>
            <p className="mt-1 text-sm text-muted-foreground hidden sm:block">已启用策略（共 {tasks.length} 任务）</p>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 grid-cols-1 lg:grid-cols-2 animate-slide-up [animation-delay:200ms]">
        <div className="flex flex-col gap-2 w-full min-w-0">
          <Card className="glass-panel border-border/70 flex-1 flex flex-col min-h-0">
            <CardHeader className="pb-2">
              <div className="flex items-center justify-between gap-2">
                <CardTitle className="text-base">主机状态矩阵</CardTitle>
                {nodes.length > 0 ? (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-7 text-muted-foreground hover:text-foreground"
                    onClick={() => setMatrixFullscreen(true)}
                    aria-label="全屏查看状态矩阵"
                    title="全屏查看"
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
                  title="正在构建状态矩阵"
                  description="正在汇聚节点实时探测与最新备份指标..."
                  rows={2}
                />
              ) : null}
              {!loading && nodes.length === 0 ? (
                <p className="rounded-xl border border-border/70 bg-background/60 px-3 py-4 text-sm text-muted-foreground">
                  暂无可展示节点，请先在节点页完成接入。
                </p>
              ) : (
                  <div className="flex flex-col flex-1 min-h-0 h-full">
                  <div
                    role="group"
                    aria-label={`主机状态矩阵预览，共显示 ${previewNodes.length} / ${nodes.length} 台`}
                    className="flex flex-wrap gap-2 overflow-y-auto pb-4"
                  >
                    {previewNodes.map((node) => {
                      let dotColor = "bg-muted-foreground/30";
                      if (node.status === "online") dotColor = "bg-success";
                      if (node.status === "warning") dotColor = "bg-destructive";
                      return (
                        <button
                          key={node.id}
                          type="button"
                          className={`relative size-3 rounded-full ${dotColor} hover:ring-2 hover:ring-primary/50 hover:ring-offset-1 hover:ring-offset-background transition-shadow focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-1 group`}
                          onClick={() => navigate(`/app/nodes?keyword=${encodeURIComponent(node.name)}`)}
                          aria-label={`${node.name}，状态${getNodeStatusLabel(node.status)}`}
                        >
                          {/* Tooltip on hover */}
                          <span className="pointer-events-none absolute bottom-full left-1/2 mb-2 w-max -translate-x-1/2 opacity-0 transition-opacity group-hover:opacity-100 z-10 rounded-md border border-border/60 bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md">
                            <span className="font-medium">{node.name}</span>
                            <span className="ml-2 text-muted-foreground">{node.lastSeenAt || "未知"}</span>
                          </span>
                        </button>
                      );
                    })}
                  </div>

                  {hiddenNodeCount > 0 ? (
                    <p className="pb-3 text-[11px] text-muted-foreground">
                      当前仅展示 {previewNodes.length} / {nodes.length} 台节点，点击右上角可全屏查看全部。
                    </p>
                  ) : null}

                  {nodes.length > 0 && (
                    <div className="mt-auto shrink-0 flex items-center gap-4 text-[11px] text-muted-foreground pt-3 border-t border-border/40">
                      <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-success"></span>在线</span>
                      <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-destructive"></span>异常</span>
                      <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-muted-foreground/30"></span>离线</span>
                    </div>
                  )}
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        <div className="flex flex-col w-full min-w-0">
              <Card className="glass-panel border-border/70 flex-1 flex flex-col min-h-0">
            <CardHeader className="pb-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <CardTitle className="text-base">流量与活动趋势（{TRAFFIC_WINDOW_LABELS[trafficWindow]}）</CardTitle>
                <div className="flex items-center gap-2">
                  {(["1h", "24h", "7d"] as OverviewTrafficWindow[]).map((window) => (
                    <Button
                      key={window}
                      size="sm"
                      variant={trafficWindow === window ? "default" : "outline"}
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
                <LoadingState className="py-6" rows={3} title="加载流量趋势..." />
              ) : trafficError ? (
                <p className="rounded-xl border border-warning/30 bg-warning/10 px-3 py-4 text-sm text-warning">
                  {trafficError}
                </p>
              ) : (
                <>
                  <svg
                    viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
                    className="h-52 w-full"
                    role="img"
                    aria-label={
                      chartMetrics.hasRealSamples
                        ? `${TRAFFIC_WINDOW_LABELS[trafficWindow]}流量与活动趋势图，峰值平均总吞吐 ${chartMetrics.peakThroughput} Mbps，开始事件 ${chartMetrics.totalStartedCount} 次，失败事件 ${chartMetrics.totalFailedCount} 次`
                        : `${TRAFFIC_WINDOW_LABELS[trafficWindow]}流量与活动趋势图，暂无真实样本`
                    }
                  >
                    <defs>
                      <linearGradient id="throughputGrad" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="0%" stopColor="hsl(var(--chart-ingress))" stopOpacity="0.25" />
                        <stop offset="100%" stopColor="hsl(var(--chart-ingress))" stopOpacity="0.02" />
                      </linearGradient>
                    </defs>

                    {/* Subtle horizontal grid lines */}
                    {[0.25, 0.5, 0.75].map((frac) => (
                      <line
                        key={frac}
                        x1="0"
                        y1={(PAD_TOP + PLOT_H * (1 - frac)).toFixed(1)}
                        x2={CHART_WIDTH}
                        y2={(PAD_TOP + PLOT_H * (1 - frac)).toFixed(1)}
                        stroke="hsl(var(--border))"
                        strokeOpacity="0.25"
                        strokeWidth="0.5"
                        strokeDasharray="4 3"
                      />
                    ))}

                    {/* Activity bars (behind line, bottom-aligned in plot area) */}
                    {visibleLayers.activity && chartMetrics.bars.map((bar, i) => bar.h > 0 && (
                      <rect
                        key={`bar-${chartMetrics.points[i]?.timestamp ?? i}`}
                        x={bar.x.toFixed(2)}
                        y={bar.y.toFixed(2)}
                        width={bar.w.toFixed(2)}
                        height={bar.h.toFixed(2)}
                        rx="2"
                        fill="hsl(var(--chart-egress))"
                        opacity="0.22"
                      />
                    ))}

                    {/* Failed event markers (red cap on bars) */}
                    {visibleLayers.failures && chartMetrics.bars.map((bar, i) => bar.failed && (
                      <rect
                        key={`fail-${chartMetrics.points[i]?.timestamp ?? i}`}
                        x={bar.x.toFixed(2)}
                        y={bar.y.toFixed(2)}
                        width={bar.w.toFixed(2)}
                        height={Math.min(3, Math.max(1, bar.h)).toFixed(1)}
                        rx="1"
                        fill="hsl(var(--destructive))"
                        opacity="0.7"
                      />
                    ))}

                    {/* Throughput area fill */}
                    {visibleLayers.throughput && chartMetrics.areaPath ? (
                      <path d={chartMetrics.areaPath} fill="url(#throughputGrad)" />
                    ) : null}

                    {/* Throughput line */}
                    {visibleLayers.throughput && chartMetrics.linePath ? (
                      <path
                        d={chartMetrics.linePath}
                        fill="none"
                        stroke="hsl(var(--chart-ingress))"
                        strokeWidth="2"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                      />
                    ) : null}

                    {/* X-axis time labels */}
                    {chartMetrics.labels.map((item) => (
                      <text
                        key={item.label}
                        x={item.x.toFixed(1)}
                        y={CHART_HEIGHT - 2}
                        textAnchor="middle"
                        fill="hsl(var(--muted-foreground))"
                        fontSize="9"
                        opacity="0.6"
                      >
                        {item.label}
                      </text>
                    ))}
                  </svg>

                  {/* Legend — matching matrix style (border-t, inline dots) */}
                  <div className="mt-auto shrink-0 flex items-center gap-4 text-[11px] text-muted-foreground pt-3 border-t border-border/40">
                    {[
                      { key: "throughput", label: "吞吐", dotClass: "size-2 rounded-full bg-[hsl(var(--chart-ingress))]" },
                      { key: "activity", label: "活动", dotClass: "size-1.5 rounded-sm bg-[hsl(var(--chart-egress))]" },
                      { key: "failures", label: "失败", dotClass: "size-1.5 rounded-full bg-destructive" },
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

      <Dialog open={matrixFullscreen} onOpenChange={setMatrixFullscreen}>
        <DialogContent size="lg" className="max-h-[90vh] flex flex-col">
          <DialogHeader>
            <DialogTitle>主机状态矩阵（共 {nodes.length} 台）</DialogTitle>
            <DialogDescription>展示所有节点的状态圆点，点击任一节点可跳转到节点页查看详情。</DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <div className="flex-1 overflow-y-auto px-6 pb-6">
            <div role="group" aria-label={`主机状态矩阵全量，共 ${nodes.length} 台`} className="flex flex-wrap gap-2">
              {nodes.map((node) => {
                let dotColor = "bg-muted-foreground/30";
                if (node.status === "online") dotColor = "bg-success";
                if (node.status === "warning") dotColor = "bg-destructive";
                return (
                  <button
                    key={node.id}
                    type="button"
                    className={`relative size-3.5 rounded-full ${dotColor} hover:ring-2 hover:ring-primary/50 hover:ring-offset-1 hover:ring-offset-background transition-shadow focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-1 group`}
                    onClick={() => {
                      setMatrixFullscreen(false);
                      navigate(`/app/nodes?keyword=${encodeURIComponent(node.name)}`);
                    }}
                    aria-label={`${node.name}，状态${getNodeStatusLabel(node.status)}`}
                  >
                    <span className="pointer-events-none absolute bottom-full left-1/2 mb-2 w-max -translate-x-1/2 opacity-0 transition-opacity group-hover:opacity-100 z-10 rounded-md border border-border/60 bg-popover px-2 py-1 text-xs text-popover-foreground shadow-md">
                      <span className="font-medium">{node.name}</span>
                      <span className="ml-2 text-muted-foreground">{node.ip}</span>
                      <span className="ml-2 text-muted-foreground">{node.lastSeenAt || "未知"}</span>
                    </span>
                  </button>
                );
              })}
            </div>
            <div className="mt-4 flex items-center gap-4 text-[11px] text-muted-foreground pt-3 border-t border-border/40">
              <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-success" />在线</span>
              <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-destructive" />异常</span>
              <span className="inline-flex items-center gap-1.5"><span className="size-2 rounded-full bg-muted-foreground/30" />离线</span>
            </div>
          </div>
        </DialogContent>
      </Dialog>

      {/* 最近同步任务框 */}
      <section className="animate-slide-up [animation-delay:250ms]">
        <Card className="glass-panel border-border/70">
          <CardHeader className="flex flex-row items-center justify-between pb-2">
            <CardTitle className="text-base flex items-center gap-2">
              <Clock className="size-4 text-primary" />
              最近同步任务
            </CardTitle>
            <Button variant="ghost" size="sm" className="h-auto p-0 text-xs px-2 py-1 text-muted-foreground hover:text-foreground" onClick={() => navigate("/app/tasks")}>
              查看更多 &rarr;
            </Button>
          </CardHeader>
          <CardContent>
            {loading ? (
              <LoadingState className="py-6" rows={3} title="加载近期任务..." />
            ) : tasks.length === 0 ? (
              <p className="py-6 text-center text-sm text-muted-foreground">暂无任务数据</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-left text-sm">
                  <thead className="border-b border-border/50 text-xs text-muted-foreground uppercase bg-muted/20">
                    <tr>
                      <th className="px-4 py-2 font-medium">节点名称</th>
                      <th className="px-4 py-2 font-medium">任务名称</th>
                      <th className="px-4 py-2 font-medium">同步状态</th>
                      <th className="px-4 py-2 font-medium">传输数据量</th>
                      <th className="px-4 py-2 font-medium text-right">完成时间</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border/30">
                    {recentTasks.map((task) => {
                        // Estimate transfer size if speed exists and it's not pending/retrying
                        let transferData = "-";
                        if (task.speedMbps > 0) {
                          // Rough estimation for display purposes during active transfer
                          transferData = `≈ ${(task.speedMbps / 8).toFixed(1)} MB/s`;
                        }

                        let StatusIcon = Clock;
                        let statusColor = "text-muted-foreground";
                        let statusLabel = "队列中";

                        switch (task.status) {
                          case "success":
                            StatusIcon = CheckCircle2;
                            statusColor = "text-success";
                            statusLabel = "成功";
                            break;
                          case "failed":
                            StatusIcon = AlertTriangle;
                            statusColor = "text-destructive";
                            statusLabel = "失败";
                            break;
                          case "running":
                            StatusIcon = TrendingUp;
                            statusColor = "text-info";
                            statusLabel = "同步中";
                            break;
                          case "retrying":
                            StatusIcon = AlertTriangle;
                            statusColor = "text-warning";
                            statusLabel = "重试中";
                            break;
                        }

                        return (
                          <tr key={task.id} className="hover:bg-muted/10 transition-colors">
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
