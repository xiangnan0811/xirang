import { useContext } from "react";
import { SharedContext, type SharedContextValue } from "@/context/shared-context.shared";

export function useSharedContext(): SharedContextValue {
  const ctx = useContext(SharedContext);
  if (!ctx) throw new Error("useSharedContext must be used within SharedContextProvider");
  return ctx;
}
