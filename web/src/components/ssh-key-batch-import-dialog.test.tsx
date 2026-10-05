import { useState } from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSSHKeysApi } from "@/lib/api/ssh-keys-api";
import { SSHKeyBatchImportDialog } from "./ssh-key-batch-import-dialog";

const { batchCreateMock, toastSuccessMock, toastErrorMock } = vi.hoisted(() => ({
  batchCreateMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("@/lib/api/ssh-keys-api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api/ssh-keys-api")>("@/lib/api/ssh-keys-api");
  return {
    ...actual,
    createSSHKeysApi: vi.fn(() => ({
      batchCreate: batchCreateMock,
    })),
  };
});

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: toastErrorMock,
  },
}));

const authRef: { current: { role: "admin" | "operator" | "viewer" | null; token: string | null } } = {
  current: { role: "admin", token: "FAKE_TOKEN_FOR_TEST_ONLY" },
};

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function createImportFile(name: string, entries: Array<Record<string, unknown>>): File {
  return new File([JSON.stringify(entries)], name, { type: "application/json" });
}

function renderControlledDialog() {
  const onImportComplete = vi.fn();

  function Harness() {
    const [open, setOpen] = useState(true);
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>重新打开批量导入</button>
        <button type="button" onClick={() => setOpen(false)}>外部关闭批量导入</button>
        <SSHKeyBatchImportDialog
          open={open}
          onOpenChange={setOpen}
          existingKeyNames={[]}
          token="FAKE_TOKEN_FOR_TEST_ONLY"
          onImportComplete={onImportComplete}
        />
      </>
    );
  }

  const view = render(<Harness />);
  return { ...view, onImportComplete };
}

