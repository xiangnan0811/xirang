import React, {
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
} from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  BarChart3,
  ChevronDown,
  ChevronRight,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  Zap,
} from "lucide-react";
import { useAuth } from "@/context/auth-context.hooks";
import { SLOPanel } from "./reports-page.slo";
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
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
} from "@/components/ui/data-surface";
import { EmptyState } from "@/components/ui/empty-state";
import { LoadingState } from "@/components/ui/loading-state";
import { PageHero } from "@/components/ui/page-hero";
import { toast } from "@/components/ui/toast-sonner";
import { useConfirm } from "@/hooks/use-confirm";
const ReportConfigDialog = React.lazy(() =>
  import("@/components/report-config-dialog").then(m => ({ default: m.ReportConfigDialog }))
);

const reportsApi = createReportsApi();
const REPORT_TABS = ["sla", "slo"] as const;
type ReportTab = (typeof REPORT_TABS)[number];

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
    <div className="border-b border-border/40 last:border-0">
      <button
        type="button"
        className="flex w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/20"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        {open ? (
          <ChevronDown className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
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
        <div className="overflow-x-auto px-6 pb-4 pt-1 text-sm text-muted-foreground">
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
                      <td className="max-w-[120px] truncate py-1 pr-4" title={f.node_name}>{f.node_name}</td>
                      <td className="max-w-[120px] truncate py-1 pr-4" title={f.task_name}>{f.task_name}</td>
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
  onEdit,
  onDelete,
  onGenerate,
}: {
  cfg: ReportConfig;
  isAdmin: boolean;
  token: string;
  onEdit: (cfg: ReportConfig) => void;
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
          <Badge tone={cfg.enabled ? "success" : "neutral"}>
            {cfg.enabled ? t("common.enabled") : t("common.disabled")}
          </Badge>
          {isAdmin && (
            <>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-muted-foreground hover:text-foreground"
                title={t("reports.generateNow")}
                aria-label={t("reports.generateNow")}
                disabled={generating}
                onClick={() => void handleGenerate()}
              >
                {generating ? (
                  <RefreshCw className="size-4 animate-spin" aria-hidden="true" />
                ) : (
                  <Zap className="size-4" aria-hidden="true" />
                )}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-muted-foreground hover:text-foreground"
                title={t("common.edit")}
                aria-label={t("common.edit")}
                onClick={() => onEdit(cfg)}
              >
                <Pencil className="size-4" aria-hidden="true" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 text-destructive/80 hover:text-destructive"
                title={t("reports.deleteConfig")}
                aria-label={t("reports.deleteConfig")}
                onClick={() => onDelete(cfg.id)}
              >
                <Trash2 className="size-4" aria-hidden="true" />
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
            <ChevronDown className="size-3.5" aria-hidden="true" />
          ) : (
            <ChevronRight className="size-3.5" aria-hidden="true" />
          )}
          {t("reports.historyReports")}
        </button>

        {expanded && (
          <div className="mt-2 overflow-hidden rounded-lg border border-border/50 bg-muted/10">
            {loadingReports ? (
              <LoadingState className="py-2" rows={2} title={t("common.loading")} />
            ) : !reports?.length ? (
              <EmptyState
                className="rounded-none border-0 py-6"
                title={t("reports.noReportsHint")}
              />
            ) : (
              reports.map((r) => <ReportRow key={r.id} report={r} />)
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function SLAContent() {
  const { t } = useTranslation();
  const { token, role } = useAuth();
  const isAdmin = role === "admin";
  const { confirm, dialog: confirmDialog } = useConfirm();
  const [configs, setConfigs] = useState<ReportConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingConfig, setEditingConfig] = useState<ReportConfig | null>(null);

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
    const ok = await confirm({ title: t("reports.deleteConfirm"), description: t("reports.deleteConfirmDesc") });
    if (!ok) return;
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
    <>
      {confirmDialog}
      <DataSurface>
        <DataSurfaceHeader
          title={t("reports.slaSurfaceTitle")}
          description={t("reports.slaSurfaceDesc", { total: configs.length })}
          actions={
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => void loadConfigs()}
                disabled={loading}
              >
                <RefreshCw
                  className={`mr-1.5 size-4 ${loading ? "animate-spin" : ""}`}
                  aria-hidden="true"
                />
                {t("common.refresh")}
              </Button>
              {isAdmin && (
                <Button size="sm" onClick={() => { setEditingConfig(null); setDialogOpen(true); }}>
                  <Plus className="mr-1.5 size-4" aria-hidden="true" />
                  {t("reports.addConfig")}
                </Button>
              )}
            </div>
          }
        />
        <DataSurfaceContent className="space-y-4">
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
                  onEdit={(c) => { setEditingConfig(c); setDialogOpen(true); }}
                  onDelete={(id) => void handleDelete(id)}
                  onGenerate={handleGenerate}
                />
              ))}
            </div>
          )}
        </DataSurfaceContent>
      </DataSurface>

      {isAdmin && (
        <Suspense fallback={null}>
          <ReportConfigDialog
            open={dialogOpen}
            onOpenChange={(v) => { setDialogOpen(v); if (!v) setEditingConfig(null); }}
            onSaved={(cfg) =>
              setConfigs((prev) =>
                prev.some((c) => c.id === cfg.id)
                  ? prev.map((c) => (c.id === cfg.id ? cfg : c))
                  : [...prev, cfg]
              )
            }
            token={token ?? ""}
            editingConfig={editingConfig}
          />
        </Suspense>
      )}
    </>
  );
}

