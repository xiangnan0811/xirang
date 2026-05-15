import { useMemo } from "react";
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
import { Maximize2 } from "lucide-react";
import type { NodeRecord } from "@/types/domain";
import type { NodePaletteEntry } from "@/components/node-metrics-theme";

export type MetricKey = "cpu" | "mem" | "disk";

export type ChartPoint = {
  time: string;
  [nodeKey: string]: string | number;
};

export type MetricChartProps = {
  metricKey: MetricKey;
  label: string;
  data: ChartPoint[];
  nodes: NodeRecord[];
  enabledNodes: Set<number>;
  nodeColorMap: Map<number, NodePaletteEntry>;
  nodeNameMap: Map<number, string>;
  height?: number;
  onExpand?: () => void;
  idPrefix?: string;
  showLabel?: boolean;
  clipPercentile?: number;
};

const RAW_VALUE_PREFIX = "__raw_";

function percentileCap(values: number[], percentile: number) {
  if (values.length < 4) return null;
  const sorted = [...values].sort((first, second) => first - second);
  const index = Math.floor((sorted.length - 1) * percentile);
  const cap = sorted[index];
  const max = sorted[sorted.length - 1];
  return cap > 0 && cap < max ? cap : null;
}

export function MetricChart({
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
  clipPercentile,
}: MetricChartProps) {
  const { t } = useTranslation();

  const gradientIds = useMemo(
    () => nodes.map((n) => `${idPrefix}grad-${metricKey}-${n.id}`),
    [nodes, metricKey, idPrefix]
  );

  const { displayData, displayKeyForNode, rawKeyForNode } = useMemo(() => {
    if (!clipPercentile) {
      return {
        displayData: data,
        displayKeyForNode: (nodeId: number) => `n${nodeId}`,
        rawKeyForNode: (nodeId: number) => `n${nodeId}`,
      };
    }

    const values: number[] = [];
    for (const point of data) {
      for (const node of nodes) {
        if (!enabledNodes.has(node.id)) continue;
        const value = point[`n${node.id}`];
        if (typeof value === "number") values.push(value);
      }
    }

    const cap = percentileCap(values, clipPercentile);
    if (cap === null) {
      return {
        displayData: data,
        displayKeyForNode: (nodeId: number) => `n${nodeId}`,
        rawKeyForNode: (nodeId: number) => `n${nodeId}`,
      };
    }

    return {
      displayData: data.map((point) => {
        const next: ChartPoint = { ...point };
        for (const node of nodes) {
          const key = `n${node.id}`;
          const value = point[key];
          if (typeof value !== "number") continue;
          next[`${RAW_VALUE_PREFIX}${key}`] = value;
          next[key] = Math.min(value, cap);
        }
        return next;
      }),
      displayKeyForNode: (nodeId: number) => `n${nodeId}`,
      rawKeyForNode: (nodeId: number) => `${RAW_VALUE_PREFIX}n${nodeId}`,
    };
  }, [clipPercentile, data, nodes, enabledNodes]);

  const yMax = useMemo(() => {
    let max = 0;
    for (const point of displayData) {
      for (const node of nodes) {
        if (!enabledNodes.has(node.id)) continue;
        const v = point[displayKeyForNode(node.id)];
        if (typeof v === "number" && v > max) max = v;
      }
    }
    if (max <= 0) return 100;
    const padded = max * 1.1;
    return Math.min(100, Math.max(10, Math.ceil(padded / 5) * 5));
  }, [displayData, displayKeyForNode, nodes, enabledNodes]);

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
              <Maximize2 className="size-3" aria-hidden />
            </button>
          )}
        </div>
      )}
      <ResponsiveContainer width="100%" height={height}>
        <AreaChart data={displayData} margin={{ top: 4, right: 4, left: -28, bottom: 0 }}>
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
            formatter={(value, name, item) => {
              const nodeId = Number(String(name).replace("n", ""));
              const nodeName = nodeNameMap.get(nodeId) ?? String(name);
              const rawValue = item.payload?.[rawKeyForNode(nodeId)];
              return [`${typeof rawValue === "number" ? rawValue : value}%`, nodeName];
            }}
          />
          {nodes.map((node, i) => {
            const color = nodeColorMap.get(node.id);
            const visible = enabledNodes.has(node.id);
            return (
              <Area
                key={node.id}
                type="monotone"
                dataKey={displayKeyForNode(node.id)}
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
