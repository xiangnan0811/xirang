import { beforeEach, describe, expect, it, vi } from "vitest";
import { createSSHKeysApi } from "./ssh-keys-api";
import { request } from "./core";

vi.mock("./core", async () => {
  const actual = await vi.importActual<typeof import("./core")>("./core");
  return {
    ...actual,
    request: vi.fn(),
  };
});

const requestMock = vi.mocked(request);

describe("ssh keys api mapper", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("maps scope metadata from snake_case without exposing secret fields", async () => {
    requestMock.mockResolvedValueOnce([
      {
        id: 7,
        name: "ops-key",
        username: "deploy",
        key_type: "ed25519",
        public_key: "ssh-ed25519 FAKE_PUBLIC_KEY_MATERIAL_FOR_TEST_ONLY",
        fingerprint: "SHA256:test",
        disabled: true,
        expires_at: "2026-06-01T10:30:00Z",
        allowed_purposes: "terminal,task_command",
        allowed_node_ids: "1,2",
        allowed_node_tags: "prod",
        broad_scope: false,
        created_at: "2026-05-18T00:00:00Z",
        last_used_at: null,
      },
    ]);

    const rows = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");

    expect(rows[0]).toMatchObject({
      id: "key-7",
      name: "ops-key",
      username: "deploy",
      keyType: "ed25519",
      publicKey: "ssh-ed25519 FAKE_PUBLIC_KEY_MATERIAL_FOR_TEST_ONLY",
      fingerprint: "SHA256:test",
      disabled: true,
      expiresAt: expect.stringMatching(/^2026-06-01T/),
      allowedPurposes: "terminal,task_command",
      allowedNodeIds: "1,2",
      allowedNodeTags: "prod",
      broadScope: false,
    });
    expect(rows[0]).not.toHaveProperty("privateKey");
  });

  it("normalizes unknown key types to auto", async () => {
    requestMock.mockResolvedValueOnce([
      {
        id: 9,
        name: "unknown-type-key",
        username: "deploy",
        key_type: "unsupported",
        fingerprint: "SHA256:test",
        created_at: "2026-05-18T00:00:00Z",
      },
    ] as unknown as Awaited<ReturnType<typeof request>>);

    const rows = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");

    expect(rows[0]?.keyType).toBe("auto");
  });

  it("falls back safely for invalid numeric fields", async () => {
    requestMock.mockResolvedValueOnce([
      {
        id: "bad-id",
        name: null,
        username: undefined,
        key_type: "auto",
        fingerprint: null,
        broad_scope: true,
        created_at: "bad-date",
      },
    ] as unknown as Awaited<ReturnType<typeof request>>);

    const rows = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");

    expect(rows[0]).toMatchObject({
      id: "key-0",
      name: "",
      username: "",
      fingerprint: "",
      broadScope: true,
    });
  });

  it("sends scope metadata and RFC3339 expiry when creating a key", async () => {
    requestMock.mockResolvedValueOnce({
      id: 8,
      name: "ops-key",
      username: "deploy",
      key_type: "auto",
      fingerprint: "SHA256:test",
      disabled: false,
      broad_scope: true,
      created_at: "2026-05-18T00:00:00Z",
    });

    await createSSHKeysApi().createSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", {
      name: "ops-key",
      username: "deploy",
      keyType: "auto",
      privateKey: "  FAKE_PRIVATE_KEY_FOR_TEST_ONLY  ",
      disabled: false,
      expiresAt: "2026-06-01T10:30",
      allowedPurposes: "terminal",
      allowedNodeIds: "1",
      allowedNodeTags: "prod",
    });

    expect(requestMock).toHaveBeenCalledWith("/ssh-keys", {
      method: "POST",
      token: "FAKE_TOKEN_FOR_TEST_ONLY",
      body: expect.objectContaining({
        disabled: false,
        expires_at: expect.stringMatching(/^2026-06-01T/),
        allowed_purposes: "terminal",
        allowed_node_ids: "1",
        allowed_node_tags: "prod",
        private_key: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      }),
    });
  });
});
