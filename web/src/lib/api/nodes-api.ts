import type { NewNodeInput, NodeDoctorCheckStatus, NodeDoctorResult, NodeRecord, NodeStatus } from "@/types/domain";
import { parseNumericId, request, formatTime } from "./core";
import { finiteNumber } from "./number-utils";

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
  expiry_date?: string | null;
  archived?: boolean;
  backup_dir?: string;
  use_sudo?: boolean;
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

type NodeDoctorCheckResponse = {
  check?: string;
  status?: string;
  evidence?: string;
  suggestion?: string;
};

type NodeDoctorResponse = {
  node_id?: number | string;
  node_name?: string;
  generated_at?: string;
  checks?: NodeDoctorCheckResponse[];
};

type NodeUpdateResponse = {
  node?: NodeResponse;
  warning?: string;
};

export type NodeUpdateResult = {
  node: NodeRecord;
  warning?: string;
};

export type NodeConnectionTestResult = {
  ok: boolean;
  message: string;
  latencyMs?: number;
  diskUsedGb?: number;
  diskTotalGb?: number;
};

export type EmergencyBackupResult = {
  triggered: number;
  taskIds: number[];
  errors: string[];
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
    expiryDate: row.expiry_date ?? undefined,
    archived: row.archived ?? false,
    backupDir: row.backup_dir || '',
    useSudo: row.use_sudo ?? false,
  };
}

function mapDoctorStatus(raw?: string): NodeDoctorCheckStatus {
  switch (raw) {
    case "pass":
    case "warn":
    case "fail":
    case "skip":
      return raw;
    default:
      return "warn";
  }
}

function mapNodeDoctorResult(row: NodeDoctorResponse): NodeDoctorResult {
  return {
    nodeId: finiteNumber(row.node_id),
    nodeName: String(row.node_name ?? ""),
    generatedAt: String(row.generated_at ?? ""),
    checks: Array.isArray(row.checks)
      ? row.checks.map((check) => ({
        check: String(check.check ?? "unknown"),
        status: mapDoctorStatus(check.status),
        evidence: String(check.evidence ?? ""),
        suggestion: String(check.suggestion ?? ""),
      }))
      : [],
  };
}

export const __test__ = { mapNodeDoctorResult, mapNode };

export function createNodesApi() {
  return {
    async getNodes(token: string, options?: { signal?: AbortSignal }): Promise<NodeRecord[]> {
      const rows = (await request<NodeResponse[]>("/nodes", { token, signal: options?.signal })) ?? [];
      return rows.map((row) => mapNode(row));
    },

    async createNode(token: string, input: NewNodeInput): Promise<NodeRecord> {
      const row = await request<NodeResponse>("/nodes", {
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
          base_path: input.basePath,
          backup_dir: input.backupDir,
          use_sudo: input.useSudo,
          maintenance_start: input.maintenanceStart ?? undefined,
          maintenance_end: input.maintenanceEnd ?? undefined,
          expiry_date: input.expiryDate ?? undefined,
        }
      });
      return mapNode(row);
    },

    async updateNode(token: string, nodeId: number, input: NewNodeInput): Promise<NodeUpdateResult> {
      const payload = await request<NodeUpdateResponse>(`/nodes/${nodeId}`, {
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
          base_path: input.basePath,
          backup_dir: input.backupDir,
          use_sudo: input.useSudo,
          maintenance_start: input.maintenanceStart ?? undefined,
          maintenance_end: input.maintenanceEnd ?? undefined,
          expiry_date: input.expiryDate ?? undefined,
        }
      });
      if (!payload?.node || typeof payload.node.id !== "number" || !payload.node.name || !payload.node.host) {
        throw new Error("invalid node update response");
      }
      const warning = typeof payload.warning === "string" ? payload.warning.trim() : "";
      return {
        node: mapNode(payload.node),
        warning: warning || undefined,
      };
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

    async testNodeConnection(token: string, nodeId: number): Promise<NodeConnectionTestResult> {
      const row = await request<TestNodeResponse>(`/nodes/${nodeId}/test-connection`, {
        method: "POST",
        token
      });
      return {
        ok: Boolean(row?.ok),
        message: String(row?.message ?? ""),
        latencyMs: row?.latency_ms,
        diskUsedGb: row?.disk_used_gb,
        diskTotalGb: row?.disk_total_gb,
      };
    },

    async runNodeDoctor(token: string, nodeId: number): Promise<NodeDoctorResult> {
      const row = await request<NodeDoctorResponse>(`/nodes/${nodeId}/doctor`, {
        method: "POST",
        token
      });
      return mapNodeDoctorResult(row);
    },

    async emergencyBackup(token: string, nodeId: number): Promise<EmergencyBackupResult> {
      const row = await request<{ triggered?: unknown; task_ids?: unknown; errors?: unknown }>(
        `/nodes/${nodeId}/emergency-backup`,
        { token, method: "POST" }
      );
      return {
        triggered: finiteNumber(row?.triggered),
        taskIds: Array.isArray(row?.task_ids)
          ? row.task_ids.map((id) => finiteNumber(id)).filter((id) => id > 0)
          : [],
        errors: Array.isArray(row?.errors) ? row.errors.map((item) => String(item)) : [],
      };
    },

    async migrateNode(
      token: string,
      sourceNodeId: number,
      targetNodeId: number,
      options?: { archiveSource?: boolean; pausePolicies?: boolean; migrateData?: boolean },
    ): Promise<MigrateNodeResult> {
      return request<MigrateNodeResult>(
        `/nodes/${sourceNodeId}/migrate`,
        {
          token, method: "POST",
          body: {
            targetNodeId,
            archiveSource: options?.archiveSource ?? false,
            pausePolicies: options?.pausePolicies ?? false,
            migrateData: options?.migrateData ?? false,
          },
        }
      );
    },

    async migrateNodePreflight(
      token: string,
      sourceNodeId: number,
      targetNodeId: number,
    ): Promise<MigratePreflightResult> {
      return request<MigratePreflightResult>(
        `/nodes/${sourceNodeId}/migrate/preflight`,
        { token, method: "POST", body: { targetNodeId } }
      );
    },
  };
}

// --- 迁移预检类型 ---

export type PreflightCheckStatus = "pass" | "fail" | "warn" | "skip";

export interface PreflightCheckItem {
  name: string;
  status: PreflightCheckStatus;
  message: string;
}

export interface PreflightNodeInfo {
  id: number;
  name: string;
  host: string;
  status: string;
  diskUsedGb: number;
  diskTotalGb: number;
}

export interface PreflightPolicy {
  id: number;
  name: string;
  sourcePath: string;
  executorType: string;
}

export interface MigratePreflightResult {
  sourceNode: PreflightNodeInfo;
  targetNode: PreflightNodeInfo;
  policies: PreflightPolicy[];
  taskCount: number;
  checks: PreflightCheckItem[];
  canProceed: boolean;
  dataMigratable: boolean;
  dataSizeMb: number;
}

export interface DataMigrateItem {
  policyId: number;
  policyName: string;
  status: "copied" | "skipped" | "error";
  message: string;
}

export interface MigrateNodeResult {
  migratedPolicies: number;
  migratedTasks: number;
  archivedSource: boolean;
  dataMigration: DataMigrateItem[] | null;
}
