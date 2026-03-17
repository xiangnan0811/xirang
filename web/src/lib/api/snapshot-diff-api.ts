import { request, type Envelope, unwrapData } from "./core";

export interface DiffChange {
  path: string;
  type: "added" | "removed" | "changed";
  size_before?: number;
  size_after?: number;
}

export interface DiffStats {
  added: number;
  removed: number;
  changed: number;
}

export interface SnapshotDiff {
  snap1: string;
  snap2: string;
  stats: DiffStats;
  changes: DiffChange[];
}

export function createSnapshotDiffApi() {
  return {
    async diffSnapshots(
      token: string,
      taskId: number,
      snap1: string,
      snap2: string,
    ): Promise<SnapshotDiff> {
      const query = new URLSearchParams({ snap1, snap2 });
      const payload = await request<Envelope<SnapshotDiff>>(
        `/tasks/${taskId}/snapshots/diff?${query}`,
        { token },
      );
      return unwrapData(payload);
    },
  };
}
