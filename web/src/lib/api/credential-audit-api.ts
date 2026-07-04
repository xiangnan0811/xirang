import type {
  CredentialAuditAction,
  CredentialAuditEventRecord,
  CredentialAuditMetadataValue,
  CredentialAuditOutcome,
} from "@/types/domain";
import i18n from "@/i18n";
import { ApiError, fetchWithFallback, formatTime, request, type PaginatedEnvelope, unwrapPaginated } from "./core";
import { finiteNumber, positiveNumberOrUndefined } from "./number-utils";

type CredentialAuditEventResponse = {
  id?: number | string;
  user_id?: number | string;
  username?: string;
  role?: string;
  action?: string;
  purpose?: string;
  credential_kind?: string;
  credential_source?: string;
  ssh_key_id?: number | string | null;
  node_id?: number | string | null;
  task_id?: number | string | null;
  task_run_id?: number | string | null;
  policy_id?: number | string | null;
  outcome?: string;
  error_message?: string | null;
  metadata?: unknown;
  client_ip?: string;
  user_agent?: string;
  created_at?: string;
};

export type CredentialAuditQueryOptions = {
  username?: string;
  role?: string;
  userId?: number;
  action?: string;
  purpose?: string;
  credentialKind?: string;
  credentialSource?: string;
  outcome?: CredentialAuditOutcome | string;
  sshKeyId?: number;
  nodeId?: number;
  taskId?: number;
  taskRunId?: number;
  policyId?: number;
  from?: string;
  to?: string;
  page?: number;
  pageSize?: number;
  sortBy?: "id" | "created_at" | "username" | "role" | "action" | "purpose" | "credential_kind" | "outcome";
  sortOrder?: "asc" | "desc";
};

const knownActions = new Set<string>([
  "ssh_key.test_connection",
  "ssh_key.export",
  "node.credential.test_connection",
  "auth.step_up",
  "terminal.open",
  "terminal.failure",
  "terminal.close",
  "task.manual_trigger",
  "task.restore_trigger",
  "task.batch_trigger",
  "snapshot.restore",
  "batch_command.create",
  "task.credential.use",
  "drill.trigger",
  "drill.phase",
  "file_browser.list",
  "file_browser.preview",
  "docker_volumes.discover",
  "config.export",
  "config.import",
  "node.doctor.run",
  "node_migration.preflight",
  "probe.ssh",
  "probe.metrics",
  "node_logs.collect",
]);

const forbiddenMetadataKeyMarkers = [
  "private",
  "password",
  "token",
  "secret",
  "credential",
  "config",
  "output",
  "stream",
  "command",
  "content",
  "payload",
];

const forbiddenMetadataValueMarkers = [
  ...forbiddenMetadataKeyMarkers,
  "bearer",
  "authorization",
];

const errorOutputMarkers = [
  "输出:",
  "output:",
  "stdout:",
  "stderr:",
  "command output:",
  "docker output:",
  "terminal stream:",
  "stream:",
  "file content:",
  "content:",
  "config:",
  "payload:",
  "diagnostic evidence:",
  "diagnostic:",
  "evidence:",
  "stack trace:",
  "traceback:",
  "panic:",
];

const stackLikeErrorPatterns = [
  /\bpanic\b/i,
  /\btraceback\b/i,
  /stack\s+trace/i,
  /\b(?:[\w.-]+\/)+[\w.-]+\.(?:go|ts|tsx|js|jsx|py|rb|java|php):\d+\b/,
];

