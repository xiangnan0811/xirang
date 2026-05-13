import { useContext } from "react";
import { NodesContext, type NodesContextValue } from "@/context/nodes-context.shared";

export function useNodesContext(): NodesContextValue {
  const ctx = useContext(NodesContext);
  if (!ctx) throw new Error("useNodesContext must be used within NodesContextProvider");
  return ctx;
}

/** Safe variant: returns null when no provider exists for global widgets. */
export function useNodesContextOptional(): NodesContextValue | null {
  return useContext(NodesContext);
}
