import { useEffect, useMemo, useState } from "react";
import { useOutletContext } from "react-router-dom";
import { Plus, Terminal, RotateCcw } from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { BatchCommandDialog } from "@/components/batch-command-dialog";
import { RestoreConfirmDialog } from "@/components/restore-confirm-dialog";
import { TaskEditorDialog } from "@/components/task-create-dialog";
import { TaskRunDetail } from "@/components/task-run-detail";
import { TaskRunHistory } from "@/components/task-run-history";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { AppSelect } from "@/components/ui/app-select";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { getErrorMessage } from "@/lib/utils";
import type { NewTaskInput, TaskRecord, TaskRunRecord, TaskStatus } from "@/types/domain";
import { TasksGrid } from "@/pages/tasks-page.grid";
import type { PendingActionType } from "@/pages/tasks-page.utils";
import { TasksTable } from "@/pages/tasks-page.table";
import { normalizeStatusFilter } from "@/pages/tasks-page.utils";

const keywordStorageKey = "xirang.tasks.keyword";
const statusStorageKey = "xirang.tasks.status";
const nodeStorageKey = "xirang.tasks.node";
const viewStorageKey = "xirang.tasks.view";

type TasksViewMode = "cards" | "list";

export function TasksPage() {
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
  } = useOutletContext<ConsoleOutletContext>();

  useEffect(() => {
    void refreshTasks();
  }, [refreshTasks]);

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
  const [batchDialogOpen, setBatchDialogOpen] = useState(false);
  const [restoreDialogOpen, setRestoreDialogOpen] = useState(false);

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
      toast.error("创建失败：任务名称与节点必填。");
      return;
    }

    try {
      const taskId = await createTask(input);
      setCreateDialogOpen(false);
      toast.success(`任务 #${taskId} 已创建。`);
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
      toast.error("保存失败：任务名称与节点必填。");
      return;
    }
    try {
      await updateTask(editingTask.id, input);
      setEditDialogOpen(false);
      setEditingTask(null);
      toast.success(`任务 #${editingTask.id} 已更新。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const handleTrigger = async (taskId: number) => {
    try {
      setPendingAction({ id: taskId, action: "trigger" });
      await triggerTask(taskId);
      toast.success(`已触发任务 #${taskId}。`);
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
      toast.success(`已取消任务 #${taskId}。`);
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
      toast.success(`已重试任务 #${taskId}。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const handleDelete = async (taskId: number) => {
    const ok = await confirm({
      title: "确认操作",
      description: `确认删除任务 #${taskId} 吗？`,
    });
    if (!ok) {
      return;
    }
    try {
      setPendingAction({ id: taskId, action: "delete" });
      await deleteTask(taskId);
      toast.success(`任务 #${taskId} 已删除。`);
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
            title: "待执行",
            value: taskStats.pending,
            description: "等待调度触发",
            tone: "info",
          },
          {
            title: "成功",
            value: taskStats.success,
            description: "最近执行成功任务",
            tone: "success",
          },
          {
            title: "运行中",
            value: taskStats.running,
            description: "包含重试中的任务",
            tone: "warning",
          },
          {
            title: "失败",
            value: taskStats.failed,
            description: "可一键重试恢复",
            tone: "destructive",
          },
        ]}
      />

      <Card className="border-border/75">
        <CardContent className="space-y-4 pt-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-2">
              <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                <Plus className="mr-1 size-3.5" />
                新建任务
              </Button>
              <Button size="sm" variant="outline" onClick={() => setBatchDialogOpen(true)}>
                <Terminal className="mr-1 size-3.5" />
                批量执行
              </Button>
            </div>
            <div className="flex items-center gap-2">
              <ViewModeToggle
                value={viewMode}
                onChange={(mode) => setViewModeRaw(mode)}
                groupLabel="任务视图切换"
                cardsButtonLabel="任务卡片视图"
                listButtonLabel="任务列表视图"
              />
            </div>
          </div>

          <FilterPanel sticky={false} className="grid gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-[2fr_1fr_1fr_auto] items-center">
            <SearchInput
              containerClassName="w-full"
              placeholder="搜索任务名称 / ID / 节点 / 策略 / 错误码"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
              aria-label="任务关键词筛选"
            />

            <AppSelect
              containerClassName="w-full"
              aria-label="任务状态筛选"
              value={statusFilter}
              onChange={(event) =>
                setStatusFilterRaw(event.target.value as "all" | TaskStatus)
              }
            >
              <option value="all">全部状态</option>
              <option value="pending">待执行</option>
              <option value="running">执行中</option>
              <option value="retrying">重试中</option>
              <option value="failed">失败</option>
              <option value="success">成功</option>
              <option value="canceled">已取消</option>
              <option value="warning">校验异常</option>
            </AppSelect>

            <AppSelect
              containerClassName="w-full"
              aria-label="任务节点筛选"
              value={nodeFilter}
              onChange={(event) => setNodeFilter(event.target.value)}
            >
              <option value="all">全部节点</option>
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
                重置
              </Button>
            </div>
          </FilterPanel>

          <FilterSummary filtered={filteredTasks.length} total={tasks.length} unit="条任务" />

          {loading ? (
            <LoadingState
              title="任务数据加载中"
              description="正在同步任务状态、进度与最近执行信息..."
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
            />
          )}
        </CardContent>
      </Card>

      <TaskEditorDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        nodes={nodes}
        policies={policies}
        onSave={handleCreateTask}
      />

      <TaskEditorDialog
        open={editDialogOpen}
        onOpenChange={(open) => {
          setEditDialogOpen(open);
          if (!open) setEditingTask(null);
        }}
        nodes={nodes}
        policies={policies}
        onSave={handleUpdateTask}
        editingTask={editingTask}
      />

      <Dialog
        open={!!historyTask}
        onOpenChange={(open) => {
          if (!open) {
            setHistoryTask(null);
            setSelectedRun(null);
          }
        }}
      >
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>
              执行历史 — {historyTask?.name || historyTask?.policyName}
            </DialogTitle>
            <DialogDescription>
              任务 #{historyTask?.id} 的执行记录
            </DialogDescription>
            {historyTask?.executorType === "rsync" && (
              <Button
                size="sm"
                variant="outline"
                className="ml-auto mr-8 shrink-0"
                onClick={() => setRestoreDialogOpen(true)}
              >
                <RotateCcw className="mr-1 size-3.5" />
                从此备份恢复
              </Button>
            )}
            <DialogCloseButton />
          </DialogHeader>
          <DialogBody>
            {historyTask && authToken && (
              selectedRun ? (
                <TaskRunDetail
                  run={selectedRun}
                  token={authToken}
                  onBack={() => setSelectedRun(null)}
                />
              ) : (
                <TaskRunHistory
                  taskId={historyTask.id}
                  token={authToken}
                  onSelectRun={setSelectedRun}
                />
              )
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      {authToken && (
        <BatchCommandDialog
          open={batchDialogOpen}
          onOpenChange={setBatchDialogOpen}
          nodes={nodes}
          token={authToken}
          onSuccess={(batchId) => toast.success(`批量任务已提交，批次 ID: ${batchId}`)}
        />
      )}

      {authToken && historyTask && (
        <RestoreConfirmDialog
          open={restoreDialogOpen}
          onOpenChange={setRestoreDialogOpen}
          taskId={historyTask.id}
          taskName={historyTask.name ?? historyTask.policyName ?? ""}
          rsyncSource={historyTask.rsyncSource}
          rsyncTarget={historyTask.rsyncTarget}
          token={authToken}
          onSuccess={(runId) => {
            setRestoreDialogOpen(false);
            toast.success(`恢复任务已触发，执行 ID: #${runId}`);
          }}
        />
      )}

      {dialog}
    </div>
  );
}
