import { useTranslation } from "react-i18next";
import { formatTaskDuration } from "@/lib/date-utils";
import type { TaskRecord } from "@/types/domain";

type TaskLatestCompletionProps = Pick<TaskRecord, "lastFinishedAt" | "lastDurationMs">;

/**
 * Latest ordinary terminal run on the task's current node, shared by the list and card layouts.
 * `lastFinishedAt` is already formatted by the API mapper; duration only belongs to that run,
 * so it is unavailable whenever the completion timestamp is.
 */
export function TaskLatestCompletion({ lastFinishedAt, lastDurationMs }: TaskLatestCompletionProps) {
  const { t } = useTranslation();
  const finishedAt = lastFinishedAt && lastFinishedAt !== "-" ? lastFinishedAt : undefined;
  const duration = finishedAt ? formatTaskDuration(lastDurationMs) : "—";

  return (
    <>
      <p className="tabular-nums">{t("tasks.lastFinishedAtLabel", { time: finishedAt ?? "—" })}</p>
      <p className="tabular-nums">{t("tasks.lastDurationLabel", { duration })}</p>
    </>
  );
}
