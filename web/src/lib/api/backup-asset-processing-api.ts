import type {
  AssetRef,
  BackupProcessingAdminControl,
  BackupProcessingBackfillPolicy,
  BackupAssetProcessingState,
  BackupProcessingCoverage,
  BackupProcessingCoverageBucket,
  BackupProcessingCoverageSummary,
  BackupProcessingFallbackAction,
  BackupProcessingFreshness,
  BackupProcessingProduct,
  BackupProcessingProductState,
  BackupProcessingRepresentation,
  BackupProcessingScanStatus,
  BackupProcessingSensitivityStatus,
  BackupProcessingUpdaterCandidate,
  BackupProcessingUpdaterCapabilityChange,
  BackupProcessingUpdaterCandidateState,
  BackupProcessingUpdaterStatus,
} from "@/types/domain";

import { request, type RequestOptions } from "./core";

type ProcessingRequester = (path: string, options: RequestOptions) => Promise<unknown>;

const defaultRequester: ProcessingRequester = (path, options) => request<unknown>(path, options);
const LOWER_HEX_32 = /^[0-9a-f]{32}$/;
const LOWER_HEX_64 = /^[0-9a-f]{64}$/;
const IDENTIFIER = /^[a-z0-9][a-z0-9._:-]{0,127}$/;
const SEMVER = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;
const PROCESSING_STATES = [
  "queued",
  "leased",
  "fetching",
  "materializing",
  "processing",
  "uploading",
  "validating",
  "retry_wait",
  "cancel_requested",
  "canceled",
  "succeeded",
  "failed",
  "superseded",
  "expired",
] as const;

function rawObject(value: unknown, required: readonly string[], optional: readonly string[] = []): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("invalid backup processing response");
  }
  const raw = value as Record<string, unknown>;
  const allowed = new Set([...required, ...optional]);
  if (required.some((key) => !(key in raw)) || Object.keys(raw).some((key) => !allowed.has(key))) {
    throw new Error("invalid backup processing response");
  }
  return raw;
}

function closedValue<T extends string>(value: unknown, allowed: readonly T[]): T {
  if (typeof value !== "string" || !allowed.includes(value as T)) {
    throw new Error("invalid backup processing response");
  }
  return value as T;
}

function optionalClosedValue<T extends string>(value: unknown, allowed: readonly T[]): T | null {
  return value === undefined || value === "" ? null : closedValue(value, allowed);
}

function identifier(value: unknown, optional = false): string | null {
  if (optional && (value === undefined || value === "")) return null;
  if (typeof value !== "string" || !IDENTIFIER.test(value)) {
    throw new Error("invalid backup processing response");
  }
  return value;
}

function lowerHex(value: unknown, expression: RegExp): string {
  if (typeof value !== "string" || !expression.test(value)) {
    throw new Error("invalid backup processing response");
  }
  return value;
}

function boundedInteger(value: unknown, maximum = Number.MAX_SAFE_INTEGER): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0 || value > maximum) {
    throw new Error("invalid backup processing response");
  }
  return value;
}

function utcTime(value: unknown, optional = false): string | null {
  if (optional && (value === undefined || value === null || value === "")) return null;
  if (typeof value !== "string" || value.length > 64 || !value.endsWith("Z") || !Number.isFinite(Date.parse(value))) {
    throw new Error("invalid backup processing response");
  }
  return value;
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("invalid backup processing response");
  return value;
}

function positiveInteger(value: unknown, minimum: number, maximum: number): number {
  const result = boundedInteger(value, maximum);
  if (result < minimum) throw new Error("invalid backup processing response");
  return result;
}

function validateCountMap(value: unknown, allowedKeys?: readonly string[]): void {
  const raw = rawObject(value, [], allowedKeys ?? Object.keys(rawRecord(value)));
  for (const count of Object.values(raw)) boundedInteger(count);
}

function rawRecord(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new Error("invalid backup processing response");
  }
  const raw = value as Record<string, unknown>;
  for (const key of Object.keys(raw)) identifier(key);
  return raw;
}

