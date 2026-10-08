import i18n from "@/i18n";
import { parseSSHKeyType, type NewSSHKeyInput, type SSHKeyPreview, type SSHKeyRecord, type SSHKeyType } from "@/types/domain";
import { preserveSSHPurposeScope } from "@/lib/ssh-purpose-scope";
import { ApiError, formatTime, parseNumericId, request } from "./core";
import { finiteNumber } from "./number-utils";

type SSHKeyResponse = {
  id: number;
  name: string;
  username: string;
  key_type?: "auto" | "rsa" | "ed25519" | "ecdsa";
  public_key?: string;
  fingerprint: string;
  disabled?: boolean;
  expires_at?: string | null;
  allowed_purposes?: string | null;
  allowed_node_ids?: string | null;
  allowed_node_tags?: string | null;
  broad_scope?: boolean;
  created_at: string;
  last_used_at?: string | null;
  public_key_fingerprint?: string | null;
};

function mapPublicKeyFingerprint(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const fingerprint = value.trim();
  return fingerprint.length > 0 ? fingerprint : undefined;
}

function normalizeDateTimeLocal(value: string | null | undefined): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value).slice(0, 16);
  const pad = (n: number) => n.toString().padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function toRfc3339(value: string | undefined): string | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

function mapSSHKey(row: SSHKeyResponse): SSHKeyRecord {
  const publicKeyFingerprint = mapPublicKeyFingerprint(row.public_key_fingerprint);
  return {
    id: `key-${finiteNumber(row.id)}`,
    name: String(row.name ?? ""),
    username: String(row.username ?? ""),
    keyType: parseSSHKeyType(String(row.key_type ?? "auto")),
    publicKey: row.public_key ?? "",
    fingerprint: String(row.fingerprint ?? ""),
    ...(publicKeyFingerprint ? { publicKeyFingerprint } : {}),
    disabled: Boolean(row.disabled),
    expiresAt: normalizeDateTimeLocal(row.expires_at),
    allowedPurposes: preserveSSHPurposeScope(row.allowed_purposes),
    allowedNodeIds: String(row.allowed_node_ids ?? ""),
    allowedNodeTags: String(row.allowed_node_tags ?? ""),
    broadScope: Boolean(row.broad_scope),
    createdAt: formatTime(row.created_at),
    lastUsedAt: formatTime(row.last_used_at)
  };
}

function mapSSHKeyPreview(raw: unknown): SSHKeyPreview {
  if (!raw || typeof raw !== "object") {
    throw new ApiError(500, i18n.t("sshKeys.previewInvalid"));
  }
  if (!("key_type" in raw) || !("public_key" in raw) || !("public_key_fingerprint" in raw)) {
    throw new ApiError(500, i18n.t("sshKeys.previewInvalid"));
  }
  const keyType = raw.key_type;
  const publicKey = raw.public_key;
  const publicKeyFingerprint = raw.public_key_fingerprint;
  if (keyType !== "rsa" && keyType !== "ed25519" && keyType !== "ecdsa") {
    throw new ApiError(500, i18n.t("sshKeys.previewInvalid"));
  }
  if (typeof publicKey !== "string" || publicKey.trim() === "") {
    throw new ApiError(500, i18n.t("sshKeys.previewInvalid"));
  }
  if (typeof publicKeyFingerprint !== "string" || publicKeyFingerprint.trim() === "") {
    throw new ApiError(500, i18n.t("sshKeys.previewInvalid"));
  }
  return {
    keyType,
    publicKey: publicKey.trim(),
    publicKeyFingerprint: publicKeyFingerprint.trim(),
  };
}

