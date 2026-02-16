import { useMemo, useState } from "react";
import { Link, useOutletContext } from "react-router-dom";
import { Clock3, Play, Plus, RotateCcw, Square, Trash2 } from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { TaskCreateDialog } from "@/components/task-create-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { NewTaskInput, TaskStatus } from "@/types/domain";

function canTrigger(status: TaskStatus) {
  return status !== "running" && status !== "retrying";
}

function canCancel(status: TaskStatus) {
  return status === "running" || status === "retrying";
}

export function TasksPage() {
  const {
    tasks,
    nodes,
    policies,
    loading,
    globalSearch,
    createTask,
    deleteTask,
    triggerTask,
    cancelTask,
    retryTask,
  } = useOutletContext<ConsoleOutletContext>();

  const { confirm, dialog } = useConfirm();

  const [keyword, setKeyword] = useState(globalSearch);
  const [statusFilter, setStatusFilter] = useState<"all" | TaskStatus>("all");
  const [nodeFilter, setNodeFilter] = useState("all");
  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [pendingId, setPendingId] = useState<number | null>(null);

  const filteredTasks = useMemo(() => {
    const effectiveKeyword = (keyword || globalSearch).trim().toLowerCase();

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
          `${task.id} ${task.policyName} ${task.nodeName} ${task.status} ${task.errorCode ?? ""} ${task.lastError ?? ""}`
            .toLowerCase()
            .trim();
        return text.includes(effectiveKeyword);
      })
      .sort((first, second) => second.id - first.id);
  }, [globalSearch, keyword, nodeFilter, statusFilter, tasks]);

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
      toast.error((error as Error).message);
    }
  };

  const handleTrigger = async (taskId: number) => {
    try {
      setPendingId(taskId);
      await triggerTask(taskId);
      setPendingId(null);
      toast.success(`已触发任务 #${taskId}。`);
    } catch (error) {
      setPendingId(null);
      toast.error((error as Error).message);
    }
  };

  const handleCancel = async (taskId: number) => {
    try {
      setPendingId(taskId);
      await cancelTask(taskId);
      setPendingId(null);
      toast.success(`已取消任务 #${taskId}。`);
    } catch (error) {
      setPendingId(null);
      toast.error((error as Error).message);
    }
  };

  const handleRetry = async (taskId: number) => {
    try {
      setPendingId(taskId);
      await retryTask(taskId);
      setPendingId(null);
      toast.success(`已重试任务 #${taskId}。`);
    } catch (error) {
      setPendingId(null);
      toast.error((error as Error).message);
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
      setPendingId(taskId);
      await deleteTask(taskId);
      setPendingId(null);
      toast.success(`任务 #${taskId} 已删除。`);
    } catch (error) {
      setPendingId(null);
      toast.error((error as Error).message);
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <section className="relative overflow-hidden rounded-2xl border border-border/75 bg-background/65 p-4 shadow-panel md:p-5">
        <div className="pointer-events-none absolute -right-14 -top-8 h-36 w-36 rounded-full bg-brand-life/20 blur-3xl" />
        <div className="pointer-events-none absolute -left-8 bottom-0 h-28 w-28 rounded-full bg-brand-soil/20 blur-3xl" />
        <div className="relative flex flex-wrap items-start justify-between gap-3">
          <div>
            <p className="text-xs text-muted-foreground">任务编排中心</p>
            <h3 className="mt-1 text-xl font-semibold tracking-tight">任务调度与执行面板</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              覆盖触发、取消、重试与日志回溯，支持快速筛选定位异常任务。
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">待执行 {taskStats.pending}</Badge>
            <Badge variant="warning">运行中 {taskStats.running}</Badge>
            <Badge variant="danger">失败 {taskStats.failed}</Badge>
            <Badge variant="success">成功 {taskStats.success}</Badge>
            <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
              <Plus className="mr-1 size-4" />
              新建任务
            </Button>
          </div>
        </div>
      </section>

      <Card className="border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">任务队列（筛选 + 触发 + 取消 + 重试）</CardTitle>
              <p className="mt-1 text-xs text-muted-foreground">任务按最新创建优先展示，支持节点与状态双重过滤</p>
            </div>
            <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
              <Plus className="mr-1 size-4" />
              新建任务
            </Button>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="grid gap-2 rounded-xl border border-border/70 bg-background/55 p-2 md:grid-cols-[1.6fr_1fr_1fr]">
            <Input
              placeholder="搜索任务 ID / 节点 / 策略 / 错误码"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />

            <select
              className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
              value={statusFilter}
              onChange={(event) =>
                setStatusFilter(event.target.value as typeof statusFilter)
              }
            >
              <option value="all">全部状态</option>
              <option value="pending">待执行</option>
              <option value="running">运行中</option>
              <option value="retrying">重试中</option>
              <option value="failed">失败</option>
              <option value="success">成功</option>
              <option value="canceled">已取消</option>
            </select>

            <select
              className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
              value={nodeFilter}
              onChange={(event) => setNodeFilter(event.target.value)}
            >
              <option value="all">全部节点</option>
              {nodes.map((node) => (
                <option key={node.id} value={String(node.id)}>
                  {node.name}
                </option>
              ))}
            </select>
          </div>

          {loading ? (
            <p className="text-sm text-muted-foreground">正在加载任务...</p>
          ) : null}

          <div className="space-y-3">
            {filteredTasks.map((task) => {
              const status = getTaskStatusMeta(task.status);
              const isPending = pendingId === task.id;

              return (
                <div
                  key={task.id}
                  className={cn(
                    "space-y-2 rounded-xl border border-border/75 bg-background/65 p-4 shadow-sm transition-all duration-200 hover:-translate-y-px hover:border-primary/35 hover:shadow-panel",
                    task.status === "failed" && "border-red-500/35 bg-red-500/10",
                    task.status === "running" && "border-cyan-500/30"
                  )}
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <p className="font-medium">{task.policyName}</p>
                      <p className="text-xs text-muted-foreground">
                        任务 ID：{task.id}
                      </p>
                    </div>
                    <Badge variant={status.variant}>{status.label}</Badge>
                  </div>

                  <div className="text-sm text-muted-foreground">
                    <p>执行节点：{task.nodeName}</p>
                    <p>开始时间：{task.startedAt}</p>
                    {task.nextRunAt ? (
                      <p>下次调度：{task.nextRunAt}</p>
                    ) : null}
                    {task.lastError ? (
                      <p className="text-red-400">
                        失败信息：{task.lastError}
                      </p>
                    ) : null}
                  </div>

                  <div className="space-y-1">
                    <div className="flex items-center justify-between text-xs text-muted-foreground">
                      <span>进度</span>
                      <span>{task.progress}%</span>
                    </div>
                    <div className="h-2 rounded-full bg-muted">
                      <div
                        className={cn(
                          "h-2 rounded-full transition-all",
                          task.status === "success"
                            ? "bg-success"
                            : task.status === "failed"
                              ? "bg-destructive"
                              : task.status === "running" ||
                                  task.status === "retrying"
                                ? "bg-info"
                                : "bg-muted-foreground"
                        )}
                        style={{ width: `${task.progress}%` }}
                      />
                    </div>
                  </div>

                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={!canTrigger(task.status) || isPending}
                      onClick={() => void handleTrigger(task.id)}
                    >
                      <Play className="mr-1 size-4" />
                      触发
                    </Button>

                    <Button
                      size="sm"
                      variant="outline"
                      disabled={!canCancel(task.status) || isPending}
                      onClick={() => void handleCancel(task.id)}
                    >
                      <Square className="mr-1 size-4" />
                      取消
                    </Button>

                    <Button
                      size="sm"
                      variant="outline"
                      disabled={task.status !== "failed" || isPending}
                      onClick={() => void handleRetry(task.id)}
                    >
                      <RotateCcw className="mr-1 size-4" />
                      重试
                    </Button>

                    <Link to={`/app/logs?task=${task.id}`}>
                      <Button size="sm" variant="outline">
                        <Clock3 className="mr-1 size-4" />
                        查看日志
                      </Button>
                    </Link>

                    <Button
                      size="sm"
                      variant="danger"
                      disabled={isPending}
                      onClick={() => void handleDelete(task.id)}
                    >
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              );
            })}

            {!filteredTasks.length ? (
              <EmptyState title="当前筛选条件下没有任务。" />
            ) : null}
          </div>
        </CardContent>
      </Card>

      <TaskCreateDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        nodes={nodes}
        policies={policies}
        onSave={handleCreateTask}
      />

      {dialog}
    </div>
  );
}
