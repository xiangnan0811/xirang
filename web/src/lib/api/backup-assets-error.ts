import type { CatalogCapabilityCode } from "@/types/domain";

import { ApiError } from "./core";

export type BackupAssetsErrorContext =
  | "repositories"
  | "recovery_points"
  | "recovery_point"
  | "directory"
  | "entry"
  | "search"
  | "cursor"
  | "overlay_mutation"
  | "content_ticket"
  | "evidence"
  | "diff";

export type BackupAssetsUIErrorCode =
  | "feature_disabled"
  | "permission_denied"
  | "not_found"
  | "invalid_request"
  | "stale_cursor"
  | "conflict"
  | "unsupported"
  | "temporarily_unavailable"
  | "rate_limited"
  | "secret_reveal_required"
  | "secure_transport_required"
  | "preview_renderer_unsupported"
  | "unknown";

export type BackupAssetsUIErrorAction =
  | "none"
  | "retry"
  | "refetch"
  | "refresh_first_page"
  | "return_context"
  | "return_overview";

export interface BackupAssetsUIError {
  code: BackupAssetsUIErrorCode;
  translationKey:
    | "backupAssets.errors.featureDisabled"
    | "backupAssets.errors.permissionDenied"
    | "backupAssets.errors.notFound"
    | "backupAssets.errors.invalidRequest"
    | "backupAssets.errors.staleCursor"
    | "backupAssets.errors.conflict"
    | "backupAssets.errors.unsupported"
    | "backupAssets.errors.temporarilyUnavailable"
    | "backupAssets.errors.rateLimited"
    | "backupAssets.errors.secretRevealRequired"
    | "backupAssets.errors.secureTransportRequired"
    | "backupAssets.errors.previewRendererUnsupported"
    | "backupAssets.errors.unknown";
  retryable: boolean;
  action: BackupAssetsUIErrorAction;
  retryAfter?: number;
  capabilityCode?: CatalogCapabilityCode;
}

const MAX_DETAIL_LENGTH = 4096;
const MAX_REASON_PARAMS = 8;
const MAX_REASON_PARAM_LENGTH = 128;
const capabilityCodes = new Set<CatalogCapabilityCode>([
  "feature_disabled",
  "task_artifact_contract_missing",
  "repository_offline",
  "repository_disconnected",
  "provider_unavailable",
  "repository_identity_unavailable",
  "provider_protocol_incompatible",
  "provider_operation_timeout",
  "provider_resource_limit",
  "point_not_committed",
  "mutable_source_changed",
  "catalog_unavailable",
  "sequential_read_unavailable",
  "range_unavailable",
  "download_unavailable",
  "restore_unavailable",
  "diff_unavailable",
  "unknown_internal_state",
]);

export function mapBackupAssetsError(
  error: unknown,
  context: BackupAssetsErrorContext
): BackupAssetsUIError {
  if (!(error instanceof ApiError)) return uiError("unknown", false, "none");

  const capabilityCode = parseCapabilityCode(error.detail);
  if (capabilityCode === "feature_disabled") {
    return withCapability(uiError("feature_disabled", false, "return_overview"), capabilityCode);
  }

  if (error.status === 403 && isSecretRevealRequired(error.detail)) {
    return uiError("secret_reveal_required", false, "none");
  }
  if (error.status === 503 && context === "content_ticket" && isSecureTransportRequired(error.detail)) {
    return uiError("secure_transport_required", false, "none");
  }
  if (error.status === 422 && context === "content_ticket" && isPreviewRendererUnsupported(error.detail)) {
    return uiError("preview_renderer_unsupported", false, "none");
  }
  if (error.status === 403) return uiError("permission_denied", false, "none");
  if (error.status === 404) return uiError("not_found", false, "return_context");
  if (error.status === 400) return uiError("invalid_request", false, "none");
  if (error.status === 409 && context === "cursor") {
    return uiError("stale_cursor", true, "refresh_first_page");
  }
  if (error.status === 409 && context === "overlay_mutation") {
    return uiError("conflict", false, "refetch");
  }
  if (error.status === 409) return uiError("conflict", false, "refetch");
  if (error.status === 429) {
    const mapped = uiError("rate_limited", true, "retry");
    const retryAfter = boundedRetryAfter(error.retryAfter);
    return retryAfter === undefined ? mapped : { ...mapped, retryAfter };
  }
  if (error.status === 501) {
    const mapped = uiError("unsupported", false, "none");
    return capabilityCode === undefined ? mapped : withCapability(mapped, capabilityCode);
  }
  if (error.status === 503) {
    const mapped = uiError("temporarily_unavailable", true, "retry");
    return capabilityCode === undefined ? mapped : withCapability(mapped, capabilityCode);
  }
  return uiError("unknown", false, "none");
}

