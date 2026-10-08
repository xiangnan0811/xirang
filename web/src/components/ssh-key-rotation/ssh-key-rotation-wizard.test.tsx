import { StrictMode, useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, bumpAuthSessionGeneration } from "@/lib/api/core";
import type { NodeRecord, SSHKeyPreview, SSHKeyRecord } from "@/types/domain";
import { SSHKeyRotationWizard } from "./ssh-key-rotation-wizard";

const { updateSSHKey, testConnection, previewSSHKey, getSSHKey } = vi.hoisted(() => ({
  updateSSHKey: vi.fn(),
  testConnection: vi.fn(),
  previewSSHKey: vi.fn(),
  getSSHKey: vi.fn(),
}));

vi.mock("@/lib/api/ssh-keys-api", () => ({
  createSSHKeysApi: () => ({
    updateSSHKey,
    testConnection,
    previewSSHKey,
    getSSHKey,
  }),
}));

const { toastSuccess, toastError } = vi.hoisted(() => ({
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const authRef: { current: { role: "admin" | "operator" | "viewer" | null; token: string | null } } = {
  current: { role: "admin", token: "token" },
};

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
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

function node(id: number, status: NodeRecord["status"], name: string): NodeRecord {
  return {
    id,
    name,
    host: `${name}.example`,
    address: `${name}.example`,
    ip: `${name}.example`,
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: [],
    status,
    lastSeenAt: "",
    lastBackupAt: "",
  };
}

const affected = [
  node(1, "online", "node-online"),
  node(2, "offline", "node-offline"),
];

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const checkedPreview: SSHKeyPreview = {
  keyType: "ed25519",
  publicKey: "ssh-ed25519 AAAA_CANDIDATE",
  publicKeyFingerprint: "SHA256:new-public",
};

function textChoice(...parts: string[]) {
  return new RegExp(parts.map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|"));
}

function exactChoice(...parts: string[]) {
  const accepted = new Set(parts);
  return (_content: string, element: Element | null) => accepted.has(element?.textContent?.trim() ?? "");
}

function DisabledKeyOnKeyPage({
  onOpenChange,
  onComplete,
}: {
  onOpenChange: (open: boolean) => void;
  onComplete: () => void;
}) {
  const [open, setOpen] = useState(true);
  const disabledKey: SSHKeyRecord = {
    ...selectedKey,
    id: "key-disabled",
    name: "停用密钥",
    disabled: true,
  };
  return (
    <MemoryRouter initialEntries={["/app/ssh-keys"]}>
      <SSHKeyRotationWizard
        open={open}
        onOpenChange={(nextOpen) => {
          onOpenChange(nextOpen);
          setOpen(nextOpen);
        }}
        sshKeys={[disabledKey]}
        keyUsageMap={new Map([[disabledKey.id, affected]])}
        preselectedKey={disabledKey}
        token="token"
        onComplete={onComplete}
      />
    </MemoryRouter>
  );
}

function wizardElement(token = "token", open = true) {
  return (
    <SSHKeyRotationWizard
      open={open}
      onOpenChange={vi.fn()}
      sshKeys={[selectedKey]}
      keyUsageMap={new Map([[selectedKey.id, affected]])}
      preselectedKey={selectedKey}
      token={token}
      onComplete={vi.fn()}
    />
  );
}

async function confirmRotation(user: UserEvent) {
  await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
  await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
  await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
  const callsBefore = updateSSHKey.mock.calls.length;
  await user.click(screen.getByRole("button", { name: "确认轮换" }));
  await waitFor(() => expect(updateSSHKey.mock.calls.length).toBe(callsBefore + 1));
}

type DeferredRead = {
  aborted: boolean;
  complete: (text: string) => void;
  fail: () => void;
};

function installDelayedFileReader(): DeferredRead[] {
  const pending: DeferredRead[] = [];

  class DelayedFileReader {
    static readonly EMPTY = 0;
    static readonly LOADING = 1;
    static readonly DONE = 2;

    onload: ((this: FileReader, ev: ProgressEvent<FileReader>) => unknown) | null = null;
    onerror: ((this: FileReader, ev: ProgressEvent<FileReader>) => unknown) | null = null;
    readyState = DelayedFileReader.EMPTY;
    aborted = false;

    abort() {
      this.aborted = true;
      this.readyState = DelayedFileReader.DONE;
    }

    readAsText() {
      this.readyState = DelayedFileReader.LOADING;
      pending.push(this);
    }

    complete(text: string) {
      this.readyState = DelayedFileReader.DONE;
      this.onload?.call(
        this as unknown as FileReader,
        { target: { result: text } } as ProgressEvent<FileReader>,
      );
    }

    fail() {
      this.readyState = DelayedFileReader.DONE;
      this.onerror?.call(
        this as unknown as FileReader,
        new ProgressEvent("error") as ProgressEvent<FileReader>,
      );
    }
  }

  vi.stubGlobal("FileReader", DelayedFileReader);
  return pending;
}

function changeKeyFile(name = "id_ed25519") {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [new File(["ignored"], name)] } });
}

describe("SSHKeyRotationWizard", () => {
  beforeEach(() => {
    authRef.current = { role: "admin", token: "token" };
    toastSuccess.mockReset();
    toastError.mockReset();
    updateSSHKey.mockReset();
    testConnection.mockReset();
    previewSSHKey.mockReset();
    getSSHKey.mockReset();
    previewSSHKey.mockResolvedValue(checkedPreview);
    getSSHKey.mockImplementation(async () => ({ ...selectedKey }));
    updateSSHKey.mockResolvedValue({ ...selectedKey, fingerprint: "SHA256:new" });
    testConnection.mockResolvedValue([
      { nodeId: "node-1", name: "node-online", host: "node-online.example", port: 22, success: true, latencyMs: 8 },
      { nodeId: "node-2", name: "node-offline", host: "node-offline.example", port: 22, success: false, latencyMs: 0, error: "refused" },
    ]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("verifies every affected node after rotation, including nodes whose stored status is not online", async () => {
    const user = userEvent.setup();
    const affected = [
      node(1, "online", "node-online"),
      node(2, "offline", "node-offline"),
    ];

    render(
      <SSHKeyRotationWizard
        open
        onOpenChange={vi.fn()}
        sshKeys={[selectedKey]}
        keyUsageMap={new Map([[selectedKey.id, affected]])}
        preselectedKey={selectedKey}
        token="token"
        onComplete={vi.fn()}
      />,
    );

    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(previewSSHKey).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));

    expect(await screen.findByText("node-offline")).toBeInTheDocument();
    expect(testConnection).toHaveBeenCalledWith("token", "key-1", ["node-1", "node-2"]);
    expect(updateSSHKey).toHaveBeenCalledWith("token", "key-1", expect.objectContaining({
      allowedPurposes: "probe",
    }));
    expect(screen.queryByText("跳过（离线）")).not.toBeInTheDocument();
    expect(screen.getByText("验证失败")).toBeInTheDocument();
    expect(screen.getByText("refused")).toBeInTheDocument();
    expect(screen.getByText("SHA256:new-public")).toBeInTheDocument();
    expect(screen.queryByText("SHA256:old")).not.toBeInTheDocument();
    expect(screen.queryByText("SHA256:new")).not.toBeInTheDocument();
    expect(previewSSHKey).toHaveBeenCalledWith("token", {
      privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      keyType: "auto",
    }, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(previewSSHKey.mock.invocationCallOrder[0]).toBeLessThan(updateSSHKey.mock.invocationCallOrder[0]);
    expect(getSSHKey.mock.invocationCallOrder[0]).toBeLessThan(updateSSHKey.mock.invocationCallOrder[0]);
  });

  it("StrictMode 下身份未变时仍测试全部受影响节点", async () => {
    const user = userEvent.setup();
    render(<StrictMode>{wizardElement()}</StrictMode>);
    await confirmRotation(user);

    expect(await screen.findByText("node-offline")).toBeInTheDocument();
    expect(testConnection).toHaveBeenCalledWith("token", "key-1", ["node-1", "node-2"]);
    expect(toastSuccess).toHaveBeenCalled();
  });

  it("轮换更新返回前身份失效后，不提示成功也不测试节点", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRecord>();
    updateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);
    expect(updateSSHKey).toHaveBeenCalledTimes(1);

    authRef.current = { role: "viewer", token: "token" };
    view.rerender(wizardElement());
    await act(async () => {
      pending.resolve({ ...selectedKey, fingerprint: "SHA256:new" });
    });

    expect(testConnection).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText("验证通过")).not.toBeInTheDocument();
  });

  it("令牌 A-B-A 后废弃的轮换更新不会继续测试节点", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRecord>();
    updateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement("token-a"));
    await confirmRotation(user);

    view.rerender(wizardElement("token-b"));
    view.rerender(wizardElement("token-a"));
    await act(async () => {
      pending.resolve({ ...selectedKey, fingerprint: "SHA256:new" });
    });

    expect(testConnection).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("关闭后，迟到的轮换更新不会测试节点", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRecord>();
    updateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);

    view.rerender(wizardElement("token", false));
    await act(async () => {
      pending.resolve({ ...selectedKey, fingerprint: "SHA256:new" });
    });

    expect(testConnection).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("卸载后，迟到的轮换更新不会测试节点", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRecord>();
    updateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);

    view.unmount();
    await act(async () => {
      pending.resolve({ ...selectedKey, fingerprint: "SHA256:new" });
    });

    expect(testConnection).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("更新失败且身份已变时不展示轮换错误，也不测试节点", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRecord>();
    updateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);

    authRef.current = { role: "operator", token: "token" };
    view.rerender(wizardElement());
    await act(async () => {
      pending.reject(new Error("rotation failed late"));
    });

    expect(testConnection).not.toHaveBeenCalled();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText("rotation failed late")).not.toBeInTheDocument();
  });

  it("连接测试进行中身份失效后，不展示验证结果或失败回填", async () => {
    const user = userEvent.setup();
    const pendingTest = createDeferred<unknown>();
    testConnection.mockReturnValue(pendingTest.promise);
    const view = render(wizardElement());
    await confirmRotation(user);
    await waitFor(() => expect(testConnection).toHaveBeenCalledTimes(1));

    authRef.current = { role: "admin", token: "token-b" };
    view.rerender(wizardElement("token-b"));
    await act(async () => {
      pendingTest.reject(new Error("late test"));
    });

    expect(screen.queryByText("验证通过")).not.toBeInTheDocument();
    expect(screen.queryByText("连接失败")).not.toBeInTheDocument();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("当前上传会把密钥文件写入私钥内容", () => {
    const pending = installDelayedFileReader();
    render(wizardElement());

    changeKeyFile();
    act(() => pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY"));

    expect(screen.getByLabelText("私钥内容")).toHaveValue("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("关闭向导后中止读取，并忽略迟到的私钥和读取失败", () => {
    const pending = installDelayedFileReader();
    const view = render(wizardElement());
    changeKeyFile("late.pem");

    view.rerender(wizardElement("token", false));
    expect(pending[0].aborted).toBe(true);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
      pending[0].fail();
    });
    expect(toastError).not.toHaveBeenCalled();

    view.rerender(wizardElement("token", true));
    expect(screen.getByLabelText("私钥内容")).toHaveValue("");
  });

  it("卸载向导后忽略迟到的私钥读取和读取失败", () => {
    const pending = installDelayedFileReader();
    const view = render(wizardElement());
    changeKeyFile("late.pem");

    view.unmount();
    expect(pending[0].aborted).toBe(true);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
      pending[0].fail();
    });
    expect(toastError).not.toHaveBeenCalled();
  });

  it("令牌 A-B-A 后忽略旧的密钥读取，新的上传仍然生效", () => {
    const pending = installDelayedFileReader();
    const view = render(wizardElement("token-a"));
    changeKeyFile("late.pem");

    view.rerender(wizardElement("token-b"));
    view.rerender(wizardElement("token-a"));
    expect(pending[0].aborted).toBe(true);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
      pending[0].fail();
    });
    expect(screen.getByLabelText("私钥内容")).toHaveValue("");
    expect(toastError).not.toHaveBeenCalled();

    changeKeyFile("fresh.pem");
    act(() => pending[1].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH"));
    expect(screen.getByLabelText("私钥内容")).toHaveValue("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("角色回到管理员后仍忽略降权期间未完成的密钥读取", () => {
    const pending = installDelayedFileReader();
    const view = render(wizardElement());
    changeKeyFile("late.pem");

    authRef.current = { role: "viewer", token: "token" };
    view.rerender(wizardElement());
    authRef.current = { role: "admin", token: "token" };
    view.rerender(wizardElement());

    expect(pending[0].aborted).toBe(true);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
      pending[0].fail();
    });
    expect(screen.getByLabelText("私钥内容")).toHaveValue("");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("修改候选或返回确认前会作废旧检查，迟到的预览不能恢复", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyPreview>();
    previewSSHKey.mockReturnValueOnce(pending.promise);
    render(wizardElement());

    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(previewSSHKey).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await user.type(screen.getByLabelText("私钥内容"), "X");
    await act(async () => {
      pending.resolve(checkedPreview);
    });

    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    expect(screen.queryByText("SHA256:new-public")).not.toBeInTheDocument();
    expect(updateSSHKey).not.toHaveBeenCalled();
  });

  it("返回编辑、关闭和认证代次变化都会丢掉候选检查", async () => {
    const user = userEvent.setup();
    const view = render(wizardElement());
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    expect(screen.getByText("SHA256:new-public")).toBeInTheDocument();
    expect(screen.queryByText("SHA256:old")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "上一步" }));
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    expect(screen.queryByText("SHA256:new-public")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    bumpAuthSessionGeneration();
    view.rerender(wizardElement());
    expect(screen.queryByText("SHA256:new-public")).not.toBeInTheDocument();
    expect(screen.getByLabelText("私钥内容")).toHaveValue("");
  });

  it("禁用密钥可见但不可选，预选禁用密钥不能跳过，零节点密钥可以进入上传", async () => {
    const user = userEvent.setup();
    const disabledKey: SSHKeyRecord = { ...selectedKey, id: "key-disabled", name: "停用密钥", disabled: true };
    const idleKey: SSHKeyRecord = { ...selectedKey, id: "key-idle", name: "空闲密钥" };
    const { unmount } = render(
      <MemoryRouter>
        <SSHKeyRotationWizard
          open
          onOpenChange={vi.fn()}
          sshKeys={[disabledKey, idleKey]}
          keyUsageMap={new Map()}
          token="token"
          onComplete={vi.fn()}
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole("radio", { name: "停用密钥" })).toBeDisabled();
    expect(screen.getByRole("link", { name: textChoice("sshKeys.rotationDisabledOpenKeys", "打开密钥页") })).toHaveAttribute("href", "/app/ssh-keys");
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    await user.click(screen.getByRole("radio", { name: "空闲密钥" }));
    expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled();
    unmount();

    render(
      <MemoryRouter>
        <SSHKeyRotationWizard
          open
          onOpenChange={vi.fn()}
          sshKeys={[disabledKey]}
          keyUsageMap={new Map([[disabledKey.id, affected]])}
          preselectedKey={disabledKey}
          token="token"
          onComplete={vi.fn()}
        />
      </MemoryRouter>,
    );
    expect(screen.queryByLabelText("私钥内容")).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "停用密钥" })).toBeDisabled();
  });

  it("当前密钥页上打开禁用密钥向导后，打开密钥页会关闭向导且不改密钥", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const onComplete = vi.fn();
    render(<DisabledKeyOnKeyPage onOpenChange={onOpenChange} onComplete={onComplete} />);

    const openKeys = screen.getByRole("link", { name: textChoice("sshKeys.rotationDisabledOpenKeys", "打开密钥页") });
    expect(openKeys).toHaveAttribute("href", "/app/ssh-keys");
    await user.click(openKeys);

    expect(onOpenChange).toHaveBeenCalledTimes(1);
    expect(onOpenChange).toHaveBeenCalledWith(false);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(previewSSHKey).not.toHaveBeenCalled();
    expect(getSSHKey).not.toHaveBeenCalled();
    expect(updateSSHKey).not.toHaveBeenCalled();
    expect(testConnection).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });

  it("保存前重读当前作用域，禁用或缺失时不提交", async () => {
    const user = userEvent.setup();
    getSSHKey.mockResolvedValueOnce({
      ...selectedKey,
      disabled: false,
      allowedPurposes: "terminal",
      allowedNodeIds: "9",
      allowedNodeTags: "edge",
      expiresAt: "2026-08-01T09:30",
    });
    const saved = render(wizardElement());
    await confirmRotation(user);
    expect(getSSHKey).toHaveBeenCalledWith("token", "key-1", expect.objectContaining({
      signal: expect.any(AbortSignal),
    }));
    expect(updateSSHKey).toHaveBeenCalledWith("token", "key-1", expect.objectContaining({
      privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      disabled: false,
      allowedPurposes: "terminal",
      allowedNodeIds: "9",
      allowedNodeTags: "edge",
    }));
    expect(Object.hasOwn(updateSSHKey.mock.calls[0]?.[2] as object, "expiresAt")).toBe(false);
    saved.unmount();

    getSSHKey.mockResolvedValue({ ...selectedKey, disabled: true, allowedPurposes: "terminal" });
    updateSSHKey.mockClear();
    const blocked = render(
      <MemoryRouter>
        {wizardElement()}
      </MemoryRouter>,
    );
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect(await screen.findAllByText(exactChoice(
      "sshKeys.rotationKeyDisabledStop",
      "该密钥已禁用。轮换未提交，也不会自动启用。",
    ))).not.toHaveLength(0);
    expect(updateSSHKey).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: textChoice("sshKeys.rotationDisabledOpenKeys", "打开密钥页") })).toHaveAttribute("href", "/app/ssh-keys");
    blocked.unmount();

    getSSHKey.mockRejectedValueOnce(new ApiError(404, "missing"));
    updateSSHKey.mockClear();
    render(wizardElement());
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect(await screen.findAllByText(exactChoice(
      "sshKeys.rotationKeyMissing",
      "密钥已不存在，轮换未提交。",
    ))).not.toHaveLength(0);
    expect(updateSSHKey).not.toHaveBeenCalled();
  });

  it("保存结果未知时不自动重试，复测只覆盖失败或未知节点", async () => {
    const user = userEvent.setup();
    updateSSHKey.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const unknownView = render(wizardElement());
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect((await screen.findAllByText(exactChoice(
      "sshKeys.rotationSaveUnknown",
      "保存结果未知。请刷新密钥事实后重新开始轮换，不要自动重试保存。",
    ))).length).toBeGreaterThan(0);
    expect(updateSSHKey).toHaveBeenCalledTimes(1);
    expect(testConnection).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: textChoice("sshKeys.rotationReverifySubset", "重新验证失败或未知节点") })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: textChoice("sshKeys.rotationEditAgain", "返回修改") })).not.toBeInTheDocument();
    unknownView.unmount();

    updateSSHKey.mockClear();
    testConnection.mockClear();
    updateSSHKey.mockResolvedValue({ ...selectedKey, fingerprint: "SHA256:new" });
    render(wizardElement());
    await confirmRotation(user);
    expect(await screen.findByText("验证失败")).toBeInTheDocument();
    expect(screen.getAllByText(exactChoice("sshKeys.rotationOutcomePartial", "部分节点失败或结果未知。")).length).toBeGreaterThan(0);
    testConnection.mockResolvedValueOnce([
      { nodeId: "node-2", name: "node-offline", host: "node-offline.example", port: 22, success: true, latencyMs: 4 },
    ]);
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationReverifySubset", "重新验证失败或未知节点") }));
    await waitFor(() => expect(testConnection).toHaveBeenCalledTimes(2));
    expect(testConnection).toHaveBeenLastCalledWith("token", "key-1", ["node-2"]);
    expect(updateSSHKey).toHaveBeenCalledTimes(1);
  });

  it("遗漏和传输失败记为未知，全部失败为严重，零节点不假装验证通过", async () => {
    const user = userEvent.setup();
    testConnection.mockResolvedValueOnce([
      { nodeId: "node-1", name: "node-online", host: "node-online.example", port: 22, success: true, latencyMs: 8 },
    ]);
    const omitted = render(wizardElement());
    await confirmRotation(user);
    expect(await screen.findAllByText(exactChoice("sshKeys.rotationVerifyUnknown", "结果未知"))).toHaveLength(1);
    expect(screen.queryByText("验证失败")).not.toBeInTheDocument();
    expect(screen.getAllByText(exactChoice("sshKeys.rotationOutcomePartial", "部分节点失败或结果未知。")).length).toBeGreaterThan(0);
    omitted.unmount();

    updateSSHKey.mockClear();
    toastSuccess.mockClear();
    testConnection.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const transport = render(wizardElement());
    await confirmRotation(user);
    expect(await screen.findAllByText(exactChoice("sshKeys.rotationVerifyUnknown", "结果未知"))).toHaveLength(2);
    expect(screen.queryByText("验证失败")).not.toBeInTheDocument();
    expect(toastSuccess).toHaveBeenCalled();
    transport.unmount();

    testConnection.mockResolvedValue([
      { nodeId: "node-1", name: "node-online", host: "node-online.example", port: 22, success: false, latencyMs: 0, error: "denied" },
      { nodeId: "node-2", name: "node-offline", host: "node-offline.example", port: 22, success: false, latencyMs: 0, error: "denied" },
    ]);
    const failed = render(wizardElement());
    await confirmRotation(user);
    expect((await screen.findAllByText(exactChoice("sshKeys.rotationOutcomeFailed", "全部受影响节点验证失败。"))).length).toBeGreaterThan(0);
    expect(screen.getAllByText("denied")).toHaveLength(2);
    const updates = updateSSHKey.mock.calls.length;
    testConnection.mockClear();
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationReverifySubset", "重新验证失败或未知节点") }));
    await waitFor(() => expect(testConnection).toHaveBeenCalledTimes(1));
    expect(testConnection).toHaveBeenCalledWith("token", "key-1", ["node-1", "node-2"]);
    expect(updateSSHKey).toHaveBeenCalledTimes(updates);
    failed.unmount();

    testConnection.mockClear();
    render(
      <SSHKeyRotationWizard
        open
        onOpenChange={vi.fn()}
        sshKeys={[selectedKey]}
        keyUsageMap={new Map([[selectedKey.id, []]])}
        preselectedKey={selectedKey}
        token="token"
        onComplete={vi.fn()}
      />,
    );
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 0 以确认受影响节点数"), "0");
    const callsBefore = updateSSHKey.mock.calls.length;
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    await waitFor(() => expect(updateSSHKey.mock.calls.length).toBe(callsBefore + 1));
    expect((await screen.findAllByText(exactChoice("sshKeys.rotationNoNodeVerification", "无需节点验证"))).length).toBeGreaterThan(0);
    expect(testConnection).not.toHaveBeenCalled();
    expect(screen.queryByText(exactChoice("sshKeys.rotationOutcomeSuccess", "全部受影响节点已验证通过。"))).not.toBeInTheDocument();
  });

  it("复制候选公钥前后都核对当前身份", async () => {
    const user = userEvent.setup();
    let resolveCopy: () => void = () => {};
    const writeText = vi.fn(() => new Promise<void>((resolve) => {
      resolveCopy = resolve;
    }));
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    const view = render(wizardElement());
    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    const pendingCopy = user.click(screen.getByRole("button", { name: "复制公钥" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("ssh-ed25519 AAAA_CANDIDATE"));
    bumpAuthSessionGeneration();
    view.rerender(wizardElement());
    await act(async () => {
      resolveCopy();
    });
    await pendingCopy;
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("轮换更新不提交过期时间，并保留刚读到的其余元数据", async () => {
    const user = userEvent.setup();
    getSSHKey.mockResolvedValueOnce({
      ...selectedKey,
      username: "deploy",
      disabled: false,
      expiresAt: "2026-11-01T01:30",
      allowedPurposes: "terminal",
      allowedNodeIds: "9",
      allowedNodeTags: "edge",
    });
    render(wizardElement());
    await confirmRotation(user);

    expect(updateSSHKey).toHaveBeenCalledTimes(1);
    const payload = updateSSHKey.mock.calls[0]?.[2] as Record<string, unknown>;
    expect(Object.hasOwn(payload, "expiresAt")).toBe(false);
    expect(payload).toMatchObject({
      name: "生产密钥",
      username: "deploy",
      keyType: "auto",
      privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      disabled: false,
      allowedPurposes: "terminal",
      allowedNodeIds: "9",
      allowedNodeTags: "edge",
    });
  });
});
