import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  BellRing,
  Mail,
  MessageSquare,
  Plus,
  RefreshCw,
  Search,
  Send,
  Trash2,
  Webhook
} from "lucide-react";
import { Link, useBeforeUnload, useBlocker, useOutletContext } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Badge } from "@/components/ui/badge";
import { StatusPulse } from "@/components/status-pulse";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { getSeverityMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertDeliveryStats, AlertRecord, IntegrationChannel, IntegrationType } from "@/types/domain";

function integrationIcon(type: IntegrationChannel["type"]) {
  switch (type) {
    case "email":
      return Mail;
    case "slack":
      return MessageSquare;
    case "telegram":
      return Send;
    default:
      return Webhook;
  }
}

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

const defaultDraft = {
  type: "email" as IntegrationType,
  name: "",
  endpoint: "",
  failThreshold: 2,
  cooldownMinutes: 5,
  enabled: true
};

type IntegrationGuide = {
  endpointLabel: string;
  endpointPlaceholder: string;
  endpointHint: string;
  sample: string;
};

type IntegrationEditDraft = {
  endpoint: string;
  failThreshold: number;
  cooldownMinutes: number;
};

type SaveIntegrationDraftOptions = {
  silent?: boolean;
};

const integrationGuideMap: Record<IntegrationType, IntegrationGuide> = {
  email: {
    endpointLabel: "收件邮箱",
    endpointPlaceholder: "ops@example.com, oncall@example.com",
    endpointHint: "可填写多个邮箱，使用逗号分隔。",
    sample: "ops@example.com"
  },
  slack: {
    endpointLabel: "Slack Webhook URL",
    endpointPlaceholder: "https://hooks.slack.com/services/xxx/yyy/zzz",
    endpointHint: "请在 Slack Incoming Webhook 中复制地址。",
    sample: "https://hooks.slack.com/services/T000/B000/XXXX"
  },
  telegram: {
    endpointLabel: "Telegram Bot Endpoint",
    endpointPlaceholder: "https://api.telegram.org/bot<token>/sendMessage?chat_id=<id>",
    endpointHint: "建议使用机器人 sendMessage 接口完整 URL。",
    sample: "https://api.telegram.org/bot123456:abc/sendMessage?chat_id=10001"
  },
  webhook: {
    endpointLabel: "Webhook URL",
    endpointPlaceholder: "https://example.com/xirang/alerts",
    endpointHint: "支持任意 HTTP/HTTPS 接收端点。",
    sample: "https://example.com/hooks/xirang"
  }
};

function isValidURL(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function validateIntegrationDraft(type: IntegrationType, endpoint: string): string | null {
  const raw = endpoint.trim();
  if (!raw) {
    return "新增失败：请填写通知地址。";
  }

  if (type === "email") {
    const emails = raw
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
    if (!emails.length) {
      return "新增失败：请填写至少一个邮箱地址。";
    }
    const mailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emails.every((item) => mailRegex.test(item))) {
      return "新增失败：邮箱格式不正确，请使用逗号分隔多个邮箱。";
    }
    return null;
  }

  if (!isValidURL(raw)) {
    return "新增失败：该通道需要合法的 http/https 地址。";
  }
  return null;
}

function toIntegrationEditDraft(integration: IntegrationChannel): IntegrationEditDraft {
  return {
    endpoint: integration.endpoint,
    failThreshold: integration.failThreshold,
    cooldownMinutes: integration.cooldownMinutes
  };
}

function normalizeIntegrationEditDraft(draft: IntegrationEditDraft): IntegrationEditDraft {
  return {
    endpoint: draft.endpoint.trim(),
    failThreshold: Math.max(1, draft.failThreshold),
    cooldownMinutes: Math.max(1, draft.cooldownMinutes)
  };
}

