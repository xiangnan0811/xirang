import type { AssetRef, CatalogProjection } from "@/types/domain";

export type RawBackupAssetObject = Record<string, unknown>;

export function isRawBackupAssetObject(value: unknown): value is RawBackupAssetObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function blockedBackupAssetProjection<T>(): CatalogProjection<T> {
  return {
    status: "blocked",
    reason: { code: "unknown_internal_state", params: {} },
  };
}

export function mapOpaqueBackupAssetId(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value) ? value : null;
}

export function mapBackupAssetEntryId(value: unknown): string | null {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value) ? value : null;
}

export function mapAssetRef(value: unknown): AssetRef | null {
  if (!isRawBackupAssetObject(value)) return null;
  const recoveryPointId = mapOpaqueBackupAssetId(value.recovery_point_id);
  const entryId = mapBackupAssetEntryId(value.entry_id);
  return recoveryPointId === null || entryId === null ? null : { recoveryPointId, entryId };
}

export function mapSafeNonNegativeInteger(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : null;
}

export function mapPositiveSafeInteger(value: unknown): number | null {
  const mapped = mapSafeNonNegativeInteger(value);
  return mapped !== null && mapped > 0 ? mapped : null;
}

export function mapUTCInstant(value: unknown): string | null {
  if (typeof value !== "string" || value === "") return null;
  const milliseconds = Date.parse(value);
  return Number.isFinite(milliseconds) ? new Date(milliseconds).toISOString() : null;
}
