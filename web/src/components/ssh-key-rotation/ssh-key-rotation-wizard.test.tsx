import { StrictMode } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";
import { SSHKeyRotationWizard } from "./ssh-key-rotation-wizard";

const { updateSSHKey, testConnection } = vi.hoisted(() => ({
  updateSSHKey: vi.fn(),
  testConnection: vi.fn(),
}));

vi.mock("@/lib/api/ssh-keys-api", () => ({
  createSSHKeysApi: () => ({
    updateSSHKey,
    testConnection,
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
  await user.click(screen.getByRole("button", { name: "下一步" }));
  await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
  await user.click(screen.getByRole("button", { name: "确认轮换" }));
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
});