export function mapProcessingBackfillPolicy(value: unknown): BackupProcessingBackfillPolicy {
  const raw = rawObject(value, [
    "schema_version",
    "revision",
    "paused",
    "batch_size",
    "jobs_per_hour",
    "bytes_per_hour",
    "provider_concurrency",
    "capability_concurrency",
  ]);
  if (raw.schema_version !== 1) throw new Error("invalid backup processing response");
  return {
    schemaVersion: 1,
    revision: lowerHex(raw.revision, LOWER_HEX_64),
    paused: booleanValue(raw.paused),
    batchSize: positiveInteger(raw.batch_size, 1, 10_000),
    jobsPerHour: positiveInteger(raw.jobs_per_hour, 1, 100_000),
    bytesPerHour: positiveInteger(raw.bytes_per_hour, 65_536, 1_099_511_627_776),
    providerConcurrency: positiveInteger(raw.provider_concurrency, 1, 32),
    capabilityConcurrency: positiveInteger(raw.capability_concurrency, 1, 32),
  };
}

export function mapProcessingAdminControl(value: unknown): BackupProcessingAdminControl {
  const raw = rawObject(value, [
    "schema_version",
    "configured",
    "local_enabled",
    "remote_enabled",
    "backfill_policy",
    "worker_counts",
    "slots",
    "queue",
    "outcomes",
    "derived",
    "reconciled_at",
  ]);
  if (raw.schema_version !== 1) throw new Error("invalid backup processing response");

  const workers = rawObject(raw.worker_counts, ["active", "draining", "degraded", "quarantined"]);
  Object.values(workers).forEach((count) => boundedInteger(count));
  const slots = rawObject(raw.slots, [
    "interactive_used",
    "interactive_total",
    "background_used",
    "background_total",
  ]);
  Object.values(slots).forEach((count) => boundedInteger(count));
  const queue = rawObject(raw.queue, ["total", "by_state", "by_priority", "oldest_queued_seconds"]);
  boundedInteger(queue.total);
  boundedInteger(queue.oldest_queued_seconds);
  validateCountMap(queue.by_state, PROCESSING_STATES);
  validateCountMap(queue.by_priority, ["interactive", "background"]);
  const outcomes = rawObject(raw.outcomes, ["by_error_category"]);
  validateCountMap(outcomes.by_error_category);
  const derived = rawObject(raw.derived, [
    "by_state",
    "logical_bytes",
    "physical_bytes",
    "orphan_bytes",
    "quota_bytes",
  ]);
  validateCountMap(derived.by_state);
  for (const key of ["logical_bytes", "physical_bytes", "orphan_bytes", "quota_bytes"] as const) {
    boundedInteger(derived[key]);
  }
  utcTime(raw.reconciled_at, true);

  return {
    schemaVersion: 1,
    configured: booleanValue(raw.configured),
    localEnabled: booleanValue(raw.local_enabled),
    remoteEnabled: booleanValue(raw.remote_enabled),
    backfillPolicy: mapProcessingBackfillPolicy(raw.backfill_policy),
  };
}

