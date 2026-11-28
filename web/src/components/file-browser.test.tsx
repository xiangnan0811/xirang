import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { FileBrowser } from "./file-browser";
import type { FileContentResult, FileListResult } from "@/lib/api/files-api";

function createDeferred<T>() {
  const promise = new Promise<T>(() => undefined);
  return { promise };
}

// The ES2022 lib configured for web/ has no Promise.withResolvers.
function controllable<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

function entry(path: string, isDir: boolean): FileListResult["entries"][number] {
  return {
    name: path.slice(path.lastIndexOf("/") + 1),
    path,
    isDir,
    size: 1,
    mode: isDir ? "drwxr-xr-x" : "-rw-r--r--",
    modTime: "2026-05-24T00:00:00Z",
  };
}

function listing(path: string, entries: FileListResult["entries"] = []): FileListResult {
  return { path, entries, truncated: false };
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

describe("FileBrowser server-selected start", () => {
  it("boots once from the initial listing, adopts its canonical path as root, then uses explicit paths", async () => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn().mockResolvedValue(
      listing("/srv/data", [entry("/srv/data/sub", true), entry("/srv/data/readme.md", false)]),
    );
    const fetchDir = vi.fn((path: string) => Promise.resolve(
      path === "/srv/data/sub" ? listing("/srv/data/sub", [entry("/srv/data/sub/inner.txt", false)]) : listing(path),
    ));
    const fetchContent = vi.fn(() => new Promise<FileContentResult>(() => undefined));

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={fetchContent} />);

    expect(await screen.findByText("readme.md")).toBeInTheDocument();
    expect(fetchInitialDir).toHaveBeenCalledTimes(1);
    expect(fetchInitialDir).toHaveBeenCalledWith(expect.any(AbortSignal));
    expect(fetchDir).not.toHaveBeenCalled();
    // Root is the canonical server path: labelled by its basename, no Up and no ancestors.
    expect(screen.getByText("data")).toHaveAttribute("title", "/srv/data");
    expect(screen.queryByRole("button", { name: "返回上级" })).not.toBeInTheDocument();
    expect(screen.queryByText("srv")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "进入目录 sub" }));
    expect(await screen.findByText("inner.txt")).toBeInTheDocument();
    expect(fetchDir).toHaveBeenLastCalledWith("/srv/data/sub", expect.any(AbortSignal));
    expect(screen.getByRole("button", { name: "data" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => expect(fetchDir).toHaveBeenLastCalledWith("/srv/data/sub", expect.any(AbortSignal)));
    expect(fetchDir).toHaveBeenCalledTimes(2);

    await user.click(await screen.findByRole("button", { name: "返回上级" }));
    await waitFor(() => expect(fetchDir).toHaveBeenLastCalledWith("/srv/data", expect.any(AbortSignal)));
    await waitFor(() => expect(screen.queryByRole("button", { name: "返回上级" })).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => expect(fetchDir).toHaveBeenCalledTimes(4));
    expect(fetchDir).toHaveBeenLastCalledWith("/srv/data", expect.any(AbortSignal));
    expect(fetchInitialDir).toHaveBeenCalledTimes(1);
  });

  it("previews files by their explicit path after bootstrap", async () => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn().mockResolvedValue(listing("/srv/data", [entry("/srv/data/readme.md", false)]));
    const fetchDir = vi.fn();
    const fetchContent = vi.fn(() => new Promise<FileContentResult>(() => undefined));

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={fetchContent} />);
    await user.click(await screen.findByRole("button", { name: "预览文件 readme.md" }));

    await waitFor(() => expect(fetchContent).toHaveBeenCalledWith("/srv/data/readme.md", expect.any(AbortSignal)));
    expect(fetchInitialDir).toHaveBeenCalledTimes(1);
    expect(fetchDir).not.toHaveBeenCalled();
  });

  it("passes canonical paths with trailing spaces through verbatim", async () => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn().mockResolvedValue(listing("/srv/data ", [entry("/srv/data /sub ", true)]));
    const fetchDir = vi.fn((path: string) => Promise.resolve(listing(path)));

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={vi.fn()} />);
    // Accessible names may be whitespace-trimmed; the request path must not be.
    await user.click(await screen.findByRole("button", { name: /^进入目录 sub\s*$/ }));
    await waitFor(() => expect(fetchDir).toHaveBeenLastCalledWith("/srv/data /sub ", expect.any(AbortSignal)));

    await user.click(await screen.findByRole("button", { name: "返回上级" }));
    await waitFor(() => expect(fetchDir).toHaveBeenLastCalledWith("/srv/data ", expect.any(AbortSignal)));
    expect(fetchInitialDir).toHaveBeenCalledTimes(1);
  });

  it("repeats the initial request on Retry and Refresh until it succeeds", async () => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn()
      .mockRejectedValueOnce(new Error("节点不可达"))
      .mockRejectedValueOnce(new Error("仍不可达"))
      .mockResolvedValueOnce(listing("/home/ops", [entry("/home/ops/notes.txt", false)]));
    const fetchDir = vi.fn();

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={vi.fn()} />);

    expect(await screen.findByText("节点不可达")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "返回上级" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("仍不可达")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "刷新" }));
    expect(await screen.findByText("notes.txt")).toBeInTheDocument();
    expect(screen.queryByText("仍不可达")).not.toBeInTheDocument();
    expect(fetchInitialDir).toHaveBeenCalledTimes(3);
    expect(fetchDir).not.toHaveBeenCalled();
    expect(screen.getByText("ops")).toHaveAttribute("title", "/home/ops");
  });

  it.each([
    ["empty", ""],
    ["relative", "srv/data"],
    ["dot segment", "/srv/./data"],
    ["parent segment", "/srv/../etc"],
    ["double slash", "/srv//data"],
    ["trailing slash", "/srv/data/"],
  ])("rejects a malformed initial path (%s) with the translated error and retries the initial request", async (_label, path) => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn()
      .mockResolvedValueOnce(listing(path, [entry("/srv/data/leak.txt", false)]))
      .mockResolvedValueOnce(listing("/srv/data", [entry("/srv/data/ok.txt", false)]));
    const fetchDir = vi.fn();

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={vi.fn()} />);

    expect(await screen.findByText("加载目录失败")).toBeInTheDocument();
    expect(screen.queryByText("leak.txt")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("ok.txt")).toBeInTheDocument();
    expect(fetchInitialDir).toHaveBeenCalledTimes(2);
    expect(fetchDir).not.toHaveBeenCalled();
  });

  it("retries a failed explicit navigation by path once bootstrapped", async () => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn().mockResolvedValue(listing("/srv/data", [entry("/srv/data/sub", true)]));
    const fetchDir = vi.fn()
      .mockRejectedValueOnce(new Error("权限不足"))
      .mockResolvedValueOnce(listing("/srv/data/sub", [entry("/srv/data/sub/inner.txt", false)]));

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "进入目录 sub" }));
    expect(await screen.findByText("权限不足")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("inner.txt")).toBeInTheDocument();
    expect(fetchDir).toHaveBeenNthCalledWith(2, "/srv/data/sub", expect.any(AbortSignal));
    expect(fetchInitialDir).toHaveBeenCalledTimes(1);
  });

  it("does not manufacture ancestors for a canonical target outside the initial root", async () => {
    const user = userEvent.setup();
    const fetchInitialDir = vi.fn().mockResolvedValue(
      listing("/srv/data", [entry("/srv/data/link", true), entry("/srv/data/sibling", true)]),
    );
    const fetchDir = vi.fn((path: string) => Promise.resolve(
      path === "/srv/data/link"
        ? listing("/mnt/other/share", [entry("/mnt/other/share/far.txt", false)])
        : path === "/srv/data/sibling"
          ? listing("/srv/database", [entry("/srv/database/prefix.txt", false)])
          : listing(path, [entry("/srv/data/link", true), entry("/srv/data/sibling", true)]),
    ));

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "进入目录 link" }));
    expect(await screen.findByText("far.txt")).toBeInTheDocument();

    expect(screen.getByText("/mnt/other/share")).toHaveAttribute("title", "/mnt/other/share");
    for (const ancestor of ["mnt", "other", "/mnt", "/mnt/other", "srv"]) {
      expect(screen.queryByText(ancestor)).not.toBeInTheDocument();
    }

    // The initial root is the only reset point offered.
    await user.click(screen.getByRole("button", { name: "data" }));
    expect(await screen.findByRole("button", { name: "进入目录 sibling" })).toBeInTheDocument();
    expect(fetchDir).toHaveBeenLastCalledWith("/srv/data", expect.any(AbortSignal));

    // A sibling sharing the root's string prefix is outside the root, not a child crumb "base".
    await user.click(screen.getByRole("button", { name: "进入目录 sibling" }));
    expect(await screen.findByText("prefix.txt")).toBeInTheDocument();
    expect(screen.getByText("/srv/database")).toBeInTheDocument();
    expect(screen.queryByText("base")).not.toBeInTheDocument();
    expect(fetchInitialDir).toHaveBeenCalledTimes(1);
  });

  it("aborts and ignores a late initial result after the session is replaced", async () => {
    const first = controllable<FileListResult>();
    const second = controllable<FileListResult>();
    const signals: AbortSignal[] = [];
    const fetchFirst = vi.fn((signal?: AbortSignal) => { signals.push(signal!); return first.promise; });
    const fetchSecond = vi.fn((signal?: AbortSignal) => { signals.push(signal!); return second.promise; });
    const fetchDir = vi.fn();

    const { rerender } = render(
      <FileBrowser key="node-1" fetchInitialDir={fetchFirst} fetchDir={fetchDir} fetchContent={vi.fn()} />,
    );
    rerender(<FileBrowser key="node-2" fetchInitialDir={fetchSecond} fetchDir={fetchDir} fetchContent={vi.fn()} />);
    expect(signals[0].aborted).toBe(true);

    await act(async () => { second.resolve(listing("/opt/two", [entry("/opt/two/current.txt", false)])); });
    expect(await screen.findByText("current.txt")).toBeInTheDocument();
    await act(async () => { first.resolve(listing("/opt/one", [entry("/opt/one/stale.txt", false)])); });
    expect(screen.queryByText("stale.txt")).not.toBeInTheDocument();
    expect(screen.getByText("two")).toHaveAttribute("title", "/opt/two");
    expect(fetchFirst).toHaveBeenCalledTimes(1);
    expect(fetchSecond).toHaveBeenCalledTimes(1);
    expect(fetchDir).not.toHaveBeenCalled();
  });

  it("aborts a pending initial request on unmount", () => {
    let signal: AbortSignal | undefined;
    const fetchInitialDir = vi.fn((s?: AbortSignal) => { signal = s; return new Promise<FileListResult>(() => undefined); });
    const { unmount } = render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={vi.fn()} fetchContent={vi.fn()} />);
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("ignores a superseded explicit navigation that resolves late", async () => {
    const user = userEvent.setup();
    const slow = controllable<FileListResult>();
    const fetchInitialDir = vi.fn().mockResolvedValue(listing("/srv", [entry("/srv/a", true), entry("/srv/b", true)]));
    const fetchDir = vi.fn((path: string) =>
      path === "/srv/a" ? slow.promise : Promise.resolve(listing("/srv/b", [entry("/srv/b/fresh.txt", false)])),
    );

    render(<FileBrowser fetchInitialDir={fetchInitialDir} fetchDir={fetchDir} fetchContent={vi.fn()} />);
    await user.click(await screen.findByRole("button", { name: "进入目录 a" }));
    await user.click(screen.getByRole("button", { name: "进入目录 b" }));
    expect(await screen.findByText("fresh.txt")).toBeInTheDocument();

    await act(async () => { slow.resolve(listing("/srv/a", [entry("/srv/a/stale.txt", false)])); });
    expect(screen.queryByText("stale.txt")).not.toBeInTheDocument();
    expect(screen.getByText("b")).toHaveAttribute("title", "/srv/b");
  });
});

describe("FileBrowser fixed root breadcrumbs", () => {
  it("splits on path components, not string prefixes", async () => {
    const fetchDir = vi.fn().mockResolvedValue(listing("/safely/x", [entry("/safely/x/f.txt", false)]));
    render(<FileBrowser rootPath="/safe" fetchDir={fetchDir} fetchContent={vi.fn()} />);

    expect(await screen.findByText("f.txt")).toBeInTheDocument();
    expect(fetchDir).toHaveBeenCalledWith("/safe", expect.any(AbortSignal));
    expect(screen.getByRole("button", { name: "safe" })).toBeInTheDocument();
    expect(screen.getByText("/safely/x")).toBeInTheDocument();
    expect(screen.queryByText("ly")).not.toBeInTheDocument();
  });
});
