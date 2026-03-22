import type { TaskStatus, TaskRecord } from "@/types/domain";

export function canTrigger(task: TaskRecord): boolean {
  return task.enabled !== false && task.status !== "running" && task.status !== "retrying";
}

export function canCancel(status: TaskStatus) {
  return status === "running" || status === "retrying";
}

export function canPause(task: TaskRecord): boolean {
  return task.enabled === true;
}

export function canResume(task: TaskRecord): boolean {
  return task.enabled === false;
}

export function canSkipNext(task: TaskRecord): boolean {
  return task.enabled !== false && !!task.cronSpec && !task.skipNext;
}

export function normalizeStatusFilter(value: string): "all" | "paused" | TaskStatus {
  if (
    value === "all" ||
    value === "paused" ||
    value === "pending" ||
    value === "running" ||
    value === "retrying" ||
    value === "failed" ||
    value === "success" ||
    value === "canceled" ||
    value === "warning"
  ) {
    return value;
  }
  return "all";
}

export type PendingActionType = { id: number; action: "retry" | "cancel" | "delete" | "trigger" | "edit" | "pause" | "resume" | "skip-next" } | null;

export type TasksViewProps = {
  loading: boolean;
  filteredTasks: TaskRecord[];
  pendingAction: PendingActionType;
  resetFilters: () => void;
  setCreateDialogOpen: (open: boolean) => void;
  handleRetry: (taskId: number) => Promise<void>;
  handleCancel: (taskId: number) => Promise<void>;
  handleDelete: (taskId: number) => Promise<void>;
  handleTrigger: (taskId: number) => Promise<void>;
  handlePause: (taskId: number, cancelRunning?: boolean) => Promise<void>;
  handleResume: (taskId: number) => Promise<void>;
  handleSkipNext: (taskId: number) => Promise<void>;
  onEdit: (task: TaskRecord) => void;
  onViewHistory: (task: TaskRecord) => void;
  selectedTaskSet: Set<number>;
  allVisibleSelected: boolean;
  toggleTaskSelection: (id: number, checked: boolean) => void;
  toggleSelectAllVisible: (checked: boolean) => void;
};
