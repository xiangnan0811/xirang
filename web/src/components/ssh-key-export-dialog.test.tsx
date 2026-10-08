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

  it("explains the private-key digest without changing download fields", async () => {
    const user = userEvent.setup();
    const key: SSHKeyRecord = {
      id: "key-1",
      name: "生产密钥",
      username: "root",
      keyType: "ed25519",
      publicKey: "ssh-ed25519 AAAA",
      fingerprint: "SHA256:abc",
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
    expect(preview()).toBe("ssh-ed25519 AAAA");
    expect(preview()).not.toMatch(/^\s*#/m);

    await user.click(screen.getByRole("button", { name: /JSON/ }));
    expect(screen.getByText(/sshKeys\.exportDigestFieldNote|fingerprint 字段是私钥摘要/)).toBeInTheDocument();
    expect(preview()).toContain('"fingerprint": "SHA256:abc"');
    expect(preview()).toContain('"public_key": "ssh-ed25519 AAAA"');
    expect(preview()).not.toContain("public_key_fingerprint");
    expect(preview()).not.toMatch(/^\s*#/m);

    await user.click(screen.getByRole("button", { name: /CSV/ }));
    expect(preview().split("\n")[0]).toBe("name,fingerprint,public_key,created_at");
    expect(preview()).toContain('"SHA256:abc"');
    expect(preview()).not.toMatch(/^\s*#/m);
  });
});
