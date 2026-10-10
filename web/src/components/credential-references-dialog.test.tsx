import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { CredentialReferencesDialog } from "./credential-references-dialog";
import { PoliciesPage } from "@/pages/policies-page";
import { ApiError } from "@/lib/api/core";
import i18n from "@/i18n";

const { authState, listReferencesMock } = vi.hoisted(() => ({
  authState: {
    current: {
      token: "test-token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
      authTransitioning: false,
    },
  },
  listReferencesMock: vi.fn(),
}));

const sharedRef: { current: Record<string, unknown> } = { current: {} };
const nodesRef: { current: Record<string, unknown> } = { current: {} };
const policiesRef: { current: Record<string, unknown> } = { current: {} };

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authState.current,
}));

vi.mock("@/lib/api/credentials", async (original) => {
  const actual = await original<typeof import("@/lib/api/credentials")>();
  return {
    ...actual,
    createCredentialsApi: () => ({
      listReferences: (...args: unknown[]) => listReferencesMock(...args),
    }),
  };
});

vi.mock("@/context/shared-context.hooks", () => ({
  useSharedContext: () => sharedRef.current,
}));
vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContext: () => nodesRef.current,
}));
vi.mock("@/context/policies-context.hooks", () => ({
  usePoliciesContext: () => policiesRef.current,
}));
vi.mock("@/components/policy-editor-dialog", () => ({
  PolicyEditorDialog: () => null,
}));
vi.mock("@/lib/api/client", () => ({
  apiClient: {
    batchTogglePolicies: vi.fn(),
    clonePolicyFromTemplate: vi.fn(),
  },
}));
vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

