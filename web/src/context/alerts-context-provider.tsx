import type { ReactNode } from "react";
import {
  AlertsContext,
  type AlertsContextValue,
} from "@/context/alerts-context.shared";

export function AlertsContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: AlertsContextValue;
}) {
  return <AlertsContext.Provider value={value}>{children}</AlertsContext.Provider>;
}
