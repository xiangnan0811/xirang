import { StrictMode, useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, bumpAuthSessionGeneration } from "@/lib/api/core";
import { SSHKeyRotationDecodeError, type SSHKeyRotationResult } from "@/lib/api/ssh-keys-api";
import type { NodeRecord, SSHKeyPreview, SSHKeyRecord } from "@/types/domain";
import { SSHKeyRotationWizard } from "./ssh-key-rotation-wizard";

const { updateSSHKey, testConnection, previewSSHKey, rotateSSHKey } = vi.hoisted(() => ({
  updateSSHKey: vi.fn(),
  testConnection: vi.fn(),
  previewSSHKey: vi.fn(),
  rotateSSHKey: vi.fn(),
}));

vi.mock("@/lib/api/ssh-keys-api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/ssh-keys-api")>("@/lib/api/ssh-keys-api");
  return {
    ...actual,
    createSSHKeysApi: () => ({
      updateSSHKey,
      testConnection,
      previewSSHKey,
    }),
    rotateSSHKey,
  };
});

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

const savedRotation: SSHKeyRotationResult = {
  status: "saved",
  reason: "",
  publicKeyFingerprint: "SHA256:saved-public",
  results: [
    { nodeId: "node-1", name: "node-online", status: "verified" },
    { nodeId: "node-9", name: "server-only", status: "verified" },
  ],
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

function wizardElement(token = "token", open = true, onComplete = vi.fn()) {
  return (
    <SSHKeyRotationWizard
      open={open}
      onOpenChange={vi.fn()}
      sshKeys={[selectedKey]}
      keyUsageMap={new Map([[selectedKey.id, affected]])}
      preselectedKey={selectedKey}
      token={token}
      onComplete={onComplete}
    />
  );
}

async function confirmRotation(user: UserEvent, acknowledgement = "2") {
  await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
  await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
  await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText(`输入 ${acknowledgement} 以确认预估节点数`), acknowledgement);
  const callsBefore = rotateSSHKey.mock.calls.length;
  await user.click(screen.getByRole("button", { name: "确认轮换" }));
  await waitFor(() => expect(rotateSSHKey.mock.calls.length).toBe(callsBefore + 1));
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
    rotateSSHKey.mockReset();
    previewSSHKey.mockResolvedValue(checkedPreview);
    rotateSSHKey.mockResolvedValue(savedRotation);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("submits one rotate call and renders the server node list", async () => {
    const user = userEvent.setup();
    const onComplete = vi.fn();
    render(wizardElement("token", true, onComplete));

    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(previewSSHKey).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: textChoice("sshKeys.rotationCheckCandidate", "检查候选密钥") }));
    await waitFor(() => expect(screen.getByRole("button", { name: "下一步" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "下一步" }));
    expect(screen.getByText("这里只是页面缓存的预估数量。确认后，服务端会按完整库存验证并决定是否保存。")).toBeInTheDocument();
    await user.type(screen.getByLabelText("输入 2 以确认预估节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));

    expect(await screen.findByText("server-only")).toBeInTheDocument();
    expect(screen.getByText("node-online")).toBeInTheDocument();
    expect(screen.queryByText("node-offline")).not.toBeInTheDocument();
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);
    expect(rotateSSHKey).toHaveBeenCalledWith("token", "key-1", {
      privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY",
      keyType: "auto",
      name: "生产密钥",
    }, expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(updateSSHKey).not.toHaveBeenCalled();
    expect(testConnection).not.toHaveBeenCalled();
    expect(screen.getByTestId("rotation-summary")).toHaveAttribute("data-rotation-status", "saved");
    expect(screen.getByText("全部受影响节点已验证通过。")).toBeInTheDocument();
    expect(screen.getByText("新公钥指纹:")).toBeInTheDocument();
    expect(screen.getByText("SHA256:saved-public")).toBeInTheDocument();
    expect(screen.queryByText("SHA256:new-public")).not.toBeInTheDocument();
    expect(toastSuccess).toHaveBeenCalledWith("密钥已更新");
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "返回修改并重新检查" })).not.toBeInTheDocument();
    expect(previewSSHKey.mock.invocationCallOrder[0]).toBeLessThan(rotateSSHKey.mock.invocationCallOrder[0]);
  });

  it("StrictMode 下身份未变时仍只提交一次轮换", async () => {
    const user = userEvent.setup();
    render(<StrictMode>{wizardElement()}</StrictMode>);
    await confirmRotation(user);

    expect(await screen.findByText("server-only")).toBeInTheDocument();
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);
    expect(toastSuccess).toHaveBeenCalledWith("密钥已更新");
  });

  it("轮换返回前身份失效后，不提示成功", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRotationResult>();
    rotateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);
    expect(screen.getByText("正在验证候选连接，通过后保存")).toBeInTheDocument();

    authRef.current = { role: "viewer", token: "token" };
    view.rerender(wizardElement());
    await act(async () => {
      pending.resolve(savedRotation);
    });

    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText("验证通过")).not.toBeInTheDocument();
    expect(screen.queryByText("server-only")).not.toBeInTheDocument();
  });

  it("令牌 A-B-A 后废弃的轮换结果不能成功，新的确认仍可提交", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRotationResult>();
    rotateSSHKey.mockReturnValueOnce(pending.promise);
    const view = render(wizardElement("token-a"));
    await confirmRotation(user);

    view.rerender(wizardElement("token-b"));
    view.rerender(wizardElement("token-a"));
    await act(async () => {
      pending.resolve(savedRotation);
    });

    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText("server-only")).not.toBeInTheDocument();
    rotateSSHKey.mockResolvedValue(savedRotation);
    await confirmRotation(user);
    expect(await screen.findByText("server-only")).toBeInTheDocument();
    expect(rotateSSHKey).toHaveBeenCalledTimes(2);
    expect(toastSuccess).toHaveBeenCalledTimes(1);
  });

  it("关闭后，迟到的轮换结果不会记为成功", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRotationResult>();
    rotateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);

    view.rerender(wizardElement("token", false));
    await act(async () => {
      pending.resolve(savedRotation);
    });

    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText("验证通过")).not.toBeInTheDocument();
  });

  it("卸载后，迟到的轮换结果和 finally 不会记为成功", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRotationResult>();
    rotateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);

    view.unmount();
    await act(async () => {
      pending.resolve(savedRotation);
    });

    expect(toastSuccess).not.toHaveBeenCalled();
    expect(updateSSHKey).not.toHaveBeenCalled();
    expect(testConnection).not.toHaveBeenCalled();
  });

  it("轮换失败且身份已变时不展示轮换错误", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRotationResult>();
    rotateSSHKey.mockReturnValue(pending.promise);
    const view = render(wizardElement());
    await confirmRotation(user);

    authRef.current = { role: "operator", token: "token" };
    view.rerender(wizardElement());
    await act(async () => {
      pending.reject(new Error("rotation failed late"));
    });

    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.queryByText("rotation failed late")).not.toBeInTheDocument();
    expect(screen.queryByText("无法确认是否已保存。请到密钥页核对当前公钥指纹，不要重试本次提交。")).not.toBeInTheDocument();
  });

  it("验证进行中关闭会中止请求、清空草稿，并忽略迟到的成功", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<SSHKeyRotationResult>();
    const onOpenChange = vi.fn();
    const onComplete = vi.fn();
    rotateSSHKey.mockReturnValue(pending.promise);
    render(
      <SSHKeyRotationWizard
        open
        onOpenChange={onOpenChange}
        sshKeys={[selectedKey]}
        keyUsageMap={new Map([[selectedKey.id, affected]])}
        preselectedKey={selectedKey}
        token="token"
        onComplete={onComplete}
      />,
    );
    await confirmRotation(user);
    expect(screen.getByText("正在验证候选连接，通过后保存")).toBeInTheDocument();
    const rotationOptions: unknown = rotateSSHKey.mock.calls[0]?.[3];
    if (!rotationOptions || typeof rotationOptions !== "object" || !("signal" in rotationOptions)) {
      throw new Error("rotation did not receive an abort signal");
    }
    const rotationSignal = rotationOptions.signal;
    if (!(rotationSignal instanceof AbortSignal)) {
      throw new Error("rotation did not receive an abort signal");
    }
    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(onOpenChange).toHaveBeenCalledTimes(1);
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(rotationSignal.aborted).toBe(true);
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "cleared");

    await act(async () => {
      pending.resolve(savedRotation);
    });

    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
    expect(screen.queryByText("验证通过")).not.toBeInTheDocument();
    expect(screen.queryByText("密钥已更新")).not.toBeInTheDocument();
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);
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
    expect(rotateSSHKey).not.toHaveBeenCalled();
    expect(updateSSHKey).not.toHaveBeenCalled();
    expect(testConnection).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });


  it("明确失败保留草稿并使用固定文案，解码失败和传输失败进入未知且不能成功", async () => {
    const user = userEvent.setup();
    const renderInRouter = () => render(<MemoryRouter>{wizardElement()}</MemoryRouter>);
    rotateSSHKey.mockRejectedValueOnce(new ApiError(404, "raw missing"));
    const missing = renderInRouter();
    await confirmRotation(user);
    expect(await screen.findByText("密钥已不存在，轮换未提交。")).toBeInTheDocument();
    expect(screen.queryByText("raw missing")).not.toBeInTheDocument();
    expect(screen.getByTestId("rotation-summary")).toHaveAttribute("data-rotation-status", "failed");
    expect(toastSuccess).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "返回修改并重新检查" }));
    expect(screen.getByLabelText("私钥内容")).toHaveValue("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "present");
    missing.unmount();

    rotateSSHKey.mockRejectedValueOnce(new ApiError(403, "forbidden raw"));
    const forbidden = renderInRouter();
    await confirmRotation(user);
    expect(await screen.findByText("当前身份不能轮换这把密钥。")).toBeInTheDocument();
    expect(screen.queryByText("forbidden raw")).not.toBeInTheDocument();
    forbidden.unmount();

    rotateSSHKey.mockRejectedValueOnce(new ApiError(413, "too large raw"));
    const oversized = renderInRouter();
    await confirmRotation(user);
    expect(await screen.findByText("候选密钥过大，轮换未提交。")).toBeInTheDocument();
    oversized.unmount();

    rotateSSHKey.mockRejectedValueOnce(new ApiError(400, "bad raw"));
    const rejected = renderInRouter();
    await confirmRotation(user);
    expect(await screen.findByText("轮换请求被拒绝，原密钥未替换。")).toBeInTheDocument();
    expect(screen.queryByText("bad raw")).not.toBeInTheDocument();
    rejected.unmount();

    rotateSSHKey.mockClear();
    rotateSSHKey.mockRejectedValueOnce(new SSHKeyRotationDecodeError());
    const decoded = renderInRouter();
    await confirmRotation(user);
    expect((await screen.findAllByText(exactChoice(
      "无法确认是否已保存。请到密钥页核对当前公钥指纹，不要重试本次提交。",
    ))).length).toBeGreaterThan(0);
    expect(screen.getByTestId("rotation-summary")).toHaveAttribute("data-rotation-status", "unknown");
    expect(screen.queryByText("验证通过")).not.toBeInTheDocument();
    expect(screen.queryByText("全部受影响节点已验证通过。")).not.toBeInTheDocument();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "cleared");
    expect(screen.queryByRole("button", { name: "返回修改并重新检查" })).not.toBeInTheDocument();
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);
    decoded.unmount();

    rotateSSHKey.mockClear();
    toastSuccess.mockClear();
    rotateSSHKey.mockRejectedValueOnce(new ApiError(500, "boom"));
    renderInRouter();
    await confirmRotation(user);
    expect((await screen.findAllByText(exactChoice(
      "无法确认是否已保存。请到密钥页核对当前公钥指纹，不要重试本次提交。",
    ))).length).toBeGreaterThan(0);
    expect(screen.queryByText("boom")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开密钥页" })).toHaveAttribute("href", "/app/ssh-keys");
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "cleared");
  });

  it("未保存时展示服务端节点和固定补救，返回修改会保留私钥但作废检查", async () => {
    const user = userEvent.setup();
    const onComplete = vi.fn();
    rotateSSHKey.mockResolvedValueOnce({
      status: "not_saved",
      reason: "validation_failed",
      publicKeyFingerprint: "SHA256:candidate",
      results: [
        { nodeId: "node-1", name: "node-online", status: "verified" },
        { nodeId: "node-4", name: "node-denied", status: "failed", errorCode: "connection_failed" },
      ],
    });
    render(wizardElement("token", true, onComplete));
    await confirmRotation(user);

    expect(screen.getByText("未替换，原密钥保持不变。")).toBeInTheDocument();
    expect(screen.getByText("候选连接未全部通过。")).toBeInTheDocument();
    expect(screen.getByText("连接失败")).toBeInTheDocument();
    expect(screen.queryByText("connection_failed")).not.toBeInTheDocument();
    expect(screen.queryByText("SHA256:candidate")).not.toBeInTheDocument();
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
    const rows = screen.getAllByTestId("rotation-node-result");
    expect(rows[0]).toHaveAttribute("data-node-id", "node-1");
    expect(rows[0]).toHaveAttribute("data-node-status", "verified");
    expect(rows[1]).toHaveAttribute("data-node-id", "node-4");
    expect(rows[1]).toHaveAttribute("data-node-status", "failed");
    expect(screen.queryByRole("button", { name: /重新验证/ })).not.toBeInTheDocument();
    expect(rotateSSHKey).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "返回修改并重新检查" }));
    expect(screen.getByLabelText("私钥内容")).toHaveValue("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(screen.getByRole("button", { name: "下一步" })).toBeDisabled();
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-draft", "present");
    expect(screen.getByRole("dialog")).toHaveAttribute("data-rotation-candidate", "absent");
  });

  it("范围、信任、超时、上限和占用给出固定补救并打开密钥页与节点页", async () => {
    const user = userEvent.setup();
    const cases = [
      ["scope_blocked", "密钥用途或节点范围不允许这次轮换。", "范围不允许"],
      ["trust_unavailable", "主机信任记录不可用。", "未检查"],
      ["validation_timeout", "验证超时，原密钥未替换。", "超时"],
      ["inventory_limit", "关联节点超过同步上限，原密钥未替换。", "未检查"],
      ["busy", "其他修改正在占用这把密钥。", "未检查"],
    ] as const;
    for (const [reason, detail, nodeError] of cases) {
      rotateSSHKey.mockResolvedValueOnce({
        status: "not_saved",
        reason,
        publicKeyFingerprint: "SHA256:candidate",
        results: [
          { nodeId: "node-2", name: "edge", status: reason === "scope_blocked" ? "failed" : "unknown", errorCode: reason === "scope_blocked" ? "scope_denied" : reason === "validation_timeout" ? "timeout" : "not_checked" },
        ],
      });
      const view = render(
        <MemoryRouter>
          {wizardElement()}
        </MemoryRouter>,
      );
      await confirmRotation(user);
      expect(screen.getByText("未替换，原密钥保持不变。")).toBeInTheDocument();
      expect(screen.getByText(detail)).toBeInTheDocument();
      expect(screen.getByText(nodeError)).toBeInTheDocument();
      expect(screen.getByTestId("rotation-summary")).toHaveAttribute("data-rotation-reason", reason);
      expect(screen.getByRole("link", { name: "打开密钥页" })).toHaveAttribute("href", "/app/ssh-keys");
      expect(screen.getByRole("link", { name: "打开节点页" })).toHaveAttribute("href", "/app/nodes");
      expect(toastSuccess).not.toHaveBeenCalled();
      view.unmount();
    }
  });

  it("冲突不提供重试，零节点保存不假装做过连接验证", async () => {
    const user = userEvent.setup();
    rotateSSHKey.mockResolvedValueOnce({
      status: "not_saved",
      reason: "conflict",
      publicKeyFingerprint: "SHA256:candidate",
      results: [],
    });
    const conflict = render(wizardElement());
    await confirmRotation(user);
    expect(screen.getByText("配置已变化，必须重新验证。")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "打开节点页" })).not.toBeInTheDocument();
    expect(screen.queryByText("全部受影响节点已验证通过。")).not.toBeInTheDocument();
    conflict.unmount();

    rotateSSHKey.mockResolvedValueOnce({
      status: "saved",
      reason: "",
      publicKeyFingerprint: "SHA256:saved-public",
      results: [],
    });
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
    await confirmRotation(user, "0");
    expect(await screen.findByText("没有关联节点，无需连接验证。")).toBeInTheDocument();
    expect(screen.queryByText("全部受影响节点已验证通过。")).not.toBeInTheDocument();
    expect(testConnection).not.toHaveBeenCalled();
    expect(updateSSHKey).not.toHaveBeenCalled();
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

});
