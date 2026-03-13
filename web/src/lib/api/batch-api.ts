import { request } from "./core";

interface BatchCreateResponse {
  batch_id: string;
  task_ids: number[];
  run_ids: number[];
  retain: boolean;
}

interface BatchStatusResponse {
  batch_id: string;
  tasks: Array<{
    id: number;
    name: string;
    status: string;
    node_id: number;
    node?: { id?: number; name?: string };
    last_error?: string;
  }>;
  total: number;
  status_counts: Record<string, number>;
}

export interface BatchResult {
  batchId: string;
  taskIds: number[];
  runIds: number[];
  retain: boolean;
}

export interface BatchStatus {
  batchId: string;
  total: number;
  statusCounts: Record<string, number>;
  tasks: Array<{
    id: number;
    name: string;
    status: string;
    nodeId: number;
    nodeName: string;
    lastError?: string;
  }>;
}

export function createBatchApi() {
  return {
    async createBatchCommand(
      token: string,
      nodeIds: number[],
      command: string,
      name?: string,
      retain?: boolean
    ): Promise<BatchResult> {
      const payload = await request<BatchCreateResponse>("/batch-commands", {
        method: "POST",
        token,
        body: { node_ids: nodeIds, command, name, retain: retain ?? false },
      });
      return {
        batchId: payload.batch_id,
        taskIds: payload.task_ids ?? [],
        runIds: payload.run_ids ?? [],
        retain: payload.retain ?? false,
      };
    },

    async getBatchStatus(token: string, batchId: string): Promise<BatchStatus> {
      const payload = await request<BatchStatusResponse>(
        `/batch-commands/${batchId}`,
        { token }
      );
      return {
        batchId: payload.batch_id,
        total: payload.total,
        statusCounts: payload.status_counts ?? {},
        tasks: (payload.tasks ?? []).map((t) => ({
          id: t.id,
          name: t.name,
          status: t.status,
          nodeId: t.node?.id ?? t.node_id,
          nodeName: t.node?.name ?? `节点-${t.node_id}`,
          lastError: t.last_error,
        })),
      };
    },

    async deleteBatch(token: string, batchId: string): Promise<{ deleted: number }> {
      return request<{ deleted: number }>(`/batch-commands/${batchId}`, {
        method: "DELETE",
        token,
      });
    },
  };
}
