import { useEffect, useState } from "react";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { toast } from "@/components/ui/toast";
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
  cronSpec: "",
};

function toNumberOrNull(value: string): number | null {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    return null;
  }
  return parsed;
}

function toTaskExecutorType(value: string): TaskExecutorType {
  if (value === "local") {
    return "local";
  }
  return "rsync";
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
  const [draft, setDraft] = useState<TaskDraft>(defaultDraft);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) {
      setDraft(defaultDraft);
    }
  }, [open]);

  const handleOpenChange = (next: boolean) => {
    onOpenChange(next);
  };

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
      command: draft.command.trim() || undefined,
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
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent size="md">
        <DialogHeader>
          <div className="flex items-center gap-2">
            <Plus className="size-5 text-primary" />
            <DialogTitle>新建任务</DialogTitle>
          </div>
          <DialogDescription>
            创建新的备份或同步任务，选择目标节点和执行策略。
          </DialogDescription>
          <DialogCloseButton />
        </DialogHeader>

        <DialogBody className="space-y-3">
          <div>
            <label className="mb-1 block text-sm font-medium">任务名称</label>
            <Input
              placeholder="例如：每日全量备份-prod-01"
              value={draft.name}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, name: event.target.value }))
              }
            />
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">目标节点</label>
            <select
              className="h-10 w-full rounded-md border bg-background px-3 text-sm"
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
            </select>
          </div>

          <div>
            <label className="mb-1 block text-sm font-medium">
              关联策略（可选）
            </label>
            <select
              className="h-10 w-full rounded-md border bg-background px-3 text-sm"
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
            </select>
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <label className="mb-1 block text-sm font-medium">
                执行器类型
              </label>
              <select
                className="h-10 w-full rounded-md border bg-background px-3 text-sm"
                value={draft.executorType}
                onChange={(event) =>
                  setDraft((prev) => ({
                    ...prev,
                    executorType: toTaskExecutorType(event.target.value),
                  }))
                }
              >
                <option value="rsync">Rsync 执行器</option>
                <option value="local">本地执行器</option>
              </select>
            </div>
            <div>
              <label className="mb-1 block text-sm font-medium">
                Cron（可选）
              </label>
              <Input
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

          <div>
            <label className="mb-1 block text-sm font-medium">
              命令（可选）
            </label>
            <Input
              placeholder="自定义执行命令"
              value={draft.command}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, command: event.target.value }))
              }
            />
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <div>
              <label className="mb-1 block text-sm font-medium">
                Rsync 源路径（可选）
              </label>
              <Input
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
              <label className="mb-1 block text-sm font-medium">
                Rsync 目标路径（可选）
              </label>
              <Input
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
        </DialogBody>

        <DialogFooter>
          <Button variant="outline" onClick={() => handleOpenChange(false)}>
            取消
          </Button>
          <Button onClick={handleSave} disabled={saving}>
            {saving ? "创建中..." : "创建任务"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export type { TaskDraft };
