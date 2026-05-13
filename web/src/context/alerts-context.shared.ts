import { createContext } from "react";
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

export const AlertsContext = createContext<AlertsContextValue | null>(null);