function uiError(
  code: BackupAssetsUIErrorCode,
  retryable: boolean,
  action: BackupAssetsUIErrorAction
): BackupAssetsUIError {
  const keys: Record<BackupAssetsUIErrorCode, BackupAssetsUIError["translationKey"]> = {
    feature_disabled: "backupAssets.errors.featureDisabled",
    permission_denied: "backupAssets.errors.permissionDenied",
    not_found: "backupAssets.errors.notFound",
    invalid_request: "backupAssets.errors.invalidRequest",
    stale_cursor: "backupAssets.errors.staleCursor",
    conflict: "backupAssets.errors.conflict",
    unsupported: "backupAssets.errors.unsupported",
    temporarily_unavailable: "backupAssets.errors.temporarilyUnavailable",
    rate_limited: "backupAssets.errors.rateLimited",
    secret_reveal_required: "backupAssets.errors.secretRevealRequired",
    secure_transport_required: "backupAssets.errors.secureTransportRequired",
    preview_renderer_unsupported: "backupAssets.errors.previewRendererUnsupported",
    unknown: "backupAssets.errors.unknown",
  };
  return { code, translationKey: keys[code], retryable, action };
}

function withCapability(
  error: BackupAssetsUIError,
  capabilityCode: CatalogCapabilityCode
): BackupAssetsUIError {
  return { ...error, capabilityCode };
}

function boundedRetryAfter(value: number | undefined): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0 && value <= 86_400
    ? value
    : undefined;
}

function isSecretRevealRequired(detail: unknown): boolean {
  if (!boundedSerializableObject(detail)) return false;
  const data = detail.data;
  if (!isPlainRecord(data)) return false;
  const reason = data.reason;
  return isPlainRecord(reason) && reason.code === "secret_reveal_required" && validReasonParams(reason.params ?? {});
}

function isSecureTransportRequired(detail: unknown): boolean {
  if (!boundedSerializableObject(detail)) return false;
  const data = detail.data;
  if (!isPlainRecord(data)) return false;
  const reason = data.reason;
  return (
    isPlainRecord(reason) &&
    Object.keys(reason).length === 2 &&
    reason.code === "secure_transport_required" &&
    isPlainRecord(reason.params) &&
    Object.keys(reason.params).length === 0
  );
}

function isPreviewRendererUnsupported(detail: unknown): boolean {
  if (!boundedSerializableObject(detail)) return false;
  const data = detail.data;
  if (!isPlainRecord(data)) return false;
  const reason = data.reason;
  return (
    isPlainRecord(reason) &&
    Object.keys(reason).length === 2 &&
    reason.code === "preview_renderer_unsupported" &&
    isPlainRecord(reason.params) &&
    Object.keys(reason.params).length === 0
  );
}

function parseCapabilityCode(detail: unknown): CatalogCapabilityCode | undefined {
  if (!boundedSerializableObject(detail)) return undefined;
  const data = detail.data;
  if (!isPlainRecord(data)) return undefined;
  const reason = data.reason;
  if (!isPlainRecord(reason) || typeof reason.code !== "string" || !capabilityCodes.has(reason.code as CatalogCapabilityCode)) {
    return undefined;
  }
  if (!validReasonParams(reason.params)) return undefined;
  return reason.code as CatalogCapabilityCode;
}

function boundedSerializableObject(value: unknown): value is Record<string, unknown> {
  if (!isPlainRecord(value)) return false;
  try {
    return JSON.stringify(value).length <= MAX_DETAIL_LENGTH;
  } catch {
    return false;
  }
}

function validReasonParams(value: unknown): boolean {
  if (!isPlainRecord(value)) return false;
  const entries = Object.entries(value);
  return (
    entries.length <= MAX_REASON_PARAMS &&
    entries.every(
      ([key, entry]) =>
        /^[a-z][a-z0-9_]{0,63}$/.test(key) &&
        typeof entry === "string" &&
        entry.length <= MAX_REASON_PARAM_LENGTH &&
        !/[\r\n\0]/.test(entry)
    )
  );
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}
