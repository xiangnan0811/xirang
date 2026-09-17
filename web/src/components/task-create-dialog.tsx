import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Pencil, Plus } from "lucide-react";
import { FormDialog } from "@/components/ui/form-dialog";
import { InlineAlert } from "@/components/ui/inline-alert";
import { useDialogDraft } from "@/hooks/use-dialog-draft";
import { ApiError } from "@/lib/api/core";
import type {
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  ResticRepositoryVersion,
  TaskExecutorType,
  TaskRecord,
  UpdateTaskInput,
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
  resticPasswordConfigured: boolean;
  resticExcludePatterns: string;
  resticRepositoryVersion: "" | "1" | "2";
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
  resticPasswordConfigured: false,
  resticExcludePatterns: "",
  resticRepositoryVersion: "",
  rcloneBandwidthLimit: "",
  rcloneTransfers: "",
};

function parseExcludePatterns(raw: string): string[] {
  return raw.split("\n").map((pattern) => pattern.trim()).filter(Boolean);
}

function sameStringList(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((item, index) => item === right[index]);
}

function resticSettingsFromTask(task: TaskRecord): { excludePatterns: string; repositoryVersion: TaskDraft["resticRepositoryVersion"] } {
  const settings = task.executorType === "restic" ? task.executorSettings : undefined;
  if (!settings || !("excludePatterns" in settings)) {
    return { excludePatterns: "", repositoryVersion: "" };
  }
  const version = settings.repositoryVersion;
  return {
    excludePatterns: settings.excludePatterns.join("\n"),
    repositoryVersion: version === 1 ? "1" : version === 2 ? "2" : "",
  };
}

function rcloneSettingsFromTask(task: TaskRecord): { bandwidthLimit: string; transfers: string } {
  const settings = task.executorType === "rclone" ? task.executorSettings : undefined;
  if (!settings || !("bandwidthLimit" in settings)) {
    return { bandwidthLimit: "", transfers: "" };
  }
  return {
    bandwidthLimit: settings.bandwidthLimit,
    transfers: settings.transfers > 0 ? String(settings.transfers) : "",
  };
}

