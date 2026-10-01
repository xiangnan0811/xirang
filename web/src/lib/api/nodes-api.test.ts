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

  it("maps the latest manual connection fields and ignores retired disk and probe payload", () => {
    const node = __test__.mapNode({
      id: 3,
      name: "edge-1",
      host: "10.0.0.3",
      status: "warning",
      last_seen_at: "2026-09-01T00:00:00Z",
      connection_latency_ms: 15,
      disk_used_gb: 40,
      disk_total_gb: 100,
      last_probe_at: "2026-09-01T00:00:00Z",
    } as never);

    expect(node).toMatchObject({
      id: 3,
      status: "warning",
      connectionLatencyMs: 15,
    });
    expect(node.lastSeenAt).not.toBe("");
    expect(node).not.toHaveProperty("diskUsedGb");
    expect(node).not.toHaveProperty("diskTotalGb");
    expect(node).not.toHaveProperty("diskFreePercent");
    expect(node).not.toHaveProperty("lastProbeAt");
    expect(node).not.toHaveProperty("diskProbeAt");
  });

  it("maps test-connection and emergency-backup snake_case results", async () => {
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { ok: true, message: "alive", latency_ms: 12, probe_at: "2026-09-01T00:00:00Z", disk_used_gb: 40, disk_total_gb: 100 },
      })))
      .mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
        code: 0,
        message: "ok",
        data: { triggered: 2, task_ids: [11, 12], errors: [] },
      })));

    const connection = await api.testNodeConnection("token-node", 7);
    expect(connection).toMatchObject({
      ok: true,
      message: "alive",
      latencyMs: 12,
    });
    expect(connection.testedAt).toMatch(/^\d{4}-\d{2}-\d{2} /);
    expect(connection).not.toHaveProperty("probeAt");
    expect(connection).not.toHaveProperty("lastProbeAt");
    expect(connection).not.toHaveProperty("diskUsedGb");
    expect(connection).not.toHaveProperty("diskTotalGb");
    await expect(api.emergencyBackup("token-node", 7)).resolves.toEqual({
      triggered: 2,
      taskIds: [11, 12],
      errors: [],
    });
    const [, backupInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(backupInit.headers).not.toHaveProperty("X-Xirang-Step-Up");
  });

  it("emergencyBackup 只映射 task_ids，并把 step-up proof 放进请求头", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        triggered: 1,
        task_ids: [7],
        run_ids: [101],
        errors: ["task 20: executor failed"],
      },
    })));

    const emergencyBackup = api.emergencyBackup as (
      token: string,
      nodeId: number,
      stepUpProof?: string,
    ) => ReturnType<typeof api.emergencyBackup>;

    await expect(emergencyBackup("token-node", 9, "fresh-manual-proof")).resolves.toEqual({
      triggered: 1,
      taskIds: [7],
      errors: ["task 20: executor failed"],
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/nodes/9/emergency-backup");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(init.headers).toMatchObject({
      Authorization: "Bearer token-node",
      "X-Xirang-Step-Up": "fresh-manual-proof",
    });
  });

  it("maps structured host-key failures and drops unknown or empty fingerprints", async () => {
    const envelope = (data: Record<string, unknown>) => createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data,
    }));
    fetchMock
      .mockResolvedValueOnce(envelope({
        ok: false,
        message: "未知主机密钥被拒绝",
        error_code: "ssh_host_key_unknown",
        host_key: { algorithm: "ssh-ed25519", fingerprint_sha256: "SHA256:abc" },
      }))
      .mockResolvedValueOnce(envelope({
        ok: false,
        message: "主机密钥不一致",
        error_code: "ssh_host_key_mismatch",
        host_key: { algorithm: "ssh-ed25519", fingerprint_sha256: "SHA256:mismatch" },
      }))
      .mockResolvedValueOnce(envelope({
        ok: false,
        message: "连接失败",
        error_code: "ssh_timeout",
        host_key: { algorithm: "ssh-ed25519", fingerprint_sha256: "SHA256:ignored" },
      }))
      .mockResolvedValueOnce(envelope({
        ok: false,
        message: "连接失败",
        error_code: "ssh_host_key_unknown",
        host_key: { algorithm: "ssh-ed25519", fingerprint_sha256: "" },
      }))
      .mockResolvedValueOnce(envelope({
        ok: false,
        message: "连接失败",
        error_code: "ssh_host_key_mismatch",
      }));

    await expect(api.testNodeConnection("token-node", 7)).resolves.toMatchObject({
      ok: false,
      message: "未知主机密钥被拒绝",
      errorCode: "ssh_host_key_unknown",
      hostKey: { algorithm: "ssh-ed25519", fingerprintSha256: "SHA256:abc" },
    });
    await expect(api.testNodeConnection("token-node", 7)).resolves.toMatchObject({
      ok: false,
      message: "主机密钥不一致",
      errorCode: "ssh_host_key_mismatch",
      hostKey: { algorithm: "ssh-ed25519", fingerprintSha256: "SHA256:mismatch" },
    });

    for (let i = 0; i < 3; i += 1) {
      const dropped = await api.testNodeConnection("token-node", 7);
      expect(dropped.ok).toBe(false);
      expect(dropped.errorCode).toBeUndefined();
      expect(dropped.hostKey).toBeUndefined();
    }
  });

  it("trustNodeHostKey posts the confirmed fingerprint and maps camelCase fields", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        trusted: true,
        already_trusted: false,
        algorithm: "ssh-ed25519",
        fingerprint_sha256: "SHA256:abc",
      },
    })));

    await expect(api.trustNodeHostKey("token-node", 7, "SHA256:abc")).resolves.toEqual({
      alreadyTrusted: false,
      algorithm: "ssh-ed25519",
      fingerprintSha256: "SHA256:abc",
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/nodes/7/trust-host-key");
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({ Authorization: "Bearer token-node" });
    expect(JSON.parse(String(init.body))).toEqual({ fingerprint_sha256: "SHA256:abc" });
  });

  it("getNodeSummary maps open_alerts and running_tasks and ignores extra fields", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: {
        open_alerts: 2,
        running_tasks: "3",
        cpu_pct: 90,
        disk_pct: 80,
      },
    })));

    await expect(api.getNodeSummary("token-node", 42)).resolves.toEqual({
      openAlerts: 2,
      runningTasks: 3,
    });

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/nodes/42/summary");
  });

  it("getNodeSummary treats missing counts as zero", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: {},
    })));

    await expect(api.getNodeSummary("token-node", 7)).resolves.toEqual({
      openAlerts: 0,
      runningTasks: 0,
    });
  });
});
