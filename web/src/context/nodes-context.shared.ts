import { createContext } from "react";
import type { NewNodeInput, NodeConnectionProbeOutcome, NodeRecord } from "@/types/domain";

export interface NodesContextValue {
  nodes: NodeRecord[];
  nodesLoading: boolean;
  nodesError: string | null;
  nodesLoaded: boolean;
  refreshNodes: (options?: { limit?: number; offset?: number }) => Promise<void>;
  createNode: (input: NewNodeInput, isCurrent?: () => boolean) => Promise<number>;
  updateNode: (nodeId: number, input: NewNodeInput, isCurrent?: () => boolean) => Promise<void>;
  deleteNode: (nodeId: number, isCurrent?: () => boolean) => Promise<void>;
  deleteNodes: (nodeIds: number[], isCurrent?: () => boolean) => Promise<{ deleted: number; notFoundIds: number[] }>;
  testNodeConnection: (nodeId: number) => Promise<NodeConnectionProbeOutcome>;
  triggerNodeBackup: (nodeId: number) => Promise<void>;
};

export const NodesContext = createContext<NodesContextValue | null>(null);
