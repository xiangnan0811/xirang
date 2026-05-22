import type { CredentialAccessGrant, CredentialAccessGrantAction, CredentialAccessGrantPurpose, CredentialAccessGrantStatus } from "@/types/domain";
import { formatTime } from "@/lib/date-utils";
import { request, type PaginatedEnvelope, unwrapPaginated } from "./core";

export type CredentialAccessGrantListOptions = {
  requesterUserId?: number;
  requesterUsername?: string;
  requesterRole?: string;
  action?: string;
  purpose?: string;
  status?: CredentialAccessGrantStatus | string;
  nodeId?: number;
  taskId?: number;
  policyId?: number;
  from?: string;
  to?: string;
  page?: number;
  pageSize?: number;
  sortBy?: "id" | "created_at" | "updated_at" | "requested_at" | "expires_at" | "status" | "action" | "purpose" | "requester_username" | "requester_role";
  sortOrder?: "asc" | "desc";
};

interface CredentialAccessGrantResponse {
  id?: unknown;
  requester_user_id?: unknown;
  requester_username?: unknown;
  requester_role?: unknown;
  action?: unknown;
  purpose?: unknown;
  node_id?: unknown;
  task_id?: unknown;
  policy_id?: unknown;
  reason?: unknown;
  status?: unknown;
  requested_ttl_seconds?: unknown;
  requested_at?: unknown;
  approved_at?: unknown;
  approver_user_id?: unknown;
  approver_username?: unknown;
  expires_at?: unknown;
  revoked_at?: unknown;
  revoked_by_user_id?: unknown;
  created_at?: unknown;
  updated_at?: unknown;
}

