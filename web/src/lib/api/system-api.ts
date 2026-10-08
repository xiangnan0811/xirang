import { request } from "./core";
import { finiteNumber } from "./number-utils";

export type VersionInfo = {
  version: string;
  buildTime: string;
  gitCommit: string;
};

export type VersionCheck = {
  updateAvailable: boolean;
  currentVersion: string;
  latestVersion: string;
  releaseUrl: string;
};

export type BackupResult = {
  filename: string;
  path: string;
  size: number;
  sha256: string;
};

export type BackupEntry = {
  filename: string;
  size: number;
  createdAt: string;
  sha256: string;
};

export const CRON_BACKUP_STATUSES = [
  "not_configured",
  "invalid_configuration",
  "directory_unreadable",
  "no_complete_backup",
  "scan_limit_exceeded",
  "clock_anomaly",
  "stale",
  "fresh",
] as const;

export type CronBackupStatusCode = (typeof CRON_BACKUP_STATUSES)[number];
export type CronBackupEngine = "sqlite" | "postgres";

export type CronBackupStatus = {
  status: CronBackupStatusCode;
  /** Empty only when status is invalid_configuration and the backend dialect is unknown. */
  engine: CronBackupEngine | "";
  checkedAt: string;
  maxAgeSeconds: number;
  directory?: string;
  latestCompleteAt?: string;
  artifactName?: string;
  evidence: "artifact_pair";
  timeSource: "mtime";
  contentVerified: false;
};

const CRON_BACKUP_STATUS_SET = new Set<string>(CRON_BACKUP_STATUSES);
const RFC3339_UTC = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d{1,9})?Z$/;
const INVALID_CRON_BACKUP_STATUS = "invalid cron backup status";

function invalidCronBackupStatus(): never {
  throw new Error(INVALID_CRON_BACKUP_STATUS);
}

function isCronBackupRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function isCronBackupStatusCode(value: unknown): value is CronBackupStatusCode {
  return typeof value === "string" && CRON_BACKUP_STATUS_SET.has(value);
}

function isRfc3339Utc(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const match = RFC3339_UTC.exec(value);
  if (!match) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59) return false;
  const probe = new Date(Date.UTC(year, month - 1, day));
  return probe.getUTCFullYear() === year && probe.getUTCMonth() === month - 1 && probe.getUTCDate() === day;
}

function requiredUtc(value: unknown): string {
  if (!isRfc3339Utc(value)) invalidCronBackupStatus();
  return value;
}

function optionalUtc(row: Record<string, unknown>, key: string): string | undefined {
  if (!Object.hasOwn(row, key)) return undefined;
  const value = row[key];
  if (!isRfc3339Utc(value)) invalidCronBackupStatus();
  return value;
}

function optionalDirectory(row: Record<string, unknown>): string | undefined {
  if (!Object.hasOwn(row, "directory")) return undefined;
  const value = row.directory;
  if (typeof value !== "string" || value.length === 0 || value !== value.trim() || value.includes("\0")) {
    invalidCronBackupStatus();
  }
  return value;
}

function optionalArtifactName(row: Record<string, unknown>): string | undefined {
  if (!Object.hasOwn(row, "artifact_name")) return undefined;
  const value = row.artifact_name;
  if (typeof value !== "string" || value.length === 0 || value !== value.trim()) invalidCronBackupStatus();
  if (value.includes("/") || value.includes("\\") || value.includes("\0") || value === "." || value === "..") {
    invalidCronBackupStatus();
  }
  return value;
}

function requiredMaxAgeSeconds(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) invalidCronBackupStatus();
  return value;
}

