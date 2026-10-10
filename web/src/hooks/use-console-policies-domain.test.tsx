import "@testing-library/jest-dom/vitest";
import { act, render, renderHook, screen } from "@testing-library/react";
import { useEffect, useState } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PoliciesContextProvider } from "@/context/policies-context";
import { PoliciesPage } from "@/pages/policies-page";
import type { AlertRecord, PolicyRecord, TaskRecord } from "@/types/domain";
import { usePoliciesDomain } from "./use-console-policies-domain";

type PolicyResponse = {
  token: string;
  signal: AbortSignal | undefined;
  resolve: (value: PolicyRecord[]) => void;
  reject: (error: unknown) => void;
};

const { authRef, policyRequests, apiClientMock, refreshNodesMock, setGlobalSearchMock } = vi.hoisted(() => ({
  authRef: {
    current: {
      token: "token-a" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
      authTransitioning: false,
    },
  },
  policyRequests: [] as PolicyResponse[],
  apiClientMock: {
    getPolicies: vi.fn((token: string, options?: { signal?: AbortSignal }) => new Promise<PolicyRecord[]>((resolve, reject) => {
      policyRequests.push({ token, signal: options?.signal, resolve, reject });
    })),
  },
  refreshNodesMock: vi.fn().mockResolvedValue(undefined),
  setGlobalSearchMock: vi.fn(),
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => ({
    globalSearch: "",
    setGlobalSearch: setGlobalSearchMock,
  }),
}));

vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => ({
    nodes: [],
    refreshNodes: refreshNodesMock,
  }),
}));

function createPolicy(id: number, name: string): PolicyRecord {
  return {
    id,
    name,
    sourcePath: `/data/${id}/src`,
    targetPath: `/data/${id}/dst`,
    cron: "0 2 * * *",
    naturalLanguage: "每天凌晨 2 点",
    enabled: true,
    criticalThreshold: 1,
    nodeIds: [],
    verifyEnabled: false,
    verifySampleRate: 0,
    drillEnabled: false,
    drillCron: "",
    drillRestorePath: "/tmp/xirang-drill",
    drillPreVerify: "",
    drillVerify: "",
    drillPostVerify: "",
    drillAutoCleanup: true,
  };
}

function abortError(): Error {
  const error = new Error("aborted");
  error.name = "AbortError";
  return error;
}

type DomainProps = {
  token: string | null;
};

function useDomainHarness({ token }: DomainProps) {
  const [policies, setPolicies] = useState<PolicyRecord[]>([]);
  const [tasks, setTasks] = useState<TaskRecord[]>([]);
  const [alerts, setAlerts] = useState<AlertRecord[]>([]);
  const domain = usePoliciesDomain({
    token,
    policies,
    setPolicies,
    setTasks,
    setAlerts,
    markTasksMutated: () => {},
    ensureDemoWriteAllowed: () => {},
    handleWriteApiError: () => {},
  });
  return { ...domain, tasks, alerts, setTasks, setAlerts };
}

async function flushDomain() {
  await act(async () => {
    await Promise.resolve();
  });
}

function requestFor(token: string): PolicyResponse {
  const match = [...policyRequests].reverse().find((request) => request.token === token);
  if (!match) throw new Error(`missing policy request for ${token}`);
  return match;
}

