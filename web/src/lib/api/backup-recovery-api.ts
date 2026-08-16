import { request, type RequestOptions } from "./core";

export type RecoveryProduct<T> =
  | { status: "available"; value: T }
  | { status: "unavailable"; reason: "invalid_product" };

export type RecoveryPlanState =
  | "draft"
  | "preflight_ready"
  | "authorized"
  | "executed"
  | "canceled"
  | "superseded"
  | "expired";

export type RecoveryTargetMode = "isolated" | "in_place";
export type RecoveryConflictPolicy =
  | "fail_on_conflict"
  | "skip_existing"
  | "overwrite_selected"
  | "exact_mirror";
export type RecoverySecurityDecision = "allow_clean" | "block" | "admin_override";

export interface RecoveryPlan {
  id: string;
  state: RecoveryPlanState;
  revision: string;
  repositoryId: string;
  recoveryPointId: string;
  targetMode: RecoveryTargetMode;
  targetNodeId: number;
  targetRootId: string;
  conflictPolicy: RecoveryConflictPolicy;
  securityDecision: RecoverySecurityDecision;
  estimatedItems: number;
  estimatedBytes: number;
  createdAt: string;
  updatedAt: string;
}

export type RecoverySecurityFindingCategory = "malware" | "suspicious" | "test_signature";
export type RecoveryPreflightReason =
  | "node_unregistered"
  | "node_archived"
  | "node_offline"
  | "node_unauthorized"
  | "credential_purpose_invalid"
  | "tool_unavailable"
  | "source_unavailable"
  | "root_not_real"
  | "root_noncanonical"
  | "device_invalid"
  | "mount_invalid"
  | "owner_invalid"
  | "mode_invalid"
  | "symlink_component"
  | "xirang_root_overlap"
  | "source_root_overlap"
  | "insufficient_bytes"
  | "insufficient_inodes"
  | "active_writer"
  | "target_conflict"
  | "security_blocked";

export interface RecoveryImpact {
  createCount: number;
  overwriteCount: number;
  skipCount: number;
  deleteCount: number;
  estimatedItems: number;
  estimatedBytes: number;
}

export interface RecoveryPreflight {
  planId: string;
  persisted: boolean;
  planRevision: string | null;
  eligible: boolean;
  preferred: boolean;
  reasons: RecoveryPreflightReason[];
  preflightId: string | null;
  targetMode: RecoveryTargetMode | null;
  conflictPolicy: RecoveryConflictPolicy | null;
  impact: RecoveryImpact;
  security: {
    decision: RecoverySecurityDecision;
    findingCount: number;
    overridableCategories: RecoverySecurityFindingCategory[];
  };
  observedAt: string | null;
  expiresAt: string | null;
}

export type RecoveryAuthorizationOperation =
  | "security_override"
  | "write_authorize"
  | "exact_mirror_delete_authorize"
  | "execute";
export type RecoveryAuthorizationCategory = "security_override" | "write" | "exact_mirror_delete" | "execute";
export type RecoveryGrantCategory = "write" | "exact_mirror_delete";
export type RecoveryGrantStatus = "issued" | "consumed";

export interface RecoveryAuthorization {
  receiptId: string;
  planId: string;
  grant: null | {
    id: string;
    category: RecoveryGrantCategory;
    expiresAt: string;
    status: RecoveryGrantStatus;
  };
  jobId: string | null;
  operation: RecoveryAuthorizationOperation;
  category: RecoveryAuthorizationCategory;
  planRevision: string;
  replay: boolean;
}

export type RecoveryJobOutcome =
  | "queued"
  | "running"
  | "verifying"
  | "succeeded"
  | "degraded"
  | "needs_attention"
  | "failed"
  | "cancel_requested"
  | "canceled";
export type RecoveryFailureCategory =
  | "source_drift"
  | "verification_mismatch"
  | "remote_outcome_unresolved"
  | "partial_write"
  | "cleanup_unavailable";
export type RecoveryResultSetLifecycle = "ready" | "revoking" | "cleaned" | "cleanup_failed";

export interface RecoveryResultSet {
  id: string;
  lifecycle: RecoveryResultSetLifecycle;
  plaintextDeadline: string;
  hardDeadline: string;
  createdAt: string;
  updatedAt: string;
}

export interface RecoveryDeleteCheckpoint {
  id: string;
  attemptId: string;
  expectedPlanRevision: string;
  status: "awaiting_authorization";
  expiresAt: string;
}

export interface RecoveryJob {
  id: string;
  planId: string;
  outcome: RecoveryJobOutcome;
  revision: string;
  targetMode: RecoveryTargetMode;
  targetNodeId: number;
  targetRootId: string;
  estimatedItems: number;
  estimatedBytes: number;
  progress: {
    totalItems: number;
    completedItems: number;
    succeededItems: number;
    skippedItems: number;
    failedItems: number;
    bytesWritten: number;
  };
  failureCategory: RecoveryFailureCategory | null;
  deleteCheckpoint: RecoveryDeleteCheckpoint | null;
  resultSet: RecoveryResultSet | null;
  plaintextDeadline: string | null;
  createdAt: string;
  updatedAt: string;
}

export type RecoveryOperation = "create" | "overwrite" | "skip" | "delete";
export type RecoveryItemOutcome = "pending" | "succeeded" | "skipped" | "failed";

export interface RecoveryJobItem {
  id: string;
  ordinal: number;
  operation: RecoveryOperation;
  outcome: RecoveryItemOutcome;
  estimatedBytes: number;
  bytesWritten: number;
  verifiedSize: number;
  failureCategory: RecoveryFailureCategory | null;
  createdAt: string;
  updatedAt: string;
}

export interface RecoveryPage<T> {
  jobId: string;
  page: number;
  pageSize: number;
  total: number;
  items: T[];
}

export type RecoveryResultKind = "regular_file" | "verification_report";
export interface RecoveryResult {
  id: string;
  kind: RecoveryResultKind;
  size: number;
  modifiedAt: string | null;
  createdAt: string;
}

export interface RecoveryResultPage extends RecoveryPage<RecoveryResult> {
  resultSet: RecoveryResultSet;
}

type RawObject = Record<string, unknown>;

