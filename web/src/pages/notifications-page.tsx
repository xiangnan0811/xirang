import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  BellRing,
  Plus,
  RefreshCw,
  Search,
  Wrench,
  Trash2,
} from "lucide-react";
import { useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { IntegrationCreateDialog } from "@/components/integration-create-dialog";
import { IntegrationEditorDialog, type IntegrationEditorDraft } from "@/components/integration-editor-dialog";
import {
  alertStatusMeta,
  integrationIcon,
  severityToTone,
  severityWeight,
  statusWeight,
} from "@/pages/notifications-page.utils";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { StatusPulse } from "@/components/status-pulse";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { getSeverityMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertDeliveryStats, IntegrationChannel } from "@/types/domain";

const notificationKeywordStorageKey = "xirang.notifications.keyword";
const notificationSeverityStorageKey = "xirang.notifications.severity";
const notificationStatusStorageKey = "xirang.notifications.status";

export function NotificationsPage() {
  const { confirm, dialog } = useConfirm();

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
    retryFailedAlertDeliveries,
    addIntegration,
    removeIntegration,
    toggleIntegration,
    updateIntegration,
    testIntegration,
    fetchAlertDeliveryStats
  } = useOutletContext<ConsoleOutletContext>();

  const [keyword, setKeyword] = usePersistentState<string>(notificationKeywordStorageKey, "");
  const [severityFilter, setSeverityFilter] =
    usePersistentState<"all" | "critical" | "warning" | "info">(notificationSeverityStorageKey, "all");
  const [statusFilter, setStatusFilter] =
    usePersistentState<"all" | "open" | "acked" | "resolved">(notificationStatusStorageKey, "all");
  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [editingIntegration, setEditingIntegration] = useState<IntegrationChannel | null>(null);
  const [testingIntegrationMap, setTestingIntegrationMap] = useState<Record<string, number>>({});
  const [updatingIntegrationMap, setUpdatingIntegrationMap] = useState<Record<string, number>>({});
  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);
  const [statsWindow, setStatsWindow] = useState<24 | 72 | 168>(24);
  const [deliveryStats, setDeliveryStats] = useState<AlertDeliveryStats | null>(null);
  const [deliveryStatsLoading, setDeliveryStatsLoading] = useState(false);
  const statsLoadedKeyRef = useRef<string>("");
  const statsRequestRef = useRef(0);

  const beginIntegrationOp = useCallback((integrationId: string, type: "test" | "update") => {
    if (type === "test") {
      setTestingIntegrationMap((prev) => ({
        ...prev,
        [integrationId]: (prev[integrationId] ?? 0) + 1
      }));
      return;
    }

    setUpdatingIntegrationMap((prev) => ({
      ...prev,
      [integrationId]: (prev[integrationId] ?? 0) + 1
    }));
  }, []);

  const endIntegrationOp = useCallback((integrationId: string, type: "test" | "update") => {
    if (type === "test") {
      setTestingIntegrationMap((prev) => {
        const current = prev[integrationId] ?? 0;
        if (current <= 1) {
          const next = { ...prev };
          delete next[integrationId];
          return next;
        }
        return {
          ...prev,
          [integrationId]: current - 1
        };
      });
      return;
    }

    setUpdatingIntegrationMap((prev) => {
      const current = prev[integrationId] ?? 0;
      if (current <= 1) {
        const next = { ...prev };
        delete next[integrationId];
        return next;
      }
      return {
        ...prev,
        [integrationId]: current - 1
      };
    });
  }, []);

  const activeIntegrations = integrations.filter((item) => item.enabled).length;
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

  const mobilePushAlerts = filteredAlerts.filter((alert) => alert.status === "open").slice(0, 6);

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((integration) => [integration.id, integration.name])),
    [integrations]
  );

  const loadDeliveryStats = useCallback((hours: 24 | 72 | 168, force = false) => {
    const queryKey = `hours:${hours}`;
    if (!force && statsLoadedKeyRef.current === queryKey) {
      return;
    }

    statsLoadedKeyRef.current = queryKey;
    const currentRequestID = statsRequestRef.current + 1;
    statsRequestRef.current = currentRequestID;

    setDeliveryStatsLoading(true);
    void fetchAlertDeliveryStats(hours)
      .then((result) => {
        if (statsRequestRef.current === currentRequestID) {
          setDeliveryStats(result);
        }
      })
      .catch((error) => {
        if (statsRequestRef.current === currentRequestID) {
          toast.error((error as Error).message);
        }
      })
      .finally(() => {
        if (statsRequestRef.current === currentRequestID) {
          setDeliveryStatsLoading(false);
        }
      });
  }, [fetchAlertDeliveryStats]);

  useEffect(() => {
    loadDeliveryStats(statsWindow);
  }, [loadDeliveryStats, statsWindow]);

  const refreshDeliveryStats = () => {
    loadDeliveryStats(statsWindow, true);
  };

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

  const openEditIntegrationDialog = (integration: IntegrationChannel) => {
    setEditingIntegration(integration);
    setEditDialogOpen(true);
  };

  const handleEditIntegration = async (draft: IntegrationEditorDraft) => {
    beginIntegrationOp(draft.id, "update");
    try {
      await updateIntegration(draft.id, {
        name: draft.name,
        endpoint: draft.endpoint,
        failThreshold: draft.failThreshold,
        cooldownMinutes: draft.cooldownMinutes,
      });
      toast.success(`通知方式 ${draft.name} 已保存。`);
      setEditDialogOpen(false);
      setEditingIntegration(null);
    } finally {
      endIntegrationOp(draft.id, "update");
    }
  };

  const handleDeleteIntegration = async (integration: IntegrationChannel) => {
    const ok = await confirm({
      title: "确认删除",
      description: `确认删除通知方式 ${integration.name} 吗？`,
    });
    if (!ok) {
      return;
    }

    beginIntegrationOp(integration.id, "update");
    try {
      await removeIntegration(integration.id);
      toast.success(`已删除通知方式：${integration.name}`);
      if (editingIntegration?.id === integration.id) {
        setEditDialogOpen(false);
        setEditingIntegration(null);
      }
    } catch (error) {
      toast.error((error as Error).message);
    } finally {
      endIntegrationOp(integration.id, "update");
    }
  };

  return (
    <div className="space-y-5 animate-fade-in">
      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Card className="border-red-500/30 bg-gradient-to-br from-red-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">待处理告警</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{openAlerts.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">实时推流中的异常项</p>
          </CardContent>
        </Card>

        <Card className="border-amber-500/30 bg-gradient-to-br from-amber-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">严重告警</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{criticalAlerts.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">优先处置，建议立即重试</p>
          </CardContent>
        </Card>

        <Card className="border-emerald-500/30 bg-gradient-to-br from-emerald-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">已启用通道</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{activeIntegrations}/{integrations.length || 0}</p>
            <p className="mt-1 text-xs text-muted-foreground">由用户手动新增通知方式</p>
          </CardContent>
        </Card>

        <Card className="border-cyan-500/30 bg-gradient-to-br from-cyan-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">24h 失败任务</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{failedTasks}</p>
            <p className="mt-1 text-xs text-muted-foreground">支持通知中心一键重试</p>
          </CardContent>
        </Card>
      </section>

      <Card className="border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">通知投递统计</CardTitle>
            <div className="flex flex-wrap items-center justify-end gap-2">
              {[{ label: "24h", value: 24 }, { label: "72h", value: 72 }, { label: "7d", value: 168 }].map((item) => (
                <Button
                  key={`stats-window-${item.value}`}
                  size="sm"
                  variant={statsWindow === item.value ? "default" : "outline"}
                  onClick={() => setStatsWindow(item.value as 24 | 72 | 168)}
                >
                  {item.label}
                </Button>
              ))}
              <Button size="sm" variant="outline" onClick={refreshDeliveryStats} disabled={deliveryStatsLoading}>
                <RefreshCw className="mr-1 size-4" />
                {deliveryStatsLoading ? "加载中" : "刷新"}
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-3">
          {deliveryStatsLoading ? (
            <LoadingState
              title="投递统计加载中"
              description="正在统计各通知渠道的成功率与失败次数..."
              rows={3}
            />
          ) : deliveryStats ? (
            <>
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">发送成功</p>
                  <p className="mt-1 text-2xl font-semibold text-emerald-500">{deliveryStats.totalSent}</p>
                </div>
                <div className="rounded-lg border border-red-500/30 bg-red-500/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">发送失败</p>
                  <p className="mt-1 text-2xl font-semibold text-red-500">{deliveryStats.totalFailed}</p>
                </div>
                <div className="rounded-lg border border-cyan-500/30 bg-cyan-500/10 p-3 shadow-sm">
                  <p className="text-xs text-muted-foreground">成功率</p>
                  <p className="mt-1 text-2xl font-semibold text-cyan-500">{deliveryStats.successRate}%</p>
                </div>
              </div>

              {deliveryStats.byIntegration.length ? (
                <div className="grid gap-2 md:grid-cols-2">
                  {deliveryStats.byIntegration.map((item) => (
                    <div key={item.integrationId} className="rounded-lg border border-border/70 bg-muted/20 p-3">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <p className="text-sm font-medium">{item.name}</p>
                        <Badge variant={item.failed > 0 ? "warning" : "success"}>{item.type}</Badge>
                      </div>
                      <div className="mt-2 grid grid-cols-3 gap-2 text-xs text-muted-foreground">
                        <p>成功 {item.sent}</p>
                        <p>失败 {item.failed}</p>
                        <p className={cn(item.successRate >= 95 ? "text-emerald-500" : "text-amber-500")}>
                          成功率 {item.successRate}%
                        </p>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">当前时间窗口内暂无投递记录。</p>
              )}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">暂无统计数据。</p>
          )}
        </CardContent>
      </Card>

      <Card className="md:hidden border-border/75">
        <CardHeader>
          <CardTitle className="text-base">移动端失败告警推流</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {mobilePushAlerts.length ? (
            mobilePushAlerts.map((alert) => {
              const severity = getSeverityMeta(alert.severity);
              return (
                <div key={`push-${alert.id}`} className="rounded-lg border border-border/75 bg-background/70 p-3 shadow-sm">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <p className="text-sm font-medium">{alert.nodeName}</p>
                    <Badge variant={severity.variant}>{severity.label}</Badge>
                  </div>
                  <p className="mt-1 text-xs text-muted-foreground">{alert.message}</p>
                  <div className="mt-2 flex items-center justify-between">
                    <span className="text-[11px] text-muted-foreground">
                      {alert.taskId ? `任务 #${alert.taskId}` : "节点探测告警"}
                    </span>
                    <Button
                      size="sm"
                      onClick={() => {
                        if (!alert.taskId) {
                          toast.error("该告警未绑定任务，请先修复节点连接。");
                          return;
                        }
                        void retryAlert(alert.id)
                          .then(() => toast.success(`已在移动告警中心重试任务 #${alert.taskId}`))
                          .catch((error) => toast.error((error as Error).message));
                      }}
                      disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
                    >
                      一键重试
                    </Button>
                  </div>
                </div>
              );
            })
          ) : (
            <p className="text-xs text-muted-foreground">当前无待处理失败推流。</p>
          )}
        </CardContent>
      </Card>

      <section className="grid gap-4 lg:grid-cols-[1.05fr_1.45fr]">
        <Card className="border-border/75">
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle className="text-base">通知与集成设置</CardTitle>
              <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                <Plus className="mr-1 size-4" />
                新增通知方式
              </Button>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            {integrations.length ? (
              integrations.map((integration) => {
                const Icon = integrationIcon(integration.type);
                const isUpdating = (updatingIntegrationMap[integration.id] ?? 0) > 0;
                const isTesting = (testingIntegrationMap[integration.id] ?? 0) > 0;
                const busy = isUpdating || isTesting;

                return (
                  <div key={integration.id} className="interactive-surface p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="rounded-md border border-primary/20 bg-primary/10 p-1.5 text-primary">
                          <Icon className="size-4" />
                        </span>
                        <div>
                          <p className="font-medium">{integration.name}</p>
                          <p className="text-xs text-muted-foreground uppercase">{integration.type}</p>
                        </div>
                      </div>

                      <div className="flex flex-wrap items-center justify-end gap-2">
                        <Switch
                          checked={integration.enabled}
                          disabled={busy}
                          onCheckedChange={() =>
                            void (async () => {
                              beginIntegrationOp(integration.id, "update");
                              try {
                                await toggleIntegration(integration.id);
                                toast.success(`通知方式 ${integration.name} 已${integration.enabled ? "停用" : "启用"}。`);
                              } catch (error) {
                                toast.error((error as Error).message);
                              } finally {
                                endIntegrationOp(integration.id, "update");
                              }
                            })()
                          }
                        />
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={busy}
                          onClick={() => {
                            beginIntegrationOp(integration.id, "test");
                            void testIntegration(integration.id)
                              .then((result) =>
                                toast.success(`${integration.name}：${result.message}（${result.latencyMs}ms）`)
                              )
                              .catch((error) => toast.error((error as Error).message))
                              .finally(() => endIntegrationOp(integration.id, "test"));
                          }}
                        >
                          {isTesting ? "测试中..." : "测试发送"}
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={busy}
                          onClick={() => openEditIntegrationDialog(integration)}
                        >
                          <Wrench className="mr-1 size-4" />
                          编辑
                        </Button>
                        <Button
                          variant="danger"
                          size="icon"
                          aria-label={`删除通知方式 ${integration.name}`}
                          disabled={busy}
                          onClick={() => {
                            void handleDeleteIntegration(integration);
                          }}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </div>

                    <div className="mt-3 space-y-1 text-xs text-muted-foreground">
                      <p className="break-all">Endpoint：{integration.endpoint}</p>
                      <p>告警阈值：连续失败 {integration.failThreshold} 次</p>
                      <p>冷却时间：{integration.cooldownMinutes} 分钟</p>
                    </div>
                  </div>
                );
              })
            ) : (
              <EmptyState title="尚未配置任何通知方式" description="请点击「新增通知方式」手动添加" />
            )}
          </CardContent>
        </Card>

        <Card className="border-border/75">
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle className="text-base">通知中心（按节点 / 任务分流）</CardTitle>
              <div className="flex flex-wrap items-center justify-end gap-2">
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
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr]">
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
                className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
                value={severityFilter}
                onChange={(event) => setSeverityFilter(event.target.value as typeof severityFilter)}
              >
                <option value="all">全部级别</option>
                <option value="critical">严重</option>
                <option value="warning">警告</option>
                <option value="info">信息</option>
              </select>

              <select
                className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
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

                      <div className="mt-3 flex flex-wrap gap-2">
                        <Button
                          size="sm"
                          onClick={() => {
                            if (!alert.taskId) {
                              toast.error("该告警未绑定任务，请先修复节点连接。");
                              return;
                            }
                            void retryAlert(alert.id)
                              .then(() => toast.success(`已触发重试：任务 #${alert.taskId}`))
                              .catch((error) => toast.error((error as Error).message));
                          }}
                          disabled={!alert.retryable || !alert.taskId || alert.status === "resolved"}
                        >
                          一键重试
                        </Button>

                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            void acknowledgeAlert(alert.id)
                              .then(() => toast.success(`已确认告警：${alert.errorCode}`))
                              .catch((error) => toast.error((error as Error).message));
                          }}
                          disabled={alert.status !== "open"}
                        >
                          标记已读
                        </Button>

                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => {
                            void resolveAlert(alert.id)
                              .then(() => toast.success(`已标记恢复：${alert.errorCode}`))
                              .catch((error) => toast.error((error as Error).message));
                          }}
                          disabled={alert.status === "resolved"}
                        >
                          标记恢复
                        </Button>

                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => toggleDeliveries(alert.id)}
                        >
                          {deliveryOpenAlertId === alert.id ? "收起投递" : "投递记录"}
                        </Button>
                      </div>

                      {deliveryOpenAlertId === alert.id ? (
                        <div className="mt-3 rounded-md border border-border/70 bg-muted/25 p-2">
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
      </section>

      <IntegrationCreateDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        onSave={async (input) => {
          await addIntegration(input);
          setCreateDialogOpen(false);
          toast.success("通知方式已新增，可按需启停。");
        }}
      />

      <IntegrationEditorDialog
        open={editDialogOpen}
        onOpenChange={(next) => {
          setEditDialogOpen(next);
          if (!next) {
            setEditingIntegration(null);
          }
        }}
        integration={editingIntegration}
        onSave={handleEditIntegration}
      />

      {dialog}
    </div>
  );
}
