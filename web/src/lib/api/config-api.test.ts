import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createConfigApi } from "./config-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body)
  } as unknown as Response;
}

describe("config api", () => {
  const fetchMock = vi.fn();
  const api = createConfigApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("exportConfig 保留后端导出包裹结构，便于直接下载再导入", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        version: "1.0",
        exported_at: "2026-03-24T00:00:00Z",
        data: {
          nodes: [{ name: "node-a" }],
          tasks: [{ name: "task-a" }]
        }
      }))
    );

    const result = await api.exportConfig("auth-marker");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({
      version: "1.0",
      data: {
        nodes: [{ name: "node-a" }],
        tasks: [{ name: "task-a" }]
      }
    });
  });

  it("includeSecrets 导出会附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { version: "1.0", data: {} },
      }))
    );

    await api.exportConfig("auth-marker", true, "step-up-marker");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/export?include_secrets=true");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer auth-marker",
      "X-Xirang-Step-Up": "step-up-marker",
    });
  });

  it("普通导出不附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({ version: "1.0", data: {} })));

    await api.exportConfig("auth-marker");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.headers).not.toHaveProperty("X-Xirang-Step-Up");
  });

  it("importConfig 会附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { imported: 1, skipped: 0 },
      }))
    );

    await api.importConfig("auth-marker", { ssh_keys: [] }, "skip", "step-up-marker");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/config/import?conflict=skip");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer auth-marker",
      "X-Xirang-Step-Up": "step-up-marker",
    });
  });

  it("importConfig 可兼容后端分项统计响应并汇总 imported", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          nodes: 1,
          ssh_keys: 2,
          policies: 3,
          tasks: 1,
          system_settings: 1
        }
      }))
    );

    const result = await api.importConfig("auth-marker", { data: { nodes: [] } }, "skip");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toEqual({ imported: 8, skipped: 0 });
  });
});
