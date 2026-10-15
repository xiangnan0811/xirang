import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, isStepUpRequiredError } from "@/lib/api/core";
import { fetchSSHKeyExportFile } from "@/lib/api/ssh-keys-api";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { SSHKeyRecord } from "@/types/domain";
import { SSHKeyExportDialog } from "./ssh-key-export-dialog";

const { useStepUpActionMock } = vi.hoisted(() => ({
  useStepUpActionMock: vi.fn(() => vi.fn()),
}));

vi.mock("@/hooks/use-step-up-action", () => ({
  useStepUpAction: useStepUpActionMock,
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    role: "admin" as const,
    token: "token",
    ensureStepUpProof: vi.fn(),
    clearStepUpProof: vi.fn(),
  }),
}));

function createMockResponse(status = 200, body = "") {
  return {
    status,
    ok: status >= 200 && status < 300,
    clone: () => createMockResponse(status, body),
    json: vi.fn().mockResolvedValue(body ? JSON.parse(body) : null),
  } as unknown as Response;
}

function createPreviewKey(overrides: Partial<SSHKeyRecord> = {}): SSHKeyRecord {
  return {
    id: "fixture",
    name: "fixture",
    username: "fixture-user",
    keyType: "ed25519",
    publicKey: "ssh-ed25519 FAKE_PUBLIC_KEY_FOR_TEST_ONLY",
    fingerprint: "SHA256:legacy",
    publicKeyFingerprint: "SHA256:standard",
    disabled: false,
    expiresAt: "",
    allowedPurposes: "",
    allowedNodeIds: "",
    allowedNodeTags: "",
    broadScope: false,
    createdAt: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

function renderPreview(keys: SSHKeyRecord[]) {
  return render(
    <SSHKeyExportDialog
      open
      onOpenChange={vi.fn()}
      sshKeys={keys}
      selectedKeyIds={[]}
      stats={{ total: keys.length, inUse: 0 }}
      token="FAKE_AUTH_TOKEN_FOR_TEST_ONLY"
    />,
  );
}

function previewText(): string {
  return document.querySelector("pre")?.textContent ?? "";
}

describe("fetchSSHKeyExportFile", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    useStepUpActionMock.mockClear();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockReset();
  });

  it("direct download 会附加 bearer token 和 step-up proof", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(200));

    await fetchSSHKeyExportFile("/api/v1/ssh-keys/export?format=json&scope=all", "token-1", "proof-1");

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/ssh-keys/export?format=json&scope=all", {
      headers: {
        Authorization: "Bearer token-1",
        "X-Xirang-Step-Up": "proof-1",
      },
    });
  });

  it("binds SSH key export to its exact step-up action", () => {
    render(
      <SSHKeyExportDialog
        open
        onOpenChange={vi.fn()}
        sshKeys={[]}
        selectedKeyIds={[]}
        stats={{ total: 0, inUse: 0 }}
        token="FAKE_AUTH_TOKEN_FOR_TEST_ONLY"
      />,
    );

    expect(useStepUpActionMock).toHaveBeenCalledWith(STEP_UP_ACTIONS.sshKeyExport);
  });

  it("direct download 会保留 STEP_UP_REQUIRED envelope 供 prompt/retry 识别", async () => {
    fetchMock.mockResolvedValueOnce(createMockResponse(403, JSON.stringify({
      code: 403,
      message: "需要二次验证",
      data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
    })));

    let captured: unknown;
    try {
      await fetchSSHKeyExportFile("/api/v1/ssh-keys/export?format=json&scope=all", "token-1");
    } catch (error) {
      captured = error;
    }

    expect(captured).toBeInstanceOf(ApiError);
    expect(isStepUpRequiredError(captured)).toBe(true);
  });

  it("keeps the legacy digest separate from the standard fingerprint in machine previews", async () => {
    const user = userEvent.setup();
    const key: SSHKeyRecord = {
      id: "key-1",
      name: "生产密钥",
      username: "root",
      keyType: "ed25519",
      publicKey: "ssh-ed25519 AAAA",
      fingerprint: "SHA256:abc",
      publicKeyFingerprint: "SHA256:standard-public",
      disabled: false,
      expiresAt: "",
      allowedPurposes: "",
      allowedNodeIds: "",
      allowedNodeTags: "",
      broadScope: false,
      createdAt: "2026-01-01 00:00:00",
    };
    render(
      <SSHKeyExportDialog
        open
        onOpenChange={vi.fn()}
        sshKeys={[key]}
        selectedKeyIds={[]}
        stats={{ total: 1, inUse: 0 }}
        token="token"
      />,
    );

    const preview = () => document.querySelector("pre")?.textContent ?? "";
    expect(screen.getByText(/sshKeys\.exportAuthorizedKeysFieldNote|authorized_keys 仅包含公钥/)).toBeInTheDocument();
    expect(preview()).toBe("ssh-ed25519 AAAA 生产密钥");
    expect(preview()).not.toMatch(/^\s*#/m);

    await user.click(screen.getByRole("button", { name: /JSON/ }));
    expect(screen.getByText(/sshKeys\.exportDigestFieldNote|fingerprint 字段是私钥摘要/)).toBeInTheDocument();
    expect(JSON.parse(preview())).toEqual([{
      name: key.name,
      fingerprint: key.fingerprint,
      public_key: key.publicKey,
      public_key_fingerprint: key.publicKeyFingerprint,
      created_at: key.createdAt,
    }]);
    expect(preview()).not.toMatch(/^\s*#/m);

    await user.click(screen.getByRole("button", { name: /CSV/ }));
    expect(preview().split("\n")[0]).toBe("name,fingerprint,public_key,created_at,public_key_fingerprint");
    expect(preview().split("\n")[1]).toBe('"生产密钥","SHA256:abc","ssh-ed25519 AAAA","2026-01-01 00:00:00","SHA256:standard-public"');
    expect(preview()).not.toMatch(/^\s*#/m);
  });

  it("quotes every CSV field and escapes quotes while preserving commas and real newlines", async () => {
    const user = userEvent.setup();
    const key = createPreviewKey({
      name: 'fixture, "quoted"\nsecond line',
      fingerprint: 'SHA256:legacy"digest',
      publicKey: 'ssh-ed25519 FAKE_PUBLIC_KEY_FOR_TEST_ONLY comment,"quoted"',
      publicKeyFingerprint: 'SHA256:standard"fingerprint',
    });
    renderPreview([key]);

    await user.click(screen.getByRole("button", { name: /CSV/ }));

    expect(previewText()).toBe([
      "name,fingerprint,public_key,created_at,public_key_fingerprint",
      '"fixture, ""quoted""\nsecond line","SHA256:legacy""digest","ssh-ed25519 FAKE_PUBLIC_KEY_FOR_TEST_ONLY comment,""quoted""","2026-01-01T00:00:00Z","SHA256:standard""fingerprint"',
    ].join("\n"));
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });

  it("omits unavailable JSON fingerprints, leaves CSV cells empty and never invents authorized keys", async () => {
    const user = userEvent.setup();
    const keys = [
      createPreviewKey({ id: "unknown", name: "unknown", publicKey: undefined, publicKeyFingerprint: undefined }),
      createPreviewKey({ id: "empty", name: "empty", publicKey: "", publicKeyFingerprint: "" }),
      createPreviewKey({ id: "real", name: "real" }),
      createPreviewKey({ id: "outside-sample", name: "outside-sample" }),
    ];
    renderPreview(keys);

    expect(previewText()).toBe(`${keys[2].publicKey} real`);
    expect(previewText()).not.toContain("AAAA...");
    expect(previewText()).not.toContain("outside-sample");
    expect(previewText()).not.toMatch(/^\s*#/m);
    expect(screen.getByText(/预览仅包含已加载记录的部分字段样本/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /JSON/ }));
    const json: Array<Record<string, unknown>> = JSON.parse(previewText());
    expect(json).toHaveLength(3);
    expect(json[0]).toEqual({
      name: "unknown", fingerprint: keys[0].fingerprint, public_key: "", created_at: keys[0].createdAt,
    });
    expect(json[1]).not.toHaveProperty("public_key_fingerprint");
    expect(json[2]).toHaveProperty("public_key_fingerprint", keys[2].publicKeyFingerprint);

    await user.click(screen.getByRole("button", { name: /CSV/ }));
    const rows = previewText().split("\n");
    expect(rows).toHaveLength(4);
    expect(rows[1]).toBe('"unknown","SHA256:legacy","","2026-01-01T00:00:00Z",""');
    expect(rows[2]).toBe('"empty","SHA256:legacy","","2026-01-01T00:00:00Z",""');
    expect(globalThis.fetch).not.toHaveBeenCalled();
  });
});
