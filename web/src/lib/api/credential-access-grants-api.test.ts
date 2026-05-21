import { beforeEach, describe, expect, it, vi } from "vitest";
import { createCredentialAccessGrantsApi, mapCredentialAccessGrant } from "./credential-access-grants-api";
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
      requestedAt: "2026-05-20T00:00:00Z",
      approvedAt: "2026-05-20T00:00:01Z",
      approverUserId: 7,
      approverUsername: "admin",
      expiresAt: "2026-05-20T00:10:00Z",
      revokedAt: undefined,
      revokedByUserId: undefined,
      createdAt: "2026-05-20T00:00:00Z",
      updatedAt: "2026-05-20T00:00:00Z",
    });
  });

  it("falls back safely for invalid numeric fields", () => {
    const grant = mapCredentialAccessGrant({
      id: "bad-id",
      requester_user_id: "bad-user",
      node_id: "bad-node",
      requested_ttl_seconds: "bad-ttl",
      reason: null,
    });

    expect(grant.id).toBe(0);
    expect(grant.requesterUserId).toBe(0);
    expect(grant.nodeId).toBeUndefined();
    expect(grant.requestedTtlSeconds).toBe(0);
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
