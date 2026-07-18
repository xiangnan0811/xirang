import type {
  BackupImmutabilityLevel,
  BackupProviderKind,
  BackupRecoveryPoint,
  CatalogCapabilityCode,
  CatalogCapabilityReason,
  CatalogCapabilitySet,
  CatalogCoverageStatus,
  CatalogGeneration,
  CatalogGenerationErrorCode,
  CatalogGenerationState,
  CatalogProjection,
  CatalogPublicationFailureCode,
  CatalogStalenessStatus,
  CatalogStatus,
  EvidenceLayerStatus,
  ManifestCompleteness,
  ProviderCompletionClass,
  RecoveryPointEvidence,
  RecoveryPointHoldState,
  RecoveryPointPage,
  RecoveryPointPhysicalAvailability,
  RecoveryPointSemantics,
  RecoveryPointState,
  RestoreDrillStatus,
  TaskRunTriggerType,
  TaskStatus,
} from "@/types/domain";
import { request } from "./core";

type RawObject = Record<string, unknown>;

type RawRecoveryPointPage = {
  items?: unknown;
  next_cursor?: unknown;
};

export type RecoveryPointSort = "captured_desc" | "captured_asc" | "created_desc";

export interface ListRecoveryPointsOptions {
  limit?: number;
  cursor?: string;
  sort?: RecoveryPointSort;
  signal?: AbortSignal;
}

const blockedReason: CatalogCapabilityReason = {
  code: "unknown_internal_state",
  params: {},
};

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

const allowedCapabilityParams = new Set([
  "provider_kind",
  "repository_status",
  "recovery_point_state",
  "capability",
  "correlation_id",
  "retry_after_seconds",
]);

function isRawObject(value: unknown): value is RawObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function blocked<T>(): CatalogProjection<T> {
  return { status: "blocked", reason: blockedReason };
}

function available<T>(value: T): CatalogProjection<T> {
  return { status: "available", value };
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function booleanValue(value: unknown): boolean {
  return value === true;
}

function finiteInteger(value: unknown, minimum = 0): number | null {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= minimum ? parsed : null;
}

function optionalPositiveInteger(value: unknown): number | undefined {
  const parsed = finiteInteger(value, 1);
  return parsed === null ? undefined : parsed;
}

/** @internal shared by the Catalog API boundary mappers. */
export function normalizeCatalogTime(value: unknown): string | null {
  if (typeof value !== "string" || value.trim() === "") {
    return null;
  }
  const milliseconds = Date.parse(value);
  return Number.isFinite(milliseconds) ? new Date(milliseconds).toISOString() : null;
}

/** @internal shared by the Catalog API boundary mappers. */
export function normalizeNullableCatalogTime(value: unknown): string | null {
  if (value === null || value === undefined || value === "") {
    return null;
  }
  return normalizeCatalogTime(value);
}

function opaqueId(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value) ? value : null;
}

function catalogGenerationState(value: unknown): CatalogGenerationState | null {
  switch (value) {
    case "building":
    case "complete":
    case "partial":
    case "failed":
    case "superseded":
      return value;
    default:
      return null;
  }
}

function catalogGenerationErrorCode(value: unknown): CatalogGenerationErrorCode | null {
  switch (value) {
    case "":
    case "catalog_build_abandoned":
    case "catalog_build_failed":
    case "catalog_build_incomplete":
    case "catalog_build_limit":
    case "catalog_build_timeout":
    case "catalog_identity_key_unavailable":
    case "catalog_invalid_record":
    case "catalog_projection_mismatch":
    case "catalog_proof_mismatch":
    case "catalog_provider_resource_limit":
    case "catalog_provider_timeout":
    case "catalog_provider_unavailable":
    case "catalog_source_changed":
      return value;
    default:
      return null;
  }
}

function catalogCoverageStatus(value: unknown): CatalogCoverageStatus | null {
  switch (value) {
    case "building":
    case "complete":
    case "partial":
    case "failed":
    case "unavailable":
      return value;
    default:
      return null;
  }
}

function catalogStalenessStatus(value: unknown): CatalogStalenessStatus | null {
  switch (value) {
    case "fresh":
    case "stale":
    case "unknown":
      return value;
    default:
      return null;
  }
}

function recoveryPointSemantics(value: unknown): RecoveryPointSemantics | null {
  switch (value) {
    case "native_snapshot":
    case "xirang_manifest":
    case "imported_baseline":
    case "mutable_head":
      return value;
    default:
      return null;
  }
}