export function mapProcessingProduct(value: unknown): BackupProcessingProduct {
  const raw = rawObject(
    value,
    ["schema_version", "state", "representation", "retryable", "fallback_actions", "terminal"],
    [
      "job_id",
      "capability",
      "profile",
      "coverage",
      "freshness",
      "scan_status",
      "sensitivity_status",
      "reason",
      "poll_after_seconds",
    ]
  );
  if (raw.schema_version !== 1 || !Array.isArray(raw.fallback_actions) || raw.fallback_actions.length > 2) {
    throw new Error("invalid backup processing response");
  }
  const state = closedValue<BackupProcessingProductState>(raw.state, [
    "native",
    "derived",
    "partial",
    "unsupported",
    "not_deployed",
    "queued",
    "failed",
  ]);
  const representation = closedValue<BackupProcessingRepresentation>(raw.representation, [
    "thumbnail",
    "text",
    "document_pages",
    "media_preview",
    "archive_index",
  ]);
  const fallbackActions = raw.fallback_actions.map((item) =>
    closedValue<BackupProcessingFallbackAction>(item, ["native_preview", "download"])
  );
  if (new Set(fallbackActions).size !== fallbackActions.length) {
    throw new Error("invalid backup processing response");
  }
  const terminal = booleanValue(raw.terminal);
  const jobId = raw.job_id === undefined || raw.job_id === "" ? null : lowerHex(raw.job_id, LOWER_HEX_32);
  const pollAfterSeconds = raw.poll_after_seconds === undefined ? 0 : boundedInteger(raw.poll_after_seconds, 30);
  if (state === "queued" ? jobId === null || terminal || pollAfterSeconds < 1 : !terminal || pollAfterSeconds !== 0) {
    throw new Error("invalid backup processing response");
  }
  return {
    schemaVersion: 1,
    jobId,
    state,
    representation,
    capability: identifier(raw.capability, true),
    profile: identifier(raw.profile, true),
    coverage: optionalClosedValue<BackupProcessingCoverage>(raw.coverage, ["complete", "partial"]),
    freshness: optionalClosedValue<BackupProcessingFreshness>(raw.freshness, ["current", "stale"]),
    scanStatus: optionalClosedValue<BackupProcessingScanStatus>(raw.scan_status, [
      "not_scanned",
      "no_finding",
      "finding",
      "stale",
    ]),
    sensitivityStatus: optionalClosedValue<BackupProcessingSensitivityStatus>(raw.sensitivity_status, [
      "non_secret",
      "secret",
      "unknown",
      "stale",
    ]),
    reason: identifier(raw.reason, true),
    retryable: booleanValue(raw.retryable),
    fallbackActions,
    pollAfterSeconds,
    terminal,
  };
}

export function mapAssetProcessingState(value: unknown): BackupAssetProcessingState {
  const raw = rawObject(value, ["schema_version", "representations"]);
  if (raw.schema_version !== 1 || !Array.isArray(raw.representations) || raw.representations.length > 5) {
    throw new Error("invalid backup processing response");
  }
  return { schemaVersion: 1, representations: raw.representations.map(mapProcessingProduct) };
}

const COVERAGE_KEYS = [
  "eligible",
  "completed",
  "partial",
  "queued",
  "failed",
  "unsupported",
  "not_deployed",
  "stale",
] as const;

function mapCoverageNumbers(raw: Record<string, unknown>) {
  return {
    eligible: boundedInteger(raw.eligible),
    completed: boundedInteger(raw.completed),
    partial: boundedInteger(raw.partial),
    queued: boundedInteger(raw.queued),
    failed: boundedInteger(raw.failed),
    unsupported: boundedInteger(raw.unsupported),
    notDeployed: boundedInteger(raw.not_deployed),
    stale: boundedInteger(raw.stale),
  };
}

function mapCoverageBucket(value: unknown): BackupProcessingCoverageBucket {
  const raw = rawObject(value, ["capability", "profile", ...COVERAGE_KEYS]);
  return {
    capability: identifier(raw.capability) ?? "",
    profile: identifier(raw.profile) ?? "",
    ...mapCoverageNumbers(raw),
  };
}

export function mapProcessingCoverageSummary(value: unknown): BackupProcessingCoverageSummary {
  const raw = rawObject(value, [
    "schema_version",
    "generated_at",
    ...COVERAGE_KEYS,
    "backlog_age_bucket",
    "estimated_seconds",
    "by_capability",
  ]);
  if (raw.schema_version !== 1 || !Array.isArray(raw.by_capability) || raw.by_capability.length > 64) {
    throw new Error("invalid backup processing response");
  }
  return {
    schemaVersion: 1,
    generatedAt: utcTime(raw.generated_at) ?? "",
    ...mapCoverageNumbers(raw),
    backlogAgeBucket: identifier(raw.backlog_age_bucket) ?? "",
    estimatedSeconds: raw.estimated_seconds === null ? null : boundedInteger(raw.estimated_seconds),
    byCapability: raw.by_capability.map(mapCoverageBucket),
  };
}

