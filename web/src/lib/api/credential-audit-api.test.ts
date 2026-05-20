import { describe, expect, it } from "vitest";
import { buildCredentialAuditQuery, mapCredentialAuditEvent } from "./credential-audit-api";

describe("credential audit api mappers", () => {
  it("maps snake_case fields, nullable IDs, unknown values and safe numeric fallbacks", () => {
    const mapped = mapCredentialAuditEvent({
      id: "7",
      user_id: "bad",
      username: "admin",
      role: "admin",
      action: "future.action",
      purpose: "terminal",
      credential_kind: "ssh_key",
      credential_source: "ssh_key_id=3",
      ssh_key_id: null,
      node_id: "12",
      task_id: "bad",
      task_run_id: "42",
      policy_id: 5,
      outcome: "deferred",
      error_message: "blocked",
      metadata: { stage: "open" },
      client_ip: "127.0.0.1",
      user_agent: "Vitest",
      created_at: "2026-05-20T10:00:00Z",
    });

    expect(mapped).toMatchObject({
      id: 7,
      userId: 0,
      username: "admin",
      role: "admin",
      action: "other",
      rawAction: "future.action",
      purpose: "terminal",
      credentialKind: "ssh_key",
      credentialSource: "ssh_key_id=3",
      nodeId: 12,
      taskRunId: 42,
      policyId: 5,
      outcome: "unknown",
      errorMessage: "blocked",
      clientIP: "127.0.0.1",
      userAgent: "Vitest",
    });
    expect(mapped.sshKeyId).toBeUndefined();
    expect(mapped.taskId).toBeUndefined();
    expect(Number.isNaN(mapped.userId)).toBe(false);
  });

  it("normalizes known action and outcome values", () => {
    const mapped = mapCredentialAuditEvent({
      action: "ssh_key.export",
      outcome: "blocked",
      metadata: {},
    });

    expect(mapped.action).toBe("ssh_key.export");
    expect(mapped.rawAction).toBe("ssh_key.export");
    expect(mapped.outcome).toBe("blocked");
  });

  it("redacts legacy output, endpoints, secrets and stack-like error messages at the mapper boundary", () => {
    expect(mapCredentialAuditEvent({ error_message: "failed stdout: FAKE_REMOTE_OUTPUT_FOR_TEST_ONLY" }).errorMessage).toBe("failed stdout: [REDACTED_OUTPUT]");
    expect(mapCredentialAuditEvent({ error_message: "panic: runtime error\nbackend/internal/task/executor/ssh_connect.go:101" }).errorMessage).toBe("[REDACTED_ERROR]");
    const mapped = mapCredentialAuditEvent({
      credential_source: "https://example.invalid/hook/FAKE_TOKEN_FOR_TEST_ONLY?token=FAKE_TOKEN_FOR_TEST_ONLY",
      error_message: "request failed: https://example.invalid/hook/FAKE_TOKEN_FOR_TEST_ONLY?token=FAKE_TOKEN_FOR_TEST_ONLY Authorization: Bearer FAKE_BEARER_FOR_TEST_ONLY private_key=FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
    });
    expect(mapped.credentialSource).toBe("[REDACTED_ENDPOINT]");
    expect(mapped.errorMessage).not.toContain("example.invalid");
    expect(mapped.errorMessage).not.toContain("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(mapped.errorMessage).not.toContain("FAKE_BEARER_FOR_TEST_ONLY");
  });

  it("accepts object and JSON string metadata while dropping unsafe keys and values", () => {
    const fromObject = mapCredentialAuditEvent({
      metadata: {
        stage: "open",
        private_key: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
        note: "authorization: Bearer FAKE_TOKEN_FOR_TEST_ONLY",
        safe_list: ["one", "token=FAKE_TOKEN_FOR_TEST_ONLY", "two", 123],
        nested: { command: "cat /etc/passwd" },
        count: 3,
        ok: true,
      },
    });

    expect(fromObject.metadata).toEqual({
      stage: "open",
      safe_list: ["one", "two"],
      count: 3,
      ok: true,
    });

    const fromJSON = mapCredentialAuditEvent({
      metadata: JSON.stringify({
        format: "csv",
        payload: "FAKE_CONFIG_PAYLOAD_FOR_TEST_ONLY",
        path_hash: "abc123",
      }),
    });
    expect(fromJSON.metadata).toEqual({ format: "csv", path_hash: "abc123" });

    expect(mapCredentialAuditEvent({ metadata: "not-json" }).metadata).toEqual({});
    expect(mapCredentialAuditEvent({ metadata: ["stage"] }).metadata).toEqual({});
  });

  it("serializes filters to backend query names", () => {
    const query = buildCredentialAuditQuery({
      username: " admin ",
      role: "admin",
      userId: 1,
      action: "ssh_key.export",
      purpose: "ssh_key_export",
      credentialKind: "ssh_key",
      credentialSource: "ssh_key_id=9",
      outcome: "success",
      sshKeyId: 9,
      nodeId: 10,
      taskId: 11,
      taskRunId: 12,
      policyId: 13,
      from: "2026-05-20T00:00:00Z",
      to: "2026-05-21T00:00:00Z",
      page: 2,
      pageSize: 30,
      sortBy: "created_at",
      sortOrder: "asc",
    });

    expect(query.get("username")).toBe("admin");
    expect(query.get("user_id")).toBe("1");
    expect(query.get("credential_kind")).toBe("ssh_key");
    expect(query.get("credential_source")).toBe("ssh_key_id=9");
    expect(query.get("ssh_key_id")).toBe("9");
    expect(query.get("task_run_id")).toBe("12");
    expect(query.get("page_size")).toBe("30");
    expect(query.get("sort_by")).toBe("created_at");
    expect(query.get("sort_order")).toBe("asc");
  });
});
