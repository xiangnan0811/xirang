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

    const result = await api.exportConfig("token-1");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toMatchObject({
      version: "1.0",
      data: {
        nodes: [{ name: "node-a" }],
        tasks: [{ name: "task-a" }]
      }
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

    const result = await api.importConfig("token-1", { data: { nodes: [] } }, "skip");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(result).toEqual({ imported: 8, skipped: 0 });
  });
});
