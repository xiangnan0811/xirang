import type {
  BackupImportCandidate,
  BackupImportCandidatePage,
  BackupImportDiscoveryResult,
  BackupImmutabilityLevel,
  BackupPublicationMode,
  BackupProviderKind,
  BackupRebuildReason,
  BackupRebuildResult,
  BackupRepository,
  BackupRepositoryCatalogSummary,
  BackupRepositoryLineage,
  BackupRepositoryLineageSource,
  BackupRepositoryMutationResult,
  BackupRepositoryMutationSnapshot,
  BackupRepositoryPage,
  BackupRepositoryStatus,
  BackupVersionMode,
  CatalogCoverageStatus,
  CatalogProjection,
  ImportCandidateKind,
  ImportReviewDecision,
  ImportReviewState,
  RecoveryPointSemantics,
  RecoveryPointState,
} from "@/types/domain";
import { request } from "./core";
import {
  mapCatalogCapabilities,
  mapCatalogContentAvailability,
  mapCatalogPermissions,
  mapRecoveryPointSnapshot,
  normalizeCatalogTime,
  normalizeNullableCatalogTime,
} from "./recovery-points-api";
import { finiteInteger } from "./lifecycle-integers";

type RawObject = Record<string, unknown>;

type RawBackupRepositoryPage = {
  items?: unknown;
  next_cursor?: unknown;
};

export interface ListBackupRepositoriesOptions {
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}

function isRawObject(value: unknown): value is RawObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function blocked<T>(): CatalogProjection<T> {
  return {
    status: "blocked",
    reason: { code: "unknown_internal_state", params: {} },
  };
}

function optionalPositiveInteger(value: unknown): number | undefined {
  const parsed = finiteInteger(value, 1);
  return parsed === null ? undefined : parsed;
}

function providerKind(value: unknown): BackupProviderKind | null {
  switch (value) {
    case "restic":
    case "rsync":
    case "rclone":
    case "command":
      return value;
    default:
      return null;
  }
}

function versionMode(value: unknown): BackupVersionMode | null {
  switch (value) {
    case "native_snapshot":
    case "hardlink_tree":
    case "full_copy_tree":
    case "versioned_prefix":
    case "native_object_versions":
    case "mutable_head":
      return value;
    default:
      return null;
  }
}