function recoveryPointState(value: unknown): RecoveryPointState | null {
  switch (value) {
    case "observed":
    case "retired":
    case "preparing":
    case "verifying":
    case "committed":
    case "degraded":
    case "expiring":
    case "expired":
    case "failed":
    case "purge_blocked":
      return value;
    default:
      return null;
  }
}

function physicalAvailability(value: unknown): RecoveryPointPhysicalAvailability | null {
  switch (value) {
    case "online":
    case "offline":
    case "missing":
    case "unknown":
      return value;
    default:
      return null;
  }
}

function holdState(value: unknown): RecoveryPointHoldState | null {
  switch (value) {
    case "none":
    case "active":
    case "released":
      return value;
    default:
      return null;
  }
}

function immutabilityLevel(value: unknown): BackupImmutabilityLevel | null {
  switch (value) {
    case "mutable":
    case "xirang_managed":
    case "backend_versioned":
    case "storage_worm":
      return value;
    default:
      return null;
  }
}

function evidenceLayerStatus(value: unknown): EvidenceLayerStatus | null {
  switch (value) {
    case "recorded":
    case "unavailable":
    case "not_recorded":
    case "invalid":
      return value;
    default:
      return null;
  }
}

function manifestCompleteness(value: unknown): ManifestCompleteness | null {
  switch (value) {
    case "complete":
    case "partial":
    case "unavailable":
      return value;
    default:
      return null;
  }
}

function providerKind(value: unknown): BackupProviderKind | null {
  switch (value) {
    case "restic":
    case "rsync":
    case "rclone":
    case "command":
    case "verified_import":
      return value;
    default:
      return null;
  }
}

function providerCompletion(value: unknown): ProviderCompletionClass | null {
  switch (value) {
    case "known_exit_zero":
    case "known_nonzero":
    case "outcome_unknown":
      return value;
    default:
      return null;
  }
}

function restoreDrillStatus(value: unknown): RestoreDrillStatus | null {
  switch (value) {
    case "pending":
    case "running":
    case "success":
    case "failed":
    case "skipped":
    case "canceled":
      return value;
    default:
      return null;
  }
}

function taskRunTrigger(value: unknown): TaskRunTriggerType | null {
  switch (value) {
    case "manual":
    case "cron":
    case "retry":
    case "restore":
    case "chain":
    case "drill":
      return value;
    default:
      return null;
  }
}

function taskStatus(value: unknown): TaskStatus | null {
  switch (value) {
    case "pending":
    case "running":
    case "success":
    case "failed":
    case "retrying":
    case "canceled":
    case "warning":
    case "skipped":
      return value;
    default:
      return null;
  }
}

function publicationFailureCode(value: unknown): CatalogPublicationFailureCode | null | undefined {
  if (value === "" || value === null || value === undefined) {
    return null;
  }
  if (typeof value !== "string") {
    return undefined;
  }
  switch (value) {
    case "publication_precondition_missing":
    case "publication_in_progress":
    case "publication_session_abandoned":
    case "evidence_missing_summary":
    case "evidence_malformed_stream":
    case "evidence_duplicate_summary":
    case "evidence_non_final_summary":
    case "evidence_invalid_native_id":
    case "provider_nonzero_exit":
    case "provider_timeout":
    case "provider_canceled":
    case "provider_resource_limit":
    case "provider_outcome_unknown":
    case "provider_completion_unproven":
    case "provider_snapshot_rewritten":
    case "repository_identity_drift":
    case "run_tag_missing":
    case "ambiguous_run_tags":
    case "native_point_conflict":
    case "manifest_partial":
    case "manifest_unavailable":
    case "lease_fence_lost":
    case "publication_deadline_exceeded":
    case "snapshot_missing_at_deadline":
    case "legacy_fallback_blocked":
    case "legacy_operation_blocked":
    case "source_drift":
    case "external_writer_detected":
    case "unexpected_version":
    case "marker_mismatch":
    case "manifest_mismatch":
      return value;
    default:
      return undefined;
  }
}

/** @internal exported for the three Catalog API boundary modules and mapper tests. */
export function mapCatalogCapabilityReason(value: unknown): CatalogCapabilityReason | null {
  if (value === null || value === undefined) {
    return null;
  }
  if (!isRawObject(value) || typeof value.code !== "string" || !capabilityCodes.has(value.code as CatalogCapabilityCode)) {
    return blockedReason;
  }
  const code = value.code as CatalogCapabilityCode;
  if (!isRawObject(value.params)) {
    return { code, params: {} };
  }
  const params: Record<string, string> = {};
  for (const [key, rawParam] of Object.entries(value.params)) {
    if (!allowedCapabilityParams.has(key) || typeof rawParam !== "string" || rawParam.length > 128 || /[\r\n\0]/.test(rawParam)) {
      return blockedReason;
    }
    if (key === "retry_after_seconds" && !/^[1-9][0-9]*$/.test(rawParam)) {
      return blockedReason;
    }
    params[key] = rawParam;
  }
  return { code, params };
}

