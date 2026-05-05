import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { BellRing, Loader2 } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Pagination } from "@/components/ui/pagination";
import { toast } from "@/components/ui/toast";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";
import type { ViewMode } from "@/components/ui/view-mode-toggle";
import { AlertFilters } from "./alert-filters";
import { AlertList } from "./alert-list";

type SortField = "triggered_at" | "severity" | "status" | "node_name";

type AlertCenterProps = {
  token: string;
  integrations: { id: string; name: string }[];
  globalSearch: string;
  setGlobalSearch: (value: string) => void;
  initialAlertId?: string | null;
  onAlertHighlighted?: () => void;
  onAlertMutated?: () => void;
  refreshVersion?: number;
};

export function AlertCenter({
  token,
  integrations,
  globalSearch,
  setGlobalSearch,
  initialAlertId,
  onAlertHighlighted,
  onAlertMutated,
  refreshVersion,
}: AlertCenterProps) {
  const { t } = useTranslation();

  // --- 筛选状态 ---
  const {
    keyword, setKeyword,
    severity: severityFilter, setSeverity: setSeverityFilter,
    status: statusFilter, setStatus: setStatusFilter,
    deferredKeyword,
    reset: resetFilters,
  } = usePageFilters({
    keyword: { key: "xirang.notifications.keyword", default: "" },
    severity: { key: "xirang.notifications.severity", default: "all" },
    status: { key: "xirang.notifications.status", default: "unresolved" },
  }, globalSearch, setGlobalSearch);

  // --- 分页与排序状态 ---
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = usePersistentState("xirang.alerts.pageSize", 20);
  const [sortBy, setSortBy] = usePersistentState<SortField>("xirang.alerts.sortBy", "triggered_at");
  const [sortOrder, setSortOrder] = usePersistentState<"asc" | "desc">("xirang.alerts.sortOrder", "desc");
  const [viewMode, setViewMode] = usePersistentState<ViewMode>("xirang.alerts.viewMode", "list");

  // --- 数据状态 ---
  const [alerts, setAlerts] = useState<AlertRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);

  // --- 投递记录状态 ---
  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);

  // --- 分组计数缓存 ---
  // Lazy: only fetched when a delivery panel opens (the only context where
  // "+N 条同类" is useful). Keyed by alertId.
  const [groupInfoMap, setGroupInfoMap] = useState<Record<string, { count: number }>>({});

  // --- 深链接高亮 ---
  const highlightClearTimerRef = useRef<number | null>(null);
  const highlightRef = useCallback((alertId: string, el: HTMLElement | null) => {
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "center" });
      if (highlightClearTimerRef.current !== null) {
        window.clearTimeout(highlightClearTimerRef.current);
      }
      highlightClearTimerRef.current = window.setTimeout(() => {
        setHighlightedAlert((current) => (current?.id === alertId ? null : current));
        highlightClearTimerRef.current = null;
      }, 600);
    }
  }, []);
  const [highlightedAlert, setHighlightedAlert] = useState<AlertRecord | null>(null);

  const abortRef = useRef<AbortController | null>(null);

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((i) => [i.id, i.name])),
    [integrations],
  );

  // --- 数据获取 ---
  const fetchAlerts = useCallback(async (targetPage: number) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    try {
      const result = await apiClient.getAlertsPaginated(token, {
        page: targetPage,
        pageSize,
        sortBy,
        sortOrder,
        status: statusFilter !== "all" ? statusFilter : undefined,
        severity: severityFilter !== "all" ? severityFilter : undefined,
        keyword: deferredKeyword.trim() || undefined,
        signal: controller.signal,
      });
      if (!controller.signal.aborted) {
        setAlerts(result.items);
        setTotal(result.total);
      }
    } catch (err) {
      if (!controller.signal.aborted && !(err instanceof DOMException && err.name === "AbortError")) {
        toast.error(getErrorMessage(err));
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  }, [token, pageSize, sortBy, sortOrder, statusFilter, severityFilter, deferredKeyword]);

  useEffect(() => {
    setPage(1);
    setHighlightedAlert(null);
    void fetchAlerts(1);
    return () => { abortRef.current?.abort(); };
  }, [fetchAlerts]);

  useEffect(() => {
    if (refreshVersion != null && refreshVersion > 0) {
      setHighlightedAlert(null);
      void fetchAlerts(page);
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- fetchAlerts and page intentionally excluded to avoid loop
  }, [refreshVersion]);

  useEffect(() => {
    return () => {
      if (highlightClearTimerRef.current !== null) {
        window.clearTimeout(highlightClearTimerRef.current);
      }
    };
  }, []);

  const handlePageChange = (p: number) => {
    setPage(p);
    setHighlightedAlert(null);
    void fetchAlerts(p);
  };

  const handlePageSizeChange = (size: number) => {
    setPageSize(size);
  };

  // --- 排序 ---
  const toggleSort = (field: SortField) => {
    if (sortBy === field) {
      setSortOrder(sortOrder === "asc" ? "desc" : "asc");
    } else {
      setSortBy(field);
      setSortOrder("desc");
    }
    setPage(1);
  };

  // --- 深链接：initialAlertId 处理 ---
  useEffect(() => {
    if (!initialAlertId || !token) return;
    resetFilters();
    void apiClient.getAlert(token, initialAlertId).then((target) => {
      setHighlightedAlert(target);
      setDeliveryOpenAlertId(target.id);
      void apiClient.getAlertDeliveries(token, target.id)
        .then((rows) => setDeliveryMap((prev) => ({ ...prev, [target.id]: rows })))
        .catch(() => {});
      onAlertHighlighted?.();
    }).catch(() => {
      onAlertHighlighted?.();
    });
  }, [initialAlertId, token, resetFilters, onAlertHighlighted]);

  // --- 投递记录操作 ---
  const refreshDeliveries = (alertId: string) => {
    setDeliveryLoadingAlertId(alertId);
    void apiClient.getAlertDeliveries(token, alertId)
      .then((rows) => setDeliveryMap((prev) => ({ ...prev, [alertId]: rows })))
      .catch((error) => toast.error(getErrorMessage(error)))
      .finally(() => setDeliveryLoadingAlertId(null));
    // Fetch group count in parallel. Best-effort: a failure here must not
    // block delivery rendering, so the error is logged at debug only.
    void apiClient.getAlertGroupInfo(token, alertId)
      .then((gi) => setGroupInfoMap((prev) => ({ ...prev, [alertId]: { count: gi.count } })))
      .catch(() => { /* non-critical; badge simply doesn't render */ });
  };

  const toggleDeliveries = (alertId: string) => {
    if (deliveryOpenAlertId === alertId) {
      setDeliveryOpenAlertId(null);
      return;
    }
    setDeliveryOpenAlertId(alertId);
    if (!deliveryMap[alertId]) {
      refreshDeliveries(alertId);
    }
  };

  // --- 告警操作 ---
  const handleAck = async (alert: AlertRecord) => {
    try {
      await apiClient.ackAlert(token, alert.id);
      toast.success(t("notifications.ackSuccess", { code: alert.errorCode }));
      void fetchAlerts(page);
      onAlertMutated?.();
    } catch (err) {
      toast.error(getErrorMessage(err));
    }
  };

  const handleResolve = async (alert: AlertRecord) => {
    try {
      await apiClient.resolveAlert(token, alert.id);
      toast.success(t("notifications.resolveSuccess", { code: alert.errorCode }));
      void fetchAlerts(page);
      onAlertMutated?.();
    } catch (err) {
      toast.error(getErrorMessage(err));
    }
  };

  const handleRetry = async (alert: AlertRecord) => {
    if (!alert.taskId) {
      toast.error(t("notifications.noAlertTask"));
      return;
    }
    try {
      await apiClient.triggerTask(token, alert.taskId);
      toast.success(t("notifications.retryTriggered", { id: alert.taskId }));
      void fetchAlerts(page);
      onAlertMutated?.();
    } catch (err) {
      toast.error(getErrorMessage(err));
    }
  };

  const handleRetryDelivery = async (alertId: string, deliveryId: string) => {
    setRetryingDeliveryKey(String(deliveryId));
    try {
      await apiClient.retryDelivery(token, deliveryId);
      toast.success(t("notifications.resendSuccess", { defaultValue: "重发成功" }));
      refreshDeliveries(alertId);
    } catch (err) {
      toast.error(getErrorMessage(err));
    } finally {
      setRetryingDeliveryKey(null);
    }
  };

  const handleRetryAllFailed = async (alertId: string) => {
    const failedDeliveries = (deliveryMap[alertId] ?? []).filter((d) => d.status === "failed");
    if (!failedDeliveries.length) return;
    setRetryingAllAlertId(alertId);
    const results = await Promise.allSettled(
      failedDeliveries.map((d) => apiClient.retryDelivery(token, d.id))
    );
    const failed = results.filter(r => r.status === "rejected").length;
    if (failed === 0) {
      toast.success("批量重发成功");
    } else {
      toast.error(`批量重发：${results.length - failed} 成功，${failed} 失败`);
    }
    refreshDeliveries(alertId);
    setRetryingAllAlertId(null);
  };

  // --- 合并高亮告警和普通列表 ---
  const displayAlerts = useMemo(() => {
    if (!highlightedAlert) return alerts;
    if (alerts.some((a) => a.id === highlightedAlert.id)) return alerts;
    return [highlightedAlert, ...alerts];
  }, [alerts, highlightedAlert]);

  return (
    <Card className="rounded-lg border border-border bg-card">
      <CardContent className="space-y-4 pt-6">
        <AlertFilters
          keyword={keyword}
          onKeywordChange={setKeyword}
          severityFilter={severityFilter}
          onSeverityChange={(v) => setSeverityFilter(v as typeof severityFilter)}
          statusFilter={statusFilter}
          onStatusChange={(v) => setStatusFilter(v as typeof statusFilter)}
          viewMode={viewMode}
          onViewModeChange={setViewMode}
          total={total}
          onReset={() => { resetFilters(); setPage(1); }}
        />

        {/* 列表内容 */}
        {loading && !alerts.length ? (
          <div className="flex items-center justify-center py-12 text-muted-foreground">
            <Loader2 className="mr-2 size-5 animate-spin" />
            {t("common.loading")}
          </div>
        ) : displayAlerts.length ? (
          <AlertList
            token={token}
            alerts={displayAlerts}
            highlightedAlertId={highlightedAlert?.id ?? null}
            highlightRef={highlightRef}
            viewMode={viewMode}
            sortBy={sortBy}
            sortOrder={sortOrder}
            onToggleSort={toggleSort}
            deliveryOpenAlertId={deliveryOpenAlertId}
            deliveryLoadingAlertId={deliveryLoadingAlertId}
            deliveryMap={deliveryMap}
            groupInfoMap={groupInfoMap}
            retryingDeliveryKey={retryingDeliveryKey}
            retryingAllAlertId={retryingAllAlertId}
            integrationNameMap={integrationNameMap}
            onRetry={(alert) => void handleRetry(alert)}
            onAck={(alert) => void handleAck(alert)}
            onResolve={(alert) => void handleResolve(alert)}
            onToggleDeliveries={toggleDeliveries}
            onRetryDelivery={(alertId, deliveryId) => void handleRetryDelivery(alertId, deliveryId)}
            onRetryAllFailed={(alertId) => void handleRetryAllFailed(alertId)}
          />
        ) : (
          <FilteredEmptyState
            icon={BellRing}
            title={t("notifications.emptyTitle")}
            description={t("notifications.emptyDesc")}
            onReset={() => { resetFilters(); setPage(1); }}
          />
        )}

        {/* 分页控件 */}
        <Pagination
          page={page}
          pageSize={pageSize}
          total={total}
          loading={loading}
          onPageChange={handlePageChange}
          onPageSizeChange={handlePageSizeChange}
        />
      </CardContent>
    </Card>
  );
}
