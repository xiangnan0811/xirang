import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "./client";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body)
  } as unknown as Response;
}

describe("apiClient ID 解析", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockResolvedValue(createMockResponse(200, ""));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("deleteIntegration 支持带前缀的 integration ID", async () => {
    await apiClient.deleteIntegration("token-1", "int-42");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];

    expect(url).toBe("/api/v1/integrations/42");
    expect(init.method).toBe("DELETE");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer token-1"
    });
  });

  it("deleteIntegration 支持纯数字字符串 ID", async () => {
    await apiClient.deleteIntegration("token-2", "7");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/integrations/7");
  });

  it("deleteIntegration 对非法 ID 直接报错且不发请求", async () => {
    await expect(apiClient.deleteIntegration("token-3", "int-abc")).rejects.toThrow(
      "无效的 int ID"
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