function repositoryStatus(value: unknown): BackupRepositoryStatus | null {
  switch (value) {
    case "connecting":
    case "online":
    case "degraded":
    case "offline":
    case "disconnected":
    case "purging":
    case "purge_blocked":
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

function coverageStatus(value: unknown): CatalogCoverageStatus | null {
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

function lineageSource(value: unknown): BackupRepositoryLineageSource | null {
  switch (value) {
    case "task_link":
    case "recovery_point":
      return value;
    default:
      return null;
  }
}

function publicationMode(value: unknown): BackupPublicationMode | null {
  switch (value) {
    case "legacy_mutable":
    case "versioned_hardlink":
    case "versioned_full_copy":
    case "versioned_prefix":
    case "native_object_versions":
    case "native_snapshot":
      return value;
    default:
      return null;
  }
}

function pointState(value: unknown): RecoveryPointState | null {
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

function pointSemantics(value: unknown): RecoveryPointSemantics | null {
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

function mapLineages(value: unknown): BackupRepositoryLineage[] | null {
  if (!Array.isArray(value)) {
    return [];
  }
  const result: BackupRepositoryLineage[] = [];
  for (const item of value) {
    if (!isRawObject(item)) {
      return null;
    }
    const source = lineageSource(item.source);
    const mode = item.publication_mode === undefined || item.publication_mode === ""
      ? undefined
      : publicationMode(item.publication_mode) ?? undefined;
    const recoveryState = item.recovery_point_state === undefined || item.recovery_point_state === ""
      ? undefined
      : pointState(item.recovery_point_state) ?? undefined;
    const semantics = item.point_semantics === undefined || item.point_semantics === ""
      ? undefined
      : pointSemantics(item.point_semantics) ?? undefined;
    if (source === null ||
      (item.publication_mode !== undefined && item.publication_mode !== "" && mode === undefined) ||
      (item.recovery_point_state !== undefined && item.recovery_point_state !== "" && recoveryState === undefined) ||
      (item.point_semantics !== undefined && item.point_semantics !== "" && semantics === undefined)) {
      return null;
    }
    result.push({
      source,
      taskRepositoryLinkId: opaqueId(item.task_repository_link_id) ?? undefined,
      taskId: optionalPositiveInteger(item.task_id),
      taskName: typeof item.task_name === "string" ? item.task_name : "",
      nodeId: finiteInteger(item.node_id) ?? 0,
      nodeName: typeof item.node_name === "string" ? item.node_name : "",
      publicationMode: mode,
      recoveryPointId: typeof item.recovery_point_id === "string" && item.recovery_point_id !== ""
        ? item.recovery_point_id
        : undefined,
      recoveryPointState: recoveryState,
      pointSemantics: semantics,
      active: item.active === true,
    });
  }
  return result;
}

function mapCatalogSummary(value: unknown): BackupRepositoryCatalogSummary | null {
  if (!isRawObject(value)) {
    return null;
  }
  const recoveryPointCount = finiteInteger(value.recovery_point_count);
  const completeCatalogCount = finiteInteger(value.complete_catalog_count);
  const coverage = coverageStatus(value.coverage);
  const contentAvailability = mapCatalogContentAvailability(value.content_availability);
  if (recoveryPointCount === null || completeCatalogCount === null || completeCatalogCount > recoveryPointCount ||
    coverage === null || contentAvailability === null) {
    return null;
  }
  return {
    recoveryPointCount,
    completeCatalogCount,
    coverage,
    contentAvailability,
    permissions: mapCatalogPermissions(value.permissions),
  };
}

function opaqueId(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value) ? value : null;
}

function importCandidateKind(value: unknown): ImportCandidateKind | null {
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

function importReviewState(value: unknown): ImportReviewState | null {
  switch (value) {
    case "pending":
    case "accepted":
    case "rejected":
      return value;
    default:
      return null;
  }
}

function rebuildReason(value: unknown): BackupRebuildReason | null {
  switch (value) {
    case "invalid_manifest":
    case "catalog_start_failed":
    case "derived_queue_failed":
      return value;
    default:
      return null;
  }
}

export function mapBackupImportCandidate(value: unknown): CatalogProjection<BackupImportCandidate> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = opaqueId(value.id);
  const repositoryId = opaqueId(value.repository_id);
  const kind = importCandidateKind(value.kind);
  const state = importReviewState(value.state);
  const createdAt = normalizeCatalogTime(value.created_at);
  const acceptedRecoveryPointId = value.accepted_recovery_point_id === undefined || value.accepted_recovery_point_id === ""
    ? undefined
    : opaqueId(value.accepted_recovery_point_id) ?? undefined;
  const reviewedAtPresent = value.reviewed_at !== undefined && value.reviewed_at !== null && value.reviewed_at !== "";
  const reviewedAt = reviewedAtPresent ? normalizeCatalogTime(value.reviewed_at) : null;
  const quarantinedPresent = value.quarantined !== undefined && value.quarantined !== null;
  if (id === null || repositoryId === null || kind === null || state === null || createdAt === null ||
    (reviewedAtPresent && reviewedAt === null) ||
    (quarantinedPresent && typeof value.quarantined !== "boolean") ||
    (state === "accepted" && acceptedRecoveryPointId === undefined) ||
    (state !== "accepted" && acceptedRecoveryPointId !== undefined) ||
    (value.accepted_recovery_point_id !== undefined && value.accepted_recovery_point_id !== "" && acceptedRecoveryPointId === undefined)) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      id,
      repositoryId,
      kind,
      state,
      acceptedRecoveryPointId,
      quarantined: value.quarantined === true,
      createdAt,
      reviewedAt,
    },
  };
}

export function mapBackupImportDiscoveryResult(value: unknown): CatalogProjection<BackupImportDiscoveryResult> {
  if (!isRawObject(value) || !Array.isArray(value.candidates)) {
    return blocked();
  }
  const discovered = finiteInteger(value.discovered);
  const existing = finiteInteger(value.existing);
  if (discovered === null || existing === null) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      candidates: value.candidates.map(mapBackupImportCandidate),
      nextCursor: typeof value.next_cursor === "string" && value.next_cursor !== "" ? value.next_cursor : null,
      discovered,
      existing,
    },
  };
}

