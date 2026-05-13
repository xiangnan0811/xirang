import type { ReactNode } from "react";
import {
  TasksContext,
  type TasksContextValue,
} from "@/context/tasks-context.shared";

export function TasksContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: TasksContextValue;
}) {
  return <TasksContext.Provider value={value}>{children}</TasksContext.Provider>;
}
