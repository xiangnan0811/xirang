import "@testing-library/jest-dom/vitest";
import { StrictMode, useEffect, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider, useLocation, type DataRouter, type InitialEntry } from "react-router-dom";
import { AppShell } from "@/components/layout/app-shell";
import {
  ApiError,
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  releaseAuthTransitionBarrier,
  rememberAuthIdentity,
} from "@/lib/api/core";
import { createNotificationConfigReturn } from "@/lib/notification-config-context";
import type { AlertRecord } from "@/types/domain";
import { NotificationsPage } from "./notifications-page";
import { SettingsPage } from "./settings-page";

type Role = "admin" | "operator" | "viewer";
type Identity = { token: string | null; role: Role | null; username: string; totpEnabled: boolean; authTransitioning: boolean };

const { api, authStore, toastMock, contexts } = vi.hoisted(() => {
  let snapshot: Identity = { token: "token-a", role: "admin", username: "admin", totpEnabled: true, authTransitioning: false };
  const listeners = new Set<() => void>();
  const fetchAlertDeliveryStats = vi.fn();
  return {
    api: {
      getAlertsPaginated: vi.fn(),
      getAlert: vi.fn(),
      getAlertDeliveries: vi.fn(),
      getAlertGroupInfo: vi.fn(),
      getAlertUnreadCount: vi.fn(),
      getTaskFailureSummary: vi.fn(),
      getNode: vi.fn(),
      createSilence: vi.fn(),
      ackAlert: vi.fn(),
      resolveAlert: vi.fn(),
      resolveAlertsBulk: vi.fn(),
      retryDelivery: vi.fn(),
      triggerTask: vi.fn(),
      requestTaskManualTriggerCredentialGrant: vi.fn(),
      getAlerts: vi.fn(),
      listEscalationPolicies: vi.fn(),
      listAlertEscalationEvents: vi.fn(),
      listAnomalyEvents: vi.fn(),
    },
    // Mirrors AuthProvider: Context consumers re-render when the identity changes.
    authStore: {
      get: () => snapshot,
      set(next: Partial<Identity>) {
        snapshot = { ...snapshot, ...next };
        for (const listener of listeners) listener();
      },
      subscribe(listener: () => void) {
        listeners.add(listener);
        return () => {
          listeners.delete(listener);
        };
      },
    },
    toastMock: { success: vi.fn(), error: vi.fn() },
    contexts: {
      shared: { globalSearch: "", setGlobalSearch: vi.fn(), refreshVersion: 0 },
      alerts: { fetchAlertDeliveryStats },
      integrations: { integrations: [] as never[], refreshIntegrations: vi.fn() },
      console: { globalSearch: "", setGlobalSearch: vi.fn(), refresh: vi.fn(), loading: false, warning: null, nodes: [], overview: {} },
    },
  };
});

// vi.mock factories are hoisted above static imports, so the modules they need load inside the factory.
vi.mock("@/context/auth-context.hooks", async () => {
  const { useSyncExternalStore } = await import("react");
  return { useAuth: () => useSyncExternalStore(authStore.subscribe, authStore.get) };
});
vi.mock("@/context/shared-context.hooks", () => ({ useSharedContext: () => contexts.shared }));
vi.mock("@/context/alerts-context.hooks", () => ({ useAlertsContext: () => contexts.alerts }));
vi.mock("@/context/integrations-context.hooks", () => ({ useIntegrationsContext: () => contexts.integrations }));
vi.mock("@/components/ui/toast-sonner", () => ({ toast: toastMock }));
vi.mock("@/hooks/use-step-up-action", () => ({
  useStepUpAction: () => async <T,>(action: (proof?: string) => Promise<T>) => action("step-up-marker"),
}));
// Hoisted factory: AlertCenter classifies failures with the real core ApiError class.
vi.mock("@/lib/api/client", async () => {
  const core = await import("@/lib/api/core");
  return { apiClient: api, ApiError: core.ApiError };
});

// Settings panels are stubbed: these tests prove routing and history, not panel behavior. The
// config panels expose a local save that only re-renders, standing in for a completed save.
vi.mock("./settings-page.personal", () => ({ PersonalTab: () => <div>personal-panel</div> }));
vi.mock("./settings-page.account", () => ({ AccountTab: () => <div>account-panel</div> }));
vi.mock("./settings-page.users", () => ({ UsersTab: () => <div>users-panel</div> }));
vi.mock("./settings-page.channels", () => ({ ChannelsTab: () => <StubConfigPanel name="channels" /> }));
vi.mock("./settings-page.silences", () => ({ SilencesPanel: () => <StubConfigPanel name="silences" /> }));
vi.mock("./settings-page.escalation", () => ({ SettingsPageEscalation: () => <StubConfigPanel name="escalation" /> }));
vi.mock("./settings-page.system", () => ({ SystemTab: () => <div>system-panel</div> }));
vi.mock("./settings-page.maintenance", () => ({ MaintenanceTab: () => <div>maintenance-panel</div> }));

// AppShell chrome is stubbed so the real AnimatedOutlet (AnimatePresence mode="wait") is the only
// shell behavior under test; it keeps the exiting page mounted with the live router location.
vi.mock("@/hooks/use-console-data", () => ({ useConsoleData: () => contexts.console }));
vi.mock("@/components/layout/desktop-sidebar", () => ({ DesktopSidebar: () => null }));
vi.mock("@/components/layout/mobile-navigation", () => ({ MobileNavigation: () => null }));
vi.mock("@/components/scroll-to-top", () => ({ ScrollToTop: () => null }));
vi.mock("@/components/theme-toggle", () => ({ ThemeToggle: () => null }));
vi.mock("@/components/display-preferences-toggle", () => ({ DisplayPreferencesToggle: () => null }));
vi.mock("@/components/language-switcher", () => ({ LanguageSwitcher: () => null }));
vi.mock("@/components/notification-bell", () => ({ NotificationBell: () => null }));
vi.mock("@/components/user-dropdown", () => ({ UserDropdown: () => null }));
vi.mock("@/components/version-banner", () => ({ VersionBanner: () => null }));
vi.mock("@/components/setup-wizard", () => ({ SetupWizard: () => null }));
vi.mock("@/components/access-denied-navigation", () => ({ AccessDeniedNavigationNotice: () => null }));
vi.mock("@/components/ui/lazy-command-palette", () => ({ LazyCommandPalette: () => null }));

