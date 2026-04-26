import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/context/auth-context";
import { useNodesContext } from "@/context/nodes-context";
import { getAlertLogs } from "@/lib/api/node-logs";
import type { AlertLogsResult, NodeLogEntry, NodeLogPriority } from "@/types/domain";
import { toast } from "@/components/ui/toast";
import { getErrorMessage } from "@/lib/utils";

const PRIORITY_COLORS: Record<NodeLogPriority, string> = {
  emerg: "bg-destructive text-destructive-foreground",
  alert: "bg-destructive/80 text-destructive-foreground",
  crit: "bg-destructive/70 text-destructive-foreground",
  err: "bg-destructive/60 text-destructive-foreground",
  warning: "bg-warning text-warning-foreground",
  notice: "bg-info/70 text-info-foreground",
  info: "bg-info text-info-foreground",
  debug: "bg-muted text-muted-foreground",
  "": "bg-muted text-muted-foreground",
};

export function AlertLogsPanel() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { nodes } = useNodesContext();
  const [searchParams] = useSearchParams();

  const alertIdParam = searchParams.get("alert_id");
  const alertId = alertIdParam ? parseInt(alertIdParam, 10) : NaN;

  const [result, setResult] = useState<AlertLogsResult | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!Number.isFinite(alertId) || alertId <= 0 || !token) {
      return undefined;
    }
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setLoading(true);
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setResult(null);
    getAlertLogs(token, alertId)
      .then((data) => { if (!controller.signal.aborted) setResult(data); })
      .catch((err) => { if (!controller.signal.aborted) toast.error(getErrorMessage(err)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => { controller.abort(); };
  }, [alertId, token]);

  if (!Number.isFinite(alertId) || alertId <= 0) {
    return (
      <div className="rounded-lg border border-border bg-card p-8 text-center text-sm text-muted-foreground">
        {t("nodeLogs.alertPlatformHint")}
      </div>
    );
  }

  if (loading) {
    return (
      <div className="rounded-lg border border-border bg-card p-8 text-center text-sm text-muted-foreground">
        {t("nodeLogs.loading")}
      </div>
    );
  }

  if (!result) {
    return null;
  }

  const isPlatformAlert = result.node_id === 0;

  if (isPlatformAlert) {
    return (
      <div className="rounded-lg border border-border bg-card p-6 space-y-2">
        <p className="text-sm text-muted-foreground">{result.hint ?? t("nodeLogs.alertPlatformHint")}</p>
      </div>
    );
  }

  const nodeName = nodes.find((n) => n.id === result.node_id)?.name ?? String(result.node_id);

  return (
    <div className="space-y-3">
      <div className="rounded-lg border border-border bg-card p-3">
        <p className="text-sm font-medium text-foreground">
          {t("nodeLogs.alertHeader", { id: alertId, node: nodeName })}
        </p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          {result.window_start} — {result.window_end}
        </p>
      </div>

      {result.data.length === 0 ? (
        <div className="rounded-lg border border-border bg-card p-8 text-center text-sm text-muted-foreground">
          {t("nodeLogs.empty")}
        </div>
      ) : (
        <AlertLogsTable entries={result.data} nodeName={nodeName} />
      )}
    </div>
  );
}

function AlertLogsTable({
  entries,
  nodeName,
}: {
  entries: NodeLogEntry[];
  nodeName: string;
}) {
  const { t } = useTranslation();

  return (
    <div className="rounded-lg border border-border bg-card overflow-x-auto">
      <table className="min-w-[900px] w-full text-left text-xs">
        <thead>
          <tr className="border-b border-border bg-muted/35 text-mini uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-3 py-2.5 w-[160px]">{t("nodeLogs.columns.time")}</th>
            <th scope="col" className="px-3 py-2.5 w-[110px]">{t("nodeLogs.columns.node")}</th>
            <th scope="col" className="px-3 py-2.5 w-[160px]">{t("nodeLogs.columns.path")}</th>
            <th scope="col" className="px-3 py-2.5 w-[90px]">{t("nodeLogs.columns.priority")}</th>
            <th scope="col" className="px-3 py-2.5">{t("nodeLogs.columns.message")}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((entry) => {
            const localTime = (() => {
              try {
                return new Date(entry.timestamp).toLocaleString();
              } catch {
                return entry.timestamp;
              }
            })();
            const pathShort = entry.path.length > 28
              ? "…" + entry.path.slice(-26)
              : entry.path;
            const priorityClass = PRIORITY_COLORS[entry.priority] ?? PRIORITY_COLORS[""];
            return (
              <tr key={entry.id} className="border-b border-border/60 hover:bg-muted/30 transition-colors">
                <td className="px-3 py-2 tabular-nums text-muted-foreground whitespace-nowrap">{localTime}</td>
                <td className="px-3 py-2 font-medium">{nodeName}</td>
                <td className="px-3 py-2">
                  <span title={entry.path} className="text-muted-foreground">{pathShort}</span>
                </td>
                <td className="px-3 py-2">
                  {entry.priority ? (
                    <span className={`inline-block rounded px-1.5 py-0.5 text-micro font-medium ${priorityClass}`}>
                      {entry.priority}
                    </span>
                  ) : null}
                </td>
                <td className="px-3 py-2 font-mono text-mini break-all">{entry.message}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
