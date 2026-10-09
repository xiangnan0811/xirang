import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NewSSHKeyInput } from "@/types/domain";
import { createSSHKeysApi, decodeSSHKeyRotationResult, rotateSSHKey, SSHKeyRotationDecodeError } from "./ssh-keys-api";
import { ApiError, request } from "./core";

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

  it("keeps a retired-only purpose list instead of mapping it to empty", async () => {
    requestMock.mockResolvedValueOnce([
      {
        id: 11,
        name: "legacy-probe-key",
        username: "deploy",
        key_type: "ed25519",
        fingerprint: "SHA256:legacy",
        allowed_purposes: " probe , node_logs ",
        broad_scope: true,
        created_at: "2026-05-18T00:00:00Z",
      },
    ]);

    const rows = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");
    expect(rows[0]?.allowedPurposes).toBe("probe,node_logs");

    requestMock.mockResolvedValueOnce({
      id: 11,
      name: "legacy-probe-key",
      username: "deploy",
      key_type: "ed25519",
      fingerprint: "SHA256:legacy",
      allowed_purposes: "probe,node_logs",
      created_at: "2026-05-18T00:00:00Z",
    });
    await createSSHKeysApi().updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-11", {
      name: "legacy-probe-key",
      username: "deploy",
      keyType: "ed25519",
      privateKey: "",
      disabled: false,
      expiresAt: "",
      allowedPurposes: " probe , node_logs ",
      allowedNodeIds: "",
      allowedNodeTags: "",
    });
    expect(requestMock).toHaveBeenLastCalledWith("/ssh-keys/11", expect.objectContaining({
      method: "PUT",
      body: expect.objectContaining({
        allowed_purposes: "probe,node_logs",
      }),
    }));
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

  it("maps a real public fingerprint without replacing the private digest", async () => {
    requestMock.mockResolvedValueOnce([
      {
        id: 3,
        name: "ops-key",
        username: "deploy",
        key_type: "ed25519",
        fingerprint: "SHA256:private-digest",
        public_key_fingerprint: "SHA256:public",
        created_at: "2026-05-18T00:00:00Z",
      },
    ]);

    const rows = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");

    expect(rows[0]?.fingerprint).toBe("SHA256:private-digest");
    expect(rows[0]?.publicKeyFingerprint).toBe("SHA256:public");
  });

  it("leaves the public fingerprint absent when the response omits it or sends a blank value", async () => {
    requestMock.mockResolvedValueOnce([
      {
        id: 4,
        name: "ops-key",
        username: "deploy",
        fingerprint: "SHA256:private-digest",
        public_key_fingerprint: "   ",
        created_at: "2026-05-18T00:00:00Z",
      },
    ]);

    const blank = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");
    expect(blank[0]?.fingerprint).toBe("SHA256:private-digest");
    expect(blank[0]?.publicKeyFingerprint).toBeUndefined();

    requestMock.mockResolvedValueOnce([
      {
        id: 5,
        name: "ops-key",
        username: "deploy",
        fingerprint: "SHA256:private-digest",
        public_key_fingerprint: 12,
        created_at: "2026-05-18T00:00:00Z",
      },
    ] as unknown as Awaited<ReturnType<typeof request>>);
    const invalid = await createSSHKeysApi().getSSHKeys("FAKE_TOKEN_FOR_TEST_ONLY");
    expect(invalid[0]?.publicKeyFingerprint).toBeUndefined();
    expect(invalid[0]?.fingerprint).toBe("SHA256:private-digest");
  });

  it("loads one key through GET", async () => {
    const signal = new AbortController().signal;
    requestMock.mockResolvedValueOnce({
      id: 4,
      name: "ops-key",
      username: "deploy",
      key_type: "rsa",
      fingerprint: "SHA256:private-digest",
      public_key_fingerprint: "SHA256:public",
      created_at: "2026-05-18T00:00:00Z",
    });

    const row = await createSSHKeysApi().getSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", { signal });

    expect(requestMock).toHaveBeenCalledWith("/ssh-keys/4", {
      token: "FAKE_TOKEN_FOR_TEST_ONLY",
      signal,
    });
    expect(row.fingerprint).toBe("SHA256:private-digest");
    expect(row.publicKeyFingerprint).toBe("SHA256:public");
    expect(row).not.toHaveProperty("privateKey");
  });

  it("posts a candidate preview and maps only a concrete result", async () => {
    const signal = new AbortController().signal;
    requestMock.mockResolvedValueOnce({
      key_type: "ed25519",
      public_key: " ssh-ed25519 AAAA ",
      public_key_fingerprint: " SHA256:public ",
      fingerprint: "SHA256:private-digest",
    });

    const preview = await createSSHKeysApi().previewSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", {
      privateKey: "SECRET",
      keyType: "ed25519",
    }, { signal });

    expect(requestMock).toHaveBeenCalledWith("/ssh-keys/preview", {
      method: "POST",
      token: "FAKE_TOKEN_FOR_TEST_ONLY",
      signal,
      body: { private_key: "SECRET", key_type: "ed25519" },
    });
    expect(preview).toEqual({
      keyType: "ed25519",
      publicKey: "ssh-ed25519 AAAA",
      publicKeyFingerprint: "SHA256:public",
    });
  });

  it("rejects a preview that does not include a real type, public key, and public fingerprint", async () => {
    const api = createSSHKeysApi();
    const cases = [
      { key_type: "auto", public_key: "ssh-ed25519 AAAA", public_key_fingerprint: "SHA256:public", fingerprint: "SHA256:private-digest" },
      { public_key: "ssh-ed25519 AAAA", public_key_fingerprint: "SHA256:public" },
      { key_type: "rsa", public_key_fingerprint: "SHA256:public", fingerprint: "SHA256:private-digest" },
      { key_type: "rsa", public_key: "ssh-rsa AAAA", fingerprint: "SHA256:private-digest" },
      { key_type: "rsa", public_key: "   ", public_key_fingerprint: "SHA256:public" },
      { key_type: "rsa", public_key: "ssh-rsa AAAA", public_key_fingerprint: "   " },
    ];

    for (const body of cases) {
      requestMock.mockResolvedValueOnce(body);
      await expect(api.previewSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", {
        privateKey: "SECRET",
        keyType: "auto",
      })).rejects.toBeInstanceOf(ApiError);
    }
  });

  function sshKeyRow(expiresAt: string | null) {
    return {
      id: 21,
      name: "ops-key",
      username: "deploy",
      key_type: "ed25519" as const,
      fingerprint: "SHA256:test",
      disabled: false,
      expires_at: expiresAt,
      allowed_purposes: "terminal",
      allowed_node_ids: "1",
      allowed_node_tags: "prod",
      broad_scope: false,
      created_at: "2026-05-18T00:00:00Z",
    };
  }

  function putBody(): Record<string, unknown> {
    const body = requestMock.mock.calls.at(-1)?.[1]?.body;
    expect(body).toEqual(expect.any(Object));
    return body as Record<string, unknown>;
  }

  it("omits expires_at when an update supplies no expiry, so a fresh 2026-11-01T06:30:45Z read is not folded back", async () => {
    const sourceExpiry = "2026-11-01T06:30:45Z";
    const api = createSSHKeysApi();
    requestMock.mockResolvedValueOnce(sshKeyRow(sourceExpiry));

    const fresh = await api.getSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-21");
    const displayed = fresh.expiresAt ?? "";
    expect(displayed).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
    expect(displayed).not.toMatch(/(?:Z|[+-]\d{2}:\d{2})$/);

    requestMock.mockResolvedValueOnce(sshKeyRow(sourceExpiry));
    await api.updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", fresh.id, {
      name: fresh.name,
      username: fresh.username,
      keyType: fresh.keyType,
      privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      disabled: fresh.disabled,
      expiresAt: displayed,
      allowedPurposes: fresh.allowedPurposes,
      allowedNodeIds: fresh.allowedNodeIds,
      allowedNodeTags: fresh.allowedNodeTags,
    });
    const folded = putBody();
    const roundTripMs = Date.parse(String(folded.expires_at));
    const sourceMs = Date.parse(sourceExpiry);
    expect(Number.isNaN(roundTripMs)).toBe(false);
    expect(Math.abs(roundTripMs - sourceMs) % 60_000).toBe(45_000);
    expect(new Date(roundTripMs).getSeconds()).toBe(0);

    requestMock.mockResolvedValueOnce(sshKeyRow(sourceExpiry));
    await api.updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", fresh.id, {
      name: fresh.name,
      username: fresh.username,
      keyType: fresh.keyType,
      privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      disabled: fresh.disabled,
      allowedPurposes: fresh.allowedPurposes,
      allowedNodeIds: fresh.allowedNodeIds,
      allowedNodeTags: fresh.allowedNodeTags,
    });
    const rotation = putBody();
    expect(requestMock.mock.calls.at(-1)?.[0]).toBe("/ssh-keys/21");
    expect(Object.hasOwn(rotation, "expires_at")).toBe(false);
    expect(rotation).toMatchObject({
      name: "ops-key",
      username: "deploy",
      key_type: "ed25519",
      private_key: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      disabled: false,
      allowed_purposes: "terminal",
      allowed_node_ids: "1",
      allowed_node_tags: "prod",
    });
  });

  it("clears expires_at when an editor update sets an explicit empty or null expiry", async () => {
    const api = createSSHKeysApi();
    for (const expiresAt of ["", null] as const) {
      requestMock.mockResolvedValueOnce(sshKeyRow(null));
      await api.updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-21", {
        name: "ops-key",
        username: "deploy",
        keyType: "ed25519",
        privateKey: "",
        disabled: false,
        expiresAt,
        allowedPurposes: "terminal",
        allowedNodeIds: "1",
        allowedNodeTags: "prod",
      } as NewSSHKeyInput);
      expect(putBody()).toMatchObject({
        disabled: false,
        expires_at: null,
        allowed_purposes: "terminal",
        allowed_node_ids: "1",
        allowed_node_tags: "prod",
      });
    }
  });

  it("converts an explicit new local expiry on update", async () => {
    const localExpiry = "2026-12-15T18:45";
    requestMock.mockResolvedValueOnce(sshKeyRow(null));
    await createSSHKeysApi().updateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-21", {
      name: "ops-key",
      username: "deploy",
      keyType: "ed25519",
      privateKey: "",
      disabled: false,
      expiresAt: localExpiry,
      allowedPurposes: "terminal",
      allowedNodeIds: "1",
      allowedNodeTags: "prod",
    });
    expect(putBody().expires_at).toBe(new Date(localExpiry).toISOString());
  });
});

