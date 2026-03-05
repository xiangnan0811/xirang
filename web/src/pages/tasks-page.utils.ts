import type { TaskStatus, TaskRecord } from "@/types/domain";

export function canTrigger(status: TaskStatus) {
  return status !== "running" && status !== "retrying";
}

export function canCancel(status: TaskStatus) {
  return status === "running" || status === "retrying";
}

export function normalizeStatusFilter(value: string): "all" | TaskStatus {
  if (
    value === "all" ||
    value === "pending" ||
    value === "running" ||
    value === "retrying" ||
    value === "failed" ||
    value === "success" ||
    value === "canceled"
  ) {
    return value;
  }
  return "all";
}

export type PendingActionType = { id: number; action: "retry" | "cancel" | "delete" | "trigger" } | null;

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
};
