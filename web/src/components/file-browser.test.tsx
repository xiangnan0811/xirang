import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { FileBrowser } from "./file-browser";
import type { FileContentResult, FileListResult } from "@/lib/api/files-api";

function createDeferred<T>() {
  const promise = new Promise<T>(() => undefined);
  return { promise };
}

const directoryResult: FileListResult = {
  path: "/safe",
  truncated: false,
  entries: [
    {
      name: "file.txt",
      path: "/safe/file.txt",
      isDir: false,
      size: 12,
      mode: "-rw-r--r--",
      modTime: "2026-05-24T00:00:00Z",
    },
  ],
};

describe("FileBrowser", () => {
  it("aborts the old root and ignores its late directory response", async () => {
    let resolveOld!: (value: FileListResult) => void;
    const oldRequest = new Promise<FileListResult>((resolve) => { resolveOld = resolve; });
    let oldSignal: AbortSignal | undefined;
    const fetchDir = vi.fn((path: string, signal?: AbortSignal) => {
      if (path === "/old") {
        oldSignal = signal;
        return oldRequest;
      }
      return Promise.resolve(directoryResult);
    });
    const fetchContent = vi.fn();
    const { rerender } = render(<FileBrowser rootPath="/old" fetchDir={fetchDir} fetchContent={fetchContent} />);
    rerender(<FileBrowser rootPath="/safe" fetchDir={fetchDir} fetchContent={fetchContent} />);
    expect(oldSignal?.aborted).toBe(true);
    expect(await screen.findByText("file.txt")).toBeInTheDocument();
    await act(async () => {
      resolveOld({ path: "/old", entries: [{ ...directoryResult.entries[0], name: "stale.txt" }], truncated: false });
    });
    expect(screen.queryByText("stale.txt")).not.toBeInTheDocument();
    expect(screen.getByText("file.txt")).toBeInTheDocument();
  });

  it("keeps directory loading unchanged and passes preview AbortSignal to fetchContent", async () => {
    const user = userEvent.setup();
    const preview = createDeferred<FileContentResult>();
    let previewSignal: AbortSignal | undefined;
    const fetchDir = vi.fn().mockResolvedValue(directoryResult);
    const fetchContent = vi.fn((_path: string, signal?: AbortSignal) => {
      previewSignal = signal;
      return preview.promise;
    });

    render(
      <FileBrowser
        rootPath="/safe"
        fetchDir={fetchDir}
        fetchContent={fetchContent}
      />,
    );

    expect(await screen.findByText("file.txt")).toBeInTheDocument();
    expect(fetchDir).toHaveBeenCalledWith("/safe", expect.any(AbortSignal));

    await user.click(screen.getByRole("button", { name: "预览文件 file.txt" }));

    await waitFor(() => expect(fetchContent).toHaveBeenCalledTimes(1));
    expect(fetchContent).toHaveBeenCalledWith("/safe/file.txt", expect.any(AbortSignal));
    expect(previewSignal).toBeInstanceOf(AbortSignal);
    expect(previewSignal?.aborted).toBe(false);

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(previewSignal?.aborted).toBe(true);
  });
});