export function mapBackupRebuildResult(value: unknown): CatalogProjection<BackupRebuildResult> {
  if (!isRawObject(value) || !isRawObject(value.reasons)) {
    return blocked();
  }
  const accepted = finiteInteger(value.accepted);
  const catalogStarted = finiteInteger(value.catalog_started);
  const derivedQueued = finiteInteger(value.derived_queued);
  const partial = finiteInteger(value.partial);
  const failed = finiteInteger(value.failed);
  if (accepted === null || catalogStarted === null || derivedQueued === null || partial === null || failed === null) {
    return blocked();
  }
  const reasons: Partial<Record<BackupRebuildReason, number>> = {};
  for (const [key, rawCount] of Object.entries(value.reasons)) {
    const reason = rebuildReason(key);
    const count = finiteInteger(rawCount);
    if (reason === null || count === null) {
      return blocked();
    }
    reasons[reason] = count;
  }
  return {
    status: "available",
    value: {
      accepted,
      catalogStarted,
      derivedQueued,
      partial,
      failed,
      reasons,
      nextCursor: typeof value.next_cursor === "string" && value.next_cursor !== "" ? value.next_cursor : null,
    },
  };
}

function mapBackupRepositoryMutationSnapshot(value: unknown): CatalogProjection<BackupRepositoryMutationSnapshot> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = opaqueId(value.id);
  const provider = providerKind(value.provider_kind);
  const mode = versionMode(value.version_mode);
  const status = repositoryStatus(value.status);
  const immutability = immutabilityLevel(value.immutability_level);
  const capabilityRevision = finiteInteger(value.capability_revision, 1);
  const createdAt = normalizeCatalogTime(value.created_at);
  const updatedAt = normalizeCatalogTime(value.updated_at);
  if (id === null || provider === null || mode === null || status === null || immutability === null ||
    capabilityRevision === null || createdAt === null || updatedAt === null) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      id,
      providerKind: provider,
      displayName: typeof value.display_name === "string" ? value.display_name : "",
      description: typeof value.description === "string" ? value.description : "",
      versionMode: mode,
      status,
      capabilityRevision,
      capabilities: mapCatalogCapabilities(value.capabilities),
      immutabilityLevel: immutability,
      lastSeenAt: normalizeNullableCatalogTime(value.last_seen_at),
      lastReconciledAt: normalizeNullableCatalogTime(value.last_reconciled_at),
      createdAt,
      updatedAt,
    },
  };
}

