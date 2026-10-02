import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { InlineAlert } from "@/components/ui/inline-alert";
import { LoadingState } from "@/components/ui/loading-state";
import { usePersistentState } from "@/hooks/use-persistent-state";
import i18n from "@/i18n";
import { cn, getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryStats } from "@/types/domain";

type DeliveryStatsProps = {
  fetchAlertDeliveryStats: (hours: number) => Promise<AlertDeliveryStats>;
};

type StatsWindow = 24 | 72 | 168;

type DeliveryStatsState =
  | { hours: StatsWindow; status: "loading" }
  | { hours: StatsWindow; status: "error"; error: string }
  | { hours: StatsWindow; status: "success"; data: AlertDeliveryStats };

const collapsedStorageKey = "xirang.notifications.stats-collapsed";

const statsWindows: Array<{ label: string; value: StatsWindow }> = [
  { label: "24h", value: 24 },
  { label: "72h", value: 72 },
  { label: "7d", value: 168 },
];

export function DeliveryStatsCard({ fetchAlertDeliveryStats }: DeliveryStatsProps) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = usePersistentState(collapsedStorageKey, true);
  const [statsWindow, setStatsWindow] = useState<StatsWindow>(24);
  const [statsState, setStatsState] = useState<DeliveryStatsState>({ hours: 24, status: "loading" });
  const statsRequestRef = useRef(0);

  const loadDeliveryStats = useCallback((hours: StatsWindow) => {
    const currentRequestID = ++statsRequestRef.current;
    setStatsState({ hours, status: "loading" });
    void fetchAlertDeliveryStats(hours)
      .then((result) => {
        if (statsRequestRef.current !== currentRequestID) {
          return;
        }
        if (result.windowHours !== hours) {
          setStatsState({
            hours,
            status: "error",
            error: i18n.t("notifications.deliveryStatsLoadFailed"),
          });
          return;
        }
        setStatsState({ hours, status: "success", data: result });
      })
      .catch((error: unknown) => {
        if (statsRequestRef.current !== currentRequestID) {
          return;
        }
        setStatsState({
          hours,
          status: "error",
          error: getErrorMessage(error, i18n.t("notifications.deliveryStatsLoadFailed")),
        });
      });
  }, [fetchAlertDeliveryStats]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadDeliveryStats(statsWindow);
    return () => {
      statsRequestRef.current += 1;
    };
  }, [loadDeliveryStats, statsWindow]);

  const selectStatsWindow = (hours: StatsWindow) => {
    if (hours === statsWindow) {
      return;
    }
    statsRequestRef.current += 1;
    setStatsState({ hours, status: "loading" });
    setStatsWindow(hours);
  };

  const visibleState: DeliveryStatsState = statsState.hours === statsWindow
    ? statsState
    : { hours: statsWindow, status: "loading" };
  const isLoading = visibleState.status === "loading";
  const summaryText = visibleState.status === "success"
    ? t("notifications.statsSummary", {
        hours: visibleState.data.windowHours,
        total: visibleState.data.totalSent + visibleState.data.totalFailed,
        rate: visibleState.data.successRate,
      })
    : visibleState.status === "error"
      ? t("notifications.deliveryStatsLoadFailed")
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
              {statsWindows.map((item) => (
                <Button
                  key={`stats-window-${item.value}`}
                  size="sm"
                  variant={statsWindow === item.value ? "default" : "outline"}
                  onClick={() => selectStatsWindow(item.value)}
                >
                  {item.label}
                </Button>
              ))}
              <Button size="sm" variant="outline" onClick={() => loadDeliveryStats(statsWindow)} disabled={isLoading}>
                <RefreshCw className="mr-1 size-4" />
                {isLoading ? t("notifications.statsLoading") : t("common.refresh")}
              </Button>
            </div>
          )}
        </div>
      </CardHeader>
      {!collapsed && (
        <CardContent className="space-y-3">
          {visibleState.status === "loading" ? (
            <LoadingState
              title={t("notifications.statsLoadingTitle")}
              description={t("notifications.statsLoadingDesc")}
              rows={3}
            />
          ) : visibleState.status === "error" ? (
            <InlineAlert tone="warning" title={visibleState.error}>
              <Button size="sm" variant="outline" onClick={() => loadDeliveryStats(statsWindow)} disabled={isLoading}>
                {t("common.retry")}
              </Button>
            </InlineAlert>
          ) : (
            <>
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="rounded-xl border border-success/30 bg-success/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">{t("notifications.deliverySent")}</p>
                  <p className="mt-1 text-2xl font-semibold text-success">{visibleState.data.totalSent}</p>
                </div>
                <div className="rounded-xl border border-destructive/30 bg-destructive/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">{t("notifications.deliveryFailed")}</p>
                  <p className="mt-1 text-2xl font-semibold text-destructive">{visibleState.data.totalFailed}</p>
                </div>
                <div className="rounded-xl border border-info/30 bg-info/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">{t("notifications.successRate")}</p>
                  <p className="mt-1 text-2xl font-semibold text-info">{visibleState.data.successRate}%</p>
                </div>
              </div>

              {visibleState.data.byIntegration.length ? (
                <div className="grid gap-2 md:grid-cols-2">
                  {visibleState.data.byIntegration.map((item) => (
                    <div key={item.integrationId} className="rounded-xl border border-border bg-muted/20 p-3">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <p className="text-sm font-medium">{item.name}</p>
                        <Badge tone={item.failed > 0 ? "warning" : "success"}>{item.type}</Badge>
                      </div>
                      <div className="mt-2 grid grid-cols-3 gap-2 text-xs text-muted-foreground">
                        <p>{t("notifications.statsSent", { count: item.sent })}</p>
                        <p>{t("notifications.statsFailed", { count: item.failed })}</p>
                        <p className={cn(item.successRate >= 95 ? "text-success" : "text-warning")}>
                          {t("notifications.statsSuccessRate", { rate: item.successRate })}
                        </p>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">{t("notifications.noDeliveryInWindow")}</p>
              )}
            </>
          )}
        </CardContent>
      )}
    </Card>
  );
}
