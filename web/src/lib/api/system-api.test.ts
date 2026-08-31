import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSystemApi } from "./system-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(body)
  } as unknown as Response;
}

describe("system api", () => {
  const fetchMock = vi.fn();
  const api = createSystemApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("backupDB unwraps filename/path/size/sha256 from the response envelope", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          filename: "xirang-20260506-120000.db",
          path: "/data/backups/xirang-20260506-120000.db",
          size: 1234,
          sha256: "abc123",
        },
      }))
    );

    const result = await api.backupDB("token-1");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toEqual({
      filename: "xirang-20260506-120000.db",
      path: "/data/backups/xirang-20260506-120000.db",
      size: 1234,
      sha256: "abc123",
    });
  });

  it("maps version check and backup list wire fields to camelCase", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          update_available: true,
          current_version: "1.0.0",
          latest_version: "1.1.0",
          release_url: "https://example.com",
        },
      })),
    );
    await expect(api.checkVersion("token-1")).resolves.toEqual({
      updateAvailable: true,
      currentVersion: "1.0.0",
      latestVersion: "1.1.0",
      releaseUrl: "https://example.com",
    });

    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: [{ filename: "a.db", size: 12, created_at: "2026-01-01T00:00:00Z", sha256: "abc" }],
      })),
    );
    await expect(api.listBackups("token-1")).resolves.toEqual([
      { filename: "a.db", size: 12, createdAt: "2026-01-01T00:00:00Z", sha256: "abc" },
    ]);
  });

  it("backupDB surfaces backend envelope messages for unsupported databases", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(501, JSON.stringify({
        code: 501,
        message: "当前仅支持 SQLite 数据库备份",
        data: null,
      }))
    );

    await expect(api.backupDB("token-1")).rejects.toThrow("当前仅支持 SQLite 数据库备份");
  });
});
