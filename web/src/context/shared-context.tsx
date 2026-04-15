import { createContext, useContext, type ReactNode } from "react";
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

const SharedContext = createContext<SharedContextValue | null>(null);

export function useSharedContext(): SharedContextValue {
  const ctx = useContext(SharedContext);
  if (!ctx) throw new Error("useSharedContext must be used within SharedContextProvider");
  return ctx;
}

export function SharedContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: SharedContextValue;
}) {
  return <SharedContext.Provider value={value}>{children}</SharedContext.Provider>;
}
