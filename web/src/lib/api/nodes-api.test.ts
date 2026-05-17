import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { __test__, createNodesApi } from "./nodes-api";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: vi.fn().mockResolvedValue(body),
  } as unknown as Response;
}

describe("nodes api", () => {
  const fetchMock = vi.fn();
  const api = createNodesApi();

  beforeEach(() => {
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("runNodeDoctor 请求节点 Doctor 并映射 snake_case 字段", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          node_id: "7",
          node_name: "node-a",
          generated_at: "2026-05-17T10:00:00Z",
          checks: [
            {
              check: "ssh",
              status: "fail",
              evidence: "SSH 认证失败",
              suggestion: "检查用户名和 SSH Key。",
            },
          ],
        },
      }))
    );

    const result = await api.runNodeDoctor("token-node", 7);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/nodes/7/doctor");
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({ Authorization: "Bearer token-node" });
    expect(result).toEqual({
      nodeId: 7,
      nodeName: "node-a",
      generatedAt: "2026-05-17T10:00:00Z",
      checks: [
        {
          check: "ssh",
          status: "fail",
          evidence: "SSH 认证失败",
          suggestion: "检查用户名和 SSH Key。",
        },
      ],
    });
  });

  it("__test__.mapNodeDoctorResult 对未知状态降级为 warn 并默认空 checks", () => {
    expect(__test__.mapNodeDoctorResult({ node_id: 1 })).toMatchObject({
      nodeId: 1,
      checks: [],
    });
    expect(__test__.mapNodeDoctorResult({ checks: [{ check: "disk", status: "unknown" }] }).checks[0].status).toBe("warn");
  });

  it("__test__.mapNodeDoctorResult 对非法 node_id 使用安全默认值", () => {
    expect(__test__.mapNodeDoctorResult({ node_id: "not-a-number" }).nodeId).toBe(0);
  });
});
