import { useMemo, useState } from "react";
import { Link, useOutletContext } from "react-router-dom";
import {
  Clock3,
  LayoutGrid,
  List,
  Play,
  Plus,
  RotateCcw,
  Square,
  Trash2,
} from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { TaskCreateDialog } from "@/components/task-create-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { LoadingState } from "@/components/ui/loading-state";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { usePersistentState } from "@/hooks/use-persistent-state";
import { getTaskStatusMeta } from "@/lib/status";
import { cn, getErrorMessage } from "@/lib/utils";
import type { NewTaskInput, TaskStatus } from "@/types/domain";

const keywordStorageKey = "xirang.tasks.keyword";
const statusStorageKey = "xirang.tasks.status";
const nodeStorageKey = "xirang.tasks.node";
const viewStorageKey = "xirang.tasks.view";

type TasksViewMode = "cards" | "list";

function canTrigger(status: TaskStatus) {
  return status !== "running" && status !== "retrying";
}

function canCancel(status: TaskStatus) {
  return status === "running" || status === "retrying";
}

function normalizeStatusFilter(value: string): "all" | TaskStatus {
  if (
    value === "all" ||
    value === "pending" ||
    value === "running" ||
    value === "retrying" ||
    value === "failed" ||
    value === "success" ||
    value === "canceled"
  ) {
    return value;
  }
  return "all";
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

  const [keyword, setKeyword] = usePersistentState<string>(keywordStorageKey, "");
  const [statusFilterRaw, setStatusFilterRaw] =
    usePersistentState<string>(statusStorageKey, "all");
  const [nodeFilter, setNodeFilter] = usePersistentState<string>(nodeStorageKey, "all");
  const [viewModeRaw, setViewModeRaw] =
    usePersistentState<string>(viewStorageKey, "cards");

  const statusFilter = normalizeStatusFilter(statusFilterRaw);
  const viewMode: TasksViewMode = viewModeRaw === "list" ? "list" : "cards";

  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [pendingId, setPendingId] = useState<number | null>(null);

  const resetFilters = () => {
    setKeyword("");
    setStatusFilterRaw("all");
    setNodeFilter("all");
  };

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
      toast.error(getErrorMessage(error));
    }
  };

  const handleTrigger = async (taskId: number) => {
    try {
      setPendingId(taskId);
      await triggerTask(taskId);
      toast.success(`已触发任务 #${taskId}。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingId(null);
    }
  };

  const handleCancel = async (taskId: number) => {
    try {
      setPendingId(taskId);
      await cancelTask(taskId);
      toast.success(`已取消任务 #${taskId}。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingId(null);
    }
  };

  const handleRetry = async (taskId: number) => {
    try {
      setPendingId(taskId);
      await retryTask(taskId);
      toast.success(`已重试任务 #${taskId}。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingId(null);
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
      toast.success(`任务 #${taskId} 已删除。`);
    } catch (error) {
      toast.error(getErrorMessage(error));
    } finally {
      setPendingId(null);
    }
  };

  return (
    <div className="animate-fade-in space-y-5">
      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Card className="border-cyan-500/30 bg-gradient-to-br from-cyan-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">待执行</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{taskStats.pending}</p>
            <p className="mt-1 text-xs text-muted-foreground">等待调度触发</p>
          </CardContent>
        </Card>

        <Card className="border-emerald-500/30 bg-gradient-to-br from-emerald-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">成功</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{taskStats.success}</p>
            <p className="mt-1 text-xs text-muted-foreground">最近执行成功任务</p>
          </CardContent>
        </Card>

        <Card className="border-amber-500/30 bg-gradient-to-br from-amber-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">运行中</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{taskStats.running}</p>
            <p className="mt-1 text-xs text-muted-foreground">包含重试中的任务</p>
          </CardContent>
        </Card>

        <Card className="border-red-500/30 bg-gradient-to-br from-red-500/10 via-transparent to-transparent">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium text-muted-foreground">失败</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-3xl font-semibold">{taskStats.failed}</p>
            <p className="mt-1 text-xs text-muted-foreground">可一键重试恢复</p>
          </CardContent>
        </Card>
      </section>

      <Card className="border-border/75">
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">任务列表（卡片 / 列表双模式）</CardTitle>
              <p className="mt-1 text-xs text-muted-foreground">视图与筛选条件自动持久化，刷新后不丢失</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <div className="inline-flex items-center gap-1 rounded-lg border border-border/80 bg-background/80 p-1">
                <Button
                  size="sm"
                  variant={viewMode === "cards" ? "default" : "ghost"}
                  onClick={() => setViewModeRaw("cards")}
                >
                  <LayoutGrid className="mr-1 size-4" />
                  卡片
                </Button>
                <Button
                  size="sm"
                  variant={viewMode === "list" ? "default" : "ghost"}
                  onClick={() => setViewModeRaw("list")}
                >
                  <List className="mr-1 size-4" />
                  列表
                </Button>
              </div>
              <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                <Plus className="mr-1 size-4" />
                新建任务
              </Button>
            </div>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="filter-panel sticky-filter grid gap-2 md:grid-cols-2 lg:grid-cols-[1.6fr_1fr_1fr_auto]">
            <Input
              placeholder="搜索任务 ID / 节点 / 策略 / 错误码"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />

            <select
              className="h-10 rounded-lg border border-input/80 bg-background/80 px-3 text-sm text-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.06)] transition-[border-color,box-shadow,background-color] ring-offset-background focus-visible:border-primary/35 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/35 aria-[invalid=true]:border-destructive/70 aria-[invalid=true]:ring-destructive/35 disabled:cursor-not-allowed disabled:opacity-60"
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

            <Button
              size="sm"
              variant="outline"
              className="md:col-span-2 lg:col-span-1 lg:justify-self-end"
              onClick={resetFilters}
            >
              重置
            </Button>
          </div>

          {loading ? (
            <LoadingState
              title="任务数据加载中"
              description="正在同步任务状态、进度与最近执行信息..."
              rows={3}
            />
          ) : null}

          {viewMode === "cards" ? (
            <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
              {filteredTasks.map((task) => {
                const status = getTaskStatusMeta(task.status);
                const isPending = pendingId === task.id;

                return (
                  <div
                    key={task.id}
                    className={cn(
                      "interactive-surface flex h-full flex-col gap-2 p-4",
                      task.status === "failed" && "border-red-500/35 bg-red-500/10",
                      task.status === "running" && "border-cyan-500/30"
                    )}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div>
                        <p className="font-medium">{task.policyName}</p>
                        <p className="text-xs text-muted-foreground">任务 ID：{task.id}</p>
                      </div>
                      <Badge variant={status.variant}>{status.label}</Badge>
                    </div>

                    <div className="space-y-0.5 text-sm text-muted-foreground">
                      <p>执行节点：{task.nodeName}</p>
                      <p>开始时间：{task.startedAt}</p>
                      {task.nextRunAt ? <p>下次调度：{task.nextRunAt}</p> : null}
                      {task.lastError ? (
                        <p className="break-all text-red-400">失败信息：{task.lastError}</p>
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
                                : task.status === "running" || task.status === "retrying"
                                  ? "bg-info"
                                  : "bg-muted-foreground"
                          )}
                          style={{ width: `${task.progress}%` }}
                        />
                      </div>
                    </div>

                    <div className="mt-auto flex flex-wrap gap-2">
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
                        aria-label={`删除任务 #${task.id}`}
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
                <EmptyState
                  className="md:col-span-2 2xl:col-span-3"
                  title="当前筛选条件下没有任务"
                  description="可重置筛选条件，或直接新建一个任务。"
                  action={(
                    <div className="flex flex-wrap items-center justify-center gap-2">
                      <Button size="sm" variant="outline" onClick={resetFilters}>
                        重置筛选
                      </Button>
                      <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                        <Plus className="mr-1 size-4" />
                        新建任务
                      </Button>
                    </div>
                  )}
                />
              ) : null}
            </div>
          ) : (
            <div className="overflow-x-auto rounded-2xl border border-border/70 bg-background/55 shadow-sm">
              <table className="min-w-[1100px] text-left text-sm">
                <thead>
                  <tr className="border-b border-border/70 bg-muted/35 text-[11px] uppercase tracking-wide text-muted-foreground">
                    <th className="px-3 py-2.5">任务</th>
                    <th className="px-3 py-2.5">节点</th>
                    <th className="px-3 py-2.5">状态</th>
                    <th className="px-3 py-2.5">进度</th>
                    <th className="px-3 py-2.5">调度</th>
                    <th className="px-3 py-2.5">错误</th>
                    <th className="px-3 py-2.5 text-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredTasks.length ? (
                    filteredTasks.map((task) => {
                      const status = getTaskStatusMeta(task.status);
                      const isPending = pendingId === task.id;
                      return (
                        <tr key={task.id} className="border-b border-border/60 transition-colors hover:bg-accent/35">
                          <td className="px-3 py-2.5">
                            <p className="font-medium">{task.policyName}</p>
                            <p className="text-xs text-muted-foreground">ID #{task.id}</p>
                          </td>
                          <td className="px-3 py-2.5 text-muted-foreground">{task.nodeName}</td>
                          <td className="px-3 py-2.5">
                            <Badge variant={status.variant}>{status.label}</Badge>
                          </td>
                          <td className="px-3 py-2.5">
                            <div className="w-36 space-y-1">
                              <p className="text-xs text-muted-foreground">{task.progress}%</p>
                              <div className="h-2 rounded-full bg-muted">
                                <div
                                  className={cn(
                                    "h-2 rounded-full",
                                    task.status === "success"
                                      ? "bg-success"
                                      : task.status === "failed"
                                        ? "bg-destructive"
                                        : task.status === "running" || task.status === "retrying"
                                          ? "bg-info"
                                          : "bg-muted-foreground"
                                  )}
                                  style={{ width: `${task.progress}%` }}
                                />
                              </div>
                            </div>
                          </td>
                          <td className="px-3 py-2.5 text-xs text-muted-foreground">
                            <p>开始：{task.startedAt}</p>
                            <p>下次：{task.nextRunAt ?? "-"}</p>
                          </td>
                          <td className="px-3 py-2.5 text-xs text-red-400">
                            <span className="line-clamp-2 break-all">{task.lastError || "-"}</span>
                          </td>
                          <td className="px-3 py-2.5 text-right">
                            <div className="flex justify-end gap-2">
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={!canTrigger(task.status) || isPending}
                                onClick={() => void handleTrigger(task.id)}
                              >
                                <Play className="size-4" />
                              </Button>
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={!canCancel(task.status) || isPending}
                                onClick={() => void handleCancel(task.id)}
                              >
                                <Square className="size-4" />
                              </Button>
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={task.status !== "failed" || isPending}
                                onClick={() => void handleRetry(task.id)}
                              >
                                <RotateCcw className="size-4" />
                              </Button>
                              <Link to={`/app/logs?task=${task.id}`}>
                                <Button size="sm" variant="outline">
                                  <Clock3 className="size-4" />
                                </Button>
                              </Link>
                              <Button
                                size="sm"
                                variant="danger"
                                aria-label={`删除任务 #${task.id}`}
                                disabled={isPending}
                                onClick={() => void handleDelete(task.id)}
                              >
                                <Trash2 className="size-4" />
                              </Button>
                            </div>
                          </td>
                        </tr>
                      );
                    })
                  ) : (
                    <tr>
                      <td colSpan={7} className="px-3 py-6">
                        <EmptyState
                          className="py-8"
                          title="当前筛选条件下没有任务"
                          description="可重置筛选条件，或直接新建一个任务。"
                          action={(
                            <div className="flex flex-wrap items-center justify-center gap-2">
                              <Button size="sm" variant="outline" onClick={resetFilters}>
                                重置筛选
                              </Button>
                              <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                                <Plus className="mr-1 size-4" />
                                新建任务
                              </Button>
                            </div>
                          )}
                        />
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          )}
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
