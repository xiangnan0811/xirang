import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSharedContext } from "@/context/shared-context.hooks";
import { useNodesContext } from "@/context/nodes-context.hooks";
import { useTasksContext } from "@/context/tasks-context.hooks";
import { usePoliciesContext } from "@/context/policies-context.hooks";
import { DataSurface, DataSurfaceContent, DataSurfaceToolbar } from "@/components/ui/data-surface";
import { LoadingState } from "@/components/ui/loading-state";
import { Pagination } from "@/components/ui/pagination";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { InventoryRetryAlert } from "@/components/ui/inventory-retry-alert";
import { toast } from "@/components/ui/toast-sonner";
import {
  dialogOpenerFromTarget,
  restoreConnectedDialogOpener,
} from "@/components/ui/dialog-opener";
import type { DialogCloseAutoFocus } from "@/components/ui/form-dialog";
import { ViewModeToggle } from "@/components/ui/view-mode-toggle";
import { useClientPagination } from "@/hooks/use-client-pagination";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useStepUpAction } from "@/hooks/use-step-up-action";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import { apiClient } from "@/lib/api/client";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import { getErrorMessage } from "@/lib/utils";
import { ApiError, getAuthSessionGeneration } from "@/lib/api/core";
import type { NewTaskInput, TaskRecord, TaskRunRecord, UpdateTaskInput } from "@/types/domain";
import { TasksGrid } from "@/pages/tasks-page.grid";
import type { PendingActionType } from "@/pages/tasks-page.utils";
import { TasksTable } from "@/pages/tasks-page.table";
import { normalizeStatusFilter, taskPreviewConnectEligibility } from "@/pages/tasks-page.utils";
import { TasksPageDialogs } from "@/pages/tasks-page.dialogs";
import { TasksFilters } from "@/pages/tasks-page.filters";
import { TasksHero } from "@/pages/tasks-page.hero";
import { TasksBulkBar } from "@/pages/tasks-page.bulk-bar";
import { TaskRunStatistics } from "@/pages/tasks-page.statistics-panel";

const keywordStorageKey = "xirang.tasks.keyword";
const statusStorageKey = "xirang.tasks.status";
const nodeStorageKey = "xirang.tasks.node";
const viewStorageKey = "xirang.tasks.view";

type TasksViewMode = "cards" | "list";

function isTaskWriteRole(role: AuthRole | null): boolean {
  return role === "admin" || role === "operator";
}

function isTaskTriggerRole(role: AuthRole | null): boolean {
  return role === "admin" || role === "operator";
}