export function ReportsPage() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const tabRefs = useRef<Partial<Record<ReportTab, HTMLButtonElement | null>>>({});
  const tab = params.get("tab") === "slo" ? "slo" : "sla";
  const setTab = useCallback(
    (next: ReportTab) => {
      const nextParams = new URLSearchParams(params);
      nextParams.set("tab", next);
      setParams(nextParams, { replace: true });
    },
    [params, setParams],
  );
  const handleTabKeyDown = useCallback(
    (event: KeyboardEvent<HTMLDivElement>) => {
      const currentIndex = REPORT_TABS.indexOf(tab);
      let nextIndex: number | null = null;

      if (event.key === "ArrowRight" || event.key === "ArrowDown") {
        nextIndex = (currentIndex + 1) % REPORT_TABS.length;
      } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
        nextIndex =
          (currentIndex - 1 + REPORT_TABS.length) % REPORT_TABS.length;
      } else if (event.key === "Home") {
        nextIndex = 0;
      } else if (event.key === "End") {
        nextIndex = REPORT_TABS.length - 1;
      }

      if (nextIndex === null) {
        return;
      }

      event.preventDefault();
      const nextTab = REPORT_TABS[nextIndex];
      setTab(nextTab);
      tabRefs.current[nextTab]?.focus();
    },
    [setTab, tab],
  );

  return (
    <div className="space-y-5 animate-fade-in">
      <PageHero
        title={t("reports.workbenchTitle")}
        subtitle={t("reports.workbenchDesc")}
        meta={
          <>
            <Badge tone={tab === "sla" ? "info" : "neutral"}>
              {t("slo.tabSLA")}
            </Badge>
            <Badge tone={tab === "slo" ? "info" : "neutral"}>
              {t("slo.tabSLO")}
            </Badge>
          </>
        }
        actions={
          <div
            className="inline-flex flex-wrap items-center gap-1 rounded-lg border border-border bg-background/70 p-1"
            role="tablist"
            aria-label={t("reports.tabListLabel")}
            tabIndex={-1}
            onKeyDown={handleTabKeyDown}
          >
            <Button
              ref={(node) => {
                tabRefs.current.sla = node;
              }}
              type="button"
              id="reports-tab-sla"
              role="tab"
              aria-controls="reports-panel-sla"
              aria-selected={tab === "sla"}
              tabIndex={tab === "sla" ? 0 : -1}
              variant={tab === "sla" ? "default" : "ghost"}
              size="sm"
              onClick={() => setTab("sla")}
            >
              {t("slo.tabSLA")}
            </Button>
            <Button
              ref={(node) => {
                tabRefs.current.slo = node;
              }}
              type="button"
              id="reports-tab-slo"
              role="tab"
              aria-controls="reports-panel-slo"
              aria-selected={tab === "slo"}
              tabIndex={tab === "slo" ? 0 : -1}
              variant={tab === "slo" ? "default" : "ghost"}
              size="sm"
              onClick={() => setTab("slo")}
            >
              {t("slo.tabSLO")}
            </Button>
          </div>
        }
      />

      {tab === "sla" ? (
        <section
          id="reports-panel-sla"
          role="tabpanel"
          aria-labelledby="reports-tab-sla"
        >
          <SLAContent />
        </section>
      ) : (
        <section
          id="reports-panel-slo"
          role="tabpanel"
          aria-labelledby="reports-tab-slo"
        >
          <SLOPanel />
        </section>
      )}
    </div>
  );
}
