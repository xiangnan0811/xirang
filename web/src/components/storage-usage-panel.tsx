import { useEffect, useState } from "react";
import { HardDrive } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  BarChart,
} from "recharts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { LoadingState } from "@/components/ui/loading-state";
import { InlineAlert } from "@/components/ui/inline-alert";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { StorageUsageData } from "@/types/domain";

function pctColor(pct: number): string {
  if (pct >= 90) return "bg-destructive";
  if (pct >= 70) return "bg-warning";
  return "bg-success";
}

function pctTextColor(pct: number): string {
  if (pct >= 90) return "text-destructive";
  if (pct >= 70) return "text-warning";
  return "text-success";
}

export function StorageUsagePanel() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [data, setData] = useState<StorageUsageData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();

    setLoading(true);
    setError(null);
    apiClient
      .getStorageUsage(token, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          setData(result);
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setError(getErrorMessage(err, t('storage.loadFailed')));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setLoading(false);
        }
      });

    return () => controller.abort();
  }, [token, t]);

  if (loading) {
    return (
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{t('storage.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          <LoadingState title={t('storage.loadingTitle')} rows={2} />
        </CardContent>
      </Card>
    );
  }

  if (error || !data) {
    return (
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{t('storage.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          <InlineAlert tone="warning">
            {error ?? t("common.noData")}
          </InlineAlert>
        </CardContent>
      </Card>
    );
  }

  const { mountPoints, perNode } = data;

  return (
    <Card className="glass-panel border-border/70">
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          <HardDrive className="size-4 text-primary" />
          {t('storage.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Mount points as progress bars */}
        {mountPoints.length > 0 && (
          <div className="space-y-3">
            <p className="text-xs font-medium text-muted-foreground">{t('storage.mountPointUsage')}</p>
            {mountPoints.map((mp) => (
              <div key={mp.path} className="space-y-1">
                <div className="flex items-center justify-between text-sm">
                  <span className="font-mono text-xs truncate">{mp.path}</span>
                  <span className={`text-xs font-medium ${pctTextColor(mp.pct)}`}>
                    {mp.usedGB.toFixed(1)} / {mp.totalGB.toFixed(1)} GB ({Math.round(mp.pct)}%)
                  </span>
                </div>
                <div className="h-2 w-full rounded-full bg-muted/50 overflow-hidden">
                  <div
                    className={`h-full rounded-full transition-all ${pctColor(mp.pct)}`}
                    style={{ width: `${Math.min(100, mp.pct)}%` }}
                  />
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Per-node storage bar chart */}
        {perNode.length > 0 && (
          <div>
            <p className="mb-2 text-xs font-medium text-muted-foreground">{t('storage.perNodeUsage')}</p>
            <div role="img" aria-label={t('storage.perNodeChartAriaLabel')}>
              <ResponsiveContainer width="100%" height={Math.max(120, perNode.length * 28 + 40)}>
                <BarChart
                  data={perNode}
                  layout="vertical"
                  margin={{ top: 4, right: 4, left: 0, bottom: 4 }}
                >
                  <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} horizontal={false} />
                  <XAxis
                    type="number"
                    tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
                    stroke="transparent"
                    tickLine={false}
                    unit=" GB"
                  />
                  <YAxis
                    type="category"
                    dataKey="nodeName"
                    tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))" }}
                    stroke="transparent"
                    tickLine={false}
                    width={80}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "hsl(var(--card))",
                      border: "1px solid hsl(var(--border))",
                      fontSize: 11,
                      borderRadius: 6,
                    }}
                    formatter={(value) => [`${Number(value).toFixed(1)} GB`, t('storage.used')]}
                    labelStyle={{ color: "hsl(var(--muted-foreground))" }}
                  />
                  <Bar dataKey="usedGB" name={t('storage.used')} fill="hsl(var(--chart-ingress))" radius={[0, 4, 4, 0]} maxBarSize={16} />
                </BarChart>
              </ResponsiveContainer>
            </div>
          </div>
        )}

        {mountPoints.length === 0 && perNode.length === 0 && (
          <p className="text-sm text-muted-foreground text-center py-4">{t('common.noData')}</p>
        )}
      </CardContent>
    </Card>
  );
}