function mapCapabilityChange(value: unknown): BackupProcessingUpdaterCapabilityChange {
  const raw = rawObject(value, ["capability", "capability_schema", "profiles"]);
  if (!Array.isArray(raw.profiles) || raw.profiles.length === 0 || raw.profiles.length > 16) {
    throw new Error("invalid backup processing response");
  }
  const profiles = raw.profiles.map((profile) => identifier(profile) ?? "");
  if (new Set(profiles).size !== profiles.length) throw new Error("invalid backup processing response");
  return {
    capability: identifier(raw.capability) ?? "",
    capabilitySchema: identifier(raw.capability_schema) ?? "",
    profiles,
  };
}

function mapUpdaterCandidate(value: unknown): BackupProcessingUpdaterCandidate {
  const raw = rawObject(
    value,
    [
      "candidate_id",
      "source_kind",
      "source_id",
      "version",
      "manifest_digest",
      "signing_key_fingerprint",
      "bundle_fingerprint",
      "state",
      "capability_changes",
    ],
    ["reason", "verified_at", "activated_at"]
  );
  if (typeof raw.version !== "string" || raw.version.length > 64 || !SEMVER.test(raw.version) ||
    !Array.isArray(raw.capability_changes) || raw.capability_changes.length > 32) {
    throw new Error("invalid backup processing response");
  }
  return {
    candidateId: lowerHex(raw.candidate_id, LOWER_HEX_32),
    sourceKind: closedValue(raw.source_kind, ["builtin", "admin_registered"] as const),
    sourceId: identifier(raw.source_id) ?? "",
    version: raw.version,
    manifestDigest: lowerHex(raw.manifest_digest, LOWER_HEX_64),
    signingKeyFingerprint: lowerHex(raw.signing_key_fingerprint, LOWER_HEX_64),
    bundleFingerprint: lowerHex(raw.bundle_fingerprint, LOWER_HEX_64),
    state: closedValue<BackupProcessingUpdaterCandidateState>(raw.state, [
      "registered",
      "verified",
      "active",
      "superseded",
      "failed",
    ]),
    reason: identifier(raw.reason, true),
    verifiedAt: utcTime(raw.verified_at, true),
    activatedAt: utcTime(raw.activated_at, true),
    capabilityChanges: raw.capability_changes.map(mapCapabilityChange),
  };
}

export function mapProcessingUpdaterCandidates(value: unknown): BackupProcessingUpdaterCandidate[] {
  const raw = rawObject(value, ["schema_version", "items"]);
  if (raw.schema_version !== 1 || !Array.isArray(raw.items) || raw.items.length > 100) {
    throw new Error("invalid backup processing response");
  }
  return raw.items.map(mapUpdaterCandidate);
}

export function mapProcessingUpdaterStatus(value: unknown): BackupProcessingUpdaterStatus {
  const raw = rawObject(value, ["schema_version", "enabled", "online_enabled"], ["active"]);
  if (raw.schema_version !== 1) throw new Error("invalid backup processing response");
  return {
    schemaVersion: 1,
    enabled: booleanValue(raw.enabled),
    onlineEnabled: booleanValue(raw.online_enabled),
    active: raw.active === undefined || raw.active === null ? null : mapUpdaterCandidate(raw.active),
  };
}

function validateRef(ref: AssetRef): void {
  lowerHex(ref.recoveryPointId, LOWER_HEX_32);
  lowerHex(ref.entryId, LOWER_HEX_64);
}

function scopedPath(ref: AssetRef): string {
  validateRef(ref);
  return `/recovery-points/${encodeURIComponent(ref.recoveryPointId)}/entries/${encodeURIComponent(ref.entryId)}`;
}

function mapAccepted(value: unknown): void {
  const raw = rawObject(value, ["schema_version", "accepted"]);
  if (raw.schema_version !== 1 || raw.accepted !== true) throw new Error("invalid backup processing response");
}

