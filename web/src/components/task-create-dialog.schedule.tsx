import { useTranslation } from "react-i18next";
import { CronGenerator } from "@/components/cron-generator";
import type { TaskDraft } from "@/components/task-create-dialog";

type TaskScheduleProps = {
  draft: TaskDraft;
  setDraft: React.Dispatch<React.SetStateAction<TaskDraft>>;
  saving: boolean;
};

export function TaskSchedule({ draft, setDraft, saving }: TaskScheduleProps) {
  const { t } = useTranslation();

  return (
    <div>
      <label htmlFor="task-editor-cron" className="mb-1 block text-sm font-medium">
        {t("taskCreate.cronOptional")}
      </label>
      <CronGenerator
        id="task-editor-cron"
        value={draft.cronSpec}
        onChange={(val) =>
          setDraft((prev) => ({ ...prev, cronSpec: val }))
        }
        disabled={saving || Boolean(draft.dependsOnTaskId)}
      />
      <p className="mt-1 text-xs text-muted-foreground">
        {t("taskCreate.cronEmptyHint")}
      </p>
    </div>
  );
}
