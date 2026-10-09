import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";
import { SSHKeyRotationWizard } from "./ssh-key-rotation-wizard";

const { toastSuccess, toastError } = vi.hoisted(() => ({
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ role: "admin" as const, token: "token", authTransitioning: false }),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: toastSuccess, error: toastError },
}));

const selectedKey: SSHKeyRecord = {
  id: "key-1",
  name: "生产密钥",
  username: "root",
  keyType: "ed25519",
  fingerprint: "SHA256:old",
  broadScope: false,
  disabled: false,
  expiresAt: "",
  allowedPurposes: "probe",
  allowedNodeIds: "",
  allowedNodeTags: "",
  createdAt: "2026-01-01 00:00:00",
  lastUsedAt: undefined,
};

const affected: NodeRecord[] = [
  {
    id: 1,
    name: "node-online",
    host: "node-online.example",
    address: "node-online.example",
    ip: "node-online.example",
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: [],
    status: "online",
    lastSeenAt: "",
    lastBackupAt: "",
  },
  {
    id: 2,
    name: "node-offline",
    host: "node-offline.example",
    address: "node-offline.example",
    ip: "node-offline.example",
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: [],
    status: "offline",
    lastSeenAt: "",
    lastBackupAt: "",
  },
];

function jsonResponse(status: number, body: unknown) {
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: { get: vi.fn().mockReturnValue(null) },
    text: vi.fn().mockResolvedValue(JSON.stringify(body)),
  } as unknown as Response;
}

const previewBody = {
  code: 0,
  message: "ok",
  data: {
    key_type: "ed25519",
    public_key: "ssh-ed25519 AAAA_CANDIDATE",
    public_key_fingerprint: "SHA256:preview",
  },
};

describe("rotation wizard unusable envelopes", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    toastSuccess.mockReset();
    toastError.mockReset();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  async function confirmRotation(user: UserEvent) {
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: "检查候选密钥" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 2 以确认预估节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
  }

  function renderWizard() {
    return render(
      <MemoryRouter>
        <SSHKeyRotationWizard
          open
          onOpenChange={vi.fn()}
          sshKeys={[selectedKey]}
          keyUsageMap={new Map([[selectedKey.id, affected]])}
          preselectedKey={selectedKey}
          token="token"
          onComplete={vi.fn()}
        />
      </MemoryRouter>,
    );
  }

  function rotateCalls() {
    return fetchMock.mock.calls.filter((call) => String(call[0]).endsWith("/ssh-keys/1/rotate"));
  }

  it.each([
    ["null code", { code: null, message: "bad", data: null }],
    ["string code", { code: "nope", message: "bad", data: null }],
    ["mismatched code", { code: 400, message: "bad", data: null }],
  ])("clears the draft and does not offer retry when HTTP 200 has %s", async (label, body) => {
    fetchMock.mockImplementation((url: string) => {
      if (String(url).endsWith("/ssh-keys/preview")) return Promise.resolve(jsonResponse(200, previewBody));
      if (String(url).endsWith("/ssh-keys/1/rotate")) return Promise.resolve(jsonResponse(200, body));
      return Promise.reject(new Error(`unexpected ${String(url)}`));
    });
    const user = userEvent.setup();
    renderWizard();
    await confirmRotation(user);

    expect(await screen.findByText("无法确认是否已保存。请到密钥页核对当前公钥指纹，不要重试本次提交。")).toBeInTheDocument();
    expect(screen.getByTestId("rotation-summary")).toHaveAttribute("data-rotation-status", "unknown");
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "cleared");
    expect(screen.queryByRole("button", { name: "返回修改并重新检查" })).not.toBeInTheDocument();
    expect(screen.queryByText("bad")).not.toBeInTheDocument();
    expect(screen.queryByText(label)).not.toBeInTheDocument();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(rotateCalls()).toHaveLength(1);
  });

  it("keeps the draft when the rotate request is a real HTTP 400", async () => {
    fetchMock.mockImplementation((url: string) => {
      if (String(url).endsWith("/ssh-keys/preview")) return Promise.resolve(jsonResponse(200, previewBody));
      if (String(url).endsWith("/ssh-keys/1/rotate")) {
        return Promise.resolve(jsonResponse(400, { code: 400, message: "nope", data: null }));
      }
      return Promise.reject(new Error(`unexpected ${String(url)}`));
    });
    const user = userEvent.setup();
    renderWizard();
    await confirmRotation(user);

    expect(await screen.findByText("轮换请求被拒绝，原密钥未替换。")).toBeInTheDocument();
    expect(screen.queryByText("nope")).not.toBeInTheDocument();
    expect(screen.getByTestId("rotation-summary")).toHaveAttribute("data-rotation-status", "failed");
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "present");
    expect(screen.getByRole("button", { name: "返回修改并重新检查" })).toBeInTheDocument();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(rotateCalls()).toHaveLength(1);
  });
});
