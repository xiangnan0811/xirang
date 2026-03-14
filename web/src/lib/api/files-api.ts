import { request, type Envelope, unwrapData } from "./core";

export type FileEntry = {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mode: string;
  mod_time: string;
};

export type FileListResult = {
  path: string;
  entries: FileEntry[];
  truncated: boolean;
};

export type FileContentResult = {
  path: string;
  content: string;
  size: number;
  truncated: boolean;
};

export function createFilesApi() {
  return {
    async listNodeFiles(
      token: string,
      nodeId: number,
      path: string,
      options?: { signal?: AbortSignal }
    ): Promise<FileListResult> {
      const query = new URLSearchParams({ path });
      const payload = await request<Envelope<FileListResult>>(
        `/nodes/${nodeId}/files?${query.toString()}`,
        { token, signal: options?.signal }
      );
      return unwrapData(payload);
    },

    async getNodeFileContent(
      token: string,
      nodeId: number,
      path: string,
      options?: { signal?: AbortSignal }
    ): Promise<FileContentResult> {
      const query = new URLSearchParams({ path });
      const payload = await request<Envelope<FileContentResult>>(
        `/nodes/${nodeId}/files/content?${query.toString()}`,
        { token, signal: options?.signal }
      );
      return unwrapData(payload);
    },

    async listTaskBackupFiles(
      token: string,
      taskId: number,
      path: string,
      options?: { signal?: AbortSignal }
    ): Promise<FileListResult> {
      const query = new URLSearchParams({ path });
      const payload = await request<Envelope<FileListResult>>(
        `/tasks/${taskId}/backup-files?${query.toString()}`,
        { token, signal: options?.signal }
      );
      return unwrapData(payload);
    },
  };
}
