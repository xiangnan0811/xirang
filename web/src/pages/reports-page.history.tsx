import { useState, useCallback } from "react";
import { useTranslation } from "react-i18next";
import { ChevronDown, ChevronRight, RefreshCw } from "lucide-react";
import { formatDateOnly } from "@/lib/api/core";
import { createReportsApi, type Report, type ReportConfig } from "@/lib/api/reports-api";
import { getErrorMessage } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";

const reportsApi = createReportsApi();

function formatDate(iso: string) {
  return formatDateOnly(iso);
}

function SuccessRateBadge({ rate }: { rate: number }) {
  const tone = rate >= 95 ? "success" : rate >= 80 ? "warning" : "destructive";
  return <Badge tone={tone}>{rate.toFixed(1)}%</Badge>;
}

function ReportRow({ report }: { report: Report }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);

  let topFailures: {
    node_name: string;
    task_name: string;
    count: number;
    last_err: string;
  }[] = [];
  try {
    topFailures = JSON.parse(report.top_failures) as typeof topFailures;
  } catch {
    /* ignore */
  }

  return (
    <div className="relative pl-8">
      {/* Timeline dot */}
      <span className="absolute left-0 top-3.5 flex size-3 items-center justify-center">
        <span className="size-2.5 rounded-full border-2 border-primary bg-background" />
      </span>

      <button
        type="button"
        className="flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left transition-colors hover:bg-muted/20"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {open ? (
          <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
        )}
        <span className="flex-1 text-sm">
          {formatDate(report.period_start)} — {formatDate(report.period_end)}
        </span>
        <SuccessRateBadge rate={report.success_rate} />
        <span className="ml-3 text-xs tabular-nums text-muted-foreground">
          {t("reports.successRuns", {
            success: report.success_runs,
            total: report.total_runs,
          })}
        </span>
        <span className="ml-3 text-xs tabular-nums text-muted-foreground">
          {t("reports.avgDuration", { ms: report.avg_duration_ms })}
        </span>
      </button>

      {open && (
        <div className="overflow-x-auto px-4 pb-4 pt-1 text-sm text-muted-foreground">
          {topFailures.length > 0 ? (
            <div>
              <p className="mb-2 font-medium text-foreground">
                {t("reports.topFailures", { count: topFailures.length })}
              </p>
              <table className="w-full text-xs">
                <thead>
                  <tr className="border-b border-border/40 text-left">
                    <th scope="col" className="pb-1.5 pr-4">{t("reports.colNode")}</th>
                    <th scope="col" className="pb-1.5 pr-4">{t("reports.colTask")}</th>
                    <th scope="col" className="pb-1.5 pr-4">{t("reports.colFailCount")}</th>
                    <th scope="col" className="pb-1.5">{t("reports.colLastError")}</th>
                  </tr>
                </thead>
                <tbody>
                  {topFailures.map((f, i) => (
                    <tr key={i} className="border-b border-border/20 last:border-0">
                      <td className="max-w-[120px] truncate py-1 pr-4" title={f.node_name}>{f.node_name}</td>
                      <td className="max-w-[120px] truncate py-1 pr-4" title={f.task_name}>{f.task_name}</td>
                      <td className="py-1 pr-4 tabular-nums">{f.count}</td>
                      <td className="max-w-xs truncate py-1">{f.last_err || "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p>{t("reports.noFailures")}</p>
          )}
        </div>
      )}
    </div>
  );
}

function HistorySkeletonRows() {
  return (
    <div className="space-y-2 pl-8">
      {[0, 1, 2].map((i) => (
        <div key={i} className="flex items-center gap-3 px-3 py-2.5">
          <Skeleton className="h-4 w-4 rounded" />
          <Skeleton className="h-3.5 flex-1 rounded" />
          <Skeleton className="h-5 w-14 rounded-full" />
          <Skeleton className="h-3 w-20 rounded" />
        </div>
      ))}
    </div>
  );
}

export function ReportHistory({
  cfg,
  token,
}: {
  cfg: ReportConfig;
  token: string;
}) {
  const { t } = useTranslation();
  const [reports, setReports] = useState<Report[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [expanded, setExpanded] = useState(false);

  const loadReports = useCallback(async () => {
    setLoading(true);
    try {
      const data = await reportsApi.listReports(token, cfg.id);
      setReports(data);
    } catch (err) {
      toast.error(t("reports.loadFailed") + ": " + getErrorMessage(err));
      setReports([]);
    } finally {
      setLoading(false);
    }
  }, [token, cfg.id, t]);

  const handleExpand = () => {
    if (!expanded && reports === null) {
      void loadReports();
    }
    setExpanded((v) => !v);
  };

  const handleRefresh = (e: React.MouseEvent) => {
    e.stopPropagation();
    void loadReports();
  };

  return (
    <div className="mt-2">
      {/* Toggle button */}
      <button
        type="button"
        className="flex items-center gap-1.5 text-xs text-primary hover:underline"
        onClick={handleExpand}
        aria-expanded={expanded}
      >
        {expanded ? (
          <ChevronDown className="size-3.5" />
        ) : (
          <ChevronRight className="size-3.5" />
        )}
        {t("reports.historyReports")}
        {reports !== null && (
          <span className="text-muted-foreground">({reports.length})</span>
        )}
      </button>

      {expanded && (
        <div className="mt-3 overflow-hidden rounded-lg border border-border/50 bg-muted/10">
          {/* Timeline header */}
          <div className="flex items-center justify-between border-b border-border/30 px-4 py-2">
            <span className="text-xs font-medium text-muted-foreground">
              {t("reports.historyReports")}
            </span>
            <button
              type="button"
              className="rounded p-1 text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
              onClick={handleRefresh}
              disabled={loading}
              aria-label={t("common.refresh")}
            >
              <RefreshCw className={`size-3.5 ${loading ? "animate-spin" : ""}`} />
            </button>
          </div>

          {/* Timeline body */}
          <div className="relative py-2">
            {/* Vertical rail */}
            <div className="absolute bottom-2 left-3.5 top-2 w-px bg-border" aria-hidden />

            {loading ? (
              <HistorySkeletonRows />
            ) : !reports?.length ? (
              <p className="py-6 text-center text-sm text-muted-foreground">
                {t("reports.noReportsHint")}
              </p>
            ) : (
              <div className="space-y-0.5">
                {reports.map((r) => (
                  <ReportRow key={r.id} report={r} />
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
