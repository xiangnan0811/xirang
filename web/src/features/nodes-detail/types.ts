import type { NodeStatus } from "@/lib/api/node-metrics-api";

export type NodeDetailAuthToken = string | null;

export type NodeDetailTabProps = {
  nodeId: number;
  token: NodeDetailAuthToken;
};

/** Overview tab reuses the page-level status poll (no second poll). */
export type OverviewTabProps = NodeDetailTabProps & {
  status: NodeStatus | null;
  statusError: unknown;
};