/** @internal exported for repository mapping. */
export function mapCatalogCapabilities(value: unknown): CatalogCapabilitySet {
  const raw = isRawObject(value) ? value : {};
  return {
    list: booleanValue(raw.list),
    searchPath: booleanValue(raw.search_path),
    openSequential: booleanValue(raw.open_sequential),
    openRange: booleanValue(raw.open_range),
    download: booleanValue(raw.download),
    restore: booleanValue(raw.restore),
    diff: booleanValue(raw.diff),
    nativeHistory: booleanValue(raw.native_history),
    reason: mapCatalogCapabilityReason(raw.reason),
  };
}

/** @internal exported for repository mapping. */
export function mapCatalogPermissions(value: unknown) {
  const raw = isRawObject(value) ? value : {};
  return {
    list: booleanValue(raw.list),
    preview: booleanValue(raw.preview),
    download: booleanValue(raw.download),
  };
}

/** @internal exported for other Catalog API boundary mappers. */
export function mapCatalogContentAvailability(value: unknown) {
  if (!isRawObject(value)) {
    return null;
  }
  const availableValue = booleanValue(value.available);
  const reason = mapCatalogCapabilityReason(value.reason);
  if (availableValue && reason !== null) {
    return null;
  }
  return { available: availableValue, reason };
}

function mapGeneration(value: unknown): CatalogGeneration | null | undefined {
  if (value === null || value === undefined) {
    return null;
  }
  if (!isRawObject(value)) {
    return undefined;
  }
  const id = opaqueId(value.id);
  const sequence = finiteInteger(value.sequence, 1);
  const state = catalogGenerationState(value.state);
  const errorCode = catalogGenerationErrorCode(value.error_code);
  const startedAt = normalizeCatalogTime(value.started_at);
  const finishedAt = normalizeNullableCatalogTime(value.finished_at);
  if (id === null || sequence === null || state === null || errorCode === null || startedAt === null ||
    (value.finished_at !== null && value.finished_at !== undefined && finishedAt === null)) {
    return undefined;
  }
  return {
    id,
    sequence,
    state,
    startedAt,
    finishedAt,
    errorCode,
    correlationId: stringValue(value.correlation_id),
  };
}

export function mapCatalogStatus(value: unknown): CatalogProjection<CatalogStatus> {
  if (!isRawObject(value) || !isRawObject(value.coverage) || !isRawObject(value.staleness)) {
    return blocked();
  }
  const generation = mapGeneration(value.generation);
  const latestBuild = mapGeneration(value.latest_build);
  const coverageStatus = catalogCoverageStatus(value.coverage.status);
  const indexedEntries = finiteInteger(value.coverage.indexed_entries);
  const expectedEntries = value.coverage.expected_entries === null
    ? null
    : finiteInteger(value.coverage.expected_entries);
  const coverageObservedAt = normalizeCatalogTime(value.coverage.observed_at);
  const stalenessStatus = catalogStalenessStatus(value.staleness.status);
  const stalenessObservedAt = normalizeNullableCatalogTime(value.staleness.observed_at);
  const contentAvailability = mapCatalogContentAvailability(value.content_availability);
  if (generation === undefined || latestBuild === undefined || coverageStatus === null || indexedEntries === null ||
    expectedEntries === null && value.coverage.expected_entries !== null || coverageObservedAt === null ||
    stalenessStatus === null || contentAvailability === null ||
    (value.staleness.observed_at !== null && value.staleness.observed_at !== undefined && stalenessObservedAt === null) ||
    (generation !== null && generation.state !== "complete") ||
    (coverageStatus === "complete" && generation === null)) {
    return blocked();
  }
  return available({
    generation,
    latestBuild,
    coverage: {
      status: coverageStatus,
      indexedEntries,
      expectedEntries,
      manifestDigest: stringValue(value.coverage.manifest_digest),
      observedAt: coverageObservedAt,
    },
    staleness: {
      status: stalenessStatus,
      observedAt: stalenessObservedAt,
      reason: mapCatalogCapabilityReason(value.staleness.reason),
    },
    contentAvailability,
    permissions: mapCatalogPermissions(value.permissions),
  });
}

