import { useState } from "react";
import { useTranslation } from "react-i18next";
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

// executor labels moved to translation keys: taskCreate.executorTypes.*

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

  const handleSave = async () => {
    const nodeId = toNumberOrNull(draft.nodeId);
    if (!nodeId) {
      toast.error(t('taskCreate.errorNodeRequired'), { id: "task-editor-node-required" });
      return;
    }

    if (!draft.name.trim()) {
      toast.error(t('taskCreate.errorNameRequired'), { id: "task-editor-name-required" });
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
      rsyncTarget: draft.executorType !== "command" ? draft.rsyncTarget.trim() || undefined : undefined,
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
      icon={isEditing ? <Pencil className="size-5 text-primary" /> : <Plus className="size-5 text-primary" />}
      title={isEditing ? t('taskCreate.titleEdit') : t('taskCreate.titleCreate')}
      description={isEditing ? t('taskCreate.descEdit') : t('taskCreate.descCreate')}
      saving={saving}
      onSubmit={handleSave}
      submitLabel={isEditing ? t('taskCreate.submitEdit') : t('taskCreate.submitCreate')}
      savingLabel={isEditing ? t('taskCreate.savingEdit') : t('taskCreate.savingCreate')}
    >
      <div>
        <label htmlFor="task-editor-name" className="mb-1 block text-sm font-medium">{t('taskCreate.taskName')}</label>
        <Input id="task-editor-name" placeholder={t('taskCreate.taskNamePlaceholder')}
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
        />
      </div>

      <div>
        <label htmlFor="task-editor-node" className="mb-1 block text-sm font-medium">{t('taskCreate.targetNode')}</label>
        <AppSelect id="task-editor-node" containerClassName="w-full"
          value={draft.nodeId}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, nodeId: event.target.value }))
          }
        >
          <option value="">{t('taskCreate.selectNode')}</option>
          {nodes.map((node) => (
            <option key={node.id} value={String(node.id)}>
              {node.name} ({node.host})
            </option>
          ))}
        </AppSelect>
      </div>

      <div>
        <label htmlFor="task-editor-policy" className="mb-1 block text-sm font-medium">
          {t('taskCreate.relatedPolicy')}
        </label>
        <AppSelect
          id="task-editor-policy"
          containerClassName="w-full"
          value={draft.policyId}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, policyId: event.target.value }))
          }
        >
          <option value="">{t('taskCreate.noPolicyCustom')}</option>
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
            {t('taskCreate.dependsOnTask')}
          </label>
          <AppSelect
            id="task-editor-depends-on"
            containerClassName="w-full"
            value={draft.dependsOnTaskId}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, dependsOnTaskId: event.target.value, cronSpec: event.target.value ? "" : prev.cronSpec }))
            }
          >
            <option value="">{t('taskCreate.noDependency')}</option>
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
              {t('taskCreate.dependsOnHint')}
            </p>
          )}
        </div>
      )}

      <div>
        <label htmlFor="task-editor-executor-type" className="mb-1 block text-sm font-medium">{t('taskCreate.executorType')}</label>
        <AppSelect
          id="task-editor-executor-type"
          containerClassName="w-full"
          value={draft.executorType}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, executorType: event.target.value as TaskExecutorType }))
          }
        >
          <option value="rsync">{t('taskCreate.executorTypes.rsync')}</option>
          <option value="command">{t('taskCreate.executorTypes.command')}</option>
          <option value="restic">{t('taskCreate.executorTypes.restic')}</option>
          <option value="rclone">{t('taskCreate.executorTypes.rclone')}</option>
        </AppSelect>
      </div>

      <div>
        <label htmlFor="task-editor-cron" className="mb-1 block text-sm font-medium">
          {t('taskCreate.cronOptional')}
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
          {t('taskCreate.cronEmptyHint')}
        </p>
      </div>

      {draft.executorType === "command" && (
        <div>
          <label htmlFor="task-editor-command" className="mb-1 block text-sm font-medium">
            {t('taskCreate.shellCommand')}
          </label>
          <Input
            id="task-editor-command"
            placeholder={t('taskCreate.commandPlaceholder')}
            value={draft.command}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, command: event.target.value }))
            }
          />
          <p className="mt-1 text-xs text-muted-foreground">
            {t('taskCreate.shellCommandHint')}
          </p>
        </div>
      )}

      {(draft.executorType === "rsync" || draft.executorType === "restic" || draft.executorType === "rclone") && (
        <div className="grid gap-3 md:grid-cols-2">
          <div>
            <label htmlFor="task-editor-rsync-source" className="mb-1 block text-sm font-medium">
              {draft.executorType === "rsync" ? t('taskCreate.rsyncSourcePath') : t('taskCreate.sourcePath')}
            </label>
            <Input
              id="task-editor-rsync-source"
              placeholder="/data/source"
              value={draft.rsyncSource}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, rsyncSource: event.target.value }))
              }
            />
          </div>
          <div>
            <label htmlFor="task-editor-rsync-target" className="mb-1 block text-sm font-medium">
              {draft.executorType === "rsync" && t('taskCreate.rsyncTargetPath')}
              {draft.executorType === "restic" && t('taskCreate.resticRepoPath')}
              {draft.executorType === "rclone" && t('taskCreate.rcloneRemotePath')}
            </label>
            <Input
              id="task-editor-rsync-target"
              placeholder={
                draft.executorType === "restic"
                  ? "/backup/restic-repo"
                  : draft.executorType === "rclone"
                  ? "s3:my-bucket/backups"
                  : "/backup/target"
              }
              value={draft.rsyncTarget}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, rsyncTarget: event.target.value }))
              }
            />
          </div>
        </div>
      )}

      {draft.executorType === "restic" && (
        <>
          <div>
            <label htmlFor="task-editor-restic-password" className="mb-1 block text-sm font-medium">
              {t('taskCreate.resticRepoPassword')}
            </label>
            <Input
              id="task-editor-restic-password"
              type="password"
              placeholder={t('taskCreate.resticPassword')}
              value={draft.resticPassword}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, resticPassword: event.target.value }))
              }
            />
          </div>
          <div>
            <label htmlFor="task-editor-restic-excludes" className="mb-1 block text-sm font-medium">
              {t('taskCreate.resticExcludeRules')}
            </label>
            <textarea
              id="task-editor-restic-excludes"
              className="glass-panel w-full min-h-[72px] resize-none rounded-md px-3 py-2 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
              placeholder={"*.log\n/tmp\n/proc"}
              value={draft.resticExcludePatterns}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, resticExcludePatterns: event.target.value }))
              }
            />
          </div>
        </>
      )}

      {draft.executorType === "rclone" && (
        <div className="grid gap-3 md:grid-cols-2">
          <div>
            <label htmlFor="task-editor-rclone-bwlimit" className="mb-1 block text-sm font-medium">
              {t('taskCreate.rcloneBandwidthLimit')}
            </label>
            <Input
              id="task-editor-rclone-bwlimit"
              placeholder={t('taskCreate.bwLimitPlaceholder')}
              value={draft.rcloneBandwidthLimit}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, rcloneBandwidthLimit: event.target.value }))
              }
            />
          </div>
          <div>
            <label htmlFor="task-editor-rclone-transfers" className="mb-1 block text-sm font-medium">
              {t('taskCreate.rcloneConcurrentTransfers')}
            </label>
            <Input
              id="task-editor-rclone-transfers"
              type="number"
              min={1}
              max={32}
              placeholder={t('taskCreate.concurrencyPlaceholder')}
              value={draft.rcloneTransfers}
              onChange={(event) =>
                setDraft((prev) => ({ ...prev, rcloneTransfers: event.target.value }))
              }
            />
          </div>
        </div>
      )}
    </FormDialog>
  );
}

export { TaskEditorDialog as TaskCreateDialog };

export type { TaskDraft };