function StubConfigPanel({ name }: { name: string }) {
  const [saves, setSaves] = useState(0);
  return (
    <div>
      <p>{`${name}-panel`}</p>
      <button type="button" onClick={() => setSaves((count) => count + 1)}>{`保存 ${name}`}</button>
      <output>{`${name}-saved-${saves}`}</output>
    </div>
  );
}

// The ES2022 lib configured for web/ has no Promise.withResolvers.
type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void; reject: (error: unknown) => void };
function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function alertRecord(id: string, errorCode: string, message: string, status: AlertRecord["status"] = "open"): AlertRecord {
  return {
    id,
    nodeName: "node-9",
    nodeId: 9,
    taskId: null,
    taskRunId: null,
    sloId: null,
    policyName: "每日备份",
    severity: "warning",
    status,
    errorCode,
    message,
    triggeredAt: "2026-10-10 01:00:00",
    retryable: false,
  };
}

const listAlert = alertRecord("alert-7", "E_LIST", "普通列表告警");
const targetAlert = alertRecord("alert-42", "E_TARGET", "目标告警正文", "resolved");
const TARGET_HEADING = "告警 #42";

function listPage(items: AlertRecord[], total = items.length) {
  return { items, total, page: 1, pageSize: 20 };
}

function renderRoutes(initialEntries: InitialEntry[], initialIndex?: number, options: { strict?: boolean } = {}) {
  const router = createMemoryRouter(
    [
      { path: "/app/notifications", element: <NotificationsPage /> },
      { path: "/app/settings", element: <SettingsPage /> },
    ],
    { initialEntries, initialIndex },
  );
  const app = <RouterProvider router={router} />;
  const view = render(options.strict ? <StrictMode>{app}</StrictMode> : app);
  return { router, ...view };
}

function location(router: DataRouter) {
  return router.state.location;
}

function targetSection() {
  const heading = screen.getByRole("heading", { name: TARGET_HEADING });
  const section = heading.closest("section");
  if (!(section instanceof HTMLElement)) throw new Error("missing target status section");
  return section;
}

// The first rendered copy is the card view, which is the one present at every width.
function targetRow(message = "目标告警正文") {
  const text = screen.getAllByText(message)[0];
  const row = text.closest("tr") ?? text.closest("div.rounded-lg");
  if (!(row instanceof HTMLElement)) throw new Error("missing target row");
  return row;
}

// AlertCenter receives the shared refreshVersion through NotificationsPage, which re-renders on auth snapshots.
function sharedRefresh() {
  act(() => {
    contexts.shared.refreshVersion += 1;
    authStore.set({});
  });
}

function signIn(token: string | null, role: Role | null) {
  act(() => {
    bumpAuthSessionGeneration();
    rememberAuthIdentity(token, role);
    authStore.set({ token, role });
  });
}

async function wait(ms: number) {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms));
  });
}

const scrollIntoView = vi.fn();

