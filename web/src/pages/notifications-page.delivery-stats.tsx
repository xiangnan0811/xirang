import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { LoadingState } from "@/components/ui/loading-state";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { toast } from "@/components/ui/toast";
import { cn, getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryStats } from "@/types/domain";

type DeliveryStatsProps = {
  fetchAlertDeliveryStats: (hours: number) => Promise<AlertDeliveryStats>;
};

const collapsedStorageKey = "xirang.notifications.stats-collapsed";

export function DeliveryStatsCard({ fetchAlertDeliveryStats }: DeliveryStatsProps) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = usePersistentState(collapsedStorageKey, true);
  const [statsWindow, setStatsWindow] = useState<24 | 72 | 168>(24);
  const [deliveryStats, setDeliveryStats] = useState<AlertDeliveryStats | null>(null);
  const [deliveryStatsLoading, setDeliveryStatsLoading] = useState(false);
  const statsLoadedKeyRef = useRef<string>("");
  const statsRequestRef = useRef(0);

  const loadDeliveryStats = useCallback((hours: 24 | 72 | 168, force = false) => {
    const queryKey = `hours:${hours}`;
    if (!force && statsLoadedKeyRef.current === queryKey) {
      return;
    }

    statsLoadedKeyRef.current = queryKey;
    const currentRequestID = statsRequestRef.current + 1;
    statsRequestRef.current = currentRequestID;

    setDeliveryStatsLoading(true);
    void fetchAlertDeliveryStats(hours)
      .then((result) => {
        if (statsRequestRef.current === currentRequestID) {
          setDeliveryStats(result);
        }
      })
      .catch((error) => {
        if (statsRequestRef.current === currentRequestID) {
          toast.error(getErrorMessage(error));
        }
      })
      .finally(() => {
        if (statsRequestRef.current === currentRequestID) {
          setDeliveryStatsLoading(false);
        }
      });
  }, [fetchAlertDeliveryStats]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadDeliveryStats(statsWindow);
  }, [loadDeliveryStats, statsWindow]);

  const summaryText = deliveryStats
    ? t("notifications.statsSummary", { hours: statsWindow, total: deliveryStats.totalSent + deliveryStats.totalFailed, rate: deliveryStats.successRate })
    : t("common.loading");

  return (
    <Card className="rounded-lg border border-border bg-card">
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <button
            type="button"
            className="flex items-center gap-2 text-left"
            onClick={() => setCollapsed((prev) => !prev)}
            aria-expanded={!collapsed}
          >
            {collapsed ? <ChevronRight className="size-4" /> : <ChevronDown className="size-4" />}
            <CardTitle className="text-base">{t("notifications.deliveryStatsTitle")}</CardTitle>
            {collapsed && (
              <span className="text-xs text-muted-foreground">{summaryText}</span>
            )}
          </button>
          {!collapsed && (
            <div className="flex flex-wrap items-center justify-end gap-2">
              {[{ label: "24h", value: 24 }, { label: "72h", value: 72 }, { label: "7d", value: 168 }].map((item) => (
                <Button
                  key={`stats-window-${item.value}`}
                  size="sm"
                  variant={statsWindow === item.value ? "default" : "outline"}
                  onClick={() => setStatsWindow(item.value as 24 | 72 | 168)}
                >
                  {item.label}
                </Button>
              ))}
              <Button size="sm" variant="outline" onClick={() => loadDeliveryStats(statsWindow, true)} disabled={deliveryStatsLoading}>
                <RefreshCw className="mr-1 size-4" />
                {deliveryStatsLoading ? t("notifications.statsLoading") : t("common.refresh")}
              </Button>
            </div>
          )}
        </div>
      </CardHeader>
      {!collapsed && (
        <CardContent className="space-y-4">
          {deliveryStatsLoading ? (
            <LoadingState
              title={t("notifications.statsLoadingTitle")}
              description={t("notifications.statsLoadingDesc")}
              rows={3}
            />
          ) : deliveryStats ? (
            <>
              {/* Summary row */}
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="rounded-xl border border-success/30 bg-success/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">{t("notifications.deliverySent")}</p>
                  <p className="mt-1 text-2xl font-semibold text-success">{deliveryStats.totalSent}</p>
                </div>
                <div className="rounded-xl border border-destructive/30 bg-destructive/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">{t("notifications.deliveryFailed")}</p>
                  <p className="mt-1 text-2xl font-semibold text-destructive">{deliveryStats.totalFailed}</p>
                </div>
                <div className="rounded-xl border border-info/30 bg-info/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">{t("notifications.successRate")}</p>
                  <p className="mt-1 text-2xl font-semibold text-info">{deliveryStats.successRate}%</p>
                </div>
              </div>

              {/* Horizontal bar rows per integration */}
              {deliveryStats.byIntegration.length ? (
                <div className="space-y-3">
                  {deliveryStats.byIntegration.map((item) => {
                    const total = item.sent + item.failed;
                    const sentPct = total > 0 ? Math.round((item.sent / total) * 100) : 0;
                    const failedPct = total > 0 ? 100 - sentPct : 0;
                    return (
                      <div key={item.integrationId} className="space-y-1.5">
                        {/* Label row */}
                        <div className="flex items-center justify-between gap-2">
                          <div className="flex items-center gap-2 min-w-0">
                            <span className="truncate text-sm font-medium">{item.name}</span>
                            <Badge tone={item.failed > 0 ? "warning" : "success"}>{item.type}</Badge>
                          </div>
                          <div className="flex shrink-0 gap-3 text-xs text-muted-foreground">
                            <span className="text-[color:var(--chart-ingress,theme(colors.green.500))]">
                              {t("notifications.statsSent", { count: item.sent })}
                            </span>
                            <span className="text-destructive">
                              {t("notifications.statsFailed", { count: item.failed })}
                            </span>
                            <span className={cn(item.successRate >= 95 ? "text-success" : "text-warning")}>
                              {t("notifications.statsSuccessRate", { rate: item.successRate })}
                            </span>
                          </div>
                        </div>
                        {/* Horizontal bar */}
                        <div className="h-2 w-full overflow-hidden rounded-full bg-muted/40">
                          {total > 0 ? (
                            <div className="flex h-full">
                              <div
                                className="h-full bg-[var(--chart-ingress,theme(colors.green.500))] transition-all"
                                style={{ width: `${sentPct}%` }}
                              />
                              {failedPct > 0 && (
                                <div
                                  className="h-full bg-destructive transition-all"
                                  style={{ width: `${failedPct}%` }}
                                />
                              )}
                            </div>
                          ) : (
                            <div className="h-full w-full bg-muted/60 rounded-full" />
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">{t("notifications.noDeliveryInWindow")}</p>
              )}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">{t("notifications.noStatsData")}</p>
          )}
        </CardContent>
      )}
    </Card>
  );
}
