import { createContext, useContext, type ReactNode } from "react";
import type {
  AlertBulkRetryResult,
  AlertDeliveryRecord,
  AlertDeliveryRetryResult,
  AlertDeliveryStats,
  AlertRecord,
} from "@/types/domain";

export interface AlertsContextValue {
  alerts: AlertRecord[];
  retryAlert: (alertId: string) => Promise<void>;
  acknowledgeAlert: (alertId: string) => Promise<void>;
  resolveAlert: (alertId: string) => Promise<void>;
  fetchAlertDeliveries: (alertId: string) => Promise<AlertDeliveryRecord[]>;
  fetchAlertDeliveryStats: (hours?: number) => Promise<AlertDeliveryStats>;
  retryAlertDelivery: (
    alertId: string,
    integrationId: string
  ) => Promise<AlertDeliveryRetryResult>;
  retryFailedAlertDeliveries: (alertId: string) => Promise<AlertBulkRetryResult>;
}

const AlertsContext = createContext<AlertsContextValue | null>(null);

export function useAlertsContext(): AlertsContextValue {
  const ctx = useContext(AlertsContext);
  if (!ctx) throw new Error("useAlertsContext must be used within AlertsContextProvider");
  return ctx;
}

export function AlertsContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: AlertsContextValue;
}) {
  return <AlertsContext.Provider value={value}>{children}</AlertsContext.Provider>;
}
