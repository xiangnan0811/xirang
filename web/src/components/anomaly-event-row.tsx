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
      : "bg-warning/10 text-warning-foreground dark:text-warning";

  const detectorLabel = t("anomaly.detector.snapshot_diff");
  const extra = event.sigma != null
    ? `${event.sigma.toFixed(2)}${t("anomaly.extra.sigmaSuffix")}`
    : "-";

  return (
    <tr
      data-testid={`anomaly-event-row-${event.id}`}
      className="border-t border-border hover:bg-muted/40 text-sm"
    >
      <td className="px-3 py-2 text-muted-foreground whitespace-nowrap">
        {new Date(event.firedAt).toLocaleString()}
      </td>

      {showNode && (
        <td className="px-3 py-2">
          <span className="text-xs">{nodeName ?? String(event.nodeId)}</span>
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
        {event.baselineValue.toFixed(2)} → {event.observedValue.toFixed(2)}
      </td>

      <td className="px-3 py-2 text-xs text-muted-foreground">{extra}</td>

      <td className="px-3 py-2 text-xs">
        {event.alertId != null ? (
          <Link
            to={`/app/notifications?alert=${event.alertId}`}
            data-testid={`anomaly-alert-link-${event.id}`}
            className="text-primary hover:underline whitespace-nowrap"
          >
            #{event.alertId}
          </Link>
        ) : (
          <span className="text-muted-foreground">-</span>
        )}
      </td>
    </tr>
  );
}
