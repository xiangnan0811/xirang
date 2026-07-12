import { useTranslation } from "react-i18next";
import { useDiskForecast } from "./use-disk-forecast";
import type { NodeDetailTabProps } from "./types";

export default function DiskForecastCard({ nodeId, token }: NodeDetailTabProps) {
  const { t } = useTranslation();
  const { data, isLoading, error } = useDiskForecast(nodeId, token);

  const confidenceCopy: Record<string, string> = {
    high: t("nodes.nodeDetail.confidenceHigh"),
    medium: t("nodes.nodeDetail.confidenceMedium"),
    low: t("nodes.nodeDetail.confidenceLow"),
    insufficient: t("nodes.nodeDetail.confidenceInsufficient"),
  };

  if (error) {
    return (
      <div data-testid="disk-forecast-card" className="rounded-md border border-border bg-card p-4">
        <div className="text-sm text-destructive" role="alert">
          {t("nodes.nodeDetail.diskForecastError")}
        </div>
      </div>
    );
  }

  if (!data || isLoading) {
    return (
      <div data-testid="disk-forecast-card" className="rounded-md border border-border bg-card p-4">
        <div className="text-sm text-muted-foreground">{t("nodes.nodeDetail.diskForecastLoading")}</div>
      </div>
    );
  }

  const { forecast, diskGbTotal, diskGbUsedNow, dailyGrowthGb } = data;
  const flatOrShrinking = dailyGrowthGb !== null && dailyGrowthGb <= 0;

  return (
    <div data-testid="disk-forecast-card" className="rounded-md border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h3 className="text-base font-medium">{t("nodes.nodeDetail.diskForecastTitle")}</h3>
        <span data-testid="confidence" className="text-xs text-muted-foreground">
          {confidenceCopy[forecast.confidence] ?? forecast.confidence}
        </span>
      </div>
      {diskGbTotal > 0 && (
        <div className="text-sm">
          {t("nodes.nodeDetail.diskForecastCurrent", {
            used: diskGbUsedNow.toFixed(1),
            total: diskGbTotal.toFixed(0),
          })}
        </div>
      )}
      {dailyGrowthGb !== null && !flatOrShrinking && (
        <div className="text-sm text-muted-foreground">
          {t("nodes.nodeDetail.diskForecastDailyGrowth", { gb: dailyGrowthGb.toFixed(2) })}
        </div>
      )}
      {flatOrShrinking && (
        <div className="text-sm text-muted-foreground">{t("nodes.nodeDetail.diskForecastFlat")}</div>
      )}
      {forecast.daysToFull !== null && forecast.daysToFull > 0 && (
        <div className="text-sm">
          {t("nodes.nodeDetail.diskForecastDaysToFull", {
            days: Math.round(forecast.daysToFull),
          })}
          {forecast.dateFull && (
            <span className="text-muted-foreground">（{forecast.dateFull}）</span>
          )}
        </div>
      )}
    </div>
  );
}
