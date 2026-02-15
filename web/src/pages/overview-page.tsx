import { useMemo } from "react";
import { useNavigate, useOutletContext } from "react-router-dom";
import { Activity, ArrowDownCircle, ArrowUpCircle, Radar } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { StatusPulse } from "@/components/status-pulse";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { cn } from "@/lib/utils";

function buildLinePath(values: number[], width: number, height: number) {
  if (!values.length) {
    return "";
  }
  const max = Math.max(...values);
  const min = Math.min(...values);
  const delta = Math.max(1, max - min);
  return values
    .map((value, index) => {
      const x = (index / Math.max(1, values.length - 1)) * width;
      const y = height - ((value - min) / delta) * height;
      return `${index === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
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

  const ingressValues = trafficSeries.map((point) => point.ingressMbps);
  const egressValues = trafficSeries.map((point) => point.egressMbps);

  return (
    <div className="space-y-4">
      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="border-emerald-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">节点健康率</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{healthRate}%</p>
            <p className="mt-1 text-xs text-muted-foreground">
              {overview.healthyNodes}/{overview.totalNodes} 节点在线
            </p>
          </CardContent>
        </Card>

        <Card className="border-sky-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">任务成功率</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{overview.overallSuccessRate}%</p>
            <p className="mt-1 text-xs text-muted-foreground">过去 24h 失败 {overview.failedTasks24h} 次</p>
          </CardContent>
        </Card>

        <Card className="border-amber-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">实时吞吐</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{overview.avgSyncMbps} Mbps</p>
            <p className="mt-1 text-xs text-muted-foreground">执行中任务 {overview.runningTasks} 个</p>
          </CardContent>
        </Card>

        <Card className="border-violet-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">策略覆盖</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{overview.activePolicies}</p>
            <p className="mt-1 text-xs text-muted-foreground">已启用策略（共 {tasks.length} 任务）</p>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 xl:grid-cols-[1.45fr_1fr]">
        <Card className="grid-noise">
          <CardHeader>
            <div className="flex items-center justify-between gap-2">
              <CardTitle className="text-base">30+ 主机状态矩阵</CardTitle>
              <Badge variant="secondary">
                <Radar className="mr-1 size-3.5" />
                秒级刷新
              </Badge>
            </div>
          </CardHeader>
          <CardContent>
            {loading ? <p className="text-sm text-muted-foreground">正在构建状态矩阵...</p> : null}
            <div className="grid grid-cols-4 gap-2 sm:grid-cols-6 md:grid-cols-8 xl:grid-cols-10">
              {nodes.map((node) => (
                <div
                  key={node.id}
                  className="rounded-md border bg-background/80 p-2 text-[11px] transition hover:border-primary/50"
                  title={`${node.name} · ${node.ip} · 成功率 ${node.successRate}%`}
                >
                  <div className="mb-1 flex items-center justify-between gap-1">
                    <StatusPulse tone={node.status} />
                    <span className="text-[10px] text-muted-foreground">{node.diskFreePercent}%</span>
                  </div>
                  <p className="truncate font-medium">{node.name}</p>
                  <p className="truncate text-[10px] text-muted-foreground">{node.ip}</p>

                  <div className="mt-2 grid grid-cols-2 gap-1">
                    <Button
                      size="sm"
                      variant="outline"
                      className="h-7 px-2 text-[10px]"
                      aria-label={`查看 ${node.name} 的实时日志`}
                      onClick={() => navigate(`/app/logs?node=${encodeURIComponent(node.name)}`)}
                    >
                      查看日志
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 px-2 text-[10px]"
                      aria-label={`定位到 ${node.name} 的节点详情`}
                      onClick={() => navigate(`/app/nodes?keyword=${encodeURIComponent(node.name)}`)}
                    >
                      详情定位
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">流量趋势（近 1 小时）</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="rounded-lg border bg-background/70 p-3">
              <svg viewBox="0 0 320 120" className="h-40 w-full">
                <path d={buildLinePath(ingressValues, 320, 120)} fill="none" stroke="#22c55e" strokeWidth="2.2" />
                <path d={buildLinePath(egressValues, 320, 120)} fill="none" stroke="#38bdf8" strokeWidth="2.2" />
              </svg>
              <div className="mt-2 flex items-center gap-4 text-xs text-muted-foreground">
                <span className="inline-flex items-center gap-1">
                  <span className="size-2 rounded-full bg-emerald-500" />入站
                </span>
                <span className="inline-flex items-center gap-1">
                  <span className="size-2 rounded-full bg-sky-400" />出站
                </span>
              </div>
            </div>

            <div className="grid gap-2 sm:grid-cols-2">
              <div className="rounded-lg border p-3">
                <p className="text-xs text-muted-foreground">峰值入站</p>
                <p className="mt-1 text-xl font-semibold">{Math.max(...ingressValues)} Mbps</p>
                <p className="text-xs text-emerald-500">+12.4%</p>
              </div>
              <div className="rounded-lg border p-3">
                <p className="text-xs text-muted-foreground">峰值出站</p>
                <p className="mt-1 text-xl font-semibold">{Math.max(...egressValues)} Mbps</p>
                <p className="text-xs text-sky-500">+8.1%</p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">成功率看板</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {nodes.slice(0, 8).map((node) => (
              <div key={node.id} className="space-y-1">
                <div className="flex items-center justify-between text-xs">
                  <span>{node.name}</span>
                  <span className="text-muted-foreground">{node.successRate}%</span>
                </div>
                <div className="h-2 rounded-full bg-muted">
                  <div
                    className={cn(
                      "h-2 rounded-full",
                      node.successRate >= 92
                        ? "bg-emerald-500"
                        : node.successRate >= 80
                          ? "bg-amber-500"
                          : "bg-red-500"
                    )}
                    style={{ width: `${node.successRate}%` }}
                  />
                </div>
              </div>
            ))}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">移动端异常优先队列</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {unhealthyNodes.slice(0, 8).map((node) => (
              <div key={node.id} className="rounded-lg border p-3">
                <div className="flex items-center justify-between gap-2">
                  <p className="font-medium">{node.name}</p>
                  <Badge variant={node.status === "offline" ? "danger" : "warning"}>
                    {node.status === "offline" ? "离线" : "告警"}
                  </Badge>
                </div>
                <p className="mt-1 text-xs text-muted-foreground">最近备份：{node.lastBackupAt}</p>
                <div className="mt-2 flex items-center gap-3 text-xs">
                  <span className="inline-flex items-center gap-1 text-rose-500">
                    <ArrowDownCircle className="size-3.5" /> 故障优先
                  </span>
                  <span className="inline-flex items-center gap-1 text-emerald-500">
                    <ArrowUpCircle className="size-3.5" /> 可一键跳转处理
                  </span>
                </div>
              </div>
            ))}
            {!unhealthyNodes.length ? (
              <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-4 text-sm text-emerald-600 dark:text-emerald-300">
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
