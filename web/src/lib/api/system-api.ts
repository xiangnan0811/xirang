import { request, type Envelope, unwrapData } from "./core";

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
    async getVersion(signal?: AbortSignal): Promise<VersionInfo> {
      const payload = await request<Envelope<VersionInfo>>("/version", { signal });
      return unwrapData(payload) ?? { version: "", build_time: "", git_commit: "" };
    },

    async checkVersion(token: string, signal?: AbortSignal): Promise<VersionCheck> {
      const payload = await request<Envelope<VersionCheck>>("/version/check", { token, signal });
      return unwrapData(payload) ?? { update_available: false, current_version: "", latest_version: "", release_url: "" };
    },

    async backupDB(token: string): Promise<BackupResult> {
      const payload = await request<Envelope<BackupResult>>("/system/backup-db", { token, method: "POST" });
      return unwrapData(payload) ?? { path: "", size: 0, sha256: "" };
    },

    async listBackups(token: string, signal?: AbortSignal): Promise<BackupEntry[]> {
      const payload = await request<Envelope<BackupEntry[]>>("/system/backups", { token, signal });
      return unwrapData(payload) ?? [];
    },
  };
}
