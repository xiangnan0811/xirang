import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogEvent } from "@/types/domain";
import { useLiveLogs } from "./use-live-logs";

const socket = vi.hoisted(() => ({
  connect: vi.fn(), disconnect: vi.fn(), updateSinceId: vi.fn(),
  messages: new Set<(event: LogEvent) => void>(),
  statuses: new Set<(connected: boolean) => void>(),
}));
vi.mock("@/lib/ws/logs-socket", () => ({
  LogsSocketClient: class {
    connect = socket.connect;
    disconnect = socket.disconnect;
    updateSinceId = socket.updateSinceId;
    isGivingUp = () => false;
    subscribe(listener: (event: LogEvent) => void) {
      socket.messages.add(listener);
      return () => socket.messages.delete(listener);
    }
    onStatusChange(listener: (connected: boolean) => void) {
      socket.statuses.add(listener);
      return () => socket.statuses.delete(listener);
    }
  },
}));

describe("useLiveLogs", () => {
  const frames = new Map<number, FrameRequestCallback>();
  let nextFrame = 0;
  beforeEach(() => {
    vi.clearAllMocks();
    socket.messages.clear();
    socket.statuses.clear();
    frames.clear();
    nextFrame = 0;
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      const id = nextFrame++;
      frames.set(id, callback);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  });
  afterEach(() => vi.unstubAllGlobals());

  function emit(logId: number) {
    socket.messages.forEach((listener) => listener({ id: String(logId), logId, level: "info", message: "log", timestamp: "" }));
  }

  function flush() {
    const callbacks = [...frames.values()];
    frames.clear();
    callbacks.forEach((callback) => callback(0));
  }

  it("clears log and cursor scope on task/token change and cancels queued frames", () => {
    const { result, rerender, unmount } = renderHook(
      ({ token, taskId }: { token: string | null; taskId: number }) => useLiveLogs(token, { taskId }),
      { initialProps: { token: "first" as string | null, taskId: 1 } },
    );
    act(() => { emit(10); flush(); });
    expect(result.current.cursorLogId).toBe(10);
    act(() => emit(11));
    rerender({ token: "first", taskId: 2 });
    expect(frames.size).toBe(0);
    expect(result.current.logs).toEqual([]);
    expect(result.current.cursorLogId).toBe(0);
    expect(socket.connect).toHaveBeenLastCalledWith("first", expect.objectContaining({ taskId: 2, sinceId: undefined }));
    act(() => { emit(2); flush(); });
    expect(result.current.cursorLogId).toBe(2);
    rerender({ token: "second", taskId: 2 });
    const options = socket.connect.mock.lastCall?.[1] as { tokenGetter: () => string };
    expect(options.tokenGetter()).toBe("second");
    expect(result.current.logs).toEqual([]);
    rerender({ token: null, taskId: 2 });
    expect(result.current.connected).toBe(false);
    expect(result.current.connectionWarning).toBeTruthy();
    expect(socket.messages.size).toBe(0);
    unmount();
  });
});
