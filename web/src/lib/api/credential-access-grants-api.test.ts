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
      "FAKE_TOKEN_FOR_TEST_ONLY",
      { nodeId: 5, reason: "Routine maintenance", requestedTtlSeconds: 600 },
      "FAKE_STEP_UP_PROOF_FOR_TEST_ONLY",
    );

    expect(requestMock).toHaveBeenCalledWith("/credential-access-grants/terminal", {
      method: "POST",
      token: "FAKE_TOKEN_FOR_TEST_ONLY",
      stepUpProof: "FAKE_STEP_UP_PROOF_FOR_TEST_ONLY",
      body: {
        node_id: 5,
        reason: "Routine maintenance",
        requested_ttl_seconds: 600,
      },
    });
    expect(grant).toMatchObject({ id: 12, nodeId: 5, status: "active" });
  });
});
