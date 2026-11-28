import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, formatTime } from "./core";
import { __test__, createNodesApi, isNodeProbeStaleError, isNodeRevisionConflictError } from "./nodes-api";

const PROBE_TOKEN = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
const REVISION = "0123456789abcdef0123456789abcdef";

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    // Error responses read Retry-After, so the fixture needs a header lookup like a real Response.
    headers: { get: vi.fn().mockReturnValue(null) },
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
          warning: "备份目录标识已更改，已有备份数据不会自动迁移",
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
    expect(result.warning).toBe("备份目录标识已更改，已有备份数据不会自动迁移");

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

  it("keeps the raw tags string and an exact blank base_path for editor round-trips", () => {
    const node = __test__.mapNode({
      id: 4,
      name: "edge-2",
      host: "10.0.0.4",
      tags: " prod,, web ,prod",
      base_path: "",
    });

    expect(node.tags).toEqual(["prod", "web", "prod"]);
    expect(node.rawTags).toBe(" prod,, web ,prod");
    expect(node.basePath).toBe("");
    expect(__test__.mapNode({ id: 5, name: "edge-3", host: "10.0.0.5" }).rawTags).toBe("");
  });

  it("derives the backup sort instant from the original RFC3339 timestamp with offset and nanoseconds", () => {
    const map = (lastBackupAt?: string | null) =>
      __test__.mapNode({ id: 9, name: "edge-9", host: "10.0.0.9", last_backup_at: lastBackupAt });
    const instant = (value: string) => map(value).lastBackupInstant;
    const fallBackEst = Date.UTC(2026, 10, 1, 6, 30) / 1000;

    // Same wall clock on both sides of the America/New_York fall-back is one hour apart.
    expect(instant("2026-11-01T01:30:00-04:00")).toEqual({ epochSeconds: fallBackEst - 3600, nanoseconds: 0 });
    expect(instant("2026-11-01T01:30:00-05:00")).toEqual({ epochSeconds: fallBackEst, nanoseconds: 0 });
    // Different offsets naming the same moment produce the same instant.
    expect(instant("2026-11-01T06:30:00Z")).toEqual({ epochSeconds: fallBackEst, nanoseconds: 0 });
    expect(instant("2026-11-01T12:00:00+05:30")).toEqual({ epochSeconds: fallBackEst, nanoseconds: 0 });
    // SQLite microseconds and Go nanoseconds stay distinct below the millisecond.
    expect(instant("2026-11-01T06:30:00.123456Z")).toEqual({ epochSeconds: fallBackEst, nanoseconds: 123_456_000 });
    expect(instant("2026-11-01T06:30:00.123456789Z")).toEqual({ epochSeconds: fallBackEst, nanoseconds: 123_456_789 });
    expect(instant("2026-11-01T06:30:00.5Z")).toEqual({ epochSeconds: fallBackEst, nanoseconds: 500_000_000 });
    // Epoch and pre-epoch seconds are signed real values, not "missing".
    expect(instant("1970-01-01T00:00:00Z")).toEqual({ epochSeconds: 0, nanoseconds: 0 });
    expect(instant("1969-12-31T23:59:59.5Z")).toEqual({ epochSeconds: -1, nanoseconds: 500_000_000 });
    expect(instant("2028-02-29T00:00:00Z")).toEqual({ epochSeconds: Date.UTC(2028, 1, 29) / 1000, nanoseconds: 0 });

    // The display string (shown in the UI and exported to CSV) is unchanged.
    expect(map("2026-11-01T01:30:00-04:00").lastBackupAt).toBe(formatTime("2026-11-01T01:30:00-04:00"));
    expect(map(null).lastBackupAt).toBe("-");
    expect(map("not-a-time").lastBackupAt).toBe("not-a-time");

    for (const invalid of [
      undefined,
      null,
      "",
      "not-a-time",
      "2026-11-01T01:30:00", // zone-less local time
      "2026-11-01 01:30:00Z",
      "2026-02-29T00:00:00Z",
      "2026-04-31T00:00:00Z",
      "2026-13-01T00:00:00Z",
      "2026-11-00T00:00:00Z",
      "2026-11-01T24:00:00Z",
      "2026-11-01T01:60:00Z",
      "2026-11-01T01:30:60Z",
      "2026-11-01T01:30:00.1234567891Z",
      "2026-11-01T01:30:00+24:00",
      "2026-11-01T01:30:00+05:60",
      "2026-11-01T01:30:00+0500",
    ]) {
      expect(map(invalid)).not.toHaveProperty("lastBackupInstant");
    }
  });

  it("updateNode omits untouched maintenance fields and sends empty strings to clear them", async () => {
    const nodeBody = JSON.stringify({
      code: 0,
      message: "ok",
      data: { node: { id: 7, name: "db-1", host: "10.0.0.7" } },
    });
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, nodeBody))
      .mockResolvedValueOnce(createMockResponse(200, nodeBody));
    const base = {
      name: "db-1",
      host: "10.0.0.7",
      port: 22,
      username: "root",
      authType: "key" as const,
      keyId: "key-3",
      tags: " prod,, web ",
      basePath: "",
    };

    await api.updateNode("token-node", 7, base);
    await api.updateNode("token-node", 7, { ...base, maintenanceStart: "", maintenanceEnd: "" });

    const untouched = JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body));
    expect(untouched).not.toHaveProperty("maintenance_start");
    expect(untouched).not.toHaveProperty("maintenance_end");
    expect(untouched).not.toHaveProperty("expiry_date");
    expect(untouched).toMatchObject({ tags: " prod,, web ", base_path: "", ssh_key_id: 3 });

    const cleared = JSON.parse(String((fetchMock.mock.calls[1] as [string, RequestInit])[1].body));
    expect(cleared).toMatchObject({ maintenance_start: "", maintenance_end: "" });
    expect(cleared).not.toHaveProperty("expiry_date");
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

  it("maps an opaque probe_token on node reads and omits a null or empty one", () => {
    expect(__test__.mapNode({ id: 1, name: "a", host: "h", probe_token: PROBE_TOKEN }).probeToken).toBe(PROBE_TOKEN);
    expect(__test__.mapNode({ id: 1, name: "a", host: "h", probe_token: null })).not.toHaveProperty("probeToken");
    expect(__test__.mapNode({ id: 1, name: "a", host: "h", probe_token: "" })).not.toHaveProperty("probeToken");
    expect(__test__.mapNode({ id: 1, name: "a", host: "h" })).not.toHaveProperty("probeToken");
  });

  it("maps an applied test's probe_token and reduces a stale result to its message", async () => {
    const envelope = (data: Record<string, unknown>) => createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data }));
    fetchMock
      .mockResolvedValueOnce(envelope({
        ok: false,
        message: "未知主机密钥被拒绝",
        probe_at: "2026-09-01T00:00:00Z",
        probe_token: PROBE_TOKEN,
        error_code: "ssh_host_key_unknown",
        host_key: { algorithm: "ssh-ed25519", fingerprint_sha256: "SHA256:abc" },
      }))
      .mockResolvedValueOnce(envelope({
        ok: false,
        stale: true,
        message: "已有更新的连接测试",
        probe_at: "2026-09-01T00:00:00Z",
        probe_token: PROBE_TOKEN,
        error_code: "ssh_host_key_unknown",
        host_key: { algorithm: "ssh-ed25519", fingerprint_sha256: "SHA256:abc" },
      }));

    await expect(api.testNodeConnection("token-node", 7)).resolves.toMatchObject({
      ok: false,
      probeToken: PROBE_TOKEN,
      errorCode: "ssh_host_key_unknown",
      hostKey: { fingerprintSha256: "SHA256:abc" },
    });
    await expect(api.testNodeConnection("token-node", 7)).resolves.toEqual({
      ok: false,
      stale: true,
      message: "已有更新的连接测试",
    });
  });

  it("trustNodeHostKey binds the trust to the reviewed probe token when one is known", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200, JSON.stringify({
      code: 0,
      message: "ok",
      data: { already_trusted: false, algorithm: "ssh-ed25519", fingerprint_sha256: "SHA256:abc" },
    })));

    await api.trustNodeHostKey("token-node", 7, "SHA256:abc", PROBE_TOKEN);

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({ fingerprint_sha256: "SHA256:abc", probe_token: PROBE_TOKEN });
  });

  it("recognizes only the node_probe_stale conflict as a stale trust", async () => {
    const conflict = (errorCode: string) => createMockResponse(409, JSON.stringify({
      code: 409,
      message: "conflict",
      data: { error_code: errorCode },
    }));
    fetchMock
      .mockResolvedValueOnce(conflict("node_probe_stale"))
      .mockResolvedValueOnce(conflict("ssh_host_key_changed"));

    const stale = await api.trustNodeHostKey("token-node", 7, "SHA256:abc", PROBE_TOKEN).catch((error: unknown) => error);
    const changed = await api.trustNodeHostKey("token-node", 7, "SHA256:abc", PROBE_TOKEN).catch((error: unknown) => error);

    expect(stale).toBeInstanceOf(ApiError);
    expect(isNodeProbeStaleError(stale)).toBe(true);
    expect(isNodeProbeStaleError(changed)).toBe(false);
    expect(isNodeProbeStaleError(new Error("node_probe_stale"))).toBe(false);
  });

  it("maps the configuration revision and the latest test outcome with its applicability", () => {
    const node = __test__.mapNode({
      id: 1,
      name: "a",
      host: "h",
      status: "offline",
      revision: REVISION,
      connection_test: { status: "online", current: false },
    });
    expect(node).toMatchObject({ status: "offline", revision: REVISION, connectionTest: { status: "online", current: false } });

    const legacy = __test__.mapNode({ id: 1, name: "a", host: "h", revision: "", connection_test: null });
    expect(legacy).not.toHaveProperty("revision");
    expect(legacy).not.toHaveProperty("connectionTest");
    for (const connectionTest of [{ status: "warning", current: true }, { status: "online" }, {}]) {
      expect(__test__.mapNode({ id: 1, name: "a", host: "h", connection_test: connectionTest })).not.toHaveProperty("connectionTest");
    }
  });

  it("sends the opened revision with an update only when the editor knows one", async () => {
    const nodeBody = JSON.stringify({ code: 0, message: "ok", data: { node: { id: 7, name: "db-1", host: "10.0.0.7", revision: REVISION } } });
    fetchMock
      .mockResolvedValueOnce(createMockResponse(200, nodeBody))
      .mockResolvedValueOnce(createMockResponse(200, nodeBody));
    const input = { name: "db-1", host: "10.0.0.7", port: 22, username: "root", authType: "key" as const, tags: "" };

    await expect(api.updateNode("token-node", 7, { ...input, expectedRevision: REVISION })).resolves.toMatchObject({ node: { revision: REVISION } });
    await api.updateNode("token-node", 7, input);

    const conditional = JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body));
    expect(conditional.expected_revision).toBe(REVISION);
    const legacy = JSON.parse(String((fetchMock.mock.calls[1] as [string, RequestInit])[1].body));
    expect(legacy).not.toHaveProperty("expected_revision");
  });

  it("adds expected_revision to a test-connection request only when asked", async () => {
    const envelope = () => createMockResponse(200, JSON.stringify({ code: 0, message: "ok", data: { ok: true, message: "alive" } }));
    fetchMock.mockResolvedValueOnce(envelope()).mockResolvedValueOnce(envelope());

    await api.testNodeConnection("token-node", 7, { expectedRevision: REVISION });
    await api.testNodeConnection("token-node", 7);

    expect((fetchMock.mock.calls[0] as [string, RequestInit])[0]).toBe(`/api/v1/nodes/7/test-connection?expected_revision=${REVISION}`);
    expect((fetchMock.mock.calls[1] as [string, RequestInit])[0]).toBe("/api/v1/nodes/7/test-connection");
  });

  it("recognizes only the node_revision_conflict refusal as a revision conflict", async () => {
    const conflict = (errorCode: string) => createMockResponse(409, JSON.stringify({
      code: 409,
      message: "conflict",
      data: { error_code: errorCode },
    }));
    fetchMock
      .mockResolvedValueOnce(conflict("node_revision_conflict"))
      .mockResolvedValueOnce(conflict("backup_dir_conflict"));
    const input = { name: "db-1", host: "10.0.0.7", port: 22, username: "root", authType: "key" as const, tags: "", expectedRevision: REVISION };

    const stale = await api.updateNode("token-node", 7, input).catch((error: unknown) => error);
    const other = await api.updateNode("token-node", 7, input).catch((error: unknown) => error);

    expect(isNodeRevisionConflictError(stale)).toBe(true);
    expect(isNodeProbeStaleError(stale)).toBe(false);
    expect(isNodeRevisionConflictError(other)).toBe(false);
    expect(isNodeRevisionConflictError(new Error("node_revision_conflict"))).toBe(false);
  });
});
