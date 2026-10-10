import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { BellRing, Loader2 } from "lucide-react";
import { CreateSilenceDialog } from "@/components/create-silence-dialog";
import { StepUpPrerequisiteNotice } from "@/components/step-up-prerequisite-notice";
import { useAuth } from "@/context/auth-context.hooks";
import { sensitiveStepUpBlock } from "@/lib/sensitive-step-up";
import { useConfirm } from "@/hooks/use-confirm";
import {
  DataSurface,
  DataSurfaceContent,
  DataSurfaceHeader,
  DataSurfaceToolbar,
  DataSurfaceFooter,
} from "@/components/ui/data-surface";
import { FilteredEmptyState } from "@/components/ui/filtered-empty-state";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Pagination } from "@/components/ui/pagination";
import { toast } from "@/components/ui/toast-sonner";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { selectQuickSilenceMatch, type QuickSilenceMatch, type QuickSilenceSelection } from "@/lib/alert-silence-match";
import { apiClient } from "@/lib/api/client";
import {
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthTransition,
} from "@/lib/api/core";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { getErrorMessage } from "@/lib/utils";
import type { AlertDeliveryRecord, AlertRecord } from "@/types/domain";
import type { ViewMode } from "@/components/ui/view-mode-toggle";
import { AlertFilters } from "./alert-filters";
import { SILENCE_ALERT_TRIGGER_ATTRIBUTE } from "./alert-bulk-actions";
import { AlertList } from "./alert-list";

type SortField = "triggered_at" | "severity" | "status" | "node_name";
type UnsupportedSilenceReason = Extract<QuickSilenceSelection, { kind: "unsupported" }>["reason"];

type SilenceFocusOwner = {
  alertId: string;
  sessionGeneration: number;
  authGeneration: number;
  token: string;
};

const SILENCE_FALLBACK_SELECTOR = "button, a[href], input, select, textarea";

function isUsableSilenceTarget(element: HTMLElement): boolean {
  if (!element.isConnected) return false;
  if (element instanceof HTMLButtonElement && element.disabled) return false;
  let current: HTMLElement | null = element;
  while (current) {
    if (current.hidden) return false;
    const style = window.getComputedStyle(current);
    if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") return false;
    current = current.parentElement;
  }
  return true;
}

function pickSilenceTrigger(
  scope: HTMLElement | null,
  alertId: string,
  saved: HTMLButtonElement | null,
): HTMLButtonElement | null {
  if (
    saved
    && saved.getAttribute(SILENCE_ALERT_TRIGGER_ATTRIBUTE) === alertId
    && isUsableSilenceTarget(saved)
  ) {
    return saved;
  }
  if (!scope) return null;
  for (const node of scope.querySelectorAll(`button[${SILENCE_ALERT_TRIGGER_ATTRIBUTE}]`)) {
    if (!(node instanceof HTMLButtonElement)) continue;
    if (node.getAttribute(SILENCE_ALERT_TRIGGER_ATTRIBUTE) !== alertId) continue;
    if (!isUsableSilenceTarget(node)) continue;
    return node;
  }
  return null;
}

function firstUsableControl(root: HTMLElement | null): HTMLElement | null {
  if (!root?.isConnected) return null;
  for (const node of root.querySelectorAll(SILENCE_FALLBACK_SELECTOR)) {
    if (!(node instanceof HTMLElement)) continue;
    if (node.tabIndex < 0) continue;
    if (!isUsableSilenceTarget(node)) continue;
    return node;
  }
  return null;
}

function restoreOwnedSilenceFocus(input: {
  owner: SilenceFocusOwner | null;
  mounted: boolean;
  sessionGeneration: number;
  identityToken: string | null;
  identityRole: string | null;
  list: HTMLElement | null;
  toolbar: HTMLElement | null;
  savedTrigger: HTMLButtonElement | null;
}): void {
  const { owner } = input;
  if (!owner || !input.mounted) return;
  if (isAuthTransitionActive()) return;
  if (input.sessionGeneration !== owner.sessionGeneration) return;
  if (getAuthSessionGeneration() !== owner.authGeneration) return;
  if (input.identityRole !== "admin" || input.identityToken !== owner.token) return;
  const trigger = pickSilenceTrigger(input.list, owner.alertId, input.savedTrigger);
  const fallback = trigger ?? firstUsableControl(input.toolbar) ?? firstUsableControl(input.list);
  fallback?.focus();
}

