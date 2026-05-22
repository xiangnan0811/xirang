import type { CredentialAccessGrant, CredentialAccessGrantAction, CredentialAccessGrantPurpose, CredentialAccessGrantStatus } from "@/types/domain";
import { request } from "./core";

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
  return raw === "terminal.open" || raw === "config.import" || raw === "config.export" || raw === "snapshot.restore" || raw === "task.restore_trigger" ? raw : "unknown";
}

function mapGrantPurpose(value: unknown): CredentialAccessGrantPurpose {
  const raw = stringValue(value);
  return raw === "terminal" || raw === "config_import" || raw === "config_export" || raw === "snapshot" || raw === "task_restore" ? raw : "unknown";
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
    requestedAt: stringValue(row.requested_at),
    approvedAt: stringValue(row.approved_at) || undefined,
    approverUserId: positiveNumberOrUndefined(row.approver_user_id),
    approverUsername: stringValue(row.approver_username) || undefined,
    expiresAt: stringValue(row.expires_at),
    revokedAt: stringValue(row.revoked_at) || undefined,
    revokedByUserId: positiveNumberOrUndefined(row.revoked_by_user_id),
    createdAt: stringValue(row.created_at),
    updatedAt: stringValue(row.updated_at),
  };
}

export function createCredentialAccessGrantsApi() {
  return {
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
  };
}
