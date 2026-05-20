import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, isStepUpRequiredError } from "@/lib/api/core";
import { fetchSSHKeyExportFile } from "@/lib/api/ssh-keys-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    clone: () => createMockResponse(status, body),
    json: vi.fn().mockResolvedValue(body ? JSON.parse(body) : null),
  } as unknown as Response;
}

describe("fetchSSHKeyExportFile", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("direct download 会附加 bearer token 和 step-up proof", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200));

    await fetchSSHKeyExportFile("/api/v1/ssh-keys/export?format=json&scope=all", "token-1", "proof-1");

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/ssh-keys/export?format=json&scope=all", {
      headers: {
        Authorization: "Bearer token-1",
        "X-Xirang-Step-Up": "proof-1",
      },
    });
  });

  it("direct download 会保留 STEP_UP_REQUIRED envelope 供 prompt/retry 识别", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(403, JSON.stringify({
      code: 403,
      message: "需要二次验证",
      data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
    })));

    let captured: unknown;
    try {
      await fetchSSHKeyExportFile("/api/v1/ssh-keys/export?format=json&scope=all", "token-1");
    } catch (error) {
      captured = error;
    }

    expect(captured).toBeInstanceOf(ApiError);
    expect(isStepUpRequiredError(captured)).toBe(true);
  });
});
