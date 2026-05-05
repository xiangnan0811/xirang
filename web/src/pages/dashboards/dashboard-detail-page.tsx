import { useState, useEffect } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowLeft, RefreshCw, Plus, Save } from "lucide-react";
import { toast } from "sonner";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { ApiError } from "@/lib/api/core";
import type { Panel, DashboardTimeRange } from "@/types/domain";
import { getErrorMessage } from "@/lib/utils";
import { useDashboard } from "./hooks/use-dashboard";
import { PanelGrid, type LayoutItem } from "./panel-grid";
import { PanelCard } from "./panel-card";
import { PanelEditorDialog } from "./panel-editor-dialog";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

// ─── 时间范围 & 自动刷新选项 ─────────────────────────────────────

const TIME_RANGE_OPTIONS: { value: DashboardTimeRange; labelKey: string }[] = [
  { value: "1h", labelKey: "dashboards.timeRange.1h" },
  { value: "6h", labelKey: "dashboards.timeRange.6h" },
  { value: "24h", labelKey: "dashboards.timeRange.24h" },
  { value: "7d", labelKey: "dashboards.timeRange.7d" },
];

const AUTO_REFRESH_OPTIONS = [
  { value: "0", labelKey: "dashboards.autoRefresh.off" },
  { value: "10", labelKey: "dashboards.autoRefresh.10" },
  { value: "30", labelKey: "dashboards.autoRefresh.30" },
  { value: "60", labelKey: "dashboards.autoRefresh.60" },
  { value: "300", labelKey: "dashboards.autoRefresh.300" },
];

// ─── 页头骨架 ─────────────────────────────────────────────────────

function DetailPageSkeleton() {
  return (
    <div className="flex flex-col gap-4 p-6">
      <div className="flex items-center gap-3">
        <Skeleton className="h-8 w-8 rounded" />
        <Skeleton className="h-6 w-48" />
      </div>
      <Skeleton className="h-8 w-full" />
      <div className="mt-4 grid gap-4 grid-cols-2">
        <Skeleton className="h-48 rounded-lg" />
        <Skeleton className="h-48 rounded-lg" />
      </div>
    </div>
  );
}

// ─── 主页面 ──────────────────────────────────────────────────────

