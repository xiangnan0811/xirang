import type { NodeSummary } from "@/lib/api/nodes-api";

export type NodeDetailAuthToken = string | null;

export type NodeDetailTabProps = {
  nodeId: number;
  token: NodeDetailAuthToken;
};

/** Overview tab reuses the page-level summary request. */
export type OverviewTabProps = NodeDetailTabProps & {
  summary: NodeSummary | null;
  summaryError: unknown;
  summaryLoading: boolean;
};
