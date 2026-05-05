import { request } from "./core";

export type VersionInfo = {
  version: string;
  build_time: string;
  git_commit: string;
};

export type VersionCheck = {
  update_available: boolean;
  current_version: string;
  latest_version: string;
  release_url: string;
};

export type BackupResult = {
  path: string;
  size: number;
  sha256: string;
};

export type BackupEntry = {
  filename: string;
  size: number;
  created_at: string;
  sha256: string;
};

export function createSystemApi() {
  return {
    async getVersion(options?: { signal?: AbortSignal }): Promise<VersionInfo> {
      return (await request<VersionInfo>("/version", { signal: options?.signal })) ?? { version: "", build_time: "", git_commit: "" };
    },

    async checkVersion(token: string, options?: { signal?: AbortSignal }): Promise<VersionCheck> {
      return (await request<VersionCheck>("/version/check", { token, signal: options?.signal })) ?? { update_available: false, current_version: "", latest_version: "", release_url: "" };
    },

    async backupDB(token: string): Promise<BackupResult> {
      return (await request<BackupResult>("/system/backup-db", { token, method: "POST" })) ?? { path: "", size: 0, sha256: "" };
    },

    async listBackups(token: string, options?: { signal?: AbortSignal }): Promise<BackupEntry[]> {
      return (await request<BackupEntry[]>("/system/backups", { token, signal: options?.signal })) ?? [];
    },
  };
}
