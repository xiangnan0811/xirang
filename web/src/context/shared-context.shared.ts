import { createContext } from "react";
import type {
  OverviewStats,
  OverviewTrafficSeries,
  OverviewTrafficWindow,
} from "@/types/domain";

export interface SharedContextValue {
  loading: boolean;
  warning: string | null;
  lastSyncedAt: string;
  refreshVersion: number;
  globalSearch: string;
  setGlobalSearch: (keyword: string) => void;
  refresh: () => void;
  overview: OverviewStats;
  fetchOverviewTraffic: (
    window: OverviewTrafficWindow,
    options?: { signal?: AbortSignal }
  ) => Promise<OverviewTrafficSeries>;
}

export const SharedContext = createContext<SharedContextValue | null>(null);
