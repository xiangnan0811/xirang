import { useMemo, useState } from "react";
import { Link, useOutletContext } from "react-router-dom";
import { Clock3, Play, Plus, RotateCcw, Square, Trash2 } from "lucide-react";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
import { useConfirm } from "@/hooks/use-confirm";
import { getTaskStatusMeta } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { NewTaskInput, TaskExecutorType, TaskStatus } from "@/types/domain";

type TaskDraft = {
  name: string;
  nodeId: string;
  policyId: string;
  executorType: TaskExecutorType;
  command: string;
  rsyncSource: string;
  rsyncTarget: string;
  cronSpec: string;
};

const defaultDraft: TaskDraft = {
  name: "",
  nodeId: "",
  policyId: "",
  executorType: "rsync",
  command: "",
  rsyncSource: "",
  rsyncTarget: "",
  cronSpec: ""
};

function toNumberOrNull(value: string): number | null {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }
  return parsed;
}

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
    retryTask
  } = useOutletContext<ConsoleOutletContext>();

  const { confirm, dialog } = useConfirm();

  const [keyword, setKeyword] = useState(globalSearch);
  const [statusFilter, setStatusFilter] = useState<"all" | TaskStatus>("all");
  const [nodeFilter, setNodeFilter] = useState("all");
  const [showCreate, setShowCreate] = useState(false);
  const [pendingId, setPendingId] = useState<number | null>(null);
  const [draft, setDraft] = useState<TaskDraft>(defaultDraft);

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

        const text = `${task.id} ${task.policyName} ${task.nodeName} ${task.status} ${task.errorCode ?? ""} ${
          task.lastError ?? ""
        }`
          .toLowerCase()
          .trim();
        return text.includes(effectiveKeyword);
      })
      .sort((first, second) => second.id - first.id);
  }, [globalSearch, keyword, nodeFilter, statusFilter, tasks]);

  const saveTask = async () => {
    const nodeId = toNumberOrNull(draft.nodeId);
    if (!draft.name.trim() || !nodeId) {
      toast.error("创建失败：任务名称与节点必填。");
      return;
    }

    const input: NewTaskInput = {
      name: draft.name.trim(),
      nodeId,
      policyId: toNumberOrNull(draft.policyId),
      executorType: draft.executorType,
      command: draft.command.trim() || undefined,
      rsyncSource: draft.rsyncSource.trim() || undefined,
      rsyncTarget: draft.rsyncTarget.trim() || undefined,
      cronSpec: draft.cronSpec.trim() || undefined
    };

    try {
      const taskId = await createTask(input);
      setDraft(defaultDraft);
      setShowCreate(false);
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
    const ok = await confirm({ title: "确认操作", description: `确认删除任务 #${taskId} 吗？` });
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
    <div className="space-y-4 animate-fade-in">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="text-base">任务队列（筛选 + 触发 + 取消 + 重试）</CardTitle>
            <Button size="sm" onClick={() => setShowCreate((prev) => !prev)}>
              <Plus className="mr-1 size-4" />
              新建任务
            </Button>
          </div>
        </CardHeader>

        <CardContent className="space-y-3">
          <div className="grid gap-2 md:grid-cols-[1.6fr_1fr_1fr]">
            <Input
              placeholder="搜索任务 ID / 节点 / 策略 / 错误码"
              value={keyword}
              onChange={(event) => setKeyword(event.target.value)}
            />

            <select
              className="h-10 rounded-md border bg-background px-3 text-sm"
              value={statusFilter}
              onChange={(event) => setStatusFilter(event.target.value as typeof statusFilter)}
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
              className="h-10 rounded-md border bg-background px-3 text-sm"
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

          {showCreate ? (
            <>
              <button
                className="fixed inset-0 z-40 bg-black/45 md:hidden"
                onClick={() => setShowCreate(false)}
                aria-label="关闭任务创建抽屉"
              />
              <div
                className={cn(
                  "space-y-3 rounded-lg border bg-muted/30 p-3",
                  "fixed inset-x-0 bottom-0 z-50 max-h-[86vh] overflow-auto rounded-b-none rounded-t-2xl border-x-0 border-b-0 bg-background p-4 md:static md:max-h-none md:rounded-lg md:border md:bg-muted/30 md:p-3"
                )}
              >
                <p className="text-xs text-muted-foreground md:hidden">移动端任务配置（底部抽屉）</p>

                <div className="grid gap-2 md:grid-cols-2">
                  <Input
                    placeholder="任务名称"
                    value={draft.name}
                    onChange={(event) => setDraft((prev) => ({ ...prev, name: event.target.value }))}
                  />
                  <select
                    className="h-10 rounded-md border bg-background px-3 text-sm"
                    value={draft.nodeId}
                    onChange={(event) => setDraft((prev) => ({ ...prev, nodeId: event.target.value }))}
                  >
                    <option value="">选择节点</option>
                    {nodes.map((node) => (
                      <option key={node.id} value={String(node.id)}>
                        {node.name}
                      </option>
                    ))}
                  </select>
                </div>

                <div className="grid gap-2 md:grid-cols-3">
                  <select
                    className="h-10 rounded-md border bg-background px-3 text-sm"
                    value={draft.policyId}
                    onChange={(event) => setDraft((prev) => ({ ...prev, policyId: event.target.value }))}
                  >
                    <option value="">不绑定策略（自定义任务）</option>
                    {policies.map((policy) => (
                      <option key={policy.id} value={String(policy.id)}>
                        {policy.name}
                      </option>
                    ))}
                  </select>

                  <select
                    className="h-10 rounded-md border bg-background px-3 text-sm"
                    value={draft.executorType}
                    onChange={(event) =>
                      setDraft((prev) => ({
                        ...prev,
                        executorType: event.target.value as TaskExecutorType
                      }))
                    }
                  >
                    <option value="rsync">Rsync 执行器</option>
                    <option value="local">本地执行器</option>
                  </select>

                  <Input
                    placeholder="Cron（可选）"
                    value={draft.cronSpec}
                    onChange={(event) => setDraft((prev) => ({ ...prev, cronSpec: event.target.value }))}
                  />
                </div>

                <Input
                  placeholder="命令（可选）"
                  value={draft.command}
                  onChange={(event) => setDraft((prev) => ({ ...prev, command: event.target.value }))}
                />

                <div className="grid gap-2 md:grid-cols-2">
                  <Input
                    placeholder="Rsync 源路径（可选）"
                    value={draft.rsyncSource}
                    onChange={(event) => setDraft((prev) => ({ ...prev, rsyncSource: event.target.value }))}
                  />
                  <Input
                    placeholder="Rsync 目标路径（可选）"
                    value={draft.rsyncTarget}
                    onChange={(event) => setDraft((prev) => ({ ...prev, rsyncTarget: event.target.value }))}
                  />
                </div>

                <div className="grid grid-cols-2 gap-2">
                  <Button variant="outline" onClick={() => setShowCreate(false)}>
                    取消
                  </Button>
                  <Button onClick={() => void saveTask()}>保存任务</Button>
                </div>
              </div>
            </>
          ) : null}

          {loading ? <p className="text-sm text-muted-foreground">正在加载任务...</p> : null}

          <div className="space-y-3">
            {filteredTasks.map((task) => {
              const status = getTaskStatusMeta(task.status);
              const isPending = pendingId === task.id;

              return (
                <div key={task.id} className="space-y-2 rounded-lg border p-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <p className="font-medium">{task.policyName}</p>
                      <p className="text-xs text-muted-foreground">任务 ID：{task.id}</p>
                    </div>
                    <Badge variant={status.variant}>{status.label}</Badge>
                  </div>

                  <div className="text-sm text-muted-foreground">
                    <p>执行节点：{task.nodeName}</p>
                    <p>开始时间：{task.startedAt}</p>
                    {task.nextRunAt ? <p>下次调度：{task.nextRunAt}</p> : null}
                    {task.lastError ? <p className="text-red-400">失败信息：{task.lastError}</p> : null}
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
                          task.status === "success" ? "bg-success" :
                          task.status === "failed" ? "bg-destructive" :
                          task.status === "running" || task.status === "retrying" ? "bg-info" :
                          "bg-muted-foreground"
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

                    <Button size="sm" variant="outline" disabled={isPending} onClick={() => void handleDelete(task.id)}>
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

      {dialog}
    </div>
  );
}
