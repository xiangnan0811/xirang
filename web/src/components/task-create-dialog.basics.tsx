import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Select } from "@/components/ui/select";
import type {
  NodeRecord,
  PolicyRecord,
  RsyncPublicationState,
  TaskExecutorType,
  TaskRecord,
} from "@/types/domain";
import type { TaskDraft } from "@/components/task-create-dialog";

function publicationStateTone(state: RsyncPublicationState): "success" | "warning" | "destructive" | "info" | "neutral" {
  switch (state) {
    case "ready":
    case "committed":
      return "success";
    case "preparing":
    case "verifying":
      return "info";
    case "failed":
    case "blocked":
      return "destructive";
    case "preflight_required":
    case "rollback_prepared":
      return "warning";
    default:
      return "neutral";
  }
}

type TaskBasicsProps = {
  draft: TaskDraft;
  setDraft: React.Dispatch<React.SetStateAction<TaskDraft>>;
  nodes: NodeRecord[];
  policies: PolicyRecord[];
  tasks?: TaskRecord[];
  editingTask?: TaskRecord | null;
  saving: boolean;
  errors?: { name?: string | null; nodeId?: string | null };
};

export function TaskBasics({
  draft,
  setDraft,
  nodes,
  policies,
  tasks,
  editingTask,
  errors,
}: TaskBasicsProps) {
  const { t } = useTranslation();

  return (
    <>
      <div>
        <label htmlFor="task-editor-name" className="mb-1 block text-sm font-medium">
          {t("taskCreate.taskName")}
        </label>
        <Input
          id="task-editor-name"
          name="task-name"
          placeholder={t("taskCreate.taskNamePlaceholder")}
          value={draft.name}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, name: event.target.value }))
          }
          aria-invalid={errors?.name ? true : undefined}
          aria-describedby={errors?.name ? "task-editor-name-error" : undefined}
        />
        {errors?.name ? (
          <p id="task-editor-name-error" role="alert" className="mt-1 text-xs text-destructive">{errors.name}</p>
        ) : null}
      </div>

      <div>
        <label htmlFor="task-editor-node" className="mb-1 block text-sm font-medium">
          {t("taskCreate.targetNode")}
        </label>
        <Select
          id="task-editor-node"
          containerClassName="w-full"
          value={draft.nodeId}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, nodeId: event.target.value }))
          }
          aria-invalid={errors?.nodeId ? true : undefined}
          aria-describedby={errors?.nodeId ? "task-editor-node-error" : undefined}
        >
          <option value="">{t("taskCreate.selectNode")}</option>
          {nodes.map((node) => (
            <option key={node.id} value={String(node.id)}>
              {node.name} ({node.host})
            </option>
          ))}
        </Select>
        {errors?.nodeId ? (
          <p id="task-editor-node-error" role="alert" className="mt-1 text-xs text-destructive">{errors.nodeId}</p>
        ) : null}
      </div>

      <div>
        <label htmlFor="task-editor-policy" className="mb-1 block text-sm font-medium">
          {t("taskCreate.relatedPolicy")}
        </label>
        <Select
          id="task-editor-policy"
          containerClassName="w-full"
          value={draft.policyId}
          onChange={(event) =>
            setDraft((prev) => ({ ...prev, policyId: event.target.value }))
          }
        >
          <option value="">{t("taskCreate.noPolicyCustom")}</option>
          {policies.map((policy) => (
            <option key={policy.id} value={String(policy.id)}>
              {policy.name}
            </option>
          ))}
        </Select>
      </div>

      <div>
        <label htmlFor="task-editor-executor-type" className="mb-1 block text-sm font-medium">
          {t("taskCreate.executorType")}
        </label>
        <Select
          id="task-editor-executor-type"
          containerClassName="w-full"
          value={draft.executorType}
          onChange={(event) =>
            setDraft((prev) => ({
              ...prev,
              executorType: event.target.value as TaskExecutorType,
            }))
          }
        >
          <option value="rsync">{t("taskCreate.executorTypes.rsync")}</option>
          <option value="command">{t("taskCreate.executorTypes.command")}</option>
          <option value="restic">{t("taskCreate.executorTypes.restic")}</option>
          <option value="rclone">{t("taskCreate.executorTypes.rclone")}</option>
        </Select>
      </div>

      {draft.executorType === "rsync" && editingTask?.rsyncPublication ? (
        <div className="space-y-2 rounded-md border border-border bg-muted/30 px-3 py-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <p className="text-xs text-muted-foreground">{t("rsyncVersioning.currentMode")}</p>
              <p className="text-sm font-medium">{t("rsyncVersioning.mode." + editingTask.rsyncPublication.mode)}</p>
            </div>
            <Badge tone={publicationStateTone(editingTask.rsyncPublication.state)}>
              {t("rsyncVersioning.state." + editingTask.rsyncPublication.state)}
            </Badge>
          </div>
          {editingTask.rsyncPublication.state === "blocked" ? (
            <InlineAlert tone="critical">
              {t("rsyncVersioning.reason." + editingTask.rsyncPublication.reasonCode)}
            </InlineAlert>
          ) : null}
        </div>
      ) : null}

      {tasks && tasks.length > 0 && (
        <div>
          <label htmlFor="task-editor-depends-on" className="mb-1 block text-sm font-medium">
            {t("taskCreate.dependsOnTask")}
          </label>
          <Select
            id="task-editor-depends-on"
            containerClassName="w-full"
            value={draft.dependsOnTaskId}
            onChange={(event) =>
              setDraft((prev) => ({
                ...prev,
                dependsOnTaskId: event.target.value,
                cronSpec: event.target.value ? "" : prev.cronSpec,
              }))
            }
          >
            <option value="">{t("taskCreate.noDependency")}</option>
            {tasks
              .filter((t) => t.id !== editingTask?.id)
              .map((t) => (
                <option key={t.id} value={String(t.id)}>
                  {t.name ?? t.policyName}
                </option>
              ))}
          </Select>
          {draft.dependsOnTaskId && (
            <p className="mt-1 text-xs text-muted-foreground">
              {t("taskCreate.dependsOnHint")}
            </p>
          )}
        </div>
      )}
    </>
  );
}