function taskRecordToDraft(task: TaskRecord): TaskDraft {
  const restic = resticSettingsFromTask(task);
  const rclone = rcloneSettingsFromTask(task);
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
    resticPassword: "",
    resticPasswordConfigured: task.executorSecretsConfigured?.repositoryPassword === true,
    resticExcludePatterns: restic.excludePatterns,
    resticRepositoryVersion: restic.repositoryVersion,
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

function parseTransfersField(value: string): number {
  const trimmed = value.trim();
  if (trimmed === "") {
    return 0;
  }
  const parsed = Number(trimmed);
  return Number.isInteger(parsed) && parsed >= 0 ? parsed : 0;
}

function parseRepositoryVersion(value: TaskDraft["resticRepositoryVersion"]): ResticRepositoryVersion | null {
  if (value === "1") return 1;
  if (value === "2") return 2;
  return null;
}
function isManagedRclone(task: TaskRecord | null | undefined): boolean {
  return task?.executorType === "rclone"
    && task.rclonePublication?.mode !== undefined
    && task.rclonePublication.mode !== "legacy_mutable";
}

function buildCreateInput(draft: TaskDraft, nodeId: number, dependsOnTaskId: number | null, managedRclone: boolean): NewTaskInput {
  let executorConfig: string | undefined;
  if (draft.executorType === "restic") {
    const config: Record<string, unknown> = {
      repository_password: draft.resticPassword,
      exclude_patterns: parseExcludePatterns(draft.resticExcludePatterns),
    };
    const version = parseRepositoryVersion(draft.resticRepositoryVersion);
    if (version !== null) {
      config.repository_version = version;
    }
    executorConfig = JSON.stringify(config);
  } else if (draft.executorType === "rclone" && !managedRclone) {
    const transfers = toNumberOrNull(draft.rcloneTransfers);
    const config: Record<string, unknown> = {};
    const bandwidth = draft.rcloneBandwidthLimit.trim();
    if (bandwidth) {
      config.bandwidth_limit = bandwidth;
    }
    if (transfers !== null) {
      config.transfers = transfers;
    }
    executorConfig = JSON.stringify(config);
  }

  return {
    name: draft.name.trim(),
    nodeId,
    policyId: toNumberOrNull(draft.policyId),
    dependsOnTaskId,
    executorType: draft.executorType,
    command: draft.executorType === "command" ? draft.command.trim() || undefined : undefined,
    rsyncSource: draft.executorType !== "command" ? draft.rsyncSource.trim() || undefined : undefined,
    rsyncTarget: draft.executorType === "rclone" && !managedRclone ? draft.rsyncTarget.trim() || undefined : undefined,
    executorConfig,
    cronSpec: dependsOnTaskId ? undefined : draft.cronSpec.trim() || undefined,
  };
}

function buildUpdateInput(task: TaskRecord, draft: TaskDraft, nodeId: number, dependsOnTaskId: number | null, managedRclone: boolean): UpdateTaskInput {
  const baseline = taskRecordToDraft(task);
  const input: UpdateTaskInput = {
    expectedRevision: task.revision ?? "",
  };

  const name = draft.name.trim();
  if (name !== baseline.name.trim()) {
    input.name = name;
  }
  const baselineNodeId = toNumberOrNull(baseline.nodeId);
  if (nodeId !== baselineNodeId) {
    input.nodeId = nodeId;
  }
  const policyId = toNumberOrNull(draft.policyId);
  const baselinePolicyId = toNumberOrNull(baseline.policyId);
  if (policyId !== baselinePolicyId) {
    input.policyId = policyId;
  }
  const baselineDependsOn = toNumberOrNull(baseline.dependsOnTaskId);
  if (dependsOnTaskId !== baselineDependsOn) {
    input.dependsOnTaskId = dependsOnTaskId;
  }
  if (draft.executorType !== baseline.executorType) {
    input.executorType = draft.executorType;
  }
  if (draft.executorType === "command" && draft.command.trim() !== baseline.command.trim()) {
    input.command = draft.command.trim();
  }
  if (draft.executorType !== "command" && draft.rsyncSource.trim() !== baseline.rsyncSource.trim()) {
    input.rsyncSource = draft.rsyncSource.trim();
  }
  if (draft.executorType === "rclone" && !managedRclone && draft.rsyncTarget.trim() !== baseline.rsyncTarget.trim()) {
    input.rsyncTarget = draft.rsyncTarget.trim();
  }

  const cronSpec = draft.cronSpec.trim();
  if (cronSpec !== baseline.cronSpec.trim() || (dependsOnTaskId !== null && dependsOnTaskId !== baselineDependsOn)) {
    input.cronSpec = cronSpec;
  }

  if (draft.executorType === "restic") {
    const excludePatterns = parseExcludePatterns(draft.resticExcludePatterns);
    const baselineExcludes = parseExcludePatterns(baseline.resticExcludePatterns);
    const repositoryVersion = parseRepositoryVersion(draft.resticRepositoryVersion);
    const baselineVersion = parseRepositoryVersion(baseline.resticRepositoryVersion);
    const settings: NonNullable<UpdateTaskInput["executorSettings"]> = {};
    if (!sameStringList(excludePatterns, baselineExcludes)) {
      settings.excludePatterns = excludePatterns;
    }
    if (repositoryVersion !== baselineVersion) {
      settings.repositoryVersion = repositoryVersion;
    }
    if (Object.keys(settings).length > 0) {
      input.executorSettings = settings;
    }
    const password = draft.resticPassword;
    if (password.trim() !== "") {
      input.executorSecrets = { repositoryPassword: password };
    }
  } else if (draft.executorType === "rclone" && !managedRclone) {
    const bandwidth = draft.rcloneBandwidthLimit.trim();
    const baselineBandwidth = baseline.rcloneBandwidthLimit.trim();
    const transfers = parseTransfersField(draft.rcloneTransfers);
    const baselineTransfers = parseTransfersField(baseline.rcloneTransfers);
    const settings: NonNullable<UpdateTaskInput["executorSettings"]> = {};
    if (bandwidth !== baselineBandwidth) {
      settings.bandwidthLimit = bandwidth;
    }
    if (transfers !== baselineTransfers) {
      settings.transfers = transfers;
    }
    if (Object.keys(settings).length > 0) {
      input.executorSettings = settings;
    }
  }

  return input;
}

type TaskEditorDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks?: TaskRecord[];
  onCreate?: (input: NewTaskInput) => Promise<void>;
  onUpdate?: (input: UpdateTaskInput) => Promise<void>;
  editingTask?: TaskRecord | null;
};

export function TaskEditorDialog({
  open,
  onOpenChange,
  nodes,
  policies,
  tasks,
  onCreate,
  onUpdate,
  editingTask,
}: TaskEditorDialogProps) {
  const { t } = useTranslation();
  const isEditing = Boolean(editingTask);
  const managedRclone = isManagedRclone(editingTask);
  const [draft, setDraft] = useDialogDraft<TaskDraft, TaskRecord>(
    open,
    defaultDraft,
    editingTask,
    taskRecordToDraft,
  );
  const [saving, setSaving] = useState(false);
  const [errors, setErrors] = useState<{ name?: string | null; nodeId?: string | null }>({});
  const [conflict, setConflict] = useState(false);

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
    setSaving(true);
    try {
      if (editingTask) {
        const input = buildUpdateInput(editingTask, draft, nodeId, dependsOnTaskId, managedRclone);
        if (!input.expectedRevision) {
          setConflict(true);
          return;
        }
        await onUpdate?.(input);
      } else {
        await onCreate?.(buildCreateInput(draft, nodeId, dependsOnTaskId, managedRclone));
      }
      setConflict(false);
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        setConflict(true);
        return;
      }
      throw error;
    } finally {
      setSaving(false);
    }
  };

  return (
    <FormDialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          setConflict(false);
        }
        onOpenChange(nextOpen);
      }}
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
      {conflict ? (
        <InlineAlert tone="warning" title={t("taskCreate.conflictStaleRevisionTitle")}>
          {t("taskCreate.conflictStaleRevision")}
        </InlineAlert>
      ) : null}

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
        managedRclone={managedRclone}
      />
    </FormDialog>
  );
}

export { TaskEditorDialog as TaskCreateDialog };
