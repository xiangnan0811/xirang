import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Plus } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import type {
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  TaskExecutorType,
  TaskRecord,
} from "@/types/domain";
import { TaskBasics } from "@/components/task-create-dialog.basics";
import { TaskSchedule } from "@/components/task-create-dialog.schedule";
import { TaskAdvanced } from "@/components/task-create-dialog.advanced";

export type TaskDraft = {
  name: string;
  nodeId: string;
  policyId: string;
  dependsOnTaskId: string;
  executorType: TaskExecutorType;
  rsyncSource: string;
  rsyncTarget: string;
  cronSpec: string;
  command: string;
  resticPassword: string;
  resticExcludePatterns: string;
  rcloneBandwidthLimit: string;
  rcloneTransfers: string;
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
  command: "",
  resticPassword: "",
  resticExcludePatterns: "",
  rcloneBandwidthLimit: "",
  rcloneTransfers: "",
};

function parseResticConfig(cfg: string): { password: string; excludePatterns: string } {
  if (!cfg) return { password: "", excludePatterns: "" };
  try {
    const parsed = JSON.parse(cfg) as { repository_password?: string; exclude_patterns?: string[] };
    return {
      password: parsed.repository_password ?? "",
      excludePatterns: (parsed.exclude_patterns ?? []).join("\n"),
    };
  } catch {
    return { password: "", excludePatterns: "" };
  }
}

function parseRcloneConfig(cfg: string): { bandwidthLimit: string; transfers: string } {
  if (!cfg) return { bandwidthLimit: "", transfers: "" };
  try {
    const parsed = JSON.parse(cfg) as { bandwidth_limit?: string; transfers?: number };
    return {
      bandwidthLimit: parsed.bandwidth_limit ?? "",
      transfers: parsed.transfers ? String(parsed.transfers) : "",
    };
  } catch {
    return { bandwidthLimit: "", transfers: "" };
  }
}

function taskRecordToDraft(task: TaskRecord): TaskDraft {
  const restic = parseResticConfig(task.executorConfig ?? "");
  const rclone = parseRcloneConfig(task.executorConfig ?? "");
  return {
    name: task.name ?? task.policyName ?? "",
    nodeId: task.nodeId ? String(task.nodeId) : "",
    policyId: task.policyId ? String(task.policyId) : "",
    dependsOnTaskId: task.dependsOnTaskId ? String(task.dependsOnTaskId) : "",
    executorType: task.executorType ?? "rsync",
    rsyncSource: task.rsyncSource ?? "",
    rsyncTarget: task.rsyncTarget ?? "",
    cronSpec: task.cronSpec ?? "",
    command: task.command ?? "",
    resticPassword: restic.password,
    resticExcludePatterns: restic.excludePatterns,
    rcloneBandwidthLimit: rclone.bandwidthLimit,
    rcloneTransfers: rclone.transfers,
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
  const { t } = useTranslation();
  const isEditing = Boolean(editingTask);
  const [draft, setDraft] = useDialogDraft<TaskDraft, TaskRecord>(
    open,
    defaultDraft,
    editingTask,
    taskRecordToDraft,
  );
  const [saving, setSaving] = useState(false);
  const [errors, setErrors] = useState<{ name?: string | null; nodeId?: string | null }>({});

  const handleSave = async () => {
    setErrors({});
    const nodeId = toNumberOrNull(draft.nodeId);
    if (!nodeId) {
      setErrors({ nodeId: t("taskCreate.errorNodeRequired") });
      return;
    }

    if (!draft.name.trim()) {
      setErrors({ name: t("taskCreate.errorNameRequired") });
      return;
    }

    const dependsOnTaskId = toNumberOrNull(draft.dependsOnTaskId);

    let executorConfig: string | undefined;
    if (draft.executorType === "restic") {
      const excludePatterns = draft.resticExcludePatterns
        .split("\n")
        .map((p) => p.trim())
        .filter(Boolean);
      executorConfig = JSON.stringify({
        repository_password: draft.resticPassword.trim(),
        exclude_patterns: excludePatterns,
      });
    } else if (draft.executorType === "rclone") {
      const transfers = toNumberOrNull(draft.rcloneTransfers);
      executorConfig = JSON.stringify({
        bandwidth_limit: draft.rcloneBandwidthLimit.trim() || undefined,
        transfers: transfers ?? undefined,
      });
    }

    const input: NewTaskInput = {
      name: draft.name.trim(),
      nodeId,
      policyId: toNumberOrNull(draft.policyId),
      dependsOnTaskId: dependsOnTaskId,
      executorType: draft.executorType,
      command: draft.executorType === "command" ? draft.command.trim() || undefined : undefined,
      rsyncSource: draft.executorType !== "command" ? draft.rsyncSource.trim() || undefined : undefined,
      rsyncTarget: draft.executorType === "rclone" ? draft.rsyncTarget.trim() || undefined : undefined,
      executorConfig,
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
      icon={
        isEditing ? (
          <Pencil className="size-5 text-primary" />
        ) : (
          <Plus className="size-5 text-primary" />
        )
      }
      title={isEditing ? t("taskCreate.titleEdit") : t("taskCreate.titleCreate")}
      description={isEditing ? t("taskCreate.descEdit") : t("taskCreate.descCreate")}
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? t("taskCreate.submitEdit") : t("taskCreate.submitCreate")}
      savingLabel={isEditing ? t("taskCreate.savingEdit") : t("taskCreate.savingCreate")}
    >
      <TaskBasics
        draft={draft}
        setDraft={setDraft}
        nodes={nodes}
        policies={policies}
        tasks={tasks}
        editingTask={editingTask}
        saving={saving}
        errors={errors}
      />

      <TaskSchedule draft={draft} setDraft={setDraft} saving={saving} />

      <TaskAdvanced
        draft={draft}
        setDraft={setDraft}
        nodes={nodes}
        isEditing={isEditing}
      />
    </FormDialog>
  );
}

export { TaskEditorDialog as TaskCreateDialog };
