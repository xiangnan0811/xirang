import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { apiClient } from "@/lib/api/client";
import type { AnomalyDetector, AnomalyEvent, EscalationEvent, EscalationPolicy } from "@/types/domain";

type AlertEscalationTimelineProps = {
  token: string;
  alertId: number;
};

function EscalationEventItem({
  event,
  integrationNameMap,
}: {
  event: EscalationEvent;
  integrationNameMap: Map<number, string>;
}) {
  const { t } = useTranslation();
  const levelLabel = t("escalation.timeline.levelLabel", { n: event.level_index + 1 });
  const firedAt = new Date(event.fired_at).toLocaleString();
  const silenced = event.integration_ids.length === 0;

  return (
    <li className="rounded-md border border-border bg-card px-3 py-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">{levelLabel}</span>
        <span className="text-xs text-muted-foreground">{firedAt}</span>
        {silenced && (
          <Badge tone="neutral">{t("escalation.timeline.silencedSkip")}</Badge>
        )}
      </div>
      {!silenced && (
        <p className="mt-1 text-xs text-muted-foreground">
          {t("escalation.timeline.integrations")}:{" "}
          {event.integration_ids
            .map((id) => integrationNameMap.get(id) ?? String(id))
            .join(", ")}
        </p>
      )}
      {event.severity_before !== event.severity_after && (
        <p className="mt-0.5 text-xs">
          {t("escalation.timeline.severityChange", {
            before: event.severity_before,
            after: event.severity_after,
          })}
        </p>
      )}
      {event.tags_added.length > 0 && (
        <p className="mt-0.5 text-xs text-muted-foreground">
          {t("escalation.timeline.tagsAdded", {
            tags: event.tags_added.join(", "),
          })}
        </p>
      )}
    </li>
  );
}

export function AlertEscalationTimeline({
  token,
  alertId,
}: AlertEscalationTimelineProps) {
  const { t } = useTranslation();
  const [events, setEvents] = useState<EscalationEvent[]>([]);
  const [timelineLoading, setTimelineLoading] = useState(true);
  const [policies, setPolicies] = useState<EscalationPolicy[]>([]);

  // Load policies once to resolve integration names via policy levels
  useEffect(() => {
    let cancelled = false;
    apiClient.listEscalationPolicies(token).then(
      (list) => { if (!cancelled) setPolicies(list); },
      () => {},
    );
    return () => { cancelled = true; };
  }, [token]);

  useEffect(() => {
    let cancelled = false;
    apiClient.listAlertEscalationEvents(token, alertId).then(
      (evs) => {
        if (!cancelled) {
          setEvents(evs);
          setTimelineLoading(false);
        }
      },
      () => {
        if (!cancelled) setTimelineLoading(false);
      },
    );
    return () => { cancelled = true; };
  }, [token, alertId]);

  // Build integration id → name map from all policy levels
  const integrationNameMap = new Map<number, string>();
  for (const policy of policies) {
    for (const level of policy.levels) {
      for (const id of level.integration_ids) {
        if (!integrationNameMap.has(id)) {
          integrationNameMap.set(id, `#${id}`);
        }
      }
    }
  }

  return (
    <section className="mt-3 space-y-2">
      <h3 className="text-sm font-medium">{t("escalation.timeline.header")}</h3>
      {timelineLoading ? (
        <Skeleton className="h-10 w-full" />
      ) : events.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("escalation.timeline.empty")}</p>
      ) : (
        <ol className="space-y-2">
          {events.map((e) => (
            <EscalationEventItem
              key={e.id}
              event={e}
              integrationNameMap={integrationNameMap}
            />
          ))}
        </ol>
      )}
    </section>
  );
}

// ── Anomaly alert context ─────────────────────────────────────────────────────

type AnomalyAlertContextProps = {
  token: string;
  errorCode: string;
  nodeId: number;
};

function detectAnomalyDetector(errorCode: string): AnomalyDetector | null {
  if (errorCode.startsWith("XR-ANOMALY-")) return "ewma";
  if (errorCode.startsWith("XR-DISKFORECAST-")) return "disk_forecast";
  return null;
}

export function AnomalyAlertContext({
  token,
  errorCode,
  nodeId,
}: AnomalyAlertContextProps) {
  const { t } = useTranslation();
  const detector = detectAnomalyDetector(errorCode);
  const [event, setEvent] = useState<AnomalyEvent | null | undefined>(undefined);
  const [loading, setLoading] = useState(detector != null && nodeId > 0);

  useEffect(() => {
    if (!detector || !nodeId) return;
    let cancelled = false;
    apiClient.listAnomalyEvents(token, { node_id: nodeId, detector, page_size: 1 }).then(
      (res) => {
        if (!cancelled) {
          setEvent(res.data[0] ?? null);
          setLoading(false);
        }
      },
      () => {
        if (!cancelled) {
          setEvent(null);
          setLoading(false);
        }
      },
    );
    return () => { cancelled = true; };
  }, [token, detector, nodeId]);

  if (!detector) return null;

  const badge =
    detector === "ewma"
      ? t("anomaly.alertDetail.anomalyBadge")
      : t("anomaly.alertDetail.diskForecastBadge");

  return (
    <section
      data-testid="anomaly-alert-context"
      className="mt-3 rounded-md border border-border bg-muted/25 p-3 space-y-2"
    >
      <div className="flex items-center gap-2">
        <h3 className="text-sm font-medium">{t("anomaly.alertDetail.header")}</h3>
        <Badge tone="neutral">{badge}</Badge>
      </div>

      {loading ? (
        <Skeleton className="h-8 w-full" />
      ) : event == null ? (
        <p className="text-xs text-muted-foreground">{t("anomaly.alertDetail.noEvent")}</p>
      ) : (
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
          <dt className="text-muted-foreground">{t("anomaly.alertDetail.baseline")}</dt>
          <dd>{event.baseline_value.toFixed(2)}</dd>

          <dt className="text-muted-foreground">{t("anomaly.alertDetail.observed")}</dt>
          <dd>{event.observed_value.toFixed(2)}</dd>

          {detector === "ewma" && event.sigma != null && (
            <>
              <dt className="text-muted-foreground">{t("anomaly.alertDetail.sigma")}</dt>
              <dd>{event.sigma.toFixed(2)}σ</dd>
            </>
          )}

          {detector === "disk_forecast" && event.forecast_days != null && (
            <>
              <dt className="text-muted-foreground">{t("anomaly.alertDetail.forecastDays")}</dt>
              <dd>{event.forecast_days.toFixed(1)}</dd>
            </>
          )}
        </dl>
      )}
    </section>
  );
}
