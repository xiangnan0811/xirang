import React, { Suspense, useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { BarChart3, Plus, RefreshCw } from "lucide-react";
import { useAuth } from "@/context/auth-context";
import { createReportsApi, type ReportConfig } from "@/lib/api/reports-api";
import { getErrorMessage } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { PageHero } from "@/components/ui/page-hero";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { ConfigCard } from "@/pages/reports-page.config";

const ReportConfigDialog = React.lazy(() =>
  import("@/components/report-config-dialog").then(m => ({ default: m.ReportConfigDialog }))
);

const reportsApi = createReportsApi();

function ConfigGridSkeleton() {
  return (
    <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
      {[0, 1, 2].map((i) => (
        <div key={i} className="rounded-xl border border-border bg-card p-4 space-y-3">
          <Skeleton className="h-5 w-2/3 rounded" />
          <Skeleton className="h-3.5 w-full rounded" />
          <Skeleton className="h-3.5 w-1/2 rounded" />
        </div>
      ))}
    </div>
  );
}

export function ReportsPage() {
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
    const ok = await confirm({
      title: t("reports.deleteConfirm"),
      description: t("reports.deleteConfirmDesc"),
    });
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
      toast.error(t("reports.generateFailed") + ": " + getErrorMessage(err));
    }
  };

  const openCreate = () => {
    setEditingConfig(null);
    setDialogOpen(true);
  };

  return (
    <div className="space-y-6 p-4">
      {confirmDialog}

      <PageHero
        title={t("reports.pageTitle")}
        subtitle={t("reports.pageSubtitle", { count: configs.length })}
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              shape="pill"
              onClick={() => void loadConfigs()}
              disabled={loading}
            >
              <RefreshCw className={`mr-1.5 size-4 ${loading ? "animate-spin" : ""}`} />
              {t("common.refresh")}
            </Button>
            {isAdmin && (
              <Button shape="pill" onClick={openCreate}>
                <Plus className="mr-1.5 size-4" aria-hidden />
                {t("reports.addConfig")}
              </Button>
            )}
          </div>
        }
      />

      {loading ? (
        <ConfigGridSkeleton />
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
              onEdit={(c) => {
                setEditingConfig(c);
                setDialogOpen(true);
              }}
              onDelete={(id) => void handleDelete(id)}
              onGenerate={handleGenerate}
            />
          ))}
        </div>
      )}

      {isAdmin && (
        <Suspense fallback={null}>
          <ReportConfigDialog
            open={dialogOpen}
            onOpenChange={(v) => {
              setDialogOpen(v);
              if (!v) setEditingConfig(null);
            }}
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
    </div>
  );
}
