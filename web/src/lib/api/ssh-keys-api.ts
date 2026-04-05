import type { NewSSHKeyInput, SSHKeyRecord } from "@/types/domain";
import { formatTime, parseNumericId, request, type Envelope, unwrapData } from "./core";

type SSHKeyResponse = {
  id: number;
  name: string;
  username: string;
  key_type?: "auto" | "rsa" | "ed25519" | "ecdsa";
  private_key?: string;
  public_key?: string;
  fingerprint: string;
  created_at: string;
  last_used_at?: string | null;
};

function mapSSHKey(row: SSHKeyResponse): SSHKeyRecord {
  return {
    id: `key-${row.id}`,
    name: row.name,
    username: row.username,
    keyType: row.key_type ?? "auto",
    publicKey: row.public_key ?? "",
    fingerprint: row.fingerprint,
    createdAt: formatTime(row.created_at),
    lastUsedAt: formatTime(row.last_used_at)
  };
}

type TestConnectionResultRaw = {
  node_id: number;
  name: string;
  host: string;
  port: number;
  success: boolean;
  latency_ms: number;
  error?: string;
};

export type TestConnectionResult = {
  nodeId: string;
  name: string;
  host: string;
  port: number;
  success: boolean;
  latencyMs: number;
  error?: string;
};

type BatchCreateResultRaw = {
  name: string;
  status: "created" | "skipped" | "error";
  error?: string;
};

export type BatchCreateResult = {
  name: string;
  status: "created" | "skipped" | "error";
  error?: string;
};

export function createSSHKeysApi() {
  return {
    async getSSHKeys(token: string, options?: { signal?: AbortSignal }): Promise<SSHKeyRecord[]> {
      const payload = await request<Envelope<SSHKeyResponse[]>>("/ssh-keys", { token, signal: options?.signal });
      const rows = unwrapData(payload) ?? [];
      return rows.map((row) => mapSSHKey(row));
    },

    async createSSHKey(token: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
      const privateKey = input.privateKey.trim();
      const payload = await request<Envelope<SSHKeyResponse>>("/ssh-keys", {
        method: "POST",
        token,
        body: {
          name: input.name,
          username: input.username,
          key_type: input.keyType,
          private_key: privateKey
        }
      });
      return mapSSHKey(unwrapData(payload));
    },

    async updateSSHKey(token: string, keyId: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
      const numericId = parseNumericId(keyId, "key");
      const privateKey = input.privateKey.trim();
      const payload = await request<Envelope<SSHKeyResponse>>(`/ssh-keys/${numericId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          username: input.username,
          key_type: input.keyType,
          ...(privateKey ? { private_key: privateKey } : {})
        }
      });
      return mapSSHKey(unwrapData(payload));
    },

    async deleteSSHKey(token: string, keyId: string): Promise<void> {
      const numericId = parseNumericId(keyId, "key");
      await request(`/ssh-keys/${numericId}`, {
        method: "DELETE",
        token
      });
    },

    async deleteSSHKeys(token: string, keyIds: string[]): Promise<{ deleted: number; skippedInUse: string[] }> {
      const numericIds = keyIds.map((id) => parseNumericId(id, "key"));
      const payload = await request<Envelope<{ deleted: number; skipped_in_use: string[] }>>("/ssh-keys/batch-delete", {
        method: "POST",
        token,
        body: { ids: numericIds },
      });
      const data = unwrapData(payload);
      return { deleted: data.deleted, skippedInUse: data.skipped_in_use ?? [] };
    },

    async testConnection(token: string, keyId: string, nodeIds: string[]): Promise<TestConnectionResult[]> {
      const numericKeyId = parseNumericId(keyId, "key");
      const numericNodeIds = nodeIds.map((id) => parseNumericId(id, "node"));
      const payload = await request<Envelope<TestConnectionResultRaw[]>>(`/ssh-keys/${numericKeyId}/test-connection`, {
        method: "POST",
        token,
        body: { node_ids: numericNodeIds },
      });
      return (unwrapData(payload) ?? []).map((r) => ({
        nodeId: `node-${r.node_id}`,
        name: r.name,
        host: r.host,
        port: r.port,
        success: r.success,
        latencyMs: r.latency_ms,
        error: r.error,
      }));
    },

    async batchCreate(token: string, keys: NewSSHKeyInput[]): Promise<BatchCreateResult[]> {
      const payload = await request<Envelope<BatchCreateResultRaw[]>>("/ssh-keys/batch", {
        method: "POST",
        token,
        body: {
          keys: keys.map((k) => ({
            name: k.name,
            username: k.username,
            key_type: k.keyType,
            private_key: k.privateKey,
          })),
        },
      });
      return (unwrapData(payload) ?? []).map((r) => ({
        name: r.name,
        status: r.status,
        error: r.error,
      }));
    },

    getExportUrl(format: "authorized_keys" | "json" | "csv", scope: "all" | "in_use", ids?: string[]): string {
      const params = new URLSearchParams({ format, scope });
      if (ids?.length) {
        const numericIds = ids.map((id) => parseNumericId(id, "key"));
        params.set("ids", numericIds.join(","));
      }
      return `/api/v1/ssh-keys/export?${params.toString()}`;
    },
  };
}