describe("usePoliciesDomain identity readiness", () => {
  beforeEach(() => {
    policyRequests.splice(0);
    apiClientMock.getPolicies.mockClear();
    authRef.current = { token: "token-a", role: "admin", authTransitioning: false };
  });

  afterEach(async () => {
    const pending = policyRequests.splice(0);
    await act(async () => {
      for (const request of pending) request.reject(abortError());
    });
  });

  it("同令牌刷新保留已成功库存，失败也不把 loaded 打回未就绪", async () => {
    const { result } = renderHook(useDomainHarness, { initialProps: { token: "token-a" } });
    expect(result.current.policiesLoaded).toBe(false);

    await act(async () => {
      void result.current.refreshPolicies();
    });
    expect(result.current.policiesLoading).toBe(true);
    expect(result.current.policiesLoaded).toBe(false);

    const first = requestFor("token-a");
    await act(async () => {
      first.resolve([createPolicy(1, "账户A策略")]);
    });
    expect(result.current.policiesLoaded).toBe(true);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);

    await act(async () => {
      void result.current.refreshPolicies();
    });
    expect(result.current.policiesLoading).toBe(true);
    expect(result.current.policiesLoaded).toBe(true);
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);

    const refresh = requestFor("token-a");
    expect(refresh).not.toBe(first);
    expect(first.signal?.aborted).toBe(true);
    await act(async () => {
      refresh.reject(new Error("策略列表暂时失败"));
    });
    expect(result.current.policiesLoading).toBe(false);
    expect(result.current.policiesLoaded).toBe(true);
    expect(result.current.policiesError).toBe("策略列表暂时失败");
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);
    expect(result.current.tasks).toEqual([]);
    expect(result.current.alerts).toEqual([]);
  });

  it("令牌 A 的成功与在途结果都不能让令牌 B 就绪，旧闭包也不能再发起刷新", async () => {
    const { result, rerender } = renderHook(useDomainHarness, { initialProps: { token: "token-a" } });
    await act(async () => {
      void result.current.refreshPolicies();
    });
    const firstA = requestFor("token-a");
    await act(async () => {
      firstA.resolve([createPolicy(1, "账户A策略")]);
    });
    expect(result.current.policiesLoaded).toBe(true);

    const refreshA = result.current.refreshPolicies;
    rerender({ token: "token-b" });
    expect(result.current.policiesLoaded).toBe(false);
    expect(result.current.policiesLoading).toBe(false);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);

    await act(async () => {
      await refreshA();
    });
    expect(apiClientMock.getPolicies.mock.calls.filter(([token]) => token === "token-a")).toHaveLength(1);

    await act(async () => {
      void result.current.refreshPolicies();
    });
    const requestB = requestFor("token-b");
    expect(result.current.policiesLoading).toBe(true);
    expect(result.current.policiesLoaded).toBe(false);

    await act(async () => {
      firstA.resolve([createPolicy(1, "过期账户A策略")]);
      await refreshA();
    });
    expect(result.current.policiesLoaded).toBe(false);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);
    expect(requestB.signal?.aborted).toBe(false);

    await act(async () => {
      requestB.resolve([createPolicy(2, "账户B策略")]);
    });
    expect(result.current.policiesLoaded).toBe(true);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户B策略"]);
  });

  it("A→B→A 时旧成功和两边的在途响应都不能冒充当前身份", async () => {
    const { result, rerender } = renderHook(useDomainHarness, { initialProps: { token: "token-a" } });
    await act(async () => {
      void result.current.refreshPolicies();
    });
    const firstA = requestFor("token-a");
    await act(async () => {
      firstA.resolve([createPolicy(1, "账户A策略")]);
    });

    await act(async () => {
      void result.current.refreshPolicies();
    });
    const pendingA = requestFor("token-a");
    expect(pendingA).not.toBe(firstA);

    rerender({ token: "token-b" });
    expect(result.current.policiesLoaded).toBe(false);
    expect(pendingA.signal?.aborted).toBe(true);
    await act(async () => {
      void result.current.refreshPolicies();
    });
    const requestB = requestFor("token-b");

    const refreshB = result.current.refreshPolicies;
    rerender({ token: "token-a" });
    expect(result.current.policiesLoaded).toBe(false);
    expect(result.current.policiesLoading).toBe(false);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);
    expect(requestB.signal?.aborted).toBe(true);

    await act(async () => {
      await refreshB();
      pendingA.resolve([createPolicy(1, "过期账户A策略")]);
      requestB.resolve([createPolicy(2, "账户B策略")]);
      requestB.reject(new Error("令牌B失败"));
    });
    expect(result.current.policiesLoaded).toBe(false);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);

    await act(async () => {
      void result.current.refreshPolicies();
    });
    const secondA = requestFor("token-a");
    expect(secondA).not.toBe(pendingA);
    expect(secondA.signal?.aborted).toBe(false);
    await act(async () => {
      secondA.resolve([createPolicy(3, "账户A再次成功")]);
    });
    expect(result.current.policiesLoaded).toBe(true);
    expect(result.current.policiesError).toBeNull();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A再次成功"]);
  });

  it("无令牌视为该身份已就绪，回到有令牌的身份后必须重新成功加载", async () => {
    const { result, rerender } = renderHook(useDomainHarness, { initialProps: { token: "token-a" as string | null } });
    await act(async () => {
      void result.current.refreshPolicies();
    });
    await act(async () => {
      requestFor("token-a").resolve([createPolicy(1, "账户A策略")]);
    });

    rerender({ token: null });
    expect(result.current.policiesLoaded).toBe(true);
    expect(result.current.policiesLoading).toBe(false);
    expect(result.current.policiesError).toBeNull();

    rerender({ token: "token-a" });
    expect(result.current.policiesLoaded).toBe(false);
    expect(result.current.policiesError).toBeNull();
    await flushDomain();
    expect(result.current.policies.map((policy) => policy.name)).toEqual(["账户A策略"]);
  });
});