function toSSHKeyScopePayload(input: NewSSHKeyInput) {
  return {
    disabled: input.disabled,
    ...(input.expiresAt === undefined ? {} : { expires_at: toRfc3339(input.expiresAt) }),
    allowed_purposes: preserveSSHPurposeScope(input.allowedPurposes),
    allowed_node_ids: input.allowedNodeIds,
    allowed_node_tags: input.allowedNodeTags,
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

export async function fetchSSHKeyExportFile(url: string, token: string, stepUpProof?: string): Promise<Response> {
  const headers: Record<string, string> = { Authorization: `Bearer ${token}` };
  if (stepUpProof) {
    headers["X-Xirang-Step-Up"] = stepUpProof;
  }
  const response = await fetch(url, { headers });
  if (response.ok) {
    return response;
  }

  let detail: unknown;
  try {
    detail = await response.clone().json();
  } catch {
    detail = undefined;
  }
  const message = detail && typeof detail === "object" && "message" in detail
    ? String((detail as { message?: unknown }).message ?? "")
    : `HTTP ${response.status}`;
  throw new ApiError(response.status, message || `HTTP ${response.status}`, detail);
}

export function createSSHKeysApi() {
  return {
    async getSSHKeys(token: string, options?: { signal?: AbortSignal }): Promise<SSHKeyRecord[]> {
      const rows = (await request<SSHKeyResponse[]>("/ssh-keys", { token, signal: options?.signal })) ?? [];
      return rows.map((row) => mapSSHKey(row));
    },

    async getSSHKey(token: string, keyId: string, options?: { signal?: AbortSignal }): Promise<SSHKeyRecord> {
      const numericId = parseNumericId(keyId, "key");
      const row = await request<SSHKeyResponse | null>(`/ssh-keys/${numericId}`, {
        token,
        signal: options?.signal,
      });
      if (!row || typeof row !== "object") {
        throw new ApiError(404, i18n.t("sshKeys.rotationKeyMissing"));
      }
      return mapSSHKey(row);
    },

    async previewSSHKey(
      token: string,
      input: { privateKey: string; keyType?: SSHKeyType },
      options?: { signal?: AbortSignal },
    ): Promise<SSHKeyPreview> {
      const body: { private_key: string; key_type?: SSHKeyType } = {
        private_key: input.privateKey,
      };
      if (input.keyType) {
        body.key_type = input.keyType;
      }
      const raw = await request<unknown>("/ssh-keys/preview", {
        method: "POST",
        token,
        signal: options?.signal,
        body,
      });
      return mapSSHKeyPreview(raw);
    },

    async createSSHKey(token: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
      const privateKey = input.privateKey.trim();
      const row = await request<SSHKeyResponse>("/ssh-keys", {
        method: "POST",
        token,
        body: {
          name: input.name,
          username: input.username,
          key_type: input.keyType,
          private_key: privateKey,
          ...toSSHKeyScopePayload(input),
        }
      });
      return mapSSHKey(row);
    },

    async updateSSHKey(token: string, keyId: string, input: NewSSHKeyInput): Promise<SSHKeyRecord> {
      const numericId = parseNumericId(keyId, "key");
      const privateKey = input.privateKey.trim();
      const row = await request<SSHKeyResponse>(`/ssh-keys/${numericId}`, {
        method: "PUT",
        token,
        body: {
          name: input.name,
          username: input.username,
          key_type: input.keyType,
          ...toSSHKeyScopePayload(input),
          ...(privateKey ? { private_key: privateKey } : {})
        }
      });
      return mapSSHKey(row);
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
      const data = await request<{ deleted: number; skipped_in_use: string[] }>("/ssh-keys/batch-delete", {
        method: "POST",
        token,
        body: { ids: numericIds },
      });
      return { deleted: data.deleted, skippedInUse: data.skipped_in_use ?? [] };
    },

    async testConnection(token: string, keyId: string, nodeIds: string[]): Promise<TestConnectionResult[]> {
      const numericKeyId = parseNumericId(keyId, "key");
      const numericNodeIds = nodeIds.map((id) => parseNumericId(id, "node"));
      const rows = (await request<TestConnectionResultRaw[]>(`/ssh-keys/${numericKeyId}/test-connection`, {
        method: "POST",
        token,
        body: { node_ids: numericNodeIds },
      })) ?? [];
      return rows.map((r) => ({
        nodeId: `node-${finiteNumber(r.node_id)}`,
        name: String(r.name ?? ""),
        host: String(r.host ?? ""),
        port: finiteNumber(r.port, 22),
        success: Boolean(r.success),
        latencyMs: finiteNumber(r.latency_ms),
        error: r.error,
      }));
    },

    async batchCreate(token: string, keys: NewSSHKeyInput[]): Promise<BatchCreateResult[]> {
      const rows = (await request<BatchCreateResultRaw[]>("/ssh-keys/batch", {
        method: "POST",
        token,
        body: {
          keys: keys.map((k) => ({
            name: k.name,
            username: k.username,
            key_type: k.keyType,
            private_key: k.privateKey,
            ...toSSHKeyScopePayload(k),
          })),
        },
      })) ?? [];
      return rows.map((r) => ({
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
