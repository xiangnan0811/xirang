import type { NewNodeInput, NodeRecord, NodeStatus } from "@/types/domain";
import { parseNumericId, request, type Envelope, formatTime, unwrapData } from "./core";

type NodeResponse = {
  id: number;
  name: string;
  host: string;
  port?: number;
  username?: string;
  auth_type?: "key" | "password";
  ssh_key_id?: number | null;
  tags?: string;
  status?: string;
  base_path?: string;
  last_seen_at?: string | null;
  last_backup_at?: string | null;
  connection_latency_ms?: number;
  disk_used_gb?: number;
  disk_total_gb?: number;
  last_probe_at?: string | null;
  maintenance_start?: string | null;
  maintenance_end?: string | null;
};

type NodeBatchDeleteResponse = {
  deleted?: number;
  not_found_ids?: number[];
};

type TestNodeResponse = {
  ok: boolean;
  message: string;
  latency_ms?: number;
  disk_used_gb?: number;
  disk_total_gb?: number;
};

function mapNodeStatus(raw?: string): NodeStatus {
  switch (raw) {
    case "online":
      return "online";
    case "warning":
      return "warning";
    default:
      return "offline";
  }
}

function mapNode(row: NodeResponse): NodeRecord {
  const diskTotalGb = row.disk_total_gb && row.disk_total_gb > 0 ? row.disk_total_gb : 0;
  const diskUsedGb = row.disk_used_gb && row.disk_used_gb >= 0 ? row.disk_used_gb : 0;
  const freePercent = diskTotalGb > 0
    ? Math.max(0, Math.round(((diskTotalGb - diskUsedGb) / diskTotalGb) * 100))
    : 0;

  return {
    id: row.id,
    name: row.name,
    host: row.host,
    address: row.host,
    ip: row.host,
    port: row.port ?? 22,
    username: row.username ?? "root",
    authType: row.auth_type ?? "key",
    keyId: row.ssh_key_id ? `key-${row.ssh_key_id}` : null,
    basePath: row.base_path ?? "/",
    status: mapNodeStatus(row.status),
    tags: row.tags ? row.tags.split(",").map((one) => one.trim()).filter(Boolean) : [],
    lastSeenAt: formatTime(row.last_seen_at),
    lastBackupAt: formatTime(row.last_backup_at),
    diskFreePercent: freePercent,
    diskUsedGb,
    diskTotalGb,
    diskProbeAt: formatTime(row.last_probe_at ?? row.last_seen_at),
    connectionLatencyMs: row.connection_latency_ms,
    lastProbeAt: formatTime(row.last_probe_at),
    maintenanceStart: row.maintenance_start ?? undefined,
    maintenanceEnd: row.maintenance_end ?? undefined,
  };
}

export function createNodesApi() {
  return {
    async getNodes(token: string, options?: { signal?: AbortSignal }): Promise<NodeRecord[]> {
      const payload = await request<Envelope<NodeResponse[]>>("/nodes", { token, signal: options?.signal });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapNode(row));
    },

    async createNode(token: string, input: NewNodeInput): Promise<NodeRecord> {
      const payload = await request<Envelope<NodeResponse>>("/nodes", {
        method: "POST",
        token,
        body: {
          name: input.name,
          host: input.host,
          port: input.port,
          username: input.username,
          auth_type: input.authType,
          password: input.password,
          ssh_key_id: input.keyId ? parseNumericId(input.keyId, "key") : null,
          private_key: input.inlinePrivateKey,
          key_type: input.inlineKeyType,
          tags: input.tags,
          base_path: input.basePath
        }
      });
      const row = unwrapData(payload);
      return mapNode(row);
    },

    async updateNode(token: string, nodeId: number, input: NewNodeInput): Promise<NodeRecord> {
      const payload = await request<Envelope<NodeResponse>>(`/nodes/${nodeId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          host: input.host,
          port: input.port,
          username: input.username,
          auth_type: input.authType,
          password: input.password,
          ssh_key_id: input.keyId ? parseNumericId(input.keyId, "key") : null,
          private_key: input.inlinePrivateKey,
          key_type: input.inlineKeyType,
          tags: input.tags,
          base_path: input.basePath
        }
      });
      const row = unwrapData(payload);
      return mapNode(row);
    },

    async deleteNode(token: string, nodeId: number): Promise<void> {
      await request(`/nodes/${nodeId}`, {
        method: "DELETE",
        token
      });
    },

    async deleteNodes(token: string, nodeIds: number[]): Promise<{ deleted: number; notFoundIds: number[] }> {
      const payload = await request<NodeBatchDeleteResponse>("/nodes/batch-delete", {
        method: "POST",
        token,
        body: {
          ids: nodeIds
        }
      });

      return {
        deleted: Number(payload.deleted ?? 0),
        notFoundIds: Array.isArray(payload.not_found_ids) ? payload.not_found_ids : []
      };
    },

    async testNodeConnection(token: string, nodeId: number): Promise<TestNodeResponse> {
      return request<TestNodeResponse>(`/nodes/${nodeId}/test-connection`, {
        method: "POST",
        token
      });
    }
  };
}
