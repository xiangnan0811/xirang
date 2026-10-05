import { StrictMode } from "react";
import { act, fireEvent, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AuthRole } from "@/context/auth-context.shared";
import type { SSHKeyRecord } from "@/types/domain";
import { RotationUpload } from "./rotation-preview";

const { toastError } = vi.hoisted(() => ({
  toastError: vi.fn(),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: toastError,
    success: vi.fn(),
  },
}));

const selectedKey = {
  id: "key-1",
  name: "生产密钥",
} as SSHKeyRecord;

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

function uploadElement(
  onNewPrivateKeyChange: (key: string) => void,
  token = "token",
  role: AuthRole | null = "admin",
) {
  return (
    <RotationUpload
      selectedKey={selectedKey}
      newKeyName="生产密钥"
      onNewKeyNameChange={vi.fn()}
      newKeyType="auto"
      onNewKeyTypeChange={vi.fn()}
      newPrivateKey=""
      onNewPrivateKeyChange={onNewPrivateKeyChange}
      token={token}
      role={role}
      onBack={vi.fn()}
      onNext={vi.fn()}
    />
  );
}

function renderUpload(token = "token", role: AuthRole | null = "admin") {
  const onNewPrivateKeyChange = vi.fn();
  const view = render(uploadElement(onNewPrivateKeyChange, token, role));
  return { ...view, onNewPrivateKeyChange };
}

function fileWithSize(name: string, size: number) {
  const file = new File(["x"], name);
  Object.defineProperty(file, "size", { value: size });
  return file;
}

function changeKeyFile(name = "id_ed25519", file = new File(["ignored"], name)) {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [file] } });
}

describe("RotationUpload", () => {
  beforeEach(() => {
    toastError.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("把当前密钥文件的文本写入私钥", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange } = renderUpload();

    changeKeyFile();
    expect(pending).toHaveLength(1);
    act(() => pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY"));

    expect(onNewPrivateKeyChange).toHaveBeenCalledTimes(1);
    expect(onNewPrivateKeyChange).toHaveBeenCalledWith("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("当前读取失败时提示文件读取失败", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange } = renderUpload();

    changeKeyFile("bad.pem");
    act(() => pending[0].fail());

    expect(toastError).toHaveBeenCalledTimes(1);
    expect(toastError).toHaveBeenCalledWith("文件读取失败");
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
    expect(pending[0].aborted).toBe(false);
  });

  it("StrictMode 下当前密钥文件仍然写入私钥", () => {
    const pending = installDelayedFileReader();
    const onNewPrivateKeyChange = vi.fn();
    render(<StrictMode>{uploadElement(onNewPrivateKeyChange)}</StrictMode>);

    changeKeyFile();
    act(() => pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY"));

    expect(pending).toHaveLength(1);
    expect(onNewPrivateKeyChange).toHaveBeenCalledTimes(1);
    expect(onNewPrivateKeyChange).toHaveBeenCalledWith("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
  });

  it("接受 128KiB 文件，并在更大文件开始读取前拒绝", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange } = renderUpload();

    changeKeyFile("limit.pem", fileWithSize("limit.pem", 128 * 1024));
    expect(pending).toHaveLength(1);
    act(() => pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LIMIT"));
    expect(onNewPrivateKeyChange).toHaveBeenCalledWith("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LIMIT");

    onNewPrivateKeyChange.mockClear();
    changeKeyFile("big.pem", fileWithSize("big.pem", 128 * 1024 + 1));

    expect(pending).toHaveLength(1);
    expect(toastError).toHaveBeenCalledTimes(1);
    expect(toastError).toHaveBeenCalledWith("文件过大（超过 100KB）");
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
  });

  it("过大文件不会中止仍在进行的有效读取", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange } = renderUpload();

    changeKeyFile("current.pem");
    changeKeyFile("big.pem", fileWithSize("big.pem", 128 * 1024 + 1));

    expect(pending).toHaveLength(1);
    expect(pending[0].aborted).toBe(false);
    expect(toastError).toHaveBeenCalledWith("文件过大（超过 100KB）");
    act(() => pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY"));
    expect(onNewPrivateKeyChange).toHaveBeenCalledTimes(1);
    expect(onNewPrivateKeyChange).toHaveBeenCalledWith("FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
  });

  it("卸载后忽略迟到的私钥读取", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange, unmount } = renderUpload();

    changeKeyFile("late.pem");
    unmount();

    expect(pending[0].aborted).toBe(true);
    act(() => pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE"));
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("卸载后忽略迟到的读取失败", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange, unmount } = renderUpload();

    changeKeyFile("late.pem");
    unmount();

    expect(pending[0].aborted).toBe(true);
    act(() => pending[0].fail());
    expect(toastError).not.toHaveBeenCalled();
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
  });

  it("新的读取会中止上一份文件，并忽略其迟到结果", () => {
    const pending = installDelayedFileReader();
    const { onNewPrivateKeyChange } = renderUpload();

    changeKeyFile("stale.pem");
    changeKeyFile("fresh.pem");

    expect(pending[0].aborted).toBe(true);
    expect(pending[1].aborted).toBe(false);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_STALE");
      pending[0].fail();
    });
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();

    act(() => pending[1].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH"));
    expect(onNewPrivateKeyChange).toHaveBeenCalledTimes(1);
    expect(onNewPrivateKeyChange).toHaveBeenCalledWith("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH");
  });

  it("令牌 A-B-A 后忽略旧读取，随后的新文件仍然生效", () => {
    const pending = installDelayedFileReader();
    const onNewPrivateKeyChange = vi.fn();
    const view = render(uploadElement(onNewPrivateKeyChange, "token-a"));

    changeKeyFile("late.pem");
    view.rerender(uploadElement(onNewPrivateKeyChange, "token-b"));
    view.rerender(uploadElement(onNewPrivateKeyChange, "token-a"));

    expect(pending[0].aborted).toBe(true);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
      pending[0].fail();
    });
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();

    changeKeyFile("fresh.pem");
    act(() => pending[1].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH"));
    expect(onNewPrivateKeyChange).toHaveBeenCalledTimes(1);
    expect(onNewPrivateKeyChange).toHaveBeenCalledWith("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_FRESH");
  });

  it("角色回到管理员后仍忽略降权期间未完成的读取", () => {
    const pending = installDelayedFileReader();
    const onNewPrivateKeyChange = vi.fn();
    const view = render(uploadElement(onNewPrivateKeyChange, "token", "admin"));

    changeKeyFile("late.pem");
    view.rerender(uploadElement(onNewPrivateKeyChange, "token", "viewer"));
    view.rerender(uploadElement(onNewPrivateKeyChange, "token", "admin"));

    expect(pending[0].aborted).toBe(true);
    act(() => {
      pending[0].complete("FAKE_PRIVATE_KEY_FOR_TEST_ONLY_LATE");
      pending[0].fail();
    });
    expect(onNewPrivateKeyChange).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });
});
