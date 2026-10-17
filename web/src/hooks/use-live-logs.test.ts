import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogEvent } from "@/types/domain";
import { useLiveLogs } from "./use-live-logs";
import { beginAuthTransitionBarrier, clearAuthTransitionBarrier } from "@/lib/api/core";

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
  afterEach(() => {
    clearAuthTransitionBarrier();
    vi.unstubAllGlobals();
  });
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

  it("isolates disabled scope from late callbacks, queued frames and cursor across enable-disable-enable", () => {
    const { result, rerender, unmount } = renderHook(
      ({ enabled }: { enabled: boolean }) => useLiveLogs("token-1", { taskId: 7, enabled }),
      { initialProps: { enabled: true } },
    );
    expect(socket.connect).toHaveBeenCalledTimes(1);
    act(() => {
      socket.statuses.forEach((listener) => listener(true));
      emit(10);
      flush();
    });
    expect(result.current.connected).toBe(true);
    expect(result.current.cursorLogId).toBe(10);
    expect(result.current.logs.map((row) => row.logId)).toEqual([10]);

    // Queue a frame and keep references to the first effect's listeners and frame.
    act(() => emit(11));
    const staleMessages = [...socket.messages];
    const staleStatuses = [...socket.statuses];
    const staleFrames = [...frames.values()];
    expect(staleFrames).toHaveLength(1);
    const disconnectsBefore = socket.disconnect.mock.calls.length;

    rerender({ enabled: false });
    expect(socket.disconnect.mock.calls.length).toBe(disconnectsBefore + 1);
    expect(socket.messages.size).toBe(0);
    expect(socket.statuses.size).toBe(0);
    expect(frames.size).toBe(0);
    expect(socket.connect).toHaveBeenCalledTimes(1);
    expect(result.current).toEqual({ logs: [], connected: false, connectionWarning: null, cursorLogId: 0 });

    // Late callbacks from the disabled owner are ignored even when fired directly.
    act(() => {
      staleMessages.forEach((listener) => listener({ id: "12", logId: 12, level: "info", message: "late", timestamp: "" }));
      staleStatuses.forEach((listener) => listener(false));
      staleFrames.forEach((callback) => callback(0));
    });
    expect(frames.size).toBe(0);
    expect(socket.updateSinceId).not.toHaveBeenCalledWith(11);
    expect(socket.updateSinceId).not.toHaveBeenCalledWith(12);
    expect(result.current).toEqual({ logs: [], connected: false, connectionWarning: null, cursorLogId: 0 });

    rerender({ enabled: true });
    expect(socket.connect).toHaveBeenCalledTimes(2);
    expect(socket.connect).toHaveBeenLastCalledWith("token-1", expect.objectContaining({ taskId: 7, sinceId: undefined }));
    expect(result.current.logs).toEqual([]);
    expect(result.current.cursorLogId).toBe(0);
    expect(result.current.connected).toBe(false);

    // Stale first-owner callbacks still cannot inject into the re-enabled scope.
    act(() => {
      staleMessages.forEach((listener) => listener({ id: "13", logId: 13, level: "info", message: "late", timestamp: "" }));
      staleStatuses.forEach((listener) => listener(true));
      staleFrames.forEach((callback) => callback(0));
    });
    expect(frames.size).toBe(0);
    expect(result.current.logs).toEqual([]);
    expect(result.current.cursorLogId).toBe(0);
    expect(result.current.connected).toBe(false);

    act(() => { emit(3); flush(); });
    expect(result.current.logs.map((row) => row.logId)).toEqual([3]);
    expect(result.current.cursorLogId).toBe(3);
    expect(socket.updateSinceId).toHaveBeenLastCalledWith(3);
    unmount();
  });

  it("never subscribes or connects when initially disabled", () => {
    const { result } = renderHook(() => useLiveLogs(null, { enabled: false }));
    expect(socket.connect).not.toHaveBeenCalled();
    expect(socket.messages.size).toBe(0);
    expect(socket.statuses.size).toBe(0);
    expect(result.current).toEqual({ logs: [], connected: false, connectionWarning: null, cursorLogId: 0 });
  });

  it("closes the log socket during an auth transition and reconnects after it ends", () => {
    renderHook(() => useLiveLogs("token-1"));
    expect(socket.connect).toHaveBeenCalledTimes(1);

    act(() => {
      beginAuthTransitionBarrier();
    });
    expect(socket.disconnect).toHaveBeenCalled();
    const connectsWhilePaused = socket.connect.mock.calls.length;

    act(() => {
      clearAuthTransitionBarrier();
    });
    expect(socket.connect).toHaveBeenCalledTimes(connectsWhilePaused + 1);
    const options = socket.connect.mock.lastCall?.[1] as { tokenGetter: () => string | null };
    expect(options.tokenGetter()).toBe("token-1");
  });
});
