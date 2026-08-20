import { request } from "./core";
import { finiteInteger } from "./lifecycle-integers";

export type BackupGaInstallationClass = "fresh" | "existing";
export type BackupGaReadinessStatus = "unknown" | "blocked" | "ready" | "acknowledged";
export type BackupGaConflictKind =
  | "shared_restic_identity"
  | "task_repository_mismatch"
  | "capability_gap"
  | "command_unsupported";

export interface BackupGaCounts {
  candidates: number;
  conflicts: number;
  unsupported: number;
  capabilityGaps: number;
}

export interface BackupGaConflict {
  kind: BackupGaConflictKind;
  taskIds: number[];
  repositoryId: string;
  stableReasonCode: string;
}

export interface BackupGaReadiness {
  schemaVersion: 1;
  class: BackupGaInstallationClass;
  status: BackupGaReadinessStatus;
  inventoryComplete: boolean;
  inventoryDigest: string;
  acknowledgedDigest: string;
  exportRootValid: boolean;
  keyDomainsReady: boolean;
  workerOptional: boolean;
  counts: BackupGaCounts;
  conflicts: BackupGaConflict[];
}

export type BackupGaInventory = BackupGaReadiness;

type RawObject = Record<string, unknown>;

function isRawObject(value: unknown): value is RawObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function installationClass(value: unknown): BackupGaInstallationClass | null {
  return value === "fresh" || value === "existing" ? value : null;
}

function readinessStatus(value: unknown): BackupGaReadinessStatus | null {
  return value === "unknown" || value === "blocked" || value === "ready" || value === "acknowledged"
    ? value
    : null;
}

function conflictKind(value: unknown): BackupGaConflictKind | null {
  switch (value) {
    case "shared_restic_identity":
    case "task_repository_mismatch":
    case "capability_gap":
    case "command_unsupported":
      return value;
    default:
      return null;
  }
}

function opaqueRepositoryId(value: unknown): string {
  return typeof value === "string" && /^[0-9a-f]{32}$/.test(value) ? value : "";
}

function inventoryDigest(value: unknown): string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value) ? value : "";
}

function mapCounts(value: unknown): BackupGaCounts {
  const raw = isRawObject(value) ? value : {};
  return {
    candidates: Math.max(0, finiteInteger(raw.candidates, 0) ?? 0),
    conflicts: Math.max(0, finiteInteger(raw.conflicts, 0) ?? 0),
    unsupported: Math.max(0, finiteInteger(raw.unsupported, 0) ?? 0),
    capabilityGaps: Math.max(0, finiteInteger(raw.capability_gaps, 0) ?? 0),
  };
}

function mapConflict(value: unknown): BackupGaConflict | null {
  if (!isRawObject(value)) {
    return null;
  }
  const kind = conflictKind(value.kind);
  if (kind === null) {
    return null;
  }
  const taskIds = Array.isArray(value.task_ids)
    ? value.task_ids.flatMap((item) => {
        const parsed = finiteInteger(item, 1);
        return parsed === null ? [] : [parsed];
      })
    : [];
  return {
    kind,
    taskIds,
    repositoryId: opaqueRepositoryId(value.repository_id),
    stableReasonCode: typeof value.stable_reason_code === "string" ? value.stable_reason_code : "",
  };
}

export function mapBackupGaReadiness(raw: unknown): BackupGaReadiness {
  if (!isRawObject(raw) || raw.schema_version !== 1) {
    throw new Error("backup GA readiness payload is invalid");
  }
  const installation = installationClass(raw.class);
  const status = readinessStatus(raw.status);
  if (installation === null || status === null) {
    throw new Error("backup GA readiness payload is invalid");
  }
  return {
    schemaVersion: 1,
    class: installation,
    status,
    inventoryComplete: raw.inventory_complete === true,
    inventoryDigest: inventoryDigest(raw.inventory_digest),
    acknowledgedDigest: inventoryDigest(raw.acknowledged_digest),
    exportRootValid: raw.export_root_valid === true,
    keyDomainsReady: raw.key_domains_ready === true,
    workerOptional: raw.worker_optional === true,
    counts: mapCounts(raw.counts),
    conflicts: Array.isArray(raw.conflicts)
      ? raw.conflicts.flatMap((item) => {
          const mapped = mapConflict(item);
          return mapped === null ? [] : [mapped];
        })
      : [],
  };
}

export function mapBackupGaInventory(raw: unknown): BackupGaInventory {
  return mapBackupGaReadiness(raw);
}

export function createBackupGaApi() {
  return {
    async getReadiness(token: string, signal?: AbortSignal): Promise<BackupGaReadiness> {
      return mapBackupGaReadiness(await request("/settings/backup-assets/ga/readiness", { token, signal }));
    },
    async runInventory(token: string, signal?: AbortSignal): Promise<BackupGaInventory> {
      return mapBackupGaInventory(
        await request("/settings/backup-assets/ga/inventory", { method: "POST", token, signal }),
      );
    },
    async acknowledge(token: string, digest: string, signal?: AbortSignal): Promise<BackupGaReadiness> {
      return mapBackupGaReadiness(
        await request("/settings/backup-assets/ga/acknowledge", {
          method: "POST",
          token,
          body: { digest },
          signal,
        }),
      );
    },
    async enable(token: string, signal?: AbortSignal): Promise<void> {
      await request("/settings", {
        method: "PUT",
        token,
        body: { "backup_assets.enabled": "true" },
        signal,
      });
    },
  };
}

export type BackupGaApi = ReturnType<typeof createBackupGaApi>;
