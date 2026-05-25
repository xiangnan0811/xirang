import { useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
});
