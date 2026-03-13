import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useOutletContext } from "react-router-dom";
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
  Cell,
} from "recharts";
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
import { NodeMetricsChart } from "@/components/node-metrics-chart";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { useAuth } from "@/context/auth-context";
import { getErrorMessage } from "@/lib/utils";
import type { NodeStatus, OverviewTrafficSeries, OverviewTrafficWindow } from "@/types/domain";

const MATRIX_PREVIEW_LIMIT = 80;
const TRAFFIC_WINDOW_LABELS: Record<OverviewTrafficWindow, string> = {
  "1h": "近 1 小时",
  "24h": "近 24 小时",
  "7d": "近 7 天"
};


function getNodeStatusLabel(status: NodeStatus) {
  if (status === "online") {
    return "在线";
  }
  if (status === "warning") {
    return "告警";
  }
  return "离线";
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



  return (
    <div className="animate-fade-in space-y-5">
      <StatCardsSection
        className="grid-cols-4 animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: "节点健康率",
            value: healthRate,
            unit: "%",
            icon: healthRate >= 90 ? (
              <TrendingUp className="size-4 sm:size-5 text-success hidden sm:block" />
            ) : undefined,
            description: `${overview.healthyNodes}/${overview.totalNodes} 节点在线`,
            tone: "success",
          },
          {
            title: "任务成功率",
            value: overview.overallSuccessRate,
            unit: "%",
            description: `过去 24h 失败 ${overview.failedTasks24h} 次`,
            tone: "info",
          },
          {
            title: "当前吞吐",
            value: overview.avgSyncMbps,
            unit: "Mbps",
            description: `执行中任务 ${overview.runningTasks} 个`,
            tone: "warning",
          },
          {
            title: "策略覆盖",
            value: overview.activePolicies,
            description: `已启用策略（共 ${tasks.length} 任务）`,
            tone: "primary",
          },
        ]}
      />

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
                            <span className="ml-2 text-muted-foreground">{node.lastProbeAt || node.lastSeenAt || "未知"}</span>
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
                  <div
                    role="img"
                    aria-label={
                      chartMetrics.hasRealSamples
                        ? `${TRAFFIC_WINDOW_LABELS[trafficWindow]}流量与活动趋势图，峰值平均总吞吐 ${chartMetrics.peakThroughput} Mbps，开始事件 ${chartMetrics.totalStartedCount} 次，失败事件 ${chartMetrics.totalFailedCount} 次`
                        : `${TRAFFIC_WINDOW_LABELS[trafficWindow]}流量与活动趋势图，暂无真实样本`
                    }
                  >
                    <ResponsiveContainer width="100%" height={208}>
                      <ComposedChart
                        data={chartMetrics.chartData}
                        margin={{ top: 6, right: 4, left: -20, bottom: 4 }}
                      >
                        <defs>
                          <linearGradient id="throughputGrad" x1="0" y1="0" x2="0" y2="1">
                            <stop offset="0%" stopColor="hsl(var(--chart-ingress))" stopOpacity="0.25" />
                            <stop offset="100%" stopColor="hsl(var(--chart-ingress))" stopOpacity="0.02" />
                          </linearGradient>
                        </defs>
                        <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} vertical={false} />
                        <XAxis
                          dataKey="label"
                          tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
                          stroke="transparent"
                          interval="preserveStartEnd"
                          tickLine={false}
                        />
                        <YAxis
                          tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
                          stroke="transparent"
                          tickLine={false}
                          axisLine={false}
                        />
                        <Tooltip
                          contentStyle={{
                            backgroundColor: "hsl(var(--card))",
                            border: "1px solid hsl(var(--border))",
                            fontSize: 11,
                            borderRadius: 6,
                          }}
                          labelStyle={{ color: "hsl(var(--muted-foreground))" }}
                        />
                        {visibleLayers.activity && (
                          <Bar dataKey="activity" name="活动" maxBarSize={8} radius={[2, 2, 0, 0]}>
                            {chartMetrics.chartData.map((entry, index) => (
                              <Cell
                                key={`activity-${index}`}
                                fill={entry.failed > 0 && visibleLayers.failures ? "hsl(var(--destructive))" : "hsl(var(--chart-egress))"}
                                opacity={entry.failed > 0 && visibleLayers.failures ? 0.7 : 0.22}
                              />
                            ))}
                          </Bar>
                        )}
                        {visibleLayers.throughput && (
                          <Area
                            type="monotone"
                            dataKey="throughput"
                            name="吞吐 (Mbps)"
                            stroke="hsl(var(--chart-ingress))"
                            strokeWidth={2}
                            fill="url(#throughputGrad)"
                            dot={false}
                            activeDot={{ r: 3 }}
                          />
                        )}
                      </ComposedChart>
                    </ResponsiveContainer>
                  </div>

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

      {/* 节点资源概览 */}
      {nodes.length > 0 && nodes.some(n => n.status === "online") && (
        <section className="animate-slide-up [animation-delay:250ms]">
          <Card className="glass-panel border-border/70">
            <CardHeader className="pb-3">
              <CardTitle className="text-base">节点资源概览（近 24h）</CardTitle>
            </CardHeader>
            <CardContent>
              <div className="grid gap-4 grid-cols-1 md:grid-cols-2 lg:grid-cols-3">
                {nodes
                  .filter(n => n.status === "online")
                  .slice(0, 6)
                  .map(node => (
                    <div key={node.id} className="rounded-lg border border-border/60 bg-card/50 p-3">
                      <p className="mb-2 text-sm font-medium truncate" title={node.name}>{node.name}</p>
                      {token && <NodeMetricsChart nodeId={node.id} token={token} />}
                    </div>
                  ))}
              </div>
              {nodes.filter(n => n.status === "online").length > 6 && (
                <p className="mt-3 text-xs text-muted-foreground">
                  仅展示前 6 个在线节点，完整资源数据请前往节点页查看。
                </p>
              )}
            </CardContent>
          </Card>
        </section>
      )}

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
                      <span className="ml-2 text-muted-foreground">{node.lastProbeAt || node.lastSeenAt || "未知"}</span>
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