export function createBackupAssetProcessingApi(requester: ProcessingRequester = defaultRequester) {
  return {
    async getAdminControl(token: string, signal?: AbortSignal) {
      return mapProcessingAdminControl(await requester("/admin/backup-asset-processing", { token, signal }));
    },
    async getState(token: string, ref: AssetRef, signal?: AbortSignal) {
      return mapAssetProcessingState(await requester(`${scopedPath(ref)}/processing`, { token, signal }));
    },
    async createPreview(
      token: string,
      ref: AssetRef,
      representation: BackupProcessingRepresentation,
      profile?: string,
      signal?: AbortSignal
    ) {
      const body: Record<string, unknown> = { schema_version: 1, representation };
      if (profile !== undefined) body.profile = identifier(profile);
      return mapProcessingProduct(await requester(`${scopedPath(ref)}/preview-jobs`, {
        method: "POST",
        token,
        body,
        signal,
      }));
    },
    async pollPreview(token: string, ref: AssetRef, jobId: string, signal?: AbortSignal) {
      lowerHex(jobId, LOWER_HEX_32);
      return mapProcessingProduct(await requester(`${scopedPath(ref)}/preview-jobs/${jobId}`, { token, signal }));
    },
    async cancelPreview(token: string, ref: AssetRef, jobId: string, signal?: AbortSignal) {
      lowerHex(jobId, LOWER_HEX_32);
      const raw = await requester(`${scopedPath(ref)}/preview-jobs/${jobId}/cancel`, {
        method: "POST",
        token,
        body: { schema_version: 1 },
        signal,
      });
      mapAccepted(rawObject(raw, ["schema_version", "canceled"]).canceled === true
        ? { schema_version: 1, accepted: true }
        : raw);
    },
    async getCoverage(token: string, signal?: AbortSignal) {
      return mapProcessingCoverageSummary(await requester("/admin/backup-asset-processing/coverage", { token, signal }));
    },
    async getUpdaterStatus(token: string, signal?: AbortSignal) {
      return mapProcessingUpdaterStatus(await requester("/admin/backup-asset-processing/updater", { token, signal }));
    },
    async listOfflineCandidates(token: string, signal?: AbortSignal) {
      return mapProcessingUpdaterCandidates(await requester(
        "/admin/backup-asset-processing/updater/offline-candidates",
        { token, signal }
      ));
    },
    async scanOfflineCandidates(token: string, signal?: AbortSignal) {
      const raw = await requester("/admin/backup-asset-processing/updater/offline-candidates/scan", {
        method: "POST",
        token,
        signal,
      });
      mapAccepted(raw);
    },
    async activateOfflineCandidate(
      token: string,
      candidateId: string,
      expectedActiveFingerprint: string | null,
      signal?: AbortSignal
    ) {
      lowerHex(candidateId, LOWER_HEX_32);
      if (expectedActiveFingerprint !== null) lowerHex(expectedActiveFingerprint, LOWER_HEX_64);
      const raw = await requester("/admin/backup-asset-processing/updater/offline-imports", {
        method: "POST",
        token,
        body: {
          schema_version: 1,
          candidate_id: candidateId,
          expected_active_fingerprint: expectedActiveFingerprint,
        },
        signal,
      });
      mapAccepted(raw);
    },
    async updateBackfillPolicy(
      token: string,
      policy: BackupProcessingBackfillPolicy,
      signal?: AbortSignal
    ) {
      const validated = mapProcessingBackfillPolicy({
        schema_version: policy.schemaVersion,
        revision: policy.revision,
        paused: policy.paused,
        batch_size: policy.batchSize,
        jobs_per_hour: policy.jobsPerHour,
        bytes_per_hour: policy.bytesPerHour,
        provider_concurrency: policy.providerConcurrency,
        capability_concurrency: policy.capabilityConcurrency,
      });
      return mapProcessingBackfillPolicy(await requester("/admin/backup-asset-processing/backfill-policy", {
        method: "PATCH",
        token,
        body: {
          schema_version: 1,
          expected_revision: validated.revision,
          paused: validated.paused,
          batch_size: validated.batchSize,
          jobs_per_hour: validated.jobsPerHour,
          bytes_per_hour: validated.bytesPerHour,
          provider_concurrency: validated.providerConcurrency,
          capability_concurrency: validated.capabilityConcurrency,
        },
        signal,
      }));
    },
  };
}

export type BackupAssetProcessingApi = ReturnType<typeof createBackupAssetProcessingApi>;
