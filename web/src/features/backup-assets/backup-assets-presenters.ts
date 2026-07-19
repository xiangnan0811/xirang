import type {
  BackupContentClassification,
  BackupRepository,
  CatalogCapabilityCode,
} from "@/types/domain";

export type BackupAssetsResultSurface = "empty" | "partial" | "loading" | "rows" | "rows_offline" | "unavailable";

export interface BackupAssetsResultSurfaceInput {
  coverage: "complete" | "partial" | "building" | "failed" | "unavailable";
  count: number;
  authoritativeEmpty: boolean;
  offline: boolean;
}

export function getBackupAssetsResultSurface(input: BackupAssetsResultSurfaceInput): BackupAssetsResultSurface {
  if (input.coverage === "building") return "loading";
  if (input.coverage === "partial") return "partial";
  if (input.count > 0) return input.offline ? "rows_offline" : "rows";
  if (input.coverage === "complete" && input.authoritativeEmpty) return "empty";
  return "unavailable";
}

export type PreviewAccess =
  | "allowed"
  | "step_up_required"
  | "blocked_unnecessary_proof"
  | "blocked_unknown";

export function getPreviewAccess(classification: unknown, proofPresent: boolean): PreviewAccess {
  if (classification === "non_secret") return proofPresent ? "blocked_unnecessary_proof" : "allowed";
  if (classification === "secret" || classification === "unknown") {
    return proofPresent ? "allowed" : "step_up_required";
  }
  return "blocked_unknown";
}

export type BackupAssetsCodeKind = "provider" | "recoveryPoint" | "coverage" | "capability";
export interface BackupAssetsCodePresentation {
  translationKey: string;
  tone: "neutral" | "success" | "warning" | "critical";
}

const capabilityCodeMap: Record<CatalogCapabilityCode, BackupAssetsCodePresentation> = {
  feature_disabled: { translationKey: "backupAssets.codes.capability.featureDisabled", tone: "warning" },
  task_artifact_contract_missing: {
    translationKey: "backupAssets.codes.capability.taskArtifactContractMissing",
    tone: "warning",
  },
  repository_offline: { translationKey: "backupAssets.codes.capability.repositoryOffline", tone: "warning" },
  repository_disconnected: {
    translationKey: "backupAssets.codes.capability.repositoryDisconnected",
    tone: "warning",
  },
  provider_unavailable: { translationKey: "backupAssets.codes.capability.providerUnavailable", tone: "warning" },
  repository_identity_unavailable: {
    translationKey: "backupAssets.codes.capability.repositoryIdentityUnavailable",
    tone: "critical",
  },
  provider_protocol_incompatible: {
    translationKey: "backupAssets.codes.capability.providerProtocolIncompatible",
    tone: "critical",
  },
  provider_operation_timeout: {
    translationKey: "backupAssets.codes.capability.providerOperationTimeout",
    tone: "warning",
  },
  provider_resource_limit: {
    translationKey: "backupAssets.codes.capability.providerResourceLimit",
    tone: "warning",
  },
  point_not_committed: { translationKey: "backupAssets.codes.capability.pointNotCommitted", tone: "warning" },
  mutable_source_changed: {
    translationKey: "backupAssets.codes.capability.mutableSourceChanged",
    tone: "critical",
  },
  catalog_unavailable: { translationKey: "backupAssets.codes.capability.catalogUnavailable", tone: "warning" },
  sequential_read_unavailable: {
    translationKey: "backupAssets.codes.capability.sequentialReadUnavailable",
    tone: "warning",
  },
  range_unavailable: { translationKey: "backupAssets.codes.capability.rangeUnavailable", tone: "warning" },
  download_unavailable: { translationKey: "backupAssets.codes.capability.downloadUnavailable", tone: "warning" },
  restore_unavailable: { translationKey: "backupAssets.codes.capability.restoreUnavailable", tone: "warning" },
  diff_unavailable: { translationKey: "backupAssets.codes.capability.diffUnavailable", tone: "warning" },
  unknown_internal_state: {
    translationKey: "backupAssets.codes.capability.unknownInternalState",
    tone: "critical",
  },
};

const codeMaps: Record<BackupAssetsCodeKind, Record<string, BackupAssetsCodePresentation>> = {
  provider: {
    restic: { translationKey: "backupAssets.codes.provider.restic", tone: "neutral" },
    rsync: { translationKey: "backupAssets.codes.provider.rsync", tone: "neutral" },
    rclone: { translationKey: "backupAssets.codes.provider.rclone", tone: "neutral" },
    command: { translationKey: "backupAssets.codes.provider.command", tone: "warning" },
    verified_import: { translationKey: "backupAssets.codes.provider.verifiedImport", tone: "neutral" },
  },
  recoveryPoint: {
    committed: { translationKey: "backupAssets.codes.recoveryPoint.committed", tone: "success" },
    degraded: { translationKey: "backupAssets.codes.recoveryPoint.degraded", tone: "warning" },
    expired: { translationKey: "backupAssets.codes.recoveryPoint.expired", tone: "critical" },
    failed: { translationKey: "backupAssets.codes.recoveryPoint.failed", tone: "critical" },
  },
  coverage: {
    complete: { translationKey: "backupAssets.codes.coverage.complete", tone: "success" },
    partial: { translationKey: "backupAssets.codes.coverage.partial", tone: "warning" },
    building: { translationKey: "backupAssets.codes.coverage.building", tone: "neutral" },
    failed: { translationKey: "backupAssets.codes.coverage.failed", tone: "critical" },
    unavailable: { translationKey: "backupAssets.codes.coverage.unavailable", tone: "warning" },
  },
  capability: capabilityCodeMap,
};

export function presentBackupAssetsCode(
  kind: BackupAssetsCodeKind,
  code: unknown
): BackupAssetsCodePresentation {
  if (typeof code === "string") {
    const known = codeMaps[kind][code];
    if (known !== undefined) return known;
  }
  return { translationKey: "backupAssets.codes.unknown", tone: "warning" };
}

export function isKnownContentClassification(value: unknown): value is BackupContentClassification {
  return value === "non_secret" || value === "secret" || value === "unknown";
}

export function backupAssetsProviderKey(provider: BackupRepository["providerKind"]): string {
  return provider === "verified_import"
    ? "backupAssets.codes.provider.verifiedImport"
    : `backupAssets.codes.provider.${provider}`;
}

export function backupAssetsVersionModeKey(mode: BackupRepository["versionMode"]): string {
  const keys: Record<BackupRepository["versionMode"], string> = {
    native_snapshot: "backupAssets.codes.versionMode.nativeSnapshot",
    hardlink_tree: "backupAssets.codes.versionMode.hardlinkTree",
    full_copy_tree: "backupAssets.codes.versionMode.fullCopyTree",
    versioned_prefix: "backupAssets.codes.versionMode.versionedPrefix",
    native_object_versions: "backupAssets.codes.versionMode.nativeObjectVersions",
    mutable_head: "backupAssets.codes.versionMode.mutableHead",
  };
  return keys[mode];
}

export function backupAssetsImmutabilityKey(level: BackupRepository["immutabilityLevel"]): string {
  const keys: Record<BackupRepository["immutabilityLevel"], string> = {
    mutable: "backupAssets.codes.immutability.mutable",
    xirang_managed: "backupAssets.codes.immutability.xirangManaged",
    backend_versioned: "backupAssets.codes.immutability.backendVersioned",
    storage_worm: "backupAssets.codes.immutability.storageWorm",
  };
  return keys[level];
}
