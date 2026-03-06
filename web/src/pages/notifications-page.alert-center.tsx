import { useMemo, useState } from "react";
import {
  BellRing,
  Loader2,
  MoreHorizontal,
  RefreshCw,
} from "lucide-react";
import {
  alertStatusMeta,
  severityToTone,
  severityWeight,
  statusWeight,
} from "@/pages/notifications-page.utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { SearchInput } from "@/components/ui/search-input";
import { StatusPulse } from "@/components/status-pulse";
import { toast } from "@/components/ui/toast";
import { usePageFilters } from "@/hooks/use-page-filters";
import { getSeverityMeta } from "@/lib/status";
import { getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";

type AlertCenterProps = {
  alerts: AlertRecord[];
  integrations: { id: string; name: string }[];
  loading: boolean;
  globalSearch: string;
  retryAlert: (id: string) => Promise<void>;
  acknowledgeAlert: (id: string) => Promise<void>;
  resolveAlert: (id: string) => Promise<void>;
  fetchAlertDeliveries: (alertId: string) => Promise<AlertDeliveryRecord[]>;
  retryAlertDelivery: (alertId: string, integrationId: string) => Promise<{ message: string }>;
  retryFailedAlertDeliveries: (alertId: string) => Promise<{ message: string }>;
};

export function AlertCenter({
  alerts,
  integrations,
  loading,
  globalSearch,
  retryAlert,
  acknowledgeAlert,
  resolveAlert,
  fetchAlertDeliveries,
  retryAlertDelivery,
  retryFailedAlertDeliveries,
}: AlertCenterProps) {
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
  }, globalSearch);
  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((i) => [i.id, i.name])),
    [integrations]
  );

  const sortedAlerts = useMemo(() => {
    return [...alerts].sort((a, b) => {
      const statusGap = statusWeight(b.status) - statusWeight(a.status);
      if (statusGap !== 0) return statusGap;
      const severityGap = severityWeight(b.severity) - severityWeight(a.severity);
      if (severityGap !== 0) return severityGap;
      const timeGap = new Date(b.triggeredAt).getTime() - new Date(a.triggeredAt).getTime();
      if (!Number.isNaN(timeGap) && timeGap !== 0) return timeGap;
      return b.id.localeCompare(a.id);
    });
  }, [alerts]);

  const filteredAlerts = useMemo(() => {
    const searchKey = deferredKeyword.trim().toLowerCase();
    return sortedAlerts.filter((alert) => {
      if (severityFilter !== "all" && alert.severity !== severityFilter) return false;
      if (statusFilter !== "all" && alert.status !== statusFilter) return false;
      if (!searchKey) return true;
      const candidate = `${alert.nodeName} ${alert.policyName} ${alert.errorCode} ${alert.message}`.toLowerCase();
      return candidate.includes(searchKey);
    });
  }, [deferredKeyword, severityFilter, sortedAlerts, statusFilter]);

  const refreshDeliveries = (alertId: string) => {
    setDeliveryLoadingAlertId(alertId);
    void fetchAlertDeliveries(alertId)
      .then((rows) => {
        setDeliveryMap((prev) => ({ ...prev, [alertId]: rows }));
      })
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

  return (
    <Card className="border-border/75">
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle className="text-base">通知中心（按节点 / 任务分流）</CardTitle>
          <Button size="sm" variant="outline" onClick={resetFilters}>
            <RefreshCw className="mr-1 size-4" />
            重置筛选
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <FilterPanel className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
          <SearchInput
            containerClassName="w-full"
            aria-label="告警关键词筛选"
            value={keyword}
            onChange={(event) => setKeyword(event.target.value)}
            placeholder="搜索节点 / 任务 / 错误码"
          />
          <AppSelect
            className="w-full"
            aria-label="告警级别筛选"
            value={severityFilter}
            onChange={(event) => setSeverityFilter(event.target.value as typeof severityFilter)}
          >
            <option value="all">全部级别</option>
            <option value="critical">严重</option>
            <option value="warning">警告</option>
            <option value="info">信息</option>
          </AppSelect>
          <AppSelect
            className="w-full"
            aria-label="告警状态筛选"
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value as typeof statusFilter)}
          >
            <option value="all">全部状态</option>
            <option value="open">待处理</option>
            <option value="acked">已确认</option>
            <option value="resolved">已恢复</option>
          </AppSelect>
          <div className="flex items-center justify-end col-span-full sm:col-span-2 md:col-span-3 lg:col-span-1">
            <Button size="sm" variant="outline" onClick={resetFilters}>
              重置
            </Button>
          </div>
        </FilterPanel>

        <FilterSummary filtered={filteredAlerts.length} total={alerts.length} unit="条告警" />

        <div className="space-y-2">
          {filteredAlerts.length ? (
            filteredAlerts.map((alert) => {
              const severity = getSeverityMeta(alert.severity);
              const status = alertStatusMeta(alert.status);
              const isDeliveryOpen = deliveryOpenAlertId === alert.id;
              const deliveryPanelId = `alert-delivery-panel-${alert.id}`;
              return (
                <div key={alert.id} className="rounded-xl border border-border/75 bg-background/65 p-3 shadow-sm">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <StatusPulse tone={severityToTone(alert.severity)} />
                      <p className="font-medium">
                        {alert.nodeName} · {alert.taskId ? `任务 #${alert.taskId}` : "节点探测"}
                      </p>
                    </div>
                    <div className="flex items-center gap-1">
                      <Badge variant={severity.variant}>{severity.label}</Badge>
                      <Badge variant={status.variant}>{status.label}</Badge>
                    </div>
                  </div>

                  <p className="mt-2 text-sm">{alert.message}</p>
                  <div className="mt-1 flex flex-wrap gap-2 text-xs text-muted-foreground">
                    <span>策略：{alert.policyName}</span>
                    <span>错误码：{alert.errorCode}</span>
                    <span>触发时间：{alert.triggeredAt}</span>
                  </div>

                  <div className="mt-3 flex flex-wrap items-center gap-2">
                    <Button
                      size="sm"
                      onClick={() => {
                        if (!alert.taskId) {
                          toast.error("该告警未绑定任务，请先修复节点连接。");
                          return;
                        }
                        void retryAlert(alert.id)
                          .then(() => toast.success(`已触发重试：任务 #${alert.taskId}`))
                          .catch((error) => toast.error(getErrorMessage(error)));
                      }}
                      disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
                    >
                      一键重试
                    </Button>

                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button size="sm" variant="outline" aria-label="更多操作">
                          <MoreHorizontal className="size-4" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="start">
                        <DropdownMenuItem
                          disabled={alert.status !== "open"}
                          onClick={() => {
                            void acknowledgeAlert(alert.id)
                              .then(() => toast.success(`已确认告警：${alert.errorCode}`))
                              .catch((error) => toast.error(getErrorMessage(error)));
                          }}
                        >
                          标记已读
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          disabled={alert.status === "resolved"}
                          onClick={() => {
                            void resolveAlert(alert.id)
                              .then(() => toast.success(`已标记恢复：${alert.errorCode}`))
                              .catch((error) => toast.error(getErrorMessage(error)));
                          }}
                        >
                          标记恢复
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          onClick={() => toggleDeliveries(alert.id)}
                        >
                          {isDeliveryOpen ? "收起投递" : "投递记录"}
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>

                  {isDeliveryOpen ? (
                    <div
                      id={deliveryPanelId}
                      role="region"
                      aria-label={`告警 ${alert.errorCode} 的投递记录`}
                      aria-busy={deliveryLoadingAlertId === alert.id}
                      className="mt-3 rounded-md border border-border/70 bg-muted/25 p-2"
                    >
                      {deliveryLoadingAlertId === alert.id ? (
                        <p className="text-xs text-muted-foreground">投递记录加载中...</p>
                      ) : (deliveryMap[alert.id] ?? []).length ? (
                        <div className="space-y-2">
                          {(deliveryMap[alert.id] ?? []).some((d) => d.status === "failed") ? (
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={retryingAllAlertId === alert.id}
                              onClick={() => {
                                setRetryingAllAlertId(alert.id);
                                void retryFailedAlertDeliveries(alert.id)
                                  .then((result) => {
                                    toast.success(result.message);
                                    refreshDeliveries(alert.id);
                                  })
                                  .catch((error) => toast.error(getErrorMessage(error)))
                                  .finally(() => setRetryingAllAlertId(null));
                              }}
                            >
                              {retryingAllAlertId === alert.id && <Loader2 className="mr-1 size-4 animate-spin" />}
                              重发全部失败投递
                            </Button>
                          ) : null}

                          {(deliveryMap[alert.id] ?? []).map((delivery) => (
                            <div key={delivery.id} className="rounded border border-border/70 bg-background/80 px-2 py-1.5 text-xs">
                              <div className="flex flex-wrap items-center justify-between gap-2">
                                <span className="font-medium">
                                  {integrationNameMap.get(delivery.integrationId) ?? delivery.integrationId}
                                </span>
                                <Badge variant={delivery.status === "sent" ? "success" : "danger"}>
                                  {delivery.status === "sent" ? "发送成功" : "发送失败"}
                                </Badge>
                              </div>
                              <p className="mt-1 text-muted-foreground">时间：{delivery.createdAt}</p>
                              {delivery.error ? <p className="mt-1 text-destructive">错误：{delivery.error}</p> : null}
                              {delivery.status === "failed" ? (
                                <Button
                                  className="mt-2"
                                  size="sm"
                                  variant="outline"
                                  disabled={retryingDeliveryKey === `${alert.id}:${delivery.integrationId}`}
                                  onClick={() => {
                                    const actionKey = `${alert.id}:${delivery.integrationId}`;
                                    setRetryingDeliveryKey(actionKey);
                                    void retryAlertDelivery(alert.id, delivery.integrationId)
                                      .then((result) => {
                                        toast.success(result.message);
                                        refreshDeliveries(alert.id);
                                      })
                                      .catch((error) => toast.error(getErrorMessage(error)))
                                      .finally(() => setRetryingDeliveryKey(null));
                                  }}
                                >
                                  {retryingDeliveryKey === `${alert.id}:${delivery.integrationId}` && <Loader2 className="mr-1 size-4 animate-spin" />}
                                  重发通知
                                </Button>
                              ) : null}
                            </div>
                          ))}
                        </div>
                      ) : (
                        <p className="text-xs text-muted-foreground">暂无投递记录。</p>
                      )}
                    </div>
                  ) : null}
                </div>
              );
            })
          ) : !loading ? (
            <FilteredEmptyState
              icon={BellRing}
              title="当前筛选条件下没有待处理通知"
              description="可以重置筛选条件，或等待下一次告警触发。"
              onReset={resetFilters}
            />
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}
