import { useContext } from "react";
import { AlertsContext, type AlertsContextValue } from "@/context/alerts-context.shared";

export function useAlertsContext(): AlertsContextValue {
  const ctx = useContext(AlertsContext);
  if (!ctx) throw new Error("useAlertsContext must be used within AlertsContextProvider");
  return ctx;
}
