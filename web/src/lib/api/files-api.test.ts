import { describe, expect, it } from "vitest";
import { mapFileEntry, mapFileListResult } from "./files-api";

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