export function TasksPage() {
  const { t } = useTranslation();
  const { globalSearch, setGlobalSearch } = useSharedContext();
  const { nodes, refreshNodes } = useNodesContext();
  const {
    tasks,
    tasksLoading,
    tasksError,
    tasksLoaded,
    createTask,
    updateTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
    pauseTask,
    resumeTask,
    skipNextTask,
    refreshTasks,
  } = useTasksContext();
  const { policies, refreshPolicies } = usePoliciesContext();
  const loading = (tasksLoading || !tasksLoaded) && tasks.length === 0 && !tasksError;
  const requestFailed = Boolean(tasksError) && tasks.length === 0;

  useEffect(() => {
    void refreshTasks();
    void refreshNodes();
    void refreshPolicies();
  }, [refreshTasks, refreshNodes, refreshPolicies]);

  // 当有活跃任务（running/pending/retrying 或有活跃 run 如 restore）时，每 5 秒自动刷新
  useEffect(() => {
    const hasActiveTask = tasks.some(
      (t) => t.status === "running" || t.status === "pending" || t.status === "retrying" || t.hasActiveRun
    );
    if (!hasActiveTask) return;

    const interval = setInterval(() => {
      void refreshTasks();
    }, 5_000);
    return () => clearInterval(interval);
  }, [tasks, refreshTasks]);

  const { confirm, dialog, cancelPending } = useConfirm();

  const {
    keyword, setKeyword,
    status: statusFilterRaw, setStatus: setStatusFilterRaw,
    node: nodeFilter, setNode: setNodeFilter,
    deferredKeyword,
    reset: resetFilters,
  } = usePageFilters({
    keyword: { key: keywordStorageKey, default: "" },
    status: { key: statusStorageKey, default: "all" },
    node: { key: nodeStorageKey, default: "all" },
  }, globalSearch, setGlobalSearch);
  const [viewModeRaw, setViewModeRaw] =
    usePersistentState<string>(viewStorageKey, "cards");

  const statusFilter = normalizeStatusFilter(statusFilterRaw);
  const viewMode: TasksViewMode = viewModeRaw === "list" ? "list" : "cards";

  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const createTaskOpenerRef = useRef<HTMLElement | null>(null);
  const editTaskOpenerRef = useRef<HTMLElement | null>(null);
  const tokenRef = useRef<string | null>(null);
  const roleRef = useRef<AuthRole | null>(null);
  const mountedRef = useRef(false);
  const identityGenerationRef = useRef(0);
  const restoreCreateTaskOpener: DialogCloseAutoFocus = (event) => {
    restoreConnectedDialogOpener(event, createTaskOpenerRef.current);
  };
  const restoreEditTaskOpener: DialogCloseAutoFocus = (event) => {
    restoreConnectedDialogOpener(event, editTaskOpenerRef.current);
  };
  const openTaskCreateFromInventory = (open: boolean, opener?: EventTarget | null) => {
    if (open && !isTaskWriteRole(roleRef.current)) return;
    if (open) createTaskOpenerRef.current = dialogOpenerFromTarget(opener);
    setCreateDialogOpen(open);
  };
  const { token: authToken, role } = useAuth();
  const canWriteTasks = isTaskWriteRole(role);
  const canTriggerTasks = isTaskTriggerRole(role);
  const canManageRsyncVersioning = role === "admin";
  const canManageRcloneVersioning = role === "admin";
  const canConnectTaskPreview = role === "admin";
  const withStepUp = useStepUpAction(
    STEP_UP_ACTIONS.taskBatchTrigger,
    { persist: false, reuseCached: false },
  );
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [editingTask, setEditingTask] = useState<TaskRecord | null>(null);
  const [pendingAction, setPendingAction] = useState<PendingActionType>(null);
  const [historyTask, setHistoryTask] = useState<TaskRecord | null>(null);
  const [selectedRun, setSelectedRun] = useState<TaskRunRecord | null>(null);
  const [showDiff, setShowDiff] = useState(false);
  const [batchDialogOpen, setBatchDialogOpen] = useState(false);
  const [batchDefaultNodeIds, setBatchDefaultNodeIds] = useState<number[] | undefined>(undefined);
  const [batchResultId, setBatchResultId] = useState<string | null>(null);
  const [batchRetain, setBatchRetain] = useState(false);
  const [rsyncVersioningTask, setRsyncVersioningTask] = useState<TaskRecord | null>(null);
  const [rcloneVersioningTask, setRcloneVersioningTask] = useState<TaskRecord | null>(null);
  const [previewConnectTask, setPreviewConnectTask] = useState<TaskRecord | null>(null);
  const [selectedTaskIds, setSelectedTaskIds] = useState<number[]>([]);
  const [pauseConfirmTask, setPauseConfirmTask] = useState<TaskRecord | null>(null);
  // Chain folding state: set of parent task ids whose children are expanded
  const [expandedChains, setExpandedChains] = useState<Set<string>>(new Set());
  const [trackedIdentity, setTrackedIdentity] = useState<{ token: string | null; role: AuthRole | null }>({
    token: authToken,
    role,
  });

  // Commit auth after render so continuations see the settled identity.
  // Cleanup retires that generation before the next setup, including StrictMode's extra cycle.
  // cancelPending runs only after that retirement, so a resolved confirm cannot resume.
  useLayoutEffect(() => {
    const generation = identityGenerationRef.current + 1;
    identityGenerationRef.current = generation;
    tokenRef.current = authToken;
    roleRef.current = role;
    mountedRef.current = true;
    return () => {
      identityGenerationRef.current = generation + 1;
      mountedRef.current = false;
      cancelPending();
    };
  }, [authToken, role, cancelPending]);

  if (trackedIdentity.token !== authToken || trackedIdentity.role !== role) {
    const tokenChanged = trackedIdentity.token !== authToken;
    setTrackedIdentity({ token: authToken, role });
    if (!canWriteTasks || tokenChanged) {
      if (createDialogOpen) setCreateDialogOpen(false);
      if (editDialogOpen) setEditDialogOpen(false);
      if (editingTask) setEditingTask(null);
      if (batchDialogOpen) setBatchDialogOpen(false);
      if (batchDefaultNodeIds !== undefined) setBatchDefaultNodeIds(undefined);
      if (pauseConfirmTask) setPauseConfirmTask(null);
      if (selectedTaskIds.length > 0) setSelectedTaskIds([]);
    }
    if (pendingAction) setPendingAction(null);
    if (role !== "admin" || tokenChanged) {
      if (rsyncVersioningTask) setRsyncVersioningTask(null);
      if (rcloneVersioningTask) setRcloneVersioningTask(null);
      if (previewConnectTask) setPreviewConnectTask(null);
    }
    if (tokenChanged && batchResultId !== null) {
      setBatchResultId(null);
    }
  }

  const writeStillCurrent = (generation: number, sessionGeneration: number) =>
    mountedRef.current
    && identityGenerationRef.current === generation
    && getAuthSessionGeneration() === sessionGeneration
    && isTaskWriteRole(roleRef.current);

  const triggerStillCurrent = (generation: number, sessionGeneration: number) =>
    mountedRef.current
    && identityGenerationRef.current === generation
    && getAuthSessionGeneration() === sessionGeneration
    && isTaskTriggerRole(roleRef.current);

  const filteredTasks = useMemo(() => {
    const effectiveKeyword = deferredKeyword.trim().toLowerCase();

    return [...tasks]
      .filter((task) => {
        if (statusFilter === "paused" && task.enabled !== false) {
          return false;
        }
        if (statusFilter !== "all" && statusFilter !== "paused" && task.status !== statusFilter) {
          return false;
        }
        if (nodeFilter !== "all" && String(task.nodeId) !== nodeFilter) {
          return false;
        }
        if (!effectiveKeyword) {
          return true;
        }

        const text =
          `${task.id} ${task.name ?? ""} ${task.policyName} ${task.nodeName} ${task.status} ${task.errorCode ?? ""} ${task.lastError ?? ""}`
            .toLowerCase()
            .trim();
        return text.includes(effectiveKeyword);
      })
      .sort((first, second) => second.id - first.id);
  }, [deferredKeyword, nodeFilter, statusFilter, tasks]);

  const { pagedItems: pagedTasks, page, pageSize, total: filteredTotal, setPage, setPageSize } = useClientPagination(filteredTasks);

  const selectedTaskSet = useMemo(() => new Set(selectedTaskIds), [selectedTaskIds]);
  const allVisibleSelected = pagedTasks.length > 0
    && pagedTasks.every((t) => selectedTaskSet.has(t.id));

  const toggleTaskSelection = useCallback((id: number, checked: boolean) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    setSelectedTaskIds((prev) =>
      checked ? [...prev, id] : prev.filter((x) => x !== id)
    );
  }, []);

  const toggleSelectAllVisible = useCallback((checked: boolean) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    setSelectedTaskIds((prev) => {
      if (checked) {
        const ids = new Set(prev);
        for (const t of pagedTasks) ids.add(t.id);
        return Array.from(ids);
      }
      const visibleIds = new Set(pagedTasks.map((t) => t.id));
      return prev.filter((id) => !visibleIds.has(id));
    });
  }, [pagedTasks]);

  if (!canWriteTasks) {
    if (selectedTaskIds.length > 0) {
      setSelectedTaskIds([]);
    }
  } else {
    const taskIdSet = new Set(tasks.map((task) => task.id));
    const validSelectedTaskIds = selectedTaskIds.filter((id) => taskIdSet.has(id));
    if (validSelectedTaskIds.length !== selectedTaskIds.length) {
      setSelectedTaskIds(validSelectedTaskIds);
    }
  }

  const handleToggleChain = useCallback((chainKey: string) => {
    setExpandedChains((prev) => {
      const next = new Set(prev);
      if (next.has(chainKey)) {
        next.delete(chainKey);
      } else {
        next.add(chainKey);
      }
      return next;
    });
  }, []);

  const taskStats = useMemo(() => {
    let pending = 0;
    let running = 0;
    let failed = 0;
    let success = 0;
    let paused = 0;
    for (const task of tasks) {
      if (!task.enabled) {
        paused += 1;
      }
      if (task.status === "pending") {
        pending += 1;
      } else if (task.status === "running" || task.status === "retrying") {
        running += 1;
      } else if (task.status === "failed") {
        failed += 1;
      } else if (task.status === "success") {
        success += 1;
      }
    }
    return { pending, running, failed, success, paused };
  }, [tasks]);

  const handleCreateTask = async (input: NewTaskInput) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    // Dialog validates name/node before calling this handler; early-return silently if bypassed
    if (!input.name.trim() || !input.nodeId) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();

    try {
      const taskId = await createTask(input);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      setCreateDialogOpen(false);
      toast.success(t("tasks.createSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(getErrorMessage(error));
    }
  };

  const handleEdit = (task: TaskRecord, opener?: EventTarget | null) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    editTaskOpenerRef.current = dialogOpenerFromTarget(opener);
    setEditingTask(task);
    setEditDialogOpen(true);
  };

  const handleViewHistory = (task: TaskRecord) => {
    setSelectedRun(null);
    setHistoryTask(task);
  };

  const handleConnectTaskPreview = (task: TaskRecord) => {
    if (roleRef.current !== "admin") return;
    const eligibility = taskPreviewConnectEligibility(task, canConnectTaskPreview);
    if (!eligibility.visible || eligibility.disabled) return;
    setPreviewConnectTask(task);
  };

  const handleManageRsyncVersioning = (task: TaskRecord) => {
    if (roleRef.current !== "admin" || task.executorType !== "rsync" || !task.rsyncPublication) {
      return;
    }
    setRsyncVersioningTask(task);
  };

  const handleManageRcloneVersioning = (task: TaskRecord) => {
    if (roleRef.current !== "admin" || task.executorType !== "rclone" || !task.rclonePublication) {
      return;
    }
    setRcloneVersioningTask(task);
  };

  const handleUpdateTask = async (input: UpdateTaskInput) => {
    if (!isTaskWriteRole(roleRef.current) || !editingTask) return;
    const taskId = editingTask.id;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    try {
      await updateTask(taskId, input);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      setEditDialogOpen(false);
      setEditingTask(null);
      toast.success(t("tasks.updateSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(error instanceof ApiError && error.status === 409
        ? t("taskCreate.conflictStaleRevision")
        : getErrorMessage(error));
      if (error instanceof ApiError && error.status === 409) {
        throw error;
      }
    }
  };

  const handleTrigger = async (taskId: number) => {
    if (!isTaskTriggerRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    const isCurrent = () => triggerStillCurrent(generation, sessionGeneration);
    try {
      setPendingAction({ id: taskId, action: "trigger" });
      await triggerTask(taskId, isCurrent);
      if (!isCurrent()) return;
      toast.success(t("tasks.triggerSuccess", { id: taskId }));
    } catch (error) {
      if (!isCurrent()) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (isCurrent()) {
        setPendingAction(null);
      }
    }
  };

  const handleCancel = async (taskId: number) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    try {
      setPendingAction({ id: taskId, action: "cancel" });
      await cancelTask(taskId);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.success(t("tasks.cancelSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (writeStillCurrent(generation, sessionGeneration)) {
        setPendingAction(null);
      }
    }
  };

  const handleRetry = async (taskId: number) => {
    if (!isTaskTriggerRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    const isCurrent = () => triggerStillCurrent(generation, sessionGeneration);
    try {
      setPendingAction({ id: taskId, action: "retry" });
      await retryTask(taskId, isCurrent);
      if (!isCurrent()) return;
      toast.success(t("tasks.retrySuccess", { id: taskId }));
    } catch (error) {
      if (!isCurrent()) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (isCurrent()) {
        setPendingAction(null);
      }
    }
  };

  const handlePause = async (taskId: number, cancelRunning?: boolean) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    try {
      setPendingAction({ id: taskId, action: "pause" });
      await pauseTask(taskId, cancelRunning);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.success(t("tasks.pauseSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (writeStillCurrent(generation, sessionGeneration)) {
        setPendingAction(null);
      }
    }
  };

  const handleResume = async (taskId: number) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    try {
      setPendingAction({ id: taskId, action: "resume" });
      await resumeTask(taskId);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.success(t("tasks.resumeSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (writeStillCurrent(generation, sessionGeneration)) {
        setPendingAction(null);
      }
    }
  };

  const handleSkipNext = async (taskId: number) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    try {
      setPendingAction({ id: taskId, action: "skip-next" });
      await skipNextTask(taskId);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.success(t("tasks.skipNextSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (writeStillCurrent(generation, sessionGeneration)) {
        setPendingAction(null);
      }
    }
  };

  const handlePauseWithConfirm = async (taskId: number) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    const task = tasks.find((t) => t.id === taskId);
    if (!task) return;
    // 定时任务弹出选项对话框，让用户选择跳过下次/暂停全部
    if (task.cronSpec) {
      setPauseConfirmTask(task);
      return;
    }
    // 手动任务直接暂停
    await handlePause(taskId);
  };

  const handleDelete = async (taskId: number) => {
    if (!isTaskWriteRole(roleRef.current)) return;
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    const ok = await confirm({
      title: t("tasks.confirmAction"),
      description: t("tasks.confirmDeleteDesc", { id: taskId }),
    });
    if (!ok || !writeStillCurrent(generation, sessionGeneration)) {
      return;
    }
    try {
      setPendingAction({ id: taskId, action: "delete" });
      await deleteTask(taskId);
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.success(t("tasks.deleteSuccess", { id: taskId }));
    } catch (error) {
      if (!writeStillCurrent(generation, sessionGeneration)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (writeStillCurrent(generation, sessionGeneration)) {
        setPendingAction(null);
      }
    }
  };

  const handleBatchExecute = () => {
    if (!isTaskWriteRole(roleRef.current)) return;
    if (selectedTaskIds.length === 0) {
      setBatchDialogOpen(true);
      return;
    }
    const nodeIds = [...new Set(
      tasks
        .filter((t) => selectedTaskSet.has(t.id))
        .map((t) => t.nodeId)
    )];
    setBatchDialogOpen(true);
    setBatchDefaultNodeIds(nodeIds);
  };

  const handleBatchTrigger = async () => {
    if (!isTaskWriteRole(roleRef.current) || !isTaskTriggerRole(roleRef.current)) return;
    if (selectedTaskIds.length === 0) {
      toast.error(t("tasks.selectAtLeastOne"));
      return;
    }
    const generation = identityGenerationRef.current;
    const sessionGeneration = getAuthSessionGeneration();
    const taskIds = [...selectedTaskIds];
    const capturedToken = tokenRef.current;
    if (!capturedToken) return;
    const batchStillCurrent = () =>
      writeStillCurrent(generation, sessionGeneration) && triggerStillCurrent(generation, sessionGeneration);
    const ok = await confirm({
      title: t("tasks.batchTriggerTitle"),
      description: t("tasks.batchTriggerConfirmDesc", { count: taskIds.length }),
    });
    if (!ok || !batchStillCurrent()) return;
    try {
      const result = await withStepUp(async (proof) => {
        if (!batchStillCurrent()) return null;
        try {
          await apiClient.requestTaskBatchTriggerCredentialGrant(capturedToken, {
            taskIds,
            reason: t("tasks.batchTriggerGrantReason", { count: taskIds.length }),
            requestedTtlSeconds: 600,
          }, proof);
          if (!batchStillCurrent()) return null;
          return await apiClient.batchTriggerTasks(capturedToken, taskIds, proof);
        } catch (error) {
          if (!batchStillCurrent()) return null;
          throw error;
        }
      });
      if (!batchStillCurrent() || result == null) return;
      setSelectedTaskIds([]);
      toast.success(t("tasks.batchTriggerSuccess", { success: result.successCount, total: result.total }));
      void refreshTasks();
    } catch (err) {
      if (!batchStillCurrent()) return;
      toast.error(t("tasks.batchTriggerFailed", { error: getErrorMessage(err) }));
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <TasksHero
        totalCount={tasks.length}
        runningCount={taskStats.running}
        canWriteTasks={canWriteTasks}
        onCreate={(event?: { currentTarget: EventTarget | null }) => {
          if (!isTaskWriteRole(roleRef.current)) return;
          createTaskOpenerRef.current = dialogOpenerFromTarget(event?.currentTarget);
          setCreateDialogOpen(true);
        }}
      />

      <StatCardsSection
        compact
        className="animate-slide-up [animation-delay:150ms]"
        items={[
          {
            title: t("tasks.statPending"),
            value: taskStats.pending,
            description: t("tasks.statPendingDesc"),
            tone: "info",
          },
          {
            title: t("tasks.statSuccess"),
            value: taskStats.success,
            description: t("tasks.statSuccessDesc"),
            tone: "success",
          },
          {
            title: t("tasks.statRunning"),
            value: taskStats.running,
            description: t("tasks.statRunningDesc"),
            tone: "warning",
          },
          {
            title: t("tasks.statFailed"),
            value: taskStats.failed,
            description: t("tasks.statFailedDesc"),
            tone: "destructive",
          },
          {
            title: t("tasks.statPaused"),
            value: taskStats.paused,
            description: t("tasks.statPausedDesc"),
            tone: "primary",
          },
        ]}
      />

      <TaskRunStatistics tasks={tasks} />

      <DataSurface>
        <DataSurfaceToolbar className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <ViewModeToggle
                value={viewMode}
                onChange={(mode) => setViewModeRaw(mode)}
                groupLabel={t("tasks.viewToggleGroup")}
                cardsButtonLabel={t("tasks.viewCards")}
                listButtonLabel={t("tasks.viewList")}
              />
            </div>
          </div>

          <TasksBulkBar
            canWriteTasks={canWriteTasks}
            selectedCount={selectedTaskIds.length}
            onBatchExecute={handleBatchExecute}
            onBatchTrigger={() => void handleBatchTrigger()}
            onClearSelection={() => setSelectedTaskIds([])}
          />

          <TasksFilters
            keyword={keyword}
            setKeyword={setKeyword}
            statusFilter={statusFilter}
            setStatusFilterRaw={setStatusFilterRaw}
            nodeFilter={nodeFilter}
            setNodeFilter={setNodeFilter}
            nodes={nodes}
            filteredCount={filteredTasks.length}
            totalCount={tasks.length}
            resetFilters={resetFilters}
          />
        </DataSurfaceToolbar>

        <DataSurfaceContent className="space-y-4">
          {tasksError ? (
            <InventoryRetryAlert error={tasksError} onRetry={() => { void refreshTasks(); }} />
          ) : null}
          {loading ? (
            <LoadingState
              title={t("tasks.loadingTitle")}
              description={t("tasks.loadingDesc")}
              rows={3}
            />
          ) : null}

          {viewMode === "cards" ? (
            <TasksGrid
              loading={loading}
              requestFailed={requestFailed}
              filteredTasks={pagedTasks}
              pendingAction={pendingAction}
              resetFilters={resetFilters}
              setCreateDialogOpen={openTaskCreateFromInventory}
              handleRetry={handleRetry}
              handleCancel={handleCancel}
              handleDelete={handleDelete}
              handleTrigger={handleTrigger}
              handlePause={handlePauseWithConfirm}
              handleResume={handleResume}
              onEdit={handleEdit}
              onViewHistory={handleViewHistory}
              canConnectTaskPreview={canConnectTaskPreview}
              onConnectTaskPreview={handleConnectTaskPreview}
              canManageRsyncVersioning={canManageRsyncVersioning}
              onManageRsyncVersioning={handleManageRsyncVersioning}
              canManageRcloneVersioning={canManageRcloneVersioning}
              onManageRcloneVersioning={handleManageRcloneVersioning}
              canWriteTasks={canWriteTasks}
              canTriggerTasks={canTriggerTasks}
              selectedTaskSet={selectedTaskSet}
              allVisibleSelected={allVisibleSelected}
              toggleTaskSelection={toggleTaskSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
            />
          ) : (
            <TasksTable
              loading={loading}
              requestFailed={requestFailed}
              filteredTasks={pagedTasks}
              pendingAction={pendingAction}
              resetFilters={resetFilters}
              setCreateDialogOpen={openTaskCreateFromInventory}
              handleRetry={handleRetry}
              handleCancel={handleCancel}
              handleDelete={handleDelete}
              handleTrigger={handleTrigger}
              handlePause={handlePauseWithConfirm}
              handleResume={handleResume}
              onEdit={handleEdit}
              onViewHistory={handleViewHistory}
              canConnectTaskPreview={canConnectTaskPreview}
              onConnectTaskPreview={handleConnectTaskPreview}
              canManageRsyncVersioning={canManageRsyncVersioning}
              onManageRsyncVersioning={handleManageRsyncVersioning}
              canManageRcloneVersioning={canManageRcloneVersioning}
              onManageRcloneVersioning={handleManageRcloneVersioning}
              canWriteTasks={canWriteTasks}
              canTriggerTasks={canTriggerTasks}
              selectedTaskSet={selectedTaskSet}
              allVisibleSelected={allVisibleSelected}
              toggleTaskSelection={toggleTaskSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
              expandedChains={expandedChains}
              onToggleChain={handleToggleChain}
            />
          )}

          <Pagination
            page={page}
            pageSize={pageSize}
            total={filteredTotal}
            onPageChange={setPage}
            onPageSizeChange={(size) => { setPageSize(size); }}
          />
        </DataSurfaceContent>
      </DataSurface>

      <TasksPageDialogs
        createDialogOpen={createDialogOpen}
        setCreateDialogOpen={setCreateDialogOpen}
        createOnCloseAutoFocus={restoreCreateTaskOpener}
        editDialogOpen={editDialogOpen}
        editOnCloseAutoFocus={restoreEditTaskOpener}
        setEditDialogOpen={setEditDialogOpen}
        editingTask={editingTask}
        setEditingTask={setEditingTask}
        historyTask={historyTask}
        setHistoryTask={setHistoryTask}
        selectedRun={selectedRun}
        setSelectedRun={setSelectedRun}
        showDiff={showDiff}
        setShowDiff={setShowDiff}
        batchDialogOpen={batchDialogOpen}
        setBatchDialogOpen={setBatchDialogOpen}
        batchDefaultNodeIds={batchDefaultNodeIds}
        setBatchDefaultNodeIds={setBatchDefaultNodeIds}
        batchResultId={batchResultId}
        setBatchResultId={setBatchResultId}
        batchRetain={batchRetain}
        setBatchRetain={setBatchRetain}
        rsyncVersioningTask={rsyncVersioningTask}
        setRsyncVersioningTask={setRsyncVersioningTask}
        canManageRsyncVersioning={canManageRsyncVersioning}
        onRsyncVersioningUpdated={refreshTasks}
        previewConnectTask={previewConnectTask}
        setPreviewConnectTask={setPreviewConnectTask}
        canConnectTaskPreview={canConnectTaskPreview}
        rcloneVersioningTask={rcloneVersioningTask}
        setRcloneVersioningTask={setRcloneVersioningTask}
        canManageRcloneVersioning={canManageRcloneVersioning}
        onRcloneVersioningUpdated={refreshTasks}
        nodes={nodes}
        policies={policies}
        tasks={tasks}
        authToken={authToken}
        canWriteTasks={canWriteTasks}
        handleCreateTask={handleCreateTask}
        handleUpdateTask={handleUpdateTask}
        pauseConfirmTask={pauseConfirmTask}
        setPauseConfirmTask={setPauseConfirmTask}
        onConfirmPause={handlePause}
        onSkipNext={handleSkipNext}
      />

      {dialog}
    </div>
  );
}
