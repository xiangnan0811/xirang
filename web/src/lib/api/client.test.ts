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

describe("apiClient 任务请求约束", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockResolvedValue(
      createMockResponse(
        200,
        JSON.stringify({
          data: {
            id: 101,
            name: "demo-task",
            status: "pending",
            node_id: 9
          }
        })
      )
    );
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("createTask 仅发送 rsync 相关字段且不包含 command", async () => {
    await apiClient.createTask("token-task", {
      name: "demo-task",
      nodeId: 9,
      executorType: "rsync",
      rsyncSource: "/data/source",
      rsyncTarget: "/data/target"
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(String(init.body));

    expect(url).toBe("/api/v1/tasks");
    expect(init.method).toBe("POST");
    expect(body).toMatchObject({
      name: "demo-task",
      node_id: 9,
      policy_id: null,
      rsync_source: "/data/source",
      rsync_target: "/data/target",
      executor_type: "rsync"
    });
    expect(body).not.toHaveProperty("command");
  });

  it("apiClient 不再暴露 execNodeCommand", () => {
    const raw = apiClient as Record<string, unknown>;
    expect(raw.execNodeCommand).toBeUndefined();
    expect("execNodeCommand" in raw).toBe(false);
  });
});