type SupportedQuickSilence = {
  alertId: string;
  match: QuickSilenceMatch;
  attempt: number;
  sessionGeneration: number;
  authGeneration: number;
  token: string;
};

type AlertCenterProps = {
  token: string;
  canWriteAlerts: boolean;
  canTriggerTasks: boolean;
  canRetryDelivery: boolean;
  integrations: { id: string; name: string }[];
  globalSearch: string;
  setGlobalSearch: (value: string) => void;
  initialAlertId?: string | null;
  onAlertHighlighted?: () => void;
  onAlertMutated?: () => void;
  refreshVersion?: number;
};

type AlertConfirm = ReturnType<typeof useConfirm>["confirm"];

function AlertWriteConfirm({ confirmRef }: { confirmRef: { current: AlertConfirm | null } }) {
  const { confirm, dialog } = useConfirm();
  useEffect(() => {
    confirmRef.current = confirm;
    return () => {
      confirmRef.current = null;
    };
  }, [confirm, confirmRef]);
  return dialog;
}

export function AlertCenter(props: AlertCenterProps) {
  return <AlertCenterSession key={props.token} {...props} />;
}

function AlertCenterSession({
  token,
  canWriteAlerts,
  canTriggerTasks,
  canRetryDelivery,
  integrations,
  globalSearch,
  setGlobalSearch,
  initialAlertId,
  onAlertHighlighted,
  onAlertMutated,
  refreshVersion,
}: AlertCenterProps) {
  const { t } = useTranslation();
  const { role, totpEnabled } = useAuth();
  const authRole = role ?? null;
  const canManageSilences = authRole === "admin";
  const withStepUp = useStepUpAction(
    STEP_UP_ACTIONS.taskManualTrigger,
    { persist: false, reuseCached: false },
  );

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
  const [selectedAlertIds, setSelectedAlertIds] = useState<string[]>([]);
  const [bulkResolving, setBulkResolving] = useState(false);

  // --- 投递记录状态 ---
  const [deliveryOpenAlertId, setDeliveryOpenAlertId] = useState<string | null>(null);
  const [deliveryLoadingAlertId, setDeliveryLoadingAlertId] = useState<string | null>(null);
  const [deliveryMap, setDeliveryMap] = useState<Record<string, AlertDeliveryRecord[]>>({});
  const [retryingDeliveryKey, setRetryingDeliveryKey] = useState<string | null>(null);
  const [retryingAllAlertId, setRetryingAllAlertId] = useState<string | null>(null);
  const [appliedAccess, setAppliedAccess] = useState({
    token,
    role: authRole,
    canWriteAlerts,
    canTriggerTasks,
    canRetryDelivery,
  });
  const confirmRef = useRef<AlertConfirm | null>(null);
  const mountedRef = useRef(true);
  const capsRef = useRef({ canWriteAlerts, canTriggerTasks, canRetryDelivery });
  const authIdentityRef = useRef({ role: authRole, token });
  const sessionGenerationRef = useRef(0);
  const bulkAttemptRef = useRef(0);
  const deliveryAttemptRef = useRef(0);
  const retryAllAttemptRef = useRef(0);
  const deliveryLoadAttemptRef = useRef(0);
  const silenceReturnRef = useRef<HTMLButtonElement | null>(null);
  const silenceListRef = useRef<HTMLDivElement>(null);
  const silenceToolbarRef = useRef<HTMLDivElement>(null);
  const silenceFocusOwnerRef = useRef<SilenceFocusOwner | null>(null);
  const silenceAttemptRef = useRef(0);
  const [supportedSilence, setSupportedSilence] = useState<SupportedQuickSilence | null>(null);
  const [unsupportedSilence, setUnsupportedSilence] = useState<{
    alertId: string;
    reason: UnsupportedSilenceReason;
  } | null>(null);
  const [silenceNotice, setSilenceNotice] = useState(false);

  const isCurrentSession = (generation: number) =>
    mountedRef.current
    && sessionGenerationRef.current === generation
    && authIdentityRef.current.role === authRole
    && authIdentityRef.current.token === token;

  // --- 分组计数缓存 ---
  // Lazy: only fetched when a delivery panel opens (the only context where
  // "+N 条同类" is useful). Keyed by alertId.
  const [groupInfoMap, setGroupInfoMap] = useState<Record<string, { count: number }>>({});

  // --- 深链接高亮 ---
  const [highlightedAlert, setHighlightedAlert] = useState<AlertRecord | null>(null);
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

  const integrationNameMap = useMemo(
    () => new Map(integrations.map((i) => [i.id, i.name])),
    [integrations],
  );

  const [reload, setReload] = useState(0);
  const filterScope = JSON.stringify([token, pageSize, sortBy, sortOrder, statusFilter, severityFilter, deferredKeyword]);
  const [requestScope, setRequestScope] = useState({ filters: filterScope, page, refreshVersion, reload });
  if (requestScope.filters !== filterScope || requestScope.page !== page || requestScope.refreshVersion !== refreshVersion || requestScope.reload !== reload) {
    if (requestScope.filters !== filterScope) setPage(1);
    setRequestScope({ filters: filterScope, page: requestScope.filters !== filterScope ? 1 : page, refreshVersion, reload });
    setHighlightedAlert(null);
    if (requestScope.filters !== filterScope || requestScope.page !== page) setSelectedAlertIds([]);
    setAlerts([]);
    setLoading(true);
  }
  if (
    appliedAccess.token !== token
    || appliedAccess.role !== authRole
    || appliedAccess.canWriteAlerts !== canWriteAlerts
    || appliedAccess.canTriggerTasks !== canTriggerTasks
    || appliedAccess.canRetryDelivery !== canRetryDelivery
  ) {
    setAppliedAccess({ token, role: authRole, canWriteAlerts, canTriggerTasks, canRetryDelivery });
    if (bulkResolving) setBulkResolving(false);
    if (retryingDeliveryKey !== null) setRetryingDeliveryKey(null);
    if (retryingAllAlertId !== null) setRetryingAllAlertId(null);
  }
  if (
    (!canManageSilences || appliedAccess.token !== token || appliedAccess.role !== authRole)
    && (supportedSilence || unsupportedSilence || silenceNotice)
  ) {
    if (supportedSilence) setSupportedSilence(null);
    if (unsupportedSilence) setUnsupportedSilence(null);
    if (silenceNotice) setSilenceNotice(false);
  }
  if (!canWriteAlerts && selectedAlertIds.length > 0) setSelectedAlertIds([]);
  if (!canWriteAlerts && bulkResolving) setBulkResolving(false);
  if (!canRetryDelivery && retryingDeliveryKey !== null) setRetryingDeliveryKey(null);
  if (!canRetryDelivery && retryingAllAlertId !== null) setRetryingAllAlertId(null);
  useEffect(() => {
    const controller = new AbortController();
    void apiClient.getAlertsPaginated(token, {
      page, pageSize, sortBy, sortOrder,
      status: statusFilter !== "all" ? statusFilter : undefined,
      severity: severityFilter !== "all" ? severityFilter : undefined,
      keyword: deferredKeyword.trim() || undefined,
      signal: controller.signal,
    }).then((result) => {
      if (controller.signal.aborted || !mountedRef.current) return;
      setAlerts(result.items);
      setTotal(result.total);
      setSelectedAlertIds((current) => {
        const unresolvedIds = new Set(result.items.filter((alert) => alert.status !== "resolved").map((alert) => alert.id));
        return current.filter((alertId) => unresolvedIds.has(alertId));
      });
    }).catch((err) => {
      if (controller.signal.aborted || !mountedRef.current || (err instanceof DOMException && err.name === "AbortError")) return;
      toast.error(getErrorMessage(err));
    }).finally(() => { if (!controller.signal.aborted && mountedRef.current) setLoading(false); });
    return () => { controller.abort(); };
  }, [token, page, pageSize, sortBy, sortOrder, statusFilter, severityFilter, deferredKeyword, refreshVersion, reload]);

  useLayoutEffect(() => () => {
    deliveryLoadAttemptRef.current += 1;
    silenceAttemptRef.current += 1;
  }, []);

  useLayoutEffect(() => {
    return subscribeAuthTransition(() => {
      if (!isAuthTransitionActive()) return;
      silenceAttemptRef.current += 1;
      setSupportedSilence((current) => (current === null ? current : null));
      setUnsupportedSilence((current) => (current === null ? current : null));
      setSilenceNotice((current) => (current ? false : current));
    });
  }, []);

  const silenceUiOpen = supportedSilence !== null || unsupportedSilence !== null;
  const silenceUiWasOpenRef = useRef(false);
  const silenceFocusTimerRef = useRef<number | null>(null);
  useEffect(() => {
    if (silenceUiWasOpenRef.current && !silenceUiOpen) {
      // After Radix's unmount focus. Both dialogs share this restore; the saved More node may be hidden or replaced.
      if (silenceFocusTimerRef.current !== null) window.clearTimeout(silenceFocusTimerRef.current);
      silenceFocusTimerRef.current = window.setTimeout(() => {
        silenceFocusTimerRef.current = null;
        restoreOwnedSilenceFocus({
          owner: silenceFocusOwnerRef.current,
          mounted: mountedRef.current,
          sessionGeneration: sessionGenerationRef.current,
          identityToken: authIdentityRef.current.token,
          identityRole: authIdentityRef.current.role,
          list: silenceListRef.current,
          toolbar: silenceToolbarRef.current,
          savedTrigger: silenceReturnRef.current,
        });
      }, 0);
    }
    silenceUiWasOpenRef.current = silenceUiOpen;
    return () => {
      if (silenceFocusTimerRef.current !== null) {
        window.clearTimeout(silenceFocusTimerRef.current);
        silenceFocusTimerRef.current = null;
      }
    };
  }, [silenceUiOpen]);

  // 身份、权限和代次在绘制前提交。清理时先作废挂载和代次，再取消高亮定时器。
  useLayoutEffect(() => {
    mountedRef.current = true;
    capsRef.current = { canWriteAlerts, canTriggerTasks, canRetryDelivery };
    authIdentityRef.current = { role: authRole, token };
    sessionGenerationRef.current += 1;
    return () => {
      mountedRef.current = false;
      sessionGenerationRef.current += 1;
      silenceAttemptRef.current += 1;
      bulkAttemptRef.current += 1;
      deliveryAttemptRef.current += 1;
      retryAllAttemptRef.current += 1;
      const highlightTimer = highlightClearTimerRef.current;
      highlightClearTimerRef.current = null;
      if (highlightTimer !== null) window.clearTimeout(highlightTimer);
    };
  }, [token, authRole, canWriteAlerts, canTriggerTasks, canRetryDelivery]);

  const handlePageChange = (p: number) => {
    setPage(p);
    setHighlightedAlert(null);

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
    let active = true;
    resetFilters();
    void apiClient.getAlert(token, initialAlertId).then((target) => {
      if (!active) return;
      setHighlightedAlert(target);
      setDeliveryOpenAlertId(target.id);
      void apiClient.getAlertDeliveries(token, target.id)
        .then((rows) => { if (active) setDeliveryMap((prev) => ({ ...prev, [target.id]: rows })); })
        .catch(() => {});
      onAlertHighlighted?.();
    }).catch(() => {
      if (active) onAlertHighlighted?.();
    });
    return () => { active = false; };
  }, [initialAlertId, token, resetFilters, onAlertHighlighted]);

  // --- 投递记录操作 ---
  const refreshDeliveries = (alertId: string) => {
    const attempt = ++deliveryLoadAttemptRef.current;
    setDeliveryLoadingAlertId(alertId);
    void apiClient.getAlertDeliveries(token, alertId)
      .then((rows) => {
        if (deliveryLoadAttemptRef.current !== attempt || !mountedRef.current) return;
        setDeliveryMap((prev) => ({ ...prev, [alertId]: rows }));
      })
      .catch((error) => {
        if (deliveryLoadAttemptRef.current !== attempt || !mountedRef.current) return;
        toast.error(getErrorMessage(error));
      })
      .finally(() => {
        if (deliveryLoadAttemptRef.current !== attempt || !mountedRef.current) return;
        setDeliveryLoadingAlertId(null);
      });
    // Fetch group count in parallel. Best-effort: a failure here must not
    // block delivery rendering, so the error is logged at debug only.
    void apiClient.getAlertGroupInfo(token, alertId)
      .then((gi) => {
        if (deliveryLoadAttemptRef.current !== attempt || !mountedRef.current) return;
        setGroupInfoMap((prev) => ({ ...prev, [alertId]: { count: gi.count } }));
      })
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
    if (!capsRef.current.canWriteAlerts) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    try {
      await apiClient.ackAlert(token, alert.id);
      if (!isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.success(t("notifications.ackSuccess", { code: alert.errorCode }));
      setReload((value) => value + 1);
      onAlertMutated?.();
    } catch (err) {
      if (!isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.error(getErrorMessage(err));
    }
  };

  const handleResolve = async (alert: AlertRecord) => {
    if (!capsRef.current.canWriteAlerts) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    try {
      await apiClient.resolveAlert(token, alert.id);
      if (!isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.success(t("notifications.resolveSuccess", { code: alert.errorCode }));
      setSelectedAlertIds((current) => current.filter((alertId) => alertId !== alert.id));
      setReload((value) => value + 1);
      onAlertMutated?.();
    } catch (err) {
      if (!isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.error(getErrorMessage(err));
    }
  };

  const handleSelectionChange = (alertId: string, selected: boolean) => {
    if (!capsRef.current.canWriteAlerts) return;
    setSelectedAlertIds((current) => {
      if (selected) {
        return current.includes(alertId) ? current : [...current, alertId];
      }
      return current.filter((id) => id !== alertId);
    });
  };

  const handleSelectAllVisible = (selected: boolean) => {
    if (!capsRef.current.canWriteAlerts) return;
    const visibleUnresolvedIds = displayAlerts.filter((alert) => alert.status !== "resolved").map((alert) => alert.id);
    setSelectedAlertIds((current) => {
      if (!selected) {
        const visibleSet = new Set(visibleUnresolvedIds);
        return current.filter((alertId) => !visibleSet.has(alertId));
      }
      const merged = new Set([...current, ...visibleUnresolvedIds]);
      return Array.from(merged);
    });
  };

  const handleBulkResolveSelected = async () => {
    if (!capsRef.current.canWriteAlerts) return;
    if (!selectedAlertIds.length || bulkResolving) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    const alertIds = selectedAlertIds;
    const attempt = ++bulkAttemptRef.current;
    setBulkResolving(true);
    try {
      const result = await apiClient.resolveAlertsBulk(token, { alertIds });
      if (bulkAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.success(t("notifications.bulkResolveSuccess", { count: result.resolvedCount }));
      setSelectedAlertIds([]);
      setReload((value) => value + 1);
      onAlertMutated?.();
    } catch (err) {
      if (bulkAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.error(getErrorMessage(err));
    } finally {
      if (bulkAttemptRef.current === attempt && isCurrentSession(generation)) setBulkResolving(false);
    }
  };

  const handleResolveNodeAlerts = async (alert: AlertRecord) => {
    if (!capsRef.current.canWriteAlerts) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    const requestConfirm = confirmRef.current;
    if (!requestConfirm) return;
    const ok = await requestConfirm({
      title: t("notifications.resolveNodeConfirmTitle"),
      description: t("notifications.resolveNodeConfirmDesc", { node: alert.nodeName }),
      confirmText: t("notifications.resolveNodeConfirmAction"),
    });
    if (!ok || !isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;

    try {
      const result = await apiClient.resolveAlertsBulk(token, { nodeId: alert.nodeId });
      if (!isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.success(t("notifications.resolveNodeSuccess", { node: alert.nodeName, count: result.resolvedCount }));
      setSelectedAlertIds((current) => current.filter((alertId) => {
        const row = displayAlerts.find((item) => item.id === alertId);
        return row ? row.nodeId !== alert.nodeId : true;
      }));
      setReload((value) => value + 1);
      onAlertMutated?.();
    } catch (err) {
      if (!isCurrentSession(generation) || !capsRef.current.canWriteAlerts) return;
      toast.error(getErrorMessage(err));
    }
  };

  const handleRetry = async (alert: AlertRecord) => {
    if (!capsRef.current.canTriggerTasks) return;
    if (!alert.taskId) {
      toast.error(t("notifications.noAlertTask"));
      return;
    }
    if (sensitiveStepUpBlock({ token, totpEnabled }) !== "ready") return;
    const generation = sessionGenerationRef.current;
    const authGeneration = getAuthSessionGeneration();
    const retryStillCurrent = () => isCurrentSession(generation)
      && !isAuthTransitionActive()
      && getAuthSessionGeneration() === authGeneration
      && capsRef.current.canTriggerTasks;
    if (!retryStillCurrent()) return;
    const taskId = alert.taskId;
    let triggered = false;
    try {
      await withStepUp(async (proof) => {
        if (!retryStillCurrent()) return;
        try {
          await apiClient.requestTaskManualTriggerCredentialGrant(token, {
            taskId,
            reason: t("tasks.manualTriggerGrantReason", { id: taskId }),
            requestedTtlSeconds: 600,
          }, proof);
          if (!retryStillCurrent()) return;
          await apiClient.triggerTask(token, taskId, proof);
          if (!retryStillCurrent()) return;
          triggered = true;
        } catch (error) {
          if (!retryStillCurrent()) return;
          throw error;
        }
      });
      if (!triggered || !retryStillCurrent()) return;
      toast.success(t("notifications.retryTriggered", { id: taskId }));
      setReload((value) => value + 1);
      onAlertMutated?.();
    } catch (err) {
      if (!retryStillCurrent()) return;
      toast.error(getErrorMessage(err));
    }
  };

  const handleRetryDelivery = async (alertId: string, deliveryId: string) => {
    if (!capsRef.current.canRetryDelivery) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    const attempt = ++deliveryAttemptRef.current;
    const deliveryKey = String(deliveryId);
    setRetryingDeliveryKey(deliveryKey);
    try {
      await apiClient.retryDelivery(token, deliveryId);
      if (deliveryAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canRetryDelivery) return;
      toast.success(t("notifications.resendSuccess", { defaultValue: "重发成功" }));
      refreshDeliveries(alertId);
    } catch (err) {
      if (deliveryAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canRetryDelivery) return;
      toast.error(getErrorMessage(err));
    } finally {
      if (deliveryAttemptRef.current === attempt && isCurrentSession(generation)) {
        setRetryingDeliveryKey((current) => (current === deliveryKey ? null : current));
      }
    }
  };

  const handleRetryAllFailed = async (alertId: string) => {
    if (!capsRef.current.canRetryDelivery) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    const failedDeliveries = (deliveryMap[alertId] ?? []).filter((d) => d.status === "failed" || d.status === "retrying");
    if (!failedDeliveries.length) return;
    const attempt = ++retryAllAttemptRef.current;
    setRetryingAllAlertId(alertId);
    let failed = 0;
    let succeeded = 0;
    try {
      for (const delivery of failedDeliveries) {
        if (retryAllAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canRetryDelivery) return;
        try {
          await apiClient.retryDelivery(token, delivery.id);
        } catch {
          if (retryAllAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canRetryDelivery) return;
          failed += 1;
          continue;
        }
        if (retryAllAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canRetryDelivery) return;
        succeeded += 1;
      }
      if (retryAllAttemptRef.current !== attempt || !isCurrentSession(generation) || !capsRef.current.canRetryDelivery) return;
      if (failed === 0) {
        toast.success("批量重发成功");
      } else {
        toast.error(`批量重发：${succeeded} 成功，${failed} 失败`);
      }
      refreshDeliveries(alertId);
    } finally {
      if (retryAllAttemptRef.current === attempt && isCurrentSession(generation)) {
        setRetryingAllAlertId((current) => (current === alertId ? null : current));
      }
    }
  };

  // 快捷静音只属于当前 admin 代次。身份、过渡和卸载都会作废尚未落地的成功提示。
  const handleSilence = (alert: AlertRecord) => {
    if (!canManageSilences || authRole !== "admin" || token.length === 0) return;
    if (isAuthTransitionActive()) return;
    const generation = sessionGenerationRef.current;
    if (!isCurrentSession(generation)) return;
    const snapshot = getAuthIdentitySnapshot();
    if (snapshot.role !== "admin" || snapshot.token !== token) return;
    const authGeneration = getAuthSessionGeneration();
    const selection = selectQuickSilenceMatch(alert);
    if (supportedSilence) silenceAttemptRef.current += 1;
    const attempt = silenceAttemptRef.current;
    silenceFocusOwnerRef.current = {
      alertId: alert.id,
      sessionGeneration: generation,
      authGeneration,
      token,
    };
    if (selection.kind === "supported") {
      setUnsupportedSilence(null);
      setSupportedSilence({
        alertId: alert.id,
        match: selection.match,
        attempt,
        sessionGeneration: generation,
        authGeneration,
        token,
      });
      return;
    }
    setSupportedSilence(null);
    setUnsupportedSilence({ alertId: alert.id, reason: selection.reason });
  };

  const acceptSilenceCreated = (opened: SupportedQuickSilence) => {
    if (silenceAttemptRef.current !== opened.attempt) return;
    if (!mountedRef.current || !isCurrentSession(opened.sessionGeneration)) return;
    if (isAuthTransitionActive() || getAuthSessionGeneration() !== opened.authGeneration) return;
    if (authRole !== "admin" || token !== opened.token) return;
    const snapshot = getAuthIdentitySnapshot();
    if (snapshot.role !== "admin" || snapshot.token !== opened.token) return;
    setSilenceNotice(true);
  };

  const unsupportedCopy = (reason: UnsupportedSilenceReason) => {
    if (reason === "platform") return t("notifications.silenceUnsupportedPlatform");
    if (reason === "unknown-node") return t("notifications.silenceUnsupportedNode");
    return t("notifications.silenceUnsupportedSource");
  };

  // --- 合并高亮告警和普通列表 ---
  const displayAlerts = highlightedAlert && !alerts.some((alert) => alert.id === highlightedAlert.id)
    ? [highlightedAlert, ...alerts]
    : alerts;
  const silenceDialogMounted = canManageSilences && token.length > 0;

  return (
      <DataSurface>
      {canTriggerTasks && totpEnabled === false ? <StepUpPrerequisiteNotice className="mb-3" /> : null}
      {silenceNotice ? (
        <div role="status" className="mb-3 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border bg-card px-3 py-2 text-sm">
          <span className="min-w-0 break-words">{t("notifications.silenceCreated")}</span>
          <Button asChild variant="link" size="sm" className="h-auto px-0">
            <Link to="/app/settings?tab=silences">{t("notifications.silenceRulesLink")}</Link>
          </Button>
        </div>
      ) : null}
      <DataSurfaceHeader
        title={t("notifications.alertCenterTitle")}
        description={t("notifications.alertCenterDesc", { total })}
      />
      <DataSurfaceToolbar>
        <div ref={silenceToolbarRef} data-silence-focus-fallback="" className="space-y-4">
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
          onReset={() => { resetFilters(); setPage(1); setSelectedAlertIds([]); }}
        />
        {canWriteAlerts && selectedAlertIds.length > 0 ? (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-card px-3 py-2">
            <span className="text-sm text-muted-foreground">
              {t("notifications.selectedAlerts", { count: selectedAlertIds.length })}
            </span>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setSelectedAlertIds([])}
              >
                {t("notifications.clearSelection")}
              </Button>
              <Button
                type="button"
                size="sm"
                disabled={bulkResolving}
                onClick={() => void handleBulkResolveSelected()}
              >
                {bulkResolving && <Loader2 className="mr-1 size-4 animate-spin" aria-hidden="true" />}
                {t("notifications.resolveSelectedAlerts")}
              </Button>
            </div>
          </div>
        ) : null}
        </div>
      </DataSurfaceToolbar>

      <DataSurfaceContent className="space-y-4">
        {loading && !alerts.length ? (
          <div className="flex items-center justify-center py-12 text-muted-foreground">
            <Loader2 className="mr-2 size-5 animate-spin" aria-hidden="true" />
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
            selectedAlertIds={selectedAlertIds}
            bulkResolving={bulkResolving}
            canWriteAlerts={canWriteAlerts}
            canTriggerTasks={canTriggerTasks}
            canRetryDelivery={canRetryDelivery}
            canManageSilences={canManageSilences}
            onSelectionChange={handleSelectionChange}
            onSelectAllVisible={handleSelectAllVisible}
            onRetry={(alert) => void handleRetry(alert)}
            onAck={(alert) => void handleAck(alert)}
            onResolve={(alert) => void handleResolve(alert)}
            onResolveNodeAlerts={(alert) => void handleResolveNodeAlerts(alert)}
            onToggleDeliveries={toggleDeliveries}
            onRetryDelivery={(alertId, deliveryId) => void handleRetryDelivery(alertId, deliveryId)}
            onRetryAllFailed={(alertId) => void handleRetryAllFailed(alertId)}
            onSilence={handleSilence}
            silenceReturnRef={silenceReturnRef}
            silenceFocusScopeRef={silenceListRef}
          />
        ) : (
          <FilteredEmptyState
            icon={BellRing}
            title={t("notifications.emptyTitle")}
            description={t("notifications.emptyDesc")}
            onReset={() => { resetFilters(); setPage(1); }}
          />
        )}
      </DataSurfaceContent>

      <DataSurfaceFooter>
        {canWriteAlerts ? <AlertWriteConfirm key={`${authRole}:${canTriggerTasks}:${canRetryDelivery}`} confirmRef={confirmRef} /> : null}
        <Pagination
          page={page}
          pageSize={pageSize}
          total={total}
          loading={loading}
          onPageChange={handlePageChange}
          onPageSizeChange={handlePageSizeChange}
        />
      </DataSurfaceFooter>
      {silenceDialogMounted && supportedSilence ? (
        <CreateSilenceDialog
          key={`${supportedSilence.alertId}:${supportedSilence.match.nodeId}:${supportedSilence.match.category}`}
          open
          onOpenChange={(open) => {
            if (!open) setSupportedSilence(null);
          }}
          onCreated={() => acceptSilenceCreated(supportedSilence)}
          token={token}
          initialMatch={supportedSilence.match}
        />
      ) : null}
      {silenceDialogMounted && unsupportedSilence ? (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open) setUnsupportedSilence(null);
          }}
        >
          <DialogContent
            onCloseAutoFocus={(event) => {
              // Same restore as the create dialog, queued by the close effect. Don't focus the saved node here.
              event.preventDefault();
            }}
          >
            <DialogHeader>
              <DialogTitle>{t("notifications.silenceUnsupportedTitle")}</DialogTitle>
              <DialogDescription className="break-words text-sm leading-5 text-foreground/80">
                {unsupportedCopy(unsupportedSilence.reason)}
              </DialogDescription>
              <DialogCloseButton />
            </DialogHeader>
            <DialogFooter className="flex-wrap">
              <Button asChild variant="outline" size="sm">
                <Link to="/app/settings?tab=silences">{t("notifications.silenceRulesLink")}</Link>
              </Button>
              <Button type="button" variant="secondary" size="sm" onClick={() => setUnsupportedSilence(null)}>
                {t("common.close")}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      ) : null}
    </DataSurface>
  );
}
