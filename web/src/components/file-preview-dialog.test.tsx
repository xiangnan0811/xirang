import { useState } from "react";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { FilePreviewDialog } from "./file-preview-dialog";
import type { FileContentResult } from "@/lib/api/files-api";

function createDeferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe("FilePreviewDialog", () => {
  it("passes an AbortSignal to the file-content loader", () => {
    const deferred = createDeferred<FileContentResult>();
    const fetchContent = vi.fn((_signal?: AbortSignal) => deferred.promise);

    const { unmount } = render(
      <FilePreviewDialog
        open
        onOpenChange={vi.fn()}
        filePath="/safe/file.txt"
        fetchContent={fetchContent}
      />,
    );

    expect(fetchContent).toHaveBeenCalledTimes(1);
    const signal = fetchContent.mock.calls[0]?.[0];
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(signal?.aborted).toBe(false);

    unmount();
  });

  it("aborts in-flight loading, clears rendered content, and ignores late resolution after close", async () => {
    const user = userEvent.setup();
    const deferred = createDeferred<FileContentResult>();
    let capturedSignal: AbortSignal | undefined;
    const fetchContent = vi.fn((signal?: AbortSignal) => {
      capturedSignal = signal;
      return deferred.promise;
    });

    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <FilePreviewDialog
            open={open}
            onOpenChange={setOpen}
            filePath="/safe/file.txt"
            fetchContent={fetchContent}
          />
          <div data-testid="dialog-state">{open ? "open" : "closed"}</div>
        </>
      );
    }

    render(<Harness />);

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(capturedSignal).toBeInstanceOf(AbortSignal);

    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(screen.getByTestId("dialog-state")).toHaveTextContent("closed");
    expect(capturedSignal?.aborted).toBe(true);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await act(async () => {
      deferred.resolve({
        path: "/safe/file.txt",
        content: "late preview content",
        size: 20,
        truncated: true,
      });
      await deferred.promise;
    });

    await waitFor(() => expect(screen.queryByText("late preview content")).not.toBeInTheDocument());
  });

  it("clears resolved content before reopening after close", async () => {
    const user = userEvent.setup();
    const firstDeferred = createDeferred<FileContentResult>();
    const secondDeferred = createDeferred<FileContentResult>();
    let callCount = 0;
    const fetchContent = vi.fn((_signal?: AbortSignal) => {
      callCount += 1;
      return callCount === 1 ? firstDeferred.promise : secondDeferred.promise;
    });

    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <>
          <button type="button" onClick={() => setOpen(true)}>Open preview</button>
          <FilePreviewDialog
            open={open}
            onOpenChange={setOpen}
            filePath="/safe/file.txt"
            fetchContent={fetchContent}
          />
        </>
      );
    }

    render(<Harness />);

    await act(async () => {
      firstDeferred.resolve({
        path: "/safe/file.txt",
        content: "resolved preview content",
        size: 24,
        truncated: false,
      });
      await firstDeferred.promise;
    });

    expect(await screen.findByText("resolved preview content")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Open preview" }));

    await waitFor(() => expect(fetchContent).toHaveBeenCalledTimes(2));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).queryByText("resolved preview content")).not.toBeInTheDocument();
    expect(within(dialog).getByText("加载中...", { selector: "div" })).toBeInTheDocument();
  });

  it("clears previous content while loading a different file", async () => {
    const firstDeferred = createDeferred<FileContentResult>();
    const secondDeferred = createDeferred<FileContentResult>();
    const signals: AbortSignal[] = [];
    const fetchContent = vi.fn((signal?: AbortSignal) => {
      if (signal) signals.push(signal);
      return fetchContent.mock.calls.length === 1 ? firstDeferred.promise : secondDeferred.promise;
    });

    const { rerender } = render(
      <FilePreviewDialog
        open
        onOpenChange={vi.fn()}
        filePath="/safe/first.txt"
        fetchContent={fetchContent}
      />,
    );

    await act(async () => {
      firstDeferred.resolve({
        path: "/safe/first.txt",
        content: "first preview content",
        size: 21,
        truncated: false,
      });
      await firstDeferred.promise;
    });

    const firstDialog = await screen.findByRole("dialog");
    expect(within(firstDialog).getByText("first preview content")).toBeInTheDocument();

    rerender(
      <FilePreviewDialog
        open
        onOpenChange={vi.fn()}
        filePath="/safe/second.txt"
        fetchContent={fetchContent}
      />,
    );

    expect(signals[0]?.aborted).toBe(true);
    const secondDialog = await screen.findByRole("dialog");
    expect(within(secondDialog).queryByText("first preview content")).not.toBeInTheDocument();
    expect(within(secondDialog).getByText("加载中...", { selector: "div" })).toBeInTheDocument();

    await act(async () => {
      secondDeferred.resolve({
        path: "/safe/second.txt",
        content: "second preview content",
        size: 22,
        truncated: false,
      });
      await secondDeferred.promise;
    });

    await waitFor(() => expect(screen.getByText("second preview content")).toBeInTheDocument());
    expect(screen.queryByText("first preview content")).not.toBeInTheDocument();
  });

  it("aborts and ignores late resolution after unmount", async () => {
    const deferred = createDeferred<FileContentResult>();
    let capturedSignal: AbortSignal | undefined;
    const fetchContent = vi.fn((signal?: AbortSignal) => {
      capturedSignal = signal;
      return deferred.promise;
    });

    const { unmount } = render(
      <FilePreviewDialog
        open
        onOpenChange={vi.fn()}
        filePath="/safe/file.txt"
        fetchContent={fetchContent}
      />,
    );

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    unmount();

    expect(capturedSignal?.aborted).toBe(true);

    await act(async () => {
      deferred.resolve({
        path: "/safe/file.txt",
        content: "late unmounted content",
        size: 22,
        truncated: false,
      });
      await deferred.promise;
    });

    await waitFor(() => expect(screen.queryByText("late unmounted content")).not.toBeInTheDocument());
  });
});
