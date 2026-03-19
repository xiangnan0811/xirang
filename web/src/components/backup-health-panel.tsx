import { useEffect, useId, useState } from "react";
import { AlertTriangle, CheckCircle2, ShieldAlert, TrendingUp, ServerOff, History, Activity } from "lucide-react";
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
import { InlineAlert } from "@/components/ui/inline-alert";
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
  const gradientId = useId();

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
      <div className="space-y-5">
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
          {[1, 2, 3, 4].map((i) => (
            <Card key={i} className="glass-panel h-[76px] animate-pulse bg-muted/20" />
          ))}
        </div>
        <Card className="glass-panel border-border/70">
          <CardHeader className="pb-3"><CardTitle className="text-base">{t('backupHealth.title')}</CardTitle></CardHeader>
          <CardContent><LoadingState title={t('backupHealth.loadingTitle')} rows={2} /></CardContent>
        </Card>
      </div>
    );
  }

  if (error || !data) {
    return (
      <Card className="glass-panel border-border/70">
        <CardHeader className="pb-3">
          <CardTitle className="text-base flex items-center gap-2">
            <ShieldAlert className="size-4 text-primary" />
            {t('backupHealth.title')}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <InlineAlert tone="warning">
            {error ?? t("common.noData")}
          </InlineAlert>
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
    <div className="space-y-5">
      {/* Mini stat cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <MiniStat
          label={t('backupHealth.neverBackedUp')}
          value={summary.neverBackedUp}
          tone={summary.neverBackedUp > 0 ? "destructive" : "success"}
          icon={<ServerOff className="size-5" />}
        />
        <MiniStat
          label={t('backupHealth.stale48h')}
          value={summary.stale48h}
          tone={summary.stale48h > 0 ? "warning" : "success"}
          icon={<History className="size-5" />}
        />
        <MiniStat
          label={t('backupHealth.policiesHealthy')}
          value={`${summary.policiesHealthy}/${summary.policiesHealthy + summary.policiesDegraded}`}
          tone={summary.policiesDegraded > 0 ? "warning" : "success"}
          icon={<CheckCircle2 className="size-5" />}
        />
        <MiniStat
          label={t('backupHealth.successRate7d')}
          value={`${Math.round(summary.successRate7d)}%`}
          tone={summary.successRate7d >= 95 ? "success" : summary.successRate7d >= 80 ? "warning" : "destructive"}
          icon={<Activity className="size-5" />}
        />
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-3 gap-5">
        {/* 7-day trend chart */}
        <Card className="xl:col-span-2 glass-panel border-border/70 flex flex-col min-h-[300px]">
          <CardHeader className="pb-3 shrink-0 border-b border-border/40">
            <CardTitle className="text-base flex items-center gap-2">
              <TrendingUp className="size-4 text-primary" />
              {t('backupHealth.trend7d')}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex-1 p-0 pt-4 px-2 min-h-[220px]" role="img" aria-label={t('backupHealth.trendAriaLabel')}>
            {healthTrend.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <ComposedChart data={healthTrend} margin={{ top: 10, right: 10, left: -20, bottom: 0 }}>
                  <CartesianGrid strokeDasharray="4 4" stroke="hsl(var(--border))" strokeOpacity={0.4} vertical={false} />
                  <XAxis
                    dataKey="date"
                    tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))", opacity: 0.8 }}
                    stroke="transparent"
                    tickLine={false}
                    minTickGap={20}
                    dy={5}
                  />
                  <YAxis
                    tick={{ fontSize: 10, fill: "hsl(var(--muted-foreground))", opacity: 0.8 }}
                    stroke="transparent"
                    tickLine={false}
                    axisLine={false}
                  />
                  <Tooltip
                    contentStyle={{
                      backgroundColor: "hsl(var(--card))",
                      border: "1px solid hsl(var(--border))",
                      fontSize: 12,
                      borderRadius: 8,
                      boxShadow: "0 4px 12px rgba(0,0,0,0.1)",
                    }}
                    labelStyle={{ color: "hsl(var(--muted-foreground))", fontWeight: 600, marginBottom: 4 }}
                  />
                  <Bar dataKey="total" name={t('backupHealth.totalRuns')} fill="hsl(var(--chart-egress))" opacity={0.3} maxBarSize={32} radius={[4, 4, 0, 0]} isAnimationActive={false} />
                  <Area
                    type="monotone"
                    dataKey="rate"
                    name={t('backupHealth.successRatePercent')}
                    stroke="hsl(var(--chart-ingress))"
                    strokeWidth={3}
                    fill={`url(#${gradientId})`}
                    dot={{ fill: "hsl(var(--chart-ingress))", r: 4, strokeWidth: 0, opacity: 0.8 }}
                    activeDot={{ r: 6, strokeWidth: 0 }}
                    isAnimationActive={false}
                  />
                  <defs>
                    <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="hsl(var(--chart-ingress))" stopOpacity={0.2} />
                      <stop offset="95%" stopColor="hsl(var(--chart-ingress))" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                </ComposedChart>
              </ResponsiveContainer>
            ) : (
              <div className="h-full w-full flex items-center justify-center text-sm text-muted-foreground">
                {t('common.noData')}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Problems table */}
        <Card className="xl:col-span-1 glass-panel border-border/70 flex flex-col min-h-[300px]">
          <CardHeader className="pb-3 shrink-0 border-b border-border/40">
            <CardTitle className="text-base flex items-center gap-2">
              <ShieldAlert className="size-4 text-primary" />
              {t('backupHealth.problemsTitle', { count: problems.length })}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex-1 overflow-y-auto thin-scrollbar p-3 space-y-2">
            {problems.length > 0 ? (
              problems.map((p) => (
                <div
                  key={p.key}
                  className={`glass-panel overflow-hidden relative group p-3 transition-colors hover:bg-muted/10`}
                >
                  <div className={`absolute top-0 left-0 w-1 h-full ${p.severity === "critical" ? "bg-destructive" : "bg-warning"} opacity-60 group-hover:opacity-100 transition-opacity`} />
                  <div className="flex items-center gap-3 pl-2">
                    <div className={`flex items-center justify-center rounded-lg p-2.5 shrink-0 ${p.severity === "critical" ? "bg-destructive/10 text-destructive" : "bg-warning/10 text-warning"}`}>
                      <AlertTriangle className="size-4" />
                    </div>
                    <div className="flex flex-col gap-0.5 min-w-0 text-sm">
                      <span className="font-medium truncate text-foreground/90">{p.label}</span>
                      <span className="text-xs text-muted-foreground line-clamp-2">{p.detail}</span>
                    </div>
                  </div>
                </div>
              ))
            ) : (
              <div className="h-full min-h-[160px] flex flex-col items-center justify-center text-center gap-3 text-muted-foreground p-6">
                <div className="rounded-full bg-success/10 p-3">
                  <CheckCircle2 className="size-8 text-success" />
                </div>
                <div className="text-sm">
                  <p className="font-medium text-success">{t('backupHealth.allHealthy')}</p>
                </div>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function MiniStat({ label, value, tone, icon }: { label: string; value: string | number; tone: "success" | "warning" | "destructive"; icon: React.ReactNode }) {
  const toneMap = {
    success: { text: "text-success", bg: "bg-success/10", border: "border-success/20", line: "bg-success" },
    warning: { text: "text-warning", bg: "bg-warning/10", border: "border-warning/20", line: "bg-warning" },
    destructive: { text: "text-destructive", bg: "bg-destructive/10", border: "border-destructive/20", line: "bg-destructive" },
  };
  const s = toneMap[tone];

  return (
    <Card className={`glass-panel border-border/70 overflow-hidden relative group`}>
      <div className={`absolute top-0 left-0 w-1 h-full ${s.line} opacity-60 group-hover:opacity-100 transition-opacity`} />
      <CardContent className="p-4 flex items-center gap-3 pl-5">
        <div className={`flex items-center justify-center rounded-lg p-2.5 ${s.bg} ${s.text}`}>
          {icon}
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-xl font-bold font-mono tracking-tight text-foreground/90">{value}</div>
          <div className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider truncate" title={label}>{label}</div>
        </div>
      </CardContent>
    </Card>
  );
}
