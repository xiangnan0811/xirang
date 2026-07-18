import type {
  BackupImmutabilityLevel,
  BackupPublicationMode,
  BackupProviderKind,
  BackupRepository,
  BackupRepositoryCatalogSummary,
  BackupRepositoryLineage,
  BackupRepositoryLineageSource,
  BackupRepositoryPage,
  BackupRepositoryStatus,
  BackupVersionMode,
  CatalogCoverageStatus,
  CatalogProjection,
  RecoveryPointSemantics,
  RecoveryPointState,
} from "@/types/domain";
import { request } from "./core";
import {
  mapCatalogCapabilities,
  mapCatalogContentAvailability,
  mapCatalogPermissions,
  normalizeCatalogTime,
  normalizeNullableCatalogTime,
} from "./recovery-points-api";

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

function finiteInteger(value: unknown, minimum = 0): number | null {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= minimum ? parsed : null;
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
  };
}
