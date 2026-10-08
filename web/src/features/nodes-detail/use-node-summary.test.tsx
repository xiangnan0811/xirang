import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier } from "@/lib/api/core";
import type { NodeSummary } from "@/lib/api/nodes-api";
import { useNodeSummary } from "./use-node-summary";

const { getNodeSummary } = vi.hoisted(() => ({
  getNodeSummary: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: { getNodeSummary },
}));

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const emptySummary: NodeSummary = { openAlerts: 0, runningTasks: 0 };
const mounted: Array<() => void> = [];

async function settleCalls(queued: readonly unknown[], previous: number): Promise<number> {
  await waitFor(() => expect(queued.length).toBeGreaterThan(previous));
  let stable = queued.length;
  for (let attempt = 0; attempt < 4; attempt += 1) {
    await act(async () => {
      await Promise.resolve();
    });
    if (queued.length === stable) break;
    stable = queued.length;
  }
  return stable;
}

describe("useNodeSummary auth generation", () => {
  beforeEach(() => {
    clearAuthTransitionBarrier();
    getNodeSummary.mockReset();
    getNodeSummary.mockImplementation(() => Promise.resolve(emptySummary));
  });

  afterEach(() => {
    for (const unmount of mounted) unmount();
    mounted.length = 0;
    getNodeSummary.mockImplementation(() => Promise.resolve(emptySummary));
    clearAuthTransitionBarrier();
    getNodeSummary.mockReset();
  });

  it("does not apply an in-flight summary that started before the auth barrier", async () => {
    const queued: Array<Deferred<NodeSummary>> = [];
    getNodeSummary.mockImplementation(() => {
      const next = deferred<NodeSummary>();
      queued.push(next);
      return next.promise;
    });
    const { result, unmount } = renderHook(() => useNodeSummary(7, "token-a"));
    mounted.push(unmount);
    const startedBeforeBarrier = await settleCalls(queued, 0);

    act(() => {
      beginAuthTransitionBarrier();
    });
    await act(async () => {
      for (let index = 0; index < startedBeforeBarrier; index += 1) {
        queued[index]?.resolve({ openAlerts: 4, runningTasks: 2 });
      }
    });
    expect(result.current.data).toBeNull();

    act(() => {
      clearAuthTransitionBarrier();
    });
    const afterRelease = await settleCalls(queued, startedBeforeBarrier);
    expect(result.current.data).toBeNull();

    await act(async () => {
      queued[afterRelease - 1]?.resolve({ openAlerts: 1, runningTasks: 0 });
    });
    await waitFor(() => expect(result.current.data).toEqual({ openAlerts: 1, runningTasks: 0 }));

    await act(async () => {
      for (let index = startedBeforeBarrier; index < afterRelease - 1; index += 1) {
        queued[index]?.resolve({ openAlerts: 4, runningTasks: 2 });
      }
    });
    expect(result.current.data).toEqual({ openAlerts: 1, runningTasks: 0 });
  });

  it("does not apply a pre-switch summary after the same token returns", async () => {
    const queued: Array<Deferred<NodeSummary>> = [];
    getNodeSummary.mockImplementation(() => {
      const next = deferred<NodeSummary>();
      queued.push(next);
      return next.promise;
    });
    const { result, rerender, unmount } = renderHook(
      ({ token }: { token: string }) => useNodeSummary(7, token),
      { initialProps: { token: "token-a" } },
    );
    mounted.push(unmount);
    const afterMount = await settleCalls(queued, 0);

    act(() => {
      bumpAuthSessionGeneration();
    });
    rerender({ token: "token-b" });
    const afterSwitch = await settleCalls(queued, afterMount);

    act(() => {
      bumpAuthSessionGeneration();
    });
    rerender({ token: "token-a" });
    const afterReturn = await settleCalls(queued, afterSwitch);

    await act(async () => {
      for (let index = 0; index < afterSwitch; index += 1) {
        queued[index]?.resolve({ openAlerts: 9, runningTasks: 9 });
      }
    });
    expect(result.current.data).toBeNull();

    await act(async () => {
      queued[afterReturn - 1]?.resolve({ openAlerts: 1, runningTasks: 0 });
    });
    await waitFor(() => expect(result.current.data).toEqual({ openAlerts: 1, runningTasks: 0 }));

    await act(async () => {
      for (let index = afterSwitch; index < afterReturn - 1; index += 1) {
        queued[index]?.resolve({ openAlerts: 8, runningTasks: 8 });
      }
    });
    expect(result.current.data).toEqual({ openAlerts: 1, runningTasks: 0 });
  });
});