type ReferencePage = {
  items: { id: number; name: string }[];
  total: number;
  page: number;
  pageSize: number;
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function range(start: number, end: number): number[] {
  return Array.from({ length: end - start + 1 }, (_, index) => start + index);
}

function referencePage(ids: number[], total: number, page: number): ReferencePage {
  return {
    items: ids.map((id) => ({ id, name: `策略 ${id}` })),
    total,
    page,
    pageSize: 20,
  };
}

function policyLinkName(id: number): string {
  return `策略 ${id}，策略 ID ${id}`;
}

function dialogElement(
  open: boolean,
  credential: { id: number; name: string } | null = { id: 7, name: "生产库" },
  onOpenChange: (open: boolean) => void = vi.fn(),
) {
  return (
    <MemoryRouter>
      <CredentialReferencesDialog
        open={open}
        onOpenChange={onOpenChange}
        credential={credential}
      />
    </MemoryRouter>
  );
}

function installPolicyInventory(policies: { id: number; name: string }[]) {
  sharedRef.current = {
    loading: false,
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    warning: null,
    lastSyncedAt: "",
    refreshVersion: 0,
    refresh: vi.fn(),
    overview: {},
    fetchOverviewTraffic: vi.fn(),
  };
  nodesRef.current = {
    nodes: [],
    refreshNodes: vi.fn().mockResolvedValue(undefined),
    nodesLoading: false,
    nodesError: null,
    nodesLoaded: true,
  };
  policiesRef.current = {
    policies,
    createPolicy: vi.fn(),
    updatePolicy: vi.fn(),
    deletePolicy: vi.fn(),
    togglePolicy: vi.fn(),
    refreshPolicies: vi.fn().mockResolvedValue(undefined),
    policiesLoading: false,
    policiesError: null,
    policiesLoaded: true,
    updatePolicySchedule: vi.fn(),
  };
}

describe("CredentialReferencesDialog", () => {
  beforeEach(() => {
    authState.current = { token: "test-token", role: "admin", authTransitioning: false };
    listReferencesMock.mockReset();
    listReferencesMock.mockResolvedValue(referencePage([1], 1, 1));
    window.localStorage.removeItem("xirang.policies.keyword");
  });

  afterEach(() => {
    cleanup();
    window.localStorage.removeItem("xirang.policies.keyword");
  });

  it("lists exact policy IDs across fixed pages of 20", async () => {
    const user = userEvent.setup();
    listReferencesMock.mockImplementation(async (_token: string, _id: number, options: { page: number }) => {
      if (options.page === 2) return referencePage(range(21, 40), 45, 2);
      if (options.page === 3) return referencePage(range(41, 45), 45, 3);
      return referencePage(range(1, 20), 45, 1);
    });

    render(dialogElement(true));

    expect(await screen.findByRole("dialog", { name: "引用 生产库 的策略" })).toHaveAccessibleDescription(
      "引用可能变化；删除时服务器会重新检查。此查询不会授权删除。",
    );
    for (const id of range(1, 20)) {
      expect(screen.getByRole("link", { name: policyLinkName(id) })).toHaveAttribute(
        "href",
        `/app/policies?policyId=${id}`,
      );
    }
    expect(screen.queryByRole("link", { name: policyLinkName(21) })).not.toBeInTheDocument();
    expect(screen.getByText("第 1 页 · 共 45 条")).toBeInTheDocument();
    expect(listReferencesMock).toHaveBeenCalledWith(
      "test-token",
      7,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );

    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(await screen.findByRole("link", { name: policyLinkName(21) })).toBeInTheDocument();
    for (const id of range(21, 40)) {
      expect(screen.getByRole("link", { name: policyLinkName(id) })).toBeInTheDocument();
    }
    expect(screen.queryByRole("link", { name: policyLinkName(1) })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(41) })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(await screen.findByRole("link", { name: policyLinkName(41) })).toBeInTheDocument();
    for (const id of range(41, 45)) {
      expect(screen.getByRole("link", { name: policyLinkName(id) })).toBeInTheDocument();
    }
    expect(screen.queryByRole("link", { name: policyLinkName(40) })).not.toBeInTheDocument();
    expect(screen.getByText("第 3 页 · 共 45 条")).toBeInTheDocument();
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "test-token",
      7,
      { page: 3, pageSize: 20 },
      expect.any(AbortSignal),
    );
  });

  it("ignores a delayed first page that resolves after the second page", async () => {
    const user = userEvent.setup();
    const established = deferred<ReferencePage>();
    const refreshedFirst = deferred<ReferencePage>();
    const second = deferred<ReferencePage>();
    let pageOneCalls = 0;
    listReferencesMock.mockImplementation((_token: string, _id: number, options: { page: number }) => {
      if (options.page === 2) return second.promise;
      pageOneCalls += 1;
      return pageOneCalls === 1 ? established.promise : refreshedFirst.promise;
    });

    render(dialogElement(true));
    await act(async () => {
      established.resolve(referencePage(range(1, 20), 45, 1));
    });
    expect(await screen.findByRole("link", { name: policyLinkName(1) })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => expect(listReferencesMock).toHaveBeenCalledTimes(2));
    const refreshedSignal = listReferencesMock.mock.calls[1][3] as AbortSignal;
    expect(screen.queryByRole("link", { name: policyLinkName(1) })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(listReferencesMock.mock.calls.some((call) => call[2]?.page === 2)).toBe(true));
    expect(refreshedSignal.aborted).toBe(true);
    expect(screen.queryByRole("link", { name: policyLinkName(1) })).not.toBeInTheDocument();

    await act(async () => {
      second.resolve(referencePage([21], 45, 2));
    });
    expect(await screen.findByRole("link", { name: policyLinkName(21) })).toBeInTheDocument();

    await act(async () => {
      refreshedFirst.resolve(referencePage([1], 45, 1));
    });
    expect(screen.getByRole("link", { name: policyLinkName(21) })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(1) })).not.toBeInTheDocument();
  });

  it("hides the current links while a refresh is still in flight", async () => {
    const user = userEvent.setup();
    const refresh = deferred<ReferencePage>();
    let calls = 0;
    listReferencesMock.mockImplementation(() => {
      calls += 1;
      if (calls === 1) return Promise.resolve(referencePage([3], 1, 1));
      return refresh.promise;
    });

    render(dialogElement(true));
    expect(await screen.findByRole("link", { name: policyLinkName(3) })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "刷新" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: policyLinkName(3) })).not.toBeInTheDocument());

    await act(async () => {
      refresh.resolve(referencePage([8], 1, 1));
    });
    expect(await screen.findByRole("link", { name: policyLinkName(8) })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(3) })).not.toBeInTheDocument();
  });

  it("does not request again when only the open-change callback identity changes", async () => {
    const first = deferred<ReferencePage>();
    listReferencesMock.mockReturnValueOnce(first.promise);
    const view = render(dialogElement(true, { id: 7, name: "生产库" }, vi.fn()));
    await waitFor(() => expect(listReferencesMock).toHaveBeenCalledTimes(1));

    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, vi.fn()));
    await act(async () => {
      await Promise.resolve();
    });
    expect(listReferencesMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      first.resolve(referencePage([2], 1, 1));
    });
    expect(await screen.findByRole("link", { name: policyLinkName(2) })).toBeInTheDocument();
  });

  it("does not restore a response that arrives after close or on reopen", async () => {
    const responses: Array<ReturnType<typeof deferred<ReferencePage>>> = [];
    listReferencesMock.mockImplementation(() => {
      const pending = deferred<ReferencePage>();
      responses.push(pending);
      return pending.promise;
    });
    const view = render(dialogElement(true));
    await waitFor(() => expect(responses).toHaveLength(1));

    view.rerender(dialogElement(false));
    await act(async () => {
      responses[0].resolve(referencePage([4], 1, 1));
    });
    expect(screen.queryByRole("link", { name: policyLinkName(4) })).not.toBeInTheDocument();

    view.rerender(dialogElement(true));
    await waitFor(() => expect(responses).toHaveLength(2));
    await act(async () => {
      responses[1].resolve(referencePage([5], 1, 1));
    });
    expect(await screen.findByRole("link", { name: policyLinkName(5) })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(4) })).not.toBeInTheDocument();
  });

  it("drops stale credential results across A to B to A", async () => {
    const responses: Array<ReturnType<typeof deferred<ReferencePage>>> = [];
    listReferencesMock.mockImplementation(() => {
      const pending = deferred<ReferencePage>();
      responses.push(pending);
      return pending.promise;
    });
    const view = render(dialogElement(true, { id: 1, name: "甲" }));
    await waitFor(() => expect(responses).toHaveLength(1));
    view.rerender(dialogElement(true, { id: 2, name: "乙" }));
    await waitFor(() => expect(responses).toHaveLength(2));
    view.rerender(dialogElement(true, { id: 1, name: "甲" }));
    await waitFor(() => expect(responses).toHaveLength(3));

    await act(async () => {
      responses[2].resolve({
        items: [{ id: 30, name: "新的甲" }],
        total: 1,
        page: 1,
        pageSize: 20,
      });
    });
    expect(await screen.findByRole("link", { name: "新的甲，策略 ID 30" })).toBeInTheDocument();

    await act(async () => {
      responses[0].resolve({
        items: [{ id: 10, name: "旧的甲" }],
        total: 1,
        page: 1,
        pageSize: 20,
      });
      responses[1].resolve({
        items: [{ id: 20, name: "乙策略" }],
        total: 1,
        page: 1,
        pageSize: 20,
      });
    });
    expect(screen.getByRole("link", { name: "新的甲，策略 ID 30" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "旧的甲，策略 ID 10" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "乙策略，策略 ID 20" })).not.toBeInTheDocument();
  });

  it.each([2, 3])(
    "reopens established page %s from a delayed page 1 without old links",
    async (establishedPage) => {
      const user = userEvent.setup();
      listReferencesMock.mockImplementation(async (_token: string, _id: number, options: { page: number }) => {
        if (options.page === 2) return referencePage(range(21, 40), 45, 2);
        if (options.page === 3) return referencePage(range(41, 45), 45, 3);
        return referencePage(range(1, 20), 45, 1);
      });
      const view = render(dialogElement(true));
      expect(await screen.findByRole("link", { name: policyLinkName(1) })).toBeInTheDocument();
      for (let page = 2; page <= establishedPage; page += 1) {
        await user.click(screen.getByRole("button", { name: "下一页" }));
        const firstId = page === 2 ? 21 : 41;
        expect(await screen.findByRole("link", { name: policyLinkName(firstId) })).toBeInTheDocument();
      }
      expect(screen.getByText(`第 ${establishedPage} 页 · 共 45 条`)).toBeInTheDocument();

      const callsBeforeClose = listReferencesMock.mock.calls.length;
      const pending = deferred<ReferencePage>();
      listReferencesMock.mockImplementation(() => pending.promise);
      view.rerender(dialogElement(false));
      view.rerender(dialogElement(true));
      await waitFor(() => expect(listReferencesMock).toHaveBeenCalledTimes(callsBeforeClose + 1));
      expect(listReferencesMock).toHaveBeenLastCalledWith(
        "test-token",
        7,
        { page: 1, pageSize: 20 },
        expect.any(AbortSignal),
      );
      expect(screen.getByRole("dialog", { name: "引用 生产库 的策略" })).toBeInTheDocument();
      expect(screen.getByRole("status")).toHaveTextContent("加载中...");
      expect(screen.queryByRole("link")).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "上一页" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "下一页" })).not.toBeInTheDocument();
      expect(screen.queryByText("第 2 页 · 共 45 条")).not.toBeInTheDocument();
      expect(screen.queryByText("第 3 页 · 共 45 条")).not.toBeInTheDocument();
    },
  );

  it("resets a completed credential to page 1 without stale rows when it returns", async () => {
    const user = userEvent.setup();
    const held: Array<ReturnType<typeof deferred<ReferencePage>>> = [];
    let hold = false;
    listReferencesMock.mockImplementation((_token: string, id: number, options: { page: number }) => {
      if (hold) {
        const pending = deferred<ReferencePage>();
        held.push(pending);
        return pending.promise;
      }
      if (id === 2) {
        return Promise.resolve({
          items: [{ id: 20, name: "乙策略" }],
          total: 1,
          page: 1,
          pageSize: 20,
        });
      }
      if (options.page === 2) {
        return Promise.resolve({
          items: [{ id: 12, name: "甲第二页" }],
          total: 45,
          page: 2,
          pageSize: 20,
        });
      }
      return Promise.resolve({
        items: [{ id: 11, name: "旧的甲" }],
        total: 45,
        page: 1,
        pageSize: 20,
      });
    });

    const view = render(dialogElement(true, { id: 1, name: "甲" }));
    expect(await screen.findByRole("link", { name: "旧的甲，策略 ID 11" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(await screen.findByRole("link", { name: "甲第二页，策略 ID 12" })).toBeInTheDocument();

    hold = true;
    view.rerender(dialogElement(true, { id: 2, name: "乙" }));
    await waitFor(() => expect(held).toHaveLength(1));
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "test-token",
      2,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryByRole("link", { name: "旧的甲，策略 ID 11" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "甲第二页，策略 ID 12" })).not.toBeInTheDocument();

    await act(async () => {
      held[0].resolve({
        items: [{ id: 20, name: "乙策略" }],
        total: 1,
        page: 1,
        pageSize: 20,
      });
    });
    expect(await screen.findByRole("link", { name: "乙策略，策略 ID 20" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "旧的甲，策略 ID 11" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "甲第二页，策略 ID 12" })).not.toBeInTheDocument();

    view.rerender(dialogElement(true, { id: 1, name: "甲" }));
    await waitFor(() => expect(held).toHaveLength(2));
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "test-token",
      1,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    await act(async () => {
      held[1].resolve({
        items: [{ id: 30, name: "新的甲" }],
        total: 1,
        page: 1,
        pageSize: 20,
      });
    });
    expect(await screen.findByRole("link", { name: "新的甲，策略 ID 30" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "旧的甲，策略 ID 11" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "甲第二页，策略 ID 12" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "乙策略，策略 ID 20" })).not.toBeInTheDocument();
  });

  it.each([
    ["empty", "当前没有策略引用此凭据。"],
    ["error", "server down"],
    ["missing", "凭据不存在"],
  ] as const)("does not keep a completed %s result while a reopen is still pending", async (kind, assertion) => {
    if (kind === "empty") {
      listReferencesMock.mockResolvedValueOnce({ items: [], total: 0, page: 1, pageSize: 20 });
    } else if (kind === "error") {
      listReferencesMock.mockRejectedValueOnce(new Error("server down"));
    } else {
      listReferencesMock.mockRejectedValueOnce(new ApiError(404, "missing"));
    }
    const view = render(dialogElement(true));
    expect(await screen.findByText(assertion)).toBeInTheDocument();

    const pending = deferred<ReferencePage>();
    listReferencesMock.mockImplementation(() => pending.promise);
    view.rerender(dialogElement(false));
    view.rerender(dialogElement(true));
    await waitFor(() => expect(listReferencesMock).toHaveBeenCalledTimes(2));
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "test-token",
      7,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryByText(assertion)).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    if (kind !== "empty") {
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    }
    if (kind === "error") {
      expect(screen.queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
    }
  });

  it("does not restore page 1 links while page 2 is pending and page 1 is requested again", async () => {
    const user = userEvent.setup();
    const pageTwo = deferred<ReferencePage>();
    const pageOneAgain = deferred<ReferencePage>();
    let pageOneCalls = 0;
    listReferencesMock.mockImplementation((_token: string, _id: number, options: { page: number }) => {
      if (options.page === 2) return pageTwo.promise;
      pageOneCalls += 1;
      if (pageOneCalls === 1) return Promise.resolve(referencePage([11], 45, 1));
      return pageOneAgain.promise;
    });

    render(dialogElement(true));
    expect(await screen.findByRole("link", { name: policyLinkName(11) })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() => expect(listReferencesMock.mock.calls.some((call) => call[2]?.page === 2)).toBe(true));
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryByRole("link", { name: policyLinkName(11) })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "上一页" }));
    await waitFor(() => expect(pageOneCalls).toBe(2));
    const pageTwoSignal = listReferencesMock.mock.calls.find((call) => call[2]?.page === 2)?.[3] as AbortSignal;
    expect(pageTwoSignal.aborted).toBe(true);
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    await act(async () => {
      pageTwo.resolve(referencePage([21], 45, 2));
    });
    expect(screen.getByRole("status")).toHaveTextContent("加载中...");
    expect(screen.queryByRole("link", { name: policyLinkName(21) })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(11) })).not.toBeInTheDocument();

    await act(async () => {
      pageOneAgain.resolve(referencePage([12], 45, 1));
    });
    expect(await screen.findByRole("link", { name: policyLinkName(12) })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(11) })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: policyLinkName(21) })).not.toBeInTheDocument();
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "test-token",
      7,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );
  });

  it("clears completed references when the token leaves and returns in the same open cycle", async () => {
    listReferencesMock.mockResolvedValueOnce(referencePage([9], 1, 1));
    const onOpenChange = vi.fn();
    const view = render(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    expect(await screen.findByRole("link", { name: policyLinkName(9) })).toBeInTheDocument();
    const calls = listReferencesMock.mock.calls.length;

    authState.current = { token: "other-token", role: "admin", authTransitioning: false };
    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(listReferencesMock).toHaveBeenCalledTimes(calls);
    expect(screen.queryByRole("link", { name: policyLinkName(9) })).not.toBeInTheDocument();

    authState.current = { token: "test-token", role: "admin", authTransitioning: false };
    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await act(async () => {
      await Promise.resolve();
    });
    expect(listReferencesMock).toHaveBeenCalledTimes(calls);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it.each(["operator", "viewer"] as const)("does not request references for %s", async (role) => {
    authState.current.role = role;
    const onOpenChange = vi.fn();
    render(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(listReferencesMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("clears an in-flight result when the token, role, or transition changes", async () => {
    const pending = deferred<ReferencePage>();
    listReferencesMock.mockImplementation(() => pending.promise);
    const onOpenChange = vi.fn();
    const view = render(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(listReferencesMock).toHaveBeenCalledTimes(1));

    authState.current = { token: "other-token", role: "admin", authTransitioning: false };
    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    view.rerender(dialogElement(false, { id: 7, name: "生产库" }, onOpenChange));
    expect(listReferencesMock).toHaveBeenCalledTimes(1);
    await act(async () => {
      pending.resolve(referencePage([1], 1, 1));
    });
    expect(screen.queryByRole("link", { name: policyLinkName(1) })).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    authState.current = { token: "other-token", role: "viewer", authTransitioning: false };
    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    view.rerender(dialogElement(false, { id: 7, name: "生产库" }, onOpenChange));
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    const callsBeforeTransition = listReferencesMock.mock.calls.length;
    authState.current = { token: "other-token", role: "admin", authTransitioning: true };
    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    view.rerender(dialogElement(false, { id: 7, name: "生产库" }, onOpenChange));
    expect(listReferencesMock).toHaveBeenCalledTimes(callsBeforeTransition);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();

    const callsBeforeReopen = listReferencesMock.mock.calls.length;
    authState.current = { token: "other-token", role: "admin", authTransitioning: false };
    view.rerender(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));
    await waitFor(() => expect(listReferencesMock).toHaveBeenCalledTimes(callsBeforeReopen + 1));
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "other-token",
      7,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );
    expect(screen.getByRole("dialog")).not.toHaveTextContent(policyLinkName(1));
  });

  it("shows not-found instead of an empty reference list and can close", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    listReferencesMock.mockRejectedValueOnce(new ApiError(404, "missing credential"));
    render(dialogElement(true, { id: 7, name: "生产库" }, onOpenChange));

    expect(await screen.findByRole("alert")).toHaveTextContent("凭据不存在");
    expect(screen.queryByText("当前没有策略引用此凭据。")).not.toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "关闭" })[0]);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("retries the current page after a failed reference request", async () => {
    const user = userEvent.setup();
    listReferencesMock.mockRejectedValueOnce(new Error("server down"));
    render(dialogElement(true));

    expect(await screen.findByRole("alert")).toHaveTextContent("server down");
    expect(screen.queryByText("当前没有策略引用此凭据。")).not.toBeInTheDocument();
    listReferencesMock.mockResolvedValueOnce(referencePage([6], 1, 1));
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByRole("link", { name: policyLinkName(6) })).toBeInTheDocument();
    expect(listReferencesMock).toHaveBeenLastCalledWith(
      "test-token",
      7,
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    );
  });

  it("surfaces an invalid reference payload instead of an empty list", async () => {
    listReferencesMock.mockRejectedValueOnce(new ApiError(500, i18n.t("credentials.referencesInvalidResponse")));
    render(dialogElement(true));
    expect(await screen.findByRole("alert")).toHaveTextContent("凭据引用响应无效。");
    expect(screen.queryByText("当前没有策略引用此凭据。")).not.toBeInTheDocument();
  });

  it("shows an empty reference list only after a successful zero total", async () => {
    listReferencesMock.mockResolvedValueOnce({ items: [], total: 0, page: 1, pageSize: 20 });
    render(dialogElement(true));
    expect(await screen.findByText("当前没有策略引用此凭据。")).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "下一页" })).not.toBeInTheDocument();
    expect(screen.queryByText("凭据不存在")).not.toBeInTheDocument();
  });

  it("explains a shrunk page and returns to the first page without looping", async () => {
    const user = userEvent.setup();
    listReferencesMock.mockImplementation(async (_token: string, _id: number, options: { page: number }) => {
      if (options.page === 1) return referencePage(range(1, 20), 45, 1);
      return { items: [], total: 45, page: options.page, pageSize: 20 };
    });
    render(dialogElement(true));
    expect(await screen.findByRole("link", { name: policyLinkName(1) })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "下一页" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("引用已变化，本页无记录。");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    const callsAfterShrink = listReferencesMock.mock.calls.length;
    await act(async () => {
      await Promise.resolve();
    });
    expect(listReferencesMock.mock.calls.length).toBe(callsAfterShrink);

    await user.click(screen.getByRole("button", { name: "回到第一页" }));
    expect(await screen.findByRole("link", { name: policyLinkName(1) })).toBeInTheDocument();
    expect(screen.queryByText("引用已变化，本页无记录。")).not.toBeInTheDocument();
  });

  it("opens the real policies page on the second-page target without applying a stored keyword", async () => {
    const user = userEvent.setup();
    const policies = range(1, 25).map((id) => ({
      id,
      name: `策略 ${id}`,
      sourcePath: `/data/${id}`,
      targetPath: `/backup/${id}`,
      cron: "0 2 * * *",
      naturalLanguage: "每天凌晨 2 点",
      criticalThreshold: 1,
      enabled: true,
    }));
    installPolicyInventory(policies);
    window.localStorage.setItem("xirang.policies.keyword", "不匹配关键字");
    listReferencesMock.mockResolvedValueOnce({
      items: [{ id: 21, name: "策略 21" }],
      total: 1,
      page: 1,
      pageSize: 20,
    });

    render(
      <MemoryRouter initialEntries={["/app/credentials"]}>
        <Routes>
          <Route
            path="/app/credentials"
            element={(
              <CredentialReferencesDialog
                open
                onOpenChange={vi.fn()}
                onCloseAutoFocus={(event) => event.preventDefault()}
                credential={{ id: 3, name: "生产库" }}
              />
            )}
          />
          <Route
            path="/app/policies"
            element={(
              <>
                <PoliciesPage />
                <LocationProbe />
              </>
            )}
          />
        </Routes>
      </MemoryRouter>,
    );

    const link = await screen.findByRole("link", { name: policyLinkName(21) });
    expect(link).toHaveAttribute("href", "/app/policies?policyId=21");
    await user.click(link);

    expect(await screen.findByTestId("location")).toHaveTextContent("/app/policies?policyId=21");
    expect(screen.queryByRole("textbox", { name: "搜索策略、路径或cron表达式" })).not.toBeInTheDocument();
    expect(screen.getAllByText("策略 21").length).toBeGreaterThan(0);
    expect(screen.queryByText("策略 1")).not.toBeInTheDocument();
    expect(screen.queryByText("策略 20")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "已定位策略 策略 21（ID 21）" })).toHaveFocus();
    expect(window.localStorage.getItem("xirang.policies.keyword")).toBe("不匹配关键字");
    expect(screen.queryByRole("dialog", { name: "引用 生产库 的策略" })).not.toBeInTheDocument();
  });
});

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}
