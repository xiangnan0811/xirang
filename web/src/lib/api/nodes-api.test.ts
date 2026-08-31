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

  it("maps PUT /nodes/:id envelope.data {node, warning} and does not treat the wrapper as a Node", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          node: {
            id: 7,
            name: "db-1",
            host: "10.0.0.7",
            port: 22,
            username: "root",
            auth_type: "key",
            status: "online",
            backup_dir: "db-1",
          },
          warning: "备份目录标识已更改，旧路径 /backup/old 下的数据不会自动迁移",
        },
      }))
    );

    const result = await api.updateNode("token-node", 7, {
      name: "db-1",
      host: "10.0.0.7",
      port: 22,
      username: "root",
      authType: "key",
      tags: "",
      basePath: "/",
    });

    expect(result).not.toHaveProperty("id");
    expect(result.node).toMatchObject({
      id: 7,
      name: "db-1",
      host: "10.0.0.7",
      status: "online",
      backupDir: "db-1",
    });
    expect(result.warning).toContain("备份目录标识已更改");

    const wrapperMappedAsNode = __test__.mapNode({
      node: { id: 7, name: "db-1", host: "10.0.0.7" },
      warning: "ignored",
    } as never);
    expect(wrapperMappedAsNode.id).not.toBe(7);
    expect(wrapperMappedAsNode.name).not.toBe("db-1");
  });

  it("rejects a PUT /nodes/:id payload that nests the node under data without a node key", async () => {
    fetchMock.mockResolvedValueOnce(
      createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: {
          data: {
            id: 7,
            name: "db-1",
            host: "10.0.0.7",
          },
          warning: "should not be mapped as a node",
        },
      }))
    );

    await expect(api.updateNode("token-node", 7, {
      name: "db-1",
      host: "10.0.0.7",
      port: 22,
      username: "root",
      authType: "key",
      tags: "",
      basePath: "/",
    })).rejects.toThrow("invalid node update response");
  });

  it("maps test-connection and emergency-backup snake_case results", async () => {
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { ok: true, message: "alive", latency_ms: 12, disk_used_gb: 40, disk_total_gb: 100 },
      })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { triggered: 2, task_ids: [11, 12], errors: [] },
      })));

    await expect(api.testNodeConnection("token-node", 7)).resolves.toEqual({
      ok: true,
      message: "alive",
      latencyMs: 12,
      diskUsedGb: 40,
      diskTotalGb: 100,
    });
    await expect(api.emergencyBackup("token-node", 7)).resolves.toEqual({
      triggered: 2,
      taskIds: [11, 12],
      errors: [],
    });
  });
});