function verifiedNode(id = 1, name = "node-a") {
  return { node_id: id, name, status: "verified" };
}

describe("ssh key rotation decoder", () => {
  beforeEach(() => {
    requestMock.mockReset();
  });

  it("posts one rotate request and decodes a saved result only when every node is verified", async () => {
    const signal = new AbortController().signal;
    requestMock.mockResolvedValueOnce({
      status: "saved",
      reason: "",
      public_key_fingerprint: " SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y ",
      results: [verifiedNode(4, "edge")],
    });

    const result = await rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", {
      privateKey: "SECRET",
      keyType: "ed25519",
      name: "  renamed  ",
    }, { signal });

    expect(requestMock).toHaveBeenCalledTimes(1);
    expect(requestMock).toHaveBeenCalledWith("/ssh-keys/4/rotate", {
      method: "POST",
      token: "FAKE_TOKEN_FOR_TEST_ONLY",
      signal,
      body: { private_key: "SECRET", key_type: "ed25519", name: "renamed" },
    });
    expect(result).toEqual({
      status: "saved",
      reason: "",
      publicKeyFingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y",
      results: [{ nodeId: "node-4", name: "edge", status: "verified" }],
    });
  });

  it("omits a blank name and key type instead of sending them", async () => {
    requestMock.mockResolvedValueOnce({
      status: "saved",
      reason: "",
      public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y",
      results: [],
    });

    await rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", {
      privateKey: "SECRET",
      name: "   ",
    });

    expect(requestMock).toHaveBeenCalledWith("/ssh-keys/4/rotate", expect.objectContaining({
      body: { private_key: "SECRET" },
    }));
  });

  it("rejects contradictions and malformed rotation payloads", () => {
    const cases = [
      { status: "saved", reason: "validation_failed", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: 1, name: "a", status: "failed", error_code: "connection_failed" }] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: 1, name: "a", status: "verified", error_code: "timeout" }] },
      { status: "not_saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "   ", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: 1, name: "a", status: "verified" }, { node_id: 1, name: "b", status: "verified" }] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: 1.5, name: "a", status: "verified" }] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: "1", name: "a", status: "verified" }] },
      { status: "not_saved", reason: "nope", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: 1, name: "a", status: "unknown" }] },
      { status: "not_saved", reason: "conflict", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [{ node_id: 2, name: "a", status: "failed" }] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [], extra: true },
      { status: "saved", reason: "", public_key_fingerprint: "sha256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "MD5:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y=", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7YZ", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Z", results: [] },
      { status: "saved", reason: "", public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3i*RxaIKt/qHJiuiIvfoVHtf7Y", results: [] },
      null,
      [],
    ];

    for (const body of cases) {
      expect(() => decodeSSHKeyRotationResult(body)).toThrow(SSHKeyRotationDecodeError);
    }
  });

  it("keeps a not-saved result authoritative without treating it as saved", () => {
    const result = decodeSSHKeyRotationResult({
      status: "not_saved",
      reason: "scope_blocked",
      public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y",
      results: [
        { node_id: 2, name: "edge", status: "failed", error_code: "scope_denied" },
        { node_id: 3, name: "late", status: "unknown", error_code: "not_checked" },
      ],
    });

    expect(result.status).toBe("not_saved");
    expect(result.results.map((row) => row.nodeId)).toEqual(["node-2", "node-3"]);
    expect(result.results[0]?.errorCode).toBe("scope_denied");
  });

  it("does not report success when rotate returns an invalid body", async () => {
    requestMock.mockResolvedValueOnce({
      status: "saved",
      reason: "",
      public_key_fingerprint: "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y",
      results: [{ node_id: 1, name: "a", status: "failed", error_code: "connection_failed" }],
    });

    await expect(rotateSSHKey("FAKE_TOKEN_FOR_TEST_ONLY", "key-4", {
      privateKey: "SECRET",
      keyType: "ed25519",
    })).rejects.toBeInstanceOf(SSHKeyRotationDecodeError);
    expect(requestMock).toHaveBeenCalledTimes(1);
  });

  it("accepts canonical unpadded SHA256 fingerprints", () => {
    const fingerprints = [
      "SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y",
      "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
      " SHA256:90mMaj7XYS8JZzh6v3iARxaIKt/qHJiuiIvfoVHtf7Y ",
    ];
    for (const fingerprint of fingerprints) {
      expect(decodeSSHKeyRotationResult({
        status: "saved",
        reason: "",
        public_key_fingerprint: fingerprint,
        results: [],
      }).publicKeyFingerprint).toBe(fingerprint.trim());
    }
  });
});
