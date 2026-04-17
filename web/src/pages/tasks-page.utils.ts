import type { TaskStatus, TaskRecord } from "@/types/domain";

export function canTrigger(task: TaskRecord): boolean {
  return task.enabled !== false && task.status !== "running" && task.status !== "retrying";
}

export function canCancel(status: TaskStatus) {
  return status === "running" || status === "retrying";
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
  onEdit: (task: TaskRecord) => void;
  onViewHistory: (task: TaskRecord) => void;
  selectedTaskSet: Set<number>;
  allVisibleSelected: boolean;
  toggleTaskSelection: (id: number, checked: boolean) => void;
  toggleSelectAllVisible: (checked: boolean) => void;
  /** Set of expanded chain_run_ids (for chain folding in table view) */
  expandedChains?: Set<string>;
  onToggleChain?: (chainRunId: string) => void;
};

/**
 * Groups tasks with the same dependsOnTaskId into simple parent/child chains.
 * Returns a map of parentId -> child task ids for UI chain folding.
 */
export function buildChainParentMap(tasks: TaskRecord[]): Map<number, number[]> {
  const map = new Map<number, number[]>();
  for (const task of tasks) {
    if (task.dependsOnTaskId) {
      const children = map.get(task.dependsOnTaskId) ?? [];
      children.push(task.id);
      map.set(task.dependsOnTaskId, children);
    }
  }
  return map;
}