function finiteNumber(value: unknown, fallback = 0): number {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function positiveNumberOrUndefined(value: unknown): number | undefined {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : value == null ? "" : String(value);
}

function mapGrantAction(value: unknown): CredentialAccessGrantAction {
  const raw = stringValue(value);
  return raw === "terminal.open" || raw === "config.import" || raw === "config.export" || raw === "snapshot.restore" || raw === "task.restore_trigger" || raw === "task.manual_trigger" || raw === "task.batch_trigger" || raw === "batch_command.create" ? raw : "unknown";
}

function mapGrantPurpose(value: unknown): CredentialAccessGrantPurpose {
  const raw = stringValue(value);
  return raw === "terminal" || raw === "config_import" || raw === "config_export" || raw === "snapshot" || raw === "task_restore" || raw === "task_command" || raw === "batch_command" ? raw : "unknown";
}

function mapGrantStatus(value: unknown): CredentialAccessGrantStatus {
  const raw = stringValue(value);
  switch (raw) {
    case "requested":
    case "approved":
    case "active":
    case "denied":
    case "expired":
    case "revoked":
      return raw;
    default:
      return "expired";
  }
}

function displayTime(value: unknown): string {
  const raw = stringValue(value);
  return raw ? formatTime(raw) : "";
}

export function buildCredentialAccessGrantQuery(options?: CredentialAccessGrantListOptions): URLSearchParams {
  const query = new URLSearchParams();
  const stringFields: Array<[keyof CredentialAccessGrantListOptions, string]> = [
    ["requesterUsername", "requester_username"],
    ["requesterRole", "requester_role"],
    ["action", "action"],
    ["purpose", "purpose"],
    ["status", "status"],
    ["from", "from"],
    ["to", "to"],
    ["sortBy", "sort_by"],
    ["sortOrder", "sort_order"],
  ];
  for (const [field, param] of stringFields) {
    const value = options?.[field];
    if (typeof value === "string" && value.trim()) {
      query.set(param, value.trim());
    }
  }

  const numericFields: Array<[keyof CredentialAccessGrantListOptions, string]> = [
    ["requesterUserId", "requester_user_id"],
    ["nodeId", "node_id"],
    ["taskId", "task_id"],
    ["policyId", "policy_id"],
    ["page", "page"],
    ["pageSize", "page_size"],
  ];
  for (const [field, param] of numericFields) {
    const value = options?.[field];
    if (typeof value === "number" && Number.isFinite(value) && value > 0) {
      query.set(param, String(value));
    }
  }
  return query;
}

export function mapCredentialAccessGrant(raw: CredentialAccessGrantResponse | null | undefined): CredentialAccessGrant {
  const row = raw ?? {};
  return {
    id: finiteNumber(row.id),
    requesterUserId: finiteNumber(row.requester_user_id),
    requesterUsername: stringValue(row.requester_username),
    requesterRole: stringValue(row.requester_role),
    action: mapGrantAction(row.action),
    purpose: mapGrantPurpose(row.purpose),
    nodeId: positiveNumberOrUndefined(row.node_id),
    taskId: positiveNumberOrUndefined(row.task_id),
    policyId: positiveNumberOrUndefined(row.policy_id),
    reason: stringValue(row.reason),
    status: mapGrantStatus(row.status),
    requestedTtlSeconds: finiteNumber(row.requested_ttl_seconds),
    requestedAt: displayTime(row.requested_at),
    approvedAt: displayTime(row.approved_at) || undefined,
    approverUserId: positiveNumberOrUndefined(row.approver_user_id),
    approverUsername: stringValue(row.approver_username) || undefined,
    expiresAt: displayTime(row.expires_at),
    revokedAt: displayTime(row.revoked_at) || undefined,
    revokedByUserId: positiveNumberOrUndefined(row.revoked_by_user_id),
    createdAt: displayTime(row.created_at),
    updatedAt: displayTime(row.updated_at),
  };
}

export function createCredentialAccessGrantsApi() {
  return {
    async listCredentialAccessGrants(
      token: string,
      options?: CredentialAccessGrantListOptions,
    ): Promise<{ items: CredentialAccessGrant[]; total: number; page: number; pageSize: number }> {
      const query = buildCredentialAccessGrantQuery(options);
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<PaginatedEnvelope<CredentialAccessGrantResponse[]>>(
        `/credential-access-grants${suffix}`,
        { token },
      );
      const result = unwrapPaginated(payload);
      return {
        items: result.items.map((row) => mapCredentialAccessGrant(row)),
        total: result.total,
        page: result.page,
        pageSize: result.pageSize,
      };
    },

    async requestTerminalCredentialGrant(
      token: string,
      input: { nodeId: number; reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant> {
      const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/terminal", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          node_id: input.nodeId,
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return mapCredentialAccessGrant(raw);
    },

    async requestConfigImportCredentialGrant(
      token: string,
      input: { reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant> {
      const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/config-import", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return mapCredentialAccessGrant(raw);
    },

    async requestConfigExportCredentialGrant(
      token: string,
      input: { reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant> {
      const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/config-export", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return mapCredentialAccessGrant(raw);
    },

    async requestSnapshotRestoreCredentialGrant(
      token: string,
      input: { taskId: number; reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant> {
      const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/snapshot-restore", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          task_id: input.taskId,
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return mapCredentialAccessGrant(raw);
    },

    async requestTaskRestoreCredentialGrant(
      token: string,
      input: { taskId: number; reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant> {
      const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/task-restore", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          task_id: input.taskId,
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return mapCredentialAccessGrant(raw);
    },

    async requestTaskManualTriggerCredentialGrant(
      token: string,
      input: { taskId: number; reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant> {
      const raw = await request<CredentialAccessGrantResponse>("/credential-access-grants/task-manual-trigger", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          task_id: input.taskId,
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return mapCredentialAccessGrant(raw);
    },

    async requestTaskBatchTriggerCredentialGrant(
      token: string,
      input: { taskIds: number[]; reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant[]> {
      const raw = await request<CredentialAccessGrantResponse[]>("/credential-access-grants/task-batch-trigger", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          task_ids: input.taskIds,
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return (raw ?? []).map((row) => mapCredentialAccessGrant(row));
    },

    async requestBatchCommandCredentialGrant(
      token: string,
      input: { nodeIds: number[]; reason: string; requestedTtlSeconds?: number },
      stepUpProof?: string,
    ): Promise<CredentialAccessGrant[]> {
      const raw = await request<CredentialAccessGrantResponse[]>("/credential-access-grants/batch-command", {
        method: "POST",
        token,
        stepUpProof,
        body: {
          node_ids: input.nodeIds,
          reason: input.reason,
          requested_ttl_seconds: input.requestedTtlSeconds,
        },
      });
      return (raw ?? []).map((row) => mapCredentialAccessGrant(row));
    },
  };
}
