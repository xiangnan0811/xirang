import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSystemApi } from "./system-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
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