export function mapBackupRepositoryMutationResult(value: unknown): CatalogProjection<BackupRepositoryMutationResult> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const hasPascalRepository = Object.prototype.hasOwnProperty.call(value, "Repository");
  const hasSnakeRepository = Object.prototype.hasOwnProperty.call(value, "repository");
  if (hasPascalRepository === hasSnakeRepository) {
    return blocked();
  }
  const usesPascalEnvelope = hasPascalRepository;
  const mutablePointKey = usesPascalEnvelope ? "MutablePoint" : "mutable_point";
  const mixedMutablePointKey = usesPascalEnvelope ? "mutable_point" : "MutablePoint";
  if (Object.prototype.hasOwnProperty.call(value, mixedMutablePointKey)) {
    return blocked();
  }
  const repository = value[usesPascalEnvelope ? "Repository" : "repository"];
  const mapped = mapBackupRepositoryMutationSnapshot(repository);
  if (mapped.status !== "available") {
    return mapped;
  }
  const mutablePoint = value[mutablePointKey];
  const mappedMutablePoint = mutablePoint === undefined || mutablePoint === null
    ? null
    : mapRecoveryPointSnapshot(mutablePoint);
  if (mappedMutablePoint !== null && mappedMutablePoint.status !== "available") {
    return blocked();
  }
  return {
    status: "available",
    value: {
      repository: mapped.value,
      mutablePoint: mappedMutablePoint === null ? null : mappedMutablePoint.value,
    },
  };
}

export function mapBackupRepository(value: unknown): CatalogProjection<BackupRepository> {
  if (!isRawObject(value)) {
    return blocked();
  }
  const id = typeof value.id === "string" && /^[0-9a-f]{32}$/.test(value.id) ? value.id : null;
  const provider = providerKind(value.provider_kind);
  const mode = versionMode(value.version_mode);
  const status = repositoryStatus(value.status);
  const immutability = immutabilityLevel(value.immutability_level);
  const capabilityRevision = finiteInteger(value.capability_revision, 1);
  const createdAt = normalizeCatalogTime(value.created_at);
  const updatedAt = normalizeCatalogTime(value.updated_at);
  const lineages = mapLineages(value.lineages);
  const catalog = mapCatalogSummary(value.catalog);
  if (id === null || provider === null || mode === null || status === null || immutability === null ||
    capabilityRevision === null || createdAt === null || updatedAt === null || lineages === null || catalog === null) {
    return blocked();
  }
  return {
    status: "available",
    value: {
      id,
      providerKind: provider,
      displayName: typeof value.display_name === "string" ? value.display_name : "",
      description: typeof value.description === "string" ? value.description : "",
      versionMode: mode,
      status,
      capabilityRevision,
      capabilities: mapCatalogCapabilities(value.capabilities),
      immutabilityLevel: immutability,
      lastSeenAt: normalizeNullableCatalogTime(value.last_seen_at),
      lastReconciledAt: normalizeNullableCatalogTime(value.last_reconciled_at),
      createdAt,
      updatedAt,
      accessActive: value.access_active === true,
      lineages,
      catalog,
    },
  };
}

function mapBackupRepositoryPage(value: unknown): BackupRepositoryPage {
  const raw: RawBackupRepositoryPage = isRawObject(value) ? value : {};
  return {
    items: Array.isArray(raw.items) ? raw.items.map(mapBackupRepository) : [],
    nextCursor: typeof raw.next_cursor === "string" && raw.next_cursor !== "" ? raw.next_cursor : null,
  };
}

function appendQuery(path: string, query: URLSearchParams): string {
  const encoded = query.toString();
  return encoded === "" ? path : `${path}?${encoded}`;
}

