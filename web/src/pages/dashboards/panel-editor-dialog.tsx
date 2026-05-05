import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogBody,
  DialogFooter,
  DialogCloseButton,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { getErrorMessage } from "@/lib/utils";
import { apiClient } from "@/lib/api/client";
import { createNodesApi } from "@/lib/api/nodes-api";
import { createTasksApi } from "@/lib/api/tasks-api";
import { PanelRenderer } from "./panel-renderer";
import type {
  Aggregation,
  ChartType,
  MetricDescriptor,
  Panel,
  PanelQueryResult,
} from "@/types/domain";
import type { PanelInput } from "@/lib/api/dashboards";

// ─── 内部常量 ────────────────────────────────────────────────────

const CHART_TYPES: ChartType[] = ["line", "area", "bar", "number", "table"];

// ─── Props ───────────────────────────────────────────────────────

export interface PanelEditorDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  dashboardID: number;
  start: string;
  end: string;
  panel?: Panel;
  onSaved: (panel: Panel) => void;
  token: string;
}

// ─── 简单多选节点/任务组件 ──────────────────────────────────────

interface MultiSelectItem {
  id: number;
  label: string;
}

interface MultiSelectProps {
  label: string;
  items: MultiSelectItem[];
  selected: number[];
  onChange: (ids: number[]) => void;
}

