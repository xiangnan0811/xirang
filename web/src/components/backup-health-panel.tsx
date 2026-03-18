import { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, ShieldAlert, TrendingUp } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  Area,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ComposedChart,
} from "recharts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { LoadingState } from "@/components/ui/loading-state";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { BackupHealthData } from "@/types/domain";

export function BackupHealthPanel() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [data, setData] = useState<BackupHealthData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();

    setLoading(true);
    setError(null);
    apiClient
      .getBackupHealth(token, { signal: controller.signal })
      .then((result) => {
        if (!controller.signal.aborted) {
          setData(result);
        }
      })
      .catch((err) => {
        if (!controller.signal.aborted) {
          setError(getErrorMessage(err, t('backupHealth.loadFailed')));
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
          <CardTitle className="text-base">{t('backupHealth.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          <LoadingState title={t('backupHealth.loadingTitle')} rows={2} />
        </CardContent>
      </Card>
    );
  }

  if (error || !data) {
    return (
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{t('backupHealth.title')}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="rounded-xl border border-warning/30 bg-warning/10 px-3 py-4 text-sm text-warning">
            {error ?? t('common.noData')}
          </p>
        </CardContent>
      </Card>
    );
  }

  const { summary, staleNodes, degradedPolicies, healthTrend } = data;

  const problems = [
    ...staleNodes.map((n) => ({
      key: `node-${n.nodeId}`,
      label: n.nodeName,
      detail: n.lastBackupAt ? t('backupHealth.hoursSinceBackup', { hours: Math.round(n.hoursSince) }) : t('backupHealth.neverBackedUp'),
      severity: n.hoursSince > 72 || !n.lastBackupAt ? ("critical" as const) : ("warning" as const),
    })),
    ...degradedPolicies.map((p) => ({
      key: `policy-${p.policyId}`,
      label: p.policyName,
      detail: t('backupHealth.consecutiveFailures', { count: p.consecutiveFailures }),
      severity: "critical" as const,
    })),
  ];

  return (
    <Card className="glass-panel border-border/70">
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          <ShieldAlert className="size-4 text-primary" />
          {t('backupHealth.title')}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Mini stat cards */}
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <MiniStat
            label={t('backupHealth.neverBackedUp')}
            value={summary.neverBackedUp}
            tone={summary.neverBackedUp > 0 ? "destructive" : "success"}
          />
          <MiniStat
            label={t('backupHealth.stale48h')}
            value={summary.stale48h}
            tone={summary.stale48h > 0 ? "warning" : "success"}
          />
          <MiniStat
            label={t('backupHealth.policiesHealthy')}
            value={`${summary.policiesHealthy}/${summary.policiesHealthy + summary.policiesDegraded}`}
            tone={summary.policiesDegraded > 0 ? "warning" : "success"}
          />
          <MiniStat
            label={t('backupHealth.successRate7d')}
            value={`${Math.round(summary.successRate7d)}%`}
            tone={summary.successRate7d >= 95 ? "success" : summary.successRate7d >= 80 ? "warning" : "destructive"}
          />
        </div>

        {/* Problems table */}
        {problems.length > 0 && (
          <div className="rounded-md border border-border/60 overflow-hidden">
            <div className="px-3 py-2 bg-muted/30 text-xs font-medium text-muted-foreground">
              {t('backupHealth.problemsTitle', { count: problems.length })}
            </div>
            <div className="divide-y divide-border/30 max-h-40 overflow-y-auto">
              {problems.map((p) => (
                <div
                  key={p.key}
                  className={`flex items-center gap-2 px-3 py-2 text-sm ${
                    p.severity === "critical" ? "bg-destructive/5" : "bg-warning/5"
                  }`}
                >
                  {p.severity === "critical" ? (
                    <AlertTriangle className="size-3.5 shrink-0 text-destructive" />
                  ) : (
                    <AlertTriangle className="size-3.5 shrink-0 text-warning" />
                  )}
                  <span className="font-medium truncate">{p.label}</span>
                  <span className="ml-auto shrink-0 text-xs text-muted-foreground">{p.detail}</span>
                </div>
              ))}
            </div>
          </div>
        )}

        {problems.length === 0 && (
          <div className="flex items-center gap-2 rounded-md border border-success/30 bg-success/5 px-3 py-3 text-sm text-success">
            <CheckCircle2 className="size-4" />
            {t('backupHealth.allHealthy')}
          </div>
        )}

        {/* 7-day trend chart */}
        {healthTrend.length > 0 && (
          <div>
            <p className="mb-2 text-xs font-medium text-muted-foreground flex items-center gap-1.5">
              <TrendingUp className="size-3.5" />
              {t('backupHealth.trend7d')}
            </p>
            <div role="img" aria-label={t('backupHealth.trendAriaLabel')}>
              <ResponsiveContainer width="100%" height={160}>
                <ComposedChart data={healthTrend} margin={{ top: 4, right: 4, left: -20, bottom: 4 }}>
                  <CartesianGrid strokeDasharray="4 3" stroke="hsl(var(--border))" strokeOpacity={0.25} vertical={false} />
                  <XAxis
                    dataKey="date"
                    tick={{ fontSize: 9, fill: "hsl(var(--muted-foreground))", opacity: 0.6 }}
                    stroke="transparent"
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
                  <Bar dataKey="total" name={t('backupHealth.totalRuns')} fill="hsl(var(--chart-egress))" opacity={0.2} maxBarSize={12} radius={[2, 2, 0, 0]} />
                  <Area
                    type="monotone"
                    dataKey="rate"
                    name={t('backupHealth.successRatePercent')}
                    stroke="hsl(var(--chart-ingress))"
                    strokeWidth={2}
                    fill="hsl(var(--chart-ingress))"
                    fillOpacity={0.08}
                    dot={false}
                    activeDot={{ r: 3 }}
                  />
                </ComposedChart>
              </ResponsiveContainer>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function MiniStat({ label, value, tone }: { label: string; value: string | number; tone: "success" | "warning" | "destructive" }) {
  const toneStyles = {
    success: "border-success/30 text-success",
    warning: "border-warning/30 text-warning",
    destructive: "border-destructive/30 text-destructive",
  };

  return (
    <div className={`rounded-lg border px-3 py-2 text-center ${toneStyles[tone]}`}>
      <div className="text-lg font-bold">{value}</div>
      <div className="text-[11px] text-muted-foreground">{label}</div>
    </div>
  );
}
