import type { SSHKeyType } from "@/types/domain";
import { ApiError, parseNumericId, request } from "./core";
import type { SSHKeyMutationRequestOptions } from "./ssh-keys-api";

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
  options?: SSHKeyMutationRequestOptions,
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
      stepUpProof: options?.stepUpProof,
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
