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
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { useAuth } from "@/context/auth-context.hooks";
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
    if (!token) {
      if (import.meta.env.VITE_ENABLE_DEMO_MODE === "true") {
        let cancelled = false;
        setLoading(true);
        setError(null);
        import("@/data/mock")
          .then((mocks) => {
            if (!cancelled) {
              setData(mocks.buildMockBackupHealth());
            }
          })
          .catch((err) => {
            if (!cancelled) {
              setError(getErrorMessage(err, t("backupHealth.loadFailed")));
            }
          })
          .finally(() => {
            if (!cancelled) {
              setLoading(false);
            }
          });
        return () => {
          cancelled = true;
        };
      }
      setData(null);
      setLoading(false);
      setError(null);
      return;
    }
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
          setError(getErrorMessage(err, t("backupHealth.loadFailed")));
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
      <DataSurface>
        <DataSurfaceHeader title={t("backupHealth.title")} />
        <DataSurfaceContent>
          <LoadingState title={t("backupHealth.loadingTitle")} rows={2} />
        </DataSurfaceContent>
      </DataSurface>
    );
  }

  if (error || !data) {
    return (
      <DataSurface>
        <DataSurfaceHeader
          title={
            <span className="inline-flex items-center gap-2">
              <ShieldAlert className="size-4 text-primary" aria-hidden />
              {t("backupHealth.title")}
            </span>
          }
        />
        <DataSurfaceContent>
          <InlineAlert tone="warning">{error ?? t("common.noData")}</InlineAlert>
        </DataSurfaceContent>
      </DataSurface>
    );
  }

  const { summary, staleNodes, degradedPolicies, healthTrend } = data;
  const policyTotal = summary.policiesHealthy + summary.policiesDegraded;

  const problems = [
    ...staleNodes.map((n) => ({
      key: `node-${n.nodeId}`,
      label: n.nodeName,
      detail: n.lastBackupAt
        ? t("backupHealth.hoursSinceBackup", { hours: Math.round(n.hoursSince) })
        : t("backupHealth.neverBackedUp"),
      severity: n.hoursSince > 72 || !n.lastBackupAt ? ("critical" as const) : ("warning" as const),
    })),
    ...degradedPolicies.map((p) => ({
      key: `policy-${p.policyId}`,
      label: p.policyName,
      detail: t("backupHealth.consecutiveFailures", { count: p.consecutiveFailures }),
      severity: "critical" as const,
    })),
  ];

  return (
    <div className="space-y-5">
      <StatCardsSection
        compact
        items={[
          {
            title: t("backupHealth.neverBackedUp"),
            value: summary.neverBackedUp,
            icon: <ServerOff className="size-4" aria-hidden />,
            tone: summary.neverBackedUp > 0 ? "destructive" : "success",
          },
          {
            title: t("backupHealth.stale48h"),
            value: summary.stale48h,
            icon: <History className="size-4" aria-hidden />,
            tone: summary.stale48h > 0 ? "warning" : "success",
          },
          {
            title: t("backupHealth.policiesHealthy"),
            value: `${summary.policiesHealthy}/${policyTotal}`,
            icon: <CheckCircle2 className="size-4" aria-hidden />,
            tone: summary.policiesDegraded > 0 ? "warning" : "success",
            description: t("backups.pageSubtitle", {
              count: policyTotal,
              healthy: summary.policiesHealthy,
            }),
          },
          {
            title: t("backupHealth.successRate7d"),
            value: Math.round(summary.successRate7d),
            unit: "%",
            icon: <Activity className="size-4" aria-hidden />,
            tone:
              summary.successRate7d >= 95 ? "success" : summary.successRate7d >= 80 ? "warning" : "destructive",
          },
        ]}
      />

      <div className="grid grid-cols-1 items-start gap-5 xl:grid-cols-3">
        <DataSurface className="xl:col-span-2 min-h-[300px]">
          <DataSurfaceHeader
            title={
              <span className="inline-flex items-center gap-2">
                <TrendingUp className="size-4 text-primary" aria-hidden />
                {t("backupHealth.trend7d")}
              </span>
            }
          />
          <DataSurfaceContent className="min-h-[220px] p-4 pt-2">
            <div className="h-[220px] w-full" role="img" aria-label={t("backupHealth.trendAriaLabel")}>
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
                        boxShadow: "var(--shadow-md)",
                      }}
                      labelStyle={{ color: "hsl(var(--muted-foreground))", fontWeight: 600, marginBottom: 4 }}
                    />
                    <Bar dataKey="total" name={t("backupHealth.totalRuns")} fill="hsl(var(--chart-egress))" opacity={0.3} maxBarSize={32} radius={[4, 4, 0, 0]} isAnimationActive={false} />
                    <Area
                      type="monotone"
                      dataKey="rate"
                      name={t("backupHealth.successRatePercent")}
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
                <div className="flex h-full w-full items-center justify-center text-sm text-muted-foreground">
                  {t("common.noData")}
                </div>
              )}
            </div>
          </DataSurfaceContent>
        </DataSurface>

        <DataSurface className="xl:col-span-1 min-h-[300px]">
          <DataSurfaceHeader
            title={
              <span className="inline-flex items-center gap-2">
                <ShieldAlert className="size-4 text-primary" aria-hidden />
                {t("backupHealth.problemsTitle", { count: problems.length })}
              </span>
            }
          />
          <DataSurfaceContent className="space-y-2 overflow-y-auto thin-scrollbar">
            {problems.length > 0 ? (
              problems.map((p) => (
                <div
                  key={p.key}
                  className="relative overflow-hidden rounded-lg border border-border bg-card p-3"
                >
                  <div
                    className={`absolute top-0 left-0 h-full w-1 ${p.severity === "critical" ? "bg-destructive" : "bg-warning"}`}
                  />
                  <div className="flex items-center gap-3 pl-2">
                    <div
                      className={`flex shrink-0 items-center justify-center rounded-lg p-2.5 ${
                        p.severity === "critical" ? "bg-destructive/10 text-destructive" : "bg-warning/10 text-warning"
                      }`}
                    >
                      <AlertTriangle className="size-4" aria-hidden />
                    </div>
                    <div className="flex min-w-0 flex-col gap-0.5 text-sm">
                      <span className="truncate font-medium text-foreground/90">{p.label}</span>
                      <span className="line-clamp-2 text-xs text-muted-foreground">{p.detail}</span>
                    </div>
                  </div>
                </div>
              ))
            ) : (
              <div className="flex min-h-[160px] flex-col items-center justify-center gap-3 p-6 text-center text-muted-foreground">
                <div className="rounded-full bg-success/10 p-3">
                  <CheckCircle2 className="size-8 text-success" aria-hidden />
                </div>
                <p className="text-sm font-medium text-success">{t("backupHealth.allHealthy")}</p>
              </div>
            )}
          </DataSurfaceContent>
        </DataSurface>
      </div>
    </div>
  );
}
