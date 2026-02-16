import { useMemo, useState } from "react";
import { BellRing, RefreshCw, Search } from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { StatusPulse } from "@/components/status-pulse";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { getSeverityMeta } from "@/lib/status";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";

function alertStatusMeta(status: AlertRecord["status"]) {
  switch (status) {
    case "open":
      return { label: "待处理", variant: "danger" as const };
    case "acked":
      return { label: "已确认", variant: "warning" as const };
    default:
      return { label: "已恢复", variant: "success" as const };
  }
}

function severityWeight(severity: AlertRecord["severity"]) {
  switch (severity) {
    case "critical":
      return 3;
    case "warning":
      return 2;
    default:
      return 1;
  }
}

function statusWeight(status: AlertRecord["status"]) {
  switch (status) {
    case "open":
      return 3;
    case "acked":
      return 2;
    default:
      return 1;
  }
}

function severityToTone(severity: AlertRecord["severity"]) {
  if (severity === "critical") {
    return "offline" as const;
  }
  if (severity === "warning") {
    return "warning" as const;
  }
  return "online" as const;
}

export function AlertCenterPage() {
  const {
    alerts,
    integrations,
    tasks,
    globalSearch,
    retryAlert,
    acknowledgeAlert,
    resolveAlert,
    fetchAlertDeliveries,
    retryAlertDelivery,
    retryFailedAlertDeliveries
  } = useOutletContext<ConsoleOutletContext>();

  const [keyword, setKeyword] = useState(globalSearch);
  const [severityFilter, setSeverityFilter] = useState<"all" | "critical" | "warning" | "info">("all");
  const [statusFilter, setStatusFilter] = useState<"all" | "open" | "acked" | "resolved">("all");

  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);

  const openAlerts = alerts.filter((item) => item.status === "open");
  const criticalAlerts = openAlerts.filter((item) => item.severity === "critical");
  const failedTasks = tasks.filter((task) => task.status === "failed").length;

  const sortedAlerts = useMemo(() => {
    return [...alerts].sort((a, b) => {
      const statusGap = statusWeight(b.status) - statusWeight(a.status);
      if (statusGap !== 0) {
        return statusGap;
      }
      const severityGap = severityWeight(b.severity) - severityWeight(a.severity);
      if (severityGap !== 0) {
        return severityGap;
      }
      const timeGap = new Date(b.triggeredAt).getTime() - new Date(a.triggeredAt).getTime();
      if (!Number.isNaN(timeGap) && timeGap !== 0) {
        return timeGap;
      }
      return b.id.localeCompare(a.id);
    });
  }, [alerts]);

  const filteredAlerts = useMemo(() => {
    const effectiveKeyword = (keyword || globalSearch).trim().toLowerCase();
    return sortedAlerts.filter((alert) => {
      if (severityFilter !== "all" && alert.severity !== severityFilter) {
        return false;
      }
      if (statusFilter !== "all" && alert.status !== statusFilter) {
        return false;
      }
      if (!effectiveKeyword) {
        return true;
      }
      const candidate = `${alert.nodeName} ${alert.policyName} ${alert.errorCode} ${alert.message}`.toLowerCase();
      return candidate.includes(effectiveKeyword);
    });
  }, [globalSearch, keyword, severityFilter, sortedAlerts, statusFilter]);

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((integration) => [integration.id, integration.name])),
    [integrations]
  );

  const refreshDeliveries = (alertId: string) => {
    setDeliveryLoadingAlertId(alertId);
    void fetchAlertDeliveries(alertId)
      .then((rows) => {
        setDeliveryMap((prev) => ({
          ...prev,
          [alertId]: rows
        }));
      })
      .catch((error) => toast.error((error as Error).message))
      .finally(() => setDeliveryLoadingAlertId(null));
  };

  const toggleDeliveries = (alertId: string) => {
    if (deliveryOpenAlertId === alertId) {
      setDeliveryOpenAlertId(null);
      return;
    }

    setDeliveryOpenAlertId(alertId);
    if (deliveryMap[alertId]) {
      return;
    }
    refreshDeliveries(alertId);
  };

  return (
    <div className="animate-fade-in space-y-4">
      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="border-destructive/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">待处理告警</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{openAlerts.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">实时推流中的异常项</p>
          </CardContent>
        </Card>

        <Card className="border-warning/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">严重告警</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{criticalAlerts.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">优先处置，建议立即重试</p>
          </CardContent>
        </Card>

        <Card className="border-info/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">24h 失败任务</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{failedTasks}</p>
            <p className="mt-1 text-xs text-muted-foreground">支持一键重试</p>
          </CardContent>
        </Card>

        <Card className="border-success/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">通知通道</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{integrations.filter((it) => it.enabled).length}/{integrations.length || 0}</p>
            <p className="mt-1 text-xs text-muted-foreground">通道状态一目了然</p>
          </CardContent>
        </Card>
      </section>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">通知中心（按节点 / 任务分流）</CardTitle>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setKeyword("");
                setSeverityFilter("all");
                setStatusFilter("all");
              }}
            >
              <RefreshCw className="mr-1 size-4" />
              重置筛选
            </Button>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="grid gap-2 md:grid-cols-[1.6fr_1fr_1fr]">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                className="pl-9"
                value={keyword}
                onChange={(event) => setKeyword(event.target.value)}
                placeholder="搜索节点 / 任务 / 错误码"
              />
            </div>

            <select
              className="h-10 rounded-md border bg-background px-3 text-sm"
              value={severityFilter}
              onChange={(event) => setSeverityFilter(event.target.value as typeof severityFilter)}
            >
              <option value="all">全部级别</option>
              <option value="critical">严重</option>
              <option value="warning">警告</option>
              <option value="info">信息</option>
            </select>

            <select
              className="h-10 rounded-md border bg-background px-3 text-sm"
              value={statusFilter}
              onChange={(event) => setStatusFilter(event.target.value as typeof statusFilter)}
            >
              <option value="all">全部状态</option>
              <option value="open">待处理</option>
              <option value="acked">已确认</option>
              <option value="resolved">已恢复</option>
            </select>
          </div>

          <div className="space-y-2">
            {filteredAlerts.length ? (
              filteredAlerts.map((alert) => {
                const severity = getSeverityMeta(alert.severity);
                const status = alertStatusMeta(alert.status);
                return (
                  <div key={alert.id} className="rounded-lg border p-3">
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

                    <div className="mt-3 flex flex-wrap gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={alert.status !== "open"}
                        onClick={() => {
                          void acknowledgeAlert(alert.id)
                            .then(() => toast.success(`告警 ${alert.errorCode} 已确认`))
                            .catch((error) => toast.error((error as Error).message));
                        }}
                      >
                        确认
                      </Button>

                      <Button
                        size="sm"
                        variant="outline"
                        disabled={alert.status === "resolved"}
                        onClick={() => {
                          void resolveAlert(alert.id)
                            .then(() => toast.success(`告警 ${alert.errorCode} 已恢复`))
                            .catch((error) => toast.error((error as Error).message));
                        }}
                      >
                        标记恢复
                      </Button>

                      <Button
                        size="sm"
                        disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
                        onClick={() => {
                          void retryAlert(alert.id)
                            .then(() => toast.success(`已重试任务 #${alert.taskId}`))
                            .catch((error) => toast.error((error as Error).message));
                        }}
                      >
                        一键重试
                      </Button>

                      <Button size="sm" variant="outline" onClick={() => toggleDeliveries(alert.id)}>
                        {deliveryOpenAlertId === alert.id ? "收起投递" : "投递记录"}
                      </Button>
                    </div>

                    {deliveryOpenAlertId === alert.id ? (
                      <div className="mt-3 rounded-md border bg-muted/30 p-2">
                        {deliveryLoadingAlertId === alert.id ? (
                          <p className="text-xs text-muted-foreground">投递记录加载中...</p>
                        ) : (deliveryMap[alert.id] ?? []).length ? (
                          <div className="space-y-2">
                            {(deliveryMap[alert.id] ?? []).some((delivery) => delivery.status === "failed") ? (
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
                                    .catch((error) => toast.error((error as Error).message))
                                    .finally(() => setRetryingAllAlertId(null));
                                }}
                              >
                                {retryingAllAlertId === alert.id ? "批量重发中..." : "重发全部失败投递"}
                              </Button>
                            ) : null}

                            {(deliveryMap[alert.id] ?? []).map((delivery) => (
                              <div key={delivery.id} className="rounded border bg-background px-2 py-1.5 text-xs">
                                <div className="flex flex-wrap items-center justify-between gap-2">
                                  <span className="font-medium">
                                    {integrationNameMap.get(delivery.integrationId) ?? delivery.integrationId}
                                  </span>
                                  <Badge variant={delivery.status === "sent" ? "success" : "danger"}>
                                    {delivery.status === "sent" ? "发送成功" : "发送失败"}
                                  </Badge>
                                </div>
                                <p className="mt-1 text-muted-foreground">时间：{delivery.createdAt}</p>
                                {delivery.error ? <p className="mt-1 text-red-500">错误：{delivery.error}</p> : null}
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
                                        .catch((error) => toast.error((error as Error).message))
                                        .finally(() => setRetryingDeliveryKey(null));
                                    }}
                                  >
                                    {retryingDeliveryKey === `${alert.id}:${delivery.integrationId}` ? "重发中..." : "重发通知"}
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
            ) : (
              <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-4 text-sm text-emerald-600 dark:text-emerald-300">
                <BellRing className="mb-1 size-4" />
                当前筛选条件下没有待处理通知。
              </div>
            )}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
