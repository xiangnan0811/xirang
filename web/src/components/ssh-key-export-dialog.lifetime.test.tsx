import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { STEP_UP_ACTIONS } from "@/lib/api/totp-api";
import type { SSHKeyRecord } from "@/types/domain";
import { SSHKeyExportDialog } from "./ssh-key-export-dialog";

const { toastError, ensureStepUpProof, clearStepUpProof, authRef } = vi.hoisted(() => {
  const ensureStepUpProof = vi.fn(async () => "proof-1");
  const clearStepUpProof = vi.fn();
  const authRef = {
    current: {
      role: "admin" as "admin" | "operator" | "viewer" | null,
      token: "token-a" as string | null,
      totpEnabled: true,
      authTransitioning: false,
      ensureStepUpProof,
      clearStepUpProof,
    },
  };
  return {
    toastError: vi.fn(),
    ensureStepUpProof,
    clearStepUpProof,
    authRef,
  };
});

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: vi.fn(),
    error: (...args: unknown[]) => toastError(...args),
  },
}));

const sampleKey: SSHKeyRecord = {
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

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function stepUpResponse(): Response {
  const detail = {
    code: 403,
    message: "需要二次验证",
    data: { error_code: "STEP_UP_REQUIRED", proof_ttl_seconds: 300 },
  };
  return {
    ok: false,
    status: 403,
    clone: () => ({ json: async () => detail }),
    json: async () => detail,
    blob: vi.fn(),
  } as unknown as Response;
}

function okResponse(blobBody = "ssh-ed25519 AAAA"): Response {
  return {
    ok: true,
    status: 200,
    blob: async () => new Blob([blobBody]),
    clone() { return this; },
    json: async () => null,
  } as unknown as Response;
}

function ExportHarness({ token = "token-a", open = true }: { token?: string; open?: boolean }) {
  return (
    <SSHKeyExportDialog
      open={open}
      onOpenChange={vi.fn()}
      sshKeys={[sampleKey]}
      selectedKeyIds={[]}
      stats={{ total: 1, inUse: 0 }}
      token={token}
    />
  );
}

describe("SSHKeyExportDialog lifetime", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    authRef.current.role = "admin";
    authRef.current.token = "token-a";
    authRef.current.totpEnabled = true;
    authRef.current.authTransitioning = false;
    ensureStepUpProof.mockClear();
    ensureStepUpProof.mockResolvedValue("proof-1");
    clearStepUpProof.mockClear();
    toastError.mockClear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    if (typeof URL.createObjectURL !== "function") {
      Object.defineProperty(URL, "createObjectURL", { configurable: true, writable: true, value: () => "blob:ssh-export" });
    }
    if (typeof URL.revokeObjectURL !== "function") {
      Object.defineProperty(URL, "revokeObjectURL", { configurable: true, writable: true, value: () => undefined });
    }
    vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:ssh-export");
    vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => undefined);
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("当前身份收到 STEP_UP_REQUIRED 时仍发起二次验证并下载", async () => {
    const user = userEvent.setup();
    fetchMock.mockResolvedValueOnce(stepUpResponse()).mockResolvedValueOnce(okResponse());
    render(<ExportHarness />);

    await user.click(screen.getByRole("button", { name: "下载文件" }));

    await waitFor(() => expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.sshKeyExport));
    await waitFor(() => expect(URL.createObjectURL).toHaveBeenCalled());
    expect(HTMLAnchorElement.prototype.click).toHaveBeenCalled();
    expect(fetchMock).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.objectContaining({
        headers: expect.objectContaining({
          Authorization: "Bearer token-a",
          "X-Xirang-Step-Up": "proof-1",
        }),
      }),
    );
    expect(toastError).not.toHaveBeenCalled();
  });

  it("请求返回前角色 A-B-A 时，过期的 STEP_UP_REQUIRED 不会打开验证或下载", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<Response>();
    fetchMock.mockReturnValue(pending.promise);
    const view = render(<ExportHarness />);
    const pendingClick = user.click(screen.getByRole("button", { name: "下载文件" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    authRef.current.role = "viewer";
    view.rerender(<ExportHarness />);
    authRef.current.role = "admin";
    view.rerender(<ExportHarness />);
    await act(async () => {
      pending.resolve(stepUpResponse());
    });
    await pendingClick;

    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("关闭、卸载或令牌变化后，不会创建导出 blob 或提示失败", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<Response>();
    fetchMock.mockReturnValue(pending.promise);
    const view = render(<ExportHarness />);
    const pendingClick = user.click(screen.getByRole("button", { name: "下载文件" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    view.rerender(<ExportHarness open={false} />);
    await act(async () => {
      pending.resolve(okResponse());
    });
    await pendingClick;
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
    view.unmount();

    const late = createDeferred<Response>();
    fetchMock.mockReset();
    fetchMock.mockReturnValue(late.promise);
    const mounted = render(<ExportHarness />);
    const secondClick = user.click(screen.getByRole("button", { name: "下载文件" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    mounted.unmount();
    await act(async () => {
      late.resolve(stepUpResponse());
    });
    await secondClick;
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(URL.createObjectURL).not.toHaveBeenCalled();

    const blobPending = createDeferred<Blob>();
    let blobStarted = false;
    fetchMock.mockReset();
    fetchMock.mockResolvedValue({
      ok: true,
      status: 200,
      blob: () => {
        blobStarted = true;
        return blobPending.promise;
      },
      clone() { return this; },
      json: async () => null,
    });
    const blobView = render(<ExportHarness token="token-a" />);
    const blobClick = user.click(screen.getByRole("button", { name: "下载文件" }));
    await waitFor(() => expect(blobStarted).toBe(true));
    blobView.rerender(<ExportHarness token="token-b" />);
    blobView.rerender(<ExportHarness token="token-a" />);
    await act(async () => {
      blobPending.resolve(new Blob(["late"]));
    });
    await blobClick;
    expect(URL.createObjectURL).not.toHaveBeenCalled();
    expect(HTMLAnchorElement.prototype.click).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("does not download or request proof when two-factor authentication is disabled", async () => {
    authRef.current.totpEnabled = false;
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <ExportHarness />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("button", { name: "下载文件" }));

    expect(fetchMock).not.toHaveBeenCalled();
    expect(ensureStepUpProof).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: /stepUp.enableTOTP|启用两步验证|Enable two-factor/ })).toHaveAttribute(
      "href",
      "/app/settings?tab=account",
    );
  });
});