const planKeys = new Set([
  "schema_version",
  "id",
  "state",
  "revision",
  "repository_id",
  "recovery_point_id",
  "target_mode",
  "target_node_id",
  "target_root_id",
  "conflict_policy",
  "security_decision",
  "selection_digest",
  "operation_set_digest",
  "delete_set_digest",
  "estimated_items",
  "estimated_bytes",
  "created_at",
  "updated_at",
]);

function unavailable<T>(): RecoveryProduct<T> {
  return { status: "unavailable", reason: "invalid_product" };
}

function isRawObject(value: unknown): value is RawObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function hasOnlyKeys(value: RawObject, keys: ReadonlySet<string>): boolean {
  return Object.keys(value).every((key) => keys.has(key)) &&
    [...keys].every((key) => Object.hasOwn(value, key));
}

function hasAllowedKeys(value: RawObject, allowed: ReadonlySet<string>, required: readonly string[]): boolean {
  return Object.keys(value).every((key) => allowed.has(key)) && required.every((key) => Object.hasOwn(value, key));
}

function opaqueId(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value) ? value : null;
}

function decimalRevision(value: unknown): string | null {
  return typeof value === "string" && /^[1-9][0-9]*$/.test(value) ? value : null;
}

function digest(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value) ? value : null;
}

function safePositiveInteger(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : null;
}

