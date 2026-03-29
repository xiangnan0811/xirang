import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  BellRing,
  ChevronDown,
  ChevronUp,
  Loader2,
  MoreHorizontal,
  RefreshCw,
} from "lucide-react";
import {
  alertStatusMeta,
  severityToTone,
} from "@/pages/notifications-page.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FilterPanel } from "@/components/ui/filter-panel";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Pagination } from "@/components/ui/pagination";
import { SearchInput } from "@/components/ui/search-input";
import { StatusPulse } from "@/components/status-pulse";
import { ViewModeToggle, type ViewMode } from "@/components/ui/view-mode-toggle";
import { toast } from "@/components/ui/toast";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { apiClient } from "@/lib/api/client";
import { getSeverityMeta } from "@/lib/status";
import { getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";

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
    status: { key: "xirang.notifications.status", default: "all" },
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

  // --- 深链接高亮 ---
  // 一次性落位：滚动到目标告警后立即清除高亮注入，避免污染后续分页结果
  const highlightRef = useCallback((el: HTMLElement | null) => {
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "center" });
      // 等动画结束后清除高亮状态，不再向列表注入额外记录
      setTimeout(() => setHighlightedAlert(null), 600);
    }
  }, []);
  const [highlightedAlert, setHighlightedAlert] = useState<AlertRecord | null>(null);

  const abortRef = useRef<AbortController | null>(null);

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((i) => [i.id, i.name])),
    [integrations],
  );

  // --- 数据获取（page 通过参数传入，避免闭包捕获旧值） ---
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

  // 筛选/排序/pageSize/关键词 变更时，重置到第 1 页并重新获取；清除深链接高亮
  useEffect(() => {
    setPage(1);
    setHighlightedAlert(null);
    void fetchAlerts(1);
    return () => { abortRef.current?.abort(); };
  }, [fetchAlerts]);

  // 全局刷新（顶栏按钮/自动刷新）时重新获取当前页，同时清除深链接高亮
  useEffect(() => {
    if (refreshVersion != null && refreshVersion > 0) {
      setHighlightedAlert(null);
      void fetchAlerts(page);
    }
  }, [refreshVersion]);

  // 翻页（同时清除深链接高亮）
  const handlePageChange = (p: number) => {
    setPage(p);
    setHighlightedAlert(null);
    void fetchAlerts(p);
  };

  // --- 服务端筛选变更（自动触发 fetchAlerts 的 useEffect） ---
  const handleSeverityChange = (value: string) => {
    setSeverityFilter(value as typeof severityFilter);
  };
  const handleStatusChange = (value: string) => {
    setStatusFilter(value as typeof statusFilter);
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

  const sortIndicator = (field: SortField) => {
    if (sortBy !== field) return null;
    return sortOrder === "asc"
      ? <ChevronUp className="inline size-3.5" />
      : <ChevronDown className="inline size-3.5" />;
  };

  const sortableThProps = (field: SortField) => ({
    role: "button" as const,
    tabIndex: 0,
    className: "px-3 py-2.5 cursor-pointer select-none",
    onClick: () => toggleSort(field),
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        toggleSort(field);
      }
    },
  });

  // 关键词搜索已下推到服务端，无需客户端过滤

  // --- 深链接：initialAlertId 处理 ---
  useEffect(() => {
    if (!initialAlertId || !token) return;
    resetFilters();
    // 通过单条 API 获取指定告警（避免全量加载的 200 条上限）
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

  // scrollIntoView is handled by the highlightRef callback ref

  // --- 投递记录操作 ---
  const refreshDeliveries = (alertId: string) => {
    setDeliveryLoadingAlertId(alertId);
    void apiClient.getAlertDeliveries(token, alertId)
      .then((rows) => setDeliveryMap((prev) => ({ ...prev, [alertId]: rows })))
      .catch((error) => toast.error(getErrorMessage(error)))
      .finally(() => setDeliveryLoadingAlertId(null));
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

  // --- 告警操作（直接调用 API，操作后刷新当前页） ---
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

  const handleRetryDelivery = async (alertId: string, integrationId: string) => {
    const actionKey = `${alertId}:${integrationId}`;
    setRetryingDeliveryKey(actionKey);
    try {
      const result = await apiClient.retryAlertDelivery(token, alertId, integrationId);
      toast.success(result.message);
      refreshDeliveries(alertId);
    } catch (err) {
      toast.error(getErrorMessage(err));
    } finally {
      setRetryingDeliveryKey(null);
    }
  };

  const handleRetryAllFailed = async (alertId: string) => {
    setRetryingAllAlertId(alertId);
    try {
      const result = await apiClient.retryFailedDeliveries(token, alertId);
      toast.success(result.message);
      refreshDeliveries(alertId);
    } catch (err) {
      toast.error(getErrorMessage(err));
    } finally {
      setRetryingAllAlertId(null);
    }
  };

  // --- 渲染：投递记录面板 ---
  const renderDeliveryPanel = (alert: AlertRecord) => {
    const isOpen = deliveryOpenAlertId === alert.id;
    if (!isOpen) return null;
    const deliveryPanelId = `alert-delivery-panel-${alert.id}`;
    return (
      <div
        id={deliveryPanelId}
        role="region"
        aria-label={t("notifications.deliveryPanelAriaLabel", { code: alert.errorCode })}
        aria-busy={deliveryLoadingAlertId === alert.id}
        className="mt-3 rounded-md border border-border/70 bg-muted/25 p-2"
      >
        {deliveryLoadingAlertId === alert.id ? (
          <p className="text-xs text-muted-foreground">{t("notifications.deliveryLoading")}</p>
        ) : (deliveryMap[alert.id] ?? []).length ? (
          <div className="space-y-2">
            {(deliveryMap[alert.id] ?? []).some((d) => d.status === "failed") ? (
              <Button
                size="sm"
                variant="outline"
                disabled={retryingAllAlertId === alert.id}
                onClick={() => void handleRetryAllFailed(alert.id)}
              >
                {retryingAllAlertId === alert.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                {t("notifications.resendAllFailed")}
              </Button>
            ) : null}
            {(deliveryMap[alert.id] ?? []).map((delivery) => (
              <div key={delivery.id} className="rounded border border-border/70 bg-background/80 px-2 py-1.5 text-xs">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-medium">
                    {integrationNameMap.get(delivery.integrationId) ?? delivery.integrationId}
                  </span>
                  <Badge variant={delivery.status === "sent" ? "success" : "danger"}>
                    {delivery.status === "sent" ? t("notifications.deliverySent") : t("notifications.deliveryFailed")}
                  </Badge>
                </div>
                <p className="mt-1 text-muted-foreground">{t("notifications.deliveryTime", { time: delivery.createdAt })}</p>
                {delivery.error ? <p className="mt-1 text-destructive">{t("notifications.deliveryError", { error: delivery.error })}</p> : null}
                {delivery.status === "failed" ? (
                  <Button
                    className="mt-2"
                    size="sm"
                    variant="outline"
                    disabled={retryingDeliveryKey === `${alert.id}:${delivery.integrationId}`}
                    onClick={() => void handleRetryDelivery(alert.id, delivery.integrationId)}
                  >
                    {retryingDeliveryKey === `${alert.id}:${delivery.integrationId}` && <Loader2 className="mr-1 size-4 animate-spin" />}
                    {t("notifications.resendNotification")}
                  </Button>
                ) : null}
              </div>
            ))}
          </div>
        ) : (
          <p className="text-xs text-muted-foreground">{t("notifications.noDeliveryRecords")}</p>
        )}
      </div>
    );
  };

  // --- 渲染：操作按钮 ---
  const renderActions = (alert: AlertRecord) => (
    <div className="flex items-center gap-1">
      <Button
        size="sm"
        onClick={() => void handleRetry(alert)}
        disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
      >
        {t("notifications.oneClickRetry")}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="outline" aria-label={t("common.more")}>
            <MoreHorizontal className="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          <DropdownMenuItem
            disabled={alert.status !== "open"}
            onClick={() => void handleAck(alert)}
          >
            {t("notifications.markRead")}
          </DropdownMenuItem>
          <DropdownMenuItem
            disabled={alert.status === "resolved"}
            onClick={() => void handleResolve(alert)}
          >
            {t("notifications.markResolved")}
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => toggleDeliveries(alert.id)}>
            {deliveryOpenAlertId === alert.id ? t("notifications.collapseDelivery") : t("notifications.deliveryRecords")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );

  // --- 渲染：卡片视图 ---
  const renderCardView = (items: AlertRecord[]) => (
    <div className="space-y-2">
      {items.map((alert) => {
        const severity = getSeverityMeta(alert.severity);
        const status = alertStatusMeta(alert.status);
        const toneClass = alert.severity === "critical" ? "bg-destructive" : alert.severity === "warning" ? "bg-warning" : "bg-info";
        return (
          <div
            key={alert.id}
            ref={alert.id === highlightedAlert?.id ? highlightRef : undefined}
            className="glass-panel overflow-hidden relative group p-4 transition-colors hover:bg-muted/10"
          >
            <div className={`absolute top-0 left-0 w-1.5 h-full ${toneClass} opacity-60 group-hover:opacity-100 transition-opacity`} />
            <div className="flex flex-wrap items-start justify-between gap-2 pl-2">
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <StatusPulse tone={severityToTone(alert.severity)} />
                  <span className="font-medium text-foreground/90 truncate">
                    {alert.nodeName}
                  </span>
                  <Badge variant={severity.variant}>{severity.label}</Badge>
                  <Badge variant={status.variant}>{status.label}</Badge>
                </div>
                <p className="mt-1.5 text-sm pl-5">{alert.message}</p>
                <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground pl-5">
                  {alert.taskId ? (
                    <span>{t("notifications.taskLabel", { id: alert.taskId })}{alert.taskRunId ? ` ${t("notifications.taskRunLabel", { id: alert.taskRunId })}` : ""}</span>
                  ) : (
                    <span>{t("notifications.nodeProbe")}</span>
                  )}
                  <span>{alert.policyName}</span>
                  <span>{alert.errorCode}</span>
                  <span>{alert.triggeredAt}</span>
                </div>
              </div>
              <div className="shrink-0">
                {renderActions(alert)}
              </div>
            </div>
            {renderDeliveryPanel(alert)}
          </div>
        );
      })}
    </div>
  );

  // --- 渲染：表格视图 ---
  const renderTableView = (items: AlertRecord[]) => (
    <div className="glass-panel overflow-x-auto">
      <table className="min-w-[1080px] text-left text-sm w-full">
        <thead>
          <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
            <th scope="col" {...sortableThProps("severity")} className="px-3 py-2.5 w-[80px] cursor-pointer select-none">
              {t("notifications.colSeverity")} {sortIndicator("severity")}
            </th>
            <th scope="col" {...sortableThProps("node_name")} className="px-3 py-2.5 w-[150px] cursor-pointer select-none">
              {t("notifications.colNode")} {sortIndicator("node_name")}
            </th>
            <th scope="col" className="px-3 py-2.5">
              {t("notifications.colMessage")}
            </th>
            <th scope="col" className="px-3 py-2.5 w-[120px]">{t("notifications.colPolicy")}</th>
            <th scope="col" className="px-3 py-2.5 w-[100px]">{t("notifications.colErrorCode")}</th>
            <th scope="col" {...sortableThProps("triggered_at")} className="px-3 py-2.5 w-[160px] cursor-pointer select-none">
              {t("notifications.colTime")} {sortIndicator("triggered_at")}
            </th>
            <th scope="col" {...sortableThProps("status")} className="px-3 py-2.5 w-[80px] cursor-pointer select-none">
              {t("notifications.colStatus")} {sortIndicator("status")}
            </th>
            <th scope="col" className="px-3 py-2.5 w-[120px]">{t("common.actions")}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((alert) => {
            const severity = getSeverityMeta(alert.severity);
            const status = alertStatusMeta(alert.status);
            return (
              <tr key={alert.id} ref={alert.id === highlightedAlert?.id ? highlightRef : undefined} className="border-b border-border/60 transition-colors duration-200 ease-out hover:bg-muted/40 group">
                <td className="px-3 py-2.5">
                  <StatusPulse tone={severityToTone(alert.severity)} />
                  <Badge variant={severity.variant} className="ml-1">{severity.label}</Badge>
                </td>
                <td className="px-3 py-2.5 font-medium">{alert.nodeName}</td>
                <td className="px-3 py-2.5 max-w-[300px] truncate" title={alert.message}>{alert.message}</td>
                <td className="px-3 py-2.5 text-muted-foreground text-xs">{alert.policyName}</td>
                <td className="px-3 py-2.5 font-mono text-xs text-muted-foreground">{alert.errorCode}</td>
                <td className="px-3 py-2.5 text-muted-foreground text-xs">{alert.triggeredAt}</td>
                <td className="px-3 py-2.5">
                  <Badge variant={status.variant}>{status.label}</Badge>
                </td>
                <td className="px-3 py-2.5">
                  {renderActions(alert)}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {items.map((alert) => deliveryOpenAlertId === alert.id ? (
        <div key={`delivery-${alert.id}`} className="px-3 pb-3">
          {renderDeliveryPanel(alert)}
        </div>
      ) : null)}
    </div>
  );

  // --- 合并高亮告警和普通列表 ---
  const displayAlerts = useMemo(() => {
    if (!highlightedAlert) return alerts;
    // 如果高亮告警已在当前页数据中，不需要额外添加
    if (alerts.some((a) => a.id === highlightedAlert.id)) return alerts;
    return [highlightedAlert, ...alerts];
  }, [alerts, highlightedAlert]);

  return (
    <Card className="glass-panel border-border/70">
      <CardContent className="space-y-4 pt-6">
        {/* 标题栏 */}
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-2 font-medium">
            {t("notifications.alertCenterTitle")}
          </div>
          <div className="flex items-center gap-2">
            <ViewModeToggle
              value={viewMode}
              onChange={setViewMode}
              groupLabel={t("notifications.viewModeLabel")}
              className="hidden md:inline-flex"
            />
            <Button size="sm" variant="outline" onClick={() => { resetFilters(); setPage(1); }}>
              <RefreshCw className="mr-1 size-3.5" />
              {t("common.resetFilter")}
            </Button>
          </div>
        </div>

        {/* 筛选栏 */}
        <FilterPanel sticky={false} className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
          <SearchInput
            containerClassName="w-full"
            aria-label={t("notifications.keywordFilter")}
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder={t("notifications.keywordPlaceholder")}
          />
          <AppSelect
            containerClassName="w-full"
            aria-label={t("notifications.severityFilter")}
            value={severityFilter}
            onChange={(event) => handleSeverityChange(event.target.value)}
          >
            <option value="all">{t("notifications.allSeverities")}</option>
            <option value="critical">{t("status.alert.critical")}</option>
            <option value="warning">{t("status.alert.warning")}</option>
            <option value="info">{t("status.alert.info")}</option>
          </AppSelect>
          <AppSelect
            containerClassName="w-full"
            aria-label={t("notifications.statusFilter")}
            value={statusFilter}
            onChange={(event) => handleStatusChange(event.target.value)}
          >
            <option value="all">{t("common.all")}{t("common.status")}</option>
            <option value="open">{t("notifications.statusOpen")}</option>
            <option value="acked">{t("notifications.statusAcked")}</option>
            <option value="resolved">{t("notifications.statusResolved")}</option>
          </AppSelect>
          {/* 重置按钮已在标题栏提供，此处不重复 */}
        </FilterPanel>

        {/* 筛选摘要：所有筛选（含关键词）均为服务端，total 反映真实结果集 */}
        <p className="text-xs text-muted-foreground">{t("common.totalItems", { total })}</p>

        {/* 列表内容 */}
        {loading && !alerts.length ? (
          <div className="flex items-center justify-center py-12 text-muted-foreground">
            <Loader2 className="mr-2 size-5 animate-spin" />
            {t("common.loading")}
          </div>
        ) : displayAlerts.length ? (
          <>
            {/* 移动端始终卡片，桌面端按 viewMode 切换 */}
            <div className="md:hidden">
              {renderCardView(displayAlerts)}
            </div>
            <div className="hidden md:block">
              {viewMode === "list" ? renderTableView(displayAlerts) : renderCardView(displayAlerts)}
            </div>
          </>
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
