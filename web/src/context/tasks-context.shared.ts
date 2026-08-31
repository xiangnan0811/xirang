import { createContext } from "react";
import type { LogEvent, NewTaskInput, TaskRecord } from "@/types/domain";

export interface TasksContextValue {
  tasks: TaskRecord[];
  tasksLoading: boolean;
  tasksError: string | null;
  tasksLoaded: boolean;
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
};

export const TasksContext = createContext<TasksContextValue | null>(null);