const domainRef: { current: { refreshPolicies: () => Promise<void> } | null } = { current: null };

function ProducerHarness() {
  const [policies, setPolicies] = useState<PolicyRecord[]>([]);
  const [, setTasks] = useState<TaskRecord[]>([]);
  const [, setAlerts] = useState<AlertRecord[]>([]);
  const domain = usePoliciesDomain({
    token: authRef.current.token,
    policies,
    setPolicies,
    setTasks,
    setAlerts,
    markTasksMutated: () => {},
    ensureDemoWriteAllowed: () => {},
    handleWriteApiError: () => {},
  });
  useEffect(() => {
    domainRef.current = { refreshPolicies: domain.refreshPolicies };
  }, [domain.refreshPolicies]);
  return (
    <MemoryRouter initialEntries={["/app/policies?policyId=1"]}>
      <PoliciesContextProvider value={domain}>
        <PoliciesPage />
      </PoliciesContextProvider>
    </MemoryRouter>
  );
}

describe("policies page consumes identity-scoped readiness", () => {
  beforeEach(() => {
    policyRequests.splice(0);
    apiClientMock.getPolicies.mockClear();
    authRef.current = { token: "token-a", role: "admin", authTransitioning: false };
    window.localStorage.clear();
  });

  afterEach(async () => {
    const pending = policyRequests.splice(0);
    await act(async () => {
      for (const request of pending) request.reject(abortError());
    });
  });

  it("已成功的账户A库存在令牌换成B后不再定位，B的库存到达后才聚焦一次", async () => {
    const view = render(<ProducerHarness />);
    expect(screen.queryByRole("heading", { name: /已定位策略/ })).not.toBeInTheDocument();
    expect(screen.queryByText("找不到该策略或当前账户不可见。")).not.toBeInTheDocument();
    expect(screen.getAllByText("策略数据加载中").length).toBeGreaterThan(0);

    await act(async () => {
      requestFor("token-a").resolve([createPolicy(1, "账户A策略")]);
    });
    expect(screen.getByRole("heading", { name: "已定位策略 账户A策略（ID 1）" })).toHaveFocus();

    await act(async () => {
      void domainRef.current?.refreshPolicies();
    });
    const pendingA = requestFor("token-a");
    expect(pendingA.signal?.aborted).toBe(false);

    authRef.current = { token: "token-b", role: "admin", authTransitioning: false };
    view.rerender(<ProducerHarness />);
    expect(pendingA.signal?.aborted).toBe(true);
    expect(screen.queryByRole("heading", { name: /已定位策略/ })).not.toBeInTheDocument();
    expect(screen.queryByText("找不到该策略或当前账户不可见。")).not.toBeInTheDocument();
    expect(screen.queryByText("账户A策略")).not.toBeInTheDocument();
    expect(screen.queryByText("暂无匹配策略")).not.toBeInTheDocument();

    await act(async () => {
      pendingA.resolve([createPolicy(1, "过期账户A策略")]);
    });
    expect(screen.queryByText("过期账户A策略")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /已定位策略/ })).not.toBeInTheDocument();

    await act(async () => {
      requestFor("token-b").resolve([createPolicy(1, "账户B策略")]);
    });
    const locatedB = "已定位策略 账户B策略（ID 1）";
    expect(screen.getByRole("heading", { name: locatedB })).toHaveFocus();

    screen.getByRole("button", { name: "显示全部策略" }).focus();
    view.rerender(<ProducerHarness />);
    expect(screen.getByRole("button", { name: "显示全部策略" })).toHaveFocus();
    expect(screen.getByRole("heading", { name: locatedB })).not.toHaveFocus();
  });
});
