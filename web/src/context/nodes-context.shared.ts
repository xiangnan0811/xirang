import { createContext } from "react";
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

export const NodesContext = createContext<NodesContextValue | null>(null);
