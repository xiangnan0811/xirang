import { request } from "./core";

export interface ResticSnapshot {
  id: string;
  short_id: string;
  time: string;
  hostname: string;
  paths: string[];
  tags?: string[];
}

export interface ResticEntry {
  name: string;
  type: string;
  path: string;
  size: number;
  mtime: string;
}

export function createSnapshotsApi() {
  return {
    async listSnapshots(token: string, taskId: number): Promise<ResticSnapshot[]> {
      return (await request<ResticSnapshot[]>(`/tasks/${taskId}/snapshots`, { token })) ?? [];
    },

    async listSnapshotFiles(token: string, taskId: number, snapshotId: string, path: string = "/"): Promise<ResticEntry[]> {
      const query = new URLSearchParams({ path });
      return (await request<ResticEntry[]>(`/tasks/${taskId}/snapshots/${snapshotId}/files?${query}`, { token })) ?? [];
    },

    async restoreSnapshot(token: string, taskId: number, snapshotId: string, includes: string[], targetPath: string): Promise<void> {
      await request<unknown>(`/tasks/${taskId}/snapshots/${snapshotId}/restore`, {
        method: "POST",
        token,
        body: { includes, targetPath },
      });
    },
  };
}