function safeCount(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

function expectedPageLength(page: number, pageSize: number, total: number): { offset: number; length: number } | null {
  const offset = (page - 1) * pageSize;
  if (!Number.isSafeInteger(offset) || offset < 0) return null;
  return { offset, length: offset >= total ? 0 : Math.min(pageSize, total - offset) };
}

function utcInstant(value: unknown): string | null {
  if (typeof value !== "string" || !value.endsWith("Z")) return null;
  const epoch = Date.parse(value);
  return Number.isFinite(epoch) ? new Date(epoch).toISOString() : null;
}

function oneOf<T extends string>(value: unknown, values: readonly T[]): T | null {
  return typeof value === "string" && values.includes(value as T) ? value as T : null;
}

export function mapRecoveryPlanProduct(value: unknown): RecoveryProduct<RecoveryPlan> {
  if (!isRawObject(value) || !hasOnlyKeys(value, planKeys) || value.schema_version !== 1) {
    return unavailable();
  }
  const id = opaqueId(value.id);
  const state = oneOf(value.state, [
    "draft", "preflight_ready", "authorized", "executed", "canceled", "superseded", "expired",
  ] as const);
  const revision = decimalRevision(value.revision);
  const repositoryId = opaqueId(value.repository_id);
  const recoveryPointId = opaqueId(value.recovery_point_id);
  const targetMode = oneOf(value.target_mode, ["isolated", "in_place"] as const);
  const targetNodeId = safePositiveInteger(value.target_node_id);
  const conflictPolicy = oneOf(value.conflict_policy, [
    "fail_on_conflict", "skip_existing", "overwrite_selected", "exact_mirror",
  ] as const);
  const securityDecision = oneOf(value.security_decision, ["allow_clean", "block", "admin_override"] as const);
  const selectionDigest = digest(value.selection_digest);
  const operationSetDigest = digest(value.operation_set_digest);
  const deleteSetDigest = digest(value.delete_set_digest);
  const estimatedItems = safeCount(value.estimated_items);
  const estimatedBytes = safeCount(value.estimated_bytes);
  const createdAt = utcInstant(value.created_at);
  const updatedAt = utcInstant(value.updated_at);
  if (id === null || state === null || revision === null || repositoryId === null || recoveryPointId === null ||
    targetMode === null || targetNodeId === null || typeof value.target_root_id !== "string" ||
    value.target_root_id.length === 0 || value.target_root_id.length > 32 || conflictPolicy === null ||
    securityDecision === null || selectionDigest === null || operationSetDigest === null || deleteSetDigest === null ||
    estimatedItems === null || estimatedBytes === null || createdAt === null ||
    updatedAt === null || updatedAt < createdAt || (conflictPolicy === "exact_mirror" && targetMode !== "in_place")) {
    return unavailable();
  }
  return {
    status: "available",
    value: {
      id,
      state,
      revision,
      repositoryId,
      recoveryPointId,
      targetMode,
      targetNodeId,
      targetRootId: value.target_root_id,
      conflictPolicy,
      securityDecision,
      estimatedItems,
      estimatedBytes,
      createdAt,
      updatedAt,
    },
  };
}

const preflightReasons = [
  "node_unregistered", "node_archived", "node_offline", "node_unauthorized",
  "credential_purpose_invalid", "tool_unavailable", "source_unavailable", "root_not_real",
  "root_noncanonical", "device_invalid", "mount_invalid", "owner_invalid", "mode_invalid",
  "symlink_component", "xirang_root_overlap", "source_root_overlap", "insufficient_bytes",
  "insufficient_inodes", "active_writer", "target_conflict", "security_blocked",
] as const;
const findingCategories = ["malware", "suspicious", "test_signature"] as const;
const preflightKeys = new Set([
  "schema_version", "plan_id", "persisted", "plan_revision", "eligible", "preferred", "reasons",
  "preflight_id", "preflight_revision", "target_mode", "conflict_policy", "operation_set_digest",
  "delete_set_digest", "impact", "security", "observed_at", "expires_at",
]);
const preflightBaseKeys = [
  "schema_version", "plan_id", "persisted", "eligible", "preferred", "reasons", "impact", "security",
] as const;
const preflightPersistedKeys = [
  ...preflightBaseKeys, "plan_revision", "preflight_id", "preflight_revision", "target_mode",
  "conflict_policy", "operation_set_digest", "delete_set_digest", "observed_at", "expires_at",
] as const;
const impactKeys = new Set([
  "create_count", "overwrite_count", "skip_count", "delete_count", "estimated_items", "estimated_bytes",
]);
const securityKeys = new Set(["decision", "finding_count", "overridable_categories"]);

function stringArrayOf<T extends string>(value: unknown, values: readonly T[]): T[] | null {
  if (!Array.isArray(value)) return null;
  const result: T[] = [];
  const seen = new Set<T>();
  for (const raw of value) {
    const mapped = oneOf(raw, values);
    if (mapped === null || seen.has(mapped)) return null;
    seen.add(mapped);
    result.push(mapped);
  }
  return result;
}

function mapImpact(value: unknown): RecoveryImpact | null {
  if (!isRawObject(value) || !hasOnlyKeys(value, impactKeys)) return null;
  const createCount = safeCount(value.create_count);
  const overwriteCount = safeCount(value.overwrite_count);
  const skipCount = safeCount(value.skip_count);
  const deleteCount = safeCount(value.delete_count);
  const estimatedItems = safeCount(value.estimated_items);
  const estimatedBytes = safeCount(value.estimated_bytes);
  if (createCount === null || overwriteCount === null || skipCount === null || deleteCount === null ||
    estimatedItems === null || estimatedBytes === null ||
    createCount + overwriteCount + skipCount + deleteCount !== estimatedItems) return null;
  return { createCount, overwriteCount, skipCount, deleteCount, estimatedItems, estimatedBytes };
}

export function mapRecoveryPreflightProduct(value: unknown): RecoveryProduct<RecoveryPreflight> {
  if (!isRawObject(value) || value.schema_version !== 1 || typeof value.persisted !== "boolean" ||
    !hasAllowedKeys(value, preflightKeys, value.persisted ? preflightPersistedKeys : preflightBaseKeys)) {
    return unavailable();
  }
  const planId = opaqueId(value.plan_id);
  const reasons = stringArrayOf(value.reasons, preflightReasons);
  const impact = mapImpact(value.impact);
  if (!isRawObject(value.security) || !hasOnlyKeys(value.security, securityKeys)) return unavailable();
  const decision = oneOf(value.security.decision, ["allow_clean", "block", "admin_override"] as const);
  const findingCount = safeCount(value.security.finding_count);
  const overridableCategories = stringArrayOf(value.security.overridable_categories, findingCategories);
  if (planId === null || typeof value.eligible !== "boolean" || typeof value.preferred !== "boolean" ||
    reasons === null || impact === null || decision === null || findingCount === null || overridableCategories === null ||
    (value.preferred && !value.eligible) || (value.eligible && reasons.length > 0) ||
    (decision === "allow_clean" && (findingCount !== 0 || overridableCategories.length !== 0)) ||
    (decision === "block" && (findingCount === 0 || value.eligible)) ||
    (overridableCategories.length > findingCount)) return unavailable();

  if (!value.persisted) {
    if (Object.keys(value).some((key) => !preflightBaseKeys.includes(key as typeof preflightBaseKeys[number]))) {
      return unavailable();
    }
    return {
      status: "available",
      value: {
        planId, persisted: false, planRevision: null, eligible: value.eligible, preferred: value.preferred,
        reasons, preflightId: null, targetMode: null, conflictPolicy: null, impact,
        security: { decision, findingCount, overridableCategories }, observedAt: null, expiresAt: null,
      },
    };
  }

  const planRevision = decimalRevision(value.plan_revision);
  const preflightId = opaqueId(value.preflight_id);
  const targetMode = oneOf(value.target_mode, ["isolated", "in_place"] as const);
  const conflictPolicy = oneOf(value.conflict_policy, [
    "fail_on_conflict", "skip_existing", "overwrite_selected", "exact_mirror",
  ] as const);
  const observedAt = utcInstant(value.observed_at);
  const expiresAt = utcInstant(value.expires_at);
  if (planRevision === null || preflightId === null || typeof value.preflight_revision !== "string" ||
    value.preflight_revision.length === 0 || value.preflight_revision.length > 128 || targetMode === null ||
    conflictPolicy === null || digest(value.operation_set_digest) === null || digest(value.delete_set_digest) === null ||
    observedAt === null || expiresAt === null || expiresAt <= observedAt ||
    (conflictPolicy === "exact_mirror" && targetMode !== "in_place")) return unavailable();
  return {
    status: "available",
    value: {
      planId, persisted: true, planRevision, eligible: value.eligible, preferred: value.preferred, reasons,
      preflightId, targetMode, conflictPolicy, impact,
      security: { decision, findingCount, overridableCategories }, observedAt, expiresAt,
    },
  };
}

const authorizationKeys = new Set([
  "schema_version", "receipt_id", "plan_id", "grant_id", "grant_category", "grant_binding_digest",
  "grant_expires_at", "grant_status", "job_id", "operation", "category",
  "plan_transition_revision", "replay",
]);
const authorizationBaseKeys = [
  "schema_version", "receipt_id", "plan_id", "operation", "category", "plan_transition_revision", "replay",
] as const;
const authorizationGrantKeys = [
  "grant_id", "grant_category", "grant_binding_digest", "grant_expires_at", "grant_status",
] as const;

export function mapRecoveryAuthorizationProduct(value: unknown): RecoveryProduct<RecoveryAuthorization> {
  if (!isRawObject(value) || value.schema_version !== 1 ||
    !hasAllowedKeys(value, authorizationKeys, authorizationBaseKeys)) return unavailable();
  const receiptId = opaqueId(value.receipt_id);
  const planId = opaqueId(value.plan_id);
  const operation = oneOf(value.operation, [
    "security_override", "write_authorize", "exact_mirror_delete_authorize", "execute",
  ] as const);
  const category = oneOf(value.category, ["security_override", "write", "exact_mirror_delete", "execute"] as const);
  const planRevision = decimalRevision(value.plan_transition_revision);
  if (receiptId === null || planId === null || operation === null || category === null || planRevision === null ||
    typeof value.replay !== "boolean") return unavailable();

  const expectedCategory: Record<RecoveryAuthorizationOperation, RecoveryAuthorizationCategory> = {
    security_override: "security_override",
    write_authorize: "write",
    exact_mirror_delete_authorize: "exact_mirror_delete",
    execute: "execute",
  };
  if (category !== expectedCategory[operation]) return unavailable();
  if (operation === "security_override") {
    if (Object.keys(value).some((key) => !authorizationBaseKeys.includes(key as typeof authorizationBaseKeys[number]))) {
      return unavailable();
    }
    return {
      status: "available",
      value: { receiptId, planId, grant: null, jobId: null, operation, category, planRevision, replay: value.replay },
    };
  }
  if (!authorizationGrantKeys.every((key) => Object.hasOwn(value, key))) return unavailable();
  const grantId = opaqueId(value.grant_id);
  const grantCategory = oneOf(value.grant_category, ["write", "exact_mirror_delete"] as const);
  const grantExpiresAt = utcInstant(value.grant_expires_at);
  const grantStatus = oneOf(value.grant_status, ["issued", "consumed"] as const);
  const hasJob = operation === "execute" || operation === "exact_mirror_delete_authorize";
  const jobId = hasJob ? opaqueId(value.job_id) : null;
  if (grantId === null || grantCategory === null || digest(value.grant_binding_digest) === null ||
    grantExpiresAt === null || grantStatus === null || (hasJob && jobId === null) ||
    (!hasJob && Object.hasOwn(value, "job_id")) ||
    (operation === "write_authorize" && (grantCategory !== "write" || grantStatus !== "issued")) ||
    (operation === "execute" && (grantCategory !== "write" || grantStatus !== "consumed")) ||
    (operation === "exact_mirror_delete_authorize" &&
      (grantCategory !== "exact_mirror_delete" || grantStatus !== "issued"))) return unavailable();
  return {
    status: "available",
    value: {
      receiptId, planId, grant: { id: grantId, category: grantCategory, expiresAt: grantExpiresAt, status: grantStatus },
      jobId, operation, category, planRevision, replay: value.replay,
    },
  };
}

const resultSetKeys = new Set([
  "id", "state", "plaintext_deadline", "hard_deadline", "created_at", "updated_at",
]);

function mapResultSet(value: unknown): RecoveryResultSet | null {
  if (!isRawObject(value) || !hasOnlyKeys(value, resultSetKeys)) return null;
  const id = opaqueId(value.id);
  const lifecycle = oneOf(value.state, ["ready", "revoking", "cleaned", "cleanup_failed"] as const);
  const plaintextDeadline = utcInstant(value.plaintext_deadline);
  const hardDeadline = utcInstant(value.hard_deadline);
  const createdAt = utcInstant(value.created_at);
  const updatedAt = utcInstant(value.updated_at);
  if (id === null || lifecycle === null || plaintextDeadline === null || hardDeadline === null || createdAt === null ||
    updatedAt === null || createdAt >= plaintextDeadline || plaintextDeadline > hardDeadline ||
    updatedAt < createdAt) return null;
  return { id, lifecycle, plaintextDeadline, hardDeadline, createdAt, updatedAt };
}

const progressKeys = new Set([
  "total_items", "completed_items", "succeeded_items", "skipped_items", "failed_items", "bytes_written",
]);
const checkpointKeys = new Set(["id", "attempt_id", "expected_plan_revision", "status", "expires_at"]);
const jobKeys = new Set([
  "schema_version", "id", "plan_id", "state", "revision", "target_mode", "target_node_id", "target_root_id",
  "estimated_items", "estimated_bytes", "progress", "failure_category", "delete_checkpoint", "result_set",
  "plaintext_deadline", "created_at", "updated_at",
]);
const jobRequiredKeys = [
  "schema_version", "id", "plan_id", "state", "revision", "target_mode", "target_node_id", "target_root_id",
  "estimated_items", "estimated_bytes", "progress", "created_at", "updated_at",
] as const;
const failureCategories = [
  "source_drift", "verification_mismatch", "remote_outcome_unresolved", "partial_write", "cleanup_unavailable",
] as const;

export function mapRecoveryJobProduct(value: unknown): RecoveryProduct<RecoveryJob> {
  if (!isRawObject(value) || value.schema_version !== 1 || !hasAllowedKeys(value, jobKeys, jobRequiredKeys) ||
    !isRawObject(value.progress) || !hasOnlyKeys(value.progress, progressKeys)) return unavailable();
  const id = opaqueId(value.id);
  const planId = opaqueId(value.plan_id);
  const outcome = oneOf(value.state, [
    "queued", "running", "verifying", "succeeded", "degraded", "needs_attention", "failed",
    "cancel_requested", "canceled",
  ] as const);
  const revision = decimalRevision(value.revision);
  const targetMode = oneOf(value.target_mode, ["isolated", "in_place"] as const);
  const targetNodeId = safePositiveInteger(value.target_node_id);
  const estimatedItems = safeCount(value.estimated_items);
  const estimatedBytes = safeCount(value.estimated_bytes);
  const totalItems = safeCount(value.progress.total_items);
  const completedItems = safeCount(value.progress.completed_items);
  const succeededItems = safeCount(value.progress.succeeded_items);
  const skippedItems = safeCount(value.progress.skipped_items);
  const failedItems = safeCount(value.progress.failed_items);
  const bytesWritten = safeCount(value.progress.bytes_written);
  const createdAt = utcInstant(value.created_at);
  const updatedAt = utcInstant(value.updated_at);
  const failureCategory = value.failure_category === undefined
    ? null
    : oneOf(value.failure_category, failureCategories);
  const plaintextDeadline = value.plaintext_deadline === undefined ? null : utcInstant(value.plaintext_deadline);
  if (id === null || planId === null || outcome === null || revision === null || targetMode === null ||
    targetNodeId === null || typeof value.target_root_id !== "string" || value.target_root_id.length === 0 ||
    value.target_root_id.length > 32 || estimatedItems === null || estimatedBytes === null || totalItems === null ||
    completedItems === null || succeededItems === null || skippedItems === null || failedItems === null ||
    bytesWritten === null || createdAt === null || updatedAt === null || updatedAt < createdAt ||
    (value.failure_category !== undefined && failureCategory === null) ||
    (value.plaintext_deadline !== undefined && plaintextDeadline === null) || totalItems !== estimatedItems ||
    completedItems !== succeededItems + skippedItems + failedItems || completedItems > totalItems ||
    bytesWritten > estimatedBytes) return unavailable();

  switch (outcome) {
    case "queued":
      if (failureCategory !== null || completedItems !== 0 || bytesWritten !== 0) return unavailable();
      break;
    case "running":
    case "cancel_requested":
    case "canceled":
      if (failureCategory !== null || failedItems !== 0) return unavailable();
      break;
    case "verifying":
    case "succeeded":
    case "degraded":
      if (failureCategory !== null || completedItems !== totalItems || failedItems !== 0) return unavailable();
      break;
    case "needs_attention":
    case "failed":
      if (failureCategory === null) return unavailable();
      break;
  }

  let deleteCheckpoint: RecoveryDeleteCheckpoint | null = null;
  if (value.delete_checkpoint !== undefined) {
    if (!isRawObject(value.delete_checkpoint) || !hasOnlyKeys(value.delete_checkpoint, checkpointKeys)) return unavailable();
    const checkpointID = opaqueId(value.delete_checkpoint.id);
    const checkpointAttemptID = opaqueId(value.delete_checkpoint.attempt_id);
    const expectedPlanRevision = decimalRevision(value.delete_checkpoint.expected_plan_revision);
    const status = oneOf(value.delete_checkpoint.status, ["awaiting_authorization"] as const);
    const expiresAt = utcInstant(value.delete_checkpoint.expires_at);
    if (checkpointID === null || checkpointAttemptID === null || expectedPlanRevision === null || status === null ||
      expiresAt === null || targetMode !== "in_place" || outcome !== "running") return unavailable();
    deleteCheckpoint = {
      id: checkpointID, attemptId: checkpointAttemptID, expectedPlanRevision, status, expiresAt,
    };
  }

  const resultSet = value.result_set === undefined ? null : mapResultSet(value.result_set);
  if ((value.result_set !== undefined && resultSet === null) || (deleteCheckpoint !== null && resultSet !== null) ||
    (resultSet !== null && (targetMode !== "isolated" || (outcome !== "succeeded" && outcome !== "degraded"))) ||
    (targetMode === "in_place" && plaintextDeadline !== null)) return unavailable();
  return {
    status: "available",
    value: {
      id, planId, outcome, revision, targetMode, targetNodeId, targetRootId: value.target_root_id,
      estimatedItems, estimatedBytes,
      progress: { totalItems, completedItems, succeededItems, skippedItems, failedItems, bytesWritten },
      failureCategory, deleteCheckpoint, resultSet, plaintextDeadline, createdAt, updatedAt,
    },
  };
}

const itemPageKeys = new Set(["schema_version", "job_id", "page", "page_size", "total", "items"]);
const itemKeys = new Set([
  "id", "ordinal", "operation", "outcome", "estimated_bytes", "bytes_written", "verified_size",
  "failure_category", "created_at", "updated_at",
]);

export function mapRecoveryJobItemPageProduct(value: unknown): RecoveryProduct<RecoveryPage<RecoveryJobItem>> {
  if (!isRawObject(value) || !hasOnlyKeys(value, itemPageKeys) || value.schema_version !== 1 ||
    !Array.isArray(value.items)) return unavailable();
  const jobId = opaqueId(value.job_id);
  const page = safePositiveInteger(value.page);
  const pageSize = safePositiveInteger(value.page_size);
  const total = safeCount(value.total);
  const expectedPage = page === null || pageSize === null || total === null
    ? null
    : expectedPageLength(page, pageSize, total);
  if (jobId === null || page === null || pageSize === null || pageSize > 100 || total === null ||
    expectedPage === null || value.items.length !== expectedPage.length) return unavailable();
  const items: RecoveryJobItem[] = [];
  const ids = new Set<string>();
  for (const [index, raw] of value.items.entries()) {
    if (!isRawObject(raw) || !hasAllowedKeys(raw, itemKeys, [
      "id", "ordinal", "operation", "outcome", "estimated_bytes", "bytes_written", "verified_size",
      "created_at", "updated_at",
    ])) return unavailable();
    const id = opaqueId(raw.id);
    const ordinal = safeCount(raw.ordinal);
    const operation = oneOf(raw.operation, ["create", "overwrite", "skip", "delete"] as const);
    const outcome = oneOf(raw.outcome, ["pending", "succeeded", "skipped", "failed"] as const);
    const estimatedBytes = safeCount(raw.estimated_bytes);
    const bytesWritten = safeCount(raw.bytes_written);
    const verifiedSize = safeCount(raw.verified_size);
    const failureCategory = raw.failure_category === undefined ? null : oneOf(raw.failure_category, failureCategories);
    const createdAt = utcInstant(raw.created_at);
    const updatedAt = utcInstant(raw.updated_at);
    if (id === null || ids.has(id) || ordinal === null || ordinal !== expectedPage.offset + index ||
      operation === null || outcome === null ||
      estimatedBytes === null || bytesWritten === null || verifiedSize === null || createdAt === null || updatedAt === null ||
      updatedAt < createdAt || (raw.failure_category !== undefined && failureCategory === null) ||
      (outcome === "failed" && failureCategory === null) || (outcome !== "failed" && failureCategory !== null) ||
      (outcome === "pending" && (bytesWritten !== 0 || verifiedSize !== 0)) ||
      (outcome === "skipped" && (operation !== "skip" || bytesWritten !== 0)) ||
      (outcome === "succeeded" && (
        operation === "skip" ||
        ((operation === "create" || operation === "overwrite") &&
          (bytesWritten !== estimatedBytes || verifiedSize !== estimatedBytes)) ||
        (operation === "delete" && (bytesWritten !== 0 || verifiedSize !== 0))
      )) || bytesWritten > estimatedBytes) return unavailable();
    ids.add(id);
    items.push({
      id, ordinal, operation, outcome, estimatedBytes, bytesWritten, verifiedSize,
      failureCategory, createdAt, updatedAt,
    });
  }
  return { status: "available", value: { jobId, page, pageSize, total, items } };
}

const resultPageKeys = new Set(["schema_version", "job_id", "result_set", "page", "page_size", "total", "items"]);
const resultKeys = new Set(["id", "kind", "size", "modified_at", "created_at"]);

export function mapRecoveryResultPageProduct(value: unknown): RecoveryProduct<RecoveryResultPage> {
  if (!isRawObject(value) || !hasOnlyKeys(value, resultPageKeys) || value.schema_version !== 1 ||
    !Array.isArray(value.items)) return unavailable();
  const jobId = opaqueId(value.job_id);
  const resultSet = mapResultSet(value.result_set);
  const page = safePositiveInteger(value.page);
  const pageSize = safePositiveInteger(value.page_size);
  const total = safeCount(value.total);
  const expectedPage = page === null || pageSize === null || total === null
    ? null
    : expectedPageLength(page, pageSize, total);
  if (jobId === null || resultSet === null || resultSet.lifecycle !== "ready" || page === null || pageSize === null ||
    pageSize > 100 || total === null || expectedPage === null || value.items.length !== expectedPage.length) return unavailable();
  const items: RecoveryResult[] = [];
  const ids = new Set<string>();
  for (const raw of value.items) {
    if (!isRawObject(raw) || !hasAllowedKeys(raw, resultKeys, ["id", "kind", "size", "created_at"])) {
      return unavailable();
    }
    const id = opaqueId(raw.id);
    const kind = oneOf(raw.kind, ["regular_file", "verification_report"] as const);
    const size = safeCount(raw.size);
    const modifiedAt = raw.modified_at === undefined ? null : utcInstant(raw.modified_at);
    const createdAt = utcInstant(raw.created_at);
    if (id === null || ids.has(id) || kind === null || size === null || createdAt === null ||
      (raw.modified_at !== undefined && modifiedAt === null)) return unavailable();
    ids.add(id);
    items.push({ id, kind, size, modifiedAt, createdAt });
  }
  return { status: "available", value: { jobId, resultSet, page, pageSize, total, items } };
}

export interface RecoveryCryptoSource {
  getRandomValues(bytes: Uint8Array): Uint8Array;
}

export function generateRecoveryGrantSecret(
  cryptoSource: RecoveryCryptoSource | null | undefined = globalThis.crypto,
): string {
  if (cryptoSource == null || typeof cryptoSource.getRandomValues !== "function") {
    throw new Error("secure random unavailable");
  }
  const bytes = new Uint8Array(32);
  try {
    cryptoSource.getRandomValues(bytes);
  } catch {
    throw new Error("secure random unavailable");
  }
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  const encoded = btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
  if (encoded.length !== 43 || !/^[A-Za-z0-9_-]{43}$/.test(encoded)) {
    throw new Error("secure random unavailable");
  }
  return encoded;
}

export interface RecoveryPlanReceipt {
  planId: string;
  state: RecoveryPlanState;
  replay: boolean;
}

export interface RecoveryRetainReceipt {
  resultSetId: string;
  jobId: string;
  jobRevision: string;
  plaintextDeadline: string;
  hardDeadline: string;
}

export interface RecoveryCleanupReceipt {
  jobId: string;
  resultSetId: string;
  lifecycle: "revoking";
  scheduledAt: string;
}

export interface RecoveryDownloadTicket {
  contentUrl: string;
  contentType: string;
  contentLength: number;
  etag: string;
  lastModified: string | null;
  range: "none" | "single";
  classification: "non_secret" | "secret" | "unknown";
  expiresAt: string;
  idleExpiresAt: string;
}

const planReceiptKeys = new Set(["schema_version", "plan_id", "state", "replay"]);
const retainReceiptKeys = new Set([
  "schema_version", "result_set_id", "job_id", "job_revision", "plaintext_deadline", "hard_deadline",
]);
const cleanupReceiptKeys = new Set(["schema_version", "job_id", "result_set_id", "state", "scheduled_at"]);
const ticketKeys = new Set([
  "schema_version", "content_url", "action", "renderer", "profile", "content_type", "content_length", "etag",
  "last_modified", "range", "classification", "expires_at", "idle_expires_at", "capability_reason",
  "fallback_actions",
]);

function mapRecoveryPlanReceipt(value: unknown): RecoveryProduct<RecoveryPlanReceipt> {
  if (!isRawObject(value) || !hasOnlyKeys(value, planReceiptKeys) || value.schema_version !== 1) return unavailable();
  const planId = opaqueId(value.plan_id);
  const state = oneOf(value.state, [
    "draft", "preflight_ready", "authorized", "executed", "canceled", "superseded", "expired",
  ] as const);
  if (planId === null || state === null || typeof value.replay !== "boolean") return unavailable();
  return { status: "available", value: { planId, state, replay: value.replay } };
}

function mapRecoveryRetainReceipt(value: unknown): RecoveryProduct<RecoveryRetainReceipt> {
  if (!isRawObject(value) || !hasOnlyKeys(value, retainReceiptKeys) || value.schema_version !== 1) return unavailable();
  const resultSetId = opaqueId(value.result_set_id);
  const jobId = opaqueId(value.job_id);
  const jobRevision = decimalRevision(value.job_revision);
  const plaintextDeadline = utcInstant(value.plaintext_deadline);
  const hardDeadline = utcInstant(value.hard_deadline);
  if (resultSetId === null || jobId === null || jobRevision === null || plaintextDeadline === null ||
    hardDeadline === null || plaintextDeadline > hardDeadline) return unavailable();
  return {
    status: "available",
    value: { resultSetId, jobId, jobRevision, plaintextDeadline, hardDeadline },
  };
}

function mapRecoveryCleanupReceipt(value: unknown): RecoveryProduct<RecoveryCleanupReceipt> {
  if (!isRawObject(value) || !hasOnlyKeys(value, cleanupReceiptKeys) || value.schema_version !== 1) {
    return unavailable();
  }
  const jobId = opaqueId(value.job_id);
  const resultSetId = opaqueId(value.result_set_id);
  const lifecycle = oneOf(value.state, ["revoking"] as const);
  const scheduledAt = utcInstant(value.scheduled_at);
  if (jobId === null || resultSetId === null || lifecycle === null || scheduledAt === null) return unavailable();
  return { status: "available", value: { jobId, resultSetId, lifecycle, scheduledAt } };
}

function mapRecoveryDownloadTicket(value: unknown): RecoveryProduct<RecoveryDownloadTicket> {
  if (!isRawObject(value) || !hasOnlyKeys(value, ticketKeys) || value.schema_version !== 1 ||
    value.action !== "download" || value.renderer !== "attachment" || value.profile !== "original_v1" ||
    value.capability_reason !== null || !Array.isArray(value.fallback_actions) || value.fallback_actions.length !== 0) {
    return unavailable();
  }
  const contentUrl = typeof value.content_url === "string" &&
    /^\/api\/v1\/asset-content\/[0-9a-f]{32}$/.test(value.content_url) ? value.content_url : null;
  const contentLength = safeCount(value.content_length);
  const lastModified = value.last_modified === null ? null : utcInstant(value.last_modified);
  const range = oneOf(value.range, ["none", "single"] as const);
  const classification = oneOf(value.classification, ["non_secret", "secret", "unknown"] as const);
  const expiresAt = utcInstant(value.expires_at);
  const idleExpiresAt = utcInstant(value.idle_expires_at);
  if (contentUrl === null || typeof value.content_type !== "string" || value.content_type.length === 0 ||
    contentLength === null || typeof value.etag !== "string" || value.etag.length === 0 ||
    (value.last_modified !== null && lastModified === null) || range === null || classification === null ||
    expiresAt === null || idleExpiresAt === null || idleExpiresAt > expiresAt) return unavailable();
  return {
    status: "available",
    value: {
      contentUrl, contentType: value.content_type, contentLength, etag: value.etag,
      lastModified, range, classification, expiresAt, idleExpiresAt,
    },
  };
}

type RecoveryRequester = (path: string, options?: RequestOptions) => Promise<unknown>;
const coreRecoveryRequester: RecoveryRequester = (path, options) => request<unknown>(path, options);

interface RecoveryCall {
  signal?: AbortSignal;
}

interface RecoveryRevisionCall extends RecoveryCall {
  expectedRevision: string;
}

interface RecoveryAuthorityCall extends RecoveryCall {
  proof: string;
  idempotencyKey: string;
}

function validCallId(value: string): boolean {
  return opaqueId(value) !== null;
}

function validEntryId(value: string): boolean {
  return /^[0-9a-f]{64}$/.test(value);
}

function validRootId(value: string): boolean {
  return value.length > 0 && value.length <= 32 && value.trim() === value;
}

function validReason(value: string): boolean {
  return value.length > 0 && value.length <= 2048 && value.trim().length > 0;
}

function validReplayKey(value: string): boolean {
  return value.length >= 16 && value.length <= 256 && value.trim() === value;
}

function validProof(value: string): boolean {
  return value.length > 0 && value.length <= 8192;
}

function validGrantSecret(value: string): boolean {
  return /^[A-Za-z0-9_-]{43}$/.test(value);
}

function assertRecoveryCall(condition: boolean): void {
  if (!condition) throw new Error("invalid recovery request");
}

export interface CreateRecoveryPlanInput extends RecoveryCall {
  repositoryId: string;
  recoveryPointId: string;
  catalogGenerationId: string;
  entryIds: string[];
  targetMode: RecoveryTargetMode;
  targetNodeId: number;
  targetRootId: string;
  conflictPolicy: RecoveryConflictPolicy;
  idempotencyKey: string;
}

export interface RecoveryPlanRevisionInput extends RecoveryRevisionCall {
  planId: string;
}

export interface RecoveryJobRevisionInput extends RecoveryRevisionCall {
  jobId: string;
}

export interface RecoverySecurityOverrideInput extends RecoveryPlanRevisionInput, RecoveryAuthorityCall {
  preflightId: string;
  findingCategory: RecoverySecurityFindingCategory;
  reason: string;
}

export interface RecoveryWriteAuthorizationInput extends RecoveryPlanRevisionInput, RecoveryAuthorityCall {
  preflightId: string;
  reason: string;
  grantSecret: string;
}

export interface RecoveryExecuteInput extends RecoveryPlanRevisionInput, RecoveryAuthorityCall {
  preflightId: string;
  grantId: string;
  grantSecret: string;
}

export interface RecoveryDeleteAuthorizationInput extends RecoveryJobRevisionInput, RecoveryAuthorityCall {
  planId: string;
  checkpointId: string;
  attemptId: string;
  reason: string;
  grantSecret: string;
}

export interface RecoveryPageInput extends RecoveryCall {
  jobId: string;
  page: number;
  pageSize: number;
}

export interface RecoveryRetainInput extends RecoveryJobRevisionInput {
  requestedDeadline: string;
  proof: string;
}

export interface RecoveryDownloadInput extends RecoveryCall {
  jobId: string;
  resultId: string;
  proof: string;
}

export function createBackupRecoveryApi(requester: RecoveryRequester = coreRecoveryRequester) {
  const revisionBody = (expectedRevision: string) => ({ schema_version: 1, expected_revision: expectedRevision });
  const revisionOptions = (token: string, input: RecoveryRevisionCall): RequestOptions => ({
    method: "POST", token, signal: input.signal, body: revisionBody(input.expectedRevision),
  });
  const authorizationOptions = (
    token: string,
    input: RecoveryAuthorityCall,
    body: Record<string, unknown>,
  ): RequestOptions => ({
    method: "POST", token, stepUpProof: input.proof, idempotencyKey: input.idempotencyKey,
    signal: input.signal, body,
  });
  return {
    async createPlan(token: string, input: CreateRecoveryPlanInput) {
      assertRecoveryCall(validCallId(input.repositoryId) && validCallId(input.recoveryPointId) &&
        validCallId(input.catalogGenerationId) && input.entryIds.length > 0 && input.entryIds.length <= 10_000 &&
        input.entryIds.every(validEntryId) && new Set(input.entryIds).size === input.entryIds.length &&
        (input.targetMode === "isolated" || input.targetMode === "in_place") &&
        safePositiveInteger(input.targetNodeId) !== null && validRootId(input.targetRootId) &&
        ["fail_on_conflict", "skip_existing", "overwrite_selected", "exact_mirror"].includes(input.conflictPolicy) &&
        (input.conflictPolicy !== "exact_mirror" || input.targetMode === "in_place") && validReplayKey(input.idempotencyKey));
      const raw = await requester("/recovery-plans", {
        method: "POST", token, idempotencyKey: input.idempotencyKey, signal: input.signal,
        body: {
          schema_version: 1, repository_id: input.repositoryId, recovery_point_id: input.recoveryPointId,
          catalog_generation_id: input.catalogGenerationId, entry_ids: [...input.entryIds], target_mode: input.targetMode,
          target_node_id: input.targetNodeId, target_root_id: input.targetRootId, conflict_policy: input.conflictPolicy,
        },
      });
      return mapRecoveryPlanReceipt(raw);
    },
    async getPlan(token: string, planId: string, signal?: AbortSignal) {
      assertRecoveryCall(validCallId(planId));
      return mapRecoveryPlanProduct(await requester(`/recovery-plans/${planId}`, { token, signal }));
    },
    async preflight(token: string, input: RecoveryPlanRevisionInput) {
      assertRecoveryCall(validCallId(input.planId) && decimalRevision(input.expectedRevision) !== null);
      return mapRecoveryPreflightProduct(await requester(
        `/recovery-plans/${input.planId}/preflights`, revisionOptions(token, input),
      ));
    },
    async overrideSecurity(token: string, input: RecoverySecurityOverrideInput) {
      assertRecoveryCall(validCallId(input.planId) && decimalRevision(input.expectedRevision) !== null &&
        validCallId(input.preflightId) && findingCategories.includes(input.findingCategory) &&
        validReason(input.reason) && validProof(input.proof) && validReplayKey(input.idempotencyKey));
      return mapRecoveryAuthorizationProduct(await requester(
        `/recovery-plans/${input.planId}/security-overrides`,
        authorizationOptions(token, input, {
          schema_version: 1, expected_revision: input.expectedRevision, preflight_id: input.preflightId,
          finding_category: input.findingCategory, reason: input.reason,
        }),
      ));
    },
    async authorizeWrite(token: string, input: RecoveryWriteAuthorizationInput) {
      assertRecoveryCall(validCallId(input.planId) && decimalRevision(input.expectedRevision) !== null &&
        validCallId(input.preflightId) && validReason(input.reason) && validProof(input.proof) &&
        validReplayKey(input.idempotencyKey) && validGrantSecret(input.grantSecret));
      return mapRecoveryAuthorizationProduct(await requester(
        `/recovery-plans/${input.planId}/write-authorizations`,
        authorizationOptions(token, input, {
          schema_version: 1, expected_revision: input.expectedRevision, preflight_id: input.preflightId,
          reason: input.reason, grant_secret: input.grantSecret,
        }),
      ));
    },
    async execute(token: string, input: RecoveryExecuteInput) {
      assertRecoveryCall(validCallId(input.planId) && decimalRevision(input.expectedRevision) !== null &&
        validCallId(input.preflightId) && validCallId(input.grantId) && validGrantSecret(input.grantSecret) &&
        validProof(input.proof) && validReplayKey(input.idempotencyKey));
      return mapRecoveryAuthorizationProduct(await requester(
        `/recovery-plans/${input.planId}/execute`,
        authorizationOptions(token, input, {
          schema_version: 1, expected_revision: input.expectedRevision, preflight_id: input.preflightId,
          grant_id: input.grantId, grant_secret: input.grantSecret,
        }),
      ));
    },
    async getJob(token: string, jobId: string, signal?: AbortSignal) {
      assertRecoveryCall(validCallId(jobId));
      return mapRecoveryJobProduct(await requester(`/recovery-jobs/${jobId}`, { token, signal }));
    },
    async authorizeExactMirrorDelete(token: string, input: RecoveryDeleteAuthorizationInput) {
      assertRecoveryCall(validCallId(input.jobId) && validCallId(input.planId) && validCallId(input.checkpointId) &&
        validCallId(input.attemptId) && decimalRevision(input.expectedRevision) !== null && validReason(input.reason) &&
        validGrantSecret(input.grantSecret) && validProof(input.proof) && validReplayKey(input.idempotencyKey));
      return mapRecoveryAuthorizationProduct(await requester(
        `/recovery-jobs/${input.jobId}/exact-mirror-delete-authorizations`,
        authorizationOptions(token, input, {
          schema_version: 1, plan_id: input.planId, checkpoint_id: input.checkpointId,
          attempt_id: input.attemptId, expected_revision: input.expectedRevision,
          reason: input.reason, grant_secret: input.grantSecret,
        }),
      ));
    },
    async getJobItems(token: string, input: RecoveryPageInput) {
      assertRecoveryCall(validCallId(input.jobId) && safePositiveInteger(input.page) !== null &&
        safePositiveInteger(input.pageSize) !== null && input.pageSize <= 100);
      return mapRecoveryJobItemPageProduct(await requester(
        `/recovery-jobs/${input.jobId}/items?page=${input.page}&page_size=${input.pageSize}`,
        { token, signal: input.signal },
      ));
    },
    async getJobResults(token: string, input: RecoveryPageInput) {
      assertRecoveryCall(validCallId(input.jobId) && safePositiveInteger(input.page) !== null &&
        safePositiveInteger(input.pageSize) !== null && input.pageSize <= 100);
      return mapRecoveryResultPageProduct(await requester(
        `/recovery-jobs/${input.jobId}/results?page=${input.page}&page_size=${input.pageSize}`,
        { token, signal: input.signal },
      ));
    },
    async cancelPlan(token: string, input: RecoveryPlanRevisionInput) {
      assertRecoveryCall(validCallId(input.planId) && decimalRevision(input.expectedRevision) !== null);
      return mapRecoveryPlanProduct(await requester(
        `/recovery-plans/${input.planId}/cancel`, revisionOptions(token, input),
      ));
    },
    async cancelJob(token: string, input: RecoveryJobRevisionInput) {
      assertRecoveryCall(validCallId(input.jobId) && decimalRevision(input.expectedRevision) !== null);
      return mapRecoveryJobProduct(await requester(
        `/recovery-jobs/${input.jobId}/cancel`, revisionOptions(token, input),
      ));
    },
    async retainResults(token: string, input: RecoveryRetainInput) {
      const requestedDeadline = utcInstant(input.requestedDeadline);
      assertRecoveryCall(validCallId(input.jobId) && decimalRevision(input.expectedRevision) !== null &&
        requestedDeadline !== null && validProof(input.proof));
      return mapRecoveryRetainReceipt(await requester(`/recovery-jobs/${input.jobId}/results/retain`, {
        method: "POST", token, stepUpProof: input.proof, signal: input.signal,
        body: { schema_version: 1, expected_revision: input.expectedRevision, requested_deadline: requestedDeadline },
      }));
    },
    async issueResultDownloadTicket(token: string, input: RecoveryDownloadInput) {
      assertRecoveryCall(validCallId(input.jobId) && validCallId(input.resultId) && validProof(input.proof));
      return mapRecoveryDownloadTicket(await requester(
        `/recovery-jobs/${input.jobId}/results/${input.resultId}/download-ticket`,
        { method: "POST", token, stepUpProof: input.proof, signal: input.signal, body: { schema_version: 1 } },
      ));
    },
    async cleanupResults(token: string, input: RecoveryJobRevisionInput) {
      assertRecoveryCall(validCallId(input.jobId) && decimalRevision(input.expectedRevision) !== null);
      return mapRecoveryCleanupReceipt(await requester(
        `/recovery-jobs/${input.jobId}/results/cleanup`, revisionOptions(token, input),
      ));
    },
  };
}

export type BackupRecoveryApi = ReturnType<typeof createBackupRecoveryApi>;
