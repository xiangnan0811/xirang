import { createContext, useContext, type ReactNode } from "react";
import type { LogEvent, NewTaskInput, TaskRecord } from "@/types/domain";

export interface TasksContextValue {
  tasks: TaskRecord[];
  refreshTasks: (options?: { limit?: number; offset?: number }) => Promise<void>;
  createTask: (input: NewTaskInput) => Promise<number>;
  updateTask: (taskId: number, input: NewTaskInput) => Promise<void>;
  deleteTask: (taskId: number) => Promise<void>;
  triggerTask: (taskId: number) => Promise<void>;
  cancelTask: (taskId: number) => Promise<void>;
  retryTask: (taskId: number) => Promise<void>;
  pauseTask: (taskId: number, cancelRunning?: boolean) => Promise<void>;
  resumeTask: (taskId: number) => Promise<void>;
  skipNextTask: (taskId: number) => Promise<void>;
  refreshTask: (taskId: number) => Promise<void>;
  fetchTaskLogs: (
    taskId: number,
    options?: { beforeId?: number; limit?: number }
  ) => Promise<LogEvent[]>;
}

const TasksContext = createContext<TasksContextValue | null>(null);

export function useTasksContext(): TasksContextValue {
  const ctx = useContext(TasksContext);
  if (!ctx) throw new Error("useTasksContext must be used within TasksContextProvider");
  return ctx;
}

/** Safe variant — returns null when no provider (for global widgets like CommandPalette). */
export function useTasksContextOptional(): TasksContextValue | null {
  return useContext(TasksContext);
}

export function TasksContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: TasksContextValue;
}) {
  return <TasksContext.Provider value={value}>{children}</TasksContext.Provider>;
}
