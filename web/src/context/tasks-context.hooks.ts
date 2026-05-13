import { useContext } from "react";
import { TasksContext, type TasksContextValue } from "@/context/tasks-context.shared";

export function useTasksContext(): TasksContextValue {
  const ctx = useContext(TasksContext);
  if (!ctx) throw new Error("useTasksContext must be used within TasksContextProvider");
  return ctx;
}

/** Safe variant: returns null when no provider exists for global widgets. */
export function useTasksContextOptional(): TasksContextValue | null {
  return useContext(TasksContext);
}
