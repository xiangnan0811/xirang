import { useState } from "react";
import { Pencil, Plus } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { toast } from "@/components/ui/toast";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { CronGenerator } from "@/components/cron-generator";
import type {
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  TaskExecutorType,
  TaskRecord,
} from "@/types/domain";

type TaskDraft = {
  name: string;
  nodeId: string;
  policyId: string;
  dependsOnTaskId: string;
  executorType: TaskExecutorType;
  rsyncSource: string;
  rsyncTarget: string;
  cronSpec: string;
};

const defaultDraft: TaskDraft = {
  name: "",
  nodeId: "",
  policyId: "",
  dependsOnTaskId: "",
  executorType: "rsync",
  rsyncSource: "",
  rsyncTarget: "",
  cronSpec: "",
};

function taskRecordToDraft(task: TaskRecord): TaskDraft {
  return {
    name: task.name ?? task.policyName ?? "",
    nodeId: task.nodeId ? String(task.nodeId) : "",
    policyId: task.policyId ? String(task.policyId) : "",
    dependsOnTaskId: task.dependsOnTaskId ? String(task.dependsOnTaskId) : "",
    executorType: task.executorType ?? "rsync",
    rsyncSource: task.rsyncSource ?? "",
    rsyncTarget: task.rsyncTarget ?? "",
    cronSpec: task.cronSpec ?? "",
  };
}

function toNumberOrNull(value: string): number | null {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }
  return parsed;
}

type TaskEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks?: TaskRecord[];
  onSave: (input: NewTaskInput) => Promise<void>;
  editingTask?: TaskRecord | null;
};

export function TaskEditorDialog({
  open,
  onOpenChange,
  nodes,
  policies,
  tasks,
  onSave,
  editingTask,
}: TaskEditorDialogProps) {
  const isEditing = Boolean(editingTask);
  const [draft, setDraft] = useDialogDraft<TaskDraft, TaskRecord>(
    open,
    defaultDraft,
    editingTask,
    taskRecordToDraft,
  );
  const [saving, setSaving] = useState(false);

  const handleSave = async () => {
    const nodeId = toNumberOrNull(draft.nodeId);
    if (!nodeId) {
      toast.error("保存失败：请选择目标节点。", { id: "task-editor-node-required" });
      return;
    }

    if (!draft.name.trim()) {
      toast.error("保存失败：请输入任务名称。", { id: "task-editor-name-required" });
      return;
    }

    const dependsOnTaskId = toNumberOrNull(draft.dependsOnTaskId);
    const input: NewTaskInput = {
      name: draft.name.trim(),
      nodeId,
      policyId: toNumberOrNull(draft.policyId),
      dependsOnTaskId: dependsOnTaskId,
      executorType: draft.executorType,
      rsyncSource: draft.rsyncSource.trim() || undefined,
      rsyncTarget: draft.rsyncTarget.trim() || undefined,
      // 有前置任务时忽略 cronSpec（后端也会校验）
      cronSpec: dependsOnTaskId ? undefined : draft.cronSpec.trim() || undefined,
    };

    setSaving(true);
    try {
      await onSave(input);
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      icon={isEditing ? <Pencil className="size-5 text-primary" /> : <Plus className="size-5 text-primary" />}
      title={isEditing ? "编辑任务" : "新建任务"}
      description={isEditing ? "修改任务配置，保存后立即生效。" : "创建新的备份或同步任务，选择目标节点和执行策略。"}
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? "保存修改" : "创建任务"}
      savingLabel={isEditing ? "保存中..." : "创建中..."}
    >
      <div>
        <label htmlFor="task-editor-name" className="mb-1 block text-sm font-medium">任务名称</label>
        <Input id="task-editor-name" placeholder="例如：每日全量备份-prod-01"
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="task-editor-node" className="mb-1 block text-sm font-medium">目标节点</label>
        <AppSelect id="task-editor-node" containerClassName="w-full"
          value={draft.nodeId}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, nodeId: event.target.value }))
          }
        >
          <option value="">选择节点</option>
          {nodes.map((node) => (
            <option key={node.id} value={String(node.id)}>
              {node.name} ({node.host})
            </option>
          ))}
        </AppSelect>
      </div>

      <div>
        <label htmlFor="task-editor-policy" className="mb-1 block text-sm font-medium">
          关联策略（可选）
        </label>
        <AppSelect
          id="task-editor-policy"
          containerClassName="w-full"
          value={draft.policyId}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, policyId: event.target.value }))
          }
        >
          <option value="">不绑定策略（自定义任务）</option>
          {policies.map((policy) => (
            <option key={policy.id} value={String(policy.id)}>
              {policy.name}
            </option>
          ))}
        </AppSelect>
      </div>

      {tasks && tasks.length > 0 && (
        <div>
          <label htmlFor="task-editor-depends-on" className="mb-1 block text-sm font-medium">
            前置任务（可选）
          </label>
          <AppSelect
            id="task-editor-depends-on"
            containerClassName="w-full"
            value={draft.dependsOnTaskId}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, dependsOnTaskId: event.target.value, cronSpec: event.target.value ? "" : prev.cronSpec }))
            }
          >
            <option value="">无前置任务</option>
            {tasks
              .filter((t) => t.id !== editingTask?.id)
              .map((t) => (
                <option key={t.id} value={String(t.id)}>
                  {t.name ?? t.policyName}
                </option>
              ))}
          </AppSelect>
          {draft.dependsOnTaskId && (
            <p className="mt-1 text-xs text-muted-foreground">
              设置了前置任务后，Cron 调度将被忽略，任务仅在前置任务成功后自动触发。
            </p>
          )}
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <div className="mb-1 text-sm font-medium">
            执行器类型
          </div>
          <div className="glass-panel flex h-10 items-center px-3 text-sm text-muted-foreground">
            Rsync 执行器
          </div>
        </div>
        <div>
          <div className="mb-1 text-sm font-medium">
            调度方式
          </div>
          <div className="glass-panel flex h-10 items-center px-3 text-sm text-muted-foreground">
            {draft.cronSpec ? "定时调度" : "手动触发"}
          </div>
        </div>
      </div>

      <div>
        <label htmlFor="task-editor-cron" className="mb-1 block text-sm font-medium">
          Cron（可选）
        </label>
        <CronGenerator
          id="task-editor-cron"
          value={draft.cronSpec}
          onChange={(val) =>
            setDraft((prev) => ({ ...prev, cronSpec: val }))
          }
          disabled={saving}
        />
        <p className="mt-1 text-xs text-muted-foreground">
          留空则为手动触发任务，不会自动调度执行。
        </p>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="task-editor-rsync-source" className="mb-1 block text-sm font-medium">
            Rsync 源路径（可选）
          </label>
          <Input
            id="task-editor-rsync-source"
            placeholder="/data/source"
            value={draft.rsyncSource}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                rsyncSource: event.target.value,
              }))
            }
          />
        </div>
        <div>
          <label htmlFor="task-editor-rsync-target" className="mb-1 block text-sm font-medium">
            Rsync 目标路径（可选）
          </label>
          <Input
            id="task-editor-rsync-target"
            placeholder="/backup/target"
            value={draft.rsyncTarget}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                rsyncTarget: event.target.value,
              }))
            }
          />
        </div>
      </div>
    </FormDialog>
  );
}

export { TaskEditorDialog as TaskCreateDialog };

export type { TaskDraft };
