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

export type SSHKeyRotationStatus = "saved" | "not_saved";

export type SSHKeyRotationReason =
  | ""
  | "validation_failed"
  | "validation_timeout"
  | "conflict"
  | "scope_blocked"
  | "trust_unavailable"
  | "inventory_limit"
  | "busy";

export type SSHKeyRotationNodeStatus = "verified" | "failed" | "unknown";

export type SSHKeyRotationErrorCode =
  | "scope_denied"
  | "ssh_host_key_unknown"
  | "ssh_host_key_mismatch"
  | "connection_failed"
  | "timeout"
  | "not_checked";

export interface SSHKeyRotationNodeResult {
  nodeId: string;
  name: string;
  status: SSHKeyRotationNodeStatus;
  errorCode?: SSHKeyRotationErrorCode;
}

export interface SSHKeyRotationResult {
  status: SSHKeyRotationStatus;
  reason: SSHKeyRotationReason;
  publicKeyFingerprint: string;
  results: SSHKeyRotationNodeResult[];
}

export class SSHKeyRotationDecodeError extends Error {
  constructor() {
    super("ssh key rotation response is invalid");
    this.name = "SSHKeyRotationDecodeError";
  }
}

const SSH_KEY_ROTATION_FIELDS = new Set(["status", "reason", "public_key_fingerprint", "results"]);
const SHA256_FINGERPRINT_PREFIX = "SHA256:";
const BASE64_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

// OpenSSH SHA256 fingerprints are "SHA256:" plus 43 unpadded base64 characters of a 32-byte digest.
// The final character carries two unused bits, and those bits are zero in the canonical form.
function isCanonicalSha256Fingerprint(value: string): boolean {
  if (!value.startsWith(SHA256_FINGERPRINT_PREFIX)) return false;
  const body = value.slice(SHA256_FINGERPRINT_PREFIX.length);
  if (body.length !== 43) return false;
  let accumulator = 0;
  let bits = 0;
  let written = 0;
  for (let index = 0; index < body.length; index += 1) {
    const alphabetIndex = BASE64_ALPHABET.indexOf(body.charAt(index));
    if (alphabetIndex < 0) return false;
    accumulator = (accumulator << 6) | alphabetIndex;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      written += 1;
      accumulator &= (1 << bits) - 1;
    }
  }
  return written === 32 && bits === 2 && accumulator === 0;
}
const SSH_KEY_ROTATION_NODE_FIELDS = new Set(["node_id", "name", "status", "error_code"]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isRotationStatus(value: string): value is SSHKeyRotationStatus {
  return value === "saved" || value === "not_saved";
}

function isRotationReason(value: string): value is SSHKeyRotationReason {
  return value === ""
    || value === "validation_failed"
    || value === "validation_timeout"
    || value === "conflict"
    || value === "scope_blocked"
    || value === "trust_unavailable"
    || value === "inventory_limit"
    || value === "busy";
}

function isNodeStatus(value: string): value is SSHKeyRotationNodeStatus {
  return value === "verified" || value === "failed" || value === "unknown";
}

function isErrorCode(value: string): value is SSHKeyRotationErrorCode {
  return value === "scope_denied"
    || value === "ssh_host_key_unknown"
    || value === "ssh_host_key_mismatch"
    || value === "connection_failed"
    || value === "timeout"
    || value === "not_checked";
}

function assertExactFields(value: Record<string, unknown>, allowed: Set<string>): void {
  for (const field of Object.keys(value)) {
    if (!allowed.has(field)) throw new SSHKeyRotationDecodeError();
  }
}

function decodeSSHKeyRotationNode(
  raw: unknown,
  seen: Set<number>,
): SSHKeyRotationNodeResult {
  if (!isRecord(raw)) throw new SSHKeyRotationDecodeError();
  assertExactFields(raw, SSH_KEY_ROTATION_NODE_FIELDS);
  const nodeId = raw.node_id;
  const name = raw.name;
  const status = raw.status;
  if (typeof nodeId !== "number" || !Number.isSafeInteger(nodeId) || nodeId <= 0) {
    throw new SSHKeyRotationDecodeError();
  }
  if (seen.has(nodeId)) throw new SSHKeyRotationDecodeError();
  seen.add(nodeId);
  if (typeof name !== "string") throw new SSHKeyRotationDecodeError();
  if (typeof status !== "string" || !isNodeStatus(status)) {
    throw new SSHKeyRotationDecodeError();
  }
  const hasErrorCode = Object.hasOwn(raw, "error_code");
  if (status === "verified") {
    if (hasErrorCode) throw new SSHKeyRotationDecodeError();
    return { nodeId: `node-${nodeId}`, name, status };
  }
  const errorCode = raw.error_code;
  if (typeof errorCode !== "string" || !isErrorCode(errorCode)) {
    throw new SSHKeyRotationDecodeError();
  }
  return {
    nodeId: `node-${nodeId}`,
    name,
    status,
    errorCode,
  };
}

export function decodeSSHKeyRotationResult(raw: unknown): SSHKeyRotationResult {
  if (!isRecord(raw)) throw new SSHKeyRotationDecodeError();
  assertExactFields(raw, SSH_KEY_ROTATION_FIELDS);
  const status = raw.status;
  const reason = raw.reason;
  const fingerprint = raw.public_key_fingerprint;
  const results = raw.results;
  if (typeof status !== "string" || !isRotationStatus(status)) throw new SSHKeyRotationDecodeError();
  if (typeof reason !== "string" || !isRotationReason(reason)) {
    throw new SSHKeyRotationDecodeError();
  }
  if (status === "saved" && reason !== "") throw new SSHKeyRotationDecodeError();
  if (status === "not_saved" && reason === "") throw new SSHKeyRotationDecodeError();
  if (typeof fingerprint !== "string") throw new SSHKeyRotationDecodeError();
  const publicKeyFingerprint = fingerprint.trim();
  if (!isCanonicalSha256Fingerprint(publicKeyFingerprint)) {
    throw new SSHKeyRotationDecodeError();
  }
  if (!Array.isArray(results)) throw new SSHKeyRotationDecodeError();
  const seen = new Set<number>();
  const mapped = results.map((result) => decodeSSHKeyRotationNode(result, seen));
  if (status === "saved" && mapped.some((result) => result.status !== "verified")) {
    throw new SSHKeyRotationDecodeError();
  }
  return {
    status,
    reason,
    publicKeyFingerprint,
    results: mapped,
  };
}

export async function rotateSSHKey(
  token: string,
  keyId: string,
  input: { privateKey: string; keyType?: SSHKeyType; name?: string },
  options?: { signal?: AbortSignal },
): Promise<SSHKeyRotationResult> {
  const numericId = parseNumericId(keyId, "key");
  const body: { private_key: string; key_type?: SSHKeyType; name?: string } = {
    private_key: input.privateKey,
  };
  if (input.keyType) body.key_type = input.keyType;
  const name = input.name?.trim();
  if (name) body.name = name;
  let raw: unknown;
  try {
    raw = await request<unknown>(`/ssh-keys/${numericId}/rotate`, {
      method: "POST",
      token,
      signal: options?.signal,
      body,
    });
  } catch (error) {
    const httpStatus = error instanceof ApiError ? error.httpStatus : undefined;
    if (typeof httpStatus === "number" && httpStatus >= 200 && httpStatus < 300) {
      throw new SSHKeyRotationDecodeError();
    }
    throw error;
  }
  return decodeSSHKeyRotationResult(raw);
}
