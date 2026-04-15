import { createContext, useContext, type ReactNode } from "react";
import type { NewNodeInput, NodeRecord } from "@/types/domain";

export interface NodesContextValue {
  nodes: NodeRecord[];
  refreshNodes: (options?: { limit?: number; offset?: number }) => Promise<void>;
  createNode: (input: NewNodeInput) => Promise<number>;
  updateNode: (nodeId: number, input: NewNodeInput) => Promise<void>;
  deleteNode: (nodeId: number) => Promise<void>;
  deleteNodes: (nodeIds: number[]) => Promise<{ deleted: number; notFoundIds: number[] }>;
  testNodeConnection: (nodeId: number) => Promise<{ ok: boolean; message: string }>;
  triggerNodeBackup: (nodeId: number) => Promise<void>;
}

const NodesContext = createContext<NodesContextValue | null>(null);

export function useNodesContext(): NodesContextValue {
  const ctx = useContext(NodesContext);
  if (!ctx) throw new Error("useNodesContext must be used within NodesContextProvider");
  return ctx;
}

export function NodesContextProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: NodesContextValue;
}) {
  return <NodesContext.Provider value={value}>{children}</NodesContext.Provider>;
}
