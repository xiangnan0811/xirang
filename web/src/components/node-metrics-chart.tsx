import { useEffect, useState } from "react";
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend,
} from "recharts";
import { apiClient } from "@/lib/api/client";
import type { NodeMetricSample } from "@/lib/api/node-metrics-api";

type Props = {
  nodeId: number;
  token: string;
};

export function NodeMetricsChart({ nodeId, token }: Props) {
  const [samples, setSamples] = useState<NodeMetricSample[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    apiClient
      .getNodeMetrics(token, nodeId, { limit: 288, since: "24h" })
      .then((res) => {
        setSamples(res.items ?? []);
        setLoading(false);
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : "加载失败");
        setLoading(false);
      });
  }, [nodeId, token]);

  if (loading) return <div className="h-40 animate-pulse rounded bg-muted" />;
  if (error) return <p className="text-sm text-muted-foreground">{error}</p>;
  if (samples.length === 0)
    return <p className="text-sm text-muted-foreground">暂无资源采样数据</p>;

  const chartData = samples.map((s) => ({
    time: new Date(s.sampled_at).toLocaleTimeString("zh-CN", {
      hour: "2-digit",
      minute: "2-digit",
    }),
    cpu: parseFloat(s.cpu_pct.toFixed(1)),
    mem: parseFloat(s.mem_pct.toFixed(1)),
    disk: parseFloat(s.disk_pct.toFixed(1)),
  }));

  return (
    <div className="space-y-1">
      <p className="text-xs text-muted-foreground">资源趋势（近 24h）</p>
      <ResponsiveContainer width="100%" height={160}>
        <LineChart
          data={chartData}
          margin={{ top: 4, right: 4, left: -24, bottom: 0 }}
        >
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
          <XAxis
            dataKey="time"
            tick={{ fontSize: 10 }}
            stroke="var(--muted-foreground)"
            interval="preserveStartEnd"
          />
          <YAxis
            domain={[0, 100]}
            tick={{ fontSize: 10 }}
            stroke="var(--muted-foreground)"
            unit="%"
          />
          <Tooltip
            contentStyle={{
              backgroundColor: "var(--card)",
              border: "1px solid var(--border)",
              fontSize: 12,
            }}
            formatter={(value) => [value != null ? `${value}%` : ""]}
          />
          <Legend iconSize={8} wrapperStyle={{ fontSize: 11 }} />
          <Line
            type="monotone"
            dataKey="cpu"
            name="CPU"
            stroke="#3b82f6"
            dot={false}
            strokeWidth={1.5}
          />
          <Line
            type="monotone"
            dataKey="mem"
            name="内存"
            stroke="#10b981"
            dot={false}
            strokeWidth={1.5}
          />
          <Line
            type="monotone"
            dataKey="disk"
            name="磁盘"
            stroke="#f59e0b"
            dot={false}
            strokeWidth={1.5}
          />
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
