import { useCallback, useState, type PropsWithChildren } from "react";
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthContext, type AuthContextValue, type AuthRole } from "@/context/auth-context.shared";
import {
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  rememberAuthIdentity,
  releaseAuthTransitionBarrier,
} from "@/lib/api/core";
import type { IntegrationChannel, NewIntegrationInput } from "@/types/domain";
import { useAlertsIntegrationsDomain } from "./use-console-alerts-integrations-domain";

const mocks = vi.hoisted(() => ({
  api: {
    getIntegrations: vi.fn(),
    createIntegration: vi.fn(),
    updateIntegration: vi.fn(),
    patchIntegration: vi.fn(),
    deleteIntegration: vi.fn(),
    testIntegration: vi.fn(),
  },
  auth: { token: "A" as string | null, role: "admin" as AuthRole | null },
  demo: { enabled: false },
}));

vi.mock("@/lib/api/client", () => ({ apiClient: mocks.api }));

type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void; reject: (error: unknown) => void };

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function channel(id: string, name: string): IntegrationChannel {
  return {
    id,
    type: "webhook",
    name,
    endpoint: "https://hooks.example.test/xirang",
    hasSecret: false,
    enabled: true,
    failThreshold: 1,
    cooldownMinutes: 5,
    proxyUrl: "",
  };
}

const input: NewIntegrationInput = {
  type: "webhook",
  name: "Demo hook",
  endpoint: "https://hooks.example.test/demo",
  failThreshold: 1,
  cooldownMinutes: 5,
  enabled: true,
};

type Rendered = { token: string | null; names: string[] };
let renders: Rendered[] = [];

function useHarness(token: string | null) {
  const [integrations, setIntegrations] = useState<IntegrationChannel[]>([]);
  const [warning, setWarning] = useState<string | null>(null);
  const ensureDemoWriteAllowed = useCallback((action: string) => {
    if (!mocks.demo.enabled) throw new Error(`no backend: ${action}`);
  }, []);
  const handleWriteApiError = useCallback((action: string, error: unknown) => {
    setWarning(`${action}: ${error instanceof Error ? error.message : "failed"}`);
    if (!mocks.demo.enabled) throw error;
  }, []);
  const domain = useAlertsIntegrationsDomain({
    token,
    alerts: [],
    integrations,
    setAlerts: vi.fn(),
    setIntegrations,
    setWarning,
    ensureDemoWriteAllowed,
    handleWriteApiError,
    retryTask: vi.fn(),
  });
  renders.push({ token, names: domain.integrations.map((item) => item.name) });
  return { ...domain, warning, setRawIntegrations: setIntegrations };
}

function authValue(): AuthContextValue {
  return {
    token: mocks.auth.token,
    username: "user",
    role: mocks.auth.role,
    userId: 1,
    totpEnabled: true,
    isAuthenticated: Boolean(mocks.auth.token),
    authTransitioning: false,
    beginTOTPActivation: vi.fn(() => 1),
    abortTOTPActivation: vi.fn(),
    completeTOTPActivation: vi.fn(() => false),
    login: vi.fn(),
    logout: vi.fn(),
    setTotpEnabled: vi.fn(),
    ensureStepUpProof: vi.fn(),
    clearStepUpProof: vi.fn(),
  };
}

function renderDomain() {
  const view = renderHook(({ token }: { token: string | null }) => useHarness(token), {
    initialProps: { token: mocks.auth.token },
    wrapper: ({ children }: PropsWithChildren) => (
      <AuthContext.Provider value={authValue()}>{children}</AuthContext.Provider>
    ),
  });
  return {
    result: view.result,
    switchIdentity(token: string | null, role: AuthRole | null, bump = true) {
      act(() => {
        mocks.auth.token = token;
        mocks.auth.role = role;
        rememberAuthIdentity(token, role);
        if (bump) bumpAuthSessionGeneration();
      });
      view.rerender({ token });
    },
    async load(rows: IntegrationChannel[]) {
      mocks.api.getIntegrations.mockResolvedValueOnce(rows);
      await act(async () => {
        await view.result.current.refreshIntegrations();
      });
    },
    names() {
      return view.result.current.integrations.map((item) => item.name);
    },
  };
}

function signalOf(call: number): AbortSignal {
  const options: unknown = mocks.api.getIntegrations.mock.calls[call]?.[1];
  if (!options || typeof options !== "object" || !("signal" in options) || !(options.signal instanceof AbortSignal)) {
    throw new Error(`getIntegrations call ${call} had no abort signal`);
  }
  return options.signal;
}