const sensitiveErrorPatterns = [
  /authorization\s*[:=]\s*bearer\s+[^\s"',;)]+/gi,
  /bearer\s+[^\s"',;)]+/gi,
  /(private[_-]?key|token|api[_-]?key|secret|password)\s*[:=]\s*[^\s"',;)]+/gi,
];

const endpointPattern = /\b(?:https?|wss?):\/\/[^\s"'<>]+/gi;

function cleanString(value: unknown, maxLength = 500): string {
  const raw = String(value ?? "").trim();
  const clean = sanitizeLegacySensitiveText(raw);
  return clean.length > maxLength ? clean.slice(0, maxLength) : clean;
}

function sanitizeLegacySensitiveText(value: string): string {
  let clean = value.replace(endpointPattern, "[REDACTED_ENDPOINT]");
  for (const pattern of sensitiveErrorPatterns) {
    clean = clean.replace(pattern, "[REDACTED_SECRET]");
  }
  return clean;
}

function metadataKeyDenied(key: string): boolean {
  const lower = key.toLowerCase();
  return forbiddenMetadataKeyMarkers.some((marker) => lower.includes(marker));
}

function metadataValueDenied(value: string): boolean {
  const lower = value.toLowerCase();
  return forbiddenMetadataValueMarkers.some((marker) => lower.includes(marker));
}

function normalizeOutcome(value?: string): CredentialAuditOutcome {
  if (value === "success" || value === "failure" || value === "blocked") {
    return value;
  }
  return "unknown";
}

function normalizeAction(value?: string): CredentialAuditAction {
  return value && knownActions.has(value) ? (value as CredentialAuditAction) : "other";
}

function sanitizeMetadataValue(value: unknown): CredentialAuditMetadataValue | undefined {
  if (typeof value === "string") {
    const clean = cleanString(value);
    return clean && !metadataValueDenied(clean) ? clean : undefined;
  }
  if (typeof value === "number") {
    return Number.isFinite(value) ? value : undefined;
  }
  if (typeof value === "boolean") {
    return value;
  }
  if (Array.isArray(value)) {
    const items = value
      .filter((item): item is string => typeof item === "string")
      .map((item) => cleanString(item))
      .filter((item) => item && !metadataValueDenied(item))
      .slice(0, 16);
    return items.length ? items : undefined;
  }
  return undefined;
}

function parseMetadataObject(raw: unknown): Record<string, unknown> {
  if (!raw) {
    return {};
  }
  if (typeof raw === "string") {
    try {
      const parsed: unknown = JSON.parse(raw);
      return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {};
    } catch {
      return {};
    }
  }
  return typeof raw === "object" && !Array.isArray(raw) ? raw as Record<string, unknown> : {};
}

function sanitizeMetadata(raw: unknown): Record<string, CredentialAuditMetadataValue> {
  const source = parseMetadataObject(raw);
  const out: Record<string, CredentialAuditMetadataValue> = {};
  for (const [key, value] of Object.entries(source)) {
    const cleanKey = cleanString(key, 64);
    if (!cleanKey || metadataKeyDenied(cleanKey)) {
      continue;
    }
    const cleanValue = sanitizeMetadataValue(value);
    if (cleanValue === undefined) {
      continue;
    }
    out[cleanKey] = cleanValue;
    if (Object.keys(out).length >= 16) {
      break;
    }
  }
  return out;
}

function sanitizeErrorMessage(value: unknown): string {
  let clean = cleanString(value);
  if (!clean) {
    return "";
  }
  for (const marker of errorOutputMarkers) {
    const index = clean.toLowerCase().indexOf(marker.toLowerCase());
    if (index >= 0) {
      clean = `${clean.slice(0, index + marker.length).trim()} [REDACTED_OUTPUT]`;
    }
  }
  return stackLikeErrorPatterns.some((pattern) => pattern.test(clean)) ? "[REDACTED_ERROR]" : clean;
}

/** @internal exported for mapper tests */
export function mapCredentialAuditEvent(row: CredentialAuditEventResponse | null | undefined): CredentialAuditEventRecord {
  const rawAction = cleanString(row?.action, 64);
  return {
    id: finiteNumber(row?.id),
    userId: finiteNumber(row?.user_id),
    username: cleanString(row?.username, 64),
    role: cleanString(row?.role, 32),
    action: normalizeAction(rawAction),
    rawAction,
    purpose: cleanString(row?.purpose, 64),
    credentialKind: cleanString(row?.credential_kind, 32),
    credentialSource: cleanString(row?.credential_source, 64),
    sshKeyId: positiveNumberOrUndefined(row?.ssh_key_id),
    nodeId: positiveNumberOrUndefined(row?.node_id),
    taskId: positiveNumberOrUndefined(row?.task_id),
    taskRunId: positiveNumberOrUndefined(row?.task_run_id),
    policyId: positiveNumberOrUndefined(row?.policy_id),
    outcome: normalizeOutcome(row?.outcome),
    errorMessage: sanitizeErrorMessage(row?.error_message),
    metadata: sanitizeMetadata(row?.metadata),
    clientIP: cleanString(row?.client_ip, 64),
    userAgent: cleanString(row?.user_agent, 255),
    createdAt: formatTime(cleanString(row?.created_at)),
  };
}

/** @internal exported for mapper tests */
export function buildCredentialAuditQuery(options?: CredentialAuditQueryOptions): URLSearchParams {
  const query = new URLSearchParams();
  const stringFields: Array<[keyof CredentialAuditQueryOptions, string]> = [
    ["username", "username"],
    ["role", "role"],
    ["action", "action"],
    ["purpose", "purpose"],
    ["credentialKind", "credential_kind"],
    ["credentialSource", "credential_source"],
    ["outcome", "outcome"],
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

  const numericFields: Array<[keyof CredentialAuditQueryOptions, string]> = [
    ["userId", "user_id"],
    ["sshKeyId", "ssh_key_id"],
    ["nodeId", "node_id"],
    ["taskId", "task_id"],
    ["taskRunId", "task_run_id"],
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

export function createCredentialAuditApi() {
  return {
    async getCredentialAuditEvents(
      token: string,
      options?: CredentialAuditQueryOptions,
    ): Promise<{ items: CredentialAuditEventRecord[]; total: number; page: number; pageSize: number }> {
      const query = buildCredentialAuditQuery(options);
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const payload = await request<PaginatedEnvelope<CredentialAuditEventResponse[]>>(
        `/credential-audit-events${suffix}`,
        { token },
      );
      const result = unwrapPaginated(payload);
      return {
        items: result.items.map((row) => mapCredentialAuditEvent(row)),
        total: result.total,
        page: result.page,
        pageSize: result.pageSize,
      };
    },

    async exportCredentialAuditEventsCSV(
      token: string,
      options?: CredentialAuditQueryOptions,
    ): Promise<Blob> {
      const query = buildCredentialAuditQuery(options);
      const suffix = query.toString() ? `?${query.toString()}` : "";
      const response = await fetchWithFallback(`/credential-audit-events/export${suffix}`, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${token}`,
        },
      });

      if (!response.ok) {
        const text = await response.text();
        let detail: unknown = text;
        if (text) {
          try {
            detail = JSON.parse(text);
          } catch {
            detail = text;
          }
        }
        throw new ApiError(response.status, i18n.t("common.requestFailed", { status: response.status }), detail);
      }

      return response.blob();
    },
  };
}
