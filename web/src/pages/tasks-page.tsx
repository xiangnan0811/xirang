import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext } from "react-router-dom";
import { Plus, Terminal, Play } from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { LoadingState } from "@/components/ui/loading-state";
import { Pagination } from "@/components/ui/pagination";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { toast } from "@/components/ui/toast";
import { ViewModeToggle } from "@/components/ui/view-mode-toggle";
import { useClientPagination } from "@/hooks/use-client-pagination";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { NewTaskInput, TaskRecord, TaskRunRecord } from "@/types/domain";
import { TasksGrid } from "@/pages/tasks-page.grid";
import type { PendingActionType } from "@/pages/tasks-page.utils";
import { TasksTable } from "@/pages/tasks-page.table";
import { normalizeStatusFilter } from "@/pages/tasks-page.utils";
import { TasksPageDialogs } from "@/pages/tasks-page.dialogs";
import { TasksFilters } from "@/pages/tasks-page.filters";

const keywordStorageKey = "xirang.tasks.keyword";
const statusStorageKey = "xirang.tasks.status";
const nodeStorageKey = "xirang.tasks.node";
const viewStorageKey = "xirang.tasks.view";

type TasksViewMode = "cards" | "list";

export function TasksPage() {
  const { t } = useTranslation();
  const {
    tasks,
    nodes,
    policies,
    loading,
    globalSearch,
    setGlobalSearch,
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
    refreshNodes,
    refreshPolicies,
  } = useOutletContext<ConsoleOutletContext>();

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

  const { confirm, dialog } = useConfirm();

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
  const { token: authToken } = useAuth();
  const [editDialogOpen, setEditDialogOpen] = useState(false);
  const [editingTask, setEditingTask] = useState<TaskRecord | null>(null);
  const [pendingAction, setPendingAction] = useState<PendingActionType>(null);
  const [historyTask, setHistoryTask] = useState<TaskRecord | null>(null);
  const [selectedRun, setSelectedRun] = useState<TaskRunRecord | null>(null);
  const [showSnapshots, setShowSnapshots] = useState(false);
  const [showDiff, setShowDiff] = useState(false);
  const [batchDialogOpen, setBatchDialogOpen] = useState(false);
  const [batchDefaultNodeIds, setBatchDefaultNodeIds] = useState<number[] | undefined>(undefined);
  const [batchResultId, setBatchResultId] = useState<string | null>(null);
  const [batchRetain, setBatchRetain] = useState(false);
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);
  const [selectedTaskIds, setSelectedTaskIds] = useState<number[]>([]);
  const [pauseConfirmTask, setPauseConfirmTask] = useState<TaskRecord | null>(null);

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
    setSelectedTaskIds((prev) =>
      checked ? [...prev, id] : prev.filter((x) => x !== id)
    );
  }, []);

  const toggleSelectAllVisible = useCallback((checked: boolean) => {
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

  // 任务列表变化时清理已删除任务的选中状态
  useEffect(() => {
    const taskIds = new Set(tasks.map((t) => t.id));
    setSelectedTaskIds((prev) => {
      const next = prev.filter((id) => taskIds.has(id));
      return next.length === prev.length ? prev : next;
    });
  }, [tasks]);

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
    if (!input.name.trim() || !input.nodeId) {
      toast.error(t("tasks.createError"));
      return;
    }

    try {
      const taskId = await createTask(input);
      setCreateDialogOpen(false);
      toast.success(t("tasks.createSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleEdit = (task: TaskRecord) => {
    setEditingTask(task);
    setEditDialogOpen(true);
  };

  const handleViewHistory = (task: TaskRecord) => {
    setSelectedRun(null);
    setHistoryTask(task);
  };

  const handleUpdateTask = async (input: NewTaskInput) => {
    if (!editingTask) return;
    if (!input.name.trim() || !input.nodeId) {
      toast.error(t("tasks.updateError"));
      return;
    }
    try {
      await updateTask(editingTask.id, input);
      setEditDialogOpen(false);
      setEditingTask(null);
      toast.success(t("tasks.updateSuccess", { id: editingTask.id }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleTrigger = async (taskId: number) => {
    try {
      setPendingAction({ id: taskId, action: "trigger" });
      await triggerTask(taskId);
      toast.success(t("tasks.triggerSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handleCancel = async (taskId: number) => {
    try {
      setPendingAction({ id: taskId, action: "cancel" });
      await cancelTask(taskId);
      toast.success(t("tasks.cancelSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handleRetry = async (taskId: number) => {
    try {
      setPendingAction({ id: taskId, action: "retry" });
      await retryTask(taskId);
      toast.success(t("tasks.retrySuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handlePause = async (taskId: number, cancelRunning?: boolean) => {
    try {
      setPendingAction({ id: taskId, action: "pause" });
      await pauseTask(taskId, cancelRunning);
      toast.success(t("tasks.pauseSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handleResume = async (taskId: number) => {
    try {
      setPendingAction({ id: taskId, action: "resume" });
      await resumeTask(taskId);
      toast.success(t("tasks.resumeSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handleSkipNext = async (taskId: number) => {
    try {
      setPendingAction({ id: taskId, action: "skip-next" });
      await skipNextTask(taskId);
      toast.success(t("tasks.skipNextSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handlePauseWithConfirm = async (taskId: number) => {
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
    const ok = await confirm({
      title: t("tasks.confirmAction"),
      description: t("tasks.confirmDeleteDesc", { id: taskId }),
    });
    if (!ok) {
      return;
    }
    try {
      setPendingAction({ id: taskId, action: "delete" });
      await deleteTask(taskId);
      toast.success(t("tasks.deleteSuccess", { id: taskId }));
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <StatCardsSection
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

      <Card className="rounded-lg border border-border bg-card">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                <Plus className="mr-1 size-3.5" />
                {t("tasks.addTask")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
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
                }}
              >
                <Terminal className="mr-1 size-3.5" />
                {selectedTaskIds.length > 0
                  ? t("tasks.batchExecuteCount", { count: selectedTaskIds.length })
                  : t("tasks.batchExecute")}
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={async () => {
                  if (selectedTaskIds.length === 0) {
                    toast.error(t("tasks.selectAtLeastOne"));
                    return;
                  }
                  const ok = await confirm({
                    title: t("tasks.batchTriggerTitle"),
                    description: t("tasks.batchTriggerConfirmDesc", { count: selectedTaskIds.length }),
                  });
                  if (!ok) return;
                  try {
                    const result = await apiClient.batchTriggerTasks(authToken!, selectedTaskIds);
                    setSelectedTaskIds([]);
                    toast.success(t("tasks.batchTriggerSuccess", { success: result.successCount, total: result.total }));
                    void refreshTasks();
                  } catch (err) {
                    toast.error(t("tasks.batchTriggerFailed", { error: getErrorMessage(err) }));
                  }
                }}
              >
                <Play className="mr-1 size-3.5" />
                {selectedTaskIds.length > 0
                  ? t("tasks.triggerCount", { count: selectedTaskIds.length })
                  : t("tasks.batchTrigger")}
              </Button>
              {selectedTaskIds.length > 0 && (
                <Button size="sm" variant="ghost" onClick={() => setSelectedTaskIds([])}>
                  {t("tasks.clearSelection")}
                </Button>
              )}
            </div>
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
              filteredTasks={pagedTasks}
              pendingAction={pendingAction}
              resetFilters={resetFilters}
              setCreateDialogOpen={setCreateDialogOpen}
              handleRetry={handleRetry}
              handleCancel={handleCancel}
              handleDelete={handleDelete}
              handleTrigger={handleTrigger}
              handlePause={handlePauseWithConfirm}
              handleResume={handleResume}
              onEdit={handleEdit}
              onViewHistory={handleViewHistory}
              selectedTaskSet={selectedTaskSet}
              allVisibleSelected={allVisibleSelected}
              toggleTaskSelection={toggleTaskSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
            />
          ) : (
            <TasksTable
              loading={loading}
              filteredTasks={pagedTasks}
              pendingAction={pendingAction}
              resetFilters={resetFilters}
              setCreateDialogOpen={setCreateDialogOpen}
              handleRetry={handleRetry}
              handleCancel={handleCancel}
              handleDelete={handleDelete}
              handleTrigger={handleTrigger}
              handlePause={handlePauseWithConfirm}
              handleResume={handleResume}
              onEdit={handleEdit}
              onViewHistory={handleViewHistory}
              selectedTaskSet={selectedTaskSet}
              allVisibleSelected={allVisibleSelected}
              toggleTaskSelection={toggleTaskSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
            />
          )}

          <Pagination
            page={page}
            pageSize={pageSize}
            total={filteredTotal}
            onPageChange={setPage}
            onPageSizeChange={(size) => { setPageSize(size); }}
          />
        </CardContent>
      </Card>

      <TasksPageDialogs
        createDialogOpen={createDialogOpen}
        setCreateDialogOpen={setCreateDialogOpen}
        editDialogOpen={editDialogOpen}
        setEditDialogOpen={setEditDialogOpen}
        editingTask={editingTask}
        setEditingTask={setEditingTask}
        historyTask={historyTask}
        setHistoryTask={setHistoryTask}
        selectedRun={selectedRun}
        setSelectedRun={setSelectedRun}
        showSnapshots={showSnapshots}
        setShowSnapshots={setShowSnapshots}
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
        restoreDialogOpen={restoreDialogOpen}
        setRestoreDialogOpen={setRestoreDialogOpen}
        onRestoreTriggered={() => void refreshTasks()}
        nodes={nodes}
        policies={policies}
        tasks={tasks}
        authToken={authToken}
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