export function createBackupRepositoriesApi() {
  return {
    async listBackupRepositories(
      token: string,
      options: ListBackupRepositoriesOptions = {},
    ): Promise<BackupRepositoryPage> {
      const query = new URLSearchParams();
      if (options.limit !== undefined) query.set("limit", String(options.limit));
      if (options.cursor) query.set("cursor", options.cursor);
      const raw = await request<unknown>(appendQuery("/backup-repositories", query), {
        token,
        signal: options.signal,
      });
      return mapBackupRepositoryPage(raw);
    },

    async getBackupRepository(
      token: string,
      repositoryId: string,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRepository>> {
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}`, { token, signal });
      return mapBackupRepository(raw);
    },

    async connectBackupRepository(
      token: string,
      input: {
        taskId: number;
        repositoryId?: string;
        displayName?: string;
        description?: string;
        replaceAccess?: boolean;
      },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRepositoryMutationResult>> {
      const raw = await request<unknown>("/backup-repositories/connect", {
        method: "POST",
        token,
        signal,
        body: {
          task_id: input.taskId,
          ...(input.repositoryId ? { repository_id: input.repositoryId } : {}),
          ...(input.displayName ? { display_name: input.displayName } : {}),
          ...(input.description ? { description: input.description } : {}),
          ...(input.replaceAccess ? { replace_access: true } : {}),
        },
      });
      return mapBackupRepositoryMutationResult(raw);
    },

    async reconcileBackupRepository(
      token: string,
      repositoryId: string,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRepositoryMutationResult>> {
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/reconcile`, {
        method: "POST",
        token,
        signal,
      });
      return mapBackupRepositoryMutationResult(raw);
    },

    async disconnectBackupRepository(
      token: string,
      repositoryId: string,
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupRepositoryMutationResult>> {
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/disconnect`, {
        method: "POST",
        token,
        signal,
      });
      return mapBackupRepositoryMutationResult(raw);
    },

    async scanBackupRepositoryImports(
      token: string,
      repositoryId: string,
      options: { limit?: number; cursor?: string; signal?: AbortSignal } = {},
    ): Promise<CatalogProjection<BackupImportDiscoveryResult>> {
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/import-scans`, {
        method: "POST",
        token,
        signal: options.signal,
        body: {
          ...(options.limit !== undefined ? { limit: options.limit } : {}),
          ...(options.cursor ? { cursor: options.cursor } : {}),
        },
      });
      return mapBackupImportDiscoveryResult(raw);
    },

    async listBackupRepositoryImportCandidates(
      token: string,
      repositoryId: string,
      options: { limit?: number; cursor?: string; signal?: AbortSignal } = {},
    ): Promise<BackupImportCandidatePage> {
      const query = new URLSearchParams();
      if (options.limit !== undefined) query.set("limit", String(options.limit));
      if (options.cursor) query.set("cursor", options.cursor);
      const raw = await request<unknown>(
        appendQuery(`/backup-repositories/${encodeURIComponent(repositoryId)}/import-candidates`, query),
        { token, signal: options.signal },
      );
      const page = isRawObject(raw) ? raw : {};
      return {
        items: Array.isArray(page.items) ? page.items.map(mapBackupImportCandidate) : [],
        nextCursor: typeof page.next_cursor === "string" && page.next_cursor !== "" ? page.next_cursor : null,
      };
    },

    async reviewBackupRepositoryImportCandidate(
      token: string,
      repositoryId: string,
      candidateId: string,
      input: { decision: ImportReviewDecision; acceptAs?: ImportCandidateKind },
      signal?: AbortSignal,
    ): Promise<CatalogProjection<BackupImportCandidate>> {
      const raw = await request<unknown>(
        `/backup-repositories/${encodeURIComponent(repositoryId)}/import-candidates/${encodeURIComponent(candidateId)}/reviews`,
        {
          method: "POST",
          token,
          signal,
          body: {
            decision: input.decision,
            ...(input.acceptAs ? { accept_as: input.acceptAs } : {}),
          },
        },
      );
      return mapBackupImportCandidate(raw);
    },

    async rebuildBackupRepositoryImports(
      token: string,
      repositoryId: string,
      options: { limit?: number; cursor?: string; signal?: AbortSignal } = {},
    ): Promise<CatalogProjection<BackupRebuildResult>> {
      const raw = await request<unknown>(`/backup-repositories/${encodeURIComponent(repositoryId)}/rebuilds`, {
        method: "POST",
        token,
        signal: options.signal,
        body: {
          ...(options.limit !== undefined ? { limit: options.limit } : {}),
          ...(options.cursor ? { cursor: options.cursor } : {}),
        },
      });
      return mapBackupRebuildResult(raw);
    },
  };
}
