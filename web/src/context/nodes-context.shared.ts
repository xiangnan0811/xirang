import { createContext } from "react";
import type { NewNodeInput, NodeConnectionProbeOutcome, NodeRecord, NodeSaveResult } from "@/types/domain";

export interface NodesContextValue {
  nodes: NodeRecord[];
  nodesLoading: boolean;
  nodesError: string | null;
  nodesLoaded: boolean;
  refreshNodes: (options?: { limit?: number; offset?: number }) => Promise<void>;
  /** Null when the write did not commit for this caller. */
  createNode: (input: NewNodeInput, isCurrent?: () => boolean, onInlineKeyCreated?: (keyId: string) => void) => Promise<NodeSaveResult | null>;
  /** Null when ownership ended; rejects with `node_revision_conflict` when `input.expectedRevision` is outdated. */
  updateNode: (nodeId: number, input: NewNodeInput, isCurrent?: () => boolean, onInlineKeyCreated?: (keyId: string) => void) => Promise<NodeSaveResult | null>;
  deleteNode: (nodeId: number, isCurrent?: () => boolean) => Promise<void>;
  deleteNodes: (nodeIds: number[], isCurrent?: () => boolean) => Promise<{ deleted: number; notFoundIds: number[] }>;
  /** `expectedRevision` is for the automatic post-save test only: it tests the committed revision or reports stale. */
  testNodeConnection: (nodeId: number, options?: { expectedRevision?: string }) => Promise<NodeConnectionProbeOutcome>;
  triggerNodeBackup: (nodeId: number) => Promise<void>;
};

export const NodesContext = createContext<NodesContextValue | null>(null);