function MultiSelectCheckboxes({ label, items, selected, onChange }: MultiSelectProps) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-xs font-medium text-muted-foreground">{label}</label>
      <div className="max-h-32 overflow-y-auto rounded-md border border-input bg-card p-2 flex flex-col gap-1">
        {items.length === 0 && (
          <span className="text-xs text-muted-foreground px-1">—</span>
        )}
        {items.map((item) => (
          <label key={item.id} className="flex items-center gap-2 cursor-pointer text-sm">
            <input
              type="checkbox"
              checked={selected.includes(item.id)}
              onChange={(e) => {
                if (e.target.checked) {
                  onChange([...selected, item.id]);
                } else {
                  onChange(selected.filter((id) => id !== item.id));
                }
              }}
              className="rounded"
            />
            <span className="truncate">{item.label}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

// ─── 主对话框 ────────────────────────────────────────────────────

export function PanelEditorDialog({
  open,
  onOpenChange,
  dashboardID,
  start,
  end,
  panel,
  onSaved,
  token,
}: PanelEditorDialogProps) {
  const { t } = useTranslation();
  const isEdit = panel !== undefined;

  // ── 表单状态 ────────────────────────────────────────────────────
  const [title, setTitle] = useState("");
  const [chartType, setChartType] = useState<ChartType>("line");
  const [metricKey, setMetricKey] = useState("");
  const [aggregation, setAggregation] = useState<Aggregation>("avg");
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [selectedTaskIds, setSelectedTaskIds] = useState<number[]>([]);

  // ── 元数据 ──────────────────────────────────────────────────────
  const [metrics, setMetrics] = useState<MetricDescriptor[]>([]);
  const [nodes, setNodes] = useState<{ id: number; label: string }[]>([]);
  const [tasks, setTasks] = useState<{ id: number; label: string }[]>([]);
  const [metricsLoaded, setMetricsLoaded] = useState(false);

  // ── 预览状态 ────────────────────────────────────────────────────
  const [previewData, setPreviewData] = useState<PanelQueryResult | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const previewTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const previewAbortRef = useRef<AbortController | null>(null);

  // ── 保存状态 ────────────────────────────────────────────────────
  const [saving, setSaving] = useState(false);

  // Gate PanelRenderer until the dialog's layout has settled so recharts'
  // ResponsiveContainer reads real pixel dimensions instead of 0. Without
  // this the browser console warns
  //   "The width(-1) and height(-1) of chart should be greater than 0"
  // because Radix's enter animation transforms the dialog from scale(0.95),
  // and RC's first measurement catches the pre-transform state. Two RAFs
  // pushes the mount past the initial paint where the dialog is settled.
  const [chartReady, setChartReady] = useState(false);
  useEffect(() => {
    if (!open) {
      setChartReady(false);
      return;
    }
    const id1 = requestAnimationFrame(() => {
      const id2 = requestAnimationFrame(() => setChartReady(true));
      (setChartReady as unknown as { _raf?: number })._raf = id2;
    });
    return () => {
      cancelAnimationFrame(id1);
      const pending = (setChartReady as unknown as { _raf?: number })._raf;
      if (pending) cancelAnimationFrame(pending);
    };
  }, [open]);

  // ── 初始化：打开时加载元数据 + 回填编辑值 ─────────────────────
  // Depend on `panel?.id` too — without it, reopening the dialog for a
  // different panel while it was already open (e.g. quick switch from "new"
  // → "edit") kept stale form state.
  useEffect(() => {
    if (!open) return;

    // 重置
    if (isEdit && panel) {
      setTitle(panel.title);
      setChartType(panel.chart_type);
      setMetricKey(panel.metric);
      setAggregation(panel.aggregation);
      setSelectedNodeIds(panel.filters?.node_ids ?? []);
      setSelectedTaskIds(panel.filters?.task_ids ?? []);
    } else {
      setTitle("");
      setChartType("line");
      setMetricKey("");
      setAggregation("avg");
      setSelectedNodeIds([]);
      setSelectedTaskIds([]);
    }
    setPreviewData(null);
    setPreviewError(null);

    // 加载指标列表（只加载一次）
    if (!metricsLoaded) {
      apiClient.listMetrics(token)
        .then((list) => {
          setMetrics(list);
          setMetricsLoaded(true);
          // 创建模式：默认选第一个指标
          if (!isEdit && list.length > 0) {
            setMetricKey(list[0].key);
            setAggregation(list[0].default_aggregation as Aggregation);
          }
        })
        .catch((err) => {
          // Failing to load metrics leaves the editor unusable; surface it
          // rather than showing an empty dropdown with no explanation.
          toast.error(t("dashboards.editor.metricsLoadFailed", {
            defaultValue: "指标列表加载失败：{{msg}}",
            msg: getErrorMessage(err),
          }));
        });
    }

    // 加载节点和任务列表
    createNodesApi().getNodes(token)
      .then((list) => setNodes(list.map((n) => ({ id: n.id, label: n.name ?? String(n.id) }))))
      .catch((err) => {
        toast.error(t("dashboards.editor.nodesLoadFailed", {
          defaultValue: "节点列表加载失败：{{msg}}",
          msg: getErrorMessage(err),
        }));
      });

    createTasksApi().getTasks(token)
      .then((list) => setTasks(list.map((t) => ({ id: t.id, label: t.name ?? String(t.id) }))))
      .catch((err) => {
        toast.error(t("dashboards.editor.tasksLoadFailed", {
          defaultValue: "任务列表加载失败：{{msg}}",
          msg: getErrorMessage(err),
        }));
      });
  }, [open, panel?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  // ── 指标变化时更新聚合默认值 ─────────────────────────────────
  const currentMetric = metrics.find((m) => m.key === metricKey);

  function handleMetricChange(key: string) {
    setMetricKey(key);
    const m = metrics.find((md) => md.key === key);
    if (m) {
      setAggregation(m.default_aggregation as Aggregation);
    }
  }

  // ── 防抖预览 ─────────────────────────────────────────────────
  useEffect(() => {
    if (!open || !metricKey || !aggregation) return;

    // 清除上一个 timer 和 request
    if (previewTimerRef.current) clearTimeout(previewTimerRef.current);
    if (previewAbortRef.current) previewAbortRef.current.abort();

    previewTimerRef.current = setTimeout(() => {
      const ctrl = new AbortController();
      previewAbortRef.current = ctrl;
      setPreviewLoading(true);
      setPreviewError(null);

      apiClient.queryPanel(
        token,
        {
          metric: metricKey,
          filters: {
            node_ids: selectedNodeIds.length > 0 ? selectedNodeIds : undefined,
            task_ids: selectedTaskIds.length > 0 ? selectedTaskIds : undefined,
          },
          aggregation,
          start,
          end,
        },
        { signal: ctrl.signal },
      )
        .then((result) => {
          setPreviewData(result);
          setPreviewError(null);
        })
        .catch((err) => {
          if ((err as Error).name === "AbortError") return;
          setPreviewError(getErrorMessage(err));
          setPreviewData(null);
        })
        .finally(() => setPreviewLoading(false));
    }, 500);

    return () => {
      if (previewTimerRef.current) clearTimeout(previewTimerRef.current);
      if (previewAbortRef.current) {
        previewAbortRef.current.abort();
        previewAbortRef.current = null;
      }
    };
  }, [open, metricKey, aggregation, selectedNodeIds, selectedTaskIds, start, end, token]);

  // ── 验证 ─────────────────────────────────────────────────────
  const titleError =
    title.trim().length === 0
      ? t("dashboards.editor.validation.titleRequired")
      : title.trim().length > 100
      ? t("dashboards.editor.validation.titleRequired")
      : null;
  const metricError = !metricKey ? t("dashboards.editor.validation.metricRequired") : null;
  const isValid = !titleError && !metricError;

  // ── 确保聚合在当前指标允许范围内 ────────────────────────────
  // Keep UI and persisted state in sync: if the user switches to a metric
  // that doesn't support the current aggregation, fold the fallback back
  // into state so reads of `aggregation` elsewhere agree with what renders.
  //
  // IMPORTANT: only sync when we actually know what the metric supports
  // (currentMetric != undefined). During initial mount the metrics list
  // is still loading; running the sync then would clobber the value that
  // the init effect just restored from the panel prop.
  const supportedAggs: Aggregation[] = (currentMetric?.supported_aggregations ?? []) as Aggregation[];
  const safeAggregation: Aggregation =
    supportedAggs.includes(aggregation) ? aggregation : ((supportedAggs[0] ?? "avg") as Aggregation);
  useEffect(() => {
    if (!currentMetric) return;
    if (safeAggregation !== aggregation) {
      setAggregation(safeAggregation);
    }
  }, [currentMetric, safeAggregation, aggregation]);

  // ── 保存 ─────────────────────────────────────────────────────
  async function handleSave() {
    if (!isValid) return;
    setSaving(true);
    const input: PanelInput = {
      title: title.trim(),
      chart_type: chartType,
      metric: metricKey,
      filters: {
        node_ids: selectedNodeIds.length > 0 ? selectedNodeIds : undefined,
        task_ids: selectedTaskIds.length > 0 ? selectedTaskIds : undefined,
      },
      aggregation: safeAggregation,
      layout_x: 0,
      layout_y: Number.MAX_SAFE_INTEGER,
      layout_w: 6,
      layout_h: 4,
    };

    try {
      let saved: Panel;
      if (isEdit && panel) {
        saved = await apiClient.updatePanel(token, dashboardID, panel.id, input);
      } else {
        saved = await apiClient.addPanel(token, dashboardID, input);
      }
      onSaved(saved);
      onOpenChange(false);
    } catch (err) {
      toast.error(getErrorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  // ── 预览占位符面板（用于传给 PanelRenderer） ─────────────────
  const previewPanel: Panel = {
    id: panel?.id ?? 0,
    dashboard_id: dashboardID,
    title: title || "预览",
    chart_type: chartType,
    metric: metricKey,
    filters: {
      node_ids: selectedNodeIds.length > 0 ? selectedNodeIds : undefined,
      task_ids: selectedTaskIds.length > 0 ? selectedTaskIds : undefined,
    },
    aggregation: safeAggregation,
    layout_x: 0,
    layout_y: 0,
    layout_w: 6,
    layout_h: 4,
  };

  // ── 判断当前指标 family ─────────────────────────────────────
  const metricFamily = currentMetric?.family;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg">
        <DialogCloseButton />
        <DialogHeader>
          <DialogTitle>
            {isEdit
              ? t("dashboards.editor.title.edit")
              : t("dashboards.editor.title.create")}
          </DialogTitle>
          <DialogDescription className="sr-only">
            {isEdit
              ? t("dashboards.editor.title.edit")
              : t("dashboards.editor.title.create")}
          </DialogDescription>
        </DialogHeader>

        <DialogBody className="flex flex-col gap-4">
          {/* 面板标题 */}
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("dashboards.editor.fields.title")}
            </label>
            <Input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={t("dashboards.editor.fields.title")}
              aria-invalid={!!titleError}
              maxLength={100}
            />
            {titleError && (
              <p className="text-xs text-destructive">{titleError}</p>
            )}
          </div>

          {/* 图表类型 */}
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("dashboards.editor.fields.chartType")}
            </label>
            <Select
              value={chartType}
              onChange={(e) => setChartType(e.target.value as ChartType)}
              className="h-9 text-sm"
            >
              {CHART_TYPES.map((ct) => (
                <option key={ct} value={ct}>
                  {t(`dashboards.editor.chartType.${ct}`)}
                </option>
              ))}
            </Select>
          </div>

          {/* 指标 */}
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("dashboards.editor.fields.metric")}
            </label>
            <Select
              value={metricKey}
              onChange={(e) => handleMetricChange(e.target.value)}
              className="h-9 text-sm"
              aria-invalid={!!metricError}
            >
              {metrics.length === 0 && (
                <option value="" disabled>
                  {t("common.loading")}
                </option>
              )}
              {metrics.map((m) => (
                <option key={m.key} value={m.key}>
                  {m.label} ({m.key})
                </option>
              ))}
            </Select>
            {metricError && (
              <p className="text-xs text-destructive">{metricError}</p>
            )}
          </div>

          {/* 聚合方式 */}
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("dashboards.editor.fields.aggregation")}
            </label>
            <Select
              value={safeAggregation}
              onChange={(e) => setAggregation(e.target.value as Aggregation)}
              className="h-9 text-sm"
            >
              {supportedAggs.map((agg) => (
                <option key={agg} value={agg}>
                  {t(`dashboards.editor.aggregation.${agg}`)}
                </option>
              ))}
            </Select>
          </div>

          {/* 条件筛选：节点 */}
          {metricFamily === "node" && (
            <MultiSelectCheckboxes
              label={t("dashboards.editor.fields.nodeIds")}
              items={nodes}
              selected={selectedNodeIds}
              onChange={setSelectedNodeIds}
            />
          )}

          {/* 条件筛选：任务 */}
          {metricFamily === "task" && (
            <MultiSelectCheckboxes
              label={t("dashboards.editor.fields.taskIds")}
              items={tasks}
              selected={selectedTaskIds}
              onChange={setSelectedTaskIds}
            />
          )}

          {/* 预览 */}
          <div className="flex flex-col gap-1.5">
            <label className="text-xs font-medium text-muted-foreground">
              {t("dashboards.editor.preview")}
            </label>
            <div className="h-[200px] rounded-md border border-border bg-card/50 overflow-hidden">
              {previewLoading && (
                <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                  {t("common.loading")}
                </div>
              )}
              {!previewLoading && previewError && (
                <div className="flex h-full items-center justify-center text-xs text-destructive px-4 text-center">
                  {previewError}
                </div>
              )}
              {!previewLoading && !previewError && previewData && chartReady && (
                <PanelRenderer panel={previewPanel} data={previewData} />
              )}
              {!previewLoading && !previewError && !previewData && (
                <div className="flex h-full items-center justify-center text-xs text-muted-foreground">
                  {t("dashboards.panel.emptyState")}
                </div>
              )}
            </div>
          </div>
        </DialogBody>

        <DialogFooter>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t("dashboards.editor.cancel")}
          </Button>
          <Button
            size="sm"
            onClick={handleSave}
            disabled={!isValid || saving}
          >
            {saving ? t("common.loading") : t("dashboards.editor.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
