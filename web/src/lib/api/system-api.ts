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

    async backupDB(token: string): Promise<BackupResult> {
      const raw = await request<RawBackupResult>("/system/backup-db", { token, method: "POST" });
      return mapBackupResult(raw);
    },

    async listBackups(token: string, options?: { signal?: AbortSignal }): Promise<BackupEntry[]> {
      const raw = await request<RawBackupEntry[]>("/system/backups", { token, signal: options?.signal });
      return Array.isArray(raw) ? raw.map(mapBackupEntry) : [];
    },
  };
}
