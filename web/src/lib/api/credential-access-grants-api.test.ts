import { beforeEach, describe, expect, it, vi } from "vitest";
import { buildCredentialAccessGrantQuery, createCredentialAccessGrantsApi, mapCredentialAccessGrant } from "./credential-access-grants-api";
import { formatTime } from "@/lib/date-utils";
import { request } from "./core";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);

describe("credential access grants api", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps terminal grant payload from snake_case to camelCase", () => {
    const grant = mapCredentialAccessGrant({
      id: "42",
      requester_user_id: "7",
      requester_username: "admin",
      requester_role: "admin",
      action: "terminal.open",
      purpose: "terminal",
      node_id: "11",
      task_id: 0,
      policy_id: undefined,
      reason: "Routine maintenance",
      status: "active",
      requested_ttl_seconds: "600",
      requested_at: "2026-05-20T00:00:00Z",
      approved_at: "2026-05-20T00:00:01Z",
      approver_user_id: "7",
      approver_username: "admin",
      expires_at: "2026-05-20T00:10:00Z",
      revoked_at: "",
      revoked_by_user_id: 0,
      created_at: "2026-05-20T00:00:00Z",
      updated_at: "2026-05-20T00:00:00Z",
    });

    expect(grant).toMatchObject({
      id: 42,
      requesterUserId: 7,
      requesterUsername: "admin",
      requesterRole: "admin",
      action: "terminal.open",
      purpose: "terminal",
      nodeId: 11,
      taskId: undefined,
      policyId: undefined,
      reason: "Routine maintenance",
      status: "active",
      requestedTtlSeconds: 600,
      requestedAt: formatTime("2026-05-20T00:00:00Z"),
      approvedAt: formatTime("2026-05-20T00:00:01Z"),
      approverUserId: 7,
      approverUsername: "admin",
      expiresAt: formatTime("2026-05-20T00:10:00Z"),
      revokedAt: undefined,
      revokedByUserId: undefined,
      createdAt: formatTime("2026-05-20T00:00:00Z"),
      updatedAt: formatTime("2026-05-20T00:00:00Z"),
    });
  });

  it("falls back safely for invalid numeric fields and nullable timestamps", () => {
    const grant = mapCredentialAccessGrant({
      id: "bad-id",
      requester_user_id: "bad-user",
      node_id: "bad-node",
      requested_ttl_seconds: "bad-ttl",
      requested_at: "not-a-date",
      approved_at: null,
      revoked_at: null,
      reason: null,
    });

    expect(grant.id).toBe(0);
    expect(grant.requesterUserId).toBe(0);
    expect(grant.nodeId).toBeUndefined();
    expect(grant.requestedTtlSeconds).toBe(0);
    expect(grant.requestedAt).toBe("not-a-date");
    expect(grant.approvedAt).toBeUndefined();
    expect(grant.revokedAt).toBeUndefined();
    expect(grant.reason).toBe("");
  });

  it("preserves config export grant tuple values", () => {
    const grant = mapCredentialAccessGrant({
      action: "config.export",
      purpose: "config_export",
      status: "active",
    });

    expect(grant.action).toBe("config.export");
    expect(grant.purpose).toBe("config_export");
    expect(grant.status).toBe("active");
  });

  it("preserves snapshot restore grant tuple and task binding", () => {
    const grant = mapCredentialAccessGrant({
      action: "snapshot.restore",
      purpose: "snapshot",
      task_id: "101",
      status: "active",
    });

    expect(grant.action).toBe("snapshot.restore");
    expect(grant.purpose).toBe("snapshot");
    expect(grant.taskId).toBe(101);
    expect(grant.status).toBe("active");
  });

  it("preserves task restore grant tuple and task binding", () => {
    const grant = mapCredentialAccessGrant({
      action: "task.restore_trigger",
      purpose: "task_restore",
      task_id: "102",
      status: "active",
    });

    expect(grant.action).toBe("task.restore_trigger");
    expect(grant.purpose).toBe("task_restore");
    expect(grant.taskId).toBe(102);
    expect(grant.status).toBe("active");
  });

  it("falls back safely for unknown action, purpose, and status", () => {
    const grant = mapCredentialAccessGrant({
      action: "ssh_key.export",
      purpose: "ssh_key_export",
      status: "unexpected",
    });

    expect(grant.action).toBe("unknown");
    expect(grant.purpose).toBe("unknown");
    expect(grant.status).toBe("expired");
  });

  it("serializes list filters for credential access grants", () => {
    const query = buildCredentialAccessGrantQuery({
      requesterUserId: 7,
      requesterUsername: "  admin  ",
      requesterRole: "admin",
      action: "task.restore_trigger",
      purpose: "task_restore",
      status: "revoked",
      nodeId: 11,
      taskId: 22,
      policyId: 33,
      from: "2026-05-20T00:00:00Z",
      to: "2026-05-21T00:00:00Z",
      page: 2,
      pageSize: 30,
      sortBy: "created_at",
      sortOrder: "desc",
    });

    expect(query.toString()).toBe(
      "requester_username=admin&requester_role=admin&action=task.restore_trigger&purpose=task_restore&status=revoked&from=2026-05-20T00%3A00%3A00Z&to=2026-05-21T00%3A00%3A00Z&sort_by=created_at&sort_order=desc&requester_user_id=7&node_id=11&task_id=22&policy_id=33&page=2&page_size=30",
    );
  });

  it("lists credential access grants through the paginated endpoint", async () => {
    requestMock.mockResolvedValueOnce({
      code: 0,
      message: "ok",
      data: [
        {
          id: 31,
          requester_user_id: 7,
          requester_username: "admin",
          requester_role: "admin",
          action: "task.restore_trigger",
          purpose: "task_restore",
          task_id: 102,
          reason: "Routine restore",
          status: "revoked",
          requested_ttl_seconds: 600,
          requested_at: "2026-05-20T00:00:00Z",
          expires_at: "2026-05-20T00:10:00Z",
          created_at: "2026-05-20T00:00:00Z",
          updated_at: "2026-05-20T00:00:00Z",
        },
      ],
      total: 1,
      page: 2,
      page_size: 30,
    });

    const result = await createCredentialAccessGrantsApi().listCredentialAccessGrants("auth-marker", {
      status: "revoked",
      page: 2,
      pageSize: 30,
      sortBy: "created_at",
      sortOrder: "desc",
    });

    expect(requestMock).toHaveBeenCalledWith(
      "/credential-access-grants?status=revoked&sort_by=created_at&sort_order=desc&page=2&page_size=30",
      { token: "auth-marker" },
    );
    expect(result).toMatchObject({
      total: 1,
      page: 2,
      pageSize: 30,
      items: [
        {
          id: 31,
          requesterUserId: 7,
          action: "task.restore_trigger",
          purpose: "task_restore",
          taskId: 102,
          status: "revoked",
        },
      ],
    });
  });

  it("requests a config import grant with reason, ttl, bearer token, and step-up proof", async () => {
    requestMock.mockResolvedValueOnce({
      id: 22,
      requester_user_id: 7,
      requester_username: "admin",
      requester_role: "admin",
      action: "config.import",
      purpose: "config_import",
      reason: "Routine restore",
      status: "active",
      requested_ttl_seconds: 600,
      requested_at: "2026-05-20T00:00:00Z",
      expires_at: "2026-05-20T00:10:00Z",
      created_at: "2026-05-20T00:00:00Z",
      updated_at: "2026-05-20T00:00:00Z",
    });

    const grant = await createCredentialAccessGrantsApi().requestConfigImportCredentialGrant(
      "auth-marker",
      { reason: "Routine restore", requestedTtlSeconds: 600 },
      "step-up-marker",
    );

    expect(requestMock).toHaveBeenCalledWith("/credential-access-grants/config-import", {
      method: "POST",
      token: "auth-marker",
      stepUpProof: "step-up-marker",
      body: {
        reason: "Routine restore",
        requested_ttl_seconds: 600,
      },
    });
    expect(grant).toMatchObject({ id: 22, nodeId: undefined, action: "config.import", purpose: "config_import", status: "active" });
  });

  it("requests a config export grant with reason, ttl, bearer token, and step-up proof", async () => {
    requestMock.mockResolvedValueOnce({
      id: 23,
      requester_user_id: 7,
      requester_username: "admin",
      requester_role: "admin",
      action: "config.export",
      purpose: "config_export",
      reason: "Routine export",
      status: "active",
      requested_ttl_seconds: 600,
      requested_at: "2026-05-20T00:00:00Z",
      expires_at: "2026-05-20T00:10:00Z",
      created_at: "2026-05-20T00:00:00Z",
      updated_at: "2026-05-20T00:00:00Z",
    });

    const grant = await createCredentialAccessGrantsApi().requestConfigExportCredentialGrant(
      "auth-marker",
      { reason: "Routine export", requestedTtlSeconds: 600 },
      "step-up-marker",
    );

    expect(requestMock).toHaveBeenCalledWith("/credential-access-grants/config-export", {
      method: "POST",
      token: "auth-marker",
      stepUpProof: "step-up-marker",
      body: {
        reason: "Routine export",
        requested_ttl_seconds: 600,
      },
    });
    expect(grant).toMatchObject({ id: 23, nodeId: undefined, action: "config.export", purpose: "config_export", status: "active" });
  });

  it("requests a snapshot restore grant with task, reason, ttl, bearer token, and step-up proof", async () => {
    requestMock.mockResolvedValueOnce({
      id: 24,
      requester_user_id: 7,
      requester_username: "admin",
      requester_role: "admin",
      action: "snapshot.restore",
      purpose: "snapshot",
      task_id: 101,
      reason: "Routine restore",
      status: "active",
      requested_ttl_seconds: 600,
      requested_at: "2026-05-20T00:00:00Z",
      expires_at: "2026-05-20T00:10:00Z",
      created_at: "2026-05-20T00:00:00Z",
      updated_at: "2026-05-20T00:00:00Z",
    });

    const grant = await createCredentialAccessGrantsApi().requestSnapshotRestoreCredentialGrant(
      "auth-marker",
      { taskId: 101, reason: "Routine restore", requestedTtlSeconds: 600 },
      "step-up-marker",
    );

    expect(requestMock).toHaveBeenCalledWith("/credential-access-grants/snapshot-restore", {
      method: "POST",
      token: "auth-marker",
      stepUpProof: "step-up-marker",
      body: {
        task_id: 101,
        reason: "Routine restore",
        requested_ttl_seconds: 600,
      },
    });
    expect(grant).toMatchObject({ id: 24, taskId: 101, nodeId: undefined, action: "snapshot.restore", purpose: "snapshot", status: "active" });
  });

  it("requests a task restore grant with task, reason, ttl, bearer token, and step-up proof", async () => {
    requestMock.mockResolvedValueOnce({
      id: 25,
      requester_user_id: 7,
      requester_username: "admin",
      requester_role: "admin",
      action: "task.restore_trigger",
      purpose: "task_restore",
      task_id: 102,
      reason: "Routine restore",
      status: "active",
      requested_ttl_seconds: 600,
      requested_at: "2026-05-20T00:00:00Z",
      expires_at: "2026-05-20T00:10:00Z",
      created_at: "2026-05-20T00:00:00Z",
      updated_at: "2026-05-20T00:00:00Z",
    });

    const grant = await createCredentialAccessGrantsApi().requestTaskRestoreCredentialGrant(
      "auth-marker",
      { taskId: 102, reason: "Routine restore", requestedTtlSeconds: 600 },
      "step-up-marker",
    );

    expect(requestMock).toHaveBeenCalledWith("/credential-access-grants/task-restore", {
      method: "POST",
      token: "auth-marker",
      stepUpProof: "step-up-marker",
      body: {
        task_id: 102,
        reason: "Routine restore",
        requested_ttl_seconds: 600,
      },
    });
    expect(grant).toMatchObject({ id: 25, taskId: 102, nodeId: undefined, action: "task.restore_trigger", purpose: "task_restore", status: "active" });
  });

  it("requests a terminal grant with node, reason, ttl, bearer token, and step-up proof", async () => {
    requestMock.mockResolvedValueOnce({
      id: 12,
      requester_user_id: 7,
      requester_username: "admin",
      requester_role: "admin",
      action: "terminal.open",
      purpose: "terminal",
      node_id: 5,
      reason: "Routine maintenance",
      status: "active",
      requested_ttl_seconds: 600,
      requested_at: "2026-05-20T00:00:00Z",
      expires_at: "2026-05-20T00:10:00Z",
      created_at: "2026-05-20T00:00:00Z",
      updated_at: "2026-05-20T00:00:00Z",
    });

    const grant = await createCredentialAccessGrantsApi().requestTerminalCredentialGrant(
      "auth-marker",
      { nodeId: 5, reason: "Routine maintenance", requestedTtlSeconds: 600 },
      "step-up-marker",
    );

    expect(requestMock).toHaveBeenCalledWith("/credential-access-grants/terminal", {
      method: "POST",
      token: "auth-marker",
      stepUpProof: "step-up-marker",
      body: {
        node_id: 5,
        reason: "Routine maintenance",
        requested_ttl_seconds: 600,
      },
    });
    expect(grant).toMatchObject({ id: 12, nodeId: 5, status: "active" });
  });
});