describe("notification config context routing", () => {
  beforeEach(() => {
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
    window.localStorage.clear();
    window.localStorage.setItem("xirang.language", "zh");
    Object.defineProperty(Element.prototype, "scrollIntoView", { configurable: true, writable: true, value: scrollIntoView });
    scrollIntoView.mockReset();
    rememberAuthIdentity("token-a", "admin");
    authStore.set({ token: "token-a", role: "admin" });
    for (const fn of Object.values(api)) fn.mockReset();
    toastMock.success.mockReset();
    toastMock.error.mockReset();
    api.getAlertsPaginated.mockResolvedValue(listPage([listAlert]));
    api.getAlert.mockResolvedValue(targetAlert);
    api.getAlertDeliveries.mockResolvedValue([]);
    api.getAlertGroupInfo.mockResolvedValue({ count: 1, siblingNodeIds: [] });
    api.getAlertUnreadCount.mockResolvedValue({ total: 0, critical: 0, warning: 0 });
    api.getTaskFailureSummary.mockResolvedValue({ failedTasks: 0, windowHours: 24 });
    api.listEscalationPolicies.mockResolvedValue([]);
    api.listAlertEscalationEvents.mockResolvedValue([]);
    api.listAnomalyEvents.mockResolvedValue({ data: [], total: 0 });
    contexts.alerts.fetchAlertDeliveryStats.mockReset();
    contexts.alerts.fetchAlertDeliveryStats.mockResolvedValue({ windowHours: 24, totalSent: 0, totalFailed: 0, successRate: 100, byIntegration: [] });
    contexts.shared.refreshVersion = 0;
    contexts.integrations.refreshIntegrations.mockReset();
    contexts.integrations.refreshIntegrations.mockResolvedValue(undefined);
  });

  afterEach(() => {
    if (isAuthTransitionActive()) clearAuthTransitionBarrier();
  });

  describe("exact alert target", () => {
    it.each(["42", "alert-42"])("keeps target %s outside the current page across time, pagination and filters", async (spelling) => {
      const user = userEvent.setup();
      api.getAlertsPaginated.mockResolvedValue(listPage([listAlert], 40));
      const { router } = renderRoutes([`/app/notifications?alert=${spelling}`]);

      expect(await screen.findByRole("heading", { name: TARGET_HEADING })).toBeInTheDocument();
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      expect(api.getAlert).toHaveBeenCalledTimes(1);
      expect(api.getAlert).toHaveBeenCalledWith("token-a", "42", { signal: expect.any(AbortSignal) });
      // Downstream reads use the verified record id, and the target delivery panel opens once.
      await waitFor(() => expect(api.getAlertDeliveries).toHaveBeenCalledWith("token-a", "alert-42"));
      expect(api.getAlertGroupInfo).toHaveBeenCalledWith("token-a", "alert-42");
      expect((await screen.findAllByRole("region", { name: "告警 E_TARGET 的投递记录" })).length).toBeGreaterThan(0);
      await waitFor(() => expect(scrollIntoView).toHaveBeenCalled());
      expect(screen.getByRole("heading", { name: TARGET_HEADING })).toHaveFocus();

      await wait(700);
      expect(screen.getAllByText("目标告警正文").length).toBeGreaterThan(0);
      expect(location(router).search).toBe(`?alert=${spelling}`);

      await user.click(screen.getAllByRole("button", { name: "下一页" })[0]);
      await waitFor(() => expect(api.getAlertsPaginated).toHaveBeenLastCalledWith("token-a", expect.objectContaining({ page: 2 })));
      expect(screen.getAllByText("目标告警正文").length).toBeGreaterThan(0);

      const statusSelect = screen.getAllByRole("combobox")[1];
      await user.selectOptions(statusSelect, "resolved");
      await waitFor(() => expect(api.getAlertsPaginated).toHaveBeenLastCalledWith("token-a", expect.objectContaining({ status: "resolved", page: 1 })));
      expect(screen.getAllByText("目标告警正文").length).toBeGreaterThan(0);
      expect(screen.getByRole("heading", { name: TARGET_HEADING })).toBeInTheDocument();
      // Ordinary paging and filtering never re-read or drop the URL target.
      expect(api.getAlert).toHaveBeenCalledTimes(1);
      expect(location(router).search).toBe(`?alert=${spelling}`);
    });

    it("hides the stale list copy while the target is unverified and shows the verified record", async () => {
      const pending = createDeferred<AlertRecord>();
      api.getAlert.mockReturnValueOnce(pending.promise);
      api.getAlertsPaginated.mockResolvedValue(listPage([alertRecord("alert-42", "E_TARGET", "列表旧副本"), listAlert]));
      renderRoutes(["/app/notifications?alert=42"]);

      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      expect(screen.getByText("正在读取原告警")).toBeInTheDocument();
      expect(screen.queryByText("列表旧副本")).not.toBeInTheDocument();
      expect(screen.queryAllByRole("region", { name: "告警 E_TARGET 的投递记录" })).toHaveLength(0);

      await act(async () => pending.resolve(alertRecord("alert-42", "E_TARGET", "已核实目标")));
      expect((await screen.findAllByText("已核实目标")).length).toBeGreaterThan(0);
      expect(screen.queryByText("列表旧副本")).not.toBeInTheDocument();
      expect(screen.queryByText("正在读取原告警")).not.toBeInTheDocument();
    });

    it("treats a response for another alert as an error without downstream reads", async () => {
      api.getAlert.mockResolvedValueOnce(alertRecord("alert-43", "E_OTHER", "另一条告警"));
      const { router } = renderRoutes(["/app/notifications?alert=42"]);

      expect(await screen.findByText("暂时无法读取原告警")).toBeInTheDocument();
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      expect(screen.queryByText("另一条告警")).not.toBeInTheDocument();
      expect(api.getAlertDeliveries).not.toHaveBeenCalled();
      expect(api.getAlertGroupInfo).not.toHaveBeenCalled();
      expect(location(router).search).toBe("?alert=42");
    });

    it.each([
      ["404 after an acknowledgement reload", () => new ApiError(404, "not found"), "原告警不存在或已删除", "ack"],
      ["403 after a shared refresh", () => Object.assign(new ApiError(403, "forbidden"), { httpStatus: 403 }), "当前无权查看原告警", "refresh"],
    ])("drops a ready, selected target that turns %s", async (_label, makeError, copy, trigger) => {
      const user = userEvent.setup();
      api.getAlertsPaginated.mockResolvedValue(listPage([alertRecord("alert-42", "E_TARGET", "列表旧副本"), listAlert]));
      api.getAlert.mockResolvedValueOnce(alertRecord("alert-42", "E_TARGET", "目标告警正文")).mockRejectedValueOnce(makeError());
      api.ackAlert.mockResolvedValue(undefined);
      const { router } = renderRoutes(["/app/notifications?alert=42"]);

      expect((await screen.findAllByRole("region", { name: "告警 E_TARGET 的投递记录" })).length).toBeGreaterThan(0);
      await waitFor(() => expect(api.getAlertDeliveries).toHaveBeenCalledWith("token-a", "alert-42"));
      await user.click(screen.getAllByRole("checkbox", { name: "选择节点 node-9 的告警 E_TARGET" })[0]);
      expect(screen.getByText("已选择 1 条未恢复告警")).toBeInTheDocument();
      const deliveryReads = api.getAlertDeliveries.mock.calls.length;
      const groupReads = api.getAlertGroupInfo.mock.calls.length;

      if (trigger === "ack") {
        await user.click(within(targetRow()).getByRole("button", { name: "更多操作" }));
        await user.click(await screen.findByRole("menuitem", { name: "确认" }));
        expect(api.ackAlert).toHaveBeenCalledWith("token-a", "alert-42");
      } else {
        sharedRefresh();
      }

      expect(await screen.findByText(copy)).toBeInTheDocument();
      expect(api.getAlert).toHaveBeenCalledTimes(2);
      expect(api.getAlert).toHaveBeenLastCalledWith("token-a", "42", { signal: expect.any(AbortSignal) });
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      await wait(20);
      // Neither the verified record nor the list copy survives, and no hidden selection stays actionable.
      expect(screen.queryByText("目标告警正文")).not.toBeInTheDocument();
      expect(screen.queryByText("列表旧副本")).not.toBeInTheDocument();
      expect(screen.queryAllByRole("checkbox", { name: "选择节点 node-9 的告警 E_TARGET" })).toHaveLength(0);
      expect(screen.queryByText(/已选择 \d+ 条未恢复告警/)).not.toBeInTheDocument();
      expect(screen.queryAllByRole("region", { name: "告警 E_TARGET 的投递记录" })).toHaveLength(0);
      expect(api.getAlertDeliveries).toHaveBeenCalledTimes(deliveryReads);
      expect(api.getAlertGroupInfo).toHaveBeenCalledTimes(groupReads);
      expect(location(router).search).toBe("?alert=42");
    });

    it("keeps a manually collapsed target closed across refresh, acknowledgement and resolution reloads", async () => {
      const user = userEvent.setup();
      const region = { name: "告警 E_TARGET 的投递记录" };
      api.getAlert
        .mockResolvedValueOnce(alertRecord("alert-42", "E_TARGET", "目标告警正文"))
        .mockResolvedValueOnce(alertRecord("alert-42", "E_TARGET", "目标告警正文"))
        .mockResolvedValueOnce(alertRecord("alert-42", "E_TARGET", "目标告警正文", "acked"))
        .mockResolvedValueOnce(alertRecord("alert-42", "E_TARGET", "目标告警正文", "resolved"));
      api.ackAlert.mockResolvedValue(undefined);
      api.resolveAlert.mockResolvedValue(undefined);
      renderRoutes(["/app/notifications?alert=42"]);

      expect((await screen.findAllByRole("region", region)).length).toBeGreaterThan(0);
      await user.click(within(targetRow()).getByRole("button", { name: "更多操作" }));
      await user.click(await screen.findByRole("menuitem", { name: "收起投递" }));
      await waitFor(() => expect(screen.queryAllByRole("region", region)).toHaveLength(0));

      // Each verified re-read also reloads deliveries, so equal counts prove the ready path ran.
      async function expectCollapsedAfterRead(reads: number) {
        await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(reads));
        await waitFor(() => expect(api.getAlertDeliveries).toHaveBeenCalledTimes(reads));
        await wait(20);
        expect(screen.getAllByText("目标告警正文").length).toBeGreaterThan(0);
        expect(screen.queryAllByRole("region", region)).toHaveLength(0);
      }

      sharedRefresh();
      await expectCollapsedAfterRead(2);

      await user.click(within(targetRow()).getByRole("button", { name: "更多操作" }));
      await user.click(await screen.findByRole("menuitem", { name: "确认" }));
      expect(api.ackAlert).toHaveBeenCalledWith("token-a", "alert-42");
      await expectCollapsedAfterRead(3);

      await user.click(within(targetRow()).getByRole("button", { name: "更多操作" }));
      await user.click(await screen.findByRole("menuitem", { name: "恢复" }));
      expect(api.resolveAlert).toHaveBeenCalledWith("token-a", "alert-42");
      await expectCollapsedAfterRead(4);
    });

    it.each([
      ["duplicate identical", "?alert=42&alert=42"],
      ["duplicate different", "?alert=42&alert=43"],
      ["unsafe integer", "?alert=9007199254740993"],
      ["leading zero", "?alert=042"],
      ["zero", "?alert=0"],
      ["negative", "?alert=-42"],
      ["exponent", "?alert=1e3"],
      ["whitespace", "?alert=%2042"],
      ["empty", "?alert="],
      ["bare prefix", "?alert=alert-"],
    ])("rejects %s targets with zero single reads", async (_label, search) => {
      const user = userEvent.setup();
      const { router } = renderRoutes([{ pathname: "/app/notifications", search: `${search}&view=compact` }]);

      const heading = await screen.findByRole("heading", { name: "告警标识无效" });
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      await waitFor(() => expect(heading).toHaveFocus());
      const section = heading.closest("section");
      if (!(section instanceof HTMLElement)) throw new Error("missing target section");
      expect(within(section).queryByRole("button", { name: "重试" })).not.toBeInTheDocument();
      expect(api.getAlert).not.toHaveBeenCalled();
      expect(location(router).search).toBe(`${search}&view=compact`);

      await user.click(within(section).getByRole("button", { name: "返回通知列表" }));
      await waitFor(() => expect(location(router).search).toBe("?view=compact"));
      expect(screen.queryByRole("heading", { name: "告警标识无效" })).not.toBeInTheDocument();
      expect(api.getAlert).not.toHaveBeenCalled();
    });

    it.each([
      ["404", () => new ApiError(404, "not found"), "原告警不存在或已删除"],
      ["HTTP 404 under an envelope code", () => Object.assign(new ApiError(40004, "envelope"), { httpStatus: 404 }), "原告警不存在或已删除"],
      ["403", () => Object.assign(new ApiError(403, "forbidden"), { httpStatus: 403 }), "当前无权查看原告警"],
      ["500", () => Object.assign(new ApiError(500, "boom"), { httpStatus: 500 }), "暂时无法读取原告警"],
      ["network failure", () => new TypeError("Failed to fetch"), "暂时无法读取原告警"],
    ])("shows the %s target state and retries only on request", async (_label, makeError, copy) => {
      const user = userEvent.setup();
      api.getAlert.mockRejectedValueOnce(makeError());
      const { router } = renderRoutes(["/app/notifications?alert=42"]);

      expect(await screen.findByText(copy)).toBeInTheDocument();
      for (const other of ["原告警不存在或已删除", "当前无权查看原告警", "暂时无法读取原告警"].filter((text) => text !== copy)) {
        expect(screen.queryByText(other)).not.toBeInTheDocument();
      }
      expect(screen.getByRole("heading", { name: TARGET_HEADING })).toHaveFocus();
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      await wait(50);
      expect(api.getAlert).toHaveBeenCalledTimes(1);
      expect(api.getAlertDeliveries).not.toHaveBeenCalled();
      expect(location(router).search).toBe("?alert=42");

      await user.click(within(targetSection()).getByRole("button", { name: "重试" }));
      await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(2));
      expect(api.getAlert).toHaveBeenLastCalledWith("token-a", "42", { signal: expect.any(AbortSignal) });
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      expect(screen.queryByText(copy)).not.toBeInTheDocument();
      expect(location(router).search).toBe("?alert=42");
    });

    it("clears only the alert target and its context state, as a new history entry", async () => {
      const user = userEvent.setup();
      const payload = createNotificationConfigReturn("42");
      expect(payload).not.toBeNull();
      const { router } = renderRoutes([
        "/app/notifications",
        {
          pathname: "/app/notifications",
          search: "?view=compact&alert=42",
          hash: "#alerts",
          state: { keep: "yes", notificationConfigReturn: payload },
        },
      ], 1);

      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      await user.click(within(targetSection()).getByRole("button", { name: "返回通知列表" }));

      await waitFor(() => expect(location(router).search).toBe("?view=compact"));
      expect(location(router).hash).toBe("#alerts");
      expect(location(router).state).toEqual({ keep: "yes" });
      expect(router.state.historyAction).toBe("PUSH");
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      // The keyword filter is always rendered at every width, unlike the per-view row controls.
      await waitFor(() => expect(screen.getByLabelText("告警关键词筛选")).toHaveFocus());

      await act(async () => {
        await router.navigate(-1);
      });
      expect(location(router).search).toBe("?view=compact&alert=42");
      expect(await screen.findByRole("heading", { name: TARGET_HEADING })).toBeInTheDocument();
      await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(2));
    });
  });

  describe("navigation context and identity", () => {
    it("restores the exact target when returning through real settings history", async () => {
      const { router } = renderRoutes(["/app/notifications?alert=alert-42&view=compact"]);
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);

      const sourceState = { notificationConfigReturn: createNotificationConfigReturn("42") };
      expect(sourceState.notificationConfigReturn).not.toBeNull();
      await act(async () => {
        await router.navigate("/app/settings?tab=channels", { state: sourceState });
      });
      expect(screen.getByRole("tab", { selected: true })).toHaveAttribute("id", "settings-tab-channels");
      expect(screen.getByText("channels-panel")).toBeInTheDocument();

      await act(async () => {
        await router.navigate(-1);
      });
      expect(location(router).search).toBe("?alert=alert-42&view=compact");
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(2));
      expect(api.getAlert).toHaveBeenLastCalledWith("token-a", "42", { signal: expect.any(AbortSignal) });

      await act(async () => {
        await router.navigate(1);
      });
      expect(location(router).pathname).toBe("/app/settings");
      expect(location(router).state).toEqual(sourceState);

      await act(async () => {
        await router.navigate("/app/notifications?alert=42", { state: location(router).state });
      });
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      expect(location(router).search).toBe("?alert=42");
      expect(location(router).state).toEqual(sourceState);
      await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(3));
    });

    it("keeps a valid list context without inventing a target", async () => {
      const state = { keep: "yes", notificationConfigReturn: createNotificationConfigReturn(null) };
      const { router } = renderRoutes([{ pathname: "/app/notifications", search: "?view=compact", state }]);
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      await wait(20);
      expect(location(router).state).toEqual(state);
      expect(location(router).search).toBe("?view=compact");
      expect(api.getAlert).not.toHaveBeenCalled();
    });

    const invalidContexts: Array<[string, () => unknown]> = [
      ["a context from an earlier session epoch", () => {
        const stale = createNotificationConfigReturn("42");
        bumpAuthSessionGeneration();
        return stale;
      }],
      ["a context for another alert", () => createNotificationConfigReturn("43")],
      ["a list context with an alert target", () => createNotificationConfigReturn(null)],
      ["a context with an injected return URL", () => ({ ...createNotificationConfigReturn("42"), returnTo: "https://evil.example/" })],
      ["an unknown source", () => ({ ...createNotificationConfigReturn("42"), source: "dashboard" })],
      ["a non-object context", () => "42"],
    ];

    it.each(invalidContexts)("drops %s before any target read", async (_label, makeContext) => {
      const notificationConfigReturn = makeContext();
      const { router } = renderRoutes([{
        pathname: "/app/notifications",
        search: "?alert=42&view=compact",
        state: { keep: "yes", notificationConfigReturn },
      }]);

      await waitFor(() => expect(location(router).search).toBe("?view=compact"));
      expect(location(router).state).toEqual({ keep: "yes" });
      expect(router.state.historyAction).toBe("REPLACE");
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      expect(api.getAlert).not.toHaveBeenCalled();
    });

    it("does not accept a history context back after A→B→A", async () => {
      const { router } = renderRoutes(["/app/notifications"]);
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      const state = { notificationConfigReturn: createNotificationConfigReturn("42") };

      signIn("token-b", "admin");
      signIn("token-a", "admin");
      await act(async () => {
        await router.navigate("/app/notifications?alert=42", { state });
      });

      await waitFor(() => expect(location(router).search).toBe(""));
      expect(location(router).state).toBeNull();
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      expect(api.getAlert).not.toHaveBeenCalled();
    });

    const identityChanges: Array<[string, () => void]> = [
      ["switches to another admin", () => signIn("token-b", "admin")],
      ["returns A→B→A", () => {
        signIn("token-b", "admin");
        signIn("token-a", "admin");
      }],
      ["logs in again with the same token", () => signIn("token-a", "admin")],
      ["loses admin on the same token", () => {
        act(() => {
          rememberAuthIdentity("token-a", "viewer");
          authStore.set({ role: "viewer" });
        });
      }],
    ];

    it.each(identityChanges)("drops a pending target when the session %s", async (_label, change) => {
      const pending = createDeferred<AlertRecord>();
      api.getAlert.mockReturnValueOnce(pending.promise);
      const { router } = renderRoutes(["/app/notifications?alert=42&view=compact"]);
      expect(await screen.findByText("正在读取原告警")).toBeInTheDocument();
      await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(1));
      const options: unknown = api.getAlert.mock.calls[0]?.[2];
      const signal = options && typeof options === "object" && "signal" in options ? options.signal : null;
      if (!(signal instanceof AbortSignal)) throw new Error("target read was not abortable");

      change();
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      await waitFor(() => expect(location(router).search).toBe("?view=compact"));
      expect(router.state.historyAction).toBe("REPLACE");
      expect(signal.aborted).toBe(true);

      await act(async () => pending.resolve(targetAlert));
      await wait(20);
      expect(screen.queryByText("目标告警正文")).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      expect(api.getAlert).toHaveBeenCalledTimes(1);
      expect(api.getAlertDeliveries).not.toHaveBeenCalled();
      expect(toastMock.error).not.toHaveBeenCalled();
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
    });

    it("drops a late target failure after the identity changes", async () => {
      const pending = createDeferred<AlertRecord>();
      api.getAlert.mockReturnValueOnce(pending.promise);
      renderRoutes(["/app/notifications?alert=42"]);
      expect(await screen.findByText("正在读取原告警")).toBeInTheDocument();

      signIn("token-b", "admin");
      await act(async () => pending.reject(new ApiError(404, "gone")));
      await wait(20);
      expect(screen.queryByText("原告警不存在或已删除")).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      expect(api.getAlert).toHaveBeenCalledTimes(1);
    });

    it("unmounts the target during an auth transition and does not restore it after release", async () => {
      const pending = createDeferred<AlertRecord>();
      api.getAlert.mockReturnValueOnce(pending.promise);
      const { router } = renderRoutes(["/app/notifications?alert=42"]);
      expect(await screen.findByText("正在读取原告警")).toBeInTheDocument();

      let barrier = 0;
      act(() => {
        barrier = beginAuthTransitionBarrier();
      });
      expect(screen.queryByRole("heading", { name: "告警中心" })).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      await act(async () => pending.resolve(targetAlert));

      act(() => {
        releaseAuthTransitionBarrier(barrier);
      });
      expect(await screen.findByRole("heading", { name: "告警中心" })).toBeInTheDocument();
      await waitFor(() => expect(location(router).search).toBe(""));
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      expect(screen.queryByText("目标告警正文")).not.toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      expect(api.getAlert).toHaveBeenCalledTimes(1);
    });

    it("does not mount the alert center while Context and the core identity disagree", async () => {
      rememberAuthIdentity("token-b", "admin");
      renderRoutes(["/app/notifications?alert=42"]);
      expect(await screen.findByRole("heading", { name: "通知与告警" })).toBeInTheDocument();
      await wait(20);
      expect(screen.queryByRole("heading", { name: "告警中心" })).not.toBeInTheDocument();
      expect(api.getAlert).not.toHaveBeenCalled();
      expect(api.getAlertsPaginated).not.toHaveBeenCalled();
    });
  });

  describe("config links and manual return", () => {
    const tabs = [
      ["channels", "通知渠道"],
      ["silences", "静音规则"],
      ["escalation", "升级策略"],
    ] as const;

    function settingsTab(id: string) {
      const element = document.getElementById(`settings-tab-${id}`);
      if (!(element instanceof HTMLElement)) throw new Error(`missing settings tab ${id}`);
      return element;
    }

    function returnPayload(router: DataRouter) {
      const state: unknown = location(router).state;
      return state && typeof state === "object" && "notificationConfigReturn" in state ? state.notificationConfigReturn : undefined;
    }

    async function targetDeliveryRegion() {
      return (await screen.findAllByRole("region", { name: "告警 E_TARGET 的投递记录" }))[0];
    }

    it.each(tabs)("opens %s from the top nav as a list context, even on an alert URL, and returns manually", async (tab, label) => {
      const user = userEvent.setup();
      const { router } = renderRoutes(["/app/notifications?alert=42"]);
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      const region = await targetDeliveryRegion();
      const topNav = screen.getAllByRole("navigation", { name: "告警配置" }).find((nav) => !region.contains(nav) && !nav.closest("[role=region]"));
      if (!topNav) throw new Error("missing top config nav");

      await user.click(within(topNav).getByRole("link", { name: label }));
      expect(location(router).pathname).toBe("/app/settings");
      expect(location(router).search).toBe(`?tab=${tab}`);
      expect(returnPayload(router)).toMatchObject({ version: 1, source: "notifications", alertId: null });
      expect(settingsTab(tab)).toHaveAttribute("aria-selected", "true");
      const source = screen.getByRole("region", { name: "来自通知列表" });
      expect(within(source).getByText("保存后仍停留在此页；请手动返回")).toBeInTheDocument();
      await waitFor(() => expect(settingsTab(tab)).toHaveFocus());

      // A completed save re-renders in place: no navigation and no stolen focus.
      const save = screen.getByRole("button", { name: `保存 ${tab}` });
      await user.click(save);
      expect(screen.getByText(`${tab}-saved-1`)).toBeInTheDocument();
      expect(location(router).pathname).toBe("/app/settings");
      expect(location(router).search).toBe(`?tab=${tab}`);
      expect(save).toHaveFocus();
      expect(screen.getByRole("region", { name: "来自通知列表" })).toBeInTheDocument();

      const back = within(source).getByRole("link", { name: "返回通知列表" });
      expect(back).toHaveAttribute("href", "/app/notifications");
      const reads = api.getAlert.mock.calls.length;
      await user.click(back);
      expect(location(router).pathname).toBe("/app/notifications");
      expect(location(router).search).toBe("");
      expect(router.state.historyAction).toBe("PUSH");
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      expect(screen.queryByRole("heading", { name: TARGET_HEADING })).not.toBeInTheDocument();
      expect(api.getAlert).toHaveBeenCalledTimes(reads);
    });

    it.each(tabs)("opens %s from the target delivery panel and returns to the same canonical alert", async (tab, label) => {
      const user = userEvent.setup();
      const { router } = renderRoutes(["/app/notifications?alert=alert-42&view=compact"]);
      const region = await targetDeliveryRegion();
      expect(within(region).getByText("配置后可返回此告警")).toBeInTheDocument();
      expect(within(region).queryByText("目标告警正文")).not.toBeInTheDocument();
      const nav = within(region).getByRole("navigation", { name: "告警配置" });

      await user.click(within(nav).getByRole("link", { name: label }));
      expect(location(router).search).toBe(`?tab=${tab}`);
      expect(returnPayload(router)).toMatchObject({ version: 1, source: "notifications", alertId: "42" });
      const source = screen.getByRole("region", { name: "来自告警 #42" });
      await user.click(screen.getByRole("button", { name: `保存 ${tab}` }));
      expect(location(router).pathname).toBe("/app/settings");

      const back = within(source).getByRole("link", { name: "返回原告警" });
      expect(back).toHaveAttribute("href", "/app/notifications?alert=42");
      await user.click(back);
      expect(location(router).pathname).toBe("/app/notifications");
      expect(location(router).search).toBe("?alert=42");
      expect(returnPayload(router)).toMatchObject({ alertId: "42" });
      expect(await screen.findByRole("heading", { name: TARGET_HEADING })).toBeInTheDocument();
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      await waitFor(() => expect(api.getAlert).toHaveBeenCalledTimes(2));
      expect(api.getAlert).toHaveBeenLastCalledWith("token-a", "42", { signal: expect.any(AbortSignal) });
    });

    it("carries the alert from the unsupported quick-silence dialog", async () => {
      const user = userEvent.setup();
      api.getAlertsPaginated.mockResolvedValue(listPage([{ ...alertRecord("alert-55", "XR-EXEC-17", "平台定时"), nodeId: 0, nodeName: "platform" }]));
      const { router } = renderRoutes(["/app/notifications"]);
      const text = (await screen.findAllByText("平台定时"))[0];
      const row = text.closest("tr") ?? text.closest("div.rounded-lg");
      if (!(row instanceof HTMLElement)) throw new Error("missing platform row");
      await user.click(within(row).getByRole("button", { name: "更多操作" }));
      await user.click(await screen.findByRole("menuitem", { name: "静音此类告警" }));
      const dialog = await screen.findByRole("dialog");
      const link = within(dialog).getByRole("link", { name: "查看静音规则" });
      expect(link).toHaveAttribute("href", "/app/settings?tab=silences");

      await user.click(link);
      expect(location(router).search).toBe("?tab=silences");
      expect(returnPayload(router)).toMatchObject({ alertId: "55" });
      expect(within(screen.getByRole("region", { name: "来自告警 #55" })).getByRole("link", { name: "返回原告警" }))
        .toHaveAttribute("href", "/app/notifications?alert=55");
      expect(api.getNode).not.toHaveBeenCalled();
      expect(api.createSilence).not.toHaveBeenCalled();
    });

    it("switches settings tabs by replacement while keeping query, hash, unrelated state and the same payload", async () => {
      const user = userEvent.setup();
      const payload = createNotificationConfigReturn("42");
      const state = { notificationConfigReturn: payload, securityReturnTo: "/app/settings?tab=account", keep: "yes" };
      const { router } = renderRoutes([
        "/app/notifications?alert=42",
        { pathname: "/app/settings", search: "?tab=channels&x=1", hash: "#panel", state },
      ], 1);
      expect(screen.getByRole("region", { name: "来自告警 #42" })).toBeInTheDocument();

      await user.click(settingsTab("silences"));
      expect(location(router).search).toBe("?tab=silences&x=1");
      expect(location(router).hash).toBe("#panel");
      expect(location(router).state).toEqual(state);
      expect(returnPayload(router)).toEqual(payload);
      expect(router.state.historyAction).toBe("REPLACE");
      expect(screen.getByRole("region", { name: "来自告警 #42" })).toBeInTheDocument();

      settingsTab("silences").focus();
      await user.keyboard("{ArrowRight}");
      expect(location(router).search).toBe("?tab=escalation&x=1");
      expect(settingsTab("escalation")).toHaveFocus();
      expect(location(router).state).toEqual(state);

      // Non-config tabs keep the context in history but do not show it.
      await user.click(settingsTab("personal"));
      expect(screen.queryByRole("region", { name: "来自告警 #42" })).not.toBeInTheDocument();
      expect(location(router).state).toEqual(state);
      await user.click(settingsTab("channels"));
      expect(screen.getByRole("region", { name: "来自告警 #42" })).toBeInTheDocument();

      // Tab changes added no history entries: one step back is the notifications page.
      await act(async () => {
        await router.navigate(-1);
      });
      expect(location(router).pathname).toBe("/app/notifications");
      expect(location(router).search).toBe("?alert=42");
    });

    it("keeps the context through browser back and forward, and drops it on identity change", async () => {
      const user = userEvent.setup();
      const { router } = renderRoutes(["/app/notifications"]);
      expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
      await user.click(within(screen.getByRole("navigation", { name: "告警配置" })).getByRole("link", { name: "升级策略" }));
      expect(screen.getByRole("region", { name: "来自通知列表" })).toBeInTheDocument();

      await act(async () => {
        await router.navigate(-1);
      });
      expect(location(router).pathname).toBe("/app/notifications");
      await act(async () => {
        await router.navigate(1);
      });
      expect(location(router).search).toBe("?tab=escalation");
      expect(screen.getByRole("region", { name: "来自通知列表" })).toBeInTheDocument();

      signIn("token-a", "admin");
      expect(screen.queryByRole("region", { name: "来自通知列表" })).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "返回通知列表" })).not.toBeInTheDocument();
      await waitFor(() => expect(location(router).state).toBeNull());
      expect(location(router).search).toBe("?tab=escalation");
      expect(settingsTab("escalation")).toHaveAttribute("aria-selected", "true");
    });

    const directContexts: Array<[string, () => unknown]> = [
      ["no state", () => undefined],
      ["an earlier session epoch", () => {
        const stale = createNotificationConfigReturn("42");
        bumpAuthSessionGeneration();
        return stale;
      }],
      ["an injected return URL", () => ({ ...createNotificationConfigReturn("42"), returnTo: "https://evil.example/" })],
      ["an unknown source", () => ({ ...createNotificationConfigReturn(null), source: "dashboard" })],
      ["an unsafe alert id", () => ({ ...createNotificationConfigReturn(null), alertId: "9007199254740993" })],
      ["a prefixed alert id", () => ({ ...createNotificationConfigReturn(null), alertId: "alert-42" })],
    ];

    it.each(directContexts)("shows no return entry for %s", async (_label, makeContext) => {
      const notificationConfigReturn = makeContext();
      const state = notificationConfigReturn === undefined ? { keep: "yes" } : { keep: "yes", notificationConfigReturn };
      const { router } = renderRoutes([{ pathname: "/app/settings", search: "?tab=silences", state }]);

      expect(settingsTab("silences")).toHaveAttribute("aria-selected", "true");
      expect(screen.getByText("silences-panel")).toBeInTheDocument();
      await waitFor(() => expect(location(router).state).toEqual({ keep: "yes" }));
      expect(screen.queryByText("保存后仍停留在此页；请手动返回")).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "返回原告警" })).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "返回通知列表" })).not.toBeInTheDocument();
      expect(screen.queryAllByRole("link").filter((link) => link.getAttribute("href")?.startsWith("http"))).toHaveLength(0);
    });

    it.each(["viewer", "operator"] as const)("hides config entries from %s and keeps their settings fallback", async (role) => {
      const stateForAdmin = { notificationConfigReturn: createNotificationConfigReturn("42") };
      act(() => {
        rememberAuthIdentity("token-a", role);
        authStore.set({ role });
      });
      const { router } = renderRoutes(["/app/notifications?alert=42"]);
      expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
      expect(screen.queryAllByRole("navigation", { name: "告警配置" })).toHaveLength(0);
      expect(screen.queryByText("配置后可返回此告警")).not.toBeInTheDocument();
      expect(screen.queryAllByRole("link").filter((link) => link.getAttribute("href")?.startsWith("/app/settings"))).toHaveLength(0);

      for (const tab of ["channels", "silences", "escalation"]) {
        await act(async () => {
          await router.navigate(`/app/settings?tab=${tab}`, { state: stateForAdmin });
        });
        expect(settingsTab("personal")).toHaveAttribute("aria-selected", "true");
        expect(screen.getByText("personal-panel")).toBeInTheDocument();
        expect(screen.queryByText(`${tab}-panel`)).not.toBeInTheDocument();
        expect(screen.queryByText("保存后仍停留在此页；请手动返回")).not.toBeInTheDocument();
        expect(document.getElementById("settings-tab-channels")).toBeNull();
      }

      await act(async () => {
        await router.navigate("/app/settings?tab=account");
      });
      expect(settingsTab("account")).toHaveAttribute("aria-selected", "true");
      expect(screen.getByText("account-panel")).toBeInTheDocument();
    });

    describe("inside the animated app shell", () => {
      // Each probe records every committed location its page saw, proving the exiting page stayed mounted.
      function renderShell(initialEntries: InitialEntry[]) {
        const seen = { notifications: [] as string[], settings: [] as string[] };
        function NotificationsProbe() {
          const { pathname } = useLocation();
          useEffect(() => {
            seen.notifications.push(pathname);
          });
          return <NotificationsPage />;
        }
        function SettingsProbe() {
          const { pathname } = useLocation();
          useEffect(() => {
            seen.settings.push(pathname);
          });
          return <SettingsPage />;
        }
        const router = createMemoryRouter(
          [{
            path: "/app",
            element: <AppShell />,
            children: [
              { path: "notifications", element: <NotificationsProbe /> },
              { path: "settings", element: <SettingsProbe /> },
            ],
          }],
          { initialEntries },
        );
        // StrictMode replays mount cleanup, the lifecycle that once poisoned render-time owner captures.
        render(<StrictMode><RouterProvider router={router} /></StrictMode>);
        return { router, seen };
      }

      it("keeps an alert context while the notifications page exits and while settings exits on return", async () => {
        const user = userEvent.setup();
        const { router, seen } = renderShell(["/app/notifications?alert=42"]);
        const region = await targetDeliveryRegion();
        await waitFor(() => expect(api.getAlert).toHaveBeenCalled());
        // StrictMode may replay the mount read; only the count after this point matters.
        const initialReads = api.getAlert.mock.calls.length;

        await user.click(within(within(region).getByRole("navigation", { name: "告警配置" })).getByRole("link", { name: "通知渠道" }));
        const source = await screen.findByRole("region", { name: "来自告警 #42" }, { timeout: 2000 });
        await wait(400);
        expect(seen.notifications).toContain("/app/settings");
        expect(location(router).pathname).toBe("/app/settings");
        expect(location(router).search).toBe("?tab=channels");
        expect(router.state.historyAction).toBe("PUSH");
        expect(returnPayload(router)).toMatchObject({ alertId: "42" });
        expect(source).toBeInTheDocument();
        // The exiting notifications page neither re-read the target nor cleaned the settings entry.
        expect(api.getAlert).toHaveBeenCalledTimes(initialReads);

        await user.click(within(source).getByRole("link", { name: "返回原告警" }));
        expect(await screen.findByRole("heading", { name: TARGET_HEADING }, { timeout: 2000 })).toBeInTheDocument();
        await wait(400);
        expect(seen.settings).toContain("/app/notifications");
        expect(location(router).pathname).toBe("/app/notifications");
        expect(location(router).search).toBe("?alert=42");
        expect(router.state.historyAction).toBe("PUSH");
        expect(returnPayload(router)).toMatchObject({ alertId: "42" });
        expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
        expect(api.getAlert.mock.calls.length).toBeGreaterThan(initialReads);
        for (const call of api.getAlert.mock.calls) expect(call.slice(0, 2)).toEqual(["token-a", "42"]);
      });

      it("keeps a list context across both animated transitions without any target read", async () => {
        const user = userEvent.setup();
        const { router, seen } = renderShell(["/app/notifications"]);
        expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);

        await user.click(within(screen.getByRole("navigation", { name: "告警配置" })).getByRole("link", { name: "静音规则" }));
        const source = await screen.findByRole("region", { name: "来自通知列表" }, { timeout: 2000 });
        await wait(400);
        expect(seen.notifications).toContain("/app/settings");
        expect(returnPayload(router)).toMatchObject({ alertId: null });
        expect(router.state.historyAction).toBe("PUSH");
        expect(screen.getByRole("region", { name: "来自通知列表" })).toBe(source);

        await user.click(within(source).getByRole("link", { name: "返回通知列表" }));
        expect((await screen.findAllByText("普通列表告警", {}, { timeout: 2000 })).length).toBeGreaterThan(0);
        await wait(400);
        expect(seen.settings).toContain("/app/notifications");
        expect(location(router).pathname).toBe("/app/notifications");
        expect(location(router).search).toBe("");
        expect(returnPayload(router)).toMatchObject({ alertId: null });
        expect(router.state.historyAction).toBe("PUSH");
        expect(api.getAlert).not.toHaveBeenCalled();
      });

      it("does not read or clean anything from an exiting target page that leaves for another route", async () => {
        const { router, seen } = renderShell(["/app/notifications?alert=42"]);
        expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
        const initialReads = api.getAlert.mock.calls.length;
        const state = { keep: "yes" };

        await act(async () => {
          await router.navigate("/app/settings?tab=personal", { state });
        });
        expect(await screen.findByText("personal-panel", {}, { timeout: 2000 })).toBeInTheDocument();
        await wait(400);
        expect(seen.notifications).toContain("/app/settings");
        expect(location(router).search).toBe("?tab=personal");
        expect(location(router).state).toEqual(state);
        expect(router.state.historyAction).toBe("PUSH");
        expect(api.getAlert).toHaveBeenCalledTimes(initialReads);
      });
    });

    describe("under StrictMode", () => {
      it("accepts the first click on a rendered list link and on its return link", async () => {
        const user = userEvent.setup();
        const { router } = renderRoutes(["/app/notifications"], undefined, { strict: true });
        expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);

        await user.click(within(screen.getByRole("navigation", { name: "告警配置" })).getByRole("link", { name: "通知渠道" }));
        expect(location(router).pathname).toBe("/app/settings");
        expect(returnPayload(router)).toMatchObject({ alertId: null });
        const source = screen.getByRole("region", { name: "来自通知列表" });

        await user.click(within(source).getByRole("link", { name: "返回通知列表" }));
        expect(location(router).pathname).toBe("/app/notifications");
        expect(location(router).search).toBe("");
        expect(returnPayload(router)).toMatchObject({ alertId: null });
        expect((await screen.findAllByText("普通列表告警")).length).toBeGreaterThan(0);
        expect(api.getAlert).not.toHaveBeenCalled();
      });

      it("accepts the first click on a rendered alert link and returns to the same alert", async () => {
        const user = userEvent.setup();
        const { router } = renderRoutes(["/app/notifications?alert=alert-42"], undefined, { strict: true });
        const region = await targetDeliveryRegion();
        const initialReads = api.getAlert.mock.calls.length;

        await user.click(within(within(region).getByRole("navigation", { name: "告警配置" })).getByRole("link", { name: "升级策略" }));
        expect(location(router).pathname).toBe("/app/settings");
        expect(location(router).search).toBe("?tab=escalation");
        expect(returnPayload(router)).toMatchObject({ alertId: "42" });
        const source = screen.getByRole("region", { name: "来自告警 #42" });
        await user.click(screen.getByRole("button", { name: "保存 escalation" }));
        expect(location(router).pathname).toBe("/app/settings");

        await user.click(within(source).getByRole("link", { name: "返回原告警" }));
        expect(location(router).pathname).toBe("/app/notifications");
        expect(location(router).search).toBe("?alert=42");
        expect(returnPayload(router)).toMatchObject({ alertId: "42" });
        expect((await screen.findAllByText("目标告警正文")).length).toBeGreaterThan(0);
        expect(api.getAlert.mock.calls.length).toBeGreaterThan(initialReads);
        for (const call of api.getAlert.mock.calls) expect(call.slice(0, 2)).toEqual(["token-a", "42"]);
      });
    });
  });
});
