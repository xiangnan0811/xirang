import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from "recharts";
import { Loader2, Maximize2 } from "lucide-react";
import { apiClient } from "@/lib/api/client";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { NodeMetricSample } from "@/lib/api/node-metrics-api";
import type { NodeRecord } from "@/types/domain";

// 节点配色方案 — 高对比度、色盲友好
const NODE_PALETTE = [
  { stroke: "#3b82f6", fill: "#3b82f6" }, // blue
  { stroke: "#10b981", fill: "#10b981" }, // emerald
  { stroke: "#f59e0b", fill: "#f59e0b" }, // amber
  { stroke: "#ef4444", fill: "#ef4444" }, // red
  { stroke: "#8b5cf6", fill: "#8b5cf6" }, // violet
  { stroke: "#06b6d4", fill: "#06b6d4" }, // cyan
  { stroke: "#ec4899", fill: "#ec4899" }, // pink
  { stroke: "#84cc16", fill: "#84cc16" }, // lime
];

const MAX_VISIBLE_NODES = 8;

type MetricKey = "cpu" | "mem" | "disk";

const METRICS_KEYS: MetricKey[] = ["cpu", "mem", "disk"];

type ChartPoint = {
  time: string;
  [nodeKey: string]: string | number;
};

type Props = {
  nodes: NodeRecord[];
  token: string;
};

