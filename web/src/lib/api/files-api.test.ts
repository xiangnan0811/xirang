import { beforeEach, describe, expect, it, vi } from "vitest";
import { request } from "./core";
import type * as CoreModule from "./core";
import { createFilesApi, mapFileEntry, mapFileListResult } from "./files-api";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof CoreModule>("./core");
  return { ...actual, request: vi.fn() };
});

const requestMock = vi.mocked(request);

describe("files-api mapping", () => {
  it("maps directory entries to camelCase", () => {
    expect(mapFileListResult({
      path: "/safe",
      truncated: false,
      entries: [{
        name: "file.txt",
        path: "/safe/file.txt",
        is_dir: false,
        size: 12,
        mode: "-rw-r--r--",
        mod_time: "2026-05-24T00:00:00Z",
      }],
    })).toEqual({
      path: "/safe",
      truncated: false,
      entries: [{
        name: "file.txt",
        path: "/safe/file.txt",
        isDir: false,
        size: 12,
        mode: "-rw-r--r--",
        modTime: "2026-05-24T00:00:00Z",
      }],
    });
    expect(mapFileEntry({ is_dir: true, name: "dir", path: "/dir" }).isDir).toBe(true);
  });
});

describe("files-api node listing requests", () => {
  beforeEach(() => {
    requestMock.mockReset();
    requestMock.mockResolvedValue({ path: "/srv/data", entries: [], truncated: false });
  });

  it("requests the server-selected start with initial=1 and no path key", async () => {
    const signal = new AbortController().signal;
    const result = await createFilesApi().listNodeInitialFiles("token", 7, { signal });

    expect(requestMock).toHaveBeenCalledTimes(1);
    const [url, options] = requestMock.mock.calls[0];
    expect(url).toBe("/nodes/7/files?initial=1");
    const params = new URL(url, "http://local").searchParams;
    expect(params.has("path")).toBe(false);
    expect(params.has("path_exact")).toBe(false);
    expect(options).toEqual({ token: "token", signal });
    expect(result).toEqual({ path: "/srv/data", entries: [], truncated: false });
  });

  it("sends explicit listings with the exact path flag and preserves trailing spaces", async () => {
    const signal = new AbortController().signal;
    await createFilesApi().listNodeFiles("token", 7, "/srv/data ", { signal });

    const [url, options] = requestMock.mock.calls[0];
    expect(url).toBe("/nodes/7/files?path=%2Fsrv%2Fdata+&path_exact=1");
    const params = new URL(url, "http://local").searchParams;
    expect(params.get("path")).toBe("/srv/data ");
    expect(params.get("path_exact")).toBe("1");
    expect(params.has("initial")).toBe(false);
    expect(options).toEqual({ token: "token", signal });
  });

  it("sends previews with the exact path flag and preserves trailing spaces", async () => {
    requestMock.mockResolvedValue({ path: "/srv/data /a b ", content: "x", size: 1, truncated: false });
    const signal = new AbortController().signal;
    await createFilesApi().getNodeFileContent("token", 7, "/srv/data /a b ", { signal });

    const [url, options] = requestMock.mock.calls[0];
    expect(url).toBe("/nodes/7/files/content?path=%2Fsrv%2Fdata+%2Fa+b+&path_exact=1");
    const params = new URL(url, "http://local").searchParams;
    expect(params.get("path")).toBe("/srv/data /a b ");
    expect(params.get("path_exact")).toBe("1");
    expect(options).toEqual({ token: "token", signal });
  });

  it("leaves the task backup listing on its legacy path-only query", async () => {
    await createFilesApi().listTaskBackupFiles("token", 3, "/backup ");

    const [url] = requestMock.mock.calls[0];
    expect(url).toBe("/tasks/3/backup-files?path=%2Fbackup+");
  });
});
