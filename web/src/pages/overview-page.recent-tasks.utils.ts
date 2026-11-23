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

export function formatTaskDuration(durationMs?: number): string {
  if (typeof durationMs !== "number" || !Number.isFinite(durationMs) || durationMs < 0) {
    return "—";
  }
  if (durationMs < 1000) {
    return `${Math.round(durationMs)}ms`;
  }
  const totalSeconds = Math.floor(durationMs / 1000);
  if (totalSeconds < 60) {
    return `${totalSeconds}s`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const remainSeconds = totalSeconds % 60;
  if (minutes < 60) {
    return `${minutes}m ${remainSeconds}s`;
  }
  const hours = Math.floor(minutes / 60);
  const remainMinutes = minutes % 60;
  return `${hours}h ${remainMinutes}m`;
}
