import { request } from "./core";
import { finiteNumber } from "./number-utils";

export type FileEntry = {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  mode: string;
  modTime: string;
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

type RawFileEntry = {
  name?: unknown;
  path?: unknown;
  is_dir?: unknown;
  size?: unknown;
  mode?: unknown;
  mod_time?: unknown;
};

type RawFileListResult = {
  path?: unknown;
  entries?: RawFileEntry[];
  truncated?: unknown;
};

type RawFileContentResult = {
  path?: unknown;
  content?: unknown;
  size?: unknown;
  truncated?: unknown;
};

export function mapFileEntry(row: RawFileEntry | null | undefined): FileEntry {
  return {
    name: String(row?.name ?? ""),
    path: String(row?.path ?? ""),
    isDir: Boolean(row?.is_dir),
    size: finiteNumber(row?.size),
    mode: String(row?.mode ?? ""),
    modTime: String(row?.mod_time ?? ""),
  };
}

export function mapFileListResult(row: RawFileListResult | null | undefined): FileListResult {
  return {
    path: String(row?.path ?? ""),
    truncated: Boolean(row?.truncated),
    entries: Array.isArray(row?.entries) ? row.entries.map(mapFileEntry) : [],
  };
}

export function mapFileContentResult(row: RawFileContentResult | null | undefined): FileContentResult {
  return {
    path: String(row?.path ?? ""),
    content: String(row?.content ?? ""),
    size: finiteNumber(row?.size),
    truncated: Boolean(row?.truncated),
  };
}

export function createFilesApi() {
  return {
    async listNodeFiles(
      token: string,
      nodeId: number,
      path: string,
      options?: { signal?: AbortSignal }
    ): Promise<FileListResult> {
      // path_exact=1: the browser only sends canonical paths from earlier responses, so the
      // backend must use them verbatim (e.g. keep trailing spaces) instead of trimming.
      const query = new URLSearchParams({ path, path_exact: "1" });
      const raw = await request<RawFileListResult>(
        `/nodes/${nodeId}/files?${query.toString()}`,
        { token, signal: options?.signal }
      );
      return mapFileListResult(raw);
    },

    /**
     * Lists the server-selected starting directory. `path` is deliberately
     * omitted: the backend only auto-selects when the key is absent.
     */
    async listNodeInitialFiles(
      token: string,
      nodeId: number,
      options?: { signal?: AbortSignal }
    ): Promise<FileListResult> {
      const raw = await request<RawFileListResult>(
        `/nodes/${nodeId}/files?initial=1`,
        { token, signal: options?.signal }
      );
      return mapFileListResult(raw);
    },

    async getNodeFileContent(
      token: string,
      nodeId: number,
      path: string,
      options?: { signal?: AbortSignal }
    ): Promise<FileContentResult> {
      const query = new URLSearchParams({ path, path_exact: "1" });
      const raw = await request<RawFileContentResult>(
        `/nodes/${nodeId}/files/content?${query.toString()}`,
        { token, signal: options?.signal }
      );
      return mapFileContentResult(raw);
    },

    async listTaskBackupFiles(
      token: string,
      taskId: number,
      path: string,
      options?: { signal?: AbortSignal }
    ): Promise<FileListResult> {
      const query = new URLSearchParams({ path });
      const raw = await request<RawFileListResult>(
        `/tasks/${taskId}/backup-files?${query.toString()}`,
        { token, signal: options?.signal }
      );
      return mapFileListResult(raw);
    },
  };
}
