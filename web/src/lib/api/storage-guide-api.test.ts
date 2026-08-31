import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createStorageGuideApi } from "./storage-guide-api";

function createMockResponse(body: unknown) {
  return {
    status: 200,
    ok: true,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify({ code: 0, message: "ok", data: body })),
  } as unknown as Response;
}

describe("storage-guide api mapping", () => {
  const fetchMock = vi.fn();
  const api = createStorageGuideApi();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps verify-mount snake_case fields to camelCase domain", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse({
      exists: true,
      is_mount_point: true,
      writable: false,
      total_gb: "12.5",
      free_gb: 4,
      filesystem: "ext4",
    }));

    await expect(api.verifyMount("token", "/mnt/nas")).resolves.toEqual({
      exists: true,
      isMountPoint: true,
      writable: false,
      totalGb: 12.5,
      freeGb: 4,
      filesystem: "ext4",
    });
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))).toEqual({ path: "/mnt/nas" });
  });
});
