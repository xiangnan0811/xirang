import { useEffect, useMemo, useState } from "react";
import { useNavigate, useOutletContext } from "react-router-dom";
import { Activity, ArrowDownCircle, ArrowUpCircle, Radar, TrendingUp } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { StatusPulse } from "@/components/status-pulse";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { LoadingState } from "@/components/ui/loading-state";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { cn } from "@/lib/utils";
import type { NodeStatus } from "@/types/domain";

const CHART_WIDTH = 320;
const CHART_HEIGHT = 120;
const MATRIX_CHUNK_SIZE = 80;
const MATRIX_SOFT_LIMIT = 320;

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

export function OverviewPage() {
  const navigate = useNavigate();
  const { overview, nodes, tasks, trafficSeries, loading } = useOutletContext<ConsoleOutletContext>();

  const healthRate = overview.totalNodes > 0
    ? Math.round((overview.healthyNodes / overview.totalNodes) * 100)
    : 0;

  const unhealthyNodes = useMemo(
    () => nodes.filter((node) => node.status !== "online").sort((a, b) => a.status.localeCompare(b.status)),
    [nodes]
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


  const cappedMatrixNodes = useMemo(() => nodes.slice(0, MATRIX_SOFT_LIMIT), [nodes]);
  const [matrixVisibleCount, setMatrixVisibleCount] = useState<number>(MATRIX_CHUNK_SIZE);

  useEffect(() => {
    setMatrixVisibleCount((prev) => {
      if (cappedMatrixNodes.length <= MATRIX_CHUNK_SIZE) {
        return cappedMatrixNodes.length;
      }
      return Math.min(Math.max(prev, MATRIX_CHUNK_SIZE), cappedMatrixNodes.length);
    });
  }, [cappedMatrixNodes.length]);

  const matrixNodes = useMemo(
    () => cappedMatrixNodes.slice(0, matrixVisibleCount),
    [cappedMatrixNodes, matrixVisibleCount]
  );
  const hiddenMatrixCount = Math.max(0, nodes.length - matrixNodes.length);
  const hiddenBySoftLimitCount = Math.max(0, nodes.length - cappedMatrixNodes.length);
  const hasMoreMatrixNodes = matrixVisibleCount < cappedMatrixNodes.length;
  const canCollapseMatrix = matrixVisibleCount > Math.min(MATRIX_CHUNK_SIZE, cappedMatrixNodes.length);
  const nextMatrixBatchCount = Math.min(
    MATRIX_CHUNK_SIZE,
    Math.max(0, cappedMatrixNodes.length - matrixVisibleCount)
  );

  return (
    <div className="animate-fade-in space-y-5">
      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4 animate-slide-up [animation-delay:150ms]">
        <Card className="glass-panel border-success/30 bg-gradient-to-br from-success/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">节点健康率</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="flex items-center gap-2">
              <p className="text-3xl font-semibold">{healthRate}%</p>
              {healthRate >= 90 ? (
                <TrendingUp className="size-5 text-success" />
              ) : null}
            </div>
            <p className="mt-1 text-sm text-muted-foreground">
              {overview.healthyNodes}/{overview.totalNodes} 节点在线
            </p>
          </CardContent>
        </Card>

        <Card className="glass-panel border-info/30 bg-gradient-to-br from-info/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">任务成功率</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{overview.overallSuccessRate}%</p>
            <p className="mt-1 text-sm text-muted-foreground">过去 24h 失败 {overview.failedTasks24h} 次</p>
          </CardContent>
        </Card>

        <Card className="glass-panel border-warning/30 bg-gradient-to-br from-warning/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">实时吞吐</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{overview.avgSyncMbps} Mbps</p>
            <p className="mt-1 text-sm text-muted-foreground">执行中任务 {overview.runningTasks} 个</p>
          </CardContent>
        </Card>

        <Card className="glass-panel border-primary/30 bg-gradient-to-br from-primary/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">策略覆盖</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{overview.activePolicies}</p>
            <p className="mt-1 text-sm text-muted-foreground">已启用策略（共 {tasks.length} 任务）</p>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 lg:grid-cols-[1.35fr_1fr] xl:grid-cols-[1.45fr_1fr] animate-slide-up [animation-delay:200ms]">
        <Card className="glass-panel border-border/70">
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div>
                <CardTitle className="text-base">主机状态矩阵</CardTitle>
                <p className="text-xs text-muted-foreground">按在线、告警、离线状态快速定位异常节点</p>
              </div>
              <div className="flex items-center gap-2">
                <Badge variant="secondary">
                  <Radar className="mr-1 size-3.5" />
                  秒级刷新
                </Badge>
                <Badge variant="outline">总计 {nodes.length} 台</Badge>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            {loading ? (
              <LoadingState
                className="mb-3"
                title="正在构建状态矩阵"
                description="正在汇聚节点实时探测与最新备份指标..."
                rows={2}
              />
            ) : null}
            {!loading && matrixNodes.length === 0 ? (
              <p className="rounded-xl border border-border/70 bg-background/60 px-3 py-4 text-sm text-muted-foreground">
                暂无可展示节点，请先在节点页完成接入。
              </p>
            ) : (
              <div className="grid grid-cols-4 gap-2 sm:grid-cols-6 md:grid-cols-8 lg:grid-cols-10">
                {matrixNodes.map((node) => (
                  <button
                    key={node.id}
                    type="button"
                    className="interactive-surface flex flex-col justify-between p-2.5 text-left text-xs"
                    title={`${node.name} · ${node.ip} · 成功率 ${node.successRate}%`}
                    onClick={() => navigate(`/app/nodes?keyword=${encodeURIComponent(node.name)}`)}
                    aria-label={`${node.name}，状态${getNodeStatusLabel(node.status)}，磁盘剩余 ${node.diskFreePercent}%，成功率 ${node.successRate}%`}
                  >
                    <div className="mb-1 flex items-center justify-between gap-1">
                      <StatusPulse tone={node.status} />
                      <span className="text-[11px] text-muted-foreground">{node.diskFreePercent}%</span>
                    </div>
                    <p className="truncate font-medium">{node.name}</p>
                    <p className="sr-only">状态：{getNodeStatusLabel(node.status)}</p>
                  </button>
                ))}
              </div>
            )}

            <div className="mt-3 flex flex-wrap items-center gap-2">
              <p className="text-xs text-muted-foreground">已展示 {matrixNodes.length} / {nodes.length} 台节点</p>
              {hasMoreMatrixNodes ? (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    setMatrixVisibleCount((prev) => Math.min(prev + MATRIX_CHUNK_SIZE, cappedMatrixNodes.length))
                  }
                >
                  继续加载 {nextMatrixBatchCount} 台
                </Button>
              ) : null}
              {canCollapseMatrix ? (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setMatrixVisibleCount(Math.min(MATRIX_CHUNK_SIZE, cappedMatrixNodes.length))}
                >
                  收起列表
                </Button>
              ) : null}
            </div>

            {hiddenBySoftLimitCount > 0 ? (
              <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>
                  当前为性能考虑最多渲染 {MATRIX_SOFT_LIMIT} 台，另有 {hiddenBySoftLimitCount} 台可在节点页查看。
                </span>
                <Button size="sm" variant="ghost" className="h-7 px-2" onClick={() => navigate("/app/nodes")}>
                  打开节点页
                </Button>
              </div>
            ) : null}

            {hiddenMatrixCount > 0 && hiddenBySoftLimitCount === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">你可以继续加载查看更多节点卡片。</p>
            ) : null}
          </CardContent>
        </Card>

        <Card className="glass-panel border-info/30">
          <CardHeader>
            <CardTitle className="text-base">流量趋势（近 1 小时）</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="glass-panel p-4">
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
              <div className="mt-2 flex items-center gap-4 text-xs text-muted-foreground">
                <span className="inline-flex items-center gap-1">
                  <span className="size-2 rounded-full bg-success" />入站
                </span>
                <span className="inline-flex items-center gap-1">
                  <span className="size-2 rounded-full bg-info" />出站
                </span>
              </div>
            </div>

            <div className="grid gap-2 sm:grid-cols-2">
              <div className="rounded-xl border border-success/30 bg-success/10 p-3">
                <p className="text-xs text-muted-foreground">峰值入站</p>
                <p className="mt-1 text-xl font-semibold">{chartMetrics.peakIngress} Mbps</p>
              </div>
              <div className="rounded-xl border border-info/30 bg-info/10 p-3">
                <p className="text-xs text-muted-foreground">峰值出站</p>
                <p className="mt-1 text-xl font-semibold">{chartMetrics.peakEgress} Mbps</p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 md:grid-cols-2 animate-slide-up [animation-delay:250ms]">
        <Card className="glass-panel">
          <CardHeader>
            <CardTitle className="text-base">成功率看板</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {nodes.slice(0, 8).map((node) => (
              <div key={node.id} className="glass-panel space-y-2 px-4 py-3">
                <div className="flex items-center justify-between text-xs">
                  <span>{node.name}</span>
                  <span className="text-muted-foreground">{node.successRate}%</span>
                </div>
                <div className="h-2 rounded-full bg-muted">
                  <div
                    className={cn(
                      "h-2 rounded-full",
                      node.successRate >= 92
                        ? "bg-success"
                        : node.successRate >= 80
                          ? "bg-warning"
                          : "bg-destructive"
                    )}
                    style={{ width: `${node.successRate}%` }}
                  />
                </div>
              </div>
            ))}
          </CardContent>
        </Card>

        <Card className="glass-panel">
          <CardHeader>
            <CardTitle className="text-base">移动端异常优先队列</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {unhealthyNodes.slice(0, 8).map((node) => (
              <div key={node.id} className="glass-panel p-3.5 flex flex-col gap-1 interactive-surface">
                <div className="flex items-center justify-between gap-2">
                  <p className="font-medium">{node.name}</p>
                  <Badge variant={node.status === "offline" ? "danger" : "warning"}>
                    {node.status === "offline" ? "离线" : "告警"}
                  </Badge>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">最近备份：{node.lastBackupAt}</p>
                <div className="mt-2 flex items-center gap-3 text-xs">
                  <span className="inline-flex items-center gap-1 text-destructive">
                    <ArrowDownCircle className="size-3.5" /> 故障优先
                  </span>
                  <span className="inline-flex items-center gap-1 text-success">
                    <ArrowUpCircle className="size-3.5" /> 可一键跳转处理
                  </span>
                </div>
              </div>
            ))}
            {!unhealthyNodes.length ? (
              <div className="rounded-xl border border-success/30 bg-success/10 p-4 text-sm text-success">
                <Activity className="mb-1 size-4" />
                当前无异常节点，移动端优先队列为空。
              </div>
            ) : null}
          </CardContent>
        </Card>
      </section>
    </div>
  );
}
