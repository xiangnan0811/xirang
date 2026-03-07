import { useMemo, useState } from "react";
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
import type { NodeStatus } from "@/types/domain";

const CHART_WIDTH = 320;
const CHART_HEIGHT = 120;
const MATRIX_PREVIEW_LIMIT = 80;

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

function buildLinePath(values: number[], width: number, height: number) {
  if (!values.length) {
    return "";
  }
  const max = safeMax(values);
  const min = safeMin(values);
  const delta = Math.max(1, max - min);
  return values
    .map((value, index) => {
      const x = (index / Math.max(1, values.length - 1)) * width;
      const y = height - ((value - min) / delta) * height;
      return `${index === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`;
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

function parseDateValue(value?: string) {
  if (!value) {
    return 0;
  }

  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
}

export function OverviewPage() {
  const navigate = useNavigate();
  const { overview, nodes, tasks, trafficSeries, loading } = useOutletContext<ConsoleOutletContext>();

  const healthRate = overview.totalNodes > 0
    ? Math.round((overview.healthyNodes / overview.totalNodes) * 100)
    : 0;

  const [matrixFullscreen, setMatrixFullscreen] = useState(false);
  const previewNodes = useMemo(() => nodes.slice(0, MATRIX_PREVIEW_LIMIT), [nodes]);
  const hiddenNodeCount = Math.max(0, nodes.length - previewNodes.length);
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
    const ingressValues = trafficSeries.map((point) => point.ingressMbps);
    const egressValues = trafficSeries.map((point) => point.egressMbps);
    const ingressLinePath = buildLinePath(ingressValues, CHART_WIDTH, CHART_HEIGHT);
    const egressLinePath = buildLinePath(egressValues, CHART_WIDTH, CHART_HEIGHT);
    return {
      ingressValues,
      egressValues,
      ingressLinePath,
      egressLinePath,
      ingressAreaPath: ingressLinePath
        ? `${ingressLinePath} L${CHART_WIDTH},${CHART_HEIGHT} L0,${CHART_HEIGHT} Z`
        : "",
      egressAreaPath: egressLinePath
        ? `${egressLinePath} L${CHART_WIDTH},${CHART_HEIGHT} L0,${CHART_HEIGHT} Z`
        : "",
      peakIngress: ingressValues.length ? safeMax(ingressValues) : 0,
      peakEgress: egressValues.length ? safeMax(egressValues) : 0
    };
  }, [trafficSeries]);



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
            <CardTitle className="text-[10px] sm:text-sm font-medium text-muted-foreground truncate">实时吞吐</CardTitle>
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
              <CardTitle className="text-base">流量趋势（近 1 小时）</CardTitle>
            </CardHeader>
            <CardContent className="flex-1 flex flex-col min-h-0 pt-2">

              <svg
                viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
                className="h-40 w-full"
                role="img"
                aria-label={
                  chartMetrics.ingressValues.length > 0 || chartMetrics.egressValues.length > 0
                    ? `近一小时流量趋势图，峰值入站 ${chartMetrics.peakIngress} Mbps，峰值出站 ${chartMetrics.peakEgress} Mbps`
                    : "近一小时流量趋势图，暂无数据"
                }
              >
                <defs>
                  <linearGradient id="ingressGrad" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="hsl(var(--chart-ingress))" stopOpacity="0.3" />
                    <stop offset="100%" stopColor="hsl(var(--chart-ingress))" stopOpacity="0.02" />
                  </linearGradient>
                  <linearGradient id="egressGrad" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="hsl(var(--chart-egress))" stopOpacity="0.3" />
                    <stop offset="100%" stopColor="hsl(var(--chart-egress))" stopOpacity="0.02" />
                  </linearGradient>
                </defs>
                {chartMetrics.ingressValues.length > 0 ? (
                  <path
                    d={chartMetrics.ingressAreaPath}
                    fill="url(#ingressGrad)"
                  />
                ) : null}
                {chartMetrics.egressValues.length > 0 ? (
                  <path
                    d={chartMetrics.egressAreaPath}
                    fill="url(#egressGrad)"
                  />
                ) : null}
                <path
                  d={chartMetrics.ingressLinePath}
                  fill="none"
                  stroke="hsl(var(--chart-ingress))"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
                <path
                  d={chartMetrics.egressLinePath}
                  fill="none"
                  stroke="hsl(var(--chart-egress))"
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
              <div className="mt-4 flex items-center gap-4 text-xs text-muted-foreground">
                <span className="inline-flex items-center gap-1">
                  <span className="size-2 rounded-full bg-success" />入站
                </span>
                <span className="inline-flex items-center gap-1">
                  <span className="size-2 rounded-full bg-info" />出站
                </span>
              </div>
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