export function mapRecoveryPoint(value: unknown): CatalogProjection<BackupRecoveryPoint> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = opaqueId(value.id);
  const repositoryId = opaqueId(value.repository_id);
  const semantics = recoveryPointSemantics(value.semantics);
  const state = recoveryPointState(value.state);
  const availabilityValue = physicalAvailability(value.physical_availability);
  const hold = holdState(value.hold_state);
  const immutability = immutabilityLevel(value.immutability_level);
  const entryCount = finiteInteger(value.entry_count);
  const logicalBytes = finiteInteger(value.logical_bytes);
  const capabilityRevision = finiteInteger(value.capability_revision, 1);
  const createdAt = normalizeCatalogTime(value.created_at);
  const updatedAt = normalizeCatalogTime(value.updated_at);
  const catalog = mapCatalogStatus(value.catalog);
  if (id === null || repositoryId === null || semantics === null || state === null || availabilityValue === null ||
    hold === null || immutability === null || entryCount === null || logicalBytes === null || capabilityRevision === null ||
    createdAt === null || updatedAt === null || catalog.status !== "available") {
    return blocked();
  }
  const lineage = isRawObject(value.lineage) ? value.lineage : {};
  const sourceRecoveryPointId = lineage.source_recovery_point_id === undefined || lineage.source_recovery_point_id === ""
    ? undefined
    : opaqueId(lineage.source_recovery_point_id) ?? undefined;
  return available({
    id,
    repositoryId,
    lineage: {
      producingTaskId: optionalPositiveInteger(lineage.producing_task_id),
      producingTaskRunId: optionalPositiveInteger(lineage.producing_task_run_id),
      sourceRecoveryPointId,
    },
    semantics,
    state,
    physicalAvailability: availabilityValue,
    holdState: hold,
    immutabilityLevel: immutability,
    manifestDigest: stringValue(value.manifest_digest),
    entryCount,
    logicalBytes,
    capturedAt: normalizeNullableCatalogTime(value.captured_at),
    committedAt: normalizeNullableCatalogTime(value.committed_at),
    observedAt: normalizeNullableCatalogTime(value.observed_at),
    capabilityRevision,
    capabilities: mapCatalogCapabilities(value.capabilities),
    createdAt,
    updatedAt,
    producingTaskName: stringValue(value.producing_task_name),
    producingNodeId: finiteInteger(value.producing_node_id) ?? 0,
    producingNodeName: stringValue(value.producing_node_name),
    catalog,
  });
}