beforeEach(() => {
  for (const fn of Object.values(mocks.api)) fn.mockReset();
  mocks.auth.token = "A";
  mocks.auth.role = "admin";
  mocks.demo.enabled = false;
  renders = [];
  clearAuthTransitionBarrier();
  rememberAuthIdentity("A", "admin");
  bumpAuthSessionGeneration();
});

afterEach(() => {
  clearAuthTransitionBarrier();
  rememberAuthIdentity(null, null);
});

describe("useAlertsIntegrationsDomain ownership", () => {
  it("reads with a real abort signal and keeps current admin saves visible", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    expect(mocks.api.getIntegrations).toHaveBeenCalledWith("A", { signal: expect.any(AbortSignal) });
    expect(view.names()).toEqual(["A hook"]);

    mocks.api.createIntegration.mockResolvedValueOnce(channel("int-2", "A created"));
    mocks.api.patchIntegration.mockResolvedValueOnce(channel("int-1", "A renamed"));
    await act(async () => {
      await view.result.current.addIntegration(input);
      await view.result.current.patchIntegration("int-1", { name: "A renamed" });
    });
    expect(view.names()).toEqual(["A created", "A renamed"]);
  });

  it("aborts an old read and masks old rows in the first render for a new identity", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    const stale = deferred<IntegrationChannel[]>();
    mocks.api.getIntegrations.mockReturnValueOnce(stale.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = view.result.current.refreshIntegrations();
    });

    renders = [];
    view.switchIdentity("B", "admin");
    expect(signalOf(1).aborted).toBe(true);
    expect(renders.filter((item) => item.token === "B").every((item) => item.names.length === 0)).toBe(true);
    expect(renders.every((item) => !item.names.includes("A hook"))).toBe(true);

    await view.load([channel("int-9", "B hook")]);
    await act(async () => {
      stale.resolve([channel("int-1", "A late")]);
      await pending;
    });
    expect(view.names()).toEqual(["B hook"]);
  });

  it("ignores old create, patch, and failures after the new identity has loaded", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    const created = deferred<IntegrationChannel>();
    const patched = deferred<IntegrationChannel>();
    const failed = deferred<IntegrationChannel>();
    mocks.api.createIntegration.mockReturnValueOnce(created.promise).mockReturnValueOnce(failed.promise);
    mocks.api.patchIntegration.mockReturnValueOnce(patched.promise);
    const create = view.result.current.addIntegration(input);
    const patch = view.result.current.patchIntegration("int-1", { name: "A patched" });
    const createFailure = view.result.current.addIntegration(input);

    view.switchIdentity("B", "admin");
    await view.load([channel("int-9", "B hook")]);

    created.resolve(channel("int-2", "A created"));
    patched.resolve(channel("int-9", "A patched"));
    failed.reject(new Error("A failure"));
    await act(async () => {
      await expect(create).rejects.toMatchObject({ name: "AbortError" });
      await expect(patch).rejects.toMatchObject({ name: "AbortError" });
      await expect(createFailure).rejects.toMatchObject({ name: "AbortError" });
    });
    expect(view.names()).toEqual(["B hook"]);
    expect(view.result.current.warning).toBeNull();
  });

  it("does not let an A→B→A return revive the first A session's late responses", async () => {
    const view = renderDomain();
    const stale = deferred<IntegrationChannel[]>();
    const staleCreate = deferred<IntegrationChannel>();
    mocks.api.getIntegrations.mockReturnValueOnce(stale.promise);
    mocks.api.createIntegration.mockReturnValueOnce(staleCreate.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = view.result.current.refreshIntegrations();
    });
    const create = view.result.current.addIntegration(input);

    view.switchIdentity("B", "admin");
    view.switchIdentity("A", "admin");
    expect(signalOf(0).aborted).toBe(true);
    await view.load([channel("int-3", "A second session")]);

    await act(async () => {
      stale.resolve([channel("int-1", "A first session")]);
      staleCreate.resolve(channel("int-2", "A first create"));
      await pending;
      await expect(create).rejects.toMatchObject({ name: "AbortError" });
    });
    expect(view.names()).toEqual(["A second session"]);
  });

  it("masks immediately on a same-token generation bump and drops the old read", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    const stale = deferred<IntegrationChannel[]>();
    mocks.api.getIntegrations.mockReturnValueOnce(stale.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = view.result.current.refreshIntegrations();
    });

    act(() => {
      bumpAuthSessionGeneration();
    });
    expect(view.names()).toEqual([]);
    expect(signalOf(1).aborted).toBe(true);

    await act(async () => {
      stale.resolve([channel("int-1", "A late")]);
      await pending;
    });
    expect(view.names()).toEqual([]);
  });

  it("keeps old token responses out of the demo collection after logout", async () => {
    mocks.demo.enabled = true;
    const view = renderDomain();
    const stale = deferred<IntegrationChannel[]>();
    const staleCreate = deferred<IntegrationChannel>();
    mocks.api.getIntegrations.mockReturnValueOnce(stale.promise);
    mocks.api.createIntegration.mockReturnValueOnce(staleCreate.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = view.result.current.refreshIntegrations();
    });
    const create = view.result.current.addIntegration({ ...input, name: "A create" });

    view.switchIdentity(null, null);
    expect(view.names()).toEqual([]);
    await act(async () => {
      await view.result.current.addIntegration(input);
    });
    expect(view.names()).toEqual(["Demo hook"]);

    await act(async () => {
      stale.resolve([channel("int-1", "A late")]);
      staleCreate.resolve(channel("int-2", "A create"));
      await pending;
      await expect(create).rejects.toMatchObject({ name: "AbortError" });
    });
    expect(view.names()).toEqual(["Demo hook"]);
  });

  it("masks on a same-token downgrade, keeps reads, and rejects channel writes", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);

    renders = [];
    view.switchIdentity("A", "operator", false);
    expect(renders.every((item) => item.names.length === 0)).toBe(true);

    await view.load([channel("int-1", "Operator view")]);
    expect(view.names()).toEqual(["Operator view"]);
    await expect(view.result.current.addIntegration(input)).rejects.toMatchObject({ name: "AbortError" });
    await expect(view.result.current.toggleIntegration("int-1")).rejects.toMatchObject({ name: "AbortError" });
    expect(mocks.api.createIntegration).not.toHaveBeenCalled();
    expect(mocks.api.patchIntegration).not.toHaveBeenCalled();
  });

  it("masks during an auth barrier and does not restore the old collection on release", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    const stale = deferred<IntegrationChannel[]>();
    mocks.api.getIntegrations.mockReturnValueOnce(stale.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = view.result.current.refreshIntegrations();
    });

    let barrier = 0;
    act(() => {
      barrier = beginAuthTransitionBarrier();
    });
    expect(view.names()).toEqual([]);
    expect(signalOf(1).aborted).toBe(true);
    await expect(view.result.current.patchIntegration("int-1", { name: "x" })).rejects.toMatchObject({ name: "AbortError" });

    await act(async () => {
      stale.resolve([channel("int-1", "A late")]);
      await pending;
    });
    act(() => {
      releaseAuthTransitionBarrier(barrier);
    });
    expect(view.names()).toEqual([]);
    expect(mocks.api.patchIntegration).not.toHaveBeenCalled();

    await view.load([channel("int-1", "A reloaded")]);
    expect(view.names()).toEqual(["A reloaded"]);
  });

  it("re-checks ownership when React runs a queued integration updater", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    const created = deferred<IntegrationChannel>();
    mocks.api.createIntegration.mockReturnValueOnce(created.promise);
    const create = view.result.current.addIntegration(input);

    await act(async () => {
      // 先排入一项更新，使渠道 updater 留在队列中直到 React 渲染时才执行。
      view.result.current.setRawIntegrations((prev) => [...prev]);
      created.resolve(channel("int-2", "A created"));
      await create;
      rememberAuthIdentity("B", "admin");
    });
    expect(view.names()).toEqual(["A hook"]);
    rememberAuthIdentity("A", "admin");
  });

  it("keeps a channel and reports the current failure when deletion fails", async () => {
    const view = renderDomain();
    await view.load([channel("int-1", "A hook")]);
    mocks.api.deleteIntegration.mockRejectedValueOnce(new Error("delete failed"));

    await act(async () => {
      await expect(view.result.current.removeIntegration("int-1")).rejects.toThrow("delete failed");
    });
    expect(view.names()).toEqual(["A hook"]);
    expect(view.result.current.warning).toContain("delete failed");
  });
});