export function mapCronBackupStatus(raw: unknown): CronBackupStatus {
  if (!isCronBackupRecord(raw)) invalidCronBackupStatus();
  if (!isCronBackupStatusCode(raw.status)) invalidCronBackupStatus();
  const engine = raw.engine;
  if (engine !== "sqlite" && engine !== "postgres" && engine !== "") invalidCronBackupStatus();
  if (engine === "" && raw.status !== "invalid_configuration") invalidCronBackupStatus();
  if (raw.evidence !== "artifact_pair" || raw.time_source !== "mtime" || raw.content_verified !== false) {
    invalidCronBackupStatus();
  }
  const status: CronBackupStatus = {
    status: raw.status,
    engine,
    checkedAt: requiredUtc(raw.checked_at),
    maxAgeSeconds: requiredMaxAgeSeconds(raw.max_age_seconds),
    evidence: "artifact_pair",
    timeSource: "mtime",
    contentVerified: false,
  };
  const directory = optionalDirectory(raw);
  if (directory !== undefined) status.directory = directory;
  const latestCompleteAt = optionalUtc(raw, "latest_complete_at");
  if (latestCompleteAt !== undefined) status.latestCompleteAt = latestCompleteAt;
  const artifactName = optionalArtifactName(raw);
  if (artifactName !== undefined) status.artifactName = artifactName;
  return status;
}

type RawVersionInfo = {
  version?: unknown;
  build_time?: unknown;
  git_commit?: unknown;
};

type RawVersionCheck = {
  update_available?: unknown;
  current_version?: unknown;
  latest_version?: unknown;
  release_url?: unknown;
};

type RawBackupResult = {
  filename?: unknown;
  path?: unknown;
  size?: unknown;
  sha256?: unknown;
};

type RawBackupEntry = {
  filename?: unknown;
  size?: unknown;
  created_at?: unknown;
  sha256?: unknown;
};

export function mapVersionInfo(row: RawVersionInfo | null | undefined): VersionInfo {
  return {
    version: String(row?.version ?? ""),
    buildTime: String(row?.build_time ?? ""),
    gitCommit: String(row?.git_commit ?? ""),
  };
}

export function mapVersionCheck(row: RawVersionCheck | null | undefined): VersionCheck {
  return {
    updateAvailable: Boolean(row?.update_available),
    currentVersion: String(row?.current_version ?? ""),
    latestVersion: String(row?.latest_version ?? ""),
    releaseUrl: String(row?.release_url ?? ""),
  };
}

export function mapBackupResult(row: RawBackupResult | null | undefined): BackupResult {
  return {
    filename: String(row?.filename ?? ""),
    path: String(row?.path ?? ""),
    size: finiteNumber(row?.size),
    sha256: String(row?.sha256 ?? ""),
  };
}

export function mapBackupEntry(row: RawBackupEntry | null | undefined): BackupEntry {
  return {
    filename: String(row?.filename ?? ""),
    size: finiteNumber(row?.size),
    createdAt: String(row?.created_at ?? ""),
    sha256: String(row?.sha256 ?? ""),
  };
}

export function createSystemApi() {
  return {
    async getVersion(options?: { signal?: AbortSignal }): Promise<VersionInfo> {
      const raw = await request<RawVersionInfo>("/version", { signal: options?.signal });
      return mapVersionInfo(raw);
    },

    async checkVersion(token: string, options?: { signal?: AbortSignal }): Promise<VersionCheck> {
      const raw = await request<RawVersionCheck>("/version/check", { token, signal: options?.signal });
      return mapVersionCheck(raw);
    },

    async backupDB(token: string, options?: { signal?: AbortSignal }): Promise<BackupResult> {
      const raw = await request<RawBackupResult>("/system/backup-db", {
        token,
        method: "POST",
        signal: options?.signal,
      });
      return mapBackupResult(raw);
    },

    async listBackups(token: string, options?: { signal?: AbortSignal }): Promise<BackupEntry[]> {
      const raw = await request<RawBackupEntry[]>("/system/backups", { token, signal: options?.signal });
      return Array.isArray(raw) ? raw.map(mapBackupEntry) : [];
    },

    async getCronBackupStatus(token: string, options?: { signal?: AbortSignal }): Promise<CronBackupStatus> {
      const raw = await request<unknown>("/system/cron-backup-status", { token, signal: options?.signal });
      return mapCronBackupStatus(raw);
    },
  };
}
