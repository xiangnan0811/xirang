import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useOutletContext } from "react-router-dom";
import { Plus, Terminal, Play } from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import { FilterPanel, FilterSummary } from "@/components/ui/filter-panel";
import { LoadingState } from "@/components/ui/loading-state";
import { SearchInput } from "@/components/ui/search-input";
import { StatCardsSection } from "@/components/ui/stat-cards-section";
import { toast } from "@/components/ui/toast";
import { ViewModeToggle } from "@/components/ui/view-mode-toggle";
import { useConfirm } from "@/hooks/use-confirm";
import { usePageFilters } from "@/hooks/use-page-filters";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { useAuth } from "@/context/auth-context";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { NewTaskInput, TaskRecord, TaskRunRecord, TaskStatus } from "@/types/domain";
import { TasksGrid } from "@/pages/tasks-page.grid";
import type { PendingActionType } from "@/pages/tasks-page.utils";
import { TasksTable } from "@/pages/tasks-page.table";
import { normalizeStatusFilter } from "@/pages/tasks-page.utils";
import { TasksPageDialogs } from "@/pages/tasks-page.dialogs";
import { TasksSelectionBar } from "@/pages/tasks-page.selection-bar";

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
    refreshTasks,
    refreshNodes,
    refreshPolicies,
  } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshTasks();
    void refreshNodes();
    void refreshPolicies();
  }, [refreshTasks, refreshNodes, refreshPolicies]);

  // 当有活跃任务（running/pending/retrying）时，每 5 秒自动刷新任务状态
  useEffect(() => {
    const hasActiveTask = tasks.some(
      (t) => t.status === "running" || t.status === "pending" || t.status === "retrying"
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

  const filteredTasks = useMemo(() => {
    const effectiveKeyword = deferredKeyword.trim().toLowerCase();

    return [...tasks]
      .filter((task) => {
        if (statusFilter !== "all" && task.status !== statusFilter) {
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

  const selectedTaskSet = useMemo(() => new Set(selectedTaskIds), [selectedTaskIds]);
  const allVisibleSelected = filteredTasks.length > 0
    && filteredTasks.every((t) => selectedTaskSet.has(t.id));

  const toggleTaskSelection = useCallback((id: number, checked: boolean) => {
    setSelectedTaskIds((prev) =>
      checked ? [...prev, id] : prev.filter((x) => x !== id)
    );
  }, []);

  const toggleSelectAllVisible = useCallback((checked: boolean) => {
    setSelectedTaskIds((prev) => {
      if (checked) {
        const ids = new Set(prev);
        for (const t of filteredTasks) ids.add(t.id);
        return Array.from(ids);
      }
      const visibleIds = new Set(filteredTasks.map((t) => t.id));
      return prev.filter((id) => !visibleIds.has(id));
    });
  }, [filteredTasks]);

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
    for (const task of tasks) {
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
    return { pending, running, failed, success };
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
        ]}
      />

      <Card className="glass-panel border-border/70">
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

          <FilterPanel sticky={false} className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
            <SearchInput
              containerClassName="w-full"
              placeholder={t("tasks.searchPlaceholder")}
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              aria-label={t("tasks.searchAriaLabel")}
            />

            <AppSelect
              containerClassName="w-full"
              aria-label={t("tasks.statusFilterAriaLabel")}
              value={statusFilter}
              onChange={(event) =>
                setStatusFilterRaw(event.target.value as "all" | TaskStatus)
              }
            >
              <option value="all">{t("tasks.allStatus")}</option>
              <option value="pending">{t("tasks.statusPending")}</option>
              <option value="running">{t("tasks.statusRunning")}</option>
              <option value="retrying">{t("tasks.statusRetrying")}</option>
              <option value="failed">{t("tasks.statusFailed")}</option>
              <option value="success">{t("tasks.statusSuccess")}</option>
              <option value="canceled">{t("tasks.statusCanceled")}</option>
              <option value="warning">{t("tasks.statusWarning")}</option>
            </AppSelect>

            <AppSelect
              containerClassName="w-full"
              aria-label={t("tasks.nodeFilterAriaLabel")}
              value={nodeFilter}
              onChange={(event) => setNodeFilter(event.target.value)}
            >
              <option value="all">{t("tasks.allNodes")}</option>
              {nodes.map((node) => (
                <option key={node.id} value={String(node.id)}>
                  {node.name}
                </option>
              ))}
            </AppSelect>

            <div className="flex items-center gap-2 justify-end col-span-full sm:col-span-2 md:col-span-3 lg:col-span-1">
              <Button
                size="sm"
                variant="outline"
                onClick={resetFilters}
              >
                {t("tasks.resetButton")}
              </Button>
            </div>
          </FilterPanel>

          <FilterSummary filtered={filteredTasks.length} total={tasks.length} unit={t("tasks.taskUnit")} />

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
              filteredTasks={filteredTasks}
              pendingAction={pendingAction}
              resetFilters={resetFilters}
              setCreateDialogOpen={setCreateDialogOpen}
              handleRetry={handleRetry}
              handleCancel={handleCancel}
              handleDelete={handleDelete}
              handleTrigger={handleTrigger}
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
              filteredTasks={filteredTasks}
              pendingAction={pendingAction}
              resetFilters={resetFilters}
              setCreateDialogOpen={setCreateDialogOpen}
              handleRetry={handleRetry}
              handleCancel={handleCancel}
              handleDelete={handleDelete}
              handleTrigger={handleTrigger}
              onEdit={handleEdit}
              onViewHistory={handleViewHistory}
              selectedTaskSet={selectedTaskSet}
              allVisibleSelected={allVisibleSelected}
              toggleTaskSelection={toggleTaskSelection}
              toggleSelectAllVisible={toggleSelectAllVisible}
            />
          )}
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
        nodes={nodes}
        policies={policies}
        tasks={tasks}
        authToken={authToken}
        handleCreateTask={handleCreateTask}
        handleUpdateTask={handleUpdateTask}
      />

      <TasksSelectionBar
        selectedTaskIds={selectedTaskIds}
        setSelectedTaskIds={setSelectedTaskIds}
        selectedTaskSet={selectedTaskSet}
        tasks={tasks}
        authToken={authToken}
        confirm={confirm}
        setBatchDefaultNodeIds={setBatchDefaultNodeIds}
        setBatchDialogOpen={setBatchDialogOpen}
        refreshTasks={refreshTasks}
      />

      {dialog}
    </div>
  );
}
