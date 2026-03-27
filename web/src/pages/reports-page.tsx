import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  BarChart3,
  ChevronDown,
  ChevronRight,
  Plus,
  RefreshCw,
  Trash2,
  Zap,
} from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { formatDateOnly } from "@/lib/api/core";
import {
  createReportsApi,
  type Report,
  type ReportConfig,
} from "@/lib/api/reports-api";
import { getErrorMessage } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { toast } from "@/components/ui/toast";
import { ReportConfigDialog } from "@/components/report-config-dialog";

const reportsApi = createReportsApi();

function formatDate(iso: string) {
  return formatDateOnly(iso);
}

function SuccessRateBadge({ rate }: { rate: number }) {
  const variant = rate >= 95 ? "success" : rate >= 80 ? "warning" : "danger";
  return <Badge variant={variant}>{rate.toFixed(1)}%</Badge>;
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
    <div className="border-b border-border/40 last:border-0">
      <button
        type="button"
        className="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/20"
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
        <div className="px-6 pb-4 pt-1 text-sm text-muted-foreground">
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
                    <th scope="col" className="pb-1.5 pr-4">
                      {t("reports.colFailCount")}
                    </th>
                    <th scope="col" className="pb-1.5">{t("reports.colLastError")}</th>
                  </tr>
                </thead>
                <tbody>
                  {topFailures.map((f, i) => (
                    <tr
                      key={i}
                      className="border-b border-border/20 last:border-0"
                    >
                      <td className="py-1 pr-4">{f.node_name}</td>
                      <td className="py-1 pr-4">{f.task_name}</td>
                      <td className="py-1 pr-4 tabular-nums">{f.count}</td>
                      <td className="max-w-xs truncate py-1">
                        {f.last_err || "—"}
                      </td>
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

function ConfigCard({
  cfg,
  isAdmin,
  token,
  onDelete,
  onGenerate,
}: {
  cfg: ReportConfig;
  isAdmin: boolean;
  token: string;
  onDelete: (id: number) => void;
  onGenerate: (id: number) => void;
}) {
  const { t } = useTranslation();
  const [reports, setReports] = useState<Report[] | null>(null);
  const [loadingReports, setLoadingReports] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [generating, setGenerating] = useState(false);

  const loadReports = useCallback(async () => {
    setLoadingReports(true);
    try {
      const data = await reportsApi.listReports(token, cfg.id);
      setReports(data);
    } catch (err) {
      toast.error(t("reports.loadFailed") + ": " + getErrorMessage(err));
      setReports([]);
    } finally {
      setLoadingReports(false);
    }
  }, [token, cfg.id, t]);

  const handleExpand = () => {
    if (!expanded && reports === null) {
      void loadReports();
    }
    setExpanded((v) => !v);
  };

  const handleGenerate = async () => {
    setGenerating(true);
    try {
      await onGenerate(cfg.id);
      setExpanded(true);
      void loadReports();
    } finally {
      setGenerating(false);
    }
  };

  const scopeLabel =
    cfg.scope_type === "all"
      ? t("reports.scopeLabels.all")
      : cfg.scope_type === "tag"
        ? t("reports.scopeTagValue", { value: cfg.scope_value })
        : t("reports.scopeLabels.node_ids");

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-2 pb-2">
        <div>
          <p className="font-semibold">{cfg.name}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {cfg.period === "weekly"
              ? t("reports.periodLabels.weekly")
              : t("reports.periodLabels.monthly")}{" "}
            · {scopeLabel} · {cfg.cron}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Badge variant={cfg.enabled ? "success" : "outline"}>
            {cfg.enabled ? t("common.enabled") : t("common.disabled")}
          </Badge>
          {isAdmin && (
            <>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-muted-foreground hover:text-foreground"
                title={t("reports.generateNow")}
                disabled={generating}
                onClick={() => void handleGenerate()}
              >
                {generating ? (
                  <RefreshCw className="size-4 animate-spin" />
                ) : (
                  <Zap className="size-4" />
                )}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-destructive/80 hover:text-destructive"
                title={t("reports.deleteConfig")}
                onClick={() => onDelete(cfg.id)}
              >
                <Trash2 className="size-4" />
              </Button>
            </>
          )}
        </div>
      </CardHeader>

      <CardContent className="pt-0">
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
        </button>

        {expanded && (
          <div className="mt-2 rounded-lg border border-border/50 bg-muted/10">
            {loadingReports ? (
              <div className="flex items-center justify-center py-6 text-sm text-muted-foreground">
                {t("common.loading")}
              </div>
            ) : !reports?.length ? (
              <div className="flex items-center justify-center py-6 text-sm text-muted-foreground">
                {t("reports.noReportsHint")}
              </div>
            ) : (
              reports.map((r) => <ReportRow key={r.id} report={r} />)
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function ReportsPage() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const isAdmin = role === "admin";
  const [configs, setConfigs] = useState<ReportConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [dialogOpen, setDialogOpen] = useState(false);

  const loadConfigs = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const data = await reportsApi.listConfigs(token);
      setConfigs(data);
    } catch (err) {
      toast.error(t("reports.loadFailed") + ": " + getErrorMessage(err));
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void loadConfigs();
  }, [loadConfigs]);

  const handleDelete = async (id: number) => {
    if (!token) return;
    try {
      await reportsApi.deleteConfig(token, id);
      toast.success(t("reports.deletedSuccess"));
      setConfigs((prev) => prev.filter((c) => c.id !== id));
    } catch (err) {
      toast.error(t("reports.deleteFailed") + ": " + getErrorMessage(err));
    }
  };

  const handleGenerate = async (id: number) => {
    if (!token) return;
    try {
      await reportsApi.generateNow(token, id);
      toast.success(t("reports.generatedSuccess"));
    } catch (err) {
      toast.error(
        t("reports.generateFailed") + ": " + getErrorMessage(err),
      );
    }
  };

  return (
    <div className="space-y-4 p-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">{t("reports.pageTitle")}</h1>
          <p className="mt-0.5 text-sm text-muted-foreground">
            {t("reports.pageDesc")}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void loadConfigs()}
            disabled={loading}
          >
            <RefreshCw
              className={`mr-1.5 size-4 ${loading ? "animate-spin" : ""}`}
            />
            {t("common.refresh")}
          </Button>
          {isAdmin && (
            <Button size="sm" onClick={() => setDialogOpen(true)}>
              <Plus className="mr-1.5 size-4" />
              {t("reports.addConfig")}
            </Button>
          )}
        </div>
      </div>

      {loading ? (
        <LoadingState title={t("reports.loading")} rows={3} />
      ) : configs.length === 0 ? (
        <EmptyState
          icon={BarChart3}
          title={t("reports.emptyTitle")}
          description={
            isAdmin
              ? t("reports.emptyDescAdmin")
              : t("reports.emptyDescViewer")
          }
        />
      ) : (
        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
          {configs.map((cfg) => (
            <ConfigCard
              key={cfg.id}
              cfg={cfg}
              isAdmin={isAdmin}
              token={token ?? ""}
              onDelete={(id) => void handleDelete(id)}
              onGenerate={handleGenerate}
            />
          ))}
        </div>
      )}

      {isAdmin && (
        <ReportConfigDialog
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          onCreated={(cfg) => setConfigs((prev) => [...prev, cfg])}
          token={token ?? ""}
        />
      )}
    </div>
  );
}
