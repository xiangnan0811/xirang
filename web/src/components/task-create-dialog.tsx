import { useState } from "react";
import { Plus } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { AppSelect } from "@/components/ui/app-select";
import { toast } from "@/components/ui/toast";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import type {
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  TaskExecutorType,
} from "@/types/domain";

type TaskDraft = {
  name: string;
  nodeId: string;
  policyId: string;
  executorType: TaskExecutorType;
  rsyncSource: string;
  rsyncTarget: string;
  cronSpec: string;
};

const defaultDraft: TaskDraft = {
  name: "",
  nodeId: "",
  policyId: "",
  executorType: "rsync",
  rsyncSource: "",
  rsyncTarget: "",
  cronSpec: "",
};

function toNumberOrNull(value: string): number | null {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }
  return parsed;
}

type TaskCreateDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  onSave: (input: NewTaskInput) => Promise<void>;
};

export function TaskCreateDialog({
  open,
  onOpenChange,
  nodes,
  policies,
  onSave,
}: TaskCreateDialogProps) {
  const [draft, setDraft] = useDialogDraft<TaskDraft>(open, defaultDraft);
  const [saving, setSaving] = useState(false);

  const handleSave = async () => {
    const nodeId = toNumberOrNull(draft.nodeId);
    if (!nodeId) {
      toast.error("创建失败：请选择目标节点。", { id: "task-create-node-required" });
      return;
    }

    if (!draft.name.trim()) {
      toast.error("创建失败：请输入任务名称。", { id: "task-create-name-required" });
      return;
    }

    const input: NewTaskInput = {
      name: draft.name.trim(),
      nodeId,
      policyId: toNumberOrNull(draft.policyId),
      executorType: draft.executorType,
      rsyncSource: draft.rsyncSource.trim() || undefined,
      rsyncTarget: draft.rsyncTarget.trim() || undefined,
      cronSpec: draft.cronSpec.trim() || undefined,
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
      icon={<Plus className="size-5 text-primary" />}
      title="新建任务"
      description="创建新的备份或同步任务，选择目标节点和执行策略。"
      saving={saving}
      onSubmit={handleSave}
      submitLabel="创建任务"
      savingLabel="创建中..."
    >
      <div>
        <label htmlFor="task-create-name" className="mb-1 block text-sm font-medium">任务名称</label>
        <Input id="task-create-name" placeholder="例如：每日全量备份-prod-01"
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="task-create-node" className="mb-1 block text-sm font-medium">目标节点</label>
        <AppSelect id="task-create-node" className="w-full"
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
        <label htmlFor="task-create-policy" className="mb-1 block text-sm font-medium">
          关联策略（可选）
        </label>
        <AppSelect
          id="task-create-policy"
          className="w-full"
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
          <label htmlFor="task-create-cron" className="mb-1 block text-sm font-medium">
            Cron（可选）
          </label>
          <Input
            id="task-create-cron"
            placeholder="例如：0 */2 * * *"
            value={draft.cronSpec}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                cronSpec: event.target.value,
              }))
            }
          />
        </div>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <label htmlFor="task-create-rsync-source" className="mb-1 block text-sm font-medium">
            Rsync 源路径（可选）
          </label>
          <Input
            id="task-create-rsync-source"
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
          <label htmlFor="task-create-rsync-target" className="mb-1 block text-sm font-medium">
            Rsync 目标路径（可选）
          </label>
          <Input
            id="task-create-rsync-target"
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

export type { TaskDraft };
