import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { listNodeAnomalyEvents } from "@/lib/api/anomaly";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";
import AnomalyEventRow from "@/components/anomaly-event-row";
import type { AnomalyEvent } from "@/types/domain";

export default function AnomalyTab({ nodeId }: { nodeId: number }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [events, setEvents] = useState<AnomalyEvent[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!token || nodeId <= 0) return;
    let cancelled = false;
    listNodeAnomalyEvents(token, nodeId, { limit: 50 }).then(
      (data) => {
        if (!cancelled) {
          setEvents(data ?? []);
          setLoading(false);
        }
      },
      (err: unknown) => {
        if (!cancelled) {
          toast.error(getErrorMessage(err) || t("anomaly.errors.loadFailed"));
          setLoading(false);
        }
      },
    );
    return () => { cancelled = true; };
  }, [token, nodeId, t]);

  if (loading) {
    return (
      <div data-testid="anomaly-tab-loading" className="space-y-2 py-4">
        {[1, 2, 3].map((i) => (
          <div
            key={i}
            className="h-8 rounded bg-muted animate-pulse"
          />
        ))}
      </div>
    );
  }

  if (events.length === 0) {
    return (
      <p
        data-testid="anomaly-tab-empty"
        className="py-6 text-sm text-muted-foreground"
      >
        {t("anomaly.tab.empty")}
      </p>
    );
  }

  return (
    <div data-testid="anomaly-tab" className="overflow-x-auto rounded-md border border-border">
      <table className="min-w-full text-sm">
        <thead className="bg-muted">
          <tr>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.firedAt")}</th>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.detector")}</th>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.metric")}</th>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.severity")}</th>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.baselineObserved")}</th>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.extra")}</th>
            <th className="px-3 py-2 text-left font-medium">{t("anomaly.table.alert")}</th>
          </tr>
        </thead>
        <tbody>
          {events.map((event) => (
            <AnomalyEventRow key={event.id} event={event} />
          ))}
        </tbody>
      </table>
    </div>
  );
}
