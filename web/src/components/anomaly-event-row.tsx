import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import type { AnomalyEvent } from "@/types/domain";

type Props = {
  event: AnomalyEvent;
  showNode?: boolean;
  nodeName?: string;
};

export default function AnomalyEventRow({ event, showNode, nodeName }: Props) {
  const { t } = useTranslation();

  const severityClass =
    event.severity === "critical"
      ? "bg-destructive/10 text-destructive"
      : "bg-amber-500/10 text-amber-600 dark:text-amber-400";

  const detectorLabel =
    event.detector === "ewma"
      ? t("anomaly.detector.ewma")
      : t("anomaly.detector.disk_forecast");

  const extra = (() => {
    if (event.detector === "ewma" && event.sigma != null) {
      return `${event.sigma.toFixed(2)}${t("anomaly.extra.sigmaSuffix")}`;
    }
    if (event.detector === "disk_forecast" && event.forecast_days != null) {
      return t("anomaly.extra.forecastPrefix", {
        days: event.forecast_days.toFixed(1),
      });
    }
    return "-";
  })();

  return (
    <tr
      data-testid={`anomaly-event-row-${event.id}`}
      className="border-t border-border hover:bg-muted/40 text-sm"
    >
      <td className="px-3 py-2 text-muted-foreground whitespace-nowrap">
        {new Date(event.fired_at).toLocaleString()}
      </td>

      {showNode && (
        <td className="px-3 py-2">
          <span className="text-xs">{nodeName ?? String(event.node_id)}</span>
        </td>
      )}

      <td className="px-3 py-2">
        <span className="rounded bg-muted px-1.5 py-0.5 text-xs font-medium">
          {detectorLabel}
        </span>
      </td>

      <td className="px-3 py-2 text-xs">{event.metric}</td>

      <td className="px-3 py-2">
        <span
          className={`rounded px-1.5 py-0.5 text-xs font-medium ${severityClass}`}
        >
          {t(`anomaly.severity.${event.severity}`)}
        </span>
      </td>

      <td className="px-3 py-2 text-xs whitespace-nowrap">
        {event.baseline_value.toFixed(2)} → {event.observed_value.toFixed(2)}
      </td>

      <td className="px-3 py-2 text-xs text-muted-foreground">{extra}</td>

      <td className="px-3 py-2 text-xs">
        {event.alert_id != null ? (
          <Link
            to={`/app/notifications?alert=${event.alert_id}`}
            data-testid={`anomaly-alert-link-${event.id}`}
            className="text-primary hover:underline whitespace-nowrap"
          >
            #{event.alert_id}
          </Link>
        ) : (
          <span className="text-muted-foreground">-</span>
        )}
      </td>
    </tr>
  );
}