function isIntegrationDraftDirty(draft: IntegrationEditDraft, integration: IntegrationChannel) {
  const normalized = normalizeIntegrationEditDraft(draft);
  return (
    normalized.endpoint !== integration.endpoint ||
    normalized.failThreshold !== integration.failThreshold ||
    normalized.cooldownMinutes !== integration.cooldownMinutes
  );
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

export function NotificationsPage() {
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

  const [keyword, setKeyword] = useState(globalSearch);
  const [severityFilter, setSeverityFilter] = useState<"all" | "critical" | "warning" | "info">("all");
  const [statusFilter, setStatusFilter] = useState<"all" | "open" | "acked" | "resolved">("all");
  const [showCreateIntegration, setShowCreateIntegration] = useState(false);
  const [integrationDraft, setIntegrationDraft] = useState(defaultDraft);
  const [toast, setToast] = useState<string | null>(null);
  const [testingIntegrationId, setTestingIntegrationId] = useState<string | null>(null);
  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);
  const [statsWindow, setStatsWindow] = useState<24 | 72 | 168>(24);
  const [deliveryStats, setDeliveryStats] = useState<AlertDeliveryStats | null>(null);
  const [deliveryStatsLoading, setDeliveryStatsLoading] = useState(false);
  const [integrationEditDrafts, setIntegrationEditDrafts] = useState<Record<string, IntegrationEditDraft>>({});
  const [savingIntegrationId, setSavingIntegrationId] = useState<string | null>(null);
  const [savingAllIntegrations, setSavingAllIntegrations] = useState(false);
  const statsLoadedKeyRef = useRef<string>("");
  const statsRequestRef = useRef(0);

  const activeIntegrationGuide = integrationGuideMap[integrationDraft.type];

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
          setToast((error as Error).message);
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
      .catch((error) => setToast((error as Error).message))
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

  useEffect(() => {
    setIntegrationEditDrafts((prev) => {
      const next: Record<string, IntegrationEditDraft> = {};
      for (const integration of integrations) {
        next[integration.id] = prev[integration.id] ?? toIntegrationEditDraft(integration);
      }
      return next;
    });
  }, [integrations]);

  const patchIntegrationDraft = useCallback((integrationId: string, patch: Partial<IntegrationEditDraft>) => {
    setIntegrationEditDrafts((prev) => {
      const current = prev[integrationId] ?? {
        endpoint: "",
        failThreshold: 1,
        cooldownMinutes: 1
      };
      return {
        ...prev,
        [integrationId]: {
          ...current,
          ...patch
        }
      };
    });
  }, []);

  const resetIntegrationDraft = useCallback((integration: IntegrationChannel) => {
    setIntegrationEditDrafts((prev) => ({
      ...prev,
      [integration.id]: toIntegrationEditDraft(integration)
    }));
  }, []);

  const saveIntegrationDraft = useCallback(async (
    integration: IntegrationChannel,
    options: SaveIntegrationDraftOptions = {}
  ) => {
    const draft = integrationEditDrafts[integration.id] ?? toIntegrationEditDraft(integration);
    const normalizedDraft = normalizeIntegrationEditDraft(draft);

    const validationError = validateIntegrationDraft(integration.type, normalizedDraft.endpoint);
    if (validationError) {
      if (!options.silent) {
        setToast(validationError);
      }
      return false;
    }

    setSavingIntegrationId(integration.id);
    try {
      await updateIntegration(integration.id, normalizedDraft);
      setIntegrationEditDrafts((prev) => ({
        ...prev,
        [integration.id]: normalizedDraft
      }));
      if (!options.silent) {
        setToast(`通知方式 ${integration.name} 已保存。`);
      }
      return true;
    } catch (error) {
      if (!options.silent) {
        setToast((error as Error).message);
      }
      return false;
    } finally {
      setSavingIntegrationId(null);
    }
  }, [integrationEditDrafts, updateIntegration]);

  const saveAllIntegrationDrafts = useCallback(async () => {
    if (savingAllIntegrations || savingIntegrationId !== null) {
      return;
    }

    const dirtyIntegrations = integrations.filter((integration) => {
      const draft = integrationEditDrafts[integration.id] ?? toIntegrationEditDraft(integration);
      return isIntegrationDraftDirty(draft, integration);
    });

    if (!dirtyIntegrations.length) {
      setToast("当前没有待保存修改。");
      return;
    }

    setSavingAllIntegrations(true);
    try {
      let successCount = 0;
      for (const integration of dirtyIntegrations) {
        const saved = await saveIntegrationDraft(integration, { silent: true });
        if (saved) {
          successCount += 1;
        }
      }

      const failedCount = dirtyIntegrations.length - successCount;
      if (failedCount === 0) {
        setToast(`已批量保存 ${successCount} 项通知配置。`);
      } else {
        setToast(`已批量保存 ${successCount}/${dirtyIntegrations.length} 项通知配置，${failedCount} 项失败，请检查后重试。`);
      }
    } finally {
      setSavingAllIntegrations(false);
    }
  }, [integrationEditDrafts, integrations, saveIntegrationDraft, savingAllIntegrations, savingIntegrationId]);

  const unsavedIntegrationCount = useMemo(() => {
    return integrations.reduce((count, integration) => {
      const draft = integrationEditDrafts[integration.id] ?? toIntegrationEditDraft(integration);
      return count + (isIntegrationDraftDirty(draft, integration) ? 1 : 0);
    }, 0);
  }, [integrationEditDrafts, integrations]);

  const hasUnsavedIntegrationChanges = unsavedIntegrationCount > 0;
  const integrationConfigBusy = savingIntegrationId !== null || savingAllIntegrations;

  const resetAllIntegrationDrafts = useCallback(() => {
    setIntegrationEditDrafts(() => {
      const next: Record<string, IntegrationEditDraft> = {};
      for (const integration of integrations) {
        next[integration.id] = toIntegrationEditDraft(integration);
      }
      return next;
    });
    setToast("已重置所有未保存修改。");
  }, [integrations]);

  useBeforeUnload(
    useCallback((event) => {
      if (!hasUnsavedIntegrationChanges) {
        return;
      }
      event.preventDefault();
      event.returnValue = "";
    }, [hasUnsavedIntegrationChanges])
  );

  const integrationLeaveBlocker = useBlocker(hasUnsavedIntegrationChanges);

  useEffect(() => {
    if (integrationLeaveBlocker.state !== "blocked") {
      return;
    }

    const shouldLeave = window.confirm("当前有未保存的通知配置，确认离开当前页面吗？");
    if (shouldLeave) {
      integrationLeaveBlocker.proceed();
      return;
    }

    integrationLeaveBlocker.reset();
  }, [integrationLeaveBlocker]);

  return (
    <div className="space-y-4">
      <section className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Card className="border-red-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">待处理告警</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{openAlerts.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">实时推流中的异常项</p>
          </CardContent>
        </Card>

        <Card className="border-amber-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">严重告警</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{criticalAlerts.length}</p>
            <p className="mt-1 text-xs text-muted-foreground">优先处置，建议立即重试</p>
          </CardContent>
        </Card>

        <Card className="border-emerald-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">已启用通道</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{activeIntegrations}/{integrations.length || 0}</p>
            <p className="mt-1 text-xs text-muted-foreground">由用户手动新增通知方式</p>
          </CardContent>
        </Card>

        <Card className="border-cyan-500/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">24h 失败任务</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{failedTasks}</p>
            <p className="mt-1 text-xs text-muted-foreground">支持通知中心一键重试</p>
          </CardContent>
        </Card>
      </section>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">通知投递统计</CardTitle>
            <div className="flex items-center gap-2">
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
            <p className="text-sm text-muted-foreground">正在拉取投递统计...</p>
          ) : deliveryStats ? (
            <>
              <div className="grid gap-3 sm:grid-cols-3">
                <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-3">
                  <p className="text-xs text-muted-foreground">发送成功</p>
                  <p className="mt-1 text-2xl font-semibold text-emerald-500">{deliveryStats.totalSent}</p>
                </div>
                <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-3">
                  <p className="text-xs text-muted-foreground">发送失败</p>
                  <p className="mt-1 text-2xl font-semibold text-red-500">{deliveryStats.totalFailed}</p>
                </div>
                <div className="rounded-lg border border-cyan-500/30 bg-cyan-500/5 p-3">
                  <p className="text-xs text-muted-foreground">成功率</p>
                  <p className="mt-1 text-2xl font-semibold text-cyan-500">{deliveryStats.successRate}%</p>
                </div>
              </div>

              {deliveryStats.byIntegration.length ? (
                <div className="grid gap-2 md:grid-cols-2">
                  {deliveryStats.byIntegration.map((item) => (
                    <div key={item.integrationId} className="rounded-lg border bg-muted/20 p-3">
                      <div className="flex items-center justify-between gap-2">
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

      <Card className="md:hidden">
        <CardHeader>
          <CardTitle className="text-base">移动端失败告警推流</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          {mobilePushAlerts.length ? (
            mobilePushAlerts.map((alert) => {
              const severity = getSeverityMeta(alert.severity);
              return (
                <div key={`push-${alert.id}`} className="rounded-lg border p-3">
                  <div className="flex items-center justify-between gap-2">
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
                          setToast("该告警未绑定任务，请先修复节点连接。");
                          return;
                        }
                        void retryAlert(alert.id)
                          .then(() => setToast(`已在移动告警中心重试任务 #${alert.taskId}`))
                          .catch((error) => setToast((error as Error).message));
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

      <section className="grid gap-4 xl:grid-cols-[1.05fr_1.45fr]">
        <Card>
          <CardHeader>
            <div className="flex items-center justify-between gap-2">
              <CardTitle className="text-base">通知与集成设置</CardTitle>
              <Button size="sm" onClick={() => setShowCreateIntegration((prev) => !prev)}>
                <Plus className="mr-1 size-4" />
                新增通知方式
              </Button>
            </div>
          </CardHeader>
          <CardContent className="space-y-3">
            {showCreateIntegration ? (
              <div className="rounded-lg border bg-muted/30 p-3">
                <div className="grid gap-2 md:grid-cols-2">
                  <select
                    className="h-10 rounded-md border bg-background px-3 text-sm"
                    value={integrationDraft.type}
                    onChange={(event) =>
                      setIntegrationDraft((prev) => ({
                        ...prev,
                        type: event.target.value as IntegrationType
                      }))
                    }
                  >
                    <option value="email">邮件</option>
                    <option value="slack">Slack</option>
                    <option value="telegram">Telegram</option>
                    <option value="webhook">Webhook</option>
                  </select>
                  <Input
                    placeholder="通道名称"
                    value={integrationDraft.name}
                    onChange={(event) =>
                      setIntegrationDraft((prev) => ({
                        ...prev,
                        name: event.target.value
                      }))
                    }
                  />
                </div>

                <div className="mt-2 space-y-1">
                  <label className="text-xs text-muted-foreground">{activeIntegrationGuide.endpointLabel}</label>
                  <Input
                    placeholder={activeIntegrationGuide.endpointPlaceholder}
                    value={integrationDraft.endpoint}
                    onChange={(event) =>
                      setIntegrationDraft((prev) => ({
                        ...prev,
                        endpoint: event.target.value
                      }))
                    }
                  />
                  <p className="text-[11px] text-muted-foreground">{activeIntegrationGuide.endpointHint}</p>
                </div>

                <div className="mt-2 flex items-center justify-between rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">
                  <span>可直接套用示例地址后再修改。</span>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      setIntegrationDraft((prev) => ({
                        ...prev,
                        endpoint: activeIntegrationGuide.sample
                      }))
                    }
                  >
                    套用示例
                  </Button>
                </div>

                <div className="mt-2 grid gap-2 md:grid-cols-2">
                  <Input
                    type="number"
                    min={1}
                    max={10}
                    value={integrationDraft.failThreshold}
                    onChange={(event) =>
                      setIntegrationDraft((prev) => ({
                        ...prev,
                        failThreshold: Number(event.target.value || 1)
                      }))
                    }
                    placeholder="失败阈值"
                  />

                  <Input
                    type="number"
                    min={1}
                    max={120}
                    value={integrationDraft.cooldownMinutes}
                    onChange={(event) =>
                      setIntegrationDraft((prev) => ({
                        ...prev,
                        cooldownMinutes: Number(event.target.value || 1)
                      }))
                    }
                    placeholder="冷却时间（分钟）"
                  />
                </div>

                <div className="mt-3 grid grid-cols-2 gap-2">
                  <Button variant="outline" onClick={() => setShowCreateIntegration(false)}>
                    取消
                  </Button>
                  <Button
                    onClick={() => {
                      if (!integrationDraft.name.trim()) {
                        setToast("新增失败：请填写通道名称。");
                        return;
                      }
                      const validationError = validateIntegrationDraft(
                        integrationDraft.type,
                        integrationDraft.endpoint
                      );
                      if (validationError) {
                        setToast(validationError);
                        return;
                      }

                      void addIntegration(integrationDraft)
                        .then(() => {
                          setIntegrationDraft(defaultDraft);
                          setShowCreateIntegration(false);
                          setToast("通知方式已新增，可按需启停。");
                        })
                        .catch((error) => setToast((error as Error).message));
                    }}
                  >
                    保存通道
                  </Button>
                </div>
              </div>
            ) : null}

            {hasUnsavedIntegrationChanges ? (
              <div
                role="status"
                className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs text-amber-700 dark:text-amber-200"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p>当前有 {unsavedIntegrationCount} 项通知配置尚未保存，离开页面会提示确认。</p>
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      onClick={() => void saveAllIntegrationDrafts()}
                      disabled={integrationConfigBusy || !hasUnsavedIntegrationChanges}
                    >
                      {savingAllIntegrations ? "批量保存中..." : "全部保存"}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={resetAllIntegrationDrafts}
                      disabled={integrationConfigBusy || !hasUnsavedIntegrationChanges}
                    >
                      全部重置
                    </Button>
                  </div>
                </div>
              </div>
            ) : null}

            {integrations.length ? (
              integrations.map((integration) => {
                const Icon = integrationIcon(integration.type);
                const draft = integrationEditDrafts[integration.id] ?? {
                  endpoint: integration.endpoint,
                  failThreshold: integration.failThreshold,
                  cooldownMinutes: integration.cooldownMinutes
                };
                const dirty = isIntegrationDraftDirty(draft, integration);
                const savingDraft = savingIntegrationId === integration.id;
                const controlsDisabled = savingAllIntegrations || savingDraft;

                return (
                  <div key={integration.id} className="rounded-lg border p-3">
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="rounded-md bg-primary/10 p-1.5 text-primary">
                          <Icon className="size-4" />
                        </span>
                        <div>
                          <p className="font-medium">{integration.name}</p>
                          <p className="text-xs text-muted-foreground uppercase">{integration.type}</p>
                        </div>
                      </div>

                      <div className="flex items-center gap-2">
                        <Switch
                          checked={integration.enabled}
                          disabled={savingAllIntegrations}
                          onCheckedChange={() =>
                            void toggleIntegration(integration.id).catch((error) =>
                              setToast((error as Error).message)
                            )
                          }
                        />
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={testingIntegrationId === integration.id || savingAllIntegrations}
                          onClick={() => {
                            setTestingIntegrationId(integration.id);
                            void testIntegration(integration.id)
                              .then((result) =>
                                setToast(`${integration.name}：${result.message}（${result.latencyMs}ms）`)
                              )
                              .catch((error) => setToast((error as Error).message))
                              .finally(() => setTestingIntegrationId(null));
                          }}
                        >
                          {testingIntegrationId === integration.id ? "测试中..." : "测试发送"}
                        </Button>
                        <Button
                          variant="outline"
                          size="icon"
                          aria-label={`删除通知方式 ${integration.name}`}
                          disabled={savingAllIntegrations}
                          onClick={() => {
                            void removeIntegration(integration.id)
                              .then(() => setToast(`已删除通知方式：${integration.name}`))
                              .catch((error) => setToast((error as Error).message));
                          }}
                        >
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </div>

                    <div className="mt-3 space-y-2">
                      <label className="block text-xs text-muted-foreground">Endpoint / 地址</label>
                      <Input
                        value={draft.endpoint}
                        disabled={controlsDisabled}
                        onChange={(event) =>
                          patchIntegrationDraft(integration.id, {
                            endpoint: event.target.value
                          })
                        }
                      />
                    </div>

                    <div className="mt-3 grid gap-2 sm:grid-cols-2">
                      <label className="space-y-1 text-xs">
                        <span className="text-muted-foreground">告警阈值（失败次数）</span>
                        <Input
                          type="number"
                          min={1}
                          max={10}
                          value={draft.failThreshold}
                          disabled={controlsDisabled}
                          onChange={(event) =>
                            patchIntegrationDraft(integration.id, {
                              failThreshold: Math.max(1, Number(event.target.value || 1))
                            })
                          }
                        />
                      </label>

                      <label className="space-y-1 text-xs">
                        <span className="text-muted-foreground">冷却时间（分钟）</span>
                        <Input
                          type="number"
                          min={1}
                          max={120}
                          value={draft.cooldownMinutes}
                          disabled={controlsDisabled}
                          onChange={(event) =>
                            patchIntegrationDraft(integration.id, {
                              cooldownMinutes: Math.max(1, Number(event.target.value || 1))
                            })
                          }
                        />
                      </label>
                    </div>

                    <div className="mt-3 flex items-center justify-end gap-2">
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={!dirty || controlsDisabled}
                        onClick={() => resetIntegrationDraft(integration)}
                      >
                        重置
                      </Button>
                      <Button
                        size="sm"
                        disabled={!dirty || controlsDisabled}
                        onClick={() => void saveIntegrationDraft(integration)}
                      >
                        {savingDraft ? "保存中..." : "保存修改"}
                      </Button>
                    </div>
                  </div>
                );
              })
            ) : (
              <div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">
                尚未配置任何通知方式，请点击“新增通知方式”手动添加。
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle className="text-base">通知中心（按节点 / 任务分流）</CardTitle>
              <div className="flex items-center gap-2">
                <Link to="/app/alert-center">
                  <Button size="sm" variant="outline">打开独立通知中心</Button>
                </Link>
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
                          onClick={() => {
                            if (!alert.taskId) {
                              setToast("该告警未绑定任务，请先修复节点连接。");
                              return;
                            }
                            void retryAlert(alert.id)
                              .then(() => setToast(`已触发重试：任务 #${alert.taskId}`))
                              .catch((error) => setToast((error as Error).message));
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
                              .then(() => setToast(`已确认告警：${alert.errorCode}`))
                              .catch((error) => setToast((error as Error).message));
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
                              .then(() => setToast(`已标记恢复：${alert.errorCode}`))
                              .catch((error) => setToast((error as Error).message));
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
                                        setToast(result.message);
                                        refreshDeliveries(alert.id);
                                      })
                                      .catch((error) => setToast((error as Error).message))
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
                                            setToast(result.message);
                                            refreshDeliveries(alert.id);
                                          })
                                          .catch((error) => setToast((error as Error).message))
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

      {toast ? (
        <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-600 dark:text-emerald-300">
          {toast}
        </div>
      ) : null}
    </div>
  );
}
