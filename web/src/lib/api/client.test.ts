import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiClient } from "./client";
import { ApiError, buildLoginRedirectPath, isStepUpRequiredError, normalizeRedirectTarget } from "./core";

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

  it("getNodes 会禁用浏览器缓存，避免刷新拿到旧节点列表", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(
        200,
        JSON.stringify({
          code: 0,
          message: "ok",
          data: [],
        })
      )
    );

    await apiClient.getNodes("token-task");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.cache).toBe("no-cache");
  });

  it("高风险任务请求会附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(
        200,
        JSON.stringify({ code: 0, message: "ok", data: { message: "triggered", run_id: 88 } })
      )
    );

    await apiClient.triggerTask("token-task", 101, "proof-1");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/tasks/101/trigger");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer token-task",
      "X-Xirang-Step-Up": "proof-1",
    });
  });

  it("step-up proof 请求只提交 TOTP code", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(
        200,
        JSON.stringify({ code: 0, message: "ok", data: { proof: "proof-1", expires_at: "2026-05-20T00:00:00Z", proof_ttl_seconds: 300 } })
      )
    );

    await apiClient.requestStepUpProof("token-task", "123456");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/auth/step-up");
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({ Authorization: "Bearer token-task" });
    expect(JSON.parse(String(init.body))).toEqual({ code: "123456" });
  });

  it("batch command 请求会附加 step-up proof header", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(
        200,
        JSON.stringify({ code: 0, message: "ok", data: { batch_id: "batch-1", task_ids: [], run_ids: [], retain: false } })
      )
    );

    await apiClient.createBatchCommand("token-task", [1], "uptime", undefined, false, "proof-2");

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/batch-commands");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer token-task",
      "X-Xirang-Step-Up": "proof-2",
    });
  });
});

describe("apiClient 会话跳转", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    sessionStorage.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("会保留当前完整相对路径作为登录返回地址", () => {
    expect(
      buildLoginRedirectPath({
        pathname: "/app/logs",
        search: "?task=7&level=error",
        hash: "#tail",
      })
    ).toBe("/login?redirect=%2Fapp%2Flogs%3Ftask%3D7%26level%3Derror%23tail");
  });

  it("登录页本身不会再附加 redirect 参数", () => {
    expect(
      buildLoginRedirectPath({
        pathname: "/login",
        search: "?redirect=%2Fapp%2Foverview",
        hash: "",
      })
    ).toBe("/login");
  });

  it("会拒绝站外 redirect 参数", () => {
    expect(normalizeRedirectTarget("https://evil.example/phish")).toBe("/app/overview");
    expect(normalizeRedirectTarget("//evil.example/phish")).toBe("/app/overview");
    expect(normalizeRedirectTarget("/\\evil.example/phish")).toBe("/app/overview");
  });

  it("STEP_UP_REQUIRED 可被识别且不会触发会话过期跳转", async () => {
    sessionStorage.setItem("xirang-auth-token", "token-1");
    sessionStorage.setItem("xirang-step-up-proof", "proof-1");
    sessionStorage.setItem("xirang-step-up-expires-at", String(Date.now() + 60_000));
    const originalHref = window.location.href;
    fetchMock.mockResolvedValueOnce(
      createMockResponse(
        403,
        JSON.stringify({
          code: 403,
          message: "需要二次验证",
          data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
        })
      )
    );

    let captured: unknown;
    try {
      await apiClient.triggerTask("token-task", 101);
    } catch (error) {
      captured = error;
    }

    expect(captured).toBeInstanceOf(ApiError);
    expect(isStepUpRequiredError(captured)).toBe(true);
    expect(sessionStorage.getItem("xirang-auth-token")).toBe("token-1");
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBe("proof-1");
    expect(window.location.href).toBe(originalHref);
  });
});