export function NodeMetricsPanel({ nodes, token }: Props) {
  const { t } = useTranslation();
  const onlineNodes = useMemo(
    () => nodes.filter((n) => n.status === "online").slice(0, MAX_VISIBLE_NODES),
    [nodes]
  );

  const [metricsMap, setMetricsMap] = useState<Record<number, NodeMetricSample[]>>({});
  const [loading, setLoading] = useState(true);
  const [enabledNodes, setEnabledNodes] = useState<Set<number>>(() => new Set());
  const initRef = useRef(false);

  // 初始化：默认全部开启
  useEffect(() => {
    if (onlineNodes.length > 0 && !initRef.current) {
      setEnabledNodes(new Set(onlineNodes.map((n) => n.id)));
      initRef.current = true;
    }
  }, [onlineNodes]);

  // 并行获取所有节点指标
  useEffect(() => {
    if (onlineNodes.length === 0) return;
    let cancelled = false;
    setLoading(true);

    Promise.all(
      onlineNodes.map((node) =>
        apiClient
          .getNodeMetrics(token, node.id, { limit: 288, since: "24h" })
          .then((res) => ({ nodeId: node.id, items: res.items ?? [] }))
          .catch(() => ({ nodeId: node.id, items: [] as NodeMetricSample[] }))
      )
    ).then((results) => {
      if (cancelled) return;
      const map: Record<number, NodeMetricSample[]> = {};
      for (const r of results) map[r.nodeId] = r.items;
      setMetricsMap(map);
      setLoading(false);
    });

    return () => { cancelled = true; };
  }, [onlineNodes, token]);

  // 节点 ID → 颜色映射
  const nodeColorMap = useMemo(() => {
    const map = new Map<number, typeof NODE_PALETTE[0]>();
    onlineNodes.forEach((n, i) => map.set(n.id, NODE_PALETTE[i % NODE_PALETTE.length]));
    return map;
  }, [onlineNodes]);

  // 节点 ID → 名称映射
  const nodeNameMap = useMemo(() => {
    const map = new Map<number, string>();
    onlineNodes.forEach((n) => map.set(n.id, n.name));
    return map;
  }, [onlineNodes]);

  const toggleNode = useCallback((nodeId: number) => {
    setEnabledNodes((prev) => {
      const next = new Set(prev);
      if (next.has(nodeId)) next.delete(nodeId);
      else next.add(nodeId);
      return next;
    });
  }, []);

  // 构建每个指标的 chart data：统一时间轴，每个节点一列
  const chartDataByMetric = useMemo(() => {
    const result: Record<MetricKey, ChartPoint[]> = { cpu: [], mem: [], disk: [] };

    // 收集所有时间戳并去重排序
    const timeSet = new Set<string>();
    for (const samples of Object.values(metricsMap)) {
      for (const s of samples) timeSet.add(s.sampled_at);
    }
    const sortedTimes = Array.from(timeSet).sort();

    // 为每个节点建立 time → sample 索引
    const nodeIndices = new Map<number, Map<string, NodeMetricSample>>();
    for (const [nodeId, samples] of Object.entries(metricsMap)) {
      const idx = new Map<string, NodeMetricSample>();
      for (const s of samples) idx.set(s.sampled_at, s);
      nodeIndices.set(Number(nodeId), idx);
    }

    for (const t of sortedTimes) {
      const label = new Date(t).toLocaleTimeString(undefined, {
        hour: "2-digit",
        minute: "2-digit",
      });

      const cpuPoint: ChartPoint = { time: label };
      const memPoint: ChartPoint = { time: label };
      const diskPoint: ChartPoint = { time: label };

      for (const node of onlineNodes) {
        const sample = nodeIndices.get(node.id)?.get(t);
        const key = `n${node.id}`;
        if (sample) {
          cpuPoint[key] = parseFloat(sample.cpu_pct.toFixed(1));
          memPoint[key] = parseFloat(sample.mem_pct.toFixed(1));
          diskPoint[key] = parseFloat(sample.disk_pct.toFixed(1));
        }
      }

      result.cpu.push(cpuPoint);
      result.mem.push(memPoint);
      result.disk.push(diskPoint);
    }

    return result;
  }, [metricsMap, onlineNodes]);

  const [expandedMetric, setExpandedMetric] = useState<MetricKey | null>(null);

  const hasData = Object.values(metricsMap).some((s) => s.length > 0);

  if (onlineNodes.length === 0) return null;

  if (loading) {
    return (
      <div className="flex items-center justify-center py-10">
        <Loader2 className="size-5 animate-spin text-muted-foreground" />
        <span className="ml-2 text-sm text-muted-foreground">{t("nodes.metricsLoading")}</span>
      </div>
    );
  }

  if (!hasData) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        {t("nodes.metricsEmpty")}
      </p>
    );
  }

  return (
    <div className="space-y-4">
      {/* 共享节点图例 */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        {onlineNodes.map((node) => {
          const color = nodeColorMap.get(node.id);
          const active = enabledNodes.has(node.id);
          return (
            <button
              key={node.id}
              type="button"
              className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs transition-[color,opacity] hover:bg-muted/60"
              style={{ opacity: active ? 1 : 0.35 }}
              onClick={() => toggleNode(node.id)}
              aria-pressed={active}
              title={t("nodes.metricToggleTitle", { action: active ? t("nodes.metricHide") : t("nodes.metricShow"), name: node.name })}
            >
              <span
                className="size-2.5 rounded-full shrink-0 transition-transform"
                style={{
                  backgroundColor: color?.stroke,
                  transform: active ? "scale(1)" : "scale(0.7)",
                }}
              />
              <span className="max-w-[8rem] truncate">{node.name}</span>
            </button>
          );
        })}
      </div>

      {/* 三张指标小图 */}
      <div className="grid gap-4 grid-cols-1 lg:grid-cols-3">
        {METRICS_KEYS.map((key) => (
          <MetricChart
            key={key}
            metricKey={key}
            label={t(`nodes.metricLabel_${key}`)}
            data={chartDataByMetric[key]}
            nodes={onlineNodes}
            enabledNodes={enabledNodes}
            nodeColorMap={nodeColorMap}
            nodeNameMap={nodeNameMap}
            onExpand={() => setExpandedMetric(key)}
          />
        ))}
      </div>

      {/* 放大图表 Dialog */}
      <Dialog open={expandedMetric !== null} onOpenChange={(open) => { if (!open) setExpandedMetric(null); }}>
        <DialogContent size="lg" className="md:max-w-[900px]">
          <DialogHeader>
            <DialogTitle>{expandedMetric ? t(`nodes.metricLabel_${expandedMetric}`) : ""}</DialogTitle>
            <DialogDescription className="sr-only">
              {expandedMetric ? t("nodes.metricExpandAriaLabel", { label: t(`nodes.metricLabel_${expandedMetric}`) }) : ""}
            </DialogDescription>
            <DialogCloseButton />
          </DialogHeader>
          <div className="overflow-y-auto px-6 pb-6 space-y-4">
            {/* Dialog 内节点图例 */}
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
              {onlineNodes.map((node) => {
                const color = nodeColorMap.get(node.id);
                const active = enabledNodes.has(node.id);
                return (
                  <button
                    key={node.id}
                    type="button"
                    className="inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs transition-[color,opacity] hover:bg-muted/60"
                    style={{ opacity: active ? 1 : 0.35 }}
                    onClick={() => toggleNode(node.id)}
                    aria-pressed={active}
                    title={t("nodes.metricToggleTitle", { action: active ? t("nodes.metricHide") : t("nodes.metricShow"), name: node.name })}
                  >
                    <span
                      className="size-2.5 rounded-full shrink-0 transition-transform"
                      style={{
                        backgroundColor: color?.stroke,
                        transform: active ? "scale(1)" : "scale(0.7)",
                      }}
                    />
                    <span className="max-w-[8rem] truncate">{node.name}</span>
                  </button>
                );
              })}
            </div>
            {expandedMetric && (
              <MetricChart
                metricKey={expandedMetric}
                label={t(`nodes.metricLabel_${expandedMetric}`)}
                data={chartDataByMetric[expandedMetric]}
                nodes={onlineNodes}
                enabledNodes={enabledNodes}
                nodeColorMap={nodeColorMap}
                nodeNameMap={nodeNameMap}
                height={400}
                idPrefix="exp-"
                showLabel={false}
              />
            )}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ---------- 单指标图 ----------

type MetricChartProps = {
  metricKey: MetricKey;
  label: string;
  data: ChartPoint[];
  nodes: NodeRecord[];
  enabledNodes: Set<number>;
  nodeColorMap: Map<number, typeof NODE_PALETTE[0]>;
  nodeNameMap: Map<number, string>;
  height?: number;
  onExpand?: () => void;
  idPrefix?: string;
  showLabel?: boolean;
};

function MetricChart({
  metricKey,
  label,
  data,
  nodes,
  enabledNodes,
  nodeColorMap,
  nodeNameMap,
  height = 160,
  onExpand,
  idPrefix = "",
  showLabel = true,
}: MetricChartProps) {
  const { t } = useTranslation();

  const gradientIds = useMemo(
    () => nodes.map((n) => `${idPrefix}grad-${metricKey}-${n.id}`),
    [nodes, metricKey, idPrefix]
  );

  const yMax = useMemo(() => {
    let max = 0;
    for (const point of data) {
      for (const node of nodes) {
        if (!enabledNodes.has(node.id)) continue;
        const v = point[`n${node.id}`];
        if (typeof v === "number" && v > max) max = v;
      }
    }
    if (max <= 0) return 100;
    const padded = max * 1.1;
    return Math.min(100, Math.max(10, Math.ceil(padded / 5) * 5));
  }, [data, nodes, enabledNodes]);

  return (
    <div className="glass-panel p-4">
      {showLabel && (
        <div className="mb-2 flex items-center justify-between">
          <p className="text-xs font-medium text-muted-foreground">{label}</p>
          {onExpand && (
            <button
              type="button"
              className="inline-flex items-center justify-center size-6 rounded-md text-muted-foreground transition-colors hover:text-foreground hover:bg-muted/60"
              onClick={onExpand}
              aria-label={t("nodes.metricExpandAriaLabel", { label })}
              title={t("nodes.metricExpandTitle")}
            >
              <Maximize2 className="size-3" />
            </button>
          )}
        </div>
      )}
      <ResponsiveContainer width="100%" height={height}>
        <AreaChart data={data} margin={{ top: 4, right: 4, left: -28, bottom: 0 }}>
          <defs>
            {nodes.map((node, i) => {
              const color = nodeColorMap.get(node.id);
              return (
                <linearGradient key={gradientIds[i]} id={gradientIds[i]} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={color?.fill} stopOpacity={0.2} />
                  <stop offset="100%" stopColor={color?.fill} stopOpacity={0.02} />
                </linearGradient>
              );
            })}
          </defs>
          <CartesianGrid
            strokeDasharray="4 3"
            stroke="hsl(var(--border))"
            strokeOpacity={0.25}
            vertical={false}
          />
          <XAxis
            dataKey="time"
            tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
            stroke="transparent"
            interval="preserveStartEnd"
            tickLine={false}
          />
          <YAxis
            domain={[0, yMax]}
            tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
            stroke="transparent"
            tickLine={false}
            axisLine={false}
            unit="%"
          />
          <Tooltip
            contentStyle={{
              backgroundColor: "hsl(var(--card))",
              border: "1px solid hsl(var(--border))",
              fontSize: 11,
              borderRadius: 6,
            }}
            labelStyle={{ color: "hsl(var(--muted-foreground))" }}
            formatter={(value, name) => {
              const nodeId = Number(String(name).replace("n", ""));
              const nodeName = nodeNameMap.get(nodeId) ?? String(name);
              return [`${value}%`, nodeName];
            }}
          />
          {nodes.map((node, i) => {
            const color = nodeColorMap.get(node.id);
            const visible = enabledNodes.has(node.id);
            return (
              <Area
                key={node.id}
                type="monotone"
                dataKey={`n${node.id}`}
                stroke={color?.stroke}
                fill={`url(#${gradientIds[i]})`}
                strokeWidth={1.5}
                dot={false}
                connectNulls
                hide={!visible}
                isAnimationActive={false}
              />
            );
          })}
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
