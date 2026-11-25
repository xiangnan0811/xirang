import type { TaskRecord } from "@/types/domain";

export function parseTaskTimestamp(value?: string): number {
  if (!value || value === "-") {
    return 0;
  }
  const direct = Date.parse(value);
  if (Number.isFinite(direct)) {
    return direct;
  }
  if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(value)) {
    const normalized = Date.parse(value.replace(" ", "T"));
    if (Number.isFinite(normalized)) {
      return normalized;
    }
  }
  return 0;
}

export function taskRecentActivityTimestamp(task: TaskRecord): number {
  if (typeof task.lastActivityMs === "number" && Number.isFinite(task.lastActivityMs) && task.lastActivityMs > 0) {
    return task.lastActivityMs;
  }
  return Math.max(
    parseTaskTimestamp(task.lastFinishedAt),
    parseTaskTimestamp(task.lastRunAt),
  );
}

export function sortRecentTasks(tasks: TaskRecord[], limit = 5): TaskRecord[] {
  return [...tasks]
    .sort((first, second) => {
      const activityGap = taskRecentActivityTimestamp(second) - taskRecentActivityTimestamp(first);
      if (activityGap !== 0) {
        return activityGap;
      }
      const updatedGap = parseTaskTimestamp(second.updatedAt) - parseTaskTimestamp(first.updatedAt);
      if (updatedGap !== 0) {
        return updatedGap;
      }
      const createdGap = parseTaskTimestamp(second.createdAt) - parseTaskTimestamp(first.createdAt);
      if (createdGap !== 0) {
        return createdGap;
      }
      return second.id - first.id;
    })
    .slice(0, limit);
}