export function mapRecoveryPointEvidence(value: unknown): CatalogProjection<RecoveryPointEvidence> {
  if (!isRawObject(value) || !isRawObject(value.lineage) || !isRawObject(value.manifest) ||
    !isRawObject(value.publication_verification) || !isRawObject(value.restore_drills)) {
    return blocked();
  }
  const pointId = opaqueId(value.recovery_point_id);
  const lineageStatus = evidenceLayerStatus(value.lineage.status);
  const manifestStatus = evidenceLayerStatus(value.manifest.status);
  const publicationStatus = evidenceLayerStatus(value.publication_verification.status);
  const restoreStatus = evidenceLayerStatus(value.restore_drills.status);
  const lineageTrigger = taskRunTrigger(value.lineage.trigger);
  const lineageRunStatus = taskStatus(value.lineage.run_status);
  const completeness = manifestCompleteness(value.manifest.completeness);
  const publicationProvider = providerKind(value.publication_verification.provider);
  const completion = providerCompletion(value.publication_verification.completion);
  const failureCode = publicationFailureCode(value.publication_verification.failure_code);
  if (pointId === null || lineageStatus === null || manifestStatus === null || publicationStatus === null || restoreStatus === null ||
    failureCode === undefined || (lineageStatus === "recorded" && (lineageTrigger === null || lineageRunStatus === null)) ||
    (manifestStatus === "recorded" && completeness === null) ||
    (publicationStatus === "recorded" && (publicationProvider === null || completion === null))) {
    return blocked();
  }
  const restoreItems = Array.isArray(value.restore_drills.items) ? value.restore_drills.items : [];
  const mappedRestoreItems = [];
  for (const item of restoreItems) {
    if (!isRawObject(item)) {
      return blocked();
    }
    const taskRunId = finiteInteger(item.task_run_id, 1);
    const status = restoreDrillStatus(item.status);
    const durationMs = finiteInteger(item.duration_ms);
    if (taskRunId === null || status === null || durationMs === null) {
      return blocked();
    }
    mappedRestoreItems.push({
      taskRunId,
      status,
      failedStep: stringValue(item.failed_step),
      confidenceEligible: booleanValue(item.confidence_eligible),
      startedAt: normalizeNullableCatalogTime(item.started_at),
      finishedAt: normalizeNullableCatalogTime(item.finished_at),
      durationMs,
    });
  }
  return available({
    recoveryPointId: pointId,
    lineage: {
      status: lineageStatus,
      taskId: optionalPositiveInteger(value.lineage.task_id),
      taskRunId: optionalPositiveInteger(value.lineage.task_run_id),
      taskName: stringValue(value.lineage.task_name),
      nodeId: finiteInteger(value.lineage.node_id) ?? 0,
      nodeName: stringValue(value.lineage.node_name),
      trigger: lineageStatus === "recorded" ? lineageTrigger : null,
      runStatus: lineageStatus === "recorded" ? lineageRunStatus : null,
      startedAt: normalizeNullableCatalogTime(value.lineage.started_at),
      finishedAt: normalizeNullableCatalogTime(value.lineage.finished_at),
    },
    manifest: {
      status: manifestStatus,
      id: stringValue(value.manifest.id),
      revision: finiteInteger(value.manifest.revision) ?? 0,
      digestAlgorithm: stringValue(value.manifest.digest_algorithm),
      digest: stringValue(value.manifest.digest),
      entryCount: finiteInteger(value.manifest.entry_count) ?? 0,
      logicalBytes: finiteInteger(value.manifest.logical_bytes) ?? 0,
      generator: stringValue(value.manifest.generator),
      generatorVersion: stringValue(value.manifest.generator_version),
      completeness: manifestStatus === "recorded" ? completeness : null,
      createdAt: normalizeNullableCatalogTime(value.manifest.created_at),
      updatedAt: normalizeNullableCatalogTime(value.manifest.updated_at),
    },
    publicationVerification: {
      status: publicationStatus,
      provider: publicationStatus === "recorded" ? publicationProvider : null,
      completion: publicationStatus === "recorded" ? completion : null,
      failureCode,
      captureStartedAt: normalizeNullableCatalogTime(value.publication_verification.capture_started_at),
      captureFinishedAt: normalizeNullableCatalogTime(value.publication_verification.capture_finished_at),
      filesProcessed: finiteInteger(value.publication_verification.files_processed) ?? 0,
      logicalBytes: finiteInteger(value.publication_verification.logical_bytes) ?? 0,
      commitRecorded: booleanValue(value.publication_verification.commit_recorded),
    },
    restoreDrills: {
      status: restoreStatus,
      items: mappedRestoreItems,
    },
  });
}

function mapRecoveryPointPage(value: unknown): RecoveryPointPage {
  const raw: RawRecoveryPointPage = isRawObject(value) ? value : {};
  return {
    items: Array.isArray(raw.items) ? raw.items.map(mapRecoveryPoint) : [],
    nextCursor: typeof raw.next_cursor === "string" && raw.next_cursor !== "" ? raw.next_cursor : null,
  };
}

function appendQuery(path: string, query: URLSearchParams): string {
  const encoded = query.toString();
  return encoded === "" ? path : `${path}?${encoded}`;
}

export function createRecoveryPointsApi() {
  return {
    async listRecoveryPoints(
      token: string,
      repositoryId: string,
      options: ListRecoveryPointsOptions = {},
    ): Promise<RecoveryPointPage> {
      const query = new URLSearchParams();
      if (options.limit !== undefined) query.set("limit", String(options.limit));
      if (options.cursor) query.set("cursor", options.cursor);
      if (options.sort) query.set("sort", options.sort);
      const raw = await request<unknown>(
        appendQuery(`/backup-repositories/${encodeURIComponent(repositoryId)}/recovery-points`, query),
        { token, signal: options.signal },
      );
      return mapRecoveryPointPage(raw);
    },

    async getRecoveryPoint(
      token: string,
      recoveryPointId: string,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRecoveryPoint>> {
      const raw = await request<unknown>(`/recovery-points/${encodeURIComponent(recoveryPointId)}`, { token, signal });
      return mapRecoveryPoint(raw);
    },

    async getRecoveryPointCatalogStatus(
      token: string,
      recoveryPointId: string,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<CatalogStatus>> {
      const raw = await request<unknown>(`/recovery-points/${encodeURIComponent(recoveryPointId)}/catalog-status`, { token, signal });
      return mapCatalogStatus(raw);
    },

    async getRecoveryPointEvidence(
      token: string,
      recoveryPointId: string,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<RecoveryPointEvidence>> {
      const raw = await request<unknown>(`/recovery-points/${encodeURIComponent(recoveryPointId)}/evidence`, { token, signal });
      return mapRecoveryPointEvidence(raw);
    },
  };
}