describe("SSHKeyBatchImportDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authRef.current = { role: "admin", token: "FAKE_TOKEN_FOR_TEST_ONLY" };
    batchCreateMock.mockResolvedValue([{ name: "fresh-key", status: "created" }]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("关闭后重新打开会清空已解析私钥，不能提交旧文件中的条目", async () => {
    const user = userEvent.setup();
    renderControlledDialog();

    const staleFile = createImportFile("stale.json", [
      {
        name: "stale-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_STALE",
      },
    ]);

    const uploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(uploadInput, { target: { files: [staleFile] } });

    expect(await screen.findByText("stale-key")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "取消" }));
    expect(screen.queryByText("stale-key")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重新打开批量导入" }));
    expect(screen.getByText("拖拽文件到此处或点击上传")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "导入 1 个有效密钥" })).not.toBeInTheDocument();

    const freshFile = createImportFile("fresh.json", [
      {
        name: "fresh-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH",
      },
    ]);
    const freshUploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(freshUploadInput, { target: { files: [freshFile] } });

    expect(await screen.findByText("fresh-key")).toBeInTheDocument();
    expect(screen.queryByText("stale-key")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "导入 1 个有效密钥" }));

    await waitFor(() => expect(batchCreateMock).toHaveBeenCalledTimes(1));
    expect(createSSHKeysApi).toHaveBeenCalled();
    expect(batchCreateMock).toHaveBeenCalledWith("FAKE_TOKEN_FOR_TEST_ONLY", [
      expect.objectContaining({
        name: "fresh-key",
        username: "deploy",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH",
      }),
    ]);
    expect(JSON.stringify(batchCreateMock.mock.calls)).not.toContain("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_STALE");

    await waitFor(() => expect(screen.queryByText("fresh-key")).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "重新打开批量导入" }));
    expect(screen.getByText("拖拽文件到此处或点击上传")).toBeInTheDocument();
    expect(screen.queryByText("fresh-key")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "导入 1 个有效密钥" })).not.toBeInTheDocument();
  });

  it("外部关闭后重新打开不能提交旧文件中的条目", async () => {
    renderControlledDialog();

    const staleFile = createImportFile("stale.json", [
      {
        name: "stale-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_STALE",
      },
    ]);

    const uploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(uploadInput, { target: { files: [staleFile] } });

    expect(await screen.findByText("stale-key")).toBeInTheDocument();
    fireEvent.click(document.querySelectorAll("button")[1]);
    await waitFor(() => expect(screen.queryByText("stale-key")).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "重新打开批量导入" }));

    expect(screen.getByText("拖拽文件到此处或点击上传")).toBeInTheDocument();
    expect(screen.queryByText("stale-key")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "导入 1 个有效密钥" })).not.toBeInTheDocument();
    expect(batchCreateMock).not.toHaveBeenCalled();
  });

  it("关闭后会忽略较晚完成的 FileReader，不会导入延迟解析出的私钥", async () => {
    const user = userEvent.setup();
    const pendingReaders: Array<{
      reader: FileReader;
      complete: (text: string) => void;
    }> = [];

    class DelayedFileReader {
      static readonly EMPTY = 0;
      static readonly LOADING = 1;
      static readonly DONE = 2;

      onerror: ((this: FileReader, ev: ProgressEvent<FileReader>) => unknown) | null = null;
      onload: ((this: FileReader, ev: ProgressEvent<FileReader>) => unknown) | null = null;
      readyState: typeof DelayedFileReader.EMPTY | typeof DelayedFileReader.LOADING | typeof DelayedFileReader.DONE = DelayedFileReader.EMPTY;

      abort() {
        this.readyState = DelayedFileReader.DONE;
      }

      readAsText() {
        this.readyState = DelayedFileReader.LOADING;
        pendingReaders.push({
          reader: this as unknown as FileReader,
          complete: (text: string) => {
            this.readyState = DelayedFileReader.DONE;
            this.onload?.call(
              this as unknown as FileReader,
              { target: { result: text } } as ProgressEvent<FileReader>,
            );
          },
        });
      }
    }

    vi.stubGlobal("FileReader", DelayedFileReader);
    renderControlledDialog();

    const lateFile = createImportFile("late.json", [
      {
        name: "late-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE",
      },
    ]);
    const uploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(uploadInput, { target: { files: [lateFile] } });
    expect(pendingReaders).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "取消" }));
    pendingReaders[0].complete(JSON.stringify([
      {
        name: "late-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE",
      },
    ]));

    await user.click(screen.getByRole("button", { name: "重新打开批量导入" }));
    expect(screen.getByText("拖拽文件到此处或点击上传")).toBeInTheDocument();
    expect(screen.queryByText("late-key")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "导入 1 个有效密钥" })).not.toBeInTheDocument();
    expect(batchCreateMock).not.toHaveBeenCalled();

    const freshFile = createImportFile("fresh.json", [
      {
        name: "fresh-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH",
      },
    ]);
    const freshUploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(freshUploadInput, { target: { files: [freshFile] } });
    pendingReaders[1].complete(JSON.stringify([
      {
        name: "fresh-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH",
      },
    ]));

    expect(await screen.findByText("fresh-key")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "导入 1 个有效密钥" }));

    await waitFor(() => expect(batchCreateMock).toHaveBeenCalledTimes(1));
    expect(batchCreateMock).toHaveBeenCalledWith("FAKE_TOKEN_FOR_TEST_ONLY", [
      expect.objectContaining({
        name: "fresh-key",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH",
      }),
    ]);
    expect(JSON.stringify(batchCreateMock.mock.calls)).not.toContain("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
  });

  async function startImport(user: UserEvent) {
    const file = createImportFile("fresh.json", [
      {
        name: "fresh-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH",
      },
    ]);
    const uploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(uploadInput, { target: { files: [file] } });
    expect(await screen.findByText("fresh-key")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "导入 1 个有效密钥" }));
    await waitFor(() => expect(batchCreateMock).toHaveBeenCalledTimes(1));
  }

  function renderIdentityDialog() {
    const onImportComplete = vi.fn();

    function Harness() {
      const [open, setOpen] = useState(true);
      const [token, setToken] = useState("FAKE_TOKEN_FOR_TEST_ONLY");
      const [role, setRole] = useState<"admin" | "operator" | "viewer">("admin");
      authRef.current = { role, token };
      return (
        <>
          <button type="button" onClick={() => setOpen(false)}>外部关闭批量导入</button>
          <button type="button" onClick={() => setOpen(true)}>重新打开批量导入</button>
          <button type="button" onClick={() => setRole("viewer")}>降为只读</button>
          <button type="button" onClick={() => setToken("token-b")}>切换令牌</button>
          <button type="button" onClick={() => setToken("FAKE_TOKEN_FOR_TEST_ONLY")}>恢复令牌</button>
          <SSHKeyBatchImportDialog
            open={open}
            onOpenChange={setOpen}
            existingKeyNames={[]}
            token={token}
            onImportComplete={onImportComplete}
          />
        </>
      );
    }

    const view = render(<Harness />);
    return { ...view, onImportComplete };
  }

  it("batchCreate 完成后身份已变时不提示、不刷新", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<Array<{ name: string; status: "created" }>>();
    batchCreateMock.mockReturnValue(pending.promise);
    const { onImportComplete } = renderIdentityDialog();
    await startImport(user);

    fireEvent.click(screen.getByRole("button", { name: "降为只读", hidden: true }));
    await act(async () => {
      pending.resolve([{ name: "fresh-key", status: "created" }]);
    });

    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(onImportComplete).not.toHaveBeenCalled();
  });

  it("令牌 A-B-A 后迟到的 batchCreate 不提示、不刷新", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<Array<{ name: string; status: "created" }>>();
    batchCreateMock.mockReturnValue(pending.promise);
    const { onImportComplete } = renderIdentityDialog();
    await startImport(user);

    fireEvent.click(screen.getByRole("button", { name: "切换令牌", hidden: true }));
    fireEvent.click(screen.getByRole("button", { name: "恢复令牌", hidden: true }));
    await act(async () => {
      pending.resolve([{ name: "fresh-key", status: "created" }]);
    });

    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(onImportComplete).not.toHaveBeenCalled();
  });

  it("关闭或关闭后重开时，迟到的 batchCreate 不提示、不刷新", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<Array<{ name: string; status: "created" }>>();
    batchCreateMock.mockReturnValue(pending.promise);
    const { onImportComplete } = renderIdentityDialog();
    await startImport(user);

    fireEvent.click(screen.getByRole("button", { name: "外部关闭批量导入", hidden: true }));
    fireEvent.click(screen.getByRole("button", { name: "重新打开批量导入", hidden: true }));
    await act(async () => {
      pending.resolve([{ name: "fresh-key", status: "created" }]);
    });

    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(onImportComplete).not.toHaveBeenCalled();
    expect(screen.getByText("拖拽文件到此处或点击上传")).toBeInTheDocument();
  });

  it("batchCreate 失败且对话框已关闭时不提示错误", async () => {
    const user = userEvent.setup();
    const pending = createDeferred<Array<{ name: string; status: "created" }>>();
    batchCreateMock.mockReturnValue(pending.promise);
    const { onImportComplete } = renderIdentityDialog();
    await startImport(user);

    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    await act(async () => {
      pending.reject(new Error("import failed late"));
    });

    expect(toastErrorMock).not.toHaveBeenCalled();
    expect(toastSuccessMock).not.toHaveBeenCalled();
    expect(onImportComplete).not.toHaveBeenCalled();
  });

  it("角色变化后忽略迟到的 FileReader，不会导入延迟解析出的私钥", async () => {
    const pendingReaders: Array<{ complete: (text: string) => void }> = [];
    class DelayedFileReader {
      static readonly EMPTY = 0;
      static readonly LOADING = 1;
      static readonly DONE = 2;
      onerror: ((this: FileReader, ev: ProgressEvent<FileReader>) => unknown) | null = null;
      onload: ((this: FileReader, ev: ProgressEvent<FileReader>) => unknown) | null = null;
      readyState = DelayedFileReader.EMPTY;

      abort() {
        this.readyState = DelayedFileReader.DONE;
      }

      readAsText() {
        this.readyState = DelayedFileReader.LOADING;
        pendingReaders.push({
          complete: (text: string) => {
            this.readyState = DelayedFileReader.DONE;
            this.onload?.call(
              this as unknown as FileReader,
              { target: { result: text } } as ProgressEvent<FileReader>,
            );
          },
        });
      }
    }

    vi.stubGlobal("FileReader", DelayedFileReader);
    renderIdentityDialog();
    const lateFile = createImportFile("late.json", [
      {
        name: "late-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE",
      },
    ]);
    const uploadInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(uploadInput, { target: { files: [lateFile] } });
    expect(pendingReaders).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "降为只读", hidden: true }));
    pendingReaders[0].complete(JSON.stringify([
      {
        name: "late-key",
        username: "deploy",
        keyType: "auto",
        privateKey: "FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE",
      },
    ]));

    expect(screen.queryByText("late-key")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "导入 1 个有效密钥" })).not.toBeInTheDocument();
    expect(batchCreateMock).not.toHaveBeenCalled();
  });
});