export function DashboardDetailPage() {
  const { id } = useParams<{ id: string }>();
  const { token } = useAuth();
  const { t } = useTranslation();
  const navigate = useNavigate();

  const {
    dashboard,
    start,
    end,
    loading,
    error,
    refreshNonce,
    refresh,
    setTimeRange,
    updateAutoRefresh,
  } = useDashboard(id, token ?? "");

  const [editMode, setEditMode] = useState(false);
  const [layoutDirty, setLayoutDirty] = useState(false);
  const [pendingLayout, setPendingLayout] = useState<LayoutItem[]>([]);
  const [savingLayout, setSavingLayout] = useState(false);

  // ── 面板编辑器状态 ──────────────────────────────────────────────
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingPanel, setEditingPanel] = useState<Panel | undefined>(undefined);

  // ── 404 处理 ────────────────────────────────────────────────────
  useEffect(() => {
    if (error) {
      const is404 = error instanceof ApiError && error.status === 404;
      toast.error(is404 ? t("dashboards.errors.notFound") : error.message);
      navigate("/app/dashboards");
    }
  }, [error, navigate, t]);

  if (error) return null;
  if (loading || !dashboard) {
    return <DetailPageSkeleton />;
  }

  // ── 布局变化 ─────────────────────────────────────────────────────
  function handleLayoutChange(items: LayoutItem[]) {
    if (!editMode) return;
    setPendingLayout(items);
    setLayoutDirty(true);
  }

  // ── 保存布局 ─────────────────────────────────────────────────────
  async function handleSaveLayout() {
    if (!dashboard) return;
    setSavingLayout(true);
    try {
      await apiClient.updateLayout(token ?? "", dashboard.id, pendingLayout);
      setLayoutDirty(false);
      toast.success(t("dashboards.panel.layoutSaved"));
    } catch (err) {
      toast.error(getErrorMessage(err));
    } finally {
      setSavingLayout(false);
    }
  }

  // ── 删除面板 ─────────────────────────────────────────────────────
  async function handleDeletePanel(panel: Panel) {
    if (!dashboard) return;
    try {
      await apiClient.deletePanel(token ?? "", dashboard.id, panel.id);
      toast.success(t("common.success"));
      refresh();
    } catch (err) {
      toast.error(getErrorMessage(err));
    }
  }

  // ── 编辑面板 ──────────────────────────────────────────────────────
  function handleEditPanel(panel: Panel) {
    setEditingPanel(panel);
    setEditorOpen(true);
  }

  // ── 添加面板 ──────────────────────────────────────────────────────
  function handleAddPanel() {
    // Auto-enable edit mode so follow-up drag/resize affordances appear.
    if (!editMode) setEditMode(true);
    setEditingPanel(undefined);
    setEditorOpen(true);
  }

  // ── 面板编辑器保存回调 ────────────────────────────────────────────
  function handlePanelSaved(_saved: Panel) {
    setEditorOpen(false);
    refresh();
  }

  const panels = dashboard.panels ?? [];

  return (
    <div className="flex flex-col gap-0 min-h-screen">
      {/* ── 页头 ──────────────────────────────────────────────── */}
      <div className="flex flex-col gap-1 border-b border-border px-6 py-4">
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="icon"
            className="size-7"
            onClick={() => navigate("/app/dashboards")}
            aria-label={t("common.back")}
          >
            <ArrowLeft className="size-4" />
          </Button>
          <div className="flex flex-col">
            <h1 className="text-base font-semibold leading-tight">{dashboard.name}</h1>
            {dashboard.description && (
              <p className="text-xs text-muted-foreground">{dashboard.description}</p>
            )}
          </div>
        </div>

        {/* ── 工具栏 ────────────────────────────────────────────── */}
        <div className="flex flex-wrap items-center gap-2 pt-2">
          {/* 时间范围 */}
          <Select
            value={dashboard.time_range}
            onChange={(e) => setTimeRange(e.target.value as DashboardTimeRange)}
            className="h-7 text-xs w-32"
            aria-label={t("dashboards.fields.timeRange")}
          >
            {TIME_RANGE_OPTIONS.map((r) => (
              <option key={r.value} value={r.value}>
                {t(r.labelKey)}
              </option>
            ))}
          </Select>

          {/* 自动刷新 */}
          <Select
            value={String(dashboard.auto_refresh_seconds)}
            onChange={(e) => updateAutoRefresh(Number(e.target.value))}
            className="h-7 text-xs w-24"
            aria-label={t("dashboards.fields.autoRefresh")}
          >
            {AUTO_REFRESH_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {t(o.labelKey)}
              </option>
            ))}
          </Select>

          {/* 手动刷新 */}
          <Button
            variant="outline"
            size="icon"
            className="size-7"
            onClick={refresh}
            aria-label={t("common.refresh")}
          >
            <RefreshCw className="size-3.5" />
          </Button>

          {/* 编辑模式切换 */}
          <Button
            variant={editMode ? "default" : "outline"}
            size="sm"
            className="h-7 text-xs"
            onClick={() => {
              setEditMode((v) => !v);
              if (editMode && layoutDirty) {
                // 退出编辑时放弃未保存布局
                setLayoutDirty(false);
              }
            }}
            aria-pressed={editMode}
          >
            {editMode
              ? t("dashboards.editToggle.on")
              : t("dashboards.editToggle.off")}
          </Button>

          {/* 保存布局（仅在 dirty 时显示） */}
          {editMode && layoutDirty && (
            <Button
              variant="default"
              size="sm"
              className="h-7 text-xs"
              onClick={handleSaveLayout}
              disabled={savingLayout}
            >
              <Save className="mr-1.5 size-3.5" />
              {savingLayout
                ? t("dashboards.panel.savingLayout")
                : t("common.save")}
            </Button>
          )}
        </div>
      </div>

      {/* ── 面板网格 ──────────────────────────────────────────────── */}
      <div className="flex-1 p-4">
        {panels.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-4 py-24 text-center">
            <p className="text-sm text-muted-foreground">
              {t("dashboards.panel.emptyState")}
            </p>
            {/* Always show on empty state so new dashboards are actionable
                without discovering the 只读/编辑中 toggle first. */}
            <Button size="sm" onClick={handleAddPanel}>
              <Plus className="mr-1.5 size-3.5" />
              {t("dashboards.panel.addButton")}
            </Button>
          </div>
        ) : (
          <PanelGrid
            panels={panels}
            editMode={editMode}
            onLayoutChange={handleLayoutChange}
          >
            {(panel) => (
              <PanelCard
                key={panel.id}
                panel={panel}
                start={start}
                end={end}
                token={token ?? ""}
                refreshNonce={refreshNonce}
                editMode={editMode}
                onEdit={handleEditPanel}
                onDelete={handleDeletePanel}
              />
            )}
          </PanelGrid>
        )}
      </div>

      {/* ── 编辑模式：固定右下角"添加面板"按钮 ────────────────────── */}
      {editMode && panels.length > 0 && (
        <div className="fixed bottom-6 right-6 z-10">
          <Button size="sm" onClick={handleAddPanel} className="shadow-lg">
            <Plus className="mr-1.5 size-3.5" />
            {t("dashboards.panel.addButton")}
          </Button>
        </div>
      )}

      {/* ── 面板编辑器对话框 ─────────────────────────────────────── */}
      <PanelEditorDialog
        open={editorOpen}
        onOpenChange={setEditorOpen}
        dashboardID={dashboard.id}
        start={start}
        end={end}
        panel={editingPanel}
        onSaved={handlePanelSaved}
        token={token ?? ""}
      />
    </div>
  );
}
